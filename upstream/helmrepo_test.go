package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A chart's artifact is `repoURL SPACE chartName`, built that way by the
// promotion pipeline. For an OCI chart the name is empty so the value trims to
// a bare URL, which is why every OCI path worked and no classic Helm repository
// ever did.

func TestAnArtifactIsNotAlwaysOneString(t *testing.T) {
	for _, tc := range []struct{ in, ref, chart string }{
		{"https://kyverno.github.io/kyverno kyverno", "https://kyverno.github.io/kyverno", "kyverno"},
		{"oci://ghcr.io/org/charts/bosun", "oci://ghcr.io/org/charts/bosun", ""},
		{"  oci://ghcr.io/org/charts/bosun  ", "oci://ghcr.io/org/charts/bosun", ""},
		{"ghcr.io/org/image", "ghcr.io/org/image", ""},
		{"", "", ""},
	} {
		ref, chart := ParseArtifact(tc.in)
		if ref != tc.ref || chart != tc.chart {
			t.Errorf("ParseArtifact(%q) = (%q,%q), want (%q,%q)", tc.in, ref, chart, tc.ref, tc.chart)
		}
	}
}

const helmIndex = `apiVersion: v1
entries:
  kyverno:
    - version: 3.6.0
      home: https://kyverno.io
      sources:
        - https://github.com/kyverno/kyverno
    - version: 3.5.2
      sources:
        - https://github.com/kyverno/kyverno
  other-chart:
    - version: 1.0.0
      sources:
        - https://github.com/someone/else
  home-only:
    - version: 2.0.0
      home: https://github.com/someone/home-only
  bare:
    - version: 9.9.9
`

// `helm repo index` copies Chart.yaml's `sources` into index.yaml, exactly as
// `helm push` copies it into an OCI annotation. Same declaration by the same
// publisher, in the format their distribution channel uses.
func TestAClassicHelmRepositoryDeclaresItsSourceInTheIndex(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, helmIndex)
	}))
	t.Cleanup(ts.Close)
	g2 := &GitHubReleases{HTTP: ts.Client()}

	repo, err := g2.sourceRepo(context.Background(), ts.URL+" kyverno", "3.6.0")
	if err != nil {
		t.Fatalf("a chart that declares its source was not resolved: %v", err)
	}
	if repo != "kyverno/kyverno" {
		t.Fatalf("resolved %q", repo)
	}
}

// The right chart out of an index that holds many. Reading the wrong entry
// answers with another project's repository, confidently.
func TestTheRightChartIsTakenFromAnIndexOfMany(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, helmIndex)
	}))
	t.Cleanup(ts.Close)
	g := &GitHubReleases{HTTP: ts.Client()}

	repo, err := g.sourceRepo(context.Background(), ts.URL+" other-chart", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if repo != "someone/else" {
		t.Fatalf("resolved %q, want the entry for the chart that was named", repo)
	}
}

// A pin that has already moved past what the index still lists is normal, and a
// `sources:` field almost never changes between versions, so the newest entry
// is a far better answer than refusing to give one.
func TestAVersionTheIndexNoLongerListsFallsBackToTheNewest(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, helmIndex)
	}))
	t.Cleanup(ts.Close)
	g := &GitHubReleases{HTTP: ts.Client()}

	repo, err := g.sourceRepo(context.Background(), ts.URL+" kyverno", "99.0.0")
	if err != nil || repo != "kyverno/kyverno" {
		t.Fatalf("repo=%q err=%v", repo, err)
	}
}

// `home` is a documentation link as often as a repository, so it is a
// fallback, but for a great many charts it is the only thing set.
func TestHomeIsUsedWhenSourcesIsAbsent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, helmIndex)
	}))
	t.Cleanup(ts.Close)
	g := &GitHubReleases{HTTP: ts.Client()}

	repo, err := g.sourceRepo(context.Background(), ts.URL+" home-only", "2.0.0")
	if err != nil || repo != "someone/home-only" {
		t.Fatalf("repo=%q err=%v", repo, err)
	}
}

// A chart that declares nothing gets an honest refusal naming the chart, the
// version and the index, not a guess derived from the repository's hostname.
func TestAChartThatDeclaresNothingIsRefusedByName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, helmIndex)
	}))
	t.Cleanup(ts.Close)
	g := &GitHubReleases{HTTP: ts.Client()}

	_, err := g.sourceRepo(context.Background(), ts.URL+" bare", "9.9.9")
	if err == nil {
		t.Fatal("a chart declaring no source was resolved to something")
	}
	for _, want := range []string{"bare", "9.9.9", "index.yaml"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

// The pipeline can only give a chart name when the target declares one. Saying
// which half is missing beats a 404 on a URL nobody meant to build.
func TestAHelmRepositoryWithNoChartNameSaysSo(t *testing.T) {
	g := &GitHubReleases{}
	_, err := g.sourceRepo(context.Background(), "https://charts.example.io", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "did not name a chart") {
		t.Fatalf("err = %v", err)
	}
}

// docker.io is a website; the v2 API is at registry-1.docker.io. Asking the
// wrong one returns HTML, which surfaced as "invalid character '<'".
func TestDockerHubRefsAndHosts(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		ok       bool
	}{
		{"redis", "docker.io/library/redis", true},
		{"linuxserver/sonarr", "docker.io/linuxserver/sonarr", true},
		{"ghcr.io/org/name", "", false},
		{"localhost:5000/name", "", false},
		{"a/b/c", "", false},
		{"oci://ghcr.io/x/y", "", false},
	} {
		got, ok := dockerHubRef(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("dockerHubRef(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if registryHost("docker.io") != "registry-1.docker.io" {
		t.Error("docker.io was not mapped to the host that serves the v2 API")
	}
	if registryHost("ghcr.io") != "ghcr.io" {
		t.Error("a normal registry host was rewritten")
	}
}
