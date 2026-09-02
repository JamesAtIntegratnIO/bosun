// Package agent is the judgement half: what to do about one promotion's pull
// request, and what to say about it.
//
// The gate answers "did anything structural change". This answers the question
// after it, is the change explicable, is it repairable without a human, and if
// not, what does the human need in front of them. Those are different jobs
// with different failure modes, which is why they are different packages: the
// gate must be boring and deterministic, and this one talks to a model.
//
// Everything it reaches for is a consumer-defined interface with a fake beside
// it, the git host, the model, the cluster, the upstream resolver, the
// in-process gate, so the workflow can be tested end to end without any of
// them. main.go is the only place that knows which implementations are real.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/egress"
	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
	"github.com/JamesAtIntegratnIO/bosun/prompt"
	"github.com/JamesAtIntegratnIO/bosun/redact"
	"github.com/JamesAtIntegratnIO/bosun/safepath"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// MergePolicy is how far a target may move before a human has to approve the
// merge. Set per target and tightenable per stage, in charts/kargo-pipelines.
type MergePolicy string

const (
	// MergeAlways merges any version change.
	MergeAlways MergePolicy = "always"
	// MergeMinor merges patch and minor changes on a 1.x or later chart.
	MergeMinor MergePolicy = "minor"
	// MergePatch merges patch and metadata changes only.
	MergePatch MergePolicy = "patch"
	// MergeNever always waits for a human.
	MergeNever MergePolicy = "never"
)

// InProcessGate is the gate. It runs in this process, against the live cluster
// inventory, and hands back a verdict as a value.
//
// A consumer-defined interface, like every other seam the agent holds: the one
// thing it wants is a verdict for a head commit, and everything else the
// service does (the sweep, the per-SHA cache, the retry window, publishing the
// comment) is none of its business. It also means the tests here supply a
// verdict directly instead of reaching into the service to seed one.
type InProcessGate interface {
	// Ensure returns the verdict for this pull request's head commit, running
	// the gate if no run has produced one yet. It does not return until there
	// is a verdict or a broken gate, so CheckPending and CheckMissing cannot
	// come back from it.
	Ensure(ctx context.Context, pr *gitprovider.PullRequest) *gateservice.Outcome
}

// Promotion is the context Kargo POSTs when a pull request opens.
type Promotion struct {
	Project     string `json:"project"`
	Stage       string `json:"stage"`
	PromotionID string `json:"promotion"`
	Artifact    string `json:"artifact"`
	From        string `json:"from"`
	To          string `json:"to"`
	// AutoMerge is the target's merge policy, and this agent deliberately does
	// not act on it.
	//
	// The Stage evaluates the same policy itself and decides whether to merge;
	// the agent's job is the verdict, not the merge. It is decoded because the
	// chart sends it, and a payload field the receiver silently drops is one
	// nobody can tell is being ignored on purpose.
	//
	// Typed rather than a bare string so its four values are written down
	// somewhere, `AutoMerge string` read like a boolean somebody had spelled
	// wrong.
	AutoMerge  MergePolicy `json:"autoMerge"`
	PRNumber   int         `json:"prNumber"`
	PRURL      string      `json:"prURL"`
	Branch     string      `json:"branch"`
	Files      []string    `json:"files"`
	VerifyApps []string    `json:"verifyApps"`
}

// Triage runs one promotion end to end.
//
// The shape is deliberately linear and bounded: look, decide, maybe act,
// always say something. It never loops waiting for a model, never retries an
// applied edit, and never touches anything outside the bot's own branch.
type Triage struct {
	// Brand is what the agent calls itself in comments and commits.
	Brand     string
	Git       gitprovider.Provider
	LLM       llm.Provider
	Policy    edits.Policy
	CheckName string
	// MaxAttempts caps self-fixes per pull request. Enforced through labels,
	// so it survives a restart, in-memory state would reset the cap every
	// time the pod moved.
	MaxAttempts int
	GatePoll    time.Duration
	// Gate is the gate: the agent renders and diffs the pull request itself.
	// The verdict arrives as a value, so there is no report comment to find
	// and no author to have to trust; the evidence never left the process.
	Gate InProcessGate
	// Upstream, when set, fetches what the maintainers wrote between the two
	// versions. Optional: without it the explanation is grounded in the render
	// alone, says so, and is still worth reading.
	Upstream upstream.Resolver

	// Egress is where the agent may reach and the record of where it went.
	//
	// Open by default with a deny-list, which is a deliberate reversal of the
	// allow-list this had: naming every chart repository, registry CDN and
	// redirect target before the agent could read it was a full-time job whose
	// failure mode was a two-minute timeout and a brief with no evidence. See
	// the egress package.
	Egress egress.Policy

	// Cluster, when set, reads what is running.
	//
	// The one thing CI structurally cannot do, and the reason ADR 0002 put
	// triage in the cluster. Everything the gate knows is a property of text:
	// "3 manifests still declare a version this chart stops serving" is a fact
	// about the repository. Whether anything is stored on that version is a
	// different question and usually the one that decides whether a human
	// needs waking.
	//
	// Optional, read-only, and soft in every direction: nil, unpermitted or
	// unreachable all produce a brief with no live section, which is the brief
	// that existed before this.
	Cluster cluster.Reader

	// Explain turns on the green-gate explanation: when the gate passes but the
	// render still changed, say what it changed. Off means the agent only ever
	// speaks about failures, which is how it was until 0.3.0, and why a bump's
	// real content stayed invisible behind a one-line version diff.
	Explain bool
	// Migrate turns on the deterministic repair for dropped served versions:
	// when that is the only reason the gate is red, rewrite the declaring
	// manifests to the version the gate says survives. No model is involved
	// on this path, see repairDropped.
	Migrate bool
	// Structural turns on the schema-guided half of the repair: when the
	// apiVersion swap leaves a document the target schema no longer accepts,
	// show the model both schemas and validate what it returns.
	//
	// Default on, and the reason is that it only runs where the deterministic
	// path already had authority and the checks in front of it are stricter
	// than anywhere else in this service.
	Structural bool
	// MaxRestructured caps document migrations per pull request. Past it the
	// remainder are named and escalated rather than attempted.
	MaxRestructured int
	CloneRoot       string
	Remote          gitprovider.Remote
	Log             func(string, ...any)

	// Checkout produces a working copy of the pull request's branch and a
	// function that discards it. Defaults to a shallow clone; tests substitute
	// it so the whole workflow can run against a directory on disk, with no
	// remote to clone from.
	Checkout func(ctx context.Context, pr *gitprovider.PullRequest) (string, func(), error)
}

