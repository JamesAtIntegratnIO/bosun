// Package helmtest renders the charts this repository ships, from a Go test.
//
// internal/charttest is the other half of the same idea and deliberately not
// the same package: charttest *builds* a two-version fixture chart on loopback
// for the gate to render against, this one *renders* charts/bosun and
// charts/kargo-pipelines. They share a shell-out to helm and nothing else.
//
// Why this exists at all. Every check on these charts has until now been a
// `helm template ... >/dev/null` in hack/portability-test.sh, which asks one
// question: did it render. That question is worth asking -- helm parses what it
// renders, so a template emitting something which is not YAML fails inside helm
// and never reaches here, and that is the whole of the 0.25.0 ClusterRole
// check with no parser needed on this side. But it is the only question bash
// could ask cheaply, and the failures since have all been the other kind: the
// document rendered, and said the wrong thing. `gate.argocd.port` opened a
// Service port where the packet had already been DNAT'd to a pod port; it
// rendered clean, linted clean, passed the values schema, and dropped every
// packet.
//
// So documents come back parsed, and the assertions are about what the render
// SAYS. hack/portability-test.sh needed an awk state machine to ask "what port
// did the egress rule open"; that question has a natural home here, where
// gopkg.in/yaml.v3 is already a dependency.
package helmtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/JamesAtIntegratnIO/bosun/childenv"
	"github.com/JamesAtIntegratnIO/bosun/internal/charttest"
)

// Doc is one document out of a render, both parsed and as helm wrote it.
//
// Raw is kept because some assertions are about the text rather than the tree:
// whether a body is a YAML string scalar rather than a mapping is a question
// the parse has already answered by the time you can ask it of Body.
type Doc struct {
	Kind, Name, Namespace string
	Body                  map[string]any
	Raw                   string
}

type args struct {
	values   []string
	sets     []string
	setStr   []string
	setJSON  []string
	showOnly []string
}

// Option is one argument to a render.
type Option func(*args)

// Values adds a `-f`, named relative to the chart directory, so a caller says
// Values("ci/lint-values.yaml") rather than restating where charts live.
func Values(rel string) Option { return func(a *args) { a.values = append(a.values, rel) } }

// Set adds `--set` arguments, each "path=value".
func Set(kv ...string) Option { return func(a *args) { a.sets = append(a.sets, kv...) } }

// SetString adds a `--set-string`, which is what a value the schema types as a
// string needs: plain --set gives helm a number for anything digit-shaped, and
// the values schema then refuses it.
func SetString(kv ...string) Option { return func(a *args) { a.setStr = append(a.setStr, kv...) } }

// SetJSON adds a `--set-json`, which is the only way to say "this list is
// empty" or "this value is null" to helm -- `--set x=null` sets the string.
func SetJSON(path, json string) Option {
	return func(a *args) { a.setJSON = append(a.setJSON, path+"="+json) }
}

// ShowOnly narrows the render to one template.
func ShowOnly(tmpl string) Option { return func(a *args) { a.showOnly = append(a.showOnly, tmpl) } }

// Dir is the absolute path of one of this repository's charts, by bare name.
//
// Resolved by walking up to go.mod rather than by a relative path, because
// tests run with cwd set to their own package directory and three packages
// call this from three different depths.
func Dir(t *testing.T, chart string) string {
	t.Helper()
	return filepath.Join(Root(t), "charts", chart)
}

// Root is the repository root: the directory holding go.mod.
func Root(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not name this file; helmtest cannot find the repository root")
	}
	dir := filepath.Dir(self)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("walked up from %s without finding go.mod", self)
		}
		dir = parent
	}
}

func command(t *testing.T, chart string, opts ...Option) *exec.Cmd {
	t.Helper()
	charttest.RequireTool(t, "helm")

	var a args
	for _, o := range opts {
		o(&a)
	}
	dir := Dir(t, chart)
	argv := []string{"template", "t", dir}
	for _, v := range a.values {
		argv = append(argv, "-f", filepath.Join(dir, v))
	}
	for _, s := range a.sets {
		argv = append(argv, "--set", s)
	}
	for _, s := range a.setStr {
		argv = append(argv, "--set-string", s)
	}
	for _, s := range a.setJSON {
		argv = append(argv, "--set-json", s)
	}
	for _, s := range a.showOnly {
		argv = append(argv, "--show-only", s)
	}
	cmd := exec.Command("helm", argv...)
	// Test support, and the same rule anyway. childenv is unprimed in a test
	// binary, so this is os.Environ() -- but writing it here means the rule in
	// subprocess_env_test.go needs no exception, and an exception is what a
	// rule gets weakened through.
	cmd.Env = childenv.Environ()
	return cmd
}

// Render runs `helm template` and returns every document it produced, failing
// the test on a non-zero exit with helm's own message -- which is already the
// best available description of what is wrong with a template.
func Render(t *testing.T, chart string, opts ...Option) []Doc {
	t.Helper()
	cmd := command(t, chart, opts...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		t.Fatalf("%s failed to render:\n%s\n\n%v", chart, stderr, cmd.Args)
	}
	return split(t, string(out))
}

