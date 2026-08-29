package migrate

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Hit is one file with at least one manifest declaring a dropped version.
type Hit struct {
	// Path is slash-separated and relative to the scanned root.
	Path string
	// Versions are the distinct dropped versions the file declares, sorted.
	Versions []string
	// Docs counts the declarations; a file can declare the same dropped
	// version several times, as documents, as manifests nested inside a values
	// block, or as manifests embedded in a string.
	Docs int
}

// Scan walks a repository worktree and returns every YAML file declaring a
// manifest of d.Kind at one of d's dropped versions.
//
// "Declaring" is deliberately wider than "is a document of that kind". A
// GitOps repository embeds manifests inside chart values, an `extraObjects:`
// list, a block-scalar string handed to a chart that applies it, and those
// render into real objects that break at apply exactly like a top-level
// document does. Measured on the repository this was built for: 13 of 27
// declaring files held the declaration somewhere other than the top level, and
// counting every shape reproduces the 33 declaring documents the incident
// analysis counted by hand.
//
// Two kinds of file are deliberately not answers. Files that fail to parse
// are skipped, not failed. And a Helm chart's templates/ directory (a
// `templates` dir beside a Chart.yaml, Helm's own definition) is skipped
// entirely: a template that happens to parse as YAML is still a program, and
// rewriting a program with a document editor is a guess. A chart declaring a
// dropped version shows up in the gate's chart-diff instead, where its render
// is what gets judged.
func Scan(root string, d Dropped) ([]Hit, error) {
	var hits []Hit
	err := walkYAML(root, func(rel string, data []byte) {
		found := onlyCovered(declarations(data, []Dropped{d}))
		if len(found) == 0 {
			return
		}
		seen := map[string]bool{}
		var versions []string
		for _, f := range found {
			if v := strings.TrimPrefix(f.old, d.Group+"/"); !seen[v] {
				seen[v] = true
				versions = append(versions, v)
			}
		}
		sort.Strings(versions)
		hits = append(hits, Hit{Path: rel, Versions: versions, Docs: len(found)})
	})
	sort.Slice(hits, func(i, j int) bool { return hits[i].Path < hits[j].Path })
	return hits, err
}

func walkYAML(root string, visit func(rel string, data []byte)) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			if entry.Name() == "templates" {
				if _, err := os.Stat(filepath.Join(filepath.Dir(path), "Chart.yaml")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		switch filepath.Ext(entry.Name()) {
		case ".yaml", ".yml":
		default:
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		visit(filepath.ToSlash(rel), data)
		return nil
	})
}

// declaration is one manifest declaring a dropped version, wherever it lives.
type declaration struct {
	// line is the 1-based line of the apiVersion scalar, exact for documents
	// and nested mappings. Zero for a manifest embedded in a string, where the
	// parser's line numbers are relative to the string rather than the file.
	line     int
	old      string // the apiVersion value as declared
	to       string // group/target it must become
	kind     string
	embedded bool
	// covered is false for an embedded declaration whose kind none of the
	// migrations name. It cannot be rewritten, and its presence makes
	// pattern-editing its neighbours unsafe, see rewrite.
	covered bool
}

// onlyCovered filters declarations to the ones some migration names.
// A verb, not an adjective: it returns the subset, not a yes or no, which is
// what `covered` read as beside every other adjective-named predicate here.
func onlyCovered(in []declaration) []declaration {
	var out []declaration
	for _, f := range in {
		if f.covered {
			out = append(out, f)
		}
	}
	return out
}

