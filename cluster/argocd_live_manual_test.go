package cluster

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// TestLiveArgoCDMatchesTheSecrets is the equivalence claim, checked against a
// real ArgoCD rather than a fixture.
//
// The unit test proves the two readers agree on a cluster somebody wrote down
// in a test file, which is exactly the kind of agreement that survives being
// wrong: both sides were written by the same person on the same afternoon, and
// a field ArgoCD populates differently from the Secret appears in neither. The
// interesting failure is a live one, ArgoCD strips an annotation, or reports a
// name the Secret spells another way, and the only thing that finds it is a
// cluster with real clusters registered in it.
//
// The gate's verdict depends on these two answering identically. A label the
// API drops is an ApplicationSet the gate stops seeing as targeted.
//
// Run it against a cluster you can reach:
//
//	ARGOCD_BASE_URL=https://argocd.example \
//	ARGOCD_TOKEN_FILE=/path/to/token \
//	KUBE_CONTEXT=your-context ARGOCD_NS=argocd \
//	 go test ./cluster -run LiveArgoCD -v
//
// The token needs `clusters, get` in ArgoCD's RBAC; the kube context needs
// get/list on Secrets in the ArgoCD namespace; this test reads both sources
// precisely because it is checking that it need not, in production, be both.
func TestLiveArgoCDMatchesTheSecrets(t *testing.T) {
	base, tokenFile := os.Getenv("ARGOCD_BASE_URL"), os.Getenv("ARGOCD_TOKEN_FILE")
	if base == "" || tokenFile == "" {
		t.Skip("set ARGOCD_BASE_URL and ARGOCD_TOKEN_FILE")
	}
	tok, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	// From a file rather than an environment variable, and never logged: a
	// token in the environment ends up in a process listing and in every
	// shell history that ran the command.
	argo := &ArgoCD{BaseURL: base, Token: string(trimSpace(tok))}

	fromArgo, err := argo.ClusterInventory(context.Background())
	if err != nil {
		t.Fatalf("reading the ArgoCD API: %v", err)
	}

	ns := os.Getenv("ARGOCD_NS")
	if ns == "" {
		ns = "argocd"
	}
	args := []string{"get", "secrets", "-n", ns, "-l", "argocd.argoproj.io/secret-type=cluster", "-o", "json"}
	if kc := os.Getenv("KUBE_CONTEXT"); kc != "" {
		args = append(args, "--context="+kc)
	}
	out, err := exec.Command("kubectl", args...).Output()
	if err != nil {
		t.Fatalf("reading the cluster Secrets: %v", err)
	}
	var list struct {
		Items []gate.ClusterSecret `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}
	fromSecrets := gate.InventoryFromSecrets(list.Items, gate.ExportFilter{})
	if len(fromSecrets.Clusters) == 0 {
		fromSecrets = implicitLocalCluster()
	}

	if len(fromArgo.Clusters) != len(fromSecrets.Clusters) {
		t.Fatalf("cluster COUNT differs: argocd has %d, the Secrets have %d",
			len(fromArgo.Clusters), len(fromSecrets.Clusters))
	}
	// Compared per cluster rather than whole, so a failure names the cluster
	// and the field instead of printing two inventories to eyeball.
	byName := map[string]gate.Cluster{}
	for _, c := range fromSecrets.Clusters {
		byName[c.Name] = c
	}
	for _, a := range fromArgo.Clusters {
		s, ok := byName[a.Name]
		if !ok {
			t.Errorf("%s: present in the ArgoCD API, absent from the Secrets", a.Name)
			continue
		}
		if a.Server != s.Server {
			t.Errorf("%s: server differs -- argocd %q, secret %q", a.Name, a.Server, s.Server)
		}
		if !reflect.DeepEqual(a.Labels, s.Labels) {
			t.Errorf("%s: LABELS differ, which changes which ApplicationSets target it\n  argocd: %v\n  secret: %v",
				a.Name, a.Labels, s.Labels)
		}
		if !reflect.DeepEqual(a.Annotations, s.Annotations) {
			t.Errorf("%s: ANNOTATIONS differ, which changes the chart path and value files rendered\n  argocd: %v\n  secret: %v",
				a.Name, a.Annotations, s.Annotations)
		}
	}
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
