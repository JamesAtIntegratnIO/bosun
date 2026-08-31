package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// triage_status answers "what is the agent doing about this pull request", and
// the answer a caller most needs is the boring one: nothing, and here is why
// that is not a failure.

func TestTriageSaysWhatItIsDoingNow(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked()).withTriage(TriageStatus{
		InFlight: []int{264}, MaxAttempts: 2, Attempts: map[int]int{264: 1},
	})
	tr := f.triageStatus(t, 264)

	if tr.Repository != "example/platform" || tr.PullRequest != 264 {
		t.Fatalf("every result names its repository and echoes what it was asked, got %+v", tr)
	}
	if tr.Phase != PhaseRunning {
		t.Errorf("a pull request being triaged now is %q, got %q", PhaseRunning, tr.Phase)
	}
	if tr.Attempts == nil || *tr.Attempts != 1 || tr.AttemptCap != 2 {
		t.Errorf("the attempt count is published against its cap, got %v of %d",
			tr.Attempts, tr.AttemptCap)
	}
	if tr.Exhausted == nil || *tr.Exhausted {
		t.Errorf("one attempt of two is not exhausted, got %v", tr.Exhausted)
	}
	if tr.Labels == nil {
		t.Fatal("the labels standing on the pull request are what the cap remembers with, " +
			"and a caller reads them to see it was escalated without asking the git host")
	}
	var got []string
	for _, l := range *tr.Labels {
		got = append(got, l.Text)
	}
	if strings.Join(got, ",") != "dependencies,bosun/attempt-1" {
		t.Errorf("want the labels the sweep saw, got %v", got)
	}
	if tr.Status.Origin != OriginBosun {
		t.Errorf("the sentence is bosun's own, got %+v", tr.Status)
	}
}

// The one a caller hits most: a pull request nobody is working.
//
// An answer rather than an error. "The agent is not triaging this" is a fact
// about the agent, and a client that got a JSON-RPC error for it would have to
// tell that apart from a broken call.
func TestAPullRequestTheAgentIsNotWorkingIsAnsweredAsSuch(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked()).withTriage(TriageStatus{MaxAttempts: 2,
		Attempts: map[int]int{264: 1}})
	tr := f.triageStatus(t, 264)

	if tr.Phase != PhaseIdle {
		t.Errorf("nothing is running for this pull request, which is %q, got %q",
			PhaseIdle, tr.Phase)
	}
	if tr.Status.Text == "" {
		t.Error("and the phase is said in words as well, for the model reading this")
	}
	// What it has spent is still known, and it is the difference between an
	// agent that has finished and one that never started.
	if tr.Attempts == nil || *tr.Attempts != 1 {
		t.Errorf("an idle agent still has a history on this pull request, got %v", tr.Attempts)
	}
}

// A newer promotion waiting behind the one in flight is its own phase.
func TestAPromotionQueuedBehindOneRunningIsItsOwnPhase(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked()).withTriage(TriageStatus{
		InFlight: []int{264}, Queued: []int{264}, MaxAttempts: 2,
	})
	if tr := f.triageStatus(t, 264); tr.Phase != PhaseQueued {
		t.Errorf("a run with newer work waiting behind it is %q, got %q", PhaseQueued, tr.Phase)
	}
}

// An agent that has spent its cap says so, because the question behind every
// call here is whether it will try again.
func TestAnAgentThatHasSpentItsCapSaysSo(t *testing.T) {
	g := blocked()
	g.Open[0].Labels = []string{"bosun/attempt-1", "bosun/attempt-2", "bosun/escalated"}
	f := newFixture(t, nil).withGate(g).withTriage(TriageStatus{MaxAttempts: 2,
		Attempts: map[int]int{264: 2}})

	tr := f.triageStatus(t, 264)
	if tr.Exhausted == nil || !*tr.Exhausted {
		t.Fatalf("two attempts of two is the cap, got %v of %d", tr.Attempts, tr.AttemptCap)
	}
	if !strings.Contains(tr.Status.Text, "will not") {
		t.Errorf("the sentence has to say the agent is finished with it, got %q", tr.Status.Text)
	}
}

// A cap of zero is a cap that is spent, not a cap with room in it.
//
// MAX_ATTEMPTS is an operator's number and nothing validates it. An agent
// configured with zero escalates on the first attempt rather than repairing,
// so publishing "not exhausted" for it would tell a caller to wait for a fix
// that is never coming -- and that caller is a program.
func TestACapOfZeroIsNotACapWithRoomInIt(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked()).withTriage(TriageStatus{MaxAttempts: 0,
		Attempts: map[int]int{264: 0}})

	tr := f.triageStatus(t, 264)
	if tr.Exhausted == nil || !*tr.Exhausted {
		t.Errorf("an agent allowed no attempts has spent all of them, got %v of %d",
			tr.Attempts, tr.AttemptCap)
	}
	if !strings.Contains(tr.Status.Text, "will not") {
		t.Errorf("and the sentence says it will not try, got %q", tr.Status.Text)
	}
}

// Something waiting to be triaged is never reported as an idle agent.
//
// The two lists travel together today -- a promotion is only ever queued
// beside the run it is waiting for -- and the phase does not depend on that
// staying true, because the one word a pull request with work waiting cannot
// be given is the word for an agent with nothing to do.
func TestWorkWaitingIsNeverReportedAsAnIdleAgent(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked()).
		withTriage(TriageStatus{Queued: []int{264}, MaxAttempts: 2})
	if tr := f.triageStatus(t, 264); tr.Phase != PhaseQueued {
		t.Errorf("a pull request with a promotion waiting is %q, got %q", PhaseQueued, tr.Phase)
	}
}

