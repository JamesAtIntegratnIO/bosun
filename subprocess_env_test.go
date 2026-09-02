package main

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/childenv"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// Every subprocess this process starts builds its environment from childenv.
//
// This is the first line, where TestEverySubprocessRedactsItsStderr is the
// second, and they are about different failures. Redaction filters what a
// child's output may publish. It does nothing about a child that writes its
// environment to a file, sends it somewhere, or is itself hostile -- and a
// chart's helm plugin is this process's child, with this process's
// environment.
//
// The state this replaces: cmd.Env was nil at every call site but five, and a
// nil Env means the child gets os.Environ() verbatim. So `helm template`,
// `kustomize build` and `kubeconform` each ran holding GIT_TOKEN,
// ARGOCD_TOKEN, the model key, the promotion and MCP tokens, the App private
// key and a possibly credential-bearing GIT_REPO_URL. The five that did set it
// made it worse rather than better, because every one of them spelled
// `append(os.Environ(), …)`: one scoped credential added on top of all of
// them.
//
// The rule is derived from the mechanism and not from a list of call sites: a
// function that builds an exec.Cmd must assign that command's Env, and the
// value it assigns must come from childenv. Both halves matter. Without the
// first, a new call site inherits everything and nothing says so; without the
// second, `append(os.Environ(), …)` passes a check that only asked whether Env
// was set, which is exactly the shape the five sites already had.
//
// Where the derivation stops, named rather than left to be discovered. It
// needs both halves in the same function, so a helper that took a *exec.Cmd as
// a parameter and set its Env there would escape the rule, and so would a
// command built as &exec.Cmd{Env: …}. Neither shape exists here today. Both
// are review's job, the way a credential read with a bare os.Getenv is.
func TestEverySubprocessRunsWithoutThisProcessesCredentials(t *testing.T) {
	started, checked := 0, 0
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
			started++
			assigned := envAssignments(fn.Body)
			if len(assigned) == 0 {
				t.Errorf("%s: %s starts a subprocess and never sets its Env.\n"+
					"A nil Env is os.Environ(), so that child runs holding every credential "+
					"this process loaded -- and it has no use for one. Assign "+
					"childenv.Environ(), or childenv.With(…) if the command needs a "+
					"credential of its own.", rel, fn.Name.Name)
				continue
			}
			for _, rhs := range assigned {
				checked++
				if fromChildenv(rhs) {
					continue
				}
				t.Errorf("%s: %s builds a subprocess environment from something other than "+
					"childenv.\n"+
					"`append(os.Environ(), …)` is what this replaced: it adds one scoped "+
					"credential on top of every credential this process loaded. childenv.With "+
					"takes the same entries and puts them on a base with this process's own "+
					"credentials removed.", rel, fn.Name.Name)
			}
		}
	}

	// The self-check, and not optional. Thirteen functions in this repository
	// build an exec.Cmd: the two pushes, Clone, EnsureHead, headSHA, gitEnvRun
	// and gitLine in gitprovider; the gate service's gitRun; the gate's own
	// runner and its kubeconform run; the agent's CRD render and its diff; and
	// internal/helmtest's chart render, which is test support and is held to
	// the rule anyway rather than carved out of it.
	//
	// Anything less means the walk has stopped seeing what it reads -- a
	// command built some other way -- and a test that checked nothing reports
	// exactly like a test that passed. A floor lowered to make a blind walk
	// pass is indistinguishable from a floor lowered because a call site went
	// away, so name what moved rather than lowering the number.
	if started < 13 || checked < 13 {
		t.Fatalf("found %d functions starting a subprocess and %d Env assignments among "+
			"them; this process runs more than that, so this walk is no longer seeing "+
			"them. Fix the walk -- do not lower these numbers.", started, checked)
	}
}

