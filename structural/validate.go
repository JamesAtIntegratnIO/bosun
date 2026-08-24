package structural

import (
	"fmt"
	"sort"
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
	for _, field := range []string{"kind"} {
		if str(proposed[field]) != str(original[field]) {
			v.Refusals = append(v.Refusals, fmt.Sprintf(
				"%s changed from %q to %q", field, str(original[field]), str(proposed[field])))
		}
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

	for _, path := range sortedKeysOf(propAt) {
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
	for _, val := range sortedSet(leafValues(original)) {
		if !propVals[val] {
			v.Lost = append(v.Lost, val)
		}
	}
	return v
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

func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
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

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
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

// Diff renders a unified-ish comparison of two documents for a comment.
//
// Line-based and deliberately simple: a reader wants to see what moved, and a
// structural diff of two maps would be a second thing to get right on a path
// whose whole job is showing a human exactly what a model reshaped.
func Diff(before, after string) string {
	b, a := strings.Split(strings.TrimRight(before, "\n"), "\n"), strings.Split(strings.TrimRight(after, "\n"), "\n")
	inB := map[string]int{}
	for _, l := range b {
		inB[l]++
	}
	inA := map[string]int{}
	for _, l := range a {
		inA[l]++
	}
	var out strings.Builder
	for _, l := range b {
		if inA[l] == 0 {
			fmt.Fprintf(&out, "-%s\n", l)
		}
	}
	for _, l := range a {
		if inB[l] == 0 {
			fmt.Fprintf(&out, "+%s\n", l)
		}
	}
	return out.String()
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
