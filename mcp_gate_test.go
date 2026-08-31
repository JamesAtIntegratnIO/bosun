package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
)

// The crossing from the gate's verdict into the tool surface's vocabulary.
//
// It is a contract nothing else can see. `mcp` imports the result types and
// the redactor and nothing else, which is what makes "no field path from a
// tool result reaches a credential" a compile-time rule rather than a filter;
// the cost is that mcp cannot import gate, so the two shapes are copies and
// the copying happens here. A field this file forgets is a field that is
// simply missing from every answer, with no compiler and no test in either
// package to notice.
//
// Both halves are derived: the verdict comes out of a real DiffResult through
// the real Summarise, and the assertion walks the findings rather than naming
// them.

// verdictWithOneOfEverything is a DiffResult carrying one finding of each kind
// the breakdown counts.
//
// Built here rather than borrowed from gate's own fixtures because it has to
// cross a package boundary, and a shared fixture would make this test agree
// with gate about a shape neither of them publishes.
func verdictWithOneOfEverything() *gate.DiffResult {
	return &gate.DiffResult{
		BaseRev: "1a2b3c4d", HeadRev: "9f2c1a4b",
		Targeting: []gate.Change{{
			Kind: gate.ChangeMoved, App: "external-secrets", AppSet: "external-secrets",
			From: "prod-a", To: "prod-b", Detail: "ApplicationSet no longer generates for prod-a",
		}},
		Other: []gate.Change{{
			Kind: gate.ChangeSource, App: "argo-cd", Cluster: "prod-eu",
			From: "chart argo-cd 5.0.0", To: "path addons/argo-cd",
			Detail: "the source itself changed, not just its version",
		}},
		Objects: []gate.ObjectChange{{
			Kind: gate.ObjectRenderFailed, Object: "authentik", Cluster: "prod-eu",
			From: "2024.2.0", To: "2024.6.0",
			Reason: "execution error at (authentik/templates/server.yaml:12)",
		}, {
			Kind:   gate.ObjectCRDVersionRemoved,
			Object: "CustomResourceDefinition/externalsecrets.external-secrets.io",
			From:   "v1beta1", To: "v1", Resource: "ExternalSecret",
			ConsumersKnown: true,
			ConsumerFiles:  []string{"addons/argo-cd/externalsecret.yaml"},
		}, {
			Kind: gate.ObjectAPIVersionMoved, Object: "Ingress/authentik in authentik",
			From: "networking.k8s.io/v1beta1", To: "networking.k8s.io/v1",
		}, {
			Kind: gate.ObjectValuesKeyDropped, Object: "grafana",
			From: "7.3.0", To: "8.0.0", Keys: []string{"grafana.ini.auth.oauth_auto_login"},
		}},
		Schema: []gate.SchemaFailure{{
			Source: "addons/harbor on prod-eu", Kind: "Deployment", Name: "harbor-core",
			Message: "spec.replicas: got string, want integer",
		}},
		Warnings: []gate.Markdown{
			"kube-prometheus-stack: renders at 62.0.0 but not at 61.3.2, so its resource " +
				"changes are NOT covered",
		},
	}
}

