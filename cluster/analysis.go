package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Reading the AnalysisRun behind a Stage's verification.
//
// A Stage that a verification has stopped reports that it happened and, when
// Kargo has one to pass on, a single message. What it never reports is which
// metric said no, what that metric actually measured, or whether the run can
// stop on its own. Those are the three questions a reader has, and all three
// live in the AnalysisRun -- an Argo Rollouts object that Kargo creates and
// then references by name.
//
// The reference is the only sound way in. The run's name is generated, its
// labels are Kargo's business and have moved between releases, and listing
// AnalysisRuns to work out which one belongs to this Stage would be a guess
// dressed as a fact. Kargo writes the answer down at
// `status.freightHistory[0].verificationHistory[0].analysisRun`, so this reads
// the one object the Stage points at and nothing else.
//
// Same two rules as the Kargo reads: GET only, and no vendored types. This
// names nine fields of a CRD owned by a project bosun does not depend on, and
// a release that adds a tenth cannot break this build. The path is built from
// readAnalysisRuns for the third rule: what the chart grants is checked
// against that table, so a read the ClusterRole does not cover fails a test
// here rather than becoming a line quietly missing from every report.

// AnalysisRun is a verification's run, reduced to what explains a Stage that
// is not moving.
type AnalysisRun struct {
	Name      string
	Namespace string
	Phase     string
	// Message is the run's own summary. Argo Rollouts writes an assessment
	// here ("Metric \"x\" assessed Failed due to failed (1) > failureLimit
	// (0)"), which names the metric but not the cause; the cause is in the
	// metric's newest measurement.
	Message string
	Metrics []AnalysisMetric
}

// AnalysisMetric is one metric of a run.
type AnalysisMetric struct {
	Name  string
	Phase string
	// Message is the newest measurement's message, falling back to the
	// result's own. The measurement is where a cause reaches text: a metric
	// result says the query failed, the measurement says
	// `dial tcp 10.106.179.157:9090: connect: no route to host`, and only the
	// second one tells anybody what to fix.
	Message string
	// Failed and Error are measurement tallies. They are carried apart
	// because they mean opposite things: a metric that failed got an answer
	// and the answer was no, a metric that errored never got one, and telling
	// somebody to fix their thresholds when their Prometheus is unreachable
	// wastes the afternoon this exists to save.
	Failed int
	Error  int
	// Unbounded reports a metric that will never finish by itself.
	//
	// Argo Rollouts runs a metric `count` times, and a metric with an
	// `interval` and no `count` measures forever. That is the shape that
	// holds a Stage's queue indefinitely, and until this was readable the
	// finding for a long-running verification asserted it as a general truth
	// about AnalysisRuns rather than a fact about this one.
	Unbounded bool
}

// Failing is the metric worth naming: the first that errored or failed, and
// otherwise the first that is unbounded, which is the one holding the queue.
// Returns false when the run explains nothing, which a caller must handle by
// saying less rather than by inventing a metric.
func (r AnalysisRun) Failing() (AnalysisMetric, bool) {
	for _, m := range r.Metrics {
		if m.Error > 0 || m.Failed > 0 {
			return m, true
		}
	}
	for _, m := range r.Metrics {
		if m.Unbounded {
			return m, true
		}
	}
	return AnalysisMetric{}, false
}

