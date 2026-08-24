package main

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
