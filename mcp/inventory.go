package mcp

import (
	"encoding/json"
	"time"
)

// inventory: the fleet as the live reading saw it.
//
// The gate reads what ArgoCD says is deployed on every run it makes, uses it
// to decide what to render, and throws it away when the run ends. An agent
// asking which cluster an Application lands on had no way to ask bosun, and
// went to the cluster with a credential of its own -- for an answer this
// process computed, repeatedly, all day.
//
// So this is that reading, retained. It is names and clusters and nothing
// else. No manifests, no values files, no values leaves, no rendered objects:
// inventory is the tool most able to become a manifest proxy by accretion, and
// this is where the line is drawn rather than where it is argued about later.
//
// # Two clocks, and neither is the sweep's
//
// The rows are as old as the last gate RUN, not the last sweep, and the two
// come apart on the install that most wants this tool: one with no open pull
// request has nothing to render, so it makes no reading, so the fleet stays as
// old as the last pull request. That staleness is real and is published rather
// than solved -- every row carries when it was observed, and no tool call is
// allowed to go and refresh it. If it turns out to be what operators complain
// about, the honest fix is a scheduled fleet read, which is a change to what
// the sweep does rather than to what a tool call may do.
//
// # Two readings, and the rule for joining them
//
// What an Application renders from -- its chart, the repository that chart
// comes from, the version pinned to it, and the ApplicationSet it was
// generated from -- is not in the live reading at all. It is in the gate's
// render expansion, which is a different observation of a different thing: the
// repository at the revision the last run started from, rather than what
// ArgoCD is serving. Neither source answers the other's question, so both are
// kept and every row says which half came from where.
//
// The merge rule, which is the whole of it:
//
//   - The live reading is the spine. It decides which rows exist.
//   - The expansion enriches, joined on an Application's identity.
//   - An Application the reading has and the expansion does not know gets NO
//     chart detail -- never somebody else's.
//   - An Application the expansion knows and the reading does not have gets no
//     row. The expansion describes an older revision, and publishing a fleet
//     member that is not there is the worse of the two errors.
//
// Staleness is published rather than solved here too. A row's identity and its
// chart detail carry their own stamps, so "most recent and relevant" is
// something a client reads instead of a merge rule it has to trust, and no
// request path may go and refresh either half.
//
// # What is absent here, and why it is not zero
//
// A row with no chart detail is absent rather than empty, under a flag that
// says whether an expansion has been read at all, because "these charts are
// unpinned", "nothing has read what they render from" and "what read it did
// not know this Application" are three different answers and a missing key is
// all three.

// inventoryDescription is what a client hands its model as this tool's
// purpose. A constant, and it has to stay one: see pipelineReportDescription.
const inventoryDescription = "The fleet as bosun's last live reading of ArgoCD saw it: every " +
	"Application that reading served, with the cluster each one lands on, and what each one " +
	"renders from -- chart, chart repository, pinned version and originating ApplicationSet. " +
	"Use it to answer \"where does this run\" and \"what version is it on\" without a cluster " +
	"credential of your own. Names and versions only -- no manifest, values file or rendered " +
	"object is served here, by any argument. The two halves come from two observations: the " +
	"reading decides which rows exist, the gate's last render says what they render from, and " +
	"each half of a row carries its own timestamp. A row with no chart detail is one that " +
	"render did not know of, and never another Application's chart. Both are as old as the " +
	"last gate run rather than the last sweep: an install with no open pull request renders " +
	"nothing and therefore reads nothing. Answers from that snapshot: it reaches no cluster, " +
	"no git host and no model, and it can change nothing."