const (
	labelNeedsHuman = "needs-human"
	labelAttempt    = "bosun/attempt-"
	labelAttemptFmt = "%s/attempt-"
)

func (t *Triage) logf(f string, a ...any) {
	if t.Log != nil {
		t.Log(f, a...)
	}
}

// defaultBrand is the name the agent signs its work with when an operator has
// not chosen one.
const defaultBrand = "bosun"

// brand is the operator's name for this agent, or the default.
//
// One method rather than the three open-coded copies this replaced, two of
// which fell back to the literal and one of which lowercased it, so a rename
// changed the footer and the commit status in different ways.
func (t *Triage) brand() string {
	if t.Brand == "" {
		return defaultBrand
	}
	return t.Brand
}

// statusName is what the agent's own commit status is called. It follows the
// brand for the same reason the attempt label does: two agents on one
// repository must not overwrite each other's verdict.
func (t *Triage) statusName() string {
	return strings.ToLower(t.brand())
}

// say publishes the agent's verdict as a commit status, and logs it.
//
// Every exit path calls this, including the ones that do nothing. Before it
// existed, "the gate was green so I stopped" and "I was never called" and "I
// crashed" were the same observation from outside: nothing on the pull
// request. The whole point is that a no-op is now something you can read.
//
// Never fatal. A status is a report, and a report that cannot be filed must
// not take down the thing it was reporting on; the most likely cause is a
// token without the "Commit statuses" write permission, which is worth a loud
// log line and nothing more.
func (t *Triage) say(ctx context.Context, pr *gitprovider.PullRequest, format string, a ...any) {
	t.status(ctx, pr, gitprovider.StateSuccess, format, a...)
}

// working says the same thing, pending. Use it for anything that is not a
// verdict.
//
// The distinction is the whole of this pair. say() used to serve both, writing
// success on entry, so from the first second a reader saw a green `bosun` and
// no comment, which is exactly what a finished run with nothing to report
// looks like. On a green gate that window is a render of both revisions plus a
// model call: minutes of a status claiming to be done. Silence that reads as
// completion is the failure this whole service exists to find, and it was
// doing it.
func (t *Triage) working(ctx context.Context, pr *gitprovider.PullRequest, format string, a ...any) {
	t.status(ctx, pr, gitprovider.StatePending, format, a...)
}

func (t *Triage) status(ctx context.Context, pr *gitprovider.PullRequest, state gitprovider.CommitState, format string, a ...any) {
	desc := fmt.Sprintf(format, a...)
	t.logf("PR %d: %s", pr.Number, desc)
	if err := t.Git.SetCommitStatus(ctx, pr.HeadSHA, t.statusName(), state, desc); err != nil {
		t.logf("PR %d: could not set the %q status (needs Commit statuses: read+write): %v",
			pr.Number, t.statusName(), err)
	}
}

// Run is the whole workflow. Errors are returned for logging; the caller has
// already answered Kargo, so nothing here can fail a promotion.
func (t *Triage) Run(ctx context.Context, p Promotion) error {
	pr, err := t.Git.GetPullRequest(ctx, p.PRNumber)
	if err != nil {
		// Nothing to write a status on: without the pull request there is no
		// head SHA to attach one to.
		return fmt.Errorf("reading PR %d: %w", p.PRNumber, err)
	}

	err = t.run(ctx, p, pr)
	if err != nil {
		// Every error below this point used to reach a pod log and nothing
		// else, leaving the status stuck on "reading <check>", which now
		// means stuck pending, and a status that never resolves is as
		// unreadable as one that lied about being finished.
		//
		// The likeliest error here is the gate breaking: `render` fails, the
		// job that publishes the report is skipped, and gateReport finds a red
		// check with nothing explaining it. That is worth saying where a human
		// will see it.
		t.say(ctx, pr, "triage did not finish: %v", err)
	}
	return err
}

