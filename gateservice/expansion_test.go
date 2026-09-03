package gateservice

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// The render expansion the gate computes on every run used to end with the
// run, like the live reading before it. It is retained now because it is the
// only thing in this process that knows which chart an Application renders
// from, and the reader who wants that is the one the live reading already
// answers half of.

// twoRevisions is a repository whose Application renders one chart version at
// the revision a run starts from and another at the revision under judgement.
//
// The two differ on purpose. The expansion this retains is the BASE one -- the
// revision the run started from, which is what the fleet is running -- and a
// fixture where both sides pinned the same version would pass whichever one
// the retention picked.
func twoRevisions() (base, head map[string]string) {
	return map[string]string{
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")},
		map[string]string{
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.8.0")}
}

func TestARunRetainsTheExpansionItRendered(t *testing.T) {
	base, head := twoRevisions()
	h := newGateHarness(t, base, head)
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return oneReading(), nil }

	before := time.Now()
	if out := h.gs.Ensure(context.Background(), gatePR("expand01")); out.Err != nil {
		t.Fatal(out.Err)
	}

	got := h.gs.Status().Expansion
	if got == nil {
		t.Fatal("the run expanded what this repository deploys and the snapshot kept none of " +
			"it, so no row can ever say what it renders from")
	}
	if got.ObservedAt.Before(before) {
		t.Errorf("the expansion is stamped %s, before the run that made it started at %s",
			got.ObservedAt, before)
	}
	want := []ExpansionApp{{
		Name: "podinfo", Cluster: "local", SourceType: gate.RowHelm,
		Chart: "podinfo", ChartRepo: "https://stefanprodan.github.io/podinfo", Version: "6.7.0",
	}}
	if !reflect.DeepEqual(got.Apps, want) {
		t.Errorf("the expansion crossed as\n %+v\nwant %+v", got.Apps, want)
	}
}

// The revision retained is the one the run started from, and not the one under
// judgement.
//
// The fleet is running the base revision; the head is the change being asked
// about, which by definition nothing has deployed. Retaining the head would
// publish a pinned version no cluster is running as the version the fleet
// runs, on the one tool a platform agent asks that question of.
func TestTheExpansionIsTheRevisionTheRunStartedFrom(t *testing.T) {
	base, head := twoRevisions()
	h := newGateHarness(t, base, head)
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return oneReading(), nil }

	if out := h.gs.Ensure(context.Background(), gatePR("expand02")); out.Err != nil {
		t.Fatal(out.Err)
	}

	got := h.gs.Status().Expansion
	if got == nil || len(got.Apps) != 1 {
		t.Fatalf("want the one expanded Application, got %+v", got)
	}
	if got.Apps[0].Version != "6.7.0" {
		t.Errorf("the expansion pinned %q, which is the revision under judgement rather than "+
			"the one the run started from; nothing has deployed the head of a pull request",
			got.Apps[0].Version)
	}
}

// Before any run there is no expansion, and a run that could not render leaves
// the last one standing rather than emptying it.
func TestNoRunMeansNoExpansionAndAFailedRenderKeepsTheLastOne(t *testing.T) {
	base, head := twoRevisions()
	h := newGateHarness(t, base, head)
	if h.gs.Status().Expansion != nil {
		t.Fatal("nothing has rendered this repository, and a snapshot that claimed an " +
			"expansion would be an expansion nothing computed")
	}

	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return oneReading(), nil }
	if out := h.gs.Ensure(context.Background(), gatePR("expand03")); out.Err != nil {
		t.Fatal(out.Err)
	}
	first := h.gs.Status().Expansion
	if first == nil {
		t.Fatal("the first run expanded nothing")
	}

	// A derivation that fails breaks the run before the render, which is the
	// cheapest way to reach this service's error path without a helm on PATH.
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) {
		return nil, fmt.Errorf("argocd said 403")
	}
	if out := h.gs.Ensure(context.Background(), gatePR("expand04")); out.Err == nil {
		t.Fatal("a run that could not read must break rather than rendering a smaller scope")
	}
	after := h.gs.Status().Expansion
	if after == nil || len(after.Apps) != len(first.Apps) {
		t.Fatalf("a run that broke dropped the last expansion: %+v", after)
	}
	if !after.ObservedAt.Equal(first.ObservedAt) {
		t.Errorf("a run that broke restamped the last expansion as fresh: %s, was %s",
			after.ObservedAt, first.ObservedAt)
	}
}

