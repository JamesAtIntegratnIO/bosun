package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
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
	body := handoffQueue(t, gate)
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
func handoffQueue(t *testing.T, gate mcp.GateStatus) []byte {
	t.Helper()

	srv := &mcp.Server{
		Repository: "example/platform",
		Report:     func() *pipeline.Report { return nil },
		Gate:       func() mcp.GateStatus { return gate },
		Triage:     func() mcp.TriageStatus { return mcp.TriageStatus{MaxAttempts: 2} },
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
