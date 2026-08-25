package structural

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Verdict is what the harness decided about a proposed document.
//
// Refusals are plural because a reader deciding whether to fix this by hand
// needs all of them. A proposal told about one of three problems gets
// resubmitted with two.
type Verdict struct {
	// Refusals are why this must not be written. Empty means write it.
	Refusals []string
	// Lost are scalar values present in the original and absent from the
	// proposal. NOT a refusal on its own -- a field the target schema no
	// longer accepts has to go somewhere, and sometimes that is nowhere -- but
	// always reported, so a human sees exactly what a migration dropped.
	Lost []string
	// Respelled are values the target schema RESPELLED rather than dropped:
	// present in the original, absent from the proposal literally, and present
	// there as a schema vocabulary member differing only in case.
	//
	// Separated from Lost because putting them together said something false.
	// cert-manager v1 spells the key algorithm `ECDSA` where v1alpha2 spelled
	// it `ecdsa`, and the enum is what dictates the new spelling -- so the
	// value survived the migration exactly as intended, and the comment
	// announced "Values not carried across: ecdsa, pkcs8" directly beneath the
	// diff that carried them. A reader cannot act on a warning that is wrong,
	// and a warning that cries wolf on the normal case is how the real one
	// gets skipped. Each entry reads `old -> new`.
	Respelled []string
}

func (v Verdict) OK() bool { return len(v.Refusals) == 0 }

// Validate is the whole guarantee.
//
// The model proposed a document. Nothing about that proposal is trusted: not
// the identity it claims, not the shape it takes, and above all not the values
// it contains. Each check below answers a specific way this could go wrong, and
// a proposal that fails any of them is refused whole -- never partially
// applied, because a half-migrated document is worse than an unmigrated one
// with a reason attached.
//
// targetAPIVersion is what the deterministic swap already decided; original is
// the document as the swap left it; proposed is what came back.
func Validate(original, proposed map[string]any, targetAPIVersion string, target Schema) Verdict {
	var v Verdict

	// 1. IDENTITY. A migration that renames the object, moves it to another
	// namespace, or changes its kind is not a migration -- it is a second
	// change riding along inside one, and the gate would count it as a new
	// object appearing and an old one vanishing.
	if got := str(proposed["apiVersion"]); got != targetAPIVersion {
		v.Refusals = append(v.Refusals, fmt.Sprintf(
			"apiVersion is %q and must be %q", got, targetAPIVersion))
	}
	if str(proposed["kind"]) != str(original["kind"]) {
		v.Refusals = append(v.Refusals, fmt.Sprintf(
			"kind changed from %q to %q", str(original["kind"]), str(proposed["kind"])))
	}
	om, _ := original["metadata"].(map[string]any)
	pm, _ := proposed["metadata"].(map[string]any)
	for _, field := range []string{"name", "namespace"} {
		if str(pm[field]) != str(om[field]) {
			v.Refusals = append(v.Refusals, fmt.Sprintf(
				"metadata.%s changed from %q to %q", field, str(om[field]), str(pm[field])))
		}
	}

	// 2. SCHEMA VALIDITY. The same walk that found the problem, run on the
	// answer. A proposal that still does not fit has not solved anything, and
	// this is the apiserver's own objection raised before the apply rather
	// than after it.
	if target != nil {
		if fs := Check(proposed, target); len(fs) > 0 {
			for _, f := range fs {
				v.Refusals = append(v.Refusals, "the proposal does not fit the target schema -- "+f.String())
			}
		}
	}

	// 3. VALUE PROVENANCE, and it is POSITIONAL.
	//
	// This is the check that makes the model a translator rather than an
	// author -- the document-level analogue of the corroboration rule that
	// stops an invented version reaching a file. A value in the proposal has to
	// have come from somewhere, and there are exactly three somewheres:
	//
	//   1. the same path in the original -- the field did not move;
	//   2. a path the target schema REJECTS -- the field moved, which is the
	//      whole job;
	//   3. the target schema itself -- a default, an enum member, a const.
	//
	// The positional half was learned rather than designed, and it is the
	// difference between a check and a formality. A set-membership version of
	// this -- "does the value appear anywhere in the original?" -- passed a
	// live proposal that filled a newly required `secretStoreRef.name` with the
	// object's own `metadata.name`. Every value was "from the document". The
	// document now referenced a store nobody had ever created, and it would
	// have rendered perfectly.
	origAt := leafPaths(original)
	propAt := leafPaths(proposed)
	displaced := displacedValues(original, target)
	allowed := schemaVocabulary(target)

	for _, path := range slices.Sorted(maps.Keys(propAt)) {
		val := propAt[path]
		if was, ok := origAt[path]; ok && was == val {
			continue
		}
		if displaced[val] || allowed[val] {
			continue
		}
		v.Refusals = append(v.Refusals, fmt.Sprintf(
			"%s = %q: that value is not at that path in the original, was not displaced by the "+
				"schema change, and is not dictated by the target schema", path, val))
	}

	// And the report. Not a refusal: a field the target schema no longer
	// accepts has to go somewhere, and sometimes nowhere is right.
	propVals := leafValues(proposed)
	for _, val := range slices.Sorted(maps.Keys(leafValues(original))) {
		if propVals[val] {
			continue
		}
		// Respelled, not dropped. Only the target schema's own vocabulary
		// counts: matching case-insensitively against anything in the proposal
		// would excuse a model that quietly lowercased a name.
		if to, ok := respelledBy(val, propVals, allowed); ok {
			v.Respelled = append(v.Respelled, val+" -> "+to)
			continue
		}
		v.Lost = append(v.Lost, val)
	}
	return v
}

