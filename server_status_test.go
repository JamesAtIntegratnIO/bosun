package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/agent"
	"github.com/JamesAtIntegratnIO/bosun/web"
)

func promote(t *testing.T, s *Server, pr int, promotionID string) {
	t.Helper()
	body := fmt.Sprintf(`{"prNumber":%d,"promotion":%q}`, pr, promotionID)
	req := httptest.NewRequest(http.MethodPost, "/v1/promotion-opened", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	s.PromotionOpened(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rec.Code)
	}
}

// The status page says what the agent is doing right now, so what it says has
// to be what the handler is actually holding: a triage in flight, a promotion
// queued behind one, and totals that count a failure as a failure.
func TestStatusReportsWhatTheHandlerHolds(t *testing.T) {
	release := make(chan struct{})
	s := &Server{Log: testLogger(t), Timeout: time.Minute}
	s.runFn = func(agent.Promotion) error {
		<-release
		return errors.New("the model refused")
	}

	promote(t, s, 42, "promo-1")
	// A different promotion for the same pull request is new work, held until
	// the running one finishes.
	promote(t, s, 42, "promo-2")

	// Wait for the triage to actually be in flight rather than merely
	// accepted; the handler answers before the goroutine reaches the work.
	st := waitFor(t, s, func(st web.TriageStatus) bool {
		return len(st.InFlight) == 1 && len(st.Queued) == 1
	})
	if st.InFlight[0] != 42 || st.Queued[0] != 42 {
		t.Fatalf("the page must name the pull request being triaged and the one queued behind it; got %+v", st)
	}

	close(release)
	s.Wait()

	final := s.Status()
	if len(final.InFlight) != 0 || len(final.Queued) != 0 {
		t.Fatalf("a finished triage must leave nothing in flight; got %+v", final)
	}
	// Both promotions ran, and both returned an error.
	if final.Done != 2 || final.Failed != 2 {
		t.Fatalf("totals must count every run and every failure; got done=%d failed=%d", final.Done, final.Failed)
	}
}

func TestStatusCountsASuccessAsASuccess(t *testing.T) {
	s := &Server{Log: testLogger(t), Timeout: time.Minute}
	s.runFn = func(agent.Promotion) error { return nil }

	promote(t, s, 7, "promo-1")
	s.Wait()

	st := s.Status()
	if st.Done != 1 || st.Failed != 0 {
		t.Fatalf("a clean triage must not be counted as a failure; got done=%d failed=%d", st.Done, st.Failed)
	}
}

// waitFor polls Status until the condition holds, because the handler answers
// before its goroutine reaches the work and a fixed sleep is how that becomes
// a flaky test.
func waitFor(t *testing.T, s *Server, ok func(web.TriageStatus) bool) web.TriageStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := s.Status()
		if ok(st) {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never settled; last was %+v", st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
