package cluster

import (
	"reflect"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// The live reading is what ArgoCD says it runs, and it is kept whole.
//
// The render plan is a filtered view of it -- only the Applications pointing
// at the gated repository become sources -- and the fleet is not. An install's
// horizon is set by what its credentials can read, never by what its readout
// filters (ADR 0014), so the Application of somebody else's repository is a
// row rather than an omission.

func TestTheFleetIsEveryApplicationTheReadingServed(t *testing.T) {
	d := deriveFixture(t, "homelab", gatedRepo)

	want := []gate.FleetApp{
		{Name: "cert-manager-hub", Namespace: "argocd",
			Destination: gate.Destination{Server: "https://hub.example:6443"}},
		{Name: "hydrated-platform", Namespace: "argocd",
			Destination: gate.Destination{Server: "https://hub.example:6443"}},
		{Name: "ingress", Namespace: "argocd",
			Destination: gate.Destination{Server: "https://spoke.example:6443"}},
		{Name: "media", Namespace: "argocd",
			Destination: gate.Destination{Server: "https://hub.example:6443"}},
		{Name: "monitoring", Namespace: "argocd",
			Destination: gate.Destination{Server: "https://hub.example:6443"}},
		// The Application of a repository this install does not gate. It
		// renders nothing here and it still runs on this control plane, which
		// is the question the fleet answers.
		{Name: "vendor-thing", Namespace: "argocd",
			Destination: gate.Destination{Server: "https://hub.example:6443"}},
	}
	if !reflect.DeepEqual(d.Fleet, want) {
		t.Errorf("the fleet is not what the reading served:\n got %+v\nwant %+v", d.Fleet, want)
	}
	if len(d.Fleet) != d.Applications {
		t.Errorf("the reading counted %d Applications and published %d rows; the count and the "+
			"rows are two readings of one list and must not disagree", d.Applications, len(d.Fleet))
	}
}

// A destination given by name arrives as a name, not as an address.
//
// ArgoCD accepts either and stores what it was given. A reader that decoded
// only one of them would drop every Application on a fleet that spells its
// destinations the other way, and the symptom is rows with no cluster on them.
func TestADestinationGivenByNameSurvivesTheRead(t *testing.T) {
	apps := []Application{{
		Name: "podinfo", Namespace: "argocd",
		Destination: gate.Destination{Name: "prod-eu"},
	}}
	d := DeriveFrom(apps, nil, gatedRepo)

	if len(d.Fleet) != 1 {
		t.Fatalf("want one row, got %+v", d.Fleet)
	}
	if got := d.Fleet[0].Destination; got != (gate.Destination{Name: "prod-eu"}) {
		t.Errorf("the destination crossed as %+v", got)
	}
}
