// Package upstream answers "what did the maintainers say changed?".
//
// The gate reports what a bump does to the RENDER -- resources added, ports
// moved, defaults flipped. That is the what. It cannot tell you the why: a
// chart that adds a DaemonSet does not say whether that is a new feature, a
// security fix, or a rewrite that will need attention in six months.
//
// The maintainers said so somewhere, and this goes and reads it.
//
// The resolution is deliberately AUTHORITATIVE rather than clever. An artifact
// reference is not a repository name -- ghcr.io/akuity/kargo-charts/kargo is
// published from github.com/akuity/kargo, and no amount of string-munging
// discovers that. What does is the OCI label the publisher set:
//
//	org.opencontainers.image.source
//
// Guessing a repository from a registry path produces plausible URLs that are
// wrong, and a wrong repository yields another project's release notes, which
// is worse than none: it reads exactly like the truth.
package upstream

import "context"

// Release is one upstream release, as its maintainers published it.
type Release struct {
	Tag  string
	Name string
	Body string
	URL  string
}

// Notes is what was found, and -- when nothing was -- why.
//
// Note is not decoration. An explanation built without upstream context must
// say so, and this is what it says. Silence about a missing source is how a
// reader comes to believe an explanation was better evidenced than it was.
type Notes struct {
	// SourceRepo is "owner/repo", empty when it could not be established.
	SourceRepo string
	// Releases, newest first. Empty is normal and not an error.
	Releases []Release
	// Note explains an empty or partial result in one sentence, for the reader.
	Note string
	// Origin says WHERE the notes came from -- "releases" or a changelog's
	// path. Not decoration: a GitHub Release is written once at the moment of
	// release, and a changelog is a file at the default branch that can have
	// been edited since. A reader weighing an explanation should know which
	// they got.
	Origin string
	// Truncated is set when releases or bodies were cut to fit a prompt.
	Truncated bool
	// Compare is what the maintainers CHANGED between the two tags, when a
	// caller asked for it. Nil when it was not asked for or could not be had,
	// which is why every reader goes through Compare.Any().
	//
	// Additive on purpose: Any() above still means "are there release notes",
	// because that is what every existing caller was asking.
	Compare *Compare
}

// Any reports whether there is anything worth putting in a prompt.
func (n *Notes) Any() bool { return n != nil && len(n.Releases) > 0 }

// Resolver finds what the maintainers said between two versions.
//
// An implementation must NEVER return an error for "nothing found" -- that is
// an ordinary outcome, described in Note. Errors are for a resolver that could
// not do its job at all, and even then the caller is expected to carry on
// without upstream context rather than fail.
type Resolver interface {
	Notes(ctx context.Context, artifact, from, to string) (*Notes, error)
	Name() string
}
