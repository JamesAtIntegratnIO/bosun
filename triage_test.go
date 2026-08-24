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
	"github.com/JamesAtIntegratnIO/bosun/upstream"
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
			wantSaying:      []string{"Needs a human.", "This is a migration.", "The CRD schema changed."},
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
// The escalation reason is the commit status's line, not the comment's: it is
// reliably a paraphrase of the summary printed right below the headline, and
// printing both had every escalation announcing itself twice before the
// reasoning announced it a third time.
func TestTheEscalationReasonStaysOnTheStatus(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	h.model.Verdict = &llm.Verdict{
		Classification:   llm.ClassEscalate,
		Summary:          "Decide whether to accept the PodDisruptionBudget migration.",
		Reasoning:        "Nothing in the editable list can express an apiVersion move.",
		EscalationReason: "apiVersion migration on a PodDisruptionBudget",
	}
	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("want one comment, got %v", h.git.Posted)
	}
	if strings.Contains(h.git.Posted[0], "apiVersion migration on a PodDisruptionBudget") {
		t.Errorf("the escalation reason belongs on the status, not in the comment:\n%s", h.git.Posted[0])
	}
	var sawReason bool
	for _, s := range h.git.Statuses {
		if strings.Contains(s.Description, "apiVersion migration on a PodDisruptionBudget") {
			sawReason = true
		}
	}
	if !sawReason {
		t.Error("the escalation reason must still reach the commit status")
	}
}

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
	// The colour is the half that matters. A green "reading addons-gate" is
	// indistinguishable from a finished run with nothing to say.
	if h.git.Statuses[0].State != gitprovider.StatePending {
		t.Errorf("the working status must be pending, got %q", h.git.Statuses[0].State)
	}
	last := h.git.Statuses[len(h.git.Statuses)-1]
	if last.State != gitprovider.StateSuccess {
		t.Errorf("the verdict must resolve the status, got %q", last.State)
	}
}

// An error anywhere in triage must still resolve the status. Otherwise the
// pending written on entry never clears, and "the gate broke" looks like "the
// agent is still thinking" -- for ever.
//
// The live shape of this: `render` fails, the job that publishes the gate
// report is skipped, and gateReport finds a red check with nothing explaining
// it. Before this, that reached a pod log and nowhere else.
func TestAnErrorResolvesTheStatus(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	h.git.Comments = nil // a red gate that published no report

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
	}); err == nil {
		t.Fatal("want an error when the gate is red with no report")
	}
	if len(h.git.Statuses) == 0 {
		t.Fatal("want a status")
	}
	last := h.git.Statuses[len(h.git.Statuses)-1]
	if last.State != gitprovider.StateSuccess {
		t.Errorf("a failed run must still resolve the status, got %q", last.State)
	}
	if !strings.Contains(last.Description, "did not finish") {
		t.Errorf("the status should say triage did not finish, got %q", last.Description)
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
	// The exact wording moves; the property must not. An explanation with no
	// upstream context has to say its evidence is the render alone, or a reader
	// credits it with sources it never had.
	if !strings.Contains(body, "render diff ONLY") {
		t.Errorf("must say where its evidence stops, got:\n%s", body)
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

// fakeUpstream stands in for the maintainers' own words.
type fakeUpstream struct {
	notes *upstream.Notes
	err   error
	calls int
}

func (f *fakeUpstream) Name() string { return "fake-upstream" }
func (f *fakeUpstream) Notes(context.Context, string, string, string) (*upstream.Notes, error) {
	f.calls++
	return f.notes, f.err
}

// With release notes, the explanation can say WHY -- and the comment has to
// show its working, because "grounded in the render" and "grounded in the
// render plus what the maintainers wrote" are very different claims.
func TestAnExplanationCitesItsUpstreamSource(t *testing.T) {
	h := newHarness(t)
	h.triage.Brand = "Bosun"
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{{
		Author: "gitops-gate",
		Body:   "<!-- gitops-gate -->\n### Resources\n\n**Added (5)**\n\n- `DaemonSet/frr-k8s`\n",
	}}
	up := &fakeUpstream{notes: &upstream.Notes{
		SourceRepo: "metallb/metallb",
		Releases: []upstream.Release{
			{Tag: "v0.16.0", Body: "FRR is now a separate DaemonSet rather than a sidecar."},
		},
		Note: "Upstream notes from metallb/metallb.",
	}}
	h.triage.Upstream = up
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassNoAction,
		Summary:        "FRR moves from sidecars to its own DaemonSet",
		Reasoning:      "Upstream split FRR out; that is the five new resources.",
	}

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
		Artifact: "quay.io/metallb/controller", From: "0.15.2", To: "0.16.0",
	}); err != nil {
		t.Fatal(err)
	}
	if up.calls != 1 {
		t.Fatalf("upstream should be consulted exactly once, got %d", up.calls)
	}
	body := h.git.Posted[0]
	if !strings.Contains(body, "metallb/metallb") {
		t.Errorf("must cite where the notes came from, got:\n%s", body)
	}
	if strings.Contains(body, "render diff ONLY") {
		t.Errorf("must not claim render-only when it had upstream notes, got:\n%s", body)
	}
}

