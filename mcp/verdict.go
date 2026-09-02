package mcp

import (
	"encoding/json"
	"time"
)

// gate_verdict: why one pull request is blocked, as data.
//
// The tool this whole surface was argued for. Answering "why is PR 264 red and
// what do I change" used to mean scraping a pull-request comment and parsing
// `<!-- gitops-gate:... -->` stamps out of it, which made an internal wire
// format into a public contract and got the reader nothing that was typed.
//
// It is also where text bosun did not write first enters a result, and that is
// most of its weight. A verdict carries chart-rendered object names, helm and
// schema error strings, and a pull-request title somebody else chose. All of
// it lands in another model's context, and that model usually holds tools
// bosun refuses for itself -- so a hostile release note does not need to
// jailbreak bosun's model, only to be delivered by it to a better-armed one.
//
// The rule this file establishes, and every later tool inherits: facts travel
// in typed fields a string cannot forge, and free text travels tagged with
// where it came from. The contract a client can rely on is that instructions
// in a result are bosun's own or absent.

// gateVerdictDescription is what a client hands its model as this tool's
// purpose. A constant, and it has to stay one: see pipelineReportDescription.
const gateVerdictDescription = "Why one pull request is blocked, or why it is not: the verdict " +
	"the gate reached against its head commit. Returns the blocker breakdown as counts per kind " +
	"and every finding behind them, with dropped API versions carried as fields -- which " +
	"definition, which versions it stopped serving, which one survives, and the kind of manifest " +
	"that has to move. Findings say whether an edit in the repository could clear them, so a " +
	"caller stops hunting for one that does not exist, and the list of what the gate could not " +
	"render travels beside them, because a clean verdict over a partial render is a narrower " +
	"claim. A pull request with no verdict standing is answered as such and is never reported as " +
	"passing. Answers from the last sweep's snapshot: it reaches no cluster, no git host and no " +
	"model, and it can change nothing."

// gateVerdictParams is the tool's input schema. Why pullRequest is required
// and repository is not is written where both tools' arguments are read:
// Server.pullRequest.
var gateVerdictParams = json.RawMessage(`{"type":"object","properties":{` +
	`"pullRequest":{"type":"integer","minimum":1,` +
	`"description":"The pull request number to report the verdict for."},` +
	`"repository":{"type":"string",` +
	`"description":"The repository, owner/repo. Optional: an install watches exactly one, and ` +
	`omitting this asks about that one."}},` +
	`"required":["pullRequest"],"additionalProperties":false}`)

// GateStatus is what the gate's last sweep saw, in this package's own shapes.
//
// A copy of gateservice's account of itself rather than the thing itself, and
// the copy is the point: this package imports the result types and the
// redactor and nothing else, so no field path from a tool result can reach a
// credential. gateservice reaches a git host and shells helm; web.GateStatus
// is the same copy made for the same reason, and its comment says so too.
//
// The composition root does the adapting, which is where the two vocabularies
// are allowed to know about each other.
type GateStatus struct {
	// SweptAt is when the last gate sweep finished. Zero before the first
	// one, which every tool reports as "nothing has looked yet" rather than
	// as a green pull request.
	SweptAt time.Time
	// Err is what stopped the last sweep from listing pull requests, and ""
	// when it ran. A gate that cannot list has one other symptom, which is a
	// surface reading "nothing open" forever.
	Err string
	// Open is every pull request the last sweep saw, each with whatever
	// verdict stands against its head commit.
	//
	// A sweep that could not list leaves what the last one that could saw, so
	// Err and a non-empty Open together mean a queue older than SweptAt.
	Open []GatePR
	// Held is how many verdicts this process has in memory, and Running how
	// many gate runs are in flight. gate_status publishes both; before the
	// first sweep they are zero and are published as absent rather than as
	// numbers.
	Held    int
	Running int
	// Fleet is what the last live reading of ArgoCD saw the control plane
	// running, or nil when no gate run has made one.
	//
	// It rides here rather than on a provider of its own because it is
	// produced by the sweep and discarded by it, which is what Gate already
	// carries. A fourth provider function would give the composition root a
	// third thing to keep fresh and a third way for one tool's snapshot to be
	// older than another's without anything saying so.
	//
	// nil is a real answer and a common one: the reading is made when a gate
	// RUN renders a pull request, so a sweep that had nothing to render leaves
	// this alone rather than emptying it.
	Fleet *GateFleet
}

