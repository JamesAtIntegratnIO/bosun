package gateservice

import (
	"fmt"
	"strings"
)

// The verdict history the gate keeps inside its own comment.
//
// A gate with no database still has to answer "what did I say last time on
// this pull request", and the only per-pull-request storage every git host
// offers is the comment itself. So the answers go in as HTML comments,
// invisible in every markdown surface, and are read back on the next run.
//
// Split from gateservice.go, which is the poll loop and the verdict: this is a
// Format, with a writer and a reader that must not drift, and it has nothing
// to do with when the gate runs.

// Stamps the gate leaves in its own comment so the next run can read what the
// last one said. HTML comments: invisible in every markdown surface, and the
// only per-pull-request memory a gate with no database has.
//
// Exported because they are already public in the strongest sense -- they are
// published into pull-request comments, and the agent must be able to prove a
// model's prose can never forge one. agent/comment_test.go used to re-declare
// all three by hand, so changing a stamp here left that test green while it
// protected a string nothing wrote any more.
const (
	StampHead    = "<!-- gitops-gate:head "
	StampVerdict = "<!-- gitops-gate:verdict "
	StampWas     = "<!-- gitops-gate:was "
)

// MaxHistory caps the remembered verdicts. Ten is far more than any pull
// request needs and stops a long-lived one growing a comment without bound.
//
// Exported because a reader of the history has to be told the cap to read a
// short one correctly: ten rows and a cap of ten is a history that has been
// truncated, and ten rows with no cap beside them is a pull request that might
// have had eleven. It travels on the snapshot rather than being looked up, so
// the number a client is given is the number this build applied.
const MaxHistory = 10

// VerdictRow is one past answer on this pull request.
//
// Exported for the same reason the stamps above are: the rows parsed out of
// the comment no longer die with the publish that parsed them. They ride the
// sweep's snapshot to the read surfaces, which is where a flapping gate stops
// being a person's impression of a comment and becomes data.
type VerdictRow struct {
	SHA      string
	Blocking bool
	Headline string
}

func boolDigit(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// parseHistory reads the rows a previous body recorded.
func parseHistory(body string) []VerdictRow {
	var out []VerdictRow
	for _, line := range strings.Split(body, "\n") {
		if row, ok := parseStampedRow(line, StampWas, true); ok {
			out = append(out, row)
		}
	}
	return out
}

// currentAsRow turns the body's own verdict into a history row, which is what
// makes the failed pass survive being edited over.
func currentAsRow(body string) []VerdictRow {
	var sha string
	for _, line := range strings.Split(body, "\n") {
		if rest, ok := trimStamp(line, StampHead); ok {
			sha = strings.TrimSpace(rest)
			break
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if row, ok := parseStampedRow(line, StampVerdict, false); ok {
			row.SHA = sha
			if row.SHA == "" {
				return nil
			}
			return []VerdictRow{row}
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
func parseStampedRow(line, stamp string, withSHA bool) (VerdictRow, bool) {
	rest, ok := trimStamp(line, stamp)
	if !ok {
		return VerdictRow{}, false
	}
	// A row is "<blocking> <headline>", or "<sha> <blocking> <headline>" for a
	// historical one. The headline contains spaces, so the split has to stop
	// counting at the last structured field.
	parts := 2
	if withSHA {
		parts = 3
	}
	fields := strings.SplitN(strings.TrimSpace(rest), " ", parts)
	if len(fields) < 2 {
		return VerdictRow{}, false
	}
	var row VerdictRow
	if withSHA {
		if len(fields) < 3 {
			return VerdictRow{}, false
		}
		row.SHA, fields = fields[0], fields[1:]
	}
	row.Blocking = fields[0] == "1"
	row.Headline = strings.TrimSpace(fields[1])
	return row, row.Headline != ""
}

// renderHistory is the visible half. Collapsed, because on a healthy pull
// request it is noise, and on a repaired one it is the whole story.
func renderHistory(history []VerdictRow) string {
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
		fmt.Fprintf(&b, "| `%s` | %s %s |\n", shortSHA8(h.SHA), mark, h.Headline)
	}
	fmt.Fprintf(&b, "\n</details>\n")
	return b.String()
}