// declarations finds every manifest in data declaring one of the dropped
// versions: top-level documents, mappings nested at any depth (an
// `extraObjects:` list), and manifests embedded in block-scalar strings.
//
// The match is always a mapping with `apiVersion` and `kind` sibling keys,
// never a bare string occurrence, so a value that only mentions a version
// is not a declaration.
//
// Uncovered declarations, a kind no migration names, at a version another
// CRD of the same group dropped, are returned too, marked, because the
// rewrite needs to see them to refuse pattern-editing around them. They are
// never counted as consumers and never edited: that CRD still serves the
// version, and rewriting its manifests would break what the migration exists
// to protect.
func declarations(data []byte, drops []Dropped) []declaration {
	oldToNew := map[string]string{} // dropped group/version -> group/target
	oldKinds := map[string]map[string]bool{}
	groups := map[string]bool{}
	for _, d := range drops {
		groups[d.Group+"/"] = true
		for _, v := range d.Versions {
			old := d.Group + "/" + v
			oldToNew[old] = d.Group + "/" + d.Target
			if oldKinds[old] == nil {
				oldKinds[old] = map[string]bool{}
			}
			oldKinds[old][d.Kind] = true
		}
	}

	var out []declaration
	var walk func(n *yaml.Node, embedded bool)
	walk = func(n *yaml.Node, embedded bool) {
		switch n.Kind {
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, c := range n.Content {
				walk(c, embedded)
			}
		case yaml.MappingNode:
			var kind string
			var apiVersion *yaml.Node
			for i := 0; i+1 < len(n.Content); i += 2 {
				switch n.Content[i].Value {
				case "kind":
					kind = n.Content[i+1].Value
				case "apiVersion":
					apiVersion = n.Content[i+1]
				}
			}
			if apiVersion != nil && oldToNew[apiVersion.Value] != "" && kind != "" {
				line := apiVersion.Line
				if embedded {
					line = 0
				}
				out = append(out, declaration{
					line: line, old: apiVersion.Value, to: oldToNew[apiVersion.Value],
					kind: kind, embedded: embedded,
					covered: oldKinds[apiVersion.Value][kind],
				})
			}
			for i := 0; i+1 < len(n.Content); i += 2 {
				walk(n.Content[i+1], embedded)
			}
		case yaml.ScalarNode:
			// A manifest smuggled in as a string, `extraManifests: - |`.
			// Parsing it is the only way to know whether the version in it is
			// a declaration or a mention.
			if embedded || !strings.Contains(n.Value, "apiVersion:") {
				return
			}
			mentions := false
			for g := range groups {
				if strings.Contains(n.Value, g) {
					mentions = true
					break
				}
			}
			if !mentions {
				return
			}
			dec := yaml.NewDecoder(strings.NewReader(n.Value))
			for {
				var doc yaml.Node
				if err := dec.Decode(&doc); err != nil {
					return
				}
				walk(&doc, true)
			}
		}
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			if err != io.EOF {
				return out
			}
			return out
		}
		walk(&doc, false)
	}
}

// Result is what a Migrate call did, and, as important, what it refused.
type Result struct {
	Applied []Applied
	Refused []Refused
}

// Applied is one file whose declarations were moved.
type Applied struct {
	Path string
	Kind string
	// From are the dropped versions the file declared, To the group/version
	// they now read. A file migrating several kinds reports the kinds joined.
	From []string
	To   string
	Docs int
}

// Refused is one file that declared a dropped version and was left alone.
type Refused struct {
	Path, Reason string
}

