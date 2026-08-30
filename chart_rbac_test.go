package main

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// The ClusterRole the chart grants must cover every fixed API read the code
// makes, in both positions of the switch that gates half of them.
//
// This is the `gate.argocd.port` class with no cluster in it. That value
// rendered clean, linted clean, passed the values schema, and then dropped
// every packet -- a failure with no symptom except a timeout that pointed
// somewhere else. A missing RBAC rule is the same shape and quieter still: the
// apiserver answers 403, and cluster/ is deliberately built to turn 403 into a
// soft, honest sentence ("not permitted to check ...") so that one missing
// grant degrades a brief rather than failing a sweep. That is the right
// behaviour at run time, and it is exactly why nobody would ever chase it.
// Nothing is broken. A line is just quietly missing from every report, forever.
//
// The reads come from cluster.Reads(), which the code builds its request paths
// from -- not a second list describing it. See cluster/reads.go.
func TestTheClusterRoleCoversEveryReadTheCodeMakes(t *testing.T) {
	for _, liveReads := range []bool{false, true} {
		t.Run(fmt.Sprintf("liveReads=%t", liveReads), func(t *testing.T) {
			rules := clusterRoleRules(t, liveReads)

			for _, r := range cluster.Reads() {
				// A read the chart grants only with live reads on is not
				// expected in the other render, and asserting it were absent
				// would be asserting the over-grant this chart removed in
				// 0.24.0 on purpose.
				if r.LiveReadsOnly && !liveReads {
					continue
				}
				for _, verb := range r.Verbs {
					if covered(rules, r.Group, r.Plural, verb) {
						continue
					}
					t.Errorf("the code makes a %s read of %s and the ClusterRole does not grant it "+
						"with liveReads=%t.\n"+
						"  what stops working: %s\n"+
						"Add the rule to charts/bosun/templates/rbac.yaml. The apiserver answers 403 "+
						"and cluster/ turns that into \"not permitted to check %s\", so nothing fails "+
						"and nothing logs -- the line is simply missing from every report.",
						verb, r.GVK(), liveReads, r.Why, r.GVK())
				}
			}
		})
	}

	// The self-check. If rbac.yaml is ever restructured -- an include, a
	// generated role, a different document name -- clusterRoleRules finds
	// nothing, every `covered` call returns false, and the test above would
	// then be loudly wrong rather than silently vacuous. This catches the
	// opposite mistake: a parse that returns rules for the wrong document.
	if got := len(clusterRoleRules(t, true)); got < 4 {
		t.Fatalf("found only %d rules in the rendered ClusterRole with live reads on; "+
			"charts/bosun/templates/rbac.yaml no longer has the shape this test reads.", got)
	}
}

// Grants the code does not use.
//
// Reported rather than failed, deliberately. Narrowing a published ClusterRole
// is a breaking change for anybody who has bound to it, so it belongs in a
// chart release with a CHANGELOG entry, not in whichever pull request happens
// to run this test first. What this does is make the drift visible while it is
// still small: 0.24.0 had to remove a cluster-wide read of every pod spec that
// installs which never used the feature were paying for, and that grant was
// only noticed because somebody went looking.
func TestGrantsTheCodeDoesNotUseAreNamed(t *testing.T) {
	used := map[string]bool{}
	groups := map[string]bool{}
	for _, r := range cluster.Reads() {
		used[r.Group+"/"+r.Plural] = true
		groups[r.Group] = true
	}

	var unused []string
	for _, rule := range clusterRoleRules(t, true) {
		for _, g := range rule.groups {
			// Only the groups this code actually reads from, and only rules
			// that name their resources. `*` is the wide live-reads grant,
			// whose whole point is that the operator chooses what it covers.
			if !groups[g] {
				continue
			}
			for _, res := range rule.resources {
				if res == "*" || used[g+"/"+res] {
					continue
				}
				unused = append(unused, g+"/"+res)
			}
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		t.Logf("the ClusterRole grants %d resources no code path reads: %s\n"+
			"Not a failure: narrowing a published role breaks anybody bound to it, so it "+
			"belongs in a chart release rather than here. Worth a look when one is next cut.",
			len(unused), strings.Join(unused, ", "))
	}
}

// rule is one entry of a ClusterRole's rules, reduced to what is checked.
type rule struct{ groups, resources, verbs []string }

func clusterRoleRules(t *testing.T, liveReads bool) []rule {
	t.Helper()
	docs := helmtest.Render(t, "bosun",
		helmtest.Values("ci/lint-values.yaml"),
		helmtest.ShowOnly("templates/rbac.yaml"),
		helmtest.Set(fmt.Sprintf("liveReads.enabled=%t", liveReads)))

	var out []rule
	for _, d := range docs {
		if d.Kind != "ClusterRole" {
			continue
		}
		entries, _ := d.Body["rules"].([]any)
		for _, e := range entries {
			m, _ := e.(map[string]any)
			out = append(out, rule{
				groups:    strings_(m["apiGroups"]),
				resources: strings_(m["resources"]),
				verbs:     strings_(m["verbs"]),
			})
		}
	}
	return out
}

func covered(rules []rule, group, plural, verb string) bool {
	for _, r := range rules {
		if has(r.groups, group) && has(r.resources, plural) && has(r.verbs, verb) {
			return true
		}
	}
	return false
}

// has reports membership, treating RBAC's `*` as the wildcard it is.
func has(set []string, want string) bool {
	for _, s := range set {
		if s == want || s == "*" {
			return true
		}
	}
	return false
}

func strings_(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, i := range items {
		if s, ok := i.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
