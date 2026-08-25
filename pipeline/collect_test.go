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
// failures -- Stages, Warehouses, promotions -- which reads as three things
// going wrong rather than one thing being absent, and sends the reader looking
// for a permissions problem that is not there.
func TestNoKargoOnTheClusterIsOneNoteNotThree(t *testing.T) {
	s := (&Collector{Kargo: absentKargo{}, Now: func() time.Time { return now }}).Collect(context.Background())

	if len(s.Notes) != 1 {
		t.Fatalf("want one note, got %d: %v", len(s.Notes), s.Notes)
	}
	if !strings.Contains(s.Notes[0], "does not serve the Kargo API") {
		t.Errorf("the note must say Kargo is absent, not that a read failed: %q", s.Notes[0])
	}
}

// A source that cannot answer the presence question is read exactly as before.
func TestASourceWithoutThePresenceCheckStillReads(t *testing.T) {
	s := (&Collector{Kargo: failingKargo{}, Now: func() time.Time { return now }}).Collect(context.Background())

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
