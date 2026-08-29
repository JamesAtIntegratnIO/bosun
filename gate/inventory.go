package gate

import (
	"fmt"
	"sort"
	"strings"
)

// Cluster mirrors the fields of an ArgoCD cluster Secret that generators and
// templates can see: the name and server, plus labels and
// annotations. Selectors match on labels; templates read both.
type Cluster struct {
	Name   string `json:"name"`
	Server string `json:"server"`
	// ArgoCD names the instance this cluster is registered with, for fleets
	// running more than one, per region, per tenant, per business unit.
	// Empty means "the only one", which is the common case.
	ArgoCD      string            `json:"argocd,omitempty"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// Inventory is the set of clusters the gate expands generators against, read
// live from the ArgoCD API by the cluster package. The verdict is only as good
// as this set: a cluster the inventory misses is a cluster whose targeting
// changes the gate cannot see.
type Inventory struct {
	Clusters []Cluster `json:"clusters"`
}

// Validate checks that the inventory can answer the questions the selectors
// ask of it.
//
// This is what makes a diminished inventory an error rather than a wrong
// answer. Selectors match on labels, and a label the inventory has never seen
// does not match, so an inventory missing one renders a fraction of the real
// Applications and then reports "no targeting change" with total confidence.
// Measured: one missing label took a render from 62 Applications to 7,
// silently.
//
// The known set is derived from the clusters themselves rather than recorded
// separately. Recording it would be the same information written twice, and
// a stale file's own record is stale too, so it would detect nothing.
func (inv *Inventory) Validate(selectorKeys []string, knownAbsent []string) error {
	known := map[string]bool{}
	for _, c := range inv.Clusters {
		for k := range c.Labels {
			known[k] = true
		}
	}
	for _, k := range knownAbsent {
		known[k] = true
	}

	var unknown []string
	for _, k := range selectorKeys {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf(
		"selectors match on label(s) no cluster in the inventory carries: %s\n\n"+
			"Either no cluster carries those labels, in which case the Applications\n"+
			"selecting on them are generated for no cluster at all and that is worth\n"+
			"knowing, or the label is misspelt in .gitops-gate.yaml or on the\n"+
			"cluster itself.\n\n"+
			"If it is deliberate, list them under `clustersExport.knownAbsentLabels`\n"+
			"in .gitops-gate.yaml.\n\n"+
			"This is refused rather than assumed because the failure is silent: a\n"+
			"missing label shrinks the render, and the gate would then compare two\n"+
			"almost-empty sets and find no difference",
		strings.Join(unknown, ", "))
}

// TemplateData is what an ApplicationSet cluster generator exposes to its
// template for one cluster, in goTemplate mode.
func (c Cluster) TemplateData(values map[string]string) map[string]any {
	md := map[string]any{
		"labels":      toAny(c.Labels),
		"annotations": toAny(c.Annotations),
	}
	d := map[string]any{
		"name":           c.Name,
		"server":         c.Server,
		"metadata":       md,
		"nameNormalized": c.Name,
	}
	if values != nil {
		d["values"] = toAny(values)
	}
	return d
}

func toAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