// And os.Environ() is spelled in exactly one place.
//
// The rule above is per function, and a helper that assembled an environment
// somewhere else and handed it back would satisfy it while reintroducing what
// it exists to stop. There is one right answer to "what should a child of this
// process inherit", it lives in childenv, and this is what keeps a second one
// from being written.
func TestOnlyChildenvReadsTheProcessEnvironment(t *testing.T) {
	root := helmtest.Root(t)
	seen := 0
	for _, path := range goFiles(t, root) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		if strings.HasPrefix(rel, "childenv"+string(filepath.Separator)) {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("could not parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Environ" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			seen++
			if pkg.Name == "os" {
				t.Errorf("%s calls os.Environ(). The environment a child of this process "+
					"inherits is childenv's answer to give; a second one here is a second "+
					"place for a credential to travel from.", rel)
			}
			return true
		})
	}

	// The self-check, and here it is the whole test. Zero matches is the
	// correct answer to this rule, and it is also what a walk that has stopped
	// recognising the call shape reports -- indistinguishable from a pass, and
	// exactly what CONTRIBUTING means by a derivation that reads like
	// coverage. So the walk is made to prove it can still recognise an
	// `X.Environ()` at all, by counting the ones it passes over.
	//
	// Eight, not thirteen: of the subprocess call sites, eight take
	// childenv.Environ() and the other five take childenv.With(…), which this
	// matcher is not looking for and should not be -- counting a second shape
	// would stop this number saying anything about the shape under test.
	if seen < 8 {
		t.Fatalf("this walk found only %d calls of the form X.Environ(); there are eight "+
			"childenv.Environ() call sites, so it is no longer recognising the shape and a "+
			"repository full of os.Environ() would pass it. Fix the walk -- do not lower "+
			"this number.", seen)
	}
}

// And the composition root primes it, before it builds anything that can start
// a subprocess.
//
// The test above proves LoadConfig names every credential; nothing proved
// anybody hands that list to childenv. Delete the one line from main and every
// other test in this repository still passes -- the names still reach
// SecretEnv, childenv stays unprimed, Environ goes back to being os.Environ()
// and every child inherits everything again. That is the shape of a control
// that is present in the design and absent from the binary, and it is why
// redaction_test.go holds the same claim about redact.Prime.
//
// Spread from the configuration's own accounting, not from a second list
// assembled here: `childenv.Prime("GIT_TOKEN", …)` in the composition root
// would be the drift this file exists to stop, wearing the shape of the fix.
func TestTheCompositionRootPrimesTheChildEnvironment(t *testing.T) {
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
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "childenv" {
			return true
		}
		if call.Ellipsis == token.NoPos || len(call.Args) != 1 {
			return true
		}
		arg, ok := call.Args[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		if fn, ok := arg.Fun.(*ast.SelectorExpr); ok && fn.Sel.Name == "SecretEnv" {
			primed = call.Pos()
		}
		return true
	})
	if primed == token.NoPos {
		t.Fatal("main does not call childenv.Prime(cfg.SecretEnv()...).\n" +
			"Unprimed, childenv.Environ() is os.Environ(), so every helm, kustomize, " +
			"kubeconform and git this process starts is back to inheriting every " +
			"credential it loaded -- and nothing else in this repository would notice.")
	}

	if built := firstCollaborator(t, body); primed > built {
		t.Errorf("main builds a collaborator before it primes the child environment.\n"+
			"Everything built after LoadConfig can start a subprocess, so the priming goes "+
			"first. (priming at offset %d, first construction at %d)", primed, built)
	}
}

// The names childenv is primed with are the credentials config.go reads, both
// spellings, plus the one it cannot see.
//
// Derived on both sides and neither written down here: the credentials come
// from config.go's own syntax tree, the same walk redaction_test.go and
// config_chart_test.go use, and what LoadConfig reports is what actually
// reaches childenv. A new call to envSecret is a new row in this test the
// moment it is written -- and because LoadConfig records the names as it reads
// them, the row passes without anybody adding it anywhere. That is the point:
// a new credential is stripped the day it is added, not the day somebody
// remembers.
func TestEveryCredentialIsNamedForStripping(t *testing.T) {
	_, credentials := configEnv(t)
	// The self-check, and not optional: a walk that stops finding credentials
	// leaves this test comparing nothing against nothing and reporting a pass.
	if len(credentials) < 5 {
		t.Fatalf("found only %d credentials in config.go; envSecret is called differently "+
			"now and this walk no longer sees them. Fix the walk in config_chart_test.go -- "+
			"do not lower this number.", len(credentials))
	}

	cfg := configWithEveryCredential(t, func(k string) string { return "a-" + k })

	named := map[string]bool{}
	for _, k := range cfg.SecretEnv() {
		named[k] = true
	}
	for _, k := range sortedKeys(credentials) {
		for _, spelling := range []string{k, k + "_FILE"} {
			if !named[spelling] {
				t.Errorf("%s is not stripped from a subprocess's environment.\n"+
					"config.go reads a credential from it, so every helm, kustomize, "+
					"kubeconform and git this process starts would inherit it.", spelling)
			}
		}
	}
	// The one no walk of envSecret can find, and the reason it is named by
	// hand in LoadConfig rather than derived: GIT_REPO_URL is read with a bare
	// os.Getenv because it is the repository rather than a credential, and it
	// is the one that may contain one.
	if !named["GIT_REPO_URL"] {
		t.Error("GIT_REPO_URL is not stripped. It is not loaded as a credential, and it is " +
			"the one that may carry one; every child handed it is handed whatever an " +
			"operator wrote into it.")
	}
}

