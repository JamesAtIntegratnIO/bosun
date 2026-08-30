package gitprovider

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The shallow race turns on `git fetch` leaving a `git maintenance` running
// behind it, and the whole of the fix is a configuration key git owns. So this
// asks git rather than asserting our own argument list back to ourselves: the
// same fetch twice under GIT_TRACE, once plainly and once through
// withoutBackgroundMaintenance, and the second must start no background pass.
//
// A git that renames the key would leave MergeBase's ladder fetching against a
// process still rewriting .git/shallow, the retry in gitFetch absorbing what
// it could, and nothing anywhere saying the cause had come back.
func TestFetchLeavesNoBackgroundMaintenance(t *testing.T) {
	o := newOrigin(t)
	o.commit("pin.yaml", "version: 1\n")
	o.git("checkout", "--quiet", "-b", "topic")
	o.commit("pin.yaml", "version: 2\n")
	dir := shallowClone(t, o.dir, "topic")

	traced := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_TRACE=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return string(out)
	}

	// Nothing to suppress on a git that never started one, and saying so is
	// the honest result: the assertion below would pass for the wrong reason.
	plain := []string{"-C", dir, "fetch", "--quiet", "--depth", "2", "origin", "main"}
	if !strings.Contains(traced(plain...), "maintenance run") {
		t.Skip("this git starts no background maintenance after a fetch")
	}

	quiet := []string{"-C", dir, "fetch", "--quiet", "--depth", "3", "origin", "main"}
	if got := traced(withoutBackgroundMaintenance(quiet...)...); strings.Contains(got, "maintenance run") {
		t.Errorf("the fetch still leaves a background pass to rewrite .git/shallow under the next one:\n%s", got)
	}
}
