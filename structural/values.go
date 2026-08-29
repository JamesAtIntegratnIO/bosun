package structural

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// The same three-check harness, pointed at a chart's values instead of a
// manifest.
//
// A chart's `values.schema.json` is the one machine-readable thing it says
// about what it accepts, and helm enforces it before templating: a repository
// whose values the chart has outgrown does not render at all. That makes the
// failure loud and the evidence exact, which is the opposite of the CRD case
// this package was built for, where the apiserver prunes a field in silence.
//
// What changes is the first check. A manifest has an identity to preserve --
// kind, name, namespace -- and a values document has none. What it has instead
// is everything the new chart still declares, and that must come through
// untouched: a migration that quietly retunes a setting it was not asked about
// is a second change riding inside one, exactly as a renamed object would be.
//
// See ADR 0013.

// ValidateValues judges a proposed values document against the chart version
// it is being migrated to.
//
// original is what the repository sets today, merged the way helm merges it;
// proposed is what came back; target is the chart's own values schema. A
// verdict with no refusals is safe to turn into a plan.
func ValidateValues(original, proposed map[string]any, target Schema) Verdict {
	var v Verdict

	// 1. Survival. Everything the target schema still accepts stays where it
	// is, with the value it had.
	//
	// This is the values-document analogue of identity, and it carries more
	// weight here than identity does there, because it is also what stops a
	// displaced value landing on a key that already had one. `port` moving to
	// `podPort` is a migration; `port` moving onto a `podPort` somebody had
	// already set is a setting overwritten by a rename, and the two are
	// indistinguishable from the proposal alone.
	origAt := leafPaths(original)
	propAt := leafPaths(proposed)
	unfit := unfitPaths(original, target)
	for _, path := range slices.Sorted(maps.Keys(origAt)) {
		if underAny(path, unfit) {
			continue
		}
		got, ok := propAt[path]
		switch {
		case !ok:
			v.Refusals = append(v.Refusals, fmt.Sprintf(
				"%s = %q is still declared by the new chart version and the proposal drops it",
				path, origAt[path]))
		case got != origAt[path]:
			v.Refusals = append(v.Refusals, fmt.Sprintf(
				"%s changed from %q to %q, and the new chart version still accepts it as it was",
				path, origAt[path], got))
		}
	}

	// 2. Schema validity. The same walk that found the problem, run on the
	// answer: helm's own objection, raised before the render rather than
	// during it.
	if target != nil {
		for _, f := range Check(proposed, target) {
			v.Refusals = append(v.Refusals, "the proposal does not fit the chart's values schema -- "+f.String())
		}
	}

	// 3. Value provenance, positional, and unchanged from the manifest path.
	// A value came from the same path in the original, from a path the target
	// schema rejects, or from the target schema itself.
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
			"%s = %q: that value is not at that path in the values this repository sets, was not "+
				"displaced by the schema change, and is not dictated by the chart's schema", path, val))
	}

	// And the report. Here, unlike the manifest path, a lost value is often
	// the entire point: a setting the chart stopped reading has nowhere to go.
	// It is still named, every one of it, because the one that should have
	// been moved and was not looks exactly like the ones that should not.
	propVals := leafValues(proposed)
	for _, val := range slices.Sorted(maps.Keys(leafValues(original))) {
		if propVals[val] {
			continue
		}
		if to, ok := respelledBy(val, propVals, allowed); ok {
			v.Respelled = append(v.Respelled, val+" -> "+to)
			continue
		}
		v.Lost = append(v.Lost, val)
	}
	return v
}

// Vacancies is, for each key the target schema refuses, the keys it declares
// beside that one and this document does not set.
//
// Computed evidence, and the reason it exists is a measurement. The schema is
// already in the prompt in full, `podPort: integer` sitting directly under the
// section `port` was refused from, and a live model dropped the value anyway:
// it read "this key is not allowed" and removed it, which is one of the two
// correct answers and was the wrong one. A refused key and an empty slot
// beside it is what distinguishes a rename from a removal, and stating it is
// cheaper than hoping the schema dump is read that closely.
//
// It is evidence rather than a hint. Nothing here is a judgement about which
// vacancy is the right one, or whether there is one at all; it is a fact about
// two documents, derived the same way the findings above it are. A refused key
// with nothing beside it is reported as such, which is the other half of the
// signal: that one really was dropped.
func Vacancies(doc map[string]any, target Schema) []string {
	if target == nil {
		return nil
	}
	set := leafPaths(doc)
	var out []string
	for _, f := range Check(doc, target) {
		if f.Kind != Rejected {
			continue
		}
		parent := ""
		if i := strings.LastIndexByte(f.Path, '.'); i >= 0 {
			parent = f.Path[:i]
		}
		sub := target
		if parent != "" {
			sub = schemaAt(target, parent)
		}
		props, _ := map[string]any(sub)["properties"].(map[string]any)
		refused, _ := valueAt(doc, f.Path)

		var free []string
		for _, name := range slices.Sorted(maps.Keys(props)) {
			path := name
			if parent != "" {
				path = parent + "." + name
			}
			if _, taken := set[path]; taken {
				continue
			}
			// A section is free only when nothing in the document lives
			// inside it. Tested that way round: the section itself is never a
			// leaf, so asking whether the document sets it is always no, and
			// `gate.argocd` would be offered as somewhere to put a value while
			// holding two of its own.
			if holdsSomething(path, set) {
				continue
			}
			// A slot that cannot hold this value is not where it went. A big
			// chart declares hundreds of keys and a refused one at the root
			// would otherwise be answered with all of them, which is the same
			// as answering with none.
			//
			// A free section stays on the list whatever the value's type: a
			// rename can move a scalar inside a new object, and the section
			// being empty is what makes it a candidate.
			if !couldHold(props[name], refused) {
				continue
			}
			free = append(free, name+declaredType(props[name]))
		}
		if len(free) == 0 {
			out = append(out, f.Path+" -> nothing free beside it")
			continue
		}
		if n := len(free) - maxVacancies; n > 0 {
			free = append(free[:maxVacancies], fmt.Sprintf("…and %d more", n))
		}
		out = append(out, f.Path+" -> "+strings.Join(free, ", "))
	}
	return out
}

