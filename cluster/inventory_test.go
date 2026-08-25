package cluster

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The inventory is the ground the gate stands on, so these tests are about
// two things: the live read decodes a Secret exactly the way the snapshot
// export does, and a read that could not answer REFUSES rather than handing
// back an empty world the gate would render nothing against.

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func secretList(items ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"items": items})
	return string(b)
}

func TestClusterInventoryDecodesSecretsTheWayTheExportDoes(t *testing.T) {
	var gotPath, gotSelector string
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotSelector = r.URL.Query().Get("labelSelector")
		fmt.Fprint(w, secretList(
			map[string]any{
				"metadata": map[string]any{
					"name":   "cluster-hub",
					"labels": map[string]string{"argocd.argoproj.io/secret-type": "cluster", "environment": "production"},
					"annotations": map[string]string{
						"addons_repo_path": "charts/application-sets",
						// Snapshot noise. A live read keeps it: nothing is
						// ever diffed against a live inventory, so churn
						// cannot cause drift and an extra key costs nothing.
						"kubectl.kubernetes.io/last-applied-configuration": "{}",
					},
				},
				"data": map[string]string{"name": b64("hub"), "server": b64("https://kubernetes.default.svc")},
			},
			map[string]any{
				// No name in data: the Secret's own name is the fallback,
				// same as the export.
				"metadata": map[string]any{"name": "tenant", "labels": map[string]string{"argocd.argoproj.io/secret-type": "cluster"}},
				"data":     map[string]string{"server": b64("https://media.example:6443")},
			},
		))
	}))
	a.ArgoCDNamespace = "argocd"

	inv, err := a.ClusterInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/namespaces/argocd/secrets" {
		t.Fatalf("read %s, want the secrets collection in the ArgoCD namespace", gotPath)
	}
	if gotSelector != "argocd.argoproj.io/secret-type=cluster" {
		t.Fatalf("selected %q -- an unselected list would sweep up every Secret in the namespace", gotSelector)
	}
	if len(inv.Clusters) != 2 {
		t.Fatalf("got %d clusters, want 2", len(inv.Clusters))
	}
	c := inv.Clusters[0]
	if c.Name != "hub" || c.Server != "https://kubernetes.default.svc" {
		t.Fatalf("first cluster decoded as %q at %q", c.Name, c.Server)
	}
	if c.Labels["environment"] != "production" {
		t.Fatal("labels must survive untrimmed -- they are selector inputs")
	}
	if c.Annotations["addons_repo_path"] != "charts/application-sets" {
		t.Fatal("annotations the bootstraps template with must survive")
	}
	if inv.Clusters[1].Name != "tenant" {
		t.Fatalf("a Secret without a name in data must fall back to its own name, got %q", inv.Clusters[1].Name)
	}
	if inv.GeneratedAt != "" {
		t.Fatal("a live inventory is not a snapshot and must not claim an export time")
	}
}

func TestClusterInventoryRefusesAnEmptyWorld(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, secretList())
	}))
	if _, err := a.ClusterInventory(context.Background()); err == nil {
		t.Fatal("an empty inventory must be refused -- a render against no clusters finds no targeting and waves everything through")
	}
}

func TestClusterInventoryReportsADeniedRead(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	_, err := a.ClusterInventory(context.Background())
	if err == nil {
		t.Fatal("a denied Secret read must surface as an error, not an empty inventory")
	}
	if !strings.Contains(err.Error(), "cluster Secrets") {
		t.Fatalf("the error should say what could not be read: %v", err)
	}
}
