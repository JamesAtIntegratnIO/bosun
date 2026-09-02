package mcp

import (
	"encoding/json"
	"slices"
	"time"
)

// triage_status: what the agent is doing about one pull request, right now.
//
// The distinction this tool exists to draw is the one the agent's own commit
// status draws for a person: working, versus finished with nothing more to
// say. Both look identical from outside -- a pull request with a comment on it
// and no new commits -- and the difference decides whether somebody waits or
// picks it up.
//
// Two of the three answers are as old as the last gate sweep, and one is not.
// The phase comes from this process's own in-flight state, which is current to
// the microsecond; the labels and therefore the attempt count come from the
// sweep's snapshot, because a tool call may not reach the git host. The result
// carries the sweep time for exactly that reason.

// triageStatusDescription is what a client hands its model as this tool's
// purpose. A constant, and it has to stay one: see pipelineReportDescription.
const triageStatusDescription = "What bosun's agent is doing about one pull request: the phase " +
	"it is in, how many automatic fix attempts it has spent against its cap, and the labels " +
	"standing on the pull request. Use it to tell an agent that is still working from one that " +
	"has finished and will not try again. A pull request the agent is not working is answered " +
	"as such rather than as an error. The phase is this process's own current state; the labels " +
	"and the attempt count are as old as the sweep the result names. Answers from a snapshot: " +
	"it reaches no cluster, no git host and no model, and it can change nothing."

// triageStatusParams is the tool's input schema. Same shape as gate_verdict's,
// and read by the same function: Server.pullRequest, where the reasoning is.
var triageStatusParams = json.RawMessage(`{"type":"object","properties":{` +
	`"pullRequest":{"type":"integer","minimum":1,` +
	`"description":"The pull request number to report on."},` +
	`"repository":{"type":"string",` +
	`"description":"The repository, owner/repo. Optional: an install watches exactly one, and ` +
	`omitting this asks about that one."}},` +
	`"required":["pullRequest"],"additionalProperties":false}`)

// TriageStatus is what the agent is doing, in this package's own shapes.
//
// A copy of the promotion endpoint's account of itself rather than the thing
// itself, for the reason GateStatus is a copy: this package imports the result
// types and the redactor and nothing else, so no field path from a tool result
// can reach a credential. The composition root does the adapting, which is
// where the two vocabularies are allowed to know about each other.
type TriageStatus struct {
	// InFlight is every pull request being triaged right now, and Queued
	// every one with a newer promotion waiting behind the run in flight.
	InFlight []int
	Queued   []int

	// MaxAttempts is the cap on automatic fix attempts per pull request. A
	// setting rather than a reading, so it is known before anything has
	// looked.
	MaxAttempts int

	// Attempts is how many attempts each pull request has spent, counted by
	// the agent from the labels standing on it.
	//
	// Counted there rather than here, and that is the seam rather than a
	// preference. The cap's only memory is a label under a prefix that
	// follows the agent's brand, so a second count in this package would be a
	// second answer to "will it try again" -- and the two would disagree
	// exactly on a renamed install, where one of them says an attempt remains
	// and the other has already escalated.
	//
	// A pull request the last sweep did not see has no entry, and this
	// surface publishes no attempt count for it rather than a zero.
	Attempts map[int]int
}

