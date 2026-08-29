package migrate

import (
	"reflect"
	"strings"
	"testing"
)

// The line the gate writes and the migration the agent reads back must be the
// same thing, and this round-trip is the contract test for it; the report is
// the wire format, so drift in either direction is a repair that stops firing.
//
// The prose path, reached because no report here carries DroppedMarker, which
// is what a report from a gate older than the contract block looks like.
func TestReportLineRoundTrips(t *testing.T) {
	want := []Dropped{{
		CRD:      "externalsecrets.external-secrets.io",
		Group:    "external-secrets.io",
		Kind:     "ExternalSecret",
		Versions: []string{"v1alpha1", "v1beta1"},
		Target:   "v1",
	}}
	// Chart-diff stamps the Application's namespace on every object, CRDs
	// included, so the real line carries " in <namespace>" and the parser must
	// read both shapes.
	for _, object := range []string{
		"CustomResourceDefinition/externalsecrets.external-secrets.io",
		"CustomResourceDefinition/externalsecrets.external-secrets.io in external-secrets",
	} {
		line := Line(object, "v1alpha1, v1beta1", "ExternalSecret", "v1")
		got := ParseReport("<!-- gitops-gate -->\nsome preamble\n" + line + "\ntrailing text\n")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip lost something:\n line %q\n got  %+v\n want %+v", line, got, want)
		}
	}
}

// A line without the kind-and-destination suffix names a problem without
// naming where consumers must move. The old gate wrote exactly that shape, and
// a repair must not guess a destination it was never given.
func TestTheOldLineFormatIsNotRepairable(t *testing.T) {
	line := Line("CustomResourceDefinition/externalsecrets.external-secrets.io",
		"v1alpha1, v1beta1", "", "")
	if got := ParseReport(line); len(got) != 0 {
		t.Fatalf("a suffix-less line must parse as nothing, got %+v", got)
	}
}

func TestTheContractBlockRoundTrips(t *testing.T) {
	want := []Dropped{{
		CRD:      "externalsecrets.external-secrets.io",
		Group:    "external-secrets.io",
		Kind:     "ExternalSecret",
		Versions: []string{"v1alpha1", "v1beta1"},
		Target:   "v1",
	}}
	report := "<!-- gitops-gate -->\n" + DroppedBlock(want) + "\n### Resources\n\nprose about it\n"
	if got := ParseReport(report); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip lost something:\n got  %+v\n want %+v\n\n%s", got, want, report)
	}
}

// The block is the instruction and the prose is a description of it. A report
// carrying the block is from a gate that put every repairable finding there,
// so a bullet claiming one more is either drift or a name some chart chose,
// and neither is a reason to rewrite manifests.
func TestProseIsNotReadWhenTheContractBlockIsPresent(t *testing.T) {
	forged := Line("CustomResourceDefinition/secrets.evil.io", "v1", "Secret", "v2")

	real := []Dropped{{CRD: "things.example.io", Group: "example.io", Kind: "Thing",
		Versions: []string{"v1beta1"}, Target: "v1"}}
	got := ParseReport(DroppedBlock(real) + "\n" + forged + "\n")
	if !reflect.DeepEqual(got, real) {
		t.Fatalf("a bullet beside the block was read as a migration: %+v", got)
	}

	// And the same with nothing genuine to hide behind: an empty block is
	// still a gate saying it has no repair, not a gate that failed to mention
	// one.
	if got := ParseReport(DroppedBlock(nil) + "\n" + forged + "\n"); len(got) != 0 {
		t.Fatalf("an empty contract must stay empty, got %+v", got)
	}
}

// Every field is a name something else chose: the CRD's own, the kind it
// declares, the versions it serves. An entry that does not hold its shape is
// not a migration to attempt with the parts that did parse.
func TestAMalformedContractEntryParsesAsNothing(t *testing.T) {
	for _, entry := range []string{
		"crd=things.example.io kind=Thing versions=v1beta1",               // no destination
		"crd=things.example.io kind=Thing target=v1",                      // no versions
		"crd=things kind=Thing versions=v1beta1 target=v1",                // not plural.group
		"crd=things.example.io kind=Th*ng versions=v1beta1 target=v1",     // not a kind
		"crd=things.example.io kind=Thing versions=v1beta1 target=next",   // not a version
		"crd=things.example.io kind=Thing versions=v1beta1,v 1 target=v1", // a version with a space in it
	} {
		report := DroppedMarker + "\n" + entry + "\n-->\n"
		if got := ParseReport(report); len(got) != 0 {
			t.Errorf("%q must parse as nothing, got %+v", entry, got)
		}
	}
}

// The writer refuses what the reader would refuse. A finding the block cannot
// carry is left out of it rather than written in a shape the agent will drop
// on the other side, where nothing would say why the repair never ran.
func TestTheBlockOmitsWhatItCannotCarry(t *testing.T) {
	block := DroppedBlock([]Dropped{
		{CRD: "things.example.io", Kind: "Thing", Versions: []string{"v1beta1"}, Target: "v1"},
		// A definition removed outright: no survivor, so no destination, and
		// no rewrite anyone could perform.
		{CRD: "gone.example.io", Kind: "Gone", Versions: []string{"v1"}},
		// A name that would end the comment early and take the genuine
		// finding above it with it.
		{CRD: "evil.example.io\n-->", Kind: "Evil", Versions: []string{"v1"}, Target: "v2"},
	})
	if strings.Contains(block, "gone.example.io") || strings.Contains(block, "evil.example.io") {
		t.Fatalf("the block carried an entry it cannot honour:\n%s", block)
	}
	got := ParseReport(block)
	if len(got) != 1 || got[0].CRD != "things.example.io" {
		t.Fatalf("want only the repairable finding, got %+v", got)
	}
}

