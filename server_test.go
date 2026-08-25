package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/agent"
)

// Kargo's http step is synchronous, so the handler must answer before doing
// any work. A handler that blocked would put a model round trip inside every
// promotion's critical path.
func TestPromotionOpenedAnswersImmediately(t *testing.T) {
	blocked := make(chan struct{})
	s := &Server{
		Triage:  nil, // never reached: the fake below replaces Run
		Log:     testLogger(t),
		Timeout: time.Minute,
	}
	s.runFn = func(p agent.Promotion) error {
		<-blocked // hold the "work" open
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/promotion-opened",
		bytes.NewReader([]byte(`{"prNumber":42,"stage":"cert-manager"}`)))
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { s.PromotionOpened(rec, req); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler blocked on the triage goroutine")
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rec.Code)
	}
	close(blocked)
	s.Wait()
}

// Kargo retries a step whose response it did not like. A retry must not start
// a second triage of the same pull request.
func TestDuplicateCallsForOnePRCollapse(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	s := &Server{Log: testLogger(t), Timeout: time.Minute}
	s.runFn = func(p agent.Promotion) error {
		started <- struct{}{}
		<-release
		return nil
	}

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/promotion-opened",
			bytes.NewReader([]byte(`{"prNumber":7}`)))
		s.PromotionOpened(httptest.NewRecorder(), req)
	}

	// Wait for the first triage rather than for a duration. A sleep long
	// enough to be reliable on a loaded CI runner is far longer than this
	// needs, and one short enough to be quick fails at random -- and it fails
	// as "0 started", which reads as the collapse being too aggressive rather
	// than as the test being early.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no triage started")
	}
	// Nothing else may arrive. The two duplicates were rejected synchronously
	// inside PromotionOpened, which has already returned for all three, so
	// this is a decided fact by now and not a race.
	if n := len(started); n != 0 {
		t.Fatalf("want the 2 duplicate calls collapsed, got %d more triage(s)", n)
	}
	close(release)
	s.Wait()
}

func TestRejectsPayloadWithoutAPRNumber(t *testing.T) {
	s := &Server{Log: testLogger(t), Timeout: time.Minute}
	s.runFn = func(agent.Promotion) error { t.Fatal("should not run"); return nil }

	req := httptest.NewRequest(http.MethodPost, "/v1/promotion-opened",
		bytes.NewReader([]byte(`{"stage":"cert-manager"}`)))
	rec := httptest.NewRecorder()
	s.PromotionOpened(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// A panic in triage must not take the process down -- one malformed pull
// request should not stop the agent handling the next.
func TestPanicInTriageIsContained(t *testing.T) {
	s := &Server{Log: testLogger(t), Timeout: time.Minute}
	s.runFn = func(agent.Promotion) error { panic("boom") }

	req := httptest.NewRequest(http.MethodPost, "/v1/promotion-opened",
		bytes.NewReader([]byte(`{"prNumber":1}`)))
	s.PromotionOpened(httptest.NewRecorder(), req)
	s.Wait() // would crash the test binary if the recover were missing
}
