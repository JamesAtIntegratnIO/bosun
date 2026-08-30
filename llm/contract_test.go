package llm

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/prompt"
)

// One response shape, stated four times.
//
// The Verdict struct's json tags, the JSON Schema handed to the provider, the
// field list at the end of each prompt, and docs/prompt-contract.md. Only the
// first two are enforced by anything, and the hand-written list in
// provider_test.go that was meant to hold even those together already omits
// escalationReason -- a hand-written list failing to cover a hand-written list,
// which is the argument for deriving both sides rather than adding a third.
//
// This package is the only one that can see all of it at once: llm declares the
// structs and the schemas, and prompt has no imports at all, so nothing here
// creates a cycle.
//
// What a break looks like: rename Verdict.Summary's tag to `headline` and the
// struct still compiles, VerdictSchema still says `summary`, the prompt still
// says `summary`, every verdict decodes with an empty Summary, Validate returns
// "verdict has no summary", and every triage escalates.

func TestTheVerdictSchemaMatchesTheVerdictStruct(t *testing.T) {
	assertSchemaMatchesStruct(t, "VerdictSchema", VerdictSchema(), reflect.TypeOf(Verdict{}))

	edits, _ := VerdictSchema()["properties"].(map[string]any)["edits"].(map[string]any)
	item, _ := edits["items"].(map[string]any)
	if item == nil {
		t.Fatal("VerdictSchema's edits property constrains no item shape, so the model may " +
			"return any object at all inside it")
	}
	assertSchemaMatchesStruct(t, "the edit sub-schema", item, reflect.TypeOf(Edit{}))
}

func TestTheMigrationSchemaMatchesTheMigrationStruct(t *testing.T) {
	assertSchemaMatchesStruct(t, "MigrationSchema", MigrationSchema(), reflect.TypeOf(Migration{}))
}

// assertSchemaMatchesStruct checks both directions, and that every property is
// required.
//
// "Every property required" is not pedantry: VerdictSchema's own comment
// records that the first live test returned classification "escalate" with an
// empty escalationReason, because the field was optional and a model will omit
// an optional field. Strict constrained decoding expects the full list.
func assertSchemaMatchesStruct(t *testing.T, name string, schema map[string]any, typ reflect.Type) {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no properties", name)
	}
	tags := jsonTagsOf(typ)

	for _, tag := range sortedOf(tags) {
		if _, ok := props[tag]; !ok {
			t.Errorf("%s has a %q field and %s does not constrain it.\n"+
				"Constrained decoding will not produce a field the schema omits, so the agent "+
				"reads it as empty on every run and the model looks like it declined to answer.",
				typ.Name(), tag, name)
		}
	}
	for _, p := range sortedOf(props) {
		if !tags[p] {
			t.Errorf("%s constrains %q and %s has no field for it: the model is being made "+
				"to produce something nothing reads.", name, p, typ.Name())
		}
	}

	required := map[string]bool{}
	for _, r := range stringsOf(schema["required"]) {
		required[r] = true
	}
	for _, p := range sortedOf(props) {
		if !required[p] {
			t.Errorf("%s constrains %q and does not require it.\n"+
				"A model omits an optional field: VerdictSchema's first live test returned "+
				"classification \"escalate\" with an empty escalationReason for exactly that "+
				"reason. Every property here must be in `required`.", name, p)
		}
	}

	if schema["additionalProperties"] != false {
		t.Errorf("%s does not set additionalProperties: false, so a model may return fields "+
			"nothing checks", name)
	}
}

// promptShapes pairs each shipped prompt with the schema sent alongside it.
var promptShapes = []struct {
	Name   string
	Text   string
	Schema func() map[string]any
	// Extra are field names from a nested shape the prompt also names, today
	// the edits array's items.
	Extra func() map[string]any
}{
	{"prompt.System", prompt.System, VerdictSchema, editItemSchema},
	{"prompt.Explain", prompt.Explain, VerdictSchema, editItemSchema},
	{"prompt.Restructure", prompt.Restructure, MigrationSchema, nil},
	{"prompt.ValuesMigration", prompt.ValuesMigration, MigrationSchema, nil},
}

// Every field a prompt names is one the schema it is sent with constrains.
//
// This is the rename catch on the prose side: a prompt telling a model to fill
// in a field that no longer exists spends its instruction budget on nothing,
// and there is no other signal that it is doing so.
func TestEveryFieldThePromptNamesIsOneTheSchemaConstrains(t *testing.T) {
	for _, s := range promptShapes {
		t.Run(s.Name, func(t *testing.T) {
			allowed := propertyNames(s.Schema())
			if s.Extra != nil {
				for k := range propertyNames(s.Extra()) {
					allowed[k] = true
				}
			}

			named := fieldsNamedIn(t, s.Text)
			for _, f := range sortedOf(named) {
				if !allowed[f] {
					t.Errorf("%s tells the model to fill in %q and the schema it is sent with "+
						"does not constrain it.\n"+
						"Either the field was renamed on one side only, or the prompt is spending "+
						"its instructions on a field that will never come back.", s.Name, f)
				}
			}
		})
	}
}

