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
// error, a terminal promotion, and no retry -- while every Application stayed
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
	// The remedy is the whole point: a refresh does NOT do this, and the
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
	// The current one may be NAMED (as the reason), but must never be closed.
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
