package cluster

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The reads in argocdapps.go are a wire contract with somebody else's server,
// and CONTRIBUTING's Rule 1a says a change to either side of one needs a test
// that sees both. Nothing here can see ArgoCD, so the fixtures under testdata/
// stand in for it: hand-written to the shapes the ADR 0012 assessment measured,
// served by a fake at the same paths. What they prove is that each shape
// decodes; what they cannot prove is that no other shape exists.

// argoServing answers the two endpoints from fixture files and 404s anything
// else, so a read aimed at the wrong path fails loudly rather than decoding an
// empty list into "this repository deploys nothing".
func argoServing(t *testing.T, fixture string) *ArgoCD {
	t.Helper()
	body := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("reading fixture: %v", err)
		}
		return b
	}
	return argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/applications":
			_, _ = w.Write(body(fixture + "-applications.json"))
		case "/api/v1/applicationsets":
			_, _ = w.Write(body(fixture + "-applicationsets.json"))
		default:
			http.Error(w, "no such endpoint", http.StatusNotFound)
		}
	}))
}

func appNamed(t *testing.T, apps []Application, name string) Application {
	t.Helper()
	for _, a := range apps {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("no Application named %q in %d read", name, len(apps))
	return Application{}
}

// One repository is written at least three ways in a live fleet, and ArgoCD
// stores whatever it was given. Its own `?repo=` filter compares by string
// equality, which is why it returned 7 of 65 Applications on the production
// install and why this function exists at all.
func TestRepoURLSpellingsOfOneRepositoryCompareEqual(t *testing.T) {
	same := []string{
		"https://github.com/example-org/homelab",
		"https://github.com/example-org/homelab.git",
		"https://github.com/example-org/homelab/",
		"https://github.com/Example-Org/Homelab.git",
		"https://GitHub.com/example-org/homelab",
		"git@github.com:example-org/homelab.git",
		"git@github.com:example-org/homelab",
		"ssh://git@github.com/example-org/homelab.git",
		"https://token@github.com/example-org/homelab.git",
	}
	want := normaliseRepoURL(same[0])
	if want != "github.com/example-org/homelab" {
		t.Fatalf("normalised to %q", want)
	}
	for _, s := range same[1:] {
		if got := normaliseRepoURL(s); got != want {
			t.Errorf("%q normalised to %q, want %q", s, got, want)
		}
	}
}

// The other direction, and the one that would be silent: over-normalising two
// repositories into one puts somebody else's manifests in the scope.
func TestDifferentRepositoriesStayDifferent(t *testing.T) {
	base := normaliseRepoURL("https://github.com/example-org/homelab")
	for _, other := range []string{
		"https://github.com/example-org/homelab-staging",
		"https://github.com/other-org/homelab",
		"https://gitlab.example/example-org/homelab",
		"https://github.com/example-org/homelab/subgroup",
		// An explicit port is a different endpoint, and merging it with the
		// default would be wrong in the direction that widens the scope.
		"https://github.com:8443/example-org/homelab",
	} {
		if normaliseRepoURL(other) == base {
			t.Errorf("%q must not compare equal to the gated repository", other)
		}
	}
	if normaliseRepoURL("") != "" {
		t.Error("an empty URL must stay empty rather than becoming a match-all")
	}
	// An unparseable spelling still has to compare equal to itself, or an
	// exact match would stop working for a host nobody anticipated.
	odd := "weird-scheme+v2://host/path"
	if normaliseRepoURL(odd) != normaliseRepoURL(odd) || normaliseRepoURL(odd) == "" {
		t.Errorf("an unfamiliar spelling must still match itself: %q", normaliseRepoURL(odd))
	}
}

func TestApplicationsDecodeEveryLiveSourceShape(t *testing.T) {
	a := argoServing(t, "homelab")
	apps, err := a.Applications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 6 {
		t.Fatalf("read %d Applications, want the fixture's 6", len(apps))
	}

	// Multi-source: the chart is somebody else's artifact, and the sibling
	// source is the gated repository carrying the values the chart is
	// rendered with. `ref` is what makes `$values/` resolvable without a
	// repository-wide guess.
	multi := appNamed(t, apps, "cert-manager-hub")
	if len(multi.Sources) != 2 {
		t.Fatalf("want both sources, got %+v", multi.Sources)
	}
	if multi.Sources[0].Chart != "cert-manager" {
		t.Errorf("the chart source lost its chart: %+v", multi.Sources[0])
	}
	if got := multi.Sources[0].Helm.ValueFiles; len(got) != 1 || !strings.HasPrefix(got[0], "$values/") {
		t.Errorf("valueFiles must survive the decode: %v", got)
	}
	if multi.Sources[1].Ref != "values" {
		t.Errorf("the sibling source must name itself, got %q", multi.Sources[1].Ref)
	}

	// The singular `spec.source` is folded into the same list, so nothing
	// downstream has to know which spelling the author used.
	dir := appNamed(t, apps, "media")
	if len(dir.Sources) != 1 || dir.Sources[0].Path != "apps/media" {
		t.Fatalf("the singular source must fold into the list: %+v", dir.Sources)
	}
	if dir.Sources[0].Directory == nil || !dir.Sources[0].Directory.Recurse {
		t.Errorf("directory.recurse decides what a path expands to: %+v", dir.Sources[0].Directory)
	}

	// Values written inline have no file in the checkout, so a render that
	// dropped them would render a chart nobody deploys.
	inline := appNamed(t, apps, "monitoring")
	if inline.Sources[0].Helm == nil || inline.Sources[0].Helm.ValuesObject["retention"] != "30d" {
		t.Errorf("valuesObject must survive the decode: %+v", inline.Sources[0].Helm)
	}

	// The hydrated shape: the branch ArgoCD syncs from is generated, and the
	// repository a pull request is opened against is the dry source.
	hydrated := appNamed(t, apps, "hydrated-platform")
	if hydrated.DrySource == nil || hydrated.DrySource.Path != "platform" {
		t.Fatalf("sourceHydrator.drySource must be read: %+v", hydrated.DrySource)
	}
	if len(hydrated.Sources) != 0 {
		t.Errorf("a hydrated Application has no spec.source to fold in: %+v", hydrated.Sources)
	}

	if multi.TrackingID == "" {
		t.Error("the tracking annotation must be read; it is the whole root test")
	}
}

// The scope test. Five of the six fixture Applications point at the gated
// repository, and each does it differently: a second source, a plain source, a
// different case, scp form, and a dry source. ArgoCD's own filter would have
// matched two of them.
func TestPointsAtFindsTheGatedRepositoryWhereverItSits(t *testing.T) {
	a := argoServing(t, "homelab")
	apps, err := a.Applications(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	const gated = "https://github.com/example-org/homelab.git"
	var matched []string
	for _, app := range apps {
		if app.PointsAt(gated) {
			matched = append(matched, app.Name)
		}
	}
	want := map[string]bool{
		"cert-manager-hub":  true, // second source, plus a `.git` suffix
		"media":             true, // singular source, no suffix
		"monitoring":        true, // different case
		"ingress":           true, // scp form
		"hydrated-platform": true, // dry source
	}
	if len(matched) != len(want) {
		t.Fatalf("matched %v, want the %d that point at the gated repository", matched, len(want))
	}
	for _, name := range matched {
		if !want[name] {
			t.Errorf("%q does not point at the gated repository", name)
		}
	}
	if appNamed(t, apps, "vendor-thing").PointsAt(gated) {
		t.Error("an Application on another repository must stay out of scope")
	}
}

// Untracked means root. The partition is the whole basis for reaching roots at
// all, so it is asserted on a fixture holding both kinds rather than inferred.
func TestApplicationSetsPartitionByTheTrackingAnnotation(t *testing.T) {
	a := argoServing(t, "homelab")
	sets, err := a.ApplicationSets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 4 {
		t.Fatalf("read %d ApplicationSets, want the fixture's 4", len(sets))
	}

	var roots, tracked []string
	for _, s := range sets {
		if s.IsRoot() {
			roots = append(roots, s.Name)
		} else {
			tracked = append(tracked, s.Name)
		}
	}
	if len(roots) != 2 || len(tracked) != 2 {
		t.Fatalf("roots %v, tracked %v -- want the two applied bootstraps as roots", roots, tracked)
	}

	// The whole object is kept, because expanding one means handing it to the
	// gate's ApplicationSet expander, which reads the same map a committed
	// manifest parses into. A decoded subset here would be a second parser.
	for _, s := range sets {
		if s.Object["kind"] != "ApplicationSet" {
			t.Errorf("%s: the served object must be kept whole: %v", s.Name, s.Object["kind"])
		}
		if s.Namespace != "argocd" {
			t.Errorf("%s: namespace lost in the decode: %q", s.Name, s.Namespace)
		}
	}
}

// The split-repository pattern is the case that turned the design from
// file-first to derive-first: the content repository's own config file would
// restate, line for line, what these two reads already say.
func TestSplitRepositoryShapeNeedsNothingFromTheGatedRepository(t *testing.T) {
	a := argoServing(t, "split-repo")
	apps, err := a.Applications(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	const content = "https://github.com/example-org/apps-config"
	for _, app := range apps {
		if !app.PointsAt(content) {
			t.Fatalf("%s should be in the content repository's scope", app.Name)
		}
		d := app.Sources[0].Directory
		if d == nil || !d.Recurse || d.Exclude != "exclude/*" {
			t.Fatalf("%s: ArgoCD's directory semantics decide what the path expands to, "+
				"and an ignored exclude renders files nobody deploys: %+v", app.Name, d)
		}
	}
	if got := apps[1].Sources[0].Directory.Include; got != "*.yaml" {
		t.Errorf("include must survive alongside exclude, got %q", got)
	}

	// The root's manifest lives in the infrastructure repository, so head has
	// no copy to prefer and the live spec is all there is.
	sets, err := a.ApplicationSets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || !sets[0].IsRoot() {
		t.Fatalf("want one untracked root, got %+v", sets)
	}
}

// A read that fails must say which of the two new policy lines is missing.
// They are separate grants, an operator adds them one at a time, and a message
// naming the wrong resource sends somebody to paste a line that changes
// nothing.
func TestTheNewReadsNameTheirOwnPolicyLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(*ArgoCD) error
		want string
	}{
		{"applications", func(a *ArgoCD) error { _, err := a.Applications(context.Background()); return err },
			"p, <account>, applications, get, */*, allow"},
		{"applicationsets", func(a *ArgoCD) error { _, err := a.ApplicationSets(context.Background()); return err },
			"p, <account>, applicationsets, get, */*, allow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "no", http.StatusForbidden)
			}))
			err := tc.read(a)
			if err == nil {
				t.Fatal("a refused read must be an error, not an empty list")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error must carry the exact line to add:\n  got:  %v\n  want: %s", err, tc.want)
			}
		})
	}
}

// The clusters read shares the parameterised message now, so its own line has
// to keep saying what it always said.
func TestTheClustersReadStillNamesItsPolicyLine(t *testing.T) {
	a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	_, err := a.ClusterInventory(context.Background())
	if err == nil || !strings.Contains(err.Error(), "p, <account>, clusters, get, *, allow") {
		t.Fatalf("got %v", err)
	}
}

// An empty fleet decodes to an empty slice rather than to nil, so a caller
// ranging over it does not have to distinguish "none" from "not read". The
// error path is the only thing that means "not read".
func TestEmptyListsDecodeToEmptySlices(t *testing.T) {
	a := argoFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"items": null}`))
	}))
	apps, err := a.Applications(context.Background())
	if err != nil || apps == nil || len(apps) != 0 {
		t.Fatalf("apps = %v, err = %v", apps, err)
	}
	sets, err := a.ApplicationSets(context.Background())
	if err != nil || sets == nil || len(sets) != 0 {
		t.Fatalf("sets = %v, err = %v", sets, err)
	}
}
