package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// The chart and the binary agree on the environment, in both directions.
//
// There is no compiler between charts/bosun/templates/deployment.yaml and
// config.go, and until this file existed no Go test in this repository had ever
// read charts/ at all. A variable renamed on one side is a setting that
// silently reverts to its default on the other -- it renders clean, it lints
// clean, it passes the values schema, and then it does the wrong thing at run
// time, which is the shape of every chart incident this repository has had.
//
// Both name sets are DERIVED. The chart's comes from rendering it; the
// binary's comes from walking config.go's syntax tree. A hand-written list is
// what config_test.go already has in three places, each with a comment
// claiming completeness that nothing checks -- and a hand-written list failing
// to stay complete is the defect, not the fix.

// envShapes are the renders it takes to reach every branch of the env block.
//
// One render cannot: the credentials are an if/else on mountAsFiles, the App
// is an if on git.app.appId, and several settings only appear `with` a value
// the defaults leave empty. Anything reachable by a switch is covered by
// renderAll, which derives its settings from the schema.
func envShapes() []struct {
	Name string
	Opts []helmtest.Option
} {
	base := helmtest.Values("ci/lint-values.yaml")
	return []struct {
		Name string
		Opts []helmtest.Option
	}{
		{"the lint values", []helmtest.Option{base}},
		{"a token credential", []helmtest.Option{base, helmtest.Set(
			"promotionAuth.existingSecret=bosun-promotion",
			"llm.existingSecret=bosun-llm", "llm.apiKeyKey=api-key")}},
		{"credentials as files", []helmtest.Option{base, helmtest.Set(
			"credentials.mountAsFiles=true",
			"promotionAuth.existingSecret=bosun-promotion",
			"llm.existingSecret=bosun-llm", "llm.apiKeyKey=api-key")}},
		{"a GitHub App", []helmtest.Option{base, helmtest.SetString(
			"git.app.appId=1234",
			"git.app.installationId=5678",
			"git.app.privateKeyKey=private-key.pem")}},
		{"a GitHub App with file credentials", []helmtest.Option{base,
			helmtest.Set("credentials.mountAsFiles=true"),
			helmtest.SetString(
				"git.app.appId=1234",
				"git.app.installationId=5678",
				"git.app.privateKeyKey=private-key.pem")}},
		{"gitea", []helmtest.Option{base, helmtest.Set(
			"git.provider=gitea", "git.apiBase=https://gitea.example.com")}},
		// Every setting the template guards with `{{- with ... }}`, whose
		// default is empty and which therefore appears in no other render.
		{"every optional setting", []helmtest.Option{base,
			helmtest.Set("gate.concurrency=4", "llm.reasoningEffort=high"),
			helmtest.SetJSON("triage.denyPaths", `["secrets/**"]`),
			helmtest.SetJSON("triage.egressDeny", `["telemetry.example.com"]`),
			helmtest.SetJSON("triage.egressAllowPrivate", `["gitea.internal"]`),
			helmtest.SetJSON("gate.validate.schemaLocations", `["default"]`),
			helmtest.SetJSON("gate.validate.skipKinds", `["CustomResourceDefinition"]`)}},
		{"the ArgoCD CA and author identity", []helmtest.Option{base, helmtest.Set(
			"gate.argocd.caSecret=bosun-argocd-ca", "gate.argocd.caKey=ca.crt",
			"git.author.name=Bosun", "git.author.email=bosun@example.com")}},
	}
}

// Every variable the chart can set is one LoadConfig reads, and every variable
// LoadConfig reads is one some render of the chart can set.
func TestEveryChartEnvVarIsOneTheBinaryReads(t *testing.T) {
	emitted := chartEnvNames(t)
	read := configEnvNames(t)

	for _, k := range sortedKeys(emitted) {
		if read[k] {
			continue
		}
		t.Errorf("charts/bosun sets %s (in the %q render) and config.go never reads it.\n"+
			"Either LoadConfig gained the reader under a different name, or the chart is setting "+
			"a variable the binary has stopped having an opinion about -- and the second is dead "+
			"configuration an operator will still copy into their values file and believe.",
			k, emitted[k])
	}

	for _, k := range sortedKeys(read) {
		if _, ok := emitted[k]; ok {
			continue
		}
		t.Errorf("config.go reads %s and no render of charts/bosun sets it.\n"+
			"A setting only reachable by editing the Deployment by hand is one the chart cannot "+
			"deliver. Add it to charts/bosun/templates/deployment.yaml, and to values.yaml and "+
			"values.schema.json with it.\n"+
			"If it is genuinely not the chart's to set, add a render to envShapes that reaches it.", k)
	}
}

