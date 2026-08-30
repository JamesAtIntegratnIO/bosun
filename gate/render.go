package gate

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"
)

// Render walks both levels of the ApplicationSet hierarchy and returns the flat
// set of Applications the cluster would end up with.
//
//	bootstrap ApplicationSet
//	 -> one Application per matching cluster
//	 -> renders the factory chart with that cluster's values layers
//	 -> N ApplicationSets
//	 -> one Application each per matching cluster <- what we return
func Render(ctx context.Context, repoRoot string, cfg *Config, inv *Inventory) (*Table, error) {
	// Collect every source concurrently. On a fleet this is the difference
	// between a gate that runs inside a pull request and one nobody waits for:
	// fifty clusters is fifty chart renders, and they do not depend on each
	// other.
	type result struct {
		idx   int
		batch []docs
		err   error
	}
	sem := make(chan struct{}, cfg.workers())
	results := make(chan result, len(cfg.Sources))
	var wg sync.WaitGroup

	for i := range cfg.Sources {
		wg.Add(1)
		go func(i int, src Source) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			b, err := collect(ctx, repoRoot, cfg, inv, src)
			results <- result{idx: i, batch: b, err: err}
		}(i, cfg.Sources[i])
	}
	wg.Wait()
	close(results)

	ordered := make([][]docs, len(cfg.Sources))
	for r := range results {
		if r.err != nil {
			return nil, r.err
		}
		ordered[r.idx] = r.batch
	}

	table := &Table{}
	seenSelectorKeys := map[string]bool{}

	for _, batch := range ordered {
		for _, d := range batch {
			if d.bootstrapRow != nil {
				table.Rows = append(table.Rows, *d.bootstrapRow)
			}
			for _, obj := range d.objects {
				kind, _ := obj["kind"].(string)
				switch kind {
				case "ApplicationSet":
					if gens, err := generatorsOf(obj); err == nil {
						for _, k := range selectorKeys(gens) {
							seenSelectorKeys[k] = true
						}
					}
					// Under `scope: cluster` the ApplicationSet is expanded
					// against only the cluster it was rendered for.
					scoped := inv
					if d.cluster != nil && d.scope == "cluster" {
						scoped = &Inventory{Clusters: []Cluster{*d.cluster}}
					}
					rows, warns, err := expandAppSet(obj, scoped)
					if err != nil {
						return nil, err
					}
					table.Warnings = append(table.Warnings, warns...)
					table.Rows = append(table.Rows, rows...)

				case "Application":
					// A plain Application needs no expansion: it targets one
					// destination and is already the thing ArgoCD will create.
					// Reading these is why a repository that commits Applications
					// directly, an extremely common layout, is covered at all.
					row, err := rowFromPlainApplication(d.source, obj, inv)
					if err != nil {
						table.Warnings = append(table.Warnings,
							Markdown(Inline(fmt.Sprintf("%s: %v", d.source, err))))
						continue
					}
					table.Rows = append(table.Rows, row)

				default:
					// Anything that is not an Application or ApplicationSet
					// is a resource the cluster will end up with. Recording
					// these is what makes an object-level diff possible.
					cluster := ""
					if d.cluster != nil {
						cluster = d.cluster.Name
					}
					if o, ok := objectFrom(d.source, cluster, "", obj); ok {
						table.Objects = append(table.Objects, o)
					}
				}
			}
		}
	}

	keys := make([]string, 0, len(seenSelectorKeys))
	for k := range seenSelectorKeys {
		keys = append(keys, k)
	}
	if err := inv.Validate(keys, cfg.ClustersExport.KnownAbsentLabels); err != nil {
		return nil, err
	}

	table.Rows = dedupeRows(table.Rows)
	table.Warnings = dedupeOrdered(table.Warnings)
	table.Sort()
	return table, nil
}

