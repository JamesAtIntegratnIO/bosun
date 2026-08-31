package mcp

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden result shapes")

// The result shape is a golden file, so a schema cannot drift without a
// reviewer seeing the diff.
//
// Prior art is the gate's stamp and naming contract tests, which exist for the
// same reason: a format with two sides that must not drift. Here the second
// side is somebody else's agent, which this repository's CI cannot run, so a
// diff in a review is the only place the change can be noticed at all. A
// renamed field, an absence that became an empty array, a number that became a
// string -- every one of those is a silent break for a client, and every one
// of them shows up here as a line to explain.
func TestTheResultShapeIsWhatItWas(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		rep  func(t *testing.T) any
	}{
		{"a sweep that found something", "pipeline_report_wedged.json",
			func(t *testing.T) any { return newFixture(t, sweep(t, wedged())).call(t, "pipeline_report") }},
		{"a sweep that found nothing", "pipeline_report_clean.json",
			func(t *testing.T) any { return newFixture(t, sweep(t, healthy())).call(t, "pipeline_report") }},
		{"before the first sweep", "pipeline_report_unswept.json",
			func(t *testing.T) any { return newFixture(t, nil).call(t, "pipeline_report") }},
		{"a blocked pull request", "gate_verdict_blocked.json", func(t *testing.T) any {
			return newFixture(t, nil).withGate(blocked()).
				callWith(t, "gate_verdict", `{"pullRequest":264}`)
		}},
		{"a pull request the gate found nothing wrong with", "gate_verdict_green.json",
			func(t *testing.T) any {
				return newFixture(t, nil).withGate(green()).
					callWith(t, "gate_verdict", `{"pullRequest":41}`)
			}},
		{"a pull request the last sweep did not see", "gate_verdict_absent.json",
			func(t *testing.T) any {
				return newFixture(t, nil).withGate(green()).
					callWith(t, "gate_verdict", `{"pullRequest":999}`)
			}},
		{"before the first gate sweep", "gate_verdict_unswept.json", func(t *testing.T) any {
			return newFixture(t, nil).callWith(t, "gate_verdict", `{"pullRequest":264}`)
		}},
		{"the tool set", "tools_list.json", func(t *testing.T) any {
			f := newFixture(t, nil)
			_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			return json.RawMessage(body)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, ok := tc.rep(t).(json.RawMessage)
			if !ok {
				t.Fatal("the fixture did not return raw JSON")
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, raw, "", "  "); err != nil {
				t.Fatalf("the result is not JSON: %v", err)
			}
			got := append(pretty.Bytes(), '\n')

			path := filepath.Join("testdata", tc.file)
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v\nRun `go test ./mcp -update` to write it, and read the diff before "+
					"committing: this file is a published contract.", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("the result shape changed.\n\nwant:\n%s\ngot:\n%s\n\n"+
					"If this is deliberate, run `go test ./mcp -update` and say in the pull "+
					"request what a client that already parses this will do with the change. "+
					"A field that used to be absent and is now an empty array is not a "+
					"compatible change on this surface: absence is what says nothing has "+
					"looked yet.", want, got)
			}
		})
	}
}

