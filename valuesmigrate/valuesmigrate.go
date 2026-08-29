// Package valuesmigrate turns a validated values document into a plan, and
// applies the plan one key at a time.
//
// It is the fourth way a file gets written in this repository, and it exists
// because the other three cannot express the change:
//
//   - `edits` rewrites one scalar, and refuses anything whose `from` does not
//     match. A deletion has no `from`, and widening `edits` to allow one would
//     put an uncorroborated operation inside the package whose whole documented
//     purpose is that everything in it has a check.
//   - `migrate` swaps `apiVersion` values and nothing else.
//   - `structural` re-serialises a migrated manifest, which is right for a
//     document and wrong for a subtree of a values file that also holds thirty
//     other addons and every note somebody left in it.
//
// # What it does not decide
//
// Nothing here chooses anything. It is handed two documents -- what the
// repository sets, and a proposal `structural.ValidateValues` has already
// accepted -- and derives the difference between them as a list of key
// operations. The model never names a file, a key or an operation; it returns
// a document, and the plan is computed from two structures that were checked
// first. That is a stronger position than `edits`, where the model does name
// the key.
//
// # Why yaml.Node
//
// The same reason `edits` and `migrate` give. yaml.Node carries source
// positions, so a key can be removed, renamed or added on its own lines with
// everything around it left exactly as it was. Re-serialising instead would
// reformat the file, discard its comments, and turn a three-key change into a
// diff nobody can review.
//
// See ADR 0013.
package valuesmigrate

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// OpKind is what happens to one key.
type OpKind string

const (
	// OpRemove: the new chart version has no room for this key. The evidence
	// is the chart's own schema rejecting it, which is a fact computed from
	// the chart rather than a claim made by a model.
	OpRemove OpKind = "remove"
	// OpRename: the same value under a new name in the same parent. Kept
	// apart from remove-plus-add because it is one line in the file and one
	// line in the diff, and because the value never moves, which is what
	// makes it checkable.
	OpRename OpKind = "rename"
	// OpSet: a value the target schema dictates, or one displaced from a key
	// the schema rejects and landing somewhere new.
	OpSet OpKind = "set"
)

// Op is one change to one key of the values document.
//
// Key is a dotted path rooted at the values document, not at the file: the
// file may hold those values under a prefix, and the prefix is discovered
// separately because it is a fact about the repository rather than about the
// migration.
type Op struct {
	Kind OpKind
	Key  string
	// To is the new leaf name, for a rename only.
	To string
	// Value is what a set writes, as the proposal holds it rather than as a
	// string. `8080` and `"8080"` are different documents and a chart whose
	// schema says `type: string` refuses the first, so the type has to survive
	// as far as the line that gets written.
	Value any
}

func (o Op) String() string {
	switch o.Kind {
	case OpRemove:
		return "remove " + o.Key
	case OpRename:
		return "rename " + o.Key + " to " + o.To
	default:
		return fmt.Sprintf("set %s to %v", o.Key, o.Value)
	}
}