// rowFromPlainApplication reads a committed Application. Its destination names
// a cluster by `name` or by `server`; both are resolved against the inventory
// so the row keys the same way an ApplicationSet-generated one does.
func rowFromPlainApplication(source string, obj map[string]any, inv *Inventory) (Row, error) {
	row, err := rowFromApp(source, "", obj)
	if err != nil {
		return Row{}, err
	}
	spec, _ := obj["spec"].(map[string]any)
	dest, _ := spec["destination"].(map[string]any)

	name, _ := dest["name"].(string)
	server, _ := dest["server"].(string)
	switch {
	case name != "":
		row.Cluster = name
	case server != "":
		row.Cluster = server
		for _, c := range inv.Clusters {
			if c.Server == server {
				row.Cluster = c.Name
				break
			}
		}
	default:
		return Row{}, fmt.Errorf("application %q has no destination", row.App)
	}
	return row, nil
}

func generatorsOf(obj map[string]any) ([]generatorSpec, error) {
	spec, _ := obj["spec"].(map[string]any)
	if spec == nil {
		return nil, fmt.Errorf("no .spec")
	}
	raw, err := yaml.Marshal(spec["generators"])
	if err != nil {
		return nil, err
	}
	var gens []generatorSpec
	if err := yaml.Unmarshal(raw, &gens); err != nil {
		return nil, fmt.Errorf("parsing generators: %w", err)
	}
	return gens, nil
}

// bootstrapSource resolves the chart path and value files for one cluster.
// Bootstrap ApplicationSets predate goTemplate mode, so placeholders here are
// the {{metadata.labels.environment}} dialect.
func bootstrapSource(bs map[string]any, p Param, cfg *Config) (string, []string, error) {
	spec, _ := bs["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	if tmpl == nil {
		return "", nil, fmt.Errorf("no .spec.template")
	}
	tspec, _ := tmpl["spec"].(map[string]any)

	// An Application template carries either `sources` (multi-source, used
	// when values come from a second repo via `ref:`) or `source` (singular).
	// The canonical gitops-bridge bootstrap uses the singular form, so reading
	// only the plural made the gate unable to parse the most common bootstrap
	// there is.
	sources, _ := tspec["sources"].([]any)
	if len(sources) == 0 {
		if single, ok := tspec["source"].(map[string]any); ok {
			sources = []any{single}
		}
	}
	if len(sources) == 0 {
		return "", nil, fmt.Errorf("no .spec.template.spec.source or .sources")
	}

	data := p.Cluster.TemplateData(p.Values)

	var chartPath string
	var valueFiles []string

	for _, s := range sources {
		src, _ := s.(map[string]any)
		if src == nil {
			continue
		}
		pathTpl, _ := src["path"].(string)
		if pathTpl == "" {
			continue
		}
		resolved, err := renderFastTemplate(pathTpl, data)
		if err != nil {
			return "", nil, fmt.Errorf("resolving source path %q: %w", pathTpl, err)
		}
		chartPath = resolved

		helm, _ := src["helm"].(map[string]any)
		if helm == nil {
			continue
		}
		vfs, _ := helm["valueFiles"].([]any)
		for _, vf := range vfs {
			s, _ := vf.(string)
			if s == "" {
				continue
			}
			r, err := renderFastTemplate(s, data)
			if err != nil {
				return "", nil, fmt.Errorf("resolving valueFile %q: %w", s, err)
			}
			// `$values/x` refers to the ref'd source, which is this repo.
			r = strings.TrimPrefix(r, "$"+cfg.ValuesRef+"/")
			valueFiles = append(valueFiles, r)
		}
	}

	if chartPath == "" {
		return "", nil, fmt.Errorf("no source with a `path` -- the gate cannot render this bootstrap")
	}
	return chartPath, valueFiles, nil
}

// helmTemplateRaw renders the factory chart and returns the raw manifest
// stream. Missing value files are skipped, matching the
// ignoreMissingValueFiles the bootstraps set; a values layer that does not
// exist for a given cluster is normal, not an error.
func helmTemplateRaw(ctx context.Context, repoRoot, chartPath string, valueFiles []string) ([]byte, error) {
	return helmTemplateRawWith(ctx, repoRoot, chartPath, valueFiles, nil)
}

