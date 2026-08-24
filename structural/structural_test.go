package structural

import (
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func doc(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func schema(t *testing.T, s string) Schema {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		t.Fatal(err)
	}
	return Schema(m)
}

// The schema a chart moved to: `spec.store` became `spec.secretStoreRef.name`,
// and `spec.refreshInterval` gained a requirement.
const targetSchema = `
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
    required: [secretStoreRef, refreshInterval]
    properties:
      secretStoreRef:
        type: object
        required: [name]
        properties:
          name: {type: string}
          kind: {type: string, enum: [SecretStore, ClusterSecretStore]}
      refreshInterval: {type: string, default: 1h}
      target:
        type: object
        properties:
          name: {type: string}
`

// The most valuable outcome, and the most common: the plain apiVersion swap was
// complete, no model is called, and nothing costs anything.
func TestACleanSwapProducesNoFindings(t *testing.T) {
	d := doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  secretStoreRef: {name: store, kind: ClusterSecretStore}
  refreshInterval: 1h
  target: {name: token}
`)
	if fs := Check(d, schema(t, targetSchema)); len(fs) > 0 {
		t.Fatalf("a document that fits was flagged: %v", fs)
	}
}

// The failure the swap cannot see. `spec.store` parses, applies, and is pruned
// by the apiserver on the way in -- so the value is gone and nothing in the
// repository, the render or the gate can tell.
func TestAFieldTheTargetPrunesIsFound(t *testing.T) {
	d := doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token}
spec:
  store: my-store
  secretStoreRef: {name: store}
  refreshInterval: 1h
`)
	fs := Check(d, schema(t, targetSchema))
	if len(fs) != 1 || fs[0].Kind != Rejected || fs[0].Path != "spec.store" {
		t.Fatalf("findings = %v", fs)
	}
}

func TestANewlyRequiredFieldIsFound(t *testing.T) {
	d := doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token}
spec:
  secretStoreRef: {name: store}
`)
	fs := Check(d, schema(t, targetSchema))
	if len(fs) != 1 || fs[0].Kind != Missing || fs[0].Path != "spec.refreshInterval" {
		t.Fatalf("findings = %v", fs)
	}
}

// A false Rejected costs a model call and a diff somebody has to read, on a
// document that was fine. CRDs model free-form sections this way and metadata
// is the commonest of them.
func TestPreservedUnknownFieldsAreNotJudged(t *testing.T) {
	d := doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: token
  annotations: {argocd.argoproj.io/sync-wave: "1"}
  labels: {app: x}
spec:
  secretStoreRef: {name: store}
  refreshInterval: 1h
`)
	if fs := Check(d, schema(t, targetSchema)); len(fs) > 0 {
		t.Fatalf("free-form metadata was judged: %v", fs)
	}
}

// A schema this walker does not understand means "not judged", not "rejected".
func TestAnAlternationIsNotJudged(t *testing.T) {
	s := schema(t, `
type: object
properties:
  spec:
    oneOf:
      - {type: object, properties: {a: {type: string}}}
      - {type: object, properties: {b: {type: string}}}
`)
	d := doc(t, "spec:\n  c: anything\n")
	if fs := Check(d, s); len(fs) > 0 {
		t.Fatalf("a oneOf was judged: %v", fs)
	}
}

// ---- the three validators ----

const swapped = `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  store: my-store
  refreshInterval: 1h
`

func TestACorrectMigrationIsAccepted(t *testing.T) {
	got := Validate(
		doc(t, swapped),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  secretStoreRef: {name: my-store}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if !got.OK() {
		t.Fatalf("a correct migration was refused: %v", got.Refusals)
	}
	if len(got.Lost) != 0 {
		t.Fatalf("nothing was dropped and %v was reported", got.Lost)
	}
}

// A migration that renames the object is not a migration: the gate would count
// it as one object appearing and another vanishing.
func TestARenamedObjectIsRefused(t *testing.T) {
	got := Validate(
		doc(t, swapped),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token-v2, namespace: apps}
spec:
  secretStoreRef: {name: my-store}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if got.OK() || !strings.Contains(strings.Join(got.Refusals, " "), "metadata.name changed") {
		t.Fatalf("refusals = %v", got.Refusals)
	}
}

// The check that makes the model a translator rather than an author. A
// plausible value that is in neither the document nor the schema was made up,
// and it would render perfectly.
func TestAnInventedValueIsRefused(t *testing.T) {
	got := Validate(
		doc(t, swapped),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  secretStoreRef: {name: vault-backend}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if got.OK() {
		t.Fatal("a store name nobody wrote down was accepted")
	}
	if !strings.Contains(strings.Join(got.Refusals, " "), "vault-backend") {
		t.Fatalf("refusals = %v", got.Refusals)
	}
}

// The other half: a value the SCHEMA dictates is not an invention. Without this
// the provenance check refuses every correct migration that has to fill in a
// newly required field.
func TestAValueTheSchemaItselfDictatesIsNotAnInvention(t *testing.T) {
	got := Validate(
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token}
spec:
  secretStoreRef: {name: my-store}
`),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token}
spec:
  secretStoreRef: {name: my-store, kind: ClusterSecretStore}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if !got.OK() {
		t.Fatalf("a schema default and enum member were treated as inventions: %v", got.Refusals)
	}
}

// A proposal that still does not fit has not solved anything. This is the
// apiserver's objection, raised before the apply rather than after it.
func TestAProposalThatStillDoesNotFitIsRefused(t *testing.T) {
	got := Validate(
		doc(t, swapped),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  store: my-store
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if got.OK() {
		t.Fatal("a proposal carrying the original problem was accepted")
	}
}

// Losing a value is not automatically wrong -- a field the target no longer
// accepts has to go somewhere, and sometimes nowhere. It is always REPORTED,
// so a human sees exactly what a migration dropped.
func TestADroppedValueIsReportedRatherThanHidden(t *testing.T) {
	got := Validate(
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token}
spec:
  legacyOnly: gone-forever
  secretStoreRef: {name: my-store}
  refreshInterval: 1h
`),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token}
spec:
  secretStoreRef: {name: my-store}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if !got.OK() {
		t.Fatalf("dropping a field the target rejects was refused: %v", got.Refusals)
	}
	if len(got.Lost) != 1 || got.Lost[0] != "gone-forever" {
		t.Fatalf("Lost = %v", got.Lost)
	}
}

