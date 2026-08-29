package gate

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"sigs.k8s.io/yaml"
)

func surface(paths ...string) map[string]bool {
	m := map[string]bool{}
	for _, p := range paths {
		m[p] = true
	}
	return m
}

func TestCoveredUnderstandsWhatASurfaceImplies(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		surf map[string]bool
		want bool
	}{
		{"exact", "a.b.c", surface("a.b.c"), true},
		{"under a documented object", "a.b.c", surface("a.b"), true},
		{"a parent of documented children is known", "a.b", surface("a.b.c"), true},
		{"unrelated sibling proves nothing", "a.b", surface("a.c"), false},
		{"empty surface knows nothing", "a", surface(), false},
		// The bug this guards: a prefix string match would call `image` known
		// because `imagePullSecrets` exists, and silently hide a real drop.
		{"a shared string prefix is not containment", "image", surface("imagePullSecrets"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathCovered(tc.path, tc.surf); got != tc.want {
				t.Fatalf("pathCovered(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestLeafPathsCountsSettingsNotContainers(t *testing.T) {
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(`
image:
  registry: ghcr.io
  tag: "1.2.3"
replicas: 2
empty: {}
`), &doc); err != nil {
		t.Fatal(err)
	}
	got := leafPaths(doc, "")
	sort.Strings(got)
	// `image` is a container, not a setting; counting it would report one drop
	// as three. `empty: {}` is a setting; someone wrote it deliberately.
	want := []string{"empty", "image.registry", "image.tag", "replicas"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("leafPaths = %v, want %v", got, want)
	}
}

func TestHelmDocsKeysReadsOnlyTheValuesTable(t *testing.T) {
	readme := `
# Chart

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| admissionController.replicas | int | ` + "`3`" + ` | how many |
| crds.groups.kyverno | object | ` + "`{}`" + ` | groups |

Some prose, and an unrelated table:

| Name | Meaning |
|------|---------|
| foo | not a values key |
`
	got := helmDocsKeys(readme)
	want := []string{"admissionController.replicas", "crds.groups.kyverno"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("helmDocsKeys = %v, want %v", got, want)
	}
}

func TestRepoValuesMergesFilesInHelmsOrder(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.yaml", "image:\n  tag: one\n  registry: ghcr.io\n")
	write("b.yaml", "image:\n  tag: two\n")

	got, err := repoValues(dir, Row{
		ValueFiles:   []string{"a.yaml", "$values/b.yaml", "missing.yaml"},
		ValuesInline: "replicas: 3\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	img := got["image"].(map[string]any)
	if img["tag"] != "two" {
		t.Errorf("later file must win: tag = %v", img["tag"])
	}
	if img["registry"] != "ghcr.io" {
		t.Errorf("merge must not drop keys the later file omits: %v", img)
	}
	if got["replicas"] != float64(3) && got["replicas"] != 3 {
		t.Errorf("inline values must apply last: %v", got["replicas"])
	}
	// A value file an Application names but does not have is normal, exactly
	// as it is for the render, ArgoCD's ignoreMissingValueFiles does this.
}

// A finding is only trustworthy if the old chart explained what we already
// set. Below the coverage floor the correct output is silence, not a guess.
func TestAnUnreadableChartSurfaceSaysNothing(t *testing.T) {
	set := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	old := surface("a") // explains 10% of what we set
	recognised := 0
	for _, p := range set {
		if pathCovered(p, old) {
			recognised++
		}
	}
	if recognised*100/len(set) >= minCoverage {
		t.Fatal("a surface explaining one setting in ten must not be trusted to prove absence")
	}
}
