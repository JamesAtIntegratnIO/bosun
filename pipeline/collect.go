package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// KargoSource is the cluster half of a sweep.
type KargoSource interface {
	Stages(ctx context.Context) ([]cluster.KargoStage, error)
	Warehouses(ctx context.Context) ([]cluster.KargoWarehouse, error)
	Promotions(ctx context.Context) ([]cluster.KargoPromotion, error)
}

// kargoPresence is the optional capability of answering whether the cluster
// serves Kargo at all.
//
// Type-asserted rather than required, matching the other capability seams
// here: a source that cannot answer is read as it always was.
//
// Worth asking because without it a cluster with no Kargo installed produces
// three separate read failures -- "Stages could not be read", "Warehouses
// could not be read", "promotions could not be read" -- which reads as three
// things going wrong rather than one thing being absent, and sends the reader
// looking for a permissions problem that is not there.
type kargoPresence interface {
	KargoAvailable(ctx context.Context) bool
}

// PRSource is the git half. Optional: without it the orphan and superseded
// detectors stay quiet rather than guessing.
type PRSource interface {
	ListOpenPullRequests(ctx context.Context) ([]gitprovider.PullRequest, error)
}

// Collector assembles a Snapshot.
//
// EVERY SOURCE IS OPTIONAL AND EVERY FAILURE IS A NOTE, never an error. That
// is not politeness -- it is the package's subject applied to itself. A sweep
// that gave up because it could not list pull requests would report nothing,
// and reporting nothing is indistinguishable from finding nothing, which is
// the exact confusion this package exists to end. So a sweep does what it can,
// says what it could not do, and the report carries both.
type Collector struct {
	Kargo KargoSource
	PRs   PRSource
	// Now is injected so a sweep is reproducible in a test.
	Now func() time.Time
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Collect reads everything available and returns a Snapshot.
//
// repoRoot is a checkout to resolve tracked pins against, and is empty when
// there is none -- which disables the pin check and puts a note on the report
// saying so. A PARAMETER, not a field: it is a property of one sweep, and the
// caller used to hand it over by writing to the Collector between calls, so two
// overlapping sweeps would have raced over which checkout the second one read.
func (c *Collector) Collect(ctx context.Context, repoRoot string) *Snapshot {
	s := &Snapshot{Now: c.now()}

	if c.Kargo == nil {
		s.Notes = append(s.Notes, "no cluster reader: nothing about Kargo could be checked")
		return s
	}
	if p, ok := c.Kargo.(kargoPresence); ok && !p.KargoAvailable(ctx) {
		s.Notes = append(s.Notes,
			"this cluster does not serve the Kargo API, so there is no pipeline here to supervise")
		return s
	}
	if stages, err := c.Kargo.Stages(ctx); err != nil {
		s.Notes = append(s.Notes, fmt.Sprintf("Stages could not be read (%v), so nothing is claimed about them", err))
	} else {
		for _, st := range stages {
			p := Stage{
				Name: st.Name, Namespace: st.Namespace, CurrentFreight: st.CurrentFreight,
				Ready: st.Ready, ReadyReason: st.ReadyReason, ReadyMessage: st.ReadyMessage,
				ReadySince:     st.ReadySince,
				VerificationID: st.VerificationID, VerificationPhase: st.VerificationPhase,
			}
			for _, u := range st.Updates {
				p.Updates = append(p.Updates, Update{Path: u.Path, Keys: u.Keys})
			}
			s.Stages = append(s.Stages, p)
		}
	}
	if whs, err := c.Kargo.Warehouses(ctx); err != nil {
		s.Notes = append(s.Notes, fmt.Sprintf("Warehouses could not be read (%v), so a stalled one would not have been found", err))
	} else {
		for _, w := range whs {
			s.Warehouses = append(s.Warehouses, Warehouse{
				Name: w.Name, Namespace: w.Namespace, Interval: w.Interval,
				DiscoveredAt: w.DiscoveredAt, Ready: w.Ready,
				ReadyReason: w.ReadyReason, ReadyMessage: w.ReadyMessage, Latest: w.Latest,
			})
		}
	}
	if ps, err := c.Kargo.Promotions(ctx); err != nil {
		s.Notes = append(s.Notes, fmt.Sprintf("promotions could not be read (%v), so a wedged Stage would not have been found", err))
	} else {
		for _, p := range ps {
			s.Promotions = append(s.Promotions, Promotion{
				Name: p.Name, Namespace: p.Namespace, Stage: p.Stage, Freight: p.Freight,
				Phase: p.Phase, StartedAt: p.StartedAt, CreatedAt: p.CreatedAt, Message: p.Message,
			})
		}
	}

	if c.PRs != nil {
		if prs, err := c.PRs.ListOpenPullRequests(ctx); err != nil {
			s.Notes = append(s.Notes, fmt.Sprintf("open pull requests could not be listed (%v), so superseded and orphaned ones were not checked", err))
		} else {
			for _, pr := range prs {
				s.OpenPRs = append(s.OpenPRs, PullRequest{Number: pr.Number, Branch: pr.Branch})
			}
		}
	}

	if repoRoot != "" {
		s.RepoRoot = repoRoot
		s.FileHas = NewFileKeys(repoRoot).Has
	}
	return s
}

// Sweep collects and detects in one call.
func (c *Collector) Sweep(ctx context.Context, repoRoot string) *Report {
	return Detect(c.Collect(ctx, repoRoot))
}
