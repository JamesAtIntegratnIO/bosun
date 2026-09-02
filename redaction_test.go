package main

import (
	"fmt"
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

// A credential an operator wrote into GIT_REPO_URL primes the redactor too.
//
// This is the gap the walk above cannot see, and its own comment says so: the
// repository URL is read with a bare os.Getenv, so no amount of deriving from
// envSecret will ever find the token inside it. It is a credential all the
// same -- gitprovider already treats it as one, stripping it out of the push
// remote so it does not end up in argv -- and every clone, fetch and
// merge-base in this process is pointed at that URL.
//
// What is primed is the credential, never the URL. A repository URL is not a
// secret, it is in the chart, the logs and half the error messages, and
// priming the whole string would replace the one piece of context that says
// which repository a failure was about.
func TestACredentialInTheRepositoryURLPrimesTheRedactor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		repoURL string
		want    string
	}{
		{
			name:    "a password in the userinfo is the credential",
			repoURL: "https://someone:hunter2@git.example.com/o/r.git",
			want:    "hunter2",
		},
		{
			// The form a token actually takes. GitHub, Gitea and GitLab all
			// accept the token as the whole userinfo, and an operator copying
			// a clone URL out of a UI gets this one.
			repoURL: "https://ghp_0123456789abcdef@github.com/o/r.git",
			name:    "a lone username is the credential, because that is how a token is written",
			want:    "ghp_0123456789abcdef",
		},
		{
			// And the username beside a password is not. It is `oauth2`,
			// `x-access-token` or somebody's name -- a placeholder the host
			// ignores -- and priming it would redact an ordinary word.
			name:    "the username beside a password is not primed",
			repoURL: "https://x-access-token:hunter2@github.com/o/r.git",
			want:    "hunter2",
		},
		{
			name:    "no userinfo, nothing to prime",
			repoURL: "https://github.com/o/r.git",
			want:    "",
		},
		{
			name:    "unset, nothing to prime",
			repoURL: "",
			want:    "",
		},
		{
			// What `git remote add origin https://TOKEN@host/...` writes back
			// into .git/config, and the form that reads as "there is a
			// password" while the password is empty.
			name:    "an empty password leaves the username as the credential",
			repoURL: "https://ghp_0123456789abcdef:@github.com/o/r.git",
			want:    "ghp_0123456789abcdef",
		},
		{
			// url.Parse decodes the userinfo, so the credential git uses and
			// the credential an error message quotes are two different
			// strings. Both are primed; this asserts the encoded one, which is
			// the spelling every message quoting GIT_REPO_URL carries.
			name:    "a percent-encoded password is primed as it was written",
			repoURL: "https://someone:p%40ss%2Fword@git.example.com/o/r.git",
			want:    "p%40ss%2Fword",
		},
		{
			// The one that has to be right. An ssh remote's username is a
			// login name and it is `git` on every forge in existence; the
			// credential is a key that never appears in the URL at all.
			// Priming `git` would replace that substring in every sentence
			// this process logs, most of which are about git.
			name:    "an ssh remote primes nothing, whatever its username",
			repoURL: "ssh://git@github.com/o/r.git",
			want:    "",
		},
		{
			name:    "an scp-style remote primes nothing either",
			repoURL: "git@github.com:o/r.git",
			want:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { redact.Prime() })
			redact.Prime((&Config{GitRepoURL: tc.repoURL}).Secrets()...)

			// Through the redactor rather than against the slice, because
			// what is being asserted is what reaches a log line, and an entry
			// the redactor drops as blank is indistinguishable from one that
			// was never added.
			const sentence = "fatal: unable to access %q: the server said no"
			got := redact.Text(fmt.Sprintf(sentence, tc.repoURL))

			if tc.want == "" {
				if want := fmt.Sprintf(sentence, tc.repoURL); got != want {
					t.Fatalf("nothing in %q is a credential, and the redactor rewrote the "+
						"text anyway:\n got %q\nwant %q", tc.repoURL, got, want)
				}
				return
			}
			if strings.Contains(got, tc.want) {
				t.Errorf("the credential in %q survived: %q\n"+
					"Config.Secrets does not name it, so every error quoting this URL -- and "+
					"every clone, fetch and merge-base in this process is pointed at it -- "+
					"is published with the token still in it.", tc.repoURL, got)
			}
			if !strings.Contains(got, redact.Marker) {
				t.Errorf("nothing was redacted at all in %q", got)
			}
		})
	}
}

