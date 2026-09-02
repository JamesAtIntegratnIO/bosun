package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// Every git subprocess in this process redacts what git wrote to stderr.
//
// The rule this replaces was narrower, and its own comment said so: it
// qualified a function only if it called pushAuthEnv, on the reasoning that a
// push is where a credential reaches git, and it named EnsureHead's fetch and
// mergebase's ladder as deliberately out of scope because "nothing in them was
// ever given a secret to echo".
//
// That last clause was wrong, and the thing that makes it wrong is one
// environment variable. Every one of those commands runs against `origin`, and
// origin's URL is GIT_REPO_URL verbatim -- agent/triage.go and
// gateservice/checkout.go and supervisor/supervisor.go each clone from it, and
// gitprovider strips the userinfo out of the push remote precisely because an
// operator may have put a credential in it. An install configured that way has
// handed a secret to every git command here, not through pushAuthEnv and not
// in argv, but in the conversation with the remote -- which is the channel the
// original reasoning was always about. git repeats what the server says.
//
// So the rule is now the mechanism with nothing left of the list: a function
// that starts `git` and then reads the buffer it gave that subprocess for its
// stderr has to pass what it read through redact.Text. Not "a function that
// pushes", not the five call sites that exist today. Both halves are found by
// syntax -- the command by its literal name, the buffer by whatever was
// assigned to the command's Stderr field, whatever it happens to be called --
// so a sixth call site is covered the day it is written.
//
// Where the derivation stops, named rather than left to be discovered. It
// needs both halves in the same function, so a helper that took its binary as
// a parameter -- run(bin, args...), called with "git" from somewhere else --
// escapes the rule and the floor below will not notice the addition. The
// stderr buffer has to be assigned to the command's Stderr field, so
// &exec.Cmd{Stderr: &buf} escapes it too. Neither shape exists here today;
// both are review's job, the way the credential read with a bare os.Getenv is.
//
// Two things it deliberately does not cover. helm, kustomize and kubeconform
// are subprocesses too, and gate/sources.go quotes their stderr the same way;
// they are the next question rather than this one, and redact's own package
// comment names them. And nothing here stops a credential reaching argv, where
// a clone URL still puts it -- that is issue #118.
func TestEveryGitSubprocessRedactsItsStderr(t *testing.T) {
	guarded, checked := 0, 0
	for _, path := range goFiles(t, helmtest.Root(t)) {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("could not parse %s: %v", path, err)
		}
		rel, err := filepath.Rel(helmtest.Root(t), path)
		if err != nil {
			rel = path
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !runsGit(fn.Body) {
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
				t.Errorf("%s: %s starts git and then quotes what git wrote to stderr "+
					"without passing it through redact.Text.\n"+
					"git repeats whatever the server says, and this command talks to a remote "+
					"whose URL is GIT_REPO_URL -- a credential an operator wrote into it reaches "+
					"a log and the gate's published report with the token still in it.", rel, fn.Name.Name)
			}
		}
	}

	// The self-check, and not optional. Nine call sites start git and read its
	// stderr, across four packages; anything less means the walk has stopped
	// seeing what it reads -- a command built some other way, a stderr buffer
	// captured some other way -- and a test that checked nothing reports
	// exactly like a test that passed.
	if guarded < 9 || checked < 9 {
		t.Fatalf("found %d git commands and %d stderr reads among them; this process runs "+
			"more git than that, so this walk is no longer seeing them. Fix the walk -- "+
			"do not lower these numbers.", guarded, checked)
	}
}

// goFiles is every non-test Go source file in the repository.
//
// Hidden directories are skipped, and that is not tidiness: .claude/worktrees
// holds whole checkouts of this repository, so a walk that descends into it
// parses every file in this list a second time and reports failures against
// paths that are not the ones anybody would edit.
func goFiles(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(found) == 0 {
		t.Fatalf("found no Go files under %s; this walk is reading the wrong tree", root)
	}
	return found
}

// runsGit reports whether n builds a subprocess and names git.
//
// Two conditions rather than one, because the binary is not always an argument
// to exec.Command: the two push sites build a list of steps and run
// `exec.CommandContext(ctx, s.args[0], s.args[1:]...)`, so the literal `"git"`
// is in a []string three statements away from the call. A rule that reads only
// the call's own arguments misses both of them, which would quietly drop the
// coverage this whole test was written to add.
//
// So: the function builds an exec.Cmd, and the word git appears in it as a
// literal. What that still excludes is the subprocess this rule is not about
// -- gate/sources.go runs whatever binary it was handed through a variable,
// and that is helm or kustomize. What it would wrongly include is a function
// that runs something else and mentions git in a string for another reason;
// that failure asks for a redaction that costs nothing and is safe, which is
// the direction to be wrong in.
func runsGit(n ast.Node) bool {
	subprocess, named := false, false
	ast.Inspect(n, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && lit.Value == `"git"` {
			named = true
			return true
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "Command") {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" {
			subprocess = true
		}
		return true
	})
	return subprocess && named
}

// stderrReads is every call that reads a buffer the function gave a subprocess
// to write its stderr into.
//
// Two steps, because the buffer's name is not the contract: first find what was
// assigned to a command's Stderr field, then find the reads of it. A rule
// keyed on the identifier `stderr` would pass the day somebody named one
// `errOut` -- and one of them is already named `out`, another `errb`. What it
// reads is the assignment, so a command built as a composite literal with its
// Stderr field set is not seen; the doc comment on the test says so.
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