func TestOtherBlockersMatchTheGateHeadings(t *testing.T) {
	crdOnly := "### Resources\n\n" +
		Line("CustomResourceDefinition/things.example.io", "v1beta1", "Thing", "v1")
	if OtherBlockers(crdOnly) {
		t.Error("a dropped served version alone is not an other blocker")
	}
	for _, h := range []string{HeadingTargeting, HeadingSource, HeadingAPIVersion} {
		if !OtherBlockers(crdOnly + "\n" + h + "\n") {
			t.Errorf("%q must count as another blocker", h)
		}
	}
}

// Ga before beta before alpha, higher numbers first within a class, the API
// server's own priority, so the destination the gate names is the one a
// Kubernetes operator would have picked.
func TestPreferredVersion(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"v1beta1", "v1"}, "v1"},
		{[]string{"v1", "v2"}, "v2"},
		{[]string{"v2beta1", "v1"}, "v1"},
		{[]string{"v1alpha1", "v1beta1"}, "v1beta1"},
		{[]string{"v2beta1", "v2beta2"}, "v2beta2"},
		{[]string{"weird", "v1"}, "v1"},
		{[]string{}, ""},
	}
	for _, c := range cases {
		if got := PreferredVersion(c.in); got != c.want {
			t.Errorf("PreferredVersion(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// A CRD removed outright renders its own line, and the parser deliberately
// cannot act on it: there is no destination, so there is no repair, only
// the consumer count, which the gate carries itself.
func TestARemovalLineIsNotRepairable(t *testing.T) {
	line := Line("CustomResourceDefinition/admissionreports.kyverno.io in kyverno",
		"v1alpha2, v2", "AdmissionReport", "")
	if !strings.Contains(line, "removed outright") {
		t.Fatalf("want the removal named as such, got %q", line)
	}
	if got := ParseReport(line); len(got) != 0 {
		t.Fatalf("a removal has no destination and must parse as nothing, got %+v", got)
	}
}

// All three object groups render an identical bullet. A reader that matched
// the bullet without tracking its group treated an added definition as a
// removed one, and every false hit is a live apiserver lookup on a path a
// human is waiting on.
func TestParseRemovedCRDsOnlyReadsTheRemovedGroup(t *testing.T) {
	report := strings.Join([]string{
		"### Rendered objects",
		"",
		ObjectGroupHeading(GroupAdded, 1),
		"",
		"- `CustomResourceDefinition/widgets.example.io`",
		"",
		ObjectGroupHeading(GroupRemoved, 2),
		"",
		"- `CustomResourceDefinition/gadgets.example.io`",
		"- `CustomResourceDefinition/sprockets.example.io in tools`",
		"  - a note about the removal",
		"",
		ObjectGroupHeading(GroupChanged, 1),
		"",
		"- `CustomResourceDefinition/doodads.example.io`",
		"",
		"### Versions",
		"",
		"- `CustomResourceDefinition/decoys.example.io`",
	}, "\n")

	got := ParseRemovedCRDs(report)
	want := []string{"gadgets.example.io", "sprockets.example.io"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseRemovedCRDsFindsNothingWhenNothingWasRemoved(t *testing.T) {
	report := ObjectGroupHeading(GroupAdded, 1) + "\n\n- `CustomResourceDefinition/widgets.example.io`\n"
	if got := ParseRemovedCRDs(report); len(got) != 0 {
		t.Errorf("added definitions must not read as removed: %v", got)
	}
}

// The prose scrape and the structured count answer slightly different
// questions, and the difference bites exactly on a retry: gate/diff.go prints
// the apiVersion heading whenever any such object exists, while Blockers
// excludes the ones the repair is itself performing. Scraping won there, so
// after a partial repair the deterministic path was skipped on attempt 2; the
// attempt the cap exists to allow.
func TestStructuredCountBeatsTheProseScrapeOnAMigrationRetry(t *testing.T) {
	report := strings.Join([]string{
		BlockersMarker + "targeting=0 source=0 apiVersion=0 consumers=2 unscanned=0 valuesDropped=0 schema=0 -->",
		"",
		HeadingAPIVersion,
		"",
		"- `CustomResourceDefinition/widgets.example.io`: no longer serves `v1alpha1` — `Widget` manifests must move to `v1`",
	}, "\n")

	if !OtherBlockers(report) {
		t.Fatal("the prose scrape should see the heading -- that is the drift being tested")
	}
	b, ok := ParseBlockers(report)
	if !ok {
		t.Fatal("the marker should parse")
	}
	if b.OtherThanDropped() {
		t.Error("a migration this repair is performing is not an unrelated blocker")
	}
}

func TestOtherThanDroppedCountsEveryUnrelatedReason(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    Blockers
		want bool
	}{
		{"nothing", Blockers{}, false},
		{"only dropped versions", Blockers{Consumers: 3}, false},
		{"unscanned only", Blockers{Unscanned: 1}, false},
		{"values dropped only", Blockers{ValuesDropped: 2}, false},
		{"targeting", Blockers{Targeting: 1}, true},
		{"source", Blockers{Source: 1}, true},
		{"apiVersion", Blockers{APIVersion: 1}, true},
		{"schema", Blockers{Schema: 1}, true},
	} {
		if got := tc.b.OtherThanDropped(); got != tc.want {
			t.Errorf("%s: OtherThanDropped() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
