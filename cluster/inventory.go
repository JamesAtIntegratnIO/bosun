package cluster

import (
	"context"
	"fmt"
	"net/url"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// ClusterInventory reads the ArgoCD cluster Secrets and builds the inventory
// the gate expands generators against -- the live edition of the file
// `gitops-gate clusters export` snapshots for CI. Same Secrets, same decode
// (gate.InventoryFromSecrets), no snapshot to go stale.
//
// This method RETURNS AN ERROR, and that breaks this package's "never an
// error for could-not-look" rule on purpose. That rule exists so a lost
// cluster fact can never leave a pull request unattended -- a brief is merely
// poorer without one. The inventory is not a fact in a brief; it is the
// ground the gate stands on. An unreadable inventory does not make the
// verdict poorer, it makes it WRONG -- a render against a world the gate
// could not see finds no targeting and waves everything through -- so the
// honest behaviour is to refuse, and the caller reports the gate itself as
// broken. (Zero Secrets is not that case: it is what a single-cluster ArgoCD
// looks like, and the answer is the implicit local cluster, below.)
//
// No ExportFilter: filtering exists to stabilise a snapshot against churn,
// and a live read is never diffed against anything.
func (a *APIServer) ClusterInventory(ctx context.Context) (*gate.Inventory, error) {
	ns := a.ArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	var out struct {
		Items []gate.ClusterSecret `json:"items"`
	}
	path := fmt.Sprintf("/api/v1/namespaces/%s/secrets?labelSelector=%s",
		url.PathEscape(ns), url.QueryEscape("argocd.argoproj.io/secret-type=cluster"))
	if err := a.get(ctx, path, &out); err != nil {
		return nil, fmt.Errorf("reading ArgoCD cluster Secrets in %s: %w", ns, err)
	}
	inv := gate.InventoryFromSecrets(out.Items, gate.ExportFilter{})
	if len(inv.Clusters) == 0 {
		// A single-cluster ArgoCD managing only itself registers no Secret at
		// all -- the local cluster is implicit. ArgoCD's own clusters
		// generator still includes it, as `in-cluster` with no labels, so an
		// inventory that mirrors ArgoCD says the same thing. No labels is
		// faithful, not lazy: a selector that matches on a label this entry
		// lacks excludes it in ArgoCD too, and the inventory validator will
		// say so out loud.
		return &gate.Inventory{Clusters: []gate.Cluster{{
			Name:        "in-cluster",
			Server:      "https://kubernetes.default.svc",
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		}}}, nil
	}
	return inv, nil
}