// GateFleet is one live reading of what ArgoCD serves, and when it was made.
//
// The whole reading rather than a set of rows, because the timestamp belongs
// to the reading rather than to any row of it: rows observed together are
// stale together, and a per-row copy of one clock is what would let a
// refactor make half a reading look fresher than the other half.
type GateFleet struct {
	// ObservedAt is when the reading was made, which is when a gate run
	// derived what this repository deploys -- not when the sweep finished.
	ObservedAt time.Time
	// Apps is every Application the reading served, unfiltered. Empty is a
	// real answer: an ArgoCD serving nothing.
	Apps []GateFleetApp
}

// GateFleetApp is one Application the live reading served.
type GateFleetApp struct {
	// Name and Namespace identify the Application object itself.
	Name      string
	Namespace string
	// Cluster is the cluster it lands on, resolved against the same cluster
	// inventory the render expands generators over, and "" when the
	// destination named no cluster that inventory knows.
	Cluster string
}

// GatePR is one open pull request as the gate last saw it.
type GatePR struct {
	Number  int
	Title   string
	URL     string
	HeadSHA string
	// State is passing, failing, error (the gate could not run), running (a
	// render is in flight), or unknown (the sweep neither produced a verdict
	// nor read one).
	State string
	// Err is why the gate could not run, set exactly when State is error.
	Err string
	// Labels are what stood on the pull request when the sweep listed it.
	//
	// Carried on the snapshot rather than fetched per request, because a tool
	// call may reach no git host at all -- and they are the attempt cap's only
	// memory, so a reader asking what the agent will do next cannot compute
	// them from anything else.
	Labels []string
	// Verdict is the whole answer as data, or nil when this process did not
	// produce one -- a verdict already standing on the git host is not
	// re-litigated, and a run in flight has nothing yet.
	Verdict *GateVerdict
}

// GateVerdict is one head commit's verdict as the gate computed it.
type GateVerdict struct {
	Blocking bool
	// Headline is the gate's own one line, composed from counts and fixed
	// words. It names nothing the render produced, which is what lets this
	// surface publish it as bosun's own; TestTheVerdictHeadlineIsBosunsOwnWords
	// is what keeps that true.
	Headline string
	Blockers GateBlockers
	Findings []GateFinding
	// NotCovered is what the run could not read and therefore did not judge.
	NotCovered []string
	BaseRev    string
	HeadRev    string
}

// GateBlockers is the counted breakdown, one field per reason a verdict
// blocks.
type GateBlockers struct {
	Targeting     int
	Source        int
	APIVersion    int
	Consumers     int
	Unscanned     int
	Unrenderable  int
	ValuesDropped int
	Schema        int
}

// GateFinding is one reason behind those counts.
type GateFinding struct {
	Kind                 string
	Count                int
	Blocking             bool
	RepositorySideRemedy bool
	Subject              string
	Cluster              string
	Source               string
	From                 string
	To                   string
	Detail               string
	// Reason is what helm or the schema validator said, verbatim.
	Reason string
	Keys   []string
	// ConsumerFiles are repository manifests still declaring a dropped
	// version, and ConsumersScanned records that the repository was looked at.
	ConsumerFiles    []string
	ConsumersScanned bool
	// Dropped is the migration this finding demands, present only when every
	// field of it passed the repair contract's own grammars.
	Dropped *GateDropped
}

// GateDropped is the dropped-served-version detail, already vetted.
type GateDropped struct {
	Definition   string
	Group        string
	ConsumerKind string
	Versions     []string
	Surviving    string
}

