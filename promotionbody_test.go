package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/agent"
	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// Rule 1a's third contract: charts/kargo-pipelines POSTs a body that
// Server.PromotionOpened decodes into agent.Promotion.
//
// CONTRIBUTING.md records this one as "Untested". It is worse than that: it is
// also RENDERED by nothing. charts/kargo-pipelines/values.yaml defaults
// triage.enabled to false and ci/lint-values.yaml never sets it, so the whole
// http step is elided from every render this repository performs -- helm lint,
// the portability test, and the chart job in CI alike. The only place it is
// switched on is local/values/kargo-pipelines.yaml, which is the demo.
//
// The doc comment above kp.triageBody records that this body was malformed from
// the day it was written until 2026-08-23 -- "the triage hook has never once
// reached the service" -- and that nobody noticed because the Kargo step is
// continueOnError. Nothing about that failure mode has changed. The step still
// swallows its own errors, so this contract has no runtime symptom and never
// will: a renamed key is a field the agent reads as empty on every promotion,
// forever, with nothing logged at either end.
//
// The two charts also ship on separate clocks. Somebody's Kargo runs
// kargo-pipelines 0.9 against bosun 0.28, so this is a published contract and
// not merely an internal one.

// The keys the chart sends are exactly the fields the handler decodes.
func TestThePromotionBodyIsTheKeysTheHandlerDecodes(t *testing.T) {
	got := bodyKeys(t, triageStepBody(t))
	want := jsonTags(reflect.TypeOf(agent.Promotion{}))

	if !slices.Equal(got, want) {
		t.Fatalf("the promotion body and agent.Promotion disagree.\n"+
			"  chart sends:   %v\n  handler wants: %v\n\n"+
			"Fix kp.triageBody in charts/kargo-pipelines/templates/_helpers.tpl or the json "+
			"tags on agent.Promotion in agent/triage.go. Note that neither side will tell "+
			"you: the http step is continueOnError, so a renamed key is a field the agent "+
			"reads as empty on every promotion, with nothing logged at either end.",
			got, want)
	}
}

// The body is a YAML string, and that is the defect that survived from
// inception to 2026-08-23.
//
// Kargo's http step schema requires `body` to be a string, and it evaluates
// every ${{ }} expression BEFORE the schema sees it. A body built by
// interpolating expressions into JSON is therefore valid JSON by construction,
// so Kargo unmarshals it into a map and the step dies at config validation with
// "body: Invalid type. Expected: string, given: object". Wrapping the whole
// thing in quote() is what keeps it a string.
//
// That is a property of the RENDERED DOCUMENT rather than of the expression
// inside it, which is the only reason it is checkable here at all.
func TestThePromotionBodyIsAStringAndNotAMapping(t *testing.T) {
	for _, d := range stagesWithTriage(t) {
		body := stepBody(t, d)
		if body == nil {
			t.Fatalf("the triage step in %s has no body at all", d.Name)
		}
		if _, ok := body.(string); !ok {
			t.Fatalf("the triage step's body in %s parsed as %T, and Kargo's http step "+
				"requires a string.\n"+
				"Kargo evaluates every ${{ }} before validating, so a body interpolated "+
				"into bare JSON becomes an object and the step dies at config validation "+
				"with \"body: Invalid type. Expected: string, given: object\". Keep the "+
				"quote(...) wrapper in kp.triageBody.", d.Name, body)
		}
	}
}

// The lists are lists, and the number is not a string.
//
// files and verifyApps are rendered by toJson as literal JSON arrays, so their
// shape is assertable here. prNumber's runtime type is Kargo's to decide and
// this test cannot reach it -- what it can assert is that nothing has wrapped
// outputs.pr.pr.id in quote() or string(), which is how a number becomes a
// string and PromotionOpened starts rejecting every call as "prNumber is
// required".
func TestTheListsAreListsAndTheNumberIsNotQuoted(t *testing.T) {
	body := triageStepBody(t)

	for _, key := range []string{"files", "verifyApps"} {
		v := valueOf(t, body, key)
		if !strings.HasPrefix(v, "[") {
			t.Errorf("%q renders as %s, and agent.Promotion decodes it into a []string.\n"+
				"toJson on a list is what makes it a JSON array literal here.", key, v)
		}
	}

	if v := valueOf(t, body, "prNumber"); strings.Contains(v, "quote(") || strings.Contains(v, "string(") {
		t.Errorf("prNumber renders as %s, which makes it a string.\n"+
			"agent.Promotion decodes prNumber into an int, so a quoted one fails to "+
			"unmarshal and PromotionOpened rejects every promotion with \"prNumber is "+
			"required\" -- a message about a field the chart is in fact sending.", v)
	}
}

