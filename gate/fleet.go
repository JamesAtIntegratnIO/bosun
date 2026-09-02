package gate

import "strings"

// The fleet: every Application a live ArgoCD serves, and where each one lands.
//
// The gate reads this on every run to decide what to render, and threw it away
// when the run ended. It is retained here because it is the answer to "where
// does this run", which anybody without it goes to a cluster to get -- with a
// credential of their own, for a fact this process already had.
//
// Names and destinations. Nothing here is content: what an Application
// deploys is the render's business, and a fleet listing that grew a manifest
// would be a second, unreviewed way to read the cluster.

// FleetApp is one Application a live ArgoCD serves.
type FleetApp struct {
	// Name and Namespace identify the Application object itself, not what it
	// deploys.
	Name      string
	Namespace string
	// Destination is `spec.destination` as ArgoCD serves it, unresolved.
	//
	// Kept as the author wrote it rather than resolved on the way in, because
	// resolving needs the cluster inventory and the reader that has one is not
	// the reader that made this. ClusterFor is where the two meet.
	Destination Destination
}

// Destination is an Application's `spec.destination`: a cluster by name, or by
// the address of its apiserver.
//
// Both, because ArgoCD accepts either and stores what it was given, and a
// fleet in the wild has both spellings in it. Neither is the cluster's
// identity on its own -- only the inventory joins them.
//
// Tagged, and decoded into directly by the reader in `cluster` rather than
// copied field by field out of a second struct of the same shape, which is
// what it was. Two two-field structs and a hand copy between them is a third
// field waiting to be added to one of them and forgotten in the copy, and the
// symptom of that is a destination silently arriving empty. `gate.Cluster` and
// `gate.Inventory` carry wire tags for the same reason.
//
// `namespace` is deliberately not read: it says where inside a cluster the
// objects land, which is a property of what an Application deploys rather than
// of where it runs, and this exists to answer the second question.
type Destination struct {
	Name   string `json:"name"`
	Server string `json:"server"`
}

// ClusterFor names the cluster a destination lands on, and "" when this
// inventory knows of none.
//
// Empty rather than the destination's own text, which is the decision. An
// unresolved destination means the Applications read and the clusters read
// disagree -- a cluster deregistered between them, or one this install's
// credentials may list Applications for and not clusters -- and publishing the
// raw address as though it were a cluster name would hand a reader something
// to act on that nothing has checked. A missing cluster is asked about; a
// wrong one is used.
//
// The name wins over the server. ArgoCD refuses an Application that sets both,
// so a destination carrying two is already malformed, and the name is the half
// an operator wrote and a report would quote.
func (inv *Inventory) ClusterFor(d Destination) string {
	if inv == nil {
		return ""
	}
	if d.Name != "" {
		for _, c := range inv.Clusters {
			if c.Name == d.Name {
				return c.Name
			}
		}
		return ""
	}
	server := trimServer(d.Server)
	if server == "" {
		return ""
	}
	for _, c := range inv.Clusters {
		if trimServer(c.Server) == server {
			return c.Name
		}
	}
	return ""
}

// trimServer is one apiserver address, spelt one way.
//
// Only the trailing slash, and deliberately nothing else. `https://host` and
// `https://host:443` are the same endpoint and this does not merge them, for
// the reason normaliseRepoURL keeps an explicit port: merging is wrong in the
// direction that puts an Application on a cluster it does not run on, and a
// self-hosted apiserver reachable on two addresses is rarer than two
// apiservers.
func trimServer(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }
