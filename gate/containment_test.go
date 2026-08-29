package gate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/safepath"
)

// Every path in this file arrives from the pull request under review, and each
// site used to reach the filesystem through filepath.Join and an existence
// check. Join cleans a path; it does not contain one. What the containment
// rules are is safepath's question and safepath's test; what these assert is
// that each site asks it, because six sites had the same escape and closing
// five of them is worth nothing.

// outsideFile is a readable YAML file that is not in the checkout: what an
// escape is aimed at, a mounted kubeconfig or a secret volume.
func outsideFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	full := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(full, []byte("secret: value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// linkOut plants a tracked symlink in the checkout pointing at a file outside
// it. A repository may hold one, and it passes every lexical test ever written.
func linkOut(t *testing.T, root, name string) {
	t.Helper()
	if err := os.Symlink(outsideFile(t), filepath.Join(root, name)); err != nil {
		t.Skipf("this filesystem does not support symbolic links: %v", err)
	}
}

func wantRefused(t *testing.T, err error, rule error) {
	t.Helper()
	if err == nil {
		t.Fatalf("want a refusal naming %v, got no error at all", rule)
	}
	if !errors.Is(err, rule) {
		t.Fatalf("want a refusal naming %v, got %v", rule, err)
	}
}

// The Row's value files are helm's `-f` arguments, and anything that parses as
// a mapping merges into the values and can render into the published comment.
func TestRenderChartVersionRefusesValueFilesOutsideTheCheckout(t *testing.T) {
	root := t.TempDir()
	r := Row{
		App: "thing", Chart: "thing", ChartRepo: "https://charts.example.com",
		Version: "1.0.0", ValueFiles: []string{"../../../../etc/passwd.yaml"},
	}
	_, err := renderChartVersion(context.Background(), root, r)
	wantRefused(t, err, safepath.ErrEscapes)
}

func TestRenderChartVersionRefusesValueFilesThroughASymlink(t *testing.T) {
	root := t.TempDir()
	linkOut(t, root, "values.yaml")
	r := Row{
		App: "thing", Chart: "thing", ChartRepo: "https://charts.example.com",
		Version: "1.0.0", ValueFiles: []string{"values.yaml"},
	}
	_, err := renderChartVersion(context.Background(), root, r)
	wantRefused(t, err, safepath.ErrSymlink)
}

// The `$values/` prefix is stripped before containment, so an escape written
// through the multi-source ref is the same escape.
func TestRenderChartVersionContainsAfterStrippingTheValuesRef(t *testing.T) {
	root := t.TempDir()
	r := Row{
		App: "thing", Chart: "thing", ChartRepo: "https://charts.example.com",
		Version: "1.0.0", ValueFiles: []string{"$values/../../../../etc/passwd.yaml"},
	}
	_, err := renderChartVersion(context.Background(), root, r)
	wantRefused(t, err, safepath.ErrEscapes)
}

// repoValues reads the same head-controlled list, and the key names of what it
// reads are what the dropped-settings finding prints.
func TestRepoValuesRefusesValueFilesOutsideTheCheckout(t *testing.T) {
	root := t.TempDir()
	_, err := repoValues(root, Row{
		App: "thing", ValueFiles: []string{"../../../../etc/passwd.yaml"},
	})
	wantRefused(t, err, safepath.ErrEscapes)
}

func TestRepoValuesRefusesValueFilesThroughASymlink(t *testing.T) {
	root := t.TempDir()
	linkOut(t, root, "values.yaml")
	_, err := repoValues(root, Row{App: "thing", ValueFiles: []string{"values.yaml"}})
	wantRefused(t, err, safepath.ErrSymlink)
}

// The factory chart's own path and values layers come from `.gitops-gate.yaml`
// at the head revision, which the pull request may rewrite.
func TestHelmTemplateRawRefusesAChartPathOutsideTheCheckout(t *testing.T) {
	_, err := helmTemplateRaw(context.Background(), t.TempDir(), "../../../../etc", nil)
	wantRefused(t, err, safepath.ErrEscapes)
}

func TestHelmTemplateRawRefusesValueFilesOutsideTheCheckout(t *testing.T) {
	root := repoWith(t, map[string]string{"charts/app/Chart.yaml": "name: app\n"})
	_, err := helmTemplateRaw(context.Background(), root, "charts/app",
		[]string{"../../../../etc/passwd.yaml"})
	wantRefused(t, err, safepath.ErrEscapes)
}

// A bootstrap ApplicationSet is read from a path the config names.
func TestCollectBootstrapRefusesAPathOutsideTheCheckout(t *testing.T) {
	_, err := collectBootstrap(context.Background(), t.TempDir(), &Config{},
		&Inventory{}, Source{Name: "bootstrap", Path: "../../../../etc/hosts"})
	wantRefused(t, err, safepath.ErrEscapes)
}

// Containment is asked of the glob's matches, not of its pattern: a `*` says
// nothing about what it expanded to, and nothing about a link standing there.
func TestReadGlobsRefusesMatchesOutsideTheCheckout(t *testing.T) {
	root := t.TempDir()
	// Planted in the checkout's own parent so the pattern has something real
	// to match; a glob that matches nothing proves nothing about containment.
	outside := filepath.Join(filepath.Dir(root), "elsewhere.yaml")
	if err := os.WriteFile(outside, []byte("secret: value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readGlobs(root, []string{"../*.yaml"})
	wantRefused(t, err, safepath.ErrEscapes)
}

func TestReadGlobsRefusesAMatchThatIsASymlink(t *testing.T) {
	root := t.TempDir()
	linkOut(t, root, "app.yaml")
	_, err := readGlobs(root, []string{"*.yaml"})
	wantRefused(t, err, safepath.ErrSymlink)
}
