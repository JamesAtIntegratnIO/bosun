package pipeline

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)

func ago(d time.Duration) time.Time { return now.Add(-d) }

func findingOf(t *testing.T, r *Report, k Kind) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Kind == k {
			return f
		}
	}
	t.Fatalf("no %s finding in %v", k, kinds(r))
	return Finding{}
}

func kinds(r *Report) []Kind {
	var out []Kind
	for _, f := range r.Findings {
		out = append(out, f.Kind)
	}
	return out
}

func none(t *testing.T, r *Report, k Kind) {
	t.Helper()
	for _, f := range r.Findings {
		if f.Kind == k {
			t.Fatalf("unexpected %s finding: %s", k, f.Summary)
		}
	}
}

// The situation that cost four addons three days of updates: one transient
// error, a terminal promotion, and no retry, while every Application stayed
// Synced and Healthy.
func TestATerminalFailureThatNothingWillRetry(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "external-secrets", Namespace: "addons", Ready: true}},
		Promotions: []Promotion{{
			Name: "external-secrets.01abc.f08f1c9", Namespace: "addons", Stage: "external-secrets",
			Freight: "f08f1c9", Phase: PhaseErrored, CreatedAt: ago(72 * time.Hour),
			StartedAt: ago(72 * time.Hour),
			Message:   `step "step-8": lookup api.github.com: server misbehaving`,
		}},
	}
	f := findingOf(t, Detect(s), KindWedged)
	if f.Severity != Blocking {
		t.Errorf("a Stage that stopped delivering is blocking, got %s", f.Severity)
	}
	if !strings.Contains(f.Summary, "3d") {
		t.Errorf("the summary must say how long, got %q", f.Summary)
	}
	if !strings.Contains(f.Detail, "server misbehaving") {
		t.Errorf("the detail must carry the reason Kargo recorded:\n%s", f.Detail)
	}
	// The remedy is the whole point: a refresh does not do this, and the
	// generateName must not end in a dot.
	for _, want := range []string{"kubectl create -f -", "generateName: external-secrets", "freight: f08f1c9"} {
		if !strings.Contains(f.Remedy, want) {
			t.Errorf("remedy missing %q:\n%s", want, f.Remedy)
		}
	}
	if strings.Contains(f.Remedy, "generateName: external-secrets.") {
		t.Errorf("generateName must not end in a dot; the webhook rejects it:\n%s", f.Remedy)
	}
	if !strings.Contains(f.Remedy, "will NOT") {
		t.Errorf("the remedy must say that a refresh does not re-run a promotion:\n%s", f.Remedy)
	}
}

func TestAFailureTheStageAlreadyRecoveredFromIsNotAFinding(t *testing.T) {
	// The Stage is running the freight the failed promotion was carrying.
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "kyverno", CurrentFreight: "6ed70bc", Ready: true}},
		Promotions: []Promotion{{
			Stage: "kyverno", Freight: "6ed70bc", Phase: PhaseFailed, CreatedAt: ago(time.Hour),
		}},
	}
	none(t, Detect(s), KindWedged)
}

func TestARetryAfterASuccessIsNotAFinding(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "authentik", Ready: true}},
		Promotions: []Promotion{
			{Stage: "authentik", Freight: "347e7d7", Phase: PhaseAborted, CreatedAt: ago(time.Hour)},
			{Stage: "authentik", Freight: "347e7d7", Phase: PhaseSucceeded, CreatedAt: ago(3 * time.Hour)},
		},
	}
	none(t, Detect(s), KindWedged)
}

func TestAWarehouseThatStoppedLooking(t *testing.T) {
	s := &Snapshot{
		Now: now,
		Warehouses: []Warehouse{
			{Name: "bosun", Namespace: "addons", Interval: 24 * time.Hour, DiscoveredAt: ago(3 * 24 * time.Hour), Ready: true},
			{Name: "kyverno", Namespace: "addons", Interval: 24 * time.Hour, DiscoveredAt: ago(2 * time.Hour), Ready: true},
		},
		Stages: []Stage{{Name: "x", Ready: true}},
	}
	r := Detect(s)
	f := findingOf(t, r, KindStalled)
	if f.Subject != "bosun" {
		t.Fatalf("the stale one is bosun, got %s", f.Subject)
	}
	if n := len(r.Findings); n != 1 {
		t.Fatalf("a Warehouse inside its interval must not be reported; got %d findings", n)
	}
	if !strings.Contains(f.Remedy, "kargo.akuity.io/refresh") {
		t.Errorf("remedy must be the refresh annotation:\n%s", f.Remedy)
	}
}

