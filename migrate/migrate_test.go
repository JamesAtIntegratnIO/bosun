package migrate

import (
	"reflect"
	"strings"
	"testing"
)

// The line the gate writes and the migration the agent reads back must be the
// same thing, and this round-trip is the contract test for it -- the report is
// the wire format, so drift in either direction is a repair that stops firing.
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

// GA before beta before alpha, higher numbers first within a class -- the API
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
// cannot act on it: there is no destination, so there is no repair -- only
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
// the bullet without tracking its group treated an ADDED definition as a
// removed one -- and every false hit is a live apiserver lookup on a path a
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
