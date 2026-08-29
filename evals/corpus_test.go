package evals

import (
	"context"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/JamesAtIntegratnIO/bosun/llm"
)

// The corpus is a few hundred lines of fixtures whose only consumer was
// TestEval, which skips without a live endpoint, so CI's claim that "the eval
// suite runs here too, with a fake provider, no model endpoint" was not true
// of the cases themselves. Nothing checked that a case was even runnable
// until somebody stood up a model.
//
// These run every case through the real harness against a fake, so a fixture
// that cannot be scored fails here rather than at the moment somebody pays for
// inference to find out.

// Every case must name a path the dispatcher handles, a class the classifier
// can return, and enough of an expectation to score.
func TestEveryCaseIsWellFormed(t *testing.T) {
	classes := map[string]bool{
		llm.ClassMechanical: true, llm.ClassEscalate: true, llm.ClassNoAction: true,
	}
	seen := map[string]bool{}

	for _, c := range Cases {
		if c.Name == "" {
			t.Fatalf("a case has no name: %+v", c.Subject)
		}
		if seen[c.Name] {
			t.Errorf("%s: duplicate case name -- results are reported by name", c.Name)
		}
		seen[c.Name] = true

		switch c.Path {
		case PathTriage, PathExplain:
			if !classes[c.WantClass] {
				t.Errorf("%s: WantClass %q is not a class the model can return", c.Name, c.WantClass)
			}
		case PathRestructure:
			if c.Restructure.OldSchema == "" || c.Restructure.NewSchema == "" {
				t.Errorf("%s: a restructure case needs both schemas", c.Name)
			}
			if c.Restructure.Document == "" {
				t.Errorf("%s: a restructure case needs a document to migrate", c.Name)
			}
		default:
			t.Errorf("%s: path %q is not one the harness dispatches", c.Name, c.Path)
		}

		// A mechanical case that names edits must name the file they land in,
		// or the applier has nowhere to put them and the case scores zero for
		// a reason that is about the fixture, not the model.
		if len(c.Triage.WantEdits) > 0 && c.Triage.EditFile == "" {
			t.Errorf("%s: WantEdits with no EditFile", c.Name)
		}
		if c.Triage.EditFile != "" && c.Files[c.Triage.EditFile] == "" {
			t.Errorf("%s: EditFile %q is not in the fixture", c.Name, c.Triage.EditFile)
		}
		for _, p := range c.ChangedFiles() {
			if _, ok := c.Files[p]; !ok {
				t.Errorf("%s: Changed names %q, which the fixture does not contain", c.Name, p)
			}
		}
	}
}

// Every case must produce a prompt: the shipped builder, over the real
// fixture, without panicking or dropping the evidence the case is about.
func TestEveryCaseBuildsAPrompt(t *testing.T) {
	for _, c := range Cases {
		if c.Path == PathRestructure {
			continue // builds its prompt from the schemas, not the file list
		}
		got := BuildPrompt(c, true)
		if !strings.Contains(got, c.Subject) {
			t.Errorf("%s: the prompt does not carry the subject", c.Name)
		}
		if c.GateReport != "" && !strings.Contains(got, strings.TrimSpace(c.GateReport)) {
			t.Errorf("%s: the prompt does not carry the gate report", c.Name)
		}
	}
}

// And every case must be runnable: handed the answer it expects, the harness
// scores it as a pass. A case that cannot pass even when the model is right is
// measuring the fixture.
func TestEveryTriageCasePassesWhenTheModelIsRight(t *testing.T) {
	for _, c := range Cases {
		if c.Path != PathTriage {
			continue
		}
		var edits []llm.Edit
		for key, to := range c.Triage.WantEdits {
			edits = append(edits, llm.Edit{
				Path: c.Triage.EditFile, Key: key,
				// The value the fixture holds. A verdict without it is not
				// "the expected answer": `from` is half the contract the
				// applier enforces, and omitting it here made the suite score
				// a model that never read the file it was editing.
				From:      currentScalar(t, c.Files[c.Triage.EditFile], key),
				To:        to,
				Rationale: "because the fixture says so",
			})
		}
		p := &llm.Fake{ID: "fake", Verdict: &llm.Verdict{
			Classification:   c.WantClass,
			Summary:          "the expected answer",
			Reasoning:        "the expected reasoning",
			EscalationReason: "the expected escalation",
			Edits:            edits,
		}}

		res := Run(context.Background(), p, "system", c, true)
		if !res.Pass() {
			t.Errorf("%s: handed the expected answer, the harness still failed it "+
				"(class=%v edits=%v grounded=%v) %v",
				c.Name, res.ClassOK, res.EditsOK, res.Grounded, res.Notes)
		}
	}
}

// currentScalar reads the value a fixture holds at a dotted key, so a test can
// state the edit the way a correct model would: with the file's own value in
// `from`.
func currentScalar(t *testing.T, doc, key string) string {
	t.Helper()
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(doc), &node); err != nil {
		t.Fatalf("fixture is not YAML: %v", err)
	}
	cur := &node
	if cur.Kind == yaml.DocumentNode && len(cur.Content) > 0 {
		cur = cur.Content[0]
	}
	for _, part := range strings.Split(key, ".") {
		found := false
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == part {
				cur, found = cur.Content[i+1], true
				break
			}
		}
		if !found {
			t.Fatalf("fixture has no key %q (at %q)", key, part)
		}
	}
	return cur.Value
}
