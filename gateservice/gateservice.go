// Package gateservice runs the gate inside this process, on a timer, for every
// open pull request.
//
// Its own package because it needs gitprovider, which gate deliberately does
// not import, so this cannot live there, and because the agent needs only one
// thing from it: a verdict for a head commit. Everything else here (the sweep,
// the per-SHA cache, the retry window, the comment and its verdict history) is
// how that verdict gets produced and published, and is nobody else's business.
package gateservice

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// Service is the gate, run by the agent.
//
// It used to run in CI, because ADR 0002 said "CI is where the checkout
// already is", and then the agent grew its own checkout, its own commit
// statuses, its own poll loop and live cluster access, at which point that
// shape, the checked-in cluster inventory it existed to work around, and the
// report-comment contract for reading the verdict back were all vestige. See
// ADR 0008.
//
// The loop is the same one the agent already runs for check states: poll the
// open pull requests, and for every head commit that has no verdict, render
// the repository at the base and the head, diff, and publish, a commit
// status for branch protection, and a report comment for humans. The verdict
// itself stays in memory, where the triage reads it as a value.
//
// There is deliberately no relevance filter. A paths filter is what CI needed,
// because a skipped required check bricks the pull request and CI minutes are
// billed; in here the status is always posted, so the safe answer to "is this
// change relevant?" is to render it and let the diff say "no change to what
// gets deployed". A docs-only pull request costs one render and gets a truthful
// green instead of a guessed one.
type Service struct {
	Git gitprovider.Provider
	// Inventory reads the live cluster inventory. In production this is
	// cluster.ArgoCD.ClusterInventory; there is no snapshot fallback in here,
	// because a snapshot can go stale and this service can look.
	Inventory func(ctx context.Context) (*gate.Inventory, error)
	// Derive reads what ArgoCD says this repository deploys: the sources to
	// render, and the ApplicationSets nothing in ArgoCD created. In
	// production this is cluster.ArgoCD.Derive.
	//
	// Nil means "do not derive", and the config file is then the whole scope,
	// exactly as it was before ADR 0012. A refused or unreachable read is an
	// error rather than an empty derivation, for the same reason an unreadable
	// inventory is: a smaller world produces a confident "no change" in it.
	Derive func(ctx context.Context, repoURL string) (*gate.Derivation, error)
	// CheckName is the commit-status context branch protection requires;
	// the same name the gate reported under when it ran in CI, so moving it
	// in-cluster changed nothing about protection rules.
	CheckName string
	Remote    gitprovider.Remote
	CloneRoot string
	// ForkPRs renders pull requests whose head branch lives in another
	// repository. Off by default: the render runs helm over the pull
	// request's content, inside the cluster, and that is a trust decision an
	// operator makes, not a default.
	ForkPRs bool
	Poll    time.Duration
	// Timeout bounds one gate run. A hung chart render must not wedge the
	// sweep forever.
	Timeout time.Duration
	Log     func(string, ...any)

	// Concurrency caps parallel renders, and is the host's answer rather than
	// the gated repository's.
	//
	// Same reasoning as Egress, and the same shape: the renders run in this
	// pod, against this pod's memory limit, beside every other open pull
	// request's. How hard to work is the operator's decision about their own
	// cluster. Zero leaves the config file's value in force, which is what
	// keeps an install that set it working; non-zero wins outright.
	Concurrency int

	// Validate is the operator's schema-validation policy, for the same
	// reason. Every field is optional, and an unset one leaves the config
	// file's value alone: an install that had `validate:` in its file keeps
	// exactly what it had until somebody sets the value.
	Validate ValidatePolicy

	// Egress is the operator's outbound deny-list. The gate pulls remote
	// charts to render them, and helm is a subprocess the egress transport
	// cannot see inside, so the policy has to reach the gate explicitly or
	// the default deployment is outside a control the start-up banner says
	// covers every outbound request.
	Egress gate.EgressPolicy

	// Checkout produces the two working copies one run compares, the commits
	// they hold, and a function that discards both. Defaults to a shallow
	// clone plus a worktree at the merge base; tests substitute directories
	// on disk.
	Checkout func(ctx context.Context, pr *gitprovider.PullRequest) (*Compared, error)

	mu       sync.Mutex
	results  map[string]*Outcome
	inflight map[string]chan struct{}

	// What the last sweep saw, for Status. Written at the end of each sweep,
	// read by the status page; see status.go.
	sweptAt  time.Time
	sweepErr string
	lastOpen []PRStatus

	// What the last live reading of ArgoCD served, for Status. Written by a
	// RUN rather than by the sweep, because that is where the reading happens;
	// see retainFleet in status.go.
	fleet *Fleet

	// What the last run's render expanded this repository into, for Status.
	// Written by a RUN for the same reason the reading is, and stamped
	// separately from it: the two are made at different moments of one run and
	// a reader joining them has to be able to see which is older. See
	// retainExpansion in status.go.
	expansion *Expansion

	// history is the verdicts each pull request's own comment recorded, as
	// the last publish onto it read them. Keyed by pull-request number,
	// oldest verdict first, dropped when the pull request stops being open.
	//
	// Kept rather than recomputed, because the publish path already parses
	// exactly this out of the existing comment on every run and then throws
	// the parse away. Recomputing it would mean a second read of the git host
	// on a path that is not allowed to make one.
	//
	// A key with no rows behind it is a comment that was read and recorded no
	// earlier verdict; no key at all is a pull request whose comment this
	// process has not read. The two are different answers and the read
	// surfaces publish them as different answers, so the map's own
	// key-present is the distinction rather than a length.
	history map[int][]VerdictRow
}

