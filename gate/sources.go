package gate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/childenv"
	"github.com/JamesAtIntegratnIO/bosun/redact"
	"github.com/JamesAtIntegratnIO/bosun/safepath"
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
	// fromLive marks a batch that came from what ArgoCD has applied rather
	// than from either revision of the repository. It is the same content on
	// both sides of the diff by construction, so it can never itself be a
	// finding; it is carried so the report can say which rows rest on it.
	fromLive bool
}

// collect turns one source into manifests.
//
// Each type answers the same question, "what Applications and ApplicationSets
// does this repository define?", and they coexist. A real repository
// routinely has ApplicationSets committed as YAML *and* a chart that renders
// more of them, which is why this is a list of strategies rather than a mode
// switch.
func collect(ctx context.Context, repoRoot string, cfg *Config, inv *Inventory, s Source) ([]docs, error) {
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
		// Neither binary ships in the gate's image, which carries helm and
		// kubeconform only. A kustomize source therefore works on a
		// workstation and on a CI runner that installs one, and fails
		// in-cluster, so say which of the two happened rather than letting
		// "kustomize build failed" cover both. A broken kustomization and a
		// missing builder want completely different next actions.
		if !haveEither("kustomize", "kubectl") {
			return nil, fmt.Errorf("source %q is type kustomize, but neither `kustomize` nor "+
				"`kubectl` is on PATH -- the gate's image ships neither, so this source can only "+
				"be rendered where one is installed", s.Name)
		}
		out, kErr := run(ctx, repoRoot, "kustomize", "build", s.Path)
		if kErr != nil {
			// `kubectl kustomize` is the same builder and is far more often
			// present; falling back costs nothing and removes a dependency
			// most clusters' operators already have another copy of.
			var fallbackErr error
			out, fallbackErr = run(ctx, repoRoot, "kubectl", "kustomize", s.Path)
			if fallbackErr != nil {
				// Both failures, because with only the second a kustomization
				// that `kustomize` explained is reported through
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

	case SourceDirectory:
		full, err := containedPath(repoRoot, s.Path)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", s.Name, err)
		}
		objs, err := readArgoDirectory(full, s.Recurse, s.Include, s.Exclude)
		if err != nil {
			return nil, fmt.Errorf("source %q: %w", s.Name, err)
		}
		return []docs{{source: s.Name, objects: objs}}, nil

	case SourceLive:
		// Nothing is read. These objects came from the caller, which for the
		// only producer means the spec ArgoCD has applied, and the report
		// says so wherever one of these contributes a row.
		return []docs{{source: s.Name, objects: s.Objects, fromLive: true}}, nil

	case SourceHelm:
		return collectHelm(ctx, repoRoot, inv, s)

	case SourceArgoCDBootstrap:
		return collectBootstrap(ctx, repoRoot, cfg, inv, s)
	}
	return nil, fmt.Errorf("source %q: unhandled type %q", s.Name, s.Type)
}

// collectHelm renders a chart, once per cluster in scope when the chart or its
// value files are templated on cluster metadata, otherwise once.
func collectHelm(ctx context.Context, repoRoot string, inv *Inventory, s Source) ([]docs, error) {
	perCluster := templated(s.Chart) || anyTemplated(s.ValueFiles)

	if !perCluster && s.Selector == nil {
		objs, err := helmRender(ctx, repoRoot, s.Chart, resolveAll(s.ValueFiles, nil), s.ValuesInline)
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
		objs, err := helmRender(ctx, repoRoot, chart, resolveAll(s.ValueFiles, data), s.ValuesInline)
		if err != nil {
			return nil, fmt.Errorf("source %q (cluster %s): %w", s.Name, c.Name, err)
		}
		out = append(out, docs{source: s.Name, cluster: &c, objects: objs, scope: s.Scope})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("source %q matched no cluster in the inventory.\n\n"+
			"Nothing it defines can be checked, so the comparison would be made\n"+
			"against an empty set and report no change. Check the source's selector\n"+
			"against the cluster labels ArgoCD reports", s.Name)
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
		// not that cluster's file. ArgoCD's own
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

// containedPath resolves a repository-relative path inside the checkout, or
// says which containment rule it broke.
//
// Every path this package joins to a checkout arrives from the pull request
// under review: `helm.valueFiles` off an Application, `chart`, `path` and
// `paths` off the head revision's `.gitops-gate.yaml`. filepath.Join cleans a
// path, which is not the same as containing one, so `../../../../etc/x.yaml`
// reached helm as `-f`, and anything that parsed as a YAML mapping merged into
// the values and rendered into the report comment the gate publishes: a
// file-read primitive aimed at whatever the pod has mounted.
//
// safepath is this repository's one answer to that question, and it asks the
// filesystem rather than the string, which is the half that catches a tracked
// symlink standing where a legitimate path is expected.
func containedPath(repoRoot, rel string) (string, error) {
	return safepath.Resolve(repoRoot, rel)
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

	// One policy for both failures, because these manifests are the render: a
	// file that will not parse fails the source, and a file that will not read
	// used to vanish from it silently, so a permission problem produced a
	// smaller target table, which the diff then reported as objects removed by
	// this pull request.
	var out []map[string]any
	for _, f := range files {
		// Containment is asked of the match, not of the pattern. A pattern
		// says nothing about what a `*` expanded to, and nothing at all about
		// a tracked symlink standing where the match landed.
		rel, err := filepath.Rel(repoRoot, f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if _, err := containedPath(repoRoot, filepath.ToSlash(rel)); err != nil {
			return nil, err
		}

		// A glob can match a directory, `paths: [apps]` rather than
		// `apps/*.yaml`. Not a manifest, and not an error either: skipped
		// explicitly so it does not arrive at ReadFile and become one.
		if info, err := os.Stat(f); err == nil && info.IsDir() {
			continue
		}
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

// toolTimeout bounds one helm, kustomize or kubectl invocation.
//
// Generous, because a chart render pulls from a registry over somebody else's
// network; bounded, because a gate run that never returns is worse than one
// that says it could not look. The agent side has always had this
// (agent.helmTimeout); the gate did not, and its own request-level
// context.WithTimeout could not help, because a context cancels nothing for a
// process started with exec.Command. A `helm template` stalling on a slow or
// hostile upstream therefore outlived the gate timeout that was supposed to
// bound it and held a chart-diff worker slot for as long as it liked.
const toolTimeout = 3 * time.Minute

func run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, toolTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	// helm, kustomize or kubectl, and none of them has any use for a
	// credential this process loaded. cmd.Env was nil here, which is
	// os.Environ(), so a chart's own helm plugin ran with GIT_TOKEN,
	// ARGOCD_TOKEN and the model key in its environment.
	cmd.Env = childenv.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The kill arrives as "signal: killed", which reads like a crash and
		// sends a reader looking at the chart instead of at the clock.
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("%s ran past a deadline (%s per invocation, and the gate run has its own): %w",
				name, toolTimeout, ctx.Err())
		case ctx.Err() != nil:
			return nil, fmt.Errorf("%s was stopped before it finished: %w", name, ctx.Err())
		}
		// Redacted before it is quoted: this renders from a registry over
		// somebody else's network, and a host that echoes a request header
		// back inside an error body is echoing whatever it was sent. See
		// subprocess_stderr_test.go, which is the rule. The other half -- that
		// the child is not handed this process's credentials in the first
		// place -- is cmd.Env above.
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(redact.Text(stderr.String())))
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
