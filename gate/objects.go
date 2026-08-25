package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// Object is one Kubernetes resource that will exist in a cluster, identified
// the way the API server identifies it.
//
// This is a different and stronger signal than the Application table. Knowing
// that a chart moved from 0.15.2 to 0.16.0 tells you a version changed;
// knowing that the change removes two containers and adds a DaemonSet, four
// CRDs and a webhook tells you what will happen. Every incident this gate was
// built for was of the second kind -- a pull request that renders fine and
// breaks at runtime -- and none of them are visible at Application level.
type Object struct {
	Source     string `json:"source"`
	Cluster    string `json:"cluster,omitempty"`
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	// Hash is of the whole object, so a changed field is detectable without
	// storing the object itself. The table is an artifact a pull request
	// carries around; embedding every manifest would make it enormous.
	Hash string `json:"hash"`

	// Body is the normalised object, carried in memory ONLY -- `json:"-"` is
	// load-bearing. It exists so a "changed" finding can say WHICH fields
	// changed, and it must never reach the target table, or the artifact that
	// Hash was invented to keep small becomes the manifests all over again.
	//
	// Populated by chart-diff, which renders both versions in the same process
	// that diffs them. A table loaded from JSON has none, and the field diff is
	// simply omitted -- the finding is still reported.
	Body map[string]any `json:"-"`
}

// ID identifies an object across revisions. Deliberately excludes apiVersion:
// a resource whose API version moved is the SAME resource being migrated, and
// reporting it as one removal plus one addition hides exactly the migration a
// reviewer needs to see.
func (o Object) ID() string {
	return o.Cluster + "|" + o.Kind + "|" + o.Namespace + "|" + o.Name
}

func (o Object) Describe() string {
	base := fmt.Sprintf("%s/%s", o.Kind, o.Name)
	if o.Namespace != "" {
		base += " in " + o.Namespace
	}
	return base
}

// isTestHook reports whether an object is a Helm test hook.
//
// Test hooks are never applied by a sync -- they run on `helm test` and
// nothing else -- so reporting them as deployed resources is wrong on its own.
// They are also the one place charts routinely generate a random name, so
// every render produces a different one and the diff shows the same three
// pods added and removed on every single bump. Other hooks (pre-install,
// post-upgrade) ARE applied, and are deliberately still reported.
func isTestHook(meta map[string]any) bool {
	ann, _ := meta["annotations"].(map[string]any)
	h, _ := ann["helm.sh/hook"].(string)
	for _, part := range strings.Split(h, ",") {
		switch strings.TrimSpace(part) {
		case "test", "test-success", "test-failure":
			return true
		}
	}
	return false
}

// objectFrom builds an Object, returning false for anything that is not a
// Kubernetes resource or is not something a sync would apply.
//
// defaultNS is the Application's destination namespace. A namespaced resource
// whose manifest omits `metadata.namespace` lands there when ArgoCD applies
// it, so that is its real identity -- and whether a chart stamps the field at
// all varies BETWEEN VERSIONS OF THE SAME CHART. podinfo 6.7.0 omits it and
// 6.14.1 sets it, which made every object in the chart read as one removal
// plus one addition rather than a change.
func objectFrom(source, cluster, defaultNS string, obj map[string]any) (Object, bool) {
	kind, _ := obj["kind"].(string)
	apiVersion, _ := obj["apiVersion"].(string)
	if kind == "" || apiVersion == "" {
		return Object{}, false
	}
	meta, _ := obj["metadata"].(map[string]any)
	name, _ := meta["name"].(string)
	if name == "" {
		return Object{}, false
	}
	if isTestHook(meta) {
		return Object{}, false
	}
	ns, _ := meta["namespace"].(string)
	if ns == "" {
		ns = defaultNS
	}

	raw, err := yaml.Marshal(normalise(obj))
	if err != nil {
		return Object{}, false
	}
	sum := sha256.Sum256(raw)

	return Object{
		Source: source, Cluster: cluster,
		APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name,
		Hash: hex.EncodeToString(sum[:8]),
		Body: normalise(obj),
	}, true
}