// Tool and parameter descriptions are constants.
//
// A description is the field a client hands its model as instructions, so a
// Stage name, a chart-rendered object name or an error string interpolated
// into one is text from a cluster arriving at the most trusted point in the
// exchange -- and it would arrive there having passed every other rule this
// surface has, because a description is not a result.
//
// Derived from this package's own syntax tree rather than from a list here: a
// second tool is covered the moment it is registered, and the failure names
// the field.
func TestEveryToolDescriptionIsAConstant(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("could not parse this package: %v", err)
	}
	pkg, ok := pkgs["mcp"]
	if !ok {
		t.Fatal("this walk did not find package mcp; it is reading the wrong directory")
	}

	// Every package-level name whose value is written here rather than
	// computed, so a description or a schema can be checked against it rather
	// than against a list in this file.
	//
	// A const if it can be one, and a var initialised from a literal where the
	// type forbids a const -- a JSON Schema is a json.RawMessage, which Go
	// cannot declare as a constant. What matters for the rule is that the
	// bytes are in the source, not which keyword introduced them.
	written := map[string]bool{}
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, n := range vs.Names {
					switch {
					case gen.Tok == token.CONST:
						written[n.Name] = true
					case i < len(vs.Values) && literal(vs.Values[i]):
						written[n.Name] = true
					}
				}
			}
		}
	}

	checked := 0
	// Every Tool literal, whether it spells its own type or has it elided by
	// the []Tool{{...}} the registry is written as. Missing the elided form is
	// how this walk would find nothing while looking straight at the tools.
	toolLiterals := func(file *ast.File) []*ast.CompositeLit {
		var out []*ast.CompositeLit
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			switch typ := lit.Type.(type) {
			case *ast.Ident:
				if typ.Name == "Tool" {
					out = append(out, lit)
				}
			case *ast.ArrayType:
				if id, ok := typ.Elt.(*ast.Ident); ok && id.Name == "Tool" {
					for _, elt := range lit.Elts {
						if inner, ok := elt.(*ast.CompositeLit); ok {
							out = append(out, inner)
						}
					}
				}
			}
			return true
		})
		return out
	}

	for _, file := range pkg.Files {
		for _, lit := range toolLiterals(file) {
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || (key.Name != "Description" && key.Name != "Params") {
					continue
				}
				checked++
				switch v := kv.Value.(type) {
				case *ast.BasicLit:
					// A literal in place is a constant by construction.
				case *ast.Ident:
					if !written[v.Name] {
						t.Errorf("a tool's %s is %s, whose value is not written in this "+
							"package's source.\n"+
							"A description and an input schema are handed to a model as "+
							"instructions, so both have to be literals here rather than "+
							"assembled from anything the process read.", key.Name, v.Name)
					}
				default:
					t.Errorf("a tool's %s is an expression rather than a constant (%T at %s).\n"+
						"Nothing from the cluster or the repository may reach the field a "+
						"client treats as instructions.", key.Name, kv.Value, fset.Position(kv.Value.Pos()))
				}
			}
		}
	}

	// The self-check, and not optional. If tools are ever registered a
	// different way -- a map, a builder, a generated table -- this walk finds
	// nothing and reports a pass over a surface it never read.
	if checked < 2 {
		t.Fatalf("found only %d description or schema fields in this package's Tool "+
			"literals; tools are registered differently now and this walk no longer sees "+
			"them. Fix the walk rather than lowering this number.", checked)
	}
}

// And the same rule asserted from outside, where the shape of the registration
// cannot hide anything.
//
// The syntax walk above proves the descriptions are constants in this build.
// This proves the published tool list carries nothing from the world the sweep
// read, whatever route it might have taken to get there.
func TestTheToolListCarriesNothingFromTheCluster(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))
	_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	// Everything the fixture's world contains that a client would recognise.
	for _, fromTheWorld := range []string{
		"external-secrets", "argo-cd", "addons", "f08f1c9",
		"server misbehaving", "chore(deps)", "example/platform",
	} {
		if bytes.Contains(body, []byte(fromTheWorld)) {
			t.Errorf("the tool list carries %q, which came from the cluster or the "+
				"repository:\n%s", fromTheWorld, body)
		}
	}
	if !bytes.Contains(body, []byte("pipeline_report")) {
		t.Fatalf("the tool list is empty, so the assertions above ran against nothing:\n%s", body)
	}
}