func TestAWarehouseThatIsNotReadyBlocksRegardlessOfInterval(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "x", Ready: true}},
		Warehouses: []Warehouse{{
			Name: "trivy", Namespace: "addons", Ready: false, ReadyReason: "ArtifactDiscoveryFailed",
			ReadyMessage: "401 from ghcr.io", Interval: time.Hour, DiscoveredAt: ago(time.Minute),
		}},
	}
	f := findingOf(t, Detect(s), KindStalled)
	if f.Severity != Blocking {
		t.Errorf("a Warehouse that cannot discover is blocking, got %s", f.Severity)
	}
	if !strings.Contains(f.Detail, "401 from ghcr.io") {
		t.Errorf("the detail must carry Kargo's own message:\n%s", f.Detail)
	}
}

// Eight promotions were left running against pull requests that had been
// closed. Kargo notices, but in tens of minutes, and holds the queue meanwhile.
func TestAPromotionWaitingOnAClosedPullRequest(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "kyverno", Ready: true}},
		Promotions: []Promotion{{
			Name: "kyverno.01xyz.6ed70bc", Namespace: "addons", Stage: "kyverno",
			Phase: PhaseRunning, StartedAt: ago(90 * time.Minute),
		}},
		OpenPRs: []PullRequest{{Number: 9, Branch: "kargo/promotion/authentik.01aaa.111"}},
	}
	f := findingOf(t, Detect(s), KindOrphanedPR)
	if !strings.Contains(f.Remedy, `{"action":"terminate"}`) {
		t.Errorf("the remedy must carry the request object, not abort=true:\n%s", f.Remedy)
	}
	if !strings.Contains(f.Remedy, "silently ignored") {
		t.Errorf("the remedy must warn that abort=true does nothing:\n%s", f.Remedy)
	}
}

// Without any open pull request we cannot tell "closed underneath it" from
// "the collector could not list them". Silence is the honest answer.
func TestNoPullRequestListMeansNoOrphanClaims(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "kyverno", Ready: true}},
		Promotions: []Promotion{{
			Name: "kyverno.01xyz.6ed70bc", Stage: "kyverno", Phase: PhaseRunning, StartedAt: ago(time.Hour),
		}},
	}
	none(t, Detect(s), KindOrphanedPR)
}

func TestSupersededPullRequestsNameTheOneThatCanMerge(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "kyverno", Ready: true}},
		OpenPRs: []PullRequest{
			{Number: 41, Branch: "kargo/promotion/kyverno.01aaa.111"},
			{Number: 173, Branch: "kargo/promotion/kyverno.01bbb.222"},
			{Number: 219, Branch: "kargo/promotion/kyverno.01ccc.333"},
		},
	}
	f := findingOf(t, Detect(s), KindSupersededPR)
	if !strings.Contains(f.Detail, "#219 is current") {
		t.Errorf("the newest must be named as current:\n%s", f.Detail)
	}
	if !strings.Contains(f.Remedy, "gh pr close 41 173") {
		t.Errorf("the remedy must close the older ones only:\n%s", f.Remedy)
	}
	// The current one may be named (as the reason), but must never be closed.
	closeArgs, _, _ := strings.Cut(strings.TrimPrefix(f.Remedy, "gh pr close "), " --comment")
	for _, n := range strings.Fields(closeArgs) {
		if n == "219" {
			t.Errorf("the current pull request must not be closed:\n%s", f.Remedy)
		}
	}
}

func TestOneOpenPullRequestPerStageIsNormal(t *testing.T) {
	s := &Snapshot{
		Now:     now,
		Stages:  []Stage{{Name: "kyverno", Ready: true}},
		OpenPRs: []PullRequest{{Number: 219, Branch: "kargo/promotion/kyverno.01ccc.333"}},
	}
	none(t, Detect(s), KindSupersededPR)
}

