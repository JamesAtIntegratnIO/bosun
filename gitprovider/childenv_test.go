package gitprovider

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/childenv"
)

// Not one git command here is handed a credential this process loaded.
//
// The derived rule in the root package proves every call site builds its
// cmd.Env from childenv; this proves what that means, against the kernel. Each
// command's environment is read back from a shim, and every entry the process
// was primed to strip has to be missing from it -- including the local
// commands, which used to get a nil Env and so inherited the whole set for
// nothing.
//
// Per command rather than over all of them, for the reason the argv tests give:
// flattened, "no credential was in any environment" and "no credential was in
// the environment of the command that ran" are the same sentence, and a run
// this walk never saw would satisfy the first.
func TestNoGitCommandInheritsAProcessCredential(t *testing.T) {
	// Spelled here rather than derived, because gitprovider cannot import the
	// package that reads them and has no business knowing the list. What is
	// under test is that a primed name does not reach a child; which names get
	// primed is config.go's claim, and subprocess_env_test.go derives it there.
	const credential = "BOSUN_TEST_CREDENTIAL"
	const value = "sentinel-must-not-be-inherited"

	r := NewRemote("https://someone:hunter2@git.example.com/o/r.git")
	for _, tc := range []struct {
		name string
		run  func(t *testing.T, dir string)
	}{
		{"Clone", func(t *testing.T, dir string) {
			if err := Clone(context.Background(), r, "main", dir); err != nil {
				t.Fatal(err)
			}
		}},
		{"EnsureHead's fetch and checkout", func(t *testing.T, dir string) {
			_ = EnsureHead(context.Background(), r, dir, "0123456789abcdef0123456789abcdef01234567")
		}},
		{"the merge-base ladder", func(t *testing.T, dir string) {
			_, _ = MergeBase(context.Background(), r, dir, "topic", "main")
		}},
		{"a push", func(t *testing.T, dir string) {
			p := &GitHub{
				RepoURL: "https://github.example.com/o/r.git",
				Owner:   "o", Repo: "r", Token: "ghs_theliveinstallationtoken",
			}
			if err := p.PushFix(context.Background(), &PullRequest{Branch: "bosun/fix-1"}, dir, "a message"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(credential, value)
			primeChildenv(t, credential)
			record := shimGit(t)
			tc.run(t, t.TempDir())

			runs := shimRuns(t, record)
			// The self-check: a shim that never ran records nothing, and every
			// assertion below then holds vacuously.
			if len(runs) == 0 {
				t.Fatal("the shim recorded nothing; no git command ran")
			}
			for _, run := range runs {
				if slices.ContainsFunc(run.env, func(e string) bool { return strings.HasPrefix(e, credential+"=") }) {
					t.Errorf("this command was started holding a credential it cannot use:\n  %s\n"+
						"/proc/<pid>/environ holds it for as long as the process runs, and a git "+
						"credential helper, a hook or an alias reads it for free.", run.argv)
				}
			}
		})
	}
}

// And the environment they are given is still an environment.
//
// The failure mode a denylist is chosen to avoid is removing one variable too
// many, and it does not look like a security bug: it looks like git, or helm,
// behaving strangely in a deployment nobody here can see. PATH is the one that
// would say so loudest -- the shim is found through it -- so the assertion is
// worth making explicitly rather than resting on the shim having run.
func TestAStrippedEnvironmentIsStillAnEnvironment(t *testing.T) {
	t.Setenv("BOSUN_TEST_CREDENTIAL", "sentinel")
	// Set here rather than assumed: HOME is the variable this claim is really
	// about, and asserting on the ambient one would make the test fail in a
	// container that has none -- which says nothing about the filter.
	t.Setenv("XDG_CACHE_HOME", "/home/bosun/.cache")
	t.Setenv("HELM_REGISTRY_CONFIG", "/home/bosun/.config/helm/registry.json")
	primeChildenv(t, "BOSUN_TEST_CREDENTIAL")

	record := shimGit(t)
	if err := Clone(context.Background(), NewRemote("https://github.com/o/r.git"), "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	runs := shimRuns(t, record)
	if len(runs) == 0 {
		t.Fatal("the shim recorded nothing; Clone did not run git")
	}
	// PATH is set by shimGit, so it is this process's own; the other two are
	// set above. Nothing here rests on what the machine happened to export.
	for _, want := range []string{"PATH=", "XDG_CACHE_HOME=", "HELM_REGISTRY_CONFIG="} {
		if !slices.ContainsFunc(runs[0].env, func(e string) bool { return strings.HasPrefix(e, want) }) {
			t.Errorf("%s did not reach the child. This is a denylist for a reason: a child "+
				"missing a variable it needs fails somewhere nobody here can see, with a "+
				"symptom that says nothing about the environment.", strings.TrimSuffix(want, "="))
		}
	}
}

// And the credential a command does need still arrives.
//
// The half a test asserting only absence would let a broken implementation
// pass. Clone's own credential travels in the environment -- that is what
// keeps it out of argv -- so an environment filter that took a little too much
// would look exactly like this change working, until a private repository
// stopped cloning.
func TestTheCommandsCredentialSurvivesTheStripping(t *testing.T) {
	const secret = "hunter2theconfiguredcredential"
	const remote = "https://git.example.com/o/r.git"
	t.Setenv("BOSUN_TEST_CREDENTIAL", "sentinel")
	primeChildenv(t, "BOSUN_TEST_CREDENTIAL")

	record := shimGit(t)
	r := NewRemote("https://someone:" + secret + "@git.example.com/o/r.git")
	if err := Clone(context.Background(), r, "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	runs := shimRuns(t, record)
	if len(runs) == 0 {
		t.Fatal("the shim recorded nothing; Clone did not run git")
	}
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "1",
		"GIT_CONFIG_KEY_0":   "http." + remote + ".extraHeader",
		"GIT_CONFIG_VALUE_0": "Authorization: Basic " + basicAuth("someone:"+secret),
	}
	got := gitConfigEnv(runs[0].env)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

// primeChildenv primes the process environment and restores it afterwards.
//
// Restoring matters more here than the priming does: it is process-wide, and a
// test that left it set would silently change what every later test in this
// package hands its subprocesses.
func primeChildenv(t *testing.T, names ...string) {
	t.Helper()
	t.Cleanup(func() { childenv.Prime() })
	childenv.Prime(names...)
}
