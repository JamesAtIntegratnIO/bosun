package structural

import (
	"fmt"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// Prompt assembles the evidence for one document migration.
//
// Both schemas are pruned before they go in. A real CustomResourceDefinition
// schema is tens of thousands of tokens of prose descriptions, and a prompt
// that spends its budget on documentation has none left for the document it is
// supposed to be migrating.
//
// It lives here rather than beside its one caller because it is measured as
// well as used: the eval suite scores the restructure path against this exact
// string. A copy in the suite would drift from the copy that ships, and the
// first symptom would be a score describing a prompt nobody is given, the
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

// ValuesPrompt assembles the evidence for one values migration.
//
// The same shape as Prompt and a different set of nouns, because the two are
// scored separately and a shared function with a mode flag would be one
// function whose output nobody can read without knowing which mode it was in.
//
// The old schema is optional and often absent: most charts ship no
// values.schema.json at all, and the one being migrated away from is exactly
// the version that was permissive enough for these values to work. When it is
// there it says what the keys used to mean, which is what makes a rename
// recognisable rather than guessable.
func ValuesPrompt(chart, fromVersion, toVersion string, values map[string]any,
	old, target Schema, findings []Finding) string {

	body, err := yaml.Marshal(values)
	if err != nil {
		// Cannot happen for a document that was decoded from YAML, and a
		// prompt is not the place to invent an error return: an empty values
		// block produces a proposal every validator refuses, which is the
		// same outcome an error would have reached by a longer route.
		body = nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CHART: %s, %s -> %s\n\n", chart, fromVersion, toVersion)
	fmt.Fprintf(&b, "VALUES THIS REPOSITORY SETS\n\n%s\n", body)
	fmt.Fprintf(&b, "\nWHY THE CHART REFUSES THEM\n\n%s\n", Summarise(findings))
	if free := Vacancies(values, target); len(free) > 0 {
		fmt.Fprintf(&b, "\nWHAT THE NEW SCHEMA DECLARES BESIDE EACH REFUSED KEY, AND THESE VALUES DO NOT SET\n\n")
		for _, line := range free {
			fmt.Fprintf(&b, "- %s\n", line)
		}
	}
	if old != nil {
		fmt.Fprintf(&b, "\nOLD VALUES SCHEMA (%s)\n\n%s\n", fromVersion, RenderSchema(old))
	} else {
		fmt.Fprintf(&b, "\nOLD VALUES SCHEMA (%s)\n\n(the old version shipped none, so what these keys "+
			"used to mean is not available here)\n", fromVersion)
	}
	fmt.Fprintf(&b, "\nNEW VALUES SCHEMA (%s)\n\n%s\n", toVersion, RenderSchema(target))
	b.WriteString("\nReturn the values, shaped for the new schema.")
	return b.String()
}

// maxSchemaDepth bounds how deep a rendered schema goes. Deep enough for the
// shapes migrations move, a field into a nested object, an object
// into a list of objects, and shallow enough that a chart with a fully
// specified PodSpec in its CRD does not fill the whole context.
const maxSchemaDepth = 8

// MaxSchemaChars bounds one rendered schema in a prompt.
//
// Measured rather than guessed: the largest schema on a real cluster renders to
// 43,831 characters, kyverno's ClusterPolicy v2beta1, and a prompt carries two
// of them plus the document plus the gate report. Left uncapped that is the
// same failure the release-note cap exists to prevent, crowding out the
// evidence the answer is supposed to be built from.
//
// Truncating is safe here in a way it would not be elsewhere, and the reason is
// worth stating: the validators run against the full schema whatever the prompt
// showed. A model that never saw the destination field cannot produce a
// proposal that passes schema-validity, so a truncated schema costs a refusal
// and an escalation, never a bad write.
const MaxSchemaChars = 12000

// RenderSchema prints the shape of a schema and nothing else: field names,
// types, requirements, and the values the schema itself dictates. Descriptions
// are dropped, because they are the bulk of a real CRD schema and the model is
// being asked where a field goes, not what it means.
func RenderSchema(s Schema) string {
	var b strings.Builder
	renderSchemaInto(&b, "", s, 0)
	if b.Len() == 0 {
		return "(none)"
	}
	out := b.String()
	if len(out) > MaxSchemaChars {
		// Cut at a line boundary, so the last thing the model sees is a whole
		// field rather than half a name it might complete from memory.
		cut := strings.LastIndexByte(out[:MaxSchemaChars], '\n')
		if cut <= 0 {
			cut = MaxSchemaChars - 1
		}
		// cut+1 keeps the newline, so the text ends on a whole field rather
		// than the character before one.
		out = out[:cut+1] + "\n[schema truncated: it is larger than this prompt can carry. Fields below\n" +
			"this point are not shown. If the field you need is not here, say so in the\n" +
			"notes and return the document unchanged rather than guessing where it went.]\n"
	}
	return out
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
