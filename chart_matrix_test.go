package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// Every switch charts/bosun declares gets rendered, and the list of switches
// comes from the chart's own values.schema.json rather than from this file.
//
// 0.24.0 and 0.25.0 both shipped a ClusterRole that was not YAML whenever
// liveReads.enabled was true. A template comment closed with `-}}`, which trims
// the following newline as well as the indentation, so the first live-reads
// rule began on the end of the previous rule's line. helm lint, the values
// schema and the portability test were all green, because all three rendered
// ci/lint-values.yaml -- and helm merges every ci/*-values.yaml with repeated
// -f, so those files can only ever describe ONE install, the one with every
// default-off feature off. It was found by a consumer rendering their own
// values by hand, two releases later.
//
// The repair at the time was five hand-added render cases in
// hack/portability-test.sh, and nothing made a sixth switch join them. So the
// list is derived instead: a boolean, a nullable boolean or an enum in
// values.schema.json is a switch, and a switch this table does not name fails
// below with the line to add.
//
// Found by writing this: charts/bosun/templates/networkpolicy.yaml renders a
// whole CiliumNetworkPolicy under networkPolicy.flavor=cilium, and nothing in
// this repository had ever set it.
//
// These checks were bash until this file existed. They moved because the list
// has to be derived from a JSON schema, and because the interesting assertions
// are about the rendered document rather than the exit code -- "the egress rule
// opened podPort" was an awk state machine over rendered text.

// position is one setting of one switch, and what it takes to render it.
type position struct {
	// Set is the --set argument, layered on top of ci/lint-values.yaml.
	Set string
	// With are the companion values this position needs to be legal. The chart
	// refuses twelve value combinations by name, and a position missing its
	// companion would fail for a reason that is about this fixture rather than
	// about the template.
	With []string
	// Refuses marks the other kind of row: a position the chart MUST reject,
	// and a distinctive part of the message it must reject it with. A `fail`
	// that stops firing is a guard that has silently become a comment, and
	// nothing about a running install would ever show it.
	Refuses string
	// Why is the incident, or the reason. Not every row needs one; the rows
	// that came from a real failure all carry it.
	Why string
}

// switchRow is every position of one switch.
type switchRow struct {
	Positions []position
	// Unrendered says this switch is deliberately rendered by nothing, and
	// why. It still has to appear in this table: the point of deriving the
	// list is that a switch cannot be absent by accident, and "there is
	// nothing to render" is a claim worth making out loud rather than by
	// omission.
	Unrendered string
}

