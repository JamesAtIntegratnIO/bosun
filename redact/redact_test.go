package redact_test

import (
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// A secret is removed wherever it appears, not only where a template put it.
//
// The helper this replaces was written for one shape, `https://u:TOKEN@host`,
// and the shapes that actually reach a log are not that one: a token quoted
// back inside a sentence, twice in one message because the host echoed the
// request line and then the header, or two different credentials in a single
// wrapped error because one client's failure was wrapped by another's.
func TestASecretGoesWhereverItAppears(t *testing.T) {
	t.Cleanup(func() { redact.Prime() })
	for _, tc := range []struct {
		name    string
		secrets []string
		in, out string
	}{
		{
			name:    "mid-string",
			secrets: []string{"s3cret"},
			in:      "fatal: https://u:s3cret@host/x.git denied",
			out:     "fatal: https://u:***@host/x.git denied",
		},
		{
			name:    "more than once",
			secrets: []string{"s3cret"},
			in:      "remote: s3cret rejected; retried with s3cret",
			out:     "remote: *** rejected; retried with ***",
		},
		{
			name:    "two credentials in one string",
			secrets: []string{"git-tok", "llm-key"},
			in:      "pushing with git-tok after llm-key expired",
			out:     "pushing with *** after *** expired",
		},
		{
			name:    "the whole string",
			secrets: []string{"s3cret"},
			in:      "s3cret",
			out:     "***",
		},
		{
			// Every credential this process holds is multi-line in one case:
			// a GitHub App private key is a PEM block.
			name:    "across lines",
			secrets: []string{"-----BEGIN\nkey\n-----END"},
			in:      "helm: parse error: -----BEGIN\nkey\n-----END is not a chart",
			out:     "helm: parse error: *** is not a chart",
		},
		{
			name:    "text holding no secret is returned as it came",
			secrets: []string{"s3cret"},
			in:      "fatal: repository not found",
			out:     "fatal: repository not found",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			redact.Prime(tc.secrets...)
			if got := redact.Text(tc.in); got != tc.out {
				t.Fatalf("Text(%q)\n got: %q\nwant: %q", tc.in, got, tc.out)
			}
		})
	}
}

// An empty secret is not a rule that matches everything.
//
// strings.ReplaceAll(s, "", m) inserts m between every rune, so a credential
// this install never configured -- and most installs configure only some of
// them -- would turn every error string in the process into confetti. That is
// worse than the leak it was meant to prevent, because it is silent: nothing
// is missing, everything is unreadable, and the cause is a value nobody set.
func TestAnUnsetSecretRedactsNothing(t *testing.T) {
	t.Cleanup(func() { redact.Prime() })
	const in = "fatal: repository not found"
	for _, name := range []string{"empty", "spaces", "a newline"} {
		var secret string
		switch name {
		case "spaces":
			secret = "   "
		case "a newline":
			secret = "\n"
		}
		t.Run(name, func(t *testing.T) {
			redact.Prime(secret)
			if got := redact.Text(in); got != in {
				t.Fatalf("an unset credential rewrote the text: %q", got)
			}
			// And the empty string itself, which is what a caller gets when
			// it redacts a message that turned out to be empty.
			if got := redact.Text(""); got != "" {
				t.Fatalf("an unset credential filled an empty string: %q", got)
			}
			// And as a per-call secret, which is the path a provider takes
			// when the field it was built with was never configured.
			if got := redact.Text(in, secret); got != in {
				t.Fatalf("an unset per-call credential rewrote the text: %q", got)
			}
		})
	}
}

// A secret that contains another is redacted whole.
//
// Two credentials sharing a prefix is not a hypothetical: an installation
// token and the App key it was minted from, or a token pasted into two values
// with one truncated. Replacing the shorter first leaves the rest of the
// longer one standing in the text, which is a partial credential published
// under a marker that says it was handled.
func TestTheLongestSecretWins(t *testing.T) {
	t.Cleanup(func() { redact.Prime() })
	redact.Prime("abc", "abcdef")
	got := redact.Text("token abcdef here")
	if strings.Contains(got, "def") {
		t.Fatalf("a longer secret was cut in half by a shorter one: %q", got)
	}
	if got != "token *** here" {
		t.Fatalf("got %q", got)
	}
}

// The process redactor is what a surface reaches for when it has no reference
// to the configuration, which is every surface: the point of priming one at
// start-up is that a call site does not have to be handed the secrets to
// remove them.
func TestTheProcessRedactorIsPrimedOnceAndReadEverywhere(t *testing.T) {
	// Unprimed is a no-op rather than a panic. A binary that has not reached
	// Prime yet -- or a test that never calls it -- still logs.
	t.Cleanup(func() { redact.Prime() })
	redact.Prime()
	if got := redact.Text("fatal: s3cret denied"); got != "fatal: s3cret denied" {
		t.Fatalf("an unprimed redactor changed the text: %q", got)
	}

	redact.Prime("s3cret", "llm-key")
	if got := redact.Text("fatal: s3cret denied"); got != "fatal: *** denied" {
		t.Fatalf("Text did not use the primed secrets: %q", got)
	}

	// And the secrets it could not have been primed with. A GitHub App mints
	// an installation token per push, so the credential that reaches git is
	// one that did not exist at start-up.
	if got := redact.Text("push failed for ghs_minted with s3cret", "ghs_minted"); got != "push failed for *** with ***" {
		t.Fatalf("Text ignored the per-call secret: %q", got)
	}

	// A per-call secret is not remembered: the next call must not be redacting
	// a token that has since been revoked and reissued to somebody else.
	if got := redact.Text("push failed for ghs_minted"); got != "push failed for ghs_minted" {
		t.Fatalf("a per-call secret outlived its call: %q", got)
	}
}

// Priming twice is the last word, so a test can restore what it changed and a
// re-primed process does not accumulate revoked credentials.
func TestPrimingReplacesRatherThanAccumulates(t *testing.T) {
	t.Cleanup(func() { redact.Prime() })
	redact.Prime("old-tok")
	redact.Prime("new-tok")
	if got := redact.Text("old-tok and new-tok"); got != "old-tok and ***" {
		t.Fatalf("got %q", got)
	}
}
