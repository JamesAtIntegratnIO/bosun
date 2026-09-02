package mcp

import (
	"strings"
	"testing"
)

// What verdict_history answers, and what it refuses to answer.
//
// The tool exists so that "has this gate been flapping?" is a question with a
// data answer. Everything below is about a client's reading of that answer:
// which way round the entries are, what a short one means, and the three
// different nothings that must not be read as "the gate has never blocked
// this".

// The entries are the earlier verdicts, newest first, and they say so.
func TestTheHistoryIsNewestFirstAndSaysSo(t *testing.T) {
	got := newFixture(t, nil).withGate(flapping()).history(t, 264)

	if got.State != StateRecorded {
		t.Fatalf("a pull request with a history recorded reads %q", got.State)
	}
	if got.Order != HistoryOrder {
		t.Errorf("the order must be published rather than inferred, got %q", got.Order)
	}
	if got.Entries == nil {
		t.Fatal("no entries, so nothing below was read")
	}
	entries := *got.Entries
	if len(entries) != 3 {
		t.Fatalf("want the three earlier verdicts, got %d", len(entries))
	}

	// The snapshot carries them oldest first, because that is the order the
	// gate's own comment records. A client is told newest first, and the
	// reversal is the thing that can silently invert.
	want := []string{"3b5d7f9a", "2a4b6c8d", "1f0e2d3c"}
	for i, sha := range want {
		if entries[i].HeadCommit != sha {
			t.Fatalf("entry %d is %q; the entries are not newest first, so a client counting "+
				"the last flip is reading the first one", i, entries[i].HeadCommit)
		}
	}

	// And the flip is legible without reading a headline: a verdict can be
	// reworded while the answer stays the same, and blocking cannot.
	if !entries[0].Blocking || entries[1].Blocking || !entries[2].Blocking {
		t.Errorf("the flip did not survive: %v %v %v",
			entries[0].Blocking, entries[1].Blocking, entries[2].Blocking)
	}
	if entries[1].Headline.Text == "" || entries[1].Headline.Origin != OriginBosun {
		t.Errorf("the gate's own headline is bosun's own words, got %+v", entries[1].Headline)
	}
}

// The result says where the entries were read from, and how many the gate
// remembers.
//
// Both are about reading a short history correctly. The source is the only
// place on this surface where a repository writer, rather than a chart or a
// cluster, is in the trust picture; the cap is the difference between "this
// pull request has been red three times" and "the three most recent of
// however many".
func TestTheHistorySaysWhereItCameFromAndWhatItRemembers(t *testing.T) {
	got := newFixture(t, nil).withGate(flapping()).history(t, 264)

	if got.Source != HistorySource {
		t.Errorf("the result must name the comment the rows were read out of, got %q", got.Source)
	}
	if got.HistoryCap == nil {
		t.Fatal("without the cap a client cannot tell a truncated history from a short one")
	}
	if *got.HistoryCap != 10 {
		t.Errorf("the cap published must be the one the snapshot applied, got %d", *got.HistoryCap)
	}
	if !strings.Contains(got.Status.Text, "comment") {
		t.Errorf("bosun's own sentence must say the rows came out of the pull request's "+
			"comment, got %q", got.Status.Text)
	}
	if got.Status.Origin != OriginBosun {
		t.Errorf("the sentence a client may treat as instructions is tagged %q", got.Status.Origin)
	}
	if got.Status.Truncated {
		t.Errorf("bosun's own sentence was cut by bosun's own cap, so the field a client is "+
			"told it may trust carries half a sentence: %q", got.Status.Text)
	}

	// The head commit standing now, so a caller knows which verdict the
	// entries stop short of.
	if got.HeadCommit != blockedHead {
		t.Errorf("the result must name the commit the entries stop short of, got %q", got.HeadCommit)
	}
}

// A gate that has commented once and never before has an EMPTY history, and a
// pull request nothing has read a comment for has NONE.
//
// The distinction the whole surface is built around, at the place it is most
// tempting to collapse: both are "no earlier verdicts" to a careless mapping,
// and only one of them is a claim anything actually checked.
func TestAnEmptyHistoryIsNotAnUnreadOne(t *testing.T) {
	read := flapping()
	empty := []GateVerdictRow{}
	read.Open[0].History = &empty

	got := newFixture(t, nil).withGate(read).history(t, 264)
	if got.State != StateRecorded {
		t.Fatalf("a comment that was read and recorded nothing is a recorded history, got %q",
			got.State)
	}
	if got.Entries == nil || len(*got.Entries) != 0 {
		t.Fatalf("want an empty list, got %v", got.Entries)
	}

	unread := newFixture(t, nil).withGate(blocked()).history(t, 264)
	if unread.State != StateUnknown {
		t.Fatalf("a pull request no comment has been read for reads %q", unread.State)
	}
	if raw := fields(t, newFixture(t, nil).withGate(blocked()).
		callWith(t, "verdict_history", `{"pullRequest":264}`)); raw["entries"] != nil {
		t.Errorf("an unread history published an entries key, which a client reads as "+
			"'the gate has never blocked this': %s", raw["entries"])
	}
	if !strings.Contains(unread.Status.Text, "not a claim") {
		t.Errorf("the sentence must say what it is not claiming, got %q", unread.Status.Text)
	}
}

