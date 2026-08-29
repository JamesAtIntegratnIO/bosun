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
// The trade is deliberate: reach anything public, say where you went, and let
// an operator forbid a destination by name. Accountability after the fact
// rather than permission before it. That is a weaker guarantee, and it is the
// right one for a component whose whole job is reading public metadata about
// public artifacts; it holds a git token and a model key, and neither is
// improved by making it fail to read a chart index.
//
// Public is where that trade holds, and only there. Internal address space is
// the opposite case: nothing this reads lives at 169.254.169.254 or on the
// cluster's own network, and a deny-list of host strings never kept it out of
// either. The same address is `http://2852039166/`, it is
// `[::ffff:a9fe:a9fe]`, and it is any name whose owner points it at the
// link-local block a moment after the string was checked. So the space in
// DefaultDenyNetworks is closed whatever the configuration says, and an
// operator running an internal chart repository opens their own network by
// naming it in AllowPrivate.
//
// What that does not close, because saying so is cheaper than finding out:
//
//   - helm is a subprocess. The gate and the schema probe check a chart
//     reference's host through Denied and log it, and then helm resolves and
//     dials on its own; an internal name handed to helm is not caught here.
//   - A proxy is dialled instead of the destination, so the address checked is
//     the proxy's. An operator whose proxy sits on an internal network names
//     that network in AllowPrivate.
//   - A caller that supplies a RoundTripper this package cannot instrument
//     keeps its own dialler, and only the host check reaches it.
//
// What did not change: the agent still writes only to the pull request's own
// branch, still refuses paths on the deny-list, and still never mutates the
// cluster. Widening what it may read is not widening what it may do.
package egress

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Policy is the destinations an operator has forbidden.
//
// Empty means every public host is permitted, which is the point. A deny-list
// is only worth having if the default is open; a deny-list on top of an
// allow-list is two lists to maintain and one of them is redundant. Empty does
// not mean the internal networks are permitted: see DefaultDenyNetworks, which
// no configuration removes.
type Policy struct {
	// Deny are hosts the agent must not contact. An entry is either an exact
	// host or a `*.suffix` pattern.
	Deny []string
	// AllowPrivate are networks from DefaultDenyNetworks an operator has
	// decided this may reach after all, as a CIDR or a single address. An
	// internal chart museum on the cluster's own network is a real deployment
	// and this is how it is named.
	//
	// Addresses, not names, because the check that matters happens where the
	// address is known: at the dial. An entry that parses as neither allows
	// nothing, which is the safe direction and shows up as the refusal
	// continuing to name the network.
	AllowPrivate []string
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
//
// The closed networks are answered first and without configuration, the way
// edits.Policy.Check answers for edits.DefaultDeny before it reads the
// operator's list. Only a host written as an address is caught here; a name is
// somebody else's answer until it is resolved, and resolving is the dial's job.
func (p Policy) Denied(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSuffix(hostOnly(host), "."))
	if network, denied := p.deniedLiteral(host); denied {
		return network.String(), true
	}
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

// hostOnly is the host with its port and its brackets off, and the one place
// that question is answered.
//
// The port has to go or a rule for a host would miss the same host on 8443.
// The brackets have to go with it: keeping them meant `[::1]:8080` came back
// whole, port and all, because the old test for an IPv6 literal was whether
// the string held a `]` and then it stripped nothing at all. Both the
// deny-list and the address rule were being handed a string that was never
// going to match anything.
func hostOnly(h string) string {
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i >= 0 {
			return h[1:i]
		}
		return h
	}
	if strings.Count(h, ":") > 1 {
		// An unbracketed IPv6 literal. There is no port on it to strip, and
		// cutting at the first colon leaves the empty string.
		return h
	}
	if i := strings.IndexByte(h, ':'); i >= 0 {
		return h[:i]
	}
	return h
}

