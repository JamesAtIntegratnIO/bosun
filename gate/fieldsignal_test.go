package gate

import (
	"strings"
	"testing"
)

// These pin the report's signal-to-noise fixes, each measured on a live pull
// request before it was fixed. The podinfo 6.7.0 -> 6.14.1 bump reported ten
// changed fields: nine were one inserted flag shifting a command array plus a
// namespace stamp the chart started writing, and the one line a reader needed
// was indistinguishable from the noise around it.

func deployment(ns string, command ...any) map[string]any {
	meta := map[string]any{"name": "podinfo"}
	if ns != "" {
		meta["namespace"] = ns
	}
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment", "metadata": meta,
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{
						"name": "podinfo", "command": command,
					}},
				},
			},
		},
	}
}

// One flag inserted into a command shifts every element after it, and
// index-wise that read as one changed field per shifted position. The finding
// is membership, not position: one line, `gained`.
func TestAnInsertedFlagIsOneLineNotAShiftedArray(t *testing.T) {
	before, _ := objectFrom("app", "prod", "ns",
		deployment("", "./podinfo", "--cert-path=/data/cert", "--port-metrics=9797", "--grpc-port=9999", "--level=info"))
	after, _ := objectFrom("app", "prod", "ns",
		deployment("", "./podinfo", "--prefix=/", "--cert-path=/data/cert", "--port-metrics=9797", "--grpc-port=9999", "--level=info"))

	fields, truncated := diffFields(before.Body, after.Body)
	if truncated != 0 {
		t.Fatalf("nothing to truncate here, got %d", truncated)
	}
	if len(fields) != 1 {
		t.Fatalf("one insertion must be one line, got %d: %+v", len(fields), fields)
	}
	f := fields[0]
	if !strings.HasSuffix(f.Path, "command[+]") || f.To != "--prefix=/" || f.From != "" {
		t.Fatalf("want the gained element named, got %+v", f)
	}

	// And the rendering says what happened, without an index to count to.
	var b strings.Builder
	fieldLine(&b, f)
	if want := "gained `--prefix=/`"; !strings.Contains(b.String(), want) {
		t.Errorf("want %q in %q", want, b.String())
	}
}

// A replaced element is the case alignment must NOT claim: index-wise it is
// one line (`a` -> `b`), aligned it would be two (lost, gained).
func TestAReplacedElementStaysOneArrow(t *testing.T) {
	before, _ := objectFrom("app", "prod", "ns", deployment("", "--level=info"))
	after, _ := objectFrom("app", "prod", "ns", deployment("", "--level=debug"))

	fields, _ := diffFields(before.Body, after.Body)
	if len(fields) != 1 {
		t.Fatalf("want one line, got %+v", fields)
	}
	if fields[0].From != "--level=info" || fields[0].To != "--level=debug" {
		t.Fatalf("want the replacement as one arrow, got %+v", fields[0])
	}
	if strings.HasSuffix(fields[0].Path, "[+]") || strings.HasSuffix(fields[0].Path, "[-]") {
		t.Fatalf("a replacement is positional, not membership: %+v", fields[0])
	}
}

// A chart version that starts stamping `metadata.namespace` where the last
// one left it implicit changes no applied byte -- ArgoCD sends the
// destination either way. objectFrom already resolved the identity; the hash
// has to agree, or the report claims a changed resource with a diff line
// saying the namespace was set to the place it was always going.
func TestANamespaceStampAloneIsNotAChange(t *testing.T) {
	implicit, _ := objectFrom("app", "prod", "podinfo", deployment("", "--level=info"))
	stamped, _ := objectFrom("app", "prod", "podinfo", deployment("podinfo", "--level=info"))
	if implicit.Hash != stamped.Hash {
		t.Fatal("a namespace stamp equal to the destination must not read as a change")
	}

	// A genuinely different namespace is a different identity, not a stamp.
	elsewhere, _ := objectFrom("app", "prod", "podinfo", deployment("elsewhere", "--level=info"))
	if elsewhere.Namespace != "elsewhere" {
		t.Fatalf("a real namespace must survive, got %q", elsewhere.Namespace)
	}
}

// The fields a reader chose surface above the fold; the chart's own fold away
// behind a summary that says whether opening it can matter.
func TestFieldsTouchingRepositoryValuesSurfaceAboveTheFold(t *testing.T) {
	render := func(o ObjectChange) string {
		var b strings.Builder
		writeFields(&b, o)
		return b.String()
	}

	linked := render(ObjectChange{
		Kind: ObjectChanged, Object: "Deployment/podinfo", ValuesChecked: true,
		Fields: []FieldChange{
			{Path: "spec.replicas", From: "1", To: "3", SetHere: true},
			{Path: "spec.template.spec.containers.0.image", From: "a:1", To: "a:2"},
			{Path: "metadata.labels.chart", From: "x", To: "y"},
		},
	})
	if !strings.Contains(linked, "Values this repository sets:") ||
		!strings.Contains(linked, "`spec.replicas`: `1` → `3`") {
		t.Errorf("the chosen field must be above the fold:\n%s", linked)
	}
	if !strings.Contains(linked, "<summary>2 more, the chart's own</summary>") {
		t.Errorf("the rest must fold with a count:\n%s", linked)
	}
	if before, after, _ := strings.Cut(linked, "<details>"); !strings.Contains(before, "spec.replicas") ||
		strings.Contains(before, "containers.0.image") || !strings.Contains(after, "containers.0.image") {
		t.Errorf("partition landed on the wrong side of the fold:\n%s", linked)
	}

	// Checked and none linked: the summary is the whole read.
	none := render(ObjectChange{
		Kind: ObjectChanged, Object: "Deployment/podinfo", ValuesChecked: true,
		Fields: []FieldChange{{Path: "metadata.labels.chart", From: "x", To: "y"}},
	})
	if !strings.Contains(none, "none of them a value this repository sets") {
		t.Errorf("a checked diff with no match must say so:\n%s", none)
	}

	// Not checked: no claim either way, the old rendering.
	unchecked := render(ObjectChange{
		Kind: ObjectChanged, Object: "Deployment/podinfo",
		Fields: []FieldChange{{Path: "metadata.labels.chart", From: "x", To: "y"}},
	})
	if strings.Contains(unchecked, "value this repository sets") {
		t.Errorf("an unchecked diff must not claim it looked:\n%s", unchecked)
	}
}

