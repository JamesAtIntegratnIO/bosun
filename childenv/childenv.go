// Package childenv is the environment this process hands to the subprocesses
// it starts.
//
// It owns one question: which of the variables in this process's own
// environment must not be in a child's, and it answers it by name. Nothing
// else. It does not decide what a child may do, it cannot make a hostile child
// harmless, and it has no opinion about any variable it was not primed with.
//
// # Why it exists at all
//
// cmd.Env is nil at almost every exec.Command in this repository, and a nil
// Env means the child gets os.Environ() verbatim. So `helm template`,
// `kustomize build` and `kubeconform` each ran with GIT_TOKEN, ARGOCD_TOKEN,
// the model API key, the promotion and MCP tokens, the GitHub App private key
// and a possibly credential-bearing GIT_REPO_URL in their environment. None of
// them has any use for one, and a chart's helm plugin is this process's child
// with this process's environment.
//
// The five call sites that did set cmd.Env made it worse rather than better,
// because every one of them spelled `append(os.Environ(), …)`: one scoped
// credential added on top of all of them.
//
// This is the first line and redact is the second. Redaction filters what a
// child's output may publish; it does nothing about a child that writes its
// environment to a file, sends it somewhere, or is itself hostile. See
// envSecret's own comment in config.go, which is about exactly this: what an
// environment variable is visible to, and who inherits it.
//
// # A denylist, not an allowlist
//
// This is the load-bearing choice. helm needs HOME, PATH, XDG_*, HELM_*,
// SSL_CERT_FILE, the proxy variables, TMPDIR, and whatever a self-hosted
// install has configured for its registry. An allowlist that misses one breaks
// a chart render in a deployment nobody here can see, and the symptom is a
// gate that abstains for a reason no log explains. Removing exactly this
// process's own credentials cannot fail that way: nothing downstream wants
// them.
//
// # The process environment
//
// Prime is called once, from the composition root, with every variable the
// configuration read a credential from. Environ is what every call site after
// that builds its cmd.Env from. A call site needs no reference to the
// configuration to keep a credential out of a child, which is the whole point:
// passing the credential names to the code that must not pass them on is how a
// control ends up with an exception in it.
//
// The ambient default is deliberate and it is the only global here, for the
// same reason redact's is. Unprimed, Environ is os.Environ() and nothing
// changes, so a test or a tool that never calls Prime behaves exactly as it
// did before this package existed -- and a test process holds none of this
// install's credentials to begin with.
//
// # Once, or per call
//
// The names are decided once, because the set of variables a credential was
// read from is fixed the moment the configuration is loaded. The environment
// is read per call, because it is not: os.Setenv exists, tests set PATH to put
// a shim in front of git, and a snapshot taken at start-up would hand every
// child a frozen copy of the environment as it was before any of that. Reading
// os.Environ() per subprocess costs one slice of the process environment,
// which is what starting a process costs anyway.
package childenv

import (
	"os"
	"strings"
	"sync/atomic"
)

// stripper is a set of variable names and the ability to leave them out of an
// environment.
//
// Unexported, along with its constructor: the package's whole surface is
// Prime, Environ and With. One process environment is what was asked for, and
// a second one a caller can build is a second place to strip the wrong names.
// A nil *stripper strips nothing, which is what makes an unprimed process safe
// to start a subprocess from.
type stripper struct{ names map[string]bool }

// newStripper builds a stripper over names.
//
// Blank entries are dropped. An empty name matches the key of no environment
// entry, so keeping one changes nothing, but it does make len(names) lie about
// how many variables this will actually remove -- and that number is what
// Environ uses to decide whether it has any work to do.
func newStripper(names ...string) *stripper {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		if strings.TrimSpace(n) == "" {
			continue
		}
		set[n] = true
	}
	return &stripper{names: set}
}

// keep reports whether an `NAME=value` entry survives.
//
// Only ever called on a stripper with names in it: Environ returns the
// environment untouched before this, and a nil check here would be a second
// answer to a question already settled, in a package whose argument is that it
// has no exceptions.
func (s *stripper) keep(entry string) bool {
	name, _, ok := strings.Cut(entry, "=")
	// An entry with no `=` is not something this can name, and dropping what
	// it cannot identify would be a filter deciding policy it was not given.
	return !ok || !s.names[name]
}

// process is the stripper Prime installs and Environ reads.
//
// An atomic pointer rather than a plain variable because Prime runs on the
// start-up goroutine and Environ runs on every other one; the race detector is
// right about that even though the write happens before anything serves.
var process atomic.Pointer[stripper]

// Prime installs the names to strip, replacing whatever was there.
//
// Called once, from the composition root, with every environment variable the
// configuration read a credential from -- in both spellings, because
// GIT_TOKEN_FILE names a path and a child that can read the path can read the
// credential. Replacing rather than accumulating is what lets a test restore
// what it changed.
func Prime(names ...string) {
	process.Store(newStripper(names...))
}

// Environ is this process's environment with its own credentials taken out,
// and is what every cmd.Env in this repository is built from.
//
// Returns a fresh slice every call, so a caller may append to what it gets
// back.
func Environ() []string {
	s := process.Load()
	all := os.Environ()
	if s == nil || len(s.names) == 0 {
		return all
	}
	kept := make([]string, 0, len(all))
	for _, entry := range all {
		if s.keep(entry) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// With is Environ plus the entries a caller adds, for the commands that do
// need a credential of their own.
//
// Environ first, so an entry the caller names wins over anything an operator
// set in the pod -- which is the order the five call sites that already
// appended to os.Environ() relied on, and the reason they cannot simply drop
// the base.
func With(extra ...string) []string {
	return append(Environ(), extra...)
}