var matrix = map[string]switchRow{
	"enabled": {Unrendered: "a parent chart's `condition: bosun.enabled` key, which the schema " +
		"declares so that a values file carrying it still validates. This chart reads it nowhere, " +
		"so there is no render to make."},

	"image.pullPolicy": {Positions: []position{
		{Set: "image.pullPolicy=Always"},
		{Set: "image.pullPolicy=Never"},
	}},

	"credentials.mountAsFiles": {Positions: []position{
		{Set: "credentials.mountAsFiles=true",
			Why: "every credential turns into a file mount and an if/else in deployment.yaml; an " +
				"addition where a branch was meant sets both K and K_FILE, which config.go refuses at start-up"},
	}},

	"web.enabled": {Positions: []position{
		{Set: "web.enabled=false", With: []string{"web.httpRoute.enabled=false", "web.ingress.enabled=false"},
			Why: "the lint values publish the page both ways, so switching the page off needs both routes off with it"},
		{Set: "web.enabled=false", Refuses: "there would be nothing listening behind the route",
			Why: "a route with the page switched off publishes a port the pod does not listen on, " +
				"and neither the Service nor the route says so"},
	}},

	// Added by the theme work while this table was being written, and caught
	// by TestEverySwitchTheSchemaDeclaresIsRendered on the first render after
	// the rebase -- which is the whole of what this table is for.
	"web.theme": {Positions: []position{
		{Set: "web.theme=dark"},
		{Set: "web.theme=light"},
		{Set: "web.theme=auto"},
	}},

	// The MCP surface, and the two rows that are the point of it. Off by
	// default, so nothing else in this repository renders any of it -- which is
	// the 0.25.0 ClusterRole's exact situation, and the reason this table is
	// derived from the schema rather than written by hand.
	"mcp.enabled": {Positions: []position{
		{Set: "mcp.enabled=true",
			With: []string{"mcp.existingSecret=bosun-mcp",
				"mcp.allowFrom[0].namespace=gateway-system"},
			Why: "the whole surface -- a container port, a Service port, a NetworkPolicy peer " +
				"and a credential -- rendered by nothing until this row existed"},
		{Set: "mcp.enabled=true", Refuses: "mcp.existingSecret is required",
			Why: "without a token the listener does not start, and the only symptom is one " +
				"WARNING in a pod log beside a Service that publishes the port anyway"},
	}},
	"mcp.dangerouslyServeWithoutAuthentication": {Positions: []position{
		{Set: "mcp.dangerouslyServeWithoutAuthentication=true",
			// Deliberately no existingSecret: rendering the surface with no
			// credential at all is the whole of what this hatch does.
			With: []string{"mcp.enabled=true", "mcp.allowFrom[0].namespace=gateway-system"},
			Why: "the escape hatch is the one way to render the surface with no credential, " +
				"and a hatch nothing renders is a hatch nobody has checked still opens"},
	}},

	"web.httpRoute.enabled": {Positions: []position{{Set: "web.httpRoute.enabled=false"}}},
	"web.ingress.enabled":   {Positions: []position{{Set: "web.ingress.enabled=false"}}},

	// The chart's enum is wider than the binary's switch: config.go implements
	// github and gitea and refuses the other two by name at start-up. That is
	// a contract worth knowing about and it is not this test's to assert --
	// TestARenderedDeploymentStartsTheBinary in config_chart_test.go is where
	// a render that cannot start is a failure. Here they only have to render.
	"git.provider": {Positions: []position{
		{Set: "git.provider=gitea", With: []string{"git.apiBase=https://gitea.example.com"}},
		{Set: "git.provider=gitlab"},
		{Set: "git.provider=bitbucket"},
	}},

	"git.insecureSkipTLSVerify": {Positions: []position{{Set: "git.insecureSkipTLSVerify=true"}}},

	"llm.provider": {Positions: []position{
		{Set: "llm.provider=anthropic", Why: "anthropic is the branch that needs no baseURL, which the openai branch requires by name"},
	}},

	"gate.forkPRs": {Positions: []position{{Set: "gate.forkPRs=true"}}},

	// Tri-state, and the position that matters is the one that is NOT set at
	// all. TestTheTriStateSettingsAreAbsentWhenUnset owns that half; these two
	// rows only prove both explicit settings render.
	"gate.validate.enabled": {Positions: []position{
		{Set: "gate.validate.enabled=true"},
		{Set: "gate.validate.enabled=false"},
	}},
	"gate.validate.ignoreMissingSchemas": {Positions: []position{
		{Set: "gate.validate.ignoreMissingSchemas=true"},
		{Set: "gate.validate.ignoreMissingSchemas=false"},
	}},

	"gate.argocd.insecureSkipTLSVerify": {Positions: []position{{Set: "gate.argocd.insecureSkipTLSVerify=true"}}},

	"triage.explainGreen":           {Positions: []position{{Set: "triage.explainGreen=false"}}},
	"triage.migrateDroppedVersions": {Positions: []position{{Set: "triage.migrateDroppedVersions=false"}}},
	"triage.upstreamNotes.enabled":  {Positions: []position{{Set: "triage.upstreamNotes.enabled=false"}}},
	"triage.structuralMigration":    {Positions: []position{{Set: "triage.structuralMigration=false"}}},

	"serviceAccount.create": {Positions: []position{
		{Set: "serviceAccount.create=false", With: []string{"serviceAccount.name=preexisting"}},
	}},
	"rbac.create": {Positions: []position{{Set: "rbac.create=false"}}},

	"networkPolicy.enabled": {Positions: []position{{Set: "networkPolicy.enabled=false"}}},
	"networkPolicy.flavor": {Positions: []position{
		{Set: "networkPolicy.flavor=cilium",
			Why: "the whole CiliumNetworkPolicy branch, rendered by nothing in this repository " +
				"until this table existed -- the 0.25.0 ClusterRole's exact situation"},
	}},
	"networkPolicy.egress.allowPublicHTTPS": {Positions: []position{{Set: "networkPolicy.egress.allowPublicHTTPS=true"}}},
	"networkPolicy.egress.allowInternet":    {Positions: []position{{Set: "networkPolicy.egress.allowInternet=true"}}},

	"metrics.serviceMonitor.enabled": {Positions: []position{
		{Set: "metrics.serviceMonitor.enabled=true", With: []string{"metrics.serviceMonitor.namespace=monitoring"}},
		{Set: "metrics.serviceMonitor.enabled=true", Refuses: "metrics.serviceMonitor.namespace is required",
			Why: "without the namespace the scrape rule admits any pod labelled prometheus, in any namespace, " +
				"which is wider than the rule it sits beside and reads exactly like it is not"},
	}},

	"liveReads.enabled": {Positions: []position{
		{Set: "liveReads.enabled=true",
			Why: "0.24.0 and 0.25.0 both shipped a ClusterRole that was not YAML in exactly this position"},
	}},
	"liveReads.scope": {Positions: []position{
		{Set: "liveReads.scope=wide", With: []string{"liveReads.enabled=true"}},
		{Set: "liveReads.scope=groups", With: []string{"liveReads.enabled=true"}},
	}},

	"supervise.enabled": {Positions: []position{{Set: "supervise.enabled=false"}}},
}

