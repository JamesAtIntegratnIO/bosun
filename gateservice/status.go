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
}

// Status returns a copy of what the last sweep recorded.
func (g *Service) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := Status{
		SweptAt: g.sweptAt,
		Err:     g.sweepErr,
		Open:    append([]PRStatus(nil), g.lastOpen...),
		Held:    len(g.results),
		Running: len(g.inflight),
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