// RenderErr is Render's other half: the render that MUST fail, returning the
// message it failed with.
//
// charts/bosun/templates/_helpers.tpl refuses twelve value combinations by
// name, and every one of them is a values mistake that would otherwise reach a
// cluster as a CrashLoop three files from its cause. A `fail` that stops firing
// is a guard that has silently become a comment, and nothing about a running
// install would ever show it.
func RenderErr(t *testing.T, chart string, opts ...Option) string {
	t.Helper()
	cmd := command(t, chart, opts...)
	out, err := cmd.Output()
	if err == nil {
		t.Fatalf("%s rendered, and this combination of values must be refused.\n"+
			"Add or restore the guard in charts/%s/templates/_helpers.tpl.\n\n%s",
			chart, chart, out)
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return string(ee.Stderr)
	}
	t.Fatalf("helm could not be run at all: %v", err)
	return ""
}

// docSep matches the document separator helm writes. Splitting on it rather
// than decoding a stream keeps Raw available, and helm always emits it on its
// own line.
var docSep = regexp.MustCompile(`(?m)^---[ \t]*$`)

func split(t *testing.T, out string) []Doc {
	t.Helper()
	var docs []Doc
	for _, chunk := range docSep.Split(out, -1) {
		if strings.TrimSpace(stripComments(chunk)) == "" {
			continue
		}
		var body map[string]any
		if err := yaml.Unmarshal([]byte(chunk), &body); err != nil {
			// Unreachable through helm, which parses what it renders. Reachable
			// if this splitter is ever wrong, and silence would look like a
			// chart that stopped emitting a document.
			t.Fatalf("a rendered document did not parse, which means this splitter is "+
				"wrong rather than the chart: %v\n\n%s", err, chunk)
		}
		if body == nil {
			continue
		}
		docs = append(docs, Doc{
			Kind:      str(body["kind"]),
			Name:      str(dig(body, "metadata", "name")),
			Namespace: str(dig(body, "metadata", "namespace")),
			Body:      body,
			Raw:       chunk,
		})
	}
	return docs
}

func stripComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// Of returns the documents of one kind.
func Of(docs []Doc, kind string) []Doc {
	var out []Doc
	for _, d := range docs {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

// One returns the single document of a kind, failing when there is not exactly
// one. "Exactly one" is the assertion: a chart that started rendering two
// Deployments is a chart whose environment no longer has one answer.
func One(t *testing.T, docs []Doc, kind string) Doc {
	t.Helper()
	got := Of(docs, kind)
	if len(got) != 1 {
		t.Fatalf("wanted exactly one %s in the render, got %d", kind, len(got))
	}
	return got[0]
}

// Env reads one container's environment out of a rendered Deployment, with
// every secretKeyRef entry resolved to the named placeholder.
//
// The placeholder is a required argument rather than a default, and that is
// deliberate: a helper that let a secretKeyRef arrive as the empty string would
// pass just as happily on a chart that had stopped setting the credential at
// all, which is the exact failure this is here to catch.
func Env(t *testing.T, docs []Doc, placeholder string) map[string]string {
	t.Helper()
	d := One(t, docs, "Deployment")

	containers, _ := dig(d.Body, "spec", "template", "spec", "containers").([]any)
	if len(containers) != 1 {
		t.Fatalf("the Deployment has %d containers; Env would have to guess which one "+
			"holds the agent's environment", len(containers))
	}
	c, _ := containers[0].(map[string]any)
	entries, _ := c["env"].([]any)

	out := make(map[string]string, len(entries))
	for _, e := range entries {
		m, _ := e.(map[string]any)
		name := str(m["name"])
		if name == "" {
			t.Fatalf("an env entry in the Deployment has no name: %v", m)
		}
		if _, dup := out[name]; dup {
			// Two entries for one name is a template that grew an addition
			// where it meant a branch. The kubelet takes the last, so this
			// would otherwise be invisible until the wrong one won.
			t.Fatalf("the Deployment sets %s twice; the kubelet takes the last, "+
				"so one of the two branches in deployment.yaml is not exclusive", name)
		}
		if v, ok := m["value"]; ok {
			out[name] = str(v)
			continue
		}
		if _, ok := m["valueFrom"]; ok {
			out[name] = placeholder
			continue
		}
		t.Fatalf("env entry %s has neither value nor valueFrom", name)
	}
	return out
}

// Mounts are the paths the Deployment's one container mounts a volume at.
//
// The other half of a credential set as a file: an environment variable naming
// a path is only a credential if something is mounted there, and the two are
// written eighty lines apart in the same template.
func Mounts(t *testing.T, docs []Doc) []string {
	t.Helper()
	d := One(t, docs, "Deployment")
	containers, _ := dig(d.Body, "spec", "template", "spec", "containers").([]any)
	if len(containers) != 1 {
		t.Fatalf("the Deployment has %d containers", len(containers))
	}
	c, _ := containers[0].(map[string]any)
	entries, _ := c["volumeMounts"].([]any)
	var out []string
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if p := str(m["mountPath"]); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func dig(m map[string]any, path ...string) any {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[p]
	}
	return cur
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