// ValidatePolicy is the host's schema-validation settings, each optional.
//
// Pointers rather than plain values because "false" and "not set" are
// different answers here. A chart that defaulted `enabled` to false would turn
// validation off for every install that had switched it on in its own file,
// and the only symptom would be a report no longer mentioning schemas.
type ValidatePolicy struct {
	Enabled              *bool
	IgnoreMissingSchemas *bool
	SchemaLocations      []string
	SkipKinds            []string
}

// applyHostPolicy overlays the operator's settings onto the config the
// repository supplied.
//
// The direction is the point. The gated repository is the thing under
// judgement, so anything it can say about how hard the gate works or what it
// checks is a request, and the host has the last word. Nothing here widens
// what the repository asked for by accident: an unset field is left alone.
func (g *Service) applyHostPolicy(cfg *gate.Config) {
	if g.Concurrency > 0 {
		cfg.Concurrency = g.Concurrency
	}
	if g.Validate.Enabled != nil {
		cfg.Validate.Enabled = *g.Validate.Enabled
	}
	if g.Validate.IgnoreMissingSchemas != nil {
		cfg.Validate.IgnoreMissingSchemas = *g.Validate.IgnoreMissingSchemas
	}
	if len(g.Validate.SchemaLocations) > 0 {
		cfg.Validate.SchemaLocations = g.Validate.SchemaLocations
	}
	if len(g.Validate.SkipKinds) > 0 {
		cfg.Validate.SkipKinds = g.Validate.SkipKinds
	}
}

// Outcome is one head commit's verdict, kept so the triage can read it
// in-process and a sweep never runs the same commit twice.
type Outcome struct {
	// State is CheckSuccess or CheckFailure. Zero when Err is set.
	State gitprovider.CheckState
	// Report is the same markdown the comment carries, marker first,
	// everything downstream of the triage parses evidence out of this string,
	// and it is now handed over instead of scraped back.
	Report string
	// Verdict is the whole answer as data: the headline, the counted
	// breakdown, every finding behind it, and what the run could not look at.
	// Nil when the gate did not reach a verdict, which is exactly when Err is
	// set.
	//
	// Held for the same reason Unrenderable is, one rung further out. The
	// report is prose and half of it is strings a chart chose; this is the
	// value a read surface publishes to another agent, so it never goes
	// through markdown and a chart cannot spell one. The DiffResult itself is
	// deliberately not kept: it carries every rendered object's field diff,
	// for every open pull request, to answer questions that are all answered
	// by the summary.
	Verdict *gate.Summary
	// Unrenderable is the repair contract for the Applications this run could
	// not render at the version the head moves them to.
	//
	// A value rather than a line in the report, and the only piece of evidence
	// that travels this way. Everything else downstream parses out of Report
	// because that is what a gate old enough to predate a reader still
	// carries; this is new on both sides at once, and ADR 0008 already bought
	// the ability to hand a verdict over instead of scraping it back. It also
	// means a chart cannot spell one: nothing here passed through markdown.
	Unrenderable []gate.Unrenderable
	// Err is the gate failing to run, which is a different thing from a
	// failing verdict, exit 2, not exit 1.
	Err error
	// retryAfter is when a broken run may be attempted again. Zero means
	// never: a verdict is final, and so is a refusal to gate fork content.
	retryAfter time.Time
}

