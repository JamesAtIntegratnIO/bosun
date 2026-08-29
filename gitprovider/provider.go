// Package gitprovider is the git-host seam.
//
// Provider is deliberately small: it carries what the triage workflow needs
// and nothing more, so adding a host is one implementation with no change to
// the triage logic, which is the whole reason the judgement half lives in the
// cluster rather than in a CI system's native syntax.
//
// The methods group into four jobs: read a pull request, read what has been
// said on it, say something, and push a fix. The count is deliberately not
// stated here; it said "four" while the interface declared ten, and a package
// doc that can be checked against the code below it and lose is worse than
// one that does not try.
package gitprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// PullRequest is the subset of a pull request the agent reasons about.
type PullRequest struct {
	Number int
	Title  string
	Body   string
	Branch string
	// BaseBranch is the branch the pull request merges into. The in-cluster
	// gate fetches it by name and renders its current tip, which is what a
	// merge would land on, and does not require the host to serve
	// arbitrary commits by SHA.
	BaseBranch string
	BaseSHA    string
	HeadSHA    string
	Labels     []string
	Author     string
	URL        string
	// FromFork is whether the head branch lives in a different repository.
	// The in-cluster gate renders what a pull request contains, with helm,
	// inside the cluster, so whose content that is matters, and a branch
	// somebody outside the repository controls is a different trust decision
	// from a branch inside it.
	FromFork bool
}

// Comment is one comment on a pull request. The agent reads these because the
// gate publishes its report as one; a comment is the only artifact surface
// every git host has, which keeps the gate's output reachable without a
// provider-specific artifacts API.
type Comment struct {
	// ID is the host's own identifier. Both implemented hosts return one and
	// both were throwing it away; it is what lets a log line name which
	// comment the agent read rather than quoting it back.
	ID int64
	// Author is the account that wrote it. Load-bearing: the gate finds its own
	// report comment to update in place, and a comment carrying its marker is
	// something anyone with write access to the pull request can write.
	Author string
	Body   string
	// CreatedAt is when the host recorded it. The agent wants the newest
	// report, a gate that re-ran leaves two, and until this existed the only
	// available proxy was the order the API happened to return, which is a
	// property of the request rather than of the comments.
	//
	// Zero when the host did not say, which is not an error: callers break
	// ties on position, the way they did before.
	CreatedAt time.Time
}

// CheckState is the aggregate state of the gate on a commit.
type CheckState string

const (
	CheckPending CheckState = "pending"
	CheckSuccess CheckState = "success"
	CheckFailure CheckState = "failure"
	CheckMissing CheckState = "missing"
)

// CommitState is the colour of a commit status.
//
// The agent wears two hats and they use different halves of this. Its own
// branded status is advisory and uses only pending and success; everything
// that is not "still working" is a verdict, and a red advisory status would
// block merges on a check nobody chose. The gate status it posts is the
// opposite: it
// exists to block, failure is its whole point, and error is how "the gate
// broke" stays distinguishable from "this change is bad", the same
// distinction the CLI draws between exit 1 and exit 2.
type CommitState string

const (
	// StatePending, still working. Not a verdict.
	StatePending CommitState = "pending"
	// StateSuccess, finished. The description says what was decided,
	// including when what was decided was "a human needs to look at this".
	StateSuccess CommitState = "success"
	// StateFailure; the gate blocks this change. Gate statuses only; the
	// agent's advisory status never reports it.
	StateFailure CommitState = "failure"
	// StateError; the gate itself could not run. Distinct from failure for
	// the reason exit 2 is distinct from exit 1: "this change is bad" and
	// "the gate is broken" want opposite reactions.
	StateError CommitState = "error"
)

