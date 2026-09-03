package gateservice

import (
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// The gate's own account of itself, for the status page.
//
// Everything here is a copy of state the sweep already holds; nothing is
// computed on demand and nothing reaches the git host. That is deliberate: the
// page that reads this may be exposed through a gateway, and a page whose
// refresh costs API calls is a page somebody's browser tab can spend a rate
// limit on.

// Status is what the last sweep saw. The zero value is honest: a SweptAt of
// zero means no sweep has completed, which is not the same as a sweep that
// found no pull requests.
type Status struct {
	// SweptAt is when the last sweep finished. Zero before the first one.
	SweptAt time.Time
	// Err is what stopped the last sweep from seeing anything, and "" when it
	// ran. A gate that cannot list pull requests must say so somewhere a
	// human looks, because its other symptom is a page that reads "nothing
	// open" forever.
	Err string
	// Open is every pull request the last sweep saw, each with the verdict
	// standing against its head commit.
	Open []PRStatus
	// Held is how many verdicts are cached in memory; Running is how many
	// gate runs are in flight right now.
	Held    int
	Running int
	// Fleet is what the last live reading of ArgoCD served, or nil when no
	// gate run has made one.
	//
	// Its own clock, and that is the point rather than an oversight. The
	// reading is made by a RUN, and a sweep with nothing to render makes none,
	// so on a quiet install this is older than SweptAt above and stays that
	// way. A reader told only the sweep's time would be reading a number about
	// something else.
	Fleet *Fleet
	// Expansion is what the last run's render expanded this repository into,
	// or nil when no run has rendered one.
	//
	// A third clock, and the oldest of the three. The reading beside it says
	// which Applications exist; this says what they render from, at the
	// revision the last run started from. Neither answers the other's
	// question, so both are kept and each says when it was made.
	Expansion *Expansion
	// HistoryCap is how many earlier verdicts one pull request's comment
	// remembers, which is what makes a short history readable: a history
	// exactly this long has had older entries dropped from it.
	//
	// On the snapshot rather than looked up by a reader, so the number a
	// client is told is the number this build applied to the rows beside it.
	HistoryCap int
}

// Fleet is one live reading of what ArgoCD serves.
//
// Retained rather than recomputed, because the run has already paid for it:
// the gate reads what this repository deploys to decide what to render, and
// the same read says what every other Application on the control plane is and
// where it lands. Discarding that is what left "where does this run" answerable
// only with a cluster credential.
//
// Nothing here is content. Names and a cluster, which is what a listing is; a
// reader wanting what an Application deploys is asking the render, and this
// type has nowhere to put the answer.
type Fleet struct {
	// ObservedAt is when the reading was made.
	ObservedAt time.Time
	// Apps is every Application it served, unfiltered and in a stable order.
	// Empty is a real answer: an ArgoCD serving nothing.
	Apps []FleetApp
}

// FleetApp is one Application, and where it lands.
type FleetApp struct {
	// Name and Namespace identify the Application object itself.
	Name      string
	Namespace string
	// Cluster is the cluster it lands on, as the cluster inventory names it,
	// and "" when the destination resolved to no cluster that inventory knows.
	//
	// Resolved here rather than published raw: a destination is a cluster name
	// or an apiserver address depending on who wrote the Application, and only
	// the inventory knows the two are one cluster. Empty rather than the
	// unresolved address, because a wrong cluster name is acted on and a
	// missing one is asked about.
	Cluster string
}

// Expansion is one render of what this repository deploys, at the revision a
// run started from.
//
// Retained for the reason the reading is: the run has already paid for it, and
// it is the only thing in this process that knows which chart an Application
// renders from. What it is NOT is a second reading of the fleet. It covers
// what the gated repository defines rather than what ArgoCD serves, and it is
// older than the reading by construction, so it enriches rows rather than
// adding any.
//
// The BASE revision, not the head. The head is the change being asked about,
// which by definition nothing has deployed; publishing its pinned versions as
// what the fleet runs would be wrong in the direction a reader acts on.
//
// Nothing here is content either. A chart name and the version pinned to it,
// and deliberately not the values the chart is rendered with: those are on the
// row this is copied from, and they stop here.
type Expansion struct {
	// ObservedAt is when the render was made.
	ObservedAt time.Time
	// Apps is every Application the expansion produced, in the render's own
	// order. Empty is a real answer: a repository that defines none.
	Apps []ExpansionApp
}

// ExpansionApp is one expanded Application, and what it renders from.
type ExpansionApp struct {
	// Name and Cluster are the Application's identity across renders, which
	// is also what a live reading's row is joined onto. Both halves, because
	// one ArgoCD serves many clusters and an Application name repeats across
	// them.
	Name    string
	Cluster string
	// AppSet is the ApplicationSet this Application was generated from, and
	// "" for one the repository commits directly.
	AppSet string
	// SourceType is how it gets its manifests: a chart at a pinned version,
	// or a directory in the repository.
	SourceType gate.RowSource
	// Chart, ChartRepo and Version are the chart it renders, where that chart
	// comes from, and the version pinned to it. All three are empty for a
	// source that is not a chart.
	Chart     string
	ChartRepo string
	Version   string
}

// The states a pull request's head commit can be in, as the sweep sees them.
//
// Constants rather than literals at the assignment sites, because this
// vocabulary now has readers outside this package: the status page renders a
// colour per state, and the MCP surface publishes the word itself to a client
// that branches on it. Three copies of five strings is how a sixth state
// reaches one reader and not the others -- and the reader that would miss it
// is the programmatic one, which has no eyes on it.
const (
	// StatePassing and StateFailing are verdicts: the gate ran and answered.
	StatePassing = "passing"
	StateFailing = "failing"
	// StateError is the gate failing to run, which is not a failing verdict:
	// nothing was judged, and the two want opposite reactions.
	StateError = "error"
	// StateRunning is a render in flight.
	StateRunning = "running"
	// StateUnknown is a verdict this sweep neither produced nor read. The
	// honest word is the vague one.
	StateUnknown = "unknown"
)

// PRStatus is one open pull request as the gate last saw it.
type PRStatus struct {
	Number int
	Title  string
	URL    string
	// HeadSHA is the commit the state below is about.
	//
	// On the snapshot because a verdict that does not name its commit cannot
	// be told apart from a stale one, and a reader deciding whether to trust
	// this answer is asking exactly that. The page shows it; a programmatic
	// reader needs it to know whether to ask again.
	HeadSHA string
	// State is the verdict on the head commit: "passing", "failing", "error"
	// (the gate could not run), "running" (a render is in flight), or
	// "unknown" (the sweep could not read one and did not produce one).
	State string
	// Err is why the gate could not run, and "" otherwise. Set exactly when
	// State is "error": that state on its own says a run failed and gives a
	// reader nothing to do about it.
	Err string
	// Labels are the labels standing on the pull request when the sweep
	// listed it.
	//
	// Carried rather than fetched, for the reason nothing else here is
	// fetched either: a reader of this snapshot may have no way to reach the
	// git host, and one that did would make every read cost a call against an
	// install's rate limit. The sweep already has them -- a listed pull
	// request arrives with its labels on it -- so this is a field kept rather
	// than a read made.
	//
	// They are how the attempt cap remembers across restarts, which is why a
	// reader asking what the agent will do next needs them and cannot compute
	// them.
	Labels []string
	// Verdict is the whole answer as data, or nil when this process did not
	// produce one.
	//
	// Nil is a real and common answer rather than a gap. A verdict already
	// standing on the git host is deliberately not re-litigated, so its state
	// is known and its breakdown is not; a run in flight has neither yet. A
	// reader that finds a state here and no verdict is being told the
	// difference, which is the difference between "green" and "green,
	// according to a run this process never made".
	Verdict *gate.Summary
	// History is what the gate said on each earlier head commit of this pull
	// request, oldest first, as the last publish onto it read the stamps back
	// out of its own comment.
	//
	// A POINTER to a slice, because absent and empty are different answers
	// here and publishing it is only worth anything if a reader can tell them
	// apart. Nil is "this process has read no comment for this pull request"
	// -- there is none, or none has been read since it started. Present and
	// empty is "the comment was read and recorded no earlier verdict", which
	// is what a pull request the gate has answered exactly once looks like.
	//
	// It is what the gate read off the git host rather than what this process
	// computed, which puts whoever can edit that comment in the trust picture.
	// The read surfaces that publish it say so.
	History *[]VerdictRow
}

// Status returns a copy of what the last sweep recorded.
func (g *Service) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := Status{
		SweptAt:    g.sweptAt,
		Err:        g.sweepErr,
		Open:       append([]PRStatus(nil), g.lastOpen...),
		Held:       len(g.results),
		Running:    len(g.inflight),
		HistoryCap: MaxHistory,
	}
	if g.fleet != nil {
		// Copied down to the slice, not just the pointer. The readers of this
		// are a status page and a tool surface, and one of them holding a
		// window onto the sweep's own memory is how a reader ends up editing
		// what the next one sees.
		out.Fleet = &Fleet{
			ObservedAt: g.fleet.ObservedAt,
			Apps:       append([]FleetApp(nil), g.fleet.Apps...),
		}
	}
	if g.expansion != nil {
		out.Expansion = &Expansion{
			ObservedAt: g.expansion.ObservedAt,
			Apps:       append([]ExpansionApp(nil), g.expansion.Apps...),
		}
	}
	return out
}

