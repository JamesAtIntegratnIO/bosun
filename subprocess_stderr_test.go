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

// Every subprocess in this process redacts what it wrote to stderr.
//
// The rule arrived narrow twice and was widened twice, and both corrections
// are the same correction. First it qualified a function only if it called
// pushAuthEnv, on the reasoning that a push is where a credential reaches git;
// that missed EnsureHead's fetch and the merge-base ladder, because those run
// against `origin`, whose URL was GIT_REPO_URL with whatever an operator wrote
// into it. Then it qualified on the command being git, and named helm,
// kustomize and kubeconform as the next question. This is that question, and
// the answer was the same both times: the binary was never the reason.
//
// What makes a subprocess's stderr dangerous is that this process starts it
// while holding credentials. cmd.Env is nil at nearly every call site here,
// and a nil Env means the child gets os.Environ() -- so helm renders a chart
// with GIT_TOKEN, ARGOCD_TOKEN and the model key in its environment, and so
// does kubeconform. A plugin, a debug flag or a chart hook that prints its
// environment puts them on stderr, and stderr is what these functions quote
// into an error that reaches a log and the gate's published report. The other
// half is the one git already demonstrated: a chart render pulls from a
// registry over somebody else's network, and a host that echoes a request
// header back inside an error body is echoing a credential it was sent.
//
// So the rule is the mechanism with the binary removed as well as the call
// sites: a function that starts a subprocess and reads the buffer it gave that
// subprocess for its stderr must pass what it read through redact.Text. Both
// halves are found by syntax -- the command by exec.Command, the buffer by
// whatever was assigned to the command's Stderr field, whatever it happens to
// be called -- so a new call site is covered the day it is written.
//
// Where the derivation stops, named rather than left to be discovered. It
// needs both halves in the same function, so a helper that took its command as
// a parameter and returned the output escapes the rule, and the floor below
// will not notice the addition. The stderr buffer has to be assigned to the
// command's Stderr field, so &exec.Cmd{Stderr: &buf} escapes it too. Neither
// shape exists here today.
//
// The third escape does exist, and it is worth knowing where. A command run
// through .Output() without a Stderr of its own has what it printed handed
// back on exec.ExitError.Stderr, which this walk cannot see because nothing
// was ever assigned. internal/helmtest reads it that way twice. That is test
// support -- both functions take a *testing.T, neither is in the binary, and
// the redactor is never primed in a test -- so it is left alone rather than
// carved out of the rule. Every .Output() in the shipped code either sets its
// own Stderr, and so is counted here, or discards the error entirely.
//
// All three are review's job, the way a credential read with a bare os.Getenv
// is.
//
// Redaction is the second line and not the first. The first would be to stop
// handing every subprocess every credential this process loaded -- an explicit
// cmd.Env rather than an inherited one -- which is a change to what helm runs
// with rather than to what bosun prints, and is issue #122.
func TestEverySubprocessRedactsItsStderr(t *testing.T) {
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
			if !ok || fn.Body == nil || !startsSubprocess(fn.Body) {
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
				t.Errorf("%s: %s starts a subprocess and then quotes what it wrote to stderr "+
					"without passing it through redact.Text.\n"+
					"That child inherited every credential this process loaded, because cmd.Env is "+
					"nil, and it repeats whatever the host it talked to says -- so this text reaches "+
					"a log and the gate's published report with a token still in it.", rel, fn.Name.Name)
			}
		}
	}

	// The self-check, and not optional. Eleven call sites start a subprocess
	// and read its stderr; anything less means the walk has stopped seeing
	// what it reads -- a command built some other way, a stderr buffer
	// captured some other way -- and a test that checked nothing reports
	// exactly like a test that passed.
	//
	// The figure has moved twice and both moves are worth being able to
	// repeat. It was nine when the rule was about git, and fell to eight when
	// the three clones were consolidated into gitprovider.Clone -- two of them
	// were counted units and went away, while the gate service's never was one
	// (it spelled `gitRun(ctx, "clone", …)`, and gitRun survives for the local
	// `worktree add`). It rose to eleven when the rule stopped being about
	// git, which added three: the CRD read in agent, the kubeconform run, and
	// gate's own runner -- one function that is helm, `kustomize build` or
	// `kubectl kustomize` depending on which binary its caller handed it,
	// which is why counting it as "a helm site" would not add up.
	//
	// A floor lowered to make a blind walk pass is indistinguishable from a
	// floor lowered because a call site went away. Only checking the walk
	// against the code tells them apart, which is why the numbers above name
	// what moved.
	if guarded < 11 || checked < 11 {
		t.Fatalf("found %d subprocesses and %d stderr reads among them; this process runs "+
			"more than that, so this walk is no longer seeing them. Fix the walk -- "+
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

// startsSubprocess reports whether n builds an exec.Cmd.
//
// One condition, where this used to be two. The rule was once about git, and
// the second condition was a test for the literal "git" -- awkward, because
// the binary is not always an argument to exec.Command: the two push sites
// build a list of steps and run `exec.CommandContext(ctx, s.args[0],
// s.args[1:]...)`, so the name is in a []string three statements away.
//
// Dropping the binary from the rule dropped the awkwardness with it. What is
// dangerous about a subprocess's stderr is not that the subprocess is git; it
// is that the process was started by something holding credentials, and every
// child here inherits them, because cmd.Env is nil at nearly every call site
// and a nil Env means os.Environ(). helm renders charts from a registry over
// somebody else's network, kubeconform is handed a document, and both of them
// -- like git -- repeat what they were told.
func startsSubprocess(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "Command") {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "exec" {
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
