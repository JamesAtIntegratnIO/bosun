package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// No stamp the gate publishes survives a trip through the MCP surface.
//
// The gate has no database, so its memory lives inside its own pull-request
// comment: the verdict it last reached, the head commit it judged, and the
// migration a repair is to perform all travel as HTML comments and are read
// back on the next run. That makes them forgeable by anything that can get
// bytes into that comment -- and a client of the MCP surface is one of those
// things, because it reads a verdict here and writes prose onto a pull
// request. A stamp smuggled through a chart-rendered object name would come
// back out of that client as a verdict the gate never reached.
//
// mcp breaks the delimiters rather than matching the stamps, precisely so that
// a seventh stamp needs no change there. This is what proves the coverage, and
// it derives the stamps from the source rather than listing them, so a stamp
// added anywhere in this module is one this test starts checking the same day.

func TestNoStampTheGatePublishesSurvivesTheToolSurface(t *testing.T) {
	stamps := publishedStamps(t)

	for _, stamp := range stamps {
		t.Run(stamp, func(t *testing.T) {
			// A stamp with a payload behind it and a terminator after it: the
			// whole forgery, not just its opening bytes.
			forged := stamp + "0000000000000000000000000000000000000000 1 forged -->"

			body := verdictThroughTheWire(t, forged)
			if bytes.Contains(body, []byte(stamp)) {
				t.Errorf("the stamp %q reached a client verbatim. A client that relays this "+
					"onto a pull request republishes the forgery, and the next gate run reads "+
					"it back as its own memory.\n%s", stamp, body)
			}
			// The delimiters are what the gate's own reader anchors on, so
			// neither may survive in any form.
			for _, delimiter := range []string{"<!--", "-->"} {
				if bytes.Contains(body, []byte(delimiter)) {
					t.Errorf("%q reached a client, so a comment can still be closed or "+
						"opened by relayed text:\n%s", delimiter, body)
				}
			}
			// And the attempt is still legible, because a name with an HTML
			// comment in it has no innocent explanation and is worth somebody
			// looking at the chart that produced it.
			if !bytes.Contains(body, []byte("gitops-gate")) {
				t.Errorf("the text was dropped rather than declawed, which loses the evidence "+
					"that something tried:\n%s", body)
			}
		})
	}
}

// verdictThroughTheWire serves one verdict whose every free-text field carries
// text, over the real handler, and returns the response.
//
// The real handler and not the mapping function, because what is under test is
// what a client receives: the redaction and the declawing happen where a byte
// reaches the wire rather than in each handler, and a test that called the
// mapping would miss exactly that.
func verdictThroughTheWire(t *testing.T, text string) []byte {
	t.Helper()

	srv := &mcp.Server{
		Repository: "example/platform",
		Report:     func() *pipeline.Report { return nil },
		Auth:       mcp.Unauthenticated{},
		Now:        func() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) },
		Gate: func() mcp.GateStatus {
			return mcp.GateStatus{
				SweptAt: time.Date(2026, 8, 30, 11, 59, 0, 0, time.UTC),
				Open: []mcp.GatePR{{
					Number: 264, Title: text, HeadSHA: "9f2c1a4b", State: mcp.StateFailing,
					Verdict: &mcp.GateVerdict{
						Blocking: true, Headline: "Blocking — 1 setting this bump stops reading",
						Blockers: mcp.GateBlockers{ValuesDropped: 1},
						Findings: []mcp.GateFinding{{
							Kind: "valuesDropped", Count: 1, Blocking: true,
							RepositorySideRemedy: true,
							Subject:              text, Detail: text, Reason: text,
							Cluster: text, Keys: []string{text}, ConsumerFiles: []string{text},
						}},
						NotCovered: []string{text},
					},
				}},
			}
		},
	}
	h, err := srv.Handler()
	if err != nil {
		t.Fatalf("the handler would not build: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+mcp.EndpointPath, bytes.NewReader([]byte(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"gate_verdict","arguments":{"pullRequest":264}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	// A tool result travels twice, and the second copy is escaped one level
	// deeper. Assert against the raw body so both are covered by one search,
	// the same reason the redaction test spells a secret several ways.
	var probe struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(buf.Bytes(), &probe); err != nil {
		t.Fatalf("%v\n%s", err, buf.Bytes())
	}
	if len(probe.Result.StructuredContent) == 0 {
		t.Fatalf("the call returned no verdict, so nothing above was checked:\n%s", buf.Bytes())
	}
	return buf.Bytes()
}

// publishedStamps is every HTML-comment stamp this module writes into a
// pull-request comment, read from the source rather than listed.
//
// Derived because a hand-written list is what the ClusterRole was fixed with
// and how it stayed broken: five entries, with nothing forcing a sixth. A
// stamp is a package-level string constant whose value opens an HTML comment
// and names this gate, and there is nowhere else in this module that shape
// occurs.
func publishedStamps(t *testing.T) []string {
	t.Helper()
	root := helmtest.Root(t)

	found := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The module's own Go packages, and nothing under a directory
			// that holds somebody else's: a vendored or cached copy would
			// contribute stamps this module does not publish.
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "site" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("%s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, unquoteErr := strconv.Unquote(lit.Value)
					if unquoteErr != nil {
						continue
					}
					if strings.HasPrefix(s, "<!--") && strings.Contains(s, "gitops-gate") {
						found[s] = true
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	out := make([]string, 0, len(found))
	for s := range found {
		out = append(out, s)
	}
	sort.Strings(out)

	// The self-check, and not optional. This module publishes six stamps
	// today -- the report marker, the head, the verdict, the historical
	// verdict, the blockers breakdown and the migration contract -- and a walk
	// that found none would leave the loop above iterating an empty list and
	// reporting a pass over a surface it never read.
	if len(out) < 6 {
		t.Fatalf("found only %d published stamps (%v); they are written differently now and "+
			"this walk no longer reads them. Fix the walk rather than lowering this number.",
			len(out), out)
	}
	return out
}
