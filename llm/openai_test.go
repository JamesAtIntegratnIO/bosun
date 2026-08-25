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

// stripFence exists for two things that survive constrained decoding on looser
// backends. Both are real observations, and neither had a test.
func TestStripFencePullsTheJSONOut(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"bare json", `{"a":1}`, `{"a":1}`},
		{"fenced with a language", "```json\n{\"a\":1}\n```", `{"a":1}`},
		{"fenced with no language", "```\n{\"a\":1}\n```", `{"a":1}`},
		{"thinking before the answer", "Let me think about this.\n{\"a\":1}", `{"a":1}`},
		{"both at once", "Thinking...\n```json\n{\"a\":1}\n```", `{"a":1}`},
		{"leading whitespace", "   {\"a\":1}   ", `{"a":1}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripFence(tc.in); got != tc.want {
				t.Errorf("stripFence(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseVerdictRefusesWhatItCannotUse(t *testing.T) {
	if _, err := parseVerdict("not json at all"); err == nil {
		t.Error("unparseable content must not become a verdict")
	} else if !strings.Contains(err.Error(), "parseable") {
		t.Errorf("the error must say what went wrong: %v", err)
	}

	// Parseable but unusable: Validate is consulted, not bypassed.
	if _, err := parseVerdict(`{"classification":"mechanical","summary":"s"}`); err == nil {
		t.Error("a mechanical verdict with no edits must be refused here too")
	}
}

func TestParseVerdictAcceptsAFencedVerdict(t *testing.T) {
	v, err := parseVerdict("```json\n" +
		`{"classification":"no_action","summary":"nothing to do","reasoning":"r"}` + "\n```")
	if err != nil {
		t.Fatal(err)
	}
	if v.Classification != ClassNoAction || v.Summary != "nothing to do" {
		t.Errorf("got %+v", v)
	}
}

// truncate bounds what a failure quotes back. An unbounded quote of a model's
// answer is how a log line becomes a megabyte.
func TestTruncate(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Errorf("short input must be untouched, got %q", got)
	}
	if got := truncate("abcdefghij", 3); got != "abc..." {
		t.Errorf("got %q", got)
	}
}

// The adapter takes an injectable client, so the whole request/response shape
// is checkable without a model.
func TestOpenAIClassifySendsASchemaAndReadsTheAnswer(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":
			"{\"classification\":\"escalate\",\"summary\":\"needs a human\",\"reasoning\":\"the CRD moved\"}"}}]}`))
	}))
	defer srv.Close()

	o := &OpenAI{BaseURL: srv.URL, Model: "m", APIKey: "k", HTTP: srv.Client()}
	v, err := o.Classify(context.Background(), "system", "user")
	if err != nil {
		t.Fatal(err)
	}
	if v.Classification != ClassEscalate {
		t.Errorf("got %+v", v)
	}
	// Recovered by Validate, not by the caller.
	if v.EscalationReason != "the CRD moved" {
		t.Errorf("the empty escalation reason must be recovered, got %q", v.EscalationReason)
	}

	// Constrained decoding is the point: where the backend honours it, a
	// malformed answer becomes impossible rather than merely unlikely.
	rf, _ := got["response_format"].(map[string]any)
	js, _ := rf["json_schema"].(map[string]any)
	if js == nil || js["strict"] != true || js["name"] != "verdict" {
		t.Errorf("the request must carry a strict json_schema, got %v", rf)
	}
	if got["temperature"] != float64(0) {
		t.Errorf("temperature must be 0, got %v", got["temperature"])
	}
}

func TestOpenAISurfacesAnErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()

	o := &OpenAI{BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	if _, err := o.Classify(context.Background(), "s", "u"); err == nil {
		t.Fatal("a 404 must not read as a verdict")
	} else if !strings.Contains(err.Error(), "404") {
		t.Errorf("the error must carry the status: %v", err)
	}
}

func TestOpenAIRestructureReadsTheDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":
			"{\"document\":\"apiVersion: v1\\nkind: ConfigMap\\n\",\"notes\":\"moved a field\"}"}}]}`))
	}))
	defer srv.Close()

	o := &OpenAI{BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	m, err := o.Restructure(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Document, "ConfigMap") {
		t.Errorf("got %+v", m)
	}
}

// An empty document is not a migration, however well-formed the JSON is.
func TestOpenAIRestructureRefusesAnEmptyDocument(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"document\":\"  \"}"}}]}`))
	}))
	defer srv.Close()

	o := &OpenAI{BaseURL: srv.URL, Model: "m", HTTP: srv.Client()}
	if _, err := o.Restructure(context.Background(), "s", "u"); err == nil {
		t.Fatal("an empty document must not be returned as a migration")
	}
}

func TestOpenAIName(t *testing.T) {
	if got := (&OpenAI{Model: "qwen3"}).Name(); got != "openai/qwen3" {
		t.Errorf("got %q", got)
	}
}
