package gate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/edits"
)

// Rule 1a's second contract: "any version the agent writes must appear
// verbatim in the gate's report".
//
// CONTRIBUTING.md has carried that line with "Still untested" beside it since
// the day it was written. This is the test.
//
// The producer is this package. Report prints every version through Inline,
// which rewrites a backtick and every control character as \xNN, and prints
// them inside code spans, table cells and prose. The consumer is
// edits.Policy.corroborated, a plain strings.Contains against the prompt the
// report was pasted into -- agent/triage.go sets Evidence to the whole user
// prompt. Nothing joins the two, and no compiler ever will: gate does not
// import edits, and must not.
//
// Every existing test on the consumer's side hands the applier a HAND-WRITTEN
// report string, in agent/ and in all of the evals/ fixtures alike. So no run
// in this repository's history has ever put a version out of a real report
// through a real corroboration, which is exactly the gap that makes this
// contract able to break in silence.
//
// What breaking it looks like: change how this file renders a version -- pad
// it, prefix it, split it across two table cells, escape a character it did not
// used to escape -- and strings.Contains stops matching. Every mechanical fix
// carrying a version is then refused as "an invented version" and escalated to
// a human instead. Nothing errors. Nothing is logged as wrong. The pull
// requests just quietly stop being fixed, and the agent looks careful doing it.
//
// These tests live beside the writer for the reason repaircontract_test.go
// gives two files over: the writer is the half that moves most.
func TestEveryVersionTheReportPrintsCorroboratesAnEdit(t *testing.T) {
	report, versions := reportWithEveryVersionShape(t)

	for _, v := range versions {
		root := t.TempDir()
		writeValues(t, root, v.value)

		res, err := edits.Apply(root,
			edits.Policy{Allow: []string{"**"}, Evidence: report},
			[]edits.Edit{{Path: "values.yaml", Key: "version", From: v.value, To: v.value}})
		if err != nil {
			t.Fatalf("%s: %v", v.where, err)
		}
		if len(res.Rejected) > 0 {
			t.Errorf("the gate printed %q in %s and the applier refused an edit carrying it:\n"+
				"  %s\n\n"+
				"gate.Inline or the report's version rendering has changed, and "+
				"edits.Policy.corroborated can no longer find what the gate wrote. Every "+
				"mechanical fix carrying a version now escalates instead, and the pull request "+
				"looks like a cautious agent rather than a broken one.\n\n"+
				"The report the gate produced:\n%s",
				v.value, v.where, res.Rejected[0].Reason, report)
		}
	}
}

