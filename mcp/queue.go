package mcp

import (
	"encoding/json"
	"time"
)

// gate_status: the queue, and the verdict standing on each of it.
//
// gate_verdict answers about one pull request somebody already knows the
// number of. This is the call before that one: what is open, what the gate
// made of each, and -- the field this tool exists for -- whether the gate
// could see anything at all.
//
// A gate that cannot reach its git host has exactly two symptoms. One is an
// error in a log nobody is reading. The other is a queue that says nothing is
// open, forever, which is indistinguishable from a quiet week and is the
// reading a caller will take. So the sweep's own failure travels beside the
// queue rather than under it, and an empty queue is published only by a sweep
// that actually listed.

// gateStatusDescription is what a client hands its model as this tool's
// purpose. A constant, and it has to stay one: see pipelineReportDescription.
const gateStatusDescription = "The queue: every open pull request bosun's last gate sweep saw, " +
	"with the verdict standing against each head commit -- the state, whether it blocks, and " +
	"the blocker breakdown as counts per kind. Call gate_verdict for the findings behind one " +
	"of those counts. If the sweep could not list pull requests at all, the result says so in " +
	"its own field, so an empty queue is never confused with a gate that could not look. " +
	"Answers from the last sweep's snapshot: it reaches no cluster, no git host and no model, " +
	"and it can change nothing."

// Queue is what gate_status returns.
//
// Open is a POINTER to a slice for the reason Report.Findings is: absent and
// empty are different answers, and the expensive mistake is reading the second
// as the first. Absent means nothing has listed pull requests -- before the
// first sweep, or after one that could not -- and empty means a sweep listed
// them and there were none.
type Queue struct {
	// Repository this queue is for, "owner/repo".
	Repository string `json:"repository"`

	// Swept is whether any gate sweep has completed since this process
	// started, and SweptAt and AgeSeconds say when and how long ago.
	//
	// SweptAt is when the last sweep FINISHED, successfully or not. When
	// SweepError is present the pull requests below are older than that: they
	// are what an earlier sweep saw, kept because they are evidence and a
	// caller with stale evidence is better off than one with none.
	Swept      bool       `json:"swept"`
	SweptAt    *time.Time `json:"sweptAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`

	// Status is the queue in words, and it is bosun's own sentence in every
	// case: before the first sweep, for a sweep that could not list, and for
	// a queue with pull requests in it.
	Status Text `json:"status"`

	// SweepError is what stopped the last sweep from listing pull requests.
	// Present only when one did, and the whole point of the field: "nothing
	// open" and "could not look" must not read the same.
	SweepError *Text `json:"sweepError,omitempty"`

	// Open is every pull request the last sweep that listed saw. Absent when
	// none has, empty when one listed and found nothing open.
	Open *[]QueuePR `json:"open,omitempty"`

	// Held is how many verdicts this process has in memory, and Running how
	// many gate runs are in flight right now. Absent before the first sweep,
	// where a zero would read as "nothing to judge" from a gate that has not
	// looked.
	//
	// The pair is how a caller tells a queue that has been judged from one
	// that is being judged: a pull request in `running` state is a verdict
	// worth asking about again in a minute, and these say how many of those
	// there are without walking the list.
	Held    *int `json:"held,omitempty"`
	Running *int `json:"running,omitempty"`
}