// helmTemplateRawWith is helmTemplateRaw plus values that live in an
// Application rather than in a file.
//
// The inline block is written to a temporary file outside the checkout and
// passed last, which is both the precedence ArgoCD gives `helm.valuesObject`
// and the only way to hand helm values it can read. Outside the checkout
// because writing into a worktree the gate is about to diff would put the
// gate's own scratch file in the answer.
func helmTemplateRawWith(ctx context.Context, repoRoot, chartPath string, valueFiles []string, inline map[string]any) ([]byte, error) {
	chartFull, err := containedPath(repoRoot, chartPath)
	if err != nil {
		return nil, fmt.Errorf("chart path %q: %w", chartPath, err)
	}
	args := []string{"template", "gate", chartFull}
	for _, vf := range valueFiles {
		full, err := containedPath(repoRoot, vf)
		if err != nil {
			// Not folded into the skip below. "this cluster has no such
			// values layer" is routine and silent; "this path leaves the
			// checkout" is the finding, and swallowing it as absence would
			// hide the one case worth seeing.
			return nil, fmt.Errorf("value file %q: %w", vf, err)
		}
		if _, err := os.Stat(full); err != nil {
			continue // ignoreMissingValueFiles: true
		}
		args = append(args, "-f", full)
	}

	if len(inline) > 0 {
		raw, marshalErr := yaml.Marshal(inline)
		if marshalErr != nil {
			return nil, fmt.Errorf("inline values for %s: %w", chartPath, marshalErr)
		}
		f, tmpErr := os.CreateTemp("", "bosun-values-*.yaml")
		if tmpErr != nil {
			return nil, fmt.Errorf("inline values for %s: %w", chartPath, tmpErr)
		}
		defer func() { _ = os.Remove(f.Name()) }()
		_, writeErr := f.Write(raw)
		if closeErr := f.Close(); writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			return nil, fmt.Errorf("inline values for %s: %w", chartPath, writeErr)
		}
		args = append(args, "-f", f.Name())
	}

	out, err := run(ctx, repoRoot, "helm", args...)
	if err != nil {
		return nil, fmt.Errorf("helm template %s: %w", chartPath, err)
	}
	return out, nil
}

func splitYAML(b []byte) [][]byte {
	parts := bytes.Split(b, []byte("\n---"))
	var out [][]byte
	for _, p := range parts {
		if len(bytes.TrimSpace(p)) == 0 {
			continue
		}
		out = append(out, p)
	}
	return out
}

