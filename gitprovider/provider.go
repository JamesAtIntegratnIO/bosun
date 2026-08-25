// Package gitprovider is the git-host seam.
//
// Provider is deliberately small: it carries what the triage workflow actually
// needs and nothing more, so adding a host is one implementation with no
// change to the triage logic -- which is the whole reason the judgement half
// lives in the cluster rather than in a CI system's native syntax.
//
// The methods group into four jobs: read a pull request, read what has been
// said on it, say something, and push a fix. The count is deliberately not
// stated here -- it said "four" while the interface declared ten, and a
// package doc that can be checked against the code below it and lose is worse
// than one that does not try.
package gitprovider

import (
	"context"
	"time"
)

// PullRequest is the subset of a pull request the agent reasons about.
type PullRequest struct {
	Number int
	Title  string
	Body   string
	Branch string
	// BaseBranch is the branch the pull request merges into. The in-cluster
	// gate fetches it by NAME and renders its current tip -- which is what a
	// merge would actually land on, and does not require the host to serve
	// arbitrary commits by SHA.
	BaseBranch string
	BaseSHA    string
	HeadSHA    string
	Labels     []string
	Author     string
	URL        string
	// FromFork is whether the head branch lives in a different repository.
	// The in-cluster gate renders what a pull request contains, with helm,
	// inside the cluster -- so whose content that is matters, and a branch
	// somebody outside the repository controls is a different trust decision
	// from a branch inside it.
	FromFork bool
}

// Comment is one comment on a pull request. The agent reads these because the
// gate publishes its report as one -- a comment is the only artifact surface
// every git host has, which keeps the gate's output reachable without a
// provider-specific artifacts API.
type Comment struct {
	// ID is the host's own identifier. Both implemented hosts return one and
	// both were throwing it away; it is what lets a log line name WHICH
	// comment the agent read rather than quoting it back.
	ID int64
	// Author is the account that wrote it. Load-bearing: the agent reads the
	// gate's verdict out of a comment, and a comment is something anyone with
	// write access to the pull request can forge. See Triage.GateReportAuthor.
	Author string
	Body   string
	// CreatedAt is when the host recorded it. The agent wants the NEWEST
	// report -- a gate that re-ran leaves two -- and until this existed the
	// only available proxy was the order the API happened to return, which is
	// a property of the request rather than of the comments.
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
// The agent wears two hats and they use different halves of this. Its OWN
// branded status is advisory and uses only pending and success -- everything
// that is not "still working" is a verdict, and a red advisory status would
// quietly become a second gate. The GATE status it posts in cluster mode is
// the opposite: it exists to block, failure is its whole point, and error is
// how "the gate broke" stays distinguishable from "this change is bad" --
// the same distinction the CLI draws between exit 1 and exit 2.
type CommitState string

const (
	// StatePending -- still working. Not a verdict.
	StatePending CommitState = "pending"
	// StateSuccess -- finished. The description says what was decided,
	// including when what was decided was "a human needs to look at this".
	StateSuccess CommitState = "success"
	// StateFailure -- the gate blocks this change. Gate statuses only; the
	// agent's advisory status never reports it.
	StateFailure CommitState = "failure"
	// StateError -- the gate itself could not run. Distinct from failure for
	// the reason exit 2 is distinct from exit 1: "this change is bad" and
	// "the gate is broken" want opposite reactions.
	StateError CommitState = "error"
)

// Provider is one git host.
type Provider interface {
	// GetPullRequest reads a pull request.
	GetPullRequest(ctx context.Context, number int) (*PullRequest, error)

	// ListOpenPullRequests returns every open pull request, newest activity
	// first or in whatever order the host serves -- callers must not read
	// meaning into it. This is how the in-cluster gate discovers work: no
	// webhook to expose, no CI event to subscribe to, just the same polling
	// the agent already does for check states.
	//
	// The values carry the same fields GetPullRequest returns. Body used to be
	// the exception, left empty here and populated there, with nothing saying
	// so -- a difference between two sources of the same type is the kind a
	// caller finds by reading an empty string at runtime.
	ListOpenPullRequests(ctx context.Context) ([]PullRequest, error)

	// ListComments returns the pull request's comments, oldest last.
	//
	// EVERY comment, not the first page of them. The gate publishes its report
	// as a comment and the agent finds it by scanning this list, so a list
	// that silently stops at one hundred is a gate report that silently
	// vanishes on a busy pull request -- and the agent cannot tell that from a
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
	// This is what keeps ONE gate report per pull request. Posting a fresh
	// report per head commit left a repaired pull request carrying two
	// twenty-thousand-character comments that differed only in their verdict,
	// and a reader could not tell which was current without comparing head
	// SHAs. Editing in place was the CI adapter's behaviour and the contract
	// the marker was designed for; cluster mode dropped it by accident.
	UpdateComment(ctx context.Context, id int64, body string) error

	// SetCommitStatus publishes the agent's OWN verdict as a commit status, so
	// it lands in the same surface as the gate rather than only in a pod log.
	//
	// ADR 0004 named this as one of the four methods from the start; it went
	// unimplemented until 2026-08-23, and the cost was that every outcome which
	// did not warrant a comment -- gate green, gate absent, attempts spent --
	// left no trace on the pull request at all. A reader could not tell whether
	// the agent had run, decided nothing was needed, or never been called.
	//
	// This is NEVER a failure state, whatever the verdict. The agent is
	// advisory: a red status here would block merges and quietly turn it into
	// a second gate, which it is expressly not. The description carries the
	// meaning; the colour stays out of the way.
	//
	// It is, however, PENDING until there is a verdict. Writing success on
	// entry -- which is what this did until 2026-08-23 -- makes "still
	// thinking" and "looked, nothing to say" the same observation, which is
	// the exact failure this method was added to end. Pending on a check
	// nobody requires blocks nothing; it only stops the status claiming to be
	// finished before it is.
	SetCommitStatus(ctx context.Context, sha, name string, state CommitState, description string) error

	// AddLabel adds a label. Labels carry the attempt cap, so this has to be
	// durable across restarts -- which is exactly why the cap is a label
	// rather than in-memory state.
	AddLabel(ctx context.Context, number int, label string) error

	// PushFix commits the working tree at root onto the pull request's branch.
	//
	// Never to the default branch. The agent's entire write surface is the
	// bot's own branch, and the change still has to pass the gate and the
	// merge policy to reach anywhere.
	PushFix(ctx context.Context, pr *PullRequest, root, message string) error

	// Name identifies the provider in logs.
	Name() string
}
