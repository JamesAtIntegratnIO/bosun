package upstream

import "testing"

func TestNormaliseHandlesTheTagSchemesUpstreamsActuallyUse(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"v1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3"},
		{"chart-1.2.3", "1.2.3"},
		{"release-2026.8.0", "2026.8.0"},
		{"kargo-1.11.2", "1.11.2"},
		{"main", ""},   // not a version; must not be guessed at
		{"latest", ""}, //
	} {
		if got := normalise(tc.in); got != tc.want {
			t.Errorf("normalise(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// (from, to]: the version being left behind is not news, the one being adopted
// is, and so is everything skipped over on the way.
func TestRangeIsExclusiveOfFromAndInclusiveOfTo(t *testing.T) {
	lo, hi := normalise("1.2.0"), normalise("1.4.0")
	for _, tc := range []struct {
		v    string
		want bool
	}{
		{"1.2.0", false}, // the version we are leaving
		{"1.2.1", true},
		{"1.3.9", true},
		{"1.4.0", true}, // the version we are adopting
		{"1.4.1", false},
		{"0.9.0", false},
		{"main", false}, // uncomparable: skipped, never guessed
	} {
		if got := inRange(normalise(tc.v), lo, hi); got != tc.want {
			t.Errorf("inRange(%q, 1.2.0, 1.4.0) = %v, want %v", tc.v, got, tc.want)
		}
	}
}

// The prerelease trap, which CLAUDE.md records for Kargo's own subscriptions
// and which this hit for real: numeric comparison reads v2.13.0-rc5 as
// 2.13.0.5, which sorts ABOVE 2.13.0 and lands inside a 2.13.0 -> 2.13.2
// range. Caught by running it against argo-cd, not by reading the code.
func TestAPrereleaseSortsAboveItsOwnRelease(t *testing.T) {
	if cmpVer(normalise("v2.13.0-rc5"), normalise("v2.13.0")) <= 0 {
		t.Fatal("this test encodes the trap; if it fails the trap is gone and " +
			"the prerelease filter may no longer be needed")
	}
	if !looksPrerelease("v2.13.0-rc5") {
		t.Error("rc must be recognised as a prerelease")
	}
	if looksPrerelease("v2.13.0") {
		t.Error("a stable version must not read as a prerelease")
	}
}

func TestSplitRefAcceptsWhatAPipelineActuallyNames(t *testing.T) {
	for _, tc := range []struct{ in, host, repo, tag string }{
		{"ghcr.io/owner/name", "ghcr.io", "owner/name", "latest"},
		{"oci://ghcr.io/akuity/kargo-charts/kargo", "ghcr.io", "akuity/kargo-charts/kargo", "latest"},
		{"quay.io/argoproj/argocd:v2.13.2", "quay.io", "argoproj/argocd", "v2.13.2"},
		{"ghcr.io/o/n@sha256:abc", "ghcr.io", "o/n", "sha256:abc"},
		// No host is REFUSED rather than assumed to be Docker Hub. Guessing a
		// registry is the same mistake as guessing a repository.
		{"nginx", "", "", ""},
		{"library/nginx", "", "", ""},
	} {
		h, r, g := splitRef(tc.in)
		if h != tc.host || r != tc.repo || g != tc.tag {
			t.Errorf("splitRef(%q) = (%q,%q,%q), want (%q,%q,%q)", tc.in, h, r, g, tc.host, tc.repo, tc.tag)
		}
	}
}

// A source URL that is not GitHub is a good answer to "where is this from" and
// a useless one here. Refusing it is what stops a GitLab project's URL being
// bent into a GitHub API call that 404s or, worse, hits a same-named repo.
func TestGithubPathRefusesWhatItCannotRead(t *testing.T) {
	for _, ok := range []string{
		"https://github.com/akuity/kargo",
		"https://github.com/akuity/kargo.git",
		"git@github.com:akuity/kargo.git",
	} {
		if got, err := githubPath(ok); err != nil || got != "akuity/kargo" {
			t.Errorf("githubPath(%q) = %q, %v", ok, got, err)
		}
	}
	for _, bad := range []string{
		"https://gitlab.com/owner/repo",
		"https://gitea.example.com/owner/repo",
		"not a url",
		"https://github.com/owner",
	} {
		if _, err := githubPath(bad); err == nil {
			t.Errorf("githubPath(%q) should have been refused", bad)
		}
	}
}
