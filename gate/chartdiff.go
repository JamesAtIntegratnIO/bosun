package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ChartDiff renders every Application whose chart version changed, at both
// versions, and diffs the resources.
//
// This is the difference between "cert-manager moved from v1.21.1 to v1.22.0"
// and "that bump adds four CRDs, removes a container, and moves a Service
// port". Every incident worth gating on is of the second kind: a pull request
// that renders fine and breaks at runtime. The version alone cannot show it,
// and a reviewer, or a triage agent, reading only the version is reasoning
// from far less than they appear to have.
//
// It costs a chart pull and two renders per changed Application, so it runs
// only for rows whose version moved, which on a typical bump pull
// request is one.
//
// valuesDropped is findings that are not object diffs: settings this
// repository makes that the new chart version no longer declares. They come
// from here because this is the only place that has both chart versions and
// the Application's own value files in hand.
//
// The results are named because two of them are adjacent same-typed slices,
// "the third return" only tells a reader which one to count to, and swapping
// before and after at a call site would compile and silently invert the diff.
func ChartDiff(repoRoot string, cfg *Config, base, head *Table) (
	before, after []Object, valuesDropped []ObjectChange, warnings []string) {
	type pair struct{ before, after Row }

	baseByKey := map[string]Row{}
	for _, r := range base.Rows {
		baseByKey[r.Key()] = r
	}

	var pairs []pair
	for _, h := range head.Rows {
		b, ok := baseByKey[h.Key()]
		if !ok || h.SourceType != RowHelm || b.SourceType != RowHelm {
			continue
		}
		if b.Version == h.Version || b.Chart != h.Chart || b.ChartRepo != h.ChartRepo {
			continue
		}
		pairs = append(pairs, pair{before: b, after: h})
	}
	if len(pairs) == 0 {
		return nil, nil, nil, nil
	}

	// Results are written into a slot per pair rather than appended under a
	// mutex. Appending publishes them in goroutine-completion order, which is
	// a different order on every run, and the drops and warnings go straight
	// into the pull request comment, so the gate would report a difference
	// between two runs that is not a difference in the manifests. Slotting by
	// index keeps the report in `pairs` order, which is head.Rows order.
	type result struct {
		before, after []Object
		drop          *ObjectChange
		warnings      []string
	}
	results := make([]result, len(pairs))

	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.workers())

	for i, p := range pairs {
		wg.Add(1)
		go func(i int, p pair) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res := &results[i]

			// helm is a subprocess: the egress transport cannot see inside it,
			// so the destination is checked and recorded here or it is neither.
			// Both versions are pulled, and they can differ in repository.
			for _, r := range []Row{p.before, p.after} {
				if reason := cfg.egressCheck(chartRef(r), releaseNameFor(r), r.Version); reason != "" {
					res.warnings = append(res.warnings, fmt.Sprintf(
						"%s: %s, so %s's resource changes are NOT covered",
						p.after.App, reason, p.after.Chart))
					return
				}
				if r.ChartRepo != "" && !strings.HasPrefix(r.ChartRepo, "oci://") {
					if reason := cfg.egressCheck(r.ChartRepo, releaseNameFor(r), r.Version); reason != "" {
						res.warnings = append(res.warnings, fmt.Sprintf(
							"%s: %s, so %s's resource changes are NOT covered",
							p.after.App, reason, p.after.Chart))
						return
					}
				}
			}

			b, errB := renderChartVersion(repoRoot, p.before)
			a, errA := renderChartVersion(repoRoot, p.after)

			// A chart that cannot be pulled is reported, never silently
			// skipped: "no resource changes" and "we could not look" must not
			// read identically.
			if errB != nil || errA != nil {
				err := errB
				if err == nil {
					err = errA
				}
				res.warnings = append(res.warnings, fmt.Sprintf(
					"%s: could not render %s at both versions, so its resource changes are NOT covered: %v",
					p.after.App, p.after.Chart, err))
				return
			}
			res.before, res.after = b, a

			// A settings drop is reported even though the render succeeded;
			// it is invisible in the render by definition, because helm
			// ignores a value it does not know rather than failing on it.
			gone, err := droppedValues(repoRoot, p.before, p.after)
			switch {
			case err != nil:
				res.warnings = append(res.warnings, fmt.Sprintf(
					"%s: could not compare %s's values surface across versions, so settings it stops reading are NOT covered: %v",
					p.after.App, p.after.Chart, err))
			case len(gone) > 0:
				res.drop = &ObjectChange{
					Kind:    ObjectValuesKeyDropped,
					Object:  p.after.App,
					Cluster: p.after.Cluster,
					From:    p.before.Version,
					To:      p.after.Version,
					Keys:    gone,
				}
			}
		}(i, p)
	}
	wg.Wait()

	for _, res := range results {
		before = append(before, res.before...)
		after = append(after, res.after...)
		if res.drop != nil {
			valuesDropped = append(valuesDropped, *res.drop)
		}
		warnings = append(warnings, res.warnings...)
	}
	return before, after, valuesDropped, warnings
}