func (t *Triage) run(ctx context.Context, p Promotion, pr *gitprovider.PullRequest) error {
	if has(pr.Labels, labelNeedsHuman) {
		t.say(ctx, pr, "already escalated; leaving it to a human")
		return nil
	}
	// Say so before the first thing that can block. A render of both revisions
	// takes as long as it takes, and a reader in that window should see the
	// agent working rather than an absence they cannot distinguish from never
	// having been called.
	t.working(ctx, pr, "reading %s", t.CheckName)

	attempt := t.AttemptsUsed(pr.Labels) + 1
	if attempt > t.MaxAttempts {
		t.say(ctx, pr, "escalated: %d of %d fix attempts used without a green gate",
			t.MaxAttempts, t.MaxAttempts)
		return t.escalate(ctx, pr, fmt.Sprintf(
			"Reached the limit of %d automatic fix attempts without a green gate.", t.MaxAttempts), nil)
	}

	// Run the gate, or read the run the poller already did. Missing and
	// pending cannot happen; Ensure does not return until there is a verdict
	// or a broken gate.
	out := t.Gate.Ensure(ctx, pr)
	if out.Err != nil {
		return fmt.Errorf("the gate could not run: %w", out.Err)
	}
	if out.State == gitprovider.CheckSuccess {
		return t.explainGreen(ctx, pr, p, out.Report)
	}
	report := out.Report

	// What is running, gathered once and used by whichever path this
	// run takes. Deterministic: every number here was counted by code against
	// a read-only view, and none of it is asserted by a model.
	live := t.liveFor(ctx, p, report)

	root, cleanup, err := t.checkout(ctx, pr)
	if err != nil {
		return fmt.Errorf("checking out %s: %w", pr.Branch, err)
	}
	defer cleanup()

	// The deterministic repair, tried before the model is consulted. A CRD
	// that stopped serving a version blocks because manifests still declare
	// it; the gate's own report names the kind, the dropped versions and the
	// destination, so moving those manifests is a function of evidence, not a
	// judgement. Only when it is the sole reason the gate is red, a repair
	// beside an unexplained targeting change would fix the fixable half and
	// leave a red gate implying it had not.
	//
	// The structured marker is authoritative; the prose scrape is the fallback
	// for a report from a gate old enough not to emit one. They had drifted,
	// the heading appears for any apiVersion object while the count excludes
	// the ones this repair is itself performing, so after a partial repair the
	// scrape reported an unrelated blocker and skipped the retry the attempt
	// cap exists to allow.
	other := migrate.OtherBlockers(report)
	if b, ok := migrate.ParseBlockers(report); ok {
		other = b.OtherThanDropped()
	}
	if t.Migrate && !other {
		if drops := migrate.ParseReport(report); len(drops) > 0 {
			return t.repairDropped(ctx, p, pr, root, report, drops, live, attempt)
		}
	}

	// The other deterministic-by-construction repair, on the same terms: only
	// when it is the sole reason the gate is red, and only where the evidence
	// is computed rather than asserted. A chart that will not render at the
	// new version named the settings it refuses, in its own values schema, and
	// the harness proves the answer by rendering the chart with it (ADR 0013).
	//
	// Behind the same flag as the document migration. Both ask a model for a
	// whole document and refuse it whole; an operator who has not turned that
	// on has not turned this on either.
	if b, ok := migrate.ParseBlockers(report); ok && t.Structural && b.UnrenderableOnly() {
		if len(out.Unrenderable) > 0 {
			return t.repairValues(ctx, pr, root, out.Unrenderable, live, attempt)
		}
		// The count says a chart would not render and the contract is empty,
		// which means this report came from a gate that predates it. Said out
		// loud rather than falling through to a model that has no way to fix
		// it either.
		t.logf("PR %d: the gate reports an unrenderable chart and carried no repair contract for it",
			pr.Number)
	}

	// A red with nothing in this repository to change. Deterministic, and
	// deliberately not a model call.
	//
	// The gate blocks on an object whose apiVersion moved even when the chart
	// renders that object and nothing here declares it, correctly, because
	// somebody should look. But there is no edit to propose, and asking a
	// model to explain that produced a paragraph restating the report and
	// burying the one sentence that mattered: there are no values to change.
	// Saying it in one line, from the gate's own count, is both truer and
	// faster.
	if b, ok := migrate.ParseBlockers(report); ok && b.Any() && !b.RepoSideRemedy() {
		t.say(ctx, pr, "escalated: nothing in this repository can change what blocks this")
		return t.escalateInformed(ctx, pr, noRemedyReason(b), nil, nil, t.upstreamFor(ctx, p, report, root), live)
	}

	userPrompt := buildUserPrompt(p, pr, report, root)
	// Live facts are fact and go in on every path, unlike upstream testimony.
	// They widen the evidence string the applier corroborates against, which
	// is safe here for a reason worth stating: nothing in this block is
	// version-shaped by `edits.versionish`, API versions are `v1beta1`, not
	// `1.2.3`, and counts are bare integers, so it adds no new value an edit
	// could claim as corroborated.
	userPrompt += promptLive(live)

	verdict, err := t.LLM.Classify(ctx, prompt.System, userPrompt)
	if err != nil {
		// A model that is down, slow, or misconfigured must not look like a
		// verdict. Say so on the pull request rather than silently doing
		// nothing, because silence here is indistinguishable from "fine".
		t.say(ctx, pr, "could not reach the model (%s)", t.LLM.Name())
		return t.escalate(ctx, pr, fmt.Sprintf("Could not reach the model (%s): %v", t.LLM.Name(), err), nil)
	}

	switch verdict.Classification {
	case llm.ClassNoAction:
		t.say(ctx, pr, "no action needed: %s", verdict.Summary)
		return t.Git.Comment(ctx, pr.Number, render(t.brand(), t.LLM.Name(), verdict, nil, "No change proposed."))

	case llm.ClassEscalate:
		t.say(ctx, pr, "escalated: %s", verdict.EscalationReason)
		// The reason goes to the status and not into the comment headline: a
		// model's escalationReason is reliably a paraphrase of its summary,
		// and printing both had every escalation announcing itself twice
		// before the reasoning announced it a third time. The comment leads
		// with the verdict marker and the summary; the handoff follows.
		//
		// Upstream is read here and not earlier: a mechanical verdict never
		// pays for it, and the evidence an edit is corroborated against must
		// stay the gate report alone.
		return t.escalateInformed(ctx, pr, "", verdict, nil, t.upstreamFor(ctx, p, report, root), live)
	}

	// Mechanical. The applier is what decides whether any of it happens.
	//
	// t.Policy is a value, so this copy is per-request and two concurrent
	// triages cannot see each other's scope.
	policy := t.Policy
	policy.Evidence = userPrompt
	// The prompt above tells the model the promotion's own file list is what it
	// may change. This is what makes that true rather than stated, and it does
	// not use the same list: the prompt is a request and the scope is a
	// guarantee, so the guarantee is the diff git reports for this pull
	// request. An edit to anything else is refused, however well the standing
	// allowlist would have permitted it, and a promotion body that names a
	// wider set than the branch actually holds now buys nothing.
	scope, err := t.scopeFor(ctx, root, pr)
	if err != nil {
		// Fail closed. The alternative on this path is an unscoped applier,
		// which is the state this replaced.
		t.say(ctx, pr, "escalated: could not establish which files this pull request changes")
		return t.escalate(ctx, pr, fmt.Sprintf(
			"Nothing was written: %v. The files a fix may touch are read from the branch, "+
				"and the promotion body's own list is not a substitute for them.", err), verdict)
	}
	policy.Scope = scope
	in := make([]edits.Edit, 0, len(verdict.Edits))
	for _, e := range verdict.Edits {
		in = append(in, edits.Edit{Path: e.Path, Key: e.Key, From: e.From, To: e.To, Rationale: e.Rationale})
	}
	res, err := edits.Apply(root, policy, in)
	if err != nil {
		// Apply returns what it managed to write alongside the failure. A
		// write that fails partway leaves the earlier edits on disk, and a
		// pull request holding a half-applied fix is the situation most worth
		// naming out loud.
		if res != nil && len(res.Applied) > 0 {
			t.say(ctx, pr, "the fix was applied partially before failing: %d edit(s) were written -- %s",
				len(res.Applied), strings.Join(appliedPaths(res), ", "))
		}
		return err
	}

	// The rule that turns miscalibration into a safe outcome: a mechanical
	// verdict whose edits were all refused is an escalation, not a success.
	if len(res.Applied) == 0 {
		// Every refusal is reported, not just the first. A reader told only
		// about one of three rejected edits would reasonably conclude the
		// other two were fine.
		t.say(ctx, pr, "escalated: all %d proposed edits were refused before anything was written",
			len(res.Rejected))
		return t.escalateInformed(ctx, pr,
			"The proposed fix was rejected before anything was written.", verdict, res,
			t.upstreamFor(ctx, p, report, root), live)
	}

	msg := fmt.Sprintf("fix(%s): %s\n\nProposed by %s, applied by bosun.\n",
		p.Stage, verdict.Summary, t.LLM.Name())
	if err := t.reserveAttempt(ctx, pr, attempt); err != nil {
		return t.escalate(ctx, pr, err.Error(), verdict)
	}
	if err := t.Git.PushFix(ctx, pr, root, msg); err != nil {
		return t.escalate(ctx, pr, fmt.Sprintf("Could not push the fix: %v", err), verdict)
	}

	t.say(ctx, pr, "pushed a fix (attempt %d of %d): %s", attempt, t.MaxAttempts, verdict.Summary)
	return t.Git.Comment(ctx, pr.Number, render(t.brand(), t.LLM.Name(), verdict, res, fmt.Sprintf(
		"Pushed a fix to `%s` (attempt %d of %d). The gate will re-run.",
		pr.Branch, attempt, t.MaxAttempts)))
}

