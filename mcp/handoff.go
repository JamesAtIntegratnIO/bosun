package mcp

import (
	"encoding/json"
	"strings"
	"time"
)

// handoff_queue: every open pull request waiting on a human.
//
// The agent labels a pull request `needs-human` when it gives up on a
// mechanical fix, and that label is the whole of what survives in this
// process. Without this, an on-call agent that wants the queue has to list
// pull requests by label against the git host and then read each verdict back
// -- a round trip bosun already made, once per sweep, and threw away.
//
// So this is gate_status's list narrowed to the pull requests somebody is
// expected to act on, with the findings attached rather than a number to go
// and fetch them by. Nothing here is computed: the labels are on the gate's
// snapshot because the attempt cap's memory is a label, the verdict is the one
// gate_verdict would answer with, and the attempt count is the agent's own.
//
// The absence rule is gate_status's, including the held-over queue, and it
// matters more here than it does there. An empty queue read from a sweep that
// could not list says "nobody is waiting on you" to the one caller who acts on
// that by going home -- so an empty list is published only where something
// actually listed, and where that listing is an earlier sweep's the sentence
// beside it says so.

// handoffQueueDescription is what a client hands its model as this tool's
// purpose. A constant, and it has to stay one: see pipelineReportDescription.
const handoffQueueDescription = "Every open pull request waiting on a human: the ones bosun's " +
	"last gate sweep saw carrying the needs-human label, which is what the agent applies when " +
	"it stops short of a mechanical fix and will not try again on its own. Each one carries " +
	"the verdict standing against its head commit -- the state, the blocker breakdown and " +
	"every finding behind it, with the files and settings a repair would have touched -- and " +
	"how many automatic fix attempts it has already spent against its cap. Use it to work the " +
	"queue that is blocked rather than the queue that is merely open. If the sweep could not " +
	"list pull requests at all, the result says so in its own field, so an empty queue is " +
	"never confused with a gate that could not look. Answers from the last sweep's snapshot: " +
	"it reaches no cluster, no git host and no model, and it can change nothing."

// handoffQueueParams is the tool's input schema: the repository qualifier and
// nothing else, read by Server.qualifier, where the reasoning is.
var handoffQueueParams = json.RawMessage(`{"type":"object","properties":{` +
	`"repository":{"type":"string",` +
	`"description":"The repository, owner/repo. Optional: an install watches exactly one, and ` +
	`omitting this asks about that one."}},` +
	`"additionalProperties":false}`)

// labelNeedsHuman is the label that puts a pull request in this queue.
//
// A literal here rather than a value read from the agent, because this package
// imports the result types and the redactor and nothing else -- the rule that
// keeps a credential out of every result also keeps `agent` off the import
// list. So the two halves of this contract cannot see each other, and
// mcp_handoff_test.go in the repository root is what holds them together: it
// derives the label the agent writes from agent's own source and drives this
// surface with it.
//
// Matched without regard to case, because a git host that folds label names
// when it creates them hands back whatever case the first person to apply one
// used, and a handoff missed over a capital letter is a person waiting for
// nobody.
const labelNeedsHuman = "needs-human"

// Handoff is what handoff_queue returns.
//
// Waiting is a POINTER to a slice for the reason Queue.Open is: absent and
// empty are different answers, and the expensive mistake is reading the second
// as the first. Absent means nothing has listed pull requests -- before the
// first sweep, or after one that could not -- and empty means a sweep listed
// them and none was labelled.
type Handoff struct {
	// Repository this queue is for, "owner/repo".
	Repository string `json:"repository"`

	// Swept is whether any gate sweep has completed since this process
	// started, and SweptAt and AgeSeconds say when and how long ago.
	//
	// SweptAt is when the last sweep FINISHED, successfully or not. When
	// SweepError is present the pull requests below are older than that.
	Swept      bool       `json:"swept"`
	SweptAt    *time.Time `json:"sweptAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`

	// Status is the queue in words, and it is bosun's own sentence in every
	// case: before the first sweep, for a sweep that could not list, and for
	// a queue with pull requests in it.
	Status Text `json:"status"`

	// SweepError is what stopped the last sweep from listing pull requests.
	// Present only when one did, and the whole point of the field: "nobody is
	// waiting on you" and "could not look" must not read the same.
	SweepError *Text `json:"sweepError,omitempty"`

	// AttemptCap is how many automatic fix attempts each pull request gets.
	//
	// One number for the queue rather than one per entry, because it is what
	// the operator configured rather than something read from the world: it
	// is the same for every pull request and it is known before anything has
	// looked, which is why it is present even before the first sweep.
	AttemptCap int `json:"attemptCap"`

	// Waiting is every open pull request the last sweep that listed saw
	// carrying the label. Absent when none has listed, empty when one listed
	// and nothing was labelled.
	Waiting *[]HandoffPR `json:"waiting,omitempty"`
}