// renderChartVersion renders one Application's chart at its pinned version,
// with the value files and inline values that Application uses.
func renderChartVersion(repoRoot string, r Row) ([]Object, error) {
	chartArgs, err := HelmChartArgs(r.ChartRepo, r.Chart)
	if err != nil {
		return nil, err
	}
	args := append([]string{"template", releaseNameFor(r)}, chartArgs...)
	args = append(args, "--version", r.Version)

	for _, vf := range r.ValueFiles {
		// `$values/x` refers to the multi-source values ref, which is this
		// repository. A file that does not exist for this Application is
		// normal, ArgoCD's ignoreMissingValueFiles behaves the same way.
		clean := vf
		if i := strings.Index(clean, "/"); strings.HasPrefix(clean, "$") && i > 0 {
			clean = clean[i+1:]
		}
		full := filepath.Join(repoRoot, clean)
		if _, err := os.Stat(full); err == nil {
			args = append(args, "-f", full)
		}
	}

	if strings.TrimSpace(r.ValuesInline) != "" {
		f, err := os.CreateTemp("", "inline-*.yaml")
		if err != nil {
			return nil, err
		}
		defer func() { _ = os.Remove(f.Name()) }()
		if _, err := f.WriteString(r.ValuesInline); err != nil {
			_ = f.Close()
			return nil, err
		}
		// Checked: helm is about to read this file, and a short write here
		// renders a chart with half the Application's inline values; a diff
		// that looks like the bump removed settings it did not touch.
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("writing inline values for %s: %w", r.App, err)
		}
		args = append(args, "-f", f.Name())
	}

	// The Application's destination namespace, or namespaced resources render
	// into helm's default and the report names a namespace nothing deploys to.
	if r.Namespace != "" {
		args = append(args, "--namespace", r.Namespace)
	}

	// CRDs are the point of several of these diffs; a chart that starts or
	// stops shipping one is exactly what a version bump hides.
	args = append(args, "--include-crds")

	out, err := run(repoRoot, "helm", args...)
	if err != nil {
		return nil, err
	}
	objs, err := parseStream(out)
	if err != nil {
		return nil, err
	}

	var result []Object
	for _, o := range objs {
		if obj, ok := objectFrom(r.App, r.Cluster, r.Namespace, o); ok {
			result = append(result, obj)
		}
	}
	return result, nil
}

// chartRef builds what `helm template` needs: an OCI URL renders directly, a
// classic repo needs,repo rather than a pre-added repository, so nothing has
// to mutate the runner's helm config.
//
// For OCI there is no separate chart name; the repository URL is the chart,
// and ArgoCD accepts a `repoURL` that already ends in it alongside a `chart`
// naming the same thing. Appending unconditionally turned
//
//	oci://ghcr.io/org/charts/bosun + bosun
//
// into `.../charts/bosun/bosun`, which the registry answers 403. The cost was
// invisible in the worst way: chart-diff is skipped for that addon and the
// report says only "not covered", so every OCI-repo addon quietly lost its
// resource-level diff while the gate stayed green. chartRef is the reference
// a row resolves to, for the egress host check, which needs the destination,
// not the whole argument list.
func chartRef(r Row) string {
	if !strings.HasPrefix(r.ChartRepo, "oci://") {
		return r.Chart
	}
	return ociChartRef(r.ChartRepo, r.Chart)
}

func releaseNameFor(r Row) string {
	if r.Chart != "" {
		return r.Chart
	}
	return "release"
}
