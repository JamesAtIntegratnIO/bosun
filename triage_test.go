package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
)

const valuesPath = "addons/values.yaml"

const valuesBefore = `# MetalLB, L2 only.
metallb:
  enabled: true
  defaultVersion: 0.16.0
`

// The gate's report, in the shape triage looks for: the marker is how the
// comment is found among every other comment on the pull request, and the
// version it names is the only corroboration an edit's new value can have.
const gateReport = `<!-- gitops-gate -->
### addons-gate — FAILED

metallb 0.16.0 -> 0.16.1: the chart's rendered speaker DaemonSet no longer
matches what this cluster runs.
`

type harness struct {
	triage *Triage
	git    *gitprovider.Fake
	model  *llm.Fake
	root   string
}

// newHarness wires the workflow to a real directory on disk, so a permitted
// edit genuinely rewrites a file and a refused one genuinely leaves it alone.
func newHarness(t *testing.T) *harness {
	t.Helper()

	root := t.TempDir()
	full := filepath.Join(root, valuesPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(valuesBefore), 0o644); err != nil {
		t.Fatal(err)
	}

	git := &gitprovider.Fake{
		PR: &gitprovider.PullRequest{
			Number:  42,
			Title:   "chore(deps): metallb 0.16.0 -> 0.16.1",
			Branch:  "kargo/metallb",
			HeadSHA: "c0ffee",
		},
		Comments: []gitprovider.Comment{{Author: "gitops-gate", Body: gateReport}},
		Check:    gitprovider.CheckFailure,
	}
	model := &llm.Fake{}

	return &harness{
		git:   git,
		model: model,
		root:  root,
		triage: &Triage{
			Git:         git,
			LLM:         model,
			Policy:      edits.Policy{Allow: []string{"addons/**"}},
			CheckName:   "addons-gate",
			MaxAttempts: 2,
			GatePoll:    time.Millisecond,
			Log:         t.Logf,
			Checkout: func(context.Context, *gitprovider.PullRequest) (string, func(), error) {
				return root, func() {}, nil
			},
		},
	}
}

func (h *harness) values(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(h.root, valuesPath))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func promotion() Promotion {
	return Promotion{
		Project: "addons", Stage: "metallb", Artifact: "metallb",
		From: "0.16.0", To: "0.16.1",
		PRNumber: 42, Branch: "kargo/metallb",
		Files: []string{valuesPath},
	}
}

