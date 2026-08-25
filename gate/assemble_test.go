package gate

import "testing"

// Assemble exists so the in-cluster service and the CLI cannot reach different
// verdicts on one commit. Without a worktree it must still produce the table
// diff -- "no repository" is a narrower scan, not a different answer about
// what it did look at.
func TestAssembleWithoutAWorktreeStillDiffsTheTables(t *testing.T) {
	base := &Table{Rows: []Row{
		{Cluster: "hub", AppSet: "addons", App: "podinfo", Chart: "podinfo",
			ChartRepo: "https://example.test", Version: "1.0.0", SourceType: "helm"},
	}}
	head := &Table{Rows: []Row{
		{Cluster: "hub", AppSet: "addons", App: "podinfo", Chart: "podinfo",
			ChartRepo: "https://example.test", Version: "2.0.0", SourceType: "helm"},
	}}

	res := Assemble("", nil, base, head)
	if len(res.Versions) != 1 {
		t.Fatalf("want the version change, got %+v", res.Versions)
	}
	if res.Versions[0].From != "1.0.0" || res.Versions[0].To != "2.0.0" {
		t.Errorf("got %+v", res.Versions[0])
	}
	if blocking, headline := res.Verdict(); blocking {
		t.Errorf("a version bump alone must not block: %q", headline)
	}
}

// Value drops are appended after Diff has already sorted, so they have to be
// re-sorted in rather than left as an unsorted tail.
func TestAssembleKeepsObjectChangesSorted(t *testing.T) {
	res := &DiffResult{Objects: []ObjectChange{
		{Kind: "changed", Object: "Deployment/a in x"},
		{Kind: "removed", Object: "Service/z in x"},
	}}
	res.Objects = append(res.Objects,
		ObjectChange{Kind: "added", Object: "ConfigMap/m in x"},
		ObjectChange{Kind: "changed", Object: "Deployment/b in x"},
	)
	sortObjectChanges(res.Objects)

	var got []string
	for _, o := range res.Objects {
		got = append(got, o.Kind+"|"+o.Object)
	}
	want := []string{
		"added|ConfigMap/m in x",
		"changed|Deployment/a in x",
		"changed|Deployment/b in x",
		"removed|Service/z in x",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