// Verdict is what gate_verdict returns.
//
// Absence carries meaning in three places here, and they are three different
// answers a client must not conflate. No sweep has completed: everything below
// Swept is missing. A sweep completed and holds no verdict for this pull
// request: State says which flavour of nothing, and the breakdown is missing.
// A verdict stands: Blockers and Findings are present, and Findings being
// EMPTY then means the gate looked and found nothing -- which is the one thing
// a green pull request is.
type Verdict struct {
	// Repository this verdict is about, "owner/repo".
	Repository string `json:"repository"`
	// PullRequest is the number asked about, echoed so a client batching
	// calls can tell the answers apart.
	PullRequest int `json:"pullRequest"`

	// Swept is whether any gate sweep has completed since this process
	// started, and SweptAt and AgeSeconds say when and how long ago.
	Swept      bool       `json:"swept"`
	SweptAt    *time.Time `json:"sweptAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`

	// State is the verdict standing against the head commit:
	//
	//   passing  the gate ran and nothing blocks
	//   failing  the gate ran and something does
	//   error    the gate could not run, which is not a failing verdict
	//   running  a render is in flight and no verdict stands yet
	//   unknown  the sweep saw this pull request and holds no verdict for it
	//   absent   a sweep completed and did not see this pull request open
	//   unswept  no gate sweep has completed, so nothing has looked at all
	//
	// The first five are the words the status page already uses, so an
	// install has one vocabulary rather than two. The last two are this
	// surface's own, because a page only ever renders pull requests it has
	// and a tool can be asked about one it does not.
	//
	// Each word means exactly one thing, and `absent` and `unswept` are
	// separate for the reason the whole surface exists: "the gate looked and
	// this pull request was not open" and "the gate has not looked" are
	// different answers, and a client should not have to read a second field
	// to tell them apart.
	State string `json:"state"`

	// Status is the same answer in words, and it is bosun's own sentence in
	// every case: before the first sweep, for each flavour of nothing, and
	// for a verdict, where it is the gate's own headline.
	Status Text `json:"status"`

	// SweepError is what stopped the last sweep from listing pull requests.
	// Present only when one did: its whole purpose is that "nothing open" and
	// "could not look" must not read the same.
	SweepError *Text `json:"sweepError,omitempty"`

	// HeadCommit is the commit this answer is about, and the field a client
	// caches against. Absent when the sweep does not have this pull request.
	HeadCommit string `json:"headCommit,omitempty"`
	// BaseCommit is the revision the verdict was the difference from -- the
	// merge base, not the base branch's tip, which is a different commit the
	// moment anything else merges.
	//
	// Abbreviated, because that is the form the run recorded. A verdict that
	// named only its head could not be told apart from one whose base was the
	// wrong commit, and the symptom of the wrong base -- resources this pull
	// request never touched, listed as removed -- looks exactly like a pull
	// request tearing out infrastructure.
	BaseCommit string `json:"baseCommit,omitempty"`
	// URL is where a person can read the pull request. Composed by the git
	// host from its own address and the number above, so it carries no
	// origin: there is no free text in it.
	URL string `json:"url,omitempty"`
	// Title is the pull request's own title, written by whoever opened it.
	Title *Text `json:"title,omitempty"`

	// Blocking is whether this verdict stops the merge. Absent with no
	// verdict, where "false" would read as "nothing blocks".
	Blocking *bool `json:"blocking,omitempty"`
	// Blockers is the counted breakdown, absent with no verdict and present
	// and possibly all zeroes with one.
	Blockers *Blockers `json:"blockers,omitempty"`
	// Findings is every reason behind those counts, in the order the report
	// reads. Absent with no verdict; empty means the gate looked and found
	// nothing.
	Findings *[]VerdictFinding `json:"findings,omitempty"`
	// NotCovered is what the gate could not read and therefore did not judge.
	// Absent with no verdict; empty means it read everything it meant to.
	//
	// Its own list rather than a class of finding, because it is a different
	// claim. A finding is "we looked and this is wrong"; these are "we did
	// not look here", and a clean verdict beside three of them is a much
	// narrower statement than a clean verdict beside none.
	//
	// The line between the two is whose doing it is, which is why an
	// Application whose chart will not render appears on one side or the
	// other rather than both. Failing to render at the version this change
	// moves to is a finding of kind `unrenderable`: the change did that, and
	// merging it leaves an Application that cannot sync. Failing at the
	// revision the change starts from is a coverage note: the repository was
	// already in that state, there is no diff to compute either way, and
	// blocking a pull request for the condition it inherited helps nobody.
	NotCovered *[]Text `json:"notCovered,omitempty"`

	// Error is the gate failing to run, present exactly when State is error.
	// A different thing from a failing verdict, and the two want opposite
	// reactions: one says the change is bad, the other says nothing judged it.
	Error *Text `json:"error,omitempty"`
}

