package gate

import (
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The report used to read identically whether it was blocking or not; the
// findings were listed and the verdict lived only in the commit status. Two
// reports on one pull request, a red one and the green one after a repair,
// were then indistinguishable at a glance.
func TestReportLeadsWithItsVerdict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		res      *DiffResult
		blocking bool
		want     string
	}{
		{
			name:     "nothing at all",
			res:      &DiffResult{},
			blocking: false,
			want:     "No change to what gets deployed",
		},
		{
			name:     "a version bump alone is not blocking",
			res:      &DiffResult{Versions: []Change{{App: "a"}, {App: "b"}}},
			blocking: false,
			want:     "2 versions changed",
		},
		{
			name:     "targeting blocks",
			res:      &DiffResult{Targeting: []Change{{App: "a"}}},
			blocking: true,
			want:     "1 Application now generated",
		},
		{
			name: "an object whose apiVersion moved blocks",
			res: &DiffResult{Objects: []ObjectChange{
				{Kind: "apiVersion", Object: "PodDisruptionBudget/x"},
			}},
			blocking: true,
			want:     "1 object whose own apiVersion moved",
		},
		{
			name: "the same change as the repair does not block",
			res: &DiffResult{Objects: []ObjectChange{
				{Kind: "apiVersion", Object: "ClusterSecretStore/x", PartOfMigration: true},
			}},
			blocking: false,
			want:     "1 rendered object changed",
		},
		{
			name: "a dropped version blocks by its consumers",
			res: &DiffResult{Objects: []ObjectChange{
				{Kind: "crdVersionRemoved", ConsumersKnown: true, ConsumerFiles: []string{"a.yaml", "b.yaml"}},
			}},
			blocking: true,
			want:     "2 manifests still declaring a dropped API version",
		},
		{
			name: "consumers that could not be counted block, and say so",
			res: &DiffResult{Objects: []ObjectChange{
				{Kind: "crdVersionRemoved", ConsumersKnown: false},
			}},
			blocking: true,
			want:     "1 definition whose consumers could not be counted",
		},
		{
			name: "a dropped version with zero consumers does not block",
			res: &DiffResult{Objects: []ObjectChange{
				{Kind: "crdVersionRemoved", ConsumersKnown: true},
			}},
			blocking: false,
			want:     "1 rendered object changed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocking, headline := tc.res.Verdict()
			if blocking != tc.blocking {
				t.Fatalf("blocking = %v, want %v (headline %q)", blocking, tc.blocking, headline)
			}
			if !strings.Contains(headline, tc.want) {
				t.Fatalf("headline %q does not contain %q", headline, tc.want)
			}
			if blocking != tc.res.Blocking() {
				t.Fatalf("Verdict disagrees with Blocking(): %v vs %v", blocking, tc.res.Blocking())
			}

			var b strings.Builder
			tc.res.Report(&b)
			mark := "✅"
			if tc.blocking {
				mark = "🔴"
			}
			if !strings.Contains(b.String(), mark+" "+headline) {
				t.Fatalf("report does not lead with its verdict:\n%s", b.String())
			}
		})
	}
}

// The agent finds unrepairable blockers with strings.Contains over the whole
// report. A headline that happened to contain one of those heading strings
// would make every report claim an unrepairable blocker it does not have.
func TestTheVerdictHeadlineCannotBeMistakenForAParsedHeading(t *testing.T) {
	cases := []*DiffResult{
		{Targeting: []Change{{App: "a"}}},
		{Other: []Change{{App: "a"}}},
		{Objects: []ObjectChange{{Kind: "apiVersion", Object: "x"}}},
		{Objects: []ObjectChange{{Kind: "crdVersionRemoved", ConsumersKnown: true, ConsumerFiles: []string{"a"}}}},
		{Versions: []Change{{App: "a"}}},
		{},
	}
	for _, res := range cases {
		_, headline := res.Verdict()
		for _, h := range []string{migrate.HeadingTargeting, migrate.HeadingSource, migrate.HeadingAPIVersion} {
			if strings.Contains(headline, h) {
				t.Fatalf("headline %q contains the parsed heading %q", headline, h)
			}
		}
	}
}

// A run blocked only by schema validation used to publish a report headlined
// "No blocking findings" and an all-zero blockers marker beside a failure
// status, because the report was written before validation ran. The report,
// the marker and the status have to be three renderings of one answer.
func TestSchemaFailuresBlockAndReachTheHeadlineAndTheMarker(t *testing.T) {
	res := &DiffResult{SchemaFailures: 3}

	if !res.Blocking() {
		t.Error("three rejected manifests must block")
	}
	blocking, headline := res.Verdict()
	if !blocking {
		t.Errorf("Verdict disagrees with Blocking: %q", headline)
	}
	if !strings.Contains(headline, "3 manifests the target schemas reject") {
		t.Errorf("headline does not name the reason: %q", headline)
	}
	if b := res.Blockers(); b.Schema != 3 || !b.Any() {
		t.Errorf("blockers do not carry the schema count: %+v", b)
	}

	var sb strings.Builder
	res.Report(&sb)
	got, ok := migrate.ParseBlockers(sb.String())
	if !ok {
		t.Fatal("report carries no blockers marker")
	}
	if got.Schema != 3 {
		t.Errorf("marker lost the schema count: %+v", got)
	}

	// The remedy is an author's, not the agent's, same class as an
	// apiVersion that moved under a chart-rendered object.
	if got.RepoSideRemedy() {
		t.Error("a schema failure has no repair the agent performs")
	}
}

// Zero must stay invisible: an older reader of the marker sees the same
// six fields it always did.
func TestNoSchemaFailuresLeavesTheVerdictAlone(t *testing.T) {
	res := &DiffResult{Versions: []Change{{App: "a"}}}
	if blocking, headline := res.Verdict(); blocking || !strings.Contains(headline, "No blocking findings") {
		t.Errorf("got blocking=%v %q", blocking, headline)
	}
}

// The gate writes the object groups and migrate reads them. The round trip is
// the only thing keeping "this definition was added" and "this definition is
// gone" apart in a report where both render the same bullet.
func TestRemovedGroupRoundTripsThroughMigrate(t *testing.T) {
	res := &DiffResult{Objects: []ObjectChange{
		{Kind: "added", Object: "CustomResourceDefinition/widgets.example.io", To: "apiextensions.k8s.io/v1"},
		{Kind: "removed", Object: "CustomResourceDefinition/gadgets.example.io", From: "apiextensions.k8s.io/v1"},
		{Kind: "changed", Object: "CustomResourceDefinition/doodads.example.io"},
	}}
	var sb strings.Builder
	res.Report(&sb)

	got := migrate.ParseRemovedCRDs(sb.String())
	if len(got) != 1 || got[0] != "gadgets.example.io" {
		t.Fatalf("got %v, want exactly [gadgets.example.io]\n\n%s", got, sb.String())
	}
}
