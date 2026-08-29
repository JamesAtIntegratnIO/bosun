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

// Each of these two is validated in one switch and dispatched in another, in a
// different file. A value the validator accepts and the dispatcher does not is
// a pod that starts healthy and then does nothing -- so the two sets have to be
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
// both halves of that credential are required -- and both are things an
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
