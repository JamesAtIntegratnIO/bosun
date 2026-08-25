package edits

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# MetalLB, L2 only.
metallb:
  enabled: true
  defaultVersion: 0.16.0
  valuesObject:
    speaker:
      frr:
        enabled: true      # keep FRR off; this cluster is L2-only
    frrk8s:
      enabled: true
  containers:
    - image: "quay.io/metallb/controller:v0.15.2"
`

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func openPolicy() Policy { return Policy{Allow: []string{"addons/**"}} }

// The real case, in the exact shape the live model produced.
func TestAppliesScalarEditsInPlace(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})

	res, err := Apply(root, openPolicy(), []Edit{
		{Path: "addons/values.yaml", Key: "metallb.valuesObject.speaker.frr.enabled", From: "true", To: "false"},
		{Path: "addons/values.yaml", Key: "metallb.valuesObject.frrk8s.enabled", From: "true", To: "false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 2 || len(res.Rejected) != 0 {
		t.Fatalf("want 2 applied 0 rejected, got %d/%d: %+v", len(res.Applied), len(res.Rejected), res.Rejected)
	}

	got, _ := os.ReadFile(filepath.Join(root, "addons/values.yaml"))
	s := string(got)

	// The trailing comment must survive -- Kargo's own yaml-update deletes
	// them, and losing one silently removes the note explaining the value.
	if !strings.Contains(s, "enabled: false      # keep FRR off; this cluster is L2-only") {
		t.Errorf("indentation or trailing comment not preserved:\n%s", s)
	}
	// Only the two target lines may change.
	if !strings.Contains(s, "# MetalLB, L2 only.") || !strings.Contains(s, "defaultVersion: 0.16.0") {
		t.Errorf("unrelated content changed:\n%s", s)
	}
	if strings.Count(s, "enabled: true") != 1 { // metallb.enabled stays
		t.Errorf("wrong number of `enabled: true` left:\n%s", s)
	}
}

// The optimistic-concurrency check: a model working from a stale or imagined
// view of the file must change nothing rather than the wrong line.
func TestRejectsWhenFromDoesNotMatch(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	before, _ := os.ReadFile(filepath.Join(root, "addons/values.yaml"))

	res, err := Apply(root, openPolicy(), []Edit{
		{Path: "addons/values.yaml", Key: "metallb.defaultVersion", From: "0.15.2", To: "0.17.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 || len(res.Rejected) != 1 {
		t.Fatalf("a mismatched `from` must be refused, got %+v", res)
	}
	if !strings.Contains(res.Rejected[0].Reason, "refusing to overwrite") {
		t.Errorf("reason should say why: %q", res.Rejected[0].Reason)
	}
	after, _ := os.ReadFile(filepath.Join(root, "addons/values.yaml"))
	if string(before) != string(after) {
		t.Error("file changed despite the edit being rejected")
	}
}

// The load-bearing test. Every one of these is a way to make a red gate green
// without fixing anything, and all are refused regardless of the allowlist.
func TestAlwaysDeniesTheGateAndThePolicy(t *testing.T) {
	forbidden := []string{
		".github/workflows/validate-addons.yaml",
		".gitops-gate.yaml",
		".gitops-gate/clusters.yaml",
		"delivery/images/bosun/prompt.go",
		"delivery/charts/kargo-pipelines/values.yaml",
		"addons/cluster-roles/control-plane/addons/kargo-projects/values.yaml",
		".gitlab-ci.yml",
		"bitbucket-pipelines.yml",
		// Deliberately outside delivery/, so the two kargo entries are tested
		// on their own rather than riding on the `delivery/**` denial.
		"charts/kargo-pipelines/values.yaml",
		"charts/kargo-projects/values.yaml",
		"kargo-pipelines/values.yaml",
		"platform/tenant/kargo-pipelines/promote/stage.yaml",
	}
	files := map[string]string{}
	for _, f := range forbidden {
		files[f] = "key: value\n"
	}
	root := repo(t, files)

	// Deliberately the most permissive allowlist possible.
	policy := Policy{Allow: []string{"**/*", "*"}}
	for _, f := range forbidden {
		res, err := Apply(root, policy, []Edit{{Path: f, Key: "key", From: "value", To: "pwned"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Applied) != 0 {
			t.Errorf("%s was written even though it is always denied", f)
		}
		if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, "denied") {
			t.Errorf("%s: want a denial, got %+v", f, res.Rejected)
		}
	}
}

