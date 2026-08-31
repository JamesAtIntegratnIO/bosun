package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/agent"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/web"
)

// The crossing from the two halves that know what the agent is doing into the
// tool surface's vocabulary.
//
// A contract nothing else can see, for the reason the verdict's crossing is
// one: mcp imports the result types and the redactor and nothing else, so it
// cannot import gateservice or agent, and the copying happens here. A field
// this file forgets is one that is simply absent from every answer, with no
// compiler and no test in either package to notice.
//
// What crosses comes from two clocks, and that is the interesting part. The
// phase is the promotion endpoint's own state, current to the microsecond; the
// labels and the attempt count are as old as the last gate sweep. The tool
// surface publishes the sweep's timestamp because of it.

func TestTheLabelsTheSweepSawCrossIntoTheToolSurface(t *testing.T) {
	g := gateservice.Status{
		SweptAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Held:    2, Running: 1,
		Open: []gateservice.PRStatus{{
			Number: 264, Title: "chore(deps): bump external-secrets",
			HeadSHA: "9f2c1a4b", State: gateservice.StateFailing,
			Labels: []string{"dependencies", "bosun/attempt-1"},
		}},
	}

	got := mcpGateStatus(g)
	if len(got.Open) != 1 {
		t.Fatalf("the queue did not cross: %+v", got.Open)
	}
	labels := got.Open[0].Labels
	if len(labels) != 2 || labels[0] != "dependencies" || labels[1] != "bosun/attempt-1" {
		t.Errorf("the labels the sweep saw did not cross, got %v.\n"+
			"They are the attempt cap's only memory, and a tool call cannot fetch them: a "+
			"read surface that answers from a snapshot has whatever the snapshot carried.",
			labels)
	}
	if got.Held != 2 || got.Running != 1 {
		t.Errorf("what is held and what is running did not cross: %d held, %d running",
			got.Held, got.Running)
	}
}

// What the agent has spent crosses as the agent's own arithmetic.
//
// The cap remembers in a label under a prefix that follows the brand, so a
// count made anywhere else has to know that -- and the failure of getting it
// wrong is silent and one-directional: a renamed install reports attempts
// remaining on a pull request it has already escalated, which is the answer a
// caller acts on by waiting.
func TestWhatTheAgentHasSpentCrossesAsTheAgentsOwnCount(t *testing.T) {
	g := gateservice.Status{
		SweptAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Open: []gateservice.PRStatus{
			{Number: 264, Labels: []string{"deckhand/attempt-1", "bosun/attempt-1"}},
			{Number: 41, Labels: []string{"dependencies"}},
		},
	}
	// A renamed agent. Its own labels are the ones it counts, and the other
	// prefix in the fixture is what a count that hard-coded the default name
	// would pick up instead.
	tr := &agent.Triage{Brand: "Deckhand", MaxAttempts: 2}

	got := mcpTriageStatus(web.TriageStatus{InFlight: []int{264}, Queued: []int{264}}, tr, g)

	if got.MaxAttempts != 2 {
		t.Errorf("the cap did not cross, got %d", got.MaxAttempts)
	}
	if n, ok := got.Attempts[264]; !ok || n != tr.AttemptsUsed(g.Open[0].Labels) || n != 1 {
		t.Errorf("want the agent's own count of 1, got %d (present: %v).\n"+
			"A second implementation of the count is a second answer to whether the agent "+
			"will try again.", n, ok)
	}
	// A zero is published for a pull request the sweep saw, because "it has
	// spent nothing" and "the sweep never saw it" are different answers and
	// the tool surface tells them apart by whether there is an entry at all.
	if n, ok := got.Attempts[41]; !ok || n != 0 {
		t.Errorf("a pull request the sweep saw with no attempts on it must cross as zero "+
			"rather than as missing, got %d (present: %v)", n, ok)
	}
	if _, ok := got.Attempts[999]; ok {
		t.Error("a pull request no sweep saw must have no entry, or the tool surface " +
			"publishes an attempt count it has no labels behind")
	}

	if len(got.InFlight) != 1 || got.InFlight[0] != 264 {
		t.Errorf("what is running did not cross: %v", got.InFlight)
	}
	if len(got.Queued) != 1 || got.Queued[0] != 264 {
		t.Errorf("what is waiting behind it did not cross: %v", got.Queued)
	}

	// And every field of the crossing, derived from the type rather than named
	// here. The fixture above populates all of them, so a field this adapter
	// forgets to fill is a zero here -- which is what it would be in every
	// answer, forever, with no compiler and no test in either package to
	// notice. A fifth field added to TriageStatus is covered the day it is
	// declared.
	v := reflect.ValueOf(got)
	if v.NumField() == 0 {
		t.Fatal("mcp.TriageStatus has no fields; this walk is proving nothing")
	}
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("mcpTriageStatus left %s at its zero value, and this fixture gives it "+
				"something to carry.\n"+
				"A field the composition root forgets is simply missing from every answer: "+
				"mcp cannot import the packages it came from, so neither side has a compiler "+
				"that can see the other half.", v.Type().Field(i).Name)
		}
	}
}

// With no agent wired there is no cap, rather than a cap of zero.
//
// Zero is the one number that must not cross here: every attempt spent, for
// every pull request, on an install where nothing has ever run.
func TestNoAgentCrossesAsNoCapRatherThanACapOfZero(t *testing.T) {
	got := mcpTriageStatus(web.TriageStatus{}, nil, gateservice.Status{})
	if got.Attempts != nil {
		t.Errorf("with no agent there is nothing to count, got %v", got.Attempts)
	}
	if got.MaxAttempts != 0 {
		t.Errorf("and no cap to claim, got %d", got.MaxAttempts)
	}
}
