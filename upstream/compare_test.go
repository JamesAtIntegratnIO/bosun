package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The first HTTP-level tests in this package, and the reason they are worth
// having is the tag resolution rather than the request. Comparing the wrong two
// refs does not fail loudly -- it returns real commits from a real range that
// is not this promotion's, which reads exactly like the truth.

// registryAndAPI stands in for both hosts this resolver talks to: the registry
// it walks to read an artifact's labels, and api.github.com. One server, routed
// by path, because the code under test is a sequence of hops and testing the
// last one against a stub of the others would prove the least interesting part.
type registryAndAPI struct {
	// labels per artifact version, keyed by the tag requested.
	labels map[string]map[string]string
	// releases returned by /repos/{repo}/releases, newest first.
	releases []map[string]any
	// compares keyed by "base...head".
	compares map[string]map[string]any

	// asked records the compare ranges requested, so a test can assert WHICH
	// two refs were chosen rather than only that something came back.
	asked []string
	// rateLimit, when set, is the status every api.github.com call answers.
	rateLimit int
}

func (s *registryAndAPI) server(t *testing.T) *GitHubReleases {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/token"):
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon"})

		case strings.Contains(path, "/manifests/"):
			tag := path[strings.LastIndex(path, "/")+1:]
			// A plain single-arch manifest: config digest doubles as the tag so
			// the blob handler knows which version's labels to serve.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config": map[string]string{"digest": "sha256:" + tag},
			})

		case strings.Contains(path, "/blobs/"):
			tag := strings.TrimPrefix(path[strings.LastIndex(path, "/")+1:], "sha256:")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config": map[string]any{"Labels": s.labels[tag]},
			})

		case strings.HasSuffix(path, "/releases"):
			if s.rateLimit != 0 {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(s.rateLimit)
				return
			}
			if r.URL.Query().Get("page") != "1" {
				_ = json.NewEncoder(w).Encode([]any{})
				return
			}
			_ = json.NewEncoder(w).Encode(s.releases)

		case strings.Contains(path, "/compare/"):
			rng := path[strings.Index(path, "/compare/")+len("/compare/"):]
			s.asked = append(s.asked, rng)
			if s.rateLimit != 0 {
				w.Header().Set("X-RateLimit-Remaining", "0")
				w.WriteHeader(s.rateLimit)
				return
			}
			body, ok := s.compares[rng]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(body)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &GitHubReleases{APIBase: srv.URL, HTTP: srv.Client()}
}

// artifact points the registry hops at the test server. The host has to look
// like a host for splitRef to accept it, and the resolver builds registry URLs
// as https://, so the client is what redirects them -- which is why every test
// here uses the server's own client.
func commit(sha, msg string) map[string]any {
	return map[string]any{
		"sha": sha, "html_url": "http://x/commit/" + sha,
		"commit": map[string]any{"message": msg, "author": map[string]string{"name": "someone"}},
	}
}

func release(tag string) map[string]any {
	return map[string]any{"tag_name": tag, "name": tag, "body": "notes for " + tag}
}

