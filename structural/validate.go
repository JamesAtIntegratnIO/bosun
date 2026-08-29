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
	// proposal. Not a refusal on its own, a field the target schema no longer
	// accepts has to go somewhere, and sometimes that is nowhere, but always
	// reported, so a human sees exactly what a migration dropped.
	Lost []string
	// Respelled are values the target schema respelled rather than dropped:
	// present in the original, absent from the proposal literally, and present
	// there as a schema vocabulary member differing only in case.
	//
	// Separated from Lost because putting them together said something false.
	// cert-manager v1 spells the key algorithm `ECDSA` where v1alpha2 spelled
	// it `ecdsa`, and the enum is what dictates the new spelling, so the value
	// survived the migration exactly as intended, and the comment announced
	// "Values not carried across: ecdsa, pkcs8" directly beneath the diff that
	// carried them. A reader cannot act on a warning that is wrong, and a
	// warning that cries wolf on the normal case is how the real one gets
	// skipped. Each entry reads `old -> new`.
	Respelled []string
}

func (v Verdict) OK() bool { return len(v.Refusals) == 0 }

// Validate is the whole guarantee.
//
// The model proposed a document. Nothing about that proposal is trusted: not
// the identity it claims, not the shape it takes, and above all not the values
// it contains. Each check below answers a specific way this could go wrong, and
// a proposal that fails any of them is refused whole; never partially applied,
// because a half-migrated document is worse than an unmigrated one with a
// reason attached.
//
// targetAPIVersion is what the deterministic swap already decided; original is
// the document as the swap left it; proposed is what came back.
func Validate(original, proposed map[string]any, targetAPIVersion string, target Schema) Verdict {
	var v Verdict

	// 1. Identity. A migration that renames the object, moves it to another
	// namespace, or changes its kind is not a migration; it is a second
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

	// 2. Schema validity. The same walk that found the problem, run on the
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

	// 3. Value provenance, and it is positional.
	//
	// This is the check that makes the model a translator rather than an
	// author, the document-level analogue of the corroboration rule that stops
	// an invented version reaching a file. A value in the proposal has to have
	// come from somewhere, and there are exactly three somewheres:
	//
	//  1. the same path in the original, so the field did not move;
	//  2. a path the target schema rejects, so the field moved, which is the
	//  whole job;
	//  3. the target schema itself: a default, an enum member, a const.
	//
	// The positional half was learned rather than designed, and it is the
	// difference between a check and a formality. A set-membership version of
	// this, "does the value appear anywhere in the original?", passed a live
	// proposal that filled a newly required `secretStoreRef.name` with the
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
// and is a member of the target schema's vocabulary, an enum member, a const, a
// declared default. Anything else is a value the model changed on its own
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

// displacedValues are the values the schema change moved: everything
// under a path the target schema no longer accepts.
//
// These are the only values allowed to appear somewhere new. A value sitting
// happily at a path the target still accepts has no business being copied
// elsewhere; that is not a migration, it is the model filling a blank with
// whatever was nearest.
func displacedValues(original map[string]any, target Schema) map[string]bool {
	out := map[string]bool{}
	if target == nil {
		// Without a target schema nothing can be shown to be displaced, and
		// the provenance check falls back to "anywhere in the original". Less
		// strict, and the only honest answer when the shape being migrated to
		// is unknown, which is also why the caller does not attempt a
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
// Keys are not collected, and that is the point of the whole check. Field names
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

// schemaVocabulary is every value the target schema itself dictates: defaults,
// enum members, consts.
//
// Without it the provenance check refuses correct migrations. A new required
// field with a single legal value, or a default the schema names, has to come
// from somewhere and the only honest somewhere is the schema, which is
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

// DeclaredDefault returns the default the schema declares at a dotted path, or
// "" if it declares none.
//
// Exported for the eval suite, which needs to tell "the model volunteered a
// default the schema already applies", noisy, from "the model wrote something
// else there", wrong. Those score differently and must, or UNSAFE stops
// meaning "would have broken something".
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
