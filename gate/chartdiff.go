package gate

import (
	"context"
	"fmt"
	"os"
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
// findings is what this step knows that no object diff can carry: a chart that
// will not render at the version the head revision moves it to, and settings
// this repository makes that the new chart version no longer declares. They
// come from here because this is the only place that has both chart versions
// and the Application's own value files in hand.
//
// The results are named because two of them are adjacent same-typed slices,
// "the third return" only tells a reader which one to count to, and swapping
// before and after at a call site would compile and silently invert the diff.
func ChartDiff(ctx context.Context, repoRoot string, cfg *Config, base, head *Table) (
	before, after []Object, findings []ObjectChange, warnings []string) {
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
		found         []ObjectChange
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

			b, errB := renderChartVersion(ctx, repoRoot, p.before)
			a, errA := renderChartVersion(ctx, repoRoot, p.after)

			// The two failures are different facts and only one of them is
			// this change's doing, which is why they are no longer reported
			// through the same sentence.
			//
			// The head revision is what merges. A chart that will not render
			// at the version this pull request moves to is an Application
			// that cannot sync once it does, and that is a finding, not a
			// coverage gap: "we looked and it does not work" is stronger
			// evidence than the unscanned consumer this gate already blocks
			// on. It used to be a warning, which counts towards nothing, so
			// the strictest possible failure -- a chart whose
			// values.schema.json rejects what this repository sets -- was the
			// one the gate passed in silence.
			//
			// A failure at the base version is coverage loss and stays a
			// warning. The repository was already in that state before this
			// change, there is no diff to compute either way, and blocking a
			// pull request for the condition it inherited helps nobody.
			switch {
			case errA != nil:
				res.found = append(res.found, ObjectChange{
					Kind:    ObjectRenderFailed,
					Object:  p.after.App,
					Cluster: p.after.Cluster,
					From:    p.before.Version,
					To:      p.after.Version,
					Reason:  errA.Error(),
				})
			case errB != nil:
				res.warnings = append(res.warnings, fmt.Sprintf(
					"%s: %s renders at %s but not at %s, so its resource changes are NOT covered: %v",
					p.after.App, p.after.Chart, p.after.Version, p.before.Version, errB))
			default:
				res.before, res.after = b, a
			}

			// Reached whether or not either render did, because it does not
			// depend on one: the values surface comes from `helm show`, not
			// from `helm template`. Behind the early return this used to sit
			// under, a chart that hard-failed on a strict values.schema.json
			// never reached the one check that names the stale keys, so the
			// clearer the breakage the less the report said about it.
			//
			// A settings drop is reported even when the render succeeded; it
			// is invisible in the render by definition, because helm ignores
			// a value it does not know rather than failing on it.
			gone, err := droppedValues(ctx, repoRoot, p.before, p.after)
			switch {
			case err != nil:
				res.warnings = append(res.warnings, fmt.Sprintf(
					"%s: could not compare %s's values surface across versions, so settings it stops reading are NOT covered: %v",
					p.after.App, p.after.Chart, err))
			case len(gone) > 0:
				res.found = append(res.found, ObjectChange{
					Kind:    ObjectValuesKeyDropped,
					Object:  p.after.App,
					Cluster: p.after.Cluster,
					From:    p.before.Version,
					To:      p.after.Version,
					Keys:    gone,
				})
			}
		}(i, p)
	}
	wg.Wait()

	for _, res := range results {
		before = append(before, res.before...)
		after = append(after, res.after...)
		findings = append(findings, res.found...)
		warnings = append(warnings, res.warnings...)
	}
	return before, after, findings, warnings
}

// renderChartVersion renders one Application's chart at its pinned version,
// with the value files and inline values that Application uses.
func renderChartVersion(ctx context.Context, repoRoot string, r Row) ([]Object, error) {
	chartArgs, err := HelmChartArgs(r.ChartRepo, r.Chart)
	if err != nil {
		return nil, err
	}
	if err := RefuseFlagLike("chart version", r.Version); err != nil {
		return nil, err
	}
	args := append([]string{"template", releaseNameFor(r)}, chartArgs...)
	args = append(args, "--version", r.Version)

	for _, vf := range r.ValueFiles {
		// `$values/x` refers to the multi-source values ref, which is this
		// repository. A file that does not exist for this Application is
		// normal, ArgoCD's ignoreMissingValueFiles behaves the same way.
		clean := stripValuesRef(vf)
		full, err := containedPath(repoRoot, clean)
		if err != nil {
			// The list is `helm.valueFiles` off an Application in the pull
			// request, so this is the head choosing what helm reads. A path
			// that leaves the checkout is reported rather than skipped: the
			// existence check above it used to be the only gate, and a file
			// that existed and parsed merged into the values and rendered
			// into the published comment.
			return nil, fmt.Errorf("%s: value file %q: %w", r.App, vf, err)
		}
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

	out, err := run(ctx, repoRoot, "helm", args...)
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

// stripValuesRef turns `$values/charts/x/values.yaml` into
// `charts/x/values.yaml`.
//
// A multi-source Application names its values source with `ref:` and then
// refers to it by `$<ref>/`. Both readers of a Row's value files, the render
// and the values-surface comparison, have to strip it the same way, or one of
// them looks for a file whose name starts with a dollar sign and quietly finds
// nothing.
func stripValuesRef(vf string) string {
	if i := strings.Index(vf, "/"); strings.HasPrefix(vf, "$") && i > 0 {
		return vf[i+1:]
	}
	return vf
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
