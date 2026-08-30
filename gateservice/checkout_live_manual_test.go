package gateservice

import (
	"context"
	"os"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// TestLiveMergeBase resolves the base side against a real repository and a
// real open pull request, which is the one thing the fixtures cannot check:
// the deepening ladder talks to a host, and a host that refuses to serve a
// commit by name, or serves a shallow fetch differently from git's own local
// transport, produces a base revision that is wrong in exactly the way the
// fixtures are built to prove impossible.
//
// The defect was found on a branch that was behind its base. Point this at one:
//
//	PROBE_REPO=https://github.com/org/repo PROBE_BRANCH=some/branch \
//	PROBE_HEAD=<sha> PROBE_BASE=main PROBE_WANT=<merge base sha> \
//	go test ./gateservice -run LiveMergeBase -v
//
// PROBE_WANT is optional; without it the test reports what it resolved rather
// than asserting, which is what makes it useful for a pull request whose merge
// base nobody has worked out yet.
func TestLiveMergeBase(t *testing.T) {
	repo, branch := os.Getenv("PROBE_REPO"), os.Getenv("PROBE_BRANCH")
	if repo == "" || branch == "" {
		t.Skip("set PROBE_REPO/PROBE_BRANCH")
	}
	base := os.Getenv("PROBE_BASE")
	if base == "" {
		base = "main"
	}

	gs := &Service{RepoURL: repo, Log: t.Logf}
	cmp, err := gs.checkout(context.Background(), &gitprovider.PullRequest{
		Branch: branch, BaseBranch: base, HeadSHA: os.Getenv("PROBE_HEAD"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cmp.Cleanup()

	t.Logf("head %s\nbase %s", cmp.HeadRev, cmp.BaseRev)
	if want := os.Getenv("PROBE_WANT"); want != "" && cmp.BaseRev != want {
		t.Errorf("base = %s, want %s", cmp.BaseRev, want)
	}
}
