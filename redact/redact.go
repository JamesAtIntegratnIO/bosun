// Package redact removes this process's own credentials from text that is
// about to leave it.
//
// It owns one question: given a string bosun is about to log, post to a pull
// request, or serialize to a caller, does it contain a secret this install was
// configured with, and if so, take it out. Nothing else. It does not decide
// what is safe to publish, it does not know where the text came from, and it
// cannot make hostile text harmless -- a redacted string is a string with the
// credentials removed, not a sanitised one.
//
// # Why it exists at all
//
// The reasoning was already written down, one level lower, on the unexported
// helper each git provider called: no push embeds a credential in its remote
// URL any more, the token travels in the environment, but git quotes back
// whatever the server says and a misconfigured host can echo a credential it
// was sent. So what git prints on a failed push is not safe to forward unread.
//
// None of that is about git. helm is a subprocess whose stderr is wrapped into
// errors; the ArgoCD and Kubernetes clients turn responses into errors that
// quote request context; the model provider returns error bodies. Every one of
// them is text this process turns into something a person or another agent
// reads, and every one of them was reached through a credential. A helper each
// caller has to remember to call is one each caller can forget -- which is how
// the two git providers came to differ, gitea calling the helper and github
// inlining two ReplaceAll calls, with only one of them reviewed the last time
// the rules changed.
//
// # The process redactor
//
// Prime is called once, at start-up, with every credential the configuration
// loaded, and Text is what every surface after that calls. A call site needs no
// reference to the configuration to remove a secret from a string, which is the
// whole point: passing the secrets to the code that must not print them is how
// a control ends up with an exception in it.
//
// The ambient default is deliberate and it is the only global here. The
// alternative is threading a redactor through every constructor in the
// process, and a control that is missing wherever somebody forgot a field is
// not a control. Unprimed, it removes nothing and changes nothing, so a test
// or a tool that never calls Prime behaves exactly as it did before this
// package existed.
//
// What it is not: an outbound filter. Nothing forces a caller through Text,
// and a compile-time rule that a package cannot reach a credential at all is
// worth more than this is -- see adr/0014 and docs/safety-model.md. This is
// the second line, for the text whose contents nobody chose.
//
// Nor is it the control on what a subprocess holds. That is childenv, and it
// is a different failure: this filters what a child's output may publish,
// while a child that writes its environment to a file has published a
// credential without printing a byte.
package redact

import (
	"sort"
	"strings"
	"sync/atomic"
)

// Marker is what a secret is replaced with.
//
// Fixed, and short enough that it does not say how long what it replaced was.
// It is the marker the git providers already published into pull-request
// comments, so nothing a reader has seen before changes spelling.
const Marker = "***"

// redactor is a set of secrets and the ability to take them out of a string.
//
// Unexported, along with its constructor and its method: the package's whole
// surface is Prime, Text and Marker. One process redactor is what was asked
// for, and a second one a caller can build is a second place to point at the
// wrong secrets. The zero value and a nil *redactor both redact nothing, which
// is what makes an unprimed process safe to log from.
type redactor struct {
	// Longest first: a credential that contains another must be replaced
	// whole. Deduplicated, and never blank -- see newRedactor.
	secrets []string
}

// newRedactor builds a redactor over secrets.
//
// Blank entries are dropped rather than kept, and that is load-bearing.
// strings.ReplaceAll(s, "", Marker) inserts the marker between every rune, so
// one unset credential -- and most installs configure only some of them --
// would turn every string in the process into confetti. Whitespace-only goes
// the same way and for the same reason: a credential that is one space is not
// a credential, and using it as one rewrites every sentence that has a space
// in it.
func newRedactor(secrets ...string) *redactor {
	seen := make(map[string]bool, len(secrets))
	kept := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if strings.TrimSpace(s) == "" || seen[s] {
			continue
		}
		seen[s] = true
		kept = append(kept, s)
	}
	// Longest first. Two credentials sharing a prefix is not hypothetical --
	// an installation token and a truncated copy of one, say -- and replacing
	// the shorter first leaves the tail of the longer one standing in text
	// that now carries a marker claiming it was handled.
	sort.Slice(kept, func(i, j int) bool {
		if len(kept[i]) != len(kept[j]) {
			return len(kept[i]) > len(kept[j])
		}
		return kept[i] < kept[j]
	})
	return &redactor{secrets: kept}
}

// redact returns s with every secret replaced by Marker.
func (r *redactor) redact(s string) string {
	if r == nil || s == "" {
		return s
	}
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, Marker)
	}
	return s
}

// process is the redactor Prime installs and Text reads.
//
// An atomic pointer rather than a plain variable because Prime runs on the
// start-up goroutine and Text runs on every other one; the race detector is
// right about that even though the write happens before anything serves.
var process atomic.Pointer[redactor]

// Prime installs the process redactor, replacing whatever was there.
//
// Called once, from the composition root, with every credential the
// configuration loaded. Replacing rather than accumulating is what lets a test
// restore what it changed, and what keeps a re-primed process from carrying a
// revoked credential that has since been reissued to somebody else.
func Prime(secrets ...string) {
	process.Store(newRedactor(secrets...))
}

// Text runs s through the process redactor, plus any secrets it could not have
// been primed with.
//
// The variadic half is for credentials that do not exist at start-up. A GitHub
// App mints an installation token per push, so the credential that actually
// reaches git is not one Prime ever saw; the caller holding it names it here.
// Those are used for this call only -- remembering them would mean redacting a
// token long after it was revoked and reissued to somebody else.
func Text(s string, also ...string) string {
	r := process.Load()
	if len(also) == 0 {
		return r.redact(s)
	}
	var primed []string
	if r != nil {
		primed = r.secrets
	}
	// A fresh slice rather than appending to `also`, which is the caller's
	// backing array whenever the call site spreads a slice into it.
	combined := make([]string, 0, len(also)+len(primed))
	combined = append(combined, also...)
	combined = append(combined, primed...)
	return newRedactor(combined...).redact(s)
}