// escalate hands a pull request to a human with the process fact that made it
// necessary.
//
// reason is for process facts the verdict does not carry; "rejected before
// anything was written", "could not push". A model's own escalation reason is
// passed as "" on purpose: the verdict's summary says the same thing, and the
// comment should say it once.
func (t *Triage) escalate(ctx context.Context, pr *gitprovider.PullRequest, reason string, v *llm.Verdict) error {
	return t.escalateInformed(ctx, pr, reason, v, nil, nil, nil)
}

// escalateInformed is escalate plus the applier's result, so a comment can
// list every refused edit rather than summarising one of them, and what the
// maintainers changed between the two versions.
//
// A handoff is somebody's next twenty minutes. "The chart removed its
// ClusterRole and no release note explains why" is an honest sentence and it
// hands over a search; the same sentence with the commit that removed it hands
// over an answer. Attached only where a human is about to spend that time,
// never on the mechanical path, where the evidence for an edit stays the gate
// report alone.
func (t *Triage) escalateInformed(ctx context.Context, pr *gitprovider.PullRequest,
	reason string, v *llm.Verdict, res *edits.Result, up *upstream.Notes, live *liveFacts) error {

	body := "### Needs a human\n\n" + reason + "\n"
	if v != nil {
		head := "**Needs a human.**"
		if reason != "" {
			head += " " + reason
		}
		body = render(t.brand(), t.LLM.Name(), v, res, head)
	}
	body += renderLive(live)
	body += renderUpstream(up)
	if err := t.Git.Comment(ctx, pr.Number, body); err != nil {
		return err
	}
	return t.Git.AddLabel(ctx, pr.Number, labelNeedsHuman)
}

