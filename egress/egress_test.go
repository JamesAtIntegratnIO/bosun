package egress

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// httptest serves on loopback, which is closed by default, so every test that
// wants to reach one says so the way an operator with an internal repository
// would. That the tests cannot skip this is the point of the rule.
const loopback = "127.0.0.0/8"

func TestADenyRuleStopsTheHostAndSaysWhich(t *testing.T) {
	p := Policy{Deny: []string{"evil.example", "*.tracker.net", " Spaced.Example ", "2606:4700::1111"}}
	for _, tc := range []struct {
		host string
		bad  bool
		rule string
	}{
		{"evil.example", true, "evil.example"},
		{"EVIL.example", true, "evil.example"},
		{"evil.example:443", true, "evil.example"},
		{"a.tracker.net", true, "*.tracker.net"},
		{"deep.a.tracker.net", true, "*.tracker.net"},
		// The apex too: an operator blocking a domain means the domain, and
		// making them write both entries is a footgun that shows up as a
		// request they thought they had stopped.
		{"tracker.net", true, "*.tracker.net"},
		{"spaced.example", true, " Spaced.Example "},
		// A rule for a host has to catch that host on any port, and an IPv6
		// literal arrives bracketed. `[::1]:8080` used to come back from
		// hostOnly whole, so a rule like this one matched nothing.
		{"[2606:4700::1111]:443", true, "2606:4700::1111"},
		{"[2606:4700::1111]", true, "2606:4700::1111"},
		{"github.com", false, ""},
		{"nottracker.net", false, ""},
	} {
		rule, denied := p.Denied(tc.host)
		if denied != tc.bad || (tc.bad && rule != tc.rule) {
			t.Errorf("Denied(%q) = (%q,%v), want (%q,%v)", tc.host, rule, denied, tc.rule, tc.bad)
		}
	}
}

// An empty policy permits every public host. That is the point of the change,
// a deny-list on top of an allow-list is two lists and one of them is
// redundant. The internal networks are the exception, and the test below it is
// the other half of this one.
func TestAnEmptyPolicyPermitsEveryPublicHost(t *testing.T) {
	if _, denied := (Policy{}).Denied("anything.example"); denied {
		t.Fatal("an empty deny-list refused something")
	}
}

// The metadata service is one destination and many strings. A deny-list of
// host strings caught none of them, which is why the rule is on the address.
func TestTheClosedNetworksAreRefusedInEveryFormAnAddressIsWritten(t *testing.T) {
	for _, tc := range []struct {
		host    string
		network string
	}{
		{"169.254.169.254", "169.254.0.0/16"},
		{"169.254.169.254:80", "169.254.0.0/16"},
		// The same sixteen bytes, spelled as IPv6.
		{"[::ffff:a9fe:a9fe]", "169.254.0.0/16"},
		{"[::ffff:169.254.169.254]:8080", "169.254.0.0/16"},
		{"127.0.0.1", "127.0.0.0/8"},
		{"[::1]", "::1/128"},
		{"0.0.0.0", "0.0.0.0/8"},
		{"10.4.5.6", "10.0.0.0/8"},
		{"172.20.0.1", "172.16.0.0/12"},
		{"192.168.1.1", "192.168.0.0/16"},
		{"100.64.0.1", "100.64.0.0/10"},
		{"[fd00::1]", "fc00::/7"},
		// A zone is not a way out: a Prefix contains no zoned address, so this
		// walked past the link-local entry it is plainly inside.
		{"[fe80::1%eth0]", "fe80::/10"},
	} {
		rule, denied := Policy{}.Denied(tc.host)
		if !denied || rule != tc.network {
			t.Errorf("Denied(%q) = (%q,%v), want (%q,true)", tc.host, rule, denied, tc.network)
		}
	}
	for _, host := range []string{"140.82.121.4", "[2606:4700::1111]", "github.com", "ghcr.io"} {
		if rule, denied := (Policy{}).Denied(host); denied {
			t.Errorf("Denied(%q) refused a public destination (rule %q)", host, rule)
		}
	}
}

// The closed networks are not configuration. An operator who names no deny
// rules has not opened the link-local block, the way an empty edits.Policy has
// not opened `.github/**`.
func TestAnEmptyPolicyStillClosesTheInternalNetworks(t *testing.T) {
	for _, p := range []Policy{{}, {Deny: []string{}}, {Deny: []string{"unrelated.example"}}} {
		if _, denied := p.Denied("169.254.169.254"); !denied {
			t.Fatalf("%+v reached the metadata address", p)
		}
	}
}

// An internal chart repository is a real deployment, so there is a way to say
// so, and it opens the network that was named and nothing else.
func TestAnOperatorOpensOneNetworkAndOnlyThatOne(t *testing.T) {
	p := Policy{AllowPrivate: []string{"10.0.0.0/8", "192.168.4.7"}}
	for _, host := range []string{"10.4.5.6", "192.168.4.7"} {
		if rule, denied := p.Denied(host); denied {
			t.Errorf("an allowed network was still refused: %s (rule %q)", host, rule)
		}
	}
	for _, host := range []string{"169.254.169.254", "192.168.4.8", "127.0.0.1"} {
		if _, denied := p.Denied(host); !denied {
			t.Errorf("allowing one network opened another: %s", host)
		}
	}
	// An entry that is neither an address nor a CIDR opens nothing. Failing
	// closed is the only direction a typo may fail in.
	if _, denied := (Policy{AllowPrivate: []string{"charts.internal"}}).Denied("10.4.5.6"); !denied {
		t.Fatal("an unparseable allowPrivate entry opened a closed network")
	}
}

