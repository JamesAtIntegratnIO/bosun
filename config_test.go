package main

import (
	"os"
	"strings"
	"testing"
)

// A correctly-configured GitHub App is not a missing token.
//
// This crashed in production: the chart stops setting GIT_TOKEN under App auth
// -- installation tokens are minted per use, so there is nothing static to set
// -- while validate() still demanded it. The pod would not start:
//
//	configuration: missing required configuration: GIT_TOKEN
//
// Verifying the chart RENDER was not the same as running the binary without a
// token, and only the second would have caught this.
func TestCredentialRequirementFollowsTheAuthMode(t *testing.T) {
	base := map[string]string{
		"GIT_OWNER": "o", "GIT_REPO": "r",
		"GIT_REPO_URL": "https://example.invalid/o/r.git",
		"LLM_PROVIDER": "openai", "LLM_MODEL": "m",
		"LLM_BASE_URL": "http://model.invalid/v1",
		"ALLOW_PATHS":  "addons/**",
	}
	for _, tc := range []struct {
		name    string
		extra   map[string]string
		wantErr string
	}{
		{
			name:  "a token alone is enough",
			extra: map[string]string{"GIT_TOKEN": "ghp_x"},
		},
		{
			name:  "an app id and key are enough, with no token at all",
			extra: map[string]string{"GITHUB_APP_ID": "123", "GITHUB_APP_PRIVATE_KEY": "-----BEGIN..."},
		},
		{
			name:    "neither is a clear error naming both options",
			extra:   map[string]string{},
			wantErr: "GIT_TOKEN (or GITHUB_APP_ID",
		},
		{
			name:    "an app id without its key says which half is missing",
			extra:   map[string]string{"GITHUB_APP_ID": "123"},
			wantErr: "GITHUB_APP_PRIVATE_KEY",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range base {
				t.Setenv(k, v)
			}
			for _, k := range []string{"GIT_TOKEN", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY"} {
				_ = os.Unsetenv(k)
			}
			for k, v := range tc.extra {
				t.Setenv(k, v)
			}

			_, err := LoadConfig()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("should have been valid: %v", err)
			case tc.wantErr != "" && err == nil:
				t.Fatal("should have been rejected")
			case tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("want an error mentioning %q, got %q", tc.wantErr, err)
			}
		})
	}
}

// The chart shipped `bosun <bosun@users.noreply.github.com>` as its default
// for its whole early life, so it sits copied into consumers' values files as
// though somebody chose it. It is the noreply address of an unrelated GitHub
// account, and honoring it attributed the first live repair's commits to that
// stranger THROUGH the release that fixed the default -- explicit values beat
// defaults, and the explicit value was the old default, fossilised.
func TestTheLegacyAuthorIsIgnoredNotHonored(t *testing.T) {
	c := &Config{AuthorName: "bosun", AuthorEmail: "bosun@users.noreply.github.com"}
	if !c.NormaliseLegacyAuthor() {
		t.Fatal("the legacy author must be cleared")
	}
	if c.AuthorName != "" || c.AuthorEmail != "" {
		t.Fatalf("want both cleared, got %q <%q>", c.AuthorName, c.AuthorEmail)
	}

	chosen := &Config{AuthorName: "release-bot", AuthorEmail: "1234+release-bot@users.noreply.github.com"}
	if chosen.NormaliseLegacyAuthor() {
		t.Fatal("an identity somebody actually chose must be honored")
	}
}

// SUPERVISE_PIPELINE defaults ON while the cluster reader is only built for
// live reads or cluster-mode gating. Left unchecked, a GATE_MODE=ci deployment
// started healthy with /pipeline and /metrics answering 404 forever, behind one
// log line at boot -- the only cross-field rule here that was not a hard
// failure, and the one nobody would notice.
func TestSuperviseNeedsApiserverAccess(t *testing.T) {
	base := func() *Config {
		return &Config{
			GitOwner: "o", GitRepo: "r", GitRepoURL: "u", GitToken: "t",
			GitProvider: "github",
			LLMProvider: "anthropic", LLMModel: "m",
			AllowPaths: []string{"addons/**"},
			GateMode:   "ci", InventorySource: InventoryFromSecrets,
		}
	}

	c := base()
	c.Supervise = true
	err := c.validate()
	if err == nil {
		t.Fatal("supervision without apiserver access must not start")
	}
	if !strings.Contains(err.Error(), "SUPERVISE_PIPELINE") {
		t.Errorf("the error must name the setting: %v", err)
	}

	// The three ways to make it valid.
	c = base()
	c.Supervise, c.LiveReads = true, true
	if err := c.validate(); err != nil {
		t.Errorf("LIVE_READS=true should satisfy it: %v", err)
	}

	c = base()
	c.Supervise, c.GateMode = true, "cluster"
	if err := c.validate(); err != nil {
		t.Errorf("GATE_MODE=cluster should satisfy it: %v", err)
	}

	c = base()
	c.Supervise = false
	if err := c.validate(); err != nil {
		t.Errorf("supervision off should satisfy it: %v", err)
	}
}