// GATE_VALIDATE_ENABLED is tri-state, and the third state is the default.
//
// Unset means "leave the gated repository's own .bosun.yaml alone"; false means
// "override it off, everywhere". config.go:289's envBoolOpt is what makes that
// expressible, and the only thing preserving it across the chart is
// deployment.yaml's `not (kindIs "invalid" ...)` guard -- one idiom away from
// `{{ .Values.gate.validate.enabled | quote }}`, which sets the variable to
// "false" on every install that never mentioned it and switches schema
// validation off across a fleet with no values file changing anywhere.
func TestTheTriStateSettingsAreAbsentWhenUnset(t *testing.T) {
	tri := []string{"GATE_VALIDATE_ENABLED", "GATE_VALIDATE_IGNORE_MISSING_SCHEMAS"}

	unset := helmtest.Env(t, helmtest.Render(t, "bosun", helmtest.Values("ci/lint-values.yaml")), "placeholder")
	for _, k := range tri {
		if v, ok := unset[k]; ok {
			t.Errorf("a default render sets %s=%q, and it must not set it at all.\n"+
				"Unset is a third state: it means \"leave the gated repository's own .bosun.yaml "+
				"alone\". Emitting the value collapses three states into two and turns validation "+
				"off on every install that never mentioned it.", k, v)
		}
	}

	for _, want := range []string{"true", "false"} {
		env := helmtest.Env(t, helmtest.Render(t, "bosun",
			helmtest.Values("ci/lint-values.yaml"),
			helmtest.Set("gate.validate.enabled="+want,
				"gate.validate.ignoreMissingSchemas="+want)), "placeholder")
		for _, k := range tri {
			if env[k] != want {
				t.Errorf("with the value set to %s the chart emitted %s=%q; an explicit setting "+
					"must reach the binary verbatim.", want, k, env[k])
			}
		}
	}
}

// No credential is set both ways at once.
//
// envSecret (config.go:467) refuses K and K_FILE together by name -- "they
// cannot both be the credential" -- and deployment.yaml's `$files` if/else is
// the only thing standing between that refusal and a running deployment. A
// template that gained an addition where a branch was intended renders a pod
// that dies at start-up on a message about a Secret whose contents are fine.
func TestNoCredentialIsSetBothWays(t *testing.T) {
	_, secrets := configEnv(t)
	if len(secrets) == 0 {
		t.Fatal("found no credentials in config.go; envSecret is read differently now " +
			"and this test is proving nothing")
	}

	for _, shape := range envShapes() {
		t.Run(shape.Name, func(t *testing.T) {
			env := helmtest.Env(t, helmtest.Render(t, "bosun", shape.Opts...), "placeholder")
			for _, k := range sortedKeys(secrets) {
				_, plain := env[k]
				_, file := env[k+"_FILE"]
				if plain && file {
					t.Errorf("this render sets both %s and %s_FILE.\n"+
						"config.go refuses that pair at start-up, so the pod CrashLoops on a "+
						"message about a credential that is perfectly fine. The if/else in "+
						"charts/bosun/templates/deployment.yaml must stay an if/else.", k, k)
				}
			}
		})
	}
}

// A rendered Deployment produces a pod that starts.
//
// The chart carries twelve `fail` guards in _helpers.tpl and config.go carries
// eight start-up-fatal conditions in validate(). Two statements of "what is
// required", and nothing checking they agree. LoadConfig is the authority, so
// the render is fed to it rather than to a second list -- a values mistake the
// chart could have named at render time is otherwise a CrashLoop whose cause is
// three files from its message.
func TestARenderedDeploymentStartsTheBinary(t *testing.T) {
	for _, shape := range envShapes() {
		t.Run(shape.Name, func(t *testing.T) {
			env := helmtest.Env(t, helmtest.Render(t, "bosun", shape.Opts...), "placeholder")
			withOnly(t, env)
			if _, err := LoadConfig(); err != nil {
				t.Fatalf("the chart rendered a pod that will not start: %v\n"+
					"Either deployment.yaml must set the value, or bosun.validate in "+
					"charts/bosun/templates/_helpers.tpl must refuse the values that omit it. "+
					"A render that lints clean and CrashLoops is the worst of the two.", err)
			}
		})
	}
}