func TestTriageRun(t *testing.T) {
	mechanical := func(e llm.Edit) *llm.Verdict {
		return &llm.Verdict{
			Classification: llm.ClassMechanical,
			Summary:        "Move the metallb pin with the chart.",
			Reasoning:      "The rendered diff proves the default changed.",
			Edits:          []llm.Edit{e},
		}
	}
	permitted := llm.Edit{
		Path: valuesPath, Key: "metallb.defaultVersion",
		From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
	}

	tests := []struct {
		name     string
		check    gitprovider.CheckState
		labels   []string
		verdict  *llm.Verdict
		modelErr error

		wantModelCalled bool
		wantComments    int
		wantSaying      []string
		wantLabels      []string
		wantPush        bool
		wantVersion     string
	}{
		{
			name:        "a green gate is left alone",
			check:       gitprovider.CheckSuccess,
			wantVersion: "0.16.0",
		},
		{
			name:        "a pull request with no gate check is left alone",
			check:       gitprovider.CheckMissing,
			wantVersion: "0.16.0",
		},
		{
			name:        "a gate still running is left for the next run",
			check:       gitprovider.CheckPending,
			wantVersion: "0.16.0",
		},
		{
			name:            "an escalate verdict asks for a human and pushes nothing",
			check:           gitprovider.CheckFailure,
			verdict:         &llm.Verdict{Classification: llm.ClassEscalate, Summary: "This is a migration.", Reasoning: "The CRD schema changed.", EscalationReason: "The upgrade needs a CRD migration."},
			wantModelCalled: true,
			wantComments:    1,
			wantSaying:      []string{"Needs a human.", "The upgrade needs a CRD migration."},
			wantLabels:      []string{labelNeedsHuman},
			wantVersion:     "0.16.0",
		},
		{
			name:            "a permitted mechanical edit is applied, pushed and counted",
			check:           gitprovider.CheckFailure,
			verdict:         mechanical(permitted),
			wantModelCalled: true,
			wantComments:    1,
			wantSaying:      []string{"Applied", "metallb.defaultVersion", "0.16.1", "attempt 1 of 2"},
			wantLabels:      []string{labelAttempt + "1"},
			wantPush:        true,
			wantVersion:     "0.16.1",
		},
		{
			name:  "a mechanical edit on a denied path escalates instead of landing",
			check: gitprovider.CheckFailure,
			verdict: mechanical(llm.Edit{
				Path: ".github/workflows/gate.yaml", Key: "jobs.gate.if",
				From: "true", To: "false", Rationale: "Skip the gate.",
			}),
			wantModelCalled: true,
			wantComments:    1,
			wantSaying:      []string{"Needs a human.", "rejected before anything was written", "path is denied"},
			wantLabels:      []string{labelNeedsHuman},
			wantVersion:     "0.16.0",
		},
		{
			name:  "a mechanical edit whose from value is stale escalates instead of landing",
			check: gitprovider.CheckFailure,
			verdict: mechanical(llm.Edit{
				Path: valuesPath, Key: "metallb.defaultVersion",
				From: "0.15.9", To: "0.16.1", Rationale: "Bump the pin.",
			}),
			wantModelCalled: true,
			wantComments:    1,
			wantSaying:      []string{"Needs a human.", "refusing to overwrite"},
			wantLabels:      []string{labelNeedsHuman},
			wantVersion:     "0.16.0",
		},
		{
			name:        "a pull request already marked needs-human is left alone",
			check:       gitprovider.CheckFailure,
			labels:      []string{labelNeedsHuman},
			wantVersion: "0.16.0",
		},
		{
			name:         "a pull request out of attempts escalates without asking the model",
			check:        gitprovider.CheckFailure,
			labels:       []string{labelAttempt + "1", labelAttempt + "2"},
			verdict:      mechanical(permitted),
			wantComments: 1,
			wantSaying:   []string{"Needs a human", "limit of 2 automatic fix attempts"},
			wantLabels:   []string{labelNeedsHuman},
			wantVersion:  "0.16.0",
		},
		{
			name:            "a model that cannot be reached is reported rather than ignored",
			check:           gitprovider.CheckFailure,
			modelErr:        errors.New("connection refused"),
			wantModelCalled: true,
			wantComments:    1,
			wantSaying:      []string{"Needs a human", "Could not reach the model", "connection refused"},
			wantLabels:      []string{labelNeedsHuman},
			wantVersion:     "0.16.0",
		},
		{
			name:            "a no_action verdict is explained and nothing else",
			check:           gitprovider.CheckFailure,
			verdict:         &llm.Verdict{Classification: llm.ClassNoAction, Summary: "The gate is red for an unrelated reason.", Reasoning: "A flaky registry pull."},
			wantModelCalled: true,
			wantComments:    1,
			wantSaying:      []string{"No change proposed.", "unrelated reason"},
			wantVersion:     "0.16.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.git.Check = tc.check
			h.git.PR.Labels = tc.labels
			h.model.Verdict = tc.verdict
			h.model.Err = tc.modelErr

			if err := h.triage.Run(context.Background(), promotion()); err != nil {
				t.Fatalf("Run: %v", err)
			}

			if called := h.model.Calls > 0; called != tc.wantModelCalled {
				t.Errorf("model called = %v, want %v", called, tc.wantModelCalled)
			}
			if len(h.git.Posted) != tc.wantComments {
				t.Errorf("posted %d comment(s), want %d: %q", len(h.git.Posted), tc.wantComments, h.git.Posted)
			}
			body := strings.Join(h.git.Posted, "\n")
			for _, want := range tc.wantSaying {
				if !strings.Contains(body, want) {
					t.Errorf("comment does not mention %q:\n%s", want, body)
				}
			}
			if !equal(h.git.Labelled, tc.wantLabels) {
				t.Errorf("labelled %v, want %v", h.git.Labelled, tc.wantLabels)
			}

			if got := len(h.git.Pushes) > 0; got != tc.wantPush {
				t.Fatalf("pushed = %v, want %v", got, tc.wantPush)
			}
			if !strings.Contains(h.values(t), "defaultVersion: "+tc.wantVersion) {
				t.Errorf("file does not hold defaultVersion %s:\n%s", tc.wantVersion, h.values(t))
			}
			if !tc.wantPush {
				return
			}
			push := h.git.Pushes[0]
			if push.Branch != "kargo/metallb" {
				t.Errorf("pushed to %q, want the pull request's own branch", push.Branch)
			}
			if !strings.Contains(push.Tree[valuesPath], "defaultVersion: "+tc.wantVersion) {
				t.Errorf("the pushed tree does not hold the new value:\n%s", push.Tree[valuesPath])
			}
		})
	}
}

