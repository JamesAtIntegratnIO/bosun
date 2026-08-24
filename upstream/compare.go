package upstream

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// Commit is one upstream commit between the two versions.
//
// The first line of the message only. A commit body is prose written for other
// maintainers and it crowds the gate report -- which is the fact -- out of a
// prompt that has to hold both.
type Commit struct {
	SHA     string
	Message string
	URL     string
	Author  string
}

// Compare is what the maintainers CHANGED between two tags, as opposed to what
// they said they changed.
//
// It exists because of a specific kind of finding. The gate proves a chart
// dropped its ClusterRole; the release notes, if there are any, say nothing
// about it; and the honest answer -- "the report does not say why" -- is
// correct and unsatisfying. The commits between the two tags do say why, and
// they say it in a form nobody wrote for a changelog and therefore nobody
// polished.
//
// Still TESTIMONY, not fact. A commit message is a claim about a change, made
// by the person making it. What renders here is the only fact in this system.
type Compare struct {
	// Range is the two tags actually compared, "v0.5.8...v1.0.0". Not the
	// promotion's own versions: a chart version and the app version its git
	// tags use are frequently different namespaces, and comparing across them
	// yields a confident 404.
	Range string
	// URL is the compare page, for a reader who wants the whole thing.
	URL string
	// Total is how many commits are in the range, including the ones filtered
	// out. A large Total with an empty Relevant is itself a finding: the
	// maintainers did a great deal and none of it mentions the thing that
	// broke.
	Total int
	// Relevant are the commits whose message mentions something the gate
	// found. Chosen by deterministic string matching over terms the CALLER
	// derived from the report -- never by a model, which would be asking the
	// thing under supervision to pick its own evidence.
	Relevant []Commit
	// Files are paths in the upstream diff that mention one of those terms.
	//
	// Often the stronger signal, and the reason this is here at all. A commit
	// titled "refactor: watch namespaces via config" does not contain the
	// string "ClusterRole"; the file it deleted --
	// `charts/x/templates/clusterrole.yaml` -- does.
	Files []string
	// Note explains an empty or partial result in one sentence.
	Note string
	// Truncated is set when the range was larger than one API answer, or when
	// the relevant list was capped.
	Truncated bool
}

// Any reports whether there is anything worth showing.
func (c *Compare) Any() bool {
	return c != nil && (len(c.Relevant) > 0 || len(c.Files) > 0)
}

// CompareResolver is a SECOND interface rather than two more methods on
// Resolver.
//
// ADR 0004's rule is that an interface is what the caller needs and nothing
// more, and that growing one is a decision. Most callers of Resolver want
// release notes and nothing else; a fake that implements Notes should not stop
// compiling because a feature it does not use arrived. Callers that want the
// commits type-assert for this, and the absence of it degrades to a sentence
// rather than an error.
type CompareResolver interface {
	Compare(ctx context.Context, artifact, from, to string, terms []string) (*Compare, error)
}

// MaxCompareCommits is the default cap on how many relevant commits reach a
// prompt or a comment.
const MaxCompareCommits = 10

// compareTruncateAt is GitHub's own limit on the compare endpoint: it returns
// at most 250 commits in one answer and reports the real count separately.
// Paging past it would mean walking thousands of commits to answer "what
// touched the ClusterRole", which is not a question worth that.
const compareTruncateAt = 250