// And the values the chart must refuse rather than render.
//
// config.go makes each of these fatal at start-up, so a chart that renders them
// produces a CrashLoop three files from its cause instead of a named error at
// install time. Two mechanisms do the refusing -- values.schema.json for what a
// schema can express, the `fail` guards in _helpers.tpl for the cross-field
// rules it cannot -- and which one fires is the chart's business. What is
// asserted is the guarantee: refused, and refused BY NAME.
func TestTheChartRefusesValuesThatCouldNotStart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		names string // the value the refusal must name, in either spelling
		text  string // or, for a cross-field rule, the guard's own words
		opts  []helmtest.Option
	}{
		{
			name: "no allowPaths", names: "triage.allowPaths",
			opts: []helmtest.Option{helmtest.Values("ci/lint-values.yaml"),
				helmtest.SetJSON("triage.allowPaths", `[]`)},
		},
		{
			name: "no llm.model", names: "llm.model",
			opts: []helmtest.Option{helmtest.Values("ci/lint-values.yaml"),
				helmtest.SetJSON("llm.model", `""`)},
		},
		// The cross-field rules, which a JSON schema cannot express and which
		// therefore have only the `fail` guards standing behind them. These are
		// the rows that would notice a guard quietly becoming a comment.
		{
			name: "a route with the page switched off",
			text: "there would be nothing listening behind the route",
			opts: []helmtest.Option{helmtest.Values("ci/lint-values.yaml"),
				helmtest.Set("web.enabled=false")},
		},
		{
			name: "a scrape rule with no namespace",
			text: "metrics.serviceMonitor.namespace is required",
			opts: []helmtest.Option{helmtest.Values("ci/lint-values.yaml"),
				helmtest.Set("metrics.serviceMonitor.enabled=true")},
		},
		{
			name: "the page published with nothing allowed to reach it",
			text: "web.allowFrom is empty",
			opts: []helmtest.Option{helmtest.Values("ci/lint-values.yaml"),
				helmtest.SetJSON("web.allowFrom", `[]`)},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := helmtest.RenderErr(t, "bosun", tc.opts...)
			if tc.text != "" {
				if !strings.Contains(msg, tc.text) {
					t.Fatalf("the chart refused this, but not for the reason this row names.\n"+
						"  wanted: %s\n  got: %s\n\n"+
						"A cross-field rule has only the `fail` guard in _helpers.tpl behind it; "+
						"a guard that stops firing is one that has silently become a comment.",
						tc.text, strings.TrimSpace(msg))
				}
				return
			}
			if !namesValue(msg, tc.names) {
				t.Fatalf("the chart refused this without naming %s.\n  got: %s\n\n"+
					"An operator reading the refusal has to be able to find the value it is about.",
					tc.names, strings.TrimSpace(msg))
			}
		})
	}
}

// namesValue reports whether a refusal names a value, in either of the two
// spellings the chart's two refusal mechanisms use: values.schema.json points
// at '/triage/allowPaths', the `fail` guards write triage.allowPaths.
func namesValue(msg, dotted string) bool {
	return strings.Contains(msg, dotted) ||
		strings.Contains(msg, "/"+strings.ReplaceAll(dotted, ".", "/"))
}

// Every credential set as a file points somewhere the pod actually mounts.
//
// The two halves are written eighty lines apart in one template: the env block
// sets GIT_TOKEN_FILE to a path, and the volumeMounts block is what puts
// anything there. If they disagree the pod does not fail to render, does not
// fail to schedule, and dies at start-up on "no such file or directory" for a
// Secret whose contents are perfectly correct.
func TestEveryCredentialFileIsMounted(t *testing.T) {
	docs := helmtest.Render(t, "bosun",
		helmtest.Values("ci/lint-values.yaml"),
		helmtest.Set("credentials.mountAsFiles=true",
			"promotionAuth.existingSecret=bosun-promotion",
			"llm.existingSecret=bosun-llm", "llm.apiKeyKey=api-key"))

	env := helmtest.Env(t, docs, "placeholder")
	mounts := helmtest.Mounts(t, docs)

	checked := 0
	for _, k := range sortedKeys(env) {
		if !strings.HasSuffix(k, "_FILE") {
			continue
		}
		checked++
		path := env[k]
		if !under(path, mounts) {
			t.Errorf("%s points at %s and the container mounts nothing there (it mounts %s).\n"+
				"The env block and the volumeMounts block are the two halves of one credential. "+
				"The pod renders, schedules, and then dies on \"no such file or directory\" for a "+
				"Secret whose contents are correct.", k, path, strings.Join(mounts, ", "))
		}
	}
	if checked == 0 {
		t.Fatal("this render set no *_FILE credential at all, so the check above ran zero times; " +
			"credentials.mountAsFiles no longer produces file-mounted credentials")
	}
}

func under(path string, dirs []string) bool {
	for _, d := range dirs {
		if path == d || strings.HasPrefix(path, strings.TrimSuffix(d, "/")+"/") {
			return true
		}
	}
	return false
}

