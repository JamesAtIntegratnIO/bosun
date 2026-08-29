package gitprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every mutating method was 0% covered on both adapters, so the shape of what
// the agent writes was verified only against the fake, which is written to
// the same interface and so agrees by construction.

type recorded struct {
	Method string
	Path   string
	Body   map[string]any
}

// recorder answers everything with 200 and remembers what it was sent.
func recorder(t *testing.T, respond func(path string) string) (*httptest.Server, *[]recorded) {
	t.Helper()
	var seen []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		seen = append(seen, recorded{Method: r.Method, Path: r.URL.Path, Body: body})
		w.Header().Set("Content-Type", "application/json")
		if respond != nil {
			if s := respond(r.URL.Path); s != "" {
				_, _ = w.Write([]byte(s))
				return
			}
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestCommentAndUpdateCommentHitTheRightEndpoints(t *testing.T) {
	for name, run := range map[string]func(*httptest.Server) (error, error){
		"github": func(s *httptest.Server) (error, error) {
			g := &GitHub{APIBase: s.URL, Owner: "o", Repo: "r", HTTP: s.Client()}
			return g.Comment(context.Background(), 7, "hello"),
				g.UpdateComment(context.Background(), 99, "edited")
		},
		"gitea": func(s *httptest.Server) (error, error) {
			g := &Gitea{BaseURL: s.URL, Owner: "o", Repo: "r", HTTP: s.Client()}
			return g.Comment(context.Background(), 7, "hello"),
				g.UpdateComment(context.Background(), 99, "edited")
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, seen := recorder(t, nil)
			postErr, patchErr := run(srv)
			if postErr != nil || patchErr != nil {
				t.Fatalf("post=%v patch=%v", postErr, patchErr)
			}
			if len(*seen) != 2 {
				t.Fatalf("want two calls, got %+v", *seen)
			}
			post, patch := (*seen)[0], (*seen)[1]
			if post.Method != http.MethodPost || !strings.HasSuffix(post.Path, "/issues/7/comments") {
				t.Errorf("new comment: got %s %s", post.Method, post.Path)
			}
			if post.Body["body"] != "hello" {
				t.Errorf("got %v", post.Body)
			}
			// PATCH, not a second POST: editing is how the gate avoids leaving
			// a wall of superseded verdicts on one pull request.
			if patch.Method != http.MethodPatch || !strings.HasSuffix(patch.Path, "/issues/comments/99") {
				t.Errorf("edit: got %s %s", patch.Method, patch.Path)
			}
			if patch.Body["body"] != "edited" {
				t.Errorf("got %v", patch.Body)
			}
		})
	}
}

// Both hosts reject or truncate a long description, and losing the whole
// status because a verdict was wordy would lose the most-read surface on the
// pull request.
func TestSetCommitStatusTrimsALongDescription(t *testing.T) {
	long := strings.Repeat("x", 300)
	for name, post := range map[string]func(*httptest.Server) error{
		"github": func(s *httptest.Server) error {
			g := &GitHub{APIBase: s.URL, Owner: "o", Repo: "r", HTTP: s.Client()}
			return g.SetCommitStatus(context.Background(), "c0ffee", "gate", StateSuccess, long)
		},
		"gitea": func(s *httptest.Server) error {
			g := &Gitea{BaseURL: s.URL, Owner: "o", Repo: "r", HTTP: s.Client()}
			return g.SetCommitStatus(context.Background(), "c0ffee", "gate", StateSuccess, long)
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, seen := recorder(t, nil)
			if err := post(srv); err != nil {
				t.Fatal(err)
			}
			got := (*seen)[0]
			if !strings.HasSuffix(got.Path, "/statuses/c0ffee") {
				t.Errorf("got %s", got.Path)
			}
			desc, _ := got.Body["description"].(string)
			if len(desc) != 140 || !strings.HasSuffix(desc, "...") {
				t.Errorf("want a 140-char description ending in an ellipsis, got %d chars", len(desc))
			}
			if got.Body["context"] != "gate" || got.Body["state"] != string(StateSuccess) {
				t.Errorf("got %v", got.Body)
			}
		})
	}
}