// QueuePR is one open pull request and the verdict standing on its head
// commit.
//
// The counted breakdown and nothing below it. A queue is read to decide which
// pull request to ask about, and gate_verdict is where the asking happens --
// so the findings, the not-covered list and every rendered object's name stay
// there rather than being multiplied by the length of the queue into a
// caller's context.
type QueuePR struct {
	// Number is the pull request, and the argument to every per-pull-request
	// tool here.
	Number int `json:"number"`
	// State is the verdict standing against the head commit: passing,
	// failing, error (the gate could not run), running (a render is in
	// flight), or unknown (the sweep neither produced a verdict nor read
	// one). The same five words gate_verdict uses.
	State string `json:"state"`
	// HeadCommit is what the state is about, and the field a client caches
	// against.
	HeadCommit string `json:"headCommit,omitempty"`
	// URL is where a person can read the pull request. Composed by the git
	// host from its own address and the number above, so it carries no
	// origin: there is no free text in it.
	URL string `json:"url,omitempty"`
	// Title is the pull request's own title, written by whoever opened it.
	Title *Text `json:"title,omitempty"`

	// Blocking is whether the verdict stops the merge. Absent when this
	// process holds no verdict, where "false" would read as "nothing blocks".
	Blocking *bool `json:"blocking,omitempty"`
	// Blockers is the counted breakdown, absent with no verdict and present
	// and possibly all zeroes with one.
	Blockers *Blockers `json:"blockers,omitempty"`
	// Error is the gate failing to run, present exactly when State is error.
	// A different thing from a failing verdict: one says the change is bad,
	// the other that nothing judged it.
	Error *Text `json:"error,omitempty"`
}

// gateStatus answers from the last gate sweep, and from nothing else.
func (s *Server) gateStatus(json.RawMessage) (any, error) {
	out := Queue{Repository: s.Repository}

	g := s.gate()
	if g.SweptAt.IsZero() {
		out.Status = say(noGateSweepYet, OriginBosun, maxSummary)
		return out, nil
	}

	out.SweptAt, out.AgeSeconds, out.Swept = s.stamp(g.SweptAt)
	out.Held, out.Running = ptr(g.Held), ptr(g.Running)

	if g.Err != "" {
		// Bosun's sentence with the git host's error in it, which is exactly
		// the string a misconfigured host is most likely to echo a credential
		// back inside.
		out.SweepError = ptr(say(g.Err, OriginCluster, maxNote))
		out.Status = say(queueSweepFailed, OriginBosun, maxSummary)
		if len(g.Open) == 0 {
			// Nothing was listed and nothing is held over, so there is no
			// queue to publish. An empty list here would be the exact claim
			// the field above exists to deny.
			return out, nil
		}
		out.Status = say(queueHeldOver, OriginBosun, maxSummary)
	} else {
		out.Status = say(queueListed, OriginBosun, maxSummary)
		if len(g.Open) == 0 {
			out.Status = say(queueEmpty, OriginBosun, maxSummary)
		}
	}

	open := make([]QueuePR, 0, len(g.Open))
	for i := range g.Open {
		open = append(open, queuePR(g.Open[i]))
	}
	out.Open = &open
	return out, nil
}

// queuePR maps one pull request onto the wire.
//
// Dull, like every mapping here: the gate decided the state and the counts,
// and this adds the origin tags and the caps and not one fact.
func queuePR(pr GatePR) QueuePR {
	out := QueuePR{
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
	if v := pr.Verdict; v != nil {
		blocking := v.Blocking
		out.Blocking = &blocking
		// A conversion rather than eight assignments, for the reason
		// gate_verdict does the same: Go refuses it the moment the two stop
		// being field-identical, which is a better guard than a list of eight
		// names that can go stale one at a time.
		b := Blockers(v.Blockers)
		out.Blockers = &b
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
	queueListed = "This is every pull request the last gate sweep saw open, with the verdict " +
		"standing against each head commit. Ask gate_verdict about one of them for the " +
		"findings behind its counts."

	queueEmpty = "The last gate sweep listed open pull requests and there were none. This is a " +
		"sweep that looked, rather than a gate that could not: a sweep that failed to list " +
		"says so in sweepError instead."

	queueSweepFailed = "The last gate sweep could not list open pull requests at all, and no " +
		"earlier sweep left one to fall back on, so nothing is claimed about what is open. " +
		"This is not an empty queue. The sweepError field says what stopped it."

	queueHeldOver = "The last gate sweep could not list open pull requests, so this queue is " +
		"what an earlier sweep saw and is older than the sweep time above. A pull request may " +
		"have been merged or opened since. The sweepError field says what stopped the last one."
)
