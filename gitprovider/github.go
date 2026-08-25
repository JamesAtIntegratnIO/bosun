package gitprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

// GitHub implements Provider against the REST API.
//
// Deliberately hand-rolled over net/http rather than pulling a client library:
// the surface used here is five endpoints, and a vendored SDK would be by far
// the largest dependency in a service whose whole point is to be small enough
// to audit.
type GitHub struct {
	// APIBase allows GitHub Enterprise. Defaults to the public API.
	APIBase string
	Owner   string
	Repo    string
	// Token is a static credential -- a PAT, or a bot user's token.
	Token string
	// TokenSource supersedes Token when set, and is how App authentication
	// arrives: installation tokens live about an hour, so the credential has
	// to be fetched per use rather than held. See AppAuth.
	TokenSource func(ctx context.Context) (string, error)
	// AuthorName and AuthorEmail identify the agent's commits. Worth setting
	// to something recognisable -- these commits land on a bot branch and a
	// reviewer should be able to tell instantly who wrote them.
	AuthorName  string
	AuthorEmail string
	HTTP        *http.Client
}

func (g *GitHub) Name() string { return "github" }

// token resolves the credential for one call. An App's installation token is
// minted on demand and cached by AppAuth; a static token is returned as-is.
func (g *GitHub) token(ctx context.Context) (string, error) {
	if g.TokenSource != nil {
		return g.TokenSource(ctx)
	}
	return g.Token, nil
}

func (g *GitHub) base() string {
	if g.APIBase != "" {
		return strings.TrimRight(g.APIBase, "/")
	}
	return "https://api.github.com"
}

