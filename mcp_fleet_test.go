package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
)

// The crossing from the gate's fleet reading into the tool surface's
// vocabulary, which is a contract nothing else can see.
//
// `mcp` may not import `gateservice`, so the two shapes are copies and the
// copying happens in this package. A field this file forgets is a field simply
// missing from every answer, with no compiler and no test in either package to
// notice -- and for a listing, the field most likely to be forgotten is the
// one that says which cluster, which is the whole question.

func TestTheFleetCrossesIntoTheToolSurfaceWhole(t *testing.T) {
	read := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	got := mcpGateStatus(gateservice.Status{
		SweptAt: read.Add(time.Hour),
		Fleet: &gateservice.Fleet{
			ObservedAt: read,
			Apps: []gateservice.FleetApp{
				{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"},
				{Name: "orphan", Namespace: "tenant-a"},
			},
		},
	})

	if got.Fleet == nil {
		t.Fatal("a reading crossed as nothing, which every tool reads as an install that has " +
			"never looked at its own fleet")
	}
	if !got.Fleet.ObservedAt.Equal(read) {
		t.Errorf("the reading crossed stamped %s, want %s", got.Fleet.ObservedAt, read)
	}
	want := []mcp.GateFleetApp{
		{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"},
		{Name: "orphan", Namespace: "tenant-a"},
	}
	if !reflect.DeepEqual(got.Fleet.Apps, want) {
		t.Errorf("the rows crossed as\n %+v\nwant %+v", got.Fleet.Apps, want)
	}

	// Field for field, off the source type rather than off the list above: two
	// strings of the same type in one struct is the shape a transposition
	// hides in, and a namespace published as a cluster name compiles, reads
	// correctly, and tells a platform agent to look on a cluster that does not
	// exist.
	from := reflect.TypeOf(gateservice.FleetApp{})
	to := reflect.TypeOf(mcp.GateFleetApp{})
	if from.NumField() != to.NumField() {
		t.Fatalf("the two rows have %d and %d fields; one of them grew a field the other "+
			"does not have", from.NumField(), to.NumField())
	}
	for i := 0; i < from.NumField(); i++ {
		name := from.Field(i).Name
		if _, ok := to.FieldByName(name); !ok {
			t.Errorf("gateservice.FleetApp.%s has no counterpart on the tool surface, so it "+
				"cannot cross at all", name)
			continue
		}
		a := reflect.ValueOf(gateservice.FleetApp{}).FieldByName(name)
		b := reflect.ValueOf(mcp.GateFleetApp{}).FieldByName(name)
		if a.Kind() != b.Kind() {
			t.Errorf("%s crosses from a %s into a %s", name, a.Kind(), b.Kind())
		}
	}
}

// No reading crosses as no reading, rather than as an empty one.
//
// The distinction the whole tool rests on, at the one place a helpful nil check
// could erase it: an empty fleet would publish zero rows, which is exactly what
// an install whose ArgoCD serves nothing looks like.
func TestNoFleetReadingCrossesAsNoReading(t *testing.T) {
	got := mcpGateStatus(gateservice.Status{SweptAt: time.Now()})
	if got.Fleet != nil {
		t.Fatalf("nothing crossed as %+v, which a client reads as a fleet somebody looked at",
			got.Fleet)
	}
}
