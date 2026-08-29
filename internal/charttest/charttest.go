// Package charttest serves a chart repository on loopback, for the tests that
// need two versions of one chart.
//
// The gate's hardest findings are all differences between two versions of the
// same chart, and a chart directory on disk cannot express one: `--version` is
// ignored for a local path, so both renders read the same files and every
// cross-version check is answered with silence. Rendering against a public
// chart buys a real answer and pays for it with somebody else's uptime in the
// test suite, on cases whose interesting fixtures -- a schema that rejects what
// the repository sets, a value the new version stops declaring -- are ones no
// public chart is obliged to keep providing.
//
// So the fixture is built here: two tarballs, an index, and a file server.
// helm sees an ordinary repository and takes an ordinary path through it.
//
// It is a package rather than a helper in one test file because two packages
// need it. The gate proves it blocks on a chart that will not render; the
// agent proves it repairs one, and a second copy of a hundred lines of tar
// writing is a second copy that drifts.
package charttest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"testing"
)

// Version is one version of one chart: the files inside it, keyed by their
// path under the chart directory.
type Version struct {
	Version string
	Files   map[string]string
}

// Serve writes the chart at each given version and serves them as a Helm
// repository, returning its URL. The server is closed with the test.
func Serve(t *testing.T, chart string, versions ...Version) string {
	t.Helper()
	dir := t.TempDir()

	// helm reads the machine's own repository list and index cache, and both
	// have to be pointed somewhere else, together.
	//
	// The cache alone is obvious: a suite that writes into ~/Library/Caches
	// gets results that depend on what a previous run left there. The
	// repository list is the one that bites. Resolving a chart URL, helm
	// scans every configured repository looking for the one that owns it, and
	// reads each of their cached indexes to do it -- so with the cache
	// redirected and the list left alone, a developer who has ever run
	// `helm repo add` gets "no cached repo found" for a repository this test
	// is serving on loopback, and CI, with no list at all, does not.
	t.Setenv("HELM_REPOSITORY_CONFIG", filepath.Join(dir, "helm", "repositories.yaml"))
	t.Setenv("HELM_REPOSITORY_CACHE", filepath.Join(dir, "cache", "repository"))
	t.Setenv("HELM_CACHE_HOME", filepath.Join(dir, "cache"))

	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)

	var entries []string
	for _, v := range versions {
		name := fmt.Sprintf("%s-%s.tgz", chart, v.Version)
		tgz := chartTarball(t, chart, v)
		if err := os.WriteFile(filepath.Join(dir, name), tgz, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(tgz)
		entries = append(entries, fmt.Sprintf(
			"  - apiVersion: v2\n    name: %s\n    version: %s\n    created: \"2020-01-01T00:00:00Z\"\n    digest: %s\n    urls:\n      - %s/%s\n",
			chart, v.Version, hex.EncodeToString(sum[:]), srv.URL, name))
	}

	index := fmt.Sprintf("apiVersion: v1\ngenerated: \"2020-01-01T00:00:00Z\"\nentries:\n  %s:\n%s",
		chart, joinAll(entries))
	if err := os.WriteFile(filepath.Join(dir, "index.yaml"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	return srv.URL
}

func joinAll(s []string) string {
	var b bytes.Buffer
	for _, x := range s {
		b.WriteString(x)
	}
	return b.String()
}

// chartTarball packs one version the way `helm package` does: every entry
// under a top-level directory named for the chart.
func chartTarball(t *testing.T, chart string, v Version) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Sorted, because a map range would pack the same chart into a different
	// tarball on every run, and a fixture that is not byte-stable is one that
	// can fail on one run in ten with nothing to look at.
	names := make([]string, 0, len(v.Files))
	for name := range v.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		body := v.Files[name]
		if err := tw.WriteHeader(&tar.Header{
			Name: path.Join(chart, name), Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Strict is the shape of the bump all of this comes from: a chart that gains a
// values.schema.json forbidding what it used to accept, drops one key the
// repository is still setting, and renames another.
//
// 1.0.0 declares `greeting`, `legacy` and `port` and ships no schema, which is
// what let those values work for years. 2.0.0 refuses everything it does not
// declare, has no room for `legacy` at all, and calls the port `podPort`. That
// is a removal and a rename in one bump, which is exactly the pair no single
// mechanism in this repository could express.
func Strict(t *testing.T) (repoURL string) {
	t.Helper()
	const body = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: thing\ndata:\n" +
		"  greeting: {{ .Values.greeting | quote }}\n"
	// 2.0.0 insists on a port its schema only types. A chart that validates
	// beyond its schema is ordinary, and it is what gives the values
	// migration something its static checks cannot catch: a key dropped where
	// it should have been renamed passes every one of them and fails here.
	const guarded = "{{- if not .Values.podPort }}{{ fail \"podPort must be set\" }}{{- end }}\n" + body
	return Serve(t, "thing",
		Version{Version: "1.0.0", Files: map[string]string{
			"Chart.yaml":        "apiVersion: v2\nname: thing\nversion: 1.0.0\n",
			"values.yaml":       "greeting: hello\nlegacy: true\nport: 8080\n",
			"templates/cm.yaml": body,
		}},
		Version{Version: "2.0.0", Files: map[string]string{
			"Chart.yaml":  "apiVersion: v2\nname: thing\nversion: 2.0.0\n",
			"values.yaml": "greeting: hello\n",
			"values.schema.json": `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "greeting": {"type": "string"},
    "podPort": {"type": "integer"},
    "global": {"type": "object"}
  }
}`,
			"templates/cm.yaml": guarded,
		}},
	)
}

// Underivable is the boundary case: a chart whose new version requires a key
// neither the repository's values nor the chart's own schema can supply.
//
// `metrics.serviceMonitor.namespace` is the real one this is drawn from. The
// human who fixed that bump chose `monitoring` from context the chart does not
// contain, and a model asked the same question would have chosen something
// too.
func Underivable(t *testing.T) (repoURL string) {
	t.Helper()
	const body = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: thing\ndata:\n" +
		"  greeting: {{ .Values.greeting | quote }}\n"
	return Serve(t, "thing",
		Version{Version: "1.0.0", Files: map[string]string{
			"Chart.yaml":        "apiVersion: v2\nname: thing\nversion: 1.0.0\n",
			"values.yaml":       "greeting: hello\nlegacy: true\nport: 8080\n",
			"templates/cm.yaml": body,
		}},
		Version{Version: "2.0.0", Files: map[string]string{
			"Chart.yaml":  "apiVersion: v2\nname: thing\nversion: 2.0.0\n",
			"values.yaml": "greeting: hello\n",
			"values.schema.json": `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["namespace"],
  "properties": {
    "greeting": {"type": "string"},
    "namespace": {"type": "string"},
    "podPort": {"type": "integer"},
    "global": {"type": "object"}
  }
}`,
			"templates/cm.yaml": body,
		}},
	)
}

// RequireTool skips rather than fails when an external binary is absent, the
// same trade the gate's own suite makes: a suite that depends on the
// developer's PATH is a suite nobody runs. REQUIRE_TOOLS=1 turns the skip into
// a failure, and CI sets it.
func RequireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		if os.Getenv("REQUIRE_TOOLS") != "" {
			t.Fatalf("%s is not on PATH and REQUIRE_TOOLS is set: "+
				"this seam must be exercised here, not skipped", name)
		}
		t.Skipf("%s is not on PATH; skipping", name)
	}
}
