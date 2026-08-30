package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// The tool answers with the sweep's findings, typed.
//
// This is the whole point of the surface: an on-call agent asks what has
// stopped promoting and gets the kind to branch on, the severity, the subject,
// the evidence, how long it has held, and the command that ends it -- rather
// than markdown written for somebody else that it has to parse.
func TestPipelineReportCarriesTheSweepsFindings(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))
	got := f.report(t)

	if got.Findings == nil {
		t.Fatal("a completed sweep must carry a findings field, even an empty one")
	}
	findings := *got.Findings
	if len(findings) == 0 {
		t.Fatal("the fixture's Stage has been sitting on an Errored promotion for three days " +
			"and produced no finding; the sweep is not seeing the world this test set up")
	}

	var wedgedFinding *Finding
	for i := range findings {
		if findings[i].Kind == string(pipeline.KindWedged) {
			wedgedFinding = &findings[i]
		}
	}
	if wedgedFinding == nil {
		t.Fatalf("no %s finding in %v", pipeline.KindWedged, kindsOf(findings))
	}

	if wedgedFinding.Severity != string(pipeline.Blocking) {
		t.Errorf("a Stage that stopped delivering is blocking, got %q", wedgedFinding.Severity)
	}
	if wedgedFinding.Subject != "external-secrets" {
		t.Errorf("the subject is the Stage in the operator's own vocabulary, got %q", wedgedFinding.Subject)
	}
	if !strings.Contains(wedgedFinding.Detail.Text, "server misbehaving") {
		t.Errorf("the detail must carry the evidence Kargo recorded:\n%s", wedgedFinding.Detail.Text)
	}
	if wedgedFinding.AgeSeconds == nil || *wedgedFinding.AgeSeconds != int64((72*time.Hour).Seconds()) {
		t.Errorf("the age must be a number a client can branch on, got %v", wedgedFinding.AgeSeconds)
	}
	if wedgedFinding.Age != "3d" {
		t.Errorf("the age must also read the way every other bosun surface says it, got %q", wedgedFinding.Age)
	}
	// The remedy is the field that took an hour to find the first time.
	if wedgedFinding.Remedy == nil {
		t.Fatal("a wedged promotion has a remedy and this one lost it")
	}
	for _, want := range []string{"kubectl create -f -", "generateName: external-secrets", "freight: f08f1c9"} {
		if !strings.Contains(wedgedFinding.Remedy.Command, want) {
			t.Errorf("the remedy is missing %q:\n%s", want, wedgedFinding.Remedy.Command)
		}
	}
	if wedgedFinding.Remedy.Origin != OriginBosun {
		t.Errorf("a remedy is composed by bosun and must say so, got %q", wedgedFinding.Remedy.Origin)
	}

	// And the sweep's own accounting beside the findings, so a clean report
	// can prove it looked rather than merely returning nothing.
	if got.Examined == nil {
		t.Fatal("a completed sweep must carry its accounting")
	}
	if got.Examined.Stages != 2 || got.Examined.Warehouses != 1 || got.Examined.Promotions != 1 {
		t.Errorf("the accounting must be the sweep's own counts, got %+v", *got.Examined)
	}
	if got.Examined.PullRequests != 1 {
		t.Errorf("the pull requests the sweep listed must be counted, got %d", got.Examined.PullRequests)
	}
}

// Worst first, because the first thing a client's model reads is the thing
// that matters.
func TestFindingsComeBackWorstFirst(t *testing.T) {
	w := wedged()
	// A Warehouse that has missed two discoveries: degraded, and it must not
	// come before the Stage that has stopped delivering.
	w.kargo.warehouses = append(w.kargo.warehouses, cluster.KargoWarehouse{
		Name: "stale", Namespace: "addons", Ready: true,
		Interval: time.Hour, DiscoveredAt: sweptAt.Add(-6 * time.Hour),
	})
	f := newFixture(t, sweep(t, w))

	findings := *f.report(t).Findings
	if len(findings) < 2 {
		t.Fatalf("this fixture must produce a blocking and a degraded finding, got %v", kindsOf(findings))
	}
	if findings[0].Severity != string(pipeline.Blocking) {
		t.Fatalf("the worst finding must come first, got %v", severitiesOf(findings))
	}
	seenDegraded := false
	for _, fi := range findings {
		if fi.Severity == string(pipeline.Degraded) {
			seenDegraded = true
		}
		if seenDegraded && fi.Severity == string(pipeline.Blocking) {
			t.Fatalf("a blocking finding came after a degraded one: %v", severitiesOf(findings))
		}
	}
}

