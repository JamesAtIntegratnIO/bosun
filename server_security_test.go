package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/agent"
)

func post(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/v1/promotion-opened", bytes.NewReader([]byte(body)))
}

// The endpoint's payload names the pull request the agent will edit and the
// files it will read into a published prompt. Unauthenticated, its only
// boundary is a namespace-level NetworkPolicy, which admits every workload
// in the namespace.
func TestTheEndpointRequiresItsTokenWhenOneIsConfigured(t *testing.T) {
	ran := make(chan struct{}, 4)
	s := &Server{Log: testLogger(t), Timeout: time.Minute, Token: "sekrit"}
	s.runFn = func(agent.Promotion) error { ran <- struct{}{}; return nil }

	// Trailing header whitespace is insignificant and is trimmed, so it is not
	// among these. A prefix of the token is: a comparison that short-circuits
	// on the first differing byte is a slow oracle for the rest of it.
	for _, h := range []string{"", "Bearer wrong", "sekri", "Bearer sekri", "Bearer sekritt"} {
		req := post(`{"prNumber":1}`)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		rec := httptest.NewRecorder()
		s.PromotionOpened(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: want 401, got %d", h, rec.Code)
		}
	}

	req := post(`{"prNumber":1}`)
	req.Header.Set("Authorization", "Bearer sekrit")
	rec := httptest.NewRecorder()
	s.PromotionOpened(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the right token must be accepted, got %d", rec.Code)
	}
	s.Wait()
	if len(ran) != 1 {
		t.Errorf("want exactly the authorized call to run, got %d", len(ran))
	}
}

// Unset must stay open: an operator upgrading into this setting would
// otherwise get a service that silently stops answering Kargo.
func TestNoTokenMeansNoCheck(t *testing.T) {
	s := &Server{Log: testLogger(t), Timeout: time.Minute}
	s.runFn = func(agent.Promotion) error { return nil }
	rec := httptest.NewRecorder()
	s.PromotionOpened(rec, post(`{"prNumber":1}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rec.Code)
	}
	s.Wait()
}

// Collapsing on the pull request number alone acknowledged a new promotion
// with 202 and dropped it: the second Freight into a stage that already
// had an open pull request got a verdict about the first one, and nothing
// ever revisited it.
func TestANewPromotionForABusyPRIsRunRatherThanDropped(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	s := &Server{Log: testLogger(t), Timeout: time.Minute}
	s.runFn = func(p agent.Promotion) error {
		started <- p.PromotionID
		<-release
		return nil
	}

	s.PromotionOpened(httptest.NewRecorder(), post(`{"prNumber":7,"promotion":"promo-a"}`))
	select {
	case id := <-started:
		if id != "promo-a" {
			t.Fatalf("want promo-a first, got %q", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no triage started")
	}

	// A retry of the running promotion is still collapsed.
	rec := httptest.NewRecorder()
	s.PromotionOpened(rec, post(`{"prNumber":7,"promotion":"promo-a"}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("want 202, got %d", rec.Code)
	}

	// A different promotion is new work.
	s.PromotionOpened(httptest.NewRecorder(), post(`{"prNumber":7,"promotion":"promo-b"}`))
	// And a third supersedes the second: newest wins, and exactly one re-run
	// follows however many arrive.
	s.PromotionOpened(httptest.NewRecorder(), post(`{"prNumber":7,"promotion":"promo-c"}`))

	close(release)
	s.Wait()

	var got []string
	for len(started) > 0 {
		got = append(got, <-started)
	}
	if len(got) != 1 || got[0] != "promo-c" {
		t.Fatalf("want exactly one re-run of the newest promotion, got %v", got)
	}
}

// Distinct pull requests still run concurrently; the bound is on how many at
// once, not on whether they interleave.
func TestConcurrentTriageIsBounded(t *testing.T) {
	const limit = 2
	inside := make(chan struct{}, 16)
	release := make(chan struct{})
	s := &Server{Log: testLogger(t), Timeout: time.Minute, MaxConcurrent: limit}
	s.runFn = func(agent.Promotion) error {
		inside <- struct{}{}
		<-release
		return nil
	}

	for i := 1; i <= 6; i++ {
		s.PromotionOpened(httptest.NewRecorder(), post(`{"prNumber":`+strconv.Itoa(i)+`}`))
	}
	// Let the admitted ones arrive.
	deadline := time.After(5 * time.Second)
	for len(inside) < limit {
		select {
		case <-deadline:
			t.Fatalf("only %d triage(s) started, want %d", len(inside), limit)
		case <-time.After(time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(inside); n > limit {
		t.Fatalf("want at most %d concurrent triages, got %d", limit, n)
	}
	close(release)
	s.Wait()
}
