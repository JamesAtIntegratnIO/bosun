package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
)

// A cluster with no Kargo installed used to produce three separate read
// failures, Stages, Warehouses, promotions, which reads as three things going
// wrong rather than one thing being absent, and sends the reader looking for a
// permissions problem that is not there.
func TestNoKargoOnTheClusterIsOneNoteNotThree(t *testing.T) {
	s := (&Collector{Kargo: absentKargo{}, Now: func() time.Time { return now }}).Collect(context.Background(), "")

	if len(s.Notes) != 1 {
		t.Fatalf("want one note, got %d: %v", len(s.Notes), s.Notes)
	}
	if !strings.Contains(s.Notes[0], "does not serve the Kargo API") {
		t.Errorf("the note must say Kargo is absent, not that a read failed: %q", s.Notes[0])
	}
}

// A source that cannot answer the presence question is read exactly as before.
func TestASourceWithoutThePresenceCheckStillReads(t *testing.T) {
	s := (&Collector{Kargo: failingKargo{}, Now: func() time.Time { return now }}).Collect(context.Background(), "")

	if len(s.Notes) != 3 {
		t.Fatalf("want a note per failed read, got %d: %v", len(s.Notes), s.Notes)
	}
}

type absentKargo struct{ failingKargo }

func (absentKargo) KargoAvailable(context.Context) bool { return false }

type failingKargo struct{}

func (failingKargo) Stages(context.Context) ([]cluster.KargoStage, error) {
	return nil, errors.New("404")
}
func (failingKargo) Warehouses(context.Context) ([]cluster.KargoWarehouse, error) {
	return nil, errors.New("404")
}
func (failingKargo) Promotions(context.Context) ([]cluster.KargoPromotion, error) {
	return nil, errors.New("404")
}

// The reads that name a finding are per-object GETs, so what a sweep costs is
// decided entirely by which objects it asks for. It asks for the ones a
// finding is about, which on a healthy fleet is none at all.
func TestNamingReadsOnlyWhatAFindingWillPrint(t *testing.T) {
	k := &namingKargo{
		stages: []cluster.KargoStage{
			// Healthy, running freight, most recent promotion succeeded.
			{Name: "healthy", Namespace: "addons", CurrentFreight: "f-ok", Ready: true},
			// Wedged: its newest promotion is terminal and did not deliver.
			{Name: "wedged", Namespace: "addons", CurrentFreight: "f-old", Ready: true},
			// Stopped by its verification.
			{Name: "verifying", Namespace: "addons", CurrentFreight: "f-held", Ready: false,
				ReadyReason: "VerificationError", VerificationPhase: "Error",
				VerificationRunNamespace: "addons", VerificationRunName: "verifying.01"},
		},
		promotions: []cluster.KargoPromotion{
			{Name: "p1", Namespace: "addons", Stage: "healthy", Freight: "f-ok",
				Phase: PhaseSucceeded, CreatedAt: now},
			{Name: "p2", Namespace: "addons", Stage: "wedged", Freight: "f-wedged",
				Phase: PhaseErrored, CreatedAt: now},
		},
	}
	s := (&Collector{Kargo: k, Now: func() time.Time { return now }}).Collect(context.Background(), "")

	// The healthy Stage's freight is never read: nothing will print it.
	want := []string{"addons/f-wedged", "addons/f-held"}
	if !equal(k.freight, want) {
		t.Errorf("freight read = %v, want %v", k.freight, want)
	}
	if !equal(k.runs, []string{"addons/verifying.01"}) {
		t.Errorf("runs read = %v", k.runs)
	}
	if _, ok := s.FreightNamed("addons", "f-wedged"); !ok {
		t.Error("what was read must reach the snapshot")
	}
	if _, ok := s.VerificationOf(s.Stages[2]); !ok {
		t.Error("the run must reach the snapshot under the Stage's own reference")
	}
}

// A healthy fleet is the common case and it must cost nothing extra.
func TestNothingWrongMeansNoExtraReads(t *testing.T) {
	k := &namingKargo{stages: []cluster.KargoStage{
		{Name: "healthy", Namespace: "addons", CurrentFreight: "f-ok", Ready: true}}}
	(&Collector{Kargo: k, Now: func() time.Time { return now }}).Collect(context.Background(), "")
	if len(k.freight)+len(k.runs) != 0 {
		t.Errorf("a healthy sweep read %v and %v", k.freight, k.runs)
	}
}

