package mcp

import (
	"encoding/json"
	"fmt"
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

	// Gate is what the gate's last sweep saw, or the zero value before the
	// first one.
	//
	// A function for the same reason Report is: the sweep replaces its
	// snapshot on every pass, and a copy taken at wiring time would answer
	// with the state of the world at start-up forever. A zero SweptAt is not
	// an error, it is the "no sweep has completed" case, and it is the case
	// this surface is most careful about.
	Gate func() GateStatus

	// Triage is what the agent is doing right now, or the zero value when
	// nothing is wired.
	//
	// A function for the reason Report and Gate are: it changes on every
	// promotion, and a copy taken at wiring time would report the work in
	// flight at start-up forever. The zero value is an agent working nothing,
	// which is both the common case and the honest answer.
	Triage func() TriageStatus

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

// stamp is when a sweep finished and how long ago, for the three fields every
// result carries: swept, sweptAt, ageSeconds.
//
// One copy of it, because there is one clock guard and a handler per tool. A
// negative age is answered as zero rather than published: a clock that went
// backwards between the sweep and the request is a machine's problem, and
// "-3 seconds old" is a number every client would have to write the same
// special case for. A copy per handler is how all but one of them keep it.
//
// The zero time is the before-the-first-sweep case and reports false, which is
// what makes every caller's absent fields absent for the same reason.
func (s *Server) stamp(at time.Time) (*time.Time, *int64, bool) {
	if at.IsZero() {
		return nil, nil, false
	}
	age := int64(s.now().Sub(at).Seconds())
	if age < 0 {
		age = 0
	}
	return &at, &age, true
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
// One list, built here, rather than a registry a tool adds itself to: the
// value of being able to read the whole surface in one place is higher than
// the value of letting a file register itself, and it goes up rather than down
// as tools are added -- what an operator publishing this port is deciding
// about is this list.
func (s *Server) tools() []Tool {
	return []Tool{{
		Name:        "pipeline_report",
		Description: pipelineReportDescription,
		Params:      noArguments,
		Result:      Report{},
		Call:        func(json.RawMessage) (any, error) { return s.pipelineReport(), nil },
	}, {
		Name:        "gate_status",
		Description: gateStatusDescription,
		Params:      noArguments,
		Result:      Queue{},
		Call:        s.gateStatus,
	}, {
		Name:        "gate_verdict",
		Description: gateVerdictDescription,
		Params:      gateVerdictParams,
		Result:      Verdict{},
		Call:        s.gateVerdict,
	}, {
		Name:        "triage_status",
		Description: triageStatusDescription,
		Params:      triageStatusParams,
		Result:      Triage{},
		Call:        s.triageStatus,
	}, {
		Name:        "verdict_history",
		Description: verdictHistoryDescription,
		Params:      verdictHistoryParams,
		Result:      History{},
		Call:        s.verdictHistory,
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
// empty response.
//
// Which is why the redaction happens HERE as well as at the wire, and why both
// copies are made from the redacted value rather than from the raw one. The
// text block is a JSON document inside a JSON string, so by the time write
// serialises the response every character in it has been escaped TWICE: a
// credential containing a newline reaches the wire spelled `\\n`, and the
// redactor at the wire compares against the credential's own bytes and does
// not match it. A flat token has no escapes and was redacted correctly, which
// is exactly what made this the kind of leak a happy-path test misses -- and
// a GitHub App private key, the credential this most matters for, is the one
// with newlines in it.
//
// So the tree is redacted once, before it is encoded at all, and everything
// downstream is made from what comes back. write still runs its own pass, for
// the responses that never come through here.
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
	//
	// Both are QUOTED, and that is the whole point of the record. The tool
	// name and the arguments are chosen by the caller, and a caller that can
	// put a newline in either can write whatever it likes on the next line of
	// a log an operator is reading to find out who asked what. %q escapes it
	// to one line; a log worth keeping is one a caller cannot forge entries in.
	args := strings.TrimSpace(string(p.Arguments))
	if args == "" {
		args = "{}"
	}
	s.logf("mcp: tool %q args %q from %s", p.Name, redact.Text(args), remoteAddr(r))

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
		clean, err := redacted(result)
		if err != nil {
			return nil, &rpcError{Code: codeInternal, Message: "the result could not be serialised"}
		}
		raw, err := json.Marshal(clean)
		if err != nil {
			return nil, &rpcError{Code: codeInternal, Message: "the result could not be serialised"}
		}
		return map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": string(raw)}},
			"structuredContent": clean,
			"isError":           false,
		}, nil
	}
	return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool " + p.Name}
}

// pullRequestArgs is the argument every per-pull-request tool takes.
//
// One type and one reading of it, rather than one per tool: the two tools that
// take these are asking about the same object with the same words, and a
// second copy of the parse is a second answer to "is 0 a pull request" waiting
// to be given.
type pullRequestArgs struct {
	PullRequest int    `json:"pullRequest"`
	Repository  string `json:"repository"`
}

// pullRequest is the number a per-pull-request tool was asked about, or the
// refusal to hand back.
//
// pullRequest is required and repository is not, and the asymmetry is the
// decision. An install binds to one repository, so the argument exists to be
// stamped rather than to be chosen; making it optional now and meaningful
// later is compatible, while teaching a client that answers are install-wide
// and then narrowing them is not.
func (s *Server) pullRequest(raw json.RawMessage) (int, error) {
	var args pullRequestArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return 0, fmt.Errorf("arguments are not an object with a pullRequest number")
		}
	}
	if args.PullRequest < 1 {
		return 0, fmt.Errorf("pullRequest must be the number of a pull request")
	}
	if args.Repository != "" && args.Repository != s.Repository {
		// The caller's own string is deliberately not echoed back. It would
		// travel through the redactor and the comment stripper like anything
		// else, and there is still no reason to put text this process did not
		// author into a message a client renders when it has nothing to say
		// with it. Naming the install's own repository answers the question
		// the caller was actually asking.
		return 0, fmt.Errorf("this install watches %s and nothing else", s.Repository)
	}
	return args.PullRequest, nil
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