// Fleet is what inventory returns.
//
// Applications is a POINTER to a slice for the reason Queue.Open is: absent
// and empty are different answers, and reading the second as the first is the
// mistake this project exists to catch. Absent means no live reading has been
// made; empty means one was made and ArgoCD served no Application at all.
type Fleet struct {
	// Repository this install watches, "owner/repo".
	//
	// Stamped like every other result, and worth a word here because the rows
	// are NOT filtered by it: they are every Application this install's ArgoCD
	// credentials can see, which is routinely more than one repository's
	// worth. An install's horizon is set by its intake rather than by its
	// readout -- ADR 0014 -- so this names which install answered, not which
	// repository the rows belong to.
	Repository string `json:"repository"`

	// Swept is whether any gate sweep has completed since this process
	// started, and SweptAt and AgeSeconds say when and how long ago.
	//
	// The sweep's clock, and deliberately not the rows'. A sweep can complete
	// without making a live reading at all, so these say how current the
	// process is and the rows below say how current the fleet is.
	Swept      bool       `json:"swept"`
	SweptAt    *time.Time `json:"sweptAt,omitempty"`
	AgeSeconds *int64     `json:"ageSeconds,omitempty"`

	// Status is the fleet in words, and it is bosun's own sentence in every
	// case: before the first sweep, before the first reading, and for a
	// reading that served rows.
	Status Text `json:"status"`

	// Applications is every Application the last live reading served. Absent
	// when no reading has been made, empty when one was and served none.
	Applications *[]FleetApp `json:"applications,omitempty"`

	// ChartDetail says whether the rows carry what they render from, and is
	// present exactly when the rows are: a claim about rows there are none of
	// is not an absence a client can do anything with.
	ChartDetail *ChartDetail `json:"chartDetail,omitempty"`
}

// FleetApp is one Application, and where it lands.
//
// Names and a cluster. Nothing here is content, and nothing here is a handle
// that could be exchanged for content: this surface has no tool that takes an
// Application name and returns what it deploys, and adding one is the decision
// this type's shape exists to make visible.
type FleetApp struct {
	// Name is the Application's own name, as ArgoCD serves it.
	Name Text `json:"name"`
	// Namespace is where the Application object itself lives -- the ArgoCD
	// namespace, or another one under apps-in-any-namespace. Not the namespace
	// the Application deploys INTO, which is a property of what it renders and
	// is therefore not published here.
	//
	// Carried because it is half of an Application's identity: two
	// Applications of one name in two namespaces are two Applications, and a
	// row a client cannot key uniquely is a row it cannot join anything onto.
	Namespace *Text `json:"namespace,omitempty"`
	// Cluster is the cluster this Application lands on, named as the cluster
	// inventory names it.
	//
	// Absent when the live reading's destination resolved to no cluster the
	// inventory knows, which is a disagreement between two ArgoCD reads rather
	// than a fleet member with nowhere to go. Absent rather than guessed: a
	// wrong cluster name is acted on, and a missing one is asked about.
	Cluster *Text `json:"cluster,omitempty"`

	// ObservedIn says which reading this row's identity came from, and
	// ObservedAt when that reading was made.
	//
	// Per row rather than per result, and that is not redundancy waiting to be
	// factored out. The rows are the live reading's today; when the expansion
	// is retained beside it, a row's chart detail will be older than its
	// identity, and this is the stamp the two are told apart by.
	ObservedIn string    `json:"observedIn"`
	ObservedAt time.Time `json:"observedAt"`
	// ObservedAgeSeconds is how long before this response that reading was
	// made. Beside the timestamp for the reason every other age on this
	// surface is: a client comparing a server's timestamp against its own
	// clock is comparing two clocks.
	ObservedAgeSeconds int64 `json:"observedAgeSeconds"`

	// Renders is what this Application renders from, joined from the gate's
	// last render expansion.
	//
	// Absent when that expansion did not know this Application, which is the
	// common case rather than the corner: the rows are every Application the
	// install's ArgoCD can see, and the expansion covers what the gated
	// repository defines. Absent rather than guessed at, and never another
	// Application's chart -- see the merge rule at the top of this file.
	Renders *FleetRender `json:"renders,omitempty"`
}