// Triage is what triage_status returns.
//
// Absence means "not known" throughout, and the two sources differ in when
// they know. Phase is always present because this process always knows what it
// is running; the labels and the attempt count are absent whenever the sweep
// has not seen the pull request, which includes before the first sweep.
type Triage struct {
	// Repository this answer is about, "owner/repo".
	Repository string `json:"repository"`
	// PullRequest is the number asked about, echoed so a client batching
	// calls can tell the answers apart.
	PullRequest int `json:"pullRequest"`

	// Swept is whether any gate sweep has completed since this process
	// started, and SweptAt and AgeSeconds say when and how long ago. They
	// date the labels and the attempt count below, not the phase.
	Swept      bool       `json:"swept"`
	SweptAt    *time.Time `json:"sweptAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`

	// Phase is what the agent is doing about this pull request:
	//
	//   running  a triage for it is in flight now
	//   queued   one is in flight and a newer promotion is waiting behind it
	//   idle     the agent is not working it; the attempt count says whether
	//            it ever did
	//   absent   the last gate sweep did not see it open, and nothing is
	//            running for it
	//   unknown  the last gate sweep could not list pull requests, so whether
	//            it is open and what it has spent are both unknown
	//   unswept  no gate sweep has completed, and nothing is running for it
	//
	// The first two are this process's own state and outrank the rest: the
	// agent knows what it is running whatever a sweep last saw. The last
	// three are separate for the reason the whole surface exists -- "the
	// sweep looked and this was not open", "the sweep could not look" and
	// "nothing has looked" are three different answers, and a client should
	// not have to read a second field to tell them apart. They are spelled
	// the way gate_verdict's states of the same name are, so a client that
	// branches on those words does not need a second table.
	Phase string `json:"phase"`

	// Status is the same answer in words, and it is bosun's own sentence in
	// every case.
	Status Text `json:"status"`

	// SweepError is what stopped the last sweep from listing pull requests.
	// Present only when one did: with it, the absence of labels below is a
	// sweep that could not look rather than a pull request that is not there.
	SweepError *Text `json:"sweepError,omitempty"`

	// URL is where a person can read the pull request, and Title is what
	// whoever opened it called it. Both absent when the sweep does not have
	// this pull request.
	URL   string `json:"url,omitempty"`
	Title *Text  `json:"title,omitempty"`

	// Attempts is how many automatic fix attempts have been spent on this
	// pull request, and AttemptCap is how many it gets. Attempts is absent
	// when the sweep has not seen the pull request, because the labels are
	// where that number is written down and a zero would claim it had spent
	// nothing.
	//
	// The cap is present always: it is what the operator configured rather
	// than something read from the world.
	Attempts   *int `json:"attempts,omitempty"`
	AttemptCap int  `json:"attemptCap"`

	// Exhausted is whether the cap is spent, published so a client does not
	// have to know that the comparison is `>=`. Absent exactly when Attempts
	// is: the answer to "will it try again" is unknown, not "yes".
	Exhausted *bool `json:"attemptsExhausted,omitempty"`

	// Labels are what stands on the pull request, as the last sweep saw them.
	// Absent when it has not seen it; empty means it looked and there were
	// none.
	//
	// Published because they are the cap's memory and the escalation's only
	// durable record: a caller reading them learns that a human was asked for
	// without spending a git-host call to find out.
	Labels *[]Text `json:"labels,omitempty"`
}

// The phases, as constants rather than as literals in a switch.
//
// `absent`, `unknown` and `unswept` are spelled the way gate_verdict's states
// of the same name are, because they mean the same thing about the same sweep:
// one vocabulary for one situation, so a client that already branches on those
// words does not need a second table.
const (
	PhaseRunning = "running"
	PhaseQueued  = "queued"
	PhaseIdle    = "idle"
	PhaseAbsent  = "absent"
	PhaseUnknown = "unknown"
	PhaseUnswept = "unswept"
)

// triageStatus answers from this process's own state and the last sweep's
// snapshot, and from nothing else.
func (s *Server) triageStatus(raw json.RawMessage) (any, error) {
	number, err := s.pullRequest(raw)
	if err != nil {
		return nil, err
	}

	tr := s.triage()
	out := Triage{
		Repository:  s.Repository,
		PullRequest: number,
		AttemptCap:  tr.MaxAttempts,
	}

	g := s.gate()
	var pr *GatePR
	if out.SweptAt, out.AgeSeconds, out.Swept = s.stamp(g.SweptAt); out.Swept {
		if g.Err != "" {
			out.SweepError = ptr(say(g.Err, OriginCluster, maxNote))
		}
		pr = g.open(number)
	}

	if pr != nil {
		out.URL = pr.URL
		if pr.Title != "" {
			out.Title = ptr(say(pr.Title, OriginAuthor, maxTitle))
		}
		labels := make([]Text, 0, len(pr.Labels))
		for _, l := range pr.Labels {
			labels = append(labels, say(l, OriginLabel, maxName))
		}
		out.Labels = &labels

		// The count is published only for a pull request the sweep saw, which
		// is the same condition the labels it was counted from are published
		// under. A count without them would be a number a caller could not
		// check against anything.
		if n, ok := tr.Attempts[number]; ok {
			out.Attempts = &n
			// No floor under the cap, and that is deliberate. MAX_ATTEMPTS is
			// an operator's number and nothing validates it, so a cap of zero
			// is reachable -- and an agent configured that way escalates on
			// the first attempt rather than repairing. Treating zero as "no
			// cap" here would publish "it will try again" for an agent that
			// never will, which is the one reading this field exists to
			// prevent.
			out.Exhausted = ptr(n >= tr.MaxAttempts)
		}
	}

	out.Phase, out.Status = triagePhase(tr, number, g, pr, out.Exhausted)
	return out, nil
}