// Provider is one git host.
type Provider interface {
	// GetPullRequest reads a pull request.
	GetPullRequest(ctx context.Context, number int) (*PullRequest, error)

	// ListOpenPullRequests returns every open pull request, newest activity
	// first or in whatever order the host serves; callers must not read
	// meaning into it. This is how the in-cluster gate discovers work: no
	// webhook to expose, no CI event to subscribe to, just the same polling
	// the agent already does for check states.
	//
	// The values carry the same fields GetPullRequest returns. Body used to be
	// the exception, left empty here and populated there, with nothing saying
	// so, a difference between two sources of the same type is the kind a
	// caller finds by reading an empty string at runtime.
	ListOpenPullRequests(ctx context.Context) ([]PullRequest, error)

	// ListComments returns the pull request's comments, oldest last.
	//
	// Every comment, not the first page of them. The gate publishes its report
	// as a comment and the agent finds it by scanning this list, so a list
	// that silently stops at one hundred is a gate report that silently
	// vanishes on a busy pull request, and the agent cannot tell that from a
	// gate that published nothing, which is a different situation with a
	// different answer.
	ListComments(ctx context.Context, number int) ([]Comment, error)

	// CheckStatus reports the aggregate state of the named check on a commit.
	CheckStatus(ctx context.Context, sha, checkName string) (CheckState, error)

	// Comment posts a comment.
	Comment(ctx context.Context, number int, body string) error

	// UpdateComment rewrites a comment the agent already posted, identified by
	// the ID ListComments returned.
	//
	// This is what keeps one gate report per pull request. Posting a fresh
	// report per head commit left a repaired pull request carrying two
	// twenty-thousand-character comments that differed only in their verdict,
	// and a reader could not tell which was current without comparing head
	// SHAs. Editing in place is the gate's behaviour and the contract the
	// marker was designed for.
	UpdateComment(ctx context.Context, id int64, body string) error

	// SetCommitStatus publishes the agent's own verdict as a commit status, so
	// it lands in the same surface as the gate rather than only in a pod log.
	//
	// ADR 0004 named this as one of the four methods from the start; it went
	// unimplemented until 2026-08-23, and the cost was that every outcome which
	// did not warrant a comment, gate green, gate absent, attempts spent, left
	// no trace on the pull request at all. A reader could not tell whether the
	// agent had run, decided nothing was needed, or never been called.
	//
	// This is never a failure state, whatever the verdict. The agent is
	// advisory: a red status here would block merges and quietly turn it into
	// a second gate, which it is expressly not. The description carries the
	// meaning; the colour stays out of the way.
	//
	// It is, however, pending until there is a verdict. Writing success on
	// entry, which is what this did until 2026-08-23, makes "still thinking"
	// and "looked, nothing to say" the same observation, which is the exact
	// failure this method was added to end. Pending on a check nobody
	// requires blocks nothing; it only stops the status claiming to be
	// finished before it is.
	SetCommitStatus(ctx context.Context, sha, name string, state CommitState, description string) error

	// AddLabel adds a label. Labels carry the attempt cap, so this has to be
	// durable across restarts, which is exactly why the cap is a label
	// rather than in-memory state.
	AddLabel(ctx context.Context, number int, label string) error

	// PushFix commits the working tree at root onto the pull request's branch.
	//
	// Never to the default branch. The agent's entire write surface is the
	// bot's own branch, and the change still has to pass the gate and the
	// merge policy to reach anywhere.
	//
	// On success it advances pr.HeadSHA to the commit it just pushed, because
	// that commit is the branch head now. Leaving the caller holding the
	// pre-push SHA is not a stale read, it is a wrong one: every status the
	// caller writes afterwards lands on a commit that is no longer the head,
	// so the new head carries a green gate and no verdict at all, and a
	// required check that can never be satisfied is indistinguishable from an
	// agent that died mid-run. Observed on two independent promotions before
	// it was traced here.
	PushFix(ctx context.Context, pr *PullRequest, root, message string) error

	// Name identifies the provider in logs.
	Name() string
}

