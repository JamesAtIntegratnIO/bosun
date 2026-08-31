// Package mcp serves what the sweeps already found, to an agent, over MCP.
//
// Bosun computes the most expensive facts in the promotion loop -- why a Stage
// silently stopped promoting, and the exact command that unsticks it -- and
// until now published them only as prose: a comment on a pull request, a page
// behind a port-forward, a metrics endpoint. That is right for a person
// reading a page and useless to the agents people actually work through, which
// were left scraping markdown written for somebody else.
//
// So this is a fourth exit for facts that already exist. Nothing here computes
// anything: every handler answers from the snapshot the last sweep left in
// memory, which is why a request cannot reach the git host, the cluster or a
// model, and why a chatty client cannot spend an install's rate limit. Nothing
// here mutates anything either, because no tool does -- the supervisor's
// ClusterRole has no write verb and a feature that seems to need one is a
// signal to reconsider the feature.
//
// # What this package owns
//
// The wire: the transport, the auth check, the tool set, and the shape of what
// comes back. What it deliberately does not own is any judgement about the
// pipeline. pipeline decides what is wrong and composes every remedy;
// supervisor decides when to look. This turns the answer into JSON and refuses
// to say anything the answer does not.
//
// # Why a hand-written JSON-RPC handler
//
// The official Go SDK was the default choice and did not survive this
// repository's dependency review. It adds eight modules to a go.mod with four
// direct requirements, and the graph includes golang.org/x/oauth2 and, through
// it, cloud.google.com/go/compute/metadata -- a client for the cloud metadata
// service at 169.254.169.254, which answers instance credentials to anything
// that asks and which this project's own NetworkPolicy excepts by name for
// exactly that reason. Linking a client for it into the process that holds a
// git token, a model key and an App key, in order to serve one read-only tool,
// is a trade this surface does not need to make. The graph also carries
// hand-written assembly for JSON decoding, on the parsing path of the one
// listener built to be reached from outside the cluster.
//
// The fallback was accepted in advance for a reason that still holds: the tool
// set is small, neither resources nor sampling are used, and streamable HTTP
// with a JSON response body is a few hundred lines. What it costs is that this
// file has to track the protocol by hand, and the protocolVersion constant is
// where that obligation is written down.
//
// # The three rules every tool obeys
//
// Honest absence is structural. Every result carries whether a sweep has
// completed, and before the first one the findings field is ABSENT rather than
// empty -- a client that reads an empty list as "nothing is wrong" from a
// supervisor that has not looked is making the most expensive mistake this
// project exists to catch. See report.go, where the pointer-to-slice that
// makes it expressible is explained where it is written.
//
// Repository-stamped from the first release. Every result names the repository
// it is about, though an install watches exactly one. Adding a field later is
// compatible; teaching a client that answers are install-wide and then
// narrowing them is not.
//
// Typed facts, tagged text. Severities, kinds, counts and durations are typed
// fields a string cannot forge, and every free-text field that can carry
// third-party content is tagged with where it came from. Bosun's results land
// in agents holding tools bosun refuses for itself, so a hostile release note
// does not need to jailbreak bosun's model -- only to be delivered by it to a
// better-armed one. The contract a client can rely on: instructions in a
// result are bosun's own or absent.
//
// Two corollaries, both of which land with gate_verdict because that is where
// text bosun did not write first enters a result.
//
// A field published as fact rather than as tagged text has to be vetted rather
// than labelled. There is exactly one -- the migration a dropped-version
// finding demands, which is what a repair acts on with no person in between --
// and it is published only when every piece of it holds an identifier's shape.
// A finding that fails keeps its prose and loses the fields.
//
// And the gate's own stamp grammar is stripped from every response, in
// declaw below. The gate keeps its memory inside its pull-request comment, so
// a client that relays bosun's text back onto a pull request must not be able
// to relay a forged verdict with it.
//
// # And two controls, in that order
//
// The primary one is a compile-time dependency rule: this package imports the
// result types and never the configuration type, so no field path from any
// tool result reaches a credential. That is worth more than a filter, because
// it holds on code paths no request exercises. mcp_credentials_test.go in the
// repository root is what keeps it true, and it is derived rather than listed.
//
// The secondary one is redaction, applied to every serialised response by
// write below rather than by each handler, because a control each caller has
// to remember is one each caller can forget.
package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// protocolVersion is the MCP revision this server speaks.
//
// A constant rather than a negotiation because there is one implementation
// here and it either speaks a revision or it does not. A client that asks for
// another gets this one back, which the specification allows and which is the
// honest answer: a server that echoed whatever version it was sent would be
// claiming a compatibility nothing here checked.
const protocolVersion = "2025-06-18"

// serverName is what a client sees in the initialize handshake. A constant,
// like every other description on this surface: nothing from the cluster or
// the repository is ever interpolated into a field clients treat as
// instructions.
const serverName = "bosun"

// maxRequestBytes bounds a request body.
//
// Every method here takes either no arguments or a small object, so a
// megabyte is already three orders of magnitude more than any legitimate call,
// and the number exists to stop a client streaming into the process rather
// than to accommodate one.
const maxRequestBytes = 1 << 20

