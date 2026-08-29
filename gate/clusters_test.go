package gate

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// gate/clusters.go was entirely untested, and it produces the cluster
// inventory every render is expanded against. A cluster that arrives with the
// wrong labels is a generator that selects the wrong set, and the gate then
// reports the difference as a targeting change the pull request made.

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func secret(name string, labels, annotations map[string]string, data map[string]string) ClusterSecret {
	var s ClusterSecret
	s.Metadata.Name = name
	s.Metadata.Labels = labels
	s.Metadata.Annotations = annotations
	s.Data = data
	return s
}

// The name and server live base64-encoded in the Secret's data, and the
// Secret's own name is the fallback.
func TestInventoryFromSecretsDecodesNameAndServer(t *testing.T) {
	inv := InventoryFromSecrets([]ClusterSecret{
		secret("hub-secret", nil, nil, map[string]string{
			"name": b64("hub"), "server": b64("https://hub.example"),
		}),
		secret("edge-secret", nil, nil, nil),
	}, ExportFilter{})

	if len(inv.Clusters) != 2 {
		t.Fatalf("got %d", len(inv.Clusters))
	}
	if inv.Clusters[0].Name != "hub" || inv.Clusters[0].Server != "https://hub.example" {
		t.Errorf("got %+v", inv.Clusters[0])
	}
	if inv.Clusters[1].Name != "edge-secret" {
		t.Errorf("the Secret name is the fallback, got %q", inv.Clusters[1].Name)
	}
	// A live read never passes through LoadInventory, and generators in the
	// wild routinely select on this label.
	if inv.Clusters[0].Labels["argocd.argoproj.io/secret-type"] != "cluster" {
		t.Errorf("the secret-type label must be present: %v", inv.Clusters[0].Labels)
	}
	// Not stamped: GeneratedAt is a property of a snapshot, added by whoever
	// takes one.
	if inv.GeneratedAt != "" {
		t.Errorf("InventoryFromSecrets must not stamp, got %q", inv.GeneratedAt)
	}
}

// Labels are selector inputs and are never dropped, which selector a future
// bootstrap will match on is unknowable, and dropping one reintroduces the
// stale-fixture failure this export exists to prevent. Annotations are trimmed
// to what the repository templates with.
func TestLabelsSurviveAndAnnotationsAreTrimmed(t *testing.T) {
	inv := InventoryFromSecrets([]ClusterSecret{
		secret("c",
			map[string]string{"environment": "production", "obscure": "kept-anyway"},
			map[string]string{"used": "yes", "unused": "no"},
			map[string]string{"name": b64("c")}),
	}, ExportFilter{KeepAnnotations: map[string]bool{"used": true}})

	c := inv.Clusters[0]
	if c.Labels["obscure"] != "kept-anyway" {
		t.Errorf("no label may be dropped: %v", c.Labels)
	}
	if c.Annotations["used"] != "yes" {
		t.Errorf("a templated annotation must survive: %v", c.Annotations)
	}
	if _, ok := c.Annotations["unused"]; ok {
		t.Errorf("an untemplated annotation must be trimmed: %v", c.Annotations)
	}
}

// Noisy keys churn without changing what any selector or template sees, and a
// check that always fails gets switched off, which is worse than not having
// it.
func TestStripDropsNoisyKeysExactlyAndByPrefix(t *testing.T) {
	f := ExportFilter{IgnoreKeys: append(append([]string{}, defaultNoisyKeys...), "site.example/*")}
	got := f.strip(map[string]string{
		"kubectl.kubernetes.io/last-applied-configuration": "huge",
		"reconcile.external-secrets.io/data-hash":          "abc",
		"site.example/anything":                            "churn",
		"site.example":                                     "exact-prefix-without-slash",
		"keep-me":                                          "yes",
	})
	if got["keep-me"] != "yes" {
		t.Errorf("an ordinary key must survive: %v", got)
	}
	for _, gone := range []string{
		"kubectl.kubernetes.io/last-applied-configuration",
		"reconcile.external-secrets.io/data-hash",
		"site.example/anything",
	} {
		if _, ok := got[gone]; ok {
			t.Errorf("%q must be stripped: %v", gone, got)
		}
	}
}