// Seven boolean settings had drifted into two idioms -- `== "true"` and
// `!= "false"` -- which agree on nothing except the exact strings "true" and
// "false". LIVE_READS=1 was off; EXPLAIN_GREEN=no was on.
func TestEveryBooleanSettingAcceptsTheSameWords(t *testing.T) {
	onWords := []string{"1", "t", "true", "TRUE", "yes", "on"}
	offWords := []string{"0", "f", "false", "FALSE", "no", "off"}

	// Off by default: absent means off, and every on-word turns it on.
	for _, k := range []string{"GIT_INSECURE_SKIP_TLS_VERIFY", "GATE_FORK_PRS", "LIVE_READS"} {
		if envBool(k, false) {
			t.Errorf("%s: unset must stay off", k)
		}
		for _, w := range onWords {
			t.Setenv(k, w)
			if !envBool(k, false) {
				t.Errorf("%s=%s must be on", k, w)
			}
		}
	}

	// On by default: absent means on, and every off-word turns it off.
	for _, k := range []string{"EXPLAIN_GREEN", "MIGRATE_DROPPED_VERSIONS", "UPSTREAM_NOTES",
		"STRUCTURAL_MIGRATION", "SUPERVISE_PIPELINE"} {
		if !envBool(k, true) {
			t.Errorf("%s: unset must stay on", k)
		}
		for _, w := range offWords {
			t.Setenv(k, w)
			if envBool(k, true) {
				t.Errorf("%s=%s must be off", k, w)
			}
		}
	}
}

// Each of these three is validated in one switch and dispatched in another, in
// a different file. A value the validator accepts and the dispatcher does not
// is a pod that starts healthy and then does nothing -- so the two sets have to
// be the same, and a named type is what lets a test say so.
func TestTheValidatorAcceptsExactlyTheValuesThatDispatch(t *testing.T) {
	base := func() *Config {
		return &Config{
			GitOwner: "o", GitRepo: "r", GitRepoURL: "u", GitToken: "t",
			GitProvider: GitGitHub,
			LLMProvider: LLMAnthropic, LLMModel: "m",
			AllowPaths: []string{"addons/**"},
			GateMode:   GateInCluster, InventorySource: InventoryFromSecrets,
		}
	}

	for _, p := range []GitProviderName{GitGitHub, GitGitea} {
		c := base()
		c.GitProvider = p
		if err := c.validate(); err != nil {
			t.Errorf("GIT_PROVIDER %q is dispatched but rejected: %v", p, err)
		}
	}
	c := base()
	c.GitProvider = "bitbucket"
	if err := c.validate(); err == nil {
		t.Error("a provider with no dispatch branch must not start")
	}

	for _, p := range []LLMProviderName{LLMOpenAI, LLMAnthropic} {
		c := base()
		c.LLMProvider = p
		// openai needs a base URL; that is a separate rule, not a rejection
		// of the provider itself.
		c.LLMBaseURL = "http://x"
		if err := c.validate(); err != nil {
			t.Errorf("LLM_PROVIDER %q is dispatched but rejected: %v", p, err)
		}
	}
	c = base()
	c.LLMProvider = "ollama"
	if err := c.validate(); err == nil {
		t.Error("a model provider with no dispatch branch must not start")
	}

	for _, m := range []GateMode{GateInCluster, GateInCI} {
		c := base()
		c.GateMode = m
		c.Supervise = false // ci mode has no cluster reader; a separate rule
		if err := c.validate(); err != nil {
			t.Errorf("GATE_MODE %q is a mode but was rejected: %v", m, err)
		}
	}
	c = base()
	c.GateMode = "local"
	if err := c.validate(); err == nil {
		t.Error("an unknown gate mode must not start")
	}

	for _, src := range []InventorySource{InventoryFromSecrets, InventoryFromArgoCD} {
		c := base()
		c.InventorySource = src
		// The argocd source needs somewhere to read and something to read it
		// with; those are separate rules, not a rejection of the source.
		c.ArgoCDBaseURL, c.ArgoCDToken = "https://argocd-server.argocd.svc", "tok"
		if err := c.validate(); err != nil {
			t.Errorf("INVENTORY_SOURCE %q is dispatched but rejected: %v", src, err)
		}
	}
	c = base()
	c.InventorySource = "configmap"
	if err := c.validate(); err == nil {
		t.Error("an inventory source with no dispatch branch must not start")
	}
}

// The argocd source replaces one credential with another, and both halves of
// the new one are things an operator forgets. Naming them at start-up is the
// difference between a pod that will not start and an `error` status on every
// pull request, discovered by whoever tries to merge next.
func TestTheArgoCDInventorySourceNeedsAURLAndAToken(t *testing.T) {
	base := func() *Config {
		return &Config{
			GitOwner: "o", GitRepo: "r", GitRepoURL: "u", GitToken: "t",
			GitProvider: GitGitHub,
			LLMProvider: LLMAnthropic, LLMModel: "m",
			AllowPaths:  []string{"addons/**"},
			GateMode:    GateInCluster,
			ArgoCDToken: "tok", ArgoCDBaseURL: "https://argocd-server.argocd.svc",
			InventorySource: InventoryFromArgoCD,
		}
	}

	c := base()
	c.ArgoCDBaseURL = ""
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "ARGOCD_BASE_URL") {
		t.Errorf("a missing ArgoCD URL must name the setting: %v", err)
	}

	c = base()
	c.ArgoCDToken = ""
	if err := c.validate(); err == nil || !strings.Contains(err.Error(), "ARGOCD_TOKEN") {
		t.Errorf("a missing ArgoCD token must name the setting: %v", err)
	}

	// CI mode never reads a live inventory, so neither is required there --
	// refusing to start would be a rule about a code path that does not run.
	c = base()
	c.GateMode, c.Supervise = GateInCI, false
	c.ArgoCDBaseURL, c.ArgoCDToken = "", ""
	if err := c.validate(); err != nil {
		t.Errorf("ci mode reads no live inventory and must not demand ArgoCD credentials: %v", err)
	}
}