// The case a host-string check cannot answer: the name is public and the
// address it resolves to is not. Nothing here is on the deny-list and nothing
// here is written as an address, so the refusal can only have come from the
// dial, which is the point of putting it there.
func TestANameIsCaughtAtTheDialByWhatItResolvesTo(t *testing.T) {
	reached := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	t.Cleanup(srv.Close)
	byName := "http://localhost:" + strings.TrimPrefix(srv.URL, "http://127.0.0.1:") + "/index.yaml"

	var lines []string
	closed := Policy{Log: func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }}
	if _, err := closed.Client(srv.Client()).Get(byName); err == nil {
		t.Fatal("a name resolving into a closed network was contacted anyway")
	} else if !strings.Contains(err.Error(), "127.0.0.0/8") && !strings.Contains(err.Error(), "::1/128") {
		t.Fatalf("the refusal did not name the network that caused it: %v", err)
	}
	if reached != 0 {
		t.Fatalf("the request reached the server (%d)", reached)
	}
	// The record has to show it being stopped. The line before it says the
	// request went out, and on its own that reads like it arrived.
	if !strings.Contains(lines[len(lines)-1], "REFUSED") {
		t.Fatalf("a dial refused for its address was not recorded: %v", lines)
	}

	// And the same request goes through once the network is allowed, so what
	// refused it was the address rule and not the name.
	resp, err := (Policy{AllowPrivate: []string{loopback, "::1/128"}}).Client(srv.Client()).Get(byName)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if reached != 1 {
		t.Fatalf("an allowed network did not go through (%d)", reached)
	}
}

func TestEveryRequestIsLoggedAndDeniedOnesNeverLeave(t *testing.T) {
	reached := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	t.Cleanup(srv.Close)

	var lines []string
	p := Policy{
		AllowPrivate: []string{loopback},
		Log:          func(f string, a ...any) { lines = append(lines, strings.TrimSpace(fmt.Sprintf(f, a...))) },
	}
	c := p.Client(srv.Client())

	resp, err := c.Get(srv.URL + "/index.yaml")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if reached != 1 {
		t.Fatalf("the request did not go through: %d", reached)
	}
	if len(lines) != 1 || !strings.Contains(lines[0], "outbound GET") || !strings.Contains(lines[0], "/index.yaml") {
		t.Fatalf("the destination was not recorded: %v", lines)
	}

	// Now forbid it. The request must not reach the server at all.
	host := strings.TrimPrefix(srv.URL, "http://")
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	p2 := Policy{Deny: []string{host}, AllowPrivate: []string{loopback}, Log: p.Log}
	if _, err := p2.Client(srv.Client()).Get(srv.URL + "/index.yaml"); err == nil {
		t.Fatal("a denied host was contacted anyway")
	}
	if reached != 1 {
		t.Fatalf("the denied request still reached the server (%d)", reached)
	}
	if !strings.Contains(lines[len(lines)-1], "REFUSED") {
		t.Fatalf("the refusal was not recorded: %v", lines)
	}
}

// A GitHub release download is a pre-signed URL with a JWT in the query. A log
// line is exactly the wrong place for a credential, however short-lived.
func TestTheQueryStringIsNotLogged(t *testing.T) {
	got := redact(mustURL(t, "https://release-assets.githubusercontent.com/x/y.tgz?sig=SECRET&jwt=ALSOSECRET"))
	if strings.Contains(got, "SECRET") {
		t.Fatalf("logged a signed URL verbatim: %s", got)
	}
	if !strings.HasPrefix(got, "https://release-assets.githubusercontent.com/x/y.tgz") {
		t.Fatalf("redaction lost the destination: %s", got)
	}
}

// The other half of the same URL. A chart repository that needs a password is
// written `https://user:token@host/path`, and trimming the query left that
// whole credential in the log line.
func TestUserinfoIsNotLogged(t *testing.T) {
	got := redact(mustURL(t, "https://helm:TOKEN@charts.example/stable/index.yaml?t=SECRET"))
	if strings.Contains(got, "TOKEN") || strings.Contains(got, "helm:") || strings.Contains(got, "SECRET") {
		t.Fatalf("logged a credential: %s", got)
	}
	if got != "https://charts.example/stable/index.yaml?…" {
		t.Fatalf("redaction lost the destination: %s", got)
	}
}

// The log line is built by the transport, not by a caller who remembered to
// call redact, so the credential has to be gone from what actually gets logged.
func TestTheLoggedLineForARealRequestCarriesNoCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)

	var lines []string
	p := Policy{
		AllowPrivate: []string{loopback},
		Log:          func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) },
	}
	u := mustURL(t, srv.URL)
	u.User = url.UserPassword("helm", "TOKEN")
	u.Path = "/index.yaml"
	u.RawQuery = "sig=SECRET"

	resp, err := p.Client(srv.Client()).Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, l := range lines {
		if strings.Contains(l, "TOKEN") || strings.Contains(l, "SECRET") {
			t.Fatalf("the outbound log line carried a credential: %s", l)
		}
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// Hosts is the shape of where it has been, for a summary line.
func TestHostsAreCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	tr := &Transport{Policy: Policy{AllowPrivate: []string{loopback}}, Base: srv.Client().Transport}
	c := &http.Client{Transport: tr}
	for i := 0; i < 3; i++ {
		r, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
	}
	total := 0
	for _, n := range tr.Hosts() {
		total += n
	}
	if total != 3 {
		t.Fatalf("counted %d requests, want 3", total)
	}
}
