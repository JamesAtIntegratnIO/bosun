package gate

import (
	"strings"
	"testing"
)

// rowFromApp and helmValues turn a rendered Application into the row the whole
// diff is computed from. A field that stops arriving here is a difference the
// gate stops being able to see, with every other test still green.

func app(spec map[string]any) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "podinfo"},
		"spec":     spec,
	}
}

func TestRowFromAppReadsAChartSource(t *testing.T) {
	row, err := rowFromApp("addons", "hub", app(map[string]any{
		"project":     "platform",
		"destination": map[string]any{"namespace": "apps"},
		"source": map[string]any{
			"chart": "podinfo", "repoURL": "https://charts.example",
			"targetRevision": "6.0.0",
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if row.AppSet != "addons" || row.Cluster != "hub" || row.App != "podinfo" {
		t.Errorf("got %+v", row)
	}
	if row.SourceType != RowHelm || row.Chart != "podinfo" || row.Version != "6.0.0" {
		t.Errorf("got %+v", row)
	}
	// Namespace and project are both reported as changes in their own right,
	// so losing either is a finding the gate stops making.
	if row.Namespace != "apps" || row.Project != "platform" {
		t.Errorf("got %+v", row)
	}
}

// The first source in these charts is a bare `ref: values` with neither a
// chart nor a path, so the interesting one is not always the first.
func TestRowFromAppSkipsAValuesRefAndFindsTheChart(t *testing.T) {
	row, err := rowFromApp("addons", "hub", app(map[string]any{
		"sources": []any{
			map[string]any{"repoURL": "https://git.example", "ref": "values"},
			map[string]any{"chart": "podinfo", "repoURL": "https://charts.example",
				"targetRevision": "6.0.0"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if row.SourceType != RowHelm || row.Chart != "podinfo" {
		t.Errorf("got %+v", row)
	}
}

// A path source keeps looking, because a manifest-source Application can carry
// both a values ref and a path while a later source is the chart.
func TestAPathSourceDoesNotStopTheSearch(t *testing.T) {
	row, err := rowFromApp("addons", "hub", app(map[string]any{
		"sources": []any{
			map[string]any{"path": "manifests", "targetRevision": "main"},
			map[string]any{"chart": "podinfo", "targetRevision": "6.0.0"},
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if row.SourceType != RowHelm || row.Chart != "podinfo" {
		t.Errorf("a later chart source must win: %+v", row)
	}

	// On its own, a path source IS the answer.
	row, err = rowFromApp("addons", "hub", app(map[string]any{
		"source": map[string]any{"path": "manifests", "targetRevision": "main"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if row.SourceType != RowPath || row.Path != "manifests" {
		t.Errorf("got %+v", row)
	}
}

// A shape the gate cannot read must be an error, not a zero Row: an empty row
// in the table reads as an Application that exists and targets nothing.
func TestRowFromAppRefusesWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      any
		wantErr string
	}{
		{"not a mapping", "a string", "not a mapping"},
		{"no metadata", map[string]any{"spec": map[string]any{}}, "no metadata or spec"},
		{"no spec", map[string]any{"metadata": map[string]any{}}, "no metadata or spec"},
		{"no usable source", app(map[string]any{"sources": []any{
			map[string]any{"ref": "values"},
		}}), "no source with a chart or path"},
		{"no sources at all", app(map[string]any{}), "no source with a chart or path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := rowFromApp("as", "c", tc.in); err == nil {
				t.Fatal("want an error")
			} else if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("got %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// The inline block is where a repository pins the chart defaults it depends
// on. Rendering without it reproduces upstream defaults rather than this
// cluster's configuration, and the diff then describes a cluster nobody has.
func TestHelmValuesCarriesTheFilesAndTheInlineBlock(t *testing.T) {
	files, inline := helmValues(map[string]any{"helm": map[string]any{
		"valueFiles": []any{"$values/a.yaml", "$values/b.yaml", 42},
		"valuesObject": map[string]any{
			"speaker": map[string]any{"frr": map[string]any{"enabled": false}},
		},
	}})
	if len(files) != 2 || files[0] != "$values/a.yaml" {
		t.Errorf("non-string entries must be dropped, not stringified: %v", files)
	}
	if !strings.Contains(inline, "frr") || !strings.Contains(inline, "false") {
		t.Errorf("the pinned default must survive: %q", inline)
	}
}

// The older `values:` string form is the fallback, and valuesObject wins.
func TestHelmValuesPrefersValuesObjectOverTheStringForm(t *testing.T) {
	_, inline := helmValues(map[string]any{"helm": map[string]any{
		"valuesObject": map[string]any{"a": 1},
		"values":       "b: 2\n",
	}})
	if !strings.Contains(inline, "a:") || strings.Contains(inline, "b:") {
		t.Errorf("valuesObject must win: %q", inline)
	}

	_, inline = helmValues(map[string]any{"helm": map[string]any{"values": "b: 2\n"}})
	if inline != "b: 2\n" {
		t.Errorf("the string form is the fallback: %q", inline)
	}
}

func TestHelmValuesOnASourceWithNoHelmBlock(t *testing.T) {
	files, inline := helmValues(map[string]any{"path": "manifests"})
	if files != nil || inline != "" {
		t.Errorf("got %v / %q", files, inline)
	}
}