// AnalysisRun reads one run by the reference a Stage carries.
//
// An error rather than a soft note, the same trade the Kargo reads make and
// for the same reason: the caller is the sweep, and only the sweep can decide
// what a failed enrichment costs the finding it was enriching.
func (a *APIServer) AnalysisRun(ctx context.Context, namespace, name string) (AnalysisRun, error) {
	if namespace == "" || name == "" {
		return AnalysisRun{}, fmt.Errorf("no AnalysisRun reference to read")
	}
	var raw struct {
		Spec struct {
			Metrics []struct {
				Name     string `json:"name"`
				Interval string `json:"interval"`
				// Count is an IntOrString in Rollouts, so it arrives as `3`
				// from one writer and `"3"` from another. Decoding it into
				// either Go type fails on the other half of the corpus, and
				// the only question asked of it is whether it is set.
				Count json.RawMessage `json:"count"`
			} `json:"metrics"`
		} `json:"spec"`
		Status struct {
			Phase         string `json:"phase"`
			Message       string `json:"message"`
			MetricResults []struct {
				Name         string `json:"name"`
				Phase        string `json:"phase"`
				Message      string `json:"message"`
				Failed       int    `json:"failed"`
				Error        int    `json:"error"`
				Measurements []struct {
					Phase   string `json:"phase"`
					Message string `json:"message"`
					Value   string `json:"value"`
				} `json:"measurements"`
			} `json:"metricResults"`
		} `json:"status"`
	}
	if err := a.get(ctx, readAnalysisRuns.namespaced(namespace, name), &raw); err != nil {
		switch code(err) {
		case http.StatusForbidden, http.StatusUnauthorized:
			return AnalysisRun{}, fmt.Errorf("not permitted to read AnalysisRuns in %s", namespace)
		case http.StatusNotFound:
			// Ordinary rather than broken. Kargo prunes old runs on its own
			// schedule, so a Stage can outlive the run that stopped it, and
			// the sentence a reader needs then is that the detail is gone --
			// not that something failed.
			return AnalysisRun{}, fmt.Errorf("AnalysisRun %s/%s no longer exists", namespace, name)
		}
		return AnalysisRun{}, err
	}

	// Which metrics can never end, by name, from the spec. The status says
	// what has happened so far and cannot say what will keep happening.
	unbounded := map[string]bool{}
	for _, m := range raw.Spec.Metrics {
		unbounded[m.Name] = m.Interval != "" && !countIsSet(m.Count)
	}

	out := AnalysisRun{
		Name:      name,
		Namespace: namespace,
		Phase:     raw.Status.Phase,
		Message:   strings.TrimSpace(raw.Status.Message),
	}
	for _, r := range raw.Status.MetricResults {
		m := AnalysisMetric{
			Name:      r.Name,
			Phase:     r.Phase,
			Message:   strings.TrimSpace(r.Message),
			Failed:    r.Failed,
			Error:     r.Error,
			Unbounded: unbounded[r.Name],
		}
		// Newest measurement last, which is Rollouts' own order. Its message
		// beats the result's summary when it has one, and its value is the
		// fallback for a metric that returned an answer nobody expected --
		// "0" where a threshold wanted more is a complete explanation and
		// carries no message at all.
		if n := len(r.Measurements); n > 0 {
			last := r.Measurements[n-1]
			switch {
			case strings.TrimSpace(last.Message) != "":
				m.Message = strings.TrimSpace(last.Message)
			case m.Message == "" && strings.TrimSpace(last.Value) != "":
				m.Message = "measured " + strings.TrimSpace(last.Value)
			}
		}
		out.Metrics = append(out.Metrics, m)
	}
	// A metric the spec declares and the status has not reached yet is still
	// worth knowing about when it is the unbounded one, because it is what
	// the queue is waiting for next.
	for _, sm := range raw.Spec.Metrics {
		if !unbounded[sm.Name] || hasMetric(out.Metrics, sm.Name) {
			continue
		}
		out.Metrics = append(out.Metrics, AnalysisMetric{Name: sm.Name, Unbounded: true})
	}
	return out, nil
}

func hasMetric(ms []AnalysisMetric, name string) bool {
	for _, m := range ms {
		if m.Name == name {
			return true
		}
	}
	return false
}

// countIsSet reports whether a metric declares a count at all. Absent, null,
// empty and zero all mean "no count", and zero is included deliberately: a
// metric asked for zero measurements is not one that stops after zero, it is
// one nobody set.
func countIsSet(raw json.RawMessage) bool {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	switch s {
	case "", "null", "0":
		return false
	}
	return true
}