// The attempt cap is a label, so a label that silently does not attach lets
// the agent retry forever. Gitea before 1.20 answers a list of names with 200
// and no label attached, which is why the name is resolved to an ID first.
func TestGiteaAddLabelResolvesTheNameToAnID(t *testing.T) {
	srv, seen := recorder(t, func(path string) string {
		if strings.HasSuffix(path, "/labels") {
			return `[{"id":11,"name":"other"},{"id":42,"name":"bosun/attempt-1"}]`
		}
		return ""
	})
	g := &Gitea{BaseURL: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
	if err := g.AddLabel(context.Background(), 7, "bosun/attempt-1"); err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 2 {
		t.Fatalf("want a lookup and an attach, got %+v", *seen)
	}
	attach := (*seen)[1]
	ids, _ := attach.Body["labels"].([]any)
	if len(ids) != 1 || ids[0] != float64(42) {
		t.Errorf("the resolved ID must be sent, not the name: %v", attach.Body)
	}
}

func TestGitHubAddLabelSendsTheName(t *testing.T) {
	srv, seen := recorder(t, nil)
	g := &GitHub{APIBase: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
	if err := g.AddLabel(context.Background(), 7, "bosun/attempt-1"); err != nil {
		t.Fatal(err)
	}
	labels, _ := (*seen)[0].Body["labels"].([]any)
	if len(labels) != 1 || labels[0] != "bosun/attempt-1" {
		t.Errorf("got %v", (*seen)[0].Body)
	}
}

// A write that fails must fail loudly. The caller decides whether it matters;
// the adapter must not decide for it.
func TestAFailedWriteIsReturned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "resource not accessible by integration", http.StatusForbidden)
	}))
	defer srv.Close()

	gh := &GitHub{APIBase: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
	gt := &Gitea{BaseURL: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
	for name, write := range map[string]func() error{
		"github comment": func() error { return gh.Comment(context.Background(), 1, "b") },
		"github status": func() error {
			return gh.SetCommitStatus(context.Background(), "s", "n", StateSuccess, "d")
		},
		"github label":  func() error { return gh.AddLabel(context.Background(), 1, "l") },
		"gitea comment": func() error { return gt.Comment(context.Background(), 1, "b") },
		"gitea status": func() error {
			return gt.SetCommitStatus(context.Background(), "s", "n", StateSuccess, "d")
		},
		"gitea label": func() error { return gt.AddLabel(context.Background(), 1, "l") },
	} {
		if err := write(); err == nil {
			t.Errorf("%s: a 403 must not read as success", name)
		}
	}
}

func TestGitHubGetPullRequestDecodesWhatTheAgentReasonsAbout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"number":7,"title":"bump","body":"b","html_url":"http://x/7",
			"head":{"ref":"kargo/x","sha":"abc","repo":{"full_name":"other/fork"}},
			"base":{"ref":"main","sha":"def"},"user":{"login":"kargo"},
			"labels":[{"name":"bosun/attempt-1"}]}`))
	}))
	defer srv.Close()

	g := &GitHub{APIBase: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
	pr, err := g.GetPullRequest(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.Branch != "kargo/x" || pr.BaseBranch != "main" || pr.HeadSHA != "abc" {
		t.Errorf("got %+v", pr)
	}
	// The attempt cap counts these, so a lost label is an agent that retries
	// forever.
	if len(pr.Labels) != 1 || pr.Labels[0] != "bosun/attempt-1" {
		t.Errorf("got %v", pr.Labels)
	}
	// A head branch in another repository is a different trust decision, and
	// an unknown origin must not read as trusted.
	if !pr.FromFork {
		t.Error("a head in another repository is a fork")
	}
}

func TestGitHubName(t *testing.T) {
	if got := (&GitHub{}).Name(); got != "github" {
		t.Errorf("Name is the PROVIDER, never the account: got %q", got)
	}
}

// Both surfaces are consulted, check runs and legacy commit statuses, because
// a repository can use either and a gate reported through the one you did not
// look at is indistinguishable from no gate at all.
func TestGitHubCheckStatusConsultsBothSurfaces(t *testing.T) {
	for _, tc := range []struct {
		name     string
		runs     string
		runsCode int
		statuses string
		want     CheckState
		wantErr  string
	}{
		{
			name: "a completed check run wins",
			runs: `{"check_runs":[{"name":"gate","status":"completed","conclusion":"success"}]}`,
			want: CheckSuccess,
		},
		{
			name: "a failing conclusion is a failure",
			runs: `{"check_runs":[{"name":"gate","status":"completed","conclusion":"failure"}]}`,
			want: CheckFailure,
		},
		{
			name: "neutral and skipped are not failures",
			runs: `{"check_runs":[{"name":"gate","status":"completed","conclusion":"skipped"}]}`,
			want: CheckSuccess,
		},
		{
			name: "a run still going is pending",
			runs: `{"check_runs":[{"name":"gate","status":"in_progress"}]}`,
			want: CheckPending,
		},
		{
			name:     "no check run, so the legacy status answers",
			runs:     `{"check_runs":[]}`,
			statuses: `[{"context":"gate","state":"success"}]`,
			want:     CheckSuccess,
		},
		{
			name:     "check runs unreadable but statuses answer",
			runsCode: http.StatusForbidden,
			statuses: `[{"context":"gate","state":"pending"}]`,
			want:     CheckPending,
		},
		{
			name:     "neither surface names it, and one could not be read",
			runsCode: http.StatusForbidden,
			statuses: `[]`,
			want:     CheckMissing,
			// "We could not look" must not be returned as "it is not there":
			// without this, a token missing Checks:read is indistinguishable
			// from a check that has not started, and the caller polls until
			// its deadline before reporting an absent gate.
			wantErr: "reading check runs",
		},
		{
			name:     "neither surface names it, and both were readable",
			runs:     `{"check_runs":[]}`,
			statuses: `[]`,
			want:     CheckMissing,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, "check-runs") {
					if tc.runsCode != 0 {
						http.Error(w, "forbidden", tc.runsCode)
						return
					}
					_, _ = w.Write([]byte(tc.runs))
					return
				}
				_, _ = w.Write([]byte(tc.statuses))
			}))
			defer srv.Close()

			g := &GitHub{APIBase: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
			got, err := g.CheckStatus(context.Background(), "c0ffee", "gate")
			if got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("want an error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
