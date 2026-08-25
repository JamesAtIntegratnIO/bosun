package agent

import (
	"fmt"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// The pull-request comment: everything the agent writes for a human to read.
//
// Split from triage.go, which is the workflow -- decide, repair, escalate,
// publish. Rendering is the largest single thing that file did and the one
// least related to the rest of it, and none of these functions is a method:
// they take the brand and the model name and nothing else, so the report text
// can be changed and tested without standing up a Triage.

// renderMigration is the comment for the deterministic path. Its footer names
// no model, because none was involved -- a reader deciding how much to trust
// this needs to know it is arithmetic, not judgement.
// renderMigration builds the migration comment.
//
// A package function taking the two strings it needs, not a method: the report
// text is the agent's most-read output and it should be changeable and
// testable without standing up a 28-field Triage.
func renderMigration(brand, model string, drops []migrate.Dropped, res *migrate.Result, rr *restructureResult,
	headline string, live *liveFacts) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", headline)
	b.WriteString("**The chart stopped serving API versions this repository still declares.** " +
		"The gate named the versions and the one that survives; every declaring manifest moves there.\n\n")
	for _, d := range drops {
		fmt.Fprintf(&b, "- `%s`: `%s/{%s}` → `%s/%s`\n",
			d.Kind, d.Group, strings.Join(d.Versions, ", "), d.Group, d.Target)
	}
	if len(res.Applied) > 0 {
		// Collapsed, because it is the commit's file list written out a second
		// time. Twenty-seven rows of it pushed the live-cluster facts -- the
		// part only this agent can supply -- off the bottom of the comment.
		files := map[string]bool{}
		for _, a := range res.Applied {
			files[a.Path] = true
		}
		fmt.Fprintf(&b, "\n<details><summary><b>Migrated</b> — %s, listed</summary>\n\n",
			countOf(len(files), "file"))
		b.WriteString("\n| File | Kind | To |\n|---|---|---|\n")
		for _, a := range res.Applied {
			fmt.Fprintf(&b, "| `%s` | `%s` | `%s` |\n", a.Path, a.Kind, a.To)
		}
		b.WriteString("\n</details>\n")
	}
	if len(res.Refused) > 0 {
		b.WriteString("\n**Refused**\n\n")
		for _, r := range res.Refused {
			fmt.Fprintf(&b, "- `%s` — %s\n", r.Path, r.Reason)
		}
	}
	b.WriteString(renderRestructured(rr))
	b.WriteString(renderLive(live))
	// The footer says honestly which kind of work this was. "No model" is a
	// claim a reader uses to decide how carefully to look, and it stops being
	// true the moment a document was reshaped.
	how := "deterministic repair, no model"
	if rr != nil && rr.ModelCalls > 0 {
		how = "schema-guided migration · " + model
	}
	fmt.Fprintf(&b, "\n<sub>%s · %s · automated triage, not a review</sub>\n", brand, how)
	return b.String()
}