// Nothing is lost crossing from the gate's verdict into the tool surface's.
func TestTheVerdictCrossesIntoTheToolSurfaceWhole(t *testing.T) {
	summary := verdictWithOneOfEverything().Summarise()
	got := mcpVerdict(summary)
	if got == nil {
		t.Fatal("a verdict crossed as nothing")
	}

	if got.Blocking != summary.Blocking || got.Headline != summary.Headline {
		t.Errorf("the verdict's own answer did not cross: blocking=%v headline=%q",
			got.Blocking, got.Headline)
	}
	if got.BaseRev != summary.BaseRev || got.HeadRev != summary.HeadRev {
		t.Errorf("the two revisions this is the difference between did not cross: %q..%q",
			got.BaseRev, got.HeadRev)
	}
	if len(got.NotCovered) != len(summary.NotCovered) {
		t.Errorf("what the gate could not render did not cross: %d of %d",
			len(got.NotCovered), len(summary.NotCovered))
	}

	// The breakdown, field by field and derived from the type rather than
	// named here: a ninth bucket that this file forgets to copy would
	// otherwise be a zero on every answer, forever, with nothing to notice it.
	from := reflect.ValueOf(summary.Blockers)
	to := reflect.ValueOf(got.Blockers)
	if from.NumField() != to.NumField() {
		t.Fatalf("the two breakdowns have %d and %d fields; one of them grew a bucket the "+
			"other does not have", from.NumField(), to.NumField())
	}
	for i := 0; i < from.NumField(); i++ {
		name := from.Type().Field(i).Name
		mirror := to.FieldByName(name)
		if !mirror.IsValid() {
			t.Errorf("the tool surface's breakdown has no %s", name)
			continue
		}
		if from.Field(i).Int() != mirror.Int() {
			t.Errorf("%s crossed as %d, not %d", name, mirror.Int(), from.Field(i).Int())
		}
	}

	if len(got.Findings) != len(summary.Findings) {
		t.Fatalf("%d findings crossed as %d", len(summary.Findings), len(got.Findings))
	}

	// Field by field, off the gate's own type rather than off a list here.
	//
	// Named, this assertion is satisfied by a copy that transposes two same-typed
	// fields: From and To are both strings, and swapping them compiles,
	// publishes, and describes every version move backwards. Walked, the two
	// shapes have to agree on the name as well as the value.
	//
	// Dropped is compared by presence rather than by value, because the two
	// types differ on purpose -- gate speaks the repair contract's vocabulary
	// (CRD, Kind, Target), the tool surface spells it out for a reader -- and
	// TestTheMigrationCrossesFieldForField is what checks that mapping.
	computed := map[string]bool{"RepositorySideRemedy": true}
	compared := 0
	for i, want := range summary.Findings {
		f := got.Findings[i]
		from := reflect.ValueOf(want)
		to := reflect.ValueOf(f)

		for j := 0; j < from.NumField(); j++ {
			name := from.Type().Field(j).Name
			mirror := to.FieldByName(name)
			if !mirror.IsValid() {
				t.Errorf("gate.Finding.%s has no counterpart on the tool surface, so it "+
					"cannot cross at all", name)
				continue
			}
			compared++
			switch name {
			case "Dropped":
				if from.Field(j).IsNil() != mirror.IsNil() {
					t.Errorf("finding %d: the migration crossed as present=%v, want %v",
						i, !mirror.IsNil(), !from.Field(j).IsNil())
				}
			case "Kind":
				if mirror.String() != from.Field(j).String() {
					t.Errorf("finding %d crossed as kind %q, not %q",
						i, mirror.String(), from.Field(j).String())
				}
			default:
				if !reflect.DeepEqual(from.Field(j).Interface(), mirror.Interface()) {
					t.Errorf("finding %d (%s): %s crossed as %#v, not %#v",
						i, want.Kind, name, mirror.Interface(), from.Field(j).Interface())
				}
			}
		}

		// The one field the crossing computes rather than copies, because it
		// is a property of the class rather than of the finding.
		if f.RepositorySideRemedy != want.Kind.RepositorySideRemedy() {
			t.Errorf("finding %d (%s): repositorySideRemedy crossed as %v",
				i, want.Kind, f.RepositorySideRemedy)
		}
	}

	// And nothing on the published side is filled by nobody: a field added to
	// the tool surface that the crossing never writes is a zero on every
	// answer, forever.
	published := reflect.TypeOf(mcp.GateFinding{})
	source := reflect.TypeOf(gate.Finding{})
	for i := 0; i < published.NumField(); i++ {
		name := published.Field(i).Name
		if computed[name] {
			continue
		}
		if _, ok := source.FieldByName(name); !ok {
			t.Errorf("mcp.GateFinding.%s comes from no field of gate.Finding and is not one "+
				"of the fields the crossing computes, so nothing fills it", name)
		}
	}

	// The self-check, and not optional: a walk that compared nothing would
	// report a clean crossing over findings it never read.
	if compared < 12*len(summary.Findings) {
		t.Fatalf("compared only %d fields across %d findings; gate.Finding is shaped "+
			"differently now and this walk no longer reads it", compared, len(summary.Findings))
	}
}

