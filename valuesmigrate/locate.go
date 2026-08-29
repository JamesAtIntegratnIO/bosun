package valuesmigrate

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Where a repository keeps the values it hands one chart.
//
// helm knows the merged document and nothing about where it came from, and
// that is the whole problem: the render was refused for a merged document, and
// the repair has to land in a file. A values file an Application passes with
// `-f` holds the chart's values at its root. An addon inside a chart-of-charts
// holds them under something like `bosun.valuesObject`, in a file that also
// holds thirty other addons.
//
// So the prefix is discovered rather than configured. Configuration would be
// one more thing an operator has to get right about their own repository
// layout, and Rule 1 says this service knows nothing about any particular one.

// Anchor is the one file and the one prefix under which a plan resolves.
type Anchor struct {
	// Path is relative to the repository root, as the caller supplied it.
	Path string
	// Prefix is the dotted path at which this file holds the chart's values.
	// Empty means the file is the values.
	Prefix string
}

func (a Anchor) String() string {
	if a.Prefix == "" {
		return a.Path
	}
	return a.Path + " under " + a.Prefix
}

// Locate finds the file and prefix that hold every key the plan already
// touches, and refuses anything less certain than exactly one answer.
//
// The evidence is the values themselves. For each key the plan removes or
// renames, the candidate files are searched for an entry whose path ends in
// that key and whose value is the one the repository actually sets, and the
// candidates are intersected. A key found under two prefixes, or in two files,
// or under none, is not a key this can safely write to, and a wrong guess here
// edits somebody else's addon.
//
// Additions are not evidence: a key the repository does not yet set cannot be
// found anywhere, and it inherits the anchor that the rest of the plan proved.
// A plan of nothing but additions therefore has no anchor at all, which is
// correct -- there is no existing setting to attach it to, and ADR 0013 sends
// that case to a person anyway.
func Locate(files map[string][]byte, ops []Op, original map[string]any) (Anchor, error) {
	at := leaves(original)

	type candidate struct{ path, prefix string }
	var wanted []Op
	for _, op := range ops {
		if _, ok := at[op.Key]; ok {
			wanted = append(wanted, op)
		}
	}
	if len(wanted) == 0 {
		return Anchor{}, fmt.Errorf("nothing in this migration names a value the repository already sets, " +
			"so there is no file it can be anchored to")
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	inventories := map[string]map[string]string{}
	for _, p := range paths {
		inv, err := inventory(files[p])
		if err != nil {
			// A file that does not parse is not a file this could have
			// written to anyway. Skipped rather than fatal: a repository is
			// allowed to hold a template that is not a document.
			continue
		}
		inventories[p] = inv
	}

	var live []candidate
	for i, op := range wanted {
		var found []candidate
		for _, p := range paths {
			for key, val := range inventories[p] {
				if val != at[op.Key] {
					continue
				}
				switch {
				case key == op.Key:
					found = append(found, candidate{p, ""})
				case strings.HasSuffix(key, "."+op.Key):
					found = append(found, candidate{p, strings.TrimSuffix(key, "."+op.Key)})
				}
			}
		}
		if i == 0 {
			live = found
			continue
		}
		keep := map[candidate]bool{}
		for _, c := range found {
			keep[c] = true
		}
		var next []candidate
		for _, c := range live {
			if keep[c] {
				next = append(next, c)
			}
		}
		live = next
	}

	seen := map[candidate]bool{}
	var unique []candidate
	for _, c := range live {
		if !seen[c] {
			seen[c] = true
			unique = append(unique, c)
		}
	}
	switch len(unique) {
	case 1:
		return Anchor{Path: unique[0].path, Prefix: unique[0].prefix}, nil
	case 0:
		return Anchor{}, fmt.Errorf("no file in this change holds every one of these settings where "+
			"the render read them: %s", strings.Join(keysOf(wanted), ", "))
	default:
		sort.Slice(unique, func(i, j int) bool {
			if unique[i].path != unique[j].path {
				return unique[i].path < unique[j].path
			}
			return unique[i].prefix < unique[j].prefix
		})
		var where []string
		for _, c := range unique {
			where = append(where, Anchor{Path: c.path, Prefix: c.prefix}.String())
		}
		return Anchor{}, fmt.Errorf("these settings appear in more than one place (%s), and writing to "+
			"the wrong one edits somebody else's addon", strings.Join(where, "; "))
	}
}

func keysOf(ops []Op) []string {
	out := make([]string, 0, len(ops))
	for _, o := range ops {
		out = append(out, o.Key)
	}
	return out
}

// inventory maps every scalar in a file to its dotted path.
//
// Its own walk rather than the one in `edits`. The two packages describe the
// same shape and answer to different owners: that one renders a list for a
// model to choose a key from, and a change to how it renders is not a change
// this should feel.
func inventory(data []byte) (map[string]string, error) {
	root, err := parse(data)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var rec func(string, *yaml.Node)
	rec = func(prefix string, n *yaml.Node) {
		switch n.Kind {
		case yaml.MappingNode:
			for i := 0; i+1 < len(n.Content); i += 2 {
				rec(joinPath(prefix, n.Content[i].Value), n.Content[i+1])
			}
		case yaml.SequenceNode:
			for i, c := range n.Content {
				rec(fmt.Sprintf("%s[%d]", prefix, i), c)
			}
		case yaml.ScalarNode:
			if prefix != "" {
				out[prefix] = n.Value
			}
		}
	}
	rec("", root)
	return out, nil
}
