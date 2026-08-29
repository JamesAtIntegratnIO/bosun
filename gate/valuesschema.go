package gate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// A chart version's own values contract, as the chart ships it.
//
// `values.schema.json` is the only machine-readable statement a chart makes
// about what it will accept, and it is what turns "this bump broke the render"
// into "these four keys are the reason". Helm applies it before it templates
// anything, so a repository whose values a chart has outgrown fails hard, with
// the offending paths named, which is both the failure and the evidence for
// the repair.
//
// There is no `helm show schema`, so the file arrives the only way it can: pull
// the chart and read it out of the tarball. That is the same host, the same
// chart and the same version the render already reached, so it adds a download
// and no new trust.

// ChartValuesSchema is the values schema a chart declares at one version, or
// nil when it declares none.
//
// A chart without a schema is not an error and must not read as one. Most
// charts ship none; helm then accepts anything, which is precisely the
// condition the values-drop check exists for, and it is a different finding
// with a different remedy.
//
// workDir is where the chart is unpacked, and the caller owns it: a temporary
// directory it removes. Nothing is written outside it.
func ChartValuesSchema(ctx context.Context, cfg *Config, workDir string, r Row) (map[string]any, error) {
	// The same accountability trade chart-diff makes, for the same reason:
	// helm is a subprocess, an HTTP transport cannot see inside it, so the
	// destination is checked and recorded here or it is neither.
	if reason := cfg.egressCheck(chartRef(r), releaseNameFor(r), r.Version); reason != "" {
		return nil, fmt.Errorf("%s", reason)
	}
	if r.ChartRepo != "" && !strings.HasPrefix(r.ChartRepo, "oci://") {
		if reason := cfg.egressCheck(r.ChartRepo, releaseNameFor(r), r.Version); reason != "" {
			return nil, fmt.Errorf("%s", reason)
		}
	}

	chartArgs, err := HelmChartArgs(r.ChartRepo, r.Chart)
	if err != nil {
		return nil, err
	}
	if err := RefuseFlagLike("chart version", r.Version); err != nil {
		return nil, err
	}
	// The archive and the unpacked copy go to different places, because helm
	// puts the archive in its destination -- the working directory, here --
	// and untars beside it. Sharing one directory leaves the tarball's name
	// sitting next to the chart's, and "the thing helm unpacked" stops being
	// answerable from a listing.
	untar := filepath.Join(workDir, "unpacked")
	args := append([]string{"pull"}, chartArgs...)
	args = append(args, "--version", r.Version, "--untar", "--untardir", untar)
	if _, err := run(ctx, workDir, "helm", args...); err != nil {
		return nil, err
	}

	// The directory helm unpacked into is named by the chart's own
	// Chart.yaml, which is not always the name the Application uses to
	// reference it: an OCI repository carries the chart in its path, and
	// ArgoCD's `chart` field can be empty or can differ in case. Reading back
	// the one directory that appeared is the only name that cannot be wrong.
	dir, err := soleChartDir(untar)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "values.schema.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("%s %s ships a values.schema.json that is not JSON: %w", r.Chart, r.Version, err)
	}
	return schema, nil
}

// soleChartDir is the chart `helm pull --untar` unpacked, identified by the
// Chart.yaml inside it, and it insists there is exactly one.
//
// By its contents rather than by its name or its position, because neither is
// reliable. helm leaves a directory named for the archive beside the chart, so
// "the only directory" is two of them; and the chart's own directory is named
// from its Chart.yaml, which is not always the name the Application uses to
// reference it -- an OCI repository carries the chart in its path, and ArgoCD's
// `chart` field can be empty or differ in case.
//
// Exactly one, not the first: a directory left over from another version would
// otherwise answer for this one, which is the mistake here that produces a
// confident wrong schema rather than an error.
func soleChartDir(untarDir string) (string, error) {
	entries, err := os.ReadDir(untarDir)
	if err != nil {
		return "", err
	}
	var charts []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(untarDir, e.Name(), "Chart.yaml")); err == nil {
			charts = append(charts, e.Name())
		}
	}
	if len(charts) != 1 {
		return "", fmt.Errorf("helm unpacked %d charts and exactly one was expected", len(charts))
	}
	return filepath.Join(untarDir, charts[0]), nil
}

// RendersWith reports whether a chart version renders with a given set of
// values, and returns what helm said when it does not.
//
// The proof a values migration gets and a manifest migration cannot. A
// migrated manifest is judged by a schema walk and then hoped for; a migrated
// values document is judged by the program that refused the original, running
// the same way it ran when it refused it.
//
// The values are written to a file of their own rather than taken from the
// Row, deliberately: the Row's values were captured when the gate rendered,
// and the whole point of asking this question is that they are about to
// change. workDir is the caller's temporary directory.
func RendersWith(ctx context.Context, cfg *Config, workDir string, r Row, values map[string]any) error {
	if reason := cfg.egressCheck(chartRef(r), releaseNameFor(r), r.Version); reason != "" {
		return fmt.Errorf("%s", reason)
	}
	chartArgs, err := HelmChartArgs(r.ChartRepo, r.Chart)
	if err != nil {
		return err
	}
	if err := RefuseFlagLike("chart version", r.Version); err != nil {
		return err
	}
	raw, err := yaml.Marshal(values)
	if err != nil {
		return err
	}
	vf := filepath.Join(workDir, "proposed-values.yaml")
	if err := os.WriteFile(vf, raw, 0o600); err != nil {
		return err
	}
	args := append([]string{"template", releaseNameFor(r)}, chartArgs...)
	args = append(args, "--version", r.Version, "-f", vf, "--include-crds")
	if r.Namespace != "" {
		args = append(args, "--namespace", r.Namespace)
	}
	_, err = run(ctx, workDir, "helm", args...)
	return err
}

// ApplicationValues is what one Application passes to its chart, merged the
// way helm merges it: each value file over the last, inline values last of all.
//
// Exported because the repair needs the same document the render was refused
// for. Re-deriving it in the agent would mean a second implementation of
// helm's merge order and of the `$values/` reference form, and the first
// symptom of the two disagreeing would be a repair judged against values helm
// never saw.
func ApplicationValues(repoRoot string, r Row) (map[string]any, error) {
	return repoValues(repoRoot, r)
}
