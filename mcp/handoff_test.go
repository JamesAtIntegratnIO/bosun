package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// handoff_queue is the queue with a person's name on it, and its difficulty is
// gate_status's twice over: an empty queue is what a quiet week looks like AND
// what a gate that could not list looks like, and the thing being counted --
// a pull request nobody has picked up -- is the one a caller is asking about
// precisely because nobody has noticed it.

func TestTheHandoffQueueIsEveryPullRequestWaitingOnAHuman(t *testing.T) {
	f := newFixture(t, nil).withGate(escalated()).
		withTriage(TriageStatus{MaxAttempts: 2, Attempts: map[int]int{264: 2, 41: 0}})
	q := f.handoff(t)

	if q.Repository != "example/platform" || !q.Swept {
		t.Fatalf("every result names its repository and its sweep; got %+v", q)
	}
	if q.Waiting == nil {
		t.Fatal("a sweep that listed publishes its queue, even to say it is empty")
	}
	waiting := *q.Waiting
	// The fixture holds two open pull requests and one label. A queue that
	// returned both would be gate_status under another name.
	if len(waiting) != 1 {
		t.Fatalf("one of the two open pull requests is labelled; the queue holds %d: %+v",
			len(waiting), waiting)
	}
	pr := waiting[0]
	if pr.Number != 264 || pr.State != StateFailing {
		t.Errorf("an entry carries the number and the state standing on it, got %+v", pr)
	}
	if pr.HeadCommit != blockedHead {
		t.Errorf("a verdict that does not name its commit cannot be told from a stale one, "+
			"got %q", pr.HeadCommit)
	}
	if pr.URL == "" {
		t.Error("a handoff is read by somebody who is about to open the pull request")
	}
	if pr.Title == nil || pr.Title.Origin != OriginAuthor {
		t.Errorf("a pull request's title is written by whoever opened it and has to say so, "+
			"got %+v", pr.Title)
	}
	if pr.Blocking == nil || !*pr.Blocking {
		t.Errorf("a failing verdict blocks, and the queue says so as a typed field, got %+v",
			pr.Blocking)
	}
	if pr.Blockers == nil || pr.Blockers.Consumers != 4 || pr.Blockers.Unrenderable != 1 {
		t.Errorf("an entry carries the counted breakdown, got %+v", pr.Blockers)
	}
	if q.Status.Origin != OriginBosun {
		t.Errorf("the sentence about the queue is bosun's own, got %+v", q.Status)
	}
}

// The findings travel with the entry, which is the difference between this
// queue and gate_status's.
//
// gate_status stops at the counts because it is read to choose which pull
// request to ask about next. This one is read by whoever is about to do the
// work, and the fields they need -- the manifests that still declare a dropped
// version, the settings a bump stopped reading, the migration -- are on the
// findings. A queue that made them call gate_verdict per entry would have
// saved them nothing.
func TestAHandoffCarriesTheFindingsBehindIt(t *testing.T) {
	q := newFixture(t, nil).withGate(escalated()).handoff(t)
	pr := (*q.Waiting)[0]

	if pr.Findings == nil {
		t.Fatal("a held verdict publishes its findings, or the handoff is a number and a colour")
	}
	findings := *pr.Findings
	if len(findings) != 5 {
		t.Fatalf("the fixture's verdict has five findings; the entry carries %d", len(findings))
	}

	var dropped *VerdictFinding
	for i := range findings {
		if findings[i].Kind == "droppedVersion" {
			dropped = &findings[i]
		}
	}
	if dropped == nil {
		t.Fatal("the dropped-version finding did not travel, and it is the one a handoff acts on")
	}
	if len(dropped.ConsumerFiles) != 4 {
		t.Errorf("the manifests that have to move are the work being handed over, got %v",
			dropped.ConsumerFiles)
	}
	if dropped.Dropped == nil || dropped.Dropped.Surviving != "v1" {
		t.Errorf("the migration a repair would have performed travels as fields, got %+v",
			dropped.Dropped)
	}
	if dropped.Subject.Origin != OriginChart {
		t.Errorf("a finding's subject is a name a render chose and has to say so, got %+v",
			dropped.Subject)
	}
}

