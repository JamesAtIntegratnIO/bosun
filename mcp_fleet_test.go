package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/mcp"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
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

// The two spellings of a source type are one vocabulary, and the list is read
// out of the gate rather than written here.
//
// `mcp` may not import `gate`, so the words a client branches on are declared
// twice, and the tool surface refuses to publish one it has not declared. That
// refusal is right and it is silent: a source type the gate grows and the
// surface has never heard of reaches a client as an absent field, which reads
// exactly like a row with no chart detail.
//
// So the subject is every `RowSource` constant in the gate's own source, and
// each one is driven through the real crossing and the real handler. A third
// one fails here on the day it is added, which is the only day anybody can
// still choose what a client should see.
func TestEverySourceTypeTheGateRendersIsOneTheToolSurfacePublishes(t *testing.T) {
	sources := rowSources(t)
	if len(sources) < 2 {
		t.Fatalf("found %d RowSource constants in gate's source; this walk is reading the "+
			"wrong package and would report agreement over nothing", len(sources))
	}

	for _, source := range sources {
		t.Run(source, func(t *testing.T) {
			got := publishedSourceType(t, gate.RowSource(source))
			if got != source {
				t.Errorf("the gate renders the source type %q and a client is told %q.\n"+
					"mcp cannot import gate, so the word is declared twice: add it to the "+
					"constants beside mcp.RenderHelm and to the vetting that decides what "+
					"may be published, or a client branching on this field gets an absent "+
					"one and no way to find out why.", source, got)
			}
		})
	}
}

// rowSources is every value of gate.RowSource, read out of gate's own syntax
// tree.
//
// Derived rather than listed, per CONTRIBUTING: a list of two with nothing
// forcing a third is how the ClusterRole stayed broken. Go has no enumeration
// of a string type's constants at runtime, so the source is the source.
func rowSources(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join(helmtest.Root(t), "gate")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("could not parse %s: %v", dir, err)
	}
	pkg, ok := pkgs["gate"]
	if !ok {
		t.Fatalf("no package gate under %s; this walk is reading the wrong directory", dir)
	}

	var out []string
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				// The declared type is what identifies these, rather than a
				// name prefix: a third one called something else is exactly
				// the one a prefix would miss.
				if !ok || len(vs.Values) == 0 {
					continue
				}
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "RowSource" {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("unreadable RowSource constant %s: %v", lit.Value, err)
					}
					out = append(out, s)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// publishedSourceType is what a client is told about an Application the gate
// rendered from one source type, through the real adapter and the real
// handler.
//
// The whole crossing rather than the constants side by side, because the
// declaration is only half of it: the surface also vets the word before
// publishing it, and a constant declared but left out of that check would
// still reach a client as nothing.
func publishedSourceType(t *testing.T, source gate.RowSource) string {
	t.Helper()
	observed := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	status := mcpGateStatus(gateservice.Status{
		SweptAt: observed.Add(time.Hour),
		Fleet: &gateservice.Fleet{ObservedAt: observed, Apps: []gateservice.FleetApp{
			{Name: "argo-cd", Namespace: "argocd", Cluster: "prod-eu"}}},
		Expansion: &gateservice.Expansion{ObservedAt: observed, Apps: []gateservice.ExpansionApp{
			{Name: "argo-cd", Cluster: "prod-eu", SourceType: source}}},
	})

	srv := &mcp.Server{
		Repository: "example/platform",
		Report:     func() *pipeline.Report { return nil },
		Triage:     func() mcp.TriageStatus { return mcp.TriageStatus{} },
		Gate:       func() mcp.GateStatus { return status },
		Auth:       mcp.Unauthenticated{},
		Now:        func() time.Time { return observed.Add(2 * time.Hour) },
	}
	h, err := srv.Handler()
	if err != nil {
		t.Fatalf("the handler would not build: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Post(ts.URL+mcp.EndpointPath, "application/json", strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"inventory","arguments":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out struct {
		Result struct {
			StructuredContent mcp.Fleet `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	apps := out.Result.StructuredContent.Applications
	if apps == nil || len(*apps) != 1 {
		t.Fatalf("the fixture published %v rows, so nothing was read", apps)
	}
	if (*apps)[0].Renders == nil {
		t.Fatal("the row carries no chart detail, so the join this reads through did not " +
			"happen and the assertion would be about the wrong absence")
	}
	return (*apps)[0].Renders.SourceType
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