// spent reports whether a broken run has waited long enough to be worth
// another attempt.
//
// A verdict is kept forever; it answers a commit, and the commit does not
// change. A failure to run is different: its cause is usually cluster-side,
// RBAC not granted yet, a chart repository briefly unreachable, an apiserver
// mid-upgrade, and the fix for those is not a commit. Cached forever, the
// `error` status would outlive its own cause and clear only when somebody
// pushed to the pull request, which is exactly the quiet trap this service
// exists to remove. Retried immediately, a broken config would
// re-render on every poll for as long as the pull request stays open.
func (o *Outcome) spent() bool {
	return o.Err != nil && !o.retryAfter.IsZero() && time.Now().After(o.retryAfter)
}

// gateRetryAfter is how long a broken gate waits before trying again. Long
// enough that a permanent breakage is not a busy loop, short enough that
// fixing the cause does not need a push to take effect.
const gateRetryAfter = 5 * time.Minute

func (g *Service) logf(f string, a ...any) {
	if g.Log != nil {
		g.Log(f, a...)
	}
}

func (g *Service) timeout() time.Duration {
	if g.Timeout > 0 {
		return g.Timeout
	}
	return 15 * time.Minute
}

// Run polls until the context ends. One sweep at a time, pull requests in
// sequence: a gate run shells helm with its own concurrency, and stacking
// renders on top of each other buys wall-clock nothing a poll interval
// doesn't.
func (g *Service) Run(ctx context.Context) {
	for {
		g.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(g.Poll):
		}
	}
}