// FleetRender is what one Application renders from, as the gate's last render
// expanded it.
//
// Its own stamps rather than the row's, and that is the point of the type
// rather than a detail of it. The identity above came from a live reading of
// ArgoCD; this came from rendering a git revision, and the two are different
// observations of different things. A row that published one time for both
// would be claiming the fresher stamp for the staler half.
type FleetRender struct {
	// SourceType is how this Application gets its manifests: "helm" for a
	// chart at a pinned version, "path" for a directory in the repository.
	//
	// A typed fact rather than tagged text, and therefore VETTED rather than
	// labelled, which is the rule mcp.go states for the one other field of
	// this kind. A client branches on this word, so it is published only when
	// it is one of the two below and absent otherwise: a third the gate grows
	// would reach a client as a word this surface never declared, and a
	// client's default branch is the wrong place to find that out.
	SourceType string `json:"sourceType,omitempty"`
	// Chart, ChartRepository and Version are the chart, where it is served
	// from, and the version pinned to it. All three are absent for a source
	// that is not a chart, which is a real answer rather than a gap.
	Chart           *Text `json:"chart,omitempty"`
	ChartRepository *Text `json:"chartRepository,omitempty"`
	Version         *Text `json:"version,omitempty"`
	// ApplicationSet is what generated this Application, and is absent for
	// one the repository commits directly. Absent rather than empty: nothing
	// generated it, and naming the source it was read from instead would name
	// an object nothing serves.
	ApplicationSet *Text `json:"applicationSet,omitempty"`

	// ObservedIn, ObservedAt and ObservedAgeSeconds are which observation this
	// half of the row came from and when it was made. Read them against the
	// row's own: two stamps that differ is the ordinary case, and it is what
	// tells a client which half to trust for what.
	ObservedIn         string    `json:"observedIn"`
	ObservedAt         time.Time `json:"observedAt"`
	ObservedAgeSeconds int64     `json:"observedAgeSeconds"`
}

// The source types a row can report, which are the gate's two.
//
// Constants because a client branches on them, and named here rather than
// taken from the gate's own because this package cannot import it: the two
// spellings agreeing is a contract, and mcp_fleet_test.go in the repository
// root is what holds it.
const (
	// RenderHelm is a chart pulled at a pinned version.
	RenderHelm = "helm"
	// RenderPath is a directory in the repository, rendered as it stands.
	RenderPath = "path"
)

// ChartDetail is what has been read about what these rows render from.
//
// A typed flag, a sentence and a stamp, rather than an absent field a client
// has to interpret. "The expansion has not run", "it ran and knew none of
// these Applications" and "these charts are unpinned" reach a reader as the
// same missing key otherwise, and they are three different reasons to go
// looking somewhere else.
type ChartDetail struct {
	// Expanded is whether the gate's render expansion -- the only source of a
	// row's chart, pinned version and originating ApplicationSet -- has been
	// read at all.
	//
	// A claim about the expansion rather than about the rows. True with no
	// row carrying chart detail is a real answer: a render that knew none of
	// the Applications ArgoCD is serving.
	Expanded bool `json:"expanded"`
	// Status is what that means, in bosun's own words.
	Status Text `json:"status"`
	// ObservedAt and ObservedAgeSeconds are when that expansion was made,
	// present exactly when Expanded is true.
	//
	// Here as well as on each row, because a render that matched no row
	// leaves no per-row stamp to read its age off -- and that is precisely
	// the case where a caller wants to know how old it is.
	ObservedAt         *time.Time `json:"observedAt,omitempty"`
	ObservedAgeSeconds *int64     `json:"observedAgeSeconds,omitempty"`
}

// ObservedLive marks a row whose identity came from the sweep's own read of
// the ArgoCD API: an Application that reached an apiserver.
//
// A word rather than a bool, because the second value is already known and is
// not "not live": it is the gate's render expansion, whose Application names
// came out of `helm template` and never reached an apiserver at all.
const ObservedLive = "live"

