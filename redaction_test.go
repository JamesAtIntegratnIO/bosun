package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// Every credential config.go reads primes the process redactor.
//
// This is the half of the control that rots. The redactor works on whatever it
// was given, so a credential added to Config and wired to its one client is a
// change that compiles, passes every test, and quietly leaves that credential
// readable in any error string that quotes it. Nothing about the symptom points
// at the omission -- there is no symptom until a host echoes the token back.
//
// So the list is DERIVED on both sides and neither side is written down here:
// the credentials come from config.go's own syntax tree, the same walk
// config_chart_test.go uses, and each is proved covered by loading a Config
// whose every credential is a distinct sentinel and asking the redactor to
// remove it. A new call to envSecret is a new row in this test the moment it is
// written, and it fails until Config.Secrets names the field it loaded into.
//
// What it cannot see is a credential read some other way. envSecret's own
// comment says it "is the only way this file reads one", and that is the
// assumption this rests on: a token wired to a bare os.Getenv is invisible to
// this walk, to TestEveryCredentialLoadsFromAFile, and to the _FILE form an
// operator expects every credential to have. Reading one that way is the
// mistake to catch in review, because no test here can.
func TestEveryCredentialPrimesTheRedactor(t *testing.T) {
	_, credentials := configEnv(t)
	// The self-check, and not optional: a walk that stops finding credentials
	// leaves this test comparing nothing against nothing and reporting a pass.
	if len(credentials) < 5 {
		t.Fatalf("found only %d credentials in config.go; envSecret is called differently "+
			"now and this walk no longer sees them. Fix the walk in config_chart_test.go -- "+
			"do not lower this number.", len(credentials))
	}

	sentinel := func(k string) string { return "sentinel-" + k + "-must-not-be-published" }
	env := map[string]string{
		"GIT_OWNER": "o", "GIT_REPO": "r",
		"GIT_REPO_URL": "https://example.invalid/o/r.git",
		"LLM_PROVIDER": "openai", "LLM_MODEL": "m",
		"LLM_BASE_URL":    "http://model.invalid/v1",
		"ALLOW_PATHS":     "addons/**",
		"ARGOCD_BASE_URL": "https://argocd-server.argocd.svc",
	}
	for _, k := range sortedKeys(credentials) {
		env[k] = sentinel(k)
	}
	withOnly(t, env)

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("a config with every credential set must load: %v", err)
	}

	t.Cleanup(func() { redact.Prime() })
	redact.Prime(c.Secrets()...)

	for _, k := range sortedKeys(credentials) {
		s := sentinel(k)
		// The shape this is actually for: a subprocess or a remote host
		// quoting a credential back inside a sentence bosun is about to log or
		// post. Redaction has to hold mid-string, not only where a URL put it.
		if got := redact.Text("error: the host reported " + s + " was rejected"); strings.Contains(got, s) {
			t.Errorf("%s reached the redactor un-primed: %q\n"+
				"config.go loads this credential and Config.Secrets does not name the field it "+
				"loads into, so any error string quoting it is published verbatim. Add the field "+
				"to Config.Secrets.", k, got)
		}
	}
}

// And the credentials are named by field, not by value.
//
// Secrets returning the fields of a Config nobody configured must be a
// redactor that removes nothing, rather than one primed with a handful of empty
// strings -- which is a match-everything rule, and turns every string in the
// process into confetti. The redactor drops them; this asserts the two halves
// still agree about that, because the emptiness arrives here.
func TestAnUnconfiguredInstallPrimesNothing(t *testing.T) {
	t.Cleanup(func() { redact.Prime() })
	redact.Prime((&Config{}).Secrets()...)

	const in = "fatal: repository not found"
	if got := redact.Text(in); got != in {
		t.Fatalf("a Config with no credentials rewrote the text: %q", got)
	}
}

// And the composition root primes it, before it builds anything that can fail.
//
// The test above proves Config.Secrets names every credential; nothing proved
// anybody calls it. Delete the one line from main and every other test in this
// repository still passes -- the credentials still reach Secrets, and nothing
// reaches the process. That is the shape of a control that is present in the
// design and absent from the binary.
//
// Order matters as much as presence, and for the reason main's own comment
// gives: everything built after the priming can fail with a message that quotes
// what it was given. So this asserts the call happens before the first
// collaborator is constructed, and derives "collaborator" from the composition
// root itself -- any value built from an imported package -- rather than from a
// list here that would go stale the first time one is added.
func TestTheCompositionRootPrimesTheRedactor(t *testing.T) {
	path := filepath.Join(helmtest.Root(t), "main.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", path, err)
	}
	body := mainBody(t, file)

	primed := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Prime" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "redact" {
			return true
		}
		// Spread from the configuration's own accounting of its credentials,
		// not from a second list assembled here. `redact.Prime("a", "b")` in
		// the composition root would be the drift this whole file exists to
		// stop, wearing the shape of the fix.
		if call.Ellipsis == token.NoPos || len(call.Args) != 1 {
			return true
		}
		arg, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := arg.Fun.(*ast.SelectorExpr); ok && fn.Sel.Name == "Secrets" {
			primed = call.Pos()
		}
		return true
	})
	if primed == token.NoPos {
		t.Fatal("main does not call redact.Prime(cfg.Secrets()...).\n" +
			"Every credential this process loads has to reach the redactor, or the whole " +
			"control is a package nothing calls: an error string quoting a token is published " +
			"verbatim, and the only symptom is a host echoing one back.")
	}

	built := firstCollaborator(t, body)
	if primed > built {
		t.Errorf("main builds a collaborator before it primes the redactor.\n"+
			"Everything built after LoadConfig can fail with a message that quotes what it "+
			"was given, so the priming goes first. (priming at offset %d, first construction "+
			"at %d)", primed, built)
	}
}

func mainBody(t *testing.T, file *ast.File) *ast.BlockStmt {
	t.Helper()
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "main" && fn.Recv == nil && fn.Body != nil {
			return fn.Body
		}
	}
	t.Fatal("found no func main in main.go; this walk is reading the wrong file")
	return nil
}

// firstCollaborator is where main first builds a value from another package.
//
// Derived rather than named, so a new collaborator is covered without this test
// being touched. The self-check is that there has to be one at all: a
// composition root that composes nothing means the walk is looking at something
// it does not understand, and an ordering assertion against nothing passes.
func firstCollaborator(t *testing.T, body *ast.BlockStmt) token.Pos {
	t.Helper()
	first := token.NoPos
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if _, ok := sel.X.(*ast.Ident); !ok {
			return true
		}
		if first == token.NoPos || lit.Pos() < first {
			first = lit.Pos()
		}
		return true
	})
	if first == token.NoPos {
		t.Fatal("main builds no value from any imported package; the composition root has " +
			"moved and this walk no longer sees what it orders the priming against")
	}
	return first
}
