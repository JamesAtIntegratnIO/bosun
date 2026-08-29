package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/egress"
)

// A correctly-configured GitHub App is not a missing token.
//
// This crashed in production: the chart stops setting GIT_TOKEN under App
// auth, installation tokens are minted per use, so there is nothing static to
// set, while validate() still demanded it. The pod would not start:
//
//	configuration: missing required configuration: GIT_TOKEN
//
// Verifying the chart render was not the same as running the binary without a
// token, and only the second would have caught this.
func TestCredentialRequirementFollowsTheAuthMode(t *testing.T) {
	base := map[string]string{
		"GIT_OWNER": "o", "GIT_REPO": "r",
		"GIT_REPO_URL": "https://example.invalid/o/r.git",
		"LLM_PROVIDER": "openai", "LLM_MODEL": "m",
		"LLM_BASE_URL": "http://model.invalid/v1",
		"ALLOW_PATHS":  "addons/**",
		// The inventory credential, so this test fails on the credential it is
		// about rather than on an unrelated required value.
		"ARGOCD_BASE_URL": "https://argocd-server.argocd.svc",
		"ARGOCD_TOKEN":    "argocd-tok",
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
// stranger through the release that fixed the default, explicit values beat
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
		t.Fatal("an identity somebody chose must be honored")
	}
}

// Seven boolean settings had drifted into two idioms, `== "true"` and `!=
// "false"`, which agree on nothing except the exact strings "true" and
// "false". LIVE_READS=1 was off; EXPLAIN_GREEN=no was on.
func TestEveryBooleanSettingAcceptsTheSameWords(t *testing.T) {
	onWords := []string{"1", "t", "true", "TRUE", "yes", "on"}
	offWords := []string{"0", "f", "false", "FALSE", "no", "off"}

	get := func(t *testing.T, k string, def bool) bool {
		t.Helper()
		v, err := envBool(k, def)
		if err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		return v
	}

	// Off by default: absent means off, and every on-word turns it on.
	for _, k := range []string{"GIT_INSECURE_SKIP_TLS_VERIFY", "GATE_FORK_PRS", "LIVE_READS"} {
		if get(t, k, false) {
			t.Errorf("%s: unset must stay off", k)
		}
		for _, w := range onWords {
			t.Setenv(k, w)
			if !get(t, k, false) {
				t.Errorf("%s=%s must be on", k, w)
			}
		}
	}

	// On by default: absent means on, and every off-word turns it off.
	for _, k := range []string{"EXPLAIN_GREEN", "MIGRATE_DROPPED_VERSIONS", "UPSTREAM_NOTES",
		"STRUCTURAL_MIGRATION", "SUPERVISE_PIPELINE"} {
		if !get(t, k, true) {
			t.Errorf("%s: unset must stay on", k)
		}
		for _, w := range offWords {
			t.Setenv(k, w)
			if get(t, k, true) {
				t.Errorf("%s=%s must be off", k, w)
			}
		}
	}
}

// A typo used to read as false, so a setting somebody deliberately turned on
// was silently off and nothing said so.
func TestABooleanTypoIsAConfigurationError(t *testing.T) {
	for _, w := range []string{"treu", "ON!", "2", "maybe", "tru"} {
		t.Setenv("EXPLAIN_GREEN", w)
		if _, err := envBool("EXPLAIN_GREEN", true); err == nil {
			t.Errorf("EXPLAIN_GREEN=%q: want a configuration error, got none", w)
		}
	}
}

// Each of these three is validated in one switch and dispatched in another, in
// a different file. A value the validator accepts and the dispatcher does not
// is a pod that starts healthy and then does nothing, so the two sets have to
// be the same, and a named type is what lets a test say so.