// headSHA reads the commit at HEAD of the worktree at root.
//
// Shared by both providers because both push the same way: commit locally,
// then push HEAD to the pull request's branch. The commit that lands is the
// one git just made, so the worktree is the authority and no API round-trip is
// needed to learn it.
//
// Falls back to the SHA the caller already had if git cannot answer. That is
// the pre-push value and therefore wrong, but it is wrong in exactly the way
// the caller was already wrong before this existed, a status on a superseded
// commit, rather than an empty SHA, which every provider rejects outright and
// which would turn a recoverable mis-target into a hard failure.
func headSHA(ctx context.Context, root, fallback string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return fallback
	}
	if sha := strings.TrimSpace(string(out)); sha != "" {
		return sha
	}
	return fallback
}

// gitStep is one git command in a push, and the environment it needs.
//
// Shared by both providers because both run the same short list and only the
// last entry in it, the push, has any business holding a credential.
type gitStep struct {
	args []string
	env  []string
}

// pushAuthEnv hands git the push credential through the environment rather
// than the command line.
//
// Both providers used to spell the credential into the remote URL,
// https://x-access-token:<token>@host/owner/repo.git, and pass that to
// `git push` as an argument. Nothing persisted it and the error text was
// redacted, but /proc/<pid>/cmdline is world-readable: for as long as the push
// ran, a live installation token was there for `ps`, for every other process
// in the namespace, and for anything that logs a command line.
//
// `git -c http.<url>.extraHeader=...` is the obvious repair and it is the trap
// in it: -c is argv, so that moves the token from one argument to another one.
// The GIT_CONFIG_COUNT/KEY/VALUE triple sets the same config from the
// environment instead, and /proc/<pid>/environ is readable only by the process
// owner.
//
// The key is scoped to the remote's URL, never the bare `http.extraHeader`.
// An unscoped one attaches the header to whatever host git ends up talking to,
// and this header is a bearer credential for exactly one of them. A redirect
// away from that host does not carry it either: curl drops an Authorization
// header when the origin changes.
func pushAuthEnv(remote, user, token string) []string {
	cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + remote + ".extraHeader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + cred,
	}
}

// hexSHA is a git object name and nothing else. Checked before a SHA reaches
// git's argv: a value beginning with "-" would be read as an option rather
// than a revision, and these arrive from a host's JSON.
var hexSHA = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// EnsureHead pins a fresh checkout to the commit the caller believes it is
// looking at.
//
// Every checkout in this service clones a branch, `--branch <pr.Branch>`,
// while every verdict it produces is published, cached and keyed by the head
// SHA the host reported moments earlier. Those are the same commit almost
// always and not by construction: a push between reading the pull request and
// cloning it means the gate renders commit B, the agent edits commit B, and
// the result is written to commit A's status and stored in commit A's cache
// entry. A green verdict then stands against a commit nothing ever inspected,
// which is the one outcome a merge gate may not produce.
//
// Preferring the exact commit over failing: hosts that serve unadvertised
// objects let the run continue on the right revision. Where that is refused
// the run stops, because the alternative is publishing an answer about the
// wrong thing.
//
// An empty want is not an error. Some hosts, and every fake, hand back a pull
// request with no head SHA; there is nothing to pin to and nothing to betray.
func EnsureHead(ctx context.Context, dir, want string) error {
	if want == "" {
		return nil
	}
	if !hexSHA.MatchString(want) {
		return fmt.Errorf("head SHA %q is not a git object name", want)
	}
	got := headSHA(ctx, dir, "")
	if got == "" {
		return fmt.Errorf("could not read HEAD of the checkout to confirm it is %s", shortSHA(want))
	}
	if strings.EqualFold(got, want) || strings.HasPrefix(strings.ToLower(got), strings.ToLower(want)) {
		return nil
	}
	for _, args := range [][]string{
		{"-C", dir, "fetch", "--quiet", "--depth", "1", "origin", want},
		{"-C", dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf(
				"the branch moved to %s while it was being checked out, and %s could not be fetched: "+
					"refusing to answer for a commit that was not inspected: %w: %s",
				shortSHA(got), shortSHA(want), err, snippet(stderr.Bytes()))
		}
	}
	return nil
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