func (g *GitHub) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (g *GitHub) do(ctx context.Context, method, path string, body any, out any) error {
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
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	tok, err := g.token(ctx)
	if err != nil {
		return err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := g.client().Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s returned %d: %s", method, path, resp.StatusCode, snippet(payload))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(payload, out)
}

func (g *GitHub) repoPath(suffix string) string {
	return fmt.Sprintf("/repos/%s/%s%s", g.Owner, g.Repo, suffix)
}

func (g *GitHub) GetPullRequest(ctx context.Context, number int) (*PullRequest, error) {
	var pr struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
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
		User   struct{ Login string }  `json:"user"`
		Labels []struct{ Name string } `json:"labels"`
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

// fromFork decides whether a head repository is this one. A deleted fork
// leaves the field empty; that is still not this repository, and treating an
// unknown origin as trusted would be the wrong default for the one caller
// that asks.
func (g *GitHub) fromFork(headRepo string) bool {
	return !strings.EqualFold(headRepo, g.Owner+"/"+g.Repo)
}

// ListOpenPullRequests pages through the open pull requests. Same page bound
// as the comment walk, for the same reason: past it something is wrong, and a
// paging bug must not become a loop against somebody's API quota.
func (g *GitHub) ListOpenPullRequests(ctx context.Context) ([]PullRequest, error) {
	var out []PullRequest
	for page := 1; page <= maxCommentPages; page++ {
		var raw []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
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
			User   struct{ Login string }  `json:"user"`
			Labels []struct{ Name string } `json:"labels"`
		}
		if err := g.do(ctx, http.MethodGet, g.repoPath(fmt.Sprintf(
			"/pulls?state=open&per_page=100&page=%d", page)), nil, &raw); err != nil {
			return nil, err
		}
		for _, pr := range raw {
			p := PullRequest{
				Number: pr.Number, Title: pr.Title,
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

// maxCommentPages bounds the walk at 100 comments a page. Reaching it means a
// pull request with several thousand comments, which is not a thing this agent
// is called about -- the bound exists so a paging bug cannot become an endless
// loop against somebody's API quota, not because the limit is expected.
const maxCommentPages = 20

// ListComments returns every comment on the pull request, oldest last.
//
// PAGED, and fetched NEWEST FIRST. Both halves of that are load-bearing.
//
// This asked for one page of a hundred and returned it. On a pull request past
// that mark the gate's report was simply not in the list, and the agent --
// which finds the report by scanning it -- reported that the gate had
// published nothing. That reads as a broken gate and it is nothing of the
// sort, which is the worst kind of wrong answer: confident, plausible, and
// pointing at the wrong component.
//
// Newest first, because a bound has to truncate somewhere and the direction is
// a choice. The report the agent wants is minutes old; the comments it can
// afford to lose are the ones from last quarter. Paging forward would spend
// the whole budget on history and drop exactly the comment it came for.
func (g *GitHub) ListComments(ctx context.Context, number int) ([]Comment, error) {
	var out []Comment
	for page := 1; page <= maxCommentPages; page++ {
		var raw []struct {
			ID      int64                  `json:"id"`
			Body    string                 `json:"body"`
			User    struct{ Login string } `json:"user"`
			Created time.Time              `json:"created_at"`
		}
		if err := g.do(ctx, http.MethodGet, g.repoPath(fmt.Sprintf(
			"/issues/%d/comments?per_page=100&sort=created&direction=desc&page=%d", number, page)),
			nil, &raw); err != nil {
			return nil, err
		}
		for _, c := range raw {
			out = append(out, Comment{ID: c.ID, Author: c.User.Login, Body: c.Body, CreatedAt: c.Created})
		}
		if len(raw) < 100 {
			break
		}
	}
	// Back to oldest-last, which is the interface's contract and what every
	// caller's "the last one wins" reading depends on.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// CheckStatus reports the aggregate state of one named check.
//
// Both surfaces are consulted: check runs (GitHub Actions) and legacy commit
// statuses, because a repository can use either and a gate reported through
// the one you did not look at is indistinguishable from no gate at all.
//
// A check-runs failure is not fatal on its own -- the statuses surface is the
// whole reason there are two -- but it is carried, and returned if the second
// surface finds nothing either. Discarded, a token without Checks:read looked
// exactly like a check that had not started: waitForGate polled for the full
// GateWait and then reported an absent gate, once per pull request, with the
// actual cause never written down anywhere.
func (g *GitHub) CheckStatus(ctx context.Context, sha, checkName string) (CheckState, error) {
	var runs struct {
		CheckRuns []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	runsErr := g.do(ctx, http.MethodGet,
		g.repoPath(fmt.Sprintf("/commits/%s/check-runs?per_page=100", sha)), nil, &runs)
	if runsErr == nil {
		for _, r := range runs.CheckRuns {
			if r.Name != checkName {
				continue
			}
			if r.Status != "completed" {
				return CheckPending, nil
			}
			switch r.Conclusion {
			case "success", "neutral", "skipped":
				return CheckSuccess, nil
			default:
				return CheckFailure, nil
			}
		}
	}

	var statuses []struct {
		Context string `json:"context"`
		State   string `json:"state"`
	}
	if err := g.do(ctx, http.MethodGet,
		g.repoPath(fmt.Sprintf("/commits/%s/statuses?per_page=100", sha)), nil, &statuses); err != nil {
		return CheckMissing, err
	}
	for _, s := range statuses {
		if s.Context != checkName {
			continue
		}
		switch s.State {
		case "success":
			return CheckSuccess, nil
		case "pending":
			return CheckPending, nil
		default:
			return CheckFailure, nil
		}
	}
	// Neither surface named the check. If one of them could not be read, that
	// is why -- and "we could not look" must not be returned as "it is not
	// there".
	if runsErr != nil {
		return CheckMissing, fmt.Errorf("reading check runs for %s: %w", sha, runsErr)
	}
	return CheckMissing, nil
}

func (g *GitHub) Comment(ctx context.Context, number int, body string) error {
	return g.do(ctx, http.MethodPost,
		g.repoPath(fmt.Sprintf("/issues/%d/comments", number)),
		map[string]string{"body": body}, nil)
}

func (g *GitHub) UpdateComment(ctx context.Context, id int64, body string) error {
	return g.do(ctx, http.MethodPatch,
		g.repoPath(fmt.Sprintf("/issues/comments/%d", id)),
		map[string]string{"body": body}, nil)
}

// SetCommitStatus posts a commit status. Never a failure state, and pending
// until there is a verdict: see the interface.
//
// Needs the token's "Commit statuses" permission at read+WRITE. Read alone is
// enough to find the gate and not enough to answer beside it, and the failure
// is a 403 on a call nothing waits for -- so it is logged by the caller rather
// than allowed to fail a triage.
func (g *GitHub) SetCommitStatus(ctx context.Context, sha, name string, state CommitState, description string) error {
	// GitHub truncates descriptions at 140 characters and rejects longer ones
	// on some paths; trim rather than let a long verdict lose the whole status.
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

func (g *GitHub) AddLabel(ctx context.Context, number int, label string) error {
	return g.do(ctx, http.MethodPost,
		g.repoPath(fmt.Sprintf("/issues/%d/labels", number)),
		map[string][]string{"labels": {label}}, nil)
}

// PushFix commits and pushes the working tree onto the pull request's branch.
//
// Uses git over HTTPS with the token in the remote URL rather than the API's
// blob/tree endpoints: it is a handful of commands, it produces an ordinary
// commit, and it works identically for any host once the URL changes.
//
// The push target is always the pull request's own branch. There is no code
// path here that writes to the default branch.
func (g *GitHub) PushFix(ctx context.Context, pr *PullRequest, root, message string) error {
	if pr.Branch == "" {
		return fmt.Errorf("pull request has no head branch")
	}
	name := g.AuthorName
	if name == "" {
		name = "bosun"
	}
	email := g.AuthorEmail
	if email == "" {
		// NEVER a users.noreply.github.com address: that namespace belongs to
		// GitHub accounts, and an email in it that is not yours attributes
		// the commit -- avatar and all -- to whoever owns the name. The
		// .invalid TLD (RFC 2606) can map to nobody. App auth replaces this
		// with the bot's real identity at start-up; a token without a
		// configured author gets an honest gray nobody instead of a stranger.
		email = "bosun@noreply.invalid"
	}
	// The push needs a credential too, and for an App that means a token
	// minted now rather than one held since start-up.
	tok, err := g.token(ctx)
	if err != nil {
		return err
	}
	remote := fmt.Sprintf("https://x-access-token:%s@github.com/%s/%s.git", tok, g.Owner, g.Repo)

	steps := [][]string{
		{"git", "-C", root, "config", "user.name", name},
		{"git", "-C", root, "config", "user.email", email},
		{"git", "-C", root, "add", "-A"},
		{"git", "-C", root, "commit", "-m", message},
		{"git", "-C", root, "push", remote, "HEAD:" + pr.Branch},
	}
	for _, s := range steps {
		cmd := exec.CommandContext(ctx, s[0], s[1:]...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// Redact the token before this reaches a log or a PR comment.
			// Redact the token that was actually used, not the configured
			// one -- with an App they are different, and leaking a live
			// installation token into a pull-request comment would be a poor
			// way to learn that.
			msg := stderr.String()
			if tok != "" {
				msg = strings.ReplaceAll(msg, tok, "***")
			}
			if g.Token != "" {
				msg = strings.ReplaceAll(msg, g.Token, "***")
			}
			return fmt.Errorf("%s: %w: %s", s[1], err, snippet([]byte(msg)))
		}
	}
	return nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}