// And every credential is read unconditionally, which is what makes "recorded
// as it reads" the same list as "every credential this install could load".
//
// The gap this closes is the one the recording buys its convenience with.
// SecretEnv is assembled by the reads that happened, so a `secret()` call
// behind an `if` would be absent from the list on any install that did not
// take that branch -- and the test above would not see it, because that test
// configures every credential and so takes every branch. The symptom would be
// a credential stripped in CI and inherited in production.
//
// So the rule is structural and about config.go's shape rather than its
// values: every credential read in LoadConfig sits in straight-line code. It
// is true today of all six, and it is the assumption SecretEnv rests on, so it
// is worth a compiler of its own rather than a sentence in a comment.
func TestEveryCredentialIsReadUnconditionally(t *testing.T) {
	path := filepath.Join(helmtest.Root(t), "config.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", path, err)
	}
	var load *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "LoadConfig" && fn.Recv == nil {
			load = fn
		}
	}
	if load == nil || load.Body == nil {
		t.Fatal("found no func LoadConfig in config.go; this walk is reading the wrong file")
	}

	// The branching statements a credential read must not be inside. A
	// FuncLit counts: LoadConfig's own `secret` closure is one, and a read
	// moved into any other closure is a read whose running is somebody else's
	// decision.
	branching := func(n ast.Node) bool {
		switch n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
			*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
			return true
		}
		return false
	}

	read := 0
	var stack []ast.Node
	ast.Inspect(load.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		defer func() { stack = append(stack, n) }()

		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		if name := calleeName(call.Fun); !secretReaders[name] {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil || key == "" {
			return true
		}
		read++
		for _, ancestor := range stack {
			if branching(ancestor) {
				t.Errorf("%s is read conditionally in LoadConfig.\n"+
					"SecretEnv is the list of reads that happened, so on an install that "+
					"does not take this branch the variable is not stripped -- and no test "+
					"here would see it, because they configure every credential and so take "+
					"every branch. Read it unconditionally, or name it the way GIT_REPO_URL "+
					"is named.", key)
				break
			}
		}
		return true
	})

	// The self-check, and not optional: a walk that finds no reads reports a
	// pass, and a pass here is the claim that every credential is read in
	// straight-line code.
	if read < 6 {
		t.Fatalf("found only %d credential reads in LoadConfig; there are six, so this walk "+
			"is no longer seeing them. Fix it -- do not lower this number.", read)
	}
}

