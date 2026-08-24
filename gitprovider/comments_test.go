package gitprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Comment paging is tested from both hosts because the two answer it
// differently and only one of the answers can be complete. GitHub can be asked
// for newest first, so its bound drops history nobody wants; Gitea cannot, so
// its bound has to be an error. A test that only covered the GitHub side would
// let the Gitea one truncate silently, which is the bug both of these exist to
// end.

// page serves `total` synthetic comments in pages of 100, honouring the
// direction the host was asked for. Bodies carry their index so a test can say
// which ones came back.
func pageHandler(t *testing.T, total int, wantParam func(*http.Request) (page int, desc bool)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, desc := wantParam(r)
		if page < 1 {
			page = 1
		}
		type c struct {
			ID      int64     `json:"id"`
			Body    string    `json:"body"`
			User    any       `json:"user"`
			Created time.Time `json:"created_at"`
		}
		base := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
		out := []c{}
		for i := 0; i < 100; i++ {
			n := (page-1)*100 + i // 0-based position in the requested order
			idx := n              // oldest-first index
			if desc {
				idx = total - 1 - n
			}
			if idx < 0 || idx >= total {
				break
			}
			out = append(out, c{
				ID:      int64(idx),
				Body:    fmt.Sprintf("comment %d", idx),
				User:    map[string]string{"login": "someone"},
				Created: base.Add(time.Duration(idx) * time.Minute),
			})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
}

func githubFor(t *testing.T, h http.Handler) *GitHub {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &GitHub{APIBase: srv.URL, Owner: "org", Repo: "repo", Token: "s3cret", HTTP: srv.Client()}
}

// The bug this replaced: 250 comments, one page read, and the gate's report --
// the newest comment on the pull request -- not in the list at all.
func TestGitHubReadsEveryCommentPastTheFirstHundred(t *testing.T) {
	const total = 250
	g := githubFor(t, pageHandler(t, total, func(r *http.Request) (int, bool) {
		if r.URL.Query().Get("direction") != "desc" {
			t.Errorf("direction = %q, want desc: the bound has to drop the OLDEST",
				r.URL.Query().Get("direction"))
		}
		var p int
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &p)
		return p, true
	}))

	got, err := g.ListComments(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Fatalf("read %d comments, want %d", len(got), total)
	}
	// Oldest last is the interface's contract, whatever order the host served.
	if got[0].Body != "comment 0" || got[total-1].Body != fmt.Sprintf("comment %d", total-1) {
		t.Fatalf("order is wrong: first %q, last %q", got[0].Body, got[total-1].Body)
	}
	if got[total-1].ID != total-1 || got[total-1].CreatedAt.IsZero() {
		t.Fatalf("id and timestamp were dropped: %+v", got[total-1])
	}
}

// A bound that truncates has to truncate the half nobody came for. Asked
// newest-first, the comments that fall off the end are the oldest -- so the
// gate's report, which is minutes old, is always still in the list.
func TestGitHubKeepsTheNewestWhenItRunsOutOfPages(t *testing.T) {
	total := maxCommentPages*100 + 500
	g := githubFor(t, pageHandler(t, total, func(r *http.Request) (int, bool) {
		var p int
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &p)
		return p, true
	}))

	got, err := g.ListComments(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxCommentPages*100 {
		t.Fatalf("read %d comments, want the %d-page bound", len(got), maxCommentPages)
	}
	if want := fmt.Sprintf("comment %d", total-1); got[len(got)-1].Body != want {
		t.Fatalf("last comment is %q, want %q -- the NEWEST must survive the bound",
			got[len(got)-1].Body, want)
	}
}

func TestGiteaReadsEveryCommentPastTheFirstHundred(t *testing.T) {
	const total = 250
	g, _ := giteaFor(t, pageHandler(t, total, func(r *http.Request) (int, bool) {
		var p int
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &p)
		return p, false
	}))

	got, err := g.ListComments(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total {
		t.Fatalf("read %d comments, want %d", len(got), total)
	}
	if got[total-1].Body != fmt.Sprintf("comment %d", total-1) {
		t.Fatalf("last comment is %q", got[total-1].Body)
	}
}

// Gitea's endpoint has no direction parameter, so the pages it can read are the
// oldest ones and the bound would drop exactly the report the agent came for.
// Saying so beats returning a list that is missing it and calling it complete.
func TestGiteaRefusesToPretendATruncatedListIsWhole(t *testing.T) {
	g, _ := giteaFor(t, pageHandler(t, maxCommentPages*100+1, func(r *http.Request) (int, bool) {
		var p int
		fmt.Sscanf(r.URL.Query().Get("page"), "%d", &p)
		return p, false
	}))

	_, err := g.ListComments(context.Background(), 1)
	if err == nil {
		t.Fatal("a list that could not reach the newest comment was returned as if it were complete")
	}
	if !strings.Contains(err.Error(), "newest") {
		t.Fatalf("the error does not say what is missing: %v", err)
	}
}
