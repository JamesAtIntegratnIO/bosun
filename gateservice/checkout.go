package gateservice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// The two working copies one gate run compares, and how they are obtained.
//
// Split from gateservice.go, which is the poll loop and the verdict, for the
// same reason gatehistory.go is: this is one question -- which two revisions
// is this pull request the difference between? -- and getting it wrong is not
// visible anywhere downstream. Every render, every diff and every finding is
// correct with respect to whatever these two directories hold.

// Compared is one gate run's two working copies and the commits they hold.
//
// One struct rather than four returns. `base, head string` beside `baseRev,
// headRev string` is four adjacent same-typed positions, and a call site that
// swaps a pair compiles, renders each revision against the other one's files
// and inverts every finding in the report.
type Compared struct {
	gate.Worktrees

	// BaseRev and HeadRev are the commits those directories are at, for the
	// report to name. Resolved rather than echoed back: the head is what the
	// clone actually holds, and the base is a commit nobody named, which is
	// exactly why it has to be said out loud.
	BaseRev, HeadRev string

	// Cleanup discards both.
	Cleanup func()
}

// checkout clones the head branch shallowly and adds a worktree at the merge
// base: the last commit this branch and its base branch share.
//
// It used to be the base branch's *tip*, on the reasoning that the tip is what
// a merge lands on. That is true and it is the wrong revision to diff against.
// The tip moves whenever anything else merges, and every commit it gained
// since this branch was cut then shows up in the comparison, backwards,
// attributed to this pull request: a two-file documentation change was
// reported as removing two HTTPRoutes, a SnippetsFilter and two Authentik
// blueprints and as downgrading an addon, because another pull request had
// added all of them an hour earlier. Nothing about the report said which
// revisions it was between, so the only way to discover that was to go and
// find the merge commit by hand.
//
// The merge base is the only revision at which the two sides differ by exactly
// this pull request. It is also stable while the head is: the outcome cache is
// keyed on the head SHA, and with the tip as the base a cached verdict went
// stale the moment anything else merged, while the cache went on serving it.
//
// Rendering the *merge result* instead -- base tip against base tip with this
// branch merged into it -- would answer a strictly better question, because it
// sees the interaction between this change and what landed beside it. It also
// makes the verdict a function of two moving revisions, so it cannot be cached
// against the head commit that branch protection reports it on, and it has to
// decide what to say when the merge does not apply. That is a different change
// from this one, which is about the report describing the pull request.
//
// The base is fetched by name, not by SHA: hosts reliably serve their
// advertised refs, and `github.event.pull_request.base.sha` was only ever CI's
// approximation of the merge base, and a stale one on any branch cut before
// the pull request was opened.
func (g *Service) checkout(ctx context.Context, pr *gitprovider.PullRequest) (*Compared, error) {
	dir, err := os.MkdirTemp(g.CloneRoot, "gate")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	head := filepath.Join(dir, "head")
	base := filepath.Join(dir, "base")

	baseRef := pr.BaseBranch
	if baseRef == "" {
		baseRef = pr.BaseSHA
	}

	if err := gitprovider.Clone(ctx, g.Remote, pr.Branch, head); err != nil {
		cleanup()
		return nil, err
	}

	// Before the base is fetched, because the head is what the verdict is
	// About. A branch clone is an approximation of a commit and the whole
	// service treats it as the commit: the outcome is cached under
	// pr.HeadSHA, the status is written to pr.HeadSHA, and a push landing in
	// this window would cache commit B's render as commit A's verdict,
	// green, published, and about something nobody rendered.
	if err := gitprovider.EnsureHead(ctx, g.Remote, head, pr.HeadSHA); err != nil {
		cleanup()
		return nil, err
	}

	// What to deepen on the head side. The branch name is not always it: if
	// the branch moved during the clone, EnsureHead has already detached onto
	// the commit under judgement, and deepening the branch would deepen the
	// history of a different commit.
	headRef := pr.HeadSHA
	if headRef == "" {
		headRef = pr.Branch
	}

	mergeBase, err := gitprovider.MergeBase(ctx, g.Remote, head, headRef, baseRef)
	if err != nil {
		cleanup()
		return nil, err
	}
	if err := gitWorktree(ctx, head, base, mergeBase); err != nil {
		cleanup()
		return nil, err
	}

	return &Compared{
		Worktrees: gate.Worktrees{Base: base, Head: head},
		BaseRev:   mergeBase,
		HeadRev:   gitprovider.HeadRevision(ctx, head),
		Cleanup:   cleanup,
	}, nil
}

// gitWorktree adds the base side beside the head clone, sharing its object
// store, which is what makes the merge base free to check out once it is known.
func gitWorktree(ctx context.Context, repo, dir, rev string) error {
	return gitRun(ctx, "-C", repo, "worktree", "add", "--quiet", "--detach", dir, rev)
}

// gitRun runs one git command with its stderr in the error, because "exit
// status 128" is the same sentence for every way this can fail.
func gitRun(ctx context.Context, args ...string) error {
	c := exec.CommandContext(ctx, "git", args...)
	var out strings.Builder
	c.Stderr = &out
	if err := c.Run(); err != nil {
		// Redacted because this runs the clone, and the URL it clones from is
		// GIT_REPO_URL -- which an operator may have written a credential into.
		return fmt.Errorf("git %s: %w: %s", strings.Join(args[:2], " "), err,
			strings.TrimSpace(redact.Text(out.String())))
	}
	return nil
}
