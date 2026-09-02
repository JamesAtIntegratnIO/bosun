package gitprovider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitRepo(t *testing.T) (dir, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir = t.TempDir()
	git(t, dir, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "one")
	return dir, git(t, dir, "rev-parse", "HEAD")
}

// Every checkout in this service clones a branch while every verdict it
// produces is keyed to the head SHA the host reported moments earlier. A push
// in that window means the gate renders commit B and publishes the result
// against commit A, a green verdict standing over a commit nothing inspected.
func TestEnsureHeadAcceptsTheCommitItWasPromised(t *testing.T) {
	dir, sha := commitRepo(t)
	if err := EnsureHead(context.Background(), Remote{}, dir, sha); err != nil {
		t.Fatalf("the checkout is at %s: %v", sha, err)
	}
	// Abbreviated SHAs are what a host's short form gives, and they are the
	// same commit.
	if err := EnsureHead(context.Background(), Remote{}, dir, sha[:10]); err != nil {
		t.Fatalf("abbreviated SHA: %v", err)
	}
}

func TestEnsureHeadRefusesADifferentCommit(t *testing.T) {
	dir, _ := commitRepo(t)
	// A real, well-formed object name that this checkout is not at, and that
	// no origin can serve, the branch-moved-and-cannot-be-fetched case.
	other := "0123456789abcdef0123456789abcdef01234567"
	err := EnsureHead(context.Background(), Remote{}, dir, other)
	if err == nil {
		t.Fatal("want a refusal for a commit the checkout is not at")
	}
	if !strings.Contains(err.Error(), "was not inspected") {
		t.Errorf("the refusal must say why: %v", err)
	}
}

// Hosts and fakes that report no head SHA leave nothing to pin to, and that is
// not an error; it is the absence of a promise.
func TestEnsureHeadIsANoOpWithoutASHA(t *testing.T) {
	dir, _ := commitRepo(t)
	if err := EnsureHead(context.Background(), Remote{}, dir, ""); err != nil {
		t.Fatal(err)
	}
}

// The SHA reaches git's argv. A value beginning with "-" would be read as an
// option rather than a revision, and these arrive from a host's JSON.
func TestEnsureHeadRefusesSomethingThatIsNotAnObjectName(t *testing.T) {
	dir, _ := commitRepo(t)
	for _, bad := range []string{"--upload-pack=touch /tmp/pwned", "main", "-x", "refs/heads/main"} {
		if err := EnsureHead(context.Background(), Remote{}, dir, bad); err == nil ||
			!strings.Contains(err.Error(), "not a git object name") {
			t.Errorf("%q: want the object-name refusal, got %v", bad, err)
		}
	}
}
