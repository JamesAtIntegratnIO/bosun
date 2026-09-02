package gateservice

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// The live reading the gate makes on every run used to end with the run. It is
// retained now, because "which cluster does this Application land on" is a
// question this process answers all day and then throws away, and the only
// other way to ask it is a cluster credential.

// oneReading is what a live ArgoCD serves: two Applications, one addressed by
// server and one by name, so both spellings of a destination go through the
// resolution rather than only the one this fixture happened to pick.
func oneReading() *gate.Derivation {
	return &gate.Derivation{
		Applications: 2, ApplicationSets: 0,
		Sources: []gate.Source{{
			Name: "app/apps", Type: gate.SourceManifests, Paths: []string{"apps/*.yaml"},
		}},
		Fleet: []gate.FleetApp{
			{Name: "podinfo", Namespace: "argocd",
				Destination: gate.Destination{Server: "https://kubernetes.default.svc"}},
			{Name: "telemetry", Namespace: "argocd",
				Destination: gate.Destination{Name: "edge"}},
		},
	}
}

func TestASweepRetainsTheFleetItRead(t *testing.T) {
	files := map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return oneReading(), nil }

	before := time.Now()
	if out := h.gs.Ensure(context.Background(), gatePR("fleet01")); out.Err != nil {
		t.Fatal(out.Err)
	}

	got := h.gs.Status().Fleet
	if got == nil {
		t.Fatal("the run read what ArgoCD serves and the snapshot kept none of it, which is " +
			"the discard this retention exists to end")
	}
	if got.ObservedAt.Before(before) {
		t.Errorf("the reading is stamped %s, before the run that made it started at %s",
			got.ObservedAt, before)
	}
	want := []FleetApp{
		{Name: "podinfo", Namespace: "argocd", Cluster: "local"},
		{Name: "telemetry", Namespace: "argocd", Cluster: "edge"},
	}
	if len(got.Apps) != len(want) {
		t.Fatalf("the reading served %d Applications and the snapshot kept %d: %+v",
			len(want), len(got.Apps), got.Apps)
	}
	for i := range want {
		if got.Apps[i] != want[i] {
			t.Errorf("row %d is %+v, want %+v", i, got.Apps[i], want[i])
		}
	}
}

// A destination the cluster inventory does not know leaves the row without a
// cluster, rather than with the address it could not resolve.
//
// The two ArgoCD reads can disagree: a cluster deregistered between them, or
// an account permitted to list Applications and not clusters. Publishing the
// raw address as a cluster name would hand a reader a value to act on that
// nothing has checked.
func TestAnUnresolvableDestinationLeavesTheClusterAbsent(t *testing.T) {
	files := map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	d := oneReading()
	d.Fleet[1].Destination = gate.Destination{Server: "https://deregistered.example:6443"}
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return d, nil }

	if out := h.gs.Ensure(context.Background(), gatePR("fleet02")); out.Err != nil {
		t.Fatal(out.Err)
	}

	got := h.gs.Status().Fleet
	if got == nil || len(got.Apps) != 2 {
		t.Fatalf("want both rows kept, got %+v", got)
	}
	if got.Apps[1].Cluster != "" {
		t.Errorf("an unresolved destination published the cluster %q; a wrong cluster name is "+
			"acted on and a missing one is asked about", got.Apps[1].Cluster)
	}
}

// Before any run there is no reading, and a run that could not read leaves the
// last one standing rather than emptying it.
func TestNoRunMeansNoFleetAndAFailedReadKeepsTheLastOne(t *testing.T) {
	files := map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	if h.gs.Status().Fleet != nil {
		t.Fatal("nothing has read the fleet, and a snapshot that claimed one would be an " +
			"empty fleet nothing looked at")
	}

	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return oneReading(), nil }
	if out := h.gs.Ensure(context.Background(), gatePR("fleet03")); out.Err != nil {
		t.Fatal(out.Err)
	}
	first := h.gs.Status().Fleet
	if first == nil {
		t.Fatal("the first run read nothing")
	}

	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) {
		return nil, fmt.Errorf("argocd said 403")
	}
	if out := h.gs.Ensure(context.Background(), gatePR("fleet04")); out.Err == nil {
		t.Fatal("a derivation that fails must break the run rather than shrinking its scope")
	}
	after := h.gs.Status().Fleet
	if after == nil || len(after.Apps) != len(first.Apps) {
		t.Fatalf("a read that failed dropped the last reading: %+v", after)
	}
	if !after.ObservedAt.Equal(first.ObservedAt) {
		t.Errorf("a read that failed restamped the last reading as fresh: %s, was %s",
			after.ObservedAt, first.ObservedAt)
	}
}

// The snapshot hands out a copy, so a caller cannot edit what the next one
// reads.
func TestTheFleetSnapshotIsACopy(t *testing.T) {
	files := map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	h.gs.Derive = func(context.Context, string) (*gate.Derivation, error) { return oneReading(), nil }
	if out := h.gs.Ensure(context.Background(), gatePR("fleet05")); out.Err != nil {
		t.Fatal(out.Err)
	}

	h.gs.Status().Fleet.Apps[0].Name = "rewritten"
	if got := h.gs.Status().Fleet.Apps[0].Name; got != "podinfo" {
		t.Errorf("one reader edited what the next one sees: %q", got)
	}
}

// Nothing crossing from the live reading into the snapshot is silently
// dropped.
//
// Derived off the source struct rather than compared against the literals
// above, for the reason CONTRIBUTING gives: a field added to `gate.FleetApp`
// that `retainFleet` forgets is simply missing from every answer, and neither
// package has a compiler or a test that can see the other half. The one field
// with no counterpart is named here with the reason it has none, so a second
// unexplained gap fails.
func TestNoFieldOfTheReadingIsDroppedOnTheWayIn(t *testing.T) {
	// Destination is consumed rather than copied: it is a cluster name or an
	// apiserver address, and what the snapshot publishes is the cluster the
	// two resolve to. TestASweepRetainsTheFleetItRead is what checks the
	// resolution itself.
	consumed := map[string]bool{"Destination": true}

	from := reflect.TypeOf(gate.FleetApp{})
	to := reflect.TypeOf(FleetApp{})
	checked := 0
	for i := 0; i < from.NumField(); i++ {
		name := from.Field(i).Name
		if consumed[name] {
			continue
		}
		checked++
		if _, ok := to.FieldByName(name); !ok {
			t.Errorf("gate.FleetApp.%s has no counterpart on the snapshot and is not one of "+
				"the fields the retention consumes, so nothing carries it", name)
		}
	}

	// And the other direction: a field on the snapshot that comes from nowhere
	// is a zero on every answer, forever.
	produced := map[string]bool{"Cluster": true}
	for i := 0; i < to.NumField(); i++ {
		name := to.Field(i).Name
		if produced[name] {
			continue
		}
		checked++
		if _, ok := from.FieldByName(name); !ok {
			t.Errorf("FleetApp.%s comes from no field of the live reading and is not one the "+
				"retention computes, so nothing fills it", name)
		}
	}

	// The self-check, and not optional: two walks over a struct they no longer
	// recognise would compare nothing and report agreement.
	if checked < 4 {
		t.Fatalf("compared only %d fields across the two rows; they are shaped differently "+
			"now and these walks no longer read them. Fix the walks rather than lowering "+
			"this number.", checked)
	}
}