// What the agent has spent, from the agent's own count.
//
// The cap's only memory is a label under a prefix that follows the brand, so
// this surface publishes the number the agent arrived at rather than counting
// the labels a second time. A pull request the sweep did not see has no entry
// and therefore no count, which is the same rule triage_status publishes under.
func TestAHandoffSaysWhatTheAgentHasAlreadySpent(t *testing.T) {
	q := newFixture(t, nil).withGate(escalated()).
		withTriage(TriageStatus{MaxAttempts: 2, Attempts: map[int]int{264: 2}}).
		handoff(t)

	if q.AttemptCap != 2 {
		t.Errorf("the cap is what the operator configured and is always known, got %d",
			q.AttemptCap)
	}
	pr := (*q.Waiting)[0]
	if pr.Attempts == nil || *pr.Attempts != 2 {
		t.Fatalf("want the agent's own count of 2, got %v", pr.Attempts)
	}
	if pr.Exhausted == nil || !*pr.Exhausted {
		t.Errorf("two of two attempts spent is a cap reached, and a caller should not have to "+
			"know the comparison is >=, got %v", pr.Exhausted)
	}
}

// A handoff the agent never worked is not a handoff it exhausted.
//
// Escalating on the first look and running out of attempts are different
// situations wanting different people, and a zero published where the count is
// unknown would collapse them. So the count is absent for a pull request the
// triage snapshot has no entry for, and absent takes the exhaustion flag with
// it: "will it try again" is unknown, not "no".
func TestAHandoffWithNoAttemptCountPublishesNoZero(t *testing.T) {
	f := newFixture(t, nil).withGate(escalated()).withTriage(TriageStatus{MaxAttempts: 2})
	raw := f.callWith(t, "handoff_queue", `{}`)

	var q Handoff
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if pr := (*q.Waiting)[0]; pr.Attempts != nil || pr.Exhausted != nil {
		t.Errorf("with nothing counted, a count and an exhaustion flag are both claims "+
			"nothing stands behind, got %v and %v", pr.Attempts, pr.Exhausted)
	}

	// And at the wire, where a zero and an absence are what a client tells
	// apart. Unmarshalling into the struct cannot see the difference.
	var tree struct {
		Waiting []map[string]json.RawMessage `json:"waiting"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Waiting) != 1 {
		t.Fatalf("the fixture publishes one entry, got %d", len(tree.Waiting))
	}
	for _, absent := range []string{"attempts", "attemptsExhausted"} {
		if _, ok := tree.Waiting[0][absent]; ok {
			t.Errorf("%q is present for a pull request nothing counted: %s", absent, raw)
		}
	}
}

// Nothing waiting on a human is a real answer, and it is an EMPTY queue rather
// than no queue.
func TestASweepThatSawNothingWaitingPublishesAnEmptyQueue(t *testing.T) {
	// A sweep that listed a pull request and found it unlabelled: the answer a
	// caller most wants to be able to trust, because it is the one they act on
	// by going home.
	f := newFixture(t, nil).withGate(blocked())
	raw := f.callWith(t, "handoff_queue", `{}`)

	if _, ok := fields(t, raw)["waiting"]; !ok {
		t.Fatal("a sweep that listed and saw nothing labelled must publish an empty queue: " +
			"absence is what says nothing has looked, and this one did")
	}
	var q Handoff
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.Waiting == nil || len(*q.Waiting) != 0 {
		t.Fatalf("want an empty queue, got %+v", q.Waiting)
	}
	if q.Status.Origin != OriginBosun {
		t.Errorf("the sentence that says the sweep looked is bosun's own, got %+v", q.Status)
	}
}

// Before the first sweep there is no queue at all.
//
// The most expensive mistake this surface can make, in the place it costs the
// most: "nothing is waiting on you" from a process that has not looked is an
// answer somebody acts on by doing nothing.
func TestBeforeTheFirstSweepThereIsNoHandoffQueue(t *testing.T) {
	f := newFixture(t, nil).withTriage(TriageStatus{MaxAttempts: 2})
	raw := f.callWith(t, "handoff_queue", `{}`)

	got := fields(t, raw)
	for _, absent := range []string{"waiting", "sweptAt", "ageSeconds", "sweepError"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%q is present before any sweep: %s", absent, raw)
		}
	}

	var q Handoff
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.Swept {
		t.Error("no sweep has completed, so nothing may claim one has")
	}
	if q.Repository != "example/platform" {
		t.Errorf("a result names its repository even when it has nothing else to say, got %q",
			q.Repository)
	}
	// The cap is not a reading of the world, so it is known before anything
	// has looked and is published either way.
	if q.AttemptCap != 2 {
		t.Errorf("the cap is what the operator configured, got %d", q.AttemptCap)
	}
}

// A sweep that could not list says so, and publishes no queue at all.
func TestAHandoffQueueFromASweepThatCouldNotListSaysSo(t *testing.T) {
	f := newFixture(t, nil).withGate(GateStatus{SweptAt: sweptAt, Err: "the host said 401"})
	raw := f.callWith(t, "handoff_queue", `{}`)

	var q Handoff
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.SweepError == nil {
		t.Fatal("a sweep that could not list pull requests must say so, or the queue reads " +
			"'nobody needs you' forever")
	}
	if q.SweepError.Origin == OriginBosun {
		t.Errorf("the error is the git host's words inside bosun's sentence, and the tag has "+
			"to say so; got %+v", q.SweepError)
	}
	if _, ok := fields(t, raw)["waiting"]; ok {
		t.Errorf("a sweep that listed nothing must not publish a queue: %s", raw)
	}
}

// A label is matched whatever case it was applied in.
//
// The agent writes it in lower case and is not the only thing that can apply
// it: a maintainer labels a pull request by hand, and a git host that folds
// label names when it creates them hands back whatever case the first person
// to apply one used. A queue that missed a handoff over a capital letter is a
// person waiting for nobody, and nothing anywhere would report it.
func TestTheLabelIsMatchedWhateverCaseItWasAppliedIn(t *testing.T) {
	for _, label := range []string{"needs-human", "Needs-Human", "NEEDS-HUMAN", " needs-human "} {
		g := blocked()
		g.Open[0].Labels = append(g.Open[0].Labels, label)

		q := newFixture(t, nil).withGate(g).handoff(t)
		if q.Waiting == nil || len(*q.Waiting) != 1 {
			t.Errorf("a pull request labelled %q is not in the queue: %+v", label, q.Waiting)
		}
	}

	// And the match is on the label rather than on something that contains it,
	// or a repository's own vocabulary decides who bosun says is waiting.
	for _, label := range []string{"needs-human-review", "no-needs-human", "needs human"} {
		g := blocked()
		g.Open[0].Labels = append(g.Open[0].Labels, label)

		q := newFixture(t, nil).withGate(g).handoff(t)
		if q.Waiting == nil || len(*q.Waiting) != 0 {
			t.Errorf("%q is not the label, and put a pull request in the queue anyway: %+v",
				label, q.Waiting)
		}
	}
}

// A queue held over from an earlier sweep is published rather than dropped,
// and the error beside it is what says it is not this sweep's work.
func TestAHandoffQueueHeldOverFromAnEarlierSweepIsStillPublished(t *testing.T) {
	g := escalated()
	g.Err = "the host said 401"
	q := newFixture(t, nil).withGate(g).handoff(t)

	if q.Waiting == nil || len(*q.Waiting) != 1 {
		t.Fatalf("what an earlier sweep saw is evidence and is kept, got %+v", q.Waiting)
	}
	if q.SweepError == nil {
		t.Fatal("and the queue's age is not this sweep's, which only the error says")
	}
}

// And a held-over queue with nobody in it is still held over.
//
// The one shape on this tool where an empty list travels beside a sweep error,
// and the pair is not a contradiction: the last sweep could not list, and the
// one before it listed and saw nobody labelled. The claim is evidence from the
// earlier sweep rather than a claim about now, which is exactly what the
// sentence beside it has to say -- an empty queue read as this sweep's work
// would be the one mistake this tool cannot make.
func TestAHeldOverQueueWithNobodyInItSaysItIsHeldOver(t *testing.T) {
	g := blocked() // listed, and nothing labelled
	g.Err = "the host said 401"

	f := newFixture(t, nil).withGate(g)
	raw := f.callWith(t, "handoff_queue", `{}`)

	if _, ok := fields(t, raw)["waiting"]; !ok {
		t.Fatal("an earlier sweep listed these pull requests, so its answer is evidence and " +
			"is published; dropping it would leave the caller with nothing at all")
	}
	var q Handoff
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	if q.Waiting == nil || len(*q.Waiting) != 0 {
		t.Fatalf("nothing in that listing was labelled, so the queue is empty: %+v", q.Waiting)
	}
	if q.SweepError == nil {
		t.Fatal("and the error is the only thing that says this is not a claim about now")
	}
	if !strings.Contains(q.Status.Text, "older than the sweep time") {
		t.Errorf("the sentence beside an empty held-over queue has to say it is held over, "+
			"or it reads as a sweep that looked just now, got %q", q.Status.Text)
	}
}

// The repository qualifier, which this tool accepts and does not need: an
// install watches one repository, so naming it is a check rather than a
// choice, and naming another is refused rather than answered.
//
// It is here because the per-pull-request tools take one, and a client that
// stamps every call with the install it means should not find that half the
// surface refuses the argument.
func TestTheHandoffQueueIsQualifiedByRepository(t *testing.T) {
	f := newFixture(t, nil).withGate(escalated())

	var q Handoff
	if err := json.Unmarshal(f.callWith(t, "handoff_queue",
		`{"repository":"example/platform"}`), &q); err != nil {
		t.Fatal(err)
	}
	if q.Waiting == nil || len(*q.Waiting) != 1 {
		t.Fatalf("naming the repository this install watches is a check that passes, got %+v",
			q.Waiting)
	}

	_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"handoff_queue","arguments":{"repository":"someone/else"}}}`)
	var resp struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatalf("another repository is refused rather than answered with this one's queue: %s",
			body)
	}
	if strings.Contains(resp.Error.Message, "someone/else") {
		t.Errorf("the caller's own string is not echoed back into a message a client renders, "+
			"got %q", resp.Error.Message)
	}
	if !strings.Contains(resp.Error.Message, "example/platform") {
		t.Errorf("the refusal names the repository this install does watch, got %q",
			resp.Error.Message)
	}
}

