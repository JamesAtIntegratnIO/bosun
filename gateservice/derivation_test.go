package gateservice

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// ADR 0012 rests on a claim that was measured once against a live fleet:
// deriving the scope from ArgoCD and reading it out of a config file produce
// the same render, row for row. A measurement taken once is a fact about that
// afternoon, so the probe is a test here.

// joinLines flattens Markdown lines for a substring assertion.
func joinLines(ms []gate.Markdown) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m)
	}
	return strings.Join(out, "\n")
}

// appSet is one ApplicationSet targeting clusters by label.
func appSet(name, label, path string) string {
	return `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: ` + name + `
  namespace: argocd
spec:
  goTemplate: true
  generators:
    - clusters:
        selector:
          matchLabels:
            ` + label + `
  template:
    metadata:
      name: '` + name + `-{{ .name }}'
    spec:
      project: default
      source:
        repoURL: https://github.com/example-org/homelab
        path: ` + path + `
        targetRevision: main
      destination:
        server: '{{ .server }}'
        namespace: ` + name + `
`
}

// fleetRepo is a checkout in the shape the assessment measured: Applications
// and ApplicationSets committed as YAML under one directory.
func fleetRepo(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0"),
		"apps/appset.yaml":  appSet("media", "argocd.argoproj.io/secret-type: cluster", "apps/media"),
	}
	for k, v := range extra {
		files[k] = v
	}
	dir := t.TempDir()
	writeGateRepo(t, dir, files)
	return dir
}

func renderPlan(t *testing.T, head string, cfg *gate.Config, name string, d *gate.Derivation) *gate.Table {
	t.Helper()
	p, err := buildPlan(head, cfg, name, d)
	if err != nil {
		t.Fatalf("building the plan: %v", err)
	}
	table, err := gate.Render(context.Background(), head, p.cfg, testInventory())
	if err != nil {
		t.Fatalf("rendering: %v", err)
	}
	return table
}

// rowKeys is a render reduced to what the diff actually compares: Diff indexes
// rows by cluster and Application name, and reports on what each one deploys.
//
// The source label is deliberately left out. A row produced by a file source
// named `apps` and the same row produced by a derived source named `app/apps`
// are the same Application on the same cluster deploying the same thing; the
// name says which strategy found it, and it is not part of the row's identity
// anywhere in Diff.
func rowKeys(table *gate.Table) []string {
	out := make([]string, 0, len(table.Rows))
	for _, r := range table.Rows {
		out = append(out, r.Key()+" "+r.Describe())
	}
	return out
}

