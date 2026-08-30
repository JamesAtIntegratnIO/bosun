package gate

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/charttest"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The bump this finding comes from. A chart gained a values.schema.json that
// forbids what it used to accept, the repository still set four of those keys,
// and helm refused to render the new version at all. The gate reported that
// under "Not covered", counted nothing, and published a green verdict on a
// change that could not deploy; a person fixed it by hand.
//
// The shape is worth stating because it is the wrong way round. A chart with no
// schema ignores the stale keys, renders, and the values-drop check turns the
// gate red. Adding `additionalProperties: false` made helm fail harder and the
// gate quieter, so the clearer the breakage the weaker the verdict.
func TestASchemaThatRejectsThisRepositorysValuesBlocks(t *testing.T) {
	requireTool(t, "helm")

	repo := charttest.Strict(t)
	root := writeRepo(t, map[string]string{"values/thing.yaml": "legacy: true\n"})
	row := func(v string) Row {
		return Row{
			Cluster: "prod", App: "thing-prod", Chart: "thing", ChartRepo: repo,
			Version: v, SourceType: RowHelm, ValueFiles: []string{"values/thing.yaml"},
		}
	}
	base := &Table{Rows: []Row{row("1.0.0")}}
	head := &Table{Rows: []Row{row("2.0.0")}}

	res := Assemble(context.Background(), Worktrees{Base: root, Head: root}, &Config{Concurrency: 1}, base, head)

	if !res.Blocking() {
		t.Fatal("a head revision that will not render must block")
	}
	b := res.Blockers()
	if b.Unrenderable != 1 {
		t.Errorf("Unrenderable = %d, want 1 (blockers: %+v)", b.Unrenderable, b)
	}
	// The other half of the same defect. The values-surface comparison never
	// needed the render -- it reads `helm show`, not `helm template` -- but it
	// sat behind the early return that a failed render took, so the one check
	// written for exactly this case was the one a strict schema switched off.
	if b.ValuesDropped != 1 {
		t.Errorf("ValuesDropped = %d, want 1: the dropped key must be named even though nothing rendered (blockers: %+v)", b.ValuesDropped, b)
	}

	var report strings.Builder
	res.Report(&report)
	published := report.String()

	// Rule 1a: the gate writes this and the agent reads it back out of a
	// published comment, and no compiler watches the join.
	got, ok := migrate.ParseBlockers(published)
	if !ok {
		t.Fatal("the report must carry the machine-readable breakdown")
	}
	if got != b {
		t.Errorf("the report round trip lost the count:\n published %+v\n in process %+v", got, b)
	}
	// A blocker with a repository-side remedy: the agent must not answer this
	// with "nothing here can change what blocks this".
	if !got.RepoSideRemedy() {
		t.Error("the values this repository sets are what the chart rejected, so there is something here to change")
	}
	if !got.OtherThanDropped() {
		t.Error("a dropped-version repair would not fix this, so it must read as an unrelated blocker")
	}

	if !strings.Contains(published, "🔴") {
		t.Error("the headline must be red")
	}
	// What helm said is the whole finding; there is no render to inspect
	// instead.
	if !strings.Contains(published, "legacy") {
		t.Errorf("the report must name the key that broke the render:\n%s", published)
	}
	if strings.Contains(published, "### Resources\n\n### ") {
		t.Error("nothing rendered, so the Resources heading must not stand over an empty section")
	}
}

// The other side of the asymmetry. A chart that renders at the version this
// change moves to and not at the one it came from is a fact about the base
// revision: there is no diff to compute either way, but nothing here is this
// pull request's doing, and blocking a change for the state it inherited helps
// nobody. It stays a warning, under "Not covered", where it belongs.
func TestOnlyTheHeadRevisionsRenderFailureBlocks(t *testing.T) {
	requireTool(t, "helm")

	repo := charttest.Strict(t)
	root := writeRepo(t, map[string]string{"values/thing.yaml": "greeting: hi\npodPort: 8080\n"})
	row := func(v string) Row {
		return Row{
			Cluster: "prod", App: "thing-prod", Chart: "thing", ChartRepo: repo,
			Version: v, SourceType: RowHelm, ValueFiles: []string{"values/thing.yaml"},
		}
	}
	// 0.9.0 is not in the index; 2.0.0 is, and accepts these values.
	base := &Table{Rows: []Row{row("0.9.0")}}
	head := &Table{Rows: []Row{row("2.0.0")}}

	res := Assemble(context.Background(), Worktrees{Base: root, Head: root}, &Config{Concurrency: 1}, base, head)

	if b := res.Blockers(); b.Unrenderable != 0 {
		t.Errorf("a base-revision failure must not block, got %+v", b)
	}
	if res.Blocking() {
		t.Error("a base-revision failure must not block")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("it is still coverage loss and must be said out loud")
	}
	// Named in the direction that happened. Both failures used to be reported
	// through one sentence that said "at both versions", which is the one
	// thing that was not true in either case.
	joined := joinLines(res.Warnings)
	if !strings.Contains(joined, "renders at 2.0.0 but not at 0.9.0") {
		t.Errorf("the warning must say which revision failed:\n%s", joined)
	}
}
