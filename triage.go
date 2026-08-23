package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
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
	// MaxAttempts caps self-fixes per pull request. Enforced through labels,
	// so it survives a restart -- in-memory state would reset the cap every
	// time the pod moved.
	MaxAttempts int
	// GateWait is how long to wait for the gate to reach a verdict before
	// giving up on this run.
	GateWait  time.Duration
	GatePoll  time.Duration
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
	desc := fmt.Sprintf(format, a...)
	t.logf("PR %d: %s", pr.Number, desc)
	if err := t.Git.SetCommitStatus(ctx, pr.HeadSHA, t.statusName(), desc); err != nil {
		t.logf("PR %d: could not set the %q status (needs Commit statuses: read+write): %v",
			pr.Number, t.statusName(), err)
	}
}

// Run is the whole workflow. Errors are returned for logging; the caller has
// already answered Kargo, so nothing here can fail a promotion.
func (t *Triage) Run(ctx context.Context, p Promotion) error {
	pr, err := t.Git.GetPullRequest(ctx, p.PRNumber)
	if err != nil {
		return fmt.Errorf("reading PR %d: %w", p.PRNumber, err)
	}

	if has(pr.Labels, labelNeedsHuman) {
		t.say(ctx, pr, "already escalated; leaving it to a human")
		return nil
	}
	// Say so before the first thing that can block. waitForGate can sit for ten
	// minutes, and a reader in that window should see the agent working rather
	// than an absence they cannot distinguish from never having been called.
	t.say(ctx, pr, "reading %s", t.CheckName)

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
		t.say(ctx, pr, "%s is green; nothing to triage", t.CheckName)
		return nil
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
		return t.escalate(ctx, pr, verdict.EscalationReason, verdict)
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
func (t *Triage) escalateWith(ctx context.Context, pr *gitprovider.PullRequest, reason string, v *llm.Verdict, res *edits.Result) error {
	body := "### Needs a human\n\n" + reason + "\n"
	if v != nil {
		body = t.render(v, res, "**Needs a human.** "+reason)
	}
	if err := t.Git.Comment(ctx, pr.Number, body); err != nil {
		return err
	}
	return t.Git.AddLabel(ctx, pr.Number, labelNeedsHuman)
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

// gateReport finds the gate's own comment. A comment is the only artifact
// surface every git host has, which is why the gate publishes there rather
// than into a provider-specific artifact store.
func (t *Triage) gateReport(ctx context.Context, pr *gitprovider.PullRequest) (string, error) {
	comments, err := t.Git.ListComments(ctx, pr.Number)
	if err != nil {
		return "", err
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if strings.Contains(comments[i].Body, gateReportMarker) {
			return comments[i].Body, nil
		}
	}
	return "", fmt.Errorf("the gate is red but published no report comment on PR %d", pr.Number)
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
