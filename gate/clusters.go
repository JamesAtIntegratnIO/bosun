package gate

import "encoding/base64"

// ClusterSecret is the subset of an ArgoCD cluster Secret the inventory is
// built from, in the shape the Kubernetes API serves it: `data` values are
// base64 strings on the JSON wire. The live reader in the cluster package does
// not parse Secrets, it reads the ArgoCD API, but the Secrets stay ArgoCD's
// own storage for these facts, and the fidelity tests decode one through here
// to prove the API read reports the same cluster the Secret defines.
type ClusterSecret struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

// InventoryFromSecrets builds an inventory from ArgoCD cluster Secrets.
func InventoryFromSecrets(items []ClusterSecret) *Inventory {
	cs := make([]Cluster, 0, len(items))
	for _, item := range items {
		c := Cluster{
			Labels:      item.Metadata.Labels,
			Annotations: item.Metadata.Annotations,
			Name:        decode(item.Data["name"]),
			Server:      decode(item.Data["server"]),
		}
		if c.Name == "" {
			c.Name = item.Metadata.Name
		}
		cs = append(cs, c)
	}
	return InventoryFromClusters(cs)
}

// InventoryFromClusters normalises clusters that arrive already decoded,
// which is what a reader that is not looking at Secrets has: the ArgoCD API
// serves name, server, labels and annotations as fields, with the credential
// block redacted.
//
// This exists so the normalisation is written once. A selector matches, or
// fails to match, on exactly these maps, so two readers that trimmed one key
// differently would produce different targeting verdicts from the same
// cluster, and nothing downstream could tell.
func InventoryFromClusters(cs []Cluster) *Inventory {
	inv := &Inventory{}
	for _, c := range cs {
		// Copied, not aliased: the inventory is normalised below, and writing
		// that into the caller's own maps would be a side effect nobody asked
		// for.
		labels := copyMap(c.Labels)
		annotations := dropManagedBy(copyMap(c.Annotations))
		// Every ArgoCD cluster Secret carries this label; it is the one the
		// Secrets are found by, and generators in the wild routinely select
		// on it.
		//
		// The one entry that must not get it is the implicit local cluster,
		// which is backed by no Secret and carries no labels in ArgoCD either.
		// Callers hand that one over already built rather than through here.
		if _, ok := labels["argocd.argoproj.io/secret-type"]; !ok {
			labels["argocd.argoproj.io/secret-type"] = "cluster"
		}
		inv.Clusters = append(inv.Clusters, Cluster{
			Name:        c.Name,
			Server:      c.Server,
			ArgoCD:      c.ArgoCD,
			Labels:      labels,
			Annotations: annotations,
		})
	}
	return inv
}

func decode(s string) string {
	if s == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// managedByAnnotation is ArgoCD's own ownership marker, dropped from every
// inventory regardless of which source built it.
//
// Found by running the Secret decode and the API read against a real ArgoCD:
// the cluster Secrets carry it and `GET /api/v1/clusters` does not, because
// ArgoCD strips it on the way out of its own API. Left alone, the same cluster
// produced two different inventories; the one thing this normalisation exists
// to make impossible.
//
// It is dropped rather than synthesised on the ArgoCD side, and that direction
// is deliberate. Re-adding it would mean asserting a fact the API did not
// report, for clusters that may never have carried it, inventing data to make
// a comparison come out even, which is the habit this codebase refuses
// everywhere else.
//
// The cost, stated because it is real rather than zero: ApplicationSet's
// cluster generator templates against the Secret's annotations verbatim, so a
// template referring to `metadata.annotations.managed-by` would see it in
// production and will not see it here. Nothing sane templates with ArgoCD's
// ownership marker, and a gate that renders one key differently beats a gate
// whose answer depends on its configuration.
const managedByAnnotation = "managed-by"

func dropManagedBy(m map[string]string) map[string]string {
	delete(m, managedByAnnotation)
	return m
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