// repairDropped is the deterministic half of the crew's job: the gate proved
// which manifests break and where they must move, so move them.
//
// The safety argument is different from the mechanical-fix path and worth
// stating. There, a model proposes and the applier corroborates. Here there is
// no proposal: kind, dropped versions and destination are parsed from the
// gate's own report line, the rewrite touches nothing but apiVersion values
// that match them, every file still answers to the deny-list and the
// allowlist, and the re-run gate re-counts the consumers itself. The scope
// check is deliberately absent; the consumers are, by definition, files the
// promotion did not touch, and the gate rather than the model is what named
// them.
func (t *Triage) repairDropped(ctx context.Context, p Promotion, pr *gitprovider.PullRequest,
	root, report string, drops []migrate.Dropped, live *liveFacts, attempt int) error {

	total, err := migrate.Migrate(root, drops, t.Policy.Check)
	if err != nil {
		return fmt.Errorf("migrating consumers: %w", err)
	}

	// The swap is done. Now: did it finish the job?
	//
	// A version that moved a field leaves a document that parses, applies, and
	// has that field pruned on the way in, green render, green gate, missing
	// value. This is where that is caught, and it runs only over files the
	// swap already rewrote and policy already permitted.
	var rr *restructureResult
	if t.Structural && len(total.Applied) > 0 {
		rr = t.restructureAll(ctx, root, drops,
			t.schemasFor(ctx, root, p, drops), migratedPaths(total), t.maxRestructured())
	}
	if rr != nil && len(rr.Refused) > 0 {
		// Nothing is pushed, including the swaps that were fine.
		//
		// A partial push is the worst available outcome here and not the
		// obvious one, so it is worth saying why. The swap alone makes the
		// gate green, the manifests no longer declare a dropped version, while
		// a document the target schema rejects sits in the tree with a value
		// the apiserver will silently drop. Pushing 27 correct files and
		// escalating one would produce a green gate over a broken change,
		// which is precisely the shape of failure this whole service exists to
		// find.
		t.say(ctx, pr, "escalated: %d document(s) need reshaping and the proposal was refused", len(rr.Refused))
		return t.escalateInformed(ctx, pr,
			renderMigration(t.brand(), t.LLM.Name(), drops, total, rr,
				"**Needs a human.** The chart moved fields between these API versions. "+
					"Swapping the version alone would leave manifests the new schema does not accept, "+
					"so nothing was pushed.", live),
			nil, nil, nil, live)
	}

	if len(total.Applied) == 0 {
		if len(total.Refused) > 0 {
			t.say(ctx, pr, "escalated: every consumer the gate named was refused by policy")
			return t.escalateInformed(ctx, pr, renderMigration(t.brand(), t.LLM.Name(), drops, total, nil,
				"**Needs a human.** The gate names manifests that must move off a dropped API version, but policy refuses every one of them.",
				live), nil, nil, t.upstreamFor(ctx, p, report, root), live)
		}
		// The gate counted consumers; this checkout has none. Fixing nothing
		// and saying so beats guessing which of the two is stale.
		t.say(ctx, pr, "escalated: the gate names consumers this branch does not have")
		return t.escalateInformed(ctx, pr,
			"The gate blocked on a dropped served version, but no manifest on this branch declares one. "+
				"The gate's report and this checkout disagree — a human should look at both.",
			nil, nil, nil, live)
	}

	files := 0
	seen := map[string]bool{}
	for _, a := range total.Applied {
		if !seen[a.Path] {
			seen[a.Path] = true
			files++
		}
	}
	how := "Deterministic rewrite by bosun -- no model involved.\n"
	if rr != nil && len(rr.Applied) > 0 {
		how = fmt.Sprintf("Deterministic rewrite by bosun. %d document(s) also needed reshaping for the\n"+
			"new schema; those were proposed by %s and validated before writing.\n", len(rr.Applied), t.LLM.Name())
	}
	msg := fmt.Sprintf("fix(%s): migrate %d manifest(s) off dropped API version(s)\n\n"+
		"The chart stopped serving them; destinations from the gate's report.\n%s", p.Stage, files, how)
	if err := t.reserveAttempt(ctx, pr, attempt); err != nil {
		return t.escalate(ctx, pr, err.Error(), nil)
	}
	if err := t.Git.PushFix(ctx, pr, root, msg); err != nil {
		return t.escalate(ctx, pr, fmt.Sprintf("Could not push the migration: %v", err), nil)
	}
	t.say(ctx, pr, "migrated %d manifest(s) off dropped API version(s)%s", files, t.attemptSuffix(attempt))
	return t.Git.Comment(ctx, pr.Number, renderMigration(t.brand(), t.LLM.Name(), drops, total, rr, fmt.Sprintf(
		"Pushed a migration to `%s`%s. The gate will re-run and re-count.",
		pr.Branch, t.attemptSuffix(attempt)), live))
}

// migratedPaths is the files the swap rewrote, deduplicated. The structural
// pass looks at these and nothing else: they are already policy-checked, and a
// file the swap did not touch has no dropped version in it to reshape.
func migratedPaths(res *migrate.Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range res.Applied {
		if !seen[a.Path] {
			seen[a.Path] = true
			out = append(out, a.Path)
		}
	}
	return out
}

func (t *Triage) maxRestructured() int {
	if t.MaxRestructured > 0 {
		return t.MaxRestructured
	}
	return 5
}

// attemptSuffix names the attempt only when there has been more than one.
//
// Every comment used to carry "(attempt 1 of 2)". On the overwhelmingly common
// path there is exactly one attempt, so the counter described a sequence that
// never happened and invited the reader to look for a second pass that does
// not exist. It earns its place only when it is telling you something: that
// this is a re-try, and how many are left.
//
// The mechanism stays either way; the label is the cap's only memory across
// restarts, and without it a repair that does not turn the gate green would be
// retried forever.
func (t *Triage) attemptSuffix(attempt int) string {
	if attempt <= 1 {
		return ""
	}
	return fmt.Sprintf(" (attempt %d of %d)", attempt, t.MaxAttempts)
}

