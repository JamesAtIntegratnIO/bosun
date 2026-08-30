package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// The token table, replayed against this listener.
//
// The same cases the promotion endpoint's own security test enumerates, and
// for the same reasons. A prefix of the token is in the table because a
// comparison that short-circuits on the first differing byte is a slow oracle
// for the rest of it; a longer near-miss is there because a comparison that
// only checked a prefix would admit it. Trailing whitespace is deliberately
// NOT a failure: a token that arrives from a mounted Secret carries the file's
// trailing newline, and an operator who copies it into a header carries it
// with them.
func TestTheListenerRefusesEveryTokenButItsOwn(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))

	for _, h := range []string{
		"",                    // absent
		"Bearer wrong",        // wrong
		"sekrit",              // right value, no Bearer prefix
		"Basic sekrit",        // wrong scheme
		"Bearer sekri",        // a prefix of it
		"Bearer sekritt",      // one byte longer
		"Bearer SEKRIT",       // right bytes, wrong case
		"Bearer  sekrit-also", // a near miss that starts the same way
	} {
		code, body := f.postWith(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, h)
		if code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: want 401, got %d: %s", h, code, body)
		}
		// And it learns nothing about the surface on the way out.
		if bytes.Contains(body, []byte("pipeline_report")) {
			t.Errorf("Authorization %q: the refusal disclosed the tool set: %s", h, body)
		}
	}

	for _, h := range []string{"Bearer sekrit", "bearer sekrit"} {
		code, body := f.postWith(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, h)
		if code != http.StatusOK {
			t.Errorf("Authorization %q must be accepted, got %d: %s", h, code, body)
		}
	}
}

// A mounted Secret's trailing newline does not lock everybody out.
//
// Asserted here rather than over HTTP because a newline is not a legal header
// value and Go's own client refuses to send one: the shape that actually
// happens is a token read from a file into the SERVER's configuration with the
// file's newline still on it, matched against a header a caller wrote by hand.
// Config already trims, and this is the second line, because the failure is a
// listener that refuses every correct caller over an invisible byte.
func TestATokenWithATrailingNewlineStillMatches(t *testing.T) {
	auth := BearerToken{Token: "sekrit\n"}
	req, err := http.NewRequest(http.MethodPost, "/mcp", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sekrit")
	if !auth.Allow(req) {
		t.Fatal("a token carrying its Secret file's trailing newline refused the caller " +
			"that presented the token itself")
	}
	req.Header.Set("Authorization", "Bearer wrong")
	if auth.Allow(req) {
		t.Fatal("trimming must not have widened the comparison")
	}
}

// The auth check is in front of everything, including the method check.
//
// An unauthenticated caller must not be able to tell a listener that
// implements a method from one that does not, because that is a map of the
// surface handed to whoever is looking for one.
func TestAnUnauthenticatedCallerLearnsNothing(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))

	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pipeline_report"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"there/is/no/such/method"}`,
		`not json at all`,
	} {
		code, out := f.postWith(t, body, "")
		if code != http.StatusUnauthorized {
			t.Errorf("%s: want 401 before anything is parsed, got %d: %s", body, code, out)
		}
	}
}

