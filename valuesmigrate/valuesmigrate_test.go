package valuesmigrate

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("fixture is not YAML: %v", err)
	}
	return out
}

func apply(t *testing.T, file, prefix string, ops []Op) string {
	t.Helper()
	out, err := Apply([]byte(file), prefix, ops)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return string(out)
}

// The bump this exists for, end to end through the two halves that write:
// three keys the new chart's schema refuses, one it renamed, and a file that
// also holds two other addons and the notes somebody left in it.
func TestTheBumpThisWasBuiltFor(t *testing.T) {
	const file = `# Cluster addons. Order is deliberate: bootstrap first.
cert-manager:
  enabled: true
  defaultVersion: 1.19.3

bosun:
  enabled: true
  defaultVersion: 0.25.1
  valuesObject:
    gate:
      # We poll rather than take webhooks; the cluster has no ingress.
      mode: service
      wait: true
      inventorySource: argocd
      argocd:
        baseURL: https://argocd.internal
        port: 8080

metallb:
  enabled: true
`
	original := decode(t, `
gate:
  mode: service
  wait: true
  inventorySource: argocd
  argocd:
    baseURL: https://argocd.internal
    port: 8080
`)
	proposed := decode(t, `
gate:
  argocd:
    baseURL: https://argocd.internal
    podPort: 8080
`)

	ops := Plan(original, proposed)
	want := []Op{
		{Kind: OpRename, Key: "gate.argocd.port", To: "podPort"},
		{Kind: OpRemove, Key: "gate.inventorySource"},
		{Kind: OpRemove, Key: "gate.mode"},
		{Kind: OpRemove, Key: "gate.wait"},
	}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("plan:\n got %v\nwant %v", ops, want)
	}

	anchor, err := Locate(map[string][]byte{"addons.yaml": []byte(file)}, ops, original)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if anchor.Path != "addons.yaml" || anchor.Prefix != "bosun.valuesObject" {
		t.Fatalf("anchor = %+v", anchor)
	}

	got := apply(t, file, anchor.Prefix, ops)

	// The point of the whole package: the file is still the file.
	for _, keep := range []string{
		"# Cluster addons. Order is deliberate: bootstrap first.",
		"cert-manager:",
		"  defaultVersion: 1.19.3",
		"metallb:",
		"        baseURL: https://argocd.internal",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("the rest of the file must be untouched, %q is gone:\n%s", keep, got)
		}
	}
	for _, gone := range []string{"mode: service", "wait: true", "inventorySource: argocd", "port: 8080"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q should be gone:\n%s", gone, got)
		}
	}
	if !strings.Contains(got, "        podPort: 8080") {
		t.Errorf("the rename must keep the value and its indentation:\n%s", got)
	}

	// And the result is the document the harness validated, not something
	// near it. This is the check the agent repeats against the real merge.
	var back map[string]any
	if err := yaml.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("the result is not YAML: %v", err)
	}
	bosun := back["bosun"].(map[string]any)["valuesObject"]
	if !reflect.DeepEqual(bosun, proposed) {
		t.Errorf("the file now holds\n %#v\nand the harness accepted\n %#v", bosun, proposed)
	}
}

// A comment introducing the *next* setting belongs to that setting. yaml.v3
// attaches it to the following key and reports that key's own line, so an
// extent measured as "up to the next sibling" takes it away with the entry
// before it.
func TestRemovingAKeyLeavesTheNextKeysCommentAlone(t *testing.T) {
	got := apply(t, `a:
  first: 1
  # why second is what it is
  second: 2
`, "", []Op{{Kind: OpRemove, Key: "a.first"}})

	if !strings.Contains(got, "# why second is what it is") {
		t.Errorf("the next key's comment must survive:\n%s", got)
	}
	if !strings.Contains(got, "second: 2") {
		t.Errorf("the next key must survive:\n%s", got)
	}
}

