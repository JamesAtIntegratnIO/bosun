package agent

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

// TestLiveCertManagerMigration is the structural migration against a real
// upgrade that real people had to survive.
//
// cert-manager v1.6 served `v1alpha2`, `v1alpha3`, `v1beta1` and `v1`. v1.7
// removed all but `v1`. The rename happened back in v0.16 and the old versions
// were kept alive by a conversion webhook until 1.7 dropped them -- so a
// repository that still declared `cert-manager.io/v1alpha2` had manifests that
// applied cleanly right up until the bump that deleted the version, and then
// did not.
//
// SIX fields move, in three different shapes, which is why this is a better
// example than a single nested rename:
//
//	keyAlgorithm  -> privateKey.algorithm      into a nested object
//	keySize       -> privateKey.size
//	keyEncoding   -> privateKey.encoding
//	emailSANs     -> emailAddresses            renamed in place
//	uriSANs       -> uris
//	organization  -> subject.organizations     into a different nested object
//
// Schemas are rendered from the published charts, not written here:
//
//	helm template cm cert-manager --repo https://charts.jetstack.io \
//	  --version v1.6.3 --include-crds --set installCRDs=true
//
//	CM_OLD_SCHEMA=cm-old-v1alpha2.json CM_NEW_SCHEMA=cm-new-v1.json \
//	DELIVERY_AGENT_LIVE=http://host:1234/v1 DELIVERY_AGENT_MODELS=qwen/qwen3.8-27b \
//	go test . -run LiveCertManager -v
func TestLiveCertManagerMigration(t *testing.T) {
	oldPath, newPath := os.Getenv("CM_OLD_SCHEMA"), os.Getenv("CM_NEW_SCHEMA")
	base := os.Getenv("DELIVERY_AGENT_LIVE")
	if oldPath == "" || newPath == "" || base == "" {
		t.Skip("set CM_OLD_SCHEMA, CM_NEW_SCHEMA and DELIVERY_AGENT_LIVE")
	}
	oldS, newS := schemaFile(t, oldPath), schemaFile(t, newPath)

	// A Certificate in the shape a 2021 repository would hold it, with its
	// apiVersion ALREADY swapped to v1 -- the state the deterministic pass
	// leaves behind, and the state whose remaining problem nothing else sees.
	const doc = `apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: platform-tls
  namespace: gateway
spec:
  secretName: platform-tls
  duration: 2160h
  renewBefore: 360h
  commonName: platform.example.internal
  dnsNames:
    - platform.example.internal
  emailSANs:
    - platform@example.internal
  uriSANs:
    - spiffe://example.internal/platform
  organization:
    - Example Platform Team
  keyAlgorithm: ecdsa
  keySize: 384
  keyEncoding: pkcs8
  issuerRef:
    name: internal-ca
    kind: ClusterIssuer
    group: cert-manager.io
`
	var original map[string]any
	if err := yaml.Unmarshal([]byte(doc), &original); err != nil {
		t.Fatal(err)
	}

	findings := structural.Check(original, newS)
	t.Logf("DETECTOR: %d finding(s) -- what v1 will not accept", len(findings))
	for _, f := range findings {
		t.Logf("   %s", f)
	}
	if len(findings) == 0 {
		t.Fatal("no findings, so there is nothing to demonstrate")
	}

	p := &llm.OpenAI{BaseURL: base, Model: os.Getenv("DELIVERY_AGENT_MODELS"),
		APIKey: os.Getenv("DELIVERY_AGENT_KEY"), Timeout: 15 * time.Minute}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	start := time.Now()
	m, err := p.Restructure(ctx, prompt.Restructure,
		structural.Prompt("gateway/platform-tls.yaml", doc, "v1alpha2", "v1", oldS, newS, findings))
	if err != nil {
		t.Fatalf("model: %v", err)
	}
	t.Logf("\nMODEL (%s, %s)\n   notes: %s\n--- proposed document ---\n%s",
		p.Name(), time.Since(start).Round(time.Second), m.Notes, m.Document)

	var proposed map[string]any
	if err := yaml.Unmarshal([]byte(m.Document), &proposed); err != nil {
		t.Fatalf("the proposal is not a YAML document: %v", err)
	}
	v := structural.Validate(original, proposed, "cert-manager.io/v1", newS)
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
}

func schemaFile(t *testing.T, path string) structural.Schema {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return structural.Schema(m)
}
