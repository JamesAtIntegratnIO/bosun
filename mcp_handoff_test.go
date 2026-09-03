package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/agent"
	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
	"github.com/JamesAtIntegratnIO/bosun/web"
)

// The label the agent writes when it gives up is the label the handoff queue
// hands back.
//
// A contract neither side can see, and the reason is the rule that makes the
// tool surface safe to publish: mcp imports the result types and the redactor
// and nothing else, so it cannot import agent, and the label it selects on is
// a literal of its own. Two literals, one meaning, no compiler between them.
//
// The failure of getting it wrong is silent and one-directional. A rename on
// either side leaves a queue that is always empty, on a tool whose empty
// answer means "nobody is waiting on you" -- so the symptom is an on-call
// agent that stops reporting handoffs, and the people in them wait for
// somebody who was told there was nobody.
//
// So the label comes from agent's own source rather than from a copy here, and
// the assertion is made over the real handler: what a client receives, from
// the label the agent actually applies.
func TestTheLabelTheAgentWritesIsTheOneTheHandoffQueueReturns(t *testing.T) {
	label := escalationLabel(t)

	// Two pull requests, one label. A queue that ignored the label would
	// return both and pass every assertion about the one it should return.
	gate := mcp.GateStatus{
		SweptAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Open: []mcp.GatePR{{
			Number: 264, Title: "chore(deps): bump external-secrets",
			HeadSHA: "9f2c1a4b", State: mcp.StateFailing,
			Labels: []string{"dependencies", "bosun/attempt-2", label},
		}, {
			Number: 41, Title: "chore(deps): bump argo-cd",
			HeadSHA: "3c1d5e7f", State: mcp.StatePassing,
			Labels: []string{"dependencies"},
		}},
	}

	var got struct {
		Waiting *[]struct {
			Number int `json:"number"`
		} `json:"waiting"`
	}
	body := handoffQueue(t, gate, mcp.TriageStatus{MaxAttempts: 2})
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if got.Waiting == nil {
		t.Fatalf("a sweep that listed publishes a queue, and this one saw two pull "+
			"requests:\n%s", body)
	}
	waiting := *got.Waiting
	if len(waiting) != 1 || waiting[0].Number != 264 {
		t.Fatalf("the pull request the agent labelled %q is not the one the queue hands "+
			"back, got %+v.\nThe agent labels and this surface selects, and neither package "+
			"can see the other: a rename on either side is a queue that is empty forever, on "+
			"the one tool whose empty answer somebody acts on by going home.", label, waiting)
	}
}