// triagePhase decides what the agent is doing and the sentence that says it.
//
// In flight first, and that ordering is the decision: the process knows what
// it is running now, and the sweep's account of the world is up to a poll
// interval old. A pull request opened and promoted since the last sweep is
// being triaged and is not in any snapshot, and reporting that as "the sweep
// never saw it" would be the one wrong answer available here.
func triagePhase(tr TriageStatus, number int, g GateStatus, pr *GatePR, exhausted *bool) (string, Text) {
	// Queued first, and not nested inside the in-flight check. A promotion
	// waiting behind a run is only ever recorded beside the run it is waiting
	// for, so today the two travel together -- but a queued pull request that
	// this function did not also find in flight would fall through to `idle`,
	// which is the one word it cannot be: something is waiting to be triaged.
	if slices.Contains(tr.Queued, number) {
		return PhaseQueued, say(triageQueued, OriginBosun, maxSummary)
	}
	if slices.Contains(tr.InFlight, number) {
		return PhaseRunning, say(triageRunning, OriginBosun, maxSummary)
	}
	switch {
	case g.SweptAt.IsZero():
		return PhaseUnswept, say(triageUnswept, OriginBosun, maxSummary)
	case pr == nil && g.Err != "":
		// Not `absent`: a sweep that could not list is the absence of
		// evidence, and reporting it as evidence of absence is how a caller
		// concludes a pull request was merged when the gate merely lost its
		// token.
		return PhaseUnknown, say(triageSweepFailed, OriginBosun, maxSummary)
	case pr == nil:
		return PhaseAbsent, say(triageAbsent, OriginBosun, maxSummary)
	case exhausted != nil && *exhausted:
		return PhaseIdle, say(triageSpent, OriginBosun, maxSummary)
	}
	return PhaseIdle, say(triageIdle, OriginBosun, maxSummary)
}

// The sentences, one per phase, plus the two flavours of idle.
//
// Constants with nothing interpolated into them, which is what lets every one
// of them be published as bosun's own.
const (
	triageRunning = "A triage for this pull request is running now. It may push a fix, explain " +
		"the gate, or escalate to a human; nothing is decided yet. Ask again shortly."

	triageQueued = "A newer promotion for this pull request is waiting to be triaged, behind a " +
		"run already in flight for it. The waiting one starts when that run finishes, so what " +
		"is on the pull request now is not the agent's last word."

	triageIdle = "The agent is not working this pull request. That is the resting state rather " +
		"than a failure: it triages on a promotion and stops when it has commented, pushed, or " +
		"escalated. The attempt count says whether it has worked this one before, and the " +
		"labels say how it left it."

	triageSpent = "The agent is not working this pull request and will not try again on its " +
		"own: every automatic fix attempt it is allowed has been spent. What happens next needs " +
		"a person."

	triageAbsent = "The last gate sweep did not see this pull request among the open ones, and " +
		"the agent is not working it. It may have been merged or closed, or opened since the " +
		"sweep ran. Nothing here is a claim about its labels or its attempts."

	triageSweepFailed = "The last gate sweep could not list open pull requests at all, so it is " +
		"not known whether this one is open, what labels stand on it, or what it has spent. " +
		"The agent is not working it now, which is the one thing here that is current. The " +
		"sweepError field says what stopped the sweep."

	triageUnswept = "No gate sweep has completed yet, so nothing is known about this pull " +
		"request's labels or its attempts. The agent is not working it now, which is the one " +
		"thing here that is current, and is not a claim that it has finished with it."
)

// triage is what the agent is doing, or the zero value when there is nothing
// to read.
//
// The nil check is here rather than at every call site for the same reason
// Server.gate has one: an agent that is working nothing is the honest reading
// of a process with no triage endpoint wired, and it is what a caller should
// see.
func (s *Server) triage() TriageStatus {
	if s.Triage == nil {
		return TriageStatus{}
	}
	return s.Triage()
}
