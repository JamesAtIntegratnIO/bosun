package pipeline

import (
	"strings"
	"testing"
	"time"
)

// The finding that used to say "stopped receiving artifacts". Artifacts is
// true and useless; a reader with a pull request open in the next tab needs
// to know it is this one.
func TestAWedgedStageNamesWhatStoppedArriving(t *testing.T) {
	s := wedged()
	s.Freight = map[string]Freight{"addons/f08f1c9": {
		Name: "f08f1c9", Namespace: "addons", Alias: "mellow-mongoose",
		Artifacts: []string{"ghcr.io/org/app:v1.4.0"},
	}}
	f := findingOf(t, Detect(s), KindWedged)
	if !strings.Contains(f.Summary, "ghcr.io/org/app:v1.4.0") {
		t.Errorf("the summary must name what stopped arriving: %q", f.Summary)
	}
	// One name for it, not two. The alias identifies the freight to a reader
	// already looking at the finding about it, and spends half a summary line
	// doing it.
	if strings.Contains(f.Summary, "mellow-mongoose") {
		t.Errorf("the artifact is the half that matches what is in front of them: %q", f.Summary)
	}
	// The hash is still in the remedy, because that is what a Promotion
	// object takes. It is not what a sentence should be built out of.
	if !strings.Contains(f.Remedy, "f08f1c9") {
		t.Errorf("the remedy still needs the freight name:\n%s", f.Remedy)
	}
}

// Unreadable freight is ordinary: pruned, refused, or a source too old to
// answer. The finding is the one it always was.
func TestAWedgedStageWithUnreadableFreightSaysArtifacts(t *testing.T) {
	f := findingOf(t, Detect(wedged()), KindWedged)
	if !strings.Contains(f.Summary, "stopped receiving artifacts") {
		t.Errorf("without the freight it must fall back, not print a hash: %q", f.Summary)
	}
	if strings.Contains(f.Summary, "f08f1c9") {
		t.Errorf("a hash in a summary reads as detail and says less: %q", f.Summary)
	}
}

// A freight assembled from a monorepo carries a dozen images. Naming all of
// them turns one line into a screen; naming none of them was the problem.
func TestManyArtifactsAreTwoAndACount(t *testing.T) {
	f := Freight{Name: "f-1", Artifacts: []string{"a:1", "b:2", "c:3", "d:4"}}
	if got := f.Describe(); got != "a:1, b:2 and 2 others" {
		t.Errorf("got %q", got)
	}
	if got := (Freight{Name: "f-1", Alias: "brave-badger"}).Describe(); got != "brave-badger" {
		t.Errorf("with no artifacts the alias is the name a reader knows, got %q", got)
	}
	if got := (Freight{Name: "f-1"}).Describe(); got != "f-1" {
		t.Errorf("with neither, the hash is all there is, got %q", got)
	}
}

// The whole reason for reading the run. Before it, this finding said a
// verification failed and handed back a command asking which one -- the
// question the reader already had.
func TestAFailedVerificationNamesTheMetricThatSaidNo(t *testing.T) {
	s := verifying("VerificationError", "Error", 72*time.Hour)
	s.Freight = map[string]Freight{"addons/f-1": {
		Name: "f-1", Artifacts: []string{"quay.io/jetstack/cert-manager:v1.15.0"},
	}}
	s.Verifications = map[string]Verification{"addons/cert-manager.01": {
		Name: "cert-manager.01", Namespace: "addons", Phase: "Failed",
		Metrics: []VerifyMetric{{
			Name: "prometheus-check", Phase: "Error", Error: 3,
			Message: "dial tcp 10.106.179.157:9090: connect: no route to host",
		}},
	}}
	f := findingOf(t, Detect(s), KindVerifyStuck)
	if !strings.Contains(f.Detail, "prometheus-check") {
		t.Errorf("it must name the metric:\n%s", f.Detail)
	}
	// Errored and failed mean opposite things, and telling somebody to fix
	// their thresholds when Prometheus is unreachable wastes the afternoon.
	if !strings.Contains(f.Detail, "could not be measured") {
		t.Errorf("an errored metric never got an answer; say so:\n%s", f.Detail)
	}
	if !strings.Contains(f.Detail, "no route to host") {
		t.Errorf("the cause reaches text in the measurement:\n%s", f.Detail)
	}
	if !strings.Contains(f.Detail, "quay.io/jetstack/cert-manager:v1.15.0") {
		t.Errorf("it must say what is being held:\n%s", f.Detail)
	}
	// Still the fix, unchanged: the freight has been verified and only a
	// reverify asks again.
	if !strings.Contains(f.Remedy, "reverify") {
		t.Errorf("the remedy is still the reverify:\n%s", f.Remedy)
	}
}