// The sentence the model gave for a handoff is the sentence the queue returns.
//
// The second half of the same contract the label test above covers, and the
// harder half. The label is a literal on both sides and a rename breaks it
// loudly here; this is a value that has to survive three hops -- the agent
// holds it, the composition root copies it across, the tool tags it -- and
// mcp cannot import agent to check any of them. A hop dropped is a queue that
// simply never carries a reason, which looks exactly like a queue where bosun
// stopped for reasons of its own.
//
// So the agent really escalates. A seeded map here would agree with a mapping
// written from the same assumption, and prove nothing about the sentence the
// agent actually keeps.
func TestTheReasonTheAgentHeldIsTheOneTheHandoffQueueReturns(t *testing.T) {
	const reason = "the chart drops the ClusterRole this repository binds, and no values change " +
		"can put it back"

	tr, git := escalatingAgent(t, reason)
	if err := tr.Run(context.Background(), agent.Promotion{
		Project: "platform", Stage: "prod", PRNumber: git.PR.Number, Branch: git.PR.Branch,
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(git.Labelled, escalationLabel(t)) {
		t.Fatalf("the agent did not hand this pull request over, so nothing below is about "+
			"a handoff; it applied %v", git.Labelled)
	}

	// The gate's snapshot as the sweep would leave it: the pull request open,
	// carrying the label the agent just applied.
	gate := mcp.GateStatus{
		SweptAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Open: []mcp.GatePR{{
			Number: git.PR.Number, Title: git.PR.Title, HeadSHA: git.PR.HeadSHA,
			State: mcp.StateFailing, Labels: git.PR.Labels,
		}},
	}

	var got struct {
		Waiting *[]struct {
			Number           int `json:"number"`
			EscalationReason *struct {
				Text   string `json:"text"`
				Origin string `json:"origin"`
			} `json:"escalationReason"`
		} `json:"waiting"`
	}
	body := handoffQueue(t, gate, mcpTriageStatus(web.TriageStatus{}, tr, gateStatusOf(gate)))
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if got.Waiting == nil || len(*got.Waiting) != 1 {
		t.Fatalf("the queue does not hold the pull request the agent handed over:\n%s", body)
	}
	entry := (*got.Waiting)[0]
	if entry.EscalationReason == nil {
		t.Fatalf("the queue carries no reason for a handoff the model asked for.\n"+
			"The agent kept the sentence, and it reaches a client through the composition "+
			"root: a hop dropped anywhere along it is a queue that is silent about every "+
			"handoff, which reads the same as one bosun stopped on its own.\n%s", body)
	}
	if entry.EscalationReason.Text != reason {
		t.Errorf("the queue carries %q, not the sentence the model gave",
			entry.EscalationReason.Text)
	}
	if entry.EscalationReason.Origin != "bosun-quoting-model" {
		t.Errorf("the reason is tagged %q rather than as the model's own words",
			entry.EscalationReason.Origin)
	}
}

// A real sweep releases what the agent is holding for a pull request it no
// longer sees.
//
// The other end of the same contract, and the one whose failure is invisible.
// The agent keeps the reason; the gate's sweep is the only thing in this
// process that learns a pull request was merged; and neither package may
// import the other, so the wiring lives in main.go and nothing but this can
// see both ends of it. Unwired, the agent keeps one sentence per escalation
// for the life of the pod and every test above still passes.
//
// So a real Service runs a real sweep here, with the hook wired the way the
// composition root wires it -- and TestTheCompositionRootWiresTheSweepToTheAgent
// is what holds that wiring to this.
func TestASweepReleasesTheReasonsForPullRequestsItNoLongerSees(t *testing.T) {
	tr, git := escalatingAgent(t, "the ClusterRole is gone and no values change puts it back")
	if err := tr.Run(context.Background(), agent.Promotion{
		Project: "platform", Stage: "prod", PRNumber: git.PR.Number, Branch: git.PR.Branch,
	}); err != nil {
		t.Fatal(err)
	}
	if tr.EscalationReason(git.PR.Number) == "" {
		t.Fatal("the agent kept no reason, so this test is about nothing")
	}

	// A verdict already standing on the host, so the sweep lists and prunes
	// without rendering anything: what is under test is the listing, not a
	// gate run.
	host := &gitprovider.Fake{Check: gitprovider.CheckSuccess}
	gs := &gateservice.Service{
		Git: host, CheckName: "addons-gate", Poll: time.Hour,
		Log:    t.Logf,
		Listed: tr.ForgetEscalationsExcept,
	}

	// Still open. The reason stays.
	host.OpenPRs = []gitprovider.PullRequest{*git.PR}
	runOneSweep(t, gs)
	if tr.EscalationReason(git.PR.Number) == "" {
		t.Fatal("a sweep that still sees the pull request released its reason, so the queue " +
			"goes silent about a handoff nobody has picked up")
	}

	// Merged. The reason goes with it.
	host.OpenPRs = nil
	runOneSweep(t, gs)
	if reason := tr.EscalationReason(git.PR.Number); reason != "" {
		t.Errorf("the reason for a pull request the sweep no longer lists is still held: %q\n"+
			"Kept, it is the slow leak the same sweep prunes its verdict cache and its "+
			"comment histories to avoid, and an answer about work nobody is waiting on.",
			reason)
	}
}

// A sweep that could not list releases nothing.
//
// Its empty list is the absence of evidence. Acting on it would forget every
// handoff this process holds the moment a token expired, which is "nothing is
// open" and "nothing looked" confused in the direction that loses the work.
func TestASweepThatCouldNotListReleasesNothing(t *testing.T) {
	tr, git := escalatingAgent(t, "the CRD schema moved and the repository still declares v1beta1")
	if err := tr.Run(context.Background(), agent.Promotion{
		Project: "platform", Stage: "prod", PRNumber: git.PR.Number, Branch: git.PR.Branch,
	}); err != nil {
		t.Fatal(err)
	}

	host := &gitprovider.Fake{Check: gitprovider.CheckSuccess,
		ListErr: errors.New("the host said 401")}
	gs := &gateservice.Service{
		Git: host, CheckName: "addons-gate", Poll: time.Hour,
		Log:    t.Logf,
		Listed: tr.ForgetEscalationsExcept,
	}
	runOneSweep(t, gs)

	if tr.EscalationReason(git.PR.Number) == "" {
		t.Error("a sweep that could not list released a reason. An empty listing from a " +
			"revoked token is not a merged pull request.")
	}
}

// The composition root wires the sweep's listing to the agent.
//
// Read from main.go's own syntax tree, for the reason redaction_test.go reads
// main's priming call the same way: deleting the one line that joins them
// compiles, passes every other test in this file, and leaves a process that
// keeps one model sentence per escalation until the pod restarts. There is no
// output to assert on, so the assertion is that the line is there.
func TestTheCompositionRootWiresTheSweepToTheAgent(t *testing.T) {
	path := filepath.Join(helmtest.Root(t), "main.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", path, err)
	}

	// The method the hook has to reach, named here and checked against the
	// agent's own type below, so a rename on the agent's side fails rather
	// than leaving this walk hunting for a method nobody declares.
	const releases = "ForgetEscalationsExcept"

	var built bool
	var wired string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Service" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "gateservice" {
			return true
		}
		built = true
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Listed" {
				continue
			}
			// The VALUE, not just the key. A field set to a closure that does
			// nothing satisfies "Listed is wired" and releases nothing, which
			// is the leak with a passing test on top of it.
			if method, ok := kv.Value.(*ast.SelectorExpr); ok {
				wired = method.Sel.Name
			} else {
				wired = "something that is not a method value"
			}
		}
		return true
	})

	// Two self-checks, and neither is optional. A walk that stops finding the
	// service, or a method that has been renamed on the agent, would otherwise
	// report agreement between things it never read.
	if !built {
		t.Fatalf("found no gateservice.Service literal in %s, so this walk read nothing. "+
			"Fix the walk, and check what the sweep is wired to.", path)
	}
	if _, ok := reflect.TypeOf(&agent.Triage{}).MethodByName(releases); !ok {
		t.Fatalf("agent.Triage has no %s method, so the name this walk looks for is not the "+
			"one that releases anything. Fix this test, and check the wiring.", releases)
	}
	if wired != releases {
		t.Errorf("%s wires the gate service's Listed to %q rather than to the agent's %s, so "+
			"nothing releases the escalation reasons it holds. They are dropped when a pull "+
			"request stops being open, and the sweep's listing is the only thing in this "+
			"process that knows one has.", path, wired, releases)
	}
}

