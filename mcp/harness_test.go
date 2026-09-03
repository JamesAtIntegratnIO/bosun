package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
	"github.com/JamesAtIntegratnIO/bosun/supervisor"
)

// The seam every behavioural test in this package uses: the real handler,
// behind a real HTTP server, answering real JSON-RPC.
//
// One seam and not several, because every requirement this surface has is
// observable as a response body or a status code. A test that reached into an
// unexported field or asserted on call order would be asserting about how a
// handler got its answer, which is not what a client depends on and is what
// breaks the first time the sweep is refactored.

const testToken = "sekrit"

// sweptAt is the moment every fixture sweep happened, and requestedAt is when
// the caller asks. Fixed, so an age is a number a test asserts rather than a
// window it has to be tolerant of.
var (
	sweptAt     = time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	requestedAt = sweptAt.Add(90 * time.Second)
	// observedAt is when the fleet's live reading was made. Deliberately
	// EARLIER than the sweep: the reading happens on a gate run, and a sweep
	// with nothing to render makes none, so a fixture where the two are equal
	// would never exercise the two clocks a caller has to tell apart.
	observedAt = sweptAt.Add(-5 * time.Minute)
)

// fixture is a server under test and the world behind it.
type fixture struct {
	srv    *Server
	http   *httptest.Server
	logged []string
	// gate is what the gate's last sweep saw. The zero value is the
	// before-the-first-sweep case; withGate replaces it, and the server reads
	// it per request rather than at wiring time, which is what a real
	// composition root does too.
	gate GateStatus
	// triage is what the agent is doing right now. The zero value is an agent
	// working nothing, which is the common case and the one a caller asking
	// about a pull request nobody is triaging has to be answered from.
	triage TriageStatus
}

// newFixture builds the server. report is what the supervisor holds; nil is
// the before-the-first-sweep case, which is the one this surface is most
// careful about.
func newFixture(t *testing.T, report *pipeline.Report) *fixture {
	t.Helper()
	f := &fixture{}
	f.srv = &Server{
		Repository: "example/platform",
		Report:     func() *pipeline.Report { return report },
		Gate:       func() GateStatus { return f.gate },
		Triage:     func() TriageStatus { return f.triage },
		Auth:       BearerToken{Token: testToken},
		Version:    "0.31.0",
		Log:        func(format string, a ...any) { f.logged = append(f.logged, fmt.Sprintf(format, a...)) },
		Now:        func() time.Time { return requestedAt },
	}
	h, err := f.srv.Handler()
	if err != nil {
		t.Fatalf("the handler would not build: %v", err)
	}
	f.http = httptest.NewServer(h)
	t.Cleanup(f.http.Close)
	return f
}

// post sends one JSON-RPC call with the token and returns the raw response.
func (f *fixture) post(t *testing.T, body string) (int, []byte) {
	t.Helper()
	return f.postWith(t, body, "Bearer "+testToken)
}

func (f *fixture) postWith(t *testing.T, body, authorization string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, f.http.URL+EndpointPath, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

// withGate installs what the gate's last sweep saw and returns the fixture,
// so a test can say what it is about in one expression.
func (f *fixture) withGate(g GateStatus) *fixture {
	f.gate = g
	return f
}

// withTriage installs what the agent is doing and returns the fixture.
func (f *fixture) withTriage(tr TriageStatus) *fixture {
	f.triage = tr
	return f
}

// call runs one tool with no arguments.
func (f *fixture) call(t *testing.T, tool string) json.RawMessage {
	t.Helper()
	return f.callWith(t, tool, `{}`)
}

// callWith runs one tool and returns its structuredContent as raw JSON, which
// is the value a client actually reads.
func (f *fixture) callWith(t *testing.T, tool, args string) json.RawMessage {
	t.Helper()
	code, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
		`"params":{"name":"`+tool+`","arguments":`+args+`}}`)
	if code != http.StatusOK {
		t.Fatalf("tools/call answered %d: %s", code, body)
	}
	var resp struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("the response is not JSON-RPC: %v\n%s", err, body)
	}
	if resp.Error != nil {
		t.Fatalf("tools/call returned an error: %s", resp.Error.Message)
	}
	if len(resp.Result.StructuredContent) == 0 {
		t.Fatalf("tools/call returned no structuredContent:\n%s", body)
	}
	return resp.Result.StructuredContent
}

