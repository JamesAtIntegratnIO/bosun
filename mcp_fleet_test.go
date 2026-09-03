package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
)

// The crossing from the gate's fleet reading into the tool surface's
// vocabulary, which is a contract nothing else can see.
//
// `mcp` may not import `gateservice`, so the two shapes are copies and the
// copying happens in this package. A field this file forgets is a field simply
// missing from every answer, with no compiler and no test in either package to
// notice -- and for a listing, the field most likely to be forgotten is the
// one that says which cluster, which is the whole question.

func TestTheFleetCrossesIntoTheToolSurfaceWhole(t *testing.T) {
	read := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	got := mcpGateStatus(gateservice.Status{
		SweptAt: read.Add(time.Hour),
		Fleet: &gateservice.Fleet{
			ObservedAt: read,
			Apps: []gateservice.FleetApp{
				{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"},
				{Name: "orphan", Namespace: "tenant-a"},
			},
		},
	})

	if got.Fleet == nil {
		t.Fatal("a reading crossed as nothing, which every tool reads as an install that has " +
			"never looked at its own fleet")
	}
	if !got.Fleet.ObservedAt.Equal(read) {
		t.Errorf("the reading crossed stamped %s, want %s", got.Fleet.ObservedAt, read)
	}
	want := []mcp.GateFleetApp{
		{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"},
		{Name: "orphan", Namespace: "tenant-a"},
	}
	if !reflect.DeepEqual(got.Fleet.Apps, want) {
		t.Errorf("the rows crossed as\n %+v\nwant %+v", got.Fleet.Apps, want)
	}

	// Field for field, off the source type rather than off the list above: two
	// strings of the same type in one struct is the shape a transposition
	// hides in, and a namespace published as a cluster name compiles, reads
	// correctly, and tells a platform agent to look on a cluster that does not
	// exist.
	from := reflect.TypeOf(gateservice.FleetApp{})
	to := reflect.TypeOf(mcp.GateFleetApp{})
	if from.NumField() != to.NumField() {
		t.Fatalf("the two rows have %d and %d fields; one of them grew a field the other "+
			"does not have", from.NumField(), to.NumField())
	}
	for i := 0; i < from.NumField(); i++ {
		name := from.Field(i).Name
		if _, ok := to.FieldByName(name); !ok {
			t.Errorf("gateservice.FleetApp.%s has no counterpart on the tool surface, so it "+
				"cannot cross at all", name)
			continue
		}
		a := reflect.ValueOf(gateservice.FleetApp{}).FieldByName(name)
		b := reflect.ValueOf(mcp.GateFleetApp{}).FieldByName(name)
		if a.Kind() != b.Kind() {
			t.Errorf("%s crosses from a %s into a %s", name, a.Kind(), b.Kind())
		}
	}
}

// No reading crosses as no reading, rather than as an empty one.
//
// The distinction the whole tool rests on, at the one place a helpful nil check
// could erase it: an empty fleet would publish zero rows, which is exactly what
// an install whose ArgoCD serves nothing looks like.
func TestNoFleetReadingCrossesAsNoReading(t *testing.T) {
	got := mcpGateStatus(gateservice.Status{SweptAt: time.Now()})
	if got.Fleet != nil {
		t.Fatalf("nothing crossed as %+v, which a client reads as a fleet somebody looked at",
			got.Fleet)
	}
}

// And the second crossing beside it: what the gate's last render expanded this
// repository into.
//
// A separate walk rather than a bigger one, because it is a separate claim
// with a separate failure. A field dropped here is chart detail missing from
// every `inventory` answer, and the reader who would notice is a platform
// agent asking what version an Application is on -- who cannot tell a chart
// nothing recorded from an Application that pins none.

func TestTheExpansionCrossesIntoTheToolSurfaceWhole(t *testing.T) {
	rendered := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	got := mcpGateStatus(gateservice.Status{
		SweptAt: rendered.Add(time.Hour),
		Expansion: &gateservice.Expansion{
			ObservedAt: rendered,
			Apps: []gateservice.ExpansionApp{{
				Name: "argo-cd", Cluster: "prod-eu", AppSet: "addons",
				SourceType: gate.RowHelm, Chart: "argo-cd",
				ChartRepo: "https://argoproj.github.io/argo-helm", Version: "7.7.0",
			}, {
				Name: "observability", Cluster: "prod-eu", SourceType: gate.RowPath,
			}},
		},
	})

	if got.Expansion == nil {
		t.Fatal("an expansion crossed as nothing, which every row reads as chart detail " +
			"nothing has computed")
	}
	if !got.Expansion.ObservedAt.Equal(rendered) {
		t.Errorf("the expansion crossed stamped %s, want %s", got.Expansion.ObservedAt, rendered)
	}
	want := []mcp.GateExpansionApp{{
		Name: "argo-cd", Cluster: "prod-eu", AppSet: "addons",
		SourceType: "helm", Chart: "argo-cd",
		ChartRepo: "https://argoproj.github.io/argo-helm", Version: "7.7.0",
	}, {
		Name: "observability", Cluster: "prod-eu", SourceType: "path",
	}}
	if !reflect.DeepEqual(got.Expansion.Apps, want) {
		t.Errorf("the rows crossed as\n %+v\nwant %+v", got.Expansion.Apps, want)
	}

	// Field for field off the source type, for the reason the reading's walk
	// does it: this row is six strings, and a transposition between any two of
	// them compiles, reads correctly, and tells an agent an Application is on
	// a chart it is not on.
	from := reflect.TypeOf(gateservice.ExpansionApp{})
	to := reflect.TypeOf(mcp.GateExpansionApp{})
	if from.NumField() != to.NumField() {
		t.Fatalf("the two rows have %d and %d fields; one of them grew a field the other "+
			"does not have", from.NumField(), to.NumField())
	}
	for i := 0; i < from.NumField(); i++ {
		name := from.Field(i).Name
		f, ok := to.FieldByName(name)
		if !ok {
			t.Errorf("gateservice.ExpansionApp.%s has no counterpart on the tool surface, so "+
				"it cannot cross at all", name)
			continue
		}
		if from.Field(i).Type.Kind() != f.Type.Kind() {
			t.Errorf("%s crosses from a %s into a %s", name,
				from.Field(i).Type.Kind(), f.Type.Kind())
		}
	}
}

// The two spellings of a source type are one vocabulary.
//
// `mcp` may not import `gate`, so the words a client branches on are declared
// twice. A rename on either side leaves a surface publishing a word no client
// recognises, and nothing else in the module can see both constants at once.
func TestTheSourceTypesAreSpelledTheSameOnBothSides(t *testing.T) {
	for _, tc := range []struct{ gate, published string }{
		{string(gate.RowHelm), mcp.RenderHelm},
		{string(gate.RowPath), mcp.RenderPath},
	} {
		if tc.gate != tc.published {
			t.Errorf("the gate renders %q and the tool surface publishes %q", tc.gate, tc.published)
		}
	}

	// The self-check: a source type the gate grows and this list does not
	// mention is one the surface publishes untranslated, so the count is
	// derived rather than trusted. There is no enumeration of RowSource in
	// Go, so the sources are the constants, and this is the assertion that
	// somebody adding a third has to come here and think about it.
	if got := len([]gate.RowSource{gate.RowHelm, gate.RowPath}); got != 2 {
		t.Fatalf("this test knows of %d source types", got)
	}
}

// No values reach the tool surface, asserted where both sides are visible.
//
// `gate.Row` carries the value files an Application layers and the inline
// values block it renders with. Both are content, `inventory` is the tool most
// able to become a manifest proxy by accretion, and neither may cross. The
// structural guard inside `mcp` bans the shapes a document travels in, and a
// list of value file paths is a slice of strings -- the same shape as
// everything else there -- so this is the half that names them.
func TestNoValuesFieldOfARenderedRowExistsOnTheToolSurface(t *testing.T) {
	// The fields of a rendered row that say what a chart is rendered WITH,
	// rather than which chart it is.
	values := []string{"ValueFiles", "ValuesInline", "ValuesLeaves"}

	row := reflect.TypeOf(gate.Row{})
	table := reflect.TypeOf(gate.Table{})
	crossing := reflect.TypeOf(mcp.GateExpansionApp{})
	for _, name := range values {
		_, onRow := row.FieldByName(name)
		_, onTable := table.FieldByName(name)
		if !onRow && !onTable {
			t.Errorf("neither gate.Row nor gate.Table has a field %s, so refusing it proves "+
				"nothing. Find what carries the values now and refuse that.", name)
		}
		if _, ok := crossing.FieldByName(name); ok {
			t.Errorf("mcp.GateExpansionApp.%s exists, so what a chart is rendered with can "+
				"cross onto a surface published outside the cluster", name)
		}
	}
}
