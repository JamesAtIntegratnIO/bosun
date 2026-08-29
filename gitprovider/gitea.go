package gitprovider

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// Gitea implements Provider against Gitea's rest API.
//
// Gitea's API is deliberately GitHub-shaped, so most of this is the same
// request against a different base path. Three places it is not,
// and each one is a silent failure rather than an error if you assume
// otherwise:
//
//   - There is no check-runs API. Everything, including Gitea Actions,
//     reports as a commit status, so CheckStatus has one surface to read
//     rather than two.
//   - Labels are attached by numeric ID on older versions, not by name.
//     Posting names against such an instance returns 200 and attaches
//     nothing, which would silently break the attempt cap.
//   - Self-hosted is the normal case, not the exception, so the instance URL
//     is required and a self-signed certificate has to be expressible.
type Gitea struct {
	// BaseURL is the instance root, https://gitea.example.com. Required:
	// unlike GitHub there is no public instance to default to.
	BaseURL string
	Owner   string
	Repo    string
	Token   string
	// Username for the push remote. Gitea accepts the token as the password
	// for any real user; the username still has to be one.
	Username string
	// AuthorName and AuthorEmail identify the agent's commits.
	AuthorName  string
	AuthorEmail string
	// InsecureSkipTLSVerify allows a self-signed certificate. Common enough
	// on self-hosted instances to be worth a value rather than a fork, and
	// it is scoped to this client; it never touches the process default.
	InsecureSkipTLSVerify bool
	HTTP                  *http.Client
}

func (g *Gitea) Name() string { return "gitea" }

func (g *Gitea) base() string {
	return strings.TrimRight(g.BaseURL, "/") + "/api/v1"
}

func (g *Gitea) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	c := &http.Client{Timeout: 30 * time.Second}
	if g.InsecureSkipTLSVerify {
		c.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	return c
}