// report decodes a pipeline_report result.
func (f *fixture) report(t *testing.T) Report {
	t.Helper()
	var out Report
	if err := json.Unmarshal(f.call(t, "pipeline_report"), &out); err != nil {
		t.Fatalf("the result does not decode as a Report: %v", err)
	}
	return out
}

// verdict decodes a gate_verdict result for one pull request.
func (f *fixture) verdict(t *testing.T, number int) Verdict {
	t.Helper()
	var out Verdict
	raw := f.callWith(t, "gate_verdict", fmt.Sprintf(`{"pullRequest":%d}`, number))
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the result does not decode as a Verdict: %v", err)
	}
	return out
}

// queue decodes a gate_status result.
func (f *fixture) queue(t *testing.T) Queue {
	t.Helper()
	var out Queue
	if err := json.Unmarshal(f.call(t, "gate_status"), &out); err != nil {
		t.Fatalf("the result does not decode as a Queue: %v", err)
	}
	return out
}

// handoff decodes a handoff_queue result.
func (f *fixture) handoff(t *testing.T) Handoff {
	t.Helper()
	var out Handoff
	if err := json.Unmarshal(f.call(t, "handoff_queue"), &out); err != nil {
		t.Fatalf("the result does not decode as a Handoff: %v", err)
	}
	return out
}

// triageStatus decodes a triage_status result for one pull request.
func (f *fixture) triageStatus(t *testing.T, number int) Triage {
	t.Helper()
	var out Triage
	raw := f.callWith(t, "triage_status", fmt.Sprintf(`{"pullRequest":%d}`, number))
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the result does not decode as a Triage: %v", err)
	}
	return out
}

// inventory decodes an inventory result.
func (f *fixture) inventory(t *testing.T) Fleet {
	t.Helper()
	var out Fleet
	if err := json.Unmarshal(f.call(t, "inventory"), &out); err != nil {
		t.Fatalf("the result does not decode as a Fleet: %v", err)
	}
	return out
}

// fields decodes a result as a bare map, for the assertions that are about a
// key being ABSENT rather than about a Go zero value. Unmarshalling into the
// struct cannot tell `"findings": []` from no findings key at all, which is
// the exact distinction this surface exists to preserve.
func fields(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the result is not a JSON object: %v\n%s", err, raw)
	}
	return out
}

