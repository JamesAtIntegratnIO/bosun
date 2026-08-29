package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAKeyIsFoundOrHonestlyAbsent(t *testing.T) {
	root := repoWith(t, map[string]string{"values.yaml": `
cleanupJobs:
  admissionReports:
    image:
      tag: "1.34.1"
webhooksCleanup:
  image:
    tag: "1.34.1"
`})
	fk := NewFileKeys(root)
	for key, want := range map[string]bool{
		"cleanupJobs.admissionReports.image.tag":        true,
		"webhooksCleanup.image.tag":                     true,
		"cleanupJobs.updateRequests.image.tag":          false,
		"policyReportsCleanup.image.tag":                false,
		"cleanupJobs.admissionReports.image.tag.deeper": false,
	} {
		got, err := fk.Has("values.yaml", key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got != want {
			t.Errorf("Has(%q) = %v, want %v", key, got, want)
		}
	}
}

// Kargo's yaml-update only writes the first document, so a key living in a
// later one is never written, which is a dead pin, and reading every
// document here would hide it.
func TestOnlyTheFirstDocumentCounts(t *testing.T) {
	root := repoWith(t, map[string]string{"multi.yaml": `
image:
  tag: "1"
---
second:
  tag: "2"
`})
	fk := NewFileKeys(root)
	if has, _ := fk.Has("multi.yaml", "image.tag"); !has {
		t.Error("the first document's key must be found")
	}
	if has, _ := fk.Has("multi.yaml", "second.tag"); has {
		t.Error("a key in a later document is never written by Kargo and must read as absent")
	}
}

func TestALeadingSeparatorIsNotAnEmptyDocument(t *testing.T) {
	root := repoWith(t, map[string]string{"lead.yaml": "---\nimage:\n  tag: \"1\"\n"})
	fk := NewFileKeys(root)
	if has, err := fk.Has("lead.yaml", "image.tag"); err != nil || !has {
		t.Fatalf("has=%v err=%v", has, err)
	}
}

// "Absent" produces a finding, so it may only be claimed about a path that was
// walked. Anything exotic makes no claim.
func TestAPathThisCannotWalkMakesNoClaim(t *testing.T) {
	root := repoWith(t, map[string]string{"list.yaml": "items:\n  - name: a\n  - name: b\n"})
	fk := NewFileKeys(root)
	if has, _ := fk.Has("list.yaml", "items.0.name"); !has {
		t.Error("an indexed path that resolves should be found")
	}
	if has, _ := fk.Has("list.yaml", "items.nope.name"); !has {
		t.Error("an index this cannot parse must not be reported as a dead pin")
	}
}

func TestAMissingFileIsAnErrorNotAnAbsentKey(t *testing.T) {
	fk := NewFileKeys(repoWith(t, map[string]string{}))
	if _, err := fk.Has("gone.yaml", "a.b"); err == nil {
		t.Fatal("a missing file must error, so it renders as a moved target rather than a dead key")
	}
}

func TestItRefusesToLeaveTheCheckout(t *testing.T) {
	fk := NewFileKeys(repoWith(t, map[string]string{}))
	if _, err := fk.Has("../../etc/passwd", "x"); err == nil {
		t.Fatal("a path escaping the checkout must be refused")
	}
}

// A separator with nothing but comments before it opens the first document.
// Reading it as a terminator made every key in such a file look absent, which
// is a false dead-pin report for the whole file, ten of them, live.
func TestACommentHeaderBeforeASeparatorIsNotTheFirstDocument(t *testing.T) {
	root := repoWith(t, map[string]string{"deploy.yaml": `# The MCP server for ArgoCD.
#
# Pinned by Kargo; see kargo-projects/values.yaml.
---
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        - image: ghcr.io/x/y:1.2.3
---
apiVersion: v1
kind: Service
`})
	fk := NewFileKeys(root)
	has, err := fk.Has("deploy.yaml", "spec.template.spec.containers.0.image")
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Fatal("the Deployment is the first document; its keys must be found")
	}
	if has, _ := fk.Has("deploy.yaml", "spec.ports.0.port"); has {
		t.Error("the Service is a later document and Kargo never writes it")
	}
}
