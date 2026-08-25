package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/prompt"
	"github.com/JamesAtIntegratnIO/bosun/structural"
)

// TestLiveRestructure runs the structural migration end to end against REAL
// schemas and a real model.
//
// It exists because the path had never fired on a live promotion: every chart
// this pipeline promotes happens to have made compatible version changes, so
// the detector correctly found nothing and the model was never called. That is
// the right outcome and it demonstrates nothing.
//
// The schemas here are not invented. They are lifted from a running cluster's
// own CustomResourceDefinition -- external-secrets `v1alpha1` -> `v1beta1`,
// where the project moved `spec.dataFrom[].{key,property,version}` down into
// `spec.dataFrom[].extract`. That is a migration external-secrets actually
// shipped, and it is exactly the shape the plain apiVersion swap cannot handle:
// swap the line alone and the apiserver prunes `key` on the way in, leaving a
// manifest that renders green and silently reads nothing.
//
//	STRUCTURAL_AUDIT_CRDS=crds.json \
//	DELIVERY_AGENT_LIVE=http://host:1234/v1 DELIVERY_AGENT_MODELS=qwen/qwen3.8-27b \
//	go test . -run LiveRestructure -v
func TestLiveRestructure(t *testing.T) {
	crdPath := os.Getenv("STRUCTURAL_AUDIT_CRDS")
	base := os.Getenv("DELIVERY_AGENT_LIVE")
	if crdPath == "" || base == "" {
		t.Skip("set STRUCTURAL_AUDIT_CRDS and DELIVERY_AGENT_LIVE")
	}
	oldS, newS := esSchemas(t, crdPath)

	// A v1alpha1 ExternalSecret in the shape this repository actually writes
	// them, with its apiVersion ALREADY swapped -- the state the deterministic
	// pass leaves behind, and the state whose remaining problem nothing else
	// can see.
	const doc = `apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: grafana-admin
  namespace: observability
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: platform-store
    kind: ClusterSecretStore
  target:
    name: grafana-admin
  dataFrom:
    - key: grafana
      property: admin-password
      version: "2"
`
	var original map[string]any
	if err := yaml.Unmarshal([]byte(doc), &original); err != nil {
		t.Fatal(err)
	}

	findings := structural.Check(original, newS)
	t.Logf("DETECTOR: %d finding(s)", len(findings))
	for _, f := range findings {
		t.Logf("   %s", f)
	}
	if len(findings) == 0 {
		t.Fatal("the detector found nothing, so there is nothing to demonstrate")
	}

	p := &llm.OpenAI{
		BaseURL: base, Model: os.Getenv("DELIVERY_AGENT_MODELS"),
		APIKey: os.Getenv("DELIVERY_AGENT_KEY"), Timeout: 15 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	start := time.Now()
	m, err := p.Restructure(ctx, prompt.Restructure,
		structural.Prompt("observability/grafana-admin.yaml", doc, "v1alpha1", "v1beta1", oldS, newS, findings))
	if err != nil {
		t.Fatalf("model: %v", err)
	}
	t.Logf("\nMODEL (%s, %s)\n   notes: %s\n--- proposed document ---\n%s",
		p.Name(), time.Since(start).Round(time.Second), m.Notes, m.Document)

	var proposed map[string]any
	if err := yaml.Unmarshal([]byte(m.Document), &proposed); err != nil {
		t.Fatalf("the proposal is not a YAML document: %v", err)
	}
	v := structural.Validate(original, proposed, "external-secrets.io/v1beta1", newS)
	t.Logf("\nHARNESS VERDICT: accepted=%v", v.OK())
	for _, r := range v.Refusals {
		t.Logf("   REFUSED: %s", r)
	}
	for _, l := range v.Lost {
		t.Logf("   value not carried across: %q", l)
	}
	if v.OK() {
		if left := structural.Check(proposed, newS); len(left) > 0 {
			t.Errorf("accepted a document that still does not fit: %v", left)
		}
	}

	// The half that matters more. A correct proposal proves the prompt works; a
	// REFUSED one proves the prompt is not what anything depends on.
	//
	// Same real schemas, same original, three ways a model could get this wrong
	// -- each tampered by hand, because waiting for the model to make the
	// mistake is not a test.
	t.Log("\nWHAT THE HARNESS REFUSES (same schemas, tampered proposals)")
	for _, bad := range []struct {
		name string
		doc  string
	}{
		{"a value from nowhere", `apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata: {name: grafana-admin, namespace: observability}
spec:
  refreshInterval: 1h
  secretStoreRef: {name: platform-store, kind: ClusterSecretStore}
  target: {name: grafana-admin}
  dataFrom:
    - extract: {key: grafana-admin-credentials, property: admin-password, version: "2"}
`},
		{"a value borrowed from another field", `apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata: {name: grafana-admin, namespace: observability}
spec:
  refreshInterval: 1h
  secretStoreRef: {name: platform-store, kind: ClusterSecretStore}
  target: {name: grafana-admin}
  dataFrom:
    - extract: {key: observability, property: admin-password, version: "2"}
`},
		{"a renamed object", `apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata: {name: grafana-admin-v2, namespace: observability}
spec:
  refreshInterval: 1h
  secretStoreRef: {name: platform-store, kind: ClusterSecretStore}
  target: {name: grafana-admin}
  dataFrom:
    - extract: {key: grafana, property: admin-password, version: "2"}
`},
	} {
		var m map[string]any
		if err := yaml.Unmarshal([]byte(bad.doc), &m); err != nil {
			t.Fatal(err)
		}
		vv := structural.Validate(original, m, "external-secrets.io/v1beta1", newS)
		if vv.OK() {
			t.Errorf("   %s: ACCEPTED -- this would have been written", bad.name)
			continue
		}
		t.Logf("   %-38s refused: %s", bad.name, vv.Refusals[0])
	}
}

func esSchemas(t *testing.T, path string) (structural.Schema, structural.Schema) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []struct {
			Spec struct {
				Names    struct{ Plural string } `json:"names"`
				Versions []struct {
					Name   string `json:"name"`
					Schema struct {
						OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
					} `json:"schema"`
				} `json:"versions"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	var a, b structural.Schema
	for _, c := range list.Items {
		if c.Spec.Names.Plural != "externalsecrets" {
			continue
		}
		for _, v := range c.Spec.Versions {
			switch v.Name {
			case "v1alpha1":
				a = structural.Schema(v.Schema.OpenAPIV3Schema)
			case "v1beta1":
				b = structural.Schema(v.Schema.OpenAPIV3Schema)
			}
		}
	}
	if a == nil || b == nil {
		t.Fatal("could not find both external-secrets schemas")
	}
	return a, b
}
