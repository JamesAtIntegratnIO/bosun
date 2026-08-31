package mcp

import (
	"encoding/json"
	"testing"
)

// gate_status is the queue, and the queue's whole difficulty is that its
// empty form is also what a broken gate looks like. Every test here is about
// one of the two ways to have no pull requests.

func TestTheQueueIsEveryOpenPullRequestTheSweepSaw(t *testing.T) {
	f := newFixture(t, nil).withGate(blocked())
	q := f.queue(t)

	if q.Repository != "example/platform" || !q.Swept {
		t.Fatalf("every result names its repository and its sweep; got %+v", q)
	}
	if q.Open == nil {
		t.Fatal("a sweep that ran publishes its queue, even to say it is empty")
	}
	open := *q.Open
	if len(open) != 1 {
		t.Fatalf("the sweep saw one pull request; the queue holds %d", len(open))
	}
	pr := open[0]
	if pr.Number != 264 || pr.State != StateFailing {
		t.Errorf("the queue must carry the number and the state standing on it, got %+v", pr)
	}
	if pr.HeadCommit != blockedHead {
		t.Errorf("a verdict that does not name its commit cannot be told from a stale one, "+
			"got %q", pr.HeadCommit)
	}
	if pr.Blocking == nil || !*pr.Blocking {
		t.Errorf("a failing verdict blocks, and the queue says so as a typed field, got %+v",
			pr.Blocking)
	}
	// The breakdown, and not the findings behind it: the queue is read to
	// choose which pull request to ask about, and gate_verdict is where the
	// asking happens.
	if pr.Blockers == nil || pr.Blockers.Consumers != 4 || pr.Blockers.Unrenderable != 1 {
		t.Errorf("the queue carries the counted breakdown, got %+v", pr.Blockers)
	}
	if pr.Title == nil || pr.Title.Origin != OriginAuthor {
		t.Errorf("a pull request's title is written by whoever opened it and has to say so, "+
			"got %+v", pr.Title)
	}
}

// What is held and what is running, which is how a caller tells a queue that
// has been judged from one that is being judged right now.
func TestTheQueueSaysWhatIsHeldAndWhatIsRunning(t *testing.T) {
	q := newFixture(t, nil).withGate(blocked()).queue(t)
	if q.Held == nil || *q.Held != 2 {
		t.Errorf("want 2 verdicts held, got %v", q.Held)
	}
	if q.Running == nil || *q.Running != 1 {
		t.Errorf("want 1 gate run in flight, got %v", q.Running)
	}
}

// Nothing open is a real answer, and it is an EMPTY queue rather than no queue.
func TestASweepThatSawNothingOpenPublishesAnEmptyQueue(t *testing.T) {
	f := newFixture(t, nil).withGate(GateStatus{SweptAt: sweptAt})
	raw := f.call(t, "gate_status")

	if _, ok := fields(t, raw)["open"]; !ok {
		t.Fatal("a sweep that ran and saw nothing open must publish an empty queue: absence " +
			"is what says no sweep has looked, and this one did")
	}
	var q Queue
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.Open == nil || len(*q.Open) != 0 {
		t.Fatalf("want an empty queue, got %+v", q.Open)
	}
	if q.Held == nil || *q.Held != 0 {
		t.Errorf("a sweep that ran holds a number of verdicts, even zero, got %v", q.Held)
	}
}

// Before the first sweep there is no queue at all, and no counts either.
//
// The single most expensive mistake this surface can make is an empty list
// read as "nothing is open" from a gate that has not looked, so the field is
// absent rather than empty and the sentence says which.
func TestBeforeTheFirstSweepThereIsNoQueueField(t *testing.T) {
	f := newFixture(t, nil)
	raw := f.call(t, "gate_status")

	got := fields(t, raw)
	for _, absent := range []string{"open", "sweptAt", "ageSeconds", "held", "running"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%q is present before any sweep: %s", absent, raw)
		}
	}

	var q Queue
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.Swept {
		t.Error("no sweep has completed, so nothing may claim one has")
	}
	if q.Status.Origin != OriginBosun {
		t.Errorf("the sentence before the first sweep is bosun's own, got %+v", q.Status)
	}
	if q.Repository != "example/platform" {
		t.Errorf("a result names its repository even when it has nothing else to say, got %q",
			q.Repository)
	}
}

// A sweep that could not list says so, in the field whose whole purpose is
// that "nothing open" and "could not look" must not read the same.
func TestAQueueFromASweepThatCouldNotListSaysSo(t *testing.T) {
	f := newFixture(t, nil).withGate(GateStatus{
		SweptAt: sweptAt, Err: "the host said 401"})
	raw := f.call(t, "gate_status")

	var q Queue
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.SweepError == nil {
		t.Fatal("a sweep that could not list pull requests must say so, or the queue reads " +
			"'nothing open' forever")
	}
	if q.SweepError.Origin == OriginBosun {
		t.Errorf("the error is the git host's words inside bosun's sentence, and the tag has "+
			"to say so; got %+v", q.SweepError)
	}
	// And the queue itself is ABSENT: this sweep listed nothing, so an empty
	// list here would be the exact claim the field above exists to deny.
	if _, ok := fields(t, raw)["open"]; ok {
		t.Errorf("a sweep that listed nothing must not publish a queue: %s", raw)
	}
}

// A queue held over from an earlier sweep is published rather than dropped,
// and the error beside it is what says it is not this sweep's work.
func TestAQueueHeldOverFromAnEarlierSweepIsStillPublished(t *testing.T) {
	g := blocked()
	g.Err = "the host said 401"
	q := newFixture(t, nil).withGate(g).queue(t)

	if q.Open == nil || len(*q.Open) != 1 {
		t.Fatalf("what an earlier sweep saw is evidence and is kept, got %+v", q.Open)
	}
	if q.SweepError == nil {
		t.Fatal("and the queue's age is not this sweep's, which only the error says")
	}
}

// The queue is served from the snapshot, like everything else here.
func TestTheQueueCostsNoGitHostCall(t *testing.T) {
	w := wedged()
	report := sweep(t, w)
	w.kargo.calls, w.prs.calls = 0, 0

	f := newFixture(t, report).withGate(blocked())
	f.queue(t)

	if w.prs.calls != 0 || w.kargo.calls != 0 {
		t.Errorf("gate_status made %d git-host and %d cluster calls; the queue is a snapshot",
			w.prs.calls, w.kargo.calls)
	}
}