func TestRejectsPathOutsideTheAllowlist(t *testing.T) {
	root := repo(t, map[string]string{"other/thing.yaml": "key: value\n"})
	res, _ := Apply(root, Policy{Allow: []string{"addons/**"}},
		[]Edit{{Path: "other/thing.yaml", Key: "key", From: "value", To: "x"}})
	if len(res.Applied) != 0 || len(res.Rejected) != 1 {
		t.Fatalf("want rejection, got %+v", res)
	}
	if !strings.Contains(res.Rejected[0].Reason, "not in the allowlist") {
		t.Errorf("unexpected reason: %q", res.Rejected[0].Reason)
	}
}

func TestRejectsTraversalOutOfTheRepository(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	// The reason matters, not just the refusal. This used to pass through the
	// allowlist while the containment guard below it was unreachable, so the
	// test proved a policy decision and not the defence it is named for.
	for _, path := range []string{
		"../../../etc/passwd",
		"addons/../../escape.yaml",
		"./../outside.yaml",
	} {
		res, _ := Apply(root, Policy{Allow: []string{"**/*", "*", "../**"}},
			[]Edit{{Path: path, Key: "key", From: "", To: "x"}})
		if len(res.Applied) != 0 {
			t.Fatalf("%s escaped the repository", path)
		}
		if len(res.Rejected) != 1 || res.Rejected[0].Reason != "path escapes the repository" {
			t.Errorf("%s: want the containment refusal, got %+v", path, res.Rejected)
		}
	}
}

