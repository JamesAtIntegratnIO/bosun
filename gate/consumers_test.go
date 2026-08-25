package gate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A CRD fixture that also names its consumer kind, the way a real chart does.
func crdWithNames(name, kind string, versions ...map[string]any) map[string]any {
	obj := crd(name, versions...)
	spec := obj["spec"].(map[string]any)
	spec["names"] = map[string]any{"kind": kind, "plural": strings.Split(name, ".")[0]}
	return obj
}

func esoFinding(t *testing.T) ObjectChange {
	t.Helper()
	before := []Object{objWith("before", crdWithNames("externalsecrets.external-secrets.io", "ExternalSecret",
		map[string]any{"name": "v1beta1", "served": true},
		map[string]any{"name": "v1", "served": true}))}
	after := []Object{objWith("after", crdWithNames("externalsecrets.external-secrets.io", "ExternalSecret",
		map[string]any{"name": "v1", "served": true}))}
	got := diffObjects(before, after)
	if len(got) != 1 || got[0].Kind != "crdVersionRemoved" {
		t.Fatalf("want one crdVersionRemoved finding, got %+v", got)
	}
	return got[0]
}

// The finding carries the repair contract: which kind consumers declare, and
// the served version they must move to. Without both, nothing downstream can
// act, and the old always-block behaviour returns.
func TestADroppedVersionNamesItsConsumersDestination(t *testing.T) {
	f := esoFinding(t)
	if f.Resource != "ExternalSecret" {
		t.Errorf("want the consumer kind from spec.names.kind, got %q", f.Resource)
	}
	if f.To != "v1" {
		t.Errorf("want the surviving version as the destination, got %q", f.To)
	}
}

func writeManifest(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const declaring = "apiVersion: external-secrets.io/v1beta1\nkind: ExternalSecret\nmetadata:\n  name: a\n"

// The blast radius is the manifests still declaring a dropped version, so they
// are what decides blocking: present blocks, counted-at-zero reports, and not
// scanned at all blocks -- "we could not look" must never read as safe.
func TestConsumersDecideWhetherADroppedVersionBlocks(t *testing.T) {
	finding := esoFinding(t)

	unscanned := &DiffResult{Objects: []ObjectChange{finding}}
	if !unscanned.Blocking() {
		t.Error("an unscanned dropped version must block")
	}

	withConsumer := t.TempDir()
	writeManifest(t, withConsumer, "platform/es.yaml", declaring)
	res := &DiffResult{Objects: []ObjectChange{finding}}
	AnnotateConsumers(res, withConsumer)
	if !res.Objects[0].ConsumersKnown || len(res.Objects[0].ConsumerFiles) != 1 {
		t.Fatalf("want the declaring manifest counted, got %+v", res.Objects[0])
	}
	if !res.Blocking() {
		t.Error("a dropped version with consumers must block")
	}

	empty := &DiffResult{Objects: []ObjectChange{finding}}
	AnnotateConsumers(empty, t.TempDir())
	if !empty.Objects[0].ConsumersKnown {
		t.Fatal("the empty repository was still scanned")
	}
	if empty.Blocking() {
		t.Error("a dropped version nothing declares must not block -- this is what lets a repair turn the gate green")
	}
}

// A finding without the consumer kind cannot say what to scan for, so it stays
// unscanned -- and therefore blocking -- rather than being counted at zero by
// a scan that was looking for nothing.
func TestAFindingWithoutAKindStaysBlocking(t *testing.T) {
	before := []Object{objWith("before", crd("things.example.io",
		map[string]any{"name": "v1beta1", "served": true},
		map[string]any{"name": "v1", "served": true}))}
	after := []Object{objWith("after", crd("things.example.io",
		map[string]any{"name": "v1", "served": true}))}
	got := diffObjects(before, after)

	res := &DiffResult{Objects: got}
	AnnotateConsumers(res, t.TempDir())
	if res.Objects[0].ConsumersKnown {
		t.Fatal("nothing to scan for, so nothing should claim to have been scanned")
	}
	if !res.Blocking() {
		t.Error("must still block")
	}
}

// The report line is rendered by the shared migrate package, and the consumers
// appear under it -- they are both the human's evidence and the reason the
// finding blocks, so the report must carry them.
func TestTheReportNamesTheConsumers(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "platform/es.yaml", declaring)
	res := &DiffResult{Objects: []ObjectChange{esoFinding(t)}}
	AnnotateConsumers(res, root)

	var b strings.Builder
	res.Report(&b)
	report := b.String()

	wantLine := "- `CustomResourceDefinition/externalsecrets.external-secrets.io in ns`: no longer serves `v1beta1` — `ExternalSecret` manifests must move to `v1`"
	if !strings.Contains(report, wantLine) {
		t.Errorf("want the repairable line:\n%s\nin report:\n%s", wantLine, report)
	}
	if !strings.Contains(report, "1 manifest(s) in this repository still declare a dropped version") ||
		!strings.Contains(report, "`platform/es.yaml`") {
		t.Errorf("want the consumer named in the report:\n%s", report)
	}
}

