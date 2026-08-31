package mcp

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// This package can reach nothing, and that is a property of its import list
// rather than of its handlers.
//
// "Serving a request performs no git-host call, no cluster call and no model
// call" is asserted behaviourally too, with counting fakes, and that assertion
// covers only the paths a test drives. This one covers the paths nobody
// drives, because a package that cannot import a client cannot call one on any
// path at all -- and a compile-time rule is cheaper to audit than a filter.
//
// An allowlist and not a deny-list, deliberately. A deny-list has to name
// every package that can dial, and stays correct only while somebody keeps
// naming them; the first package added to this repository that can reach the
// network is exactly the one a deny-list written today does not mention.
var mayImport = map[string]string{
	"github.com/JamesAtIntegratnIO/bosun/pipeline": "the findings and their remedies, which is what this surface publishes",
	"github.com/JamesAtIntegratnIO/bosun/redact":   "the process redactor every response passes through",
}

// The standard library this package may reach.
//
// net/http is here because this is an HTTP server. That is not a loophole for
// a client: the behavioural test with counting fakes is what covers the
// difference, and a review that sees http.Get in this package has the whole
// question in front of it on one line.
var mayImportStdlib = map[string]bool{
	"bytes": true, "crypto/subtle": true, "encoding/json": true, "fmt": true,
	"net/http": true, "slices": true, "strings": true, "time": true,
	"unicode/utf8": true,
}

func TestThisPackageCannotReachTheOutsideWorld(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("could not parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: unreadable import %s", name, imp.Path.Value)
			}
			switch {
			case strings.HasPrefix(path, "github.com/JamesAtIntegratnIO/bosun/"):
				if _, ok := mayImport[path]; !ok {
					t.Errorf("%s imports %s.\n"+
						"This package may import only the result types and the redactor. Every "+
						"other package in this repository either dials something or is built on "+
						"one that does, and a read surface published beyond the cluster stays "+
						"read-only because it cannot reach a client rather than because its "+
						"handlers happen not to. If this import is genuinely safe, add it to "+
						"mayImport with the reason.", name, path)
				}
			case strings.Contains(path, "."):
				t.Errorf("%s imports the third-party package %s.\n"+
					"This surface has no third-party dependency and that is the reason the "+
					"official MCP SDK was not taken -- see this package's doc comment. Adding "+
					"one is a decision to make out loud.", name, path)
			default:
				if !mayImportStdlib[path] {
					t.Errorf("%s imports %s, which is not on this package's standard-library "+
						"allowlist.\n"+
						"Add it here if it is genuinely needed. The list exists so that os/exec, "+
						"net, and os are decisions somebody makes on purpose rather than "+
						"consequences of an editor's auto-import.", name, path)
				}
			}
		}
	}

	// The self-check, and not optional: a walk that reads no files compares
	// nothing against the allowlist and reads exactly like a pass.
	if checked < 4 {
		t.Fatalf("found only %d non-test files in this package; the walk is reading the "+
			"wrong directory and is proving nothing.", checked)
	}
}
