package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// inventory is the fleet as the last live reading saw it. Its whole difficulty
// is that "nothing runs here" and "nothing has looked" are the same empty list
// unless the surface keeps them apart.

func TestTheInventoryIsEveryApplicationTheLiveReadingSaw(t *testing.T) {
	f := newFixture(t, nil).withGate(fleet())
	inv := f.inventory(t)

	if inv.Repository != "example/platform" || !inv.Swept {
		t.Fatalf("every result names its repository and its sweep; got %+v", inv)
	}
	if inv.Applications == nil {
		t.Fatal("a live reading that ran publishes its rows, even to say there were none")
	}
	apps := *inv.Applications
	if len(apps) != 3 {
		t.Fatalf("the live reading served three Applications; the inventory holds %d", len(apps))
	}
	first := apps[0]
	if first.Name.Text != "argo-cd" {
		t.Errorf("the row names the Application ArgoCD serves, got %+v", first.Name)
	}
	if first.Cluster == nil || first.Cluster.Text != "prod-eu" {
		t.Errorf("a row says which cluster the Application lands on, got %+v", first.Cluster)
	}
}

// Before the first sweep there is no fleet at all, and no claim about charts
// either.
//
// The single most expensive mistake this surface can make is an empty list
// read as "nothing runs here" from a process that has not looked, so the field
// is absent rather than empty and the sentence says which.
func TestBeforeTheFirstSweepThereIsNoFleetField(t *testing.T) {
	f := newFixture(t, nil)
	raw := f.call(t, "inventory")

	got := fields(t, raw)
	for _, absent := range []string{"applications", "chartDetail", "sweptAt", "ageSeconds"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%q is present before any sweep: %s", absent, raw)
		}
	}

	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Swept {
		t.Error("no sweep has completed, so nothing may claim one has")
	}
	if inv.Status.Origin != OriginBosun {
		t.Errorf("the sentence before the first sweep is bosun's own, got %+v", inv.Status)
	}
	if inv.Repository != "example/platform" {
		t.Errorf("a result names its repository even when it has nothing else to say, got %q",
			inv.Repository)
	}
}

// A sweep that completed without any gate run reading the fleet is its own
// answer, and it is not an empty fleet.
//
// The common shape on a quiet install: the reading is made when the gate
// renders a pull request, so a week with none open leaves this process holding
// a sweep and no fleet. Publishing zero rows for it would say ArgoCD serves
// nothing, which is the one thing nothing here has checked.
func TestASweptInstallThatHasReadNoFleetSaysSo(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked())
	raw := f.call(t, "inventory")

	if _, ok := fields(t, raw)["applications"]; ok {
		t.Errorf("a sweep that read no fleet must publish no rows: %s", raw)
	}
	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if !inv.Swept {
		t.Error("a sweep completed, and the result has to say so or this reads as a dead process")
	}
	if inv.ChartDetail != nil {
		t.Errorf("with no rows there is nothing for a chart-detail claim to be about, got %+v",
			inv.ChartDetail)
	}
	if !strings.Contains(inv.Status.Text, "not an empty fleet") {
		t.Errorf("the sentence has to deny the reading a client will otherwise take, got %q",
			inv.Status.Text)
	}
}

// An ArgoCD that serves no Application at all is an EMPTY fleet rather than no
// fleet, and the two are distinguishable at the wire.
func TestAReadingThatServedNothingPublishesAnEmptyFleet(t *testing.T) {
	g := blocked()
	g.Fleet = &GateFleet{ObservedAt: observedAt}
	raw := newFixture(t, nil).withGate(g).call(t, "inventory")

	if _, ok := fields(t, raw)["applications"]; !ok {
		t.Fatal("a reading that was made and served nothing must publish an empty list: " +
			"absence is what says nothing has read, and something did")
	}
	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Applications == nil || len(*inv.Applications) != 0 {
		t.Fatalf("want an empty fleet, got %+v", inv.Applications)
	}
}

// Rows with no chart detail say the expansion has not run, rather than leaving
// a reader to read unpinned charts out of a missing key.
func TestRowsWithoutChartDetailSayTheExpansionHasNotRun(t *testing.T) {
	inv := newFixture(t, nil).withGate(fleet()).inventory(t)

	if inv.ChartDetail == nil {
		t.Fatal("rows without chart detail must say why, or a reader cannot tell an " +
			"expansion that has not run from a fleet of unpinned charts")
	}
	if inv.ChartDetail.Expanded {
		t.Error("no row carries a chart, and the flag has to agree with the rows")
	}
	if inv.ChartDetail.Status.Origin != OriginBosun {
		t.Errorf("the explanation is bosun's own sentence, got %+v", inv.ChartDetail.Status)
	}
	if !strings.Contains(inv.ChartDetail.Status.Text, "not unpinned") {
		t.Errorf("the sentence has to deny the reading a client will otherwise take, got %q",
			inv.ChartDetail.Status.Text)
	}
}

// A row says where its names were read, and the answer is not a guess.
//
// An Application name the live reading served reached an apiserver, so it is
// an RFC1123 name; one the gate's expansion produced came out of `helm
// template`, which applies nothing, so it is whatever the template wrote. The
// two are indistinguishable in the bytes, so the origin has to follow the
// row's provenance rather than the mapping site that happened to build it.
func TestAFleetRowSaysWhereItsNamesWereRead(t *testing.T) {
	inv := newFixture(t, nil).withGate(fleet()).inventory(t)
	row := (*inv.Applications)[0]

	if row.ObservedIn != ObservedLive {
		t.Fatalf("this row came from the live reading and has to say so, got %q", row.ObservedIn)
	}
	for name, text := range map[string]*Text{
		"name":      &row.Name,
		"namespace": row.Namespace,
		"cluster":   row.Cluster,
	} {
		if text == nil {
			t.Fatalf("the fixture published no %s, so this assertion read nothing", name)
		}
		if text.Origin != OriginCluster {
			t.Errorf("%s came off an apiserver and is tagged %q", name, text.Origin)
		}
	}

	// And the other direction, asserted on the mapping rather than through the
	// handler, which is a deliberate exception to this package's seam rule.
	// Nothing published by this build carries a row from anywhere but the live
	// reading, so there is no response body the claim can be read off; and an
	// untested default is exactly how "these two are not distinguished by
	// luck" becomes untrue the day a second reading arrives.
	if got := originOf("expansion"); got != OriginChart {
		t.Errorf("a name that did not reach an apiserver must carry the chart origin, got %q", got)
	}
}