// The marking end to end: leaves from the Application's own values, threaded
// through the table, land on the fields whose values the repository supplied.
func TestValueLeavesMarkTheFieldsTheyExplain(t *testing.T) {
	mk := func(image string) map[string]any {
		return map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"name": "d"},
			"spec": map[string]any{
				"replicas": 2,
				"image":    image,
			},
		}
	}
	before, _ := objectFrom("my-app", "prod", "ns", mk("repo/thing:1.0.0"))
	after, _ := objectFrom("my-app", "prod", "ns", mk("repo/thing:2.0.0"))
	after.Body["spec"].(map[string]any)["replicas"] = 5

	got := diffObjects([]Object{before}, []Object{after},
		map[string]map[string]bool{"my-app": {"5": true, "hello": true}})
	if len(got) != 1 || !got[0].ValuesChecked {
		t.Fatalf("want one checked change, got %+v", got)
	}
	marks := map[string]bool{}
	for _, f := range got[0].Fields {
		marks[f.Path] = f.SetHere
	}
	if !marks["spec.replicas"] {
		t.Errorf("the replica count is this repository's value and must be marked: %+v", got[0].Fields)
	}
	if marks["spec.image"] {
		t.Errorf("the image is the chart's own and must not be: %+v", got[0].Fields)
	}

	// A different app's leaves say nothing about this one.
	other := diffObjects([]Object{before}, []Object{after},
		map[string]map[string]bool{"other-app": {"5": true}})
	if other[0].ValuesChecked {
		t.Error("another Application's values must not count as checked here")
	}
}

// Short and boolean leaves must not claim half the chart. "true" appears in
// every render; a five-character floor guards the substring form and equality
// carries the short scalars that are genuinely theirs.
func TestTrivialLeavesDoNotClaimTheDiff(t *testing.T) {
	leaves := map[string]bool{"true": true, "1": true, "10m": true}
	if setHere(FieldChange{From: "false", To: "true"}, leaves) {
		t.Error("a boolean leaf marks nothing")
	}
	if !setHere(FieldChange{From: "1", To: "3"}, leaves) {
		t.Error("an exact short match is still theirs (replicaCount: 1)")
	}
	if setHere(FieldChange{From: "repo:1.2.3", To: "repo:1.2.4"}, leaves) {
		t.Error("a one-character substring must not fire")
	}
}

// A leaf naming the addon itself distinguishes nothing: every render of that
// addon carries it in labels, selectors and the names built from them. The
// first live rich report filed a kyverno bump's aggregation-label churn under
// "Values this repository sets", because the repository's values say `kyverno`
// somewhere and the substring form matched it everywhere.
func TestTheAddonsOwnNameDoesNotClaimItsLabelChurn(t *testing.T) {
	row := Row{App: "kyverno", Chart: "kyverno", Namespace: "kyverno"}
	leaves := leafValueSet(map[string]any{
		"admissionController": map[string]any{
			"serviceMonitor": map[string]any{"enabled": true},
			"replicas":       3,
		},
		"existingImagePullSecrets": []any{"kyverno"},
	}, identityTokens(row))

	churn := []FieldChange{
		{Path: "metadata.labels.app.kubernetes.io/name", To: "kyverno-admission-controller"},
		{Path: "aggregationRule.clusterRoleSelectors.0.matchLabels.app.kubernetes.io/instance", From: "kyverno"},
	}
	if setHere(churn[0], leaves) {
		t.Error("a label the chart stamps with the addon's name is not the reader's value")
	}

	// Equality is left alone. A field whose whole value IS the identity token
	// was still set to it, and the fold-not-filter design prices a debatable
	// mark at one line above the fold rather than a hidden finding.
	if !setHere(churn[1], leaves) {
		t.Error("an exact match on an identity leaf stays a match")
	}

	// The demotion is scoped to the identity: an ordinary leaf keeps both forms.
	if !setHere(FieldChange{From: "2", To: "3"}, leaves) {
		t.Error("a replica count the repository sets must still mark")
	}
	elsewhere := leafValueSet(map[string]any{"prefix": "kyverno"}, identityTokens(Row{Chart: "other"}))
	if !setHere(churn[0], elsewhere) {
		t.Error("the same string is a substring match for an Application it does not name")
	}
}
