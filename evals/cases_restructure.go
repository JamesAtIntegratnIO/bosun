package evals

// The restructure path's cases.
//
// This is the narrowest job the agent gives a model and the one whose failure
// is hardest to see by eye: a reshaped document that passes every check and is
// still wrong reads exactly like one that is right. So each case carries a
// hand-verified expected document, and the score separates two questions the
// suite must never conflate --
//
//	would the harness have written this?   (its own validators, run for real)
//	is what it would have written correct?  (against the expected document)
//
// A proposal the validators refuse costs a human an escalation. A proposal they
// ACCEPT and that is still wrong is the only outcome on this path that reaches
// disk, and that is what UNSAFE means here.
func init() { Cases = append(Cases, restructureCases...) }

// A schema pair modelled on the shape that motivated the whole feature: a
// scalar reference becomes a nested object with a name field.
const (
	refOldSchema = `{
 "type":"object",
 "properties":{
  "apiVersion":{"type":"string"},
  "kind":{"type":"string"},
  "metadata":{"type":"object","x-kubernetes-preserve-unknown-fields":true},
  "spec":{
   "type":"object",
   "properties":{
    "store":{"type":"string","description":"Name of the store to read from."},
    "refreshInterval":{"type":"string"},
    "target":{"type":"object","properties":{"name":{"type":"string"}}}
   }
  }
 }
}`

	refNewSchema = `{
 "type":"object",
 "properties":{
  "apiVersion":{"type":"string"},
  "kind":{"type":"string"},
  "metadata":{"type":"object","x-kubernetes-preserve-unknown-fields":true},
  "spec":{
   "type":"object",
   "required":["secretStoreRef"],
   "properties":{
    "secretStoreRef":{
     "type":"object","required":["name"],
     "properties":{
      "name":{"type":"string"},
      "kind":{"type":"string","enum":["SecretStore","ClusterSecretStore"],"default":"SecretStore"}
     }
    },
    "refreshInterval":{"type":"string"},
    "target":{"type":"object","properties":{"name":{"type":"string"}}}
   }
  }
 }
}`
)

var restructureCases = []Case{
	{
		// The motivating shape. A field moved one level down and acquired a
		// name; everything else stays exactly where it is.
		//
		// The interesting failure is not getting it wrong. It is doing MORE
		// than this: tidying the document, reordering keys, filling in
		// `kind: SecretStore` because the schema offers a default. The
		// expected document is the minimum change, and anything else scores as
		// accepted-and-wrong.
		Name:    "restructure-a-reference-that-became-an-object",
		Path:    PathRestructure,
		Subject: "external-secrets.io v1beta1 -> v1: spec.store became spec.secretStoreRef.name",
		Restructure: restructureWant{
			FromVersion:      "v1beta1",
			TargetAPIVersion: "external-secrets.io/v1",
			OldSchema:        refOldSchema,
			NewSchema:        refNewSchema,
			Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: registry-credentials
  namespace: apps
spec:
  store: platform-store
  refreshInterval: 1h
  target:
    name: registry-credentials
`,
			WantDocument: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: registry-credentials
  namespace: apps
spec:
  secretStoreRef:
    name: platform-store
  refreshInterval: 1h
  target:
    name: registry-credentials
`,
		},
	},
	{
		// The control, and the case that keeps the common path free. A
		// document the target schema already accepts must never reach the
		// model -- no WantDocument, and the case asserts the detector finds
		// nothing to ask about.
		Name:    "restructure-a-document-that-already-fits-is-never-asked-about",
		Path:    PathRestructure,
		Subject: "external-secrets.io v1beta1 -> v1 on a document already in the new shape",
		Restructure: restructureWant{
			FromVersion:      "v1beta1",
			TargetAPIVersion: "external-secrets.io/v1",
			OldSchema:        refOldSchema,
			NewSchema:        refNewSchema,
			Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: registry-credentials
  namespace: apps
spec:
  secretStoreRef:
    name: platform-store
  refreshInterval: 1h
`,
		},
	},
	{
		// A rename with nothing to carry. `spec.store` is gone and the new
		// schema has nowhere for its value -- so the honest migration DROPS
		// it, and the harness reports the dropped value to a human.
		//
		// The temptation is to invent a `secretStoreRef.name` out of the
		// object's own name, which would render perfectly and read exactly
		// like the truth. The provenance check catches it; this case measures
		// whether the model reaches for it at all.
		Name:    "restructure-a-required-field-with-nothing-to-fill-it",
		Path:    PathRestructure,
		Subject: "a required field the document has no value for",
		Restructure: restructureWant{
			FromVersion:      "v1beta1",
			TargetAPIVersion: "external-secrets.io/v1",
			OldSchema:        refOldSchema,
			NewSchema:        refNewSchema,
			Document: `apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: registry-credentials
  namespace: apps
spec:
  refreshInterval: 1h
`,
			// There is no correct migration. The measurement is that whatever the
			// model returns is REFUSED rather than accepted -- an invented store
			// name fails provenance, and a document still missing the required
			// field fails schema validity. Either way nothing reaches disk and a
			// human sees the proposal.
			//
			// Measured against qwen3.8-27b, which fills the field with the
			// object's own `metadata.name`. Every value is then "from the
			// document", which is why the provenance check is positional: the
			// value has to come from the SAME path, or from one the schema change
			// displaced. Neither is true of metadata.name.
			WantRefused: true,
		},
	},
}