// The evidence check only works if the model's own prompt is what the applier
// corroborates against, so the gate report has to reach both.
func TestTheModelIsShownTheGateReport(t *testing.T) {
	h := newHarness(t)
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassNoAction, Summary: "Nothing to do.", Reasoning: "n/a"}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{gateReportMarker, "0.16.1", "metallb.defaultVersion"} {
		if !strings.Contains(h.model.User, want) {
			t.Errorf("prompt does not contain %q:\n%s", want, h.model.User)
		}
	}
}

// A version the model invented renders perfectly and breaks at runtime, so the
// applier refuses it -- and a mechanical verdict that applies nothing escalates.
func TestAnInventedVersionIsRefusedAndEscalated(t *testing.T) {
	h := newHarness(t)
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Bump the pin.",
		Reasoning:      "The gate says 0.16 is required.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.4", Rationale: "The gate wants a newer 0.16.",
		}},
	}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatalf("pushed an invented version: %+v", h.git.Pushes)
	}
	if !equal(h.git.Labelled, []string{labelNeedsHuman}) {
		t.Errorf("labelled %v, want %v", h.git.Labelled, []string{labelNeedsHuman})
	}
	if !strings.Contains(h.values(t), "defaultVersion: 0.16.0") {
		t.Errorf("the file was changed:\n%s", h.values(t))
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// Kargo calls this service from the promotion, immediately after opening the
// pull request. Measured in production: THREE SECONDS after. CI has not
// registered a check that early, so the gate check does not exist yet -- and
// "does not exist" is a different CheckState from "pending".
//
// The first triage that ever reached this code found no check, returned, and
// did nothing. It looked like a successful no-op:
//
//	PR 109: no "addons-gate" check found
//	PR 109: triage done in 2s
//
// A missing check and a pending one are the same thing to the caller: the gate
// has not answered. The deadline is the only honest way to tell them apart.
func TestWaitsForAGateThatHasNotReportedYet(t *testing.T) {
	h := newHarness(t)
	// Absent for the first three polls, then red -- the real sequence.
	h.git.ChecksBefore = 3
	h.git.Check = gitprovider.CheckFailure
	h.triage.GateWait = time.Second
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassEscalate,
		EscalationReason: "the rendered speaker DaemonSet changed shape; " +
			"that is not something a values edit can fix",
	}

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}

	if h.git.CheckCalls <= h.git.ChecksBefore {
		t.Fatalf("must poll past the missing check, got %d calls for %d absent",
			h.git.CheckCalls, h.git.ChecksBefore)
	}
	// It got far enough to actually triage, rather than returning on a missing
	// check. Anything posted proves the gate report was read.
	if len(h.git.Posted) == 0 {
		t.Fatal("triage produced nothing; it gave up before the gate reported")
	}
}