// Blockers is the counted breakdown of why a verdict blocks.
//
// This package's own copy of the gate's, for the reason every result type here
// is: a result may only be built from mcp's own shapes, so that whether some
// other package's type can reach a credential is not a question anybody has to
// re-answer on every upgrade.
//
// Every count is manifests, objects or settings rather than findings: a
// definition four manifests still declare counts four, because four files have
// to change.
type Blockers struct {
	// Targeting is Applications now generated for a different set of clusters.
	Targeting int `json:"targeting"`
	// Source is Applications whose source itself moved, rather than its
	// version.
	Source int `json:"source"`
	// APIVersion is rendered objects whose own apiVersion moved, and that are
	// not part of a migration this same verdict demands.
	APIVersion int `json:"apiVersion"`
	// Consumers is manifests in the repository still declaring a version a
	// definition stopped serving.
	Consumers int `json:"consumers"`
	// Unscanned is definitions whose consumers could not be counted. "We
	// could not look" blocks, and is not "we looked and found none".
	Unscanned int `json:"unscanned"`
	// Unrenderable is Applications whose chart will not render at the version
	// this change moves them to.
	Unrenderable int `json:"unrenderable"`
	// ValuesDropped is settings the repository makes that the new chart
	// version no longer declares. Helm ignores an unknown value rather than
	// failing on it, so these stop applying while the render stays green.
	ValuesDropped int `json:"valuesDropped"`
	// Schema is rendered manifests the target cluster's schemas reject.
	Schema int `json:"schema"`
}

// VerdictFinding is one reason the gate has an opinion, in the form a client
// branches on.
//
// Everything here that is not a number, a boolean or the Dropped block is text
// from a render, and carries an origin saying so. A client that wants a
// sentence it may treat as instructions reads Status on the result, which is
// bosun's alone and is tested to be.
type VerdictFinding struct {
	// Kind is the class, and the field to branch on: targeting, source,
	// apiVersion, droppedVersion, valuesDropped, unrenderable, schema.
	Kind string `json:"kind"`
	// Count is this finding's contribution to the breakdown, and the number
	// of things wrong rather than of findings.
	Count int `json:"count"`
	// Blocking is Count > 0, published so a client does not have to know that.
	Blocking bool `json:"blocking"`
	// RepositorySideRemedy is whether an edit somewhere in the gated
	// repository could clear this. False for an apiVersion the chart moved
	// and for a manifest a schema rejects: both need an author rather than a
	// version swap, and a caller told otherwise hunts for an edit that does
	// not exist.
	RepositorySideRemedy bool `json:"repositorySideRemedy"`

	// Subject is what the finding is about: an Application name, or
	// `Kind/name in namespace` for a rendered object.
	//
	// Tagged, unlike pipeline_report's subject, and the difference is not an
	// inconsistency. A Kargo Stage name reached an apiserver, so it is an
	// RFC1123 subdomain with no room in it to hide a sentence. This name
	// never reached one: `helm template` does not apply, so what is in it is
	// whatever the template wrote.
	Subject Text `json:"subject"`
	// Cluster is where, when the finding belongs to one. The name ArgoCD
	// knows the cluster by.
	Cluster *Text `json:"cluster,omitempty"`
	// Source is which rendered stream produced it. Schema findings only.
	Source *Text `json:"source,omitempty"`
	// From and To are the two sides of the move, when the finding is one.
	From *Text `json:"from,omitempty"`
	To   *Text `json:"to,omitempty"`

	// Summary is bosun's sentence about this finding, and some of them name
	// what they are about.
	//
	// Tagged as quoting the render throughout rather than per kind. A per-kind
	// tag would be accurate today and a lie the day one of these sentences
	// gains a name, and the field a client is told it may trust is Status.
	Summary Text `json:"summary"`

	// Reason is what a tool said when it refused, verbatim: helm on a chart
	// that will not render, the validator on a manifest it rejects. Its own
	// field, and its own origin, because it is the one string here bosun
	// composed no part of.
	Reason *Text `json:"reason,omitempty"`

	// Keys are the settings the new chart version stopped declaring.
	Keys []Text `json:"keys,omitempty"`

	// ConsumerFiles are the repository manifests still declaring a dropped
	// version, and ConsumersScanned records that the repository was scanned
	// at all. The pair is the difference between "nothing depends on it" and
	// "we could not look", and it is also which count above this finding
	// lands in.
	ConsumerFiles    []Text `json:"consumerFiles,omitempty"`
	ConsumersScanned bool   `json:"consumersScanned,omitempty"`

	// Dropped is which manifests move to which apiVersion.
	Dropped *Dropped `json:"dropped,omitempty"`
}