// withOnly installs exactly this environment, clearing everything else
// LoadConfig reads.
//
// The clear is the load-bearing half and it is easy to leave out. A developer
// with GIT_TOKEN exported would otherwise mask a chart that had stopped setting
// it, and the test would pass on their machine and nowhere else. Empty is what
// env() and validate() both treat as absent, so clearing to "" is the same as
// unsetting for every reader in that file.
func withOnly(t *testing.T, env map[string]string) {
	t.Helper()
	all, _ := configEnv(t)
	for k := range all {
		t.Setenv(k, "")
	}
	// A credential set as a file names a path inside the pod's volume mount,
	// which does not exist in a test process -- envSecret reads it and fails.
	// The path itself is asserted by TestEveryCredentialFileIsMounted; what
	// matters here is that LoadConfig can complete, so each one is pointed at
	// a real file holding a placeholder.
	dir := t.TempDir()
	for k, v := range env {
		if strings.HasSuffix(k, "_FILE") && v != "" {
			real := filepath.Join(dir, strings.ToLower(k))
			if err := os.WriteFile(real, []byte("placeholder-credential\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			v = real
		}
		t.Setenv(k, v)
	}
}

func chartEnvNames(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	note := func(name string, env map[string]string) {
		for k := range env {
			if _, seen := out[k]; !seen {
				out[k] = name
			}
		}
	}
	for _, shape := range envShapes() {
		note(shape.Name, helmtest.Env(t, helmtest.Render(t, "bosun", shape.Opts...), "placeholder"))
	}
	// Everything a switch can reach, derived from values.schema.json rather
	// than named here, so a new toggle that emits a new variable is covered
	// without this list being touched.
	note("every feature on", helmtest.Env(t, renderAll(t, true), "placeholder"))
	note("every feature off", helmtest.Env(t, renderAll(t, false), "placeholder"))
	return out
}

func configEnvNames(t *testing.T) map[string]bool {
	t.Helper()
	all, _ := configEnv(t)
	return all
}

// readers are the ways config.go reads its environment, as of this file.
//
// `b` and `secret` are the two closures LoadConfig defines to collect the
// errors its readers can return; they forward to envBool and envSecret.
var readers = map[string]bool{
	"Getenv": true, "env": true, "envBool": true, "envBoolOpt": true,
	"envInt": true, "envDur": true, "envList": true, "envSecret": true,
	"b": true, "secret": true,
}

// secretReaders also imply a `_FILE` spelling, because envSecret says so.
var secretReaders = map[string]bool{"envSecret": true, "secret": true}

// configEnv walks config.go's syntax tree for every environment variable it
// reads, returning all of them and the credentials among them.
//
// Derived rather than listed for the reason this whole file exists. There are
// three hand-written enumerations in config_test.go whose comments claim
// completeness -- "a new one that is not here is a new one that only arrives
// through the environment" -- and nothing that keeps any of them complete.
func configEnv(t *testing.T) (all map[string]bool, secrets map[string]bool) {
	all, secrets, _ = configEnvAll(t)
	return all, secrets
}

// configBools is every boolean setting config.go reads, mapped to the default
// it reads it with.
//
// The default is part of the contract and the half that is easy to get wrong:
// a setting that should be on unless somebody turns it off cannot be expressed
// by `os.Getenv(k) == "true"`, and seven reads in this file had drifted into
// exactly that idiom before envBool existed.
func configBools(t *testing.T) map[string]bool {
	t.Helper()
	_, _, bools := configEnvAll(t)
	if len(bools) < 6 {
		t.Fatalf("found only %d boolean settings in config.go; envBool is called differently "+
			"now and this walk no longer sees the defaults.", len(bools))
	}
	return bools
}

func configEnvAll(t *testing.T) (all, secrets, bools map[string]bool) {
	t.Helper()
	path := filepath.Join(helmtest.Root(t), "config.go")
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", path, err)
	}

	all, secrets, bools = map[string]bool{}, map[string]bool{}, map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name := calleeName(call.Fun)
		if !readers[name] {
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
		all[key] = true
		if secretReaders[name] {
			secrets[key] = true
			all[key+"_FILE"] = true
		}
		// envBool and the `b` closure carry their default as a second
		// argument, and the default is half of what the setting means.
		if (name == "envBool" || name == "b") && len(call.Args) == 2 {
			if id, ok := call.Args[1].(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
				bools[key] = id.Name == "true"
			}
		}
		return true
	})

	// The self-check, and not optional. These are the shapes config.go uses
	// TODAY. If the reader is ever refactored -- a table, struct tags, a
	// generated loader -- this walk finds nothing, and a test comparing two
	// empty sets reports agreement and reads exactly like a pass.
	if len(all) < 40 {
		t.Fatalf("found only %d environment variables in config.go; LoadConfig's reader "+
			"functions have moved and this walk no longer sees them. Update readers in "+
			"config_chart_test.go to name the new ones -- do not lower this number.", len(all))
	}
	return all, secrets, bools
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
