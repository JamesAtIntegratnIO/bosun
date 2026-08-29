package evals

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/prompt"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// TestEval measures the shipped prompts against a real endpoint. Skipped unless
// DELIVERY_AGENT_LIVE is set, so `go test./...` stays hermetic and offline.
//
// The prompts are imported, not supplied. They used to arrive through three
// environment variables filled by a shell script that regex-scraped the Go
// source, and that bridge silently supplied nothing when a constant was
// renamed, so a shipped prompt went unmeasured while the suite reported a
// number for the two it still found. Importing them means the thing scored and
// the thing shipped are the same constant, checked by the compiler.
//
//	DELIVERY_AGENT_LIVE=http://localhost:1234/v1 \
//	DELIVERY_AGENT_MODELS=qwen/qwen3.8-27b \
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
	withInventory := os.Getenv("DELIVERY_AGENT_NO_INVENTORY") == ""
	// A substring filter, because the loop worth running most often is one
	// case against a prompt you just edited, and without this that costs the
	// whole suite every time.
	only := os.Getenv("DELIVERY_AGENT_CASES")

	// A prompt per path, so a case can never be scored against the wrong one.
	prompts := map[string]string{
		PathTriage:      prompt.System,
		PathExplain:     prompt.Explain,
		PathRestructure: prompt.Restructure,
		PathValues:      prompt.ValuesMigration,
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
		if len(c.Explain.MustMention) == 0 && len(c.Explain.MustNotMention) == 0 {
			t.Errorf("%s: an explain case with no grounding assertion measures only the class", c.Name)
		}
		evidence := BuildPrompt(c, true) + upstream.Render(c.Explain.Notes)
		for _, never := range c.Explain.MustNotMention {
			if containsWord(evidence, never) {
				t.Errorf("%s: %q appears in the evidence, so the probe fires on a grounded answer",
					c.Name, never)
			}
		}
		for _, want := range c.Explain.MustMention {
			if !strings.Contains(strings.ToLower(evidence), strings.ToLower(want)) {
				t.Errorf("%s: %q is nowhere in the evidence, so citing it would itself be an invention",
					c.Name, want)
			}
		}
	}
}
