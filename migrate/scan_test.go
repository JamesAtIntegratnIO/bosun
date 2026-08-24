package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var eso = Dropped{
	CRD: "externalsecrets.external-secrets.io", Group: "external-secrets.io",
	Kind: "ExternalSecret", Versions: []string{"v1alpha1", "v1beta1"}, Target: "v1",
}

// A multi-document file with two declarations at two dropped versions, one of
// them quoted and one carrying a trailing comment, next to a document that must
// not be touched.
const consumers = `# two secrets and a config map
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: a
---
apiVersion: "external-secrets.io/v1alpha1" # migrate me
kind: ExternalSecret
metadata:
  name: b
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: c
`

const consumersAfter = `# two secrets and a config map
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: a
---
apiVersion: "external-secrets.io/v1" # migrate me
kind: ExternalSecret
metadata:
  name: b
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: c
`

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "platform/secrets.yaml", consumers)
	// Same group and version, different kind: a SecretStore is not an
	// ExternalSecret, and plural.group similarity must not blur that.
	write(t, root, "platform/store.yaml",
		"apiVersion: external-secrets.io/v1beta1\nkind: SecretStore\nmetadata:\n  name: s\n")
	// A Helm template is a program, not a manifest -- even one that happens to
	// parse as YAML. The Chart.yaml beside its templates/ dir is what marks it.
	write(t, root, "charts/x/Chart.yaml", "apiVersion: v2\nname: x\nversion: 0.1.0\n")
	write(t, root, "charts/x/templates/es.yaml",
		"apiVersion: external-secrets.io/v1beta1\nkind: ExternalSecret\nmetadata:\n  name: {{ .Release.Name }}\n")
	write(t, root, "README.md", "not yaml at all")
	return root
}

func TestScanFindsDeclaringManifestsAndOnlyThose(t *testing.T) {
	hits, err := Scan(tree(t), eso)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly the declaring file, got %+v", hits)
	}
	h := hits[0]
	if h.Path != "platform/secrets.yaml" || h.Docs != 2 {
		t.Fatalf("want platform/secrets.yaml with 2 documents, got %+v", h)
	}
	if len(h.Versions) != 2 || h.Versions[0] != "v1alpha1" || h.Versions[1] != "v1beta1" {
		t.Fatalf("want both dropped versions recorded, got %+v", h.Versions)
	}
}

