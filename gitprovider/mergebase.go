package gitprovider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// The last commit a pull request's branch and its base branch share, found in
// a shallow clone.
//
// Here rather than in either caller because both of them ask it, and both of
// them used to answer it with the base branch's *tip*. The gate rendered the
// tip and reported everything that had merged since the branch was cut as this
// pull request's doing, in reverse; the agent diffed against the tip and
// widened `Policy.Scope` -- the guarantee that a fix cannot edit a file this
// change did not touch -- to include every file those other merges touched.
// One wrong answer, two consequences, and the second one is a safety property.
//
// The tip is not a wrong answer to a different question either. It is what a
// merge lands *on*, which is why it looked right; it is simply not the
// revision a pull request is the difference from.

// mergeBaseRef is where the base branch is pinned inside the checkout.
//
// A named ref rather than FETCH_HEAD, which the next deepening fetch would
// overwrite, leaving the answer to depend on which ref was fetched last.
const mergeBaseRef = "refs/bosun/base"

// deepenTo is how much history to fetch looking for the merge base, in order.
//
// A shallow clone knows no commit's parents, so the question has no answer at
// depth 1 even when the branch is one commit ahead of a base that never moved:
// something has to be fetched. The ladder is the compromise. 64 covers a
// branch behind by a week of merges, which is every pull request anybody
// opens; the rung above it and the unshallow behind that exist so that the one
// that is not still gets a right answer rather than a fast wrong one.
var deepenTo = []int{64, 1024}

// MergeBase is the last commit the checkout's HEAD and baseRef share.
//
// headRef is what HEAD is called on the host -- the head SHA where the caller
// has one, the branch otherwise -- and is used only to deepen the head side.
// The comparison is against HEAD itself, not against the fetched branch tip,
// because a branch that moved mid-run has already been pinned by EnsureHead
// and the answer must be about the commit under judgement.
//
// Both refs reach git's argv and both arrive in a git host's JSON, so both are
// checked first: a value beginning with "-" is read as an option, and
// `--upload-pack=...` is a command.
func MergeBase(ctx context.Context, r Remote, dir, headRef, baseRef string) (string, error) {
	for name, ref := range map[string]string{"head": headRef, "base": baseRef} {
		if ref == "" {
			return "", fmt.Errorf("no %s revision to find a merge base from", name)
		}
		if !gitRefName.MatchString(ref) {
			return "", fmt.Errorf("%s %q is not a git ref name", name, ref)
		}
	}

	// Depth 1 first, and try before deepening anything. On a checkout that is
	// not shallow -- the agent's own worktree is one on some paths -- this is
	// the whole job, and the ladder below would fetch a thousand commits to
	// learn what git already knew.
	if err := fetchBase(ctx, r, dir, baseRef, "--depth", "1"); err != nil {
		return "", err
	}
	if sha, err := gitLine(ctx, dir, "merge-base", mergeBaseRef, "HEAD"); err == nil {
		return sha, nil
	}

	var lastErr, headErr error
	for _, depth := range deepenTo {
		depthArg := fmt.Sprint(depth)
		// Best effort on the head side, and recorded rather than returned. A
		// host that will not serve this commit under this name -- a branch
		// deleted mid-run, a SHA it does not advertise -- has not made the
		// question unanswerable, because deepening the base alone reaches the
		// branch point from the other direction. Giving up here would turn a
		// host's reticence into a refusal to gate.
		if err := gitFetch(ctx, r, dir, "--depth", depthArg, "origin", headRef); err != nil {
			headErr = err
		}
		if err := fetchBase(ctx, r, dir, baseRef, "--depth", depthArg); err != nil {
			return "", err
		}
		sha, err := gitLine(ctx, dir, "merge-base", mergeBaseRef, "HEAD")
		if err == nil {
			return sha, nil
		}
		lastErr = err
	}

	// Every rung came up short, so stop guessing and take the whole history.
	// Rare enough to be worth the cost when it happens: a branch more than a
	// thousand commits behind its base is one somebody has forgotten, and the
	// wrong answer on it is a report claiming it removes the last year of work.
	if err := gitFetch(ctx, r, dir, "--unshallow", "origin"); err != nil {
		// A repository that is already complete refuses --unshallow, which is
		// not a failure to fetch: the history is there either way.
		if !strings.Contains(err.Error(), "does not make sense") {
			return "", err
		}
	}
	if err := fetchBase(ctx, r, dir, baseRef); err != nil {
		return "", err
	}
	sha, err := gitLine(ctx, dir, "merge-base", mergeBaseRef, "HEAD")
	if err != nil {
		if headErr != nil {
			return "", fmt.Errorf(
				"%s and %s share no history the host would serve, so there is no revision this pull "+
					"request is the difference from: %w (fetching %s: %v; last attempt: %v)",
				baseRef, headRef, err, headRef, headErr, lastErr)
		}
		return "", fmt.Errorf(
			"%s and %s share no history, so there is no revision this pull request is the difference from: %w (%v)",
			baseRef, headRef, err, lastErr)
	}
	return sha, nil
}

