// Package egress is what the agent is allowed to reach, and the record of where
// it went.
//
// This replaces an allow-list, and the reason is worth writing down. The
// allow-list was correct and it was a full-time job: every chart repository,
// every registry's blob CDN, every redirect target had to be named before the
// agent could read it, and each omission surfaced as a two-minute timeout and a
// brief that said it had no evidence. Three separate incidents added a host
// after the fact, `pkg-containers.githubusercontent.com`,
// `release-assets.githubusercontent.com`, `external-secrets.io`, and the next
// chart adoption would have added a fourth.
//
// The trade is deliberate: reach anything, say where you went, and let an
// operator forbid a destination by name. Accountability after the fact rather
// than permission before it. That is a weaker guarantee, and it is the right
// one for a component whose whole job is reading public metadata about public
// artifacts; it holds a git token and a model key, and neither is improved by
// making it fail to read a chart index.
//
// What did not change: the agent still writes only to the pull request's own
// branch, still refuses paths on the deny-list, and still never mutates the
// cluster. Widening what it may read is not widening what it may do.
package egress

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
)

// Policy is the destinations an operator has forbidden.
//
// Empty means everything is permitted, which is the point. A deny-list is only
// worth having if the default is open; a deny-list on top of an allow-list is
// two lists to maintain and one of them is redundant.
type Policy struct {
	// Deny are hosts the agent must not contact. An entry is either an exact
	// host or a `*.suffix` pattern.
	Deny []string
	// Log receives one line per outbound request. Never nil in practice; a nil
	// Log means the record is not kept, which an operator should have to choose
	// rather than get by omission.
	Log func(string, ...any)
}

// Denied reports whether a host is forbidden, and by which rule.
//
// Matching is on the host only. A rule per path would look more precise and be
// less enforceable: a redirect can move a request to another path on the same
// host, and the thing an operator wants to stop is talking to somebody.
func (p Policy) Denied(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSuffix(hostOnly(host), "."))
	for _, rule := range p.Deny {
		r := strings.ToLower(strings.TrimSpace(rule))
		switch {
		case r == "":
		case r == host:
			return rule, true
		case strings.HasPrefix(r, "*."):
			// `*.example.com` forbids subdomains and the apex. An operator
			// blocking a domain means the domain; making them write both is a
			// footgun that shows up as a request they thought they had stopped.
			suffix := r[1:]
			if strings.HasSuffix(host, suffix) || host == strings.TrimPrefix(suffix, ".") {
				return rule, true
			}
		}
	}
	return "", false
}

func hostOnly(h string) string {
	if i := strings.IndexByte(h, ':'); i >= 0 {
		// Strip a port, but not an IPv6 literal's colons.
		if !strings.Contains(h, "]") {
			return h[:i]
		}
	}
	return h
}

// Transport logs every outbound request and refuses the denied ones.
//
// A RoundTripper rather than a check at each call site, because the call sites
// are the problem: the registry walk, the API reads and the redirect chains
// each build their own URLs, and a redirect in particular reaches a host no
// call site ever named. This sees the actual connection.
type Transport struct {
	Policy Policy
	Base   http.RoundTripper

	mu   sync.Mutex
	seen map[string]int
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	host := hostOnly(r.URL.Host)
	if rule, denied := t.Policy.Denied(host); denied {
		t.logf("outbound REFUSED %s (egress deny rule %q)", host, rule)
		return nil, fmt.Errorf("egress to %s is denied by policy (rule %q)", host, rule)
	}
	t.note(host)
	t.logf("outbound %s %s", r.Method, redact(r.URL.String()))

	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(r)
}

// Hosts is every host contacted so far, with a count. For a periodic summary, a
// per-request log is the record, and a reader wants the shape of it.
func (t *Transport) Hosts() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int, len(t.seen))
	for k, v := range t.seen {
		out[k] = v
	}
	return out
}

func (t *Transport) note(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.seen == nil {
		t.seen = map[string]int{}
	}
	t.seen[host]++
}

func (t *Transport) logf(f string, a ...any) {
	if t.Policy.Log != nil {
		t.Policy.Log(f, a...)
	}
}

// redact removes the query string.
//
// Registry and release-asset URLs carry signed tokens in their query, a GitHub
// release download is a pre-signed blob URL with a JWT in it, and a log line is
// exactly the wrong place for a credential that happens to be short-lived. The
// path is what tells a reader what was fetched.
func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i] + "?…"
	}
	return u
}

// Client returns an http.Client that logs and enforces.
func (p Policy) Client(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	c := *base
	c.Transport = &Transport{Policy: p, Base: base.Transport}
	return &c
}

// HostOf is the host a reference will reach, and the single owner of
// that question.
//
// It lived privately in two packages that both feed it to Denied, and they
// disagreed on the case that matters: a chart repository written without a
// scheme. `ghcr.io/org/charts` is a real destination, helm is handed it as
// `oci://ghcr.io/org/charts` moments later, and one copy returned "" for it,
// so the deny-list and the outbound log were both skipped for exactly those
// references. The other copy returned the first segment of anything, so a bare
// chart name like `podinfo` was checked as though it were a hostname.
//
// The rule that separates them is the one a registry uses: the first element
// is a host if it looks like one; it contains a dot, or it is localhost, with
// an optional port. Anything else is a chart name and reaches nothing on its
// own.
func HostOf(ref string) string {
	s := strings.TrimSpace(ref)
	for _, scheme := range []string{"oci://", "https://", "http://"} {
		s = strings.TrimPrefix(s, scheme)
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	host := s
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return ""
	}
	// A scheme is proof enough on its own; without one, the shape has to say so.
	if strings.Contains(ref, "://") {
		return host
	}
	if strings.Contains(host, ".") || host == "localhost" {
		return host
	}
	return ""
}