// reserveAttempt records this attempt before anything is pushed, and refuses
// the push if it cannot.
//
// The cap has exactly one memory, a label on the pull request, and both repair
// paths used to push first and label afterwards, logging a label failure and
// carrying on. That is a cap that fails open, and it does not take an attacker
// to reach: a token with `contents: write` and no `issues: write` pushes the
// fix, fails to label, and every later run counts zero attempts and repairs
// again. The gate re-runs on each push, so the loop is a model call and a
// commit per iteration against somebody's repository, indefinitely.
//
// Reserving first inverts every failure. A label that cannot be written is a
// pull request that gets a human instead of a commit.
func (t *Triage) reserveAttempt(ctx context.Context, pr *gitprovider.PullRequest, attempt int) error {
	label := fmt.Sprintf("%s%d", t.attemptPrefix(), attempt)
	if err := t.Git.AddLabel(ctx, pr.Number, label); err != nil {
		t.logf("PR %d: could not record attempt %d as %q, so nothing was pushed: %v",
			pr.Number, attempt, label, err)
		return fmt.Errorf("Could not record attempt %d of %d as the label %q, which is how the "+
			"attempt cap remembers across restarts. Nothing was pushed: an unrecorded attempt "+
			"would let this repeat without limit. The token needs permission to label issues. (%v)",
			attempt, t.MaxAttempts, label, err)
	}
	return nil
}

// AttemptsUsed is how many self-fix attempts the labels standing on a pull
// request record.
//
// Exported because a read surface publishes it: a caller asking what the agent
// is doing about a pull request wants to know whether it will try again, and
// that answer is this number against MaxAttempts. The labels are the cap's only
// memory, so they are also the only place the answer exists.
//
// A method rather than a function taking a prefix, because the prefix follows
// the brand and a caller that had to know that could get it wrong in the one
// direction that matters: counting under the wrong name reports an agent with
// attempts left when it has none. The path that enforces the cap calls this
// too, so there is one count rather than two that agree today.
func (t *Triage) AttemptsUsed(labels []string) int {
	return attemptsSoFar(labels, t.attemptPrefix())
}

// attemptPrefix is the label prefix the attempt cap counts. It follows the
// brand: a renamed agent must not keep writing labels under its old name, or
// the cap silently resets on the rename.
func (t *Triage) attemptPrefix() string {
	if t.Brand == "" {
		return labelAttempt
	}
	return fmt.Sprintf(labelAttemptFmt, strings.ToLower(t.Brand))
}

func (t *Triage) clone(ctx context.Context, pr *gitprovider.PullRequest) (string, func(), error) {
	root, err := os.MkdirTemp(t.CloneRoot, "pr")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }

	if err := gitprovider.Clone(ctx, t.Remote, pr.Branch, root); err != nil {
		cleanup()
		return "", func() {}, err
	}
	// The clone asked for a branch; everything downstream is about a commit.
	// The gate's verdict, the report this reads, the status this writes and
	// the attempt label that caps it are all keyed to pr.HeadSHA, so a push
	// that landed between reading the pull request and cloning it would have
	// this agent repair a commit it never triaged and report the result
	// against one it never saw.
	if err := gitprovider.EnsureHead(ctx, t.Remote, root, pr.HeadSHA); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return root, cleanup, nil
}

// buildUserPrompt assembles the evidence. The scalar inventory is the part
// that matters: handed one, a model selects a key from a list instead of
// inventing a path and paraphrasing a value.
//
// Every path is confined to the checkout before it is read. Promotion.Files
// arrives in the request body, and this process holds the git token, the LLM
// key and the App private key, so `../../../root/.ssh/id_rsa` in that list
// would otherwise be read and rendered into a prompt sent to a model. The
// write path has said this in a comment since it was written; the read path
// had no equivalent.
func buildUserPrompt(p Promotion, pr *gitprovider.PullRequest, report, root string) string {
	files := make([]prompt.File, 0, len(p.Files))
	for _, f := range p.Files {
		full, err := containedPath(root, f)
		if err != nil {
			files = append(files, prompt.File{Path: f, Err: err})
			continue
		}
		data, err := os.ReadFile(full)
		files = append(files, prompt.File{Path: f, Data: data, Err: err})
	}
	return prompt.User(prompt.UserInput{
		Header: fmt.Sprintf("PULL REQUEST #%d: %s\n\nArtifact %s moving %s -> %s (project %s, stage %s).",
			pr.Number, pr.Title, p.Artifact, p.From, p.To, p.Project, p.Stage),
		Report:    report,
		Files:     files,
		Inventory: true,
	})
}

func has(items []string, want string) bool {
	for _, s := range items {
		if s == want {
			return true
		}
	}
	return false
}

