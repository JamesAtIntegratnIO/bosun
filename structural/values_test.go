package structural

import (
	"encoding/json"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func schemaOf(t *testing.T, s string) Schema {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("fixture schema is not JSON: %v", err)
	}
	return Schema(out)
}

func valuesOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("fixture values are not YAML: %v", err)
	}
	return out
}

// The bump this exists for: a chart that gained a schema refusing what it used
// to accept, dropped one key and renamed another.
const strictValuesSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "greeting": {"type": "string"},
    "podPort": {"type": "integer"},
    "mode": {"type": "string", "enum": ["service"]}
  }
}`

const setToday = `
greeting: hello
port: 8080
legacy: true
`

func TestTheMigrationThisPathIsFor(t *testing.T) {
	v := ValidateValues(
		valuesOf(t, setToday),
		valuesOf(t, "greeting: hello\npodPort: 8080\n"),
		schemaOf(t, strictValuesSchema))

	if !v.OK() {
		t.Fatalf("a rename and a removal must both be allowed: %v", v.Refusals)
	}
	// The dropped setting is named. Every one of them, every time: the one
	// that should have been renamed and was not looks exactly like the rest.
	if len(v.Lost) != 1 || v.Lost[0] != "true" {
		t.Errorf("Lost = %v, want the dropped value named", v.Lost)
	}
}

// Survival. The check that replaces identity, and the one that also stops a
// displaced value landing on a key that already had one.
func TestASettingTheNewChartStillAcceptsMayNotChange(t *testing.T) {
	for _, tc := range []struct {
		name, proposed, want string
	}{
		{
			name:     "retuned on the way past",
			proposed: "greeting: goodbye\npodPort: 8080\n",
			want:     "greeting",
		},
		{
			name:     "dropped on the way past",
			proposed: "podPort: 8080\n",
			want:     "greeting",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := ValidateValues(valuesOf(t, setToday), valuesOf(t, tc.proposed), schemaOf(t, strictValuesSchema))
			if v.OK() {
				t.Fatal("a setting the chart still accepts must come through untouched")
			}
			if !strings.Contains(strings.Join(v.Refusals, "\n"), tc.want) {
				t.Errorf("the refusal must name it: %v", v.Refusals)
			}
		})
	}
}

// Provenance, positional and unchanged from the manifest path. The document
// fits the schema perfectly and one of its values came from nowhere.
func TestAValueFromNowhereIsRefusedEvenWhenItFits(t *testing.T) {
	v := ValidateValues(
		valuesOf(t, setToday),
		valuesOf(t, "greeting: hello\npodPort: 9090\n"),
		schemaOf(t, strictValuesSchema))

	if v.OK() {
		t.Fatal("9090 is in neither the values nor the schema")
	}
	if !strings.Contains(strings.Join(v.Refusals, "\n"), "9090") {
		t.Errorf("the refusal must name the value: %v", v.Refusals)
	}
}

// The schema's own vocabulary is evidence, computed and fetched by the
// harness. Without it the check refuses correct migrations: a key whose only
// legal value the schema names has to come from somewhere, and the only honest
// somewhere is the schema.
func TestAValueTheSchemaDictatesIsAllowed(t *testing.T) {
	v := ValidateValues(
		valuesOf(t, setToday),
		valuesOf(t, "greeting: hello\npodPort: 8080\nmode: service\n"),
		schemaOf(t, strictValuesSchema))

	if !v.OK() {
		t.Fatalf("a single-member enum is an answer the chart gave: %v", v.Refusals)
	}
}

// Where repair ends. A required key the schema does not name a value for has
// an answer only a person holds, and this is answered before a model is asked
// anything, so the escalation names the field instead of reporting a refused
// proposal that was never going to pass.
func TestARequiredKeyWithNoDerivableValueNeedsAnAuthor(t *testing.T) {
	for _, tc := range []struct {
		name, schema string
		want         []string
	}{
		{
			name: "nothing says what it should be",
			schema: `{"type":"object","required":["namespace"],
			          "properties":{"namespace":{"type":"string"}}}`,
			want: []string{"namespace"},
		},
		{
			name: "a default is the chart's own answer",
			schema: `{"type":"object","required":["namespace"],
			          "properties":{"namespace":{"type":"string","default":"monitoring"}}}`,
		},
		{
			name: "a const is the chart's own answer",
			schema: `{"type":"object","required":["namespace"],
			          "properties":{"namespace":{"const":"monitoring"}}}`,
		},
		{
			name: "one legal value is an answer",
			schema: `{"type":"object","required":["namespace"],
			          "properties":{"namespace":{"enum":["monitoring"]}}}`,
		},
		{
			// Three legal values say which values are legal, not which one
			// belongs here. Treating that as an answer is the invention this
			// check exists to refuse.
			name: "three legal values are not an answer",
			schema: `{"type":"object","required":["namespace"],
			          "properties":{"namespace":{"enum":["a","b","c"]}}}`,
			want: []string{"namespace"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NeedsAnAuthor(valuesOf(t, "greeting: hello\n"), schemaOf(t, tc.schema))
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("NeedsAnAuthor = %v, want %v", got, tc.want)
			}
		})
	}
}

// A key the document already supplies is not missing, however required it is.
func TestARequiredKeyTheValuesAlreadySetNeedsNobody(t *testing.T) {
	got := NeedsAnAuthor(
		valuesOf(t, "namespace: monitoring\n"),
		schemaOf(t, `{"type":"object","required":["namespace"],
		              "properties":{"namespace":{"type":"string"}}}`))
	if len(got) != 0 {
		t.Errorf("NeedsAnAuthor = %v, want nothing", got)
	}
}

// A chart's values may legitimately have a `metadata` key, and the API
// machinery's names are only carved out of the walk where a schema is silent
// about them. Skipping one a schema does declare would leave a rejected
// setting unreported, which on this path means nothing offers to migrate it.
func TestAValuesKeyNamedForTheAPIMachineryIsStillJudged(t *testing.T) {
	got := Check(
		valuesOf(t, "metadata:\n  labels:\n    a: b\n  gone: yes\n"),
		schemaOf(t, `{"type":"object","additionalProperties":false,
		              "properties":{"metadata":{"type":"object","additionalProperties":false,
		                "properties":{"labels":{"type":"object"}}}}}`))

	if len(got) != 1 || got[0].Path != "metadata.gone" || got[0].Kind != Rejected {
		t.Fatalf("want metadata.gone rejected, got %v", got)
	}

	// And the carve-out still holds where it was measured: a schema that
	// declares spec and status and nothing else must not reject a manifest's
	// own identity.
	quiet := Check(
		valuesOf(t, "apiVersion: g/v1\nkind: Widget\nmetadata:\n  name: w\nspec:\n  size: 1\n"),
		schemaOf(t, `{"type":"object","additionalProperties":false,
		              "properties":{"spec":{"type":"object","properties":{"size":{"type":"integer"}}}}}`))
	if len(quiet) != 0 {
		t.Errorf("the API machinery's own fields must not be judged here: %v", quiet)
	}
}

// The evidence that turned a live model's answer around. Measured: with the
// whole new schema in the prompt and `podPort: integer` sitting directly under
// the section `port` was refused from, qwen3.8-27b dropped the value. Told
// that `port` had one free slot beside it and the other three keys had none,
// it moved the first and dropped the rest.
func TestVacanciesTellARenameFromARemoval(t *testing.T) {
	got := Vacancies(
		valuesOf(t, `
