package pipeline

import (
	"strings"
	"testing"
	"time"
)

// The report is read by somebody who has just learned that something they did
// not know about has been true for a while. The thing they most need is not a
// description of the problem but the command that ends it, so every finding
// puts its remedy in a fenced block, and nothing is written that a reader
// would have to translate into an action themselves.

func TestHeadlineSeparatesNotCheckedFromHealthy(t *testing.T) {
	// Zero Stages read is not a healthy pipeline: reporting "healthy" for a
	// sweep that saw nothing is the most expensive thing a monitor can say.
	notChecked := (&Report{At: now}).Headline()
	if !strings.Contains(notChecked, "not checked") {
		t.Errorf("got %q", notChecked)
	}

	healthy := (&Report{At: now, Checked: Checked{Stages: 3, Warehouses: 2, Promotions: 9}}).Headline()
	if !strings.Contains(healthy, "healthy") {
		t.Errorf("got %q", healthy)
	}
	// It says what it checked, so "healthy" is a claim with a scope.
	for _, want := range []string{"3 Stages", "2 Warehouses", "9 promotions"} {
		if !strings.Contains(healthy, want) {
			t.Errorf("the headline must state its scope, %q missing from %q", want, healthy)
		}
	}
}

func TestHeadlineCountsBySeverity(t *testing.T) {
	got := (&Report{
		At:      now,
		Checked: Checked{Stages: 3},
		Findings: []Finding{
			{Kind: KindWedged, Severity: Blocking, Subject: "a"},
			{Kind: KindWedged, Severity: Blocking, Subject: "b"},
			{Kind: KindDeadPin, Severity: Degraded, Subject: "c"},
		},
	}).Headline()

	if !strings.Contains(got, "2 Stages not delivering") {
		t.Errorf("blocking findings lead: %q", got)
	}
	if !strings.Contains(got, "1 thing degraded") {
		t.Errorf("got %q", got)
	}
	// Singular and plural both read as English, because a headline that says
	// "1 Stages" reads like nobody proof-read the tool.
	one := (&Report{At: now, Checked: Checked{Stages: 1}, Findings: []Finding{
		{Kind: KindWedged, Severity: Blocking, Subject: "a"},
	}}).Headline()
	if strings.Contains(one, "1 Stages") {
		t.Errorf("got %q", one)
	}
}

// The remedy is the point. A finding without one is a problem statement, and a
// reader has to go and work out the command themselves.
func TestRenderPutsEveryRemedyInAPasteableBlock(t *testing.T) {
	var sb strings.Builder
	(&Report{
		At:      now,
		Checked: Checked{Stages: 1},
		Findings: []Finding{{
			Kind: KindVerifyStuck, Severity: Blocking, Subject: "cert-manager",
			Since:   72 * time.Hour,
			Summary: "cert-manager stopped promoting",
			Detail:  "the verification ended and nothing retries",
			Remedy:  "kubectl -n addons annotate stage cert-manager reverify=x",
		}},
	}).Render(&sb)

	got := sb.String()
	if !strings.Contains(got, "cert-manager stopped promoting") {
		t.Errorf("the summary must be present:\n%s", got)
	}
	if !strings.Contains(got, "```") || !strings.Contains(got, "kubectl -n addons annotate") {
		t.Errorf("the remedy must be fenced and pasteable:\n%s", got)
	}
	// Detail sits between the two, because a remedy with no explanation is a
	// command somebody runs without knowing what it does.
	if !strings.Contains(got, "the verification ended and nothing retries") {
		t.Errorf("the detail must be present:\n%s", got)
	}
}

// Since is carried on the Finding and rendered nowhere: each detector formats
// the age into its own Summary, because "for three days" belongs in the
// sentence rather than in a field beside it. It reaches a scrape through
// Metrics instead, which is the consumer that wants it as a number.
func TestTheAgeReachesAReaderThroughTheSummaryAndAScrapeThroughMetrics(t *testing.T) {
	r := &Report{At: now, Checked: Checked{Stages: 1}, Findings: []Finding{{
		Kind: KindVerifyStuck, Severity: Blocking, Subject: "cert-manager",
		Since: 72 * time.Hour, Summary: "cert-manager stopped promoting 3d ago",
	}}}

	var md strings.Builder
	r.Render(&md)
	if !strings.Contains(md.String(), "3d ago") {
		t.Errorf("the age reaches the reader through the summary:\n%s", md.String())
	}

	var metrics strings.Builder
	r.Metrics(&metrics)
	if !strings.Contains(metrics.String(), "259200") {
		t.Errorf("and a scrape through the age metric, in seconds:\n%s", metrics.String())
	}
}

// A sweep that could not look at something says so on the report, because
// "we did not check" and "we checked and it is fine" are different answers.
func TestRenderCarriesTheNotesAboutWhatWasNotChecked(t *testing.T) {
	var sb strings.Builder
	(&Report{
		At: now,
		Checked: Checked{
			Stages: 1,
			Notes:  []string{"pins were not checked: no checkout was available"},
		},
	}).Render(&sb)
	if !strings.Contains(sb.String(), "pins were not checked") {
		t.Errorf("a gap in the sweep must reach the reader:\n%s", sb.String())
	}
}

// Text is the same content without the markdown, for an operator reading it in
// a terminal while fixing it.
func TestTextIsTheSameContentWithoutMarkdown(t *testing.T) {
	r := &Report{
		At:      now,
		Checked: Checked{Stages: 1},
		Findings: []Finding{{
			Kind: KindWedged, Severity: Blocking, Subject: "s",
			Summary: "s is wedged", Detail: "because of a thing",
			Remedy: "kubectl get promotions",
		}},
	}
	var sb strings.Builder
	r.Text(&sb)
	got := sb.String()

	if !strings.Contains(got, "s is wedged") || !strings.Contains(got, "kubectl get promotions") {
		t.Errorf("the terminal form must carry the same facts:\n%s", got)
	}
	if strings.Contains(got, "```") || strings.Contains(got, "**") {
		t.Errorf("the terminal form must not carry markdown:\n%s", got)
	}
}

// A healthy report still renders: silence and success must not look alike.
func TestAHealthyReportStillSaysSomething(t *testing.T) {
	var sb strings.Builder
	(&Report{At: now, Checked: Checked{Stages: 4, Warehouses: 1, Promotions: 2}}).Render(&sb)
	if got := sb.String(); !strings.Contains(got, "healthy") {
		t.Errorf("a clean sweep must say so:\n%s", got)
	}
}
