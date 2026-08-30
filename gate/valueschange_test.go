package gate

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/charttest"
)

// warehouseChart is one version of one chart whose only interesting output is
// a field its values set, which is the shape of every values-only change: the
// chart did not move, the Application did not move, and what deploys is
// different.
func warehouseChart(t *testing.T) string {
	t.Helper()
	return charttest.Serve(t, "pipelines", charttest.Version{
		Version: "0.2.0",
		Files: map[string]string{
			"Chart.yaml":  "apiVersion: v2\nname: pipelines\nversion: 0.2.0\n",
			"values.yaml": "defaults:\n  interval: 24h0m0s\n",
			"templates/warehouse.yaml": "apiVersion: kargo.akuity.io/v1alpha1\n" +
				"kind: Warehouse\nmetadata:\n  name: charts\nspec:\n" +
				"  interval: {{ .Values.defaults.interval }}\n",
		},
	})
}

// The second of the two defects this file's fix comes from, and the worse one:
// the change the pull request actually made was never reported at all.
//
// One Application, one chart version, one edited value file. Nothing in the
// target table moves -- the row is identical on both sides, because the row
// records which values files an Application layers and not what is in them --
// so the version-move pairing that was the whole of chart rendering selected
// nothing, and the report said "no change" about thirty-nine rendered fields
// that changed.
//
// The Application is reached only this way. Its chart is somebody else's
// artifact in a registry, so derivation cannot turn it into a path in this
// checkout and no source renders it; on the repository this was measured
// against, thirty-one of sixty-six Applications were in that class, and every
// values-only tuning of any of them passed the gate unseen.
func TestAValuesOnlyChangeIsRendered(t *testing.T) {
	requireTool(t, "helm")

	repo := warehouseChart(t)
	// `$values/` is deliberate: a multi-source Application names its values
	// source with `ref:` and addresses it that way, which is the exact shape
	// the defect was found on.
	const vf = "$values/addons/kargo-projects/values.yaml"
	row := Row{
		Cluster: "prod", App: "pipelines-prod", Chart: "pipelines",
		ChartRepo: repo, Version: "0.2.0", SourceType: RowHelm, ValueFiles: []string{vf},
	}
	base := &Table{Rows: []Row{row}}
	head := &Table{Rows: []Row{row}}

	trees := Worktrees{
		Base: writeRepo(t, map[string]string{
			"addons/kargo-projects/values.yaml": "defaults:\n  interval: 24h0m0s\n"}),
		Head: writeRepo(t, map[string]string{
			"addons/kargo-projects/values.yaml": "defaults:\n  interval: 6h0m0s\n"}),
	}

	res := Assemble(context.Background(), trees, &Config{Concurrency: 1}, base, head)

	var changed []ObjectChange
	for _, o := range res.Objects {
		if o.Kind == ObjectChanged {
			changed = append(changed, o)
		}
	}
	if len(changed) != 1 {
		t.Fatalf("want the one Warehouse whose interval moved, got %+v", res.Objects)
	}
	if !strings.Contains(changed[0].Object, "Warehouse") {
		t.Errorf("changed object = %q, want the Warehouse", changed[0].Object)
	}

	var report strings.Builder
	res.Report(&report)
	published := report.String()
	for _, want := range []string{"Warehouse", "24h0m0s", "6h0m0s"} {
		if !strings.Contains(published, want) {
			t.Errorf("the report does not say %q:\n%s", want, published)
		}
	}
}

// The same Application with the same values on both sides. Nothing to render,
// and nothing rendered: the values comparison reads files rather than pulling
// charts precisely so that the answer "nothing moved" costs no registry round
// trip, and a test that only proved the positive would not notice if it did.
func TestAnUnchangedValueFileIsNotRendered(t *testing.T) {
	repo := "https://charts.invalid.example"
	const vf = "$values/addons/kargo-projects/values.yaml"
	row := Row{
		Cluster: "prod", App: "pipelines-prod", Chart: "pipelines",
		ChartRepo: repo, Version: "0.2.0", SourceType: RowHelm, ValueFiles: []string{vf},
	}
	files := map[string]string{"addons/kargo-projects/values.yaml": "defaults:\n  interval: 24h0m0s\n"}
	trees := Worktrees{Base: writeRepo(t, files), Head: writeRepo(t, files)}

	before, after, found := ChartDiff(context.Background(), trees, &Config{Concurrency: 1},
		&Table{Rows: []Row{row}}, &Table{Rows: []Row{row}})

	// The chart repository does not resolve, so anything selected here would
	// have failed to render and said so.
	if len(before) != 0 || len(after) != 0 || len(found.Changes) != 0 || len(found.Warnings) != 0 {
		t.Fatalf("identical values must select nothing, got %d/%d objects, %+v, %+v",
			len(before), len(after), found.Changes, found.Warnings)
	}
}