// maxVacancies bounds one refused key's list. The finding is "here is where
// this could have gone"; twenty candidates make that point worse than four do,
// and the type filter above has usually already left one.
const maxVacancies = 8

// couldHold reports whether a declared slot could take a value: same type, an
// undeclared type, or a section, which can hold anything inside it.
func couldHold(slot any, v any) bool {
	m, ok := slot.(map[string]any)
	if !ok {
		return true
	}
	t, _ := m["type"].(string)
	switch t {
	case "", "object":
		return true
	case "array":
		_, isList := v.([]any)
		return isList
	}
	return v == nil || scalarFits(v, t)
}

// valueAt looks a dotted path up in a decoded document.
func valueAt(doc map[string]any, path string) (any, bool) {
	var cur any = doc
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// holdsSomething reports whether the document sets anything at or beneath a
// path.
func holdsSomething(path string, set map[string]string) bool {
	for p := range set {
		if p == path || strings.HasPrefix(p, path+".") || strings.HasPrefix(p, path+"[") {
			return true
		}
	}
	return false
}

// declaredType renders " (type)" for a schema, or "" when it names none.
func declaredType(sub any) string {
	m, ok := sub.(map[string]any)
	if !ok {
		return ""
	}
	t, _ := m["type"].(string)
	if t == "" {
		return ""
	}
	return " (" + t + ")"
}

// NeedsAnAuthor is every field the target schema requires that neither the
// document nor the schema can supply a value for.
//
// This is where repair ends. A required key with a default, a const, or one
// legal value has an answer computed from the chart; a required key without
// one has an answer only a person holds. `metrics.serviceMonitor.namespace`
// is the case this was written from: the human who fixed that bump chose
// `monitoring` from context the chart does not contain, and a model asked the
// same question would have chosen something too.
//
// Answered before the model is called, so the escalation names the field
// rather than reporting a refused proposal that was never going to pass.
func NeedsAnAuthor(original map[string]any, target Schema) []string {
	if target == nil {
		return nil
	}
	var out []string
	for _, f := range Check(original, target) {
		if f.Kind != Missing || dictated(target, f.Path) {
			continue
		}
		out = append(out, f.Path)
	}
	return out
}

// dictated reports whether the schema names the value for a path itself: a
// default, a const, or an enum with exactly one member.
//
// One member, not any: an enum of three says which values are legal, not which
// one belongs here, and treating that as an answer is the invention this check
// exists to refuse.
func dictated(s Schema, path string) bool {
	sub := schemaAt(s, path)
	if sub == nil {
		return false
	}
	if _, ok := sub["default"]; ok {
		return true
	}
	if _, ok := sub["const"]; ok {
		return true
	}
	e, ok := sub["enum"].([]any)
	return ok && len(e) == 1
}

// schemaAt walks to the subschema for a dotted path, or nil.
//
// A path through a list is not walked. Values documents put required fields
// inside list items rarely, and answering "not dictated" there sends the case
// to a human, which is the safe direction to be wrong in.
func schemaAt(s Schema, path string) Schema {
	if strings.ContainsAny(path, "[]") {
		return nil
	}
	cur := map[string]any(s)
	for _, seg := range strings.Split(path, ".") {
		props, _ := cur["properties"].(map[string]any)
		next, ok := props[seg].(map[string]any)
		if !ok {
			return nil
		}
		cur = next
	}
	return Schema(cur)
}

// unfitPaths are the paths in a document the target schema does not accept as
// they stand: a key it has no room for, or a value whose type it contradicts.
// Only these may move, and only these may change.
func unfitPaths(doc map[string]any, target Schema) []string {
	if target == nil {
		return nil
	}
	var out []string
	for _, f := range Check(doc, target) {
		if f.Kind == Rejected || f.Kind == WrongType {
			out = append(out, f.Path)
		}
	}
	return out
}

// underAny reports whether a leaf path is at or beneath one of the given
// paths.
func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+".") || strings.HasPrefix(path, r+"[") {
			return true
		}
	}
	return false
}
