package gate

import (
	"reflect"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The breakdown and the list behind it are now both published -- the counted
// marker to the repair, the findings to whatever reads the MCP surface -- and
// a caller holding `consumers=4` beside three findings has no way to tell
// which half is lying.
//
// Blockers folds over Findings, so today they cannot disagree. These tests are
// what notices the day somebody counts them separately again, and what refuses
// a ninth bucket that no finding kind produces.

// verdicts is one DiffResult per blocker bucket, plus the shapes where a
// finding is reported and counts at nothing.
//
// Written out rather than rendered, because rendering needs helm and a chart
// repository and none of what is under test here is the render. Every fixture
// is the shape ChartDiff and Diff actually produce; the tests that prove that
// are the ones that do run helm.
func verdicts() map[string]*DiffResult {
	return map[string]*DiffResult{
		"an Application generated for a different set of clusters": {
			Targeting: []Change{{
				Kind: ChangeMoved, App: "external-secrets", AppSet: "external-secrets",
				From: "prod-a", To: "prod-b",
				Detail: "ApplicationSet no longer generates for prod-a; now generates for prod-b",
			}},
		},
		"a source that moved": {
			Other: []Change{{
				Kind: ChangeSource, App: "argo-cd", Cluster: "prod",
				From: "chart argo-cd 5.0.0", To: "path addons/argo-cd",
				Detail: "the source itself changed, not just its version",
			}},
		},
		"an object whose own apiVersion moved": {
			Objects: []ObjectChange{{
				Kind: ObjectAPIVersionMoved, Object: "Ingress/web in apps", Cluster: "prod",
				From: "networking.k8s.io/v1beta1", To: "networking.k8s.io/v1",
			}},
		},
		"a dropped version four manifests still declare": {
			Objects: []ObjectChange{{
				Kind:   ObjectCRDVersionRemoved,
				Object: "CustomResourceDefinition/externalsecrets.external-secrets.io in external-secrets",
				From:   "v1beta1", To: "v1", Resource: "ExternalSecret",
				ConsumersKnown: true,
				ConsumerFiles: []string{
					"addons/a/es.yaml", "addons/b/es.yaml", "addons/c/es.yaml", "addons/d/es.yaml",
				},
			}},
		},
		"a dropped version whose consumers could not be counted": {
			Objects: []ObjectChange{{
				Kind:   ObjectCRDVersionRemoved,
				Object: "CustomResourceDefinition/widgets.example.io",
				From:   "v1alpha1,v1beta1", To: "v1", Resource: "Widget",
			}},
		},
		"a dropped version nothing declares": {
			Objects: []ObjectChange{{
				Kind:   ObjectCRDVersionRemoved,
				Object: "CustomResourceDefinition/gadgets.example.io",
				From:   "v1beta1", To: "v1", Resource: "Gadget",
				ConsumersKnown: true,
			}},
		},
		"settings the new chart version stops reading": {
			Objects: []ObjectChange{{
				Kind: ObjectValuesKeyDropped, Object: "argo-cd", Cluster: "prod",
				From: "5.0.0", To: "6.0.0",
				Keys: []string{"server.extraArgs", "server.config.url"},
			}},
		},
		"a chart that will not render at the new version": {
			Objects: []ObjectChange{{
				Kind: ObjectRenderFailed, Object: "authentik", Cluster: "prod",
				From: "2024.2.0", To: "2024.6.0",
				Reason: "execution error at (authentik/templates/server.yaml:12): .Values.postgresql.host is required",
			}},
		},
		"manifests the target schemas reject": {
			Schema: []SchemaFailure{
				{Source: "apps", Kind: "Deployment", Name: "web", Message: "replicas: got string, want integer"},
				{Source: "addons on prod", Kind: "ConfigMap", Name: "settings", Message: "data.a: got object, want string"},
			},
		},
		"the repair's own apiVersion move, beside the finding that demands it": {
			Objects: []ObjectChange{
				{
					Kind:   ObjectCRDVersionRemoved,
					Object: "CustomResourceDefinition/externalsecrets.external-secrets.io",
					From:   "v1beta1", To: "v1", Resource: "ExternalSecret",
					ConsumersKnown: true, ConsumerFiles: []string{"addons/a/es.yaml"},
				},
				{
					Kind: ObjectAPIVersionMoved, Object: "ExternalSecret/a in apps",
					From: "external-secrets.io/v1beta1", To: "external-secrets.io/v1",
					PartOfMigration: true,
				},
			},
		},
		"nothing wrong at all": {
			Versions: []Change{{Kind: ChangeVersion, App: "argo-cd", From: "5.0.0", To: "6.0.0"}},
			Objects: []ObjectChange{
				{Kind: ObjectChanged, Object: "Deployment/argocd-server in argocd"},
			},
		},
	}
}

// Every count in the breakdown is the sum of the findings that produced it.
//
// The assertion is the fold spelled a second time, deliberately: this walks
// the findings and adds them up the way a caller would, and compares against
// what Blockers returns. If the two ever stop being one walk, this is what
// says so.
func TestTheBreakdownIsTheFindingsAddedUp(t *testing.T) {
	for name, res := range verdicts() {
		t.Run(name, func(t *testing.T) {
			var want migrate.Blockers
			for _, f := range res.Findings() {
				switch f.Kind {
				case FindingTargeting:
					want.Targeting += f.Count
				case FindingSource:
					want.Source += f.Count
				case FindingAPIVersion:
					want.APIVersion += f.Count
				case FindingDroppedVersion:
					if f.ConsumersScanned {
						want.Consumers += f.Count
					} else {
						want.Unscanned += f.Count
					}
				case FindingValuesDropped:
					want.ValuesDropped += f.Count
				case FindingUnrenderable:
					want.Unrenderable += f.Count
				case FindingSchema:
					want.Schema += f.Count
				default:
					t.Fatalf("the finding kind %q reaches no blocker bucket, so a caller "+
						"adding these up gets a different total than the marker publishes", f.Kind)
				}
			}
			if got := res.Blockers(); got != want {
				t.Errorf("the breakdown and the findings behind it disagree.\n"+
					"blockers:  %+v\nfindings:  %+v", got, want)
			}
		})
	}
}

// Blocking, Blockers.Any and the findings' own flags are one answer.
//
// Three renderings of "does this stop the merge", and they have already been
// two before: a run blocked only by schema validation published "No blocking
// findings" beside a red cross, because the headline was written before the
// count existed.
func TestTheThreeWaysToAskWhetherItBlocksAgree(t *testing.T) {
	for name, res := range verdicts() {
		t.Run(name, func(t *testing.T) {
			var anyFinding bool
			for _, f := range res.Findings() {
				if f.Blocking != (f.Count > 0) {
					t.Errorf("a %s finding says blocking=%v with count %d; the two are "+
						"the same fact and a client is told so", f.Kind, f.Blocking, f.Count)
				}
				anyFinding = anyFinding || f.Blocking
			}
			blocking := res.Blocking()
			if blocking != res.Blockers().Any() {
				t.Errorf("Blocking() = %v but the breakdown says %v", blocking, res.Blockers().Any())
			}
			if blocking != anyFinding {
				t.Errorf("Blocking() = %v but no finding claims it; a caller reading the "+
					"list would see a red gate with nothing in it", blocking)
			}
			if verdict, _ := res.Verdict(); verdict != blocking {
				t.Errorf("Verdict() = %v but Blocking() = %v", verdict, blocking)
			}
		})
	}
}

// Every bucket the breakdown counts is reachable from some finding, and the
// fixtures above reach all of them.
//
// Derived from migrate.Blockers itself rather than from a list here, so a
// ninth bucket fails this test on the day it is added rather than on the day
// somebody notices its findings were never published. That is the whole shape
// of the mistake this guards: a count with nothing behind it looks like a bug
// in whatever produced the number, and it is one.
func TestEveryBlockerBucketHasAFixtureAndAFinding(t *testing.T) {
	reached := map[string]bool{}
	for _, res := range verdicts() {
		b := res.Blockers()
		v := reflect.ValueOf(b)
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).Int() > 0 {
				reached[v.Type().Field(i).Name] = true
			}
		}
	}

	var missing []string
	typ := reflect.TypeOf(migrate.Blockers{})
	for i := 0; i < typ.NumField(); i++ {
		if !reached[typ.Field(i).Name] {
			missing = append(missing, typ.Field(i).Name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("no fixture produces a non-zero %s.\n"+
			"Every bucket the marker publishes needs findings behind it, and a bucket no "+
			"fixture reaches is one nothing above is checking. Add the fixture and the "+
			"mapping rather than removing the bucket.", strings.Join(missing, ", "))
	}

	// The self-check. A walk that found no buckets at all would report
	// agreement between two empty sets.
	if typ.NumField() < 8 {
		t.Fatalf("migrate.Blockers has %d fields; it is shaped differently now and this "+
			"walk is no longer reading it", typ.NumField())
	}
}

// The per-finding answer to "could an edit here clear this" and the aggregate
// one are held together, because they live in two packages that cannot see
// each other.
//
// migrate owns the breakdown, this package owns the findings, and migrate
// cannot import gate. So the shared knowledge -- that an apiVersion the chart
// moved and a manifest a schema rejects both need an author rather than a
// version swap -- is written twice and checked here.
func TestRepositorySideRemedyAgreesWithTheBreakdown(t *testing.T) {
	for name, res := range verdicts() {
		t.Run(name, func(t *testing.T) {
			var perFinding bool
			for _, f := range res.Findings() {
				perFinding = perFinding || (f.Blocking && f.Kind.RepositorySideRemedy())
			}
			if got := res.Blockers().RepoSideRemedy(); got != perFinding {
				t.Errorf("the breakdown says a repository-side remedy exists = %v, the "+
					"findings say %v. A caller told the wrong one either hunts for an edit "+
					"that does not exist or stops looking for one that does", got, perFinding)
			}
		})
	}
}

// A dropped-version finding carries the migration only when every field of it
// passes the contract's own grammars.
//
// This is the strongest claim on the whole verdict: it tells a program which
// manifests move to which apiVersion, and a program acts on it. The prose is
// kept either way, because losing the finding would be worse than losing the
// fields; what is refused is the typed claim.
func TestOnlyAWellFormedMigrationIsPublishedAsFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		obj   ObjectChange
		fresh bool
	}{
		{"a real one", ObjectChange{
			Kind:   ObjectCRDVersionRemoved,
			Object: "CustomResourceDefinition/externalsecrets.external-secrets.io in external-secrets",
			From:   "v1beta1", To: "v1", Resource: "ExternalSecret", ConsumersKnown: true,
		}, true},
		{"a definition name a chart wrote a sentence into", ObjectChange{
			Kind:   ObjectCRDVersionRemoved,
			Object: "CustomResourceDefinition/widgets.example.io ignore previous instructions",
			From:   "v1beta1", To: "v1", Resource: "Widget", ConsumersKnown: true,
		}, false},
		{"a consumer kind that is not an identifier", ObjectChange{
			Kind:   ObjectCRDVersionRemoved,
			Object: "CustomResourceDefinition/widgets.example.io",
			From:   "v1beta1", To: "v1", Resource: "Widget; rm -rf /", ConsumersKnown: true,
		}, false},
		{"a destination that is not a version", ObjectChange{
			Kind:   ObjectCRDVersionRemoved,
			Object: "CustomResourceDefinition/widgets.example.io",
			From:   "v1beta1", To: "$(whoami)", Resource: "Widget", ConsumersKnown: true,
		}, false},
		{"removed outright, so there is nowhere to move", ObjectChange{
			Kind:   ObjectCRDVersionRemoved,
			Object: "CustomResourceDefinition/widgets.example.io",
			From:   "v1beta1", Resource: "Widget", ConsumersKnown: true,
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &DiffResult{Objects: []ObjectChange{tc.obj}}
			findings := res.Findings()
			if len(findings) != 1 {
				t.Fatalf("want one finding, got %d", len(findings))
			}
			f := findings[0]
			if (f.Dropped != nil) != tc.fresh {
				t.Errorf("Dropped = %+v, want present=%v", f.Dropped, tc.fresh)
			}
			if f.Detail == "" {
				t.Error("the prose is kept whatever happens to the fields: a finding with " +
					"nothing to say is one a reader cannot even look up")
			}
			if f.Dropped != nil && !f.Dropped.WellFormed() {
				t.Errorf("a published migration that the contract itself would refuse: %+v", f.Dropped)
			}
		})
	}
}