// The handshake a client actually performs.
//
// Written out because this package speaks the protocol by hand: there is no
// SDK behind it whose own tests would notice a malformed initialize result,
// and the symptom of one is a client that connects and lists no tools.
func TestTheHandshakeAClientPerforms(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))

	code, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"initialize",`+
		`"params":{"protocolVersion":"2025-06-18","capabilities":{},`+
		`"clientInfo":{"name":"probe","version":"1"}}}`)
	if code != http.StatusOK {
		t.Fatalf("initialize answered %d: %s", code, body)
	}
	var init struct {
		ID     json.Number `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools *struct{} `json:"tools"`
			} `json:"capabilities"`
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &init); err != nil {
		t.Fatalf("initialize did not answer JSON-RPC: %v\n%s", err, body)
	}
	if init.ID.String() != "1" {
		t.Errorf("a response must echo the id it was sent, got %q", init.ID)
	}
	if init.Result.ProtocolVersion != protocolVersion {
		t.Errorf("want protocol %s, got %q", protocolVersion, init.Result.ProtocolVersion)
	}
	if init.Result.Capabilities.Tools == nil {
		t.Error("a server that serves tools has to declare the capability, or a client lists none")
	}
	if init.Result.ServerInfo.Name != serverName || init.Result.ServerInfo.Version != "0.31.0" {
		t.Errorf("the handshake must name the build that answered, got %+v", init.Result.ServerInfo)
	}

	// The notification every client sends next. A body in reply to it is a
	// protocol error rather than a harmless extra.
	code, body = f.post(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if code != http.StatusAccepted {
		t.Errorf("a notification is answered with 202 and no body, got %d: %s", code, body)
	}
	if len(bytes.TrimSpace(body)) != 0 {
		t.Errorf("a notification must be answered with no body, got %s", body)
	}
}

// The transport's refusals, which are the ones a hand-written handler gets
// wrong.
func TestTheTransportRefusesWhatItShould(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))

	// No server-initiated stream: every tool answers from a snapshot, so a
	// GET that opened an idle SSE stream would be a connection held open to
	// receive nothing.
	req, _ := http.NewRequest(http.MethodGet, f.http.URL+EndpointPath, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET must be refused, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Allow") != http.MethodPost {
		t.Errorf("a 405 has to say what is allowed, got %q", resp.Header.Get("Allow"))
	}

	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"not JSON", `{`, codeParse},
		{"no version", `{"id":1,"method":"tools/list"}`, codeInvalidRequest},
		{"no method", `{"jsonrpc":"2.0","id":1}`, codeInvalidRequest},
		{"unknown method", `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`, codeMethodNotFound},
		{"no tool name", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`, codeInvalidParams},
		{"unknown tool", `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rm"}}`, codeInvalidParams},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, body := f.post(t, tc.body)
			var resp struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				t.Fatalf("the refusal is not JSON-RPC: %v\n%s", err, body)
			}
			if resp.Error == nil {
				t.Fatalf("want a JSON-RPC error, got %s", body)
			}
			if resp.Error.Code != tc.code {
				t.Errorf("want code %d, got %d (%s)", tc.code, resp.Error.Code, resp.Error.Message)
			}
		})
	}
}

// A tool result travels as structured content AND as text, and they are the
// same bytes.
//
// The typed value is what this surface exists to publish; the text block is
// what a client that predates structured content reads, and a client seeing
// two different answers depending on which field it looked at would be worse
// than one seeing neither.
func TestAToolResultIsTheSameInBothPlaces(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))
	_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call",`+
		`"params":{"name":"pipeline_report","arguments":{}}}`)

	var resp struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StructuredContent json.RawMessage `json:"structuredContent"`
			IsError           bool            `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	if len(resp.Result.Content) != 1 || resp.Result.Content[0].Type != "text" {
		t.Fatalf("want one text block, got %+v", resp.Result.Content)
	}
	if resp.Result.IsError {
		t.Error("a successful call is not an error")
	}

	var fromText, fromStructured any
	if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &fromText); err != nil {
		t.Fatalf("the text block is not the result as JSON: %v", err)
	}
	if err := json.Unmarshal(resp.Result.StructuredContent, &fromStructured); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fromText)
	b, _ := json.Marshal(fromStructured)
	if !bytes.Equal(a, b) {
		t.Errorf("the two copies of one result disagree.\ntext: %s\nstructured: %s", a, b)
	}
}

// literal reports whether an expression's value is written in the source
// rather than computed from anything.
//
// A conversion counts, because a JSON Schema is a json.RawMessage and Go has
// no constant of that type: `json.RawMessage("{...}")` is a literal wearing a
// type. A call that is not a conversion does not, which is the case this
// exists to refuse.
func literal(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return true
	case *ast.BinaryExpr: // a long string, split across lines with +
		return literal(v.X) && literal(v.Y)
	case *ast.CallExpr:
		return len(v.Args) == 1 && literal(v.Args[0])
	}
	return false
}