// The rewrite is a value replacement on the apiVersion line and nothing else:
// comments, quoting, ordering and every untouched document survive
// byte-for-byte, because a migration that reformats 33 files is unreviewable.
func TestMigrateRewritesOnlyTheDeclarations(t *testing.T) {
	root := tree(t)
	res, err := Migrate(root, []Dropped{eso}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 {
		t.Fatalf("nothing should be refused: %+v", res.Refused)
	}
	if len(res.Applied) != 1 || res.Applied[0].Path != "platform/secrets.yaml" ||
		res.Applied[0].To != "external-secrets.io/v1" {
		t.Fatalf("want one file moved to external-secrets.io/v1, got %+v", res.Applied)
	}

	got, err := os.ReadFile(filepath.Join(root, "platform/secrets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != consumersAfter {
		t.Errorf("the rewrite touched more than the values:\n--- got ---\n%s\n--- want ---\n%s", got, consumersAfter)
	}

	store, err := os.ReadFile(filepath.Join(root, "platform/store.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(store) != "apiVersion: external-secrets.io/v1beta1\nkind: SecretStore\nmetadata:\n  name: s\n" {
		t.Errorf("a different kind was rewritten: %s", store)
	}

	// The whole point: after the migration, a re-scan finds nothing -- which
	// is exactly the check the re-run gate performs to go green.
	left, err := Scan(root, eso)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("declarations survived the migration: %+v", left)
	}
}

// The policy answers for every file, including here. A denied consumer is
// reported as refused -- loudly, because a silent skip would let the re-run
// gate stay red with no explanation on the pull request.
func TestMigrateAnswersToThePathPolicy(t *testing.T) {
	root := t.TempDir()
	write(t, root, "platform/secrets.yaml", consumers)
	write(t, root, ".gitops-gate/fixture.yaml",
		"apiVersion: external-secrets.io/v1beta1\nkind: ExternalSecret\nmetadata:\n  name: g\n")

	deny := func(path string) string {
		if path == ".gitops-gate/fixture.yaml" {
			return "path is denied (.gitops-gate/**)"
		}
		return ""
	}
	res, err := Migrate(root, []Dropped{eso}, deny)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || res.Applied[0].Path != "platform/secrets.yaml" {
		t.Fatalf("the permitted file should still move: %+v", res.Applied)
	}
	if len(res.Refused) != 1 || res.Refused[0].Path != ".gitops-gate/fixture.yaml" {
		t.Fatalf("the denied file must be reported refused, got %+v", res.Refused)
	}
	data, _ := os.ReadFile(filepath.Join(root, ".gitops-gate/fixture.yaml"))
	if string(data) != "apiVersion: external-secrets.io/v1beta1\nkind: ExternalSecret\nmetadata:\n  name: g\n" {
		t.Error("the denied file was rewritten anyway")
	}
}

func TestMigrateWithoutATargetRefusesToRun(t *testing.T) {
	d := eso
	d.Target = ""
	if _, err := Migrate(t.TempDir(), []Dropped{d}, func(string) string { return "" }); err == nil {
		t.Fatal("a migration with no destination must be an error, not a guess")
	}
}

// A GitOps repository embeds manifests inside chart values -- an extraObjects
// list, or a whole manifest as a block-scalar string. Both render into real
// objects that break at apply, so both are declarations: counted by Scan,
// moved by Migrate.
const embeddedConsumers = `metrics-server:
  enabled: true
external-secrets:
  extraObjects:
    - apiVersion: external-secrets.io/v1beta1
      kind: ClusterSecretStore
      metadata:
        name: onepassword-store
cert-manager:
  extraDeploy:
    - |
      apiVersion: external-secrets.io/v1beta1
      kind: ExternalSecret
      metadata:
        name: cloudflare-api-key
`

const embeddedConsumersAfter = `metrics-server:
  enabled: true
external-secrets:
  extraObjects:
    - apiVersion: external-secrets.io/v1
      kind: ClusterSecretStore
      metadata:
        name: onepassword-store
cert-manager:
  extraDeploy:
    - |
      apiVersion: external-secrets.io/v1
      kind: ExternalSecret
      metadata:
        name: cloudflare-api-key
`

var esoStores = Dropped{
	CRD: "clustersecretstores.external-secrets.io", Group: "external-secrets.io",
	Kind: "ClusterSecretStore", Versions: []string{"v1beta1"}, Target: "v1",
}

func TestEmbeddedManifestsAreDeclarationsToo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "addons/values.yaml", embeddedConsumers)
	// A string that merely MENTIONS a version is not a declaration: no kind,
	// no manifest, no match.
	write(t, root, "addons/notes.yaml",
		"docs: |\n  upgrade note: external-secrets.io/v1beta1 is going away\n")

	hits, err := Scan(root, eso)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "addons/values.yaml" || hits[0].Docs != 1 {
		t.Fatalf("want the embedded ExternalSecret counted once and the mention not at all, got %+v", hits)
	}

	res, err := Migrate(root, []Dropped{eso, esoStores}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Refused) != 0 || len(res.Applied) != 1 {
		t.Fatalf("want one clean migration, got %+v", res)
	}
	got, _ := os.ReadFile(filepath.Join(root, "addons/values.yaml"))
	if string(got) != embeddedConsumersAfter {
		t.Errorf("embedded rewrite drifted:\n--- got ---\n%s\n--- want ---\n%s", got, embeddedConsumersAfter)
	}
	notes, _ := os.ReadFile(filepath.Join(root, "addons/notes.yaml"))
	if !strings.Contains(string(notes), "external-secrets.io/v1beta1") {
		t.Error("the mention was rewritten; it is not a declaration")
	}
}

// One embedded manifest of a kind no migration covers, declaring the same
// dropped version: its apiVersion line is indistinguishable by pattern from
// the ones that should move, so the whole file is refused rather than edited
// on a guess.
func TestAForeignEmbeddedKindRefusesTheWholeFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "addons/values.yaml", `things:
  - |
    apiVersion: external-secrets.io/v1beta1
    kind: ExternalSecret
    metadata:
      name: a
  - |
    apiVersion: external-secrets.io/v1beta1
    kind: PushSecret
    metadata:
      name: b
`)
	res, err := Migrate(root, []Dropped{eso}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("nothing may be applied: %+v", res.Applied)
	}
	if len(res.Refused) != 1 || !strings.Contains(res.Refused[0].Reason, "PushSecret") {
		t.Fatalf("want the foreign kind named in the refusal, got %+v", res.Refused)
	}
	got, _ := os.ReadFile(filepath.Join(root, "addons/values.yaml"))
	if !strings.Contains(string(got), "apiVersion: external-secrets.io/v1beta1") {
		t.Error("the refused file was edited anyway")
	}
}
