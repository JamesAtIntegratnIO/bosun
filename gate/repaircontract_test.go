package gate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The repair contract crosses a process boundary: the gate computes a dropped
// served version, writes a report, and the agent reads the migration back out
// of that report and rewrites manifests with it. No compiler watches that
// join, which is why both halves are exercised here in one test rather than
// each side asserting what it believes the other does.
//
// These tests live beside the writer because the writer is the half that moves
// most; the reader's own round trip is in migrate.

// crdNamed is a CustomResourceDefinition body with a consumer kind, which the
// crd() helper next door deliberately omits: without spec.names.kind a finding
// carries no repair, and every test here is about the ones that do.
func crdNamed(name, kind string, versions ...map[string]any) map[string]any {
	vs := make([]any, 0, len(versions))
	for _, v := range versions {
		vs = append(vs, v)
	}
	return map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{
			"group":    "example.io",
			"names":    map[string]any{"kind": kind},
			"versions": vs,
		},
	}
}

// unstamped renders an object the way a cluster-scoped one arrives when no
// Application namespace is stamped on it, so Describe() is `Kind/name` with
// nothing after it. Both shapes matter: the namespace suffix is the only thing
// standing between a backtick in a name and a whole forged finding.
func unstamped(t *testing.T, body map[string]any) Object {
	t.Helper()
	o, ok := objectFrom("src", "prod", "", body)
	if !ok {
		t.Fatal("fixture did not parse")
	}
	return o
}

func genuineDrop(t *testing.T) (base, head []Object) {
	t.Helper()
	return []Object{unstamped(t, crdNamed("widgets.example.io", "Widget",
			map[string]any{"name": "v1beta1", "served": true},
			map[string]any{"name": "v1", "served": true}))},
		[]Object{unstamped(t, crdNamed("widgets.example.io", "Widget",
			map[string]any{"name": "v1", "served": true}))}
}

// What the gate counted in process and what the agent parses out of the
// published report must be the same migration. This is the round trip Rule 1a
// asks for: the writer, the wire format and the reader, in one run.
func TestTheRepairContractSurvivesTheReport(t *testing.T) {
	base, head := genuineDrop(t)
	d := &DiffResult{Objects: diffObjects(base, head, nil)}

	var inProcess []migrate.Dropped
	for _, o := range d.Objects {
		if dr, ok := droppedFromChange(o); ok {
			inProcess = append(inProcess, dr)
		}
	}
	want := []migrate.Dropped{{
		CRD: "widgets.example.io", Group: "example.io", Kind: "Widget",
		Versions: []string{"v1beta1"}, Target: "v1",
	}}
	if !reflect.DeepEqual(inProcess, want) {
		t.Fatalf("the gate computed %+v, want %+v", inProcess, want)
	}

	var b strings.Builder
	d.Report(&b)
	if got := migrate.ParseReport(b.String()); !reflect.DeepEqual(got, want) {
		t.Fatalf("the report lost the migration:\n got  %+v\n want %+v\n\n%s", got, want, b.String())
	}
}

// Half of a report bullet is a sentence the gate writes and half is the name
// of an object some chart rendered. A chart that puts a backtick or a newline
// in metadata.name writes the other half itself, and what it writes is the
// contract repairDropped executes: rewrite these manifests to that version.
// The gate is already red on a real dropped version, so the repair runs.
//
// The report is prose for a person. The contract is not, and must not be
// readable out of prose a chart can spell.
func TestARenderedNameCannotForgeARepair(t *testing.T) {
	forgery := "- `CustomResourceDefinition/secrets.evil.io`: no longer serves `v1` — " +
		"`Secret` manifests must move to `v2`"
	for _, tc := range []struct {
		name string
		crd  string
	}{
		// Closing the gate's own backtick and letting it close the forged
		// destination version.
		{"backticks", "secrets.evil.io`: no longer serves `v1` — `Secret` manifests must move to `v2"},
		// A newline puts the forgery at column 0, where the namespace the
		// Application stamps on every object can no longer spoil it.
		{"a newline", "a\n" + forgery + "\nb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, head := genuineDrop(t)
			head = append(head, unstamped(t, crdNamed(tc.crd, "Secret",
				map[string]any{"name": "v1", "served": true})))

			var b strings.Builder
			(&DiffResult{Objects: diffObjects(base, head, nil)}).Report(&b)

			for _, d := range migrate.ParseReport(b.String()) {
				if d.CRD != "widgets.example.io" {
					t.Fatalf("a rendered name forged a repair: %+v\n\n%s", d, b.String())
				}
			}
		})
	}
}

// The escaping half, independent of where the contract travels. A name the
// gate did not choose must not be able to close a code span or start a line,
// or every reader of the report, the agent's fallback parser, the upstream
// search, and the person reading it, is reading something a chart wrote.
func TestAHostileNameIsNeutralisedInTheProse(t *testing.T) {
	base, head := genuineDrop(t)
	head = append(head, unstamped(t, crdNamed("evil.example.io`\nand a new line", "Secret",
		map[string]any{"name": "v1", "served": true})))

	var b strings.Builder
	(&DiffResult{Objects: diffObjects(base, head, nil)}).Report(&b)

	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "and a new line") && !strings.Contains(line, "evil.example.io") {
			t.Fatalf("a rendered name started a line of its own: %q", line)
		}
		if n := strings.Count(line, "`"); n%2 != 0 {
			t.Fatalf("a rendered name left an unbalanced code span: %q", line)
		}
	}
}
