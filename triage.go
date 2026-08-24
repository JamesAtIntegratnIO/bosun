package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// Promotion is the context Kargo POSTs when a pull request opens.
type Promotion struct {
	Project     string   `json:"project"`
	Stage       string   `json:"stage"`
	PromotionID string   `json:"promotion"`
	Artifact    string   `json:"artifact"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	AutoMerge   string   `json:"autoMerge"`
	PRNumber    int      `json:"prNumber"`
	PRURL       string   `json:"prURL"`
	Branch      string   `json:"branch"`
	Files       []string `json:"files"`
	VerifyApps  []string `json:"verifyApps"`
}

// Triage runs one promotion end to end.
//
// The shape is deliberately linear and bounded: look, decide, maybe act,
// always say something. It never loops waiting for a model, never retries an
// applied edit, and never touches anything outside the bot's own branch.
type Triage struct {
	// Brand is what the agent calls itself in comments and commits.
	Brand     string
	BrandMark string
	Git       gitprovider.Provider
	LLM       llm.Provider
	Policy    edits.Policy
	CheckName string
	// GateReportAuthor is the only account whose gate report this will read.
	//
	// The gate's verdict arrives as a pull-request comment carrying a marker,
	// and until this existed the marker was the whole of the check. Anyone who
	// can comment on the pull request can write that marker, and the report
	// under it is the evidence every other decision here is made from: which
	// manifests the deterministic repair rewrites, which versions the applier
	// will corroborate, what the model is told actually rendered. A forged
	// report is not a wrong opinion, it is a wrong instruction with the gate's
	// authority behind it.
	//
	// Empty or "*" trusts any author. That is what a host with no stable CI
	// identity can express, and it is the behaviour that existed before -- but
	// it is a choice now, made in a values file, rather than an omission.
	GateReportAuthor string
	// MaxAttempts caps self-fixes per pull request. Enforced through labels,
	// so it survives a restart -- in-memory state would reset the cap every
	// time the pod moved.
	MaxAttempts int
	// GateWait is how long to wait for the gate to reach a verdict before
	// giving up on this run.
	GateWait time.Duration
	GatePoll time.Duration
	// Upstream, when set, fetches what the maintainers wrote between the two
	// versions. Optional: without it the explanation is grounded in the render
	// alone, says so, and is still worth reading.
	Upstream upstream.Resolver

	// Explain turns on the green-gate explanation: when the gate passes but the
	// render still changed, say what it changed. Off means the agent only ever
	// speaks about failures, which is how it was until 0.3.0 -- and why a
	// bump's real content stayed invisible behind a one-line version diff.
	Explain bool
	// Migrate turns on the deterministic repair for dropped served versions:
	// when that is the only reason the gate is red, rewrite the declaring
	// manifests to the version the gate says survives. No model is involved
	// on this path -- see repairDropped.
	Migrate   bool
	CloneRoot string
	RepoURL   string
	Log       func(string, ...any)

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

// statusName is what the agent's own commit status is called. It follows the
// brand for the same reason the attempt label does: two agents on one
// repository must not overwrite each other's verdict.
func (t *Triage) statusName() string {
	if t.Brand == "" {
		return "bosun"
	}
	return strings.ToLower(t.Brand)
}

// say publishes the agent's verdict as a commit status, and logs it.
//
// EVERY exit path calls this, including the ones that do nothing. Before it
// existed, "the gate was green so I stopped" and "I was never called" and "I
// crashed" were the same observation from outside: nothing on the pull
// request. The whole point is that a no-op is now something you can read.
//
// Never fatal. A status is a report, and a report that cannot be filed must
// not take down the thing it was reporting on -- the most likely cause is a
// token without the "Commit statuses" WRITE permission, which is worth a loud
// log line and nothing more.
func (t *Triage) say(ctx context.Context, pr *gitprovider.PullRequest, format string, a ...any) {
	t.status(ctx, pr, gitprovider.StateSuccess, format, a...)
}

// working says the same thing, PENDING. Use it for anything that is not a
// verdict.
//
// The distinction is the whole of this pair. say() used to serve both, writing
// success on entry, so from the first second a reader saw a green `bosun` and
// no comment -- which is exactly what a finished run with nothing to report
// looks like. On a green gate that window is `GateWait` plus a model call: ten
// minutes of a status claiming to be done. Silence that reads as completion is
// the failure this whole service exists to find, and it was doing it.
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
		// else, leaving the status stuck on "reading <check>" -- which now
		// means stuck PENDING, and a status that never resolves is as
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
	// Say so before the first thing that can block. waitForGate can sit for ten
	// minutes, and a reader in that window should see the agent working rather
	// than an absence they cannot distinguish from never having been called.
	t.working(ctx, pr, "reading %s", t.CheckName)

	attempt := attemptsSoFar(pr.Labels, t.attemptPrefix()) + 1
	if attempt > t.MaxAttempts {
		t.say(ctx, pr, "escalated: %d of %d fix attempts used without a green gate",
			t.MaxAttempts, t.MaxAttempts)
		return t.escalate(ctx, pr, fmt.Sprintf(
			"Reached the limit of %d automatic fix attempts without a green gate.", t.MaxAttempts), nil)
	}

	state, err := t.waitForGate(ctx, pr)
	if err != nil {
		return err
	}
	switch state {
	case gitprovider.CheckSuccess:
		return t.explainGreen(ctx, pr, p)
	case gitprovider.CheckMissing:
		t.say(ctx, pr, "no %s check appeared within %s", t.CheckName, t.GateWait)
		return nil
	case gitprovider.CheckPending:
		t.say(ctx, pr, "%s still had no verdict after %s", t.CheckName, t.GateWait)
		return nil
	}

	report, err := t.gateReport(ctx, pr)
	if err != nil {
		return err
	}

	checkout := t.Checkout
	if checkout == nil {
		checkout = t.clone
	}
	root, cleanup, err := checkout(ctx, pr)
	if err != nil {
		return fmt.Errorf("checking out %s: %w", pr.Branch, err)
	}
	defer cleanup()

	// The deterministic repair, tried before the model is consulted. A CRD
	// that stopped serving a version blocks because manifests still declare
	// it; the gate's own report names the kind, the dropped versions and the
	// destination, so moving those manifests is a function of evidence, not a
	// judgement. Only when it is the sole reason the gate is red -- a repair
	// beside an unexplained targeting change would fix the fixable half and
	// leave a red gate implying it had not.
	if t.Migrate && !migrate.OtherBlockers(report) {
		if drops := migrate.ParseReport(report); len(drops) > 0 {
			return t.repairDropped(ctx, p, pr, root, drops, attempt)
		}
	}

	prompt, err := buildUserPrompt(p, pr, report, root)
	if err != nil {
		return err
	}

	verdict, err := t.LLM.Classify(ctx, systemPrompt, prompt)
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
		return t.Git.Comment(ctx, pr.Number, t.render(verdict, nil, "No change proposed."))

	case llm.ClassEscalate:
		t.say(ctx, pr, "escalated: %s", verdict.EscalationReason)
		// The reason goes to the status and NOT into the comment headline: a
		// model's escalationReason is reliably a paraphrase of its summary,
		// and printing both had every escalation announcing itself twice
		// before the reasoning announced it a third time. The comment leads
		// with the verdict marker and the summary; the handoff follows.
		return t.escalate(ctx, pr, "", verdict)
	}

	// Mechanical. The applier is what decides whether any of it happens.
	//
	// t.Policy is a value, so this copy is per-request and two concurrent
	// triages cannot see each other's scope.
	policy := t.Policy
	policy.Evidence = prompt
	// The promotion already reports which files it rewrote, and the prompt
	// above already tells the model those are the files it may change. This is
	// what makes that true rather than merely stated: an edit to anything else
	// is refused, however well the standing allowlist would have permitted it.
	policy.Scope = p.Files
	in := make([]edits.Edit, 0, len(verdict.Edits))
	for _, e := range verdict.Edits {
		in = append(in, edits.Edit{Path: e.Path, Key: e.Key, From: e.From, To: e.To, Rationale: e.Rationale})
	}
	res, err := edits.Apply(root, policy, in)
	if err != nil {
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
		return t.escalateWith(ctx, pr,
			"The proposed fix was rejected before anything was written.", verdict, res)
	}

	msg := fmt.Sprintf("fix(%s): %s\n\nProposed by %s, applied by bosun.\n",
		p.Stage, verdict.Summary, t.LLM.Name())
	if err := t.Git.PushFix(ctx, pr, root, msg); err != nil {
		return t.escalate(ctx, pr, fmt.Sprintf("Could not push the fix: %v", err), verdict)
	}

	if err := t.Git.AddLabel(ctx, pr.Number, fmt.Sprintf("%s%d", t.attemptPrefix(), attempt)); err != nil {
		t.logf("PR %d: could not label attempt %d: %v", pr.Number, attempt, err)
	}
	t.say(ctx, pr, "pushed a fix (attempt %d of %d): %s", attempt, t.MaxAttempts, verdict.Summary)
	return t.Git.Comment(ctx, pr.Number, t.render(verdict, res, fmt.Sprintf(
		"Pushed a fix to `%s` (attempt %d of %d). The gate will re-run.",
		pr.Branch, attempt, t.MaxAttempts)))
}

func (t *Triage) escalate(ctx context.Context, pr *gitprovider.PullRequest, reason string, v *llm.Verdict) error {
	return t.escalateWith(ctx, pr, reason, v, nil)
}

// escalateWith is escalate plus the applier's result, so a comment can list
// every refused edit rather than summarising one of them.
//
// reason is for PROCESS facts the verdict does not carry -- "rejected before
// anything was written", "could not push". A model's own escalation reason is
// passed as "" on purpose: the verdict's summary says the same thing, and the
// comment should say it once.
func (t *Triage) escalateWith(ctx context.Context, pr *gitprovider.PullRequest, reason string, v *llm.Verdict, res *edits.Result) error {
	body := "### Needs a human\n\n" + reason + "\n"
	if v != nil {
		head := "**Needs a human.**"
		if reason != "" {
			head += " " + reason
		}
		body = t.render(v, res, head)
	}
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
// check is deliberately absent -- the consumers are, by definition, files the
// promotion did not touch, and the gate rather than the model is what named
// them.
func (t *Triage) repairDropped(ctx context.Context, p Promotion, pr *gitprovider.PullRequest,
	root string, drops []migrate.Dropped, attempt int) error {

	total, err := migrate.Migrate(root, drops, t.Policy.Check)
	if err != nil {
		return fmt.Errorf("migrating consumers: %w", err)
	}

	if len(total.Applied) == 0 {
		if len(total.Refused) > 0 {
			t.say(ctx, pr, "escalated: every consumer the gate named was refused by policy")
			return t.escalate(ctx, pr, t.renderMigration(drops, total,
				"**Needs a human.** The gate names manifests that must move off a dropped API version, but policy refuses every one of them."), nil)
		}
		// The gate counted consumers; this checkout has none. Fixing nothing
		// and saying so beats guessing which of the two is stale.
		t.say(ctx, pr, "escalated: the gate names consumers this branch does not have")
		return t.escalate(ctx, pr,
			"The gate blocked on a dropped served version, but no manifest on this branch declares one. "+
				"The gate's report and this checkout disagree — a human should look at both.", nil)
	}

	files := 0
	seen := map[string]bool{}
	for _, a := range total.Applied {
		if !seen[a.Path] {
			seen[a.Path] = true
			files++
		}
	}
	msg := fmt.Sprintf("fix(%s): migrate %d manifest(s) off dropped API version(s)\n\n"+
		"The chart stopped serving them; destinations from the gate's report.\n"+
		"Deterministic rewrite by bosun -- no model involved.\n", p.Stage, files)
	if err := t.Git.PushFix(ctx, pr, root, msg); err != nil {
		return t.escalate(ctx, pr, fmt.Sprintf("Could not push the migration: %v", err), nil)
	}
	if err := t.Git.AddLabel(ctx, pr.Number, fmt.Sprintf("%s%d", t.attemptPrefix(), attempt)); err != nil {
		t.logf("PR %d: could not label attempt %d: %v", pr.Number, attempt, err)
	}
	t.say(ctx, pr, "migrated %d manifest(s) off dropped API version(s) (attempt %d of %d)",
		files, attempt, t.MaxAttempts)
	return t.Git.Comment(ctx, pr.Number, t.renderMigration(drops, total, fmt.Sprintf(
		"Pushed a migration to `%s` (attempt %d of %d). The gate will re-run and re-count.",
		pr.Branch, attempt, t.MaxAttempts)))
}

// renderMigration is the comment for the deterministic path. Its footer names
// no model, because none was involved -- a reader deciding how much to trust
// this needs to know it is arithmetic, not judgement.
func (t *Triage) renderMigration(drops []migrate.Dropped, res *migrate.Result, headline string) string {
	var b strings.Builder
	if t.Brand != "" {
		mark := t.BrandMark
		if mark != "" {
			mark += " "
		}
		fmt.Fprintf(&b, "%s**%s**\n\n", mark, t.Brand)
	}
	fmt.Fprintf(&b, "%s\n\n", headline)
	b.WriteString("**The chart stopped serving API versions this repository still declares.** " +
		"The gate named the versions and the one that survives; every declaring manifest moves there.\n\n")
	for _, d := range drops {
		fmt.Fprintf(&b, "- `%s`: `%s/{%s}` → `%s/%s`\n",
			d.Kind, d.Group, strings.Join(d.Versions, ", "), d.Group, d.Target)
	}
	if len(res.Applied) > 0 {
		b.WriteString("\n**Migrated**\n\n| File | Kind | To |\n|---|---|---|\n")
		for _, a := range res.Applied {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n", a.Path, a.Kind, a.To)
		}
	}
	if len(res.Refused) > 0 {
		b.WriteString("\n**Refused**\n\n")
		for _, r := range res.Refused {
			fmt.Fprintf(&b, "- `%s` — %s\n", r.Path, r.Reason)
		}
	}
	brand := t.Brand
	if brand == "" {
		brand = "bosun"
	}
	fmt.Fprintf(&b, "\n<sub>%s · deterministic repair, no model · automated triage, not a review</sub>\n", brand)
	return b.String()
}

// render builds the pull-request comment. It always states which model
// produced the verdict, and always lists what was refused -- a silent refusal
// would let a reader believe a fix was applied when it was not.
// attemptPrefix is the label prefix the attempt cap counts. It follows the
// brand: a renamed agent must not keep writing labels under its old name, or
// the cap silently resets on the rename.
func (t *Triage) attemptPrefix() string {
	if t.Brand == "" {
		return labelAttempt
	}
	return fmt.Sprintf(labelAttemptFmt, strings.ToLower(t.Brand))
}

func (t *Triage) render(v *llm.Verdict, res *edits.Result, headline string) string {
	var b strings.Builder
	// Lead with the identity. A comment that opens with a verdict reads like
	// a colleague's review until you reach the footer, and the difference
	// matters most in the seconds before someone acts on it.
	if t.Brand != "" {
		mark := t.BrandMark
		if mark != "" {
			mark += " "
		}
		fmt.Fprintf(&b, "%s**%s**\n\n", mark, t.Brand)
	}
	fmt.Fprintf(&b, "%s\n\n", headline)
	fmt.Fprintf(&b, "**%s**\n\n%s\n", v.Summary, v.Reasoning)

	if res != nil && len(res.Applied) > 0 {
		b.WriteString("\n**Applied**\n\n| File | Key | From | To |\n|---|---|---|---|\n")
		for _, a := range res.Applied {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | `%s` |\n", a.Path, a.Key, a.From, a.To)
		}
	}
	if res != nil && len(res.Rejected) > 0 {
		b.WriteString("\n**Refused**\n\n")
		for _, r := range res.Rejected {
			fmt.Fprintf(&b, "- `%s` in `%s` — %s\n", r.Key, r.Path, r.Reason)
		}
	}
	brand := t.Brand
	if brand == "" {
		brand = "bosun"
	}
	fmt.Fprintf(&b, "\n<sub>%s · %s · automated triage, not a review</sub>\n", brand, t.LLM.Name())
	return b.String()
}

// waitForGate blocks until the gate reaches a verdict, the deadline passes, or
// the context is cancelled.
//
// MISSING IS TREATED AS PENDING, and that is the whole subtlety. Kargo calls
// this service from the promotion, immediately after opening the pull request
// -- measured at THREE SECONDS after, in the first triage that ever reached
// here. CI has not registered a check that early, so the check does not exist
// rather than existing and being pending, and the first version of this
// returned immediately and did nothing.
//
// From the caller's side those two states are the same thing: the gate has not
// answered yet. The only honest distinction is time, and the deadline already
// expresses it -- a check still missing after GateWait really is absent, and
// gets reported as such.
func (t *Triage) waitForGate(ctx context.Context, pr *gitprovider.PullRequest) (gitprovider.CheckState, error) {
	deadline := time.Now().Add(t.GateWait)
	for {
		state, err := t.Git.CheckStatus(ctx, pr.HeadSHA, t.CheckName)
		if err != nil {
			return gitprovider.CheckMissing, err
		}
		settled := state != gitprovider.CheckPending && state != gitprovider.CheckMissing
		if settled || time.Now().After(deadline) {
			return state, nil
		}
		select {
		case <-ctx.Done():
			return gitprovider.CheckPending, ctx.Err()
		case <-time.After(t.GatePoll):
		}
	}
}

// errNoGateReport is the gate having said nothing, as opposed to having said
// something this agent would not believe. The two are different situations
// with different answers -- one is a quiet gate, the other is a configuration
// mistake or an attempt -- and a caller that treats every failure here as
// "no report" turns the second into the first.
var errNoGateReport = errors.New("no gate report")

// gateReport finds the gate's own comment. A comment is the only artifact
// surface every git host has, which is why the gate publishes there rather
// than into a provider-specific artifact store.
//
// It is also, for the same reason, a surface anyone with write access can
// publish to. So the marker is necessary and not sufficient: the comment has
// to come from the account the operator named as the gate. Everything
// downstream -- which files the deterministic repair rewrites, which version
// strings the applier will corroborate, what the model is told rendered --
// is read out of this string, so a report the gate did not write is an
// instruction from a stranger wearing the gate's authority.
//
// The newest qualifying report wins. A gate that re-ran leaves two, and the
// stale one describes a commit that is no longer the head.
func (t *Triage) gateReport(ctx context.Context, pr *gitprovider.PullRequest) (string, error) {
	comments, err := t.Git.ListComments(ctx, pr.Number)
	if err != nil {
		return "", err
	}
	best := -1
	var untrusted []string
	for i, c := range comments {
		if !strings.Contains(c.Body, gateReportMarker) {
			continue
		}
		if !t.trustsReportFrom(c.Author) {
			untrusted = append(untrusted, c.Author)
			continue
		}
		// Newest wins, and position breaks the tie -- a host that did not
		// timestamp its comments leaves every CreatedAt zero, which is the
		// order-is-recency reading this had before.
		if best < 0 || !c.CreatedAt.Before(comments[best].CreatedAt) {
			best = i
		}
	}
	if best >= 0 {
		return comments[best].Body, nil
	}
	if len(untrusted) > 0 {
		// Named, because the overwhelmingly likely cause is not an attack but
		// a gate that publishes as somebody else -- and a reader can only fix
		// that if the message says whose name to put in the values file.
		return "", fmt.Errorf(
			"PR %d carries the gate's marker from %s, but %s is configured as the gate: "+
				"ignoring it. Set gate.reportAuthor to the account your gate comments as, "+
				"or to \"*\" to read the report whoever wrote it",
			pr.Number, strings.Join(dedupe(untrusted), ", "), quoted(t.GateReportAuthor))
	}
	return "", fmt.Errorf("%w: the gate is red but published no report comment on PR %d",
		errNoGateReport, pr.Number)
}

// trustsReportFrom is the whole of the check. Case-insensitive because git
// hosts are about usernames, and unset means unchecked -- which is a
// deployment saying it has no stable CI identity to name, not a bypass.
func (t *Triage) trustsReportFrom(author string) bool {
	want := strings.TrimSpace(t.GateReportAuthor)
	if want == "" || want == "*" {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(author), want)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			s = "an account the host did not name"
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func quoted(s string) string {
	if s == "" {
		return "nothing"
	}
	return "\"" + s + "\""
}

const gateReportMarker = "<!-- gitops-gate -->"

func (t *Triage) clone(ctx context.Context, pr *gitprovider.PullRequest) (string, func(), error) {
	root, err := os.MkdirTemp(t.CloneRoot, "pr")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }

	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", pr.Branch, t.RepoURL, root)
	var out strings.Builder
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("%w: %s", err, out.String())
	}
	return root, cleanup, nil
}

// buildUserPrompt assembles the evidence. The scalar inventory is the part
// that matters: handed one, a model selects a key from a list instead of
// inventing a path and paraphrasing a value.
func buildUserPrompt(p Promotion, pr *gitprovider.PullRequest, report, root string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "PULL REQUEST #%d: %s\n\n", pr.Number, pr.Title)
	fmt.Fprintf(&b, "Artifact %s moving %s -> %s (project %s, stage %s).\n\n",
		p.Artifact, p.From, p.To, p.Project, p.Stage)
	fmt.Fprintf(&b, "%s\n\n", report)

	files := append([]string{}, p.Files...)
	sort.Strings(files)
	b.WriteString("Repository files this pull request may change.\n")
	b.WriteString("Use these keys and values EXACTLY as written.\n\n")
	for _, f := range files {
		data, err := os.ReadFile(root + "/" + f)
		if err != nil {
			continue
		}
		inv, err := edits.Inventory(data, "")
		if err != nil {
			continue
		}
		b.WriteString(edits.Render(f, inv))
		b.WriteString("\n")
	}
	b.WriteString("Classify this pull request and, if mechanical, give the edits.")
	return b.String(), nil
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

// gateSaidNothingChanged is what the gate writes when the render is identical.
// Matching on it is how the agent avoids explaining a change that is not one.
const gateSaidNothingChanged = "No change to what gets deployed"

// explainGreen handles a gate that passed.
//
// A green gate is not the same as an uneventful change. The gate blocks on
// structural things -- targeting, sources, apiVersion migrations -- and REPORTS
// the rest: a chart that added four resources, moved a port, flipped a default.
// All of that renders green and arrives as a pull request whose visible diff is
// one version number.
//
// So this is the common case, not the boring one, and until now the agent
// stopped here and said nothing.
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
func (t *Triage) explainGreen(ctx context.Context, pr *gitprovider.PullRequest, p Promotion) error {
	if !t.Explain {
		t.say(ctx, pr, "%s is green; nothing to triage", t.CheckName)
		return nil
	}

	report, err := t.gateReport(ctx, pr)
	switch {
	case errors.Is(err, errNoGateReport):
		// A green gate that published no report is normal on repositories where
		// the gate only comments when it has something to say.
		t.say(ctx, pr, "%s is green; no report to explain", t.CheckName)
		return nil
	case err != nil:
		// Not the same thing, and it used to be reported as if it were. A
		// report this agent refused to read -- wrong author -- or a comment
		// list it could not finish is a fact about the deployment, and
		// "nothing to explain" is exactly the sentence that hides it.
		t.say(ctx, pr, "%s is green; %v", t.CheckName, err)
		return nil
	}
	if strings.Contains(report, gateSaidNothingChanged) {
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

	prompt, err := buildUserPrompt(p, pr, report, root)
	if err != nil {
		t.say(ctx, pr, "%s is green; could not build the explanation", t.CheckName)
		return nil
	}

	// What the maintainers said, if it can be found. Never fatal, and never
	// silent about its absence: an explanation with no upstream context has to
	// say so, or a reader credits it with evidence it does not have.
	notes := t.upstreamNotes(ctx, p)
	prompt += renderNotes(notes)

	v, err := t.LLM.Classify(ctx, explainPrompt, prompt)
	if err != nil {
		// Explaining is a courtesy. A model that is down must not be the reason
		// a green pull request looks unattended.
		t.say(ctx, pr, "%s is green; could not reach the model to explain it", t.CheckName)
		return nil
	}

	// A green gate is a verdict on the render, not on the bump. This path may
	// now ask for a human -- it blocks nothing, since the commit status is
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
		if err := t.Git.Comment(ctx, pr.Number, t.renderExplanation(v, notes)); err != nil {
			return err
		}
		return t.Git.AddLabel(ctx, pr.Number, labelNeedsHuman)
	}

	t.say(ctx, pr, "%s is green: %s", t.CheckName, v.Summary)
	return t.Git.Comment(ctx, pr.Number, t.renderExplanation(v, notes))
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

// renderExplanation is deliberately shorter than render(). Nothing was changed
// and nothing was refused, so there are no tables to show -- and a long comment
// on a green pull request is the fastest way to teach people to ignore this
// agent entirely.
func (t *Triage) renderExplanation(v *llm.Verdict, notes *upstream.Notes) string {
	var b strings.Builder
	b.WriteString(explanationMarker + "\n")
	if t.Brand != "" {
		mark := t.BrandMark
		if mark != "" {
			mark += " "
		}
		fmt.Fprintf(&b, "%s**%s**\n\n", mark, t.Brand)
	}
	if v.Classification == llm.ClassEscalate {
		// The marker alone, before the summary: a reader who stops at the
		// first bold line must still learn this one wants their eyes. The
		// escalation reason itself stays on the commit status -- it is
		// reliably a paraphrase of the summary printed on the next line, and
		// printing both was the agent announcing itself twice.
		b.WriteString("**Worth a look before merging.**\n\n")
	}
	fmt.Fprintf(&b, "**%s**\n\n%s\n\n", v.Summary, v.Reasoning)
	// Provenance, always. A reader deciding how much to trust this needs to
	// know whether it had the maintainers' own words or only the render.
	b.WriteString("---\n")
	if notes.Any() {
		fmt.Fprintf(&b, "_Grounded in the gate's render diff and %d upstream release note(s)", len(notes.Releases))
		if notes.SourceRepo != "" {
			fmt.Fprintf(&b, " from [%s](https://github.com/%s/releases)", notes.SourceRepo, notes.SourceRepo)
		}
		if notes.Truncated {
			b.WriteString(", truncated")
		}
		fmt.Fprintf(&b, ". Explained by %s._\n", t.LLM.Name())
	} else {
		reason := "no upstream release notes were read"
		if notes != nil && notes.Note != "" {
			reason = strings.TrimSuffix(strings.TrimPrefix(notes.Note, "No upstream release notes: "), ".")
		}
		fmt.Fprintf(&b, "_Grounded in the gate's render diff ONLY -- %s. Explained by %s._\n",
			reason, t.LLM.Name())
	}
	return b.String()
}

// upstreamNotes never fails the explanation. A resolver that is misconfigured,
// rate-limited or looking at an artifact with no source label produces an
// explanation grounded in the render alone, which is the behaviour that existed
// before this and is still useful.
func (t *Triage) upstreamNotes(ctx context.Context, p Promotion) *upstream.Notes {
	if t.Upstream == nil {
		return &upstream.Notes{Note: "upstream lookup is not configured"}
	}
	n, err := t.Upstream.Notes(ctx, p.Artifact, p.From, p.To)
	if err != nil || n == nil {
		return &upstream.Notes{Note: fmt.Sprintf("upstream lookup failed (%v)", err)}
	}
	return n
}

// renderNotes puts the maintainers' words in the prompt, clearly labelled as
// TESTIMONY rather than as the render. The distinction is the point: the gate
// report is computed and the release notes are claimed, and an explanation that
// blurs them will state an intention as an outcome.
func renderNotes(n *upstream.Notes) string {
	var b strings.Builder
	b.WriteString("\n\nUPSTREAM RELEASE NOTES\n\n")
	if !n.Any() {
		fmt.Fprintf(&b, "None. %s\n\nSay what the render changed and do not supply a reason.\n", n.Note)
		return b.String()
	}
	fmt.Fprintf(&b, "%s What the maintainers wrote, newest first. This is what they SAY they\n"+
		"changed; the gate report above is what actually rendered.\n\n", n.Note)
	for _, r := range n.Releases {
		title := r.Tag
		if r.Name != "" && r.Name != r.Tag {
			title = r.Tag + " -- " + r.Name
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n\n", title, r.Body)
	}
	return b.String()
}

// checkout is the working copy, however the caller supplies it.
func (t *Triage) checkout(ctx context.Context, pr *gitprovider.PullRequest) (string, func(), error) {
	if t.Checkout != nil {
		return t.Checkout(ctx, pr)
	}
	return t.clone(ctx, pr)
}
