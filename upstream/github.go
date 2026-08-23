package upstream

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// GitHubReleases reads what maintainers wrote in their GitHub releases.
//
// It needs no egress this agent did not already have: api.github.com is
// already permitted so the gate's checks can be read, and the token is the one
// that already comments. The registry hops in oci.go DO need egress to
// wherever the artifact is published, which is the only new network surface
// this feature asks for -- and it is a bounded list, because every artifact
// this pipeline promotes is named in the target list.
type GitHubReleases struct {
	// Token is optional. Unauthenticated GitHub allows 60 requests an hour per
	// IP, which one busy morning of promotions will exhaust; with a token it is
	// 5000. Release notes are public either way.
	Token string
	// APIBase defaults to https://api.github.com.
	APIBase string
	// HTTP is injectable for tests.
	HTTP *http.Client

	// MaxReleases caps how many releases reach a prompt. A bump that crosses
	// fourteen versions is exactly when the notes are most useful and least
	// affordable; the newest are the ones that matter.
	MaxReleases int
	// MaxBodyChars caps one release body. Some projects paste an entire commit
	// log into a release, which would crowd out the gate report the explanation
	// is actually grounded in.
	MaxBodyChars int
}

func (g *GitHubReleases) Name() string { return "github-releases" }

func (g *GitHubReleases) apiBase() string {
	if g.APIBase != "" {
		return strings.TrimSuffix(g.APIBase, "/")
	}
	return "https://api.github.com"
}

// Notes resolves the artifact to a repository and returns the releases that
// fall in (from, to].
//
// Never an error for "nothing found". A missing source label, a non-GitHub
// upstream, a project that publishes no releases -- all ordinary, all reported
// in Note so the explanation can say where its evidence stops.
func (g *GitHubReleases) Notes(ctx context.Context, artifact, from, to string) (*Notes, error) {
	repo, err := g.sourceRepo(ctx, artifact, to)
	if err != nil {
		return &Notes{Note: fmt.Sprintf(
			"No upstream release notes: %v. Nothing below is informed by what the maintainers wrote.",
			err)}, nil
	}

	max := g.MaxReleases
	if max <= 0 {
		max = 5
	}

	n := &Notes{SourceRepo: repo}
	lo, hi := normalise(from), normalise(to)

	// PAGINATE. Releases come newest-first, and a project like argo-cd has
	// hundreds -- a bump from 2.13.0 to 2.13.2 is nowhere near the first page,
	// and reading only that page silently reports "no releases in range" for
	// every artifact with an active upstream. Which is to say: for the ones
	// that matter most.
	//
	// Bounded twice. Stop once a release older than `from` appears, because
	// the list is ordered and nothing beyond it can be in range; and cap the
	// pages outright, so an upstream with a decade of releases and an
	// unparseable tag scheme cannot turn one explanation into a hundred API
	// calls.
	const maxPages = 10
	raw, err := g.releasePages(ctx, repo, lo, maxPages)
	if err != nil {
		return &Notes{SourceRepo: repo, Note: fmt.Sprintf(
			"No upstream release notes: could not read %s releases (%v).", repo, err)}, nil
	}

	// A stable target does not want release candidates. This is the same trap
	// CLAUDE.md records for Kargo's own subscriptions: numeric comparison reads
	// v2.13.0-rc5 as 2.13.0.5, which sorts ABOVE 2.13.0, so an rc lands inside
	// a 2.13.0 -> 2.13.2 range and gets presented as news. GitHub already knows
	// which releases are prereleases; ask it rather than parse.
	wantPre := looksPrerelease(to)
	for _, r := range raw {
		if r.Draft {
			continue
		}
		if r.Prerelease && !wantPre {
			continue
		}
		v := normalise(r.TagName)
		// (from, to]: the version being left behind is not news; the one being
		// adopted is, and so is everything skipped over on the way.
		if !inRange(v, lo, hi) {
			continue
		}
		body := strings.TrimSpace(r.Body)
		limit := g.MaxBodyChars
		if limit <= 0 {
			limit = 4000
		}
		if len(body) > limit {
			body = body[:limit] + "\n\n[truncated]"
			n.Truncated = true
		}
		n.Releases = append(n.Releases, Release{
			Tag: r.TagName, Name: r.Name, Body: body, URL: r.HTMLURL,
		})
		if len(n.Releases) >= max {
			n.Truncated = true
			break
		}
	}

	switch {
	case len(n.Releases) == 0 && hi == "":
		n.Note = fmt.Sprintf(
			"No upstream release notes: could not read %q as a version, so no release range could be selected.", to)
	case len(n.Releases) == 0:
		n.Note = fmt.Sprintf(
			"No upstream release notes: %s publishes releases, but none tagged between %s and %s.", repo, from, to)
	case n.Truncated:
		n.Note = fmt.Sprintf("Upstream notes from %s, truncated to the %d most recent.", repo, len(n.Releases))
	default:
		n.Note = fmt.Sprintf("Upstream notes from %s.", repo)
	}
	return n, nil
}

func (g *GitHubReleases) doJSON(ctx context.Context, req *http.Request, out any) error {
	return g.getJSONReq(req, out)
}

var numeric = regexp.MustCompile(`\d+`)

// normalise reduces a tag to comparable numbers.
//
// Upstream tags are not consistent even within one project: v1.2.3, 1.2.3,
// chart-1.2.3, release-2026.8.0. Comparing the numbers in order handles all of
// them, and a tag with no numbers at all is simply not comparable and gets
// skipped rather than guessed at.
func normalise(v string) string {
	parts := numeric.FindAllString(v, -1)
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ".")
}

func inRange(v, lo, hi string) bool {
	if v == "" || hi == "" {
		return false
	}
	if cmpVer(v, hi) > 0 {
		return false
	}
	if lo == "" {
		return true
	}
	return cmpVer(v, lo) > 0
}

func cmpVer(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

type ghRelease struct {
	TagName    string `json:"tag_name"`
	Name       string `json:"name"`
	Body       string `json:"body"`
	HTMLURL    string `json:"html_url"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

// releasePages walks the release list newest-first, stopping as soon as it
// passes below lo. Returns everything read; the caller does the range filter.
func (g *GitHubReleases) releasePages(ctx context.Context, repo, lo string, maxPages int) ([]ghRelease, error) {
	var all []ghRelease
	for page := 1; page <= maxPages; page++ {
		url := fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", g.apiBase(), repo, page)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return all, err
		}
		if g.Token != "" {
			req.Header.Set("Authorization", "Bearer "+g.Token)
		}
		req.Header.Set("Accept", "application/vnd.github+json")

		var batch []ghRelease
		if err := g.getJSONReq(req, &batch); err != nil {
			return all, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, nil // last page
		}
		// Ordered newest-first: once this page ends below the floor, nothing
		// further down can be in range.
		if lo != "" {
			last := normalise(batch[len(batch)-1].TagName)
			if last != "" && cmpVer(last, lo) <= 0 {
				return all, nil
			}
		}
	}
	return all, nil
}

// looksPrerelease reports whether a version string carries a prerelease
// marker, so a bump TO an rc can still see the rc notes.
func looksPrerelease(v string) bool {
	v = strings.ToLower(v)
	for _, m := range []string{"-rc", "-alpha", "-beta", "-pre", "-dev", "-snapshot"} {
		if strings.Contains(v, m) {
			return true
		}
	}
	return false
}
