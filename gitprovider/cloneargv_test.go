package gitprovider

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

// The clone's half of the guarantee TestThePushCredentialNeverReachesArgv
// makes for the push, checked the same way and for the same reason.
//
// A credential an operator wrote into GIT_REPO_URL used to travel to git as
// part of the remote it was cloning, which is argv, which is
// /proc/<pid>/cmdline, which is world-readable. The guarantee is the absence:
// no argument of the clone contains it, and the header that replaces it
// arrives in the environment scoped to the one remote it authenticates.
func TestTheCloneCredentialNeverReachesArgv(t *testing.T) {
	const secret = "hunter2theconfiguredcredential"
	const remote = "https://git.example.com/o/r.git"

	record := shimGit(t)
	r := NewRemote("https://someone:" + secret + "@git.example.com/o/r.git")
	if err := Clone(context.Background(), r, "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	argv, env := shimLog(t, record)

	// The self-check: a shim that never ran records nothing, and every
	// assertion below then holds vacuously.
	if len(argv) == 0 {
		t.Fatal("the shim recorded nothing; Clone did not run git")
	}
	var cloned bool
	for _, line := range argv {
		if strings.Contains(line, secret) {
			t.Errorf("the credential is in a command line: %s", line)
		}
		if strings.Contains(line, " clone ") {
			cloned = true
			if !strings.Contains(line, remote) {
				t.Errorf("the clone does not name the remote: %s", line)
			}
		}
	}
	if !cloned {
		t.Fatal("no clone was run")
	}

	// And it can still authenticate, which is the half that a test asserting
	// only absence would let a broken implementation pass.
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "1",
		"GIT_CONFIG_KEY_0":   "http." + remote + ".extraHeader",
		"GIT_CONFIG_VALUE_0": "Authorization: Basic " + basicAuth("someone:"+secret),
	}
	got := gitConfigEnv(env)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// And a remote with no credential is given nothing to send, which is the
// public-repository case and by far the common one.
func TestAPublicCloneIsGivenNoAuthorizationToSend(t *testing.T) {
	record := shimGit(t)
	if err := Clone(context.Background(), NewRemote("https://github.com/o/r.git"), "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	argv, env := shimLog(t, record)
	if len(argv) == 0 {
		t.Fatal("the shim recorded nothing; Clone did not run git")
	}
	if got := gitConfigEnv(env); len(got) != 0 {
		t.Errorf("a public clone was handed an authorization header: %v", got)
	}
}

// shimLog splits the shim's log into the command lines and the environments.
func shimLog(t *testing.T, path string) (argv, env []string) {
	t.Helper()
	for _, r := range shimRuns(t, path) {
		argv = append(argv, r.argv)
		env = append(env, r.env...)
	}
	return argv, env
}

// run is one invocation of the shim: what it was called with, and what it was
// called with in its environment.
type run struct {
	argv string
	env  []string
}

// shimRuns keeps the two together, which is what a claim about *which* command
// carries a credential needs. Flattened, "the credential was in some
// environment" and "the credential was in the environment of the command that
// needed it" are the same sentence, and only one of them is the guarantee.
func shimRuns(t *testing.T, path string) []run {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var runs []run
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "argv:"):
			runs = append(runs, run{argv: strings.TrimPrefix(line, "argv:")})
		case strings.HasPrefix(line, "env: ") && len(runs) > 0:
			last := &runs[len(runs)-1]
			last.env = append(last.env, strings.TrimPrefix(line, "env: "))
		}
	}
	return runs
}

func gitConfigEnv(env []string) map[string]string {
	got := map[string]string{}
	for _, line := range env {
		if k, v, ok := strings.Cut(line, "="); ok && strings.HasPrefix(k, "GIT_CONFIG_") {
			got[k] = v
		}
	}
	return got
}

// The fetches that follow a clone still authenticate.
//
// This is the half that a test of Clone alone cannot see, and the half this
// change created the risk for. The clone now hands git a URL with no
// credential in it, so `origin` in the resulting checkout has none either --
// which is ArgoCD's arrangement and the reason it attaches credentials per
// command rather than once. Everything that fetches afterwards has to be
// given the credential explicitly, and nothing about forgetting looks wrong:
// it compiles, every other test passes, and the first symptom is a private
// repository failing to fetch in production.
//
// So: real entry points, a shim for git, and an assertion per command rather
// than over all of them. "The credential was in some environment" and "the
// credential was in the environment of the command that needed it" are the
// same sentence once the runs are flattened, and only the second is the
// guarantee.
func TestTheFetchesAfterACloneAreStillAuthenticated(t *testing.T) {
	const secret = "hunter2theconfiguredcredential"
	r := NewRemote("https://someone:" + secret + "@git.example.com/o/r.git")
	header := "Authorization: Basic " + basicAuth("someone:"+secret)

	for _, tc := range []struct {
		name string
		// the subcommand that must carry the credential
		remoteFacing string
		run          func(t *testing.T, dir string)
	}{
		{
			name:         "EnsureHead's fetch",
			remoteFacing: " fetch ",
			run: func(t *testing.T, dir string) {
				// The shim answers rev-parse with a different SHA, so the
				// checkout is never at `want` and the fetch always runs.
				_ = EnsureHead(context.Background(), r, dir, "0123456789abcdef0123456789abcdef01234567")
			},
		},
		{
			name:         "the merge-base ladder's fetches",
			remoteFacing: " fetch ",
			run: func(t *testing.T, dir string) {
				_, _ = MergeBase(context.Background(), r, dir, "topic", "main")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := shimGit(t)
			tc.run(t, t.TempDir())

			var sawRemoteFacing bool
			for _, run := range shimRuns(t, record) {
				if !strings.Contains(run.argv, tc.remoteFacing) {
					continue
				}
				sawRemoteFacing = true
				if !slices.ContainsFunc(run.env, func(e string) bool { return strings.Contains(e, header) }) {
					t.Errorf("this command talks to the host and was given no credential:\n  %s\n"+
						"origin carries none since the clone stopped embedding it, so a private "+
						"repository fails here and nowhere earlier.", run.argv)
				}
				if strings.Contains(run.argv, secret) {
					t.Errorf("the credential is in argv: %s", run.argv)
				}
			}
			// The self-check: a shim that ran nothing remote-facing leaves
			// every assertion above holding vacuously.
			if !sawRemoteFacing {
				t.Fatalf("no command containing %q was run, so this test checked nothing", tc.remoteFacing)
			}
		})
	}
}

// And the local commands are given nothing, which is the other half of
// ArgoCD's arrangement: a credential in the environment of a process that has
// no use for it is readable from /proc/<pid>/environ for as long as it runs.
func TestLocalGitCommandsAreGivenNoCredential(t *testing.T) {
	r := NewRemote("https://someone:hunter2@git.example.com/o/r.git")
	record := shimGit(t)
	_ = EnsureHead(context.Background(), r, t.TempDir(), "0123456789abcdef0123456789abcdef01234567")

	var sawLocal bool
	for _, run := range shimRuns(t, record) {
		// checkout and rev-parse reach no host.
		if !strings.Contains(run.argv, " checkout ") && !strings.Contains(run.argv, " rev-parse ") {
			continue
		}
		sawLocal = true
		if got := gitConfigEnv(run.env); len(got) != 0 {
			t.Errorf("a local command was handed a credential it cannot use: %s\n  %v", run.argv, got)
		}
	}
	if !sawLocal {
		t.Fatal("no local command was run, so this test checked nothing")
	}
}
