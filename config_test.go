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
			GateMode:   "ci",
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