// The probe, made permanent. A repository rendered from its committed config
// and the same repository rendered from a derivation of what ArgoCD serves
// must produce the same rows: the file is a second copy of the pointers, and
// two copies that disagree is the failure the ADR removes.
func TestADerivedScopeRendersWhatTheConfigFileRenders(t *testing.T) {
	head := fleetRepo(t, nil)

	fromFile, err := gate.ParseConfig([]byte(gateConfig), ".gitops-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	viaFile := renderPlan(t, head, fromFile, ".gitops-gate.yaml", nil)

	// What ArgoCD would serve for this repository: one Application pointing
	// at the directory the file globs, and no roots.
	derived := &gate.Derivation{
		Applications: 3, ApplicationSets: 1,
		Sources: []gate.Source{{
			Name: "app/apps", Type: gate.SourceDirectory, Path: "apps", Recurse: true,
		}},
	}
	viaLive := renderPlan(t, head, nil, "", derived)

	if len(viaLive.Rows) == 0 {
		t.Fatal("the derived render produced nothing, so the comparison below proves nothing")
	}
	if !reflect.DeepEqual(rowKeys(viaFile), rowKeys(viaLive)) {
		t.Fatalf("the two scopes disagree about what this repository deploys:\n  file: %v\n  live: %v",
			rowKeys(viaFile), rowKeys(viaLive))
	}
}

// The split-repository shape: roots in an infrastructure repository, content
// in this one, and no config file at all. This is the case that turned the
// design from file-first to derive-first, and `exclude` is load-bearing in it.
func TestTheSplitRepositoryShapeNeedsNoFile(t *testing.T) {
	head := t.TempDir()
	writeGateRepo(t, head, map[string]string{
		"tenants/a/app.yaml": appManifest("tenant-a", "https://kubernetes.default.svc", "6.7.0"),
		// The trap. A live source carrying `exclude: exclude/*` had a
		// bootstrap manifest under that path; rendering it would put an
		// ApplicationSet in the verdict that the cluster does not have.
		"tenants/a/exclude/bootstrap.yaml": appSet("trap", "argocd.argoproj.io/secret-type: cluster", "nowhere"),
	})

	derived := &gate.Derivation{
		Applications: 1,
		Sources: []gate.Source{{
			Name: "app/tenant-a", Type: gate.SourceDirectory, Path: "tenants/a",
			Recurse: true, Exclude: "exclude/*",
		}},
	}
	table := renderPlan(t, head, nil, "", derived)

	for _, r := range table.Rows {
		if strings.Contains(r.AppSet, "trap") {
			t.Fatalf("the excluded ApplicationSet reached the verdict: %v", rowKeys(table))
		}
	}
	if len(table.Rows) != 1 {
		t.Fatalf("want the one Application the tenant deploys, got %v", rowKeys(table))
	}
}

// The blind spot ADR 0012 names, and the thing head-over-live buys. A pull
// request that re-targets a root must be judged on its own content: reading
// the applied spec instead would compare the previous answer with itself and
// find no change on the one edit that matters most.
func TestARootEditIsGatedFromHeadNotFromTheAppliedSpec(t *testing.T) {
	const rootPath = "bootstrap/apps.yaml"
	// Live still has the root targeting every cluster.
	liveObject := parseYAML(t, appSet("apps", "argocd.argoproj.io/secret-type: cluster", "apps/media"))
	derived := func() *gate.Derivation {
		return &gate.Derivation{
			Applications: 0, ApplicationSets: 1,
			Roots: []gate.LiveRoot{{
				Kind: "ApplicationSet", Name: "apps", Namespace: "argocd", Object: liveObject,
			}},
		}
	}

	// Head narrows it to one cluster, which is a targeting change.
	base := t.TempDir()
	writeGateRepo(t, base, map[string]string{
		rootPath: appSet("apps", "argocd.argoproj.io/secret-type: cluster", "apps/media")})
	// The selector matches on a key every cluster carries, with a value none
	// of them has. Selecting on an unknown *key* is refused before any of this
	// is reached, and rightly: a label the inventory has never seen shrinks
	// the render silently.
	head := t.TempDir()
	writeGateRepo(t, head, map[string]string{
		rootPath: appSet("apps", "argocd.argoproj.io/secret-type: none", "apps/media")})

	baseTable := renderPlan(t, base, nil, "", derived())
	headTable := renderPlan(t, head, nil, "", derived())

	if len(baseTable.Rows) == len(headTable.Rows) {
		t.Fatalf("the head copy was not used: base %v, head %v", rowKeys(baseTable), rowKeys(headTable))
	}
	if len(headTable.Rows) != 0 {
		t.Fatalf("head selects on a label no cluster carries, so it should target nothing: %v", rowKeys(headTable))
	}
	if len(baseTable.Rows) != 2 {
		t.Fatalf("base targets both clusters: %v", rowKeys(baseTable))
	}
}

// A root whose manifest this repository does not hold is rendered from the
// applied spec, because there is nothing else, and the report says so. The
// alternative is not rendering it at all, which removes a whole layer from
// both sides of the diff and reports no change in it.
func TestARootWithNoManifestHereIsRenderedFromLiveAndSaidSo(t *testing.T) {
	head := t.TempDir()
	writeGateRepo(t, head, map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")})

	derived := &gate.Derivation{
		Applications: 1, ApplicationSets: 1,
		Sources: []gate.Source{{Name: "app/apps", Type: gate.SourceDirectory, Path: "apps", Recurse: true}},
		Roots: []gate.LiveRoot{{
			Kind: "ApplicationSet", Name: "elsewhere", Namespace: "argocd",
			Object: parseYAML(t, appSet("elsewhere", "argocd.argoproj.io/secret-type: cluster", "apps/media")),
		}},
	}

	p, err := buildPlan(head, nil, "", derived)
	if err != nil {
		t.Fatal(err)
	}
	var live int
	for _, s := range p.cfg.Sources {
		if s.Type == gate.SourceLive {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("the root has no manifest here, so it must come from the applied spec: %+v", p.cfg.Sources)
	}
	if !strings.Contains(joinLines(p.scope), "elsewhere") {
		t.Fatalf("a row resting on the applied spec has to be named in the report: %v", p.scope)
	}
}

// The same root, with its manifest in the checkout. Head wins, and nothing is
// rendered twice.
func TestARootFoundInTheCheckoutIsPreferredOverLive(t *testing.T) {
	head := t.TempDir()
	writeGateRepo(t, head, map[string]string{
		"bootstrap/apps.yaml": appSet("apps", "argocd.argoproj.io/secret-type: cluster", "apps/media")})

	derived := &gate.Derivation{
		ApplicationSets: 1,
		Roots: []gate.LiveRoot{{
			Kind: "ApplicationSet", Name: "apps", Namespace: "argocd",
			Object: parseYAML(t, appSet("apps", "argocd.argoproj.io/secret-type: none", "apps/media")),
		}},
	}
	p, err := buildPlan(head, nil, "", derived)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.cfg.Sources) != 1 {
		t.Fatalf("one root, one source: %+v", p.cfg.Sources)
	}
	if p.cfg.Sources[0].Type == gate.SourceLive {
		t.Fatal("the checkout holds this root, so the applied spec must not be used")
	}
	if got := p.cfg.Sources[0].Paths; len(got) != 1 || got[0] != "bootstrap/apps.yaml" {
		t.Fatalf("want the manifest found in the checkout, got %v", got)
	}
}

// The file's only remaining job: naming a root that lives here. It is what
// closes the first-run blind spot, because a root this pull request introduces
// has no live object to be found by, so derivation never reaches it.
func TestARootNamedInTheFileIsRenderedEvenWithNothingLive(t *testing.T) {
	head := t.TempDir()
	writeGateRepo(t, head, map[string]string{
		"bootstrap/new.yaml": appSet("brand-new", "argocd.argoproj.io/secret-type: cluster", "apps/media")})

	cfg, err := gate.ParseConfig([]byte("roots:\n  - bootstrap/new.yaml\n"), ".bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Nothing live: the root does not exist yet, which is the whole point.
	p, err := buildPlan(head, cfg, ".bosun.yaml", &gate.Derivation{})
	if err != nil {
		t.Fatal(err)
	}
	table, err := gate.Render(context.Background(), head, p.cfg, testInventory())
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Rows) != 2 {
		t.Fatalf("a root introduced by this pull request must still be rendered: %v", rowKeys(table))
	}
}

// And when it does exist live, naming it renders it once, from head.
func TestANamedRootSuppressesTheLiveFallbackForItsIdentity(t *testing.T) {
	head := t.TempDir()
	writeGateRepo(t, head, map[string]string{
		"bootstrap/apps.yaml": appSet("apps", "argocd.argoproj.io/secret-type: cluster", "apps/media")})

	cfg, err := gate.ParseConfig([]byte("roots:\n  - bootstrap/apps.yaml\n"), ".bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := buildPlan(head, cfg, ".bosun.yaml", &gate.Derivation{
		ApplicationSets: 1,
		Roots: []gate.LiveRoot{{
			Kind: "ApplicationSet", Name: "apps", Namespace: "argocd",
			Object: parseYAML(t, appSet("apps", "argocd.argoproj.io/secret-type: none", "apps/media")),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.cfg.Sources) != 1 {
		t.Fatalf("naming a root that also exists live must render it once: %+v", p.cfg.Sources)
	}
}

// A typo in `roots:` must not fall back to the applied spec. That is the one
// entry the file exists for, and a silent fallback produces a green gate on
// exactly the change it was added to see.
func TestARootPathThatDoesNotResolveIsAnError(t *testing.T) {
	head := t.TempDir()
	writeGateRepo(t, head, map[string]string{"apps/x.yaml": "kind: ConfigMap\nmetadata:\n  name: x\n"})

	cfg, err := gate.ParseConfig([]byte("roots:\n  - bootstrap/typo.yaml\n"), ".bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildPlan(head, cfg, ".bosun.yaml", &gate.Derivation{}); err == nil {
		t.Fatal("a roots entry naming nothing must be an error, not a silent fallback")
	}
}

// Two config files is an error rather than a precedence rule. A silent
// precedence is how a repository ends up maintaining the file the gate is not
// reading.
func TestBothConfigFilenamesPresentIsAnError(t *testing.T) {
	dir := t.TempDir()
	writeGateRepo(t, dir, map[string]string{
		".bosun.yaml":       gateConfig,
		".gitops-gate.yaml": gateConfig,
	})
	_, _, err := readConfig(dir)
	if err == nil {
		t.Fatal("two files configuring one gate must be refused")
	}
	for _, want := range []string{".bosun.yaml", ".gitops-gate.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name both files: %v", err)
		}
	}
}

func TestEitherConfigFilenameIsRead(t *testing.T) {
	for _, name := range []string{".bosun.yaml", ".gitops-gate.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeGateRepo(t, dir, map[string]string{name: gateConfig})
			cfg, got, err := readConfig(dir)
			if err != nil || cfg == nil {
				t.Fatalf("cfg=%v err=%v", cfg, err)
			}
			if got != name {
				t.Fatalf("read %q, want %q", got, name)
			}
		})
	}
}

