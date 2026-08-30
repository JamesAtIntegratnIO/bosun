package cluster

import (
	"fmt"
	"net/url"
)

// The fixed Kubernetes reads this package makes, as data.
//
// Every one of these needs a matching rule in the ClusterRole that
// charts/bosun grants, and until this table existed there was nothing joining
// the two: the paths were string literals spread across kargo.go and
// apiserver.go, and the grant was a hand-written list in a template in another
// language. A verb the code needs and the chart does not grant is not a crash
// -- the apiserver answers 403, and this package is deliberately built to turn
// that into a soft, honest sentence ("not permitted to check ...") so one
// missing grant degrades a brief instead of failing a sweep. That is the right
// behaviour at run time and it is exactly why the mistake would never be
// chased: nothing is broken, a line is just quietly missing from every report.
//
// So the paths are built FROM this table, not merely described by it. A test in
// the root package renders the chart and checks the ClusterRole covers it, and
// that test is only worth anything because the code below cannot disagree with
// what it reads.
//
// What is deliberately NOT here: CountLive's group/version/plural. Which custom
// resources the agent may count is the operator's choice, expressed in
// liveReads.apiGroups and answered by a `resources: ["*"]` rule -- there is no
// fixed list to check it against, and inventing one would be a second, wrong
// answer to a question the chart already answers.
type Read struct {
	// Group, Version and Plural name the resource, as the API path spells it.
	Group, Version, Plural string
	// Verbs are the RBAC verbs this read needs. `get` for one object by name,
	// `list` for a collection.
	Verbs []string
	// LiveReadsOnly marks a read the chart grants only when
	// liveReads.enabled is true, so a test knows which render to look in.
	LiveReadsOnly bool
	// Why names the caller, so a failing coverage test can say what stops
	// working rather than only which rule is missing.
	Why string
}

var (
	readStages = Read{Group: "kargo.akuity.io", Version: "v1alpha1", Plural: "stages",
		Verbs: []string{"list"}, Why: "the pipeline sweep reads every Stage's promotion template"}
	readWarehouses = Read{Group: "kargo.akuity.io", Version: "v1alpha1", Plural: "warehouses",
		Verbs: []string{"list"}, Why: "the sweep needs the Warehouse to know what should have been promoted"}
	readPromotions = Read{Group: "kargo.akuity.io", Version: "v1alpha1", Plural: "promotions",
		Verbs: []string{"list"}, Why: "the sweep finds the promotions that never happened"}

	readApplications = Read{Group: "argoproj.io", Version: "v1alpha1", Plural: "applications",
		Verbs: []string{"get"}, Why: "the triage reports the health of the Applications a promotion verifies"}

	readCRDs = Read{Group: "apiextensions.k8s.io", Version: "v1", Plural: "customresourcedefinitions",
		Verbs: []string{"get"}, LiveReadsOnly: true,
		Why: "the gate learns which versions a definition still serves before calling one dropped"}
)

// Reads is every fixed API read this package makes.
//
// Exported for the chart's benefit: the ClusterRole in charts/bosun has to
// cover this, and a list nothing can enumerate is a list that drifts.
func Reads() []Read {
	return []Read{readStages, readWarehouses, readPromotions, readApplications, readCRDs}
}

// GVK names the resource the way a person reads it, for a message.
func (r Read) GVK() string { return gvk(r.Group, r.Version, r.Plural) }

// groupRoot is the path of the API group itself, which is what a discovery
// probe asks for: a cluster that does not serve this group answers 404 there.
func (r Read) groupRoot() string { return fmt.Sprintf("/apis/%s/%s", r.Group, r.Version) }

// collection is the path listing every object of this kind, cluster-wide.
func (r Read) collection() string { return listPath(r.Group, r.Version, r.Plural) }

// object is the path of one cluster-scoped object by name.
func (r Read) object(name string) string {
	return r.collection() + "/" + url.PathEscape(name)
}

// namespaced is the path of one namespaced object by name.
func (r Read) namespaced(ns, name string) string {
	return fmt.Sprintf("/apis/%s/%s/namespaces/%s/%s/%s",
		r.Group, r.Version, url.PathEscape(ns), r.Plural, url.PathEscape(name))
}
