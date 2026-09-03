package mcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// inventory is the fleet as the last live reading saw it. Its whole difficulty
// is that "nothing runs here" and "nothing has looked" are the same empty list
// unless the surface keeps them apart.

func TestTheInventoryIsEveryApplicationTheLiveReadingSaw(t *testing.T) {
	f := newFixture(t, nil).withGate(fleet())
	inv := f.inventory(t)

	if inv.Repository != "example/platform" || !inv.Swept {
		t.Fatalf("every result names its repository and its sweep; got %+v", inv)
	}
	if inv.Applications == nil {
		t.Fatal("a live reading that ran publishes its rows, even to say there were none")
	}
	apps := *inv.Applications
	if len(apps) != 4 {
		t.Fatalf("the live reading served four Applications; the inventory holds %d", len(apps))
	}
	first := apps[0]
	if first.Name.Text != "argo-cd" {
		t.Errorf("the row names the Application ArgoCD serves, got %+v", first.Name)
	}
	if first.Cluster == nil || first.Cluster.Text != "prod-eu" {
		t.Errorf("a row says which cluster the Application lands on, got %+v", first.Cluster)
	}
}

// Before the first sweep there is no fleet at all, and no claim about charts
// either.
//
// The single most expensive mistake this surface can make is an empty list
// read as "nothing runs here" from a process that has not looked, so the field
// is absent rather than empty and the sentence says which.
func TestBeforeTheFirstSweepThereIsNoFleetField(t *testing.T) {
	f := newFixture(t, nil)
	raw := f.call(t, "inventory")

	got := fields(t, raw)
	for _, absent := range []string{"applications", "chartDetail", "sweptAt", "ageSeconds"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%q is present before any sweep: %s", absent, raw)
		}
	}

	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Swept {
		t.Error("no sweep has completed, so nothing may claim one has")
	}
	if inv.Status.Origin != OriginBosun {
		t.Errorf("the sentence before the first sweep is bosun's own, got %+v", inv.Status)
	}
	if inv.Repository != "example/platform" {
		t.Errorf("a result names its repository even when it has nothing else to say, got %q",
			inv.Repository)
	}
}

// A sweep that completed without any gate run reading the fleet is its own
// answer, and it is not an empty fleet.
//
// The common shape on a quiet install: the reading is made when the gate
// renders a pull request, so a week with none open leaves this process holding
// a sweep and no fleet. Publishing zero rows for it would say ArgoCD serves
// nothing, which is the one thing nothing here has checked.
func TestASweptInstallThatHasReadNoFleetSaysSo(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked())
	raw := f.call(t, "inventory")

	if _, ok := fields(t, raw)["applications"]; ok {
		t.Errorf("a sweep that read no fleet must publish no rows: %s", raw)
	}
	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if !inv.Swept {
		t.Error("a sweep completed, and the result has to say so or this reads as a dead process")
	}
	if inv.ChartDetail != nil {
		t.Errorf("with no rows there is nothing for a chart-detail claim to be about, got %+v",
			inv.ChartDetail)
	}
	if !strings.Contains(inv.Status.Text, "not an empty fleet") {
		t.Errorf("the sentence has to deny the reading a client will otherwise take, got %q",
			inv.Status.Text)
	}
}

// An ArgoCD that serves no Application at all is an EMPTY fleet rather than no
// fleet, and the two are distinguishable at the wire.
func TestAReadingThatServedNothingPublishesAnEmptyFleet(t *testing.T) {
	g := blocked()
	g.Fleet = &GateFleet{ObservedAt: observedAt}
	raw := newFixture(t, nil).withGate(g).call(t, "inventory")

	if _, ok := fields(t, raw)["applications"]; !ok {
		t.Fatal("a reading that was made and served nothing must publish an empty list: " +
			"absence is what says nothing has read, and something did")
	}
	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Applications == nil || len(*inv.Applications) != 0 {
		t.Fatalf("want an empty fleet, got %+v", inv.Applications)
	}
}

