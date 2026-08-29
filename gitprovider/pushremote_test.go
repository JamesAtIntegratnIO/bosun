package gitprovider

import (
	"strings"
	"testing"
)

// APIBase has supported GitHub Enterprise since this provider was written, so
// an Enterprise deployment read its pull requests from its own host and pushed
// the fix to github.com -- failing outright if no such repository exists, and
// pushing an unreviewed commit and a live installation token to a stranger's
// repository if `owner/repo` happens to be taken there.
func TestThePushGoesWhereTheCloneCameFrom(t *testing.T) {
	for _, tc := range []struct {
		name, repoURL, want string
		wantErr             bool
	}{
		{
			name:    "enterprise",
			repoURL: "https://github.example.com/platform/addons.git",
			want:    "https://x-access-token:tok@github.example.com/platform/addons.git",
		},
		{
			name:    "public github, spelled out",
			repoURL: "https://github.com/o/r.git",
			want:    "https://x-access-token:tok@github.com/o/r.git",
		},
		{
			name:    "unset falls back to the public host",
			repoURL: "",
			want:    "https://x-access-token:tok@github.com/o/r.git",
		},
		{
			// The credential would be ignored rather than refused, which is a
			// push that silently authenticates as whatever key the pod holds.
			name:    "ssh remotes cannot carry this credential",
			repoURL: "git@github.com:o/r.git",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &GitHub{Owner: "o", Repo: "r", RepoURL: tc.repoURL}
			got, err := g.pushRemote("tok")
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
// remote -- the failure this test is named for was exactly that substitution.
func TestAnEnterpriseRemoteNeverMentionsPublicGitHub(t *testing.T) {
	g := &GitHub{
		APIBase: "https://github.example.com/api/v3",
		RepoURL: "https://github.example.com/platform/addons.git",
		Owner:   "platform", Repo: "addons",
	}
	got, err := g.pushRemote("tok")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "github.com/") && !strings.Contains(got, "github.example.com/") {
		t.Errorf("the push targets public GitHub: %q", got)
	}
}
