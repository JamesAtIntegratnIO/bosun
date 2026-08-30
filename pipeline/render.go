package pipeline

import (
	"fmt"
	"io"
	"strings"
)

// ReportMarker leads the rendered report, for the same reason the gate's does:
// whatever publishes this, a comment, an issue, a job summary, finds its own
// previous copy by looking for this string, and an adapter that has to
// remember a magic string is an adapter that will forget it.
const ReportMarker = "<!-- bosun:pipeline -->"

// Mark is the severity's glyph, exported because the web page and the
// markdown report must agree on it or the same finding reads differently on
// two surfaces.
func (s Severity) Mark() string {
	switch s {
	case Blocking:
		return "🔴"
	case Degraded:
		return "🟠"
	default:
		return "·"
	}
}

// Headline is the one line to put in a commit status, a log, or a chat
// message. It states the situation, never the mechanism.
func (r *Report) Headline() string {
	if r.Checked.Stages == 0 {
		return "Pipeline not checked — no Stages were read"
	}
	if len(r.Findings) == 0 {
		return fmt.Sprintf("Pipeline healthy — %s, %s and %s checked",
			plural(r.Checked.Stages, "Stage"),
			plural(r.Checked.Warehouses, "Warehouse"),
			plural(r.Checked.Promotions, "promotion"))
	}
	counts := map[Severity]int{}
	for _, f := range r.Findings {
		counts[f.Severity]++
	}
	var parts []string
	if n := counts[Blocking]; n > 0 {
		parts = append(parts, fmt.Sprintf("%s not delivering", plural(n, "Stage")))
	}
	if n := counts[Degraded]; n > 0 {
		parts = append(parts, plural(n, "thing")+" degraded")
	}
	if n := counts[Note]; n > 0 {
		parts = append(parts, plural(n, "note"))
	}
	return r.Worst().Mark() + " Promotion pipeline — " + joinAnd(parts)
}

// Render writes the whole report as markdown.
//
// Shaped around one belief: the reader is looking at this because something
// they did not know about has been true for a while, and the thing they most
// need is not a description of the problem but the command that ends it. So
// every finding puts its remedy in a fenced block, ready to paste, and nothing
// is written that a reader would have to translate into an action themselves.
func (r *Report) Render(w io.Writer) {
	fmt.Fprintf(w, "%s\n", ReportMarker)
	fmt.Fprintf(w, "## %s\n\n", r.Headline())

	if len(r.Findings) == 0 {
		if r.Checked.Stages == 0 {
			fmt.Fprintf(w, "Nothing was read, so nothing is claimed. This is not a clean bill of health.\n\n")
		} else {
			fmt.Fprintf(w, "Every Stage has promoted its latest freight, every Warehouse is discovering "+
				"on schedule, and every tracked pin writes to a key that exists.\n\n")
		}
		r.renderChecked(w)
		return
	}

	// Blocking findings first and separately: a reader who stops after the
	// first section must have read everything that is stopped.
	bySeverity := map[Severity][]Finding{}
	for _, f := range r.Findings {
		bySeverity[f.Severity] = append(bySeverity[f.Severity], f)
	}
	for _, sev := range []Severity{Blocking, Degraded, Note} {
		fs := bySeverity[sev]
		if len(fs) == 0 {
			continue
		}
		fmt.Fprintf(w, "### %s %s\n\n", sev.Mark(), sev.SectionTitle())
		fmt.Fprintf(w, "%s\n\n", sev.SectionBlurb())
		for _, f := range fs {
			r.renderFinding(w, f)
		}
	}
	r.renderChecked(w)
}

// SectionTitle and SectionBlurb are the report's section copy, exported for
// the same reason Mark is: two renderings of one report should not have two
// vocabularies.
func (s Severity) SectionTitle() string {
	switch s {
	case Blocking:
		return "Not delivering"
	case Degraded:
		return "Working, but wrong"
	default:
		return "Worth knowing"
	}
}

func (s Severity) SectionBlurb() string {
	switch s {
	case Blocking:
		return "These have stopped, and will not start again on their own. " +
			"Every Application involved is still Synced and Healthy on the older version, " +
			"which is why nothing else has mentioned it."
	case Degraded:
		return "These still run. They are producing an effect other than the one intended, " +
			"and will keep doing so quietly."
	default:
		return "No action today."
	}
}

func (r *Report) renderFinding(w io.Writer, f Finding) {
	fmt.Fprintf(w, "#### %s\n\n", f.Summary)
	if d := strings.TrimSpace(f.Detail); d != "" {
		fmt.Fprintf(w, "%s\n\n", d)
	}
	if rem := strings.TrimSpace(f.Remedy); rem != "" {
		fmt.Fprintf(w, "```bash\n%s\n```\n\n", rem)
	}
}

// renderChecked is the honesty section, and it is not optional.
//
// This package's entire subject is the difference between "nothing is wrong"
// and "nobody looked". A report that printed findings and stopped would be
// making exactly the mistake it exists to catch.
func (r *Report) renderChecked(w io.Writer) {
	c := r.Checked
	fmt.Fprintf(w, "---\n\n<sub>Checked %s, %s, %s, %s",
		plural(c.Stages, "Stage"),
		plural(c.Warehouses, "Warehouse"),
		plural(c.Promotions, "promotion"),
		plural(c.PullRequests, "open pull request"))
	if c.PinsScanned > 0 {
		fmt.Fprintf(w, " and %s", plural(c.PinsScanned, "tracked pin"))
	}
	if len(r.Namespaces) > 0 {
		fmt.Fprintf(w, " in %s", strings.Join(r.Namespaces, ", "))
	}
	fmt.Fprintf(w, ".</sub>\n")
	for _, n := range c.Notes {
		fmt.Fprintf(w, "\n<sub>⚠ %s</sub>\n", n)
	}
}

// Text renders the report for a terminal: same content, no markdown, because
// the CLI is where an operator reads this while fixing it.
func (r *Report) Text(w io.Writer) {
	fmt.Fprintf(w, "%s\n\n", r.Headline())
	for _, f := range r.Findings {
		fmt.Fprintf(w, "%s %s\n", f.Severity.Mark(), f.Summary)
		for _, line := range strings.Split(strings.TrimSpace(f.Detail), "\n") {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
		if rem := strings.TrimSpace(f.Remedy); rem != "" {
			fmt.Fprintln(w)
			for _, line := range strings.Split(rem, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
		}
		fmt.Fprintln(w)
	}
	c := r.Checked
	fmt.Fprintf(w, "checked %d stages, %d warehouses, %d promotions, %d open PRs, %d pins\n",
		c.Stages, c.Warehouses, c.Promotions, c.PullRequests, c.PinsScanned)
	for _, n := range c.Notes {
		fmt.Fprintf(w, "warning: %s\n", n)
	}
}