// The apiVersion is the one thing the deterministic swap already decided, and
// a proposal is not allowed to revisit it.
func TestAProposalCannotChooseItsOwnApiVersion(t *testing.T) {
	got := Validate(
		doc(t, swapped),
		doc(t, `
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  secretStoreRef: {name: my-store}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if got.OK() || !strings.Contains(strings.Join(got.Refusals, " "), "apiVersion") {
		t.Fatalf("refusals = %v", got.Refusals)
	}
}

// The check that a set-membership version of provenance let through, live:
// a newly required `secretStoreRef.name` filled with the object's own
// `metadata.name`. Every value was "from the document". The document now
// referenced a store nobody had created, and it would have rendered perfectly.
func TestAValueBorrowedFromAFieldThatDidNotMoveIsRefused(t *testing.T) {
	got := Validate(
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: registry-credentials, namespace: apps}
spec:
  refreshInterval: 1h
`),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: registry-credentials, namespace: apps}
spec:
  secretStoreRef: {name: registry-credentials}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if got.OK() {
		t.Fatal("a required field was filled from an unrelated field and accepted")
	}
	if !strings.Contains(strings.Join(got.Refusals, " "), "spec.secretStoreRef.name") {
		t.Fatalf("refusals = %v", got.Refusals)
	}
}

// The other side of the same rule: a value under a path the target schema
// REJECTS is exactly what a migration is for, and must be allowed to appear
// somewhere new.
func TestAValueDisplacedByTheSchemaChangeMayMove(t *testing.T) {
	got := Validate(
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  store: platform-store
  refreshInterval: 1h
`),
		doc(t, `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec:
  secretStoreRef: {name: platform-store}
  refreshInterval: 1h
`),
		"external-secrets.io/v1", schema(t, targetSchema))
	if !got.OK() {
		t.Fatalf("the migration this exists to perform was refused: %v", got.Refusals)
	}
}

// The prompt carries two schemas, and the largest on a real cluster renders to
// nearly 44,000 characters. Left uncapped that crowds out the document the
// migration is supposed to be about.
//
// Truncating is safe here and nowhere else: the validators run against the FULL
// schema whatever the prompt showed, so a model that never saw the destination
// field cannot produce a proposal that passes schema-validity. The cost is a
// refusal, never a bad write.
func TestAnEnormousSchemaIsCappedAndSaysSo(t *testing.T) {
	props := map[string]any{}
	for i := 0; i < 4000; i++ {
		props[fmt.Sprintf("field%04d", i)] = map[string]any{"type": "string"}
	}
	got := RenderSchema(Schema{"type": "object", "properties": props})
	if len(got) <= MaxSchemaChars {
		t.Fatalf("rendered %d chars, want it capped near %d", len(got), MaxSchemaChars)
	}
	if !strings.Contains(got, "schema truncated") {
		t.Error("a truncated schema did not say it was truncated")
	}
	body, _, found := strings.Cut(got, "[schema truncated")
	if !found || !strings.HasSuffix(body, "\n\n") {
		t.Error("the cut landed mid-line, so the last field shown is half a name")
	}
}

// A schema that fits is untouched -- the common case must not grow a footer.
func TestASchemaThatFitsIsNotAnnotated(t *testing.T) {
	got := RenderSchema(schema(t, targetSchema))
	if strings.Contains(got, "truncated") {
		t.Errorf("a small schema was marked truncated:\n%s", got)
	}
}