func attemptsSoFar(labels []string, prefix string) int {
	n := 0
	for _, l := range labels {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// explainGreen handles a gate that passed.
//
// A green gate is not the same as an uneventful change. The gate blocks on
// structural things (targeting, sources, apiVersion migrations) and reports
// the rest: a chart that added four resources, moved a port, flipped a
// default. All of that renders green and arrives as a pull request whose
// visible diff is one version number.
//
// This is the common case, and until now the agent stopped here and said
// nothing.
//
// Three things it deliberately does not do:
//
//   - no model call when the gate reports no change at all. There is nothing to
//     explain, and burning inference to say "nothing happened" is how a useful
//     comment becomes noise people scroll past.
//   - no comment on the same pull request twice. The status carries the verdict
//     on every run; the comment is for when there is something to read.
//   - no failure. Explanation is a courtesy on a green gate. If the model is
//     down or the report is unreadable, the merge is not this agent's business
//     to hold up.
func (t *Triage) explainGreen(ctx context.Context, pr *gitprovider.PullRequest, p Promotion, report string) error {
	if !t.Explain {
		t.say(ctx, pr, "%s is green; nothing to triage", t.CheckName)
		return nil
	}

	if gate.SaysNothingChanged(report) {
		t.say(ctx, pr, "%s is green; the render is unchanged", t.CheckName)
		return nil
	}
	if t.alreadyExplained(ctx, pr) {
		t.say(ctx, pr, "%s is green; already explained", t.CheckName)
		return nil
	}

	root, cleanup, err := t.checkout(ctx, pr)
	if err != nil {
		t.say(ctx, pr, "%s is green; could not read the branch to explain it", t.CheckName)
		return nil
	}
	defer cleanup()

	userPrompt := buildUserPrompt(p, pr, report, root)

	// What the maintainers said, if it can be found. Never fatal, and never
	// silent about its absence: an explanation with no upstream context has to
	// say so, or a reader credits it with evidence it does not have.
	live := t.liveFor(ctx, p, report)
	userPrompt += promptLive(live)

	notes := t.upstreamFor(ctx, p, report, root)
	userPrompt += upstream.Render(notes)

	v, err := t.LLM.Classify(ctx, prompt.Explain, userPrompt)
	if err != nil {
		// Explaining is a courtesy. A model that is down must not be the reason
		// a green pull request looks unattended.
		t.say(ctx, pr, "%s is green; could not reach the model to explain it", t.CheckName)
		return nil
	}

	// A green gate is a verdict on the render, not on the bump. This path may
	// now ask for a human; it blocks nothing, since the commit status is
	// never a failure state, but it labels the pull request and says why.
	//
	// Edits are ignored here whatever the model returned. Nothing on the
	// explain path writes a file, and that is a property of this function
	// rather than a request in the prompt.
	if v.Classification == llm.ClassEscalate {
		reason := v.EscalationReason
		if reason == "" {
			reason = v.Summary
		}
		t.say(ctx, pr, "%s is green, but flagged: %s", t.CheckName, reason)
		if err := t.Git.Comment(ctx, pr.Number,
			renderExplanation(t.LLM.Name(), v, notes)+renderLive(live)+renderUpstream(notes)); err != nil {
			return err
		}
		return t.Git.AddLabel(ctx, pr.Number, labelNeedsHuman)
	}

	t.say(ctx, pr, "%s is green: %s", t.CheckName, v.Summary)
	return t.Git.Comment(ctx, pr.Number, renderExplanation(t.LLM.Name(), v, notes))
}

// alreadyExplained keeps the agent to one explanation per pull request. Kargo
// can call more than once for the same promotion, and a bot that re-explains on
// every retry is a bot people collapse.
func (t *Triage) alreadyExplained(ctx context.Context, pr *gitprovider.PullRequest) bool {
	comments, err := t.Git.ListComments(ctx, pr.Number)
	if err != nil {
		return false
	}
	for _, c := range comments {
		if strings.Contains(c.Body, explanationMarker) {
			return true
		}
	}
	return false
}

const explanationMarker = "<!-- bosun:explanation -->"

// upstreamFor never fails anything. A resolver that is misconfigured,
// rate-limited or looking at an artifact with no source label produces an
// answer grounded in the render alone, which is the behaviour that existed
// before this and is still useful.
//
// It fetches two things where it can. The release notes are what the
// maintainers said; the commits between the two tags are what they did, and
// the second exists because of the findings the first cannot explain. A chart
// that quietly dropped its ClusterRole and shipped a release note about
// performance leaves an explanation with nothing to say, while the commit that
// deleted the template says exactly why, in a sentence nobody wrote for a
// changelog.
//
// The commits are aimed by migrate.Subjects: the kinds and names the gate's own
// findings are about. No model chooses its own evidence here.
func (t *Triage) upstreamFor(ctx context.Context, p Promotion, report, root string) *upstream.Notes {
	if t.Upstream == nil {
		return &upstream.Notes{Note: "upstream lookup is not configured"}
	}
	// Where this fetches from is the promotion body's artifact string, so the
	// question in front of every other one is whether this repository has
	// anything to do with that host. See repoReaches.
	if !repoReaches(root, p.Artifact) {
		return &upstream.Notes{Note: fmt.Sprintf(
			"upstream lookup was skipped: nothing in this repository names %s, "+
				"and that is where the lookup would have gone", artifactHost(p.Artifact))}
	}
	n, err := t.Upstream.Notes(ctx, p.Artifact, p.From, p.To)
	if err != nil || n == nil {
		n = &upstream.Notes{Note: fmt.Sprintf("upstream lookup failed (%v)", err)}
	}

	// A second interface, type-asserted rather than required: a resolver that
	// only reads releases keeps working and contributes no commits.
	cr, ok := t.Upstream.(upstream.CompareResolver)
	if !ok {
		return n
	}
	terms := migrate.Subjects(report)
	if len(terms) == 0 {
		// Nothing to aim at. Reading a commit range with no filter would
		// produce a list of everything, which is not evidence about anything.
		return n
	}
	c, err := cr.Compare(ctx, p.Artifact, p.From, p.To, terms)
	if err != nil || c == nil {
		c = &upstream.Compare{Note: fmt.Sprintf("upstream commit lookup failed (%v)", err)}
	}
	n.Compare = c
	return n
}

// checkout is the working copy, however the caller supplies it.
func (t *Triage) checkout(ctx context.Context, pr *gitprovider.PullRequest) (string, func(), error) {
	if t.Checkout != nil {
		return t.Checkout(ctx, pr)
	}
	return t.clone(ctx, pr)
}

// scopeFor is the exact set of paths a fix may write on this pull request, or
// the reason there is none.
//
// This is the applier's Scope, the guarantee behind "cannot edit a file this
// change did not touch". It was `p.Files` straight off the wire: an empty array
// turned scope narrowing off entirely, since edits.Policy only enforces Scope
// when it is non-empty, and any other array widened it to whatever the standing
// allowlist happens to permit. DefaultDeny, Allow and the corroboration rule
// still held, so this was never arbitrary write, but the per-request tightening
// was worth exactly as much as the caller's honesty. The diff is a fact about
// the pull request; the body is a claim about it.
//
// A pull request that changed nothing is a real answer and it must not come
// back as an empty list, because empty is what edits.Policy reads as unscoped:
// handing one back would turn the guarantee off in exactly the case with the
// least justification for it. It fails closed instead.
//
// Only the path that writes asks for this. The escalations and the
// deterministic migration have nothing to scope, and fetching the base branch
// is not free.
func (t *Triage) scopeFor(ctx context.Context, root string, pr *gitprovider.PullRequest) ([]string, error) {
	paths, err := changedFiles(ctx, t.Remote, root, pr)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("this pull request changes no file, so there is nothing a fix may write")
	}
	return paths, nil
}

// repoReaches reports whether the checkout names the host a promotion's
// artifact would send this process to.
//
// The `artifact` field of a promotion body becomes an outbound request:
// `{repoURL}/index.yaml` for a classic Helm repository, a token and manifest
// fetch for an OCI reference. The host is the caller's string verbatim, so
// without this the endpoint fetches whatever it is told to, including
// 169.254.169.254 and anything answering on `.svc`, and the outcome comes back
// on the pull request comment: a port scanner with a published result. The
// bearer token in front of the endpoint raises the bar and does not change
// what a legitimate caller may name.
//
// egress.Transport refuses private and link-local addresses, and that is the
// layer below this one. This is the layer above: is this an artifact the
// repository has anything to do with? A repository that deploys a chart names
// the place it comes from, in an Argo CD Application, a Flux HelmRepository,
// a values file, somewhere, because something has to pull it.
//
// A substring search over the checkout rather than a parse of a named field,
// and deliberately: `repoURL` is Argo CD's and Kargo's spelling, `spec.url` is
// Flux's, and picking one would be an assumption about a repository layout,
// which Rule 1 does not allow. What they have in common is that the host is in
// the file.
//
// A reference with no host of its own is allowed through. `redis` and
// `linuxserver/sonarr` are real entries in a real target list, and the only
// place they can resolve to is Docker Hub, which is a destination the
// convention chose rather than the caller.
func repoReaches(root, artifact string) bool {
	host := artifactHost(artifact)
	if host == "" {
		return true
	}
	found := false
	needle := []byte(host)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return nil
		case found:
			return fs.SkipAll
		case d.IsDir():
			// The object store is not the repository. It holds compressed
			// copies of everything the working tree already has, and packfiles
			// are where a substring search goes to spend a minute finding
			// nothing.
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		case !d.Type().IsRegular():
			// Symlinks included: this reads the checkout to decide where the
			// process may connect, and a tracked link at a permitted path
			// points wherever whoever wrote it wanted.
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxSearchedBytes {
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, needle) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// artifactHost is where a promotion's artifact sends this process, or "" when
// it names no host of its own.
func artifactHost(artifact string) string {
	ref, _ := upstream.ParseArtifact(artifact)
	return egress.HostOf(ref)
}

// maxSearchedBytes caps one file read while looking for a host. A GitOps
// repository is manifests, and a file larger than this is a vendored blob that
// declares nothing.
const maxSearchedBytes = 1 << 20

// changedFiles is the diff between what this pull request merges into and what
// it contains.
//
// The base is the *merge base*, and that is a correction with a safety
// consequence rather than a tidying. It used to be the base branch's tip,
// which is a moving revision: every file any other pull request merged after
// this branch was cut showed up in this diff, and this diff is `Policy.Scope`,
// the guarantee that a fix cannot edit a file this change did not touch. The
// guarantee held exactly as long as nothing else merged. The gate had the same
// defect in its own half, where it reported those files as this pull request
// removing them, and both are gone the same way.
//
// `-z` because git quotes a path with a space in it, and a quoted path is one
// that silently matches nothing when the applier compares it.
func changedFiles(ctx context.Context, r gitprovider.Remote, root string, pr *gitprovider.PullRequest) ([]string, error) {
	base := pr.BaseBranch
	if base == "" {
		base = pr.BaseSHA
	}
	if base == "" {
		return nil, fmt.Errorf("the pull request names no base branch or base commit to diff against")
	}
	head := pr.HeadSHA
	if head == "" {
		head = pr.Branch
	}

	mergeBase, err := gitprovider.MergeBase(ctx, r, root, head, base)
	if err != nil {
		return nil, fmt.Errorf("finding what %s and this branch last shared, to see what this pull request changes: %w", base, err)
	}

	diff := exec.CommandContext(ctx, "git", "-C", root, "diff", "--name-only", "-z", mergeBase, "HEAD")
	var stderr strings.Builder
	diff.Stderr = &stderr
	out, err := diff.Output()
	if err != nil {
		return nil, fmt.Errorf("diffing %s against this branch: %w: %s",
			base, err, strings.TrimSpace(redact.Text(stderr.String())))
	}

	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// appliedPaths is the files an edits.Result wrote, deduplicated in
// the order they were written; one edit per key means one path can appear
// several times, and repeating it in a message helps nobody.
func appliedPaths(res *edits.Result) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range res.Applied {
		if !seen[a.Path] {
			seen[a.Path] = true
			out = append(out, a.Path)
		}
	}
	return out
}

// containedPath resolves one promotion-supplied path inside the checkout, and
// refuses anything that leaves it.
//
// The same test edits.Apply makes before writing, for the same reason and on
// input from the same place. Reading is not harmless here: what is read goes
// into a prompt, and a prompt is published.
//
// It was a lexical test until it was a real one. `charts/app/values.yaml` is
// contained by every string comparison and is not contained at all if the
// checkout holds it as a link to the pod's service-account token, which this
// path would then read and render into a prompt.
func containedPath(root, rel string) (string, error) {
	return safepath.Resolve(root, rel)
}
