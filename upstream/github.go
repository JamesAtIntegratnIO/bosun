package upstream

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/egress"
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
	// TokenSource supersedes Token when set, and is fetched PER CALL. That is
	// not a style choice: a GitHub App's installation token expires in about an
	// hour, so a resolver holding one taken at start-up spends most of its life
	// unauthenticated -- which is exactly what happened here. The agent has run
	// as an App since 0.8.0 and this struct was still being handed `cfg.GitToken`,
	// which App mode leaves empty. Every upstream read was anonymous, against a
	// 60-per-hour-per-IP limit, and the failure surfaced as "no upstream release
	// notes" -- indistinguishable from an artifact that publishes none.
	//
	// The token buys rate limit and nothing else. It grants no access to the
	// upstream repository, which is somebody else's and public.
	TokenSource func(ctx context.Context) (string, error)
	// APIBase defaults to https://api.github.com.
	APIBase string
	// HTTP is injectable for tests. When nil, Egress builds one.
	HTTP *http.Client
	// Egress logs every destination and refuses the denied ones. Applied to
	// the client this package builds, so the registry walk, the API reads and
	// -- the part no call site can name -- every redirect target go through it.
	Egress egress.Policy

	// MaxReleases caps how many releases reach a prompt. A bump that crosses
	// fourteen versions is exactly when the notes are most useful and least
	// affordable; the newest are the ones that matter.
	MaxReleases int
	// MaxBodyChars caps one release body. Some projects paste an entire commit
	// log into a release, which would crowd out the gate report the explanation
	// is actually grounded in.
	MaxBodyChars int
	// MaxCommits caps how many relevant commits reach a prompt or a comment.
	// Zero means MaxCompareCommits.
	MaxCommits int
}

// authorise puts the current credential on a request bound for the GitHub API.
//
// Only for api.github.com. The registry hops in oci.go talk to somebody else's
// registry with somebody else's anonymous pull token, and sending this one
// there would be handing a credential to a host that never asked for it.
func (g *GitHubReleases) authorise(ctx context.Context, req *http.Request) {
	tok := g.Token
	if g.TokenSource != nil {
		// Soft. An upstream read is a courtesy and a token source that is
		// briefly unavailable should cost rate limit, not the whole answer.
		if t, err := g.TokenSource(ctx); err == nil && t != "" {
			tok = t
		}
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
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
		if isRateLimited(err) {
			// Worth its own sentence. "Could not read the releases" invites a
			// reader to check whether the project publishes any; "rate
			// limited" tells them the answer would have been there and points
			// at the credential, which is the actual fix.
			return &Notes{SourceRepo: repo, Note: fmt.Sprintf(
				"No upstream release notes: rate limited by the GitHub API while reading %s.", repo)}, nil
		}
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

	if len(n.Releases) > 0 {
		n.Origin = "releases"
	}

	// No release objects in range is the COMMON case, not a failure: creating
	// a Release is an optional step a great many projects never take, and a
	// chart's own version numbers frequently appear nowhere else at all. The
	// changelog is where those projects write the same thing down, in the same
	// commit as the change.
	if len(n.Releases) == 0 && hi != "" {
		if picked, path := g.changelogNotes(ctx, repo, chartNameOf(artifact), lo, hi); len(picked) > 0 {
			n.Releases, n.Origin = picked, path
			n.Note = fmt.Sprintf("Upstream notes from %s in %s.", path, repo)
			return n, nil
		}
	}

	switch {
	case len(n.Releases) == 0 && hi == "":
		n.Note = fmt.Sprintf(
			"No upstream release notes: could not read %q as a version, so no release range could be selected.", to)
	case len(n.Releases) == 0 && len(raw) == 0:
		// Different situation, and the old wording asserted the opposite of it.
		// A project that tags without ever creating a GitHub Release has no
		// release notes to read at all, and saying "publishes releases, but
		// none in range" sends a reader to check the version numbers rather
		// than to the actual answer. This project is one of them: 8 tags, 0
		// releases.
		n.Note = fmt.Sprintf(
			"No upstream release notes: %s publishes no GitHub releases and no changelog entry "+
				"for this range.", repo)
	case len(n.Releases) == 0:
		n.Note = fmt.Sprintf(
			"No upstream release notes: %s has neither a release nor a changelog entry between %s and %s.",
			repo, from, to)
	case n.Truncated:
		n.Note = fmt.Sprintf("Upstream notes from %s, truncated to the %d most recent.", repo, len(n.Releases))
	default:
		n.Note = fmt.Sprintf("Upstream notes from %s.", repo)
	}
	return n, nil
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
		g.authorise(ctx, req)
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

// tagNames lists the repository's git tags, newest first.
//
// The fallback for a project that TAGS but never creates a GitHub Release --
// which is a common shape and, as it turns out, this project's own. Tags carry
// no notes, so they are useless to Notes; they are exactly what Compare needs,
// because a compare range is two refs and a tag is a ref.
//
// Bounded to one page. A hundred newest tags reaches back further than any
// promotion range worth explaining, and paging a repository with ten thousand
// tags to answer "what changed between two adjacent versions" is not a trade
// worth making.
func (g *GitHubReleases) tagNames(ctx context.Context, repo string) ([]string, error) {
	url := fmt.Sprintf("%s/repos/%s/tags?per_page=100", g.apiBase(), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	g.authorise(ctx, req)
	req.Header.Set("Accept", "application/vnd.github+json")

	var raw []struct {
		Name string `json:"name"`
	}
	if err := g.getJSONReq(req, &raw); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		out = append(out, t.Name)
	}
	return out, nil
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