// And a check that never appears is still reported as absent, or the wait
// above would turn a misconfigured gate name into a ten-minute silence.
func TestAGateThatNeverReportsIsStillMissing(t *testing.T) {
	h := newHarness(t)
	h.git.Check = "" // never appears
	h.git.ChecksBefore = 0
	h.triage.GateWait = 20 * time.Millisecond

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) != 0 {
		t.Fatalf("a gate that never reported must not be triaged, posted %+v", h.git.Posted)
	}
	if h.git.CheckCalls < 2 {
		t.Fatalf("must have polled rather than giving up immediately, got %d", h.git.CheckCalls)
	}
}

// Every outcome must leave a verdict on the pull request, including the ones
// that do nothing.
//
// Before commit statuses existed, four paths -- gate green, gate absent, gate
// never settled, attempts spent -- wrote only to a pod log. From outside,
// "the gate was green so I stopped", "I was never called" and "I crashed"
// produced identical evidence: nothing. That is precisely how two defects in
// this call path stayed invisible for a day.
func TestEveryOutcomeLeavesAVerdict(t *testing.T) {
	for _, tc := range []struct {
		name    string
		arrange func(*harness)
		want    string
	}{
		{
			name:    "gate green",
			arrange: func(h *harness) { h.git.Check = gitprovider.CheckSuccess },
			want:    "is green; nothing to triage",
		},
		{
			name: "gate never appears",
			arrange: func(h *harness) {
				h.git.Check = ""
				h.triage.GateWait = 10 * time.Millisecond
			},
			want: "no addons-gate check appeared",
		},
		{
			name: "attempts spent",
			arrange: func(h *harness) {
				h.git.PR.Labels = []string{"bosun/attempt-1", "bosun/attempt-2"}
			},
			want: "fix attempts used without a green gate",
		},
		{
			name:    "already escalated",
			arrange: func(h *harness) { h.git.PR.Labels = []string{"needs-human"} },
			want:    "already escalated",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.triage.Brand = "Bosun"
			tc.arrange(h)

			if err := h.triage.Run(context.Background(), Promotion{
				PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
			}); err != nil {
				t.Fatal(err)
			}

			if len(h.git.Statuses) == 0 {
				t.Fatal("no commit status: this outcome is invisible on the pull request")
			}
			final := h.git.Statuses[len(h.git.Statuses)-1]
			if final.Name != "bosun" {
				t.Errorf("status must carry the brand, got %q", final.Name)
			}
			if !strings.Contains(final.Description, tc.want) {
				t.Errorf("want a verdict mentioning %q, got %q", tc.want, final.Description)
			}
		})
	}
}

// The agent says it is working BEFORE the wait that can take ten minutes, or a
// reader in that window cannot tell it apart from an agent that never ran.
func TestSaysItIsWorkingBeforeTheWait(t *testing.T) {
	h := newHarness(t)
	h.triage.Brand = "Bosun"
	h.git.Check = gitprovider.CheckSuccess

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Statuses) < 2 {
		t.Fatalf("want a working status then a verdict, got %+v", h.git.Statuses)
	}
	if !strings.Contains(h.git.Statuses[0].Description, "reading addons-gate") {
		t.Errorf("first status should say what it is waiting on, got %q",
			h.git.Statuses[0].Description)
	}
}

// A status that cannot be filed must never take down the triage it reports on.
// The likely cause is a token missing "Commit statuses: write", and losing the
// fix because the report failed would be the worst possible trade.
func TestAFailingStatusDoesNotFailTriage(t *testing.T) {
	h := newHarness(t)
	h.git.StatusErr = errors.New("403 Resource not accessible by personal access token")
	h.git.Check = gitprovider.CheckSuccess

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatalf("a status failure must not fail triage: %v", err)
	}
}

