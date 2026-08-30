package cluster

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

const gatedRepo = "https://github.com/example-org/homelab.git"

// joinLines flattens Markdown lines for a substring assertion.
func joinLines(ms []gate.Markdown) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m)
	}
	return strings.Join(out, "\n")
}

func deriveFixture(t *testing.T, fixture, repoURL string) *gate.Derivation {
	t.Helper()
	a := argoServing(t, fixture)
	d, err := a.Derive(context.Background(), repoURL)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func sourceNamed(t *testing.T, d *gate.Derivation, name string) gate.Source {
	t.Helper()
	for _, s := range d.Sources {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no derived source named %q in %+v", name, d.Sources)
	return gate.Source{}
}

// The whole algorithm against the fleet shape, in one place: which
// Applications become sources, what kind each becomes, and what is left out.
func TestDerivationTurnsTheFleetIntoARenderPlan(t *testing.T) {
	d := deriveFixture(t, "homelab", gatedRepo)

	if d.Applications != 6 || d.ApplicationSets != 4 {
		t.Fatalf("the counts the report quotes must be what was read: %d/%d", d.Applications, d.ApplicationSets)
	}

	// The multi-source addon contributes nothing, and that is correct rather
	// than a gap. Its chart lives in somebody else's repository, so there is
	// no path here to render, and its only source on this repository is the
	// values source the chart addresses as `$values`, which exists to be
	// referred to. The change that matters to it, a chart version moving,
	// is what the chart diff already renders both sides of.
	//
	// It is not silently dropped either: the row it produces still arrives
	// through the ApplicationSet that creates it, which this repository does
	// hold.
	for _, s := range d.Sources {
		if s.Name == "app/cert-manager-hub" {
			t.Errorf("a chart in another repository is not a path in this checkout: %+v", s)
		}
	}

	// A plain directory Application.
	media := sourceNamed(t, d, "app/media")
	if media.Type != gate.SourceDirectory || media.Path != "apps/media" || !media.Recurse {
		t.Errorf("directory semantics must survive derivation: %+v", media)
	}

	// A path with a helm block becomes a helm source, with values written
	// into the Application carried across: there is no file in the checkout
	// to read for those.
	monitoring := sourceNamed(t, d, "app/monitoring")
	if monitoring.Type != gate.SourceHelm || monitoring.Chart != "charts/monitoring" {
		t.Errorf("a path with a helm block is a chart render: %+v", monitoring)
	}
	if monitoring.ValuesInline["retention"] != "30d" {
		t.Errorf("inline values must reach the render: %+v", monitoring.ValuesInline)
	}

	// The hydrated Application's dry source is where a pull request edits.
	if got := sourceNamed(t, d, "app/hydrated-platform"); got.Path != "platform" {
		t.Errorf("the dry source is the one a pull request changes: %+v", got)
	}

	// Somebody else's repository is not in this repository's scope.
	for _, s := range d.Sources {
		if s.Name == "app/vendor-thing" {
			t.Error("an Application on another repository must not become a source")
		}
	}

	// Untracked means root, and both bootstraps are.
	if len(d.Roots) != 2 {
		t.Fatalf("want the two applied bootstraps as roots, got %+v", d.Roots)
	}
	for _, r := range d.Roots {
		if r.Kind != "ApplicationSet" || r.Namespace != "argocd" || r.Object == nil {
			t.Errorf("a root has to carry enough to be found and, failing that, rendered: %+v", r)
		}
	}
}

// A chart named on the gated repository's own URL is still an artifact rather
// than a path, and the skip is reported rather than silent.
func TestAChartNamedOnTheGatedRepositoryIsNotAPath(t *testing.T) {
	d := DeriveFrom([]Application{{
		Name:    "odd",
		Sources: []AppSource{{RepoURL: gatedRepo, Chart: "thing", TargetRevision: "1.0.0"}},
	}}, nil, gatedRepo)

	if len(d.Sources) != 0 {
		t.Fatalf("nothing here is a path to render: %+v", d.Sources)
	}
	if !strings.Contains(joinLines(d.Warnings), "rather than a path") {
		t.Errorf("the skip must announce itself: %v", d.Warnings)
	}
}

// `$values/…` resolves through the ref the Application itself declares, which
// is what retires the repository-wide `valuesRef` guess: that key was one
// answer applied to every Application at once, and it was wrong the moment two
// of them chose different names.
func TestValuesRefsResolveThroughTheApplicationsOwnSibling(t *testing.T) {
	apps := []Application{{
		Name: "addon",
		Sources: []AppSource{
			{
				RepoURL: gatedRepo, Path: "charts/addon",
				Helm: &AppHelm{ValueFiles: []string{
					"$vals/envs/prod/values.yaml",
					"values.yaml",
					"$missing/x.yaml",
				}},
			},
			{RepoURL: gatedRepo, Ref: "vals"},
		},
	}}
	d := DeriveFrom(apps, nil, gatedRepo)

	got := sourceNamed(t, d, "app/addon")
	want := []string{"envs/prod/values.yaml", "values.yaml"}
	if len(got.ValueFiles) != len(want) {
		t.Fatalf("value files = %v, want %v", got.ValueFiles, want)
	}
	for i := range want {
		if got.ValueFiles[i] != want[i] {
			t.Errorf("value file %d = %q, want %q", i, got.ValueFiles[i], want[i])
		}
	}
	if !strings.Contains(joinLines(d.Warnings), "$missing") {
		t.Errorf("a ref the Application does not declare must be reported, not dropped: %v", d.Warnings)
	}
}

// A ref pointing at another repository resolves to a file this checkout does
// not have. Rendering without that layer is what ArgoCD's own
// ignoreMissingValueFiles does, but doing it silently would let the diff
// attribute the difference to the pull request.
func TestAValuesRefIntoAnotherRepositoryIsReportedRatherThanRendered(t *testing.T) {
	apps := []Application{{
		Name: "addon",
		Sources: []AppSource{
			{RepoURL: gatedRepo, Path: "charts/addon",
				Helm: &AppHelm{ValueFiles: []string{"$vals/values.yaml"}}},
			{RepoURL: "https://github.com/other-org/values", Ref: "vals"},
		},
	}}
	d := DeriveFrom(apps, nil, gatedRepo)

	if got := sourceNamed(t, d, "app/addon"); len(got.ValueFiles) != 0 {
		t.Errorf("a file in another repository is not in this checkout: %v", got.ValueFiles)
	}
	if !strings.Contains(joinLines(d.Warnings), "not the repository being gated") {
		t.Errorf("the missing layer must be reported: %v", d.Warnings)
	}
}

// A fleet renders one path once per cluster, so the same shape arrives many
// times under many Application names. Keyed on the name, fifty clusters would
// mean fifty identical renders of one directory.
func TestIdenticalShapesAreRenderedOnce(t *testing.T) {
	var apps []Application
	for _, name := range []string{"media-hub", "media-edge", "media-lab"} {
		apps = append(apps, Application{
			Name:    name,
			Sources: []AppSource{{RepoURL: gatedRepo, Path: "apps/media", Directory: &AppDirectory{Recurse: true}}},
		})
	}
	// One genuinely different shape, which must survive.
	apps = append(apps, Application{
		Name:    "other",
		Sources: []AppSource{{RepoURL: gatedRepo, Path: "apps/other"}},
	})

	d := DeriveFrom(apps, nil, gatedRepo)
	if len(d.Sources) != 2 {
		t.Fatalf("three identical shapes are one render, plus the different one: %+v", d.Sources)
	}
}

// The URL-spelling matrix, through the whole path rather than through the
// comparison alone. Each of these Applications spells the gated repository
// differently, and every one has to land in the scope.
func TestEverySpellingOfTheRepositoryReachesTheRenderPlan(t *testing.T) {
	for _, spelling := range []string{
		"https://github.com/example-org/homelab",
		"https://github.com/example-org/homelab.git",
		"https://github.com/Example-Org/Homelab",
		"git@github.com:example-org/homelab.git",
		"ssh://git@github.com/example-org/homelab",
	} {
		t.Run(spelling, func(t *testing.T) {
			apps := []Application{{
				Name:    "media",
				Sources: []AppSource{{RepoURL: spelling, Path: "apps/media"}},
			}}
			// Asked for under a different spelling again, which is the case
			// that matters: the gate is configured with one and ArgoCD holds
			// another.
			d := DeriveFrom(apps, nil, "git@github.com:Example-Org/homelab.git")
			if len(d.Sources) != 1 {
				t.Fatalf("%q did not reach the scope: %+v", spelling, d.Sources)
			}
		})
	}
}

// The split-repository shape end to end: two tenant Applications with
// directory semantics, one untracked root whose manifest is in another
// repository, and no config file anywhere in the picture.
func TestTheSplitRepositoryShapeDerivesWithNoFile(t *testing.T) {
	d := deriveFixture(t, "split-repo", "https://github.com/example-org/apps-config")

	if len(d.Sources) != 2 {
		t.Fatalf("both tenants should be in scope: %+v", d.Sources)
	}
	for _, s := range d.Sources {
		if s.Type != gate.SourceDirectory || !s.Recurse || s.Exclude != "exclude/*" {
			t.Errorf("ArgoCD's directory semantics decide what the path expands to: %+v", s)
		}
	}
	if len(d.Roots) != 1 || d.Roots[0].Name != "tenants" {
		t.Fatalf("the root is untracked and its manifest is elsewhere: %+v", d.Roots)
	}
}

// A read that fails is an error, not a smaller scope. Same rule as the
// inventory, same reason: a render against a world the gate could not see
// finds no targeting change and waves everything through.
func TestDeriveRefusesWhenArgoCDDoes(t *testing.T) {
	a := argoFor(t, forbidden())
	if _, err := a.Derive(context.Background(), gatedRepo); err == nil {
		t.Fatal("a refused read must not produce an empty derivation")
	}
}

// A warning is half prose the derivation writes and half names ArgoCD serves.
// The report renders warnings as the composed lines they are, so the names
// are neutralised here — an Application called evil` must not be able to
// close a code span in a report it never earned a line of.
func TestAHostileApplicationNameIsNeutralisedInWarnings(t *testing.T) {
	d := DeriveFrom([]Application{{
		Name:    "evil`name",
		Sources: []AppSource{{RepoURL: gatedRepo, Chart: "thing", TargetRevision: "1.0.0"}},
	}}, nil, gatedRepo)

	joined := joinLines(d.Warnings)
	if !strings.Contains(joined, `evil\x60name`) {
		t.Errorf("the backtick must be spelt out, visibly: %v", d.Warnings)
	}
	if strings.Contains(joined, "evil`name") {
		t.Errorf("a raw backtick survived into a composed line: %v", d.Warnings)
	}
}
