package gitprovider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The list and GetPullRequest return the same type, so they have to carry the
// same fields. Body was the exception, populated by one, left empty by the
// other, with nothing saying so, and a difference like that is one a caller
// finds by reading an empty string at runtime.
func TestListOpenPullRequestsCarriesTheBody(t *testing.T) {
	const listJSON = `[{"number":7,"title":"bump metallb","body":"promotion body",
		"html_url":"http://x/7","head":{"ref":"kargo/x","sha":"abc",
		"repo":{"full_name":"o/r"}},"base":{"ref":"main","sha":"def"},
		"user":{"login":"kargo"},"labels":[]}]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(listJSON))
	}))
	defer srv.Close()

	for name, list := range map[string]func() ([]PullRequest, error){
		"github": func() ([]PullRequest, error) {
			g := &GitHub{APIBase: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
			return g.ListOpenPullRequests(context.Background())
		},
		"gitea": func() ([]PullRequest, error) {
			g := &Gitea{BaseURL: srv.URL, Owner: "o", Repo: "r", HTTP: srv.Client()}
			return g.ListOpenPullRequests(context.Background())
		},
	} {
		prs, err := list()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(prs) == 0 {
			t.Fatalf("%s: no pull requests", name)
		}
		if prs[0].Body != "promotion body" {
			t.Errorf("%s: Body = %q, want the body the host served", name, prs[0].Body)
		}
	}
}