func TestNoConfigFileIsNotAnError(t *testing.T) {
	cfg, name, err := readConfig(t.TempDir())
	if err != nil || cfg != nil || name != "" {
		t.Fatalf("the common install has no file: cfg=%v name=%q err=%v", cfg, name, err)
	}
}

// An ArgoCD that serves a fleet with nothing pointing at this repository is
// not a green gate. Two empty sets have no difference between them, and
// reporting that would pass every pull request.
func TestADerivationThatFindsNothingRefuses(t *testing.T) {
	_, err := buildPlan(t.TempDir(), nil, "", &gate.Derivation{Applications: 65, ApplicationSets: 60})
	if err == nil {
		t.Fatal("an empty scope must refuse rather than report no change")
	}
	for _, want := range []string{"nothing to render", "65 Applications", "60 ApplicationSets"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal should say what was looked at: %v", err)
		}
	}
}

// A refused or unreachable ArgoCD is an error, not a smaller scope, for the
// same reason an unreadable inventory is.
func TestAFailedDerivationBreaksTheRunRatherThanShrinkingIt(t *testing.T) {
	h := newGateHarness(t,
		map[string]string{"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")},
		map[string]string{"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.1")})
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) {
		return nil, os.ErrPermission
	}

	out := h.gs.Ensure(context.Background(), gatePR("derr"))
	if out.Err == nil {
		t.Fatal("a derivation that could not be read must not produce a verdict")
	}
	if s := lastStatus(t, h.git); s.State != gitprovider.StateError {
		t.Fatalf("'the gate is broken' and 'this change is bad' want opposite reactions: %s %q", s.State, s.Description)
	}
}