// The pin that writes nowhere: a `yaml-update` whose key is absent succeeds
// and changes nothing, so it looks maintained forever.
func TestAPinThatWritesNowhere(t *testing.T) {
	files := map[string]map[string]bool{
		"addons/environments/production/addons/kyverno/values.yaml": {
			"webhooksCleanup.image.tag": true,
		},
	}
	s := &Snapshot{
		Now: now,
		Stages: []Stage{{Name: "kubectl", Namespace: "addons", Ready: true, Updates: []Update{{
			Path: "./repo/addons/environments/production/addons/kyverno/values.yaml",
			Keys: []string{
				"webhooksCleanup.image.tag",
				"cleanupJobs.admissionReports.image.tag",
				"cleanupJobs.updateRequests.image.tag",
			},
		}}}},
		FileHas: fileHasFrom(files),
	}
	r := Detect(s)
	f := findingOf(t, r, KindDeadPin)
	if !strings.Contains(f.Summary, "2 keys") {
		t.Errorf("both dead keys must be counted, and the live one excluded: %q", f.Summary)
	}
	if strings.Contains(f.Detail, "webhooksCleanup") {
		t.Errorf("a key the file DOES set must not be listed as dead:\n%s", f.Detail)
	}
	if r.Checked.PinsScanned != 3 {
		t.Errorf("all three pins were resolved; PinsScanned = %d", r.Checked.PinsScanned)
	}
}

func TestATargetPointingAtAFileThatMoved(t *testing.T) {
	s := &Snapshot{
		Now: now,
		Stages: []Stage{{Name: "mcpo", Namespace: "addons", Ready: true, Updates: []Update{{
			Path: "./repo/addons/gone/values.yaml", Keys: []string{"image.tag"},
		}}}},
		FileHas: fileHasFrom(map[string]map[string]bool{}),
	}
	f := findingOf(t, Detect(s), KindDeadPin)
	if !strings.Contains(f.Summary, "which this branch does not have") {
		t.Errorf("a missing file must read as a missing file: %q", f.Summary)
	}
}

// The distinction this package exists for: a sweep that could not look must
// never render as a sweep that found nothing.
func TestNoCheckoutMeansThePinCheckSaysSoRatherThanPassing(t *testing.T) {
	s := &Snapshot{
		Now: now,
		Stages: []Stage{{Name: "kubectl", Ready: true, Updates: []Update{{
			Path: "./repo/x.yaml", Keys: []string{"a.b"},
		}}}},
	}
	r := Detect(s)
	none(t, r, KindDeadPin)
	if r.Checked.PinsScanned != 0 {
		t.Fatal("nothing was scanned")
	}
	joined := strings.Join(r.Checked.Notes, " ")
	if !strings.Contains(joined, "pins were not checked") {
		t.Fatalf("the report must admit the pin check did not run: %v", r.Checked.Notes)
	}
}

func TestACleanSweepIsNotTheSameAsAnEmptyOne(t *testing.T) {
	empty := Detect(&Snapshot{Now: now})
	if empty.Clean() {
		t.Fatal("a sweep that examined no Stages has not proved anything clean")
	}
	real := Detect(&Snapshot{Now: now, Stages: []Stage{{Name: "x", Ready: true}}})
	if !real.Clean() {
		t.Fatalf("a sweep that looked and found nothing is clean: %v", kinds(real))
	}
}

func TestFindingsReadWorstFirstAndStably(t *testing.T) {
	s := &Snapshot{
		Now: now,
		Stages: []Stage{
			{Name: "zeta", Ready: true},
			{Name: "alpha", Ready: true},
		},
		OpenPRs: []PullRequest{
			{Number: 1, Branch: "kargo/promotion/zeta.01a.1"},
			{Number: 2, Branch: "kargo/promotion/zeta.01b.2"},
			{Number: 3, Branch: "kargo/promotion/alpha.01a.1"},
			{Number: 4, Branch: "kargo/promotion/alpha.01b.2"},
		},
		Promotions: []Promotion{
			{Name: "zeta.01c.3", Stage: "zeta", Freight: "f", Phase: PhaseErrored, CreatedAt: ago(time.Hour)},
		},
	}
	r := Detect(s)
	if r.Findings[0].Severity != Blocking {
		t.Fatalf("blocking first, got %v", kinds(r))
	}
	// Same severity and kind: alphabetical by subject, so two sweeps of an
	// unchanged cluster produce byte-identical reports.
	var subjects []string
	for _, f := range r.Findings {
		if f.Kind == KindSupersededPR {
			subjects = append(subjects, f.Subject)
		}
	}
	if fmt.Sprint(subjects) != "[alpha zeta]" {
		t.Fatalf("stable order expected, got %v", subjects)
	}
}