// Migrate rewrites every manifest under root that declares one of the dropped
// versions so it declares that migration's target instead, and nothing else:
// each write is a value replacement on the apiVersion's own line, preserving
// indentation, quoting and any trailing comment, the same discipline the
// edits package applies to values files.
//
// All migrations run as one pass so a file declaring several kinds is judged
// once, whole. check is the caller's path policy, deny-list first, and a
// non-empty return refuses the file. After rewriting, the file is re-scanned;
// if any dropped declaration survives, the file is restored untouched and
// refused, because a half-migrated file is worse than an unmigrated one with
// a reason attached.
func Migrate(root string, drops []Dropped, check func(path string) string) (*Result, error) {
	for _, d := range drops {
		if d.Target == "" {
			return nil, fmt.Errorf("migration for %s has no target version", d.CRD)
		}
	}
	res := &Result{}
	err := walkYAML(root, func(rel string, data []byte) {
		found := declarations(data, drops)
		mine := onlyCovered(found)
		if len(mine) == 0 {
			return
		}
		if reason := check(rel); reason != "" {
			res.Refused = append(res.Refused, Refused{rel, reason})
			return
		}
		updated, err := rewrite(data, found)
		if err != nil {
			res.Refused = append(res.Refused, Refused{rel, err.Error()})
			return
		}
		if left := onlyCovered(declarations(updated, drops)); len(left) > 0 {
			res.Refused = append(res.Refused, Refused{rel,
				fmt.Sprintf("%d declaration(s) would survive the rewrite -- leaving the file untouched", len(left))})
			return
		}
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), updated, 0o644); err != nil {
			res.Refused = append(res.Refused, Refused{rel, fmt.Sprintf("cannot write: %v", err)})
			return
		}
		seenV, seenK := map[string]bool{}, map[string]bool{}
		var versions, kinds []string
		to := ""
		for _, f := range found {
			if v := f.old[strings.LastIndex(f.old, "/")+1:]; !seenV[v] {
				seenV[v] = true
				versions = append(versions, v)
			}
			if !seenK[f.kind] {
				seenK[f.kind] = true
				kinds = append(kinds, f.kind)
			}
			to = f.to
		}
		sort.Strings(versions)
		sort.Strings(kinds)
		res.Applied = append(res.Applied, Applied{
			Path: rel, Kind: strings.Join(kinds, ", "), From: versions, To: to, Docs: len(found),
		})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// rewrite applies the found declarations to data.
//
// Documents and nested mappings carry an exact line, and only that line is
// touched. Embedded-string declarations have no file-relative line, so their
// lines are found by pattern, and that is only safe when every embedded
// declaration of a dropped version is covered by a migration. One embedded
// manifest of a foreign kind at the same version makes the pattern ambiguous,
// and the whole file is refused rather than edited on a guess.
func rewrite(data []byte, found []declaration) ([]byte, error) {
	lines := strings.Split(string(data), "\n")
	exact := map[int]bool{}

	embeddedWant := map[string]int{} // old value -> expected pattern lines
	for _, f := range found {
		if f.embedded {
			if !f.covered {
				return nil, fmt.Errorf("an embedded `%s` manifest declares `%s`, which no migration covers -- refusing to pattern-edit the file", f.kind, f.old)
			}
			embeddedWant[f.old]++
			continue
		}
		idx := f.line - 1
		if idx < 0 || idx >= len(lines) {
			return nil, fmt.Errorf("apiVersion resolved to line %d, outside the file", f.line)
		}
		// A known line is claimed either way, so the embedded pattern pass
		// below can never mistake it for one of its own.
		exact[idx] = true
		if !f.covered {
			// A kind no migration names, at a version another CRD of the same
			// group dropped. Its own CRD still serves it; leave it alone.
			continue
		}
		i := strings.Index(lines[idx], f.old)
		if i < 0 {
			return nil, fmt.Errorf("value %q not found on its own line (%q)", f.old, strings.TrimSpace(lines[idx]))
		}
		lines[idx] = lines[idx][:i] + f.to + lines[idx][i+len(f.old):]
	}

	for old, want := range embeddedWant {
		to := ""
		for _, f := range found {
			if f.embedded && f.old == old {
				to = f.to
			}
		}
		pattern := regexp.MustCompile(`^\s*apiVersion:\s*["']?` + regexp.QuoteMeta(old) + `["']?\s*(#.*)?$`)
		var idxs []int
		for i, l := range lines {
			if !exact[i] && pattern.MatchString(l) {
				idxs = append(idxs, i)
			}
		}
		if len(idxs) != want {
			return nil, fmt.Errorf("found %d apiVersion line(s) for embedded `%s` declarations but expected %d -- refusing to edit on a mismatch", len(idxs), old, want)
		}
		for _, i := range idxs {
			j := strings.Index(lines[i], old)
			lines[i] = lines[i][:j] + to + lines[i][j+len(old):]
		}
	}
	return []byte(strings.Join(lines, "\n")), nil
}