func dedupeRows(rows []Row) []Row {
	seen := map[string]bool{}
	var out []Row
	for _, r := range rows {
		k := r.AppSet + "\x00" + r.Key()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// dedupeOrdered keeps first-seen order and does not trim or drop blanks. Named
// for both, because package main has a dedupeSorted that does the opposite on
// each count.
func dedupeOrdered(in []Markdown) []Markdown {
	seen := map[Markdown]bool{}
	var out []Markdown
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// bootstrapRow describes the Application a bootstrap ApplicationSet generates
// for one cluster.
func bootstrapRow(bs map[string]any, p Param, appsetName, chartPath string) (Row, bool) {
	spec, _ := bs["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	if tmpl == nil {
		return Row{}, false
	}
	meta, _ := tmpl["metadata"].(map[string]any)
	nameTpl, _ := meta["name"].(string)
	if nameTpl == "" {
		return Row{}, false
	}
	name, err := renderFastTemplate(nameTpl, p.Cluster.TemplateData(p.Values))
	if err != nil {
		return Row{}, false
	}

	row := Row{
		AppSet:     appsetName,
		Cluster:    p.Cluster.Name,
		App:        name,
		SourceType: RowPath,
		Path:       chartPath,
	}
	tspec, _ := tmpl["spec"].(map[string]any)
	if tspec != nil {
		row.Project, _ = tspec["project"].(string)
		if dest, ok := tspec["destination"].(map[string]any); ok {
			row.Namespace, _ = dest["namespace"].(string)
		}
	}
	return row, true
}

// helmRender renders a chart and returns every object in the output.
func helmRender(ctx context.Context, repoRoot, chartPath string, valueFiles []string, inline map[string]any) ([]map[string]any, error) {
	stream, err := helmTemplateRawWith(ctx, repoRoot, chartPath, valueFiles, inline)
	if err != nil {
		return nil, err
	}
	return parseStream(stream)
}

// collectBootstrap handles the gitops-bridge shape: an app-of-apps
// ApplicationSet whose template points at a chart, with the path and the value
// files templated from metadata on each ArgoCD cluster Secret.
//
// It is one source type rather than the model, because plenty of repositories
// have no such layer, but where it exists it is doing real work, and reading
// it is how the gate learns which values each cluster is rendered with without
// being told twice.
func collectBootstrap(ctx context.Context, repoRoot string, cfg *Config, inv *Inventory, s Source) ([]docs, error) {
	full, err := containedPath(repoRoot, s.Path)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", s.Name, err)
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("source %q: reading %s: %w", s.Name, s.Path, err)
	}
	var bs map[string]any
	if err := yaml.Unmarshal(raw, &bs); err != nil {
		return nil, fmt.Errorf("source %q: parsing %s: %w", s.Name, s.Path, err)
	}

	gens, err := generatorsOf(bs)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", s.Name, err)
	}
	params, _, err := expandGenerators(gens, inv)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", s.Name, err)
	}
	if len(params) == 0 {
		return nil, fmt.Errorf(
			"source %q matched no cluster in the inventory.\n\n"+
				"Nothing it generates can be checked, so the comparison would be made\n"+
				"against an empty set and report no change.\n\n"+
				"Check the source's selector against the cluster labels ArgoCD reports.\n"+
				"If it genuinely targets no cluster here, remove it from .gitops-gate.yaml\n"+
				"rather than leaving the gate blind to it", s.Name)
	}

	var out []docs
	for i := range params {
		p := params[i]
		chartPath, valueFiles, err := bootstrapSource(bs, p, cfg)
		if err != nil {
			return nil, fmt.Errorf("source %q (cluster %s): %w", s.Name, p.Cluster.Name, err)
		}
		// A bootstrap's source path is either a chart or a directory of
		// committed manifests that ArgoCD walks with `directory.recurse`.
		//
		// The canonical gitops-bridge bootstrap is the second kind; it points
		// at a directory and applies every ApplicationSet YAML it finds.
		// Assuming a chart made the gate blind to that entire pattern, which
		// is the one most people using this run. Detect by looking
		// for Chart.yaml, exactly as ArgoCD decides.
		objs, err := renderBootstrapPath(ctx, repoRoot, chartPath, valueFiles)
		if err != nil {
			return nil, fmt.Errorf("source %q (cluster %s): %w", s.Name, p.Cluster.Name, err)
		}

		// The bootstrap Application itself is a row: a cluster appearing or
		// disappearing here means it started or stopped receiving addons
		// entirely, which is the largest targeting change there is.
		c := p.Cluster
		d := docs{source: s.Name, cluster: &c, objects: objs}
		if row, ok := bootstrapRow(bs, p, s.Name, chartPath); ok {
			d.bootstrapRow = &row
		}
		out = append(out, d)
	}
	return out, nil
}

// renderBootstrapPath resolves a bootstrap's source path the way ArgoCD does:
// a directory containing Chart.yaml is a chart, anything else is a directory of
// manifests to be read recursively.
func renderBootstrapPath(ctx context.Context, repoRoot, path string, valueFiles []string) ([]map[string]any, error) {
	full, err := containedPath(repoRoot, path)
	if err != nil {
		return nil, fmt.Errorf("source path %q: %w", path, err)
	}
	if _, err := os.Stat(filepath.Join(full, "Chart.yaml")); err == nil {
		return helmRender(ctx, repoRoot, path, valueFiles, nil)
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("source path %s: %w", path, err)
	}
	if !info.IsDir() {
		raw, err := os.ReadFile(full)
		if err != nil {
			return nil, err
		}
		return parseStream(raw)
	}
	return readDirRecursive(full)
}