// A long-running verification is either slow or endless, and those are
// different sentences. The claim used to be made either way.
func TestALongRunningVerificationSaysWhetherItCanEverEnd(t *testing.T) {
	run := func(ms ...VerifyMetric) *Snapshot {
		s := verifying("VerificationRunning", "Running", 2*time.Hour)
		s.Verifications = map[string]Verification{
			"addons/cert-manager.01": {Name: "cert-manager.01", Namespace: "addons", Metrics: ms}}
		return s
	}

	endless := findingOf(t, Detect(run(VerifyMetric{Name: "soak", Unbounded: true})), KindVerifyStuck)
	if !strings.Contains(endless.Detail, "`soak` has an interval and no count") {
		t.Errorf("the unbounded metric is the finding:\n%s", endless.Detail)
	}
	if !strings.Contains(endless.Detail, "until something stops it") {
		t.Errorf("say that it will not end on its own:\n%s", endless.Detail)
	}

	slow := findingOf(t, Detect(run(VerifyMetric{Name: "quick", Phase: "Running"})), KindVerifyStuck)
	if !strings.Contains(slow.Detail, "no failing or unbounded metric") {
		t.Errorf("ruling out the usual cause is worth a line:\n%s", slow.Detail)
	}
	if strings.Contains(slow.Detail, "indefinitely") {
		t.Errorf("a bounded run must not be described as endless:\n%s", slow.Detail)
	}

	// Read or not, a verification that has not finished is never told to
	// re-run: that would abandon an answer still being computed.
	for _, f := range []Finding{endless, slow} {
		if strings.Contains(f.Remedy, "reverify") {
			t.Errorf("still running must not be told to re-run:\n%s", f.Remedy)
		}
	}
}

// With the reference, the remedy addresses one object. Without it, it is the
// search this finding used to hand back in every case: list, sort by age, and
// hope the newest is the right one.
func TestTheRemedyStopsGuessingWhichRunItWas(t *testing.T) {
	unread := findingOf(t, Detect(verifying("VerificationRunning", "Running", 2*time.Hour)), KindVerifyStuck)
	if !strings.Contains(unread.Remedy, "--sort-by") {
		t.Errorf("without the run there is nothing to do but search:\n%s", unread.Remedy)
	}

	s := verifying("VerificationRunning", "Running", 2*time.Hour)
	s.Verifications = map[string]Verification{"addons/cert-manager.01": {
		Name: "cert-manager.01", Namespace: "addons",
		Metrics: []VerifyMetric{{Name: "soak", Unbounded: true}}}}
	f := findingOf(t, Detect(s), KindVerifyStuck)
	if !strings.Contains(f.Remedy, "analysisrun cert-manager.01") {
		t.Errorf("with the run, the remedy names it:\n%s", f.Remedy)
	}
	if strings.Contains(f.Remedy, "--sort-by") {
		t.Errorf("and does not also search for it:\n%s", f.Remedy)
	}
}

// Everything above degrades. A cluster that refuses the reads, or a Kargo old
// enough not to record the reference, still gets every finding it did before,
// word for word.
func TestAVerificationThatCouldNotBeReadStillProducesTheFinding(t *testing.T) {
	over := findingOf(t, Detect(verifying("VerificationFailed", "Failed", time.Hour)), KindVerifyStuck)
	if over.Severity != Blocking {
		t.Errorf("got %s", over.Severity)
	}
	if !strings.Contains(over.Detail, "That freight has been verified and the answer was no") {
		t.Errorf("with no freight read, it says what it always said:\n%s", over.Detail)
	}
	if !strings.Contains(over.Remedy, "reverify") {
		t.Errorf("the remedy does not depend on any of this:\n%s", over.Remedy)
	}

	running := findingOf(t, Detect(verifying("VerificationRunning", "Running", 2*time.Hour)), KindVerifyStuck)
	if !strings.Contains(running.Detail, "An AnalysisRun with no timeout") {
		t.Errorf("the general sentence is the fallback, not a deletion:\n%s", running.Detail)
	}
}

func wedged() *Snapshot {
	return &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "argo-cd", Namespace: "addons", Ready: true}},
		Promotions: []Promotion{{
			Name: "argo-cd.01abc.f08f1c9", Namespace: "addons", Stage: "argo-cd",
			Freight: "f08f1c9", Phase: PhaseErrored,
			CreatedAt: ago(72 * time.Hour), StartedAt: ago(72 * time.Hour),
			Message: `step "step-8": lookup api.github.com: server misbehaving`,
		}},
	}
}

func verifying(reason, phase string, since time.Duration) *Snapshot {
	return &Snapshot{Now: now, Stages: []Stage{{
		Name: "cert-manager", Namespace: "addons", Ready: false,
		ReadyReason: reason, ReadySince: since, CurrentFreight: "f-1",
		ReadyMessage:      "the analysis said no",
		VerificationID:    "v-1",
		VerificationPhase: phase,
		// Only set where a test also provides the run; a Stage whose run was
		// not readable leaves the maps empty, which is the fallback path.
		VerificationRunNamespace: "addons",
		VerificationRunName:      "cert-manager.01",
	}}}
}

// The two tallies mean opposite things and the sentence has to keep them
// apart. A metric that errored never got an answer; one that failed got the
// wrong answer, and the fix for each is in a different building.
func TestAMetricSaysWhetherItGotAnAnswerAtAll(t *testing.T) {
	for _, tc := range []struct {
		m    VerifyMetric
		want string
	}{
		{VerifyMetric{Name: "p", Error: 3}, "`p` could not be measured at all (3 attempts)"},
		{VerifyMetric{Name: "p", Failed: 1}, "`p` measured and failed 1 time"},
		{VerifyMetric{Name: "p", Error: 2, Failed: 1}, "`p` could not be measured 2 times and failed 1 time"},
		{VerifyMetric{Name: "p", Unbounded: true}, "`p` has an interval and no count"},
		// Nothing to say about it: the caller's answer is a shorter sentence.
		{VerifyMetric{Name: "p"}, "`p`"},
	} {
		if got := tc.m.Because(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
