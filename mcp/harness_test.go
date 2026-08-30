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
)

// fixture is a server under test and the world behind it.
type fixture struct {
	srv    *Server
	http   *httptest.Server
	logged []string
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

// call runs one tool and returns its structuredContent as raw JSON, which is
// the value a client actually reads.
func (f *fixture) call(t *testing.T, tool string) json.RawMessage {
	t.Helper()
	code, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
		`"params":{"name":"`+tool+`","arguments":{}}}`)
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
