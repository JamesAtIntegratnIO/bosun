package gateservice

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// originRepo is a real repository on disk, since the whole of what these tests
// check is what git does: a fake would be a second implementation of the
// question being asked.
type originRepo struct {
	t   *testing.T
	dir string
}

func newOrigin(t *testing.T) *originRepo {
	t.Helper()
	o := &originRepo{t: t, dir: t.TempDir()}
	o.git("init", "--quiet", "-b", "main")
	return o
}

func (o *originRepo) git(args ...string) {
	o.t.Helper()
	cmd := append([]string{"-C", o.dir, "-c", "user.name=test", "-c", "user.email=test@test"}, args...)
	if out, err := execGit(cmd...); err != nil {
		o.t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func (o *originRepo) commit(name, body, msg string) string {
	o.t.Helper()
	if err := os.WriteFile(filepath.Join(o.dir, name), []byte(body), 0o644); err != nil {
		o.t.Fatal(err)
	}
	o.git("add", ".")
	o.git("commit", "--quiet", "-m", msg)
	sha, err := execGit("-C", o.dir, "rev-parse", "HEAD")
	if err != nil {
		o.t.Fatal(err)
	}
	return trimLine(sha)
}

func trimLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// The default checkout, against a real repository on disk: the head arrives
// as a clone of the pull request's branch, the base as a worktree at the
// commit the two share, reached from a base branch fetched by name, which any
// host serves, rather than by SHA, which some refuse.
func TestTwoRevisionCheckout(t *testing.T) {
	o := newOrigin(t)
	o.commit("pin.yaml", "version: 1\n", "base")
	o.git("checkout", "--quiet", "-b", "kargo/bump")
	o.commit("pin.yaml", "version: 2\n", "bump")

	gs := &Service{RepoURL: o.dir, Log: t.Logf}
	cmp, err := gs.checkout(context.Background(),
		&gitprovider.PullRequest{Branch: "kargo/bump", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer cmp.Cleanup()

	for path, want := range map[string]string{
		filepath.Join(cmp.Head, "pin.yaml"): "version: 2\n",
		filepath.Join(cmp.Base, "pin.yaml"): "version: 1\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s holds %q, want %q", path, got, want)
		}
	}
	if cmp.BaseRev == "" || cmp.HeadRev == "" || cmp.BaseRev == cmp.HeadRev {
		t.Fatalf("both revisions must be named and distinct, got base %q head %q", cmp.BaseRev, cmp.HeadRev)
	}
}

// The defect this whole file exists for. The base branch moves while a pull
// request is open -- which is the normal state of a repository anybody is
// working in -- and the base side used to follow it, so everything that landed
// on main since the branch was cut was attributed to the pull request, in
// reverse: files another change added were reported as this one removing them.
//
// The branch here touches one file and is behind by one commit that touches a
// different one. The base side must hold the branch point, so the comparison
// is over `pin.yaml` alone and `other.yaml` never appears on either side of
// it.
func TestTheBaseIsTheMergeBaseNotTheBaseBranchTip(t *testing.T) {
	o := newOrigin(t)
	branchPoint := o.commit("pin.yaml", "version: 1\n", "base")
	o.git("checkout", "--quiet", "-b", "kargo/bump")
	o.commit("pin.yaml", "version: 2\n", "bump")

	// Somebody else merges while this pull request is open.
	o.git("checkout", "--quiet", "main")
	o.commit("other.yaml", "landed: true\n", "another pull request")

	gs := &Service{RepoURL: o.dir, Log: t.Logf}
	cmp, err := gs.checkout(context.Background(),
		&gitprovider.PullRequest{Branch: "kargo/bump", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer cmp.Cleanup()

	if cmp.BaseRev != branchPoint {
		t.Errorf("base rendered at %s, want the merge base %s", cmp.BaseRev, branchPoint)
	}
	if _, err := os.Stat(filepath.Join(cmp.Base, "other.yaml")); err == nil {
		t.Error("the base side holds a file this pull request never saw, so its absence from the head " +
			"will be reported as this pull request removing it")
	}
	if got, err := os.ReadFile(filepath.Join(cmp.Base, "pin.yaml")); err != nil || string(got) != "version: 1\n" {
		t.Errorf("base pin.yaml = %q, %v; want the branch point's", got, err)
	}
}