func fileHasFrom(files map[string]map[string]bool) func(string, string) (bool, error) {
	return func(path, key string) (bool, error) {
		keys, ok := files[path]
		if !ok {
			return false, fmt.Errorf("no such file: %s", path)
		}
		if key == "" {
			return true, nil
		}
		return keys[key], nil
	}
}

// A verification that failed is over. Kargo does not re-run it, that freight
// has been verified and the answer was no, so the Stage sits Ready=False
// forever while every Application stays Synced and Healthy. Three Stages were
// held that way for three days.
func TestAFinishedVerificationIsBlockingAndNamesItsId(t *testing.T) {
	s := &Snapshot{
		Now: now,
		Stages: []Stage{{
			Name: "cert-manager", Namespace: "addons", Ready: false,
			ReadyReason: "VerificationError", ReadySince: 72 * time.Hour,
			ReadyMessage:      `dial tcp 10.106.179.157:9090: connect: no route to host`,
			VerificationID:    "a0108a58-5a02-4486-9699-14c6f9ee4458",
			VerificationPhase: "Error",
		}},
	}
	f := findingOf(t, Detect(s), KindVerifyStuck)
	if f.Severity != Blocking {
		t.Errorf("a Stage that stopped promoting is blocking, got %s", f.Severity)
	}
	if !strings.Contains(f.Summary, "will not re-run it") {
		t.Errorf("the summary must say it is over, not that it is still running: %q", f.Summary)
	}
	if !strings.Contains(f.Remedy, `kargo.akuity.io/reverify={"id":"a0108a58-5a02-4486-9699-14c6f9ee4458"}`) {
		t.Errorf("the remedy must carry the id; it is three levels deeper than anyone looks:\n%s", f.Remedy)
	}
	// The whole lesson from fixing the NetworkPolicy and watching nothing move.
	if !strings.Contains(f.Remedy, "not enough") {
		t.Errorf("the remedy must say that fixing the cause does not restart it:\n%s", f.Remedy)
	}
}

// Still running is a different, softer situation, and only after long enough.
func TestAVerificationStillRunningIsOnlyReportedOnceItIsLate(t *testing.T) {
	running := func(since time.Duration) *Snapshot {
		return &Snapshot{Now: now, Stages: []Stage{{
			Name: "argo-cd", Ready: false, ReadyReason: "VerificationRunning",
			ReadySince: since, VerificationPhase: "Running",
		}}}
	}
	none(t, Detect(running(10*time.Minute)), KindVerifyStuck)

	f := findingOf(t, Detect(running(2*time.Hour)), KindVerifyStuck)
	if f.Severity != Degraded {
		t.Errorf("still running is degraded, not blocking; got %s", f.Severity)
	}
	if strings.Contains(f.Remedy, "reverify") {
		t.Errorf("a verification that has not finished must not be told to re-run:\n%s", f.Remedy)
	}
}

// Without an id the remedy still has to be runnable: it says how to find it.
func TestAMissingVerificationIdStillYieldsRunnableSteps(t *testing.T) {
	s := &Snapshot{Now: now, Stages: []Stage{{
		Name: "x", Namespace: "addons", Ready: false, ReadyReason: "VerificationFailed",
		ReadySince: time.Hour, VerificationPhase: "Failed",
	}}}
	f := findingOf(t, Detect(s), KindVerifyStuck)
	if !strings.Contains(f.Remedy, "verificationHistory[0].id") {
		t.Fatalf("it must say where the id lives:\n%s", f.Remedy)
	}
}

// "Not open" covers MERGED as well as closed, and the seconds between a merge
// and Kargo noticing it are exactly when a promotion is doing the right thing.
// Reporting that window would be a false alarm on every successful promotion.
//
// Caught by running it: the first live sweep flagged bosun's own promotion
// three minutes after its pull request merged.
func TestAPromotionIsNotStrandedTheMomentItsPullRequestMerges(t *testing.T) {
	snap := func(age time.Duration) *Snapshot {
		return &Snapshot{
			Now:    now,
			Stages: []Stage{{Name: "bosun", Ready: true}},
			Promotions: []Promotion{{
				Name: "bosun.01xyz.8e21911", Namespace: "addons", Stage: "bosun",
				Phase: PhaseRunning, StartedAt: ago(age),
			}},
			// Some other Stage's pull request is open, so the list is real.
			OpenPRs: []PullRequest{{Number: 9, Branch: "kargo/promotion/other.01a.1"}},
		}
	}
	none(t, Detect(snap(3*time.Minute)), KindOrphanedPR)

	// A stranded one waits indefinitely; the ones observed live had
	// been running for hours.
	f := findingOf(t, Detect(snap(6*time.Hour)), KindOrphanedPR)
	if f.Severity != Blocking {
		t.Errorf("a promotion holding a queue against a dead pull request is blocking, got %s", f.Severity)
	}
}