// A pull request the sweep did not see is answered as absent, and never as an
// agent that merely has nothing to do.
func TestAPullRequestTheSweepDidNotSeeIsAbsentRatherThanIdle(t *testing.T) {
	f := newFixture(t, nil).withGate(green()).withTriage(TriageStatus{MaxAttempts: 2})
	raw := f.callWith(t, "triage_status", `{"pullRequest":999}`)

	var tr Triage
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Phase != PhaseAbsent {
		t.Errorf("the last sweep did not see this pull request open, which is %q, got %q",
			PhaseAbsent, tr.Phase)
	}
	// Nothing is known about its labels, so nothing is claimed about them.
	for _, absent := range []string{"labels", "attempts", "attemptsExhausted"} {
		if _, ok := fields(t, raw)[absent]; ok {
			t.Errorf("%q is published for a pull request the sweep never saw: %s", absent, raw)
		}
	}
}

// A sweep that could not list is not evidence that a pull request is gone.
//
// The distinction gate_verdict draws with the same word, for the same reason:
// reporting the absence of evidence as evidence of absence is how a caller
// concludes a pull request was merged when the gate merely lost its token.
func TestASweepThatCouldNotListIsNotAPullRequestThatIsGone(t *testing.T) {
	f := newFixture(t, nil).
		withGate(GateStatus{SweptAt: sweptAt, Err: "the host said 401"}).
		withTriage(TriageStatus{MaxAttempts: 2})

	raw := f.callWith(t, "triage_status", `{"pullRequest":264}`)
	var tr Triage
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Phase != PhaseUnknown {
		t.Errorf("a sweep that could not list leaves the pull request %q, not %q; got %q",
			PhaseUnknown, PhaseAbsent, tr.Phase)
	}
	if tr.SweepError == nil {
		t.Error("and what stopped the sweep is what makes the phase actionable")
	}
	if _, ok := fields(t, raw)["labels"]; ok {
		t.Errorf("nothing read a label, so an empty list would be a claim: %s", raw)
	}
}

// Before the first sweep, the labels are unknown and the phase says so.
func TestBeforeTheFirstSweepTriageClaimsNoLabels(t *testing.T) {
	f := newFixture(t, nil).withTriage(TriageStatus{MaxAttempts: 2})
	raw := f.callWith(t, "triage_status", `{"pullRequest":264}`)

	var tr Triage
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Phase != PhaseUnswept || tr.Swept {
		t.Errorf("no sweep has completed, which is %q, got %q", PhaseUnswept, tr.Phase)
	}
	if _, ok := fields(t, raw)["labels"]; ok {
		t.Errorf("no sweep has read a label, so an empty list would be a claim: %s", raw)
	}
	// The cap is a setting rather than a reading, so it is known before
	// anything has looked.
	if tr.AttemptCap != 2 {
		t.Errorf("the cap is what the operator configured and is known always, got %d",
			tr.AttemptCap)
	}
}

// Work in flight outranks the sweep: the process knows what it is running now
// whatever the last sweep saw.
func TestARunningTriageIsReportedEvenForAPullRequestTheSweepMissed(t *testing.T) {
	f := newFixture(t, nil).withTriage(TriageStatus{InFlight: []int{264}, MaxAttempts: 2})
	if tr := f.triageStatus(t, 264); tr.Phase != PhaseRunning {
		t.Errorf("the agent is triaging this right now, whatever the sweep saw; got %q", tr.Phase)
	}
}

// Labels carry an origin, like every other free text on this surface. Anyone
// who can label a pull request chooses these bytes.
func TestALabelSaysWhereItCameFrom(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked()).withTriage(TriageStatus{MaxAttempts: 2})
	tr := f.triageStatus(t, 264)
	if tr.Labels == nil || len(*tr.Labels) == 0 {
		t.Fatal("this fixture's pull request carries labels")
	}
	for _, l := range *tr.Labels {
		if l.Origin != OriginLabel {
			t.Errorf("a label is not bosun's own text, got %+v", l)
		}
	}
}

// The repository argument is optional and means this install's own.
func TestTriageTakesTheSameRepositoryArgumentAsEveryOtherTool(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked()).withTriage(TriageStatus{MaxAttempts: 2})

	if tr := f.callWith(t, "triage_status",
		`{"pullRequest":264,"repository":"example/platform"}`); len(tr) == 0 {
		t.Fatal("naming the install's own repository is the same call as omitting it")
	}

	_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"triage_status","arguments":{"pullRequest":264,"repository":"someone/else"}}}`)
	if !strings.Contains(string(body), "example/platform") {
		t.Errorf("a repository this install does not watch is refused with the one it does, "+
			"got %s", body)
	}
	if strings.Contains(string(body), "someone/else") {
		t.Errorf("the caller's own string is not echoed back into a message it renders: %s", body)
	}
}

func TestTriageRefusesArgumentsThatAreNotAPullRequest(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked())
	for _, args := range []string{`{}`, `{"pullRequest":0}`, `{"pullRequest":-3}`} {
		_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
			`{"name":"triage_status","arguments":`+args+`}}`)
		var resp struct {
			Error *struct{ Message string } `json:"error"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil {
			t.Errorf("%s is not a pull request and has to be refused, got %s", args, body)
		}
	}
}