// FieldChange is one leaf that differs between two renders of an object.
//
// Paths are dotted with numeric list indices --
// `spec.template.spec.containers.0.image` -- the same shape the agent's edit
// inventory uses, so a human and an agent read the report the same way.
type FieldChange struct {
	Path string `json:"path"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// servedVersions is the set of versions a CustomResourceDefinition serves.
//
// Empty for anything else, and empty when the body was not carried -- a table
// loaded from JSON cannot answer this, and saying "no versions removed"
// because we could not look would be the worst possible answer.
func servedVersions(o Object) map[string]bool {
	if o.Kind != "CustomResourceDefinition" || o.Body == nil {
		return nil
	}
	spec, _ := o.Body["spec"].(map[string]any)
	raw, _ := spec["versions"].([]any)
	out := map[string]bool{}
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		// `served: true` is the default in apiextensions/v1 when the field is
		// absent, so a missing key means served, not unserved.
		if served, present := m["served"].(bool); present && !served {
			continue
		}
		if name, _ := m["name"].(string); name != "" {
			out[name] = true
		}
	}
	return out
}

// droppedVersions are versions the base served and the head does not.
func droppedVersions(before, after Object) []string {
	was, now := servedVersions(before), servedVersions(after)
	if len(was) == 0 {
		return nil
	}
	var gone []string
	for v := range was {
		if !now[v] {
			gone = append(gone, v)
		}
	}
	sort.Strings(gone)
	return gone
}

// crdConsumerKind is what a manifest consuming this CRD writes in its `kind:`
// field -- spec.names.kind, which is not derivable from the CRD's own name.
func crdConsumerKind(o Object) string {
	spec, _ := o.Body["spec"].(map[string]any)
	names, _ := spec["names"].(map[string]any)
	kind, _ := names["kind"].(string)
	return kind
}

// survivingVersion is the served version consumers should move to, chosen by
// API-server priority from what the head still serves. Empty when nothing
// survives, in which case there is no destination to migrate to and the
// finding stays a human's problem.
func survivingVersion(o Object) string {
	var still []string
	for v := range servedVersions(o) {
		still = append(still, v)
	}
	return migrate.PreferredVersion(still)
}

// ObjectChange is one difference between two renders of the same object set.
type ObjectChange struct {
	Kind    string `json:"kind"` // added | removed | changed | apiVersion | crdVersionRemoved | valuesKeyDropped
	Object  string `json:"object"`
	Cluster string `json:"cluster,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`

	// crdVersionRemoved only. Resource is the kind consumers declare
	// (spec.names.kind); To above carries the served version they must move
	// to. ConsumerFiles are the repository manifests still declaring a dropped
	// version, and ConsumersKnown records that the repository was actually
	// scanned -- an unscanned finding blocks, because "we could not look" must
	// never read as "nothing depends on it".
	Resource       string   `json:"resource,omitempty"`
	ConsumerFiles  []string `json:"consumerFiles,omitempty"`
	ConsumersKnown bool     `json:"consumersKnown,omitempty"`

	// apiVersion only. True when this move is exactly what a
	// crdVersionRemoved finding in the same diff demands -- same kind, from a
	// dropped version, to the named survivor. That is the repair, not a new
	// migration, and blocking on it would mean no pull request that fixes a
	// dropped served version could ever go green: the first live repair
	// proved it by migrating 27 manifests and turning its own gate red.
	PartOfMigration bool `json:"partOfMigration,omitempty"`

	// valuesKeyDropped only. Keys are paths this repository SETS that the old
	// chart version declared and the new one does not. Helm ignores an unknown
	// value rather than failing on it, so every one of these is a setting that
	// silently stops applying while the render stays green.
	Keys []string `json:"keys,omitempty"`

	// Note is a computed fact about this change worth a reader's eyes --
	// today, a removed binding whose ServiceAccount retains no RBAC in the
	// new render. Reported under the item, never blocking.
	Note string `json:"note,omitempty"`

	// Fields are the leaves that differ, when both renders were available in
	// process. Empty is not "nothing changed" -- it is "not computed here".
	Fields []FieldChange `json:"fields,omitempty"`
	// Truncated counts further leaves beyond MaxFieldsPerObject.
	Truncated int `json:"truncatedFields,omitempty"`
}

// MaxFieldsPerObject bounds one object's field list. A chart that rewrites
// every label would otherwise produce a report longer than any comment box
// accepts, and a report nobody can open is worth less than a short one.
const MaxFieldsPerObject = 12