// Every response passes through the process redactor, on every path.
//
// Fault injection rather than a happy-path assertion, because the happy path
// is not where a credential appears. It appears in an error string: a
// misconfigured host echoes back a token it was sent, a client wraps the
// response into an error, the sweep records that error as a note, and the note
// is serialised to a caller outside the cluster. The primary control is that
// no result type can reach a credential by field path; this is the second
// line, for the text whose contents nobody chose.
func TestNoResponseCarriesAPrimedSecret(t *testing.T) {
	// Two shapes, and the second is the one a naive implementation misses: a
	// GitHub App private key contains newlines, which JSON escapes, so a
	// redactor comparing against the encoded body would never match it.
	const (
		flatSecret = "ghp-sentinel-must-not-be-published"
		pemSecret  = "-----BEGIN KEY-----\nsentinel-private-key-line\n-----END KEY-----"
	)
	t.Cleanup(func() { redact.Prime() })
	redact.Prime(flatSecret, pemSecret)

	// Every read fails, quoting the credential it was rejected with, which is
	// exactly the shape this exists for.
	w := wedged()
	w.kargo.stageErr = errors.New("401 from the apiserver using token " + flatSecret)
	w.kargo.warehouseErr = errors.New("tls handshake failed with key " + pemSecret)
	w.kargo.promotionErr = errors.New("refused: " + flatSecret + " and " + pemSecret)
	w.prs.ListErr = errors.New("the git host said " + flatSecret + " was revoked")

	f := newFixture(t, sweep(t, w))

	// Every method, not only the tool: an error response is a response.
	for _, body := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pipeline_report","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"no/such/method"}`,
	} {
		_, out := f.post(t, body)
		for _, secret := range []string{flatSecret, pemSecret} {
			for depth, spelling := range spellings(t, secret) {
				if bytes.Contains(out, []byte(spelling)) {
					t.Errorf("a credential reached the wire for %s, escaped %d time(s):\n%s",
						body, depth, out)
				}
			}
		}
	}

	// The self-check, and not optional. If the fixture stops producing notes
	// that quote the failures, the loop above compares a clean body against
	// two secrets and reports a pass.
	got := f.report(t)
	if got.Examined == nil || len(got.Examined.Notes) == 0 {
		t.Fatal("no note reached the response, so nothing above carried a credential to redact " +
			"and this test proved nothing. Fix the fixture rather than deleting it.")
	}
	var marked bool
	for _, n := range got.Examined.Notes {
		if strings.Contains(n.Text, redact.Marker) {
			marked = true
		}
	}
	if !marked {
		t.Fatalf("no note shows the redaction marker, so the secrets never reached the "+
			"serialiser at all: %v", got.Examined.Notes)
	}
}

// spellings is every way a secret can appear in a JSON body.
//
// Itself, then escaped once for each JSON encoding it passes through. A tool
// result travels twice -- once as structured content and once as a string
// holding the same JSON -- so the text block's copy is escaped ONE LEVEL
// DEEPER than the structured one, and a credential with a newline in it
// reaches the wire spelled `\\n`.
//
// That level is not a hypothetical. A check that knew only the first two
// spellings passed over a live leak: the flat token had no escapes and was
// redacted correctly, the multi-line one was compared against the wrong
// spelling, and the happy path looked clean. Three levels here rather than
// two, so a result that grows a third encoding fails rather than passing.
func spellings(t *testing.T, secret string) []string {
	t.Helper()
	out := []string{secret}
	for cur := secret; len(out) < 4; {
		encoded, err := json.Marshal(cur)
		if err != nil {
			t.Fatal(err)
		}
		// Drop the quotes json.Marshal wraps it in; what travels inside a body
		// is the escaped content, not a quoted string of its own.
		cur = string(encoded[1 : len(encoded)-1])
		out = append(out, cur)
	}
	return out
}

// A credential quoted by the cluster into a finding's own text is redacted too.
//
// Not only the notes: a Kargo condition message is free text from outside this
// process, it lands in a finding's detail, and a Warehouse that could not
// authenticate to a registry is exactly the object most likely to quote a
// credential back.
func TestACredentialInAConditionMessageIsRedacted(t *testing.T) {
	const secret = "registry-sentinel-must-not-be-published"
	t.Cleanup(func() { redact.Prime() })
	redact.Prime(secret)

	w := wedged()
	w.kargo.warehouses = append(w.kargo.warehouses, cluster.KargoWarehouse{
		Name: "charts-private", Namespace: "addons", Ready: false,
		ReadyReason:  "DiscoveryFailed",
		ReadyMessage: "unauthorized: the registry rejected " + secret,
	})

	f := newFixture(t, sweep(t, w))
	_, out := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
		`"params":{"name":"pipeline_report","arguments":{}}}`)
	for depth, spelling := range spellings(t, secret) {
		if bytes.Contains(out, []byte(spelling)) {
			t.Fatalf("a credential a Warehouse quoted back reached the wire, escaped %d time(s):\n%s",
				depth, out)
		}
	}
	if !bytes.Contains(out, []byte(redact.Marker)) {
		t.Fatalf("the message never reached the response, so nothing here was redacted:\n%s", out)
	}
}

// Serving a request performs no git-host call, no cluster call and no model
// call.
//
// It is what makes this surface safe to publish: a chatty client cannot spend
// an install's rate limit, because there is nothing on the request path that
// could. The sweep pays for everything, once per interval, and every tool
// answers from what it left behind.
func TestServingARequestReachesNothing(t *testing.T) {
	w := wedged()
	report := sweep(t, w)

	// Reset after the sweep: the sweep is allowed to read, and what is under
	// test is everything that happens afterwards.
	w.kargo.calls, w.prs.calls = 0, 0
	// And make any read from here on loud rather than merely counted.
	w.kargo.stageErr = errors.New("a tool call read the cluster")
	w.prs.ListErr = errors.New("a tool call read the git host")

	f := newFixture(t, report)
	for i := 0; i < 5; i++ {
		f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		f.call(t, "pipeline_report")
	}

	if w.kargo.calls != 0 {
		t.Errorf("serving %d requests made %d cluster reads; every answer comes from the "+
			"sweep's snapshot", 10, w.kargo.calls)
	}
	if w.prs.calls != 0 {
		t.Errorf("serving requests made %d git-host calls; a chatty client would spend the "+
			"install's rate limit", w.prs.calls)
	}
}

// Every tool call is logged once, with the tool, its arguments and who asked.
//
// The record exists so an operator can answer "who asked what", which is the
// question a read surface published beyond the cluster eventually raises. Once
// per call: a line written only on success would be a record with the
// interesting half missing.
func TestEveryToolCallIsLoggedOnceWithItsCaller(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))

	f.call(t, "pipeline_report")
	if len(f.logged) != 1 {
		t.Fatalf("want exactly one line per tool call, got %d: %v", len(f.logged), f.logged)
	}
	line := f.logged[0]
	for _, want := range []string{"pipeline_report", "{}", "127.0.0.1:"} {
		if !strings.Contains(line, want) {
			t.Errorf("the audit line must carry %q, got %q", want, line)
		}
	}

	// A call for a tool that does not exist is still a call somebody made.
	f.post(t, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"no_such_tool"}}`)
	if len(f.logged) != 2 {
		t.Fatalf("a refused tool call is still a call and must be logged: %v", f.logged)
	}

	// And the methods that are not tool calls are not logged as ones.
	f.post(t, `{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	if len(f.logged) != 2 {
		t.Fatalf("tools/list is not a tool call and must not appear in the audit log: %v", f.logged)
	}
}

// A tool's own arguments are redacted before they are logged.
//
// The arguments are text this process did not author, and the log is a place
// this process's own output goes. A caller that put a credential in an
// argument -- its own, or one it scraped -- must not be able to write it into
// bosun's log.
func TestLoggedArgumentsAreRedacted(t *testing.T) {
	const secret = "argument-sentinel-must-not-be-logged"
	t.Cleanup(func() { redact.Prime() })
	redact.Prime(secret)

	f := newFixture(t, sweep(t, wedged()))
	f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
		`"params":{"name":"pipeline_report","arguments":{"token":"`+secret+`"}}}`)

	for _, line := range f.logged {
		if strings.Contains(line, secret) {
			t.Fatalf("a credential reached the audit log through a tool argument: %q", line)
		}
	}
}

// The escape hatch admits everybody, and it is not something an omission can
// reach.
//
// The zero value of Auth is nil, which refuses to build a handler at all. This
// posture exists only as a type an operator has to name.
func TestTheUnauthenticatedPostureIsOnlyReachableByName(t *testing.T) {
	if (BearerToken{}).Allow(&http.Request{Header: http.Header{}}) {
		t.Fatal("an empty token must admit nobody; \"empty means everybody\" is the single " +
			"worst way for this to fail")
	}
	open := Unauthenticated{}
	if !open.Allow(&http.Request{Header: http.Header{}}) {
		t.Fatal("the escape hatch exists to admit everybody")
	}
	if !strings.Contains(open.Describe(), "NO AUTHENTICATION") {
		t.Errorf("the posture has to be visibly regrettable in a pod log, got %q", open.Describe())
	}
}

// A server missing any of its collaborators names the one it is missing, and
// builds no handler at all.
//
// The listener not starting without a token is decided in the composition
// root, where the operator's configuration is. This is the other half, and it
// is what makes "forgot to set Auth" a wiring error reported with the field
// that would fix it rather than a surface that quietly admits everybody.
func TestAHandlerNamesWhatItIsMissing(t *testing.T) {
	for _, tc := range []struct {
		name string
		bend func(*Server)
		says string
	}{
		{"no auth", func(s *Server) { s.Auth = nil }, "Auth"},
		{"no repository", func(s *Server) { s.Repository = "" }, "Repository"},
		{"no report", func(s *Server) { s.Report = nil }, "Report"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				Repository: "example/platform",
				Report:     func() *pipeline.Report { return nil },
				Auth:       BearerToken{Token: testToken},
			}
			tc.bend(s)
			h, err := s.Handler()
			if err == nil {
				t.Fatalf("a server with %s built a handler anyway", tc.name)
			}
			if h != nil {
				t.Error("a handler must not be returned beside the error; a caller that " +
					"ignored the error would serve with the field still missing")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the error must name what is missing, got %q", err)
			}
		})
	}
}