// respelledBy reports the proposal value that is `val` under a different case
// AND is a member of the target schema's vocabulary -- an enum member, a const,
// a declared default. Anything else is a value the model changed on its own
// authority, which is not a respelling and must still read as lost.
func respelledBy(val string, proposed map[string]bool, allowed map[string]bool) (string, bool) {
	// Sorted: two vocabulary members differing only in case is pathological,
	// but a report that names a different one on each run is worse than either
	// answer.
	for _, cand := range slices.Sorted(maps.Keys(proposed)) {
		if allowed[cand] && cand != val && strings.EqualFold(cand, val) {
			return cand, true
		}
	}
	return "", false
}

// displacedValues are the values the schema change actually moved: everything
// under a path the target schema no longer accepts.
//
// These are the only values allowed to appear somewhere new. A value sitting
// happily at a path the target still accepts has no business being copied
// elsewhere -- that is not a migration, it is the model filling a blank with
// whatever was nearest.
func displacedValues(original map[string]any, target Schema) map[string]bool {
	out := map[string]bool{}
	if target == nil {
		// Without a target schema nothing can be shown to be displaced, and
		// the provenance check falls back to "anywhere in the original". Less
		// strict, and the only honest answer when the shape being migrated to
		// is unknown -- which is also why the caller does not attempt a
		// restructure at all in that case.
		return leafValues(original)
	}
	rejected := map[string]bool{}
	for _, f := range Check(original, target) {
		if f.Kind == Rejected {
			rejected[f.Path] = true
		}
	}
	for path, val := range leafPaths(original) {
		for r := range rejected {
			if path == r || strings.HasPrefix(path, r+".") || strings.HasPrefix(path, r+"[") {
				out[val] = true
				break
			}
		}
	}
	return out
}

// leafPaths maps every scalar leaf's dotted path to its rendered value.
func leafPaths(node any) map[string]string {
	out := map[string]string{}
	var rec func(string, any)
	rec = func(prefix string, n any) {
		switch t := n.(type) {
		case map[string]any:
			for k, v := range t {
				rec(join(prefix, k), v)
			}
		case []any:
			for i, v := range t {
				rec(fmt.Sprintf("%s[%d]", prefix, i), v)
			}
		case nil:
		default:
			out[prefix] = fmt.Sprint(t)
		}
	}
	rec("", node)
	return out
}