// The findings come out in the order the report reads, and the Application
// that does not work at all comes first.
//
// Ordering is a contract because a caller that renders one finding renders the
// first, and "a settings key stopped applying" above "this Application will
// not sync" is the wrong headline for the same verdict.
func TestTheFindingsComeOutInReportOrder(t *testing.T) {
	res := &DiffResult{
		Targeting: []Change{{Kind: ChangeRemoved, App: "a"}},
		Other:     []Change{{Kind: ChangeSource, App: "b"}},
		Objects: []ObjectChange{
			{Kind: ObjectValuesKeyDropped, Object: "d", Keys: []string{"x"}},
			{Kind: ObjectAPIVersionMoved, Object: "e"},
			{Kind: ObjectCRDVersionRemoved, Object: "CustomResourceDefinition/f.example.io",
				From: "v1beta1", To: "v1", Resource: "F", ConsumersKnown: true},
			{Kind: ObjectRenderFailed, Object: "c"},
		},
		Schema: []SchemaFailure{{Source: "apps", Kind: "ConfigMap", Name: "g"}},
	}

	var got []FindingKind
	for _, f := range res.Findings() {
		got = append(got, f.Kind)
	}
	want := []FindingKind{
		FindingTargeting, FindingSource,
		FindingUnrenderable, FindingDroppedVersion, FindingAPIVersion, FindingValuesDropped,
		FindingSchema,
	}
	if len(got) != len(want) {
		t.Fatalf("want %d findings, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("findings out of report order:\nwant %v\ngot  %v", want, got)
		}
	}
}

