package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
)

// The agent's side of the in-process gate: the verdict is handed over as a
// value rather than scraped back out of a comment, so there is no report to
// find and no check to poll.

// The triage reading the verdict in-process: no gate comment on the pull
// request, no check to poll -- the evidence is handed over, not scraped back.
func TestTriageReadsTheVerdictInProcess(t *testing.T) {
	h := newHarness(t)
	// Strip away everything the CI path needed: no report comment, no check.
	h.git.Comments = nil
	h.git.Check = gitprovider.CheckMissing

	h.triage.Gate = fakeGate{&gateservice.Outcome{
		State:  gitprovider.CheckFailure,
		Report: gateReport,
	}}
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassEscalate,
		Summary:        "This is a migration.", Reasoning: "The CRD schema changed.",
		EscalationReason: "The upgrade needs a CRD migration.",
	}

	if err := h.triage.Run(context.Background(), Promotion{PRNumber: 42, Files: []string{valuesPath}}); err != nil {
		t.Fatal(err)
	}
	if h.git.CheckCalls != 0 {
		t.Fatal("an in-process verdict must not be waited for through the check API")
	}
	if len(h.git.Posted) != 1 || !strings.Contains(h.git.Posted[0], "Needs a human.") {
		t.Fatalf("the red verdict must reach the model and the human: %v", h.git.Posted)
	}
	if !strings.Contains(h.model.User, "0.16.1") {
		t.Fatal("the model must be shown the same report the gate produced in-process")
	}
}

// A broken in-process gate resolves the triage status rather than leaving it
// pending forever -- the same rule every other error path answers to.
func TestTriageSurfacesABrokenInProcessGate(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = nil
	h.triage.Gate = fakeGate{&gateservice.Outcome{Err: fmt.Errorf("secrets is forbidden")}}

	_ = h.triage.Run(context.Background(), Promotion{PRNumber: 42})
	s := h.git.Statuses[len(h.git.Statuses)-1]
	if s.State != gitprovider.StateSuccess || !strings.Contains(s.Description, "did not finish") {
		t.Fatalf("a broken gate must resolve the advisory status with the reason: %s %q", s.State, s.Description)
	}
}

// fakeGate hands over a verdict that a real run would have produced.
type fakeGate struct{ out *gateservice.Outcome }

func (f fakeGate) Ensure(context.Context, *gitprovider.PullRequest) *gateservice.Outcome {
	return f.out
}