// ObservedExpansion marks a row's chart detail as the gate's render
// expansion's: names that came out of `helm template` and never reached an
// apiserver at all.
//
// The second value originOf was written for, and the reason it takes an
// argument rather than assuming.
const ObservedExpansion = "expansion"

// inventory answers from the last live reading, and from nothing else.
func (s *Server) inventory(json.RawMessage) (any, error) {
	out := Fleet{Repository: s.Repository}

	g := s.gate()
	out.SweptAt, out.AgeSeconds, out.Swept = s.stamp(g.SweptAt)

	// The reading decides whether there are rows, and the sweep decides
	// nothing about it. That is not a stylistic preference: a sweep stamps
	// itself only once every pull request it started has been answered, and a
	// run makes its reading in the middle of that, so a first sweep in flight
	// is a process holding a fleet and no sweep time. Gating the rows on the
	// sweep would have that process answer "nothing has read what this fleet
	// runs" while holding the answer -- the one claim this tool exists to make
	// impossible.
	if g.Fleet == nil {
		out.Status = say(fleetUnread, OriginBosun, maxSummary)
		if !out.Swept {
			out.Status = say(fleetUnswept, OriginBosun, maxSummary)
		}
		return out, nil
	}

	// The join is set up before the rows are built, and over the two readings
	// together: an identity that appears twice on EITHER side is one this
	// process cannot resolve, and a row must not be given a chart that may
	// belong to the Application beside it.
	renders := joinable(g.Fleet.Apps, g.Expansion)

	apps := make([]FleetApp, 0, len(g.Fleet.Apps))
	matched := 0
	for _, a := range g.Fleet.Apps {
		row := s.fleetApp(a, ObservedLive, g.Fleet.ObservedAt)
		if from, ok := renders[identity{a.Name, a.Cluster}]; ok {
			row.Renders = ptr(s.fleetRender(*from, ObservedExpansion, g.Expansion.ObservedAt))
			matched++
		}
		apps = append(apps, row)
	}
	out.Applications = &apps
	out.Status = say(fleetRead, OriginBosun, maxSummary)
	if len(apps) == 0 {
		out.Status = say(fleetEmpty, OriginBosun, maxSummary)
	}
	out.ChartDetail = s.chartDetail(g.Expansion, matched)
	return out, nil
}

// chartDetail is the result's own claim about where a row's chart came from,
// and there are three of them rather than a flag.
//
// "No expansion has been read", "one has and knew none of these Applications"
// and "one has and enriched some of them" are different situations with
// different next steps, and a client that could only see a boolean would treat
// the middle one as the first.
func (s *Server) chartDetail(e *GateExpansion, matched int) *ChartDetail {
	if e == nil {
		return &ChartDetail{
			Expanded: false,
			Status:   say(chartDetailUnexpanded, OriginBosun, maxSummary),
		}
	}
	sentence := chartDetailExpanded
	if matched == 0 {
		sentence = chartDetailUnmatched
	}
	age := s.since(e.ObservedAt)
	return &ChartDetail{
		Expanded:           true,
		Status:             say(sentence, OriginBosun, maxSummary),
		ObservedAt:         &e.ObservedAt,
		ObservedAgeSeconds: &age,
	}
}

// identity is what the two readings are joined on: an Application's name and
// the cluster it lands on.
//
// A comparable struct rather than the two halves spliced into one string,
// because a separator is a byte an Application name could contain and this map
// key is the whole of the guarantee that a row gets its own chart.
//
// Both halves, because one ArgoCD serves many clusters and one Application
// name generated for each of them is the ordinary shape rather than a
// collision.
//
// The Application object's own namespace is deliberately NOT in it, and cannot
// be: the expansion knows the namespace an Application deploys INTO, and the
// reading knows the namespace the Application object lives in. Two different
// namespaces under one word, and joining on either would be joining on
// something the other side does not have. joinable is where the ambiguity that
// leaves is answered.
type identity struct{ Name, Cluster string }