func TestRemovingAKeyTakesItsWholeBlock(t *testing.T) {
	got := apply(t, `a:
  gone:
    deep:
      - one
      - two
  kept: yes
`, "", []Op{{Kind: OpRemove, Key: "a.gone"}})

	if strings.Contains(got, "deep") || strings.Contains(got, "- one") {
		t.Errorf("the whole block must go:\n%s", got)
	}
	if !strings.Contains(got, "kept: yes") {
		t.Errorf("the sibling must stay:\n%s", got)
	}
}

// `gate:` with nothing under it is null, not an empty section, and helm reads
// a null in a user's values as an instruction to drop the chart's own defaults
// for that key. Removing three settings would then remove every default beside
// them, which renders, and changes what deploys.
func TestAnEmptiedSectionGoesWithItsLastKey(t *testing.T) {
	got := apply(t, `top:
  gate:
    mode: service
  other: 1
`, "", []Op{{Kind: OpRemove, Key: "top.gate.mode"}})

	if strings.Contains(got, "gate:") {
		t.Errorf("an emptied section must not be left behind as a null:\n%s", got)
	}
	if !strings.Contains(got, "other: 1") {
		t.Errorf("the sibling must stay:\n%s", got)
	}
	// And not one level too far.
	if !strings.Contains(got, "top:") {
		t.Errorf("the cascade must stop where the section is no longer empty:\n%s", got)
	}
}

func TestRenameKeepsTheValuesQuotingAndComment(t *testing.T) {
	got := apply(t, `a:
  port: "8080"   # the pod port, not the service port
`, "", []Op{{Kind: OpRename, Key: "a.port", To: "podPort"}})

	if want := `  podPort: "8080"   # the pod port, not the service port`; !strings.Contains(got, want) {
		t.Errorf("want %q in:\n%s", want, got)
	}
}

// Renaming onto a key that already exists produces a document declaring it
// twice, which yaml resolves by keeping one of them, and which one is not
// something a reader of the diff would predict.
func TestRenameRefusesToLandOnAnExistingKey(t *testing.T) {
	_, err := Apply([]byte("a:\n  port: 8080\n  podPort: 9090\n"), "",
		[]Op{{Kind: OpRename, Key: "a.port", To: "podPort"}})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want a refusal naming the collision, got %v", err)
	}
}

func TestSetAddsAKeyToAnExistingSection(t *testing.T) {
	got := apply(t, `metrics:
  serviceMonitor:
    enabled: true
other: 1
`, "", []Op{{Kind: OpSet, Key: "metrics.serviceMonitor.interval", Value: "30s"}})

	if want := "    interval: 30s"; !strings.Contains(got, want) {
		t.Errorf("want %q in:\n%s", want, got)
	}
	if !strings.Contains(got, "other: 1") {
		t.Errorf("the following key must stay where it was:\n%s", got)
	}
}

// `8080` and `"8080"` are different documents, and a chart whose schema says
// `type: string` refuses the first. The type has to survive as far as the line
// that gets written, which is why a plan carries the value and not a rendering
// of it.
func TestSetWritesTheTypeTheProposalHeld(t *testing.T) {
	got := apply(t, "a:\n  keep: 1\n", "", []Op{
		{Kind: OpSet, Key: "a.text", Value: "8080"},
		{Kind: OpSet, Key: "a.number", Value: 8080},
		{Kind: OpSet, Key: "a.flag", Value: true},
	})
	for _, want := range []string{`text: "8080"`, "number: 8080", "flag: true"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
}

func TestSetRefusesASectionThatDoesNotExist(t *testing.T) {
	_, err := Apply([]byte("a:\n  keep: 1\n"), "", []Op{{Kind: OpSet, Key: "b.c.d", Value: 1}})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("want a refusal naming the missing section, got %v", err)
	}
}

