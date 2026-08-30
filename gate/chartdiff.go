package gate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

// pair is one Application as it stands on each side of the change, and which
// of the two kinds of move put it here.
//
// valuesOnly is carried rather than recomputed from `before.Version ==
// after.Version`, because two later decisions turn on it -- which side's
// failure is this change's doing, and whether a values surface can have moved
// at all -- and each would otherwise re-derive the same comparison and be free
// to get it wrong.
type pair struct {
	before, after Row
	valuesOnly    bool
}

// ChartDiff renders every Application this change moves, on both sides, and
// diffs the resources.
//
// This is the difference between "cert-manager moved from v1.21.1 to v1.22.0"
// and "that bump adds four CRDs, removes a container, and moves a Service
// port". Every incident worth gating on is of the second kind: a pull request
// that renders fine and breaks at runtime. The version alone cannot show it,
// and a reviewer, or a triage agent, reading only the version is reasoning
// from far less than they appear to have.
//
// An Application "moves" two ways, and for a long time this saw only the
// first. A chart version bump is the loud one. The quiet one is an edit to a
// value file the Application layers: the chart, the version and the
// Application are all identical on both sides, so the row is identical on both
// sides, and until this rendered them nothing rendered them at all. That is
// not a corner: an addon whose chart lives in a registry is reached *only*
// through this step, because derivation cannot turn somebody else's artifact
// into a path in this checkout, so every values-only tuning of every remote
// chart passed the gate unrendered and unmentioned. Measured on one repository,
// 31 of 66 Applications were in that class.
//
// It costs a chart pull and two renders per moved Application, which is why
// the values side is decided by reading the files rather than by rendering
// them: same bytes, no render.
//
// The third result is what this step knows that no object diff can carry: a
// chart that will not render at the head revision, and settings this
// repository makes that the new chart version no longer declares. It comes
// from here because this is the only place that has both chart versions and
// the Application's own value files in hand.
//
// before and after are named because they are adjacent same-typed slices,
// "the second return" only tells a reader which one to count to, and swapping
// them at a call site would compile and silently invert the diff.
func ChartDiff(ctx context.Context, trees Worktrees, cfg *Config, base, head *Table) (
	before, after []Object, found ChartFindings) {

	// A caller with only one worktree renders both sides from it, which is
	// what this did for every pair before there were two. It cannot see a
	// values change -- both sides read the same files -- and the pairing below
	// does not claim to, but the version moves are still worth rendering, and
	// an empty root passed to helm would resolve every value file against this
	// process's working directory instead.
	baseRoot := trees.Base
	if baseRoot == "" {
		baseRoot = trees.Head
	}

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
		if b.Chart != h.Chart || b.ChartRepo != h.ChartRepo {
			continue
		}
		switch {
		case b.Version != h.Version:
			pairs = append(pairs, pair{before: b, after: h})
		case trees.Base != "" && valuesMoved(trees, b, h):
			pairs = append(pairs, pair{before: b, after: h, valuesOnly: true})
		}
	}
	if len(pairs) == 0 {
		return nil, nil, ChartFindings{}
	}

	// Results are written into a slot per pair rather than appended under a
	// mutex. Appending publishes them in goroutine-completion order, which is
	// a different order on every run, and the drops and warnings go straight
	// into the pull request comment, so the gate would report a difference
	// between two runs that is not a difference in the manifests. Slotting by
	// index keeps the report in `pairs` order, which is head.Rows order.
	type result struct {
		before, after []Object
		changes       []ObjectChange
		unrenderable  []Unrenderable
		leaves        map[string]bool
		warnings      []Markdown
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
					res.warnings = append(res.warnings, Markdown(Inline(fmt.Sprintf(
						"%s: %s, so %s's resource changes are NOT covered",
						p.after.App, reason, p.after.Chart))))
					return
				}
				if r.ChartRepo != "" && !strings.HasPrefix(r.ChartRepo, "oci://") {
					if reason := cfg.egressCheck(r.ChartRepo, releaseNameFor(r), r.Version); reason != "" {
						res.warnings = append(res.warnings, Markdown(Inline(fmt.Sprintf(
							"%s: %s, so %s's resource changes are NOT covered",
							p.after.App, reason, p.after.Chart))))
						return
					}
				}
			}

			// Each side from its own checkout. Rendering both from the head
			// is what made a values edit invisible even once it was paired:
			// the two renders differ only in the version, so an Application
			// whose version did not move produced two identical streams and
			// an empty diff. It also silently mis-attributed the mixed case,
			// a bump and a values edit in one pull request, to the bump.
			b, errB := renderChartVersion(ctx, baseRoot, p.before)
			a, errA := renderChartVersion(ctx, trees.Head, p.after)

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
			//
			// On a values-only pair the order of those two tests is reversed,
			// and it has to be. A version move can only fail at the head
			// version because of the head version; a values-only pair holds
			// one version, so a chart that does not render outside a cluster
			// -- a `lookup`, a required value the cluster supplies -- fails on
			// both sides, and the head test read first would turn every pull
			// request touching that repository red for a condition it
			// inherited. Base first asks the question that separates them:
			// did this change break it, or was it already broken?
			switch {
			case p.valuesOnly && errB != nil:
				res.warnings = append(res.warnings, Markdown(Inline(fmt.Sprintf(
					"%s: %s does not render at %s with the values it had before this change, "+
						"so its resource changes are NOT covered: %v",
					p.after.App, p.after.Chart, p.after.Version, errB))))
			case errA != nil:
				res.changes = append(res.changes, ObjectChange{
					Kind:    ObjectRenderFailed,
					Object:  p.after.App,
					Cluster: p.after.Cluster,
					From:    p.before.Version,
					To:      p.after.Version,
					Reason:  errA.Error(),
				})
				// The same fact in the form a repair needs, alongside the
				// form a reader needs. Two derivations of one finding, from
				// one place, for the reason DroppedBlock exists: the prose is
				// where a chart's own strings end up, and a repair must not be
				// spelled by anything a chart chose.
				res.unrenderable = append(res.unrenderable, Unrenderable{
					Head: p.after, From: p.before.Version, Reason: errA.Error(),
				})
			case errB != nil:
				res.warnings = append(res.warnings, Markdown(Inline(fmt.Sprintf(
					"%s: %s renders at %s but not at %s, so its resource changes are NOT covered: %v",
					p.after.App, p.after.Chart, p.after.Version, p.before.Version, errB))))
			default:
				res.before, res.after = b, a
			}

			// What this Application's own values supply, for the field diff
			// to mark against. Read here because this goroutine already has
			// the row and the checkout in hand, and best-effort for the same
			// reason the README half of the values surface is: a values file
			// that does not parse costs the marking, not the diff.
			if vals, err := repoValues(trees.Head, p.after); err == nil {
				res.leaves = leafValueSet(vals, identityTokens(p.after))
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
			//
			// Not on a values-only pair. The question it answers is "what did
			// the new chart version stop declaring", and there is no new chart
			// version: it would pull the same chart twice, compare a values
			// surface with itself, and spend two registry round trips to
			// report nothing.
			if p.valuesOnly {
				return
			}
			gone, err := droppedValues(ctx, trees.Head, p.before, p.after)
			switch {
			case err != nil:
				res.warnings = append(res.warnings, Markdown(Inline(fmt.Sprintf(
					"%s: could not compare %s's values surface across versions, so settings it stops reading are NOT covered: %v",
					p.after.App, p.after.Chart, err))))
			case len(gone) > 0:
				res.changes = append(res.changes, ObjectChange{
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

	for i, res := range results {
		before = append(before, res.before...)
		after = append(after, res.after...)
		found.Changes = append(found.Changes, res.changes...)
		found.Unrenderable = append(found.Unrenderable, res.unrenderable...)
		found.Warnings = append(found.Warnings, res.warnings...)
		if res.leaves != nil {
			if found.ValuesLeaves == nil {
				found.ValuesLeaves = map[string]map[string]bool{}
			}
			found.ValuesLeaves[pairs[i].after.App] = res.leaves
		}
	}
	return before, after, found
}

// valuesMoved reports whether this Application layers different values over
// its chart than it did before the change.
//
// It reads files rather than rendering, and that is the whole reason the
// values half of ChartDiff is affordable. The alternative -- render every
// Application on both sides and diff -- is two chart pulls and two renders per
// Application per pull request, sixty-six of each on the repository this was
// measured against, for an answer that is "nothing changed" almost every time.
// Identical bytes cannot produce a different render, so identical bytes are
// where the question stops.
//
// The comparison is over the list *and* its contents. A pull request that adds
// a values layer, drops one, or reorders two has changed what helm reads
// without changing any file, and helm's last-wins layering makes the order
// load-bearing.
func valuesMoved(trees Worktrees, before, after Row) bool {
	if before.ValuesInline != after.ValuesInline || len(before.ValueFiles) != len(after.ValueFiles) {
		return true
	}
	for i, vf := range after.ValueFiles {
		if before.ValueFiles[i] != vf {
			return true
		}
		if !bytes.Equal(valuesBytes(trees.Base, vf), valuesBytes(trees.Head, vf)) {
			return true
		}
	}
	return false
}

// valuesBytes is one values layer as helm would read it in one checkout, or
// nil where there is nothing to read.
//
// Both ways of having nothing to read collapse to nil deliberately. A layer
// this cluster has no file for is ArgoCD's own ignoreMissingValueFiles and is
// routine on every side; a path that leaves the checkout is a finding, but it
// is the render's finding to report, by name and with the rule it broke.
// Deciding either of them here would mean this function reporting on a
// containment failure that the side it is comparing against might not have.
func valuesBytes(root, vf string) []byte {
	full, err := containedPath(root, StripValuesRef(vf))
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	return raw
}

// leafValueSet renders every scalar leaf of a values document as the string
// the field diff will compare against. The value is whether that leaf may
// match as a substring; identity leaves are in the set but equality-only.
func leafValueSet(node any, identity map[string]bool) map[string]bool {
	out := map[string]bool{}
	var rec func(any)
	rec = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			for _, v := range t {
				rec(v)
			}
		case []any:
			for _, v := range t {
				rec(v)
			}
		case nil:
		default:
			s := fmt.Sprintf("%v", t)
			out[s] = !identity[s]
		}
	}
	rec(node)
	return out
}

// identityTokens is what this Application calls itself: the names every render
// of it is stamped with, in labels, selectors, resource names and the strings
// a chart builds out of them.
//
// A values leaf equal to one of these is kept for the equality form -- a
// repository that sets `nameOverride` to the chart's own name did choose that
// value, and arguably said something -- but demoted out of the substring form,
// where it says nothing at all. `kyverno` is inside
// `kyverno-admission-controller`, inside `app.kubernetes.io/instance: kyverno`,
// and inside every aggregation label a bump churns; a mark that fires on all of
// them tells a reader their own settings moved when what moved was the chart's
// naming.
func identityTokens(r Row) map[string]bool {
	out := map[string]bool{}
	for _, tok := range []string{r.Chart, releaseNameFor(r), r.App, r.Namespace} {
		if tok != "" {
			out[tok] = true
		}
	}
	return out
}

// ChartFindings is what a chart-diff pass produced besides rendered objects.
//
// One struct rather than three more returns. Three same-shaped slices in an
// argument list is a call site where a reader counts positions, and the two
// that are both findings differ only in who reads them.
type ChartFindings struct {
	// Changes are the findings a reader sees: a chart that would not render,
	// and settings the new version stops declaring.
	Changes []ObjectChange
	// Unrenderable is the repair contract for the render failures in Changes:
	// what to pull, what to render it with, and what helm said. Nothing in it
	// is prose, and nothing in it was chosen by a chart.
	Unrenderable []Unrenderable
	// Warnings are coverage this pass lost, and blame nobody for.
	Warnings []Markdown
	// ValuesLeaves is, per rendered Application, the scalar values its own
	// values supply -- Table.ValuesLeaves' content, computed here because
	// this is the only place with the row, its identity and the checkout
	// together.
	ValuesLeaves map[string]map[string]bool
}

// Unrenderable is one Application whose chart will not render at the version
// this change moves it to, in the form a repair needs.
//
// The whole head Row rather than a copy of five of its fields: a repair has to
// pull the same chart from the same repository and render it with the same
// values, and a hand-copied subset is a subset that goes stale. From is the
// version that still rendered, which is not on the head Row and is what makes
// the failure this change's doing rather than the repository's.
type Unrenderable struct {
	Head   Row
	From   string
	Reason string
}

// renderChartVersion renders one Application's chart at its pinned version,
// with the value files and inline values that Application uses.
//
// repoRoot is the checkout this side of the comparison reads its value files
// from, which is that side's own: the base row's files come from the base
// worktree and the head row's from the head one, or an Application whose
// version did not move renders twice from one set of files and diffs to
// nothing.
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
		clean := StripValuesRef(vf)
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

// StripValuesRef turns `$values/charts/x/values.yaml` into
// `charts/x/values.yaml`.
//
// A multi-source Application names its values source with `ref:` and then
// refers to it by `$<ref>/`. Both readers of a Row's value files, the render
// and the values-surface comparison, have to strip it the same way, or one of
// them looks for a file whose name starts with a dollar sign and quietly finds
// nothing.
func StripValuesRef(vf string) string {
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