// The live scenario that forced this: the repair migrated every consumer to
// the survivor, and the re-run gate blocked on the migration's own apiVersion
// moves. A move the findings themselves demand is the repair, not a new
// migration -- and a move they do not name still blocks, exactly as before.
func TestTheRepairsOwnMoveDoesNotReBlock(t *testing.T) {
	crdFinding := func() ObjectChange {
		before := []Object{objWith("before", crdWithNames("clustersecretstores.external-secrets.io", "ClusterSecretStore",
			map[string]any{"name": "v1beta1", "served": true},
			map[string]any{"name": "v1", "served": true}))}
		after := []Object{objWith("after", crdWithNames("clustersecretstores.external-secrets.io", "ClusterSecretStore",
			map[string]any{"name": "v1", "served": true}))}
		got := diffObjects(before, after)
		if len(got) != 1 || got[0].Kind != "crdVersionRemoved" {
			t.Fatalf("want one crdVersionRemoved finding, got %+v", got)
		}
		return got[0]
	}

	repair := ObjectChange{Kind: "apiVersion", Object: "ClusterSecretStore/central-store in secrets",
		From: "external-secrets.io/v1beta1", To: "external-secrets.io/v1"}
	wrongTarget := ObjectChange{Kind: "apiVersion", Object: "ClusterSecretStore/other in secrets",
		From: "external-secrets.io/v1beta1", To: "external-secrets.io/v2"}
	wrongKind := ObjectChange{Kind: "apiVersion", Object: "PushSecret/other in secrets",
		From: "external-secrets.io/v1beta1", To: "external-secrets.io/v1"}

	objects := []ObjectChange{crdFinding(), repair, wrongTarget, wrongKind}
	markMigrationConsistent(objects)

	if !objects[1].PartOfMigration {
		t.Error("the move the finding demands must be marked as the repair")
	}
	if objects[2].PartOfMigration || objects[3].PartOfMigration {
		t.Error("a move to another version, or of another kind, is still unexplained")
	}

	// With consumers counted at zero, the repaired diff is green; the two
	// unexplained moves each keep it red on their own.
	res := &DiffResult{Objects: []ObjectChange{objects[0], objects[1]}}
	AnnotateConsumers(res, t.TempDir())
	if res.Blocking() {
		t.Error("a completed repair must go green: consumers at zero and only the demanded move")
	}
	still := &DiffResult{Objects: []ObjectChange{objects[0], objects[1], objects[2]}}
	AnnotateConsumers(still, t.TempDir())
	if !still.Blocking() {
		t.Error("an unexplained apiVersion move must still block")
	}

	var b strings.Builder
	res.Report(&b)
	if !strings.Contains(b.String(), "the repair, not a new migration") {
		t.Errorf("the report must say why the move is not blocking:\n%s", b.String())
	}
}