// Upstream lookup is best-effort. A rate limit, an unreachable registry or an
// artifact with no source label must degrade to the render-only explanation --
// which is exactly what this did before upstream notes existed, and is still
// worth posting.
func TestUpstreamFailureDegradesToRenderOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		up   *fakeUpstream
	}{
		{"resolver errors", &fakeUpstream{err: errors.New("403 rate limited")}},
		{"no source label", &fakeUpstream{notes: &upstream.Notes{
			Note: "No upstream release notes: publishes no org.opencontainers.image.source."}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.triage.Explain = true
			h.triage.Upstream = tc.up
			h.git.Check = gitprovider.CheckSuccess
			h.git.Comments = []gitprovider.Comment{{
				Author: "gitops-gate",
				Body:   "<!-- gitops-gate -->\n### Resources\n\n**Added (5)**\n",
			}}
			h.model.Verdict = &llm.Verdict{
				Classification: llm.ClassNoAction,
				Summary:        "five resources appear",
				Reasoning:      "The render gains five resources. The report does not say why.",
			}

			if err := h.triage.Run(context.Background(), Promotion{
				PRNumber: 42, Branch: "kargo/metallb", Files: []string{valuesPath},
			}); err != nil {
				t.Fatalf("an upstream failure must not fail the explanation: %v", err)
			}
			if len(h.git.Posted) != 1 {
				t.Fatalf("should still explain from the render alone, posted %d", len(h.git.Posted))
			}
			if !strings.Contains(h.git.Posted[0], "render diff ONLY") {
				t.Errorf("must say the evidence was render-only, got:\n%s", h.git.Posted[0])
			}
		})
	}
}

// A green gate is a verdict on the RENDER, not on the bump. Measured against
// four real held promotions: kyverno 3.2.8 -> 3.9.0 was escalated correctly and
// precisely, but only because its PodDisruptionBudget migration turned the gate
// red. external-secrets 0.10.3 -> 2.9.0 -- the more dangerous of the two --
// rendered GREEN, and this path was pinned to no_action, so the same model
// produced an accurate inventory and said nothing about the risk.
func TestAGreenGateCanStillAskForAHuman(t *testing.T) {
	h := newHarness(t)
	h.triage.Brand = "Bosun"
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: `<!-- gitops-gate -->
### Versions

| Application | From | To |
|---|---|---|
| external-secrets | 0.10.3 | 2.9.0 |

### Resources

**Changed (25)**
`}}
	h.model.Verdict = &llm.Verdict{
		Classification:   llm.ClassEscalate,
		Summary:          "external-secrets 0.10.3 to 2.9.0 crosses two major versions",
		Reasoning:        "The render changes 25 resources including the CRDs that serve the API this repository's manifests declare.",
		EscalationReason: "a two-major-version jump that changes the CRDs serving an API the repository still declares",
	}

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/eso", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}
	if !has(h.git.Labelled, labelNeedsHuman) {
		t.Errorf("a flagged green gate must label the pull request, got %v", h.git.Labelled)
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("want one comment, got %d", len(h.git.Posted))
	}
	// The flag has to survive a reader who stops at the first bold line.
	if !strings.Contains(h.git.Posted[0], "Worth a look before merging") {
		t.Errorf("the flag must lead the comment, got:\n%s", h.git.Posted[0])
	}
	final := h.git.Statuses[len(h.git.Statuses)-1]
	if final.State != gitprovider.StateSuccess {
		t.Errorf("flagging must not fail the status -- the agent is advisory, got %q", final.State)
	}
	if !strings.Contains(final.Description, "flagged") {
		t.Errorf("the status should say it flagged, got %q", final.Description)
	}
}

// The other half of the same rule: a routine bump must NOT be flagged, or the
// label stops meaning anything and people stop reading it.
func TestARoutineGreenBumpIsNotFlagged(t *testing.T) {
	h := newHarness(t)
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: "<!-- gitops-gate -->\n### Resources\n\n**Changed (1)**\n"}}
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassNoAction,
		Summary:        "bumps its own image tag",
		Reasoning:      "One container image moved. Nothing else changed.",
	}

	if err := h.triage.Run(context.Background(), Promotion{
		PRNumber: 42, Branch: "kargo/thing", Files: []string{valuesPath},
	}); err != nil {
		t.Fatal(err)
	}
	if has(h.git.Labelled, labelNeedsHuman) {
		t.Error("a routine bump must not be labelled; a flag on everything is a flag on nothing")
	}
	if strings.Contains(h.git.Posted[0], "Worth a look before merging") {
		t.Error("a routine bump must not carry the flag banner")
	}
}