// sweep runs a real supervisor sweep over a fake cluster and the existing
// pull-request fake, and returns what it found.
//
// The real supervisor rather than a hand-built Report: the thing under test is
// what a caller sees, and a fixture that assembled a Report itself would agree
// with this package's mapping while proving nothing about the sweep that feeds
// it.
func sweep(t *testing.T, w *world) *pipeline.Report {
	t.Helper()
	sup := &supervisor.Supervisor{
		Collector: &pipeline.Collector{
			Kargo: w.kargo, PRs: w.prs,
			Now: func() time.Time { return sweptAt },
		},
		// Long enough that exactly one sweep runs before the context is
		// cancelled; Run sweeps immediately and then waits out the interval.
		Every: time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); sup.Run(ctx) }()

	deadline := time.After(10 * time.Second)
	for sup.Report() == nil {
		select {
		case <-deadline:
			cancel()
			t.Fatal("no sweep completed in ten seconds")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	return sup.Report()
}

// world is what one sweep reads: a cluster and a git host, both fake.
//
// One value rather than two arguments, so a fixture builder can hand a whole
// situation to sweep in a single expression and a test that wants to bend one
// object can say which.
type world struct {
	kargo *fakeKargo
	// prs is the existing pull-request fake with a call counter wrapped round
	// it. Always counted rather than counted only where a test asks, because
	// "serving a request costs no git-host call" is a property of every path
	// through this surface and not of one test.
	prs *countingPRs
}

// fakeKargo is the cluster half of a sweep.
//
// A local fake rather than cluster.Fake, which answers the gate's questions
// and not these. Every Err is how a test reaches the path where the sweep
// could not look, which is the case this surface must never render as
// "nothing is wrong".
type fakeKargo struct {
	stages     []cluster.KargoStage
	warehouses []cluster.KargoWarehouse
	promotions []cluster.KargoPromotion

	stageErr     error
	warehouseErr error
	promotionErr error

	// calls counts every read, so a test can assert a tool call reached none
	// of them.
	calls int
}

func (f *fakeKargo) Stages(context.Context) ([]cluster.KargoStage, error) {
	f.calls++
	return f.stages, f.stageErr
}

func (f *fakeKargo) Warehouses(context.Context) ([]cluster.KargoWarehouse, error) {
	f.calls++
	return f.warehouses, f.warehouseErr
}

func (f *fakeKargo) Promotions(context.Context) ([]cluster.KargoPromotion, error) {
	f.calls++
	return f.promotions, f.promotionErr
}

// countingPRs is the pull-request fake with a counter on it, for the test that
// asserts serving a request costs no git-host call.
type countingPRs struct {
	*gitprovider.Fake
	calls int
}

func (c *countingPRs) ListOpenPullRequests(ctx context.Context) ([]gitprovider.PullRequest, error) {
	c.calls++
	return c.Fake.ListOpenPullRequests(ctx)
}

// wedged is the world this package was written for: a Stage whose last
// promotion ended without delivering, three days ago, and which will not try
// again on its own. It is the situation that cost four addons three days of
// updates while every Application stayed Synced and Healthy.
func wedged() *world {
	return &world{kargo: &fakeKargo{
		stages: []cluster.KargoStage{
			{Name: "external-secrets", Namespace: "addons", Ready: true},
			{Name: "argo-cd", Namespace: "addons", CurrentFreight: "f-ok", Ready: true},
		},
		warehouses: []cluster.KargoWarehouse{
			{Name: "charts", Namespace: "addons", Ready: true,
				Interval: time.Hour, DiscoveredAt: sweptAt.Add(-10 * time.Minute)},
		},
		promotions: []cluster.KargoPromotion{{
			Name: "external-secrets.01abc.f08f1c9", Namespace: "addons",
			Stage: "external-secrets", Freight: "f08f1c9", Phase: pipeline.PhaseErrored,
			CreatedAt: sweptAt.Add(-72 * time.Hour), StartedAt: sweptAt.Add(-72 * time.Hour),
			Message: `step "step-8": lookup api.github.com: server misbehaving`,
		}},
	}, prs: &countingPRs{Fake: &gitprovider.Fake{
		OpenPRs: []gitprovider.PullRequest{{Number: 41, Title: "chore(deps): bump argo-cd",
			Branch: "kargo/promotion/argo-cd.01j9x.a1b2c3"}},
	}}}
}

// healthy is a sweep that examined a fleet and found nothing wrong.
func healthy() *world {
	return &world{kargo: &fakeKargo{
		stages: []cluster.KargoStage{
			{Name: "external-secrets", Namespace: "addons", CurrentFreight: "f-ok", Ready: true},
		},
		warehouses: []cluster.KargoWarehouse{
			{Name: "charts", Namespace: "addons", Ready: true,
				Interval: time.Hour, DiscoveredAt: sweptAt.Add(-10 * time.Minute)},
		},
	}, prs: &countingPRs{Fake: &gitprovider.Fake{}}}
}

// errRefused is what a missing ClusterRole grant looks like to a sweep: a read
// that is refused rather than one that answers nothing.
var errRefused = errors.New("stages.kargo.akuity.io is forbidden")

// The gate's half of the world, in the shapes the composition root hands over.
//
// Written out rather than produced by a gate run, and that is the seam rather
// than a shortcut: a real run shells helm against a chart repository, and
// gate's own tests are where the render is under test. What is under test here
// is what a caller receives, so the input is the value the adapter produces
// and the adapter itself is checked in the root package, where gate and this
// package are both visible at once.
//
// blockedHead is the commit every blocked fixture is about. Fixed, so a test
// asserts a stamp rather than tolerating one.
const (
	blockedHead = "9f2c1a4b8e6d0c3f5a7b9d1e3f5a7b9d1e3f5a7b"
	greenHead   = "3c1d5e7f9a1b3c5d7e9f1a3b5c7d9e1f3a5b7c9d"
)

// blocked is the verdict this tool was argued for: a bump that will not render
// on one Application, drops a served API version four manifests still declare,
// moves an object's own apiVersion, stops reading two settings, and renders one
// manifest the target schemas reject.
func blocked() GateStatus {
	return GateStatus{
		SweptAt: sweptAt,
		Held:    2, Running: 1,
		Open: []GatePR{{
			Number: 264, Title: "chore(deps): bump external-secrets to 0.10.0",
			URL:     "https://example.invalid/example/platform/pull/264",
			HeadSHA: blockedHead, State: StateFailing,
			Labels: []string{"dependencies", "bosun/attempt-1"},
			Verdict: &GateVerdict{
				Blocking: true,
				Headline: "Blocking — 1 Application whose chart will not render at the new version, " +
					"1 object whose own apiVersion moved, 4 manifests still declaring a dropped API " +
					"version, 2 settings this bump stops reading and 1 manifest the target schemas reject",
				Blockers: GateBlockers{
					APIVersion: 1, Consumers: 4, Unrenderable: 1, ValuesDropped: 2, Schema: 1,
				},
				Findings: []GateFinding{{
					Kind: "unrenderable", Count: 1, Blocking: true, RepositorySideRemedy: true,
					Subject: "authentik", Cluster: "prod-eu", From: "2024.2.0", To: "2024.6.0",
					Detail: "the chart will not render at the version this change moves it to, " +
						"so there is nothing to diff and nothing that will sync",
					Reason: "execution error at (authentik/templates/server.yaml:12): " +
						".Values.postgresql.host is required",
				}, {
					Kind: "droppedVersion", Count: 4, Blocking: true, RepositorySideRemedy: true,
					Subject: "CustomResourceDefinition/externalsecrets.external-secrets.io in external-secrets",
					Cluster: "prod-eu", From: "v1beta1", To: "v1",
					Detail: "1 version no longer served; 4 manifests in this repository still " +
						"declare a dropped version",
					ConsumersScanned: true,
					ConsumerFiles: []string{
						"addons/argo-cd/externalsecret.yaml", "addons/grafana/externalsecret.yaml",
						"addons/harbor/externalsecret.yaml", "addons/vault/externalsecret.yaml",
					},
					Dropped: &GateDropped{
						Definition:   "externalsecrets.external-secrets.io",
						Group:        "external-secrets.io",
						ConsumerKind: "ExternalSecret",
						Versions:     []string{"v1beta1"},
						Surviving:    "v1",
					},
				}, {
					Kind: "apiVersion", Count: 1, Blocking: true, RepositorySideRemedy: false,
					Subject: "Ingress/authentik in authentik", Cluster: "prod-eu",
					From: "networking.k8s.io/v1beta1", To: "networking.k8s.io/v1",
					Detail: "this object's own apiVersion moved, which renders cleanly and can " +
						"break at apply",
				}, {
					Kind: "valuesDropped", Count: 2, Blocking: true, RepositorySideRemedy: true,
					Subject: "grafana", Cluster: "prod-eu", From: "7.3.0", To: "8.0.0",
					Keys: []string{"grafana.ini.auth.oauth_auto_login", "sidecar.dashboards.searchNamespace"},
					Detail: "2 settings set here that the new chart version no longer declares; " +
						"helm ignores an unknown value rather than failing on it, so they stop " +
						"applying while the render stays green",
				}, {
					Kind: "schema", Count: 1, Blocking: true, RepositorySideRemedy: false,
					Subject: "Deployment/harbor-core", Source: "addons/harbor on prod-eu",
					Detail: "the target cluster's schemas reject this manifest",
					Reason: "problem validating schema. Check JSON formatting: " +
						"spec.replicas: got string, want integer",
				}},
				NotCovered: []string{
					"kube-prometheus-stack: kube-prometheus-stack renders at 62.0.0 but not at " +
						"61.3.2, so its resource changes are NOT covered: exit status 1",
				},
				BaseRev: "1a2b3c4d", HeadRev: "9f2c1a4b",
			},
		}},
	}
}

// handedOver puts the agent's escalation label on every open pull request in a
// world, so a fixture built for another tool can be asked of this one.
//
// The label is spelled out rather than taken from the constant the handler
// selects on, because a fixture built from that constant agrees with it by
// construction: the two would still match after a rename that made the surface
// select on a label nothing writes. mcp_handoff_test.go in the repository root
// is the other half, and it takes the label from the agent's own source.
func handedOver(g GateStatus) GateStatus {
	for i := range g.Open {
		g.Open[i].Labels = append(g.Open[i].Labels, "needs-human")
	}
	return g
}

// escalated is the world handoff_queue was written for: the blocked pull
// request, with the agent's escalation label standing on it, beside a green
// one that nobody is waiting on.
//
// Two pull requests and one label, because a queue that returned both would be
// gate_status under another name and every assertion about selection would
// pass against it.
func escalated() GateStatus {
	g := handedOver(blocked())
	g.Open = append(g.Open, green().Open...)
	return g
}

// green is a pull request the gate ran and found nothing wrong with. The
// findings list is EMPTY rather than absent, and that is the whole difference
// between this and every other fixture here.
func green() GateStatus {
	return GateStatus{
		SweptAt: sweptAt,
		Held:    1,
		Open: []GatePR{{
			Number: 41, Title: "chore(deps): bump argo-cd to 7.7.0",
			URL:     "https://example.invalid/example/platform/pull/41",
			HeadSHA: greenHead, State: StatePassing,
			Verdict: &GateVerdict{
				Headline: "No blocking findings — 1 version changed, nothing else",
				BaseRev:  "1a2b3c4d", HeadRev: "3c1d5e7f",
			},
		}},
	}
}

// fleet is a blocked pull request and a live reading beside it.
//
// The reading is the subject; the pull request is there because a sweep that
// has produced neither is a different fixture, and every inventory assertion
// wants a swept install underneath it.
func fleet() GateStatus { return withFleet(blocked()) }

// withFleet hangs a live reading on whatever situation a test is already
// about: three Applications on two clusters, read five minutes before the
// sweep that publishes them.
//
// Composable rather than a second whole fixture, because the reading is
// orthogonal to what the pull requests are doing -- a test about escalation
// that also wants rows should not have to choose between two worlds.
//
// The rows are deliberately not one per cluster and not one per repository.
// They are every Application this install's ArgoCD credentials can see, which
// is what makes the third -- an Application of a repository this install does
// not gate -- the interesting one rather than a filler.
func withFleet(g GateStatus) GateStatus {
	g.Fleet = &GateFleet{
		ObservedAt: observedAt,
		Apps: []GateFleetApp{
			{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"},
			{Name: "external-secrets", Namespace: "argocd", Cluster: "prod-eu"},
			{Name: "tenant-billing", Namespace: "argocd", Cluster: "prod-us"},
		},
	}
	return g
}

// history decodes a verdict_history result for one pull request.
func (f *fixture) history(t *testing.T, number int) History {
	t.Helper()
	var out History
	raw := f.callWith(t, "verdict_history", fmt.Sprintf(`{"pullRequest":%d}`, number))
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("the result does not decode as a History: %v", err)
	}
	return out
}

// toolCall is one registered tool and arguments it accepts.
type toolCall struct {
	name string
	args string
}

// everyToolCall is one call per registered tool.
//
// The subject is derived from the registry, so a tool added without a thought
// for the assertions below fails here rather than quietly escaping them: a
// listener's auth check, its audit line and its promise to reach nothing are
// properties of the surface, not of the tools somebody remembered to name.
//
// The arguments are the part that cannot be derived -- a schema says a number
// is required and not which number this fixture's world holds -- so they are a
// table, and a table missing an entry is a failure rather than a tool skipped.
func everyToolCall(t *testing.T) []toolCall {
	t.Helper()
	args := map[string]string{
		"pipeline_report": `{}`,
		"gate_status":     `{}`,
		"gate_verdict":    `{"pullRequest":264}`,
		"triage_status":   `{"pullRequest":264}`,
		"handoff_queue":   `{}`,
		"verdict_history": `{"pullRequest":264}`,
		"inventory":       `{}`,
	}

	tools := Tools()
	if len(tools) == 0 {
		t.Fatal("this package registers no tools, so every assertion driven from here runs " +
			"against nothing and reads exactly like a pass")
	}
	out := make([]toolCall, 0, len(tools))
	for _, tool := range tools {
		a, ok := args[tool.Name]
		if !ok {
			t.Fatalf("the tool %q has no arguments in this table, so nothing here drives it. "+
				"Add them rather than removing the check: the auth table, the audit log and "+
				"the reaches-nothing assertion are all replayed from this list.", tool.Name)
		}
		out = append(out, toolCall{name: tool.Name, args: a})
		delete(args, tool.Name)
	}
	for name := range args {
		t.Errorf("this table names %q, which is not a registered tool; the assertions "+
			"driven from it are exercising a surface that no longer exists", name)
	}
	return out
}

// walkText visits every string in a decoded result, saying where it is and
// what a client would fence it by.
//
// One visitor rather than one per assertion, because the two questions asked
// of a string on this surface are the same two every time: which field is it
// in, and whose words does the result say they are. `key` is the enclosing
// field name -- carried down through `text` and `command`, so a string inside
// a Text or a Remedy is attributed to the field that holds it rather than to
// the word "text". `origin` is the sibling origin of that Text, and "" where
// there is none, which is what an untagged field looks like.
func walkText(tree any, visit func(path, key, origin, text string)) {
	var walk func(v any, path, key, origin string)
	walk = func(v any, path, key, origin string) {
		switch node := v.(type) {
		case string:
			visit(path, key, origin, node)
		case []any:
			for i := range node {
				walk(node[i], path+"[]", key, origin)
			}
		case map[string]any:
			here, _ := node["origin"].(string)
			for k, val := range node {
				// A Text and a Remedy both put their content under a fixed
				// key beside the origin. Everywhere else the key IS the
				// field, so it replaces what was carried in.
				next, tag := k, ""
				if k == "text" || k == "command" {
					next, tag = key, here
				}
				walk(val, path+"."+k, next, tag)
			}
		}
	}
	walk(tree, "", "", "")
}

// flapping is the world this tool was argued for: a pull request whose verdict
// has gone red, green and red again across three head commits.
//
// A flip in it rather than three of the same, because "the gate changed its
// mind" and "my push fixed it" are the two readings a caller is here to tell
// apart, and a history with no flip in it cannot tell them apart at all.
//
// Oldest first, which is the order the gate's own comment records and the
// order the snapshot carries; the wire's is the reverse, and that reversal is
// what TestTheHistoryIsNewestFirst reads.
func flapping() GateStatus {
	g := blocked()
	g.HistoryCap = 10
	rows := []GateVerdictRow{{
		SHA: "1f0e2d3c", Blocking: true,
		Headline: "Blocking — 4 manifests still declaring a dropped API version",
	}, {
		SHA: "2a4b6c8d", Blocking: false,
		Headline: "No blocking findings — 1 version changed, nothing else",
	}, {
		SHA: "3b5d7f9a", Blocking: true,
		Headline: "Blocking — 1 Application whose chart will not render at the new version",
	}}
	g.Open[0].History = &rows
	return g
}
