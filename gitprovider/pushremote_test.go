package gitprovider

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// APIBase has supported GitHub Enterprise since this provider was written, so
// an Enterprise deployment read its pull requests from its own host and pushed
// the fix to github.com, failing outright if no such repository exists, and
// pushing an unreviewed commit to a stranger's repository if `owner/repo`
// happens to be taken there.
func TestThePushGoesWhereTheCloneCameFrom(t *testing.T) {
	for _, tc := range []struct {
		name, repoURL, want string
		wantErr             bool
	}{
		{
			name:    "enterprise",
			repoURL: "https://github.example.com/platform/addons.git",
			want:    "https://github.example.com/platform/addons.git",
		},
		{
			name:    "public github, spelled out",
			repoURL: "https://github.com/o/r.git",
			want:    "https://github.com/o/r.git",
		},
		{
			name:    "unset falls back to the public host",
			repoURL: "",
			want:    "https://github.com/o/r.git",
		},
		{
			// The credential would be ignored rather than refused, which is a
			// push that silently authenticates as whatever key the pod holds.
			name:    "ssh remotes cannot carry this credential",
			repoURL: "git@github.com:o/r.git",
			wantErr: true,
		},
		{
			// It would be the one credential left in argv, and git matches
			// the user as part of a URL, so the scoped config key carrying
			// the real one would stop matching too.
			name:    "a credential already in the URL is dropped",
			repoURL: "https://someone:hunter2@github.example.com/o/r.git",
			want:    "https://github.example.com/o/r.git",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &GitHub{Owner: "o", Repo: "r", RepoURL: tc.repoURL}
			got, err := g.pushRemote()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want a refusal, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A configured Enterprise host must never appear alongside github.com in the
// remote; the failure this test is named for was exactly that substitution.
func TestAnEnterpriseRemoteNeverMentionsPublicGitHub(t *testing.T) {
	g := &GitHub{
		APIBase: "https://github.example.com/api/v3",
		RepoURL: "https://github.example.com/platform/addons.git",
		Owner:   "platform", Repo: "addons",
	}
	got, err := g.pushRemote()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "github.com/") && !strings.Contains(got, "github.example.com/") {
		t.Errorf("the push targets public GitHub: %q", got)
	}
}

// Gitea pushes to the instance it read the pull request from, and over http
// only: an Authorization header sent over ssh is ignored rather than refused,
// which is the same silent authentication as somebody else that the GitHub
// side already refuses.
func TestTheGiteaPushGoesToTheConfiguredInstance(t *testing.T) {
	g := &Gitea{BaseURL: "https://gitea.example.com/", Owner: "o", Repo: "r"}
	got, err := g.pushRemote()
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://gitea.example.com/o/r.git"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := (&Gitea{BaseURL: "ssh://gitea.example.com", Owner: "o", Repo: "r"}).pushRemote(); err == nil {
		t.Error("an ssh BaseURL must be refused: the header would be ignored, not applied")
	}
}

// shimGit puts a fake git first on PATH that appends its own argv and
// environment to a file, so a test can read exactly what the operating system
// would have seen. Anything else, a captured remote string or a mocked runner,
// would be testing the code's opinion of its command line rather than the
// command line.
func shimGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	shim := "#!/bin/sh\n" +
		"{ printf 'argv:'; for a in \"$@\"; do printf ' %s' \"$a\"; done; printf '\\n'\n" +
		"  env | sed 's/^/env: /'; } >> \"$BOSUN_TEST_RECORD\"\n" +
		// headSHA runs `rev-parse HEAD` and wants an object name back.
		"echo 0000000000000000000000000000000000000000\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOSUN_TEST_RECORD", record)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

// The push credential used to be spelled into the remote URL and handed to
// `git push` as an argument. /proc/<pid>/cmdline is world-readable, so for the
// length of the push a live installation token was there for `ps` and for
// anything that logs a command line.
//
// The guarantee is now the absence: no argument of any command either provider
// runs contains the token, and the header that replaces it arrives in the
// environment, scoped to the one remote it authenticates.
func TestThePushCredentialNeverReachesArgv(t *testing.T) {
	const token = "ghs_theliveinstallationtoken"
	for _, tc := range []struct {
		name, user, remote string
		p                  Provider
	}{
		{
			name:   "github",
			user:   "x-access-token",
			remote: "https://github.example.com/o/r.git",
			p: &GitHub{
				RepoURL: "https://github.example.com/o/r.git",
				Owner:   "o", Repo: "r", Token: token,
			},
		},
		{
			name:   "gitea",
			user:   "bosun",
			remote: "https://gitea.example.com/o/r.git",
			p: &Gitea{
				BaseURL: "https://gitea.example.com",
				Owner:   "o", Repo: "r", Token: token,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := shimGit(t)
			pr := &PullRequest{Branch: "bosun/fix-1"}
			if err := tc.p.PushFix(context.Background(), pr, t.TempDir(), "a message"); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}

			var argv, env []string
			for _, line := range strings.Split(string(raw), "\n") {
				switch {
				case strings.HasPrefix(line, "argv:"):
					argv = append(argv, strings.TrimPrefix(line, "argv:"))
				case strings.HasPrefix(line, "env: "):
					env = append(env, strings.TrimPrefix(line, "env: "))
				}
			}
			if len(argv) == 0 {
				t.Fatal("the shim recorded nothing; PushFix did not run git")
			}
			var pushed bool
			for _, line := range argv {
				if strings.Contains(line, token) {
					t.Errorf("the token is in a command line: %s", line)
				}
				if strings.Contains(line, " push ") {
					pushed = true
					if !strings.Contains(line, tc.remote) {
						t.Errorf("the push does not name the remote: %s", line)
					}
				}
			}
			if !pushed {
				t.Fatal("no push was run")
			}

			want := map[string]string{
				"GIT_CONFIG_COUNT": "1",
				"GIT_CONFIG_KEY_0": "http." + tc.remote + ".extraHeader",
				"GIT_CONFIG_VALUE_0": "Authorization: Basic " +
					base64.StdEncoding.EncodeToString([]byte(tc.user+":"+token)),
			}
			got := map[string]string{}
			for _, line := range env {
				k, v, ok := strings.Cut(line, "=")
				if ok && strings.HasPrefix(k, "GIT_CONFIG_") {
					got[k] = v
				}
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
			// An unscoped key attaches a bearer credential to every host git
			// is asked to contact, which is the other half of this fix.
			if got["GIT_CONFIG_KEY_0"] == "http.extraHeader" {
				t.Error("the header is not scoped to the remote")
			}
		})
	}
}