// The motivating case. A chart bump from 0.5.8 to 1.0.0 whose upstream tags are
// app versions: the base is the release the repository is LEAVING, not the
// oldest one that happens to fall in range, or the commits that removed the
// thing the gate found are outside the window.
func TestTheRangeStartsAtTheReleaseBeingLeft(t *testing.T) {
	s := &registryAndAPI{
		labels: map[string]map[string]string{
			"1.0.0": {"org.opencontainers.image.source": "https://github.com/example-org/explorer"},
		},
		releases: []map[string]any{release("v1.0.0"), release("v0.9.0"), release("v0.5.8"), release("v0.5.0")},
		compares: map[string]map[string]any{
			"v0.5.8...v1.0.0": {
				"html_url": "http://x/compare", "total_commits": 42,
				"commits": []any{
					commit("aaaaaaaaaaaaaaa", "refactor: watch namespaces via config"),
					commit("bbbbbbbbbbbbbbb", "chore: bump go to 1.24"),
					commit("ccccccccccccccc", "fix: drop the ClusterRole, ship a Role"),
				},
				"files": []any{
					map[string]string{"filename": "charts/explorer/templates/clusterrole.yaml"},
					map[string]string{"filename": "README.md"},
				},
			},
		},
	}
	g := s.server(t)
	g.HTTP = registryClient(t, g)

	c, err := g.Compare(context.Background(), "ghcr.io/example-org/charts/explorer", "0.5.8", "1.0.0",
		[]string{"ClusterRole", "explorer"})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.asked) != 1 || s.asked[0] != "v0.5.8...v1.0.0" {
		t.Fatalf("compared %v, want v0.5.8...v1.0.0 -- the release being left, not the oldest in range", s.asked)
	}
	if c.Total != 42 {
		t.Errorf("Total = %d, want the API's own count", c.Total)
	}
	// Message matching, and the file that carries the answer the message does
	// not: "watch namespaces via config" never says ClusterRole.
	if len(c.Relevant) != 1 || !strings.Contains(c.Relevant[0].Message, "drop the ClusterRole") {
		t.Errorf("relevant commits = %+v", c.Relevant)
	}
	if len(c.Files) != 1 || c.Files[0] != "charts/explorer/templates/clusterrole.yaml" {
		t.Errorf("files = %v, want the template the term names and not the README", c.Files)
	}
}