// retainFleet keeps the live reading a run just made, resolved onto the
// clusters the same run read.
//
// The newest reading wins, and it is compared by time rather than by arrival:
// runs are concurrent up to the configured limit, so two readings can land out
// of order, and a snapshot that took whichever finished last would occasionally
// publish the older fleet with the newer stamp on it.
//
// A run that could not derive does not reach here at all, which is deliberate:
// the last reading stays, stamped with when it was actually made. Stale
// evidence beats none as long as it is labelled stale, and the label is the
// timestamp.
func (g *Service) retainFleet(d *gate.Derivation, inv *gate.Inventory, at time.Time) {
	if d == nil {
		return
	}
	apps := make([]FleetApp, 0, len(d.Fleet))
	for _, a := range d.Fleet {
		apps = append(apps, FleetApp{
			Name:      a.Name,
			Namespace: a.Namespace,
			Cluster:   inv.ClusterFor(a.Destination),
		})
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fleet != nil && g.fleet.ObservedAt.After(at) {
		return
	}
	g.fleet = &Fleet{ObservedAt: at, Apps: apps}
}

// retainExpansion keeps what a run just rendered the repository into.
//
// The newest render wins, compared by time rather than by arrival, for the
// reason retainFleet gives: runs are concurrent, two renders can land out of
// order, and taking whichever finished last would occasionally publish the
// older expansion with the newer stamp on it.
//
// A run that could not render does not reach here, so the last expansion
// stays, stamped with when it was actually made. Stale evidence beats none as
// long as it is labelled stale, and the label is the timestamp.
//
// The values each row carries are dropped here rather than at the surface that
// publishes them. A field that never crosses cannot be published by mistake,
// and this is the one line where the whole of `gate.Row` is in scope.
func (g *Service) retainExpansion(t *gate.Table, at time.Time) {
	if t == nil {
		return
	}
	apps := make([]ExpansionApp, 0, len(t.Rows))
	for _, r := range t.Rows {
		app := ExpansionApp{
			Name: r.App, Cluster: r.Cluster,
			SourceType: r.SourceType,
			Chart:      r.Chart, ChartRepo: r.ChartRepo, Version: r.Version,
		}
		// Only when it names one. A row read from a committed Application
		// carries the config source it came from in the same field, and
		// publishing that as the ApplicationSet it was generated from would
		// name an object nothing serves.
		if r.FromAppSet {
			app.AppSet = r.AppSet
		}
		apps = append(apps, app)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.expansion != nil && g.expansion.ObservedAt.After(at) {
		return
	}
	g.expansion = &Expansion{ObservedAt: at, Apps: apps}
}

// snapshotLocked renders the sweep's view of its pull requests. Called with
// g.mu held, at the end of a sweep, when every Ensure the sweep started has
// stored its outcome.
//
// posted carries the verdicts the sweep read off the host and did not
// re-litigate; everything else is answered from results and inflight.
func (g *Service) snapshotLocked(prs []gitprovider.PullRequest, posted map[string]gitprovider.CheckState) []PRStatus {
	out := make([]PRStatus, 0, len(prs))
	for i := range prs {
		pr := &prs[i]
		st := PRStatus{Number: pr.Number, Title: pr.Title, URL: pr.URL, HeadSHA: pr.HeadSHA,
			Labels: append([]string(nil), pr.Labels...)}
		if held, read := g.history[pr.Number]; read {
			// Key-present rather than length, so a comment that recorded no
			// earlier verdict crosses as an empty history and a comment
			// nothing has read crosses as no history at all.
			rows := append(make([]VerdictRow, 0, len(held)), held...)
			st.History = &rows
		}
		switch {
		case g.inflight[pr.HeadSHA] != nil:
			st.State = StateRunning
		default:
			if o, ok := g.results[pr.HeadSHA]; ok {
				st.Verdict = o.Verdict
				switch {
				case o.Err != nil:
					st.State, st.Err = StateError, o.Err.Error()
				case o.State == gitprovider.CheckSuccess:
					st.State = StatePassing
				default:
					st.State = StateFailing
				}
			} else if s, ok := posted[pr.HeadSHA]; ok {
				if s == gitprovider.CheckSuccess {
					st.State = StatePassing
				} else {
					st.State = StateFailing
				}
			} else {
				// A verdict this sweep neither produced nor read. The honest
				// word is the vague one.
				st.State = StateUnknown
			}
		}
		out = append(out, st)
	}
	return out
}