// The host has the last word on how hard the gate works and what it checks.
// The gated repository is the thing under judgement, so what it says about
// either is a request.
func TestTheHostPolicyOverridesTheRepositorysOwn(t *testing.T) {
	cfg, err := gate.ParseConfig([]byte(
		"concurrency: 4\nvalidate:\n  enabled: false\n  skipKinds: [Everything]\n"), ".bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	on := true
	g := &Service{
		Concurrency: 12,
		Validate:    ValidatePolicy{Enabled: &on, SkipKinds: []string{"CustomResourceDefinition"}},
	}
	g.applyHostPolicy(cfg)

	if cfg.Concurrency != 12 {
		t.Errorf("the host's concurrency must win, got %d", cfg.Concurrency)
	}
	if !cfg.Validate.Enabled {
		t.Error("a repository must not be able to switch off the validation the operator turned on")
	}
	if !reflect.DeepEqual(cfg.Validate.SkipKinds, []string{"CustomResourceDefinition"}) {
		t.Errorf("the host's skipKinds must win, got %v", cfg.Validate.SkipKinds)
	}
	if !cfg.Validate.IgnoreMissingSchemas {
		// Unset in the policy, so whatever the file said stands. The file
		// said nothing, and ParseConfig leaves it false.
		t.Log("ignoreMissingSchemas is left as the file had it, which is the point")
	}
}

// Unset means "leave the repository's own file alone", and that has to be
// distinguishable from false: an install that switched validation on in its
// own file must not lose it to a chart default.
func TestAnUnsetHostPolicyLeavesTheFileAlone(t *testing.T) {
	cfg, err := gate.ParseConfig([]byte("concurrency: 4\nvalidate:\n  enabled: true\n"), ".bosun.yaml")
	if err != nil {
		t.Fatal(err)
	}
	(&Service{}).applyHostPolicy(cfg)
	if cfg.Concurrency != 4 || !cfg.Validate.Enabled {
		t.Fatalf("an unset policy changed the config: %+v", cfg)
	}
}

// A source written in the file wins over a derived one rendering the same
// path, and nothing is rendered twice.
func TestFileSourcesTakePrecedenceOverDerivedOnes(t *testing.T) {
	head := fleetRepo(t, nil)
	cfg, err := gate.ParseConfig([]byte(gateConfig), ".gitops-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := buildPlan(head, cfg, ".gitops-gate.yaml", &gate.Derivation{
		Applications: 1,
		Sources: []gate.Source{
			{Name: "app/apps", Type: gate.SourceManifests, Paths: []string{"apps/*.yaml"}},
			{Name: "app/other", Type: gate.SourceDirectory, Path: "other", Recurse: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.cfg.Sources) != 2 {
		t.Fatalf("the duplicate should be dropped and the new one kept: %+v", p.cfg.Sources)
	}
	if p.cfg.Sources[0].Name != "apps" {
		t.Errorf("the file's own source must come first: %+v", p.cfg.Sources[0])
	}
	if !strings.Contains(joinLines(p.scope), "take precedence") {
		t.Errorf("the report should say a file is in force: %v", p.scope)
	}
}

// parseYAML stands in for what ArgoCD serves: the object as a map, which is
// the shape a live root arrives in.
func parseYAML(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("parsing the fixture: %v", err)
	}
	return m
}

// The pull-request comment met this the hard way: scope lines are composed
// here with deliberate backticks around the config file's name, and the
// report used to escape them along with everything else, publishing
// `\x60.gitops-gate.yaml\x60` where a code span should have been. The lines
// are gate.Markdown now — rendered as written — so the values inside them are
// neutralised here, at composition, or nowhere.
func TestScopeLinesSurviveTheReportAsWritten(t *testing.T) {
	derived := &gate.Derivation{Applications: 1, ApplicationSets: 1}
	cfg, err := gate.ParseConfig([]byte("sources:\n  - type: manifests\n    paths: [apps]\n"), ".gitops-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}

	res := &gate.DiffResult{Scope: scopeLines(derived, cfg, ".gitops-gate.yaml", nil)}
	var b strings.Builder
	res.Report(&b)

	if !strings.Contains(b.String(), "- `.gitops-gate.yaml` is present") {
		t.Errorf("the config file's name must render as a code span:\n%s", b.String())
	}
	if strings.Contains(b.String(), `\x60`) {
		t.Errorf("nothing in this scope needed escaping:\n%s", b.String())
	}
}

// The property that made the whole-line escape look right in the first place,
// kept without it: a root name is ArgoCD's to spell, and one carrying a
// backtick must not close the code span the gate put it in.
func TestAHostileRootNameCannotWriteScopeStructure(t *testing.T) {
	derived := &gate.Derivation{Applications: 1, ApplicationSets: 1}
	cfg, err := gate.ParseConfig([]byte("{}"), ".gitops-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}

	lines := scopeLines(derived, cfg, "", []string{"evil` breaks `out"})
	joined := joinLines(lines)

	if !strings.Contains(joined, `evil\x60 breaks \x60out`) {
		t.Errorf("the root's backticks must be spelt out, visibly: %s", joined)
	}
	for _, line := range lines {
		if n := strings.Count(string(line), "`"); n%2 != 0 {
			t.Fatalf("a root name left an unbalanced code span: %q", line)
		}
	}
}