// A chart version and the git tags of the project it packages are different
// namespaces. When no release lands in the promotion's range, the publisher's
// own recorded build revisions are the only refs that need no arithmetic.
func TestItFallsBackToTheRevisionsThePublisherRecorded(t *testing.T) {
	s := &registryAndAPI{
		labels: map[string]map[string]string{
			"0.5.1": {
				"org.opencontainers.image.source":   "https://github.com/example-org/explorer",
				"org.opencontainers.image.revision": "1111111111111111111111111111111111111111",
			},
			"0.5.2": {
				"org.opencontainers.image.source":   "https://github.com/example-org/explorer",
				"org.opencontainers.image.revision": "2222222222222222222222222222222222222222",
			},
		},
		// Real releases, none of them numbered anywhere near the chart.
		releases: []map[string]any{release("v1.0.0"), release("v0.9.0")},
		compares: map[string]map[string]any{
			"1111111111111111111111111111111111111111...2222222222222222222222222222222222222222": {
				"total_commits": 3,
				"commits":       []any{commit("ddddddddddddddd", "fix: remove the ClusterRole")},
			},
		},
	}
	g := s.server(t)
	g.HTTP = registryClient(t, g)

	c, err := g.Compare(context.Background(), "ghcr.io/example-org/charts/explorer", "0.5.1", "0.5.2",
		[]string{"ClusterRole"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Relevant) != 1 {
		t.Fatalf("no commits found through the revision labels: %+v (note: %s)", c.Relevant, c.Note)
	}
	if !strings.Contains(c.Note, "revisions the publisher recorded") {
		t.Errorf("the note does not say how the range was chosen: %q", c.Note)
	}
}

// Neither namespace meets the other. The honest answer is a sentence, and no
// compare call at all -- comparing two refs picked out of the wrong numbering
// would return real commits from a range that is not this promotion's.
func TestItRefusesToGuessARangeItCannotEstablish(t *testing.T) {
	s := &registryAndAPI{
		labels: map[string]map[string]string{
			"0.5.2": {"org.opencontainers.image.source": "https://github.com/example-org/explorer"},
			"0.5.1": {"org.opencontainers.image.source": "https://github.com/example-org/explorer"},
		},
		releases: []map[string]any{release("v1.0.0")},
	}
	g := s.server(t)
	g.HTTP = registryClient(t, g)

	c, err := g.Compare(context.Background(), "ghcr.io/example-org/charts/explorer", "0.5.1", "0.5.2", []string{"x"})
	if err != nil {
		t.Fatal("a range it could not establish was returned as an error rather than a sentence")
	}
	if len(s.asked) != 0 {
		t.Fatalf("it compared %v anyway", s.asked)
	}
	if c.Any() || c.Note == "" {
		t.Fatalf("no note explains the absence: %+v", c)
	}
}

// A great deal of upstream work, none of it about the finding. That is a real
// answer about a bump and it has to survive as one -- an empty section that
// simply vanished would read as "nothing was looked for".
func TestManyCommitsAndNoneRelevantIsItselfTheFinding(t *testing.T) {
	s := &registryAndAPI{
		labels: map[string]map[string]string{
			"1.0.0": {"org.opencontainers.image.source": "https://github.com/example-org/explorer"},
		},
		releases: []map[string]any{release("v1.0.0"), release("v0.5.8")},
		compares: map[string]map[string]any{
			"v0.5.8...v1.0.0": {
				"total_commits": 300,
				"commits":       []any{commit("eeeeeeeeeeeeeee", "chore: bump dependencies")},
			},
		},
	}
	g := s.server(t)
	g.HTTP = registryClient(t, g)

	c, _ := g.Compare(context.Background(), "ghcr.io/example-org/charts/explorer", "0.5.8", "1.0.0",
		[]string{"ClusterRole"})
	if c.Any() {
		t.Fatalf("a dependency bump matched a ClusterRole term: %+v", c.Relevant)
	}
	if !strings.Contains(c.Note, "none of them mentions") {
		t.Errorf("the note does not report the interesting negative: %q", c.Note)
	}
	// 300 is past what one answer carries, so the filter ran over a partial
	// list and has to say so rather than report "nothing mentions this".
	if !c.Truncated {
		t.Error("a range larger than one API answer was reported as fully read")
	}
}

// Rate limiting is a credential problem with a fix. Reporting it as "could not
// read the releases" sends a reader to check whether the project publishes any.
func TestRateLimitingSaysSoAndDoesNotRetry(t *testing.T) {
	s := &registryAndAPI{
		labels: map[string]map[string]string{
			"1.0.0": {"org.opencontainers.image.source": "https://github.com/example-org/explorer"},
		},
		rateLimit: http.StatusForbidden,
	}
	g := s.server(t)
	g.HTTP = registryClient(t, g)

	n, err := g.Notes(context.Background(), "ghcr.io/example-org/charts/explorer", "0.5.8", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(n.Note, "rate limited") {
		t.Errorf("the note does not name the cause: %q", n.Note)
	}
}

// The bug this fixes: the resolver was handed a static token that App mode
// leaves empty, so every upstream read went out anonymous.
func TestTheTokenSourceIsConsultedOnEveryCallAndNotJustOnce(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode([]any{})
	}))
	t.Cleanup(srv.Close)

	n := 0
	g := &GitHubReleases{APIBase: srv.URL, HTTP: srv.Client(), Token: "static",
		TokenSource: func(context.Context) (string, error) {
			n++
			return fmt.Sprintf("minted-%d", n), nil
		}}

	_, _ = g.releasePages(context.Background(), "o/r", "", 1)
	_, _ = g.releasePages(context.Background(), "o/r", "", 1)
	if len(seen) != 2 || seen[0] != "Bearer minted-1" || seen[1] != "Bearer minted-2" {
		t.Fatalf("credentials sent: %v -- an installation token lives about an hour "+
			"and has to be fetched per use", seen)
	}
}

// registryClient rewrites the https://<registry>/... URLs the OCI walk builds
// onto the test server, so the whole hop sequence runs rather than being stubbed
// out at the interesting end.
func registryClient(t *testing.T, g *GitHubReleases) *http.Client {
	t.Helper()
	base := strings.TrimPrefix(g.apiBase(), "http://")
	inner := g.HTTP
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != base {
			r = r.Clone(r.Context())
			r.URL.Scheme, r.URL.Host = "http", base
		}
		return inner.Transport.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