// Dropped is a dropped-served-version finding as fields rather than as prose.
//
// Untagged, and it is the only free text on this surface that is. These are
// not tagged because they are not free text: every one of them was matched
// against the repair contract's own grammars before this block was published
// at all, and nothing that passes those can hold a space, a backtick or a
// newline. A finding whose fields do not hold their shape is published without
// this block rather than with an unvetted one, which is why absence here means
// "bosun would not vouch for the detail" and never "there is no detail".
//
// It is the strongest claim the surface makes, because it is the one a program
// acts on with no person in between: these fields say which manifests move to
// which apiVersion.
type Dropped struct {
	// Definition is the CustomResourceDefinition's own name,
	// <plural>.<group>.
	Definition string `json:"definition"`
	// Group is the API group consuming manifests declare.
	Group string `json:"group"`
	// ConsumerKind is what a consuming manifest writes in its `kind:` field.
	ConsumerKind string `json:"consumerKind"`
	// Versions are the served versions that are gone.
	Versions []string `json:"versions"`
	// Surviving is the served version consumers must move to.
	Surviving string `json:"surviving"`
}

// gateVerdict answers from the last gate sweep, and from nothing else.
func (s *Server) gateVerdict(raw json.RawMessage) (any, error) {
	number, err := s.pullRequest(raw)
	if err != nil {
		return nil, err
	}

	out := Verdict{Repository: s.Repository, PullRequest: number}

	g := s.gate()
	if g.SweptAt.IsZero() {
		out.State, out.Status = StateUnswept, say(noGateSweepYet, OriginBosun, maxSummary)
		return out, nil
	}

	out.SweptAt, out.AgeSeconds, out.Swept = s.stamp(g.SweptAt)
	if g.Err != "" {
		// Bosun's sentence with the git host's error in it, which is exactly
		// the string a misconfigured host is most likely to echo a credential
		// back inside.
		out.SweepError = ptr(say(g.Err, OriginCluster, maxNote))
	}

	var pr *GatePR
	for i := range g.Open {
		if g.Open[i].Number == number {
			pr = &g.Open[i]
			break
		}
	}
	if pr == nil {
		// Two different nothings. A sweep that ran and did not see this pull
		// request open is evidence; a sweep that could not list them is the
		// absence of evidence, and reporting the second as the first is how a
		// caller concludes a pull request was merged when the gate merely
		// lost its token.
		out.State, out.Status = StateAbsent, say(verdictAbsent, OriginBosun, maxSummary)
		if g.Err != "" {
			out.State, out.Status = StateUnknown, say(verdictSweepFailed, OriginBosun, maxSummary)
		}
		return out, nil
	}

	out.State = pr.State
	out.HeadCommit = pr.HeadSHA
	out.URL = pr.URL
	if pr.Title != "" {
		out.Title = ptr(say(pr.Title, OriginAuthor, maxTitle))
	}
	if pr.State == StateError && pr.Err != "" {
		// Guarded on the state rather than on the string alone, because the
		// field's own documentation promises a client that the two travel
		// together. gateservice sets them together today; a promise a caller
		// can branch on should not rest on that staying true upstream.
		out.Error = ptr(say(pr.Err, OriginRender, maxNote))
	}

	v := pr.Verdict
	if v == nil {
		// A state with no breakdown behind it, and there are three ways to
		// get here: a render in flight, a verdict already standing on the git
		// host that this process deliberately did not re-litigate, and a run
		// that broke. Each gets its own sentence, because "no findings" is
		// the one answer none of them may be mistaken for.
		switch pr.State {
		case StateRunning:
			out.Status = say(verdictRunning, OriginBosun, maxSummary)
		case StateError:
			out.Status = say(verdictErrored, OriginBosun, maxSummary)
		case StatePassing, StateFailing:
			out.Status = say(verdictNotHeld, OriginBosun, maxSummary)
		default:
			out.Status = say(verdictUnknown, OriginBosun, maxSummary)
		}
		return out, nil
	}

	// Bosun's own, and the tag is a claim worth checking rather than a
	// courtesy: gate.DiffResult.Verdict composes its headline from counts and
	// fixed words and quotes nothing a render produced.
	out.Status = say(v.Headline, OriginBosun, maxSummary)
	out.Blocking = &v.Blocking
	out.BaseCommit = v.BaseRev
	if out.HeadCommit == "" {
		// The full SHA off the pull request is preferred, and it is the same
		// commit: the gate pins HEAD to it before rendering and abandons the
		// run where it cannot. The gate's own record is abbreviated, so
		// taking it in preference would hand a caching client eight
		// characters where it had forty.
		out.HeadCommit = v.HeadRev
	}

	// A conversion rather than eight assignments. GateBlockers and Blockers
	// are field-identical by construction -- one is what the composition root
	// hands over, the other is what a client reads -- and Go refuses the
	// conversion the moment they stop being, which is a better guard than a
	// list of eight names that can go stale one at a time.
	b := Blockers(v.Blockers)
	out.Blockers = &b

	findings := make([]VerdictFinding, 0, len(v.Findings))
	for _, f := range v.Findings {
		findings = append(findings, verdictFinding(f))
	}
	out.Findings = &findings

	notCovered := make([]Text, 0, len(v.NotCovered))
	for _, n := range v.NotCovered {
		notCovered = append(notCovered, say(n, OriginRender, maxNote))
	}
	out.NotCovered = &notCovered
	return out, nil
}