// Before the first sweep, the findings field is ABSENT and the result says so
// in words.
//
// This is the assertion the whole result shape was designed around. A client
// that reads an empty findings list as "nothing is wrong" from a supervisor
// that has not looked has recorded the most expensive possible measurement,
// and it is exactly what an empty array would invite.
func TestBeforeTheFirstSweepThereIsNoFindingsField(t *testing.T) {
	f := newFixture(t, nil)
	raw := f.call(t, "pipeline_report")
	keys := fields(t, raw)

	if _, present := keys["findings"]; present {
		t.Errorf("the findings field must be ABSENT before the first sweep, not empty:\n%s", raw)
	}
	if _, present := keys["examined"]; present {
		t.Errorf("the accounting must be absent too: there is no sweep to account for:\n%s", raw)
	}
	if _, present := keys["sweptAt"]; present {
		t.Errorf("there is no sweep timestamp before the first sweep:\n%s", raw)
	}

	got := f.report(t)
	if got.Swept {
		t.Error("swept must be false before the first sweep")
	}
	if got.Clean {
		t.Error("a supervisor that has not looked is not clean; that is the whole distinction")
	}
	if !strings.Contains(got.Status.Text, "No sweep has completed") {
		t.Errorf("the result must say in words that nothing has looked, got %q", got.Status.Text)
	}
	if !strings.Contains(got.Status.Text, "not a clean report") {
		t.Errorf("the words must rule out the reading they exist to prevent, got %q", got.Status.Text)
	}
}

// A sweep that examined nothing is distinguishable from a sweep that examined
// things and found nothing wrong.
//
// Both have no findings. One of them is good news. Telling them apart must not
// require a client to add up four counters and guess, which is why clean is a
// field.
func TestASweepThatLookedAtNothingIsNotACleanReport(t *testing.T) {
	// A cluster that answers nothing: every read fails, which is what a
	// missing ClusterRole grant or an unreachable apiserver looks like.
	blind := &world{kargo: &fakeKargo{
		stageErr:     errRefused,
		warehouseErr: errRefused,
		promotionErr: errRefused,
	}, prs: &countingPRs{Fake: &gitprovider.Fake{}}}
	f := newFixture(t, sweep(t, blind))
	got := f.report(t)

	if !got.Swept {
		t.Fatal("the sweep did complete; it simply could not read anything")
	}
	if got.Findings == nil || len(*got.Findings) != 0 {
		t.Fatalf("a sweep that read nothing has no findings, and the field is present and empty: %v", got.Findings)
	}
	if got.Clean {
		t.Fatal("a sweep that examined nothing must not report itself clean")
	}
	if got.Examined == nil || got.Examined.Stages != 0 {
		t.Fatalf("the accounting must say it examined nothing, got %+v", got.Examined)
	}
	if len(got.Examined.Notes) == 0 {
		t.Fatal("a sweep that could not look must say what it could not do")
	}

	// And the opposite: a fleet examined, nothing wrong.
	clean := newFixture(t, sweep(t, healthy())).report(t)
	if !clean.Clean {
		t.Fatal("a sweep that examined a fleet and found nothing wrong is clean")
	}
	if clean.Findings == nil || len(*clean.Findings) != 0 {
		t.Fatalf("a clean sweep's findings field is present and empty, got %v", clean.Findings)
	}
	if clean.Examined.Stages == 0 {
		t.Fatal("a clean report has to be able to prove it looked")
	}
}

// Every result names its repository and the sweep it came from.
//
// The repository because an answer whose install is ambiguous cannot be
// cached, and the timestamp because a client deciding whether to trust an
// answer or wait for the next sweep has no other way to tell.
func TestEveryResultCarriesTheRepositoryAndTheSweep(t *testing.T) {
	report := sweep(t, wedged())
	f := newFixture(t, report)
	got := f.report(t)

	if got.Repository != "example/platform" {
		t.Errorf("every result carries its repository, got %q", got.Repository)
	}
	if got.SweptAt == nil || !got.SweptAt.Equal(report.At) {
		t.Errorf("the sweep timestamp must be the sweep's own, got %v want %v", got.SweptAt, report.At)
	}
	if got.AgeSeconds == nil || *got.AgeSeconds != 90 {
		t.Errorf("the answer must say how old it is, got %v", got.AgeSeconds)
	}

	// The before-the-first-sweep case carries the repository too: it is the
	// one field that is true whether or not anything has looked.
	if empty := newFixture(t, nil).report(t); empty.Repository != "example/platform" {
		t.Errorf("a result with no sweep behind it still names its repository, got %q", empty.Repository)
	}
}

