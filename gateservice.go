package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// GateService is the gate, run by the agent instead of by CI.
//
// The CI shape existed because ADR 0002 said "CI is where the checkout
// already is" -- and then the agent grew its own checkout, its own commit
// statuses, its own poll loop and live cluster access, at which point the CI
// adapter, the checked-in cluster inventory it exists to work around, and the
// report-comment contract for reading the verdict back were all vestige. See
// ADR 0008.
//
// The loop is the same one the agent already runs for check states: poll the
// open pull requests, and for every head commit that has no verdict, render
// the repository at the base and the head, diff, and publish -- a commit
// status for branch protection, and the same report comment CI would have
// posted, for humans. The verdict itself stays in memory, where the triage
// reads it without scraping its own comment back off the pull request.
//
// There is deliberately NO relevance filter. The CI adapter needed a paths
// job because a skipped required check bricks the pull request and CI minutes
// are billed; in here the status is always posted, so the safe answer to "is
// this change relevant?" is to render it and let the diff say "no change to
// what gets deployed". A docs-only pull request costs one render and gets a
// truthful green instead of a guessed one.
type GateService struct {
	Git gitprovider.Provider
	// Inventory reads the live cluster inventory. In production this is
	// cluster.APIServer.ClusterInventory; there is no snapshot fallback in
	// here, because a snapshot is a CI concern and CI mode does not run this
	// service.
	Inventory func(ctx context.Context) (*gate.Inventory, error)
	// CheckName is the commit-status context branch protection requires --
	// the same name the CI adapter reported under, so moving the gate
	// in-cluster changes nothing about protection rules.
	CheckName string
	RepoURL   string
	CloneRoot string
	// ForkPRs renders pull requests whose head branch lives in another
	// repository. Off by default: the render runs helm over the pull
	// request's content, inside the cluster, and that is a trust decision an
	// operator makes -- not a default.
	ForkPRs bool
	Poll    time.Duration
	// Timeout bounds one gate run. A hung chart render must not wedge the
	// sweep forever.
	Timeout time.Duration
	Log     func(string, ...any)

	// Checkout produces working copies at the base and head revisions and a
	// function that discards both. Defaults to a shallow clone plus a
	// worktree; tests substitute directories on disk.
	Checkout func(ctx context.Context, pr *gitprovider.PullRequest) (base, head string, cleanup func(), err error)

	mu       sync.Mutex
	results  map[string]*gateOutcome
	inflight map[string]chan struct{}
}

// gateOutcome is one head commit's verdict, kept so the triage can read it
// in-process and a sweep never runs the same commit twice.
type gateOutcome struct {
	// State is CheckSuccess or CheckFailure. Zero when Err is set.
	State gitprovider.CheckState
	// Report is the same markdown the comment carries, marker first --
	// everything downstream of the triage parses evidence out of this string,
	// and it is now handed over instead of scraped back.
	Report string
	// Err is the gate failing to run, which is a different thing from a
	// failing verdict -- exit 2, not exit 1.
	Err error
}

func (g *GateService) logf(f string, a ...any) {
	if g.Log != nil {
		g.Log(f, a...)
	}
}

func (g *GateService) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 15 * time.Minute
}

// Run polls until the context ends. One sweep at a time, pull requests in
// sequence: a gate run shells helm with its own concurrency, and stacking
// renders on top of each other buys wall-clock nothing a poll interval
// doesn't.
func (g *GateService) Run(ctx context.Context) {
	for {
		g.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(g.Poll):
		}
	}
}

func (g *GateService) sweep(ctx context.Context) {
	prs, err := g.Git.ListOpenPullRequests(ctx)
	if err != nil {
		g.logf("gate: listing open pull requests: %v", err)
		return
	}

	open := map[string]bool{}
	for i := range prs {
		pr := &prs[i]
		open[pr.HeadSHA] = true

		if g.known(pr.HeadSHA) {
			continue
		}
		if pr.FromFork && !g.ForkPRs {
			// An unreported required check blocks the merge with no
			// explanation, which is the CI adapter's paths-filter trap wearing
			// a new hat. Error, with the reason, says why and how to decide
			// otherwise.
			g.store(pr.HeadSHA, &gateOutcome{Err: fmt.Errorf("fork pull request")})
			g.status(ctx, pr, gitprovider.StateError,
				"not gated: fork pull request (gate.forkPRs renders fork content in-cluster)")
			continue
		}
		// A verdict that already stands -- from a previous life of this pod,
		// or from a CI adapter still running during a migration -- is not
		// re-litigated. The triage re-renders on demand if it needs the
		// report; the sweep's job is only that every head commit gets one.
		if state, err := g.Git.CheckStatus(ctx, pr.HeadSHA, g.CheckName); err == nil {
			if state == gitprovider.CheckSuccess || state == gitprovider.CheckFailure {
				continue
			}
		}
		g.Ensure(ctx, pr)
	}

	// Verdicts for commits no longer on any open pull request are history the
	// host already has. Kept, they are a slow leak.
	g.mu.Lock()
	for sha := range g.results {
		if !open[sha] {
			delete(g.results, sha)
		}
	}
	g.mu.Unlock()
}

