package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The failure the apiVersion swap cannot see.
//
// `spec.store` becomes `spec.secretStoreRef.name` between v1beta1 and v1. Swap
// the version alone and the document parses, applies, and has `store` pruned by
// the apiserver on the way in. The render is fine, the gate goes GREEN, and the
// value is gone. Everything here is about catching that -- and about refusing
// the cure when it is worse than the disease.

const esOldSchema = `{
 "type":"object",
 "properties":{
  "apiVersion":{"type":"string"},"kind":{"type":"string"},
  "metadata":{"type":"object","x-kubernetes-preserve-unknown-fields":true},
  "spec":{"type":"object","properties":{"store":{"type":"string"},"refreshInterval":{"type":"string"}}}
 }
}`

const esNewSchema = `{
 "type":"object",
 "properties":{
  "apiVersion":{"type":"string"},"kind":{"type":"string"},
  "metadata":{"type":"object","x-kubernetes-preserve-unknown-fields":true},
  "spec":{
   "type":"object","required":["secretStoreRef"],
   "properties":{
    "secretStoreRef":{"type":"object","required":["name"],
      "properties":{"name":{"type":"string"},"kind":{"type":"string","enum":["SecretStore","ClusterSecretStore"]}}},
    "refreshInterval":{"type":"string"}
   }
  }
 }
}`

const consumerBefore = `apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: token
  namespace: apps
spec:
  store: my-store
  refreshInterval: 1h
`

// A report whose only blocking finding is one CRD dropping one version.
func oneDropReport() string {
	return gateReportMarker + "\n### Resources\n\n" +
		"**A CustomResourceDefinition stopped serving a version**\n\n" +
		migrate.Line("CustomResourceDefinition/externalsecrets.external-secrets.io",
			"v1beta1", "ExternalSecret", "v1") + "\n"
}

func restructureHarness(t *testing.T, schemas map[string]string) *harness {
	t.Helper()
	h := newHarness(t)
	h.triage.Migrate = true
	h.triage.Structural = true
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: oneDropReport()}}
	h.writeFile(t, "addons/external-secrets/externalsecret.yaml", consumerBefore)

	crd := cluster.CRD{Known: true, Versions: []string{"v1"}, Schemas: map[string]map[string]any{}}
	for version, raw := range schemas {
		crd.Schemas[version] = jsonMap(t, raw)
	}
	h.triage.Cluster = &cluster.Fake{
		CRDs:   map[string]cluster.CRD{"externalsecrets.external-secrets.io": crd},
		Counts: map[string]cluster.Count{"external-secrets.io/v1beta1/externalsecrets": {Known: true}},
	}
	return h
}

func bothSchemas() map[string]string {
	return map[string]string{"v1beta1": esOldSchema, "v1": esNewSchema}
}

// The whole feature: the swap happens, the document no longer fits, the model
// proposes, the harness checks, and one commit carries both.
func TestASwapThatLeavesADocumentUnfitIsReshapedAndPushedTogether(t *testing.T) {
	h := restructureHarness(t, bothSchemas())
	h.model.Migration = &llm.Migration{
		Notes: "spec.store became spec.secretStoreRef.name",
		Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: token
  namespace: apps
spec:
  secretStoreRef:
    name: my-store
  refreshInterval: 1h
`}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("pushes = %d, want one commit carrying both halves", len(h.git.Pushes))
	}
	got := h.git.Pushes[0].Tree["addons/external-secrets/externalsecret.yaml"]
	if !strings.Contains(got, "external-secrets.io/v1\n") {
		t.Errorf("the apiVersion was not swapped:\n%s", got)
	}
	if !strings.Contains(got, "secretStoreRef") || strings.Contains(got, "store: my-store") {
		t.Errorf("the document was not reshaped:\n%s", got)
	}
	// The value came across. That is the whole point.
	if !strings.Contains(got, "my-store") {
		t.Errorf("the value was lost in the reshape:\n%s", got)
	}
	// The comment shows exactly what was reshaped, and the footer stops
	// claiming no model was involved.
	body := h.git.Posted[0]
	if !strings.Contains(body, "Reshaped for the new schema") || !strings.Contains(body, "```diff") {
		t.Errorf("the comment does not show the reshape:\n%s", body)
	}
	if strings.Contains(body, "deterministic repair, no model") {
		t.Errorf("the footer still claims no model was involved:\n%s", body)
	}
}

// The common case, and the one that must stay free: the swap was the whole job,
// so the model is never called.
func TestACleanSwapNeverCallsTheModel(t *testing.T) {
	h := restructureHarness(t, map[string]string{
		"v1beta1": esOldSchema,
		// A target schema that accepts what the document already is.
		"v1": esOldSchema,
	})

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.MigrationCalls != 0 {
		t.Fatalf("the model was consulted %d time(s) on a document that already fitted", h.model.MigrationCalls)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("the plain swap did not land: %+v", h.git.Pushes)
	}
	if !strings.Contains(h.git.Posted[0], "deterministic repair, no model") {
		t.Error("the footer does not say this was arithmetic")
	}
}

