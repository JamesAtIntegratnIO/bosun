package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gate/validate.go was 0% covered end to end while sitting on the in-cluster
// path, and the count it produces now blocks the gate, so nothing between
// "the manifests are fine" and "the manifests are wrong" was exercised.

// repoWith writes a fixture repository and returns its root.
func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func needKubeconform(t *testing.T) {
	t.Helper()
	requireTool(t, "kubeconform")
}

const goodManifest = `apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
  namespace: apps
data:
  key: value
`

// A manifest whose replicas is a string, not an integer, the shape that
// renders fine and is rejected at apply.
const badManifest = `apiVersion: apps/v1
kind: Deployment
metadata:
  name: broken
  namespace: apps
spec:
  replicas: "three"
  selector:
    matchLabels: {app: broken}
  template:
    metadata:
      labels: {app: broken}
    spec:
      containers:
        - name: c
          image: busybox
`

func TestValidateManifestsPassesAValidStream(t *testing.T) {
	needKubeconform(t)
	root := repoWith(t, map[string]string{"apps/cm.yaml": goodManifest})
	cfg := &Config{
		Sources:  []Source{{Name: "apps", Type: SourceManifests, Paths: []string{"apps/*.yaml"}}},
		Validate: ValidateConfig{Enabled: true, IgnoreMissingSchemas: true},
	}

	var out strings.Builder
	failures, err := ValidateManifests(context.Background(), root, cfg, &Inventory{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Errorf("a valid manifest must not fail: %d\n%s", failures, out.String())
	}
	// Silence and success must not look the same: a reader has to be able to
	// tell "it passed" from "it did not run".
	if !strings.Contains(out.String(), "passed schema validation") {
		t.Errorf("a clean run must say so:\n%s", out.String())
	}
}

func TestValidateManifestsCountsAndNamesAFailure(t *testing.T) {
	needKubeconform(t)
	root := repoWith(t, map[string]string{"apps/bad.yaml": badManifest})
	cfg := &Config{
		Sources:  []Source{{Name: "apps", Type: SourceManifests, Paths: []string{"apps/*.yaml"}}},
		Validate: ValidateConfig{Enabled: true, IgnoreMissingSchemas: true},
	}

	var out strings.Builder
	failures, err := ValidateManifests(context.Background(), root, cfg, &Inventory{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("want one failure, got %d\n%s", failures, out.String())
	}
	// The count is what blocks; the text is what a human acts on, so it has to
	// name the object rather than just report a number.
	body := out.String()
	if !strings.Contains(body, "Deployment") || !strings.Contains(body, "broken") {
		t.Errorf("the report must name the object:\n%s", body)
	}
	if strings.Contains(body, "passed schema validation") {
		t.Errorf("a failing run must not also claim success:\n%s", body)
	}
}

// skipKinds is a suppression, and the gate now reports it as one, but it has
// to suppress.
func TestSkipKindsSuppressesTheFailure(t *testing.T) {
	needKubeconform(t)
	root := repoWith(t, map[string]string{"apps/bad.yaml": badManifest})
	cfg := &Config{
		Sources: []Source{{Name: "apps", Type: SourceManifests, Paths: []string{"apps/*.yaml"}}},
		Validate: ValidateConfig{
			Enabled: true, IgnoreMissingSchemas: true, SkipKinds: []string{"Deployment"},
		},
	}

	var out strings.Builder
	failures, err := ValidateManifests(context.Background(), root, cfg, &Inventory{}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Errorf("a skipped kind must not fail: %d\n%s", failures, out.String())
	}
}

// A source that cannot be read is an error, not a pass. Reporting zero
// failures for manifests nobody looked at is the confusion this whole gate
// exists to end.
func TestValidateManifestsSurfacesAnUnreadableSource(t *testing.T) {
	needKubeconform(t)
	cfg := &Config{
		Sources:  []Source{{Name: "apps", Type: SourceManifests, Paths: []string{"apps/*.yaml"}}},
		Validate: ValidateConfig{Enabled: true, IgnoreMissingSchemas: true},
	}
	root := repoWith(t, map[string]string{"apps/broken.yaml": "{{ not: yaml"})

	var out strings.Builder
	if _, err := ValidateManifests(context.Background(), root, cfg, &Inventory{}, &out); err == nil {
		t.Fatal("an unparseable manifest must not read as zero failures")
	}
}

// A document with no `kind` is not a manifest, and offering one to kubeconform
// is how a values file sitting in a directory source put a standing `schema=2`
// on every pull request of a live repository -- on Applications those pull
// requests did not touch, while ArgoCD reported both Synced and Healthy.
//
// The gate already refuses one everywhere else: objectFrom drops it from the
// resource diff, so this was the single finding that saw it. Blockers.Schema
// has no repository-side remedy and blocks, so the cost was a check that is
// red on every change, which is a check people learn to override.
func TestAKindlessDocumentIsNotASchemaFailure(t *testing.T) {
	requireTool(t, "kubeconform")

	// The shape that caused it: a directory source holding a real manifest
	// and the values.yaml a sibling chart source consumes through `$values/`.
	root := repoWith(t, map[string]string{
		"addons/thing/configmap.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: thing\ndata:\n  a: b\n",
		"addons/thing/values.yaml":    "replicaCount: 2\nimage:\n  tag: v1.2.3\n",
	})
	cfg := &Config{
		Sources: []Source{{Name: "app/thing", Type: SourceDirectory, Path: "addons/thing", Recurse: true}},
		Validate: ValidateConfig{Enabled: true, IgnoreMissingSchemas: true,
			SchemaLocations: []string{"default"}},
	}

	var report strings.Builder
	failures, err := ValidateManifests(context.Background(), root, cfg, testInventory(), &report)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 0 {
		t.Fatalf("a values file is not a manifest the schemas reject, got %d failure(s):\n%s",
			failures, report.String())
	}
	// Never silent: a reader must be able to tell "skipped a values file" from
	// "skipped your manifest".
	if got := report.String(); !strings.Contains(got, "declare no `kind`") || !strings.Contains(got, "app/thing (1)") {
		t.Errorf("the skip must be named and counted:\n%s", got)
	}
}

// And the other half of the same line: a document that HAS a kind and does not
// fit is still a failure. Folding the two together is how this started.
func TestADocumentWithAKindIsStillValidated(t *testing.T) {
	requireTool(t, "kubeconform")

	root := repoWith(t, map[string]string{
		"addons/thing/values.yaml": "replicaCount: 2\n",
		"addons/thing/bad.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: bad\n" +
			"data:\n  nested:\n    notAString: true\n",
	})
	cfg := &Config{
		Sources: []Source{{Name: "app/thing", Type: SourceDirectory, Path: "addons/thing", Recurse: true}},
		Validate: ValidateConfig{Enabled: true, IgnoreMissingSchemas: true,
			SchemaLocations: []string{"default"}},
	}

	var report strings.Builder
	failures, err := ValidateManifests(context.Background(), root, cfg, testInventory(), &report)
	if err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Fatalf("a ConfigMap whose data is not string-valued must still fail, got %d:\n%s",
			failures, report.String())
	}
	if !strings.Contains(report.String(), "app/thing (1)") {
		t.Errorf("the skipped values file must still be counted beside the real failure:\n%s", report.String())
	}
}