// The guard must not reject ordinary paths that merely contain a ".." segment
// resolving back inside the repository.
func TestAllowsATraversalThatStaysInside(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	res, err := Apply(root, Policy{Allow: []string{"addons/**"}},
		[]Edit{{Path: "addons/nested/../values.yaml", Key: "metallb.defaultVersion", From: "0.16.0", To: "0.16.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("a path that stays inside must be applied, got %+v", res)
	}
}

func TestRejectsAKeyThatDoesNotExist(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	res, _ := Apply(root, openPolicy(), []Edit{
		{Path: "addons/values.yaml", Key: "metallb.valuesObject.nope.enabled", From: "true", To: "false"},
	})
	if len(res.Applied) != 0 || !strings.Contains(res.Rejected[0].Reason, "not found") {
		t.Fatalf("edits change values, they never add them: %+v", res)
	}
}

// The shape the model produced BEFORE the prompt spelled out the contract: a
// file path in `key`, a multi-line blob in `from`. It must fail closed.
func TestRejectsAMalformedEditShape(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	res, _ := Apply(root, openPolicy(), []Edit{{
		Path: "addons/values.yaml",
		Key:  "addons/values.yaml",
		From: "speaker:\n  frr:\n    enabled: true",
		To:   "speaker:\n  frr:\n    enabled: false",
	}})
	if len(res.Applied) != 0 {
		t.Fatal("a malformed edit was applied")
	}
}

func TestRejectsNonScalarTarget(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	res, _ := Apply(root, openPolicy(), []Edit{
		{Path: "addons/values.yaml", Key: "metallb.valuesObject", From: "", To: "x"},
	})
	if len(res.Applied) != 0 || !strings.Contains(res.Rejected[0].Reason, "not a scalar") {
		t.Fatalf("want a not-a-scalar rejection, got %+v", res)
	}
}

func TestAppliesInsideAList(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	res, err := Apply(root, openPolicy(), []Edit{{
		Path: "addons/values.yaml", Key: "metallb.containers.0.image",
		From: "quay.io/metallb/controller:v0.15.2",
		To:   "quay.io/metallb/controller:v0.16.0",
	}})
	if err != nil || len(res.Applied) != 1 {
		t.Fatalf("want 1 applied, got %+v %v", res, err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "addons/values.yaml"))
	// The surrounding quotes are part of the line, not the value, so they stay.
	if !strings.Contains(string(got), `- image: "quay.io/metallb/controller:v0.16.0"`) {
		t.Errorf("quoting style not preserved:\n%s", got)
	}
}

// A model told "requires Gateway API v1.5" will confidently write v1.5.0 when
// the answer was v1.5.1 -- observed on a live model, with the prompt
// explicitly forbidding it. So the guarantee lives in code.
func TestRefusesAnInventedVersion(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": "gateway-api-crds:\n  defaultVersion: v1.4.0\n"})
	policy := Policy{
		Allow:    []string{"addons/**"},
		Evidence: "nginx-gateway-fabric 2.6.7 requires Gateway API v1.5 or newer.",
	}

	res, err := Apply(root, policy, []Edit{
		{Path: "addons/values.yaml", Key: "gateway-api-crds.defaultVersion", From: "v1.4.0", To: "v1.5.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatal("an uncorroborated version was written")
	}
	if !strings.Contains(res.Rejected[0].Reason, "invented version") {
		t.Errorf("reason should name the cause: %q", res.Rejected[0].Reason)
	}
}

func TestAllowsAVersionThatAppearsInTheEvidence(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": "gateway-api-crds:\n  defaultVersion: v1.4.0\n"})
	policy := Policy{
		Allow:    []string{"addons/**"},
		Evidence: "The exact version to move to is v1.5.1.",
	}
	res, _ := Apply(root, policy, []Edit{
		{Path: "addons/values.yaml", Key: "gateway-api-crds.defaultVersion", From: "v1.4.0", To: "v1.5.1"},
	})
	if len(res.Applied) != 1 {
		t.Fatalf("a corroborated version must be allowed: %+v", res.Rejected)
	}
}

// Booleans and ports must not be caught by the corroboration check -- "false"
// rarely appears in a failure report, and rejecting toggles would break the
// most common mechanical fix there is.
func TestCorroborationIgnoresNonVersionValues(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	policy := Policy{Allow: []string{"addons/**"}, Evidence: "chart 0.16.0 flips the default"}

	res, _ := Apply(root, policy, []Edit{
		{Path: "addons/values.yaml", Key: "metallb.valuesObject.frrk8s.enabled", From: "true", To: "false"},
	})
	if len(res.Applied) != 1 {
		t.Fatalf("a boolean toggle must not need corroboration: %+v", res.Rejected)
	}
}

// Scope is what makes "the files this pull request may change" a guarantee
// rather than a line in a prompt.
//
// The concrete case: a MetalLB bump rewrites metallb.defaultVersion in
// addons.yaml and nothing else, while the repository also holds a
// NetworkPolicy naming the old metrics port. Before Scope, a model that
// proposed editing that NetworkPolicy got it applied -- the standing
// allowlist is addons/**, and the NetworkPolicy is under addons/.
func TestScopeRefusesAFileThePromotionDidNotTouch(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const bumped = "addons/environments/production/addons/addons.yaml"
	const untouched = "addons/cluster-roles/control-plane/addons/network-policies/metallb-system.yaml"
	write(bumped, "metallb:\n  defaultVersion: 0.16.0\n")
	write(untouched, "spec:\n  ingress:\n    - ports:\n        - port: 7472\n")

	policy := Policy{
		Allow: []string{"addons/**"}, // would permit both
		Scope: []string{bumped},      // the promotion touched only this
	}

	res, err := Apply(root, policy, []Edit{
		{Path: untouched, Key: "spec.ingress.0.ports.0.port", From: "7472", To: "9120"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("an edit outside the promotion's files must be refused, applied %+v", res.Applied)
	}
	if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, "outside this change") {
		t.Fatalf("want a scope refusal naming why, got %+v", res.Rejected)
	}

	// And the same policy still applies an edit to the file it DID touch,
	// or scoping would just be a way to refuse everything.
	res, err = Apply(root, policy, []Edit{
		{Path: bumped, Key: "metallb.defaultVersion", From: "0.16.0", To: "0.16.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("an in-scope edit must still apply, got %+v rejected=%+v", res.Applied, res.Rejected)
	}
}

// An empty Scope means unscoped, so callers with no notion of "the files this
// change touched" keep working.
func TestEmptyScopeIsUnscoped(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, "addons/a.yaml")
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("k: 1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(root, Policy{Allow: []string{"addons/**"}},
		[]Edit{{Path: "addons/a.yaml", Key: "k", From: "1.0.0", To: "1.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("empty Scope must not refuse anything, got %+v", res.Rejected)
	}
}

// matchGlob's `**` forms are what the deny-list is written in, so a form the
// matcher quietly fails to support is a deny-list entry that does not hold.
func TestMatchGlobDoubleStarForms(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/kargo-pipelines/**", "charts/kargo-pipelines/values.yaml", true},
		{"**/kargo-pipelines/**", "a/b/c/kargo-pipelines/d/e.yaml", true},
		{"**/kargo-pipelines/**", "kargo-pipelines/values.yaml", true},
		{"**/kargo-pipelines/**", "kargo-pipelines", true},
		{"**/kargo-pipelines/**", "charts/kargo-pipelines", true},
		{"**/kargo-pipelines/**", "charts/kargo-pipelines-staging/values.yaml", false},
		{"**/kargo-pipelines/**", "charts/other/values.yaml", false},
		{"delivery/**", "delivery/images/bosun/prompt.go", true},
		{"delivery/**", "delivery", true},
		{"delivery/**", "deliverance/x.yaml", false},
		{"**/values.yaml", "charts/app/values.yaml", true},
		{"**/values.yaml", "values.yaml", true},
		{"**/values.yaml", "charts/app/other.yaml", false},
	}
	for _, c := range cases {
		if got := matchGlob(c.pattern, c.path); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
