package gate

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Only rows whose version moved are rendered. A chart pull and two
// renders per Application is real cost, and on a typical bump pull request
// exactly one row qualifies.
// oneTree is both sides of a comparison in one directory: these tests are
// about which pairs get selected by version, and an identical Base and Head
// is what says "no values moved" without a fixture per row.
func oneTree(t *testing.T) Worktrees {
	t.Helper()
	dir := t.TempDir()
	return Worktrees{Base: dir, Head: dir}
}

func TestChartDiffOnlyConsidersVersionChanges(t *testing.T) {
	mk := func(app, chart, version string, sourceType RowSource) Row {
		return Row{
			Cluster: "prod", App: app, Chart: chart, ChartRepo: "https://charts.example.com",
			Version: version, SourceType: sourceType,
		}
	}
	base := &Table{Rows: []Row{
		mk("same", "a", "1.0.0", "helm"),
		mk("moved", "b", "1.0.0", "helm"),
		mk("path-source", "c", "1.0.0", "path"),
	}}
	head := &Table{Rows: []Row{
		mk("same", "a", "1.0.0", "helm"),
		mk("moved", "b", "2.0.0", "helm"),
		mk("path-source", "c", "2.0.0", "path"),
	}}

	// No helm on PATH in this test, and no chart repository behind these
	// rows either; what matters is which pairs it selects, which is
	// observable through what it says about the renders that failed.
	cfg := &Config{Concurrency: 2}
	_, _, found := ChartDiff(context.Background(), oneTree(t), cfg, base, head)

	said := joinLines(found.Warnings)
	for _, f := range found.Changes {
		said += "\n" + f.Object
	}
	if strings.Contains(said, "same") {
		t.Error("an unchanged version must not be rendered")
	}
	if strings.Contains(said, "path-source") {
		t.Error("a path source has no chart to render")
	}
	// Positive as well as negative: with both negatives satisfied by an empty
	// result, this test passed for a while against a ChartDiff that had
	// stopped saying anything at all.
	if !strings.Contains(said, "moved") {
		t.Errorf("the row whose version moved must be rendered and reported on, got %+v", found)
	}
}

// A chart that cannot be pulled at the version this change moves to is a
// finding, not a coverage note. "No resource changes", "we could not look" and
// "we looked and it does not work" are three different answers, and only the
// last one is about the pull request.
func TestChartDiffReportsWhatItCouldNotRender(t *testing.T) {
	row := func(v string) Row {
		return Row{
			Cluster: "prod", App: "thing", Chart: "nonexistent-chart-xyz",
			ChartRepo: "https://charts.invalid.example", Version: v, SourceType: "helm",
		}
	}
	base := &Table{Rows: []Row{row("1.0.0")}}
	head := &Table{Rows: []Row{row("2.0.0")}}

	before, after, found := ChartDiff(context.Background(), oneTree(t), &Config{Concurrency: 1}, base, head)
	if len(before) != 0 || len(after) != 0 {
		t.Fatal("a failed render must contribute no objects")
	}
	var failures []ObjectChange
	for _, f := range found.Changes {
		if f.Kind == ObjectRenderFailed {
			failures = append(failures, f)
		}
	}
	if len(failures) != 1 {
		t.Fatalf("the head revision's failure must be a finding, got %+v", found.Changes)
	}
	if failures[0].Object != "thing" || failures[0].To != "2.0.0" {
		t.Errorf("the finding must name the Application and the version it failed at, got %+v", failures[0])
	}
	// The version alone says only that something is wrong, and with no render
	// there is nowhere else for the reader to look it up.
	if failures[0].Reason == "" {
		t.Error("the finding must carry what helm said")
	}
	// The reader's finding and the repair's contract are two derivations of
	// one fact, and a repair that never hears about a failure the report
	// blocks on is the gap this whole change is closing.
	if len(found.Unrenderable) != 1 || found.Unrenderable[0].Head.Version != "2.0.0" ||
		found.Unrenderable[0].From != "1.0.0" {
		t.Fatalf("the repair contract must carry the same failure, got %+v", found.Unrenderable)
	}
}

// Helm stamps chart and app version onto every object it renders. Hashing
// those makes a bump report every resource as changed, measured at 101 of
// 105 on one cert-manager bump, burying the handful that changed.
func TestVersionStampsDoNotCountAsChanges(t *testing.T) {
	withStamp := func(chart, version string) map[string]any {
		return map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{
				"name": "controller", "namespace": "x",
				"labels": map[string]any{
					"helm.sh/chart":             chart,
					"app.kubernetes.io/version": version,
					"app.kubernetes.io/name":    "controller",
				},
			},
			"spec": map[string]any{"replicas": 1},
		}
	}
	a, _ := objectFrom("s", "prod", "", withStamp("cert-manager-1.19.3", "v1.19.3"))
	b, _ := objectFrom("s", "prod", "", withStamp("cert-manager-1.21.1", "v1.21.1"))
	if a.Hash != b.Hash {
		t.Fatal("a version stamp alone must not read as a changed resource")
	}

	// A real change still registers.
	changed := withStamp("cert-manager-1.21.1", "v1.21.1")
	changed["spec"] = map[string]any{"replicas": 3}
	c, _ := objectFrom("s", "prod", "", changed)
	if a.Hash == c.Hash {
		t.Fatal("a real spec change must register")
	}
}

