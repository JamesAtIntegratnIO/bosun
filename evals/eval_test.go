package evals

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// TestEval measures the shipped prompts against a real endpoint. Skipped unless
// DELIVERY_AGENT_LIVE is set, so `go test ./...` stays hermetic and offline.
//
// BOTH prompts, and each case says which one it is for. Passing only
// DELIVERY_AGENT_PROMPT measures triage and skips the explain cases with a
// line saying so -- which is better than quietly scoring the explanation
// against the classifier's prompt and reporting a number for it.
//
//	DELIVERY_AGENT_LIVE=http://localhost:1234/v1 \
//	DELIVERY_AGENT_MODELS=qwen/qwen3.8-27b \
//	DELIVERY_AGENT_PROMPT="$(scripts/extract-prompt.sh)" \
//	DELIVERY_AGENT_EXPLAIN_PROMPT="$(scripts/extract-prompt.sh explainPrompt)" \
//	go test ./evals -run Eval -v -timeout 60m
func TestEval(t *testing.T) {
	base := os.Getenv("DELIVERY_AGENT_LIVE")
	if base == "" {
		t.Skip("set DELIVERY_AGENT_LIVE to measure against a real endpoint")
	}
	models := strings.Split(os.Getenv("DELIVERY_AGENT_MODELS"), ",")
	if len(models) == 0 || models[0] == "" {
		t.Fatal("DELIVERY_AGENT_MODELS is required")
	}
	system := os.Getenv("DELIVERY_AGENT_PROMPT")
	if system == "" {
		t.Fatal("DELIVERY_AGENT_PROMPT is required")
	}
	explain := os.Getenv("DELIVERY_AGENT_EXPLAIN_PROMPT")
	withInventory := os.Getenv("DELIVERY_AGENT_NO_INVENTORY") == ""
	// A substring filter, because the loop worth running most often is one
	// case against a prompt you just edited -- and without this that costs the
	// whole suite every time.
	only := os.Getenv("DELIVERY_AGENT_CASES")

	// A prompt per path, so a case can never be scored against the wrong one.
	prompts := map[string]string{PathTriage: system, PathExplain: explain}

	skipped := 0
	for _, c := range Cases {
		if prompts[c.Path] == "" && (only == "" || strings.Contains(c.Name, only)) {
			skipped++
		}
	}
	if skipped > 0 {
		t.Logf("skipping %d case(s): no prompt supplied for their path "+
			"(set DELIVERY_AGENT_EXPLAIN_PROMPT to measure the explain path)", skipped)
	}

	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		p := &llm.OpenAI{
			BaseURL: base, Model: model,
			APIKey:          os.Getenv("DELIVERY_AGENT_KEY"),
			ReasoningEffort: os.Getenv("DELIVERY_AGENT_EFFORT"),
			Timeout:         20 * time.Minute,
		}
		var results []Result
		for _, c := range Cases {
			if only != "" && !strings.Contains(c.Name, only) {
				continue
			}
			prompt := prompts[c.Path]
			if prompt == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
			results = append(results, Run(ctx, p, prompt, c, withInventory))
			cancel()
		}
		t.Log(Summarise(model, results).String())
	}
}

// The explain cases exist to catch invention, and a probe that cannot fire is
// worth nothing. This is the guard on the probes themselves: every
// MustNotMention string has to be absent from the evidence the case supplies,
// or it is measuring the fixture rather than the model.
func TestTheInventionProbesCouldOnlyFireOnAnInvention(t *testing.T) {
	for _, c := range Cases {
		if c.Path != PathExplain {
			continue
		}
		if len(c.MustMention) == 0 && len(c.MustNotMention) == 0 {
			t.Errorf("%s: an explain case with no grounding assertion measures only the class", c.Name)
		}
		evidence := BuildPrompt(c, true) + upstream.Render(c.Notes)
		for _, never := range c.MustNotMention {
			if containsWord(evidence, never) {
				t.Errorf("%s: %q appears in the evidence, so the probe fires on a grounded answer",
					c.Name, never)
			}
		}
		for _, want := range c.MustMention {
			if !strings.Contains(strings.ToLower(evidence), strings.ToLower(want)) {
				t.Errorf("%s: %q is nowhere in the evidence, so citing it would itself be an invention",
					c.Name, want)
			}
		}
	}
}
