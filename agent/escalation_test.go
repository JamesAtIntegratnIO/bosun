package agent

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
)

// What the model said when it asked for a human is kept, so a read surface can
// publish it.
//
// Today it reaches a person twice and is then gone: once on the commit status,
// once in the log. The pull request keeps the label and the comment, and the
// sentence that explains the label -- the one thing a triager wants before
// opening the diff -- is nowhere this process can be asked for.
func TestTheModelsEscalationReasonIsKept(t *testing.T) {
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

	reason := h.triage.EscalationReason(h.git.PR.Number)
	if reason == "" {
		t.Fatalf("no reason is held for PR %d after the model escalated it. The label says a "+
			"human is needed and nothing in this process can say why.", h.git.PR.Number)
	}
	if reason != "apiVersion migration on a PodDisruptionBudget" {
		t.Errorf("the reason held is not the one the model gave, got %q", reason)
	}
}

// An escalation bosun decided on a process fact holds no model sentence.
//
// The field the reason reaches is tagged as a model's words. A verdict that
// asked for no human, on a path bosun stopped for reasons of its own -- the
// push failed, the cap is spent -- has no such sentence about this decision,
// and publishing an unrelated one under that tag would be the surface telling
// a client a model wrote something it did not write about this.
func TestAnEscalationBosunDecidedHoldsNoModelReason(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	// A mechanical verdict that still carries an escalationReason. Nothing in
	// the schema forbids one, so the guard cannot be "is the string empty".
	h.model.Verdict = &llm.Verdict{
		Classification:   llm.ClassMechanical,
		Summary:          "Bump the pinned version.",
		Reasoning:        "The chart moved.",
		EscalationReason: "a sentence about a decision the model did not make here",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
		}},
	}
	h.git.PushErr = errors.New("the remote refused the push")
	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(h.git.Labelled, labelNeedsHuman) {
		t.Fatalf("a fix that could not be pushed must still hand over, labels %v", h.git.Labelled)
	}
	if reason := h.triage.EscalationReason(h.git.PR.Number); reason != "" {
		t.Errorf("a model sentence is held for a handoff bosun decided, got %q.\n"+
			"The field it reaches says a model wrote it about this stop, and a mechanical "+
			"verdict's leftover escalationReason is a sentence about something else.", reason)
	}
}

// A refused label keeps no reason.
//
// A token with push permission and no permission to label is a real shape --
// it is what made the attempt cap fail open once. On it the pull request never
// enters the queue at all, so a reason held for it is a sentence no reader can
// reach and one nothing will release until the pull request closes.
func TestARefusedLabelKeepsNoReason(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	h.git.LabelErr = errors.New("the token may not label")
	h.model.Verdict = &llm.Verdict{
		Classification:   llm.ClassEscalate,
		Summary:          "Decide whether to accept the removed ClusterRole.",
		Reasoning:        "Nothing in the editable list can restore a deleted template.",
		EscalationReason: "the chart dropped a ClusterRole this repository binds",
	}
	if err := h.triage.Run(context.Background(), promotion()); err == nil {
		t.Fatal("a handoff whose label was refused must not report success")
	}

	// The self-check: the label really was attempted, so the assertion below
	// is about a refusal rather than about a path that never got there.
	if !slices.Contains(h.git.LabelAttempts, labelNeedsHuman) {
		t.Fatalf("the label was never attempted, so nothing here is about a refused one: %v",
			h.git.LabelAttempts)
	}
	if reason := h.triage.EscalationReason(h.git.PR.Number); reason != "" {
		t.Errorf("a reason is held for a pull request that never entered the queue: %q", reason)
	}
}

// A reason leaves when its pull request leaves the queue.
//
// Kept, they are the slow leak the gate prunes its verdict cache and its
// comment histories to avoid, and a stale one answers a caller about a handoff
// nobody is waiting on any more.
func TestAReasonIsReleasedWhenThePullRequestLeavesTheQueue(t *testing.T) {
	tr := &Triage{}
	tr.rememberEscalation(264, &llm.Verdict{
		Classification: llm.ClassEscalate, EscalationReason: "the CRD schema moved"})
	tr.rememberEscalation(41, &llm.Verdict{
		Classification: llm.ClassEscalate, EscalationReason: "the ClusterRole is gone"})

	tr.ForgetEscalationsExcept([]int{264})

	if tr.EscalationReason(264) == "" {
		t.Error("the reason for a pull request that is still open was dropped")
	}
	if reason := tr.EscalationReason(41); reason != "" {
		t.Errorf("the reason for a pull request that has left the queue is still held: %q", reason)
	}

	// And it stays gone. A store that only hid the entry would answer again
	// the moment the same number came back on a different pull request.
	tr.ForgetEscalationsExcept([]int{264, 41})
	if tr.EscalationReason(41) != "" {
		t.Error("a released reason came back")
	}
}
