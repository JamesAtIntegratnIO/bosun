package mcp

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Auth decides whether a request may be served.
//
// An interface with two implementations, both in this file, and the reason it
// is an interface at all is written into the spec this came from: the auth
// ladder past a static token -- a gateway-fronted SSO, then a token verifier
// -- waits for the audit log to show enough distinct consumers to justify it.
// The whole obligation to that future is that the check has a seam, so the
// rung is additive rather than a rewrite of the handler.
type Auth interface {
	// Allow reports whether the request carries an acceptable credential.
	Allow(*http.Request) bool
	// Describe is the sentence the start-up log prints about this posture.
	// On the interface because the sentence differs per implementation and an
	// operator reading a pod log is the only person who will ever see it.
	Describe() string
}

// BearerToken admits callers holding one shared secret.
type BearerToken struct{ Token string }

// Allow compares in constant time.
//
// The caller can retry, the comparison is against a shared secret, and a
// byte-at-a-time short circuit is a slow oracle for the rest of it. That much
// is the same check the promotion endpoint makes.
//
// Two things differ from it, and both are deliberate.
//
// The scheme is REQUIRED. The promotion endpoint uses TrimPrefix, which leaves
// a header with no scheme untouched and therefore accepts a bare token in the
// Authorization header. That is harmless where the caller is Kargo inside the
// cluster; this listener is built to be reached from outside it, where the
// header is written by clients, gateways and proxies this project has never
// seen, and "any credential-shaped string, however framed" is a wider door
// than the specification asks for.
//
// And the configured token is trimmed as well as the presented one. Every
// credential this process loads already goes through TrimSpace, so this is
// belt and braces -- but a BearerToken built anywhere else with a token read
// straight from a mounted Secret would otherwise refuse every correct caller
// over one invisible newline, which is a failure nobody diagnoses quickly.
func (b BearerToken) Allow(r *http.Request) bool {
	want := strings.TrimSpace(b.Token)
	if want == "" {
		// Unreachable through the composition root, which refuses to start
		// this listener without a token. Belt and braces, because "empty
		// token means everybody" is the single worst way for this to fail.
		return false
	}
	header := r.Header.Get("Authorization")
	scheme, rest, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	got := strings.TrimSpace(rest)
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (b BearerToken) Describe() string { return "a bearer token is required" }

// Unauthenticated admits everybody, and exists so that the one operator who
// genuinely wants that has to say so on purpose.
//
// It is not the zero value and it is not what an empty token falls back to.
// Both of those would make an unauthenticated programmatic API something that
// could happen by omission -- an upgrade, a typo in a values file, a Secret
// key renamed -- on the one listener in this process built to be reached from
// outside the cluster. It is reachable only through a chart value named to be
// visibly regrettable, and it says so every time the process starts.
type Unauthenticated struct{}

func (Unauthenticated) Allow(*http.Request) bool { return true }

func (Unauthenticated) Describe() string {
	return "NO AUTHENTICATION: anything that can reach this port may read everything this install holds"
}