// diffFields walks two normalised objects and returns the leaves that differ.
//
// Leaves only: a map or list that gained members reports the members, not the
// container, because "spec.template.spec.containers changed" is the same
// non-answer the object-level diff already gives.
func diffFields(before, after map[string]any) ([]FieldChange, int) {
	var out []FieldChange
	// A leaf is usually a scalar, but a whole map or list arrives here when one
	// side gained an element. Go's %v prints those as `map[name:mcp port:8081]`,
	// which is not a shape anyone reading Kubernetes manifests recognises.
	// Compact JSON is.
	scalar := func(v any) string {
		if v == nil {
			return ""
		}
		var s string
		switch v.(type) {
		case map[string]any, []any:
			if b, err := json.Marshal(v); err == nil {
				s = string(b)
			} else {
				s = fmt.Sprintf("%v", v)
			}
		default:
			s = fmt.Sprintf("%v", v)
		}
		if len(s) > 120 {
			s = s[:117] + "..."
		}
		return s
	}
	join := func(p, k string) string {
		if p == "" {
			return k
		}
		return p + "." + k
	}
	var walk func(path string, b, a any)
	walk = func(path string, b, a any) {
		switch bb := b.(type) {
		case map[string]any:
			aa, ok := a.(map[string]any)
			if !ok {
				out = append(out, FieldChange{Path: path, From: scalar(b), To: scalar(a)})
				return
			}
			seen := map[string]bool{}
			var names []string
			for k := range bb {
				if !seen[k] {
					seen[k] = true
					names = append(names, k)
				}
			}
			for k := range aa {
				if !seen[k] {
					seen[k] = true
					names = append(names, k)
				}
			}
			sort.Strings(names)
			for _, k := range names {
				walk(join(path, k), bb[k], aa[k])
			}
		case []any:
			aa, ok := a.([]any)
			if !ok {
				out = append(out, FieldChange{Path: path, From: scalar(b), To: scalar(a)})
				return
			}
			n := len(bb)
			if len(aa) > n {
				n = len(aa)
			}
			for i := 0; i < n; i++ {
				var bv, av any
				if i < len(bb) {
					bv = bb[i]
				}
				if i < len(aa) {
					av = aa[i]
				}
				walk(fmt.Sprintf("%s.%d", path, i), bv, av)
			}
		default:
			if fmt.Sprintf("%v", b) != fmt.Sprintf("%v", a) {
				out = append(out, FieldChange{Path: path, From: scalar(b), To: scalar(a)})
			}
		}
	}
	walk("", before, after)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) > MaxFieldsPerObject {
		return out[:MaxFieldsPerObject], len(out) - MaxFieldsPerObject
	}
	return out, 0
}

// diffObjects compares two object sets.
func diffObjects(base, head []Object) []ObjectChange {
	byID := func(in []Object) map[string]Object {
		m := make(map[string]Object, len(in))
		for _, o := range in {
			m[o.ID()] = o
		}
		return m
	}
	b, h := byID(base), byID(head)

	var out []ObjectChange
	for id, o := range h {
		prev, ok := b[id]
		switch {
		case !ok:
			out = append(out, ObjectChange{Kind: "added", Object: o.Describe(), Cluster: o.Cluster, To: o.APIVersion})
		case prev.APIVersion != o.APIVersion:
			// Called out separately because it is never routine: an API
			// version moving under a resource is a migration, and it is the
			// single most reliable signal that a bump needs a human.
			out = append(out, ObjectChange{
				Kind: "apiVersion", Object: o.Describe(), Cluster: o.Cluster,
				From: prev.APIVersion, To: o.APIVersion,
			})
		case prev.Hash != o.Hash:
			// A CRD that stops serving a version is a migration wearing a
			// content change. The object's own apiVersion does not move --
			// both sides are apiextensions.k8s.io/v1 -- so the apiVersion rule
			// cannot see it, and every manifest in the repository still
			// declaring the dropped version breaks at apply time.
			if gone := droppedVersions(prev, o); len(gone) > 0 {
				out = append(out, ObjectChange{
					Kind: "crdVersionRemoved", Object: o.Describe(), Cluster: o.Cluster,
					From: strings.Join(gone, ", "),
					// The repair contract: which kind consumers declare, and
					// where they must move. Both from the head body, so a
					// finding built from a bodiless table carries neither and
					// stays un-repairable on purpose.
					Resource: crdConsumerKind(o),
					To:       survivingVersion(o),
				})
				continue
			}
			c := ObjectChange{Kind: "changed", Object: o.Describe(), Cluster: o.Cluster}
			if prev.Body != nil && o.Body != nil {
				c.Fields, c.Truncated = diffFields(prev.Body, o.Body)
			}
			out = append(out, c)
		}
	}
	for id, o := range b {
		if _, ok := h[id]; !ok {
			// A CRD removed outright is the limiting case of dropping served
			// versions -- ALL of them, with nowhere to move. It used to sit in
			// the plain Removed list, uninspected, while the version-drop path
			// counted consumers; a reviewer got "12 resources removed" and had
			// to go looking themselves whether anything here used those APIs.
			// It now joins the consumer-scanned class: with a worktree, the
			// report either names every declaring manifest (blocking) or says
			// outright that nothing in the repository uses the API and the
			// removal looks safe from inspection. No survivor means no repair
			// -- the agent's parser deliberately cannot act on it.
			if versions := servedVersions(o); len(versions) > 0 {
				var all []string
				for v := range versions {
					all = append(all, v)
				}
				sort.Strings(all)
				out = append(out, ObjectChange{
					Kind: "crdVersionRemoved", Object: o.Describe(), Cluster: o.Cluster,
					From:     strings.Join(all, ", "),
					Resource: crdConsumerKind(o),
				})
				continue
			}
			c := ObjectChange{Kind: "removed", Object: o.Describe(), Cluster: o.Cluster, From: o.APIVersion}
			// A removed binding's blast radius is its subjects: a
			// ServiceAccount that no remaining binding grants anything is how
			// "the chart tidied its RBAC" and "the workload just lost every
			// permission it runs on" tell each other apart.
			c.Note = unboundSubjects(o, head)
			out = append(out, c)
		}
	}

	// The same resource changing identically on several clusters is one
	// finding, not one per cluster. Collapse them and say where.
	collapsed := map[string]*ObjectChange{}
	var order []string
	for i := range out {
		c := out[i]
		k := c.Kind + "|" + c.Object + "|" + c.From + "|" + c.To
		if prev, ok := collapsed[k]; ok {
			if c.Cluster != "" && !strings.Contains(prev.Cluster, c.Cluster) {
				prev.Cluster += ", " + c.Cluster
			}
			continue
		}
		cp := c
		collapsed[k] = &cp
		order = append(order, k)
	}
	out = out[:0]
	for _, k := range order {
		out = append(out, *collapsed[k])
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Object < out[j].Object
	})
	return out
}