// Plan is the difference between what the repository sets and what the
// harness accepted, as operations.
//
// Renames are recognised, not proposed: a key that disappeared and a key that
// appeared under the same parent carrying the identical value is one key that
// moved, and reporting it as a removal plus an addition would lose the fact
// that the value never changed. Anything less obvious than that is a removal
// and an addition, which has the same effect on the file and makes no claim
// about intent.
func Plan(original, proposed map[string]any) []Op {
	origAt, propAt := leaves(original), leaves(proposed)

	var removed, added []string
	for path := range origAt {
		if _, ok := propAt[path]; !ok {
			removed = append(removed, path)
		}
	}
	for path := range propAt {
		if _, ok := origAt[path]; !ok {
			added = append(added, path)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)

	claimed := map[string]bool{}
	var ops []Op
	for _, r := range removed {
		var match string
		for _, a := range added {
			if claimed[a] || parentOf(a) != parentOf(r) || propAt[a] != origAt[r] {
				continue
			}
			if match != "" {
				// Two candidates is not a rename anybody can prove. Fall
				// through to remove-plus-add, which does the same thing to
				// the file and asserts nothing.
				match = ""
				break
			}
			match = a
		}
		if match == "" {
			ops = append(ops, Op{Kind: OpRemove, Key: r})
			continue
		}
		claimed[match] = true
		ops = append(ops, Op{Kind: OpRename, Key: r, To: leafOf(match)})
	}
	for _, a := range added {
		if claimed[a] {
			continue
		}
		v, _ := value(proposed, a)
		ops = append(ops, Op{Kind: OpSet, Key: a, Value: v})
	}
	// A value that changed in place. Rare, and legitimate only where the
	// schema respelled it: ValidateValues has already refused anything else.
	var changed []string
	for path, was := range origAt {
		if now, ok := propAt[path]; ok && now != was {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	for _, path := range changed {
		v, _ := value(proposed, path)
		ops = append(ops, Op{Kind: OpSet, Key: path, Value: v})
	}
	return ops
}

// Apply runs a plan against one file, returning the new contents.
//
// prefix is where this file holds the chart's values: empty for a values file
// an Application passes with `-f`, `bosun.valuesObject` for an addon inside a
// chart-of-charts. Operations are applied one at a time and the document is
// re-parsed between them, because every one of them moves the lines under it
// and a plan computed against stale positions writes in the wrong place.
//
// Refuses rather than improvises. A key inside a flow mapping, a value that is
// not a scalar, a parent that does not exist: each of those is a shape this
// cannot edit a line of, and a partial plan is worse than none, so the whole
// application fails and the caller escalates.
func Apply(data []byte, prefix string, ops []Op) ([]byte, error) {
	out := data
	for _, op := range inApplyOrder(ops) {
		next, err := applyOne(out, joinPath(prefix, op.Key), op)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op, err)
		}
		out = next
	}
	return out, nil
}

// inApplyOrder is the plan, resequenced so that no operation destroys the
// place a later one needs.
//
// Removals go last, and that is the whole of it. Removing the last key of a
// section takes the section with it -- it has to, because a key left holding
// nothing is null, and helm reads a null in a user's values as an instruction
// to drop the chart's own defaults for it. A section emptied before something
// was added to it is a section that is no longer there to add to, so a
// migration that clears three keys out of `gate` and writes a fourth back
// would fail on the write.
//
// The plan itself keeps its own order, which is the one the comment lists and
// a person reads: what was renamed, what was removed, what was added.
func inApplyOrder(ops []Op) []Op {
	out := make([]Op, 0, len(ops))
	for _, kind := range []OpKind{OpRename, OpSet, OpRemove} {
		for _, op := range ops {
			if op.Kind == kind {
				out = append(out, op)
			}
		}
	}
	return out
}

func applyOne(data []byte, full string, op Op) ([]byte, error) {
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	segs := strings.Split(full, ".")

	switch op.Kind {
	case OpRemove:
		return removeCascade(data, segs)

	case OpRename:
		key, _, parent, err := find(root, segs)
		if err != nil {
			return nil, err
		}
		if err := blockOnly(parent); err != nil {
			return nil, err
		}
		// The sibling check is not a nicety. Renaming onto a key that already
		// exists produces a document with the key twice, which yaml resolves
		// by keeping one of them, and which of the two it keeps is not
		// something a reader of the diff would predict.
		if _, ok := childIndex(parent, op.To); ok {
			return nil, fmt.Errorf("the target key %q already exists here", op.To)
		}
		return replaceToken(data, key, op.To)

	default:
		_, val, parent, err := find(root, segs)
		switch {
		case errors.Is(err, ErrNotFound):
			return insertEntry(data, root, segs, op.Value)
		case err != nil:
			return nil, err
		}
		if val.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("the existing value is not a scalar")
		}
		if err := blockOnly(parent); err != nil {
			return nil, err
		}
		token, err := scalarToken(op.Value)
		if err != nil {
			return nil, err
		}
		return replaceToken(data, val, token)
	}
}

// removeCascade deletes a key, and then the section that held it if that
// section is now empty, and so on upwards.
//
// Left behind, `gate:` with nothing under it is not an empty section: it is
// null, and helm's coalescing treats a null in a user's values as an
// instruction to delete the chart's own defaults for that key. Removing three
// settings would then remove every default beside them, which renders, and
// changes what deploys.
func removeCascade(data []byte, segs []string) ([]byte, error) {
	out := data
	for len(segs) > 0 {
		root, err := parse(out)
		if err != nil {
			return nil, err
		}
		key, _, parent, err := find(root, segs)
		if err != nil {
			return nil, err
		}
		out, err = removeEntry(out, parent, key)
		if err != nil {
			return nil, err
		}
		segs = segs[:len(segs)-1]
		if len(segs) == 0 {
			break
		}
		root, err = parse(out)
		if err != nil {
			return nil, err
		}
		_, val, _, err := find(root, segs)
		if err != nil || !emptySection(val) {
			break
		}
	}
	return out, nil
}

// emptySection is a mapping key with nothing left under it.
//
// Two shapes, because removing the last child produces the second one. A
// mapping with no entries is what a document that was written that way parses
// to; a key whose children have just been deleted parses as a null scalar, and
// null is the shape that matters, because that is the one helm reads as "drop
// the chart's defaults for this key".
func emptySection(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.MappingNode:
		return len(n.Content) == 0
	case yaml.ScalarNode:
		return n.Tag == "!!null"
	}
	return false
}
