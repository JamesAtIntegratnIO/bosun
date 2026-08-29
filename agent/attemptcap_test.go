package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
)

// The attempt cap has exactly one memory: a label on the pull request. Both
// repair paths used to push first and label afterwards, logging a label
// failure and carrying on, so a token with push permission and no permission
// to label repaired, failed to record it, counted zero attempts on the next
// run and repaired again, indefinitely.
//
// Reserving the label first inverts every failure: a cap that cannot be
// recorded is a pull request that gets a human instead of a commit.
func TestTheAttemptCapRefusesToPushWhenItCannotBeRecorded(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	h.git.LabelErr = errors.New("403 Resource not accessible by integration")
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Move the metallb pin with the chart.",
		Reasoning:      "The rendered diff proves the default changed.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
		}},
	}

	// escalate also labels, so with a host refusing every label this returns
	// the error. What matters is what did not happen.
	_ = h.triage.Run(context.Background(), promotion())

	if len(h.git.Pushes) != 0 {
		t.Fatalf("pushed a fix the attempt cap could not record: %+v", h.git.Pushes)
	}
	if len(h.git.LabelAttempts) == 0 {
		t.Fatal("the attempt label was never even tried")
	}
	if !strings.HasPrefix(h.git.LabelAttempts[0], "bosun/attempt-") &&
		!strings.HasPrefix(h.git.LabelAttempts[0], labelAttempt) {
		t.Errorf("the first write must be the attempt label, got %q", h.git.LabelAttempts[0])
	}
}

// The ordering is the fix, so it is asserted directly rather than only through
// the failure case: the label must be on the pull request before the push, or
// a crash between the two loses the attempt.
func TestTheAttemptIsRecordedBeforeTheFixIsPushed(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Move the metallb pin with the chart.",
		Reasoning:      "The rendered diff proves the default changed.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
		}},
	}
	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("want one push, got %d", len(h.git.Pushes))
	}
	if len(h.git.Labelled) == 0 || !strings.Contains(h.git.Labelled[0], "attempt") {
		t.Errorf("the attempt label must be written first, got %v", h.git.Labelled)
	}
}