// A row is stamped with when it was observed, and how long ago.
//
// Two clocks and not one: the reading is made by a gate RUN, so on an install
// with nothing open it is older than the sweep, and a caller that read the
// sweep's age as the fleet's would be trusting a number about something else.
func TestEachFleetRowCarriesItsOwnObservationTime(t *testing.T) {
	inv := newFixture(t, nil).withGate(fleet()).inventory(t)

	if inv.AgeSeconds == nil || *inv.AgeSeconds != 90 {
		t.Fatalf("the result carries the sweep's age, got %v", inv.AgeSeconds)
	}
	for _, row := range *inv.Applications {
		if !row.ObservedAt.Equal(observedAt) {
			t.Errorf("%s was observed at %s, want %s", row.Name.Text, row.ObservedAt, observedAt)
		}
		if row.ObservedAgeSeconds != 390 {
			t.Errorf("%s was read %d seconds ago; the reading is five minutes older than the "+
				"sweep and a row that reported the sweep's age would hide that",
				row.Name.Text, row.ObservedAgeSeconds)
		}
	}
}

// Names and clusters only: the result type cannot express a manifest.
//
// inventory is the tool most able to become a manifest proxy by accretion --
// a chart name, then the values that configure it, then the objects it
// renders, each one a small step from the last. So the line is drawn on the
// TYPE rather than on what a handler happens to fill in: a manifest, a values
// file and a rendered object are all documents, and a type with no map, no
// interface and no raw bytes anywhere in it has nowhere to put one.
//
// Derived from the registered tool rather than from Fleet by name, so the
// check follows the tool if the result type is ever replaced.
//
// What it does NOT prove, stated because a guard read as wider than it is is
// worse than none: a Text field could still be filled with a rendered
// manifest, because a document serialised into a string is a string. This
// bans the SHAPES a document travels in unflattened, which is what accretion
// actually looks like -- a chart name, then the values under it, then the
// objects those render. A field that deliberately stuffed YAML into a name is
// a different failure, and the reviewer reading the diff is what catches it.
func TestTheInventoryResultTypeCannotCarryADocument(t *testing.T) {
	var result any
	for _, tool := range Tools() {
		if tool.Name == "inventory" {
			result = tool.Result
		}
	}
	if result == nil {
		t.Fatal("no tool named inventory is registered, so this walk has nothing to read")
	}

	// The leaves a row may be built from. Everything here is a scalar or a
	// tagged string; none of them can hold a document.
	allowed := map[reflect.Type]bool{
		reflect.TypeOf(Text{}):      true,
		reflect.TypeOf(time.Time{}): true,
	}

	fields := 0
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		if allowed[typ] {
			return
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice:
			walk(typ.Elem(), path+"[]")
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int64:
			// A name, a flag, a count. Nothing document-shaped.
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				fields++
				walk(f.Type, path+"."+f.Name)
			}
		default:
			t.Errorf("%s is a %s.\n"+
				"This result carries names, clusters and versions and nothing else. A map, an "+
				"interface or a byte slice is somewhere a manifest, a values file or a rendered "+
				"object can be put, and the whole point of this type's shape is that there is "+
				"nowhere.", path, typ.Kind())
		}
	}
	walk(reflect.TypeOf(result), "inventory")

	// The self-check, and not optional: a walk that descended into nothing
	// would report a clean pass over a type it never read.
	if fields < 10 {
		t.Fatalf("this walk saw only %d fields; the result is shaped differently now and it "+
			"is no longer reading it. Fix the walk rather than lowering this number.", fields)
	}
}

// A reading held while the first sweep is still running publishes its rows.
//
// The sweep stamps itself only once every pull request it started has been
// answered, and a run makes its reading in the middle of that -- so this is
// not a corner, it is every install's first minute, and the triage path can
// reach a run before any sweep finishes at all. Gating the rows on the sweep
// had this answer "nothing has read what this fleet runs" while holding the
// reading, which is the one claim this tool exists to make impossible.
func TestAReadingHeldBeforeTheFirstSweepIsStillPublished(t *testing.T) {
	f := newFixture(t, nil).withGate(GateStatus{
		Fleet: &GateFleet{ObservedAt: observedAt, Apps: []GateFleetApp{
			{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"}}},
	})
	raw := f.call(t, "inventory")

	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Applications == nil || len(*inv.Applications) != 1 {
		t.Fatalf("a reading this process holds must be published whatever the sweep has "+
			"done: %s", raw)
	}
	// And the sweep is still reported honestly, because the two are separate
	// claims: no sweep has finished, and a reading exists.
	if inv.Swept {
		t.Error("no sweep has completed, so nothing may claim one has")
	}
	if _, ok := fields(t, raw)["sweptAt"]; ok {
		t.Errorf("a sweep time is published for a sweep that has not finished: %s", raw)
	}
	if inv.Status.Text != fleetRead {
		t.Errorf("the sentence must describe the reading rather than the sweep, got %q",
			inv.Status.Text)
	}
}