// The check that makes the model a translator rather than an author. A store
// name nobody wrote down would render perfectly.
func TestAnInventedValueIsRefusedAndNothingIsPushed(t *testing.T) {
	h := restructureHarness(t, bothSchemas())
	h.model.Migration = &llm.Migration{
		Notes: "moved the store reference",
		Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: token
  namespace: apps
spec:
  secretStoreRef:
    name: vault-backend
  refreshInterval: 1h
`}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("a document carrying an invented value was pushed")
	}
	if !has(h.git.Labelled, labelNeedsHuman) {
		t.Fatal("the refusal did not reach a human")
	}
	if !strings.Contains(h.git.Posted[0], "vault-backend") {
		t.Errorf("the comment does not say what was refused:\n%s", h.git.Posted[0])
	}
}

// A migration that renames the object is a different change riding inside one.
func TestARenamedObjectIsRefused(t *testing.T) {
	h := restructureHarness(t, bothSchemas())
	h.model.Migration = &llm.Migration{Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: token-v2
  namespace: apps
spec:
  secretStoreRef:
    name: my-store
`}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("a renamed object was pushed")
	}
	if !strings.Contains(h.git.Posted[0], "metadata.name changed") {
		t.Errorf("the comment does not name the refusal:\n%s", h.git.Posted[0])
	}
}

// The part that is not the obvious choice. A partial push makes the gate GREEN
// -- no manifest declares a dropped version any more -- over a document the
// apiserver will silently prune. That is the exact failure shape this service
// exists to find.
func TestNothingIsPushedWhenAnyDocumentIsRefused(t *testing.T) {
	h := restructureHarness(t, bothSchemas())
	h.writeFile(t, "addons/external-secrets/second.yaml", strings.Replace(consumerBefore, "token", "other", 1))
	h.model.Migrations = []*llm.Migration{
		{Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: other, namespace: apps}
spec: {secretStoreRef: {name: my-store}, refreshInterval: 1h}
`},
		{Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: token, namespace: apps}
spec: {secretStoreRef: {name: invented}, refreshInterval: 1h}
`},
	}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("a half-migrated set was pushed, which would render green over a broken document")
	}
	if !strings.Contains(h.git.Posted[0], "nothing was pushed") {
		t.Errorf("the comment does not say nothing was pushed:\n%s", h.git.Posted[0])
	}
}

// Without both schemas there is nothing to check against, so the plain swap
// ships -- and says that is all it did.
func TestWithoutBothSchemasThePlainSwapShipsAndSaysSo(t *testing.T) {
	h := restructureHarness(t, map[string]string{"v1beta1": esOldSchema})

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.MigrationCalls != 0 {
		t.Fatal("a document was reshaped against a schema nobody had")
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("the plain swap did not land: %+v", h.git.Pushes)
	}
	if !strings.Contains(h.git.Posted[0], "Not checked for structural changes") {
		t.Errorf("the comment does not say what was not checked:\n%s", h.git.Posted[0])
	}
}

// Off is off: the behaviour that existed before this, unchanged.
func TestWithStructuralOffTheSwapIsTheWholeJob(t *testing.T) {
	h := restructureHarness(t, bothSchemas())
	h.triage.Structural = false

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.MigrationCalls != 0 {
		t.Fatal("the model was called with the feature off")
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("the plain swap did not land: %+v", h.git.Pushes)
	}
}

// The cap is per pull request and the remainder is named rather than silently
// left behind.
func TestTheDocumentCapEscalatesTheRemainder(t *testing.T) {
	h := restructureHarness(t, bothSchemas())
	h.triage.MaxRestructured = 1
	h.writeFile(t, "addons/external-secrets/second.yaml", strings.Replace(consumerBefore, "token", "other", 1))
	h.model.Migration = &llm.Migration{Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: {name: other, namespace: apps}
spec: {secretStoreRef: {name: my-store}, refreshInterval: 1h}
`}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.MigrationCalls != 1 {
		t.Fatalf("the cap was not applied: %d call(s)", h.model.MigrationCalls)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("a capped pass pushed anyway")
	}
	if !strings.Contains(h.git.Posted[0], "limit of 1 document migrations") {
		t.Errorf("the comment does not name the cap:\n%s", h.git.Posted[0])
	}
}

// jsonMap decodes a schema fixture. JSON rather than YAML in these fixtures
// because an OpenAPI schema is mostly punctuation and the indentation of a
// YAML one buries the two fields each test is actually about.
func jsonMap(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// ADR 0007 promises the live-CRD fallback is "labelled as one in the comment".
// It was not: the note was attached to the schema pair and then only surfaced
// when the pair was INCOMPLETE -- which is exactly when the fallback had not
// been used. A fallback that works was silent, which is the wrong way round.
//
// It matters because a target schema taken from what the cluster serves TODAY
// predates the bump. It can miss a field the new chart version added, so a
// clean result carries less confidence than a clean result checked against the
// chart's own schema -- and only the comment can tell those apart.
func TestAFallbackSchemaSaysSoEvenWhenItWorked(t *testing.T) {
	h := restructureHarness(t, bothSchemas())
	// No helm on PATH in the test environment, so the target schema can only
	// have come from the live CustomResourceDefinition -- the fallback.
	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) == 0 {
		t.Fatal("nothing was posted")
	}
	body := h.git.Posted[0]
	if !strings.Contains(body, "Which schema the check used") {
		t.Fatalf("the comment does not say where the target schema came from:\n%s", body)
	}
	if !strings.Contains(body, "predates this bump") {
		t.Fatalf("the comment does not say the fallback may be stale:\n%s", body)
	}
}
