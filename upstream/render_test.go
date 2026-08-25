package upstream

import (
	"strings"
	"testing"
)

// This block goes into the model's prompt, and the eval suite scores the
// explain path against it. Its whole job is keeping two things apart: the gate
// report is COMPUTED, somebody rendered both versions and diffed them, while
// release notes are CLAIMED, somebody wrote down what they meant to do. An
// explanation that blurs those states an intention as an outcome, fluently,
// and the reader cannot tell.

func TestRenderKeepsClaimedApartFromComputed(t *testing.T) {
	got := Render(&Notes{
		SourceRepo: "org/repo",
		Origin:     OriginReleases,
		Releases:   []Release{{Tag: "v2.0.0", Name: "Big one", Body: "removed the sidecar"}},
	})
	if !strings.Contains(got, "they SAY they changed") {
		t.Errorf("the claimed/computed distinction must survive:\n%s", got)
	}
	if !strings.Contains(got, "removed the sidecar") {
		t.Errorf("the body is the evidence:\n%s", got)
	}
	// Tag and name together, because a tag alone is not a description.
	if !strings.Contains(got, "v2.0.0 -- Big one") {
		t.Errorf("got:\n%s", got)
	}
}

// A changelog is a file at the default branch that can have been edited since;
// a release is written once at the moment of release. A reader weighing an
// explanation should know which they got.
func TestRenderSaysWhereTheNotesCameFrom(t *testing.T) {
	fromChangelog := Render(&Notes{
		SourceRepo: "org/repo", Origin: "CHANGELOG.md",
		Releases: []Release{{Tag: "v2", Body: "b"}},
	})
	if !strings.Contains(fromChangelog, "in CHANGELOG.md") {
		t.Errorf("a changelog must be named:\n%s", fromChangelog)
	}

	fromReleases := Render(&Notes{
		SourceRepo: "org/repo", Origin: OriginReleases,
		Releases: []Release{{Tag: "v2", Body: "b"}},
	})
	if !strings.Contains(fromReleases, "in their releases") {
		t.Errorf("releases are the default phrasing:\n%s", fromReleases)
	}
}

// The case that matters most: with no maintainer account of WHY the render
// changed, a model either says so or invents one, and inventing one is this
// path's whole failure mode.
func TestRenderTellsTheModelNotToSupplyAReasonItDoesNotHave(t *testing.T) {
	got := Render(&Notes{Note: "the repository publishes no releases"})
	if !strings.Contains(got, "do not supply a reason") {
		t.Errorf("with no evidence the instruction must be explicit:\n%s", got)
	}
	if !strings.Contains(got, "the repository publishes no releases") {
		t.Errorf("the note explaining the absence must reach the model:\n%s", got)
	}
}

// Nil is the ordinary case on a repository the resolver could not identify,
// and it must render a block that says so rather than panicking or vanishing.
func TestRenderHandlesNilNotes(t *testing.T) {
	got := Render(nil)
	if !strings.Contains(got, "UPSTREAM RELEASE NOTES") {
		t.Errorf("the heading must be present so the model knows the section exists:\n%s", got)
	}
	if !strings.Contains(got, "do not supply a reason") {
		t.Errorf("got:\n%s", got)
	}
}

// A compare is what the maintainers CHANGED, as opposed to what they wrote --
// and with no releases at all it is the only evidence there is.
func TestRenderCarriesTheCompareEvenWithNoReleases(t *testing.T) {
	got := Render(&Notes{
		Note: "no releases",
		Compare: &Compare{
			Range: "v0.5.8...v1.0.0", Total: 42,
			Relevant: []Commit{{SHA: "abcdef1234567890", Message: "drop the ClusterRole"}},
		},
	})
	if !strings.Contains(got, "drop the ClusterRole") {
		t.Errorf("the commit is evidence and must survive:\n%s", got)
	}
	// With a compare present, the "invent nothing" instruction is not the
	// right one -- there IS something to reason from.
	if strings.Contains(got, "do not supply a reason") {
		t.Errorf("a compare is a reason:\n%s", got)
	}
}
