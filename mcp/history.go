package mcp

import (
	"encoding/json"
	"time"
)

// verdict_history: what the gate said on each earlier head commit, so a
// flapping gate is data rather than somebody's impression of a comment.
//
// gate_verdict answers about the head commit standing now. This answers about
// the ones before it, which is the difference between "my last push fixed it"
// and "the gate changed its mind" -- two readings of the same green that want
// opposite next moves, and until now the only way to tell them apart was for a
// person to expand a collapsed table in a pull-request comment.
//
// The rows exist because a gate with no database still has to know what it
// said last time, and the only per-pull-request storage a git host offers is
// the comment itself. So the gate writes its memory in as HTML stamps and
// parses them back on the next run. This tool publishes that parse instead of
// letting it be thrown away, which makes the stamp format load-bearing in a
// second place: a change to it is now a change a client can see, and
// mcp/testdata is where a reviewer sees it.
//
// It also puts a comment editor in the trust picture, which nothing else on
// this surface does. Every other answer here was computed in this process from
// what a cluster or a render said; these rows came off the git host, out of a
// comment anybody with write access to the repository can edit. The result
// says where they were read from for exactly that reason, and the gate's own
// stamp grammar is broken in them like everything else -- the sharpest case on
// the surface, since these rows are parsed out of the very comment the stamps
// live in.

// verdictHistoryDescription is what a client hands its model as this tool's
// purpose. A constant, and it has to stay one: see pipelineReportDescription.
const verdictHistoryDescription = "What the gate said about one pull request on each of its " +
	"earlier head commits: the commit, whether that verdict blocked the merge, and the gate's " +
	"own headline for it. Newest first. Use it to tell a push that fixed something from a gate " +
	"that changed its mind, and to count how often a verdict has flipped. The entries are read " +
	"out of the gate's own comment on the pull request, which is where a gate with no database " +
	"keeps its memory, so the result names that comment as the source and publishes the cap on " +
	"how many verdicts it remembers -- a history exactly that long has had older ones dropped. " +
	"A pull request no history has been read for is answered as such rather than as one that " +
	"has never been red. Answers from the last sweep's snapshot: it reaches no cluster, no git " +
	"host and no model, and it can change nothing."

// verdictHistoryParams is the tool's input schema. Same shape as
// gate_verdict's, and read by the same function: Server.pullRequest.
var verdictHistoryParams = json.RawMessage(`{"type":"object","properties":{` +
	`"pullRequest":{"type":"integer","minimum":1,` +
	`"description":"The pull request number to report the earlier verdicts for."},` +
	`"repository":{"type":"string",` +
	`"description":"The repository, owner/repo. Optional: an install watches exactly one, and ` +
	`omitting this asks about that one."}},` +
	`"required":["pullRequest"],"additionalProperties":false}`)

