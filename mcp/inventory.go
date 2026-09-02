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
// # What is absent here, and why it is not zero
//
// Chart, pinned version and originating ApplicationSet come from the gate's
// render expansion, which this does not yet retain. They are absent rather
// than empty, under a flag that says the expansion has not run, because "these
// charts are unpinned" and "nothing has read what they render from" are
// different answers and only one of them is true.

// inventoryDescription is what a client hands its model as this tool's
// purpose. A constant, and it has to stay one: see pipelineReportDescription.
const inventoryDescription = "The fleet as bosun's last live reading of ArgoCD saw it: every " +
	"Application that reading served, with the cluster each one lands on. Use it to answer " +
	"\"where does this run\" without a cluster credential of your own. Names and clusters only " +
	"-- no manifest, values file or rendered object is served here, by any argument. Every row " +
	"says when it was observed, which is as old as the last gate run rather than the last " +
	"sweep: an install with no open pull request renders nothing and therefore reads nothing. " +
	"Answers from that snapshot: it reaches no cluster, no git host and no model, and it can " +
	"change nothing."

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
}

// ChartDetail is whether a row says what it renders from.
//
// A typed flag and a sentence, rather than an absent field a client has to
// interpret. "The expansion has not run" and "these charts are unpinned" reach
// a reader as the same missing key otherwise, and only one of them is a reason
// to go looking.
type ChartDetail struct {
	// Expanded is whether the gate's render expansion -- the only source of a
	// row's chart, pinned version and originating ApplicationSet -- has been
	// retained for these rows.
	Expanded bool `json:"expanded"`
	// Status is what that means, in bosun's own words.
	Status Text `json:"status"`
}

// ObservedLive marks a row whose identity came from the sweep's own read of
// the ArgoCD API: an Application that reached an apiserver.
//
// A word rather than a bool, because the second value is already known and is
// not "not live": it is the gate's render expansion, whose Application names
// came out of `helm template` and never reached an apiserver at all.
const ObservedLive = "live"

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

	apps := make([]FleetApp, 0, len(g.Fleet.Apps))
	for _, a := range g.Fleet.Apps {
		apps = append(apps, s.fleetApp(a, ObservedLive, g.Fleet.ObservedAt))
	}
	out.Applications = &apps
	out.Status = say(fleetRead, OriginBosun, maxSummary)
	if len(apps) == 0 {
		out.Status = say(fleetEmpty, OriginBosun, maxSummary)
	}
	out.ChartDetail = &ChartDetail{
		// False, and written here rather than plumbed: nothing retains the
		// render expansion yet, so no row can carry a chart. The flag exists
		// so that stays a fact a client reads instead of a key it has to
		// notice is missing.
		Expanded: false,
		Status:   say(chartDetailUnexpanded, OriginBosun, maxSummary),
	}
	return out, nil
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
		"expansion, and this build does not retain it. These charts are not unpinned; nothing " +
		"here has read what they render from."
)
