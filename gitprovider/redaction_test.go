package gitprovider

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// Every git command this package hands a credential redacts what git printed.
//
// This is the drift the helper it replaces was written to stop, and its comment
// named the incident: gitea called the helper, github inlined two ReplaceAll
// calls, and only one of them was reviewed the last time the rules changed. Two
// providers doing the same thing two ways is one of them being wrong, silently,
// until a host echoes a token back into a pull-request comment.
//
// The rule is derived from the mechanism rather than from a list of call sites.
// pushAuthEnv is how a credential reaches git in this package -- it is in the
// environment, never in argv and never in the remote URL -- so a function that
// calls it is a function whose subprocess was given a secret, and whatever that
// subprocess wrote to stderr is what must not be forwarded unread. The stderr
// buffer is found the same way: whatever was assigned to the command's Stderr
// field, whatever it happens to be named.
//
// Deliberately silent about the git commands that carry no credential --
// EnsureHead's fetch, mergebase's ladder. They quote git's stderr too, and
// nothing in them was ever given a secret to echo.
func TestEveryCredentialHandlingCommandRedactsItsStderr(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}

	guarded, checked := 0, 0
	for _, path := range files {
		if len(path) > 8 && path[len(path)-8:] == "_test.go" {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("could not parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !calls(fn.Body, "pushAuthEnv") {
				continue
			}
			reads := stderrReads(fn.Body)
			if len(reads) == 0 {
				continue
			}
			guarded++
			safe := redactedArgs(fn.Body)
			for _, r := range reads {
				checked++
				if safe[r.Pos()] {
					continue
				}
				t.Errorf("%s: %s hands git a credential through pushAuthEnv and then "+
					"quotes what git wrote to stderr without passing it through redact.Text.\n"+
					"git repeats whatever the server says, and a misconfigured host can echo a "+
					"credential it was sent, so this text reaches a log and a pull-request "+
					"comment with the token still in it.", path, fn.Name.Name)
			}
		}
	}

	// The self-check, and not optional. Both providers push, so anything less
	// than two means the walk has stopped seeing what it reads -- a renamed
	// pushAuthEnv, a stderr buffer captured some other way -- and a test that
	// checked nothing reports exactly like a test that passed.
	if guarded < 2 || checked < 2 {
		t.Fatalf("found %d credential-handling commands and %d stderr reads among them; "+
			"both providers push, so this walk is no longer seeing them. Fix the walk -- "+
			"do not lower these numbers.", guarded, checked)
	}
}

// calls reports whether n contains a call to the function named name.
func calls(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// stderrReads is every call that reads a buffer the function gave a subprocess
// to write its stderr into.
//
// Two steps, because the buffer's name is not the contract: first find what was
// assigned to a command's Stderr field, then find the reads of it. A rule
// keyed on the identifier `stderr` would pass the day somebody named one
// `errOut`.
func stderrReads(body *ast.BlockStmt) []ast.Node {
	buffers := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Stderr" || i >= len(assign.Rhs) {
				continue
			}
			rhs := assign.Rhs[i]
			if unary, ok := rhs.(*ast.UnaryExpr); ok {
				rhs = unary.X
			}
			if id, ok := rhs.(*ast.Ident); ok {
				buffers[id.Name] = true
			}
		}
		return true
	})

	var reads []ast.Node
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "String" && sel.Sel.Name != "Bytes") {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && buffers[id.Name] {
			reads = append(reads, call)
		}
		return true
	})
	return reads
}

// redactedArgs is the position of every expression handed to redact.Text, which
// is what a stderr read has to be to count as handled.
func redactedArgs(body *ast.BlockStmt) map[token.Pos]bool {
	safe := map[token.Pos]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Text" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "redact" {
			return true
		}
		for _, arg := range call.Args {
			safe[arg.Pos()] = true
		}
		return true
	})
	return safe
}