gate:
  mode: service
  wait: true
  argocd:
    baseURL: https://argocd.example
    port: 8080
`),
		schemaOf(t, `{"type":"object","additionalProperties":false,
		  "properties":{"gate":{"type":"object","additionalProperties":false,
		    "properties":{"argocd":{"type":"object","additionalProperties":false,
		      "properties":{"baseURL":{"type":"string"},"podPort":{"type":"integer"}}}}}}}`))

	want := []string{
		"gate.argocd.port -> podPort (integer)",
		// Nothing free beside them is the other half of the signal: these
		// really were dropped, and a model told so stops looking for a home.
		"gate.mode -> nothing free beside it",
		"gate.wait -> nothing free beside it",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Vacancies:\n got %v\nwant %v", got, want)
	}
}

// A section holding two of its own keys is not somewhere free to put a third.
// Asked the other way round -- "does the document set `gate.argocd`?" -- the
// answer is always no, because a section is never a leaf, and every occupied
// section in the chart would be offered as a destination.
func TestAnOccupiedSectionIsNotAVacancy(t *testing.T) {
	got := Vacancies(
		valuesOf(t, "legacy: true\nkept:\n  a: 1\n"),
		schemaOf(t, `{"type":"object","additionalProperties":false,
		  "properties":{"kept":{"type":"object","properties":{"a":{"type":"integer"}}}}}`))

	if len(got) != 1 || got[0] != "legacy -> nothing free beside it" {
		t.Errorf("Vacancies = %v, want the occupied section left out", got)
	}
}

// A slot that cannot hold the value is not where the value went. A big chart
// declares hundreds of keys, and answering a refused one with all of them is
// the same as answering with none.
func TestVacanciesLeaveOutSlotsThatCouldNotHoldTheValue(t *testing.T) {
	got := Vacancies(
		valuesOf(t, "port: 8080\n"),
		schemaOf(t, `{"type":"object","additionalProperties":false,
		  "properties":{
		    "podPort":{"type":"integer"},
		    "name":{"type":"string"},
		    "enabled":{"type":"boolean"},
		    "service":{"type":"object","properties":{"port":{"type":"integer"}}}
		  }}`))

	// The integer slot, and the empty section a rename could move it inside.
	// Not the string and not the boolean.
	want := "port -> podPort (integer), service (object)"
	if len(got) != 1 || got[0] != want {
		t.Errorf("Vacancies = %v, want [%q]", got, want)
	}
}
