package structural

import (
	"strings"
	"testing"
)

// The defect this diff was rewritten for.
//
// cert-manager v1 moves `organization` under `subject.organizations`. The list
// item itself does not move a column, so its line is byte-identical on both
// sides, and the set-difference implementation printed it on neither. The
// comment showed the value being deleted into an empty field, directly above a
// "Values not carried across" line. A reader had every reason to believe the
// migration had lost it.
func TestDiffKeepsAMovedValueVisible(t *testing.T) {
	before := strings.Join([]string{
		"spec:",
		"  commonName: platform.localtest.me",
		"  organization:",
		"    - Example Platform Team",
		"  secretName: platform-tls",
	}, "\n")
	after := strings.Join([]string{
		"spec:",
		"  commonName: platform.localtest.me",
		"  secretName: platform-tls",
		"  subject:",
		"    organizations:",
		"    - Example Platform Team",
	}, "\n")

	got := Diff(before, after)
	if !strings.Contains(got, "Example Platform Team") {
		t.Fatalf("the moved value is invisible in the diff:\n%s", got)
	}
	if !strings.Contains(got, "+  subject:") || !strings.Contains(got, "-  organization:") {
		t.Fatalf("the key move is not shown:\n%s", got)
	}
}

func TestDiffShowsContextAroundAChange(t *testing.T) {
	before := "a\nb\nc\nd\ne"
	after := "a\nb\nC\nd\ne"
	got := Diff(before, after)
	for _, want := range []string{" b", "-c", "+C", " d"} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}

// Identical documents produce nothing at all, not a wall of context.
func TestDiffOfNoChangeIsEmpty(t *testing.T) {
	doc := "a\nb\nc"
	if got := Diff(doc, doc); got != "" {
		t.Fatalf("want empty, got:\n%s", got)
	}
}

// Unchanged stretches far from any change are dropped, so a long document does
// not bury the three lines that moved.
func TestDiffElidesDistantUnchangedLines(t *testing.T) {
	var b, a []string
	for i := 0; i < 40; i++ {
		b = append(b, "line")
		a = append(a, "line")
	}
	b = append(b, "old")
	a = append(a, "new")
	got := Diff(strings.Join(b, "\n"), strings.Join(a, "\n"))
	if n := strings.Count(got, "\n"); n > 2*diffContext+2 {
		t.Fatalf("want a hunk, got %d lines:\n%s", n, got)
	}
	if !strings.Contains(got, "-old") || !strings.Contains(got, "+new") {
		t.Fatalf("the change is missing:\n%s", got)
	}
}

// A duplicated line must not be collapsed. The old implementation counted
// membership, not multiplicity, so removing one of two identical lines showed
// as no change at all.
func TestDiffCountsRepeatedLines(t *testing.T) {
	got := Diff("x\nx\ny", "x\ny")
	if !strings.Contains(got, "-x") {
		t.Fatalf("a removed duplicate is invisible:\n%s", got)
	}
}

// --respelled is not lost,--

const certTarget = `
type: object
required: [apiVersion, kind, spec]
properties:
  apiVersion: {type: string}
  kind: {type: string}
  metadata:
    type: object
    x-kubernetes-preserve-unknown-fields: true
  spec:
    type: object
    properties:
      privateKey:
        type: object
        properties:
          algorithm: {type: string, enum: [RSA, ECDSA, Ed25519]}
      commonName: {type: string}
`

// cert-manager v1 spells the key algorithm `ECDSA` where v1alpha2 spelled it
// `ecdsa`, and the enum is what dictates the new spelling. Reported as lost,
// that says the migration dropped a value on exactly the bump where it did its
// job, directly beneath the diff that carried it.
func TestRespelledIsNotLost(t *testing.T) {
	got := Validate(
		// The apiVersion is already the target: the deterministic swap runs
		// first and the model is only ever shown a document whose version has
		// moved. Feeding the pre-swap document here would refuse on the
		// apiVersion itself and test the wrong thing.
		doc(t, `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: platform-tls, namespace: gateway}
spec:
  keyAlgorithm: ecdsa
`),
		doc(t, `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: platform-tls, namespace: gateway}
spec:
  privateKey: {algorithm: ECDSA}
`),
		"cert-manager.io/v1", schema(t, certTarget))
	if !got.OK() {
		t.Fatalf("refused: %v", got.Refusals)
	}
	for _, l := range got.Lost {
		if l == "ecdsa" {
			t.Fatalf("a schema-dictated respelling was reported as lost: %v", got.Lost)
		}
	}
	if len(got.Respelled) != 1 || got.Respelled[0] != "ecdsa -> ECDSA" {
		t.Fatalf("want [ecdsa -> ECDSA], got %v", got.Respelled)
	}
}

// The escape hatch must not become one. A value the model changed on its own
// authority is not a respelling, however similar it looks, the target
// schema's vocabulary is the only thing that can excuse a new spelling.
func TestACaseChangeTheSchemaDidNotDictateIsStillLost(t *testing.T) {
	got := Validate(
		doc(t, `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: platform-tls, namespace: gateway}
spec:
  commonName: Platform.Localtest.Me
`),
		doc(t, `
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: platform-tls, namespace: gateway}
spec:
  commonName: platform.localtest.me
`),
		"cert-manager.io/v1", schema(t, certTarget))
	if len(got.Respelled) != 0 {
		t.Fatalf("a value the schema does not dictate was excused as respelled: %v", got.Respelled)
	}
	found := false
	for _, l := range got.Lost {
		if l == "Platform.Localtest.Me" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want the original spelling reported lost, got %v", got.Lost)
	}
}