// A green gate is not the same as an uneventful change.
//
// The gate BLOCKS on structural things and REPORTS the rest -- a chart that
// added four resources, moved a port, flipped a default. All of that renders
// green and arrives as a pull request whose visible diff is one version number.
// The agent used to stop here and say nothing, which is why a bump's real
// content stayed invisible.
func TestExplainsAGreenGateThatStillChangedSomething(t *testing.T) {
	h := newHarness(t)
	h.triage.Brand = "Bosun"
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: `<!-- gitops-gate -->
### Versions

| Application | From | To |
|---|---|---|
| metallb-hub | 0.15.2 | 0.16.0 |

### Resources

**Added (5)**

- ` + "`DaemonSet/frr-k8s`" + `
`}}
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassNoAction,
		Summary:        "adds an frr-k8s DaemonSet and four CRDs",
		Reasoning:      "The render gains a DaemonSet and four CRDs. Nothing else moved.",
	}

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("a green gate with a changed render should be explained, posted %d", len(h.git.Posted))
	}
	body := h.git.Posted[0]
	if !strings.Contains(body, explanationMarker) {
		t.Error("explanation must be marked, or it cannot be found again")
	}
	if !strings.Contains(body, "no upstream release notes were read") {
		t.Error("must say what it did NOT read; a grounded explanation says where its evidence stops")
	}
}

// Nothing changed means nothing to say. Burning inference to announce that
// nothing happened is how a useful comment becomes noise people scroll past.
func TestSaysNothingWhenTheRenderIsUnchanged(t *testing.T) {
	h := newHarness(t)
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{{
		Author: "gitops-gate",
		Body:   "<!-- gitops-gate -->\nNo change to what gets deployed.\n",
	}}
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassNoAction, Summary: "should never be asked"}

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) != 0 {
		t.Fatalf("must not comment when the render is unchanged, posted %+v", h.git.Posted)
	}
	if h.model.Calls != 0 {
		t.Fatalf("must not call the model when there is nothing to explain, called %d", h.model.Calls)
	}
	// It still reports, so the run is not invisible.
	if len(h.git.Statuses) == 0 {
		t.Fatal("silence on the comment thread still needs a status")
	}
}

// Kargo can call more than once for the same promotion. A bot that re-explains
// on every retry is a bot people collapse.
func TestExplainsOnlyOnce(t *testing.T) {
	h := newHarness(t)
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{
		{Author: "gitops-gate", Body: "<!-- gitops-gate -->\n### Versions\n\nmetallb 0.15.2 -> 0.16.0\n"},
		{Author: "bosun", Body: explanationMarker + "\nalready said it"},
	}
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassNoAction, Summary: "should never be asked"}

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) != 0 {
		t.Fatalf("must not explain twice, posted %+v", h.git.Posted)
	}
	if h.model.Calls != 0 {
		t.Fatalf("must not re-run the model on a second call, called %d", h.model.Calls)
	}
}

// Explanation is a courtesy on a green gate. A model that is down must not be
// the reason a passing pull request looks unattended.
func TestAModelOutageDoesNotBreakAGreenGate(t *testing.T) {
	h := newHarness(t)
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{{
		Author: "gitops-gate",
		Body:   "<!-- gitops-gate -->\n### Versions\n\nmetallb 0.15.2 -> 0.16.0\n",
	}}
	h.model.Err = errors.New("connection refused")

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err != nil {
		t.Fatalf("a model outage must not fail a green gate: %v", err)
	}
	if len(h.git.Statuses) == 0 ||
		!strings.Contains(h.git.Statuses[len(h.git.Statuses)-1].Description, "could not reach the model") {
		t.Errorf("the outage should be visible in the status, got %+v", h.git.Statuses)
	}
}