// The states a verdict can be in, as constants rather than as literals in a
// switch. The first five are the status page's own words, so an install has
// one vocabulary; the last two are this surface's, because a tool can be asked
// about a pull request a page would never have rendered.
const (
	StatePassing = "passing"
	StateFailing = "failing"
	StateError   = "error"
	StateRunning = "running"
	StateUnknown = "unknown"
	StateAbsent  = "absent"
	StateUnswept = "unswept"
)

// The sentences, one per flavour of nothing.
//
// Constants with nothing interpolated into them, which is what lets every one
// of them be published as bosun's own. A client branches on State; the model
// it is speaking for reads these, and both have to be told the same thing.
const (
	noGateSweepYet = "No gate sweep has completed yet, so no verdict is claimed about any pull " +
		"request. This is not a passing verdict: the gate has not looked. Ask again after the " +
		"next sweep."

	verdictAbsent = "The last gate sweep did not see this pull request among the open ones. It " +
		"may have been merged or closed, or opened since the sweep ran. Nothing here is a " +
		"verdict on it."

	verdictSweepFailed = "The last gate sweep could not list open pull requests at all, so it is " +
		"not known whether this one is open. Nothing here is a verdict on it, and the sweepError " +
		"field says what stopped the sweep."

	verdictRunning = "A gate run for this head commit is in flight. No verdict stands against it " +
		"yet; ask again once the run finishes."

	verdictErrored = "The gate could not run against this head commit, which is not the same as a " +
		"failing verdict: nothing was judged, and the change has not been found wanting. The " +
		"error field says what stopped it."

	verdictNotHeld = "A verdict already stood against this head commit when the sweep looked, so " +
		"the gate was not re-run and this process holds no breakdown of it. The state is what " +
		"the git host reports; there are no findings here because none were computed, which is " +
		"not the same as none being found."

	verdictUnknown = "The last gate sweep saw this pull request open and holds no verdict for its " +
		"head commit: none was produced here, and none could be read from the git host. This is " +
		"not a passing verdict."
)

