package structural

import (
	"fmt"
	"strings"
)

// A general line diff, kept beside the schema validation it serves but not
// mixed into it: nothing here knows what a schema is, and validate.go's
// conformance rules do not know what a hunk is. diff_test.go was already
// separate; this is the other half of that split.

// Diff renders a line comparison of two documents for a comment, with context.
//
// It renders what a model reshaped, and the reader's decision to trust the
// harness rests on it, so the one thing it must never do is make a preserved
// value look dropped. The first implementation did exactly that. It was a set
// difference on line text: every line whose exact text appeared on both sides
// was printed on neither. A Certificate whose `organization: [Example Platform
// Team]` became `subject.organizations: [Example Platform Team]` rendered as
//
//   - organization:
//   - subject:
//   - organizations:
//
// because the list item ` - Example Platform Team` was byte-identical on
// both sides and therefore invisible. The value survived; the diff showed it
// being deleted into an empty field. Directly beneath it sat a "Values not
// carried across" line, which a reader would read as confirmation.
//
// So: a real diff, over the longest common subsequence, emitting unchanged
// lines around each change. A moved value now appears as context under its new
// key, which is the whole question a reader has.
//
// Known noise, and not a defect in this function: the proposal is re-serialised
// from a map, so its keys come out sorted and its lists at canonical indent.
// Every untouched field whose position or indent moved therefore shows as a
// change, and a six-field migration can render as twenty diff lines. The
// semantic account, which field went where, and why, is written above the diff
// by the caller; this is the "show me everything" backstop underneath it.
// Removing the noise means preserving the original document's key order and
// style through the round trip, which is a node-level rewrite of a different
// size.
//
// Documents here are single Kubernetes manifests and the caller caps how many
// are reshaped per pull request, so the quadratic table is small by
// construction. The guard below is for the pathological input, not the
// expected one.
func Diff(before, after string) string {
	b := strings.Split(strings.TrimRight(before, "\n"), "\n")
	a := strings.Split(strings.TrimRight(after, "\n"), "\n")

	// A manifest that size is not a manifest. Fall back to something honest
	// rather than build a 10^8-cell table.
	if len(b) > maxDiffLines || len(a) > maxDiffLines {
		var out strings.Builder
		fmt.Fprintf(&out, "# document too large to diff line by line (%d -> %d lines)\n", len(b), len(a))
		return out.String()
	}

	var out strings.Builder
	for _, h := range hunks(lineOps(b, a), diffContext) {
		for _, op := range h {
			out.WriteString(op.sign + op.line + "\n")
		}
	}
	return out.String()
}

const (
	// Unchanged lines shown either side of a change. Three is the convention
	// every reader of a patch already has in their eye.
	diffContext = 3
	// Above this, the LCS table stops being free.
	maxDiffLines = 2000
)

type diffOp struct {
	sign string // " ", "-", "+"
	line string
}

// lineOps is a longest-common-subsequence diff of two line slices.
func lineOps(b, a []string) []diffOp {
	n, m := len(b), len(a)
	// lcs[i][j] = length of the LCS of b[i:] and a[j:].
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if b[i] == a[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case b[i] == a[j]:
			ops = append(ops, diffOp{" ", b[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, diffOp{"-", b[i]})
			i++
		default:
			ops = append(ops, diffOp{"+", a[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{"-", b[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{"+", a[j]})
	}
	return ops
}

// hunks groups ops into runs of change surrounded by at most ctx unchanged
// lines, dropping the unchanged stretches in between.
func hunks(ops []diffOp, ctx int) [][]diffOp {
	keep := make([]bool, len(ops))
	any := false
	for i, op := range ops {
		if op.sign == " " {
			continue
		}
		any = true
		lo, hi := i-ctx, i+ctx
		if lo < 0 {
			lo = 0
		}
		if hi >= len(ops) {
			hi = len(ops) - 1
		}
		for k := lo; k <= hi; k++ {
			keep[k] = true
		}
	}
	if !any {
		return nil
	}
	var out [][]diffOp
	var cur []diffOp
	for i, op := range ops {
		if keep[i] {
			cur = append(cur, op)
			continue
		}
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}
