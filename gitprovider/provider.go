// Package gitprovider is the git-host seam.
//
// Four methods, chosen because they are what the triage workflow actually
// needs and nothing more. Adding a host is one implementation of this
// interface with no change to the triage logic -- which is the whole reason
// the judgement half lives in the cluster rather than in a CI system's native
// syntax.
package gitprovider

import (
	"context"
	"time"
)

// PullRequest is the subset of a pull request the agent reasons about.
type PullRequest struct {
	Number  int
	Title   string
	Body    string
	Branch  string
	BaseSHA string
	HeadSHA string
	Labels  []string
	Author  string
	URL     string
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

// CommitState is the colour of a commit status. Two values, deliberately:
// the agent is advisory, so it never reports a failure, and everything that is
// not "still working" is a verdict.
type CommitState string

const (
	// StatePending -- triage is running. Not a verdict.
	StatePending CommitState = "pending"
	// StateSuccess -- triage finished. The description says what it decided,
	// including when what it decided was "a human needs to look at this".
	StateSuccess CommitState = "success"
)

// Provider is one git host.
type Provider interface {
	// GetPullRequest reads a pull request.
	GetPullRequest(ctx context.Context, number int) (*PullRequest, error)

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
