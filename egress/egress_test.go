package egress

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestADenyRuleStopsTheHostAndSaysWhich(t *testing.T) {
	p := Policy{Deny: []string{"evil.example", "*.tracker.net", " Spaced.Example "}}
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
		{"github.com", false, ""},
		{"nottracker.net", false, ""},
	} {
		rule, denied := p.Denied(tc.host)
		if denied != tc.bad || (tc.bad && rule != tc.rule) {
			t.Errorf("Denied(%q) = (%q,%v), want (%q,%v)", tc.host, rule, denied, tc.rule, tc.bad)
		}
	}
}

// An empty policy permits everything. That is the point of the change, a
// deny-list on top of an allow-list is two lists and one of them is redundant.
func TestAnEmptyPolicyPermitsEverything(t *testing.T) {
	if _, denied := (Policy{}).Denied("anything.example"); denied {
		t.Fatal("an empty deny-list refused something")
	}
}

func TestEveryRequestIsLoggedAndDeniedOnesNeverLeave(t *testing.T) {
	reached := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached++ }))
	t.Cleanup(srv.Close)

	var lines []string
	p := Policy{Log: func(f string, a ...any) { lines = append(lines, strings.TrimSpace(fmt.Sprintf(f, a...))) }}
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
	p2 := Policy{Deny: []string{host}, Log: p.Log}
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
	got := redact("https://release-assets.githubusercontent.com/x/y.tgz?sig=SECRET&jwt=ALSOSECRET")
	if strings.Contains(got, "SECRET") {
		t.Fatalf("logged a signed URL verbatim: %s", got)
	}
	if !strings.HasPrefix(got, "https://release-assets.githubusercontent.com/x/y.tgz") {
		t.Fatalf("redaction lost the destination: %s", got)
	}
}

// Hosts is the shape of where it has been, for a summary line.
func TestHostsAreCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	tr := &Transport{Policy: Policy{}, Base: srv.Client().Transport}
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