// The completeness half: this is the test that answers the 0.25.0 ClusterRole.
// A switch that exists and is rendered by nothing fails HERE, with the line to
// add, rather than in somebody's cluster two releases later.
func TestEverySwitchTheSchemaDeclaresIsRendered(t *testing.T) {
	declared := switchesOf(t, "bosun")

	for _, s := range declared {
		row, ok := matrix[s.Path]
		if !ok {
			t.Errorf("values.schema.json declares the switch %q (%s) and nothing renders it.\n"+
				"Add a row to matrix in this file:\n\n"+
				"    %q: {Positions: []position{{Set: %q, Why: \"...\"}}},\n\n"+
				"or, if it is genuinely rendered by nothing, say so and say why:\n\n"+
				"    %q: {Unrendered: \"...\"},\n\n"+
				"A default-off feature that no render reaches is the 0.25.0 ClusterRole again: "+
				"helm lint, the values schema and the ci values were all green on a document that "+
				"was not YAML, because all three rendered the feature off.",
				s.Path, s.Kind, s.Path, s.Path+"="+s.Opposite, s.Path)
			continue
		}
		if row.Unrendered == "" && len(row.Positions) == 0 {
			t.Errorf("matrix names %q with no positions and no stated reason.", s.Path)
		}
	}

	known := map[string]bool{}
	for _, s := range declared {
		known[s.Path] = true
	}
	for path := range matrix {
		if !known[path] {
			t.Errorf("matrix names %q, which values.schema.json no longer declares.\n"+
				"Delete the row: a render of a value the chart has dropped proves nothing.", path)
		}
	}

	// The self-check. If the schema is ever restructured -- $defs, a $ref, a
	// oneOf -- this walk finds nothing, both loops above run zero times, and
	// the test reports two empty sets as agreement.
	if len(declared) < 20 {
		t.Fatalf("found only %d switches in charts/bosun/values.schema.json; the schema's "+
			"shape has changed and switchesOf no longer sees it. Fix the walk rather than "+
			"lowering this number.", len(declared))
	}
}