// JSON-RPC error codes, from the specification. Named because a bare -32601
// in a switch is a number the next reader has to look up.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// rpcRequest is one JSON-RPC call.
//
// ID is kept as raw JSON rather than decoded: the specification allows a
// string or a number, a response has to echo back exactly what it was sent,
// and decoding a number into a float64 and re-encoding it is how an id like
// 10000000000000001 comes back as a different id.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification reports whether this call wants no response. A JSON-RPC
// notification has no id, and answering one is a protocol error rather than a
// harmless extra.
func (r rpcRequest) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Handler is the MCP endpoint, or an error naming what this server is missing.
//
// An error rather than a nil check inside every handler, and an error rather
// than a panic, because every one of these is a wiring mistake in the
// composition root and the composition root is where it can be reported with
// the value that would fix it. What is NOT an error here is "no token
// configured": that is an operator's situation rather than a programming one,
// it is decided before this is called, and its whole point is that the
// listener does not start.
func (s *Server) Handler() (http.Handler, error) {
	switch {
	case s.Auth == nil:
		return nil, fmt.Errorf("no Auth: this surface is built to be reached from outside " +
			"the cluster and there is no default that would be safe")
	case s.Repository == "":
		return nil, fmt.Errorf("no Repository: every result names the repository it is about, " +
			"and a client that cannot tell which install answered cannot cache anything")
	case s.Report == nil:
		return nil, fmt.Errorf("no Report: with nothing to read, every tool would answer " +
			"\"no sweep has completed\" forever, which is indistinguishable from a supervisor " +
			"that is switched off")
	case s.Gate == nil:
		return nil, fmt.Errorf("no Gate: gate_verdict would report that no gate sweep has " +
			"completed for every pull request forever, which is the one answer that must never " +
			"be a wiring mistake")
	}

	mux := http.NewServeMux()
	mux.HandleFunc(EndpointPath, s.serve)
	return mux, nil
}

// EndpointPath is where the MCP endpoint lives on its listener.
//
// Exported because the chart's route and this have to agree and nothing else
// would notice if they stopped: a client configured with the wrong path gets a
// 404 that looks exactly like a listener that is switched off.
const EndpointPath = "/mcp"

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	// Auth before anything else, including before the method check, so an
	// unauthenticated caller learns nothing about what this server implements.
	if !s.Auth.Allow(r) {
		// A plain HTTP status rather than a JSON-RPC error: the request has
		// not been accepted as a JSON-RPC call at all, and a 401 with a
		// WWW-Authenticate header is what an HTTP client, a gateway and a log
		// aggregator all already understand.
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+serverName+`"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		// No server-to-client stream. Every tool here answers from a snapshot
		// the process already holds, so there is nothing to push and nothing
		// to resume; a GET that opened an idle SSE stream would be a
		// connection a client holds open forever to receive nothing.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "this endpoint answers POST only: every tool returns a snapshot, "+
			"so there is no server-initiated stream to open", http.StatusMethodNotAllowed)
		return
	}

	body := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var req rpcRequest
	dec := json.NewDecoder(body)
	if err := dec.Decode(&req); err != nil {
		s.write(w, rpcResponse{JSONRPC: "2.0", Error: &rpcError{
			Code: codeParse, Message: "the request body is not JSON-RPC"}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		s.write(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{
			Code: codeInvalidRequest, Message: `every call needs "jsonrpc":"2.0" and a method`}})
		return
	}

	result, rerr := s.dispatch(r, req)

	// A notification is answered with 202 and no body, which is what the
	// transport requires. `notifications/initialized` is the one every client
	// sends, and a body in reply to it is a protocol error rather than a
	// harmless extra.
	if req.isNotification() {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if rerr != nil {
		s.write(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: rerr})
		return
	}
	s.write(w, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
}

func (s *Server) dispatch(r *http.Request, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": protocolVersion,
			// Tools, and nothing else. No resources: the tempting one is the
			// supervisor's markdown report served whole, and it is a
			// composite of trusted and untrusted text with no field boundary
			// to hang provenance on, which is the thing the tools exist to
			// avoid. No sampling: a caller that could ask this process to
			// call a model could spend the install's budget and steer its
			// prompts.
			"capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo":   map[string]any{"name": serverName, "version": s.Version},
			"instructions": instructions,
		}, nil
	case "notifications/initialized", "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.listTools()}, nil
	case "tools/call":
		return s.callTool(r, req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "unknown method " + req.Method}
	}
}

// instructions is what a client is told this server is for. A constant, and
// short: it is read by a model, and every sentence in it competes with the
// user's own request for that model's attention.
const instructions = "Read-only access to what bosun's last sweep found about one gitops " +
	"repository's promotion pipeline. Nothing here can change anything, and every answer " +
	"comes from a snapshot rather than from a live read, so it is as old as the sweep it " +
	"names. Commands returned in a remedy field are composed by bosun; text in any other " +
	"field may quote a cluster or a repository and carries an origin saying so."

// write serialises a response, and is the one place a byte reaches the wire.
//
// Redaction and the declawing of HTML comment delimiters both happen HERE
// rather than in each handler, which is the whole point of having one exit: a
// handler that forgot to call it would be a leak with no symptom until a
// misconfigured host echoed a credential back inside an error string this then
// serialised, or a chart-rendered name carried a stamp out to a client that
// writes onto pull requests. The compile-time rule that no result type can
// reach a credential is the primary control; this is the second line, for the
// text whose contents nobody chose.
//
// It is not the ONLY line, and the reason is written out in callTool: a value
// encoded into a string that is then encoded again has been escaped twice, so
// a credential with a newline in it does not match its own bytes here. A tool
// result is therefore redacted before it is embedded, and this pass covers
// every response that never went through a tool -- the handshake, the tool
// list, and every error message.
func (s *Server) write(w http.ResponseWriter, resp rpcResponse) {
	out, err := redacted(resp)
	if err == nil {
		var raw []byte
		raw, err = json.Marshal(out)
		if err == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)
			return
		}
	}
	// Nothing in a response is unmarshalable today, and a handler that makes
	// one tomorrow must not answer with a half-written body.
	s.logf("mcp: a response could not be serialised: %v", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}