// Free text is tagged with where it came from, and a remedy is never tagged as
// anything but bosun's own.
//
// Bosun's results land in agents holding tools bosun refuses for itself, so a
// hostile release note does not have to jailbreak bosun's model -- only to be
// delivered by it to a better-armed one. The contract a client can rely on is
// that instructions in a result are bosun's own or absent, and the origin is
// what makes that checkable rather than asserted.
func TestEveryFreeTextFieldSaysWhereItCameFrom(t *testing.T) {
	f := newFixture(t, sweep(t, wedged()))
	got := f.report(t)

	if got.Status.Origin == "" {
		t.Error("the status line carries a headline about the cluster and must be tagged")
	}
	for _, fi := range *got.Findings {
		if fi.Summary.Origin == "" || fi.Detail.Origin == "" {
			t.Errorf("%s: summary and detail quote the cluster and must both be tagged (%q, %q)",
				fi.Kind, fi.Summary.Origin, fi.Detail.Origin)
		}
		if fi.Remedy != nil && fi.Remedy.Origin != OriginBosun {
			t.Errorf("%s: a remedy is bosun's own or it is not emitted, got %q", fi.Kind, fi.Remedy.Origin)
		}
	}
	for _, n := range got.Examined.Notes {
		if n.Origin == "" {
			t.Errorf("a note quotes the error that stopped a read and must be tagged: %q", n.Text)
		}
	}
}

// A finding whose subject bosun cannot vouch for comes back without a remedy,
// never with a suspect one.
//
// pipeline owns the grammar and its own tests cover it; this asserts the
// property survives the trip to the wire, because that is where the command
// reaches something that will run it.
func TestAHostileNameReachesTheWireWithoutARemedy(t *testing.T) {
	const bad = "stage; rm -rf /"
	w := wedged()
	w.kargo.stages = append(w.kargo.stages, cluster.KargoStage{Name: bad, Namespace: "addons", Ready: true})
	w.kargo.promotions = append(w.kargo.promotions, cluster.KargoPromotion{
		Name: "p", Namespace: "addons", Stage: bad, Freight: "f08f1c9",
		Phase: pipeline.PhaseErrored, CreatedAt: sweptAt.Add(-72 * time.Hour),
	})

	f := newFixture(t, sweep(t, w))
	raw := f.call(t, "pipeline_report")
	got := f.report(t)

	var found bool
	for _, fi := range *got.Findings {
		if fi.Subject != bad {
			continue
		}
		found = true
		if fi.Remedy != nil {
			t.Errorf("a name outside the grammar reached a remedy:\n%s", fi.Remedy.Command)
		}
	}
	if !found {
		t.Fatal("the hostile Stage produced no finding at all; dropping it would trade a " +
			"forgeable command for a pipeline nobody is told has stopped")
	}
	// Belt and braces, over the whole body: the name may appear in a subject
	// and a summary, and must appear in no command anywhere.
	for _, cmd := range commandsIn(t, raw) {
		if strings.Contains(cmd, bad) {
			t.Errorf("a command in the response carries a name bosun could not validate:\n%s", cmd)
		}
	}
}

// commandsIn is every remedy command in a result.
func commandsIn(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var r Report
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	var out []string
	if r.Findings == nil {
		return out
	}
	for _, f := range *r.Findings {
		if f.Remedy != nil {
			out = append(out, f.Remedy.Command)
		}
	}
	return out
}

func kindsOf(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

func severitiesOf(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Severity)
	}
	return out
}

// The status line is bosun's own words, and the origin tag says so.
//
// It is the one free-text field on this result tagged OriginBosun, which is
// the tag a careful client will not fence. That claim rests entirely on
// pipeline.Report.Headline composing from counts and fixed words, so this is
// the test that notices the day a headline starts naming a Stage.
func TestTheStatusLineIsBosunsOwnWords(t *testing.T) {
	const bad = "stage-name-kargo-handed-us"
	w := wedged()
	w.kargo.stages = append(w.kargo.stages, cluster.KargoStage{Name: bad, Namespace: bad, Ready: true})
	w.kargo.promotions = append(w.kargo.promotions, cluster.KargoPromotion{
		Name: "p." + bad, Namespace: bad, Stage: bad, Freight: "f0",
		Phase: pipeline.PhaseErrored, CreatedAt: sweptAt.Add(-72 * time.Hour),
		Message: "the registry said " + bad,
	})

	got := newFixture(t, sweep(t, w)).report(t)
	if got.Status.Origin != OriginBosun {
		t.Fatalf("the status line is tagged %q; if the headline has started quoting the "+
			"cluster, change the tag rather than the assertion", got.Status.Origin)
	}
	if strings.Contains(got.Status.Text, bad) {
		t.Fatalf("the status line quotes the cluster while claiming to be bosun's own words: %q",
			got.Status.Text)
	}
	// The self-check: the name has to be in the response somewhere, or this
	// asserted that an unrelated string is absent.
	if len(*got.Findings) == 0 {
		t.Fatal("the fixture produced no findings, so the name never reached the response")
	}
}
