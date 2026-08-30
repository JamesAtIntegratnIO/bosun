package cluster

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The point of reading the run at all: the Stage says a verification failed,
// and only the AnalysisRun says which metric and what it saw. The measurement
// message beats the result's own summary, because the summary names the
// metric and the measurement names the cause.
func TestAnAnalysisRunNamesTheMetricAndTheCause(t *testing.T) {
	a := serveJSON(t, `{
		"spec":{"metrics":[{"name":"prometheus-check","interval":"1m","count":3}]},
		"status":{"phase":"Failed","message":"Metric \"prometheus-check\" assessed Failed",
			"metricResults":[{"name":"prometheus-check","phase":"Error","error":3,
				"message":"query failed",
				"measurements":[
					{"phase":"Error","message":"first"},
					{"phase":"Error","message":"dial tcp 10.106.179.157:9090: connect: no route to host"}]}]}}`)

	run, err := a.AnalysisRun(context.Background(), "addons", "cert-manager.01")
	if err != nil {
		t.Fatal(err)
	}
	if run.Phase != "Failed" || run.Name != "cert-manager.01" || run.Namespace != "addons" {
		t.Errorf("got %+v", run)
	}
	m, ok := run.Failing()
	if !ok {
		t.Fatal("a run with three errored measurements must have a failing metric")
	}
	if m.Name != "prometheus-check" || m.Error != 3 {
		t.Errorf("got %+v", m)
	}
	if !strings.Contains(m.Message, "no route to host") {
		t.Errorf("the newest measurement is where the cause is, got %q", m.Message)
	}
	// A count is a count: this one ends after three, so it is not what holds
	// a Stage forever.
	if m.Unbounded {
		t.Error("a metric with a count is bounded")
	}
}

// The claim the finding used to make about AnalysisRuns in general. It is
// true of a metric with an interval and no count, and of no other shape, and
// until this was readable it was asserted either way.
func TestAMetricWithAnIntervalAndNoCountIsUnbounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
		want bool
	}{
		{"interval and no count", `{"name":"m","interval":"30s"}`, true},
		{"interval and a count", `{"name":"m","interval":"30s","count":5}`, false},
		{"count written as a string", `{"name":"m","interval":"30s","count":"5"}`, false},
		{"a count of zero is nobody's count", `{"name":"m","interval":"30s","count":0}`, true},
		{"no interval at all runs once", `{"name":"m"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := serveJSON(t, `{"spec":{"metrics":[`+tc.spec+`]},
				"status":{"metricResults":[{"name":"m","phase":"Running"}]}}`)
			run, err := a.AnalysisRun(context.Background(), "ns", "r")
			if err != nil {
				t.Fatal(err)
			}
			if got := run.Metrics[0].Unbounded; got != tc.want {
				t.Errorf("Unbounded = %v, want %v", got, tc.want)
			}
		})
	}
}

// A metric the spec declares and the status has not reached is invisible in
// metricResults, and it is exactly the one worth knowing about when it is the
// unbounded one: it is what the queue waits for next.
func TestAnUnboundedMetricWithNoResultYetIsStillReported(t *testing.T) {
	a := serveJSON(t, `{
		"spec":{"metrics":[{"name":"done","count":1},{"name":"forever","interval":"1m"}]},
		"status":{"phase":"Running","metricResults":[{"name":"done","phase":"Successful"}]}}`)

	run, err := a.AnalysisRun(context.Background(), "ns", "r")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := run.Failing()
	if !ok || m.Name != "forever" {
		t.Fatalf("want the unbounded metric, got %+v (%v)", m, ok)
	}
}

// A run that says nothing must not be made to say something. The caller's
// answer to this is a shorter sentence, not an invented metric.
func TestARunThatExplainsNothingSaysSo(t *testing.T) {
	a := serveJSON(t, `{"spec":{"metrics":[{"name":"m","count":1}]},
		"status":{"phase":"Running","metricResults":[{"name":"m","phase":"Running"}]}}`)
	run, err := a.AnalysisRun(context.Background(), "ns", "r")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Failing(); ok {
		t.Error("a bounded metric that has not failed explains nothing")
	}
}

// A measurement with a value and no message still carries the whole
// explanation: a threshold that wanted more than zero, and got zero.
func TestAValueStandsInForAMissingMessage(t *testing.T) {
	a := serveJSON(t, `{"spec":{"metrics":[{"name":"m","count":1}]},
		"status":{"metricResults":[{"name":"m","phase":"Failed","failed":1,
			"measurements":[{"phase":"Failed","value":"0"}]}]}}`)
	run, err := a.AnalysisRun(context.Background(), "ns", "r")
	if err != nil {
		t.Fatal(err)
	}
	if got := run.Metrics[0].Message; got != "measured 0" {
		t.Errorf("got %q", got)
	}
}

// Refused and pruned are different sentences with different actions, and the
// second one is ordinary: Kargo prunes runs on its own schedule, so a Stage
// outliving the run that stopped it is not a failure.
func TestReadingARunDistinguishesRefusedFromGone(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{
		{http.StatusForbidden, "not permitted"},
		{http.StatusNotFound, "no longer exists"},
	} {
		a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.code)
		}))
		_, err := a.AnalysisRun(context.Background(), "ns", "r")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%d: got %v, want %q", tc.code, err, tc.want)
		}
	}
}

// Without a reference there is nothing to read, and asking anyway would GET a
// path with an empty segment, which the apiserver answers with the collection.
func TestAnEmptyReferenceIsNotARead(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("no request should have been made")
	}))
	if _, err := a.AnalysisRun(context.Background(), "ns", ""); err == nil {
		t.Error("want an error")
	}
}