// Compare reads the commits between the two versions and keeps the ones that
// mention something the gate found.
//
// Never an error for "could not look". Every failure -- an artifact with no
// source label, tags that cannot be matched to the promotion's versions, a
// rate-limited API -- becomes a Note, because the explanation this feeds is a
// courtesy and losing it must never be the reason a pull request looks
// unattended.
func (g *GitHubReleases) Compare(ctx context.Context, artifact, from, to string, terms []string) (*Compare, error) {
	repo, err := g.sourceRepo(ctx, artifact, to)
	if err != nil {
		return &Compare{Note: fmt.Sprintf("No upstream commits: %v.", err)}, nil
	}

	base, head, how, err := g.compareTags(ctx, repo, artifact, from, to)
	if err != nil {
		return &Compare{Note: fmt.Sprintf("No upstream commits: %v.", err)}, nil
	}

	url := fmt.Sprintf("%s/repos/%s/compare/%s...%s", g.apiBase(), repo, base, head)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return &Compare{Note: fmt.Sprintf("No upstream commits: %v.", err)}, nil
	}
	g.authorise(ctx, req)
	req.Header.Set("Accept", "application/vnd.github+json")

	var payload struct {
		HTMLURL string `json:"html_url"`
		Total   int    `json:"total_commits"`
		Commits []struct {
			SHA     string `json:"sha"`
			HTMLURL string `json:"html_url"`
			Commit  struct {
				Message string `json:"message"`
				Author  struct {
					Name string `json:"name"`
				} `json:"author"`
			} `json:"commit"`
		} `json:"commits"`
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if err := g.getJSONReq(req, &payload); err != nil {
		if isRateLimited(err) {
			return &Compare{Note: "No upstream commits: rate limited by the GitHub API."}, nil
		}
		return &Compare{Note: fmt.Sprintf(
			"No upstream commits: could not compare %s...%s in %s (%v).", base, head, repo, err)}, nil
	}

	c := &Compare{
		Range: base + "..." + head,
		URL:   payload.HTMLURL,
		Total: payload.Total,
	}
	if how != "" {
		c.Note = how
	}
	// GitHub answers with at most 250 commits and reports the real total
	// separately, so a range bigger than that is INCOMPLETE and has to say so.
	// A filter run over a truncated list that claimed to be whole would report
	// "nothing upstream mentions this" about a range it never read.
	if payload.Total > compareTruncateAt || len(payload.Commits) < payload.Total {
		c.Truncated = true
	}

	max := g.MaxCommits
	if max <= 0 {
		max = MaxCompareCommits
	}
	for _, raw := range payload.Commits {
		msg := firstLine(raw.Commit.Message)
		if !matchesAny(msg, terms) {
			continue
		}
		if len(c.Relevant) >= max {
			c.Truncated = true
			break
		}
		c.Relevant = append(c.Relevant, Commit{
			SHA: shortSHA(raw.SHA), Message: msg, URL: raw.HTMLURL, Author: raw.Commit.Author.Name,
		})
	}
	for _, f := range payload.Files {
		if !matchesAny(f.Filename, terms) {
			continue
		}
		if len(c.Files) >= max {
			c.Truncated = true
			break
		}
		c.Files = append(c.Files, f.Filename)
	}

	if c.Note == "" {
		switch {
		case c.Any():
			c.Note = fmt.Sprintf("Upstream commits from %s, %s.", repo, c.Range)
		case c.Total > 0:
			// The interesting negative. Somebody did a lot of work and none of
			// it says anything about the thing that changed here.
			c.Note = fmt.Sprintf(
				"%d commit(s) between %s in %s, and none of them mentions what the gate found.",
				c.Total, c.Range, repo)
		default:
			c.Note = fmt.Sprintf("No commits between %s in %s.", c.Range, repo)
		}
	}
	return c, nil
}

