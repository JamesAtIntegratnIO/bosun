package gate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// A subprocess that prints a credential does not get to publish it.
//
// The derived rule in subprocess_stderr_test.go proves redact.Text is called
// here; this proves the call does something, with a real subprocess and a real
// primed secret. The two say different things and the syntactic one is the
// weaker: it would pass on an implementation that redacted the wrong string.
//
// The shape is not hypothetical. cmd.Env is nil at this call site, so the
// child renders with every credential this process loaded in its environment
// -- a helm plugin, a debug flag, or a chart hook that prints its environment
// puts them on stderr, and stderr is what run() quotes into the error the gate
// publishes. Stopping the inheritance is #122; this is the second line.
func TestASubprocessCannotPublishACredentialItPrinted(t *testing.T) {
	const secret = "argocd-token-must-not-be-published"

	dir := t.TempDir()
	tool := filepath.Join(dir, "renderer")
	// Exits non-zero having written the secret to stderr, which is what a
	// registry echoing a request header back would look like from here.
	script := "#!/bin/sh\necho \"error: upstream rejected " + secret + "\" >&2\nexit 1\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { redact.Prime() })
	redact.Prime(secret)

	_, err := run(context.Background(), dir, tool)
	if err == nil {
		t.Fatal("the tool exits non-zero, so this must be an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the credential reached the gate's error with nothing removed: %v", err)
	}
	if !strings.Contains(err.Error(), redact.Marker) {
		t.Errorf("nothing was redacted in %q; this error no longer carries what the tool "+
			"wrote, so it is not the text this test set out to check", err)
	}
}

// And an unprimed process still reports what the tool said, which is what
// keeps this safe on a path no configuration has run through.
func TestAnUnprimedGateStillReportsWhatTheToolSaid(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "renderer")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\necho 'chart is malformed' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	redact.Prime()

	_, err := run(context.Background(), dir, tool)
	if err == nil {
		t.Fatal("the tool exits non-zero, so this must be an error")
	}
	if !strings.Contains(err.Error(), "chart is malformed") {
		t.Errorf("an unprimed redactor must remove nothing, and this lost the tool's own "+
			"words, which are the whole diagnostic: %v", err)
	}
}
