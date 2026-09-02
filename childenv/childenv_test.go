package childenv

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// The primed names are gone and everything else is untouched.
//
// The second half is the one that matters here. This is a denylist because an
// allowlist that misses HELM_* or a proxy variable breaks a chart render in a
// deployment nobody here can see, so a bug that removed one variable too many
// would have exactly the failure mode the design was chosen to avoid.
func TestEnvironStripsTheNamesItWasPrimedWithAndNothingElse(t *testing.T) {
	t.Setenv("GIT_TOKEN", "the-configured-credential")
	t.Setenv("GIT_TOKEN_FILE", "/var/run/secrets/git-token")
	t.Setenv("HELM_REGISTRY_CONFIG", "/home/bosun/.config/helm/registry.json")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.invalid:3128")

	primeForTest(t, "GIT_TOKEN", "GIT_TOKEN_FILE")

	got := Environ()
	for _, gone := range []string{"GIT_TOKEN=", "GIT_TOKEN_FILE="} {
		if slices.ContainsFunc(got, func(e string) bool { return strings.HasPrefix(e, gone) }) {
			t.Errorf("%s survived into a child's environment", strings.TrimSuffix(gone, "="))
		}
	}
	for _, kept := range []string{
		"HELM_REGISTRY_CONFIG=/home/bosun/.config/helm/registry.json",
		"HTTPS_PROXY=http://proxy.example.invalid:3128",
	} {
		if !slices.Contains(got, kept) {
			t.Errorf("%q was removed; this is a denylist, and a chart render that cannot "+
				"reach its registry is the failure mode it exists to avoid", kept)
		}
	}
}

// A name is a name, not a prefix and not a substring.
//
// GIT_TOKEN and GIT_TOKEN_FILE are both stripped, by being named; the point of
// this is that GIT_TOKEN_HINT would not be, and neither would a variable whose
// *value* happens to contain the name of one that is.
func TestOnlyTheExactVariableNameIsStripped(t *testing.T) {
	t.Setenv("GIT_TOKEN", "the-configured-credential")
	t.Setenv("GIT_TOKEN_HINT", "read it from the file")
	t.Setenv("NOTES", "GIT_TOKEN=is what the operator called it")
	primeForTest(t, "GIT_TOKEN")

	got := Environ()
	if slices.Contains(got, "GIT_TOKEN=the-configured-credential") {
		t.Error("GIT_TOKEN survived")
	}
	for _, kept := range []string{
		"GIT_TOKEN_HINT=read it from the file",
		"NOTES=GIT_TOKEN=is what the operator called it",
	} {
		if !slices.Contains(got, kept) {
			t.Errorf("%q was removed by a rule that was given one variable name", kept)
		}
	}
}

// An unprimed process is the process as it was before this package existed.
//
// Every test binary in this repository is one, and so is any tool built from
// these packages that never loads a configuration. A control that broke those
// would be removed rather than fixed, so this is the property that keeps it
// cheap to hold.
func TestAnUnprimedProcessHandsOverItsWholeEnvironment(t *testing.T) {
	t.Setenv("GIT_TOKEN", "the-configured-credential")
	primeForTest(t)

	if got, want := Environ(), os.Environ(); !slices.Equal(got, want) {
		t.Errorf("an unprimed Environ differs from os.Environ():\n got %v\nwant %v", got, want)
	}
}

// With adds the caller's entries after the base, so the caller's win.
//
// git reads the last assignment of a repeated variable, and the five call
// sites that append a scoped credential relied on that order when they
// appended to os.Environ(). Reversing it would leave a push authenticating
// with whatever an operator had set in the pod, which fails as a rejected
// token -- the symptom of a dozen unrelated mistakes.
func TestWithPutsTheCallersEntriesLast(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "0")
	primeForTest(t, "GIT_TOKEN")

	got := With("GIT_CONFIG_COUNT=1")
	if got[len(got)-1] != "GIT_CONFIG_COUNT=1" {
		t.Fatalf("the caller's entry is not last: %v", got[len(got)-1])
	}
	if !slices.Contains(got, "GIT_CONFIG_COUNT=0") {
		t.Error("With dropped the process's own entry rather than letting the caller's win")
	}
}

// And Environ hands back a slice the caller may keep.
//
// With appends to it, and so does anything that wants one more entry. A shared
// backing array here would mean two subprocesses started from one environment
// silently editing each other's.
func TestEachCallGetsItsOwnSlice(t *testing.T) {
	t.Setenv("GIT_TOKEN", "the-configured-credential")
	primeForTest(t, "GIT_TOKEN")

	first := With("A=1")
	second := With("B=2")
	if slices.Contains(first, "B=2") {
		t.Error("a second call wrote into the first call's environment")
	}
	if !slices.Contains(second, "B=2") {
		t.Error("the second call lost its own entry")
	}
}

// An empty name would be a rule that removes nothing while making the set look
// primed, and "primed with nothing" is the path that skips the filter entirely.
func TestABlankNameIsNotAPrimedName(t *testing.T) {
	primeForTest(t, "", "   ")
	if got, want := Environ(), os.Environ(); !slices.Equal(got, want) {
		t.Error("a stripper primed only with blanks behaved as though it had names")
	}
}

// primeForTest primes the process environment and restores it afterwards.
func primeForTest(t *testing.T, names ...string) {
	t.Helper()
	old := process.Load()
	t.Cleanup(func() { process.Store(old) })
	Prime(names...)
}