// verdictFinding maps one gate finding onto the wire.
//
// Deliberately dull, like report.go's. Everything interesting -- what is
// wrong, whether it blocks, whether an edit could clear it, which manifests
// move where -- was decided by gate, which is the package that can be tested
// without a listener. This adds the origin tags and the caps, and it does not
// add a single fact.
func verdictFinding(f GateFinding) VerdictFinding {
	out := VerdictFinding{
		Kind:                 f.Kind,
		Count:                f.Count,
		Blocking:             f.Blocking,
		RepositorySideRemedy: f.RepositorySideRemedy,
		Subject:              say(f.Subject, OriginChart, maxName),
		Summary:              say(f.Detail, OriginChart, maxSummary),
		ConsumersScanned:     f.ConsumersScanned,
	}
	if f.Cluster != "" {
		out.Cluster = ptr(say(f.Cluster, OriginCluster, maxName))
	}
	if f.Source != "" {
		out.Source = ptr(say(f.Source, OriginChart, maxName))
	}
	if f.From != "" {
		out.From = ptr(say(f.From, OriginChart, maxName))
	}
	if f.To != "" {
		out.To = ptr(say(f.To, OriginChart, maxName))
	}
	if f.Reason != "" {
		// Whose words these are depends on which tool refused, and the two
		// are not interchangeable: one is a template that would not execute,
		// the other a verdict on a document that rendered perfectly well.
		origin := OriginHelm
		if f.Kind == kindSchema {
			origin = OriginValidator
		}
		out.Reason = ptr(say(f.Reason, origin, maxDetail))
	}
	for _, k := range f.Keys {
		out.Keys = append(out.Keys, say(k, OriginChart, maxName))
	}
	for _, p := range f.ConsumerFiles {
		out.ConsumerFiles = append(out.ConsumerFiles, say(p, OriginRepository, maxName))
	}
	out.Dropped = vetted(f.Dropped)
	return out
}

// vetted is the migration a finding demands, published only if this package
// can see for itself that none of it can carry a sentence.
//
// The gate has already matched every one of these against the repair
// contract's own grammars, and that check is the real one: it is the same
// patterns the repair reads with, so what the writer emits is exactly what the
// reader accepts. This is not a second copy of it -- copying a contract is how
// two halves start disagreeing -- it is this surface's own floor, and it is
// here because Dropped is the only free text on the wire that carries no
// origin.
//
// Everything else a client is handed says where it came from, so a client can
// fence it. These fields say "this is a fact, act on it", so the claim has to
// be true whatever happens upstream of this function: a wrongly wired adapter,
// a future caller of the tool surface that skipped the gate, a grammar that
// loosens. What is asserted is narrow enough to state in one line: an
// identifier's alphabet, and a length.
//
// It is deliberately not a superset of the contract. The alphabet is wider
// than any of the contract's own patterns, so nothing they accept is refused
// on those grounds; the length is not, because they bound nothing and an
// object name is bounded by the API machinery at 253 characters whatever a
// regular expression says. A definition name longer than a legal Kubernetes
// object name loses its fields here and keeps its prose, which is the right
// way round for a value a program acts on.
//
// A finding that fails keeps its prose and loses this block, which is what
// absence here means: bosun would not vouch for the detail.
func vetted(d *GateDropped) *Dropped {
	if d == nil {
		return nil
	}
	for _, s := range append([]string{d.Definition, d.Group, d.ConsumerKind, d.Surviving}, d.Versions...) {
		if !identifier(s) {
			return nil
		}
	}
	if len(d.Versions) == 0 {
		return nil
	}
	return &Dropped{
		Definition:   d.Definition,
		Group:        d.Group,
		ConsumerKind: d.ConsumerKind,
		Versions:     append([]string(nil), d.Versions...),
		Surviving:    d.Surviving,
	}
}

// identifier reports whether a string is a name and nothing else: non-empty,
// no longer than the longest legal Kubernetes object name, and drawn from an
// alphabet with no space, no quote, no angle bracket and no newline in it.
//
// A byte loop rather than a regular expression, because this package has no
// third-party dependency and the standard library's regexp is a larger thing
// to reach for than the four conditions it would express.
func identifier(s string) bool {
	if s == "" || len(s) > maxIdentifier {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// maxIdentifier is the RFC1123 subdomain limit, and therefore the longest
// legal Kubernetes object name.
const maxIdentifier = 253

// kindSchema is the one finding kind this file has to recognise by name, to
// decide whose refusal a Reason is quoting. The rest of the vocabulary is
// gate's and travels through untouched, which is why there is one constant
// here and not seven.
const kindSchema = "schema"

// gate is the last gate sweep, or the zero value when there is nothing to
// read.
//
// The nil check is here rather than at every call site for the same reason
// Server.now has one: a surface that answers "nothing has looked yet" is the
// honest response to a gate that is switched off, and it is exactly what a
// client should see.
func (s *Server) gate() GateStatus {
	if s.Gate == nil {
		return GateStatus{}
	}
	return s.Gate()
}

// ptr is the address of a value, for the optional fields whose absence means
// something. Written out because Go has no way to take the address of a
// function's return.
func ptr[T any](v T) *T { return &v }