// A cluster that refuses these reads refuses all of them, and one note is the
// whole of what that is worth saying.
func TestRefusedNamingReadsCollapseToOneNote(t *testing.T) {
	k := &namingKargo{
		err: errors.New("not permitted to read AnalysisRuns in addons"),
		stages: []cluster.KargoStage{
			{Name: "a", Namespace: "addons", CurrentFreight: "f-1", Ready: false,
				ReadyReason: "VerificationError", VerificationRunNamespace: "addons", VerificationRunName: "a.01"},
			{Name: "b", Namespace: "addons", CurrentFreight: "f-2", Ready: false,
				ReadyReason: "VerificationError", VerificationRunNamespace: "addons", VerificationRunName: "b.01"},
		},
	}
	s := (&Collector{Kargo: k, Now: func() time.Time { return now }}).Collect(context.Background(), "")
	if len(s.Notes) != 1 {
		t.Fatalf("want one note, got %d: %v", len(s.Notes), s.Notes)
	}
	if strings.Count(s.Notes[0], "not permitted") != 1 {
		t.Errorf("four refusals are not four times the information: %q", s.Notes[0])
	}
	// And the findings are still produced, from what was readable.
	if len(Detect(s).Findings) == 0 {
		t.Error("a refused enrichment must not cost the finding it was enriching")
	}
}

// A source that predates either capability is read exactly as it was.
func TestASourceWithoutTheNamingReadsIsUnchanged(t *testing.T) {
	s := (&Collector{Kargo: plainKargo{}, Now: func() time.Time { return now }}).Collect(context.Background(), "")
	if len(s.Notes) != 0 {
		t.Errorf("no capability is not a failure: %v", s.Notes)
	}
	if len(s.Freight) != 0 || len(s.Verifications) != 0 {
		t.Error("nothing should have been named")
	}
}

// plainKargo answers the three required reads and neither optional one.
type plainKargo struct{}

func (plainKargo) Stages(context.Context) ([]cluster.KargoStage, error) {
	return []cluster.KargoStage{{Name: "a", Namespace: "addons", CurrentFreight: "f-1",
		Ready: false, ReadyReason: "VerificationError"}}, nil
}
func (plainKargo) Warehouses(context.Context) ([]cluster.KargoWarehouse, error) { return nil, nil }
func (plainKargo) Promotions(context.Context) ([]cluster.KargoPromotion, error) { return nil, nil }

// namingKargo records which objects a sweep asked for, which is the property
// under test: the cost of a sweep is the set of names it chose.
type namingKargo struct {
	stages     []cluster.KargoStage
	promotions []cluster.KargoPromotion
	err        error

	freight []string
	runs    []string
}

func (k *namingKargo) Stages(context.Context) ([]cluster.KargoStage, error) { return k.stages, nil }
func (k *namingKargo) Warehouses(context.Context) ([]cluster.KargoWarehouse, error) {
	return nil, nil
}
func (k *namingKargo) Promotions(context.Context) ([]cluster.KargoPromotion, error) {
	return k.promotions, nil
}

func (k *namingKargo) Freight(_ context.Context, ns, name string) (cluster.KargoFreight, error) {
	k.freight = append(k.freight, ns+"/"+name)
	if k.err != nil {
		return cluster.KargoFreight{}, k.err
	}
	return cluster.KargoFreight{Name: name, Namespace: ns, Artifacts: []string{"ghcr.io/org/app:v1"}}, nil
}

func (k *namingKargo) AnalysisRun(_ context.Context, ns, name string) (cluster.AnalysisRun, error) {
	k.runs = append(k.runs, ns+"/"+name)
	if k.err != nil {
		return cluster.AnalysisRun{}, k.err
	}
	return cluster.AnalysisRun{Name: name, Namespace: ns, Phase: "Failed"}, nil
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// The capabilities are type-asserted, which is what keeps every existing
// source working -- and what makes a drifted signature turn the feature off
// silently instead of failing to compile. These two lines are the compiler
// checking that the reader main.go actually passes still carries both.
var (
	_ freightSource      = (*cluster.APIServer)(nil)
	_ verificationSource = (*cluster.APIServer)(nil)
	_ KargoSource        = (*cluster.APIServer)(nil)
)