// A body carrying the chart's keys reaches the handler with nothing left over.
//
// The keys come from the rendered chart; the values are stand-ins, because the
// real ones are Kargo expressions this test cannot evaluate. DisallowUnknownFields
// is what turns "the chart sends a key the handler does not decode" into a
// failure: json.Decode drops an unknown field in silence, which is the whole
// reason this contract can rot unobserved.
func TestASynthesisedPromotionBodyDecodesWithNothingLeftOver(t *testing.T) {
	keys := bodyKeys(t, triageStepBody(t))

	payload := map[string]any{}
	for _, k := range keys {
		switch k {
		case "prNumber":
			payload[k] = 42
		case "files", "verifyApps":
			payload[k] = []string{"addons/thing.yaml"}
		case "autoMerge":
			payload[k] = "patch"
		default:
			payload[k] = "x"
		}
	}
	blob, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	var p agent.Promotion
	dec := json.NewDecoder(bytes.NewReader(blob))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		t.Fatalf("a body carrying the chart's own keys does not decode into agent.Promotion: %v\n"+
			"body: %s\n\n"+
			"A key the handler does not declare is dropped silently by json.Decode, so the "+
			"only symptom in production is a field that is always empty.", err, blob)
	}

	// And it reaches the handler, which is the half a struct test cannot see:
	// PromotionOpened validates the payload before it accepts it, and a field
	// that decoded into the wrong place is one this catches and json.Decode
	// does not.
	got := make(chan agent.Promotion, 1)
	srv := &Server{Log: testLogger(t), Timeout: time.Minute}
	srv.runFn = func(p agent.Promotion) error { got <- p; return nil }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/promotion-opened", bytes.NewReader(blob))
	srv.PromotionOpened(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the handler answered %d for a body carrying the chart's own keys: %s\n"+
			"PromotionOpened validates before accepting, so this is the chart sending "+
			"something the handler will not take.", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	srv.Wait()

	select {
	case p := <-got:
		// The three the handler requires, and the two whose types the chart
		// chooses. A key that decoded into the wrong field arrives here as a
		// zero value that reads exactly like a deliberate absence.
		if p.PRNumber != 42 || p.Stage != "x" || len(p.Files) != 1 || p.AutoMerge != "patch" {
			t.Fatalf("the payload decoded into the wrong fields: %+v", p)
		}
	default:
		t.Fatal("the handler accepted the body and never ran the triage")
	}
}

// The step is elided when triage is off, which is the reason none of the above
// is covered by any other render in this repository.
func TestTheTriageStepIsElidedWhenTriageIsOff(t *testing.T) {
	docs := helmtest.Render(t, "kargo-pipelines", helmtest.Values("ci/lint-values.yaml"))
	for _, d := range docs {
		if d.Kind == "Stage" && strings.Contains(d.Raw, "as: triage") {
			t.Fatalf("%s renders a triage step with triage.enabled at its default", d.Name)
		}
	}
	// And the inverse, so this test cannot pass by the chart having stopped
	// rendering Stages at all.
	if len(stagesWithTriage(t)) == 0 {
		t.Fatal("no Stage renders a triage step even with triage.enabled=true")
	}
}

