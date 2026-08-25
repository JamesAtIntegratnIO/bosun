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

	// Egress is the operator's outbound deny-list. The gate pulls remote
	// charts to render them, and helm is a subprocess the egress transport
	// cannot see inside -- so the policy has to reach the gate explicitly or
	// the default deployment is outside a control the start-up banner says
	// covers every outbound request.
	Egress gate.EgressPolicy

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
	// retryAfter is when a BROKEN run may be attempted again. Zero means
	// never: a verdict is final, and so is a refusal to gate fork content.
	retryAfter time.Time
}

// spent reports whether a broken run has waited long enough to be worth
// another attempt.
//
// A verdict is kept forever -- it answers a commit, and the commit does not
// change. A FAILURE TO RUN is different: its cause is usually cluster-side --
// RBAC not granted yet, a chart repository briefly unreachable, an apiserver
// mid-upgrade -- and the fix for those is not a commit. Cached forever, the
// `error` status would outlive its own cause and clear only when somebody
// pushed to the pull request, which is exactly the quiet trap this service
// exists to remove. Retried immediately, a genuinely broken config would
// re-render on every poll for as long as the pull request stays open.
func (o *gateOutcome) spent() bool {
	return o.Err != nil && !o.retryAfter.IsZero() && time.Now().After(o.retryAfter)
}

// gateRetryAfter is how long a broken gate waits before trying again. Long
// enough that a permanent breakage is not a busy loop, short enough that
// fixing the cause does not need a push to take effect.
const gateRetryAfter = 5 * time.Minute

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
		state, err := g.Git.CheckStatus(ctx, pr.HeadSHA, g.CheckName)
		switch {
		case err != nil:
			// Re-gating is the safe answer -- a verdict we could not read is
			// not a verdict we may assume -- but it is not free, so say why.
			// Silently, a revoked Checks:read read as "nothing has run yet"
			// on every sweep, for every pull request, forever.
			g.logf("gate: PR %d: could not read the %q status, re-gating: %v", pr.Number, g.CheckName, err)
		case state == gitprovider.CheckSuccess || state == gitprovider.CheckFailure:
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
	g.mu.Unlock()
}