// DefaultDenyNetworks is address space the agent does not reach, whatever the
// configuration says. Each entry is somewhere that answering at all means the
// agent has been pointed back at its own side of the network instead of at a
// public artifact.
//
// Not a default in the sense of a value with an override: an empty Policy has
// every one of them, and the only way past one is to name it in AllowPrivate,
// which is a decision an operator takes rather than one they reach by
// forgetting to deny something. edits.DefaultDeny is the same shape for the
// same reason.
var DefaultDenyNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // "this network"; 0.0.0.0 is the local stack on Linux
	netip.MustParsePrefix("127.0.0.0/8"),    // loopback: this pod's own sidecars and metrics endpoints
	netip.MustParsePrefix("169.254.0.0/16"), // link-local, and the cloud metadata service at 169.254.169.254
	netip.MustParsePrefix("10.0.0.0/8"),     // RFC1918: the cluster, and everything standing beside it
	netip.MustParsePrefix("172.16.0.0/12"),  //
	netip.MustParsePrefix("192.168.0.0/16"), //
	netip.MustParsePrefix("100.64.0.0/10"),  // RFC6598, which is pod and node addressing on more than one managed platform
	netip.MustParsePrefix("::1/128"),        // loopback
	netip.MustParsePrefix("::/128"),         // unspecified
	netip.MustParsePrefix("fe80::/10"),      // link-local
	netip.MustParsePrefix("fc00::/7"),       // unique local
}

// deniedAddr reports whether an address is inside the closed space, and which
// network caught it.
//
// Two normalisations, and without either one a destination walks past the list
// on a technicality. An IPv4-mapped address is a different sixteen bytes for
// the same place, so `[::ffff:a9fe:a9fe]` is 169.254.169.254 and has to be
// compared as one; and a Prefix contains no zoned address at all, so
// `fe80::1%eth0` would clear the link-local entry it is plainly inside.
func (p Policy) deniedAddr(a netip.Addr) (netip.Prefix, bool) {
	a = a.Unmap().WithZone("")
	if !a.IsValid() {
		return netip.Prefix{}, false
	}
	for _, entry := range p.AllowPrivate {
		if allowed, ok := parseNetwork(entry); ok && allowed.Contains(a) {
			return netip.Prefix{}, false
		}
	}
	for _, network := range DefaultDenyNetworks {
		if network.Contains(a) {
			return network, true
		}
	}
	return netip.Prefix{}, false
}

// deniedLiteral answers for a host that is already written as an address.
//
// A host that is not one is not canonicalised here, and does not need to be:
// `2852039166` and `0251.0376.0251.0376` are names as far as this is
// concerned, exactly as they are to anything that has not resolved them yet.
// Whatever they turn into is caught at the dial, which is the only place the
// answer is a fact rather than a guess.
func (p Policy) deniedLiteral(host string) (netip.Prefix, bool) {
	a, err := netip.ParseAddr(hostOnly(host))
	if err != nil {
		return netip.Prefix{}, false
	}
	return p.deniedAddr(a)
}

func parseNetwork(entry string) (netip.Prefix, bool) {
	entry = strings.TrimSpace(entry)
	if network, err := netip.ParsePrefix(entry); err == nil {
		return network.Masked(), true
	}
	if a, err := netip.ParseAddr(entry); err == nil {
		return netip.PrefixFrom(a.Unmap().WithZone(""), a.Unmap().BitLen()), true
	}
	return netip.Prefix{}, false
}

// refusal is what an operator is told when a closed network stops a request,
// and it is a type rather than a string so the transport can recognise its own
// refusal coming back up out of a dial and write the record. It names the
// network rather than only the address: the address is what they already have,
// the network is what they would have to allow.
type refusal struct {
	dest    string
	network netip.Prefix
}