// Each of these two is validated in one switch and dispatched in another, in a
// different file. A value the validator accepts and the dispatcher does not is
// a pod that starts healthy and then does nothing, so the two sets have to be
// the same, and a named type is what lets a test say so.
func TestTheValidatorAcceptsExactlyTheValuesThatDispatch(t *testing.T) {
	base := func() *Config {
		return &Config{
			GitOwner: "o", GitRepo: "r", GitRepoURL: "u", GitToken: "t",
			GitProvider: GitGitHub,
			LLMProvider: LLMAnthropic, LLMModel: "m",
			AllowPaths:    []string{"addons/**"},
			ArgoCDBaseURL: "https://argocd-server.argocd.svc", ArgoCDToken: "tok",
		}
	}

	for _, p := range []GitProviderName{GitGitHub, GitGitea} {
		c := base()
		c.GitProvider = p
		if p == GitGitea {
			// Gitea has no public API to default to; see the dedicated test
			// below. This one is about dispatch parity, not about that rule.
			c.GitAPIBase = "https://gitea.example.com"
		}
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

}

// The gate reads its inventory from the ArgoCD API and from nowhere else, so
// both halves of that credential are required, and both are things an
// operator forgets. Naming them at start-up is the difference between a pod
// that will not start and an `error` status on every pull request, discovered
// by whoever tries to merge next.
func TestTheInventoryNeedsAnArgoCDURLAndAToken(t *testing.T) {
	base := func() *Config {
		return &Config{
			GitOwner: "o", GitRepo: "r", GitRepoURL: "u", GitToken: "t",
			GitProvider: GitGitHub,
			LLMProvider: LLMAnthropic, LLMModel: "m",
			AllowPaths:  []string{"addons/**"},
			ArgoCDToken: "tok", ArgoCDBaseURL: "https://argocd-server.argocd.svc",
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
}

// GitHub defaults to the public API when GIT_API_BASE is empty. Gitea has no
// public instance to default to, so an empty base URL is a pod that starts
// healthy and then cannot read a pull request or push a fix, and PushFix says
// so only once a fix is ready to push, minutes and one model call later.
func TestGiteaRequiresAnAPIBase(t *testing.T) {
	base := func() *Config {
		return &Config{
			GitOwner: "o", GitRepo: "r", GitRepoURL: "u", GitToken: "t",
			GitProvider: GitGitea,
			LLMProvider: LLMAnthropic, LLMModel: "m",
			AllowPaths:    []string{"addons/**"},
			ArgoCDBaseURL: "https://argocd-server.argocd.svc", ArgoCDToken: "tok",
		}
	}
	if err := base().validate(); err == nil {
		t.Error("gitea with no GIT_API_BASE must not start")
	}
	c := base()
	c.GitAPIBase = "https://gitea.example.com"
	if err := c.validate(); err != nil {
		t.Errorf("gitea with a base URL must start: %v", err)
	}
	// The rule is Gitea's alone: GitHub's default is the public API.
	c = base()
	c.GitProvider = GitGitHub
	if err := c.validate(); err != nil {
		t.Errorf("github with no GIT_API_BASE must start: %v", err)
	}
}

// A credential in an environment variable is readable by `kubectl exec -- env`
// and by every process the agent spawns, and it spawns git and helm. Each one
// also loads from a file, and the file has to behave the same way the
// environment variable does once it is loaded, or the fix is a new class of
// outage rather than a smaller one.
func TestEveryCredentialLoadsFromAFile(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Every credential this file reads. A new one that is not here is a new
	// one that only arrives through the environment.
	for _, k := range []string{"GIT_TOKEN", "GITHUB_APP_PRIVATE_KEY", "LLM_API_KEY",
		"ARGOCD_TOKEN", "PROMOTION_TOKEN"} {
		t.Run(k, func(t *testing.T) {
			// A mounted Secret ends in a newline and `echo -n` does not. A
			// token with a stray \n is rejected exactly the way a wrong token
			// is, so the newline goes and nothing else does.
			t.Setenv(k+"_FILE", write(t, k, "s3cret\n"))
			v, err := envSecret(k)
			if err != nil {
				t.Fatalf("a readable file must load: %v", err)
			}
			if v != "s3cret" {
				t.Fatalf("want the value with its trailing newline gone, got %q", v)
			}

			// Unreadable is not empty. Falling back would reach validate() as
			// "missing required configuration", which points at the Secret key
			// and never at the volume.
			t.Setenv(k+"_FILE", filepath.Join(dir, "not-mounted"))
			if _, err := envSecret(k); err == nil {
				t.Error("a file that cannot be read must fail at start-up")
			}

			// So is empty: a credential that reads as "not configured" when
			// somebody configured it is the failure worth naming.
			t.Setenv(k+"_FILE", write(t, k+".empty", "\n"))
			if _, err := envSecret(k); err == nil {
				t.Error("an empty credential file must fail at start-up")
			}

			// Both forms set is a question with no right answer.
			t.Setenv(k+"_FILE", write(t, k, "s3cret\n"))
			t.Setenv(k, "from-the-environment")
			if _, err := envSecret(k); err == nil {
				t.Error("both forms set must be a configuration error")
			}

			// And the environment alone still works, because an operator
			// upgrading into this has not mounted anything yet.
			_ = os.Unsetenv(k + "_FILE")
			if v, err := envSecret(k); err != nil || v != "from-the-environment" {
				t.Fatalf("the plain variable must keep working, got %q, %v", v, err)
			}
		})
	}
}

// The file form has to reach the fields, not just the helper: a credential
// wired to os.Getenv is one nobody can move out of the environment.
func TestTheFileFormReachesEveryCredentialField(t *testing.T) {
	dir := t.TempDir()
	file := func(t *testing.T, name, content string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for _, k := range []string{"GIT_TOKEN", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY",
		"LLM_API_KEY", "ARGOCD_TOKEN", "PROMOTION_TOKEN"} {
		_ = os.Unsetenv(k)
	}
	for k, v := range map[string]string{
		"GIT_OWNER": "o", "GIT_REPO": "r",
		"GIT_REPO_URL": "https://example.invalid/o/r.git",
		"LLM_PROVIDER": "openai", "LLM_MODEL": "m",
		"LLM_BASE_URL": "http://model.invalid/v1",
		"ALLOW_PATHS":  "addons/**",

		"ARGOCD_BASE_URL":             "https://argocd-server.argocd.svc",
		"GIT_TOKEN_FILE":              file(t, "git-token", "git-tok\n"),
		"LLM_API_KEY_FILE":            file(t, "llm-key", "llm-key\n"),
		"ARGOCD_TOKEN_FILE":           file(t, "argocd-token", "argocd-tok\n"),
		"PROMOTION_TOKEN_FILE":        file(t, "promotion-token", "promo-tok\n"),
		"GITHUB_APP_PRIVATE_KEY_FILE": file(t, "app-key", "-----BEGIN...\n"),
	} {
		t.Setenv(k, v)
	}

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("a config whose credentials are all mounted must load: %v", err)
	}
	for _, got := range []struct{ field, want, have string }{
		{"GitToken", "git-tok", c.GitToken},
		{"LLMKey", "llm-key", c.LLMKey},
		{"ArgoCDToken", "argocd-tok", c.ArgoCDToken},
		{"PromotionToken", "promo-tok", c.PromotionToken},
		{"AppPrivateKey", "-----BEGIN...", c.AppPrivateKey},
	} {
		if got.have != got.want {
			t.Errorf("%s: want %q from the mounted file, got %q", got.field, got.want, got.have)
		}
	}

	// And a mount that is not there stops the pod rather than starting one
	// that reports every pull request as an error.
	t.Setenv("GIT_TOKEN_FILE", filepath.Join(dir, "not-mounted"))
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "GIT_TOKEN_FILE") {
		t.Errorf("an unreadable credential file must name itself: %v", err)
	}
}

// TestTheOnlyWayPastAClosedNetworkIsNamingIt covers the one setting whose
// absence is a refusal rather than a default.
//
// egress.DefaultDenyNetworks is closed whatever EGRESS_DENY says, so an
// operator running an internal chart repository has exactly one way to reach
// it, and it is this variable. If it stops being read the symptom is not a
// failed start-up: it is a pod that runs, refuses every lookup to the internal
// registry at the dial, and reports pull requests it could not read the
// upstream for.
func TestTheOnlyWayPastAClosedNetworkIsNamingIt(t *testing.T) {
	base := map[string]string{
		"GIT_OWNER": "o", "GIT_REPO": "r",
		"GIT_REPO_URL": "https://example.invalid/o/r.git",
		"GIT_TOKEN":    "tok",
		"LLM_PROVIDER": "openai", "LLM_MODEL": "m",
		"LLM_BASE_URL": "http://model.invalid/v1",
		"LLM_API_KEY":  "k",
		"ALLOW_PATHS":  "addons/**",

		"ARGOCD_BASE_URL": "https://argocd-server.argocd.svc",
		"ARGOCD_TOKEN":    "argocd-tok",
	}

	t.Run("unset stays closed", func(t *testing.T) {
		for k, v := range base {
			t.Setenv(k, v)
		}
		t.Setenv("EGRESS_ALLOW_PRIVATE", "")
		c, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		if len(c.EgressAllowPrivate) != 0 {
			t.Errorf("no network is open by default, got %v", c.EgressAllowPrivate)
		}
	})

	t.Run("named networks reach the policy", func(t *testing.T) {
		for k, v := range base {
			t.Setenv(k, v)
		}
		t.Setenv("EGRESS_ALLOW_PRIVATE", "10.42.0.0/16,192.168.1.7")
		c, err := LoadConfig()
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"10.42.0.0/16", "192.168.1.7"}
		if !slices.Equal(c.EgressAllowPrivate, want) {
			t.Errorf("want %v, got %v", want, c.EgressAllowPrivate)
		}
		// The value is only worth reading if the package it feeds agrees the
		// networks are the ones it had closed. This is the seam between the
		// two, and it is the half a chart typo would break silently.
		p := egress.Policy{AllowPrivate: c.EgressAllowPrivate}
		if _, denied := p.Denied("10.42.0.9"); denied {
			t.Error("a named network must be reachable")
		}
		if _, denied := p.Denied("169.254.169.254"); !denied {
			t.Error("naming one network must not open the others")
		}
	})
}