// Pending is the phase with no symptom: the Stage reports no error and every
// Application it manages stays Synced and Healthy on the version it already
// had, while the promotion that would move it never starts. Nothing about that
// produces an event, which is why the supervisor is a timer.
func TestAPromotionQueuedTooLongIsBlocking(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "cert-manager", Namespace: "addons", Ready: true}},
		Promotions: []Promotion{{
			Name: "cert-manager.01", Namespace: "addons", Stage: "cert-manager",
			Phase: PhasePending, CreatedAt: now.Add(-4 * time.Hour),
		}},
	}
	f := findingOf(t, Detect(s), KindPendingStuck)
	if f.Severity != Blocking {
		t.Errorf("a queue that never drains is the pipeline stopped, got %s", f.Severity)
	}
	if !strings.Contains(f.Summary, "cert-manager") {
		t.Errorf("the summary must name the Stage: %q", f.Summary)
	}
	if !strings.Contains(f.Remedy, "cert-manager.01") {
		t.Errorf("the remedy must name the promotion to look at: %q", f.Remedy)
	}
}

// The grace matters as much as the detection. A promotion queued behind a
// normal verification clears in minutes, and reporting that window is a false
// alarm on every healthy promotion the repository makes.
func TestARecentlyQueuedPromotionIsNotAFinding(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "cert-manager", Namespace: "addons", Ready: true}},
		Promotions: []Promotion{{
			Name: "cert-manager.01", Namespace: "addons", Stage: "cert-manager",
			Phase: PhasePending, CreatedAt: now.Add(-10 * time.Minute),
		}},
	}
	for _, f := range Detect(s).Findings {
		if f.Kind == KindPendingStuck {
			t.Fatalf("a promotion queued ten minutes ago is normal: %q", f.Summary)
		}
	}
}

// A wedged queue backs up behind one blocker. Reporting each promotion in it
// separately turns one problem into a page of them.
func TestAQueuedStageProducesOneFindingOnTheOldest(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "cert-manager", Namespace: "addons", Ready: true}},
		Promotions: []Promotion{
			{Name: "newest", Namespace: "addons", Stage: "cert-manager",
				Phase: PhasePending, CreatedAt: now.Add(-3 * time.Hour)},
			{Name: "oldest", Namespace: "addons", Stage: "cert-manager",
				Phase: PhasePending, CreatedAt: now.Add(-9 * time.Hour)},
			{Name: "middle", Namespace: "addons", Stage: "cert-manager",
				Phase: PhasePending, CreatedAt: now.Add(-5 * time.Hour)},
		},
	}
	var found []Finding
	for _, f := range Detect(s).Findings {
		if f.Kind == KindPendingStuck {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want one finding for the Stage, got %d", len(found))
	}
	if !strings.Contains(found[0].Remedy, "oldest") {
		t.Errorf("the finding must point at the oldest queued promotion: %q", found[0].Remedy)
	}
	if !strings.Contains(found[0].Detail, "3 promotions are queued") {
		t.Errorf("the detail must say how deep the queue is: %q", found[0].Detail)
	}
}

// The two phase vocabularies overlap on "Failed" and "Aborted" and differ
// everywhere else, and the near-miss is the trap: a promotion says "Errored",
// a verification says "Error". Tidying one set into the other would silently
// stop this detector firing.
func TestVerificationPhasesAreNotPromotionPhases(t *testing.T) {
	if VerifyError == PhaseErrored {
		t.Fatal("these are different words on purpose: Error vs Errored")
	}
	for _, terminal := range []string{VerifyFailed, VerifyError, VerifyAborted, VerifyInconclusive} {
		if !isTerminalVerification(terminal) {
			t.Errorf("%q is terminal: nothing re-runs it, so the Stage is stuck", terminal)
		}
	}
	for _, running := range []string{"Running", "Pending", "Successful", PhaseErrored, ""} {
		if isTerminalVerification(running) {
			t.Errorf("%q must not read as a finished verification", running)
		}
	}
}