// A flow mapping puts several entries on one line, so removing one of them is
// a change inside a line rather than to a set of lines. There is no
// partially-correct answer here that beats a human.
func TestFlowMappingsAreRefusedRatherThanGuessedAt(t *testing.T) {
	for _, op := range []Op{
		{Kind: OpRemove, Key: "a.b"},
		{Kind: OpRename, Key: "a.b", To: "c"},
	} {
		_, err := Apply([]byte("a: {b: 1, d: 2}\n"), "", []Op{op})
		if err == nil || !strings.Contains(err.Error(), "flow mapping") {
			t.Errorf("%s: want a refusal naming the shape, got %v", op, err)
		}
	}
}

// A value at one path and the same value at another is the shape a rename is
// recognised from, so two candidates must not be read as one.
func TestAnAmbiguousRenameIsARemovalAndAnAddition(t *testing.T) {
	ops := Plan(
		decode(t, "a:\n  old: same\n"),
		decode(t, "a:\n  x: same\n  y: same\n"))

	var kinds []OpKind
	for _, o := range ops {
		kinds = append(kinds, o.Kind)
	}
	for _, k := range kinds {
		if k == OpRename {
			t.Fatalf("two candidates is not a rename anybody can prove: %v", ops)
		}
	}
}

func TestLocateRefusesAKeyThatAppearsTwice(t *testing.T) {
	files := map[string][]byte{
		"a.yaml": []byte("one:\n  valuesObject:\n    gate:\n      mode: service\n"),
		"b.yaml": []byte("two:\n  valuesObject:\n    gate:\n      mode: service\n"),
	}
	original := decode(t, "gate:\n  mode: service\n")
	_, err := Locate(files, []Op{{Kind: OpRemove, Key: "gate.mode"}}, original)
	if err == nil || !strings.Contains(err.Error(), "more than one place") {
		t.Fatalf("want a refusal naming the ambiguity, got %v", err)
	}
}

// The values a chart is handed are not always the whole file, and they are not
// always a subtree either. Both shapes have to resolve, and a wrong prefix
// edits a different addon.
func TestLocateFindsBothShapes(t *testing.T) {
	original := decode(t, "gate:\n  mode: service\n")
	ops := []Op{{Kind: OpRemove, Key: "gate.mode"}}

	for _, tc := range []struct {
		name, file, wantPrefix string
	}{
		{"a values file the Application passes with -f", "gate:\n  mode: service\n", ""},
		{"an addon inside a chart of charts", "bosun:\n  valuesObject:\n    gate:\n      mode: service\n", "bosun.valuesObject"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Locate(map[string][]byte{"v.yaml": []byte(tc.file)}, ops, original)
			if err != nil {
				t.Fatal(err)
			}
			if got.Prefix != tc.wantPrefix {
				t.Errorf("prefix = %q, want %q", got.Prefix, tc.wantPrefix)
			}
		})
	}
}

// A key whose value in the file is not the value the render read is not that
// key. The suffix match alone would claim it, and the file it claims is one
// this would then write to.
func TestLocateWantsTheValueTheRenderRead(t *testing.T) {
	files := map[string][]byte{"a.yaml": []byte("x:\n  gate:\n    mode: webhook\n")}
	_, err := Locate(files, []Op{{Kind: OpRemove, Key: "gate.mode"}}, decode(t, "gate:\n  mode: service\n"))
	if err == nil || !strings.Contains(err.Error(), "no file in this change") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// Removing the last key of a section takes the section with it, which it must:
// a key left holding nothing is null, and helm reads a null in a user's values
// as an instruction to drop the chart's own defaults for it. So a plan that
// clears a section and writes something back into it has to write first.
func TestASectionIsNotEmptiedBeforeSomethingIsAddedToIt(t *testing.T) {
	got := apply(t, `top:
  gate:
    mode: service
    wait: true
`, "", []Op{
		{Kind: OpRemove, Key: "top.gate.mode"},
		{Kind: OpRemove, Key: "top.gate.wait"},
		{Kind: OpSet, Key: "top.gate.podPort", Value: 8080},
	})

	if !strings.Contains(got, "podPort: 8080") {
		t.Errorf("the added key must survive the removals:\n%s", got)
	}
	for _, gone := range []string{"mode: service", "wait: true"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q should be gone:\n%s", gone, got)
		}
	}
}