func (g *Service) sweep(ctx context.Context) {
	prs, err := g.Git.ListOpenPullRequests(ctx)
	if err != nil {
		g.logf("gate: listing open pull requests: %v", err)
		// Recorded, not only logged: a gate that cannot list has a status
		// page reading "nothing open" forever, and that page's whole subject
		// is the difference between "nothing is wrong" and "nobody looked".
		g.mu.Lock()
		g.sweptAt, g.sweepErr = time.Now(), err.Error()
		g.mu.Unlock()
		return
	}

	open := map[string]bool{}
	// The same question asked of the pull request rather than of its head
	// commit, because the verdict history is a pull request's memory and
	// survives every push to it. A commit key would drop the history on the
	// push that most wants it read back.
	openNumbers := map[int]bool{}
	// Verdicts already standing on the host, kept so the status snapshot can
	// say what they were rather than shrugging about the commits this sweep
	// deliberately did not re-litigate.
	posted := map[string]gitprovider.CheckState{}
	for i := range prs {
		pr := &prs[i]
		open[pr.HeadSHA] = true
		openNumbers[pr.Number] = true

		if g.known(pr.HeadSHA) {
			continue
		}
		// A verdict that already stands, from a previous life of this pod, is
		// not re-litigated. The triage re-renders on demand if it needs the
		// report; the sweep's job is only that every head commit gets one.
		state, err := g.Git.CheckStatus(ctx, pr.HeadSHA, g.CheckName)
		switch {
		case err != nil:
			// Re-gating is the safe answer, a verdict we could not read is
			// not a verdict we may assume, but it is not free, so say why.
			// Silently, a revoked Checks:read read as "nothing has run yet"
			// on every sweep, for every pull request, forever.
			g.logf("gate: PR %d: could not read the %q status, re-gating: %v", pr.Number, g.CheckName, err)
		case state == gitprovider.CheckSuccess || state == gitprovider.CheckFailure:
			posted[pr.HeadSHA] = state
			continue
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
	// And the same for the histories, which are a slow leak in the same way:
	// a merged pull request's memory is on the git host, in the comment it was
	// read from, and nothing here will ever be asked about it again.
	for number := range g.history {
		if !openNumbers[number] {
			delete(g.history, number)
		}
	}
	g.sweptAt, g.sweepErr = time.Now(), ""
	g.lastOpen = g.snapshotLocked(prs, posted)
	g.mu.Unlock()
}

func (g *Service) known(sha string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, running := g.inflight[sha]; running {
		return true
	}
	out, done := g.results[sha]
	return done && !out.spent()
}

// rememberHistory records what one pull request's comment said before this
// run rewrote it, or forgets what an earlier run recorded when this one could
// not read the comment at all.
//
// One writer for both, so the refused-read case cannot be the one somebody
// forgets: a stale history kept across a failed read says "these are the
// earlier verdicts" while the verdict being published right now is missing
// from it.
//
// Called from the publish path, which does not hold the lock, so it takes it
// like every other writer of the snapshot's inputs does.
func (g *Service) rememberHistory(number int, rows []VerdictRow, read bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !read {
		delete(g.history, number)
		return
	}
	if g.history == nil {
		g.history = map[int][]VerdictRow{}
	}
	g.history[number] = append([]VerdictRow(nil), rows...)
}

func (g *Service) store(sha string, out *Outcome) {
	g.mu.Lock()
	if g.results == nil {
		g.results = map[string]*Outcome{}
	}
	g.results[sha] = out
	g.mu.Unlock()
}

// Ensure returns the verdict for the pull request's head commit, running the
// gate if no run has answered for it yet. Concurrent callers for the same
// commit share one run, the sweep and a Kargo-triggered triage arriving
// together must not render twice and comment twice.
func (g *Service) Ensure(ctx context.Context, pr *gitprovider.PullRequest) *Outcome {
	g.mu.Lock()
	if out, ok := g.results[pr.HeadSHA]; ok {
		if !out.spent() {
			g.mu.Unlock()
			return out
		}
		// A broken run that has waited out its delay gets another attempt.
		delete(g.results, pr.HeadSHA)
	}
	if ch, ok := g.inflight[pr.HeadSHA]; ok {
		g.mu.Unlock()
		select {
		case <-ch:
			g.mu.Lock()
			out := g.results[pr.HeadSHA]
			g.mu.Unlock()
			if out == nil {
				return &Outcome{Err: fmt.Errorf("the gate run for %s was abandoned", pr.HeadSHA)}
			}
			return out
		case <-ctx.Done():
			return &Outcome{Err: ctx.Err()}
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
		g.results = map[string]*Outcome{}
	}
	g.results[pr.HeadSHA] = out
	delete(g.inflight, pr.HeadSHA)
	g.mu.Unlock()
	close(ch)
	return out
}

// run is one gate run: the same render, diff and validation the CLI performs,
// against the live inventory, published as a status and a report comment.
func (g *Service) run(ctx context.Context, pr *gitprovider.PullRequest) *Outcome {
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()

	// Here rather than in the sweep, because the sweep is not the only way in.
	// Ensure is called directly by the triage on a network-triggered promotion,
	// and a fork pull request the sweep had not reached yet would have been
	// rendered, with helm, in the cluster, over content somebody outside the
	// repository controls. That is the trust decision gate.forkPRs exists to
	// make, and it belongs on the path that does the work.
	if pr.FromFork && !g.ForkPRs {
		// An unreported required check blocks the merge with no explanation,
		// the same trap a CI paths filter used to spring. Error, with the
		// reason, says why and how to decide otherwise.
		g.status(ctx, pr, gitprovider.StateError,
			"not gated: fork pull request (gate.forkPRs renders fork content in-cluster)")
		return &Outcome{Err: fmt.Errorf("fork pull request")}
	}

	g.status(ctx, pr, gitprovider.StatePending, "rendering %s and %s", refName(pr.BaseBranch), refName(pr.Branch))

	inv, err := g.Inventory(ctx)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("reading the cluster inventory: %w", err))
	}

	checkout := g.Checkout
	if checkout == nil {
		checkout = g.checkout
	}
	cmp, err := checkout(ctx, pr)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("checking out %s and %s: %w", refName(pr.BaseBranch), refName(pr.Branch), err))
	}
	defer cmp.Cleanup()
	base, head := cmp.Base, cmp.Head

	// The config comes from the HEAD at both revisions. It describes how to
	// render, not what to render, and the base may predate it entirely,
	// notably on the pull request that introduces the gate. Since ADR 0012 it
	// is also optional: ArgoCD says what this repository deploys, and most
	// repositories need no file at all.
	fileCfg, cfgName, err := readConfig(head)
	if err != nil {
		return g.broke(ctx, pr, err)
	}

	// Derivation is a live read, and it refuses like the inventory does. An
	// ArgoCD that cannot be read produces a smaller scope, not a poorer one,
	// and a smaller scope reports no change with total confidence.
	var derived *gate.Derivation
	if g.Derive != nil {
		// Stamped before the call rather than after it. The reading is only
		// as good as the moment it started -- ArgoCD may change while it is
		// being read -- so a timestamp taken on return overstates how fresh
		// the rows are by however long the read took, in the one field a
		// caller uses to decide whether to trust them.
		readAt := time.Now()
		derived, err = g.Derive(ctx, g.Remote.URL())
		if err != nil {
			return g.broke(ctx, pr, fmt.Errorf("deriving what this repository deploys: %w", err))
		}
		// Kept, rather than used and dropped. The reading says what the whole
		// control plane runs and where, this run needs a fraction of it to
		// decide what to render, and every other reader of that fact was
		// paying a cluster credential for it.
		//
		// Retained here rather than after the render, because the reading is
		// what succeeded: a chart that will not render says nothing about
		// whether ArgoCD answered.
		g.retainFleet(derived, inv, readAt)
	}

	p, err := buildPlan(head, fileCfg, cfgName, derived)
	if err != nil {
		return g.broke(ctx, pr, err)
	}
	cfg := p.cfg

	// The host's policy, attached after parsing so a pull request cannot widen
	// its own. helm is a subprocess and the egress transport cannot see inside
	// it, so without this the in-cluster gate, the default deployment, pulled
	// remote charts with no policy check and no log line, while the start-up
	// banner promised otherwise.
	cfg.Egress = g.Egress
	cfg.Log = g.Log
	g.applyHostPolicy(cfg)

	// Stamped before the render rather than after it, for the reason the
	// reading above is: what a render describes is the world as it was when
	// the render started, and a timestamp taken on return overstates how fresh
	// it is by however long the render took.
	renderedAt := time.Now()
	baseTable, err := gate.Render(ctx, base, cfg, inv)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("rendering %s: %w", refName(pr.BaseBranch), err))
	}
	// Kept, rather than rendered and dropped. This is the only thing in the
	// process that knows which chart an Application renders from, and the
	// reader asking that is the one the live reading already half answers.
	//
	// The BASE expansion, which is the revision this run started from and
	// therefore the one the fleet is running. The head is the change under
	// judgement, and nothing has deployed it.
	g.retainExpansion(baseTable, renderedAt)
	headTable, err := gate.Render(ctx, head, cfg, inv)
	if err != nil {
		return g.broke(ctx, pr, fmt.Errorf("rendering %s: %w", refName(pr.Branch), err))
	}

	// Both worktrees: consumer annotation asks the head which manifests still
	// declare a dropped version, and chart-diff renders each side from its
	// own, which is what makes a values edit visible at all.
	// gate.Assemble owns the order of the four steps
	// so this surface and the CLI cannot reach different verdicts on one
	// commit, which they had already started to do.
	res := gate.Assemble(ctx, cmp.Worktrees, cfg, baseTable, headTable)
	res.Suppressed = suppressedChecks(base, head, cfg, cfgName)
	res.Scope = p.scope
	// Which two revisions this is the difference between, on the report and
	// not only in this process. The head SHA was already stamped; the base
	// was not recorded anywhere at all, and it is the one a wrong answer
	// hides in.
	res.BaseRev, res.HeadRev = shortSHA8(cmp.BaseRev), shortSHA8(cmp.HeadRev)

	// Validation runs before the report is written, and its count goes onto
	// the result rather than beside it. Written after, the headline and the
	// machine-readable marker were already on the page by the time the failure
	// was known: a run blocked only by schema validation published "✅ No
	// blocking findings" and an all-zero blockers marker next to a failure
	// status, and the triage agent read the marker.
	var schemaDetail strings.Builder
	if cfg.Validate.Enabled {
		res.Schema, err = gate.ValidateManifests(ctx, head, cfg, inv, &schemaDetail)
		if err != nil {
			return g.broke(ctx, pr, fmt.Errorf("schema validation: %w", err))
		}
	}

	var report strings.Builder
	res.Report(&report)
	if len(res.Schema) > 0 {
		fmt.Fprintf(&report, "### Schema validation\n\n%s\n", schemaDetail.String())
	}

	out := &Outcome{Report: report.String(), Unrenderable: res.Unrenderable, Verdict: res.Summarise()}
	blocking := res.Blocking()

	// The comment is for humans and for the audit trail; the verdict no
	// longer travels through it. Posted when there is something to read, a
	// change, or a blocking finding, and never twice for one commit, which
	// the head line makes checkable across restarts.
	if blocking || !gate.SaysNothingChanged(out.Report) {
		g.comment(ctx, pr, out.Report)
	}

	if blocking {
		out.State = gitprovider.CheckFailure
		// The status says what the report says. It used to count only
		// targeting and source changes, so a pull request blocked for any
		// other reason, an apiVersion that moved, settings the bump stops
		// reading, got "0 targeting change(s), 0 other source change(s)"
		// beside a red cross. That is the most-read surface on the pull
		// request telling the reader nothing changed, on the one occasion it
		// most needed to say what did. Verdict now counts schema failures
		// itself, so the status, the report headline and the blockers marker
		// are three renderings of one answer rather than three places to keep
		// in step.
		_, headline := res.Verdict()
		g.status(ctx, pr, gitprovider.StateFailure, "%s", strings.TrimPrefix(headline, "Blocking — "))
		return out
	}

	out.State = gitprovider.CheckSuccess
	if gate.SaysNothingChanged(out.Report) {
		g.status(ctx, pr, gitprovider.StateSuccess, "no change to what gets deployed")
	} else {
		g.status(ctx, pr, gitprovider.StateSuccess, "no targeting change; %d version change(s)", len(res.Versions))
	}
	return out
}