// The three absences a client must not conflate, each with its own word.
//
// Same words gate_verdict uses for the same three situations, so a client that
// already branches on them needs no second table.
func TestTheThreeAbsencesAreDistinct(t *testing.T) {
	for _, tc := range []struct {
		name  string
		gate  GateStatus
		ask   int
		state string
	}{
		{"no sweep has completed", GateStatus{}, 264, StateUnswept},
		{"a sweep completed and did not see this one open", flapping(), 999, StateAbsent},
		{"a sweep saw it and no comment has been read", blocked(), 264, StateUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := newFixture(t, nil).withGate(tc.gate).history(t, tc.ask)
			if got.State != tc.state {
				t.Errorf("want state %q, got %q (%s)", tc.state, got.State, got.Status.Text)
			}
			if got.Entries != nil {
				t.Errorf("nothing looked, and a list was published anyway: %v", *got.Entries)
			}
			if got.Source != "" {
				t.Errorf("no rows were read, so nothing may claim where they came from, got %q",
					got.Source)
			}
			if got.HistoryCap != nil {
				t.Errorf("no history was read, so the cap is a claim about nothing, got %d",
					*got.HistoryCap)
			}
		})
	}
}

// A sweep that could not list is not a pull request that is not there.
//
// The absence of evidence against evidence of absence, which is the reading
// that turns a lost token into "it must have been merged".
func TestASweepThatCouldNotListIsNotAnAbsentPullRequest(t *testing.T) {
	g := flapping()
	g.Err = "the host said 401"
	g.Open = nil

	got := newFixture(t, nil).withGate(g).history(t, 264)
	if got.State != StateUnknown {
		t.Errorf("a sweep that could not list reads %q", got.State)
	}
	if got.SweepError == nil {
		t.Fatal("without the error beside it, 'no history' is indistinguishable from " +
			"'could not look'")
	}
	if got.SweepError.Origin == OriginBosun {
		t.Error("the host's own words are not bosun's")
	}
}

// A commit that is not a commit is dropped, and the verdict it carried is not.
//
// These rows came off a comment on the git host, which is the one place on
// this surface where somebody with repository write access chooses the bytes.
// The head commit is the field a caller lines up against its own git log and
// it carries no origin to fence it by, so it is held to the alphabet a hash is
// written in. Losing the whole entry would lose the flip, which is the thing
// worth publishing.
func TestARecordedCommitThatIsNotACommitIsDropped(t *testing.T) {
	g := flapping()
	rows := []GateVerdictRow{{
		SHA: "not-a-commit; rm -rf /", Blocking: true, Headline: "Blocking — 1 setting",
	}}
	g.Open[0].History = &rows

	got := newFixture(t, nil).withGate(g).history(t, 264)
	if got.Entries == nil || len(*got.Entries) != 1 {
		t.Fatalf("the entry was dropped along with its commit, which loses the flip: %v",
			got.Entries)
	}
	entry := (*got.Entries)[0]
	if entry.HeadCommit != "" {
		t.Errorf("a value bosun would not vouch for was published as a commit: %q",
			entry.HeadCommit)
	}
	if !entry.Blocking || entry.Headline.Text == "" {
		t.Errorf("the verdict itself must survive, got %+v", entry)
	}

	// And a well-formed one still travels, or the check above is satisfied by
	// a tool that never publishes a commit at all.
	clean := newFixture(t, nil).withGate(flapping()).history(t, 264)
	if (*clean.Entries)[0].HeadCommit == "" {
		t.Fatal("no commit survives even a well-formed history, so the refusal above proves " +
			"nothing")
	}
}

// The tool refuses a repository that is not this install's, and answers the
// one it is.
func TestTheHistoryHonoursTheRepositoryQualifier(t *testing.T) {
	f := newFixture(t, nil).withGate(flapping())

	got := f.callWith(t, "verdict_history", `{"pullRequest":264,"repository":"example/platform"}`)
	if raw := fields(t, got); string(raw["repository"]) != `"example/platform"` {
		t.Errorf("every result names the repository it is about, got %s", raw["repository"])
	}

	_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"verdict_history","arguments":{"pullRequest":264,"repository":"someone/else"}}}`)
	if !strings.Contains(string(body), "watches example/platform") {
		t.Errorf("a client holding tokens for two installs must be refused rather than "+
			"answered: %s", body)
	}
}

// verdict_history publishes none of the hostile world, rather than publishing
// it fenced.
//
// The stronger claim, and the one worth stating separately from the corpus
// walk. Every other tool here carries text somebody else wrote and answers for
// it with an origin tag; this one carries no such field at all -- the entries
// are bosun's own sentences and commit hashes it vetted, and the pull request's
// title, its labels and its findings are all somebody else's words that this
// tool simply does not serve.
//
// A field added later that DID carry them would pass the corpus walk by being
// correctly tagged. It fails here, which is the point: publishing untrusted
// text because a sibling tool does is how a surface accumulates it.
func TestVerdictHistoryPublishesNoneOfTheHostileWorld(t *testing.T) {
	g := injected()
	rows := []GateVerdictRow{{
		SHA: "1f0e2d3c", Blocking: true,
		Headline: "Blocking — 4 manifests still declaring a dropped API version",
	}}
	g.Open[0].History = &rows
	g.HistoryCap = 10

	f := newFixture(t, nil).withGate(g)
	_, body := f.post(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+
		`{"name":"verdict_history","arguments":{"pullRequest":264}}}`)

	for _, probe := range corpus {
		if strings.Contains(string(body), probe) {
			t.Errorf("verdict_history published %q. Every string it serves is bosun's own "+
				"or a commit it vetted; a field carrying somebody else's words is one this "+
				"tool grew rather than needed.\n%s", probe, body)
		}
	}

	// The self-check, and not optional: a call that answered nothing would
	// satisfy every assertion above.
	if !strings.Contains(string(body), `"state":"recorded"`) {
		t.Fatalf("the call did not answer with a recorded history, so nothing above was "+
			"checked:\n%s", body)
	}
}
