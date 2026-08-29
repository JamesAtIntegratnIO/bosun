package agent

import (
	"maps"
	"slices"
	"testing"
)

// The target schema comes out of a rendered chart, and `helm
// template,include-crds` produces a manifest stream with everything else in it
// too. These are about pulling the right objects out of that and, more
// importantly, about what happens to the parts that are not objects at all.

const renderedStream = `---
# Source: es/templates/serviceaccount.yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: external-secrets
---
# Source: es/crds/externalsecret.yaml
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: externalsecrets.external-secrets.io
spec:
  group: external-secrets.io
  versions:
    - name: v1beta1
      served: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                store: {type: string}
    - name: v1
      served: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                secretStoreRef:
                  type: object
                  properties:
                    name: {type: string}
---
# Source: es/templates/deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: external-secrets
`

func TestTheTargetSchemaIsFoundInARenderedChart(t *testing.T) {
	got := crdSchemasFromStream(renderedStream)
	byVersion, ok := got["externalsecrets.external-secrets.io"]
	if !ok {
		t.Fatalf("the definition was not found: %v", slices.Sorted(maps.Keys(got)))
	}
	if len(byVersion) != 2 {
		t.Fatalf("versions = %v, want both", slices.Sorted(maps.Keys(byVersion)))
	}
	v1, ok := byVersion["v1"]
	if !ok {
		t.Fatal("the target version's schema is missing")
	}
	spec, _ := v1["properties"].(map[string]any)
	if _, ok := spec["spec"]; !ok {
		t.Fatalf("the schema did not survive decoding: %v", v1)
	}
	// Everything that is not a CustomResourceDefinition is ignored rather than
	// half-decoded into the map.
	if len(got) != 1 {
		t.Fatalf("picked up %v, want only the definition", slices.Sorted(maps.Keys(got)))
	}
}

// A rendered stream carries comments, empty documents and, on some charts,
// fragments that do not parse. None of those is a reason to lose the schemas
// that did parse, the fallback for "no schema" is the plain apiVersion swap,
// and taking that path because one document was odd would be a silent
// downgrade.
func TestARenderedStreamWithJunkStillYieldsItsSchemas(t *testing.T) {
	junk := "---\nthis: [is not, valid: yaml\n" + renderedStream + "\n---\n\n---\n# just a comment\n"
	if got := crdSchemasFromStream(junk); len(got) != 1 {
		t.Fatalf("schemas lost to unrelated junk: %v", slices.Sorted(maps.Keys(got)))
	}
}

// A chart that ships no CustomResourceDefinitions is ordinary, not broken.
func TestAChartWithNoDefinitionsYieldsNothingAndNotAnError(t *testing.T) {
	if got := crdSchemasFromStream("apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\n"); len(got) != 0 {
		t.Fatalf("invented %v", slices.Sorted(maps.Keys(got)))
	}
}