func (g *GateService) known(sha string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, done := g.results[sha]
	_, running := g.inflight[sha]
	return done || running
}

func (g *GateService) store(sha string, out *gateOutcome) {
	g.mu.Lock()
	if g.results == nil {
		g.results = map[string]*gateOutcome{}
	}
	g.results[sha] = out
	g.mu.Unlock()
}

// Ensure returns the verdict for the pull request's head commit, running the
// gate if no run has answered for it yet. Concurrent callers for the same
// commit share one run -- the sweep and a Kargo-triggered triage arriving
// together must not render twice and comment twice.
func (g *GateService) Ensure(ctx context.Context, pr *gitprovider.PullRequest) *gateOutcome {
	g.mu.Lock()
	if out, ok := g.results[pr.HeadSHA]; ok {
		g.mu.Unlock()
		return out
	}
	if ch, ok := g.inflight[pr.HeadSHA]; ok {
		g.mu.Unlock()
		select {
		case <-ch:
			g.mu.Lock()
			out := g.results[pr.HeadSHA]
			g.mu.Unlock()
			if out == nil {
				return &gateOutcome{Err: fmt.Errorf("the gate run for %s was abandoned", pr.HeadSHA)}
			}
			return out
		case <-ctx.Done():
			return &gateOutcome{Err: ctx.Err()}
		}
	}
	if g.inflight == nil {
		g.inflight = map[string]chan struct{}{}
	}
	ch := make(chan struct{})
	g.inflight[pr.HeadSHA] = ch
	g.mu.Unlock()

	out := g.run(ctx, pr)

	g.mu.Lock()
	if g.results == nil {
		g.results = map[string]*gateOutcome{}
	}
	g.results[pr.HeadSHA] = out
	delete(g.inflight, pr.HeadSHA)
	g.mu.Unlock()
	close(ch)
	return out
}

// run is one gate run: the same render, diff and validation the CLI performs,
// against the live inventory, published as a status and a report comment.
func (g *GateService) run(ctx context.Context, pr *gitprovider.PullRequest) *gateOutcome {
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()

	g.status(ctx, pr, gitprovider.StatePending, "rendering %s and %s", refName(pr.BaseBranch), refName(pr.Branch))

	inv, err := g.Inventory(ctx)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("reading the cluster inventory: %w", err))
	}

	checkout := g.Checkout
	if checkout == nil {
		checkout = g.checkout
	}
	base, head, cleanup, err := checkout(ctx, pr)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("checking out %s and %s: %w", refName(pr.BaseBranch), refName(pr.Branch), err))
	}
	defer cleanup()

	// The config comes from the HEAD at both revisions -- the same rule the
	// CI adapter applies. The config describes how to render, not what to
	// render, and the base may predate it entirely, notably on the pull
	// request that introduces the gate.
	cfgRaw, err := os.ReadFile(filepath.Join(head, ".gitops-gate.yaml"))
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("no .gitops-gate.yaml at the head revision: %w", err))
	}
	cfg, err := gate.ParseConfig(cfgRaw, ".gitops-gate.yaml")
	if err != nil {
		return g.broke(ctx, pr, err)
	}

	baseTable, err := gate.Render(base, cfg, inv)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("rendering %s: %w", refName(pr.BaseBranch), err))
	}
	headTable, err := gate.Render(head, cfg, inv)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("rendering %s: %w", refName(pr.Branch), err))
	}

	// Chart-diff and consumer annotation both want a worktree; the head is
	// the one under judgement.
	beforeOb, afterOb, warns := gate.ChartDiff(head, cfg, baseTable, headTable)
	baseTable.Objects = append(baseTable.Objects, beforeOb...)
	headTable.Objects = append(headTable.Objects, afterOb...)
	baseTable.Warnings = append(baseTable.Warnings, warns...)

	res := gate.Diff(baseTable, headTable)
	gate.AnnotateConsumers(res, head)

	var report strings.Builder
	res.Report(&report)

	schemaFailures := 0
	if cfg.Validate.Enabled {
		var sb strings.Builder
		schemaFailures, err = gate.ValidateManifests(head, cfg, inv, &sb)
		if err != nil {
			return g.broke(ctx, pr, fmt.Errorf("schema validation: %w", err))
		}
		if schemaFailures > 0 {
			fmt.Fprintf(&report, "### Schema validation\n\n%s\n", sb.String())
		}
	}

	out := &gateOutcome{Report: report.String()}
	blocking := res.Blocking() || schemaFailures > 0

	// The comment is for humans and for the audit trail; the verdict no
	// longer travels through it. Posted when there is something to read --
	// a change, or a blocking finding -- and never twice for one commit,
	// which the head line makes checkable across restarts.
	if blocking || !strings.Contains(out.Report, gateSaidNothingChanged) {
		g.comment(ctx, pr, out.Report)
	}

	if blocking {
		out.State = gitprovider.CheckFailure
		switch {
		case schemaFailures > 0 && res.Blocking():
			g.status(ctx, pr, gitprovider.StateFailure, "%d targeting change(s), %d manifest(s) failed schema validation",
				len(res.Targeting)+len(res.Other), schemaFailures)
		case schemaFailures > 0:
			g.status(ctx, pr, gitprovider.StateFailure, "%d manifest(s) failed schema validation", schemaFailures)
		default:
			g.status(ctx, pr, gitprovider.StateFailure, "%d targeting change(s), %d other source change(s)",
				len(res.Targeting), len(res.Other))
		}
		return out
	}

	out.State = gitprovider.CheckSuccess
	if strings.Contains(out.Report, gateSaidNothingChanged) {
		g.status(ctx, pr, gitprovider.StateSuccess, "no change to what gets deployed")
	} else {
		g.status(ctx, pr, gitprovider.StateSuccess, "no targeting change; %d version change(s)", len(res.Versions))
	}
	return out
}

