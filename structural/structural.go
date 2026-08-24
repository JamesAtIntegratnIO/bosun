// Package structural decides whether a document survives a schema change, and
// judges a proposed rewrite when it does not.
//
// The deterministic migration in `migrate` swaps an apiVersion line and nothing
// else. That is exactly right when the two versions are compatible -- and a
// silent corruption when they are not. A chart that moves `spec.foo` to
// `spec.bar.foo` between v1beta1 and v1 leaves a document that parses, applies,
// and quietly loses a field the API server prunes on the way in. Nothing in the
// repository, the render or the gate can see that: the manifest is valid YAML
// declaring a served version, and the value is simply gone.
//
// Enumerating every upstream's structural changes is not possible. So the model
// is shown BOTH schemas and the document, and asked to translate. What makes
// that safe is not the prompt. It is this package.
//
// # The three checks
//
// A proposal is written only if it passes all three, and they are deliberately
// about the OUTPUT rather than the proposal:
//
//   - IDENTITY. apiVersion is the target, and kind, metadata.name and
//     metadata.namespace are byte-identical to the original. A migration that
//     renames the object is not a migration.
//   - SCHEMA VALIDITY. Every field the target schema rejects is gone, every
//     field it requires is present, and every typed leaf has the type the
//     schema names. This is what the apiserver would have said, said earlier.
//   - VALUE PROVENANCE. Every scalar leaf in the proposal appears as a scalar
//     leaf in the ORIGINAL, unless the target schema itself dictates it -- a
//     default, a single-value enum, a const. Structure comes from the schema;
//     DATA comes only from the document. This is the document-level analogue
//     of "never invent a version", and it is the check that makes the model a
//     translator rather than an author.
//
// And one report rather than a check: values present in the original and absent
// from the proposal are LISTED. Some of those are correct -- a field the target
// schema no longer accepts has to go somewhere, and sometimes nowhere. A human
// reads that list; the harness does not silently accept it.
package structural

import (
	"fmt"
	"sort"
	"strings"
)

// Schema is an OpenAPI v3 schema as it arrives from a CustomResourceDefinition:
// a decoded map, not a typed struct.
//
// Untyped on purpose. These schemas are enormous, they use vendor extensions
// (`x-kubernetes-preserve-unknown-fields`, `x-kubernetes-int-or-string`) that no
// generic OpenAPI struct models, and every field this package does not
// understand has to be handled as "do not judge it" rather than "drop it".
type Schema map[string]any

// Finding is one reason a document does not fit the target schema.
type Finding struct {
	// Path is the dotted field path, e.g. spec.provider.vault.auth.
	Path string
	// Kind is what is wrong.
	Kind FindingKind
	// Detail is a human sentence.
	Detail string
}

type FindingKind string

const (
	// Rejected: the target schema has no such field and does not preserve
	// unknown ones. The apiserver would prune it, silently.
	Rejected FindingKind = "rejected"
	// Missing: the target schema requires the field and the document has not
	// got it. The apiserver would refuse the object.
	Missing FindingKind = "missing"
	// WrongType: the field exists in both and the target names a different
	// type for it.
	WrongType FindingKind = "wrong-type"
)

func (f Finding) String() string { return fmt.Sprintf("%s: %s", f.Path, f.Detail) }

// Check walks a document against a schema and reports everything that does not
// fit.
//
// An EMPTY result is the common case and the valuable one: it means the plain
// apiVersion swap was complete, and no model is called at all. Every guard
// downstream of here exists for the minority of bumps that actually moved a
// field.
//
// Unknown constructs are not findings. A schema this does not understand --
// oneOf, anyOf, a vendor extension, a $ref -- means the field is not judged
// rather than judged harshly, because the cost of a false Rejected is a model
// call and a diff a human has to read, on a document that was fine.
func Check(doc map[string]any, schema Schema) []Finding {
	var out []Finding
	walk("", doc, schema, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func walk(prefix string, node any, schema Schema, out *[]Finding) {
	if schema == nil {
		return
	}
	// A schema that explicitly preserves unknown fields judges nothing below
	// it. This is how CRDs model free-form sections, and treating those as
	// rejections would flag every legitimate document that uses one.
	if b, ok := schema["x-kubernetes-preserve-unknown-fields"].(bool); ok && b {
		return
	}
	// Anything with an alternation is beyond a hand-rolled walker. Not judged,
	// deliberately: see the Check comment.
	for _, k := range []string{"oneOf", "anyOf", "allOf", "not", "$ref"} {
		if _, ok := schema[k]; ok {
			return
		}
	}

	switch typed := node.(type) {
	case map[string]any:
		if t, _ := schema["type"].(string); t != "" && t != "object" {
			*out = append(*out, Finding{prefix, WrongType,
				fmt.Sprintf("the target schema says %s and the document has a mapping", t)})
			return
		}
		props, _ := schema["properties"].(map[string]any)
		additional := schema["additionalProperties"]

		for _, name := range sortedKeys(typed) {
			child := join(prefix, name)
			sub, ok := props[name]
			if !ok {
				// additionalProperties true, or a schema, means the field is
				// allowed. Only an explicit absence is a rejection.
				switch a := additional.(type) {
				case bool:
					if !a {
						*out = append(*out, Finding{child, Rejected,
							"the target schema has no such field and does not allow extra ones"})
					}
				case map[string]any:
					walk(child, typed[name], Schema(a), out)
				case nil:
					if props == nil {
						// No properties and no additionalProperties: an
						// unconstrained object. Not judged.
						continue
					}
					*out = append(*out, Finding{child, Rejected,
						"the target schema has no such field"})
				}
				continue
			}
			subSchema, _ := sub.(map[string]any)
			walk(child, typed[name], Schema(subSchema), out)
		}

		for _, r := range stringList(schema["required"]) {
			if _, ok := typed[r]; !ok {
				*out = append(*out, Finding{join(prefix, r), Missing,
					"the target schema requires this field and the document has not got it"})
			}
		}

	case []any:
		if t, _ := schema["type"].(string); t != "" && t != "array" {
			*out = append(*out, Finding{prefix, WrongType,
				fmt.Sprintf("the target schema says %s and the document has a list", t)})
			return
		}
		items, _ := schema["items"].(map[string]any)
		for i, el := range typed {
			walk(fmt.Sprintf("%s[%d]", prefix, i), el, Schema(items), out)
		}

	default:
		// A scalar. Only an outright type contradiction is worth a finding:
		// integer-vs-number and the int-or-string extension are not errors and
		// flagging them would call the model on documents that are fine.
		t, _ := schema["type"].(string)
		if t == "" || t == "object" && node == nil {
			return
		}
		if !scalarFits(node, t) {
			*out = append(*out, Finding{prefix, WrongType,
				fmt.Sprintf("the target schema says %s", t)})
		}
	}
}

func scalarFits(v any, t string) bool {
	if v == nil {
		// An explicit null satisfies anything; the schema's own nullable
		// handling is beyond what this walker judges.
		return true
	}
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "integer", "number":
		switch v.(type) {
		case int, int32, int64, float32, float64:
			return true
		}
		return false
	case "object", "array":
		// The document has a scalar where the schema wants a structure.
		return false
	default:
		return true
	}
}

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func stringList(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		if s, ok := v.([]string); ok {
			return s
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Summarise renders findings for a prompt or a comment.
func Summarise(fs []Finding) string {
	if len(fs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	return b.String()
}