// And the whole chain holds against a real subprocess.
//
// The tests above are syntax and slices. This is the claim they stand in for:
// a real LoadConfig over a real environment, primed into childenv the way main
// primes it, driving a real call site, with a shim in place of git recording
// what the kernel actually handed the child.
//
// Both halves, because a test asserting only the absence would pass on an
// implementation that had stopped authenticating: no credential this process
// loaded is in the child's environment, and the one credential that child
// needs still is.
func TestARealChildIsHandedNoneOfTheseCredentials(t *testing.T) {
	const inTheURL = "hunter2theconfiguredcredential"
	_, credentials := configEnv(t)
	if len(credentials) < 5 {
		t.Fatalf("found only %d credentials in config.go; this walk no longer sees them",
			len(credentials))
	}

	sentinel := func(k string) string { return "sentinel-" + k + "-must-not-be-inherited" }
	cfg := configWithEveryCredential(t, sentinel,
		"GIT_REPO_URL", "https://someone:"+inTheURL+"@git.example.invalid/o/r.git")

	t.Cleanup(func() { childenv.Prime() })
	childenv.Prime(cfg.SecretEnv()...)

	record := shimGitForEnv(t)
	remote := gitprovider.NewRemote(cfg.GitRepoURL)
	if err := gitprovider.Clone(context.Background(), remote, "main", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	child := string(raw)
	// The self-check: a shim that never ran records nothing, and every
	// assertion below then holds vacuously.
	if !strings.Contains(child, "argv:") {
		t.Fatal("the shim recorded nothing; Clone did not run git")
	}

	for _, k := range append(sortedKeys(credentials), "GIT_REPO_URL") {
		if strings.Contains(child, "\nenv: "+k+"=") {
			t.Errorf("git was started holding %s. It has no use for one, and "+
				"/proc/<pid>/environ holds it for as long as the process runs.", k)
		}
	}
	for _, k := range sortedKeys(credentials) {
		if strings.Contains(child, sentinel(k)) {
			t.Errorf("the value of %s reached the child under some other name:\n%s", k, child)
		}
	}
	// The credential an operator wrote into GIT_REPO_URL, which is the one the
	// clone does need: it reaches git as base64 inside an Authorization
	// header, asserted below, and so must not appear anywhere in plain.
	// Written as one condition rather than two, because Clone always sets that
	// header -- `contains(secret) && !contains("Authorization: Basic")` reads
	// like a check and can never fire.
	if strings.Contains(child, inTheURL) {
		t.Errorf("the credential in GIT_REPO_URL reached the child in plain, rather than "+
			"only as the scoped header that authenticates the clone:\n%s", child)
	}

	// And it can still authenticate, which is the half a test asserting only
	// absence would let a broken implementation pass. A stripped environment
	// that also stripped the credential the command needs is a private
	// repository that stops cloning, and nothing about it looks wrong here.
	if !strings.Contains(child, "env: GIT_CONFIG_KEY_0=http."+remote.URL()+".extraHeader") {
		t.Errorf("the clone was given no credential to send:\n%s", child)
	}
}

// configWithEveryCredential loads a Config with every credential config.go
// reads set to sentinel(name), plus whatever else an install needs to be
// valid.
//
// The credentials are derived; the rest is the smallest environment LoadConfig
// will accept, and it is written out because there is nothing to derive it
// from -- a required setting added later fails here loudly, which is the right
// place to notice it.
func configWithEveryCredential(t *testing.T, sentinel func(string) string, also ...string) *Config {
	t.Helper()
	_, credentials := configEnv(t)
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
	for i := 0; i+1 < len(also); i += 2 {
		env[also[i]] = also[i+1]
	}
	withOnly(t, env)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("a config with every credential set must load: %v", err)
	}
	return cfg
}

// envAssignments is every expression assigned to a command's Env field.
//
// Keyed on the field rather than on the variable's name, for the reason
// stderrReads is: the command is called `cmd` in most places, `c` in the gate
// service and `diff` in the agent, and a rule spelled against an identifier
// would pass the day somebody renamed one.
func envAssignments(body *ast.BlockStmt) []ast.Expr {
	var found []ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			sel, ok := lhs.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Env" || i >= len(assign.Rhs) {
				continue
			}
			found = append(found, assign.Rhs[i])
		}
		return true
	})
	return found
}

// fromChildenv reports whether an expression is a call into childenv.
func fromChildenv(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "childenv"
}

// shimGitForEnv puts a git on PATH that records its argv and its environment,
// the way gitprovider's own shim does, and returns the file it records into.
func shimGitForEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	shim := "#!/bin/sh\n" +
		"{ printf 'argv:'; for a in \"$@\"; do printf ' %s' \"$a\"; done; printf '\\n'\n" +
		"  env | sed 's/^/env: /'; } >> \"$BOSUN_TEST_RECORD\"\n" +
		"echo 0000000000000000000000000000000000000000\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOSUN_TEST_RECORD", record)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}