func (g *Gitea) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if g.Token != "" {
		// Gitea's own form. It also accepts Bearer, but `token` is what its
		// documentation and its own clients use.
		req.Header.Set("Authorization", "token "+g.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.client().Do(req)
	if err != nil {
		// Wrapped, like GitHub's. The token travels in the Authorization
		// header, not the URL, so a transport error's text cannot carry it,
		// redacting here bought nothing and cost the Unwrap chain, which is
		// what errors.Is and errors.As need. Redaction belongs where the
		// credential is embedded: the push remote, in PushFix.
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, snippet(payload))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

func (g *Gitea) repoPath(suffix string) string {
	return fmt.Sprintf("/repos/%s/%s%s", url.PathEscape(g.Owner), url.PathEscape(g.Repo), suffix)
}

func (g *Gitea) GetPullRequest(ctx context.Context, number int) (*PullRequest, error) {
	var pr struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		HTML   string `json:"html_url"`
		Head   struct {
			Ref  string `json:"ref"`
			SHA  string `json:"sha"`
			Repo struct {
				FullName string `json:"full_name"`
			} `json:"repo"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		User   struct{ Login string } `json:"user"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
	}
	if err := g.do(ctx, http.MethodGet, g.repoPath(fmt.Sprintf("/pulls/%d", number)), nil, &pr); err != nil {
		return nil, err
	}
	out := &PullRequest{
		Number: pr.Number, Title: pr.Title, Body: pr.Body,
		Branch: pr.Head.Ref, BaseBranch: pr.Base.Ref, HeadSHA: pr.Head.SHA, BaseSHA: pr.Base.SHA,
		Author: pr.User.Login, URL: pr.HTML,
		FromFork: g.fromFork(pr.Head.Repo.FullName),
	}
	for _, l := range pr.Labels {
		out.Labels = append(out.Labels, l.Name)
	}
	return out, nil
}

// fromFork decides whether a head repository is this one. An empty name, a
// deleted fork, is still not this repository; unknown must not read as
// trusted.
func (g *Gitea) fromFork(headRepo string) bool {
	return !strings.EqualFold(headRepo, g.Owner+"/"+g.Repo)
}

// ListOpenPullRequests pages through the open pull requests, with the same
// page bound as the comment walk so a paging bug cannot loop forever.
func (g *Gitea) ListOpenPullRequests(ctx context.Context) ([]PullRequest, error) {
	var out []PullRequest
	for page := 1; page <= maxCommentPages; page++ {
		var raw []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Body   string `json:"body"`
			HTML   string `json:"html_url"`
			Head   struct {
				Ref  string `json:"ref"`
				SHA  string `json:"sha"`
				Repo struct {
					FullName string `json:"full_name"`
				} `json:"repo"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"base"`
			User   struct{ Login string } `json:"user"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		}
		if err := g.do(ctx, http.MethodGet, g.repoPath(fmt.Sprintf(
			"/pulls?state=open&limit=100&page=%d", page)), nil, &raw); err != nil {
			return nil, err
		}
		for _, pr := range raw {
			p := PullRequest{
				Number: pr.Number, Title: pr.Title, Body: pr.Body,
				Branch: pr.Head.Ref, BaseBranch: pr.Base.Ref, HeadSHA: pr.Head.SHA, BaseSHA: pr.Base.SHA,
				Author: pr.User.Login, URL: pr.HTML,
				FromFork: g.fromFork(pr.Head.Repo.FullName),
			}
			for _, l := range pr.Labels {
				p.Labels = append(p.Labels, l.Name)
			}
			out = append(out, p)
		}
		if len(raw) < 100 {
			break
		}
	}
	return out, nil
}

// ListComments returns every comment on the pull request, oldest last.
//
// Paged, like GitHub's, and for the same reason: a list that stops at a
// hundred is a gate report that disappears on a busy pull request, reported as
// a gate that published nothing.
//
// Paged forward, unlike GitHub's, because Gitea's issue-comments endpoint has
// no direction parameter; it returns oldest first and that is the only order
// on offer. So the bound cannot be made to truncate the half nobody needs, and
// a pull request that reaches it is an error rather than a short list. Handing
// back the oldest two thousand comments and calling it "every comment" is the
// exact silence this change exists to remove.
func (g *Gitea) ListComments(ctx context.Context, number int) ([]Comment, error) {
	var out []Comment
	for page := 1; page <= maxCommentPages; page++ {
		var raw []struct {
			ID      int64                  `json:"id"`
			Body    string                 `json:"body"`
			User    struct{ Login string } `json:"user"`
			Created time.Time              `json:"created_at"`
		}
		if err := g.do(ctx, http.MethodGet, g.repoPath(fmt.Sprintf(
			"/issues/%d/comments?limit=100&page=%d", number, page)), nil, &raw); err != nil {
			return nil, err
		}
		for _, c := range raw {
			out = append(out, Comment{ID: c.ID, Author: c.User.Login, Body: c.Body, CreatedAt: c.Created})
		}
		if len(raw) < 100 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("pull request %d has more than %d comments: this client pages oldest-first, "+
		"so the newest -- including the gate's report -- are past the end of what it read",
		number, maxCommentPages*100)
}

// CheckStatus reports the aggregate state of one named check.
//
// Gitea has no check-runs API: Actions, external CI and anything else all
// report as commit statuses, keyed by `context`. One surface, unlike GitHub's
// two, but the same failure mode if the name does not match, so a check
// nobody reported is CheckMissing rather than an error.
//
// Statuses come back newest first, and a commit carries several for one
// context as CI re-runs, so the newest is the one that counts.
//
// Position is not enough to find it. Gitea stamps whole seconds, and a gate
// that announces itself pending and then publishes a verdict routinely lands
// both inside one, at which point "newest first" has nothing to sort by and
// the order within the tie is arbitrary. Observed on the proving ground:
// `pending` and `success` both at 01:04:02, pending listed first, and a reader
// that took the first match saw a check which had gone green in seconds as
// permanently pending. Nothing failed, which is how it survived a demo whose
// writer happened to leave exactly one status per context.
//
// So ties are broken on meaning rather than on position: a settled state beats
// pending, because a verdict cannot precede the pending that announced it. A
// tie between two settled states keeps the earlier entry, which under
// newest-first is the newer one.
func (g *Gitea) CheckStatus(ctx context.Context, sha, checkName string) (CheckState, error) {
	var statuses []struct {
		Context string    `json:"context"`
		Status  string    `json:"status"`
		Created time.Time `json:"created_at"`
	}
	if err := g.do(ctx, http.MethodGet,
		g.repoPath(fmt.Sprintf("/statuses/%s?limit=100", url.PathEscape(sha))), nil, &statuses); err != nil {
		return CheckMissing, err
	}

	out := CheckMissing
	var newest time.Time
	for _, s := range statuses {
		if s.Context != checkName {
			continue
		}
		state := giteaState(s.Status)
		switch {
		case out == CheckMissing,
			s.Created.After(newest),
			s.Created.Equal(newest) && out == CheckPending && state != CheckPending:
			out, newest = state, s.Created
		}
	}
	return out, nil
}

func giteaState(s string) CheckState {
	switch s {
	case "success":
		return CheckSuccess
	case "pending":
		return CheckPending
	default:
		// `failure`, `error` and `warning` all mean do not merge.
		return CheckFailure
	}
}

func (g *Gitea) Comment(ctx context.Context, number int, body string) error {
	return g.do(ctx, http.MethodPost,
		g.repoPath(fmt.Sprintf("/issues/%d/comments", number)),
		map[string]string{"body": body}, nil)
}

func (g *Gitea) UpdateComment(ctx context.Context, id int64, body string) error {
	return g.do(ctx, http.MethodPatch,
		g.repoPath(fmt.Sprintf("/issues/comments/%d", id)),
		map[string]string{"body": body}, nil)
}

// SetCommitStatus posts a commit status. Never a failure state, and pending
// until there is a verdict: see the interface.
//
// Gitea has no check-runs API, so a status is the only way anything reports
// beside the gate here, which makes this the surface that matters most on a
// self-hosted instance, not a nicety.
func (g *Gitea) SetCommitStatus(ctx context.Context, sha, name string, state CommitState, description string) error {
	if len(description) > 140 {
		description = description[:137] + "..."
	}
	return g.do(ctx, http.MethodPost,
		g.repoPath(fmt.Sprintf("/statuses/%s", sha)),
		map[string]string{
			"state":       string(state),
			"context":     name,
			"description": description,
		}, nil)
}

// The label walk's bounds. Same shape as the comment walk and for the same
// reason: past this something is wrong with paging, and a paging bug must not
// become a loop against somebody's API quota.
const (
	labelPageSize = 100
	maxLabelPages = 20
)

// AddLabel attaches a label by resolving its name to an ID first.
//
// Gitea before 1.20 accepts only numeric IDs here and answers a list of names
// with 200 and no label attached. Since the attempt cap is a label, that
// silent no-op would let the agent retry forever. Resolving the name is one
// extra call and works on every version.
func (g *Gitea) AddLabel(ctx context.Context, number int, label string) error {
	// Paged, not the first hundred. The unpaged read made an existing label
	// past the hundredth invisible, so this fell through to creating it, the
	// create failed on the name already being taken, and AddLabel returned an
	// error, which is now what refuses the push. A repository with a busy
	// label list would have stopped every automatic repair.
	for page := 1; page <= maxLabelPages; page++ {
		var labels []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		if err := g.do(ctx, http.MethodGet,
			g.repoPath(fmt.Sprintf("/labels?limit=%d&page=%d", labelPageSize, page)), nil, &labels); err != nil {
			return fmt.Errorf("listing labels: %w", err)
		}
		for _, l := range labels {
			if l.Name == label {
				return g.do(ctx, http.MethodPost,
					g.repoPath(fmt.Sprintf("/issues/%d/labels", number)),
					map[string][]int64{"labels": {l.ID}}, nil)
			}
		}
		if len(labels) < labelPageSize {
			break
		}
	}
	// Creating it is the right move rather than an error: the agent's labels
	// are its own bookkeeping, and requiring an operator to pre-create them
	// makes the cap fail open on a fresh repository.
	var created struct {
		ID int64 `json:"id"`
	}
	if err := g.do(ctx, http.MethodPost, g.repoPath("/labels"),
		map[string]string{"name": label, "color": "ededed"}, &created); err != nil {
		return fmt.Errorf("creating label %q: %w", label, err)
	}
	return g.do(ctx, http.MethodPost,
		g.repoPath(fmt.Sprintf("/issues/%d/labels", number)),
		map[string][]int64{"labels": {created.ID}}, nil)
}

// PushFix commits and pushes the working tree onto the pull request's branch.
//
// Same shape as the GitHub implementation and for the same reason: git over
// HTTPS produces an ordinary commit and needs no blob/tree API. The push
// target is always the pull request's own branch; nothing here writes to the
// default branch.
func (g *Gitea) PushFix(ctx context.Context, pr *PullRequest, root, message string) error {
	if pr.Branch == "" {
		return fmt.Errorf("pull request has no head branch")
	}
	if g.BaseURL == "" {
		return fmt.Errorf("BaseURL is required to push to a Gitea instance")
	}
	name := g.AuthorName
	if name == "" {
		name = "bosun"
	}
	email := g.AuthorEmail
	if email == "" {
		email = "bosun@localhost"
	}
	user := g.Username
	if user == "" {
		// Gitea authenticates on the token; the username only has to be
		// present. Naming the agent keeps the reflog readable.
		user = "bosun"
	}

	u, err := url.Parse(strings.TrimRight(g.BaseURL, "/"))
	if err != nil {
		return fmt.Errorf("BaseURL %q is not a URL: %w", g.BaseURL, err)
	}
	u.User = url.UserPassword(user, g.Token)
	remote := fmt.Sprintf("%s/%s/%s.git", u.String(), g.Owner, g.Repo)

	steps := [][]string{
		{"git", "-C", root, "config", "user.name", name},
		{"git", "-C", root, "config", "user.email", email},
	}
	if g.InsecureSkipTLSVerify {
		// Scoped to this repository's config, never `--global`: the agent
		// runs in a container it shares with nothing, but a global setting
		// would still outlive the one push that needs it.
		steps = append(steps, []string{"git", "-C", root, "config", "http.sslVerify", "false"})
	}
	steps = append(steps,
		[]string{"git", "-C", root, "add", "-A"},
		[]string{"git", "-C", root, "commit", "-m", message},
		[]string{"git", "-C", root, "push", remote, "HEAD:" + pr.Branch},
	)

	for _, s := range steps {
		cmd := exec.CommandContext(ctx, s[0], s[1:]...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w: %s", s[1], err,
				snippet([]byte(redactErr(stderr.String(), g.Token))))
		}
	}
	// The branch head moved; tell the caller so its statuses land on it.
	pr.HeadSHA = headSHA(ctx, root, pr.HeadSHA)
	return nil
}
