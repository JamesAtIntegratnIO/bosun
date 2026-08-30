package gitprovider

import (
	"context"
	"strings"
)

// The two halves of one failure: a git command that leaves a background
// process behind it, and a shallow fetch that will not survive one.
//
// `git fetch` ends by spawning `git maintenance run --auto --quiet --detach`,
// which daemonises and outlives the fetch that started it. Every checkout this
// service makes is fetched into more than once -- EnsureHead pins the commit
// under judgement, then the ladder in MergeBase deepens both sides, up to six
// fetches inside a second -- so one of those background passes is in flight
// during the next fetch by construction, not by bad luck.
//
// A pass that decides to repack rewrites .git/shallow through a lock and a
// rename, which is a new inode even where the content is unchanged. Meanwhile
// a shallow fetch reads .git/shallow while building its request, negotiates
// with the host, and re-stats the file before taking the lock; git compares
// inode, size and mtime, so a file that moved in between is
//
//	fatal: shallow file has changed since we read it
//
// and the fetch dies with 128. Nothing is damaged -- git is refusing to act on
// a read it can no longer trust -- but MergeBase returns the error, and the
// gate then has no revision to diff against and declines to judge a pull
// request that was fine. It surfaced first in CI, where the checkouts are tiny
// and the load is high; a real checkout is the one where the background pass
// has enough loose objects to decide it has work to do.

// withoutBackgroundMaintenance prefixes a git invocation with the
// configuration that stops it leaving that process behind.
//
// Both keys, because which one git consults moved: a fetch used to run `git gc
// --auto` directly and now runs `git maintenance run --auto`, and the two read
// gc.auto and maintenance.auto respectively. Neither costs anything to set on
// a version that ignores it.
//
// Nothing here wants the maintenance in the first place. These checkouts are
// cloned for one pull request and removed when it has been answered, so
// repacking one buys nothing at all and costs this.
func withoutBackgroundMaintenance(args ...string) []string {
	return append([]string{
		"-c", "maintenance.auto=false",
		"-c", "gc.auto=0",
	}, args...)
}

// staleShallowRead is the sentence git dies with, matched rather than
// distinguished by exit code, because 128 is what git returns for a deleted
// branch and an unreachable host too.
//
// Matching English: git translates this message, so on a localised build a
// checkout hitting the race gets the error rather than the retry, which is
// what it got before this existed. The upstream fix is the configuration
// above; this is the net under it.
const staleShallowRead = "shallow file has changed since we read it"

// gitFetch runs one fetch, and runs it again if git refused because
// .git/shallow moved while it was reading it.
//
// Turning off this service's own background maintenance removes the writer it
// controls; it does not make the checkout the only thing on the machine. An
// operator's own gc, a clone root shared with a second run, or a git that
// learns a third name for this pass can all rewrite the file inside the same
// window. Since the error means nothing was written, a second attempt reads
// the file afresh and proceeds -- a better answer than a merge gate that
// abstains because an inode moved.
func gitFetch(ctx context.Context, dir string, args ...string) error {
	full := append([]string{"fetch", "--quiet"}, args...)
	err := gitRun(ctx, dir, full...)
	if err == nil || !strings.Contains(err.Error(), staleShallowRead) {
		return err
	}
	return gitRun(ctx, dir, full...)
}