// unboundSubjects reports the removed binding's ServiceAccounts that no
// binding in the head render still grants anything, on the same cluster.
//
// Empty when the question cannot be answered honestly: a binding without a
// body (a JSON-loaded table), or any head binding whose subjects cannot be
// read -- claiming "unbound" past an unreadable binding would be a guess, and
// the note simply does not appear rather than appearing wrong.
func unboundSubjects(o Object, head []Object) string {
	if (o.Kind != "ClusterRoleBinding" && o.Kind != "RoleBinding") || o.Body == nil {
		return ""
	}
	subs := serviceAccountSubjects(o)
	if len(subs) == 0 {
		return ""
	}
	bound := map[string]bool{}
	for _, h := range head {
		if h.Kind != "ClusterRoleBinding" && h.Kind != "RoleBinding" {
			continue
		}
		if h.Cluster != o.Cluster {
			continue
		}
		if h.Body == nil {
			return ""
		}
		for _, s := range serviceAccountSubjects(h) {
			bound[s] = true
		}
	}
	var lost []string
	for _, s := range subs {
		if !bound[s] {
			lost = append(lost, s)
		}
	}
	if len(lost) == 0 {
		return ""
	}
	sort.Strings(lost)
	return fmt.Sprintf("leaves ServiceAccount `%s` with **no role binding at all** in the new render — either the new version needs none, or something just lost every permission it runs on",
		strings.Join(lost, "`, `"))
}

// serviceAccountSubjects lists a binding's ServiceAccount subjects as
// namespace/name, defaulting the namespace to the binding's own.
func serviceAccountSubjects(o Object) []string {
	raw, _ := o.Body["subjects"].([]any)
	var out []string
	for _, s := range raw {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if kind, _ := m["kind"].(string); kind != "ServiceAccount" {
			continue
		}
		name, _ := m["name"].(string)
		if name == "" {
			continue
		}
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = o.Namespace
		}
		out = append(out, ns+"/"+name)
	}
	return out
}

// versionStamps are labels Helm writes into EVERY object it renders, carrying
// the chart and app version.
//
// Hashing them makes a version bump report every single resource as changed --
// measured at 101 of 105 on one cert-manager bump -- which buries the four
// that actually changed. They are stripped before hashing, so "changed" means
// something a reader should look at. The version itself is already reported,
// in the Versions table, once.
var versionStamps = []string{
	"helm.sh/chart",
	"app.kubernetes.io/version",
	"app.kubernetes.io/managed-by",
	"chart",
}

// normalise returns a copy of an object with version stamps removed.
func normalise(obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	meta, ok := obj["metadata"].(map[string]any)
	if !ok {
		return out
	}
	newMeta := make(map[string]any, len(meta))
	for k, v := range meta {
		newMeta[k] = v
	}
	for _, field := range []string{"labels", "annotations"} {
		m, ok := meta[field].(map[string]any)
		if !ok {
			continue
		}
		cleaned := make(map[string]any, len(m))
		for k, v := range m {
			stamp := false
			for _, s := range versionStamps {
				if k == s {
					stamp = true
					break
				}
			}
			if !stamp {
				cleaned[k] = v
			}
		}
		newMeta[field] = cleaned
	}
	out["metadata"] = newMeta
	return out
}
