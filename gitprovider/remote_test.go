package gitprovider

import (
	"slices"
	"strings"
	"testing"
)

// A remote is the URL git is given plus the environment that authenticates it,
// and the credential is in neither the URL nor anything that reaches argv.
func TestNewRemoteKeepsTheCredentialOutOfTheURL(t *testing.T) {
	for _, tc := range []struct {
		name     string
		raw      string
		wantURL  string
		wantAuth bool
		// what the Authorization header must be built from, user:password
		wantBasic string
	}{
		{
			name:      "a password in the userinfo authenticates, and leaves the URL",
			raw:       "https://someone:hunter2@git.example.com/o/r.git",
			wantURL:   "https://git.example.com/o/r.git",
			wantAuth:  true,
			wantBasic: "someone:hunter2",
		},
		{
			// How a forge writes a token into a clone URL. git sends it as the
			// username with an empty password, and so must this.
			name:      "a lone username is a token, and is sent as one",
			raw:       "https://ghp_0123456789abcdef@github.com/o/r.git",
			wantURL:   "https://github.com/o/r.git",
			wantAuth:  true,
			wantBasic: "ghp_0123456789abcdef:",
		},
		{
			// git decodes the userinfo before it builds the header, so the
			// credential on the wire is the decoded one.
			name:      "a percent-encoded password is decoded, the way git decodes it",
			raw:       "https://someone:p%40ss%2Fword@git.example.com/o/r.git",
			wantURL:   "https://git.example.com/o/r.git",
			wantAuth:  true,
			wantBasic: "someone:p@ss/word",
		},
		{
			name:    "no userinfo, nothing to authenticate with",
			raw:     "https://github.com/o/r.git",
			wantURL: "https://github.com/o/r.git",
		},
		{
			// ssh carries its own credential and this one would be ignored
			// rather than refused. Untouched, both halves.
			name:    "an ssh remote is left exactly as it was",
			raw:     "ssh://git@github.com/o/r.git",
			wantURL: "ssh://git@github.com/o/r.git",
		},
		{
			name:    "an scp-style remote is left exactly as it was",
			raw:     "git@github.com:o/r.git",
			wantURL: "git@github.com:o/r.git",
		},
		{
			name:    "unset stays unset",
			raw:     "",
			wantURL: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRemote(tc.raw)

			if got := r.URL(); got != tc.wantURL {
				t.Errorf("URL() = %q, want %q", got, tc.wantURL)
			}
			if strings.Contains(r.URL(), "hunter2") || strings.Contains(r.URL(), "ghp_") ||
				strings.Contains(r.URL(), "p%40ss") {
				t.Errorf("the credential is still in the URL git is given: %q", r.URL())
			}

			env := r.Env()
			if !tc.wantAuth {
				if len(env) != 0 {
					t.Errorf("nothing to authenticate with, and Env() is %q", env)
				}
				return
			}
			if len(env) == 0 {
				t.Fatal("a credential was configured and Env() authenticates with nothing")
			}
			// The header git will send, built the way the push path builds it.
			want := basicAuth(tc.wantBasic)
			if !slices.ContainsFunc(env, func(e string) bool { return strings.Contains(e, want) }) {
				t.Errorf("Env() does not carry the credential git needs.\ngot  %q\nwant one entry containing %q", env, want)
			}
		})
	}
}

// And nothing in Env() reaches argv, which is the whole point of this type.
//
// A test that only checked URL() would pass on an implementation that put the
// credential back on the command line as `-c http.…extraHeader=…`, which is
// exactly the shape this is replacing.
func TestRemoteEnvIsEnvironmentAndNotArguments(t *testing.T) {
	r := NewRemote("https://someone:hunter2@git.example.com/o/r.git")
	for _, e := range r.Env() {
		name, _, ok := strings.Cut(e, "=")
		if !ok {
			t.Errorf("%q is not a NAME=VALUE environment entry, so it cannot be one", e)
		}
		if strings.HasPrefix(name, "-") {
			t.Errorf("%q is a command-line flag wearing an environment entry's shape", e)
		}
	}
}
