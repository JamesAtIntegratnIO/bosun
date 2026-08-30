package mcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/pipeline"
	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// Server is bosun's read-only MCP surface.
//
// Every field is either a snapshot the process already holds or a decision the
// operator made. There is deliberately no client of anything here: no git
// provider, no cluster reader, no model. That is not an omission this could
// grow out of, it is the property that makes the surface safe to publish, and
// mcp_reads_nothing_test.go is what keeps it true.
type Server struct {
	// Repository is the one repository this install watches, "owner/repo".
	//
	// Every result carries it. An install binds to exactly one, so today this
	// is a constant per process; it is on the wire from the first release
	// because a client that has been taught answers are install-wide cannot
	// later be told they are narrower without breaking.
	Repository string

	// Report is the last completed sweep, or nil before the first one.
	//
	// A function rather than a value because the supervisor replaces it on
	// every sweep, and a copy taken at wiring time would answer with the
	// state of the world at start-up forever. nil is not an error: it is the
	// "no sweep has completed" case, and it is the case this whole surface is
	// most careful about.
	Report func() *pipeline.Report

	// Auth decides whether a request may be served.
	//
	// An interface with one implementation, which is the shape asked for
	// rather than an abstraction reaching for a second caller. The eventual
	// rung is a token verifier fronted by a gateway, and keeping the check
	// behind this makes that additive instead of a rewrite of the handler.
	Auth Auth

	// Version is what the operator deployed, reported in the handshake so a
	// client can tell which build answered.
	Version string

	// Log records one line per tool call. nil discards, which is what a test
	// that is not asserting on the log wants.
	Log func(string, ...any)

	// Now is the clock, for the age of a report. Injected so a result is
	// reproducible in a test.
	Now func() time.Time
}

func (s *Server) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(f, a...)
	}
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Tool is one read-only tool.
//
// Description and Params are constants at every registration site, and that is
// a rule rather than a habit: a client hands both to a model as instructions,
// so a Stage name or a release note interpolated into either is text from a
// cluster arriving in the field a model trusts most. TestDescriptionsAreConstants
// derives the check from this package's own syntax tree.
type Tool struct {
	Name        string
	Description string
	// Params is the JSON Schema for the tool's arguments.
	Params json.RawMessage
	// Result is the zero value of what Call returns.
	//
	// Carried so the structural guard can walk the type without issuing a
	// request: the wire can sample what a handler happens to produce, and it
	// cannot prove that no field of the type could ever reach a credential.
	Result any
	// Call answers, from snapshots only.
	Call func(args json.RawMessage) (any, error)
}

// tools is the registered tool set, bound to this server.
//
// One list, built here, rather than a registry a tool adds itself to: with
// four tools coming, the value of being able to read the whole surface in one
// place is higher than the value of letting a file register itself.
func (s *Server) tools() []Tool {
	return []Tool{{
		Name:        "pipeline_report",
		Description: pipelineReportDescription,
		Params:      noArguments,
		Result:      Report{},
		Call:        func(json.RawMessage) (any, error) { return s.pipelineReport(), nil },
	}}
}

// Tools is every tool this surface registers, unbound.
//
// Built against a zero Server so a guard can walk the result types without a
// report, a repository or a token: what a tool returns is a property of this
// package rather than of one install. It is how the structural test in the
// repository root enumerates its subject instead of listing it.
func Tools() []Tool { return (&Server{}).tools() }

// noArguments is the input schema for a tool that takes none.
//
// `additionalProperties: false` rather than an empty object: a client that
// sends an argument to a tool that has none has misunderstood something, and
// the schema is the cheapest place for it to find that out.
var noArguments = json.RawMessage(
	`{"type":"object","properties":{},"additionalProperties":false}`)

// listTools is the tools/list payload.
func (s *Server) listTools() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools()))
	for _, t := range s.tools() {
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Params,
		})
	}
	return out
}

// callParams is the tools/call request.
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// callTool runs one tool and wraps its result the way MCP expects.
//
// The result travels twice: as structuredContent, which is the typed value
// this surface exists to publish, and as one text block holding the same JSON,
// because a client that predates structured content would otherwise see an
// empty response. They are the same bytes, so there is no second shape to keep
// in step.
func (s *Server) callTool(r *http.Request, params json.RawMessage) (any, *rpcError) {
	var p callParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "params is not a tools/call object"}
		}
	}
	if p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "no tool name"}
	}

	// The audit line, and it is written before the tool runs rather than
	// after: an operator asking "who asked what" needs the record of the
	// request, and a line written only on success is a record with the
	// interesting half missing. Arguments go through the redactor for the
	// same reason every response does -- they are text this process did not
	// author.
	args := strings.TrimSpace(string(p.Arguments))
	if args == "" {
		args = "{}"
	}
	s.logf("mcp: %s %s from %s", p.Name, redact.Text(args), remoteAddr(r))

	for _, t := range s.tools() {
		if t.Name != p.Name {
			continue
		}
		result, err := t.Call(p.Arguments)
		if err != nil {
			// Redacted here as well as on the way out, because this string
			// becomes a field of the response rather than the whole of it.
			return nil, &rpcError{Code: codeInternal, Message: redact.Text(err.Error())}
		}
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, &rpcError{Code: codeInternal, Message: "the result could not be serialized"}
		}
		return map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": string(raw)}},
			"structuredContent": result,
			"isError":           false,
		}, nil
	}
	return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool " + p.Name}
}

// remoteAddr is who asked, for the audit line.
//
// r.RemoteAddr and nothing else. A gateway's X-Forwarded-For would be more
// useful and is a header the caller sets, so a log that preferred it would be
// a log any client can write whatever it likes into -- and the reason to keep
// this record is that somebody may one day have to trust it.
func remoteAddr(r *http.Request) string {
	if r == nil || r.RemoteAddr == "" {
		return "unknown"
	}
	return r.RemoteAddr
}
