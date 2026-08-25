// Package gate answers one question about a change to a GitOps repository:
// does it change what actually gets deployed, and is what it produces still
// valid?
//
// It renders every ArgoCD Application and ApplicationSet a repository defines,
// expands the generators against a cluster inventory, and diffs two renders.
// The inventory can come from a checked-in snapshot (the CLI in cmd/gitops-gate,
// for CI and local runs) or from the live ArgoCD cluster Secrets (the agent,
// which runs in-cluster and has no snapshot to go stale).
//
// The CLI and the in-cluster gate call the same functions in here. That is the
// point of the package boundary: two delivery surfaces, one verdict.
package gate