// The other half, and not optional. A version the report never printed must
// still be refused -- without this, a corroboration that had degenerated to
// `return true` would pass the test above with nothing to show for it.
func TestAVersionTheReportNeverPrintedIsStillRefused(t *testing.T) {
	report, _ := reportWithEveryVersionShape(t)

	const invented = "9.9.9"
	if strings.Contains(report, invented) {
		t.Fatalf("the fixture report contains %s, so this test cannot tell a working "+
			"corroboration from one that always says yes", invented)
	}

	root := t.TempDir()
	writeValues(t, root, "1.0.0")
	res, err := edits.Apply(root,
		edits.Policy{Allow: []string{"**"}, Evidence: report},
		[]edits.Edit{{Path: "values.yaml", Key: "version", From: "1.0.0", To: invented}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rejected) != 1 {
		t.Fatalf("a version the gate never printed must be refused, got %d rejections; "+
			"corroboration is no longer checking anything", len(res.Rejected))
	}
}

// And the completeness half: the fixture above claims to carry a version in
// every shape the report prints one. This reads the rendered report back and
// fails on a version-shaped token the fixture did not name -- which is what
// happens the day Report grows a section.
//
// The pattern is edits.versionish, unexported there and copied here with its
// origin named. That is the same trade agent/comment_test.go already makes for
// the gateservice stamps: a copy whose drift is the thing under test is worth
// more than an export that widens an API for a test's convenience. If the two
// ever disagree, this test is the one that says so.
func TestTheFixtureCarriesEveryVersionShapeTheReportPrints(t *testing.T) {
	report, versions := reportWithEveryVersionShape(t)

	named := map[string]bool{}
	for _, v := range versions {
		named[v.value] = true
	}

	found := map[string]bool{}
	for _, tok := range tokens.FindAllString(report, -1) {
		if !versionishHere.MatchString(tok) {
			continue
		}
		found[tok] = true
		if !named[tok] {
			t.Errorf("the report prints the version-shaped token %q and "+
				"reportWithEveryVersionShape does not name it.\n"+
				"Report has grown a section that prints a version, and the contract test above "+
				"is not covering it. Add it to the fixture -- that is the whole point of this "+
				"test existing.", tok)
		}
	}
	if len(found) != len(versions) {
		t.Fatalf("found %d distinct version-shaped tokens in the report and the fixture "+
			"names %d; the report has stopped printing versions this fixture believes it "+
			"prints, or prints one this fixture does not name", len(found), len(versions))
	}
}

// An apiVersion is deliberately outside corroboration, and this is where that
// is written down.
//
// edits.versionish matches semver, v-prefixed semver and date-style tags. It
// does not match `v1alpha2`, so an edit moving a manifest between API versions
// is never checked against the evidence at all -- corroborated returns true for
// anything unversion-shaped, by design, because "corroborating those would
// reject legitimate toggles".
//
// That is the right call and it is worth a test, because the natural reading of
// "any version the agent writes must appear in the report" is that it covers
// this, and it does not. The guarantee for apiVersion moves lives in migrate/
// and structural/ instead. If versionish is ever widened to cover them, this
// test fails and points at the two packages whose refusals would then be
// duplicated here.
func TestAnAPIVersionIsNotCorroborated(t *testing.T) {
	for _, v := range []string{"v1alpha2", "v1beta1", "v1"} {
		if versionishHere.MatchString(v) {
			t.Errorf("%q is now version-shaped, so edits.Policy.corroborated has started "+
				"checking apiVersion moves against the report. That is a real change in what "+
				"the applier guarantees: check migrate/ and structural/ still own the "+
				"refusals for those, and that a report naming the survivor version reaches "+
				"the applier as evidence.", v)
		}
	}
}

// versionishHere is edits.versionish, copied. See the comment above.
var versionishHere = regexp.MustCompile(`^v?\d+\.\d+(\.\d+)?([.\-+][0-9A-Za-z.\-]+)?$`)

// tokens splits the rendered report into the runs of characters a version
// could be spelled with, so the scan sees a version inside a code span and
// inside a table cell alike.
var tokens = regexp.MustCompile(`[0-9A-Za-z.\-+]+`)

type printedVersion struct {
	value string
	where string
}

// reportWithEveryVersionShape renders one DiffResult carrying a version
// everywhere Report prints one, and returns the report with the versions in it.
//
// Every value is distinct, so a failure names the section that broke rather
// than only saying that something did.
func reportWithEveryVersionShape(t *testing.T) (string, []printedVersion) {
	t.Helper()

	d := &DiffResult{
		Targeting: []Change{{
			Kind: ChangeAdded, App: "kyverno", Cluster: "prod",
			From: "1.10.0", To: "1.11.0",
		}},
		Introduced: []Change{{
			Kind: ChangeIntroduced, App: "newthing", Cluster: "prod", To: "2.1.0",
		}},
		Versions: []Change{{
			Kind: ChangeVersion, App: "cert-manager", Cluster: "prod",
			From: "v1.14.4", To: "v1.15.0",
		}},
		Other: []Change{{
			Kind: ChangeSource, App: "external-secrets", Cluster: "prod",
			From: "0.9.11", To: "0.10.0",
		}},
		Objects: []ObjectChange{
			{
				Kind: ObjectAPIVersionMoved, Object: "Certificate/example",
				From: "cert-manager.io/v1alpha2", To: "cert-manager.io/v1",
			},
			{
				Kind: ObjectCRDVersionRemoved, Object: "certificates.cert-manager.io",
				Resource: "Certificate", From: "v1alpha2", To: "v1",
				ConsumersKnown: true, ConsumerFiles: []string{"addons/cert.yaml"},
			},
			{
				Kind: ObjectValuesKeyDropped, Object: "kyverno",
				From: "3.1.4", To: "3.2.0", Keys: []string{"replicaCount"},
			},
			{
				Kind: ObjectRenderFailed, Object: "bosun",
				From: "0.20.0", To: "0.25.1",
				Reason: "values don't meet the specifications of the schema",
			},
			{
				Kind: ObjectChanged, Object: "Deployment/kyverno", ValuesChecked: true,
				Fields: []FieldChange{
					{Path: "spec.template.spec.containers[0].image", From: "kyverno:1.10.0", To: "kyverno:1.11.0"},
					{Path: "spec.replicas", To: "3.0.1"},
				},
			},
		},
	}

	var b strings.Builder
	d.Report(&b)
	report := b.String()

	// Named per section, so a failure says which one stopped matching. The
	// image tag values above are deliberately not listed: `kyverno:1.10.0` is
	// not a version-shaped token on its own, and the tokeniser splits it at
	// the colon into a word and a version that IS printed elsewhere.
	versions := []printedVersion{
		{"1.10.0", "the targeting table's from"},
		{"1.11.0", "the targeting table's to"},
		{"2.1.0", "the new-addon table"},
		{"v1.14.4", "the version table's from"},
		{"v1.15.0", "the version table's to"},
		{"0.9.11", "the source-change table's from"},
		{"0.10.0", "the source-change table's to"},
		{"3.1.4", "a values-key-dropped finding's from"},
		{"3.2.0", "a values-key-dropped finding's to"},
		{"0.20.0", "an unrenderable chart's base version"},
		{"0.25.1", "an unrenderable chart's head version"},
		{"3.0.1", "a field-level change"},
	}

	// The self-check. A fixture that stopped producing a report would pass
	// every assertion above by having nothing to assert.
	if len(report) < 200 {
		t.Fatalf("the fixture rendered a %d-byte report; DiffResult's shape has changed "+
			"and this fixture no longer exercises Report", len(report))
	}
	for _, v := range versions {
		if !strings.Contains(report, v.value) {
			t.Fatalf("the fixture claims the report prints %q in %s and it does not.\n"+
				"Report stopped printing that section, or prints it differently. Fix the "+
				"fixture to match what Report does now -- but check first that the change "+
				"to Report was intended, because the applier reads these by substring.\n\n%s",
				v.value, v.where, report)
		}
	}
	return report, versions
}

func writeValues(t *testing.T, root, version string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "values.yaml"),
		[]byte("version: "+version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
