package structural

import (
	"strings"
	"testing"
)

// The defect this diff was rewritten for.
//
// cert-manager v1 moves `organization` under `subject.organizations`. The list
// item itself does not move a column, so its line is byte-identical on both
// sides -- and the set-difference implementation printed it on neither. The
// comment showed the value being deleted into an empty field, directly above a
// "Values not carried across" line. A reader had every reason to believe the
// migration had lost it.
func TestDiffKeepsAMovedValueVisible(t *testing.T) {
	before := strings.Join([]string{
		"spec:",
		"  commonName: platform.localtest.me",
		"  organization:",
		"    - Example Platform Team",
		"  secretName: platform-tls",
	}, "\n")
	after := strings.Join([]string{
		"spec:",
		"  commonName: platform.localtest.me",
		"  secretName: platform-tls",
		"  subject:",
		"    organizations:",
		"    - Example Platform Team",
	}, "\n")

	got := Diff(before, after)
	if !strings.Contains(got, "Example Platform Team") {
		t.Fatalf("the moved value is invisible in the diff:\n%s", got)
	}
	if !strings.Contains(got, "+  subject:") || !strings.Contains(got, "-  organization:") {
		t.Fatalf("the key move is not shown:\n%s", got)
	}
}

func TestDiffShowsContextAroundAChange(t *testing.T) {
	before := "a\nb\nc\nd\ne"
	after := "a\nb\nC\nd\ne"
	got := Diff(before, after)
	for _, want := range []string{" b", "-c", "+C", " d"} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// Identical documents produce nothing at all -- not a wall of context.
func TestDiffOfNoChangeIsEmpty(t *testing.T) {
	doc := "a\nb\nc"
	if got := Diff(doc, doc); got != "" {
		t.Fatalf("want empty, got:\n%s", got)
	}
}

// Unchanged stretches far from any change are dropped, so a long document does
// not bury the three lines that moved.
func TestDiffElidesDistantUnchangedLines(t *testing.T) {
	var b, a []string
	for i := 0; i < 40; i++ {
		b = append(b, "line")
		a = append(a, "line")
	}
	b = append(b, "old")
	a = append(a, "new")
	got := Diff(strings.Join(b, "\n"), strings.Join(a, "\n"))
	if n := strings.Count(got, "\n"); n > 2*diffContext+2 {
		t.Fatalf("want a hunk, got %d lines:\n%s", n, got)
	}
	if !strings.Contains(got, "-old") || !strings.Contains(got, "+new") {
		t.Fatalf("the change is missing:\n%s", got)
	}
}

// A duplicated line must not be collapsed. The old implementation counted
// membership, not multiplicity, so removing one of two identical lines showed
// as no change at all.
func TestDiffCountsRepeatedLines(t *testing.T) {
	got := Diff("x\nx\ny", "x\ny")
	if !strings.Contains(got, "-x") {
		t.Fatalf("a removed duplicate is invisible:\n%s", got)
	}
}
