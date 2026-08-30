package gitprovider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// origin is a real repository on disk. The whole of what these tests check is
// what git does with a shallow clone, and a fake would be a second
// implementation of the question being asked.
type origin struct {
	t   *testing.T
	dir string
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	o := &origin{t: t, dir: t.TempDir()}
	o.git("init", "--quiet", "-b", "main")
	return o
}

func (o *origin) git(args ...string) string {
	o.t.Helper()
	full := append([]string{"-C", o.dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		o.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return trimNewline(string(out))
}

func (o *origin) commit(name, body string) string {
	o.t.Helper()
	if err := os.WriteFile(filepath.Join(o.dir, name), []byte(body), 0o644); err != nil {
		o.t.Fatal(err)
	}
	o.git("add", ".")
	o.git("commit", "--quiet", "-m", name)
	return o.git("rev-parse", "HEAD")
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// clone is the shallow clone every caller starts from, which is the whole
// reason MergeBase has to fetch anything at all.
func shallowClone(t *testing.T, url, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "work")
	out, err := exec.Command("git", "clone", "--quiet", "--depth", "1", "--branch", branch, url, dir).CombinedOutput()
	if err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	return dir
}

// The answer at every distance the ladder is built for, including one past its
// first rung. A run that gave up early would report the whole intervening
// delta as this pull request's doing, which is the defect, so "it found an
// answer" is not enough: it has to be this answer.
func TestMergeBaseFindsTheBranchPoint(t *testing.T) {
	for _, behind := range []int{0, 1, deepenTo[0] + 8} {
		t.Run(fmt.Sprintf("behind-by-%d", behind), func(t *testing.T) {
			if behind > 8 && testing.Short() {
				t.Skip("builds more commits than the first deepening fetches")
			}
			o := newOrigin(t)
			branchPoint := o.commit("pin.yaml", "version: 1\n")
			o.git("checkout", "--quiet", "-b", "topic")
			o.commit("pin.yaml", "version: 2\n")

			o.git("checkout", "--quiet", "main")
			for i := 0; i < behind; i++ {
				o.commit("churn.yaml", fmt.Sprintf("n: %d\n", i))
			}

			dir := shallowClone(t, o.dir, "topic")
			got, err := MergeBase(context.Background(), dir, "topic", "main")
			if err != nil {
				t.Fatal(err)
			}
			if got != branchPoint {
				t.Errorf("MergeBase = %s, want the branch point %s", got, branchPoint)
			}
		})
	}
}

// Two histories with no common commit have no merge base, and the message has
// to say that rather than reporting some commit. A wrong revision here is a
// report about a change nobody made.
func TestMergeBaseSaysWhenThereIsNone(t *testing.T) {
	o := newOrigin(t)
	o.commit("a.yaml", "a\n")
	o.git("checkout", "--quiet", "--orphan", "unrelated")
	o.git("rm", "--quiet", "-rf", ".")
	o.commit("b.yaml", "b\n")

	dir := shallowClone(t, o.dir, "unrelated")
	if _, err := MergeBase(context.Background(), dir, "unrelated", "main"); err == nil {
		t.Fatal("two unrelated histories must not produce a merge base")
	}
}

// Both refs reach git's argv and both arrive in a git host's JSON. A value
// beginning with "-" is read as an option, and `--upload-pack=<command>` is
// how that becomes execution.
func TestMergeBaseRefusesAFlagShapedRef(t *testing.T) {
	for _, tc := range []struct{ head, base string }{
		{"topic", "--upload-pack=touch /tmp/pwned"},
		{"--upload-pack=touch /tmp/pwned", "main"},
		{"topic", ""},
		{"", "main"},
	} {
		if _, err := MergeBase(context.Background(), t.TempDir(), tc.head, tc.base); err == nil {
			t.Errorf("MergeBase(%q, %q) was accepted", tc.head, tc.base)
		}
	}
}
