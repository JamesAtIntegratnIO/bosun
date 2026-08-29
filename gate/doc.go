// Package gate answers one question about a change to a GitOps repository:
// does it change what gets deployed, and is what it produces still valid?
//
// It renders every ArgoCD Application and ApplicationSet a repository defines,
// expands the generators against a cluster inventory, and diffs two renders.
// The agent imports this package and runs the gate in-cluster, with the
// inventory read live from the ArgoCD API (the cluster package). No model is
// involved at this layer; the AI half lives in the agent that calls it.
package gate
