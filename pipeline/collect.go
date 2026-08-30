package pipeline

import (
	"context"
	"fmt"
	"strings"
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
// three separate read failures, "Stages could not be read", "Warehouses could
// not be read", "promotions could not be read", which reads as three things
// going wrong rather than one thing being absent, and sends the reader
// looking for a permissions problem that is not there.
type kargoPresence interface {
	KargoAvailable(ctx context.Context) bool
}

// freightSource is the optional capability of saying what a freight carries.
//
// Type-asserted rather than added to KargoSource, matching the presence check
// above. A source without it produces exactly the findings it always did,
// with the freight hash in them where the artifact would have been.
type freightSource interface {
	Freight(ctx context.Context, namespace, name string) (cluster.KargoFreight, error)
}

// verificationSource is the optional capability of reading the AnalysisRun a
// Stage's verification is. Same rule: absent means a quieter finding, never a
// missing one.
type verificationSource interface {
	AnalysisRun(ctx context.Context, namespace, name string) (cluster.AnalysisRun, error)
}

// PRSource is the git half. Optional: without it the orphan and superseded
// detectors stay quiet rather than guessing.
type PRSource interface {
	ListOpenPullRequests(ctx context.Context) ([]gitprovider.PullRequest, error)
}

// Collector assembles a Snapshot.
//
// Every source is optional and every failure is a note, never an error. That
// is not politeness; it is the package's subject applied to itself. A sweep
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
// there is none, which disables the pin check and puts a note on the report
// saying so. A parameter, not a field: it is a property of one sweep, and the
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
				VerificationRunNamespace: st.VerificationRunNamespace,
				VerificationRunName:      st.VerificationRunName,
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

	c.name(ctx, s)

	if repoRoot != "" {
		s.RepoRoot = repoRoot
		s.FileHas = NewFileKeys(repoRoot).Has
	}
	return s
}

// name reads the objects a finding will name, and nothing else.
//
// Both of these are per-object GETs, and the temptation is to list instead --
// one request rather than several. It is the wrong trade twice over. Kargo
// creates a Freight per discovery and prunes none of them, so a cluster that
// has been running a year holds thousands, and a sweep on a timer would read
// all of them every time to print two. And the objects worth naming are
// exactly the objects a finding is about, which is a set the size of the
// number of things currently wrong: on a healthy fleet this makes no requests
// at all.
//
// The gates below deliberately mirror the two detectors that print these,
// which is why the Stage predicate is a method rather than a copied substring.
// A miss is silent by design -- Describe falls back to the hash and the
// verification sentence shortens -- so erring wide here costs a request and
// erring narrow costs nothing but detail.
func (c *Collector) name(ctx context.Context, s *Snapshot) {
	fr, hasFreight := c.Kargo.(freightSource)
	vr, hasVerification := c.Kargo.(verificationSource)
	if !hasFreight && !hasVerification {
		return
	}

	// Distinct failures, in the order they happened. A cluster that refuses
	// one of these refuses all of them, and eleven copies of "not permitted
	// to read AnalysisRuns" is not eleven times the information.
	var why []string
	seen := map[string]bool{}
	fail := func(err error) {
		if msg := err.Error(); !seen[msg] {
			seen[msg] = true
			why = append(why, msg)
		}
	}

	byStage := s.promotionsByStage()
	freight := func(namespace, id string) {
		if !hasFreight || namespace == "" || id == "" {
			return
		}
		key := namespace + "/" + id
		if _, done := s.Freight[key]; done {
			return
		}
		f, err := fr.Freight(ctx, namespace, id)
		if err != nil {
			fail(err)
			return
		}
		if s.Freight == nil {
			s.Freight = map[string]Freight{}
		}
		s.Freight[key] = Freight{
			Name: f.Name, Namespace: f.Namespace, Alias: f.Alias, Artifacts: f.Artifacts,
		}
	}

	for _, st := range s.Stages {
		// What a wedged Stage stopped receiving: the freight of its newest
		// promotion, which is the one detectWedged reports on.
		if ps := byStage[st.Name]; len(ps) > 0 && Unsuccessful(ps[0].Phase) {
			freight(ps[0].Namespace, ps[0].Freight)
		}
		if !st.StoppedByVerification() {
			continue
		}
		// What a stopped verification is holding.
		freight(st.Namespace, st.CurrentFreight)
		if !hasVerification || st.VerificationRunName == "" {
			continue
		}
		run, err := vr.AnalysisRun(ctx, st.VerificationRunNamespace, st.VerificationRunName)
		if err != nil {
			fail(err)
			continue
		}
		v := Verification{
			Name: run.Name, Namespace: run.Namespace, Phase: run.Phase, Message: run.Message,
		}
		for _, m := range run.Metrics {
			v.Metrics = append(v.Metrics, VerifyMetric{
				Name: m.Name, Phase: m.Phase, Message: m.Message,
				Failed: m.Failed, Error: m.Error, Unbounded: m.Unbounded,
			})
		}
		if s.Verifications == nil {
			s.Verifications = map[string]Verification{}
		}
		s.Verifications[run.Namespace+"/"+run.Name] = v
	}

	if len(why) > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf(
			"some findings could not be filled in (%s), so they name the object and not what is in it",
			strings.Join(why, "; ")))
	}
}

// Sweep collects and detects in one call.
func (c *Collector) Sweep(ctx context.Context, repoRoot string) *Report {
	return Detect(c.Collect(ctx, repoRoot))
}