// broke reports the gate failing to run, error, never failure, for the
// reason exit 2 is not exit 1: "this change is bad" and "the gate is broken"
// want opposite reactions, and a status that shows them identically teaches
// people to ignore the check.
func (g *Service) broke(ctx context.Context, pr *gitprovider.PullRequest, err error) *Outcome {
	g.status(ctx, pr, gitprovider.StateError, "the gate could not run: %v", err)
	return &Outcome{Err: err, retryAfter: time.Now().Add(gateRetryAfter)}
}

func (g *Service) status(ctx context.Context, pr *gitprovider.PullRequest, state gitprovider.CommitState, format string, a ...any) {
	desc := fmt.Sprintf(format, a...)
	g.logf("gate: PR %d %s: %s (%s)", pr.Number, shortSHA8(pr.HeadSHA), state, desc)
	if err := g.Git.SetCommitStatus(ctx, pr.HeadSHA, g.CheckName, state, desc); err != nil {
		g.logf("gate: PR %d: could not set the %q status: %v", pr.Number, g.CheckName, err)
	}
}

// comment publishes the report as one comment per pull request, rewritten in
// place on every run, carrying the verdicts that came before it.
//
// It used to post a fresh comment per head commit. A pull request the agent
// repaired therefore ended up with two twenty-thousand-character reports that
// differed only in their verdict, and since neither stated a verdict, the
// failed pass read as a duplicate of the pass that succeeded rather than as
// the thing that had to be fixed.
//
// Editing in place alone would be worse: it would delete the failed pass. So
// the body carries a compact history, which is the part a reviewer wants;
// what was wrong, and that it is not wrong any more.
func (g *Service) comment(ctx context.Context, pr *gitprovider.PullRequest, report string) {
	isBlocking, headline := g.lastVerdict(report)
	blocking := boolDigit(isBlocking)

	// Two different questions, deliberately answered by two different scans.
	//
	// "Has this already been said?" is about the commit and must consider every
	// author: a previous life of this pod counts, and it is not necessarily
	// recognisable as us.
	//
	// "Which comment may I rewrite?" is about ownership, a host lets an author
	// edit only its own comments, so that one is filtered by author, and the two
	// must not be collapsed into one loop however similar they look.
	var existing *gitprovider.Comment
	var history []VerdictRow
	comments, err := g.Git.ListComments(ctx, pr.Number)
	if err != nil {
		// Both scans below are how "never comment twice for one commit" and
		// "edit our own comment rather than adding another" are enforced, and
		// an empty list defeats both. Posting a second report is the lesser
		// harm, the alternative is a commit with no verdict on it at all, but
		// it is a real one, and it used to happen with nothing said.
		g.logf("gate: PR %d: could not read the existing comments, so this report may duplicate one and will not carry the verdict history: %v",
			pr.Number, err)
	}
	for i := range comments {
		if strings.Contains(comments[i].Body, StampHead+pr.HeadSHA+" -->") {
			return
		}
	}
	// Ours is the one carrying the verdict stamp, which only this code path
	// writes. Not the one whose author matches, Name() is the provider's name
	// ("github"), never the account, and a report somebody else posted is not
	// ours to rewrite anyway. Matching on the stamp is the same question asked
	// of the artefact instead of the identity, and it is the question that has
	// an answer here.
	for i := range comments {
		if strings.Contains(comments[i].Body, StampVerdict) {
			existing = &comments[i]
		}
	}
	if existing != nil {
		history = append(parseHistory(existing.Body), currentAsRow(existing.Body)...)
		if len(history) > MaxHistory {
			history = history[len(history)-MaxHistory:]
		}
	}
	// Kept, not dropped. This parse is the only account anything in this
	// process has of what the gate said before now, and until this line it was
	// computed on every run and discarded at the end of the function.
	//
	// Whether the read succeeded travels with it rather than being inferred
	// from the rows, because a listing that failed produces the same empty
	// parse a pull request with no gate comment produces. Recording the first
	// as the second would publish "no earlier verdicts" for a pull request
	// whose comment nothing managed to open -- and a refused read also
	// invalidates whatever an earlier one saw, since the verdict this run is
	// about to publish is now one of the earlier ones.
	g.rememberHistory(pr.Number, history, err == nil)

	var stamps strings.Builder
	fmt.Fprintf(&stamps, "%s%s -->\n", StampHead, pr.HeadSHA)
	fmt.Fprintf(&stamps, "%s%s %s -->\n", StampVerdict, blocking, headline)
	for _, h := range history {
		fmt.Fprintf(&stamps, "%s%s %s %s -->\n", StampWas, h.SHA, boolDigit(h.Blocking), h.Headline)
	}

	body := strings.Replace(report, gate.ReportMarker+"\n",
		gate.ReportMarker+"\n"+stamps.String(), 1) + renderHistory(history)

	if existing != nil {
		if err := g.Git.UpdateComment(ctx, existing.ID, body); err == nil {
			return
		} else {
			// Falling through to a new comment is deliberate: a report nobody
			// can read is worse than a duplicate one.
			g.logf("gate: PR %d: could not rewrite the report, posting a new one: %v", pr.Number, err)
		}
	}
	if err := g.Git.Comment(ctx, pr.Number, body); err != nil {
		g.logf("gate: PR %d: could not post the report: %v", pr.Number, err)
	}
}

