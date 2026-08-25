package gate

import (
	"strings"
	"testing"
)

func TestFastTemplateResolvesClusterPaths(t *testing.T) {
	data := Cluster{
		Name:   "hub",
		Labels: map[string]string{"environment": "production", "cluster_role": "control-plane"},
	}.TemplateData(nil)

	got, err := renderFastTemplate(
		"$values/addons/environments/{{metadata.labels.environment}}/addons/addons.yaml", data)
	if err != nil {
		t.Fatal(err)
	}
	want := "$values/addons/environments/production/addons/addons.yaml"
	if got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

// A placeholder with no value must fail loudly. Rendering it as an empty string
// produces a path that silently resolves to the wrong values layer.
func TestFastTemplateFailsOnMissingKey(t *testing.T) {
	data := Cluster{Name: "hub"}.TemplateData(nil)
	if _, err := renderFastTemplate("{{metadata.labels.nope}}", data); err == nil {
		t.Fatal("a missing key must be an error, not an empty string")
	}
}

func TestGoTemplateRendersValuesAndName(t *testing.T) {
	data := Cluster{
		Name:   "hub",
		Labels: map[string]string{"environment": "production"},
	}.TemplateData(map[string]string{"chart": "cert-manager", "addonChartVersion": "v1.2.3"})

	out, err := renderGoTemplate(map[string]any{
		"metadata": map[string]any{"name": "{{ .values.chart }}-{{ .name }}"},
		"spec":     map[string]any{"targetRevision": "{{ .values.addonChartVersion }}"},
	}, data)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	meta := m["metadata"].(map[string]any)
	if meta["name"] != "cert-manager-hub" {
		t.Fatalf("want cert-manager-hub, got %v", meta["name"])
	}
}

func TestSplitPathHandlesQuotedIndex(t *testing.T) {
	data := map[string]any{"metadata": map[string]any{
		"labels": map[string]any{"platform.example/team": "core"},
	}}
	got, ok := lookupPath(data, `metadata.labels["platform.example/team"]`)
	if !ok || got != "core" {
		t.Fatalf("want core, got %q ok=%v", got, ok)
	}
}

// The template being rendered comes from the pull request under review, in a
// process holding the git token, the LLM key and the App private key. A
// template that can read the environment is a credential read.
func TestGoTemplateCannotReachTheEnvironmentOrNetwork(t *testing.T) {
	t.Setenv("GIT_TOKEN", "ghp_secret_value")

	for _, fn := range []string{
		`{{ env "GIT_TOKEN" }}`,
		`{{ expandenv "$GIT_TOKEN" }}`,
		`{{ getHostByName "example.com" }}`,
	} {
		node := map[string]any{"name": fn}
		out, err := renderGoTemplate(node, map[string]any{})
		if err == nil {
			t.Errorf("%s rendered instead of failing: %v", fn, out)
			continue
		}
		if !strings.Contains(err.Error(), "parsing template") {
			t.Errorf("%s: want a parse failure, got %v", fn, err)
		}
	}
}

// Deleting the three must not cost us the rest of sprig -- the dialect has to
// stay the one Argo CD renders, or the gate blocks changes Argo accepts.
func TestGoTemplateKeepsOrdinarySprigFunctions(t *testing.T) {
	node := map[string]any{"name": `{{ .cluster | upper | trunc 4 }}`}
	out, err := renderGoTemplate(node, map[string]any{"cluster": "hub-east"})
	if err != nil {
		t.Fatalf("ordinary sprig functions should still render: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok || m["name"] != "HUB-" {
		t.Fatalf("got %#v, want name HUB-", out)
	}
}