// fetchBase pins the base branch inside the checkout under a ref of this
// service's own.
//
// Two steps rather than one refspec, because the ref being fetched is a branch
// name on every host and a bare commit on a provider that reports no base
// branch, and `<sha>:refs/…` is the form a server is entitled to refuse.
// Fetching by name and naming the result afterwards asks nothing of the host
// that EnsureHead does not already ask.
func fetchBase(ctx context.Context, r Remote, dir, baseRef string, args ...string) error {
	// A fresh slice rather than appending to args, which is the caller's and
	// would be written through on any call that had spare capacity.
	fetch := append(append([]string{}, args...), "origin", baseRef)
	if err := gitFetch(ctx, r, dir, fetch...); err != nil {
		return err
	}
	return gitRun(ctx, dir, "update-ref", mergeBaseRef, "FETCH_HEAD")
}

// gitRun runs one git command in dir, with its stderr in the error, because
// "exit status 128" is the same sentence for every way this can fail.
func gitRun(ctx context.Context, dir string, args ...string) error {
	return gitEnvRun(ctx, nil, dir, args...)
}

// gitEnvRun is gitRun with an environment, for the commands that contact a
// remote and therefore need a credential.
//
// Separate rather than a variadic on gitRun, so that the local commands --
// merge-base, update-ref, rev-parse -- keep running with nothing added to
// their environment. A credential in the environment of a process that has no
// use for it is readable from /proc/<pid>/environ for as long as it runs, and
// the cheapest way not to leak one is not to hand it over.
func gitEnvRun(ctx context.Context, env []string, dir string, args ...string) error {
	full := withoutBackgroundMaintenance(append([]string{"-C", dir}, args...)...)
	cmd := exec.CommandContext(ctx, "git", full...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Redacted before it is quoted, and before snippet truncates it: the
		// ladder deepens a clone of origin, and a credential in that remote's
		// URL is one this host can echo back. See redact's package comment.
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err,
			snippet([]byte(redact.Text(stderr.String()))))
	}
	return nil
}

// gitLine runs a git command that prints one object name, and returns it.
func gitLine(ctx context.Context, dir string, args ...string) (string, error) {
	full := withoutBackgroundMaintenance(append([]string{"-C", dir}, args...)...)
	cmd := exec.CommandContext(ctx, "git", full...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err,
			snippet([]byte(redact.Text(stderr.String()))))
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("git %s: no revision", strings.Join(args, " "))
	}
	return sha, nil
}

// HeadRevision is the commit a checkout is at, or "" when git cannot say.
//
// Empty rather than an error: the only caller puts it on a report, and a run
// is not worth failing over a label it could not read.
func HeadRevision(ctx context.Context, dir string) string {
	sha, err := gitLine(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}