// And both spellings are primed, not just the one a message happens to carry.
//
// The table above asserts the encoded form, because that is what a message
// quoting GIT_REPO_URL carries. This is the other direction and the one the
// redactor exists for: git talks to the host about the *decoded* credential,
// so a host echoing it back echoes that spelling, and it has to go too.
func TestBothSpellingsOfAnEncodedCredentialArePrimed(t *testing.T) {
	t.Cleanup(func() { redact.Prime() })
	redact.Prime((&Config{
		GitRepoURL: "https://someone:p%40ss%2Fword@git.example.com/o/r.git",
	}).Secrets()...)

	for _, spelling := range []string{"p%40ss%2Fword", "p@ss/word"} {
		got := redact.Text("error: the host reported " + spelling + " was rejected")
		if strings.Contains(got, spelling) {
			t.Errorf("the %s spelling survived: %q", spelling, got)
		}
	}
}

// And the credential reaches Secrets from the environment, not just from a
// struct literal.
//
// The table above builds a Config by hand, which proves what Secrets does with
// a field and nothing about how that field is filled. GIT_REPO_URL is read
// with a bare os.Getenv, outside the envSecret walk that derives every other
// credential, so the wiring from variable to field to redactor is exactly the
// part no other test in this file is watching.
func TestTheRepositoryURLCredentialIsPrimedFromTheEnvironment(t *testing.T) {
	_, credentials := configEnv(t)
	env := map[string]string{
		"GIT_OWNER": "o", "GIT_REPO": "r",
		"GIT_REPO_URL": "https://x-access-token:sentinel-in-the-repo-url@git.example.com/o/r.git",
		"LLM_PROVIDER": "openai", "LLM_MODEL": "m",
		"LLM_BASE_URL":    "http://model.invalid/v1",
		"ALLOW_PATHS":     "addons/**",
		"ARGOCD_BASE_URL": "https://argocd-server.argocd.svc",
	}
	for _, k := range sortedKeys(credentials) {
		env[k] = "sentinel-" + k
	}
	withOnly(t, env)

	c, err := LoadConfig()
	if err != nil {
		t.Fatalf("a config with a credential in its repository URL must load: %v", err)
	}
	t.Cleanup(func() { redact.Prime() })
	redact.Prime(c.Secrets()...)

	got := redact.Text("fatal: the host reported sentinel-in-the-repo-url was rejected")
	if strings.Contains(got, "sentinel-in-the-repo-url") {
		t.Errorf("the credential in GIT_REPO_URL never reached the redactor: %q\n"+
			"LoadConfig reads that variable with a bare os.Getenv, so nothing derived from "+
			"envSecret will catch this if the wiring through Config.Secrets is broken.", got)
	}
}

// And the URL around the credential survives.
//
// Separate from the table because it is the other half of the same decision:
// redacting the whole of GIT_REPO_URL would satisfy every assertion above and
// leave an operator reading `unable to access "***"`, which does not say which
// repository, host or branch the run was about.
func TestTheRepositoryURLItselfIsNotRedacted(t *testing.T) {
	t.Cleanup(func() { redact.Prime() })
	redact.Prime((&Config{
		GitRepoURL: "https://someone:hunter2@git.example.com/o/r.git",
	}).Secrets()...)

	got := redact.Text("fatal: unable to access 'https://git.example.com/o/r.git/'")
	for _, keep := range []string{"git.example.com", "o/r.git"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q was redacted out of %q; the host and path are not the credential "+
				"and they are what says which repository failed", keep, got)
		}
	}
}