// HandoffPR is one pull request the agent handed over, and everything a person
// picking it up would otherwise have to fetch.
//
// The findings travel here, unlike in gate_status's queue, and the difference
// is who is reading. A queue is read to choose which pull request to ask
// about, so multiplying every rendered object's name by its length would be
// spending a caller's context on rows they are about to discard. This list is
// the work itself: every entry on it is one somebody has already been asked to
// do, and the fields a repair would have used -- the manifests that still
// declare a dropped version, the settings a bump stopped reading, the
// migration -- are what the handoff is about.
//
// What is not here is the coverage the run lost, which stays on gate_verdict:
// it qualifies the verdict rather than describing the work, and a caller who
// needs it is one call away.
//
// Which leaves a type that is QueuePR plus three fields, spelled out rather
// than built from it, and that is the same decision Verdict already makes
// about the same fields. Each of these is a published schema somebody else's
// client parses, and embedding would tie two of them together: an entry gained
// or documented on gate_status's queue would silently change what this tool
// returns, on a surface whose whole discipline is that a shape cannot move
// without a reviewer seeing the diff. The golden files are how that is
// noticed, and they can only notice it per tool.
type HandoffPR struct {
	// Number is the pull request, and the argument to every per-pull-request
	// tool here.
	Number int `json:"number"`
	// State is the verdict standing against the head commit: passing,
	// failing, error (the gate could not run), running (a render is in
	// flight), or unknown (the sweep neither produced a verdict nor read
	// one). The same five words gate_verdict uses.
	//
	// A handed-off pull request is usually failing and does not have to be:
	// the agent escalates a render it cannot repair as readily as one it
	// judged, and a maintainer can label anything.
	State string `json:"state"`
	// HeadCommit is what the state is about, and the field a client caches
	// against.
	HeadCommit string `json:"headCommit,omitempty"`
	// URL is where the person picking this up will go. Composed by the git
	// host from its own address and the number above, so it carries no
	// origin: there is no free text in it.
	URL string `json:"url,omitempty"`
	// Title is the pull request's own title, written by whoever opened it.
	Title *Text `json:"title,omitempty"`

	// Attempts is how many automatic fix attempts the agent has spent on this
	// pull request, counted by the agent from the labels standing on it.
	//
	// Absent when this process holds no count for it, which is also when the
	// labels it was counted from are not being published -- a number nothing
	// stands behind. The distinction it carries is the one a triager wants
	// first: an agent that ran out of attempts and one that refused on the
	// first look are different situations wanting different people.
	Attempts *int `json:"attempts,omitempty"`
	// Exhausted is whether the cap is spent, published so a client does not
	// have to know that the comparison is `>=`. Absent exactly when Attempts
	// is: the answer to "would it have tried again" is unknown, not "no".
	Exhausted *bool `json:"attemptsExhausted,omitempty"`

	// Blocking is whether the verdict stops the merge. Absent when this
	// process holds no verdict, where "false" would read as "nothing blocks".
	Blocking *bool `json:"blocking,omitempty"`
	// Blockers is the counted breakdown, absent with no verdict and present
	// and possibly all zeroes with one.
	Blockers *Blockers `json:"blockers,omitempty"`
	// Findings is every reason behind those counts, in the order the report
	// reads. Absent with no verdict; empty means the gate looked and found
	// nothing -- which is a pull request labelled by a person rather than by
	// the agent, and is worth being able to tell apart.
	Findings *[]VerdictFinding `json:"findings,omitempty"`
	// Error is the gate failing to run, present exactly when State is error.
	// A different thing from a failing verdict: one says the change is bad,
	// the other that nothing judged it.
	Error *Text `json:"error,omitempty"`
}

// handoffQueue answers from the last gate sweep and the agent's own count, and
// from nothing else.
func (s *Server) handoffQueue(raw json.RawMessage) (any, error) {
	if err := s.qualifier(raw); err != nil {
		return nil, err
	}

	tr := s.triage()
	out := Handoff{Repository: s.Repository, AttemptCap: tr.MaxAttempts}

	g := s.gate()
	if g.SweptAt.IsZero() {
		out.Status = say(handoffUnswept, OriginBosun, maxSummary)
		return out, nil
	}
	out.SweptAt, out.AgeSeconds, out.Swept = s.stamp(g.SweptAt)
	if g.Err != "" {
		// Bosun's sentence with the git host's error in it, which is exactly
		// the string a misconfigured host is most likely to echo a credential
		// back inside.
		out.SweepError = ptr(say(g.Err, OriginCluster, maxNote))
	}

	waiting := make([]HandoffPR, 0, len(g.Open))
	for i := range g.Open {
		if waitsOnAHuman(g.Open[i].Labels) {
			waiting = append(waiting, handoffPR(g.Open[i], tr))
		}
	}

	switch {
	case g.Err != "" && len(g.Open) == 0:
		// Nothing was listed and nothing is held over, so there is no queue
		// to publish. An empty list here would be the exact claim the
		// sweepError field exists to deny.
		out.Status = say(handoffSweepFailed, OriginBosun, maxSummary)
		return out, nil
	case g.Err != "":
		out.Status = say(handoffHeldOver, OriginBosun, maxSummary)
	case len(waiting) == 0:
		out.Status = say(handoffEmpty, OriginBosun, maxSummary)
	default:
		out.Status = say(handoffWaiting, OriginBosun, maxSummary)
	}
	out.Waiting = &waiting
	return out, nil
}