// Rows with no chart detail say the expansion has not run, rather than leaving
// a reader to read unpinned charts out of a missing key.
func TestRowsWithoutChartDetailSayTheExpansionHasNotRun(t *testing.T) {
	inv := newFixture(t, nil).withGate(fleet()).inventory(t)

	if inv.ChartDetail == nil {
		t.Fatal("rows without chart detail must say why, or a reader cannot tell an " +
			"expansion that has not run from a fleet of unpinned charts")
	}
	if inv.ChartDetail.Expanded {
		t.Error("no row carries a chart, and the flag has to agree with the rows")
	}
	if inv.ChartDetail.Status.Origin != OriginBosun {
		t.Errorf("the explanation is bosun's own sentence, got %+v", inv.ChartDetail.Status)
	}
	if !strings.Contains(inv.ChartDetail.Status.Text, "not unpinned") {
		t.Errorf("the sentence has to deny the reading a client will otherwise take, got %q",
			inv.ChartDetail.Status.Text)
	}
}

// A row says where its names were read, and the answer is not a guess.
//
// An Application name the live reading served reached an apiserver, so it is
// an RFC1123 name; one the gate's expansion produced came out of `helm
// template`, which applies nothing, so it is whatever the template wrote. The
// two are indistinguishable in the bytes, so the origin has to follow the
// row's provenance rather than the mapping site that happened to build it.
func TestAFleetRowSaysWhereItsNamesWereRead(t *testing.T) {
	inv := newFixture(t, nil).withGate(fleet()).inventory(t)
	row := (*inv.Applications)[0]

	if row.ObservedIn != ObservedLive {
		t.Fatalf("this row came from the live reading and has to say so, got %q", row.ObservedIn)
	}
	for name, text := range map[string]*Text{
		"name":      &row.Name,
		"namespace": row.Namespace,
		"cluster":   row.Cluster,
	} {
		if text == nil {
			t.Fatalf("the fixture published no %s, so this assertion read nothing", name)
		}
		if text.Origin != OriginCluster {
			t.Errorf("%s came off an apiserver and is tagged %q", name, text.Origin)
		}
	}

	// And the other direction, asserted on the mapping rather than through the
	// handler, which is a deliberate exception to this package's seam rule.
	// Nothing published by this build carries a row from anywhere but the live
	// reading, so there is no response body the claim can be read off; and an
	// untested default is exactly how "these two are not distinguished by
	// luck" becomes untrue the day a second reading arrives.
	if got := originOf("expansion"); got != OriginChart {
		t.Errorf("a name that did not reach an apiserver must carry the chart origin, got %q", got)
	}
}

// A row is stamped with when it was observed, and how long ago.
//
// Two clocks and not one: the reading is made by a gate RUN, so on an install
// with nothing open it is older than the sweep, and a caller that read the
// sweep's age as the fleet's would be trusting a number about something else.
func TestEachFleetRowCarriesItsOwnObservationTime(t *testing.T) {
	inv := newFixture(t, nil).withGate(fleet()).inventory(t)

	if inv.AgeSeconds == nil || *inv.AgeSeconds != 90 {
		t.Fatalf("the result carries the sweep's age, got %v", inv.AgeSeconds)
	}
	for _, row := range *inv.Applications {
		if !row.ObservedAt.Equal(observedAt) {
			t.Errorf("%s was observed at %s, want %s", row.Name.Text, row.ObservedAt, observedAt)
		}
		if row.ObservedAgeSeconds != 390 {
			t.Errorf("%s was read %d seconds ago; the reading is five minutes older than the "+
				"sweep and a row that reported the sweep's age would hide that",
				row.Name.Text, row.ObservedAgeSeconds)
		}
	}
}