// And the migration crosses field for field, in the right order.
//
// Four strings of the same type in one struct literal, which is the shape a
// transposition hides in: crd, group, kind and target all cross as strings,
// and swapping two of them compiles, publishes, and tells a repair to move the
// wrong manifests to the wrong version.
func TestTheMigrationCrossesFieldForField(t *testing.T) {
	summary := verdictWithOneOfEverything().Summarise()
	got := mcpVerdict(summary)

	var found int
	for _, f := range got.Findings {
		if f.Dropped == nil {
			continue
		}
		found++
		d := f.Dropped
		if d.Definition != "externalsecrets.external-secrets.io" {
			t.Errorf("definition crossed as %q", d.Definition)
		}
		if d.Group != "external-secrets.io" {
			t.Errorf("group crossed as %q", d.Group)
		}
		if d.ConsumerKind != "ExternalSecret" {
			t.Errorf("the kind consuming manifests declare crossed as %q", d.ConsumerKind)
		}
		if d.Surviving != "v1" {
			t.Errorf("the surviving version crossed as %q", d.Surviving)
		}
		if len(d.Versions) != 1 || d.Versions[0] != "v1beta1" {
			t.Errorf("the versions that are gone crossed as %v", d.Versions)
		}
	}
	if found != 1 {
		t.Fatalf("%d migrations crossed, want 1; the assertions above ran against nothing", found)
	}
}

// A gate that has reached no verdict crosses as no verdict, rather than as an
// empty one.
//
// The distinction the whole surface rests on, at the one place it could be
// erased by a helpful nil check: an empty GateVerdict would publish an
// all-zero breakdown and an empty findings list, which is exactly what a green
// pull request looks like.
func TestNoVerdictCrossesAsNoVerdict(t *testing.T) {
	if got := mcpVerdict(nil); got != nil {
		t.Fatalf("nothing crossed as %+v, which a client reads as a clean verdict", got)
	}
}

// Every state the gate's snapshot can be in reaches the tool surface spelled
// the same way.
//
// Two vocabularies for one set of states is how a client ends up with a
// default branch that swallows "error". Both sides are read from their own
// packages' syntax trees rather than listed here: a sixth state added to
// gateservice is one this test starts checking the same day, which a written
// list is not.
func TestTheStateVocabularyIsOneVocabulary(t *testing.T) {
	root := helmtest.Root(t)
	gateStates := stateConstants(t, filepath.Join(root, "gateservice", "status.go"))
	toolStates := stateConstants(t, filepath.Join(root, "mcp", "verdict.go"))

	published := map[string]string{}
	for name, value := range toolStates {
		published[value] = name
	}
	for name, value := range gateStates {
		if _, ok := published[value]; !ok {
			t.Errorf("gateservice.%s publishes the state %q and the tool surface has no "+
				"constant with that value.\n"+
				"A client branching on the documented set falls through, and the fall-through "+
				"is silent: the word reaches the wire either way.", name, value)
		}
	}

	// And the other direction, which is the one that says what this surface
	// added rather than what it forgot. Every extra has to mean something the
	// gate's own snapshot cannot express, because a state nothing produces is
	// a branch a client writes and never reaches.
	extra := map[string]bool{}
	for _, value := range toolStates {
		var fromTheGate bool
		for _, g := range gateStates {
			fromTheGate = fromTheGate || g == value
		}
		if !fromTheGate {
			extra[value] = true
		}
	}
	for _, want := range []string{mcp.StateAbsent, mcp.StateUnswept} {
		if !extra[want] {
			t.Errorf("%q is no longer a state the tool surface adds; a pull request the "+
				"gate's snapshot does not contain has to be answerable as something, and "+
				"reusing one of the gate's own words for it would be a lie", want)
		}
		delete(extra, want)
	}
	for value := range extra {
		t.Errorf("the tool surface publishes the state %q, which no gate sweep produces and "+
			"which is not one of the two this surface adds on purpose. A client cannot "+
			"branch on a state nothing reaches.", value)
	}

	// The self-check, and not optional: two walks that found nothing would
	// compare two empty sets and report agreement.
	if len(gateStates) < 5 || len(toolStates) < 7 {
		t.Fatalf("found %d gate states and %d tool states; they are written differently now "+
			"and these walks no longer read them. Fix the walks rather than lowering these "+
			"numbers.", len(gateStates), len(toolStates))
	}
}

// stateConstants reads every `State*` string constant in one file.
//
// By name and by value, because the two questions this test asks are different
// ones: whether a word reaches both sides, and whether a word one side
// publishes is a word anything produces.
func stateConstants(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", path, err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "State") || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				out[name.Name] = value
			}
		}
	}
	return out
}
