package main

import (
	"strings"
	"testing"
)

// The status page's repository link carries no credential.
//
// It used to. GIT_REPO_URL went to the page verbatim, so an install that had a
// token in that URL published it as an href on the one surface built for a
// person to look at, on a listener whose whole design is that it is safe to
// expose. Nothing about the link said so.
func TestTheRepositoryLinkCarriesNoCredential(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{
			name: "a password in the userinfo never reaches the page",
			in:   "https://someone:hunter2@git.example.com/o/r.git",
			want: "https://git.example.com/o/r",
		},
		{
			name: "nor a token written as the whole userinfo",
			in:   "https://ghp_0123456789abcdef@github.com/o/r.git",
			want: "https://github.com/o/r",
		},
		{
			name: "an ordinary URL is unchanged",
			in:   "https://github.com/o/r.git",
			want: "https://github.com/o/r",
		},
		{
			// ssh is not a link a browser can follow, and was never rendered.
			name: "an ssh remote is still no link at all",
			in:   "ssh://git@github.com/o/r.git",
			want: "",
		},
		{
			name: "scp-style is still no link at all",
			in:   "git@github.com:o/r.git",
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := repoLink(tc.in)
			if got != tc.want {
				t.Errorf("repoLink(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, secret := range []string{"hunter2", "ghp_0123456789abcdef"} {
				if strings.Contains(got, secret) {
					t.Errorf("the credential is in a link a browser will render: %q", got)
				}
			}
		})
	}
}