// runOneSweep runs the service until exactly one sweep has finished.
//
// Run sweeps immediately and then waits out Poll, so a long Poll and a cancel
// after the first stamp is one sweep. Asserted on the stamp rather than on a
// sleep, because a test that waited a fixed time would pass on a machine that
// was slow enough and prove nothing.
func runOneSweep(t *testing.T, gs *gateservice.Service) {
	t.Helper()

	before := gs.Status().SweptAt
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); gs.Run(ctx) }()

	deadline := time.After(10 * time.Second)
	for gs.Status().SweptAt.Equal(before) {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("no sweep completed in ten seconds")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}

// escalatingAgent is a real Triage, wired to fakes, whose model asks for a
// human with the sentence given.
//
// Everything the agent reaches for is a consumer-defined interface with a fake
// beside it, which is what makes this affordable: the whole workflow runs
// here, in the composition root's own test binary, with no cluster, no git
// host and no model.
func escalatingAgent(t *testing.T, reason string) (*agent.Triage, *gitprovider.Fake) {
	t.Helper()

	git := &gitprovider.Fake{
		PR: &gitprovider.PullRequest{
			Number: 264, Title: "chore(deps): bump external-secrets to 0.10.0",
			Branch: "kargo/external-secrets", BaseBranch: "main", HeadSHA: "9f2c1a4b",
		},
		Check: gitprovider.CheckFailure,
	}
	root := t.TempDir()
	return &agent.Triage{
		Git: git,
		LLM: &llm.Fake{ID: "fake", Verdict: &llm.Verdict{
			Classification:   llm.ClassEscalate,
			Summary:          "Decide whether to accept the removed ClusterRole.",
			Reasoning:        "Nothing in the editable list can restore a template the chart deleted.",
			EscalationReason: reason,
		}},
		CheckName:   "addons-gate",
		MaxAttempts: 2,
		Log:         t.Logf,
		Gate: redGate{&gateservice.Outcome{
			State:  gitprovider.CheckFailure,
			Report: gate.ReportMarker + "\nThe render lost a ClusterRole.\n",
		}},
		Checkout: func(context.Context, *gitprovider.PullRequest) (string, func(), error) {
			return root, func() {}, nil
		},
	}, git
}