// Every position renders, or is refused with the message it promises.
func TestEverySwitchPositionRenders(t *testing.T) {
	paths := make([]string, 0, len(matrix))
	for p := range matrix {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		row := matrix[path]
		for i, p := range row.Positions {
			name := p.Set
			if p.Refuses != "" {
				name += "/refused"
			}
			t.Run(fmt.Sprintf("%s#%d/%s", path, i, name), func(t *testing.T) {
				t.Parallel()
				opts := []helmtest.Option{helmtest.Values("ci/lint-values.yaml"), helmtest.Set(p.Set)}
				if len(p.With) > 0 {
					opts = append(opts, helmtest.Set(p.With...))
				}
				if p.Refuses != "" {
					msg := helmtest.RenderErr(t, "bosun", opts...)
					if !strings.Contains(msg, p.Refuses) {
						t.Fatalf("the chart refused %s, but not with the message this row names.\n"+
							"  wanted to contain: %s\n  got: %s\n\n"+
							"Either the guard in charts/bosun/templates/_helpers.tpl now refuses this "+
							"for a different reason, or a different guard fired first -- and a row that "+
							"passes on the wrong refusal is a row that has stopped testing its own case.",
							p.Set, p.Refuses, strings.TrimSpace(msg))
					}
					return
				}
				if docs := helmtest.Render(t, "bosun", opts...); len(docs) == 0 {
					t.Fatalf("%s rendered nothing at all", p.Set)
				}
			})
		}
	}
}

// Every feature on at once, and every feature off at once.
//
// One switch at a time cannot reach a template that only breaks when two
// features are on together, and an adopter's real values file is never a single
// flip away from the defaults -- it was a real consumer's real values, with
// several features on, that found the 0.25.0 ClusterRole.
//
// Both directions are rendered because they fail differently. All-on is the
// one that reaches the default-off branches, which is where the incidents have
// been. All-off is the cheap opposite: a chart whose every optional piece is
// disabled must still render a Deployment that runs, and a template that
// renders only because some other feature happens to be on fails here.
func TestEveryFeatureOnAtOnce(t *testing.T) {
	docs := renderAll(t, true)
	if len(helmtest.Of(docs, "Deployment")) != 1 {
		t.Fatalf("all features on rendered %d Deployments", len(helmtest.Of(docs, "Deployment")))
	}
	t.Logf("all features on: %d documents", len(docs))
}

func TestEveryFeatureOffAtOnce(t *testing.T) {
	docs := renderAll(t, false)
	if len(helmtest.Of(docs, "Deployment")) != 1 {
		t.Fatalf("all features off rendered %d Deployments", len(helmtest.Of(docs, "Deployment")))
	}
	t.Logf("all features off: %d documents", len(docs))
}

// renderAll sets every switch the schema declares to one end of its range.
//
// The settings come from the schema, not from the matrix, so this pair cannot
// drift from the switch list either. What it does take from the matrix is the
// companion values -- a namespace, a pre-existing ServiceAccount name -- which
// the chart requires by name in some positions and which are not themselves
// switches.
func renderAll(t *testing.T, on bool) []helmtest.Doc {
	t.Helper()
	declared := switchesOf(t, "bosun")
	isSwitch := map[string]bool{}
	for _, s := range declared {
		isSwitch[s.Path] = true
	}

	var sets []string
	for _, s := range declared {
		if members := enumMembers(t, s); members != nil {
			if on {
				sets = append(sets, s.Path+"="+members[len(members)-1])
			} else {
				sets = append(sets, s.Path+"="+members[0])
			}
			continue
		}
		sets = append(sets, fmt.Sprintf("%s=%t", s.Path, on))
	}

	// Companion values, which are the ones the chart refuses to render
	// without. A companion that is itself a switch is skipped: every switch
	// already has an explicit setting above, and a companion overriding one
	// would quietly take this render off the position it claims to be at.
	for _, path := range sortedKeys(matrix) {
		for _, p := range matrix[path].Positions {
			if p.Refuses != "" {
				continue
			}
			for _, w := range p.With {
				if !isSwitch[strings.SplitN(w, "=", 2)[0]] {
					sets = append(sets, w)
				}
			}
		}
	}

	return helmtest.Render(t, "bosun",
		helmtest.Values("ci/lint-values.yaml"), helmtest.Set(sets...))
}

