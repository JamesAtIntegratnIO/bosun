package main

import (
	"fmt"
	"strings"
)

// The verdict history the gate keeps inside its own comment.
//
// A gate with no database still has to answer "what did I say last time on
// this pull request", and the only per-pull-request storage every git host
// offers is the comment itself. So the answers go in as HTML comments --
// invisible in every markdown surface -- and are read back on the next run.
//
// Split from gateservice.go, which is the poll loop and the verdict: this is a
// FORMAT, with a writer and a reader that must not drift, and it has nothing
// to do with when the gate runs.

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
		fmt.Fprintf(&b, "| `%s` | %s %s |\n", shortSHA8(h.SHA), mark, h.Headline)
	}
	fmt.Fprintf(&b, "\n</details>\n")
	return b.String()
}