func (g *GateService) known(sha string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, running := g.inflight[sha]; running {
		return true
	}
	out, done := g.results[sha]
	return done && !out.spent()
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
	if err == nil {
		// The HOST's egress policy, attached after parsing so a pull request
		// cannot widen its own. helm is a subprocess and the egress transport
		// cannot see inside it, so without this the in-cluster gate -- the
		// DEFAULT deployment -- pulled remote charts with no policy check and
		// no log line, while the start-up banner promised otherwise.
		cfg.Egress = g.Egress
		cfg.Log = g.Log
	}
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
	// the one under judgement. gate.Assemble owns the order of the four steps
	// so this surface and the CLI cannot reach different verdicts on one
	// commit -- which they had already started to do.
	res := gate.Assemble(head, cfg, baseTable, headTable)

	// Validation runs BEFORE the report is written, and its count goes onto
	// the result rather than beside it. Written after, the headline and the
	// machine-readable marker were already on the page by the time the failure
	// was known: a run blocked only by schema validation published "✅ No
	// blocking findings" and an all-zero blockers marker next to a FAILURE
	// status, and the triage agent read the marker.
	var schemaDetail strings.Builder
	if cfg.Validate.Enabled {
		res.SchemaFailures, err = gate.ValidateManifests(head, cfg, inv, &schemaDetail)
		if err != nil {
			return g.broke(ctx, pr, fmt.Errorf("schema validation: %w", err))
		}
	}

	var report strings.Builder
	res.Report(&report)
	if res.SchemaFailures > 0 {
		fmt.Fprintf(&report, "### Schema validation\n\n%s\n", schemaDetail.String())
	}

	out := &gateOutcome{Report: report.String()}
	blocking := res.Blocking()

	// The comment is for humans and for the audit trail; the verdict no
	// longer travels through it. Posted when there is something to read --
	// a change, or a blocking finding -- and never twice for one commit,
	// which the head line makes checkable across restarts.
	if blocking || !gate.SaysNothingChanged(out.Report) {
		g.comment(ctx, pr, out.Report)
	}

	if blocking {
		out.State = gitprovider.CheckFailure
		// The status says what the report says. It used to count only
		// targeting and source changes, so a pull request blocked for any
		// OTHER reason -- an apiVersion that moved, settings the bump stops
		// reading -- got "0 targeting change(s), 0 other source change(s)"
		// beside a red cross. That is the most-read surface on the pull
		// request telling the reader nothing changed, on the one occasion it
		// most needed to say what did.
		// Verdict now counts schema failures itself, so the status, the
		// report headline and the blockers marker are three renderings of one
		// answer rather than three places to keep in step.
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

// broke reports the gate failing to run -- error, never failure, for the
// reason exit 2 is not exit 1: "this change is bad" and "the gate is broken"
// want opposite reactions, and a status that shows them identically teaches
// people to ignore the check.
func (g *GateService) broke(ctx context.Context, pr *gitprovider.PullRequest, err error) *gateOutcome {
	g.status(ctx, pr, gitprovider.StateError, "the gate could not run: %v", err)
	return &gateOutcome{Err: err, retryAfter: time.Now().Add(gateRetryAfter)}
}

func (g *GateService) status(ctx context.Context, pr *gitprovider.PullRequest, state gitprovider.CommitState, format string, a ...any) {
	desc := fmt.Sprintf(format, a...)
	g.logf("gate: PR %d %s: %s (%s)", pr.Number, shortSHA(pr.HeadSHA), state, desc)
	if err := g.Git.SetCommitStatus(ctx, pr.HeadSHA, g.CheckName, state, desc); err != nil {
		g.logf("gate: PR %d: could not set the %q status: %v", pr.Number, g.CheckName, err)
	}
}

// Stamps the gate leaves in its own comment so the next run can read what the
// last one said. HTML comments: invisible in every markdown surface, and the
// only per-pull-request memory a gate with no database has.
const (
	stampHead    = "<!-- gitops-gate:head "
	stampVerdict = "<!-- gitops-gate:verdict "
	stampWas     = "<!-- gitops-gate:was "
)

// maxHistory caps the remembered verdicts. Ten is far more than any pull
// request needs and stops a long-lived one growing a comment without bound.
const maxHistory = 10

// verdictRow is one past answer on this pull request.
type verdictRow struct {
	SHA      string
	Blocking bool
	Headline string
}

// comment publishes the report as ONE comment per pull request, rewritten in
// place on every run, carrying the verdicts that came before it.
//
// It used to post a fresh comment per head commit. A pull request the agent
// repaired therefore ended up with two twenty-thousand-character reports that
// differed only in their verdict -- and since neither stated a verdict, the
// failed pass read as a duplicate of the pass that succeeded rather than as
// the thing that had to be fixed.
//
// Editing in place alone would be worse: it would DELETE the failed pass. So
// the body carries a compact history, which is the part a reviewer actually
// wants -- what was wrong, and that it is not wrong any more.
func (g *GateService) comment(ctx context.Context, pr *gitprovider.PullRequest, report string) {
	blocking, headline := "0", ""
	if b, h := g.lastVerdict(report); b {
		blocking, headline = "1", h
	} else {
		headline = h
	}

	// Two different questions, deliberately answered by two different scans.
	//
	// "Has this already been said?" is about the COMMIT and must consider every
	// author: a previous life of this pod, or a CI adapter still running during
	// a migration, both count and neither is necessarily us.
	//
	// "Which comment may I rewrite?" is about OWNERSHIP -- a host lets an author
	// edit only its own comments -- so that one is filtered by author, and the
	// two must not be collapsed into one loop however similar they look.
	var existing *gitprovider.Comment
	var history []verdictRow
	comments, err := g.Git.ListComments(ctx, pr.Number)
	if err != nil {
		// Both scans below are how "never comment twice for one commit" and
		// "edit our own comment rather than adding another" are enforced, and
		// an empty list defeats both. Posting a second report is the lesser
		// harm -- the alternative is a commit with no verdict on it at all --
		// but it is a real one, and it used to happen with nothing said.
		g.logf("gate: PR %d: could not read the existing comments, so this report may duplicate one and will not carry the verdict history: %v",
			pr.Number, err)
	}
	for i := range comments {
		if strings.Contains(comments[i].Body, stampHead+pr.HeadSHA+" -->") {
			return
		}
	}
	// Ours is the one carrying the verdict stamp, which only this code path
	// writes. NOT the one whose author matches -- Name() is the PROVIDER's
	// name ("github"), never the account, and a report posted by a CI adapter
	// is not ours to rewrite anyway. Matching on the stamp is the same
	// question asked of the artefact instead of the identity, and it is the
	// question that has an answer here.
	for i := range comments {
		if strings.Contains(comments[i].Body, stampVerdict) {
			existing = &comments[i]
		}
	}
	if existing != nil {
		history = append(parseHistory(existing.Body), currentAsRow(existing.Body)...)
		if len(history) > maxHistory {
			history = history[len(history)-maxHistory:]
		}
	}

	var stamps strings.Builder
	fmt.Fprintf(&stamps, "%s%s -->\n", stampHead, pr.HeadSHA)
	fmt.Fprintf(&stamps, "%s%s %s -->\n", stampVerdict, blocking, headline)
	for _, h := range history {
		fmt.Fprintf(&stamps, "%s%s %s %s -->\n", stampWas, h.SHA, boolDigit(h.Blocking), h.Headline)
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
func (g *GateService) lastVerdict(report string) (bool, string) {
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

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// parseHistory reads the rows a previous body recorded.
func parseHistory(body string) []verdictRow {
	var out []verdictRow
	for _, line := range strings.Split(body, "\n") {
		if row, ok := parseStampedRow(line, stampWas, true); ok {
			out = append(out, row)
		}
	}
	return out
}

// currentAsRow turns the body's OWN verdict into a history row, which is what
// makes the failed pass survive being edited over.
func currentAsRow(body string) []verdictRow {
	var sha string
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := trimStamp(line, stampHead); ok {
			sha = strings.TrimSpace(rest)
			break
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if row, ok := parseStampedRow(line, stampVerdict, false); ok {
			row.SHA = sha
			if row.SHA == "" {
				return nil
			}
			return []verdictRow{row}
		}
	}
	return nil
}

func trimStamp(line, stamp string) (string, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, stamp) || !strings.HasSuffix(line, "-->") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(line, stamp), "-->"), true
}

// parseStampedRow reads "<sha> <0|1> <headline>" when withSHA, else
// "<0|1> <headline>".
func parseStampedRow(line, stamp string, withSHA bool) (verdictRow, bool) {
	rest, ok := trimStamp(line, stamp)
	if !ok {
		return verdictRow{}, false
	}
	fields := strings.SplitN(strings.TrimSpace(rest), " ", map[bool]int{true: 3, false: 2}[withSHA])
	if len(fields) < 2 {
		return verdictRow{}, false
	}
	var row verdictRow
	if withSHA {
		if len(fields) < 3 {
			return verdictRow{}, false
		}
		row.SHA, fields = fields[0], fields[1:]
	}
	row.Blocking = fields[0] == "1"
	row.Headline = strings.TrimSpace(fields[1])
	return row, row.Headline != ""
}

// renderHistory is the visible half. Collapsed, because on a healthy pull
// request it is noise, and on a repaired one it is the whole story.
func renderHistory(history []verdictRow) string {
	if len(history) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n<details><summary>Earlier verdicts on this pull request (%d)</summary>\n\n", len(history))
	fmt.Fprintf(&b, "| Head | Verdict |\n|---|---|\n")
	for _, h := range history {
		mark := "✅"
		if h.Blocking {
			mark = "🔴"
		}
		fmt.Fprintf(&b, "| `%s` | %s %s |\n", shortSHA(h.SHA), mark, h.Headline)
	}
	fmt.Fprintf(&b, "\n</details>\n")
	return b.String()
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