// A CRD removed outright is the limiting case of dropping served versions --
// all of them, no survivor. It joins the consumer-scanned class: consumers
// present blocks, counted at zero the report says the removal looks safe from
// inspection, and the agent's parser deliberately cannot repair it (there is
// nowhere to move).
func TestARemovedCRDIsInspectedNotJustListed(t *testing.T) {
	before := []Object{objWith("before", crdWithNames("admissionreports.kyverno.io", "AdmissionReport",
		map[string]any{"name": "v2", "served": true},
		map[string]any{"name": "v1alpha2", "served": true}))}

	got := diffObjects(before, nil)
	if len(got) != 1 || got[0].Kind != "crdVersionRemoved" {
		t.Fatalf("want the removal as a consumer-scanned finding, got %+v", got)
	}
	f := got[0]
	if f.Resource != "AdmissionReport" || f.To != "" || f.From != "v1alpha2, v2" {
		t.Fatalf("want every served version dropped with no survivor, got %+v", f)
	}

	// Nothing in the repository declares it: report, do not block, say why.
	clean := &DiffResult{Objects: []ObjectChange{f}}
	AnnotateConsumers(clean, t.TempDir())
	if !clean.Objects[0].ConsumersKnown || clean.Blocking() {
		t.Errorf("an unused removed API must not block, got %+v", clean.Objects[0])
	}
	var b strings.Builder
	clean.Report(&b)
	if !strings.Contains(b.String(), "removed outright") ||
		!strings.Contains(b.String(), "from inspection the removal looks safe") {
		t.Errorf("the report must say the inspection happened and what it found:\n%s", b.String())
	}

	// A manifest still uses it: that is the blast radius, and it blocks.
	used := t.TempDir()
	writeManifest(t, used, "policies/report.yaml",
		"apiVersion: kyverno.io/v2\nkind: AdmissionReport\nmetadata:\n  name: r\n")
	dirty := &DiffResult{Objects: []ObjectChange{f}}
	AnnotateConsumers(dirty, used)
	if len(dirty.Objects[0].ConsumerFiles) != 1 || !dirty.Blocking() {
		t.Errorf("a removed API with consumers must block and name them, got %+v", dirty.Objects[0])
	}
}

func bindingWith(name string, subjects ...map[string]any) map[string]any {
	subs := make([]any, 0, len(subjects))
	for _, s := range subjects {
		subs = append(subs, s)
	}
	return map[string]any{
		"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
		"metadata": map[string]any{"name": name},
		"subjects": subs,
		"roleRef":  map[string]any{"kind": "ClusterRole", "name": name},
	}
}

// A removed binding is routine chart tidying or a workload silently losing
// every permission it runs on, and the difference is whether its
// ServiceAccount is still bound to anything in the new render.
func TestARemovedBindingNamesItsOrphanedServiceAccount(t *testing.T) {
	sa := map[string]any{"kind": "ServiceAccount", "name": "explorer", "namespace": "trivy-system"}
	before := []Object{objWith("before", bindingWith("explorer", sa))}

	got := diffObjects(before, nil)
	if len(got) != 1 || got[0].Kind != "removed" {
		t.Fatalf("want a removed finding, got %+v", got)
	}
	if !strings.Contains(got[0].Note, "trivy-system/explorer") ||
		!strings.Contains(got[0].Note, "no role binding at all") {
		t.Errorf("want the orphaned ServiceAccount named, got note %q", got[0].Note)
	}

	var b strings.Builder
	(&DiffResult{Objects: got}).Report(&b)
	if !strings.Contains(b.String(), "trivy-system/explorer") {
		t.Errorf("the note must reach the report:\n%s", b.String())
	}

	// Rebound elsewhere in the head render: routine tidying, no note.
	rebound := diffObjects(before, []Object{objWith("after", bindingWith("explorer-v2", sa))})
	if len(rebound) != 2 {
		t.Fatalf("want a removal and an addition, got %+v", rebound)
	}
	for _, c := range rebound {
		if c.Note != "" {
			t.Errorf("a rebound ServiceAccount is not a finding, got note %q", c.Note)
		}
	}

	// A head binding whose subjects cannot be read: no claim either way.
	blind := diffObjects(before, []Object{{Cluster: "prod", Kind: "ClusterRoleBinding",
		Name: "opaque", APIVersion: "rbac.authorization.k8s.io/v1", Hash: "x"}})
	for _, c := range blind {
		if c.Note != "" {
			t.Errorf("an unreadable binding forbids the claim, got note %q", c.Note)
		}
	}
}