// The same resource changing identically on several clusters is one finding.
func TestIdenticalChangesAcrossClustersCollapse(t *testing.T) {
	base := []Object{
		{Cluster: "a", Kind: "Deployment", Namespace: "x", Name: "c", APIVersion: "apps/v1", Hash: "1"},
		{Cluster: "b", Kind: "Deployment", Namespace: "x", Name: "c", APIVersion: "apps/v1", Hash: "1"},
	}
	head := []Object{
		{Cluster: "a", Kind: "Deployment", Namespace: "x", Name: "c", APIVersion: "apps/v1", Hash: "2"},
		{Cluster: "b", Kind: "Deployment", Namespace: "x", Name: "c", APIVersion: "apps/v1", Hash: "2"},
	}
	got := diffObjects(base, head, nil)
	if len(got) != 1 {
		t.Fatalf("want one collapsed change, got %d: %+v", len(got), got)
	}
	// Exact, not Contains: the input arrives from a map range, so a report
	// that names both clusters in an unstable order still differs run to run.
	if got[0].Cluster != "a, b" {
		t.Errorf("want cluster %q, got %q", "a, b", got[0].Cluster)
	}
}

// The two loops in diffObjects range over maps, so the only way to know the
// report is stable is to run it enough times to catch an unstable one.
func TestCollapsedChangeIsStableAcrossRuns(t *testing.T) {
	mk := func(hash string) []Object {
		var out []Object
		for _, c := range []string{"hub", "hub-east", "edge", "adam", "zulu"} {
			out = append(out,
				Object{Cluster: c, Kind: "Deployment", Namespace: "x", Name: "c", APIVersion: "apps/v1", Hash: hash},
				Object{Cluster: c, Kind: "Service", Namespace: "x", Name: "s", APIVersion: "v1", Hash: hash},
			)
		}
		return out
	}
	base, head := mk("1"), mk("2")

	want := diffObjects(base, head, nil)
	for i := 0; i < 200; i++ {
		got := diffObjects(base, head, nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("run %d differs:\n got %+v\nwant %+v", i, got, want)
		}
	}
	for _, c := range want {
		// "hub" must survive alongside "hub-east"; a substring membership
		// test would have swallowed it.
		if c.Cluster != "adam, edge, hub, hub-east, zulu" {
			t.Errorf("want every cluster named in sorted order, got %q", c.Cluster)
		}
	}
}

// An OCI repository URL is the chart. ArgoCD accepts one that already ends in
// the chart name alongside a `chart` field naming the same thing, and the gate
// used to append regardless, producing `.../charts/bosun/bosun`, a 403, and an
// addon silently dropped from resource-level coverage.
func TestOCIChartRefDoesNotDoubleTheChartName(t *testing.T) {
	for _, tc := range []struct {
		name, repo, chart, want string
	}{
		{
			name:  "repo already ends in the chart name",
			repo:  "oci://ghcr.io/org/charts/bosun",
			chart: "bosun",
			want:  "oci://ghcr.io/org/charts/bosun",
		},
		{
			name:  "repo is the parent path",
			repo:  "oci://ghcr.io/org/charts",
			chart: "bosun",
			want:  "oci://ghcr.io/org/charts/bosun",
		},
		{
			name:  "trailing slash is not a path segment",
			repo:  "oci://ghcr.io/org/charts/bosun/",
			chart: "bosun",
			want:  "oci://ghcr.io/org/charts/bosun",
		},
		{
			name:  "a chart whose name is a suffix of the last segment is still appended",
			repo:  "oci://ghcr.io/org/charts/kargo-pipelines",
			chart: "pipelines",
			want:  "oci://ghcr.io/org/charts/kargo-pipelines/pipelines",
		},
		{
			name:  "no chart name",
			repo:  "oci://ghcr.io/org/charts/bosun",
			chart: "",
			want:  "oci://ghcr.io/org/charts/bosun",
		},
		{
			name:  "a classic repo passes the bare chart name and uses --repo",
			repo:  "https://charts.example",
			chart: "thing",
			want:  "thing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := chartRef(Row{ChartRepo: tc.repo, Chart: tc.chart})
			if got != tc.want {
				t.Errorf("chartRef(%q, %q) = %q, want %q", tc.repo, tc.chart, got, tc.want)
			}
		})
	}
}