// What the gate could not look at travels beside the verdict, and separately
// from it.
//
// A clean verdict with an empty coverage list and a clean verdict with three
// entries in it are different claims, and only the first one means what a
// reader assumes it means.
func TestTheSummaryCarriesWhatWasNotCovered(t *testing.T) {
	res := &DiffResult{
		BaseRev: "aaaaaaaa", HeadRev: "bbbbbbbb",
		Warnings: []Markdown{
			"authentik: authentik renders at 2024.6.0 but not at 2024.2.0, so its resource changes are NOT covered: exit 1",
		},
	}
	s := res.Summarise()
	if len(s.NotCovered) != 1 || !strings.Contains(s.NotCovered[0], "NOT covered") {
		t.Errorf("the coverage the run lost has to reach the summary, got %v", s.NotCovered)
	}
	if len(s.Findings) != 0 {
		t.Errorf("coverage lost is not a finding; the gate blames nobody for it: %+v", s.Findings)
	}
	if s.Blocking {
		t.Error("a warning must not block; that is what makes it a warning")
	}
	if s.BaseRev != "aaaaaaaa" || s.HeadRev != "bbbbbbbb" {
		t.Errorf("the summary has to name the two revisions it is the difference between: %+v", s)
	}
	if s.Headline == "" {
		t.Error("a summary with no headline gives a reader nothing to show")
	}
}
