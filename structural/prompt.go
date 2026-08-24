package structural

import (
	"fmt"
	"sort"
	"strings"
)

// buildRestructurePrompt assembles the evidence for one document.
//
// Both schemas are pruned before they go in. A real CustomResourceDefinition
// schema is tens of thousands of tokens of prose descriptions, and a prompt
// that spends its budget on documentation has none left for the document it is
// supposed to be migrating.
// Prompt assembles the evidence for one document migration.
//
// It lives here rather than beside its one caller because it is measured as
// well as used: the eval suite scores the restructure path against this exact
// string. A copy in the suite would drift from the copy that ships, and the
// first symptom would be a score describing a prompt nobody is given -- the
// same reason upstream.Render moved into the package that owns Notes.
func Prompt(path, body, fromVersion, toVersion string, old, target Schema, findings []Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "FILE: %s\n\n", path)
	fmt.Fprintf(&b, "DOCUMENT (apiVersion already migrated to the new version)\n\n%s\n\n", body)
	fmt.Fprintf(&b, "WHY IT DOES NOT FIT\n\n%s\n", Summarise(findings))
	fmt.Fprintf(&b, "\nOLD SCHEMA (%s)\n\n%s\n", fromVersion, RenderSchema(old))
	fmt.Fprintf(&b, "\nNEW SCHEMA (%s)\n\n%s\n", toVersion, RenderSchema(target))
	b.WriteString("\nReturn the document, shaped for the new schema.")
	return b.String()
}

// maxSchemaDepth bounds how deep a rendered schema goes. Deep enough for the
// shapes migrations actually move -- a field into a nested object, an object
// into a list of objects -- and shallow enough that a chart with a fully
// specified PodSpec in its CRD does not fill the whole context.
const maxSchemaDepth = 8

// renderSchema prints the shape of a schema and nothing else: field names,
// types, requirements, and the values the schema itself dictates. Descriptions
// are dropped, because they are the bulk of a real CRD schema and the model is
// being asked where a field GOES, not what it means.
func RenderSchema(s Schema) string {
	var b strings.Builder
	renderSchemaInto(&b, "", s, 0)
	if b.Len() == 0 {
		return "(none)"
	}
	return b.String()
}

func renderSchemaInto(b *strings.Builder, indent string, s Schema, depth int) {
	if s == nil || depth > maxSchemaDepth {
		return
	}
	required := map[string]bool{}
	if raw, ok := s["required"].([]any); ok {
		for _, r := range raw {
			if name, ok := r.(string); ok {
				required[name] = true
			}
		}
	}
	props, _ := s["properties"].(map[string]any)
	names := make([]string, 0, len(props))
	for k := range props {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		sub, _ := props[name].(map[string]any)
		typ, _ := sub["type"].(string)
		line := indent + name
		if typ != "" {
			line += ": " + typ
		}
		if required[name] {
			line += " (required)"
		}
		if d, ok := sub["default"]; ok {
			line += fmt.Sprintf(" default=%v", d)
		}
		if e, ok := sub["enum"].([]any); ok {
			parts := make([]string, 0, len(e))
			for _, x := range e {
				parts = append(parts, fmt.Sprint(x))
			}
			line += " one of [" + strings.Join(parts, ", ") + "]"
		}
		if p, ok := sub["x-kubernetes-preserve-unknown-fields"].(bool); ok && p {
			line += " (free-form)"
		}
		fmt.Fprintf(b, "%s\n", line)

		switch typ {
		case "array":
			if items, ok := sub["items"].(map[string]any); ok {
				renderSchemaInto(b, indent+"  - ", Schema(items), depth+1)
			}
		default:
			renderSchemaInto(b, indent+"  ", Schema(sub), depth+1)
		}
	}
}
