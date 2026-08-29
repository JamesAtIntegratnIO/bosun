package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The Messages API answers through a forced tool call, so the verdict arrives
// already shaped, and the adapter has to prefer that over any text the model
// also produced.
func TestAnthropicClassifyReadsTheToolInput(t *testing.T) {
	var got map[string]any
	var hdr http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"content":[
			{"type":"text","text":"I will record this."},
			{"type":"tool_use","name":"record_verdict","input":
				{"classification":"mechanical","summary":"flip two defaults",
				 "reasoning":"the chart changed them",
				 "edits":[{"path":"a.yaml","key":"k","from":"true","to":"false","rationale":"r"}]}}]}`))
	}))
	defer srv.Close()

	a := &Anthropic{BaseURL: srv.URL, Model: "m", APIKey: "k", HTTP: srv.Client()}
	v, err := a.Classify(context.Background(), "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if v.Classification != ClassMechanical || len(v.Edits) != 1 {
		t.Fatalf("got %+v", v)
	}

	// The forcing is the point: without tool_choice the model may answer in
	// prose, and prose is what parseVerdict exists to salvage rather than rely on.
	tc, _ := got["tool_choice"].(map[string]any)
	if tc == nil || tc["type"] != "tool" || tc["name"] != "record_verdict" {
		t.Errorf("the tool call must be forced, got %v", tc)
	}
	if hdr.Get("anthropic-version") == "" {
		t.Error("the API version header is required by the endpoint")
	}
	if hdr.Get("x-api-key") != "k" {
		t.Errorf("the key goes in x-api-key, got %q", hdr.Get("x-api-key"))
	}
}

// A backend that answers in text instead of through the tool still has to be
// readable; that is what parseVerdict is for on this path.
func TestAnthropicFallsBackToText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":
			"{\"classification\":\"no_action\",\"summary\":\"nothing changed\"}"}]}`))
	}))
	defer srv.Close()

	a := &Anthropic{BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	v, err := a.Classify(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if v.Classification != ClassNoAction {
		t.Errorf("got %+v", v)
	}
}

// An unusable verdict is refused here, not handed on.
func TestAnthropicRefusesAnUnusableToolInput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","name":"record_verdict","input":
			{"classification":"mechanical","summary":"s"}}]}`))
	}))
	defer srv.Close()

	a := &Anthropic{BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	if _, err := a.Classify(context.Background(), "s", "u"); err == nil {
		t.Fatal("mechanical with no edits must be refused")
	} else if !strings.Contains(err.Error(), "unusable") {
		t.Errorf("got %v", err)
	}
}

func TestAnthropicSurfacesAnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "overloaded", http.StatusTooManyRequests)
	}))
	defer srv.Close()

	a := &Anthropic{BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	if _, err := a.Classify(context.Background(), "s", "u"); err == nil {
		t.Fatal("a 429 must not read as a verdict")
	} else if !strings.Contains(err.Error(), "429") {
		t.Errorf("the error must carry the status: %v", err)
	}
}

func TestAnthropicRestructureReadsTheDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"tool_use","name":"record_migration","input":
			{"document":"apiVersion: v1\nkind: Secret\n","notes":"n"}}]}`))
	}))
	defer srv.Close()

	a := &Anthropic{BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	m, err := a.Restructure(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Document, "Secret") {
		t.Errorf("got %+v", m)
	}
}

func TestAnthropicName(t *testing.T) {
	if got := (&Anthropic{Model: "claude-x"}).Name(); !strings.Contains(got, "claude-x") {
		t.Errorf("got %q", got)
	}
}