// readDirRecursive reads every YAML manifest under a directory, matching
// ArgoCD's `directory.recurse: true`.
//
// Walk and read failures are fatal, like the parse failure below them. What
// this returns is the render, so a manifest that quietly does not arrive is
// not a smaller answer; it is the same answer with objects missing from it,
// which the diff then attributes to the pull request.
func readDirRecursive(dir string) ([]map[string]any, error) {
	return readArgoDirectory(dir, true, "", "")
}

// readArgoDirectory reads a path the way ArgoCD reads a directory source.
//
// Three rules, and every one of them changes what deploys. Without recurse,
// ArgoCD reads the path's own files and descends into nothing, so a gate that
// always recursed would render subdirectories nobody applies. `include` and
// `exclude` are globs over each file's path relative to the source path, with
// exclude winning, which is how a repository keeps a directory of fragments
// beside the manifests that use them. Measured on a live install: a source
// carrying `exclude: exclude/*` had a bootstrap manifest sitting under that
// path, so ignoring the pattern would have rendered an ApplicationSet the
// cluster does not have.
func readArgoDirectory(dir string, recurse bool, include, exclude string) ([]map[string]any, error) {
	var out []map[string]any
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			// The root itself is always entered; anything below it only when
			// recursing, which is ArgoCD's own rule.
			if !recurse && p != dir {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(p) {
		case ".yaml", ".yml", ".json":
		default:
			return nil
		}
		rel, relErr := filepath.Rel(dir, p)
		if relErr != nil {
			return fmt.Errorf("%s: %w", p, relErr)
		}
		rel = filepath.ToSlash(rel)
		if !directoryAllows(rel, include, exclude) {
			return nil
		}
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			return fmt.Errorf("%s: %w", p, readErr)
		}
		objs, parseErr := parseStream(raw)
		if parseErr != nil {
			return fmt.Errorf("%s: %w", p, parseErr)
		}
		out = append(out, objs...)
		return nil
	})
	return out, err
}

// directoryAllows applies ArgoCD's include/exclude pair to one relative path.
//
// Exclude wins, and an empty include means everything, both of which are
// ArgoCD's semantics rather than a choice made here.
func directoryAllows(rel, include, exclude string) bool {
	if exclude != "" && globMatches(exclude, rel) {
		return false
	}
	return include == "" || globMatches(include, rel)
}

// globMatches applies one include/exclude pattern to a relative path.
//
// `*` and `?` stop at a path separator and `**` crosses them, which is the
// reading every tool that distinguishes the two uses, and brace groups are
// expanded first because ArgoCD accepts `{a,b/*}`.
//
// Where this is an approximation, it is approximate in the direction that
// shows. ArgoCD compiles these patterns with its own glob library, and if its
// `*` turns out to cross separators where this one does not, the difference is
// that `exclude: build/*` keeps out `build/x.yaml` here and `build/a/x.yaml`
// there. The gate then renders an object ArgoCD does not, and it appears in
// the report as an object nobody deployed, which a reader can see and chase.
// The opposite error, excluding more than ArgoCD does, removes objects from
// the render with no symptom at all: the diff compares two sets that are both
// missing the same things and finds no difference. Write `**` where "and
// everything below" is meant.
//
// A pattern this cannot compile matches nothing, so the file is included, for
// the same reason.
func globMatches(pattern, rel string) bool {
	for _, p := range expandBraces(pattern) {
		re, err := regexp.Compile(globRegexp(p))
		if err != nil {
			continue
		}
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// globRegexp converts one glob to an anchored regular expression.
func globRegexp(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i++
				continue
			}
			b.WriteString("[^/]*")
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return b.String()
}

// expandBraces turns `a{b,c}d` into `abd` and `acd`. One group, which is what
// ArgoCD supports; a pattern with none comes back as itself.
func expandBraces(pattern string) []string {
	open := strings.Index(pattern, "{")
	closeIdx := strings.Index(pattern, "}")
	if open < 0 || closeIdx < open {
		return []string{pattern}
	}
	prefix, suffix := pattern[:open], pattern[closeIdx+1:]
	var out []string
	for _, alt := range strings.Split(pattern[open+1:closeIdx], ",") {
		out = append(out, prefix+alt+suffix)
	}
	return out
}
