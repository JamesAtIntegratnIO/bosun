package gate

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// docs is a batch of manifests obtained from one source, tagged with the
// cluster context they were produced under (empty when the source is
// cluster-independent).
type docs struct {
	source  string
	cluster *Cluster
	objects []map[string]any
	// scope is the source's Scope, carried so expansion can honour it.
	scope string
	// rendered marks a batch as already-deployed objects rather than
	// Applications to expand.
	rendered bool
	// bootstrapRow is the app-of-apps Application itself, when this batch came
	// from one.
	bootstrapRow *Row
}

// collect turns one source into manifests.
//
// Each type answers the same question -- "what Applications and
// ApplicationSets does this repository define?" -- and they coexist. A real
// repository routinely has ApplicationSets committed as YAML *and* a chart
// that renders more of them, which is why this is a list of strategies rather
// than a mode switch.
func collect(repoRoot string, cfg *Config, inv *Inventory, s Source) ([]docs, error) {
	switch s.Type {
	case SourceRendered:
		objs, err := readGlobs(repoRoot, s.Paths)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", s.Name, err)
		}
		return []docs{{source: s.Name, objects: objs, rendered: true}}, nil

	case SourceManifests:
		objs, err := readGlobs(repoRoot, s.Paths)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", s.Name, err)
		}
		return []docs{{source: s.Name, objects: objs}}, nil

	case SourceKustomize:
		// NEITHER binary ships in the gate's image, which carries helm and
		// kubeconform only. A kustomize source therefore works on a
		// workstation and on a CI runner that installs one, and fails
		// in-cluster -- so say which of the two happened rather than letting
		// "kustomize build failed" cover both. A broken kustomization and a
		// missing builder want completely different next actions.
		if !haveEither("kustomize", "kubectl") {
			return nil, fmt.Errorf("source %q is type kustomize, but neither `kustomize` nor "+
				"`kubectl` is on PATH -- the gate's image ships neither, so this source can only "+
				"be rendered where one is installed", s.Name)
		}
		out, kErr := run(repoRoot, "kustomize", "build", s.Path)
		if kErr != nil {
			// `kubectl kustomize` is the same builder and is far more often
			// present; falling back costs nothing and removes a dependency
			// most clusters' operators already have another copy of.
			var fallbackErr error
			out, fallbackErr = run(repoRoot, "kubectl", "kustomize", s.Path)
			if fallbackErr != nil {
				// BOTH failures, because with only the second a kustomization
				// that `kustomize` explained clearly is reported through
				// whatever `kubectl kustomize` happened to say instead.
				return nil, fmt.Errorf("source %q: kustomize build %s: %w (kubectl kustomize also failed: %v)",
					s.Name, s.Path, kErr, fallbackErr)
			}
		}
		objs, err := parseStream(out)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", s.Name, err)
		}
		return []docs{{source: s.Name, objects: objs}}, nil

	case SourceHelm:
		return collectHelm(repoRoot, inv, s)

	case SourceArgoCDBootstrap:
		return collectBootstrap(repoRoot, cfg, inv, s)
	}
	return nil, fmt.Errorf("source %q: unhandled type %q", s.Name, s.Type)
}

// collectHelm renders a chart, once per cluster in scope when the chart or its
// value files are templated on cluster metadata, otherwise once.
func collectHelm(repoRoot string, inv *Inventory, s Source) ([]docs, error) {
	perCluster := templated(s.Chart) || anyTemplated(s.ValueFiles)

	if !perCluster && s.Selector == nil && s.ArgoCD == "" {
		objs, err := helmRender(repoRoot, s.Chart, resolveAll(s.ValueFiles, nil))
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", s.Name, err)
		}
		return []docs{{source: s.Name, objects: objs}}, nil
	}

	var out []docs
	for i := range inv.Clusters {
		c := inv.Clusters[i]
		if !s.matches(c) {
			continue
		}
		data := c.TemplateData(nil)
		chart, err := renderFastTemplate(s.Chart, data)
		if err != nil {
			return nil, fmt.Errorf("source %q chart for cluster %s: %w", s.Name, c.Name, err)
		}
		objs, err := helmRender(repoRoot, chart, resolveAll(s.ValueFiles, data))
		if err != nil {
			return nil, fmt.Errorf("source %q (cluster %s): %w", s.Name, c.Name, err)
		}
		out = append(out, docs{source: s.Name, cluster: &c, objects: objs, scope: s.Scope})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source %q matched no cluster in the inventory.\n\n"+
			"Nothing it defines can be checked, so the comparison would be made\n"+
			"against an empty set and report no change. Usually a stale inventory:\n"+
			"re-run `gitops-gate clusters export`", s.Name)
	}
	return out, nil
}

func resolveAll(files []string, data map[string]any) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		if data == nil {
			out = append(out, f)
			continue
		}
		if r, err := renderFastTemplate(f, data); err == nil {
			out = append(out, r)
		}
		// A value file whose placeholders do not resolve for this cluster is
		// simply not that cluster's file. ArgoCD's own
		// ignoreMissingValueFiles behaves the same way.
	}
	return out
}

func templated(s string) bool { return strings.Contains(s, "{{") }

func anyTemplated(ss []string) bool {
	for _, s := range ss {
		if templated(s) {
			return true
		}
	}
	return false
}

func readGlobs(repoRoot string, patterns []string) ([]map[string]any, error) {
	var files []string
	for _, p := range patterns {
		m, err := filepath.Glob(filepath.Join(repoRoot, p))
		if err != nil {
			return nil, fmt.Errorf("bad pattern %q: %w", p, err)
		}
		files = append(files, m...)
	}
	sort.Strings(files)

	// One policy for both failures, because these manifests ARE the render: a
	// file that will not parse fails the source, and a file that will not read
	// used to vanish from it silently -- so a permission problem produced a
	// smaller target table, which the diff then reported as objects removed by
	// this pull request.
	var out []map[string]any
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		objs, err := parseStream(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		out = append(out, objs...)
	}
	return out, nil
}

func parseStream(b []byte) ([]map[string]any, error) {
	var out []map[string]any
	for _, doc := range splitYAML(b) {
		var obj map[string]any
		if err := yaml.Unmarshal(doc, &obj); err != nil {
			return nil, fmt.Errorf("parsing manifest: %w", err)
		}
		if obj != nil {
			out = append(out, obj)
		}
	}
	return out, nil
}

func run(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// haveEither reports whether at least one of the named binaries is on PATH.
func haveEither(names ...string) bool {
	for _, n := range names {
		if _, err := exec.LookPath(n); err == nil {
			return true
		}
	}
	return false
}