// broke reports the gate failing to run -- error, never failure, for the
// reason exit 2 is not exit 1: "this change is bad" and "the gate is broken"
// want opposite reactions, and a status that shows them identically teaches
// people to ignore the check.
func (g *GateService) broke(ctx context.Context, pr *gitprovider.PullRequest, err error) *gateOutcome {
	g.status(ctx, pr, gitprovider.StateError, "the gate could not run: %v", err)
	return &gateOutcome{Err: err}
}

func (g *GateService) status(ctx context.Context, pr *gitprovider.PullRequest, state gitprovider.CommitState, format string, a ...any) {
	desc := fmt.Sprintf(format, a...)
	g.logf("gate: PR %d %s: %s (%s)", pr.Number, shortSHA(pr.HeadSHA), state, desc)
	if err := g.Git.SetCommitStatus(ctx, pr.HeadSHA, g.CheckName, state, desc); err != nil {
		g.logf("gate: PR %d: could not set the %q status: %v", pr.Number, g.CheckName, err)
	}
}

// comment posts the report, stamped with the head commit so a restarted pod
// can see it already said this and stay quiet.
func (g *GateService) comment(ctx context.Context, pr *gitprovider.PullRequest, report string) {
	headLine := fmt.Sprintf("<!-- gitops-gate:head %s -->", pr.HeadSHA)
	if comments, err := g.Git.ListComments(ctx, pr.Number); err == nil {
		for _, c := range comments {
			if strings.Contains(c.Body, headLine) {
				return
			}
		}
	}
	body := strings.Replace(report, gate.ReportMarker+"\n", gate.ReportMarker+"\n"+headLine+"\n", 1)
	if err := g.Git.Comment(ctx, pr.Number, body); err != nil {
		g.logf("gate: PR %d: could not post the report: %v", pr.Number, err)
	}
}

// checkout clones the head branch shallowly and adds a worktree at the base
// branch's current tip -- which is what a merge would actually land on. The
// base is fetched by NAME, not by SHA: hosts reliably serve their advertised
// refs, and `github.event.pull_request.base.sha` was only ever CI's
// approximation of the same thing.
func (g *GateService) checkout(ctx context.Context, pr *gitprovider.PullRequest) (string, string, func(), error) {
	dir, err := os.MkdirTemp(g.CloneRoot, "gate")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	head := filepath.Join(dir, "head")
	base := filepath.Join(dir, "base")

	baseRef := pr.BaseBranch
	if baseRef == "" {
		baseRef = pr.BaseSHA
	}

	for _, cmd := range [][]string{
		{"git", "clone", "--quiet", "--depth", "1", "--branch", pr.Branch, g.RepoURL, head},
		{"git", "-C", head, "fetch", "--quiet", "--depth", "1", "origin", baseRef},
		{"git", "-C", head, "worktree", "add", "--quiet", "--detach", base, "FETCH_HEAD"},
	} {
		c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
		var out strings.Builder
		c.Stderr = &out
		if err := c.Run(); err != nil {
			cleanup()
			return "", "", func() {}, fmt.Errorf("%s: %w: %s", strings.Join(cmd[:2], " "), err, out.String())
		}
	}
	return base, head, cleanup, nil
}

func refName(s string) string {
	if s == "" {
		return "the base"
	}
	return s
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