// The snapshot hands out a copy, so a caller cannot edit what the next one
// reads.
func TestTheExpansionSnapshotIsACopy(t *testing.T) {
	base, head := twoRevisions()
	h := newGateHarness(t, base, head)
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return oneReading(), nil }
	if out := h.gs.Ensure(context.Background(), gatePR("expand05")); out.Err != nil {
		t.Fatal(out.Err)
	}

	h.gs.Status().Expansion.Apps[0].Chart = "rewritten"
	if got := h.gs.Status().Expansion.Apps[0].Chart; got != "podinfo" {
		t.Errorf("one reader edited what the next one sees: %q", got)
	}
}

// Nothing crossing from the rendered row into the snapshot is silently
// dropped, and the ones that are dropped are dropped on purpose.
//
// Derived off the source struct for the reason CONTRIBUTING gives, and with a
// second job here: `gate.Row` carries what an Application renders its chart
// WITH -- the value files it layers and the inline values block -- and none of
// that may cross into a surface published outside the cluster. Naming them
// here with the reason is what makes an unexplained third omission, or a
// values field quietly added back, fail.
func TestNoFieldOfTheRenderedRowIsDroppedWithoutAReason(t *testing.T) {
	// Refused: values are content. `inventory` is the tool most able to
	// become a manifest proxy by accretion, and a values file is the first
	// step of it.
	refused := map[string]bool{"ValueFiles": true, "ValuesInline": true}
	// Not carried: neither says what an Application renders from. Namespace
	// here is `spec.destination.namespace`, which is where what it renders
	// LANDS, and the snapshot already carries the Application object's own
	// namespace from the live reading -- two different namespaces under one
	// word is the transposition this omission avoids.
	notCarried := map[string]bool{"Project": true, "Namespace": true, "Path": true}
	// Consumed: it decides whether AppSet crosses at all, rather than
	// crossing itself. A row whose AppSet is the config source it was read
	// from carries no ApplicationSet, and publishing one would name an object
	// nothing serves.
	consumed := map[string]bool{"FromAppSet": true}
	// Renamed: the row calls an Application's name App, and everything else
	// in this package calls it Name.
	renamed := map[string]string{"App": "Name"}

	from := reflect.TypeOf(gate.Row{})
	to := reflect.TypeOf(ExpansionApp{})
	checked := 0
	for i := 0; i < from.NumField(); i++ {
		name := from.Field(i).Name
		if refused[name] || notCarried[name] || consumed[name] {
			continue
		}
		if to, ok := renamed[name]; ok {
			name = to
		}
		checked++
		if _, ok := to.FieldByName(name); !ok {
			t.Errorf("gate.Row.%s has no counterpart on the snapshot and is not one of the "+
				"fields this retention refuses or leaves behind, so nothing carries it", name)
		}
	}

	// And the other direction: a field on the snapshot that comes from nowhere
	// is a zero on every answer, forever.
	for i := 0; i < to.NumField(); i++ {
		name := to.Field(i).Name
		if name == "Name" {
			name = "App"
		}
		checked++
		if _, ok := from.FieldByName(name); !ok {
			t.Errorf("ExpansionApp.%s comes from no field of a rendered row, so nothing "+
				"fills it", name)
		}
	}

	// The self-check on the refusal, and not optional: a values field renamed
	// on `gate.Row` would leave the map above naming nothing, and this walk
	// would report that no values cross while carrying them under their new
	// name.
	for name := range refused {
		if _, ok := from.FieldByName(name); !ok {
			t.Errorf("gate.Row has no field %s, so refusing it proves nothing. Find what "+
				"carries the values now and refuse that.", name)
		}
	}
	if checked < 10 {
		t.Fatalf("compared only %d fields across the two rows; they are shaped differently "+
			"now and these walks no longer read them. Fix the walks rather than lowering "+
			"this number.", checked)
	}
}