// compareTags decides WHICH two refs to compare, and it is the part that has
// to be right.
//
// The promotion's own versions are the wrong answer and confidently so. A
// chart version and the git tags of the project it packages are different
// namespaces -- chart 0.5.1 ships app v1.0.0 -- so `compare/0.5.8...1.0.0`
// against the source repository is a 404 at best and, at worst, two real tags
// from an unrelated numbering.
//
// In order:
//
//  1. The project's own releases. The tags on release objects are real git
//     tags by construction. Base is the newest release at or below `from` --
//     the release the repository is actually leaving -- and head is the newest
//     in range. This is the case that works whenever the upstream publishes
//     releases at all.
//  2. The artifact's OCI labels. `org.opencontainers.image.revision` is a
//     commit SHA the publisher wrote down at build time, which needs no
//     version arithmetic to be correct. Read for both versions, compared as
//     SHAs.
//  3. Neither. Say which namespaces failed to meet and make no call.
func (g *GitHubReleases) compareTags(ctx context.Context, repo, artifact, from, to string) (base, head, note string, err error) {
	lo, hi := normalise(from), normalise(to)
	if releases, rerr := g.releasePages(ctx, repo, lo, 10); rerr == nil {
		names := make([]string, 0, len(releases))
		for _, r := range releases {
			if !r.Draft {
				names = append(names, r.TagName)
			}
		}
		if base, head := framing(names, lo, hi); base != "" && head != "" {
			return base, head, "", nil
		}
	}

	// A project that tags without releasing. Same arithmetic as above against
	// a different list, because a compare range wants refs and a tag is a ref
	// -- the release OBJECT was only ever a convenient place to find one.
	if tags, terr := g.tagNames(ctx, repo); terr == nil {
		if base, head := framing(tags, lo, hi); base != "" && head != "" {
			return base, head,
				"Compared between git tags: this project publishes no GitHub releases, so there are no notes to go with them.", nil
		}
	}

	fromRev, ferr := g.artifactRevision(ctx, artifact, from)
	toRev, terr := g.artifactRevision(ctx, artifact, to)
	if ferr == nil && terr == nil && fromRev != "" && toRev != "" && fromRev != toRev {
		return fromRev, toRev,
			"Compared by the revisions the publisher recorded in the artifact, " +
				"because no release in this repository is tagged in the promotion's version range.", nil
	}

	return "", "", "", fmt.Errorf(
		"no two refs in %s match the promotion's %s -> %s: the project's release tags are not in that "+
			"numbering and the artifact records no build revision", repo, from, to)
}

// framing picks the two refs to compare from a newest-first list of tag names.
//
// Base is the newest ref AT OR BELOW `from` -- the version the repository is
// actually leaving -- and head is the newest one in range. Base is not "the
// oldest in range", and the difference is the whole point: the commits that
// did the damage usually sit between the version being left and the first one
// in range, so the narrower window would exclude exactly what was wanted.
//
// Shared by the release list and the tag list, because they are the same
// question asked of two sources and two implementations would eventually
// disagree about it.
func framing(names []string, lo, hi string) (base, head string) {
	for _, name := range names {
		v := normalise(name)
		if v == "" {
			continue
		}
		// Newest-first, so the first match in each direction is the one wanted.
		if head == "" && inRange(v, lo, hi) {
			head = name
		}
		if base == "" && lo != "" && cmpVer(v, lo) <= 0 {
			base = name
		}
	}
	// Both or neither. A half-answer here would be a range with one end, and
	// every caller would have to remember to check for it.
	if base == "" || head == "" || base == head {
		return "", ""
	}
	return base, head
}

// firstLine is the commit subject. Bodies are prose for other maintainers and
// would crowd out the gate report, which is the only fact in the prompt.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func shortSHA(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// matchesAny is the relevance filter, and it is deliberately dull.
//
// The terms come from the gate's own findings, which the caller derived. The
// match is a case-insensitive substring against a squashed form of both sides,
// so `ClusterRole` finds `templates/clusterrole.yaml` and `cluster-role`
// without any of them having to agree on punctuation.
//
// No model chooses evidence here. Asking the thing under supervision which
// commits support its conclusion is not evidence, it is a second opinion from
// the same opinion.
func matchesAny(text string, terms []string) bool {
	if len(terms) == 0 {
		return false
	}
	sq := squash(text)
	for _, t := range terms {
		if t = squash(t); t != "" && strings.Contains(sq, t) {
			return true
		}
	}
	return false
}

// squash lowercases and drops everything that is not a letter or a digit, so
// the same name written four ways matches itself.
func squash(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
