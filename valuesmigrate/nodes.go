package valuesmigrate

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// The line surgery. Everything here works from a parsed document's source
// positions and writes back into the original lines, so a file keeps its
// indentation, its quoting and its comments, and the diff a reviewer reads is
// the keys that changed.

// ErrNotFound is a path this file does not hold. Distinguished from every
// other failure because a set has an answer for it -- insert -- and nothing
// else does.
var ErrNotFound = errors.New("no such key in this file")

func parse(data []byte) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("file is not valid YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("file is empty")
	}
	return doc.Content[0], nil
}

// find walks to one key and returns its key node, its value node, and the
// mapping that holds it.
//
// The mapping comes back because every operation needs it and none of them can
// get it afterwards: a yaml.Node knows its children and not its parent.
func find(root *yaml.Node, segs []string) (key, val, parent *yaml.Node, err error) {
	cur := root
	for i, seg := range segs {
		if cur.Kind != yaml.MappingNode {
			return nil, nil, nil, fmt.Errorf("%s is not a mapping", strings.Join(segs[:i], "."))
		}
		idx, ok := childIndex(cur, seg)
		if !ok {
			return nil, nil, nil, ErrNotFound
		}
		key, val, parent = cur.Content[idx], cur.Content[idx+1], cur
		cur = val
	}
	if key == nil {
		return nil, nil, nil, ErrNotFound
	}
	return key, val, parent, nil
}

// childIndex is the position of a key inside a mapping's Content, which
// alternates key, value, key, value.
func childIndex(m *yaml.Node, name string) (int, bool) {
	if m == nil || m.Kind != yaml.MappingNode {
		return 0, false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == name {
			return i, true
		}
	}
	return 0, false
}

// blockOnly refuses a flow mapping.
//
// `{a: 1, b: 2}` puts several entries on one line, so removing or renaming one
// of them is a change inside a line rather than a change to a set of lines,
// and this editor works on lines. Refused and escalated rather than attempted:
// there is no partially-correct answer here that is better than a human.
func blockOnly(m *yaml.Node) error {
	if m != nil && m.Style&yaml.FlowStyle != 0 {
		return fmt.Errorf("the key sits in a flow mapping, which this cannot edit a line of")
	}
	return nil
}

// removeEntry deletes a key and everything under it.
//
// The extent is measured by indentation rather than by the next sibling's
// line, and the difference matters: yaml.v3 attaches a comment above a key to
// that key, but reports the key's own line, so "up to the next sibling" would
// take the comment introducing the *next* setting away with this one.
func removeEntry(data []byte, parent, key *yaml.Node) ([]byte, error) {
	if err := blockOnly(parent); err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	start := key.Line - 1
	if start < 0 || start >= len(lines) {
		return nil, fmt.Errorf("resolved to line %d, outside the file", key.Line)
	}
	indent := key.Column - 1
	end := start
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if leadingSpaces(lines[i]) <= indent {
			break
		}
		end = i
	}
	return []byte(strings.Join(append(append([]string{}, lines[:start]...), lines[end+1:]...), "\n")), nil
}

// insertEntry adds a scalar under an existing block mapping, after its last
// entry.
//
// Last rather than sorted into place: a values file's order is the author's,
// and a migration that also reorders their keys hands a reviewer a diff whose
// shape hides the one line that matters.
func insertEntry(data []byte, root *yaml.Node, segs []string, value any) ([]byte, error) {
	if len(segs) < 2 {
		// A new top-level key. There is no parent mapping to hang it from
		// beyond the document itself, which is a shape this does not need and
		// would get subtly wrong on an empty file.
		return nil, fmt.Errorf("only a key inside an existing section can be added")
	}
	_, parentVal, _, err := find(root, segs[:len(segs)-1])
	if err != nil {
		return nil, fmt.Errorf("the section %q does not exist here, and creating one is not something "+
			"this can do without reformatting", strings.Join(segs[:len(segs)-1], "."))
	}
	if parentVal.Kind != yaml.MappingNode || len(parentVal.Content) == 0 {
		return nil, fmt.Errorf("the section %q is empty or is not a mapping", strings.Join(segs[:len(segs)-1], "."))
	}
	if err := blockOnly(parentVal); err != nil {
		return nil, err
	}
	token, err := scalarToken(value)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	last := parentVal.Content[len(parentVal.Content)-2]
	indent := last.Column - 1
	at := last.Line - 1
	for i := at + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if leadingSpaces(lines[i]) <= indent {
			break
		}
		at = i
	}
	entry := strings.Repeat(" ", indent) + segs[len(segs)-1] + ": " + token
	out := append([]string{}, lines[:at+1]...)
	out = append(out, entry)
	out = append(out, lines[at+1:]...)
	return []byte(strings.Join(out, "\n")), nil
}

// replaceToken rewrites one scalar's own text on its own line.
//
// The token is located by the node's source column and its rendered length,
// never by searching the line for the old text. `version: version` and
// `{a: old, b: old}` are both ordinary, and in both of them a search rewrites
// the wrong half; `edits` learned that on a live repository.
func replaceToken(data []byte, node *yaml.Node, to string) ([]byte, error) {
	lines := strings.Split(string(data), "\n")
	idx := node.Line - 1
	if idx < 0 || idx >= len(lines) {
		return nil, fmt.Errorf("resolved to line %d, outside the file", node.Line)
	}
	line := lines[idx]
	start := node.Column - 1
	width := len(node.Value)
	var wrap func(string) string
	switch {
	case node.Style&yaml.SingleQuotedStyle != 0:
		width += 2
		wrap = func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	case node.Style&yaml.DoubleQuotedStyle != 0:
		width += 2
		wrap = func(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }
	case node.Style == 0:
		wrap = func(s string) string { return s }
	default:
		// Literal and folded scalars span lines, and an anchor or a tag puts
		// something other than the value at the column. Neither is a token
		// this can swap.
		return nil, fmt.Errorf("the value is written in a style this cannot rewrite in place")
	}
	if start < 0 || start+width > len(line) {
		return nil, fmt.Errorf("the value does not sit where the parser said it did")
	}
	lines[idx] = line[:start] + wrap(to) + line[start+width:]
	return []byte(strings.Join(lines, "\n")), nil
}

// scalarToken renders a value as the YAML literal for it, so a number stays a
// number and a string that looks like one stays a string.
//
// Through the marshaller rather than through fmt: `8080` and `"8080"` are
// different documents, and a chart whose schema says `type: string` refuses
// the first. Anything that does not render to a single line is not a scalar
// this can write on one.
func scalarToken(v any) (string, error) {
	raw, err := yaml.Marshal(v)
	if err != nil {
		return "", err
	}
	token := strings.TrimRight(string(raw), "\n")
	if strings.Contains(token, "\n") {
		return "", fmt.Errorf("the value is not a single-line scalar")
	}
	return token, nil
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

// leaves maps every scalar leaf's dotted path to its rendered value, the same
// shape structural uses, so a plan and the verdict that permitted it are
// talking about the same paths.
func leaves(node any) map[string]string {
	out := map[string]string{}
	var rec func(string, any)
	rec = func(prefix string, n any) {
		switch t := n.(type) {
		case map[string]any:
			for k, v := range t {
				rec(joinPath(prefix, k), v)
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

// value looks a dotted path up in a decoded document.
func value(doc map[string]any, path string) (any, bool) {
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

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func parentOf(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[:i]
	}
	return ""
}

func leafOf(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i+1:]
	}
	return path
}