// A values layer added, removed or reordered changes what helm reads without
// changing a single file, and helm's layering is last-wins, so the order is
// part of the answer.
func TestTheValuesListItselfIsPartOfTheComparison(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"a.yaml": "interval: 1h\n",
		"b.yaml": "interval: 2h\n",
	})
	trees := Worktrees{Base: root, Head: root}
	row := func(files ...string) Row {
		return Row{App: "thing", Chart: "c", ChartRepo: "r", Version: "1", SourceType: RowHelm, ValueFiles: files}
	}
	for _, tc := range []struct {
		name          string
		before, after Row
		want          bool
	}{
		{"identical", row("a.yaml"), row("a.yaml"), false},
		{"a layer added", row("a.yaml"), row("a.yaml", "b.yaml"), true},
		{"a layer dropped", row("a.yaml", "b.yaml"), row("a.yaml"), true},
		{"reordered", row("a.yaml", "b.yaml"), row("b.yaml", "a.yaml"), true},
		{"inline values changed", Row{ValuesInline: "x: 1"}, Row{ValuesInline: "x: 2"}, true},
		{"a layer this cluster has no file for", row("missing.yaml"), row("missing.yaml"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := valuesMoved(trees, tc.before, tc.after); got != tc.want {
				t.Errorf("valuesMoved = %v, want %v", got, tc.want)
			}
		})
	}
}

// A values edit can break a render as thoroughly as a version bump can, and
// when it does it is unambiguously this pull request's doing: the chart did
// not move, so nothing else changed that could have caused it. Blocking, with
// the repair contract, exactly as the bump case is.
func TestAValuesEditThatBreaksTheRenderBlocks(t *testing.T) {
	requireTool(t, "helm")

	repo := charttest.Serve(t, "thing", charttest.Version{
		Version: "1.0.0",
		Files: map[string]string{
			"Chart.yaml": "apiVersion: v2\nname: thing\nversion: 1.0.0\n",
			"templates/cm.yaml": "{{- if not .Values.port }}{{ fail \"port must be set\" }}{{- end }}\n" +
				"apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: thing\ndata:\n" +
				"  port: {{ .Values.port | quote }}\n",
		},
	})
	row := Row{
		Cluster: "prod", App: "thing-prod", Chart: "thing", ChartRepo: repo,
		Version: "1.0.0", SourceType: RowHelm, ValueFiles: []string{"values/thing.yaml"},
	}
	trees := Worktrees{
		Base: writeRepo(t, map[string]string{"values/thing.yaml": "port: 8080\n"}),
		Head: writeRepo(t, map[string]string{"values/thing.yaml": "prot: 8080\n"}),
	}

	res := Assemble(context.Background(), trees, &Config{Concurrency: 1},
		&Table{Rows: []Row{row}}, &Table{Rows: []Row{row}})

	if b := res.Blockers(); b.Unrenderable != 1 {
		t.Fatalf("Unrenderable = %d, want 1 (blockers: %+v)", b.Unrenderable, b)
	}
	if len(res.Unrenderable) != 1 || res.Unrenderable[0].Head.App != "thing-prod" {
		t.Errorf("the repair contract must name the Application, got %+v", res.Unrenderable)
	}
}

// The other side of that, and the reason a values-only pair asks about the
// base render first. Charts that do not render outside a cluster are ordinary
// -- a `lookup`, a value the cluster supplies -- and one of them sitting in a
// repository must not turn every pull request that edits its values red for a
// condition the repository already had. It is coverage lost, said out loud,
// and nothing more.
func TestAChartAlreadyUnrenderableIsNotThisChangesFault(t *testing.T) {
	requireTool(t, "helm")

	repo := charttest.Serve(t, "thing", charttest.Version{
		Version: "1.0.0",
		Files: map[string]string{
			"Chart.yaml":        "apiVersion: v2\nname: thing\nversion: 1.0.0\n",
			"templates/cm.yaml": "{{ fail \"this chart needs a cluster\" }}\n",
		},
	})
	row := Row{
		Cluster: "prod", App: "thing-prod", Chart: "thing", ChartRepo: repo,
		Version: "1.0.0", SourceType: RowHelm, ValueFiles: []string{"values/thing.yaml"},
	}
	trees := Worktrees{
		Base: writeRepo(t, map[string]string{"values/thing.yaml": "port: 8080\n"}),
		Head: writeRepo(t, map[string]string{"values/thing.yaml": "port: 9090\n"}),
	}

	res := Assemble(context.Background(), trees, &Config{Concurrency: 1},
		&Table{Rows: []Row{row}}, &Table{Rows: []Row{row}})

	if res.Blocking() {
		_, headline := res.Verdict()
		t.Fatalf("a chart that rendered on neither side must not block: %q", headline)
	}
	if len(res.Warnings) != 1 || !strings.Contains(string(res.Warnings[0]), "NOT covered") {
		t.Errorf("the lost coverage must be stated, got %+v", res.Warnings)
	}
}