// enumMembers returns an enum switch's members, or nil for a boolean.
func enumMembers(t *testing.T, s aSwitch) []string {
	t.Helper()
	if !strings.HasPrefix(s.Kind, "enum ") {
		return nil
	}
	trimmed := strings.Trim(strings.TrimPrefix(s.Kind, "enum "), "[]")
	return strings.Fields(trimmed)
}

// A NetworkPolicy matches the destination port of the packet, and a ClusterIP
// is DNAT'd to the backend pod's port BEFORE policy is evaluated -- so this
// rule has to name argocd-server's container port, not the Service port that
// appears in gate.argocd.baseURL.
//
// `gate.argocd.port`, added in 0.20.0, defaulted to 443 and read as though the
// port belonged to baseURL. Setting the two consistently is what the comment
// invited: it renders clean, passes helm lint, passes the values schema, and
// then drops every packet. Nothing errors at either end -- the pod blocks its
// full HTTP timeout and dies saying argocd-server is unreachable, which is true
// and says nothing about why.
//
// Ported from hack/portability-test.sh, where it was an awk state machine over
// the rendered text. Asserting on the parsed document is what makes the
// obvious-looking simplification -- deriving the port from baseURL -- fail on
// the value rather than on a line match.
func TestTheArgoCDEgressRuleNamesAPodPort(t *testing.T) {
	declared := podPortDefault(t)

	for _, baseURL := range []string{"http://argocd-server.argocd.svc", "https://argocd-server.argocd.svc"} {
		got := argocdEgressPort(t, baseURL)
		if got != declared {
			t.Errorf("with baseURL %s the ArgoCD egress rule opened port %d, not gate.argocd.podPort %d.\n"+
				"The rule must name argocd-server's container port and must not move with the URL: "+
				"a ClusterIP is DNAT'd to the pod's port before the policy is evaluated, so a rule "+
				"naming the Service port drops every packet and logs nothing at either end.",
				baseURL, got, declared)
		}
	}

	// The default itself, separately. The comparison above stays true if
	// somebody sets the default back to a Service port, because it compares
	// the render to the default rather than to reality. 80 and 443 are the two
	// numbers that cannot be a container port here: they are what the
	// argocd-server Service publishes, and the packet has been DNAT'd past them.
	if declared == 80 || declared == 443 {
		t.Errorf("gate.argocd.podPort defaults to %d, which is a Service port -- the rule needs the pod's", declared)
	}
}

// argocdEgressPort finds the egress rule that opens the way to argocd-server
// and returns the port it opened.
//
// The rule is pinned by the namespace it targets, set here to a value nothing
// else in the chart uses. The bash this replaces matched the line
// `metadata.name: argocd`, which is the namespace selector's LABEL and would
// match any rule aimed at a namespace happening to be called argocd -- so
// naming the namespace makes the assertion about this rule rather than about
// whichever rule sorted first.
const argocdProbeNamespace = "argocd-egress-probe"

