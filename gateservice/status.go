package gateservice

import (
	"time"

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
}

// PRStatus is one open pull request as the gate last saw it.
type PRStatus struct {
	Number int
	Title  string
	URL    string
	// State is the verdict on the head commit: "passing", "failing", "error"
	// (the gate could not run), "running" (a render is in flight), or
	// "unknown" (the sweep could not read one and did not produce one).
	State string
}

// Status returns a copy of what the last sweep recorded.
func (g *Service) Status() Status {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Status{
		SweptAt: g.sweptAt,
		Err:     g.sweepErr,
		Open:    append([]PRStatus(nil), g.lastOpen...),
		Held:    len(g.results),
		Running: len(g.inflight),
	}
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
		st := PRStatus{Number: pr.Number, Title: pr.Title, URL: pr.URL}
		switch {
		case g.inflight[pr.HeadSHA] != nil:
			st.State = "running"
		default:
			if o, ok := g.results[pr.HeadSHA]; ok {
				switch {
				case o.Err != nil:
					st.State = "error"
				case o.State == gitprovider.CheckSuccess:
					st.State = "passing"
				default:
					st.State = "failing"
				}
			} else if s, ok := posted[pr.HeadSHA]; ok {
				if s == gitprovider.CheckSuccess {
					st.State = "passing"
				} else {
					st.State = "failing"
				}
			} else {
				// A verdict this sweep neither produced nor read. The honest
				// word is the vague one.
				st.State = "unknown"
			}
		}
		out = append(out, st)
	}
	return out
}