// Names and clusters only: the result type cannot express a manifest.
//
// inventory is the tool most able to become a manifest proxy by accretion --
// a chart name, then the values that configure it, then the objects it
// renders, each one a small step from the last. So the line is drawn on the
// TYPE rather than on what a handler happens to fill in: a manifest, a values
// file and a rendered object are all documents, and a type with no map, no
// interface and no raw bytes anywhere in it has nowhere to put one.
//
// Derived from the registered tool rather than from Fleet by name, so the
// check follows the tool if the result type is ever replaced.
//
// What it does NOT prove, stated because a guard read as wider than it is is
// worse than none: a Text field could still be filled with a rendered
// manifest, because a document serialised into a string is a string. This
// bans the SHAPES a document travels in unflattened, which is what accretion
// actually looks like -- a chart name, then the values under it, then the
// objects those render. A field that deliberately stuffed YAML into a name is
// a different failure, and the reviewer reading the diff is what catches it.
func TestTheInventoryResultTypeCannotCarryADocument(t *testing.T) {
	var result any
	for _, tool := range Tools() {
		if tool.Name == "inventory" {
			result = tool.Result
		}
	}
	if result == nil {
		t.Fatal("no tool named inventory is registered, so this walk has nothing to read")
	}

	// The leaves a row may be built from. Everything here is a scalar or a
	// tagged string; none of them can hold a document.
	allowed := map[reflect.Type]bool{
		reflect.TypeOf(Text{}):      true,
		reflect.TypeOf(time.Time{}): true,
	}

	fields := 0
	var walk func(typ reflect.Type, path string)
	walk = func(typ reflect.Type, path string) {
		if allowed[typ] {
			return
		}
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice:
			walk(typ.Elem(), path+"[]")
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int64:
			// A name, a flag, a count. Nothing document-shaped.
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				fields++
				walk(f.Type, path+"."+f.Name)
			}
		default:
			t.Errorf("%s is a %s.\n"+
				"This result carries names, clusters and versions and nothing else. A map, an "+
				"interface or a byte slice is somewhere a manifest, a values file or a rendered "+
				"object can be put, and the whole point of this type's shape is that there is "+
				"nowhere.", path, typ.Kind())
		}
	}
	walk(reflect.TypeOf(result), "inventory")

	// The self-check, and not optional: a walk that descended into nothing
	// would report a clean pass over a type it never read.
	if fields < 10 {
		t.Fatalf("this walk saw only %d fields; the result is shaped differently now and it "+
			"is no longer reading it. Fix the walk rather than lowering this number.", fields)
	}
}