func argocdEgressPort(t *testing.T, baseURL string) int {
	t.Helper()
	docs := helmtest.Render(t, "bosun",
		helmtest.Values("ci/lint-values.yaml"),
		helmtest.ShowOnly("templates/networkpolicy.yaml"),
		helmtest.Set("liveReads.argocdNamespace="+argocdProbeNamespace),
		helmtest.Set("gate.argocd.baseURL="+baseURL))

	for _, d := range docs {
		egress, _ := dig(d.Body, "spec", "egress").([]any)
		for _, rule := range egress {
			r, _ := rule.(map[string]any)
			if !targetsNamespace(r, argocdProbeNamespace) {
				continue
			}
			ports, _ := r["ports"].([]any)
			for _, p := range ports {
				if port, ok := p.(map[string]any)["port"]; ok {
					return toInt(t, port)
				}
			}
			t.Fatalf("the ArgoCD egress rule opened no port at all for %s.\n"+
				"A rule with no port opens every port in that namespace, which is wider "+
				"than the rule reads.", baseURL)
		}
	}
	t.Fatalf("no egress rule targets the ArgoCD namespace in the render for %s.\n"+
		"charts/bosun/templates/networkpolicy.yaml must emit one: without it the gate "+
		"cannot reach argocd-server, and the pod dies at start-up saying so.", baseURL)
	return 0
}

// targetsNamespace reports whether an egress rule is aimed at one namespace by
// the well-known label every namespace carries.
func targetsNamespace(rule map[string]any, ns string) bool {
	to, _ := rule["to"].([]any)
	for _, peer := range to {
		labels, _ := dig(mapOf(peer), "namespaceSelector", "matchLabels").(map[string]any)
		if v, _ := labels["kubernetes.io/metadata.name"].(string); v == ns {
			return true
		}
	}
	return false
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// dig walks a path of map keys, returning nil at the first one that is absent
// or not a map.
func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func podPortDefault(t *testing.T) int {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(helmtest.Dir(t, "bosun"), "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "podPort:") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(trimmed, "podPort:")), "%d", &n); err == nil {
			return n
		}
	}
	t.Fatal("charts/bosun/values.yaml declares no gate.argocd.podPort")
	return 0
}

func toInt(t *testing.T, v any) int {
	t.Helper()
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case string:
		var out int
		if _, err := fmt.Sscanf(n, "%d", &out); err == nil {
			return out
		}
	}
	t.Fatalf("a port rendered as %T (%v), which is neither a number nor a numeric string", v, v)
	return 0
}

// aSwitch is one value that selects a code path in a template.
type aSwitch struct {
	Path string
	Kind string
	// Opposite is a position that is not the default, used only to write a
	// useful failure message.
	Opposite string
}

// switchesOf reads the switches out of a chart's own values.schema.json.
//
// A boolean is a switch. A nullable boolean is a switch with three positions.
// An enum is a switch with as many positions as it has members. Anything else
// is a value, and a value that selects no code path needs no render of its own.
func switchesOf(t *testing.T, chart string) []aSwitch {
	t.Helper()
	schema := readSchema(t, filepath.Join(helmtest.Dir(t, chart), "values.schema.json"))

	var out []aSwitch
	var walk func(node map[string]any, path string)
	walk = func(node map[string]any, path string) {
		if path != "" {
			switch {
			case hasType(node, "boolean"):
				out = append(out, aSwitch{Path: path, Kind: typeOf(node), Opposite: "true"})
			case node["enum"] != nil:
				members, _ := node["enum"].([]any)
				opposite := ""
				if len(members) > 0 {
					opposite = fmt.Sprint(members[len(members)-1])
				}
				out = append(out, aSwitch{Path: path, Kind: fmt.Sprintf("enum %v", members), Opposite: opposite})
			}
		}
		props, _ := node["properties"].(map[string]any)
		for _, k := range sortedKeys(props) {
			child, ok := props[k].(map[string]any)
			if !ok {
				continue
			}
			next := k
			if path != "" {
				next = path + "." + k
			}
			walk(child, next)
		}
	}
	walk(schema, "")
	return out
}

func hasType(node map[string]any, want string) bool {
	switch t := node["type"].(type) {
	case string:
		return t == want
	case []any:
		for _, e := range t {
			if s, _ := e.(string); s == want {
				return true
			}
		}
	}
	return false
}

func typeOf(node map[string]any) string {
	b, _ := json.Marshal(node["type"])
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
