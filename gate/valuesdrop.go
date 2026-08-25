package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// A setting the repository makes that the new chart version no longer reads.
//
// This is the quietest failure a version bump has. Helm does not error on an
// unknown value -- it ignores it -- so a chart that renames or removes a key
// takes the setting with it and renders perfectly. Nothing in the text diff
// shows it: the values file did not change, the chart did. The resource diff
// often does not show it either, because the affected object frequently
// disappears in the same bump.
//
// Measured on the bump that prompted this: kyverno 3.2.8 -> 3.9.0 dropped 48
// of the 77 values this repository sets, six of them keys Kargo rewrites on
// every image promotion. Merging it would have left those pins being updated
// forever against a chart that stopped reading them, and the gate was green.
//
// # Why absence is trustworthy here
//
// A key missing from the new chart only means something if the OLD chart's
// declared surface actually covered what the repository sets. So coverage is
// measured first, and a chart whose surface does not explain what we already
// set says nothing at all -- see minCoverage.
const (
	// minCoverage is how much of the repository's own settings the OLD chart
	// version must account for before this check will trust an absence.
	//
	// Below it, the chart simply does not declare the surface it reads --
	// undocumented-but-honoured keys are common -- and every finding would be
	// a guess. Silence is the correct output for a chart we cannot read.
	minCoverage = 90

	// maxDroppedListed bounds one Application's list. The finding is "this
	// bump stops reading things you set"; forty paths make that point no
	// better than eight do, and a comment nobody opens is worth nothing.
	maxDroppedListed = 12
)

// valuesSurface is every path a chart version declares it knows about.
//
// Two sources, unioned, because neither is complete alone. The chart's own
// values.yaml is universal and authoritative for defaults but omits keys that
// are optional. A helm-docs README table documents the optional ones and does
// not exist for every chart. A key found in either is known; a key in neither
// is what this check is looking for.
func valuesSurface(repoRoot string, r Row) (map[string]bool, error) {
	out := map[string]bool{}

	defaults, err := helmShow(repoRoot, "values", r)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(defaults, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s %s default values: %w", r.Chart, r.Version, err)
	}
	for _, p := range allPaths(doc, "") {
		out[p] = true
	}

	// Best-effort: a chart without a README, or with one helm-docs did not
	// write, simply contributes nothing here.
	if readme, err := helmShow(repoRoot, "readme", r); err == nil {
		for _, k := range helmDocsKeys(string(readme)) {
			out[k] = true
		}
	}
	return out, nil
}

func helmShow(repoRoot, what string, r Row) ([]byte, error) {
	args := []string{"show", what, chartRef(r), "--version", r.Version}
	if !strings.HasPrefix(r.ChartRepo, "oci://") && r.ChartRepo != "" {
		args = append(args, "--repo", r.ChartRepo)
	}
	return run(repoRoot, "helm", args...)
}

// helmDocsRow matches a values row in a helm-docs table: the key is the first
// cell, and helm-docs writes it as a dotted path. Anchored hard, because a
// prose table elsewhere in the README must not contribute keys.
var helmDocsRow = regexp.MustCompile(`^\|\s*([A-Za-z_][A-Za-z0-9_.\-\[\]"]*)\s*\|\s*(?:bool|int|string|object|list|float|tpl/[a-z]+)\s*\|`)

func helmDocsKeys(readme string) []string {
	var out []string
	for _, line := range strings.Split(readme, "\n") {
		if m := helmDocsRow.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// allPaths is every node in a decoded document, parents included: a chart that
// documents `crds.groups` as one object still knows about everything under it.
func allPaths(node any, prefix string) []string {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		out = append(out, p)
		out = append(out, allPaths(v, p)...)
	}
	return out
}

// leafPaths is every path the repository actually SETS. Only leaves: a parent
// that exists to hold children is not itself a setting, and counting it would
// report the same drop several times over.
func leafPaths(node any, prefix string) []string {
	m, ok := node.(map[string]any)
	if !ok || len(m) == 0 {
		if prefix == "" {
			return nil
		}
		return []string{prefix}
	}
	var out []string
	for k, v := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		out = append(out, leafPaths(v, p)...)
	}
	return out
}

// repoValues is what this Application sets, merged in the order helm would
// merge it: each value file over the last, inline values last of all.
func repoValues(repoRoot string, r Row) (map[string]any, error) {
	merged := map[string]any{}
	add := func(raw []byte) error {
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			return err
		}
		mergeInto(merged, doc)
		return nil
	}
	for _, vf := range r.ValueFiles {
		clean := vf
		if i := strings.Index(clean, "/"); strings.HasPrefix(clean, "$") && i > 0 {
			clean = clean[i+1:]
		}
		full := filepath.Join(repoRoot, clean)
		raw, err := os.ReadFile(full)
		if err != nil {
			// Missing is normal, exactly as it is for the render.
			continue
		}
		if err := add(raw); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", clean, err)
		}
	}
	if strings.TrimSpace(r.ValuesInline) != "" {
		if err := add([]byte(r.ValuesInline)); err != nil {
			return nil, fmt.Errorf("parsing inline values for %s: %w", r.App, err)
		}
	}
	return merged, nil
}

func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if existing, ok := dst[k].(map[string]any); ok {
				mergeInto(existing, sub)
				continue
			}
			cp := map[string]any{}
			mergeInto(cp, sub)
			dst[k] = cp
			continue
		}
		dst[k] = v
	}
}

// covered reports whether a surface accounts for a path: the path itself, any
// ancestor of it (a documented object covers what is inside it), or any key
// beneath it (setting a parent of documented children is setting known keys).
func covered(path string, surface map[string]bool) bool {
	parts := strings.Split(path, ".")
	for i := len(parts); i > 0; i-- {
		if surface[strings.Join(parts[:i], ".")] {
			return true
		}
	}
	for k := range surface {
		if strings.HasPrefix(k, path+".") {
			return true
		}
	}
	return false
}

// droppedValues finds settings this Application makes that the new chart
// version no longer declares.
//
// Returns nothing -- not an error -- when the old chart's surface does not
// account for what the repository already sets. See minCoverage.
func droppedValues(repoRoot string, before, after Row) ([]string, error) {
	vals, err := repoValues(repoRoot, after)
	if err != nil {
		return nil, err
	}
	set := leafPaths(vals, "")
	if len(set) == 0 {
		return nil, nil
	}
	oldSurface, err := valuesSurface(repoRoot, before)
	if err != nil {
		return nil, err
	}
	newSurface, err := valuesSurface(repoRoot, after)
	if err != nil {
		return nil, err
	}

	var recognised int
	var gone []string
	for _, p := range set {
		if !covered(p, oldSurface) {
			continue
		}
		recognised++
		if !covered(p, newSurface) {
			gone = append(gone, p)
		}
	}
	if recognised*100/len(set) < minCoverage {
		return nil, nil
	}
	sort.Strings(gone)
	return gone, nil
}