// redGate is the in-process gate, already red. The agent asks it for a verdict
// and does not care where one came from, which is the whole point of the seam.
type redGate struct{ out *gateservice.Outcome }

func (g redGate) Ensure(context.Context, *gitprovider.PullRequest) *gateservice.Outcome {
	return g.out
}

// gateStatusOf turns the tool surface's own snapshot back into the sweep's, so
// a test can drive the crossing with the same pull requests it drives the
// handler with rather than writing the list twice.
func gateStatusOf(g mcp.GateStatus) gateservice.Status {
	out := gateservice.Status{SweptAt: g.SweptAt, Err: g.Err}
	for _, pr := range g.Open {
		out.Open = append(out.Open, gateservice.PRStatus{
			Number: pr.Number, Title: pr.Title, URL: pr.URL,
			HeadSHA: pr.HeadSHA, State: pr.State, Labels: pr.Labels,
		})
	}
	return out
}

// escalationLabel is the label the agent applies when it stops short of a
// mechanical fix, read from agent's own source.
//
// Derived rather than written out, for the reason every contract test in this
// repository derives its subject: a copy here would agree with a copy there
// until somebody changed one of them, which is the only moment either would
// matter. The self-check is what makes the derivation honest -- a walk that
// stops finding the constant fails loudly rather than comparing two empty
// strings.
func escalationLabel(t *testing.T) string {
	t.Helper()
	path := filepath.Join(helmtest.Root(t), "agent", "triage.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", path, err)
	}

	var label string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != "labelNeedsHuman" || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				if label, err = strconv.Unquote(lit.Value); err != nil {
					t.Fatalf("%s: labelNeedsHuman is not a string literal: %v", path, err)
				}
			}
		}
	}

	// The self-check, and not optional. If the constant moves or is renamed,
	// this test would otherwise drive the surface with an empty label and
	// report a pass over a contract it never read.
	if label == "" {
		t.Fatalf("found no labelNeedsHuman constant in %s. The agent records its escalations "+
			"differently now, and handoff_queue selects on a label nothing writes. Fix this "+
			"walk, and check what the tool is selecting on.", path)
	}
	return label
}

// handoffQueue calls the tool over the real handler and returns the result.
//
// The whole listener rather than the mapping function, because what is under
// test is what a client receives -- and because the composition root is the
// only place where the agent's vocabulary and the tool surface's are both
// visible, which is what this file exists to check.
func handoffQueue(t *testing.T, gate mcp.GateStatus, triage mcp.TriageStatus) []byte {
	t.Helper()

	srv := &mcp.Server{
		Repository: "example/platform",
		Report:     func() *pipeline.Report { return nil },
		Gate:       func() mcp.GateStatus { return gate },
		Triage:     func() mcp.TriageStatus { return triage },
		Auth:       mcp.Unauthenticated{},
		Now:        func() time.Time { return time.Date(2026, 8, 30, 12, 1, 0, 0, time.UTC) },
	}
	h, err := srv.Handler()
	if err != nil {
		t.Fatalf("the handler would not build: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+mcp.EndpointPath, bytes.NewReader([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"handoff_queue","arguments":{}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var probe struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buf.Bytes(), &probe); err != nil {
		t.Fatalf("%v\n%s", err, buf.Bytes())
	}
	if probe.Error != nil {
		t.Fatalf("handoff_queue answered with an error: %s", probe.Error.Message)
	}
	if len(probe.Result.StructuredContent) == 0 {
		t.Fatalf("handoff_queue returned no result:\n%s", buf.Bytes())
	}
	return probe.Result.StructuredContent
}
