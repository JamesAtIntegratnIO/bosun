package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

type fakeKargo struct {
	stages     []cluster.KargoStage
	promotions []cluster.KargoPromotion
	stagesErr  error
}

func (f *fakeKargo) Stages(context.Context) ([]cluster.KargoStage, error) {
	return f.stages, f.stagesErr
}
func (f *fakeKargo) Warehouses(context.Context) ([]cluster.KargoWarehouse, error) { return nil, nil }
func (f *fakeKargo) Promotions(context.Context) ([]cluster.KargoPromotion, error) {
	return f.promotions, nil
}

func supervisorFor(t *testing.T, k *fakeKargo) (*Supervisor, *[]string) {
	t.Helper()
	var lines []string
	return &Supervisor{
		Collector: &pipeline.Collector{Kargo: k},
		Log:       func(f string, a ...any) { lines = append(lines, strings.TrimSpace(sprintf(f, a...))) },
	}, &lines
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

// A supervisor that reprints an unchanged report every ten minutes teaches
// people to filter it out, and then it is not a supervisor.
func TestItSpeaksOnlyWhenTheAnswerChanges(t *testing.T) {
	k := &fakeKargo{stages: []cluster.KargoStage{{Name: "kyverno", Ready: true}}}
	s, lines := supervisorFor(t, k)
	ctx := context.Background()

	s.sweep(ctx)
	first := len(*lines)
	if first == 0 {
		t.Fatal("the first sweep must say what it found")
	}
	s.sweep(ctx)
	if len(*lines) != first {
		t.Fatalf("an unchanged answer must not be repeated; said %v", (*lines)[first:])
	}

	// Something breaks: it must speak again, and carry the remedy.
	k.promotions = []cluster.KargoPromotion{{
		Name: "kyverno.01a.f", Namespace: "addons", Stage: "kyverno", Freight: "f",
		Phase: cluster.KargoPromotion{}.Phase, CreatedAt: time.Now().Add(-time.Hour),
	}}
	k.promotions[0].Phase = pipeline.PhaseErrored
	s.sweep(ctx)
	said := strings.Join((*lines)[first:], "\n")
	if !strings.Contains(said, "stopped receiving artifacts") {
		t.Fatalf("a new blocking finding must be announced:\n%s", said)
	}
	if !strings.Contains(said, "kubectl create -f -") {
		t.Fatalf("the log must carry the remedy, not only the problem:\n%s", said)
	}
}

// A source it cannot read is a note on the report, never a skipped sweep.
func TestAnUnreadableSourceStillProducesAReport(t *testing.T) {
	s, lines := supervisorFor(t, &fakeKargo{stagesErr: errors.New("403 Forbidden")})
	s.sweep(context.Background())
	r := s.Report()
	if r == nil {
		t.Fatal("a sweep that could not read Stages must still publish a report")
	}
	if r.Clean() {
		t.Fatal("a sweep that read nothing must not claim the pipeline is clean")
	}
	if !strings.Contains(strings.Join(r.Checked.Notes, " "), "403 Forbidden") {
		t.Fatalf("the report must say what it could not read: %v", r.Checked.Notes)
	}
	if !strings.Contains(strings.Join(*lines, "\n"), "could not check everything") {
		t.Fatalf("the log must say so too:\n%s", strings.Join(*lines, "\n"))
	}
}

// A scraper that reads zeroes from a supervisor which has not swept yet would
// record "nothing is wrong" as a measurement.
func TestEndpointsRefuseToAnswerBeforeTheFirstSweep(t *testing.T) {
	s, _ := supervisorFor(t, &fakeKargo{})
	for _, format := range []string{"metrics", "markdown"} {
		rec := httptest.NewRecorder()
		s.Handler(format).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s answered %d before any sweep; a zero read as a measurement is the "+
				"most expensive thing a monitor can do", format, rec.Code)
		}
	}
}

func TestTheMetricsEndpointServesPrometheusText(t *testing.T) {
	s, _ := supervisorFor(t, &fakeKargo{stages: []cluster.KargoStage{{Name: "x", Ready: true}}})
	s.sweep(context.Background())
	rec := httptest.NewRecorder()
	s.Handler("metrics").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content type %q", ct)
	}
	if !strings.Contains(rec.Body.String(), "bosun_pipeline_sweep_timestamp_seconds") {
		t.Fatalf("body:\n%s", rec.Body.String())
	}
}

// The checkout is what makes the pin check run at all; a failure must degrade
// to a note rather than losing the whole sweep.
func TestAFailedCheckoutDoesNotLoseTheSweep(t *testing.T) {
	s, _ := supervisorFor(t, &fakeKargo{stages: []cluster.KargoStage{{Name: "x", Ready: true}}})
	s.Checkout = func(context.Context) (string, func(), error) {
		return "", func() {}, errors.New("no such branch")
	}
	s.sweep(context.Background())
	r := s.Report()
	if r == nil || r.Checked.Stages != 1 {
		t.Fatal("the cluster half is worth having on its own")
	}
	if !strings.Contains(strings.Join(r.Checked.Notes, " "), "pins were not checked") {
		t.Fatalf("the report must admit the pin check did not run: %v", r.Checked.Notes)
	}
}

var _ = gitprovider.PullRequest{}
