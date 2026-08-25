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
// ground the gate stands on. An empty or unreadable inventory does not make
// the verdict poorer, it makes it WRONG -- a render against no clusters finds
// no targeting and waves everything through -- so the honest behaviour is to
// refuse, and the caller reports the gate itself as broken.
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
		return nil, fmt.Errorf("no ArgoCD cluster Secrets in namespace %q -- the gate cannot expand a generator against an empty inventory", ns)
	}
	return inv, nil
}
