package migrate

import (
	"strings"
	"testing"
)

// The gate writes these lines and this reads them back. Both halves live in
// this package so a change to the report's shape breaks a test rather than
// quietly selecting nothing.

func TestSubjectsNamesWhatTheReportIsAbout(t *testing.T) {
	report := "<!-- gitops-gate -->\n" +
		"### Resources\n\n" +
		"**Removed (2)**\n\n" +
		"- `ClusterRole/trivy-operator-explorer`\n" +
		"- `ClusterRoleBinding/trivy-operator-explorer in security`\n" +
		"  - was bound to the operator's service account\n\n" +
		"**Added (1)**\n\n" +
		"- `Role/trivy-operator-explorer in security`\n"

	got := Subjects(report)
	for _, want := range []string{"ClusterRole", "ClusterRoleBinding", "Role", "trivy-operator-explorer"} {
		if !has(got, want) {
			t.Errorf("Subjects did not name %q: %v", want, got)
		}
	}
	// The namespace stamp is chart-diff's, not the object's identity.
	for _, never := range []string{"security", "trivy-operator-explorer in security"} {
		if has(got, never) {
			t.Errorf("Subjects picked up %q, which is not what the finding is about: %v", never, got)
		}
	}
	// Names first: they are the terms that only match when a commit is
	// genuinely about this, and a caller that takes the front of the list
	// should get those rather than `Role`.
	if got[0] != "trivy-operator-explorer" {
		t.Errorf("names should sort ahead of kinds, got %v", got)
	}
}

// A dropped version is already parsed into a structure. Reading it out of prose
// a second time would be a second parser for one format.
func TestSubjectsTakesTheDroppedVersionFindingFromTheParsedLine(t *testing.T) {
	report := "<!-- gitops-gate -->\n" +
		Line("CustomResourceDefinition/externalsecrets.external-secrets.io",
			"v1alpha1, v1beta1", "ExternalSecret", "v1") + "\n"

	got := Subjects(report)
	for _, want := range []string{"ExternalSecret", "external-secrets.io", "externalsecrets.external-secrets.io"} {
		if !has(got, want) {
			t.Errorf("Subjects did not name %q: %v", want, got)
		}
	}
}

// A term short enough to match everything selects nothing; it only makes the
// selection look large.
func TestSubjectsDropsTermsTooShortToMeanAnything(t *testing.T) {
	got := Subjects("**Removed (1)**\n\n- `Pod/db`\n")
	for _, s := range got {
		if len(s) < 3 {
			t.Errorf("kept %q, which matches everything", s)
		}
	}
	if !has(got, "Pod") {
		t.Errorf("dropped a term that is long enough: %v", got)
	}
}

func TestSubjectsIgnoresProse(t *testing.T) {
	report := "The gate is RED.\n\nSomething about `values.yaml` and a note.\n" +
		"- this is not an object line\n"
	if got := Subjects(report); len(got) != 0 {
		t.Errorf("Subjects invented %v out of prose", got)
	}
}

func has(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