func stagesWithTriage(t *testing.T) []helmtest.Doc {
	t.Helper()
	docs := helmtest.Render(t, "kargo-pipelines",
		helmtest.Values("ci/lint-values.yaml"),
		helmtest.Set("triage.enabled=true", "triage.when=always",
			"triage.url=http://bosun.bosun.svc:8080/v1/promotion-opened"))

	var out []helmtest.Doc
	for _, d := range docs {
		if d.Kind == "Stage" && strings.Contains(d.Raw, "as: triage") {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		t.Fatal("no Stage rendered a triage step with triage.enabled=true; " +
			"charts/kargo-pipelines/templates/stage.yaml no longer emits one")
	}
	return out
}

// stepBody digs the triage step's body out of one Stage's promotion template.
func stepBody(t *testing.T, d helmtest.Doc) any {
	t.Helper()
	spec, _ := d.Body["spec"].(map[string]any)
	tmpl, _ := spec["promotionTemplate"].(map[string]any)
	inner, _ := tmpl["spec"].(map[string]any)
	steps, _ := inner["steps"].([]any)
	for _, s := range steps {
		step, _ := s.(map[string]any)
		if as, _ := step["as"].(string); as != "triage" {
			continue
		}
		cfg, _ := step["config"].(map[string]any)
		return cfg["body"]
	}
	t.Fatalf("no step named triage in %s", d.Name)
	return nil
}

func triageStepBody(t *testing.T) string {
	t.Helper()
	body, ok := stepBody(t, stagesWithTriage(t)[0]).(string)
	if !ok {
		t.Fatal("the triage step's body is not a string; see TestThePromotionBodyIsAStringAndNotAMapping")
	}
	return body
}

// bodyKeys returns the top-level keys of the object kp.triageBody builds.
//
// Depth-aware rather than a regexp over the whole string, so that a file path
// inside the files array -- or any value a target's own values file chose --
// cannot forge a key. That is the same reasoning gate/repaircontract_test.go
// applies to a rendered name: a value must never be able to spell structure.
func bodyKeys(t *testing.T, body string) []string {
	t.Helper()
	obj := objectExpr(t, body)

	var out []string
	depth := 0
	for i := 0; i < len(obj); i++ {
		switch obj[i] {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				if len(out) == 0 {
					t.Fatalf("found no keys in the promotion body: %s", body)
				}
				return out
			}
		case '"':
			if depth == 1 {
				if m := keyAt.FindStringSubmatch(obj[i:]); m != nil {
					out = append(out, m[1])
					// Past the key AND its colon, so the closing quote of the
					// key is never re-read as the opening quote of a value.
					i += len(m[0]) - 1
					continue
				}
			}
			// Any other quote opens a string value, whose contents must not be
			// scanned for keys: a file path a target's values file chose would
			// otherwise be able to spell one. Skip to its close.
			if j := strings.IndexByte(obj[i+1:], '"'); j >= 0 {
				i += j + 1
			}
		}
	}
	t.Fatalf("the promotion body's braces never close: %s", body)
	return nil
}

// objectExpr is the `{...}` kp.triageBody wraps in quote(), located by that
// wrapper rather than by the first brace in the string -- the first brace
// belongs to Kargo's own `${{` and counting from it puts every key two levels
// deeper than it is.
func objectExpr(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, "quote(")
	if i < 0 {
		t.Fatalf("the promotion body is not wrapped in quote(), so Kargo will read it as "+
			"an object rather than a string: %s", body)
	}
	rest := body[i+len("quote("):]
	j := strings.IndexByte(rest, '{')
	if j < 0 {
		t.Fatalf("quote() wraps no object expression: %s", body)
	}
	return rest[j:]
}

// keyAt matches a quoted key and its colon at the point a scan reaches one.
var keyAt = regexp.MustCompile(`^"([A-Za-z][A-Za-z0-9_]*)"\s*:`)

// valueOf returns the text of one top-level key's value, up to the next
// top-level comma.
func valueOf(t *testing.T, body, key string) string {
	t.Helper()
	obj := objectExpr(t, body)
	marker := `"` + key + `":`
	i := strings.Index(obj, marker)
	if i < 0 {
		t.Fatalf("the promotion body has no %q key: %s", key, body)
	}
	rest := obj[i+len(marker):]
	depth := 0
	for j := 0; j < len(rest); j++ {
		switch rest[j] {
		case '{', '[':
			depth++
		case '}', ']':
			if depth == 0 {
				return strings.TrimSpace(rest[:j])
			}
			depth--
		case ',':
			if depth == 0 {
				return strings.TrimSpace(rest[:j])
			}
		}
	}
	return strings.TrimSpace(rest)
}

// jsonTags is the json field names of a struct, in declaration order.
func jsonTags(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}
