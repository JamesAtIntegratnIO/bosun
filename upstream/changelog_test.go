package upstream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const keepAChangelog = `# Changelog

All notable changes. Format follows Keep a Changelog.

## [Unreleased]

- something in progress

## [0.13.0] - 2026-08-24

### Added

- Structure from the schema, data from the document.

### Changed

- The agent image carries helm.

## [0.12.0] - 2026-08-24

### Added

- liveReads, off by default.

## [0.9.3] - 2026-08-01

### Fixed

- The legacy author is ignored.
`

// A section runs to the next heading at the same level or higher. Ending it at
// any heading would truncate every entry at its first `### Added`, which is
// where the content is; the entry would become its own blank line.
func TestAnEntryKeepsItsSubsections(t *testing.T) {
	secs := parseChangelog(keepAChangelog)
	byVersion := map[string]string{}
	for _, s := range secs {
		byVersion[s.Version] = s.Body
	}
	got := byVersion["0.13.0"]
	for _, want := range []string{"### Added", "Structure from the schema", "### Changed", "carries helm"} {
		if !strings.Contains(got, want) {
			t.Errorf("0.13.0 lost %q:\n%s", want, got)
		}
	}
	// And it stops at the next version rather than swallowing it.
	if strings.Contains(got, "liveReads") {
		t.Errorf("0.13.0 swallowed the next entry:\n%s", got)
	}
}

// "Unreleased" is a heading with no version. It is a section title, not an
// entry, and treating it as one would attach in-progress work to a release.
func TestAHeadingWithNoVersionIsNotAnEntry(t *testing.T) {
	for _, s := range parseChangelog(keepAChangelog) {
		if s.Version == "" || strings.Contains(strings.ToLower(s.Body), "something in progress") {
			t.Fatalf("picked up a non-version heading: %+v", s)
		}
	}
}

// Keep a Changelog is far from universal. What every style has is a heading
// containing a version-shaped token.
func TestTheHeadingStylesProjectsActuallyUse(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"## [1.2.3] - 2026-08-24", "1.2.3"},
		{"## v1.2.3", "1.2.3"},
		{"# 1.2.3 (2026-08-24)", "1.2.3"},
		{"### Release 2.0", "2.0"},
		{"## Unreleased", ""},
		{"### Added", ""},
	} {
		secs := parseChangelog(tc.line + "\nbody\n")
		got := ""
		if len(secs) > 0 {
			got = secs[0].Version
		}
		if got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.line, got, tc.want)
		}
	}
}

// changelogFile serves one path and 404s everything else, so a test can assert
// which candidate was taken.
func changelogServer(t *testing.T, files map[string]string) (*GitHubReleases, *[]string) {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const marker = "/contents/"
		i := strings.Index(r.URL.Path, marker)
		if i < 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		path := r.URL.Path[i+len(marker):]
		asked = append(asked, path)
		body, ok := files[path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"content":  base64.StdEncoding.EncodeToString([]byte(body)),
			"encoding": "base64",
			"html_url": "https://example.invalid/" + path,
		})
	}))
	t.Cleanup(srv.Close)
	return &GitHubReleases{APIBase: srv.URL, HTTP: srv.Client(), MaxReleases: 5, MaxBodyChars: 4000}, &asked
}

// A chart's version numbers and its application's are different sequences, and
// a repository that publishes both has a file for each. Reading the root
// changelog for a chart bump answers with the wrong project's versions,
// confidently, and in exactly the right shape.
func TestAChartsOwnChangelogIsPreferredToTheRepositorys(t *testing.T) {
	g, asked := changelogServer(t, map[string]string{
		"charts/bosun/CHANGELOG.md": "## [0.13.0]\n\nthe chart's own entry\n",
		"CHANGELOG.md":              "## [0.13.0]\n\nthe application's entry\n",
	})

	got, origin := g.changelogNotes(context.Background(), "org/repo", "bosun",
		normalise("0.12.0"), normalise("0.13.0"))
	if len(got) != 1 || !strings.Contains(got[0].Body, "the chart's own entry") {
		t.Fatalf("got %+v from %q", got, origin)
	}
	if (*asked)[0] != "charts/bosun/CHANGELOG.md" {
		t.Errorf("asked for %v first", (*asked)[0])
	}
}

// Only entries in (from, to]. The version being left is not news.
func TestOnlyTheEntriesInRangeAreTaken(t *testing.T) {
	g, _ := changelogServer(t, map[string]string{"CHANGELOG.md": keepAChangelog})

	got, _ := g.changelogNotes(context.Background(), "org/repo", "",
		normalise("0.9.3"), normalise("0.13.0"))
	var versions []string
	for _, r := range got {
		versions = append(versions, r.Tag)
	}
	if len(versions) != 2 || versions[0] != "0.13.0" || versions[1] != "0.12.0" {
		t.Fatalf("versions = %v, want newest-first 0.13.0, 0.12.0 and NOT the 0.9.3 being left", versions)
	}
}

// A repository with no changelog is ordinary, and the walk is bounded.
func TestNoChangelogIsNotAnError(t *testing.T) {
	g, asked := changelogServer(t, map[string]string{})
	got, origin := g.changelogNotes(context.Background(), "org/repo", "thing",
		normalise("1.0.0"), normalise("2.0.0"))
	if len(got) != 0 || origin != "" {
		t.Fatalf("invented %+v from %q", got, origin)
	}
	if len(*asked) > 6 {
		t.Errorf("tried %d paths: %v", len(*asked), *asked)
	}
}