// waitsOnAHuman reports whether these labels put a pull request in the queue.
func waitsOnAHuman(labels []string) bool {
	for _, l := range labels {
		if strings.EqualFold(strings.TrimSpace(l), labelNeedsHuman) {
			return true
		}
	}
	return false
}

// handoffPR maps one handed-over pull request onto the wire.
//
// Dull, like every mapping here: the gate decided the state and the findings
// and the agent counted the attempts, and this adds the origin tags and the
// caps and not one fact.
func handoffPR(pr GatePR, tr TriageStatus) HandoffPR {
	out := HandoffPR{
		Number:     pr.Number,
		State:      pr.State,
		HeadCommit: pr.HeadSHA,
		URL:        pr.URL,
	}
	if pr.Title != "" {
		out.Title = ptr(say(pr.Title, OriginAuthor, maxTitle))
	}
	if pr.State == StateError && pr.Err != "" {
		// Guarded on the state rather than on the string alone, for the
		// reason gate_verdict guards the same pair: the field's documentation
		// promises a caller that the two travel together, and a promise a
		// caller branches on should not rest on that staying true upstream.
		out.Error = ptr(say(pr.Err, OriginRender, maxNote))
	}
	if n, ok := tr.Attempts[pr.Number]; ok {
		// Counted by the agent and published here, never recounted: the cap's
		// only memory is a label under a prefix that follows the brand, and a
		// second count would disagree with the first exactly on a renamed
		// install -- where one says an attempt remains and the other has
		// already escalated. triage_status publishes the same number under
		// the same rule, and this is deliberately not a second reading of it.
		out.Attempts = &n
		out.Exhausted = ptr(n >= tr.MaxAttempts)
	}
	if v := pr.Verdict; v != nil {
		blocking := v.Blocking
		out.Blocking = &blocking
		// A conversion rather than eight assignments, for the reason
		// gate_verdict does the same: Go refuses it the moment the two stop
		// being field-identical, which is a better guard than a list of eight
		// names that can go stale one at a time.
		b := Blockers(v.Blockers)
		out.Blockers = &b
		findings := make([]VerdictFinding, 0, len(v.Findings))
		for _, f := range v.Findings {
			findings = append(findings, verdictFinding(f))
		}
		out.Findings = &findings
		if out.HeadCommit == "" {
			out.HeadCommit = v.HeadRev
		}
	}
	return out
}

// The sentences, one per shape the queue can be in.
//
// Constants with nothing interpolated into them, which is what lets every one
// of them be published as bosun's own. A client branches on the fields; the
// model it is speaking for reads these, and both have to be told the same
// thing.
const (
	handoffWaiting = "These are the open pull requests the last gate sweep saw carrying the " +
		"needs-human label: the agent has stopped on each of them and will not act again on " +
		"its own. The findings behind each one are here; ask triage_status about one for what " +
		"the agent is doing now, and gate_verdict for the coverage the run lost."

	handoffEmpty = "The last gate sweep listed the open pull requests and none of them is " +
		"waiting on a human. This is a sweep that looked, rather than a gate that could not: a " +
		"sweep that failed to list says so in sweepError instead."

	handoffSweepFailed = "The last gate sweep could not list open pull requests at all, and no " +
		"earlier sweep left a queue to fall back on, so nothing is claimed about what is " +
		"waiting on a human. This is not an empty queue. The sweepError field says what " +
		"stopped it."

	handoffHeldOver = "The last gate sweep could not list open pull requests, so this queue is " +
		"what an earlier sweep saw and is older than the sweep time above. A pull request may " +
		"have been picked up, merged or handed over since. The sweepError field says what " +
		"stopped the last one."

	handoffUnswept = "No gate sweep has completed yet, so it is not known whether anything is " +
		"waiting on a human. This is not an empty queue: nothing has looked. Ask again after " +
		"the next sweep."
)