// And the other direction: every field the schema constrains is named in the
// prompt sent with it.
//
// A field the model is never told the job of is one it fills badly, and
// escalationReason is the standing example -- the schema requires it, and what
// makes it a short status label rather than a second summary is one line of
// prose in the prompt.
func TestEverySchemaFieldIsNamedInThePrompt(t *testing.T) {
	for _, s := range promptShapes {
		t.Run(s.Name, func(t *testing.T) {
			props, _ := s.Schema()["properties"].(map[string]any)
			for _, p := range sortedOf(propertyNames(s.Schema())) {
				if taught(s.Text, p, props[p]) {
					continue
				}
				t.Errorf("the schema sent with %s constrains %q and the prompt teaches it "+
					"neither by name nor by its values.\n"+
					"The model is required to return the field and is told nothing about what "+
					"it is for, so it will fill it with whatever the neighbouring field says.",
					s.Name, p)
			}
		})
	}
}

// docs/prompt-contract.md is the fourth statement of the shape, and the one a
// person reads before changing a prompt.
//
// Every field the prompts name must appear there, or the document describes an
// interface the code no longer has. Directional on purpose: the document is
// also allowed to discuss things that are not fields.
func TestThePromptContractDocumentsEveryFieldThePromptNames(t *testing.T) {
	doc := readDoc(t, "prompt-contract.md")

	for _, s := range promptShapes {
		for _, f := range sortedOf(fieldsNamedIn(t, s.Text)) {
			if !mentions(doc, f) {
				t.Errorf("%s names the field %q and docs/prompt-contract.md never mentions it.\n"+
					"That document is what a person reads before changing a prompt; a field "+
					"missing from it is one they will not know is part of the contract.", s.Name, f)
			}
		}
	}
}

// fieldsNamedIn extracts the response fields a prompt names.
//
// Two shapes, both unambiguous, so this needs no exception list:
//
//   - a JSON key in a worked example: a quoted identifier followed by a colon.
//     A quoted VALUE -- "escalate", "no_action" -- is never followed by one.
//   - a line in the response-shape block: two spaces, the name, then the column
//     of description text. Only that block is scanned, because the same
//     two-column layout is used for worked examples elsewhere in the prompt --
//     a `right`/`wrong` pair of illustrations is not a pair of fields.
func fieldsNamedIn(t *testing.T, text string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range jsonKey.FindAllStringSubmatch(text, -1) {
		out[m[1]] = true
	}
	for _, m := range answerLine.FindAllStringSubmatch(responseBlock(t, text), -1) {
		out[m[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("found no field names in a prompt; the prompts no longer state their " +
			"response shape in a way fieldsNamedIn recognises, and every check over it " +
			"is now vacuous")
	}
	return out
}

var (
	jsonKey    = regexp.MustCompile(`"([a-z][A-Za-z0-9_]*)"\s*:`)
	answerLine = regexp.MustCompile(`(?m)^ {2}([a-z][A-Za-z0-9_]*) {2,}\S`)
)

// responseBlock is the tail of a prompt that states its response shape.
//
// Every shipped prompt introduces it the same two ways, and one of them has to
// be present: a prompt with no response-shape section is a prompt that has
// stopped telling the model what to fill in.
func responseBlock(t *testing.T, text string) string {
	t.Helper()
	best := -1
	for _, marker := range []string{"## Answer", "give each field its own job"} {
		if i := strings.LastIndex(text, marker); i > best {
			best = i
		}
	}
	if best < 0 {
		t.Fatal("a prompt states no response shape: it has neither an \"## Answer\" " +
			"section nor the \"give each field its own job\" line, so fieldsNamedIn " +
			"cannot find the fields and every check over them is vacuous")
	}
	return text[best:]
}

// taught reports whether a prompt tells the model what a field is for.
//
// Naming it is the usual way. An enum has a second, equally good one: naming
// every value it can take. prompt.System never writes the word
// "classification" and spends three sections on what "mechanical", "escalate"
// and "no_action" each mean -- the model is told exactly what the field is,
// and constrained decoding puts the answer in the right place. Requiring the
// name there would be requiring a worse prompt.
func taught(text, field string, schema any) bool {
	if mentions(text, field) {
		return true
	}
	node, _ := schema.(map[string]any)
	values := stringsOf(node["enum"])
	if len(values) == 0 {
		return false
	}
	for _, v := range values {
		if !strings.Contains(text, v) {
			return false
		}
	}
	return true
}

// mentions reports whether prose names a field, as a whole word.
func mentions(text, field string) bool {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(field) + `\b`)
	return re.MatchString(text)
}

func editItemSchema() map[string]any {
	edits, _ := VerdictSchema()["properties"].(map[string]any)["edits"].(map[string]any)
	item, _ := edits["items"].(map[string]any)
	return item
}

func propertyNames(schema map[string]any) map[string]bool {
	out := map[string]bool{}
	props, _ := schema["properties"].(map[string]any)
	for k := range props {
		out[k] = true
	}
	return out
}

func jsonTagsOf(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

func stringsOf(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		var out []string
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func sortedOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func readDoc(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "docs", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