// joinable indexes the expansion by identity, leaving out every identity
// either reading holds twice.
//
// Left out rather than marked, so there is no entry a caller has to remember
// to distrust: an identity this process cannot resolve and one the expansion
// never had are the same answer, which is no chart detail.
//
// Two Applications of one name on one cluster is what apps-in-any-namespace
// permits, and the namespace that would tell them apart is not a field both
// readings have -- so there is nothing here that can say which of them a chart
// belongs to. An absent chart is asked about, and a wrong one is acted on.
//
// nil when there is no expansion, which reads as an index that matches
// nothing, which is what it is.
func joinable(live []GateFleetApp, e *GateExpansion) map[identity]*GateExpansionApp {
	if e == nil {
		return nil
	}
	// Counted first, and on both sides, because an identity's second
	// occurrence can arrive after the entry has been indexed. A pass that
	// removed a key as it found the duplicate would put a third occurrence
	// back.
	expanded := map[identity]int{}
	for _, a := range e.Apps {
		expanded[identity{a.Name, a.Cluster}]++
	}
	served := map[identity]int{}
	for _, a := range live {
		served[identity{a.Name, a.Cluster}]++
	}

	out := make(map[identity]*GateExpansionApp, len(e.Apps))
	for i := range e.Apps {
		// One entry for an identity the reading holds twice belongs to at
		// most one of those rows, and giving it to both hands a reader
		// somebody else's chart.
		k := identity{e.Apps[i].Name, e.Apps[i].Cluster}
		if expanded[k] > 1 || served[k] > 1 {
			continue
		}
		out[k] = &e.Apps[i]
	}
	return out
}

// fleetApp maps one Application onto the wire.
//
// Dull, like every mapping here: the live reading decided which rows exist and
// what each one is called, and this adds the origin tags, the caps and the
// stamps, and not one fact.
func (s *Server) fleetApp(a GateFleetApp, observedIn string, at time.Time) FleetApp {
	// The provenance is an argument rather than a constant in here, and the
	// origin is derived from it rather than written beside it. There is one
	// reading today and there will be two; a mapping site that tagged its rows
	// by hand is where the second one gets tagged as the first.
	origin := originOf(observedIn)
	out := FleetApp{
		Name:               say(a.Name, origin, maxName),
		ObservedIn:         observedIn,
		ObservedAt:         at,
		ObservedAgeSeconds: s.since(at),
	}
	if a.Namespace != "" {
		out.Namespace = ptr(say(a.Namespace, origin, maxName))
	}
	if a.Cluster != "" {
		out.Cluster = ptr(say(a.Cluster, origin, maxName))
	}
	return out
}

// fleetRender maps what one Application renders from onto the wire.
//
// As dull as fleetApp, and tagged the same way: the origin follows the
// provenance argument rather than being written in here, because every string
// on this path came out of `helm template` and the day one of them does not is
// the day a hand-written tag is wrong.
//
// Absent rather than empty, field by field. A source that is not a chart has
// no chart, and an Application nothing generated has no ApplicationSet; both
// are answers rather than gaps, and an empty string on the wire is a gap.
func (s *Server) fleetRender(a GateExpansionApp, observedIn string, at time.Time) FleetRender {
	origin := originOf(observedIn)
	out := FleetRender{
		SourceType:         vetSourceType(a.SourceType),
		ObservedIn:         observedIn,
		ObservedAt:         at,
		ObservedAgeSeconds: s.since(at),
	}
	if a.Chart != "" {
		out.Chart = ptr(say(a.Chart, origin, maxName))
	}
	if a.ChartRepo != "" {
		out.ChartRepository = ptr(say(a.ChartRepo, origin, maxName))
	}
	if a.Version != "" {
		out.Version = ptr(say(a.Version, origin, maxName))
	}
	if a.AppSet != "" {
		out.ApplicationSet = ptr(say(a.AppSet, origin, maxName))
	}
	return out
}

