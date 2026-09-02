package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// Only gitprovider may run a git command that contacts a remote.
//
// This is what keeps a credential out of argv, and it is a structural rule
// rather than a promise. A credential an operator writes into GIT_REPO_URL
// used to travel to git as part of the URL on the command line, on three
// separate clones; /proc/<pid>/cmdline is world-readable, so for the length of
// each clone the token was there for `ps` and for anything that logs a command
// line. gitprovider.Remote is what splits that URL into the address git is
// given and the credential it is handed through the environment, and a clone
// written anywhere else is a clone that never met it.
//
// So the rule is the mechanism and not the three call sites it was written
// for: a git command whose arguments name a subcommand that talks to a host
// belongs to the package that owns the credential. The subcommand list below
// is a fact about git rather than a list of ours -- these are the commands
// that open a connection -- in the same way that the string "git" is.
//
// Two things this cannot see, both of which are review's job. A remote-facing
// command assembled entirely from variables names no subcommand and is
// invisible. And talksGit, which stands in front of this rule to keep `helm
// pull` out of it, recognises the indirect shape by a naming convention: a
// call to a same-package function whose name begins with `git`. A helper
// called `run` that started git would take a clone straight past this, and the
// self-check would not notice, because a floor only catches a count that
// falls. Neither shape exists here; both would be a review comment rather than
// a test failure.
func TestOnlyGitproviderTalksToARemote(t *testing.T) {
	// git's remote-facing subcommands: the ones that open a connection to a
	// host, which is the property that makes a credential necessary and
	// therefore makes argv dangerous. A list about git rather than about this
	// repository's call sites, which is the distinction that matters -- but it
	// is still a list, and nothing forces an eighth entry. `git archive
	// --remote` and the `*-pack` plumbing are here because they contact a
	// host; anything git grows later has to be added by hand.
	remoteFacing := map[string]bool{
		"clone": true, "fetch": true, "pull": true, "push": true,
		"ls-remote": true, "submodule": true, "remote": true,
		"archive": true, "fetch-pack": true, "send-pack": true,
	}

	found := 0
	root := helmtest.Root(t)
	for _, path := range goFiles(t, root) {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("could not parse %s: %v", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		pkg := filepath.Dir(rel)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !talksGit(fn.Body) {
				continue
			}
			for _, sub := range gitSubcommands(fn.Body, remoteFacing) {
				found++
				if pkg == "gitprovider" {
					continue
				}
				t.Errorf("%s: %s runs `git %s`, which contacts a remote, from outside gitprovider.\n"+
					"The URL of that remote is GIT_REPO_URL, and a credential an operator wrote into it "+
					"reaches argv -- /proc/<pid>/cmdline is world-readable -- unless it went through "+
					"gitprovider.Remote first. Move the command into gitprovider, or call one that is "+
					"already there: Clone, EnsureHead, MergeBase.", rel, fn.Name.Name, sub)
			}
		}
	}

	// The self-check. This process clones, fetches and pushes; a walk that
	// finds none of that is reading something it does not understand, and an
	// assertion against nothing passes.
	if found < 5 {
		t.Fatalf("found only %d git commands that contact a remote; this process clones, "+
			"fetches and pushes, so this walk is no longer seeing them. Fix the walk -- "+
			"do not lower this number.", found)
	}
}

// gitSubcommands is every remote-facing subcommand a function names.
//
// Any string literal in the function, and deliberately not "an argument to
// exec.Command". The shape this has to catch is the one that used to be here:
// gateservice spelled `gitRun(ctx, "clone", …)` and let a helper three lines
// away start the subprocess, so a rule keyed on the function that calls exec
// walks straight past the clone it was written to find. The arguments are also
// built as `[]string{"clone", …}` in two places and appended to in a third, so
// keying on the call's own argument list finds nothing at all.
//
// The cost is that a function with the bare string "push" or "remote" in it
// for some other reason is asked to justify itself, which is why talksGit
// stands in front of this: `helm pull` is a real command in gate/, and it is
// not a git remote.
func gitSubcommands(body *ast.BlockStmt, of map[string]bool) []string {
	var found []string
	seen := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		name := strings.Trim(lit.Value, `"`)
		if of[name] && !seen[name] {
			seen[name] = true
			found = append(found, name)
		}
		return true
	})
	return found
}

// talksGit reports whether a function is about git at all.
//
// Two ways, because there are two shapes. A function that runs git itself
// names it, as the literal "git". A function that hands its arguments to a
// local helper -- `gitRun(ctx, "clone", …)`, which is how the clone this rule
// exists for used to be written -- names nothing but the helper, so a call to
// a same-package function whose name starts with `git` counts too.
//
// A call into gitprovider is deliberately not either of those. Reaching
// gitprovider.Clone from another package is the approved path and the whole
// point of the rule; flagging it would ask every caller to move into the
// package it is already correctly calling.
func talksGit(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if n.Kind == token.STRING && strings.Trim(n.Value, `"`) == "git" {
				found = true
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && strings.HasPrefix(id.Name, "git") {
				found = true
			}
		}
		return !found
	})
	return found
}