// History is what verdict_history returns.
//
// Absence carries meaning in the same three places gate_verdict's does, in the
// same words, and for the same reason: no sweep has completed, a sweep
// completed and did not see this pull request open, and a sweep saw it and
// there was no comment to read a history from. A client that already branches
// on `unswept` and `absent` needs no second table.
type History struct {
	// Repository this history is about, "owner/repo".
	Repository string `json:"repository"`
	// PullRequest is the number asked about, echoed so a client batching
	// calls can tell the answers apart.
	PullRequest int `json:"pullRequest"`

	// Swept is whether any gate sweep has completed since this process
	// started, and SweptAt and AgeSeconds say when and how long ago.
	Swept      bool       `json:"swept"`
	SweptAt    *time.Time `json:"sweptAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`

	// State says which answer this is:
	//
	//   recorded  a history was read from this pull request's gate comment
	//   unknown   the sweep saw this pull request and no history has been
	//             read for it, or the sweep could not list at all
	//   absent    a sweep completed and did not see this pull request open
	//   unswept   no gate sweep has completed, so nothing has looked at all
	//
	// `recorded` is this tool's own, because it is the only one that is about
	// a history rather than about a verdict, and it is the only state under
	// which the entries below are published.
	//
	// `absent` and `unswept` are gate_verdict's words for the same two
	// situations, meaning exactly what they mean there. `unknown` is its word
	// covering MORE here, and that is worth stating rather than glossing:
	// there it is only "the sweep could not list", here it is that AND the
	// ordinary "the sweep saw this pull request and no comment has been read
	// for it", which is the common answer rather than the broken one.
	// SweepError is what separates them, and Status says which is which.
	State string `json:"state"`

	// Status is the same answer in words, and it is bosun's own sentence in
	// every case.
	Status Text `json:"status"`

	// SweepError is what stopped the last sweep from listing pull requests.
	// Present only when one did: without it "no history was read" and "the
	// gate could not look" would read the same.
	SweepError *Text `json:"sweepError,omitempty"`

	// HeadCommit is the commit standing now, and URL is where a person can
	// read the pull request. Absent when the sweep does not have it.
	//
	// The head is here so a caller can tell which commit the entries stop
	// short of: they are the verdicts BEFORE this one, and the verdict on
	// this one is gate_verdict's answer. The URL is composed by the git host
	// from its own address and the number above, so it carries no origin;
	// there is no free text in it.
	//
	// The pull request's TITLE is deliberately not here, and the other
	// per-pull-request tools carrying one is not a reason to add it. It is a
	// string whoever opened the pull request chose, this tool's subject is
	// what the gate said rather than what the change is, and a client that
	// wants a human label for the number is one call away from gate_verdict.
	// An untrusted field published for symmetry is an untrusted field
	// published for nothing.
	HeadCommit string `json:"headCommit,omitempty"`
	URL        string `json:"url,omitempty"`

	// Source is where these entries were read from. Published exactly when
	// Entries is, because it is a claim about them rather than about the
	// tool.
	//
	// A constant this package writes, not a URL and not a comment id: what a
	// client has to know is that the rows came out of a comment on the pull
	// request rather than out of this process, because that is a different
	// set of people who can write them. `pull-request-comment` is the only
	// value it has.
	Source string `json:"source,omitempty"`

	// Order is the order the entries are in. Published rather than left for a
	// client to infer, because inferring it from two entries is guessing and
	// a client that guesses backwards counts the oldest flip as the newest.
	//
	// `newest-first` is the only value it has.
	Order string `json:"order,omitempty"`

	// HistoryCap is how many earlier verdicts the gate's comment remembers.
	// Published so a client can tell a truncated history from a short one:
	// entries exactly this long means older verdicts have been dropped, and a
	// pull request's whole life is not what is on the wire.
	//
	// Absent when the snapshot did not state one, rather than published as a
	// zero, which would read as "it remembers none".
	HistoryCap *int `json:"historyCap,omitempty"`

	// Entries are the earlier verdicts, NEWEST FIRST. Present exactly when
	// State is `recorded`; empty then means the comment was read and recorded
	// no earlier verdict, which is what a pull request the gate has answered
	// exactly once looks like.
	Entries *[]HistoryEntry `json:"entries,omitempty"`
}

// HistoryEntry is one verdict the gate reached on an earlier head commit.
type HistoryEntry struct {
	// HeadCommit is the commit that verdict was about, in the form the gate
	// recorded -- abbreviated, on a gate that abbreviates.
	//
	// Absent when what was recorded is not a commit. It is the one field here
	// a caller correlates against its own git history, so it is held to the
	// alphabet a commit is written in rather than published as whatever the
	// comment happened to hold: these rows came off the git host, and a value
	// this surface would not vouch for is better missing than believed. The
	// verdict itself is still published, because losing the entry would lose
	// the flip.
	HeadCommit string `json:"headCommit,omitempty"`

	// Blocking is whether that verdict stopped the merge. The field to count
	// flips from: a headline can change wording while the answer stays the
	// same, and this cannot.
	Blocking bool `json:"blocking"`

	// Headline is the gate's own one line for that verdict.
	//
	// Tagged bosun, which is the strongest claim this surface makes about any
	// string, and it is made on the same grounds as gate_verdict's status: a
	// headline is composed from counts and fixed vocabulary and names nothing
	// a render produced, which gate's TestTheVerdictHeadlineIsBosunsOwnWords
	// is what keeps true.
	//
	// The one thing that is different here, and the reason Source is on the
	// result: this copy of it made a round trip through a comment on the git
	// host. Bosun composed the sentence and bosun parsed it back, and in
	// between it sat somewhere a repository writer could edit. The stamp
	// grammar is broken in it and it is length-capped like every other free
	// text here, and a client that needs to know who could have touched it
	// reads Source.
	Headline Text `json:"headline"`
}

// StateRecorded is a history read from the pull request's gate comment, and
// the one state this tool adds.
//
// StateAbsent and StateUnswept are gate_verdict's own and are reused rather
// than respelled: they mean the same thing about the same sweep, and one
// vocabulary for one situation is what lets a client branch on those words
// once. StateUnknown is reused too, and it is the one this tool widens -- see
// historyUnread.
const StateRecorded = "recorded"

// The two closed vocabularies this result publishes, each with exactly one
// value today. Constants rather than literals at the assignment site, because
// a client branches on them and a typo in a string nothing compares is a
// branch that silently never runs.
const (
	// HistorySource is where a recorded history was read from.
	HistorySource = "pull-request-comment"
	// HistoryOrder is the order the entries are published in.
	HistoryOrder = "newest-first"
)

