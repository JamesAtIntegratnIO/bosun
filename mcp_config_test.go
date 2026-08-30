package main

import (
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
)

// An unset token stops the MCP listener, and the log says why.
//
// The other three postures are here beside it because the interesting thing is
// which of them are reachable by omission: exactly one is, and it is the one
// that serves nothing. An operator gets an authenticated listener by setting a
// token, an unauthenticated one by naming a value spelled to be regretted, and
// no listener at all by doing neither -- there is no path from "I forgot
// something" to "an open programmatic API on a published port".
func TestTheMCPListenerWillNotStartWithoutATokenOrAnExplicitHatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want string // the type of Auth, or "" for no listener
		says []string
	}{
		{
			name: "a token",
			cfg:  Config{MCP: true, MCPToken: "sekrit"},
			want: "mcp.BearerToken",
		},
		{
			name: "no token, no hatch",
			cfg:  Config{MCP: true},
			says: []string{"NOT starting", "no token is set", "MCP_TOKEN",
				"mcp.dangerouslyServeWithoutAuthentication"},
		},
		{
			name: "the hatch, named on purpose",
			cfg:  Config{MCP: true, MCPAllowUnauthenticated: true},
			want: "mcp.Unauthenticated",
		},
		{
			// The token wins. An operator who set both has said two things,
			// and the safe reading of the pair is the one that authenticates.
			name: "both",
			cfg:  Config{MCP: true, MCPToken: "sekrit", MCPAllowUnauthenticated: true},
			want: "mcp.BearerToken",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth, refusal := mcpAuth(&tc.cfg)
			if tc.want == "" {
				if auth != nil {
					t.Fatalf("the listener must not start, and it got %T", auth)
				}
				for _, want := range tc.says {
					if !strings.Contains(refusal, want) {
						t.Errorf("the log line must say %q, got:\n%s", want, refusal)
					}
				}
				return
			}
			if auth == nil {
				t.Fatalf("the listener must start, and it was refused with: %s", refusal)
			}
			if got := typeName(auth); got != tc.want {
				t.Errorf("want %s, got %s", tc.want, got)
			}
			if refusal != "" {
				t.Errorf("a listener that starts has nothing to explain, got: %s", refusal)
			}
		})
	}
}

func typeName(a mcp.Auth) string {
	switch a.(type) {
	case mcp.BearerToken:
		return "mcp.BearerToken"
	case mcp.Unauthenticated:
		return "mcp.Unauthenticated"
	}
	return "unknown"
}

// The MCP surface is off by default, so an upgrade does not open it.
//
// Both halves, because either one alone is a half-truth. The binary reading an
// unset MCP as false is what protects a Deployment somebody edits by hand; the
// chart's own default is what protects the install that runs `helm upgrade`
// and never opens a values file. The chart default is read from the rendered
// Deployment rather than from values.yaml, which is the only place the two can
// be seen to agree.
func TestTheMCPSurfaceIsOffUnlessAnOperatorSaysOtherwise(t *testing.T) {
	// The binary's half, derived from config.go's own reader so a default
	// flipped in the source fails here rather than shipping.
	if configBools(t)["MCP"] {
		t.Error("config.go reads MCP with a default of true.\n" +
			"This listener is built to be reached from outside the cluster. An upgrade must " +
			"not open one on an install whose operator has not decided to have it.")
	}

	// The chart's half, from a render of the defaults.
	env := helmtest.Env(t, helmtest.Render(t, "bosun", helmtest.Values("ci/lint-values.yaml")), "placeholder")
	if env["MCP"] != "false" {
		t.Errorf("a default render sets MCP=%q; the chart must ship the surface off", env["MCP"])
	}
	for _, k := range []string{"MCP_ADDR", "MCP_TOKEN", "MCP_TOKEN_FILE",
		"MCP_DANGEROUSLY_SERVE_WITHOUT_AUTHENTICATION"} {
		if v, ok := env[k]; ok {
			t.Errorf("a default render sets %s=%q, and it must set nothing about a surface "+
				"that is off; the variables are how a reader tells an install that enabled "+
				"this from one that did not", k, v)
		}
	}

	// And the ports and the peer stay off with it: an install that never
	// mentioned this must publish nothing for it.
	docs := helmtest.Render(t, "bosun", helmtest.Values("ci/lint-values.yaml"))
	svc := helmtest.One(t, docs, "Service")
	if strings.Contains(svc.Raw, "mcp") {
		t.Errorf("the default Service publishes an MCP port:\n%s", svc.Raw)
	}
	np := helmtest.One(t, docs, "NetworkPolicy")
	if strings.Contains(np.Raw, "8082") {
		t.Errorf("the default NetworkPolicy admits the MCP port:\n%s", np.Raw)
	}
}

// The MCP token is a credential like every other one.
//
// TestEveryCredentialLoadsFromAFile already covers the _FILE form and the
// trailing newline for every credential config.go reads, derived from its
// syntax tree, so MCP_TOKEN joined it the moment it was written. What that
// test cannot see is the trim on the way into Config, which is what makes a
// token read from a mounted Secret compare equal to the one an operator
// pasted into a client.
func TestTheMCPTokenIsTrimmedTheWayEveryCredentialIs(t *testing.T) {
	env := map[string]string{
		"GIT_OWNER": "o", "GIT_REPO": "r",
		"GIT_REPO_URL": "https://example.invalid/o/r.git",
		"LLM_PROVIDER": "openai", "LLM_MODEL": "m",
		"LLM_BASE_URL":    "http://model.invalid/v1",
		"ALLOW_PATHS":     "addons/**",
		"ARGOCD_BASE_URL": "https://argocd-server.argocd.svc",
		"ARGOCD_TOKEN":    "argocd-tok",
		"GIT_TOKEN":       "git-tok",
		"MCP":             "true",
		"MCP_TOKEN":       "  sekrit\n",
	}
	withOnly(t, env)

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("a config with an MCP token must load: %v", err)
	}
	if c.MCPToken != "sekrit" {
		t.Fatalf("want the token with its Secret file's whitespace gone, got %q", c.MCPToken)
	}
}

// And it primes the redactor.
//
// redaction_test.go derives its list from config.go, so this is covered there
// the moment envSecret reads MCP_TOKEN. Named here anyway because the
// consequence is specific to this surface: the MCP listener serialises a great
// deal of text it did not author to callers outside the cluster, so its own
// token turning up in one of those responses is the exact shape of the leak
// the redactor exists to stop.
func TestTheMCPTokenIsOneOfTheSecrets(t *testing.T) {
	c := &Config{MCPToken: "mcp-sentinel"}
	for _, s := range c.Secrets() {
		if s == "mcp-sentinel" {
			return
		}
	}
	t.Fatal("Config.Secrets does not name MCPToken, so the listener's own token is not " +
		"redacted from anything it serialises")
}
