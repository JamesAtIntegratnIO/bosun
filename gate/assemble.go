package gate

import (
	"context"
	"sort"
)

// Worktrees is the pair of checkouts one comparison is computed from.
//
// A struct rather than two string arguments, for the reason ChartDiff names
// its own returns: `base, head string` is two adjacent same-typed positions,
// and a call site that swaps them compiles, renders each revision with the
// other one's files, and inverts every finding in the report. The gate had
// exactly one worktree until it needed two, and the moment it needed two was
// the moment that mistake became possible.
type Worktrees struct {
	// Base is the checkout at the revision this change starts from -- the
	// merge base, not the base branch's tip, which is a different commit the
	// moment anything else merges.
	Base string
	// Head is the checkout under judgement.
	Head string
}

// Assemble is the gate's verdict, start to finish: render the charts on both
// sides, diff the tables, fold in the settings a bump stops reading, and trace
// dropped API versions to the manifests that still declare them.
//
// It exists because the sequence is not obvious and the order inside it
// matters. The gate has two delivery surfaces, the in-cluster service and the
// gitops-gate CLI, and each used to hand-assemble these four steps itself. They
// had already drifted: the same commit could get two different verdicts
// depending on which surface looked at it, which is the one thing a gate must
// never do. `gate` exported only the primitives, so there was nowhere for the
// sequence to live except in each caller.
//
// trees are the two checkouts. An empty Head means "no worktree available":
// the two steps that need one, chart rendering and consumer annotation, are
// skipped and the result says so by having no chart-derived findings, rather
// than claiming a clean scan of something it never read. cfg may be nil only
// when Head is empty. An empty Base narrows chart rendering to the version
// moves, because a values change cannot be seen without the revision it
// changed from.
//
// base and head are mutated: the rendered objects and the render warnings are
// spliced into them, which is what makes them visible to Diff.
func Assemble(ctx context.Context, trees Worktrees, cfg *Config, base, head *Table) *DiffResult {
	var found ChartFindings
	if trees.Head != "" {
		var beforeOb, afterOb []Object
		beforeOb, afterOb, found = ChartDiff(ctx, trees, cfg, base, head)
		base.Objects = append(base.Objects, beforeOb...)
		head.Objects = append(head.Objects, afterOb...)
		// On base, not head: Diff dedupes the union of both sides' warnings,
		// and a render warning belongs to the comparison rather than to either
		// side of it.
		base.Warnings = append(base.Warnings, found.Warnings...)
	}

	head.ValuesLeaves = found.ValuesLeaves
	res := Diff(base, head)
	res.Unrenderable = found.Unrenderable

	// Neither of these is an object diff. A setting the new chart stops
	// reading leaves the render identical, which is exactly why it needs
	// saying out loud; a chart that will not render at the new version has no
	// objects to diff at all, and that absence is the finding.
	//
	// Diff has already sorted res.Objects, so these are re-sorted in rather
	// than appended to the end; otherwise they arrive in ChartDiff's pair
	// order after an otherwise sorted list, and the report has a tail that
	// does not follow its own ordering.
	if len(found.Changes) > 0 {
		res.Objects = append(res.Objects, found.Changes...)
		sortObjectChanges(res.Objects)
	}

	// With a worktree, a dropped served version can be traced to the manifests
	// that still declare it, which is what decides whether it blocks, and what
	// a repair needs to know it moved. The head one: the question is which
	// manifests still declare it after this change, not before.
	if trees.Head != "" {
		AnnotateConsumers(trees.Head, res)
	}
	return res
}

// sortObjectChanges is the object-change order the report is written in. Kind
// and Object alone leave ties, one object can produce two findings differing
// only in From/To, and a tie left to sort.Slice is a report that reorders
// itself between runs.
func sortObjectChanges(in []ObjectChange) {
	sort.Slice(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Cluster < b.Cluster
	})
}