// renderRestructured shows exactly what a model reshaped, and what it could
// not.
//
// The diff is folded but present. A reader who trusts the harness never opens
// it; a reader who does not can see every line that moved, which is the only
// basis on which trusting it is reasonable.
func renderRestructured(rr *restructureResult) string {
	if rr == nil || (!rr.touched() && len(rr.Skipped) == 0 && len(rr.Provenance) == 0) {
		return ""
	}
	var b strings.Builder
	if len(rr.Applied) > 0 {
		b.WriteString("\n**Reshaped for the new schema**\n\n")
		b.WriteString("The chart moved fields between these versions, so swapping the version alone " +
			"would have left manifests the new schema does not accept. Each document below was " +
			"proposed by the model and then checked: identity unchanged, valid against the new " +
			"schema, and every value present in the original or dictated by the schema itself.\n\n")
		for _, a := range rr.Applied {
			fmt.Fprintf(&b, "<details><summary><code>%s</code> — %s/%s", a.Path, a.Kind, a.Name)
			if a.Notes != "" {
				fmt.Fprintf(&b, " — %s", a.Notes)
			}
			b.WriteString("</summary>\n\n")
			for _, f := range a.Reasons {
				fmt.Fprintf(&b, "- %s\n", f)
			}
			fmt.Fprintf(&b, "\n```diff\n%s```\n", a.Diff)
			// Respelled BEFORE lost, and named differently, because they are
			// different news. A value the target schema spells another way
			// survived; a value with nowhere to go did not. Printed together
			// under one heading, the first kind made the migration look lossy
			// on exactly the bumps where it had done its job.
			if len(a.Respelled) > 0 {
				fmt.Fprintf(&b, "\n**Respelled by the new schema:** `%s`\n", strings.Join(a.Respelled, "`, `"))
			}
			if len(a.Lost) > 0 {
				fmt.Fprintf(&b, "\n**Values not carried across:** `%s`\n", strings.Join(a.Lost, "`, `"))
			}
			b.WriteString("\n</details>\n")
		}
	}
	if len(rr.Refused) > 0 {
		b.WriteString("\n**Refused before anything was written**\n\n")
		for _, r := range rr.Refused {
			fmt.Fprintf(&b, "- `%s` — %s/%s\n", r.Path, r.Kind, r.Name)
			for _, w := range r.Why {
				fmt.Fprintf(&b, "  - %s\n", w)
			}
			for _, f := range r.Findings {
				fmt.Fprintf(&b, "  - still does not fit: %s\n", f)
			}
		}
	}
	if len(rr.Skipped) > 0 {
		b.WriteString("\n**Not checked for structural changes**\n\n")
		for _, s := range rr.Skipped {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	if len(rr.Provenance) > 0 {
		b.WriteString("\n**Which schema the check used**\n\n")
		for _, s := range rr.Provenance {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}
	return b.String()
}

// countOf is "1 file" / "27 files".
func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// noRemedyReason states, from the gate's own count, exactly why no fix was
// attempted. A reader's next question after "needs a human" is always "what
// would I even change?", and the honest answer here is nothing in this
// repository -- so the comment leads with that instead of implying a search.
func noRemedyReason(b migrate.Blockers) string {
	var what string
	switch {
	case b.APIVersion == 1:
		what = "An object this chart renders has moved to a new apiVersion."
	case b.APIVersion > 1:
		what = fmt.Sprintf("%d objects this chart renders have moved to new apiVersions.", b.APIVersion)
	default:
		what = "The gate is blocking."
	}
	return what + "\n\n" +
		"**Nothing in this repository declares them, so there is nothing here to rewrite** — " +
		"no manifest to migrate and no value to change. The move is the chart's own, and it " +
		"will apply when this merges.\n\n" +
		"What is worth checking before it does: that the new apiVersion is served by this " +
		"cluster, and that anything outside this repository which addresses those objects " +
		"still can. The gate blocks on this class because it renders perfectly and can break " +
		"at runtime — not because it found something you can edit."
}

// render builds the pull-request comment. It always states which model
// produced the verdict, and always lists what was refused -- a silent refusal
// would let a reader believe a fix was applied when it was not.
func render(brand, model string, v *llm.Verdict, res *edits.Result, headline string) string {
	var b strings.Builder
	// No identity header. It was here because the agent used to comment as
	// whoever owned its token -- a person -- so a comment opening with a
	// verdict read like a colleague's review. Authenticating as a GitHub App
	// moved identity into the avatar and the name the host renders above every
	// comment, which says it earlier and more reliably than a bold line could.
	// The footer still records the provenance, which is the half a reader
	// actually needs and the host cannot supply.
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
	fmt.Fprintf(&b, "\n<sub>%s · %s · automated triage, not a review</sub>\n", brand, model)
	return b.String()
}

func quoted(s string) string {
	if s == "" {
		return "nothing"
	}
	return "\"" + s + "\""
}

// renderExplanation is deliberately shorter than render(). Nothing was changed
// and nothing was refused, so there are no tables to show -- and a long comment
// on a green pull request is the fastest way to teach people to ignore this
// agent entirely.
func renderExplanation(model string, v *llm.Verdict, notes *upstream.Notes) string {
	var b strings.Builder
	b.WriteString(explanationMarker + "\n")
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
	var c *upstream.Compare
	if notes != nil {
		c = notes.Compare
	}
	switch {
	case notes.Any():
		// "release note" is no longer accurate for every case: the entry may
		// have come from a changelog file, which is written in the same commit
		// as the change rather than at the moment of release. A reader
		// weighing an explanation should be told which they got.
		what := "upstream release note(s)"
		if notes.Origin != "" && notes.Origin != "releases" {
			what = fmt.Sprintf("upstream changelog entr(ies) from `%s`", notes.Origin)
		}
		fmt.Fprintf(&b, "_Grounded in the gate's render diff and %d %s", len(notes.Releases), what)
		if notes.SourceRepo != "" && (notes.Origin == "" || notes.Origin == "releases") {
			fmt.Fprintf(&b, " from [%s](https://github.com/%s/releases)", notes.SourceRepo, notes.SourceRepo)
		} else if notes.SourceRepo != "" {
			fmt.Fprintf(&b, " in [%s](https://github.com/%s)", notes.SourceRepo, notes.SourceRepo)
		}
		if notes.Truncated {
			b.WriteString(", truncated")
		}
		b.WriteString(commitProvenance(c))
		fmt.Fprintf(&b, ". Explained by %s._\n", model)
	case c.Any():
		// The case this feature was built for: no release note explains the
		// finding, and the commits do. The provenance has to say which of the
		// two it had, or a reader credits the explanation with the wrong one.
		fmt.Fprintf(&b, "_Grounded in the gate's render diff%s -- "+
			"no release note in this range explains it. Explained by %s._\n",
			commitProvenance(c), model)
	default:
		reason := "no upstream release notes were read"
		if notes != nil && notes.Note != "" {
			reason = strings.TrimSuffix(strings.TrimPrefix(notes.Note, "No upstream release notes: "), ".")
		}
		fmt.Fprintf(&b, "_Grounded in the gate's render diff ONLY -- %s. Explained by %s._\n",
			reason, model)
	}
	return b.String()
}

// commitProvenance says what the commit range actually contributed.
//
// "0 upstream commit(s) in v0.13.1...v0.13.2" was the first wording, and it
// reads as THE RANGE WAS EMPTY. It was not: there were two commits and neither
// mentioned what the gate found, which is a different statement and a more
// useful one -- it says the maintainers did work and none of it explains this.
//
// The same class of error as a fully-read range calling itself truncated. A
// provenance line's whole job is being exact about what the evidence was, so a
// number in it that reads as the wrong fact is worse than no number.
func commitProvenance(c *upstream.Compare) string {
	switch {
	case c == nil:
		return ""
	case len(c.Relevant) > 0:
		out := fmt.Sprintf(", and %d upstream commit(s) in `%s`", len(c.Relevant), c.Range)
		if c.Capped {
			out += " (the first few)"
		}
		return out
	case len(c.Files) > 0:
		// The commits said nothing and the DIFF said something. That is the
		// ordinary shape of the case this feature was built for: a commit
		// titled "watch namespaces via config" does not contain the string
		// ClusterRole, and the template it deleted does. Claiming nothing
		// mentions it here would disown the evidence the explanation used.
		return fmt.Sprintf(", and %d file(s) in the upstream diff for `%s`", len(c.Files), c.Range)
	case c.Total > 0 && c.Truncated:
		// "None of the 1896 mentions it" claims a search nobody ran: GitHub
		// answers a compare with at most 250 commits, so the filter saw a
		// fraction of them. Same rule as everywhere else here -- a provenance
		// line may not describe evidence it did not have.
		return fmt.Sprintf(", and none of the %d commit(s) read from `%s` (of %d) mentions it",
			upstream.CompareReadCap, c.Range, c.Total)
	case c.Total > 0:
		return fmt.Sprintf(", and none of the %d commit(s) in `%s` mentions it", c.Total, c.Range)
	default:
		return ""
	}
}

// renderUpstream is the "what upstream says" half of a handoff comment.
//
// Rendered only where a human is about to spend time: an escalation, and a
// green gate flagged for a second look. An ordinary green-gate explanation
// FETCHES the commits -- they are what it is grounded in -- and does not print
// them, because a comment nobody needed to act on is how this agent becomes
// something people collapse.
//
// The mechanical path neither renders nor fetches. An edit's evidence is the
// gate report and nothing else, and a commit message that mentions a version
// number must not become corroboration for writing one.
func renderUpstream(n *upstream.Notes) string {
	if n == nil {
		return ""
	}
	c := n.Compare
	if !c.Any() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n**What upstream says**\n\n")
	if c.URL != "" {
		fmt.Fprintf(&b, "Between [`%s`](%s)", c.Range, c.URL)
	} else {
		fmt.Fprintf(&b, "Between `%s`", c.Range)
	}
	if c.Total > 0 {
		fmt.Fprintf(&b, ", %d commit(s)", c.Total)
		if c.Truncated {
			b.WriteString(" (more than could be read)")
		}
		if c.Capped {
			b.WriteString(", showing the first few")
		}
	}
	b.WriteString(" — these mention what the gate found:\n\n")
	for _, cm := range c.Relevant {
		if cm.URL != "" {
			fmt.Fprintf(&b, "- [`%s`](%s) %s\n", cm.SHA, cm.URL, cm.Message)
		} else {
			fmt.Fprintf(&b, "- `%s` %s\n", cm.SHA, cm.Message)
		}
	}
	if len(c.Files) > 0 {
		b.WriteString("\nFiles the upstream diff touched that name the same things:\n\n")
		for _, f := range c.Files {
			fmt.Fprintf(&b, "- `%s`\n", f)
		}
	}
	b.WriteString("\n<sub>Commit messages are testimony, not the render. " +
		"They say what the maintainers meant to change.</sub>\n")
	return b.String()
}
