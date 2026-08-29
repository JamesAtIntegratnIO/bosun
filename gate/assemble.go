package gate

import (
	"context"
	"sort"
)

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
// repoRoot is the head worktree. Empty means "no worktree available": the two
// steps that need one, chart rendering and consumer annotation, are skipped and
// the result says so by having no chart-derived findings, rather than claiming
// a clean scan of something it never read. cfg may be nil only when repoRoot is
// empty.
//
// base and head are mutated: the rendered objects and the render warnings are
// spliced into them, which is what makes them visible to Diff.
func Assemble(ctx context.Context, repoRoot string, cfg *Config, base, head *Table) *DiffResult {
	var found ChartFindings
	if repoRoot != "" {
		var beforeOb, afterOb []Object
		beforeOb, afterOb, found = ChartDiff(ctx, repoRoot, cfg, base, head)
		base.Objects = append(base.Objects, beforeOb...)
		head.Objects = append(head.Objects, afterOb...)
		// On base, not head: Diff dedupes the union of both sides' warnings,
		// and a render warning belongs to the comparison rather than to either
		// side of it.
		base.Warnings = append(base.Warnings, found.Warnings...)
	}

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
	// a repair needs to know it moved.
	if repoRoot != "" {
		AnnotateConsumers(repoRoot, res)
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
