package upstream

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Where maintainers actually write down what they changed, in the order this
// asks.
//
//	RELEASES    a GitHub Release object per version. The richest source and the
//	            least reliable: creating one is a separate, optional step that
//	            plenty of projects never take. This repository has 8 tags and 0
//	            releases.
//	CHANGELOG   a file in the repository. Kept by most projects that keep
//	            anything, updated in the same commit as the change, and -- for
//	            a chart -- frequently the ONLY place the chart's own version
//	            numbers are described at all.
//	COMMITS     what they did rather than what they wrote. Always available,
//	            never polished. Handled by Compare.
//
// All three are TESTIMONY. The gate's render is the only computed fact in a
// brief, and a changelog is a claim like the rest -- with one wrinkle worth
// stating: it is read at the default branch, so an entry can have been edited
// after the release it describes. That is a smaller risk than it sounds (an
// edited changelog is usually a corrected one) and it is why the provenance
// line says which source an explanation had.

// changelogCandidates are the paths worth trying, in order.
//
// The chart's own changelog comes first for a chart artifact, and that ordering
// is the point rather than a nicety: a chart's version numbers and the
// application's are different sequences, and a repository that publishes both
// has a file for each. Reading the root changelog for a chart bump answers with
// the wrong project's versions -- confidently, and in the right shape.
func changelogCandidates(chart string) []string {
	var out []string
	if chart != "" {
		out = append(out, "charts/"+chart+"/CHANGELOG.md", "chart/"+chart+"/CHANGELOG.md")
	}
	return append(out, "CHANGELOG.md", "CHANGES.md", "HISTORY.md", "docs/CHANGELOG.md")
}

// changelogHeading matches a markdown heading that names a version.
//
// Deliberately tolerant. `## [1.2.3] - 2026-08-24` is Keep a Changelog, and it
// is far from universal: `## v1.2.3`, `# 1.2.3 (2026-08-24)` and
// `## Release 1.2.3` are all in the wild. What every one of them has is a
// heading line containing a version-shaped token, so that is what this looks
// for -- and a heading with no version in it is a section title like
// "Unreleased" or "Added", correctly ignored.
var changelogHeading = regexp.MustCompile(`^(#{1,3})\s+.*?v?(\d+\.\d+(?:\.\d+)?)`)

type changelogSection struct {
	Version string
	Body    string
}

// parseChangelog splits a changelog into version sections.
//
// A section runs until the next heading AT THE SAME LEVEL OR HIGHER, so the
// `### Fixed` sub-headings inside an entry stay part of it. Matching any
// heading would truncate every entry at its first subsection, which is where
// the content is.
func parseChangelog(text string) []changelogSection {
	lines := strings.Split(text, "\n")
	var out []changelogSection
	var cur *changelogSection
	curLevel := 0
	var body []string

	flush := func() {
		if cur != nil {
			cur.Body = strings.TrimSpace(strings.Join(body, "\n"))
			out = append(out, *cur)
		}
		body = nil
	}

	for _, line := range lines {
		if m := changelogHeading.FindStringSubmatch(line); m != nil {
			flush()
			curLevel = len(m[1])
			cur = &changelogSection{Version: m[2]}
			continue
		}
		if cur != nil && strings.HasPrefix(line, "#") {
			// A heading with no version. Ends the section only if it is at the
			// same level or higher -- otherwise it is this entry's own "Fixed".
			level := len(line) - len(strings.TrimLeft(line, "#"))
			if level <= curLevel {
				flush()
				cur = nil
				continue
			}
		}
		if cur != nil {
			body = append(body, line)
		}
	}
	flush()
	return out
}

// changelogNotes reads the repository's changelog and returns the entries in
// (from, to].
//
// Bounded: at most one file is read in full, from a short candidate list, and
// the first that yields an entry in range wins. Never an error -- an absent
// changelog is the ordinary case and is reported by returning nothing.
func (g *GitHubReleases) changelogNotes(ctx context.Context, repo, chart, lo, hi string) ([]Release, string) {
	maxSections := g.MaxReleases
	if maxSections <= 0 {
		maxSections = 5
	}
	limit := g.MaxBodyChars
	if limit <= 0 {
		limit = 4000
	}

	found := ""
	for _, path := range changelogCandidates(chart) {
		text, url, err := g.repoFile(ctx, repo, path)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		found = path

		var picked []Release
		for _, sec := range parseChangelog(text) {
			if !inRange(normalise(sec.Version), lo, hi) {
				continue
			}
			body := sec.Body
			if len(body) > limit {
				body = body[:limit] + "\n\n[truncated]"
			}
			picked = append(picked, Release{
				Tag:  sec.Version,
				Name: path,
				Body: body,
				URL:  url,
			})
		}
		if len(picked) == 0 {
			continue
		}
		// Newest first, to match the release path and because a reader's
		// attention is finite and the newest entry is the one that matters.
		sort.Slice(picked, func(i, j int) bool {
			return cmpVer(normalise(picked[i].Tag), normalise(picked[j].Tag)) > 0
		})
		if len(picked) > maxSections {
			picked = picked[:maxSections]
		}
		return picked, path
	}
	return nil, found
}

// repoFile reads one file from a repository's default branch.
//
// The default branch rather than the tag, deliberately: it is one request
// instead of two, and a changelog's historical entries are all present there.
// The cost is that an entry may have been edited after its release, which is
// why the provenance line names the source.
func (g *GitHubReleases) repoFile(ctx context.Context, repo, path string) (text, url string, err error) {
	u := fmt.Sprintf("%s/repos/%s/contents/%s", g.apiBase(), repo, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", "", err
	}
	g.authorise(ctx, req)
	req.Header.Set("Accept", "application/vnd.github+json")

	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
		HTMLURL  string `json:"html_url"`
	}
	if err := g.getJSONReq(req, &out); err != nil {
		return "", "", err
	}
	if out.Encoding != "base64" {
		// A file over a megabyte comes back with empty content and a pointer
		// to the blob API. Not worth a second endpoint for a changelog.
		return "", out.HTMLURL, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
	if err != nil {
		return "", out.HTMLURL, err
	}
	return string(raw), out.HTMLURL, nil
}

// chartNameOf is the last path segment of an OCI reference, which for a chart
// repository is the chart's name -- and therefore the directory its own
// changelog lives in.
// chartNameOf is the chart's name, for finding its own CHANGELOG.md.
//
// The promotion names it outright for a classic Helm repository. For an OCI
// chart the name field is empty and the last path segment IS the chart, which
// is how `helm push` addresses it.
func chartNameOf(artifact string) string {
	ref, chart := ParseArtifact(artifact)
	if chart != "" {
		return chart
	}
	if IsHelmRepo(ref) {
		return ""
	}
	_, repo, _ := splitRef(ref)
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}
