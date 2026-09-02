package gate

import "testing"

// An Application's destination is a cluster name or a server address, and
// ArgoCD stores whichever the author wrote. The inventory is the only thing
// that knows they are the same cluster.

func TestADestinationResolvesOntoTheClusterInventory(t *testing.T) {
	inv := &Inventory{Clusters: []Cluster{
		{Name: "in-cluster", Server: "https://kubernetes.default.svc"},
		{Name: "prod-eu", Server: "https://eu.example:6443"},
	}}

	for _, tc := range []struct {
		name string
		dest Destination
		want string
	}{
		{"by name", Destination{Name: "prod-eu"}, "prod-eu"},
		{"by server", Destination{Server: "https://eu.example:6443"}, "prod-eu"},
		{"the implicit local cluster", Destination{Server: "https://kubernetes.default.svc"}, "in-cluster"},
		{"a trailing slash is the same address", Destination{Server: "https://eu.example:6443/"}, "prod-eu"},
		{"a server no cluster serves", Destination{Server: "https://gone.example:6443"}, ""},
		{"a name no cluster carries", Destination{Name: "prod-ap"}, ""},
		{"nothing at all", Destination{}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := inv.ClusterFor(tc.dest); got != tc.want {
				t.Errorf("%+v resolved to %q, want %q", tc.dest, got, tc.want)
			}
		})
	}
}

// A name and a server that disagree resolve by name.
//
// ArgoCD refuses an Application that sets both, so this is a shape no healthy
// fleet produces -- which is exactly why the answer must not depend on field
// order. The name is what an operator wrote and what the report would quote.
func TestADestinationWithBothResolvesByName(t *testing.T) {
	inv := &Inventory{Clusters: []Cluster{
		{Name: "prod-eu", Server: "https://eu.example:6443"},
		{Name: "prod-us", Server: "https://us.example:6443"},
	}}
	got := inv.ClusterFor(Destination{Name: "prod-us", Server: "https://eu.example:6443"})
	if got != "prod-us" {
		t.Errorf("want the name the destination gave, got %q", got)
	}
}
