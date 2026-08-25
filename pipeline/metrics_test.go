package pipeline

import (
	"strings"
	"testing"
	"time"
)

func metricsOf(r *Report) string {
	var b strings.Builder
	r.Metrics(&b)
	return b.String()
}

// The rule that matters most is the one about this package failing. A metric
// that only exists when there is a finding cannot express "and now there are
// none", and cannot be alerted on with a comparison.
func TestEveryKindIsEmittedEvenAtZero(t *testing.T) {
	m := metricsOf(Detect(&Snapshot{Now: now, Stages: []Stage{{Name: "x", Ready: true}}}))
	for _, k := range allKinds {
		if !strings.Contains(m, `kind="`+string(k)+`"`) {
			t.Errorf("kind %s missing from a clean report; it could not be graphed back to zero", k)
		}
	}
	if !strings.Contains(m, "bosun_pipeline_sweep_timestamp_seconds") {
		t.Error("without a sweep timestamp, a supervisor that stopped looks like a healthy pipeline")
	}
	if !strings.Contains(m, `bosun_pipeline_checked{resource="stages"} 1`) {
		t.Errorf("the sweep must report what it read:\n%s", m)
	}
}

func TestAgeIsExportedBecauseUrgencyLivesThere(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "external-secrets", Ready: true}},
		Promotions: []Promotion{{
			Name: "p", Stage: "external-secrets", Freight: "f",
			Phase: PhaseErrored, CreatedAt: ago(72 * time.Hour), StartedAt: ago(72 * time.Hour),
		}},
	}
	m := metricsOf(Detect(s))
	want := `bosun_pipeline_finding_age_seconds{kind="wedged_promotion",subject="external-secrets"} 259200`
	if !strings.Contains(m, want) {
		t.Fatalf("expected\n  %s\ngot\n%s", want, m)
	}
}

func TestACleanReportEmitsNoAgeSeries(t *testing.T) {
	m := metricsOf(Detect(&Snapshot{Now: now, Stages: []Stage{{Name: "x", Ready: true}}}))
	if strings.Contains(m, "bosun_pipeline_finding_age_seconds{") {
		t.Fatalf("a finding with no duration must not claim an age of zero:\n%s", m)
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	if got := escape(`a"b\c`); got != `a\"b\\c` {
		t.Fatalf("escape produced %q", got)
	}
}
