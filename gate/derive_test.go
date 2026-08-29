package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const anAppSet = `apiVersion: argoproj.io/v1alpha1
kind: ApplicationSet
metadata:
  name: apps
  namespace: argocd
spec:
  generators: []
`

// The scan runs over a whole gitops repository, and a gitops repository is
// full of files that are not YAML documents. Refusing on the first one is not
// a hypothetical: a naive scan died on a Helm template, and the root it was
// looking for was three directories further on.
func TestFindManifestSkipsWhatItCannotParse(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"charts/x/templates/deploy.yaml":  "{{- if .Values.enabled }}\nkind: Deployment\n{{- end }}\n",
		"charts/x/templates/_helpers.tpl": "{{- define \"x.name\" -}}\nx\n{{- end -}}\n",
		"ci/pipeline.yaml":                "steps:\n  - uses: [broken\n",
		"bootstrap/apps.yaml":             anAppSet,
	})

	got, found, err := FindManifest(root, "ApplicationSet", "argocd", "apps")
	if err != nil {
		t.Fatalf("an unparseable file must not end the scan: %v", err)
	}
	if !found || got != "bootstrap/apps.yaml" {
		t.Fatalf("found=%v path=%q, want the manifest that declares it", found, got)
	}
}

// A manifest committed without a namespace takes one from wherever it is
// applied. Refusing to match it would send every such root down the live-spec
// fallback, which is the path this whole mechanism exists to avoid.
func TestFindManifestMatchesAManifestWithNoNamespace(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"bootstrap/apps.yaml": "apiVersion: argoproj.io/v1alpha1\nkind: ApplicationSet\nmetadata:\n  name: apps\n",
	})
	if _, found, err := FindManifest(root, "ApplicationSet", "argocd", "apps"); err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestFindManifestDoesNotMatchADifferentObject(t *testing.T) {
	root := writeFiles(t, map[string]string{"bootstrap/apps.yaml": anAppSet})
	for _, tc := range []struct{ kind, ns, name string }{
		{"Application", "argocd", "apps"},
		{"ApplicationSet", "argocd", "other"},
		{"ApplicationSet", "other", "apps"},
	} {
		if _, found, _ := FindManifest(root, tc.kind, tc.ns, tc.name); found {
			t.Errorf("%v must not match", tc)
		}
	}
}

// ArgoCD's directory semantics decide what a path expands to, and each of the
// three rules changes what deploys. `exclude` is the one measured in the wild:
// a live source carried `exclude: exclude/*` with a bootstrap manifest under
// that path, so ignoring it renders an ApplicationSet the cluster does not
// have.
func TestDirectorySemanticsMatchArgoCD(t *testing.T) {
	for _, tc := range []struct {
		name             string
		rel              string
		include, exclude string
		want             bool
	}{
		{"no rules includes everything", "a/b.yaml", "", "", true},
		{"exclude a directory", "exclude/bootstrap.yaml", "", "exclude/*", false},
		// `*` stops at a separator, so this one is NOT excluded. The
		// direction is deliberate: rendering an object ArgoCD does not puts
		// it in the report where a reader can chase it, and excluding one
		// ArgoCD renders removes it from both sides of the diff, which finds
		// no difference and says so with total confidence.
		{"a single star stops at a separator", "exclude/deep/x.yaml", "", "exclude/*", true},
		{"a double star crosses them", "exclude/deep/x.yaml", "", "exclude/**", false},
		{"exclude leaves the rest", "apps/x.yaml", "", "exclude/*", true},
		{"include narrows", "apps/x.yaml", "apps/*", "", true},
		{"include excludes the rest", "other/x.yaml", "apps/*", "", false},
		{"exclude wins over include", "apps/x.yaml", "apps/*", "apps/*", false},
		{"a brace group is one pattern per alternative", "env/x.yaml", "", "{skip/*,env/*}", false},
		{"a brace group leaves what it does not name", "keep/x.yaml", "", "{skip/*,env/*}", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := directoryAllows(tc.rel, tc.include, tc.exclude); got != tc.want {
				t.Errorf("directoryAllows(%q, include=%q, exclude=%q) = %v, want %v",
					tc.rel, tc.include, tc.exclude, got, tc.want)
			}
		})
	}
}

// Without recurse ArgoCD reads the path's own files and descends into nothing,
// so a gate that always recursed would render subdirectories nobody applies.
func TestDirectorySourceHonoursRecurse(t *testing.T) {
	root := writeFiles(t, map[string]string{
		"apps/top.yaml":          "kind: ConfigMap\nmetadata:\n  name: top\n",
		"apps/deep/nested.yaml":  "kind: ConfigMap\nmetadata:\n  name: nested\n",
		"apps/exclude/trap.yaml": "kind: ConfigMap\nmetadata:\n  name: trap\n",
	})
	dir := filepath.Join(root, "apps")

	shallow, err := readArgoDirectory(dir, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(shallow) != 1 {
		t.Fatalf("without recurse only the path's own files are read, got %d", len(shallow))
	}

	deep, err := readArgoDirectory(dir, true, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(deep) != 3 {
		t.Fatalf("with recurse every manifest below it is read, got %d", len(deep))
	}

	trapped, err := readArgoDirectory(dir, true, "", "exclude/*")
	if err != nil {
		t.Fatal(err)
	}
	if len(trapped) != 2 {
		t.Fatalf("the excluded path must not be rendered, got %d objects", len(trapped))
	}
	for _, o := range trapped {
		md, _ := o["metadata"].(map[string]any)
		if md["name"] == "trap" {
			t.Error("an excluded manifest reached the render")
		}
	}
}

// `live` is the one source type configuration may not set: a source that
// renders whatever it was handed would put content into the verdict that no
// revision of this repository contains.
func TestConfigurationCannotSetTheLiveSourceType(t *testing.T) {
	_, err := ParseConfig([]byte("sources:\n  - {name: x, type: live}\n"), ".bosun.yaml")
	if err == nil {
		t.Fatal("a config setting type: live must not parse")
	}
	if !strings.Contains(err.Error(), "configuration cannot set") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// An empty config is an ordinary shape since ADR 0012: ArgoCD says what this
// repository deploys, and a file holding only `roots:` or `validate:` is not a
// mistake. The refusal for "nothing to render at all" belongs where the
// derivation is also known.
func TestAnEmptyConfigParses(t *testing.T) {
	cfg, err := ParseConfig([]byte("validate:\n  enabled: true\n"), ".bosun.yaml")
	if err != nil {
		t.Fatalf("a config with no sources must parse: %v", err)
	}
	if len(cfg.Sources) != 0 || !cfg.Validate.Enabled {
		t.Fatalf("got %+v", cfg)
	}
}
