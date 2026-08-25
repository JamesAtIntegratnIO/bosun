package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Anthropic speaks the Messages API.
//
// Covers hosted Anthropic directly, and -- via BaseURL -- Bedrock and Vertex
// gateways or a LiteLLM-style proxy presenting the same shape.
type Anthropic struct {
	BaseURL   string
	APIKey    string
	Model     string
	MaxTokens int
	Version   string
	Timeout   time.Duration
	HTTP      *http.Client
}

func (a *Anthropic) Name() string { return fmt.Sprintf("anthropic/%s", a.Model) }

func (a *Anthropic) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	t := a.Timeout
	if t == 0 {
		t = 10 * time.Minute
	}
	return &http.Client{Timeout: t}
}

func (a *Anthropic) Classify(ctx context.Context, systemPrompt, userPrompt string) (*Verdict, error) {
	input, text, err := a.structured(ctx, systemPrompt, userPrompt,
		"record_verdict", "Record the triage verdict for this pull request.", VerdictSchema())
	if err != nil {
		return nil, err
	}
	if len(input) > 0 {
		var v Verdict
		if err := json.Unmarshal(input, &v); err != nil {
			return nil, fmt.Errorf("parsing tool input: %w", err)
		}
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("model returned an unusable verdict: %w", err)
		}
		return &v, nil
	}
	if text != "" {
		return parseVerdict(text)
	}
	return nil, fmt.Errorf("no verdict in response")
}

// Restructure asks for one migrated document, through the same forced tool
// call.
func (a *Anthropic) Restructure(ctx context.Context, systemPrompt, userPrompt string) (*Migration, error) {
	input, text, err := a.structured(ctx, systemPrompt, userPrompt,
		"record_migration", "Record the migrated document.", MigrationSchema())
	if err != nil {
		return nil, err
	}
	var m Migration
	switch {
	case len(input) > 0:
		if err := json.Unmarshal(input, &m); err != nil {
			return nil, fmt.Errorf("parsing tool input: %w", err)
		}
	case text != "":
		if err := json.Unmarshal([]byte(stripFence(text)), &m); err != nil {
			return nil, fmt.Errorf("parsing migration: %w", err)
		}
	}
	if strings.TrimSpace(m.Document) == "" {
		return nil, fmt.Errorf("no migration in response")
	}
	return &m, nil
}

// structured runs one forced tool call and returns its input, or the text a
// gateway without tool support answered with instead.
func (a *Anthropic) structured(ctx context.Context, systemPrompt, userPrompt, tool, desc string,
	schema map[string]any) (json.RawMessage, string, error) {

	maxTokens := a.MaxTokens
	if maxTokens == 0 {
		maxTokens = 8192
	}
	version := a.Version
	if version == "" {
		version = "2023-06-01"
	}

	// A single forced tool call is how the Messages API does structured
	// output: the model must answer through the tool's schema, so the reply
	// arrives already shaped.
	body := map[string]any{
		"model":      a.Model,
		"max_tokens": maxTokens,
		"system":     systemPrompt,
		"messages": []map[string]any{
			{"role": "user", "content": userPrompt},
		},
		"tools": []map[string]any{{
			"name":         tool,
			"description":  desc,
			"input_schema": schema,
		}},
		"tool_choice": map[string]any{"type": "tool", "name": tool},
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}

	base := a.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	url := strings.TrimRight(base, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", version)
	if a.APIKey != "" {
		req.Header.Set("x-api-key", a.APIKey)
	}

	resp, err := a.client().Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("calling %s: %w", url, err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, truncate(string(payload), 400))
	}

	var out struct {
		Content []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
			Text  string          `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, "", fmt.Errorf("parsing response: %w", err)
	}

	for _, c := range out.Content {
		if c.Type == "tool_use" && c.Name == tool {
			return c.Input, "", nil
		}
	}
	// Fall back to text, for gateways that do not implement tool use.
	for _, c := range out.Content {
		if c.Type == "text" && strings.Contains(c.Text, "{") {
			return nil, c.Text, nil
		}
	}
	return nil, "", nil
}