// leafValues collects every scalar leaf, rendered as a string.
//
// KEYS ARE NOT COLLECTED, and that is the point of the whole check. Field names
// are structure and the schema supplies them; values are data and only the
// document supplies them.
func leafValues(node any) map[string]bool {
	out := map[string]bool{}
	var rec func(any)
	rec = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			for _, v := range t {
				rec(v)
			}
		case []any:
			for _, v := range t {
				rec(v)
			}
		case nil:
		default:
			if s := fmt.Sprint(t); s != "" {
				out[s] = true
			}
		}
	}
	rec(node)
	return out
}

// schemaVocabulary is every value the TARGET SCHEMA ITSELF dictates: defaults,
// enum members, consts.
//
// Without it the provenance check refuses correct migrations. A new required
// field with a single legal value, or a default the schema names, has to come
// from somewhere and the only honest somewhere is the schema -- which is
// evidence, computed and fetched by the harness, not remembered by the model.
func schemaVocabulary(s Schema) map[string]bool {
	out := map[string]bool{}
	var rec func(any)
	rec = func(n any) {
		m, ok := n.(map[string]any)
		if !ok {
			return
		}
		if d, ok := m["default"]; ok {
			for k := range leafValues(d) {
				out[k] = true
			}
		}
		if c, ok := m["const"]; ok {
			for k := range leafValues(c) {
				out[k] = true
			}
		}
		if e, ok := m["enum"].([]any); ok {
			for _, x := range e {
				for k := range leafValues(x) {
					out[k] = true
				}
			}
		}
		for _, key := range []string{"properties", "items", "additionalProperties"} {
			switch child := m[key].(type) {
			case map[string]any:
				if key == "properties" {
					for _, v := range child {
						rec(v)
					}
				} else {
					rec(child)
				}
			}
		}
	}
	rec(map[string]any(s))
	return out
}

func str(v any) string {
	if v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return s
}

// Diff renders a line comparison of two documents for a comment, WITH CONTEXT.
//
// It renders what a model reshaped, and the reader's decision to trust the
// harness rests on it -- so the one thing it must never do is make a preserved
// value look dropped. The first implementation did exactly that. It was a set
// difference on line text: every line whose exact text appeared on both sides
// was printed on neither. A Certificate whose `organization: [Example Platform
// Team]` became `subject.organizations: [Example Platform Team]` rendered as
//
//   - organization:
//   - subject:
//   - organizations:
//
// because the list item `    - Example Platform Team` was byte-identical on
// both sides and therefore invisible. The value survived; the diff showed it
// being deleted into an empty field. Directly beneath it sat a "Values not
// carried across" line, which a reader would read as confirmation.
//
// So: a real diff, over the longest common subsequence, emitting unchanged
// lines around each change. A moved value now appears as context under its new
// key, which is the whole question a reader has.
//
// KNOWN NOISE, and not a defect in this function: the proposal is re-serialised
// from a map, so its keys come out sorted and its lists at canonical indent.
// Every untouched field whose position or indent moved therefore shows as a
// change, and a six-field migration can render as twenty diff lines. The
// semantic account -- which field went where, and why -- is written above the
// diff by the caller; this is the "show me everything" backstop underneath it.
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

// DeclaredDefault returns the default the schema declares at a dotted path, or
// "" if it declares none.
//
// Exported for the eval suite, which needs to tell "the model volunteered a
// default the schema already applies" -- noisy -- from "the model wrote
// something else there" -- wrong. Those score differently and must, or UNSAFE
// stops meaning "would have broken something".
func DeclaredDefault(s Schema, path string) string {
	cur := map[string]any(s)
	for _, seg := range strings.Split(path, ".") {
		if cur == nil {
			return ""
		}
		props, _ := cur["properties"].(map[string]any)
		next, ok := props[seg].(map[string]any)
		if !ok {
			return ""
		}
		cur = next
	}
	if d, ok := cur["default"]; ok {
		return fmt.Sprint(d)
	}
	return ""
}
