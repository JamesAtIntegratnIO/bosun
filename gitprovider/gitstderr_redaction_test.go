package gitprovider

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// The structural guard in the root package proves every git command in this
// process calls redact.Text. These prove the call does something: real git,
// real stderr, a primed secret in the text git wrote, gone from the error that
// comes back.
//
// A local git will not echo a credential out of a remote URL -- 2.55 prints
// `unable to access 'https://host/o/r.git/'` with the userinfo already
// stripped -- and that is the point rather than a gap. The threat this exists
// for is a *host* quoting a credential back inside its response, which no test
// without a server can reproduce. What these do instead is put the secret
// where the real one arrives, in the remote git was pointed at, and let git
// quote it back the way a host would.
//
// Not in argv: the first draft of this file did that, and it failed for a
// reason worth keeping. gitRun and gitLine format their own arguments into the
// message beside the stderr they redact, so a secret passed as an argument
// comes back through the half that is not stderr and no redaction here would
// have caught it. Every one of these drives the remote instead.
const sentinel = "hunter2-must-not-be-published"

// badRemote points dir at an origin that does not exist and whose path carries
// the secret, which is what git quotes back when it cannot open it.
func badRemote(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "remote", "add", "origin", filepath.Join(t.TempDir(), sentinel, "o", "r.git"))
}

// EnsureHead's fetch, whose failure is the "branch moved and could not be
// fetched" refusal an operator reads.
func TestEnsureHeadRedactsWhatGitPrintsAboutTheRemote(t *testing.T) {
	dir, _ := commitRepo(t)
	badRemote(t, dir)

	t.Cleanup(func() { redact.Prime() })
	redact.Prime(sentinel)

	err := EnsureHead(context.Background(), dir, "0123456789abcdef0123456789abcdef01234567")
	if err == nil {
		t.Fatal("want a refusal: the checkout is not at that commit and no origin can serve it")
	}
	assertRedacted(t, err.Error())
}

// And the merge-base ladder, whose errors reach the gate's published report.
func TestTheMergeBaseLadderRedactsWhatGitPrints(t *testing.T) {
	dir, _ := commitRepo(t)
	badRemote(t, dir)

	t.Cleanup(func() { redact.Prime() })
	redact.Prime(sentinel)

	// gitRun, in the shape the ladder actually uses it: a deepening fetch.
	if err := gitRun(context.Background(), dir, "fetch", "--quiet", "--depth", "2", "origin"); err == nil {
		t.Fatal("want an error: that origin does not exist")
	} else {
		assertRedacted(t, err.Error())
	}

	// And gitLine, the same command shape that reads one object name back.
	if _, err := gitLine(context.Background(), dir, "ls-remote", "origin", "HEAD"); err == nil {
		t.Fatal("want an error: that origin does not exist")
	} else {
		assertRedacted(t, err.Error())
	}
}

// And an unprimed process is unchanged, which is what makes this safe on paths
// a tool or a test drives without a configuration.
func TestAnUnprimedProcessStillReportsWhatGitSaid(t *testing.T) {
	dir, _ := commitRepo(t)
	badRemote(t, dir)
	t.Cleanup(func() { redact.Prime() })
	redact.Prime()

	_, err := gitLine(context.Background(), dir, "ls-remote", "origin", "HEAD")
	if err == nil {
		t.Fatal("want an error: that origin does not exist")
	}
	if !strings.Contains(err.Error(), sentinel) {
		t.Errorf("an unprimed redactor must remove nothing, and this lost what git said: %v", err)
	}
}

func assertRedacted(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, sentinel) {
		t.Errorf("git's stderr reached the error with the secret still in it: %q", msg)
	}
	// Not merely absent -- absent because it was replaced. An error that
	// quoted no stderr at all would pass the check above while proving
	// nothing, and that is what a refactor which stops reading stderr looks
	// like from here. TestAnUnprimedProcessStillReportsWhatGitSaid is the
	// other end of the same statement: it fails if git stops naming the
	// remote, so these two cannot both pass for the wrong reason.
	if !strings.Contains(msg, redact.Marker) {
		t.Errorf("nothing was redacted in %q; this error no longer carries what git wrote, "+
			"so it is not the text this test set out to check", msg)
	}
}