func (r *refusal) Error() string {
	return fmt.Sprintf("egress to %s is denied: it is inside %s, which this does not reach "+
		"unless an operator allows that network", r.dest, r.network)
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
	// Asked here as well as at the dial, and asked first: a request written
	// straight at an address inside the closed space is refused before a name
	// is looked up or a packet is sent, and the refusal can say which network
	// and what an operator would have to do about it. Denied answers this too,
	// as a rule string, for the callers that only have a host.
	if network, denied := t.Policy.deniedLiteral(host); denied {
		t.logf("outbound REFUSED %s (inside %s, closed by default)", host, network)
		return nil, &refusal{dest: host, network: network}
	}
	if rule, denied := t.Policy.Denied(host); denied {
		t.logf("outbound REFUSED %s (egress deny rule %q)", host, rule)
		return nil, fmt.Errorf("egress to %s is denied by policy (rule %q)", host, rule)
	}
	t.note(host)
	t.logf("outbound %s %s", r.Method, redact(r.URL))

	base := t.Base
	if base == nil {
		base = t.Policy.guarded()
	}
	resp, err := base.RoundTrip(r)
	// The dial is the only place a name becomes an address, so it is the only
	// place a name pointed at an internal network is ever caught, and the line
	// above has already said the request went out. Without this the record
	// would show it leaving and never show it being stopped.
	var stopped *refusal
	if errors.As(err, &stopped) {
		t.logf("outbound REFUSED %s (it resolved to %s, inside %s, closed by default)",
			host, stopped.dest, stopped.network)
	}
	return resp, err
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

// redact is the destination as a log line may carry it: scheme, host, path.
//
// Rebuilt from the parsed URL rather than trimmed out of the printed one,
// because a URL carries a credential in two places and a trimmer only ever
// remembers the one it was written for. Registry and release-asset URLs carry
// signed tokens in their query, and a GitHub release download is a pre-signed
// blob URL with a JWT in it; that was the case this handled. The one it did
// not is userinfo, which is how a chart repoURL or an artifact reference gets
// written when a repository needs a password, and
// `https://user:token@host/path` was logged verbatim. Building the line out of
// the parts that are safe cannot forget a third one. The path is what tells a
// reader what was fetched.
func redact(u *url.URL) string {
	if u == nil {
		return ""
	}
	out := u.Scheme + "://" + u.Host + u.EscapedPath()
	if u.ForceQuery || u.RawQuery != "" {
		out += "?…"
	}
	return out
}

// Client returns an http.Client that logs and enforces.
func (p Policy) Client(base *http.Client) *http.Client {
	if base == nil {
		base = &http.Client{}
	}
	c := *base
	c.Transport = &Transport{Policy: p, Base: p.guard(base.Transport)}
	return &c
}

// guard puts the address rule on the base transport's dial.
//
// The dial is where the rule can be a fact. Everything before it is a string
// somebody else controls: a name resolves to whatever its owner is serving at
// the moment it is asked, and asking twice is two answers, so a check on the
// name is a check on the first one and the connection is made with the second.
// Control is handed the address that is about to be connected to.
//
// A base that already dials its own way has that dialler replaced rather than
// wrapped, and a base that is not an *http.Transport at all is left alone,
// which is a fake in a test and dials nothing. The package doc says so; both
// are the case where only the host check applies.
func (p Policy) guard(rt http.RoundTripper) http.RoundTripper {
	base, ok := rt.(*http.Transport)
	if !ok {
		// nil included: Transport.RoundTrip reaches for the shared guarded one.
		return rt
	}
	guarded := base.Clone()
	guarded.DialContext = p.dialer().DialContext
	return guarded
}

func (p Policy) dialer() *net.Dialer {
	return &net.Dialer{
		// http.DefaultTransport's numbers. This replaces that dialler and a
		// request with no timeout on the connect is how a dead host becomes a
		// pod that is doing nothing and says nothing.
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   p.control,
	}
}

func (p Policy) control(_, address string, _ syscall.RawConn) error {
	a, err := netip.ParseAddrPort(address)
	if err != nil {
		// Not an address this can read is not an address this can contain, and
		// every destination this package exists for is one it can read.
		return fmt.Errorf("egress refuses %q: it is not an address this can check", address)
	}
	if network, denied := p.deniedAddr(a.Addr()); denied {
		return &refusal{dest: a.Addr().String(), network: network}
	}
	return nil
}

// guarded is the default transport with the address rule on its dial, one per
// distinct set of allowed networks.
//
// Shared rather than built per call, because Client is called per request:
// upstream builds one for every registry hop, and a transport apiece is a
// connection pool apiece, which turns a walk of several requests into several
// TLS handshakes that used to be one. The allowed networks are the whole key
// because they are the whole of what the dialler reads; the rest of a Policy
// is applied above this, per request.
func (p Policy) guarded() http.RoundTripper {
	key := strings.Join(p.AllowPrivate, "\x00")
	guardedMu.Lock()
	defer guardedMu.Unlock()
	if t, ok := guardedTransports[key]; ok {
		return t
	}
	t := &http.Transport{}
	if d, ok := http.DefaultTransport.(*http.Transport); ok {
		t = d.Clone()
	}
	t.DialContext = p.dialer().DialContext
	guardedTransports[key] = t
	return t
}

var (
	guardedMu         sync.Mutex
	guardedTransports = map[string]*http.Transport{}
)

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
