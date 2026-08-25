package pipeline

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Metrics writes the report in Prometheus text exposition format.
//
// HAND-ROLLED, and the same argument the cluster package makes about
// client-go: this is forty lines of fmt against a stable, documented text
// format, and vendoring a metrics SDK would make the largest dependency in
// this service the thing that prints six numbers.
//
// # What to alert on
//
// The obvious rule is `bosun_pipeline_findings{severity="blocking"} > 0`, and
// it is worth having. The IMPORTANT one is the other direction:
//
//	absent(bosun_pipeline_sweep_timestamp_seconds)
//	or time() - bosun_pipeline_sweep_timestamp_seconds > 3600
//
// A supervisor whose whole subject is silent failure has to be able to fail
// loudly itself. Without that rule, a supervisor that stopped sweeping looks
// exactly like a pipeline with nothing wrong -- which is the joke this package
// would rather not be.
//
// `bosun_pipeline_checked` is the same guard one level down: a sweep that read
// zero Stages found no problems, and must never be read as having proved
// anything.
func (r *Report) Metrics(w io.Writer) {
	writeHeader(w, "bosun_pipeline_sweep_timestamp_seconds", "gauge",
		"When the last pipeline sweep completed. Alert on its absence or age: a supervisor that stopped looking reports no findings.")
	fmt.Fprintf(w, "bosun_pipeline_sweep_timestamp_seconds %d\n", r.At.Unix())

	writeHeader(w, "bosun_pipeline_checked", "gauge",
		"How many objects the last sweep actually read, by resource. Zero Stages means nothing was proved.")
	for _, kv := range []struct {
		res string
		n   int
	}{
		{"stages", r.Checked.Stages},
		{"warehouses", r.Checked.Warehouses},
		{"promotions", r.Checked.Promotions},
		{"pull_requests", r.Checked.PullRequests},
		{"pins", r.Checked.PinsScanned},
	} {
		fmt.Fprintf(w, "bosun_pipeline_checked{resource=%q} %d\n", kv.res, kv.n)
	}

	// Every known kind is emitted, including the zeroes. A metric that only
	// appears when something is wrong cannot be graphed, cannot be alerted on
	// with a comparison, and disappears at exactly the moment someone wants to
	// confirm the problem is gone.
	writeHeader(w, "bosun_pipeline_findings", "gauge",
		"Open pipeline findings by kind and severity.")
	counts := r.Counts()
	for _, k := range allKinds {
		for _, sev := range []Severity{Blocking, Degraded, Note} {
			n := 0
			if counts[k] != nil {
				n = counts[k][sev]
			}
			fmt.Fprintf(w, "bosun_pipeline_findings{kind=%q,severity=%q} %d\n", k, sev, n)
		}
	}

	// Age is what separates "a promotion failed four minutes ago" from "four
	// addons have not updated in three days". Only non-zero ages are emitted:
	// a finding with no knowable duration should not claim one of zero.
	var aged []Finding
	for _, f := range r.Findings {
		if f.Since > 0 {
			aged = append(aged, f)
		}
	}
	if len(aged) > 0 {
		writeHeader(w, "bosun_pipeline_finding_age_seconds", "gauge",
			"How long a finding's situation has held. This is the number that decides whether it is urgent.")
		sort.SliceStable(aged, func(i, j int) bool {
			if aged[i].Kind != aged[j].Kind {
				return aged[i].Kind < aged[j].Kind
			}
			return aged[i].Subject < aged[j].Subject
		})
		for _, f := range aged {
			fmt.Fprintf(w, "bosun_pipeline_finding_age_seconds{kind=%q,subject=%q} %d\n",
				f.Kind, escape(f.Subject), int(f.Since.Seconds()))
		}
	}
}

// allKinds is every kind the metrics endpoint emits a zero for, so an alert
// rule can fire on a series that exists before the first occurrence rather than
// on one that appears at the same moment as the problem.
//
// It must list every Kind the const block declares. A kind missing here has no
// series at all until something goes wrong, which is the failure mode the zeros
// exist to prevent -- TestEveryKindHasAMetricSeries pins the two together.
var allKinds = []Kind{
	KindWedged, KindStalled, KindDeadPin, KindOrphanedPR,
	KindSupersededPR, KindVerifyStuck, KindPendingStuck,
}

func writeHeader(w io.Writer, name, typ, help string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// escape makes a label value safe. Stage names cannot contain any of these,
// but a metrics endpoint that can be broken by a name is a metrics endpoint
// that will be.
func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}
