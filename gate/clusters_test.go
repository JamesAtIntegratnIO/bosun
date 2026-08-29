package gate

import (
	"encoding/base64"
	"testing"
)

// gate/clusters.go produces the cluster inventory every render is expanded
// against. A cluster that arrives with the wrong labels is a generator that
// selects the wrong set, and the gate then reports the difference as a
// targeting change the pull request made.

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func secret(name string, labels, annotations map[string]string, data map[string]string) ClusterSecret {
	var s ClusterSecret
	s.Metadata.Name = name
	s.Metadata.Labels = labels
	s.Metadata.Annotations = annotations
	s.Data = data
	return s
}

// The name and server live base64-encoded in the Secret's data, and the
// Secret's own name is the fallback.
func TestInventoryFromSecretsDecodesNameAndServer(t *testing.T) {
	inv := InventoryFromSecrets([]ClusterSecret{
		secret("hub-secret", nil, nil, map[string]string{
			"name": b64("hub"), "server": b64("https://hub.example"),
		}),
		secret("edge-secret", nil, nil, nil),
	})

	if len(inv.Clusters) != 2 {
		t.Fatalf("got %d", len(inv.Clusters))
	}
	if inv.Clusters[0].Name != "hub" || inv.Clusters[0].Server != "https://hub.example" {
		t.Errorf("got %+v", inv.Clusters[0])
	}
	if inv.Clusters[1].Name != "edge-secret" {
		t.Errorf("the Secret name is the fallback, got %q", inv.Clusters[1].Name)
	}
	// Generators in the wild routinely select on this label, and every real
	// cluster Secret carries it.
	if inv.Clusters[0].Labels["argocd.argoproj.io/secret-type"] != "cluster" {
		t.Errorf("the secret-type label must be present: %v", inv.Clusters[0].Labels)
	}
}

// Labels are selector inputs and are never dropped: which selector a future
// bootstrap will match on is unknowable. The one key any inventory loses is
// ArgoCD's own ownership marker, because the API strips it and the Secrets
// carry it, and the verdict must not depend on which source was read.
func TestNormalisationKeepsLabelsAndDropsOnlyManagedBy(t *testing.T) {
	labels := map[string]string{"environment": "production", "obscure": "kept-anyway"}
	annotations := map[string]string{"used": "yes", "managed-by": "argocd.argoproj.io"}
	inv := InventoryFromSecrets([]ClusterSecret{
		secret("c", labels, annotations, map[string]string{"name": b64("c")}),
	})

	c := inv.Clusters[0]
	if c.Labels["obscure"] != "kept-anyway" {
		t.Errorf("no label may be dropped: %v", c.Labels)
	}
	if c.Annotations["used"] != "yes" {
		t.Errorf("an ordinary annotation must survive: %v", c.Annotations)
	}
	if _, ok := c.Annotations["managed-by"]; ok {
		t.Errorf("ArgoCD's ownership marker must be dropped: %v", c.Annotations)
	}
	// Normalised copies, not aliases: the caller's own maps are untouched.
	if _, ok := annotations["managed-by"]; !ok {
		t.Errorf("the input map must not be mutated: %v", annotations)
	}
	if _, ok := labels["argocd.argoproj.io/secret-type"]; ok {
		t.Errorf("the input map must not be mutated: %v", labels)
	}
}

func TestDecodeRejectsWhatIsNotBase64(t *testing.T) {
	if got := decode(b64("hello")); got != "hello" {
		t.Errorf("got %q", got)
	}
	if got := decode("not base64!!!"); got != "" {
		t.Errorf("undecodable data must not become a cluster name, got %q", got)
	}
	if got := decode(""); got != "" {
		t.Errorf("got %q", got)
	}
}