func TestKeepOnlyIsOpenWhenNothingIsNamed(t *testing.T) {
	in := map[string]string{"a": "1", "b": "2"}
	if got := keepOnly(in, nil); len(got) != 2 {
		t.Errorf("an empty keep-set keeps everything, got %v", got)
	}
	if got := keepOnly(in, map[string]bool{"a": true}); len(got) != 1 || got["a"] != "1" {
		t.Errorf("got %v", got)
	}
}

// A re-export must not report drift purely because time passed.
func TestNormaliseInventoryDropsTheTimestamp(t *testing.T) {
	a := "generatedAt: 2024-01-01T00:00:00Z\nclusters:\n  - name: hub\n"
	b := "generatedAt: 2025-09-09T09:09:09Z\nclusters:\n  - name: hub\n"
	if NormaliseInventory([]byte(a)) != NormaliseInventory([]byte(b)) {
		t.Error("two exports of the same clusters must normalise to the same text")
	}
	// A real difference must still show.
	c := "generatedAt: 2024-01-01T00:00:00Z\nclusters:\n  - name: edge\n"
	if NormaliseInventory([]byte(a)) == NormaliseInventory([]byte(c)) {
		t.Error("a different cluster set must not normalise away")
	}
	// Unparseable input is returned as-is rather than swallowed: the caller
	// compares strings, and an empty one would read as "no drift".
	if got := NormaliseInventory([]byte("{{ not yaml")); got != "{{ not yaml" {
		t.Errorf("got %q", got)
	}
}

func TestDecodeRejectsWhatIsNotBase64(t *testing.T) {
	if got := decode(b64("hello")); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := decode("not base64!!!"); got != "" {
		t.Errorf("undecodable data must not become a cluster name, got %q", got)
	}
	if got := decode(""); got != "" {
		t.Errorf("got %q", got)
	}
}

// The keep-list is derived from the repository rather than configured: a list
// an operator maintains by hand is a list that goes wrong, and the answer is
// already in the repository. It scans the whole tree, not just the
// bootstraps, the inner ApplicationSets template with annotations too.
func TestAnnotationsUsedByScansTheWholeRepository(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"bootstrap/appset.yaml": "path: '{{ metadata.annotations.bootstrap_key }}'\n",
		"inner/nested/app.yaml": `ns: '{{ metadata.annotations["cert_manager_namespace"] }}'`,
		"chart/values.tpl":      "x: {{ metadata.annotations.tpl_key }}\n",
		"README.md":             "metadata.annotations.not_scanned\n",
		".git/config":           "metadata.annotations.never\n",
	}
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := annotationsUsedBy(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bootstrap_key", "cert_manager_namespace", "tpl_key"} {
		if !got[want] {
			t.Errorf("%q is templated with and was not found: %v", want, got)
		}
	}
	// Only the extensions that can be templates, and never.git.
	if got["not_scanned"] || got["never"] {
		t.Errorf("scanned something it should not have: %v", got)
	}
}

// The filter carries the defaults every ArgoCD install needs, plus the
// config's, plus what the repository templates with.
func TestNewExportFilterCombinesAllThreeSources(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.yaml"),
		[]byte("x: '{{ metadata.annotations.from_repo }}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{ClustersExport: ClustersExportConfig{IgnoreKeys: []string{"site.example/*"}}}

	f := NewExportFilter(root, cfg)
	if !f.KeepAnnotations["from_repo"] {
		t.Errorf("the repository's own usage must reach the filter: %v", f.KeepAnnotations)
	}
	joined := strings.Join(f.IgnoreKeys, " ")
	if !strings.Contains(joined, "site.example/*") {
		t.Errorf("the config's keys must reach the filter: %v", f.IgnoreKeys)
	}
	for _, d := range defaultNoisyKeys {
		if !strings.Contains(joined, d) {
			t.Errorf("the default %q must survive: %v", d, f.IgnoreKeys)
		}
	}

	// No repository and no config is the live-read case: nothing kept back,
	// because nothing is ever diffed against a live inventory.
	bare := NewExportFilter("", nil)
	if len(bare.KeepAnnotations) != 0 {
		t.Errorf("an empty keep-set keeps every annotation, got %v", bare.KeepAnnotations)
	}
}