// vetSourceType is the source type if this surface has declared it, and ""
// otherwise.
//
// The check a typed fact gets instead of a tag. Everything else out of the
// expansion is text somebody's template chose and travels labelled as such;
// this one is bosun's own vocabulary and a client branches on the word, so
// publishing one that is not in the vocabulary would be handing a client a
// case it has no branch for, in the field it trusts to be closed.
//
// Absent is the refusal, for the reason a forgeable migration is absent rather
// than labelled: a client can see a missing field, and cannot see that a word
// it did not recognise was ever meant to mean something.
func vetSourceType(s string) string {
	switch s {
	case RenderHelm, RenderPath:
		return s
	}
	return ""
}

// originOf is where a row's strings came from, decided by where the row was
// observed rather than by whichever branch happened to build it.
//
// The distinction is real and is invisible in the bytes. An Application name
// the live reading served reached an apiserver, so it is an RFC1123 name with
// no sentence in it for anything to hide inside. A name the gate's expansion
// produced came out of `helm template`, which applies nothing, so
// `metadata.name` is whatever the template wrote -- newlines and backticks
// included. Tagging the two the same way, or tagging them by hand at each
// mapping site, is how they end up distinguished by luck.
func originOf(observedIn string) Origin {
	if observedIn == ObservedLive {
		return OriginCluster
	}
	// ObservedExpansion, and anything a later reading is called.
	//
	// Anything that did not come off an apiserver is a string somebody's
	// template chose, and the chart origin is the weakest true thing to say
	// about it. A default that guessed the other way would be a claim this
	// package cannot check.
	return OriginChart
}

// The sentences, one per shape the fleet can be in.
//
// Constants with nothing interpolated into them, which is what lets every one
// of them be published as bosun's own.
const (
	fleetUnswept = "Nothing has read what this fleet runs, and no gate sweep has completed " +
		"either, so this install has not yet looked at anything. This is not an empty fleet: " +
		"nothing has looked. Ask again once the gate has rendered a pull request."

	fleetUnread = "No gate run has read the fleet yet, so nothing is claimed about what runs " +
		"where. The reading is made when the gate renders a pull request, so an install with " +
		"none open has not made one however many sweeps have completed. This is not an empty " +
		"fleet."

	fleetRead = "This is every Application bosun's last live reading of ArgoCD served, with " +
		"the cluster each one lands on. They are not filtered by repository: this is what " +
		"the install's ArgoCD credentials can see. Each row says when it was observed."

	fleetEmpty = "The last live reading of ArgoCD served no Application at all. This is a " +
		"reading that looked, rather than a fleet nothing has read: an install that has read " +
		"nothing publishes no rows at all and says so above."

	chartDetailUnexpanded = "No row says what it renders from. The chart, the pinned version " +
		"and the ApplicationSet an Application was generated from come from the gate's render " +
		"expansion, which is made when the gate renders a pull request, and no run has made " +
		"one. These charts are not unpinned; nothing here has read what they render from."

	chartDetailExpanded = "The rows the gate's last render knew of say what they render from, " +
		"each stamped with when that render was made. It is a second observation, of the " +
		"revision that render started from rather than of what ArgoCD serves, so read the two " +
		"stamps on a row rather than the sweep time above. A row with no chart detail is one " +
		"that render did not know of: it covers what this repository defines, and the rows " +
		"are every Application the install's ArgoCD can see."

	chartDetailUnmatched = "The gate's last render has been read and it knew none of the " +
		"Applications this reading served, so no row says what it renders from. A render " +
		"covers what this repository defines, at the revision it started from, and the rows " +
		"are every Application the install's ArgoCD can see; the two can have nothing in " +
		"common. These charts are not unpinned; nothing here has matched them to what " +
		"renders them."
)