// Every free-text field is tagged and capped, including the ones this tool is
// the first to publish.
//
// The caps are the same ones the verdict's fields go through, because these
// are the verdict's fields: a handoff carries findings, and a client's context
// is a resource this surface can spend without ever seeing the bill.
func TestTheHandoffQueuesFreeTextIsTaggedAndCapped(t *testing.T) {
	flood := strings.Repeat("A", 200_000)

	g := escalated()
	pr := &g.Open[0]
	pr.Title = flood
	pr.Verdict.Findings[0].Subject = flood
	pr.Verdict.Findings[0].Detail = flood
	pr.Verdict.Findings[0].Reason = flood

	got := newFixture(t, nil).withGate(g).handoff(t)
	entry := (*got.Waiting)[0]
	first := (*entry.Findings)[0]

	for name, text := range map[string]Text{
		"title":   *entry.Title,
		"subject": first.Subject,
		"summary": first.Summary,
		"reason":  *first.Reason,
	} {
		if len(text.Text) >= len(flood) {
			t.Errorf("%s was published at its full %d characters", name, len(text.Text))
		}
		if !text.Truncated {
			t.Errorf("%s was cut and does not say so, which is indistinguishable from a "+
				"value that ended in an ellipsis of its own", name)
		}
		if text.Origin == "" {
			t.Errorf("%s carries somebody else's words with no origin to fence them by", name)
		}
	}
}

// The gate could not run, and the pull request was handed over anyway.
//
// A verdict that failed is not a verdict that found nothing, and this is the
// entry where the two are most easily confused: there are no findings to
// publish, so the reason nothing was judged is the only thing the entry has to
// say.
func TestAHandoffForAPullRequestTheGateCouldNotJudgeCarriesTheError(t *testing.T) {
	g := escalated()
	pr := &g.Open[0]
	pr.State, pr.Verdict, pr.Err = StateError, nil, "helm exited 1"

	entry := (*newFixture(t, nil).withGate(g).handoff(t).Waiting)[0]

	if entry.State != StateError {
		t.Fatalf("the state travels as the gate reported it, got %q", entry.State)
	}
	if entry.Error == nil || !strings.Contains(entry.Error.Text, "helm exited 1") {
		t.Fatalf("a gate that could not run says what stopped it, got %+v", entry.Error)
	}
	if entry.Error.Origin == OriginBosun {
		t.Errorf("the error quotes whatever refused the render, got %+v", entry.Error)
	}
	if entry.Findings != nil || entry.Blockers != nil {
		t.Errorf("nothing judged this pull request, so there is no breakdown to publish: "+
			"%+v %+v", entry.Findings, entry.Blockers)
	}
}