// A reading held while the first sweep is still running publishes its rows.
//
// The sweep stamps itself only once every pull request it started has been
// answered, and a run makes its reading in the middle of that -- so this is
// not a corner, it is every install's first minute, and the triage path can
// reach a run before any sweep finishes at all. Gating the rows on the sweep
// had this answer "nothing has read what this fleet runs" while holding the
// reading, which is the one claim this tool exists to make impossible.
func TestAReadingHeldBeforeTheFirstSweepIsStillPublished(t *testing.T) {
	f := newFixture(t, nil).withGate(GateStatus{
		Fleet: &GateFleet{ObservedAt: observedAt, Apps: []GateFleetApp{
			{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"}}},
	})
	raw := f.call(t, "inventory")

	var inv Fleet
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	if inv.Applications == nil || len(*inv.Applications) != 1 {
		t.Fatalf("a reading this process holds must be published whatever the sweep has "+
			"done: %s", raw)
	}
	// And the sweep is still reported honestly, because the two are separate
	// claims: no sweep has finished, and a reading exists.
	if inv.Swept {
		t.Error("no sweep has completed, so nothing may claim one has")
	}
	if _, ok := fields(t, raw)["sweptAt"]; ok {
		t.Errorf("a sweep time is published for a sweep that has not finished: %s", raw)
	}
	if inv.Status.Text != fleetRead {
		t.Errorf("the sentence must describe the reading rather than the sweep, got %q",
			inv.Status.Text)
	}
}

// The join, which is the substance of this tool's second half: the live
// reading decides which rows exist, and the expansion says what they render
// from.

func TestARowSaysWhatItRendersFrom(t *testing.T) {
	inv := newFixture(t, nil).withGate(expanded()).inventory(t)

	rows := map[string]FleetApp{}
	for _, row := range *inv.Applications {
		rows[row.Name.Text] = row
	}
	argo, ok := rows["argo-cd"]
	if !ok {
		t.Fatal("the fixture published no argo-cd row, so this test read nothing")
	}
	if argo.Renders == nil {
		t.Fatal("the expansion knows what argo-cd renders and the row does not say so, which " +
			"is the whole of this ticket")
	}
	got := argo.Renders
	if got.SourceType != RenderHelm {
		t.Errorf("argo-cd renders a chart and the row says %q", got.SourceType)
	}
	for name, want := range map[string]struct {
		text *Text
		want string
	}{
		"chart":           {got.Chart, "argo-cd"},
		"chartRepository": {got.ChartRepository, "https://argoproj.github.io/argo-helm"},
		"version":         {got.Version, "7.7.0"},
		"applicationSet":  {got.ApplicationSet, "addons"},
	} {
		if want.text == nil {
			t.Errorf("%s is absent, and the expansion knows it", name)
			continue
		}
		if want.text.Text != want.want {
			t.Errorf("%s is %q, want %q", name, want.text.Text, want.want)
		}
		// Every one of these came out of `helm template`, which applies
		// nothing, so `metadata.name` is whatever the template wrote. The
		// chart origin is the weakest true thing to say about them.
		if want.text.Origin != OriginChart {
			t.Errorf("%s is tagged %q; nothing in the expansion reached an apiserver",
				name, want.text.Origin)
		}
	}

	// And a source that is not a chart says so with no chart beside it, rather
	// than with an empty one.
	obs, ok := rows["observability"]
	if !ok || obs.Renders == nil {
		t.Fatalf("the fixture published no expanded path source, so half the shape a client "+
			"has to handle went unchecked: %+v", obs)
	}
	if obs.Renders.SourceType != RenderPath {
		t.Errorf("observability renders a directory and the row says %q", obs.Renders.SourceType)
	}
	if obs.Renders.Chart != nil || obs.Renders.Version != nil {
		t.Errorf("a source with no chart published one: %+v", obs.Renders)
	}
}

// An Application the live reading has and the expansion does not know carries
// no chart detail, and never somebody else's.
//
// The common case rather than the corner: the reading is every Application the
// install's ArgoCD serves, and the expansion covers only what the gated
// repository defines.
func TestARowTheExpansionDoesNotKnowCarriesNoChartDetail(t *testing.T) {
	inv := newFixture(t, nil).withGate(expanded()).inventory(t)

	var found bool
	for _, row := range *inv.Applications {
		if row.Name.Text != "tenant-billing" {
			continue
		}
		found = true
		if row.Renders != nil {
			t.Errorf("a row the expansion never saw was given chart detail: %+v", row.Renders)
		}
	}
	if !found {
		t.Fatal("the fixture published no row outside the expansion, so nothing above ran")
	}
	// And the row is still there, which is the other half: the reading decides
	// which rows exist and the expansion adds none and removes none.
	if len(*inv.Applications) != 4 {
		t.Errorf("the live reading served four Applications and the result holds %d",
			len(*inv.Applications))
	}
}

// An Application the expansion knows and the live reading does not have is not
// a fleet member at all.
//
// The expansion describes the revision the last run started from, which is a
// claim about what the repository defines rather than about what is running.
// Publishing a row for it would say "this is deployed on prod-eu" about
// something ArgoCD does not serve, and a fleet member that is not there is the
// worse of the two errors.
func TestAnApplicationOnlyTheExpansionKnowsIsNoRowAtAll(t *testing.T) {
	inv := newFixture(t, nil).withGate(expanded()).inventory(t)

	for _, row := range *inv.Applications {
		if row.Name.Text == "loki" {
			t.Fatalf("the expansion added a row the live reading never served: %+v", row)
		}
	}
	// The self-check: an expansion that carried no such Application would
	// leave the assertion above passing over a fixture that never tested it.
	var offered bool
	for _, app := range expanded().Expansion.Apps {
		offered = offered || app.Name == "loki"
	}
	if !offered {
		t.Fatal("the fixture's expansion knows of no Application the reading lacks, so " +
			"nothing above was checked")
	}
}

// A row whose identity and chart detail came from different observations
// reports both, with their own stamps.
//
// Two clocks on one row, which is what makes "most recent and relevant"
// something a client reads instead of a merge rule it has to trust. The
// identity is the live reading's; the chart detail is a render that may be
// hours older, and a row that published one time for both would be claiming
// the fresher one for the staler half.
func TestARowsIdentityAndItsChartDetailCarryTheirOwnStamps(t *testing.T) {
	inv := newFixture(t, nil).withGate(expanded()).inventory(t)

	var checked int
	for _, row := range *inv.Applications {
		if row.ObservedIn != ObservedLive || !row.ObservedAt.Equal(observedAt) {
			t.Errorf("%s says its identity came from %q at %s", row.Name.Text,
				row.ObservedIn, row.ObservedAt)
		}
		if row.Renders == nil {
			continue
		}
		checked++
		if row.Renders.ObservedIn != ObservedExpansion {
			t.Errorf("%s says its chart detail came from %q", row.Name.Text,
				row.Renders.ObservedIn)
		}
		if !row.Renders.ObservedAt.Equal(expandedAt) {
			t.Errorf("%s stamps its chart detail %s, want %s", row.Name.Text,
				row.Renders.ObservedAt, expandedAt)
		}
		if row.Renders.ObservedAgeSeconds != 990 || row.ObservedAgeSeconds != 390 {
			t.Errorf("%s reports ages %d and %d; the two halves of this row were observed "+
				"ten minutes apart and a client reading one number would trust the fresher "+
				"one for the staler half", row.Name.Text,
				row.ObservedAgeSeconds, row.Renders.ObservedAgeSeconds)
		}
	}
	if checked == 0 {
		t.Fatal("no row carried chart detail, so the second stamp went unchecked")
	}

	// And the result's own claim about the expansion carries the same stamp,
	// because a reading that matched no row leaves no per-row stamp to read it
	// off.
	if inv.ChartDetail == nil || !inv.ChartDetail.Expanded {
		t.Fatalf("the expansion has run and the result does not say so: %+v", inv.ChartDetail)
	}
	if inv.ChartDetail.ObservedAt == nil || !inv.ChartDetail.ObservedAt.Equal(expandedAt) {
		t.Errorf("the chart-detail claim is stamped %v, want %s",
			inv.ChartDetail.ObservedAt, expandedAt)
	}
	if inv.ChartDetail.ObservedAgeSeconds == nil || *inv.ChartDetail.ObservedAgeSeconds != 990 {
		t.Errorf("the chart-detail claim carries the age %v", inv.ChartDetail.ObservedAgeSeconds)
	}
}

// An expansion that has run and matched nothing is its own answer, and it is
// not an expansion that has not run.
func TestAnExpansionThatMatchedNoRowSaysSo(t *testing.T) {
	g := withExpansion(fleet())
	g.Expansion.Apps = []GateExpansionApp{{
		Name: "nothing-that-runs", Cluster: "prod-eu", SourceType: "helm", Chart: "x"}}
	inv := newFixture(t, nil).withGate(g).inventory(t)

	for _, row := range *inv.Applications {
		if row.Renders != nil {
			t.Fatalf("%s matched an expansion that knows none of these rows: %+v",
				row.Name.Text, row.Renders)
		}
	}
	if inv.ChartDetail == nil || !inv.ChartDetail.Expanded {
		t.Fatalf("an expansion was read, and a result that denies it reads as a build that "+
			"cannot expand at all: %+v", inv.ChartDetail)
	}
	if !strings.Contains(inv.ChartDetail.Status.Text, "not unpinned") {
		t.Errorf("the sentence has to deny the reading a client will otherwise take, got %q",
			inv.ChartDetail.Status.Text)
	}
}

// Two Applications of one name on one cluster leave both without chart detail,
// rather than one of them with the other's.
//
// The identity the two readings can be joined on is a name and a cluster: the
// expansion knows the namespace an Application deploys INTO, and the reading
// knows the namespace the Application object lives in, which are different
// namespaces under one word. So apps-in-any-namespace can put two Applications
// of one name on one cluster, and there is nothing here that can tell which of
// them a chart belongs to. Absent is the answer; a chart detail from the wrong
// Application is one a reader acts on.
func TestAnAmbiguousJoinPublishesNoChartDetail(t *testing.T) {
	dup := func(g GateStatus) GateStatus {
		g.Fleet.Apps = append(g.Fleet.Apps, GateFleetApp{
			Name: "argo-cd", Namespace: "tenant-a", Cluster: "prod-eu"})
		return g
	}
	for name, g := range map[string]GateStatus{
		"two live rows of one name": dup(withExpansion(fleet())),
		"two expanded rows of one name": func() GateStatus {
			g := withExpansion(fleet())
			g.Expansion.Apps = append(g.Expansion.Apps, GateExpansionApp{
				Name: "argo-cd", Cluster: "prod-eu", SourceType: "helm",
				Chart: "some-other-argo-cd", Version: "0.0.1"})
			return g
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			inv := newFixture(t, nil).withGate(g).inventory(t)
			var checked int
			for _, row := range *inv.Applications {
				if row.Name.Text != "argo-cd" {
					continue
				}
				checked++
				if row.Renders != nil {
					t.Errorf("an ambiguous join published %+v, which belongs to whichever "+
						"of two Applications this process cannot tell apart", row.Renders)
				}
			}
			if checked == 0 {
				t.Fatal("the fixture published no ambiguous row, so nothing above ran")
			}
			// And the unambiguous rows beside it are untouched: an ambiguity
			// is one key's problem.
			for _, row := range *inv.Applications {
				if row.Name.Text == "external-secrets" && row.Renders == nil {
					t.Error("an ambiguity on one key cost an unrelated row its chart detail")
				}
			}
		})
	}
}

// Names and versions only: the result type has nowhere to put what a chart is
// rendered WITH.
//
// The expansion this joins from carries an Application's value files and its
// inline values block, and neither may cross onto a surface published outside
// the cluster. The shape guard above cannot see that one: a values file list
// is a slice of strings, which is the same shape as anything else here.
//
// So the check is the whole set of field names, compared against the set
// written here. It is deliberately not a list of banned words -- `inline`
// would slip past a ban on `values` -- and the failure it exists for is a
// field added to this result without a reviewer deciding it belongs. Adding
// one means adding it below, which is the sentence the pull request has to
// justify.
func TestTheInventoryResultTypeCarriesOnlyNamesClustersAndVersions(t *testing.T) {
	var result any
	for _, tool := range Tools() {
		if tool.Name == "inventory" {
			result = tool.Result
		}
	}
	if result == nil {
		t.Fatal("no tool named inventory is registered, so this walk has nothing to read")
	}

	want := map[string]bool{
		// The result and its two clocks.
		"Repository": true, "Swept": true, "SweptAt": true, "AgeSeconds": true,
		"Status": true, "Applications": true, "ChartDetail": true,
		"Expanded": true, "ObservedAt": true, "ObservedAgeSeconds": true,
		// One row: who it is, where it lands, and when it was seen.
		"Name": true, "Namespace": true, "Cluster": true, "ObservedIn": true,
		// What it renders from.
		"Renders": true, "SourceType": true, "Chart": true,
		"ChartRepository": true, "Version": true, "ApplicationSet": true,
		// A tagged string, wherever one appears.
		"Text": true, "Origin": true, "Truncated": true,
	}

	got := map[string]bool{}
	var walk func(typ reflect.Type)
	walk = func(typ reflect.Type) {
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice:
			walk(typ.Elem())
		case reflect.Struct:
			if typ == reflect.TypeOf(time.Time{}) {
				return
			}
			for i := 0; i < typ.NumField(); i++ {
				got[typ.Field(i).Name] = true
				walk(typ.Field(i).Type)
			}
		}
	}
	walk(reflect.TypeOf(result))

	for name := range got {
		if !want[name] {
			t.Errorf("this result carries a field named %s that nothing here accounts for.\n"+
				"inventory publishes names, clusters and versions. A values file, a values "+
				"leaf, a manifest or a rendered object is content, and the expansion this "+
				"joins from carries all of them. Add the field here with what it is, or do "+
				"not add it.", name)
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("this result no longer carries %s, so the set above is describing a "+
				"type that has moved on. Fix the set rather than leaving it stale.", name)
		}
	}
}

// A source type this surface has not declared is absent rather than published.
//
// The one typed fact on this result: a client branches on the word, so it gets
// the treatment a typed fact gets rather than the tag free text gets. The gate
// growing a third source type must not reach a client's default branch as a
// word nobody wrote a case for; absence is a shape every client already
// handles, because a row with no chart detail has no source type either.
func TestASourceTypeThisSurfaceHasNotDeclaredIsNotPublished(t *testing.T) {
	g := withExpansion(fleet())
	g.Expansion.Apps[0].SourceType = "oci-with-a-registry-login"
	inv := newFixture(t, nil).withGate(g).inventory(t)

	var checked int
	for _, row := range *inv.Applications {
		if row.Name.Text != "argo-cd" {
			continue
		}
		checked++
		if row.Renders == nil {
			t.Fatal("the row lost its chart detail; the refusal is one field's, not the row's")
		}
		if row.Renders.SourceType != "" {
			t.Errorf("a source type this surface never declared reached a client as %q",
				row.Renders.SourceType)
		}
		// And the rest of the detail still travels: it is tagged text, which
		// is a different promise from this one.
		if row.Renders.Chart == nil {
			t.Error("refusing the word cost the row the chart beside it")
		}
	}
	if checked == 0 {
		t.Fatal("the fixture published no row for the doctored expansion, so nothing ran")
	}
}