// lastVerdict reads the headline the report already rendered, rather than
// recomputing it: one source of truth means the stamp and the visible headline
// can never disagree.
func (g *Service) lastVerdict(report string) (bool, string) {
	for _, line := range strings.Split(report, "\n") {
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		switch {
		case strings.HasPrefix(text, "🔴 "):
			return true, strings.TrimSpace(strings.TrimPrefix(text, "🔴 "))
		case strings.HasPrefix(text, "✅ "):
			return false, strings.TrimSpace(strings.TrimPrefix(text, "✅ "))
		}
	}
	return false, ""
}

func refName(s string) string {
	if s == "" {
		return "the base"
	}
	return s
}

// shortSHA8 is the eight-character form the gate's log lines use. The length
// is in the name because upstream has a twelve-character shortSHA12, and a
// reader who learns one of them must not mispredict the other.
func shortSHA8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// suppressedChecks names the checks this pull request's own configuration
// turned off.
//
// The gate reads.gitops-gate.yaml from the HEAD, which is the right rule,
// the config describes how to render, not what to render, and the base may
// predate it entirely, notably on the pull request that introduces the gate.
// It also means a change can switch a check off, in a file the agent is
// forbidden to edit and about which the report previously said nothing.
//
// So the rule stays and the suppression becomes visible, which is what the
// project's own "cannot act without saying so" line requires.
func suppressedChecks(base, head string, cfg *gate.Config, name string) []gate.Markdown {
	var out []gate.Markdown
	if !cfg.Validate.Enabled {
		out = append(out, "**Schema validation** — `validate.enabled` is false, so no rendered manifest "+
			"was checked against the target cluster's schemas.")
	}
	if n := len(cfg.Validate.SkipKinds); n > 0 {
		// The kinds are the config file's words, not the gate's, so each one
		// is neutralised before it sits inside the gate's own backticks.
		escaped := make([]string, n)
		for i, k := range cfg.Validate.SkipKinds {
			escaped[i] = gate.Inline(k)
		}
		out = append(out, gate.Markdown(fmt.Sprintf("**Schema validation** skipped %s entirely (`validate.skipKinds`): `%s`.",
			plural(n, "kind"), strings.Join(escaped, "`, `"))))
	}

	// Whether the config itself moved in this pull request. Read as bytes
	// rather than compared field by field: any difference is worth one line,
	// and a reader with the line can go and look.
	//
	// Both filenames are checked, not just the one in force at head: a pull
	// request that renames `.gitops-gate.yaml` to `.bosun.yaml` changes the
	// configuration in exactly the way this line exists to report, and
	// comparing one name against itself would find nothing on either side.
	headRaw, headErr := anyConfigBytes(head)
	baseRaw, baseErr := anyConfigBytes(base)
	shown := name
	if shown == "" {
		shown = configNames[0]
	}
	switch {
	case headErr != nil:
		// No file at head. The scope was derived, and there is nothing this
		// pull request could have turned off in a file it does not have.
	case baseErr != nil:
		out = append(out, gate.Markdown(fmt.Sprintf("**This pull request introduces `%s`.** Everything above was "+
			"checked under a configuration that did not exist on the base branch.", gate.Inline(shown))))
	case !bytes.Equal(headRaw, baseRaw):
		out = append(out, gate.Markdown(fmt.Sprintf("**`%s` changed in this pull request**, and the gate read the "+
			"head revision's copy. Everything above was checked under the new configuration.", gate.Inline(shown))))
	}
	return out
}

// anyConfigBytes reads whichever config file a revision has.
//
// Deliberately indifferent to which name it found: the comparison above is
// "did this pull request change how the gate is configured", and a rename from
// one name to the other is a change of exactly that kind.
func anyConfigBytes(dir string) ([]byte, error) {
	var lastErr error
	for _, n := range configNames {
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err == nil {
			return raw, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// plural is "1 kind" / "3 kinds".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
