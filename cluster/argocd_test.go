package cluster

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// The ArgoCD API is where the gate reads the inventory, and the CLI's `clusters
// export` snapshot is decoded from the cluster Secrets instead. Those two must
// describe the same cluster the same way; a selector matches on those maps, so
// a key one side trimmed differently is a different targeting verdict from the
// same cluster. So the centre of gravity here is the equivalence test: the same
// cluster, decoded once from a Secret and once from an ArgoCD cluster, must
// produce byte-identical inventories. Everything else is about refusing rather
// than guessing.

func argoFor(t *testing.T, h http.Handler) *ArgoCD {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &ArgoCD{BaseURL: srv.URL, Token: "argocd-tok", HTTP: srv.Client()}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func clusterList(items ...map[string]any) string {
	b, _ := json.Marshal(map[string]any{"items": items})
	return string(b)
}

func TestArgoCDAndSecretsDecodeTheSameClusterIdentically(t *testing.T) {
	labels := map[string]string{
		"argocd.argoproj.io/secret-type": "cluster",
		"environment":                    "production",
	}
	annotations := map[string]string{"addons_repo_path": "charts/application-sets"}

	// Decoded from JSON rather than built as a literal, because that is the
	// shape the apiserver hands a Secret over in: `data` values base64 on the
	// wire.
	raw, _ := json.Marshal(map[string]any{
		"metadata": map[string]any{"name": "cluster-hub", "labels": labels, "annotations": annotations},
		"data":     map[string]string{"name": b64("hub"), "server": b64("https://media.example:6443")},
	})
	var secret gate.ClusterSecret
	if err := json.Unmarshal(raw, &secret); err != nil {
		t.Fatal(err)
	}
	fromSecrets := gate.InventoryFromSecrets([]gate.ClusterSecret{secret})

	argo := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, clusterList(map[string]any{
			"name": "hub", "server": "https://media.example:6443",
			"labels": labels, "annotations": annotations,
			// ArgoCD serves the credential block redacted. Nothing here reads
			// it, and the struct has no field for it; this asserts that an
			// unknown field does not break the decode.
			"config": map[string]any{"bearerToken": "********"},
		}))
	}))

	fromArgo, err := argo.ClusterInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromSecrets, fromArgo) {
		t.Fatalf("the API read and the Secret decode disagree about the same cluster, so "+
			"the inventory depends on which source was read:\n  secrets: %+v\n  argocd:  %+v",
			fromSecrets.Clusters, fromArgo.Clusters)
	}
}

func TestArgoCDSendsTheTokenAndReadsTheClustersEndpoint(t *testing.T) {
	var gotPath, gotAuth string
	a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		fmt.Fprint(w, clusterList(map[string]any{
			"name": "hub", "server": "https://media.example:6443",
			"labels": map[string]string{"environment": "production"},
		}))
	}))

	inv, err := a.ClusterInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/clusters" {
		t.Fatalf("read %s, want the ArgoCD clusters endpoint", gotPath)
	}
	if gotAuth != "Bearer argocd-tok" {
		t.Fatalf("sent %q -- the ArgoCD API authenticates with an account token", gotAuth)
	}
	if len(inv.Clusters) != 1 || inv.Clusters[0].Name != "hub" {
		t.Fatalf("got %+v", inv.Clusters)
	}
	// Not served by the API for a cluster registered before labels were
	// copied, and selectors in the wild match on it.
	if inv.Clusters[0].Labels["argocd.argoproj.io/secret-type"] != "cluster" {
		t.Fatal("a secret-backed cluster must carry the secret-type label, as it does from the Secret path")
	}
}

func TestArgoCDLeavesTheImplicitLocalClusterUnlabelled(t *testing.T) {
	a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// ArgoCD includes the local cluster in this list rather than omitting
		// it, with no labels, exactly the entry the Secret path invents.
		fmt.Fprint(w, clusterList(map[string]any{
			"name": "in-cluster", "server": "https://kubernetes.default.svc",
		}))
	}))

	inv, err := a.ClusterInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Clusters) != 1 || inv.Clusters[0].Name != "in-cluster" {
		t.Fatalf("got %+v", inv.Clusters)
	}
	if len(inv.Clusters[0].Labels) != 0 {
		t.Fatalf("the implicit cluster carries no labels in ArgoCD, so stamping one here "+
			"would make a selector target a cluster ArgoCD would not: %v", inv.Clusters[0].Labels)
	}
}

func TestArgoCDKeepsALocalClusterSomebodyRegistered(t *testing.T) {
	a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Same address, but a real Secret behind it, so it has labels, and
		// dropping it as "the implicit one" would delete a cluster the gate
		// must render against.
		fmt.Fprint(w, clusterList(map[string]any{
			"name": "hub", "server": "https://kubernetes.default.svc",
			"labels": map[string]string{"argocd.argoproj.io/secret-type": "cluster", "environment": "production"},
		}))
	}))

	inv, err := a.ClusterInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Clusters) != 1 || inv.Clusters[0].Name != "hub" ||
		inv.Clusters[0].Labels["environment"] != "production" {
		t.Fatalf("a registered local cluster must survive with its labels: %+v", inv.Clusters)
	}
}

func TestArgoCDWithNoClustersAnswersAsTheSecretPathDoes(t *testing.T) {
	a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, clusterList())
	}))
	inv, err := a.ClusterInventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inv, implicitLocalCluster()) {
		t.Fatalf("an empty list must answer with the implicit local cluster, got %+v", inv)
	}
}

func TestArgoCDRefusesRatherThanRenderingAgainstNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		want string
	}{
		// Both are ordinary misconfigurations and they have different fixes,
		// so the error has to say which one happened.
		{"token rejected", http.StatusUnauthorized, "token was rejected"},
		{"account cannot list clusters", http.StatusForbidden, "clusters, get"},
		{"argocd-server down", http.StatusBadGateway, "502"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "no", tc.code)
			}))
			_, err := a.ClusterInventory(context.Background())
			if err == nil {
				t.Fatal("a failed read must surface as an error, not an empty inventory")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("the error should say what to fix: %v", err)
			}
		})
	}
}