// verdictHistory answers from the last gate sweep's snapshot, and from nothing
// else.
func (s *Server) verdictHistory(raw json.RawMessage) (any, error) {
	number, err := s.pullRequest(raw)
	if err != nil {
		return nil, err
	}

	out := History{Repository: s.Repository, PullRequest: number}

	g := s.gate()
	if g.SweptAt.IsZero() {
		out.State, out.Status = StateUnswept, say(historyUnswept, OriginBosun, maxSummary)
		return out, nil
	}

	out.SweptAt, out.AgeSeconds, out.Swept = s.stamp(g.SweptAt)
	if g.Err != "" {
		out.SweepError = ptr(say(g.Err, OriginCluster, maxNote))
	}

	pr := g.open(number)
	if pr == nil {
		// The same two nothings gate_verdict draws apart, drawn apart here for
		// the same reason: a sweep that ran and did not see this pull request
		// open is evidence, and a sweep that could not list them is the
		// absence of evidence.
		out.State, out.Status = StateAbsent, say(historyAbsent, OriginBosun, maxSummary)
		if g.Err != "" {
			out.State, out.Status = StateUnknown, say(historySweepFailed, OriginBosun, maxSummary)
		}
		return out, nil
	}

	out.HeadCommit = pr.HeadSHA
	out.URL = pr.URL

	if pr.History == nil {
		out.State, out.Status = StateUnknown, say(historyUnread, OriginBosun, maxSummary)
		return out, nil
	}

	out.State, out.Status = StateRecorded, say(historyRecorded, OriginBosun, maxSummary)
	out.Source, out.Order = HistorySource, HistoryOrder
	// Published only when the snapshot states one. A zero would read as a gate
	// that remembers nothing, which is a claim about the gate rather than
	// about what this process was told.
	if g.HistoryCap > 0 {
		limit := g.HistoryCap
		out.HistoryCap = &limit
	}

	// Reversed here, where the order is published and documented, rather than
	// upstream where it is produced: the snapshot carries the order the gate's
	// own comment records, and a snapshot that quietly disagreed with the
	// comment it was parsed from would be harder to notice than a reversal
	// with a field beside it saying which way round it is.
	rows := *pr.History
	entries := make([]HistoryEntry, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		entries = append(entries, historyEntry(rows[i]))
	}
	out.Entries = &entries
	return out, nil
}

// historyEntry maps one recorded verdict onto the wire.
func historyEntry(row GateVerdictRow) HistoryEntry {
	out := HistoryEntry{
		Blocking: row.Blocking,
		Headline: say(row.Headline, OriginBosun, maxSummary),
	}
	if commitish(row.SHA) {
		out.HeadCommit = row.SHA
	}
	return out
}

// commitish reports whether a string is written the way a commit is: non-empty
// hexadecimal, no longer than the longest hash a git host publishes.
//
// The same argument vetted() makes about a migration, one rung smaller. A
// commit is the field a caller lines up against its own git log, and it is
// published with no origin because a hash has nowhere to hide a sentence --
// which is only true while it is a hash. These rows come off a comment, so
// what was recorded is whatever was written there, and a value that is not a
// commit is dropped rather than published as one.
//
// 64 rather than 40: SHA-256 object names are 64 characters, and a repository
// that has moved to them writes commits this surface has no reason to refuse.
func commitish(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// The sentences, one per state.
//
// Constants with nothing interpolated into them, which is what lets every one
// of them be published as bosun's own. A client branches on State; the model
// it is speaking for reads these, and both have to be told the same thing.
const (
	historyUnswept = "No gate sweep has completed yet, so nothing has been read about what the " +
		"gate has said on any pull request. This is not a claim that it has never blocked this " +
		"one: nothing has looked. Ask again after the next sweep."

	historyRecorded = "The verdicts the gate reached on this pull request's earlier head " +
		"commits, newest first, as the last run that published onto it read them out of its " +
		"own comment -- where a gate with no database keeps its memory, and which any " +
		"repository writer can edit. It is what that comment recorded, not every head this " +
		"pull request has had: neither the verdict standing now nor one the gate never " +
		"published is among them. As many entries as the cap means older ones were dropped."

	historyUnread = "The last gate sweep saw this pull request open and no verdict history has " +
		"been read for it: the gate has published no comment on it that this process has read, " +
		"so there is nowhere to have read one from. This is not a claim that the gate has never " +
		"blocked this pull request."

	historyAbsent = "The last gate sweep did not see this pull request among the open ones. It " +
		"may have been merged or closed, or opened since the sweep ran. Nothing here is a claim " +
		"about what the gate has said on it."

	historySweepFailed = "The last gate sweep could not list open pull requests at all, so it is " +
		"not known whether this one is open or what the gate has said on it. The sweepError " +
		"field says what stopped the sweep."
)
