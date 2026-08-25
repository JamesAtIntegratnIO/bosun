// Package pipeline supervises artifact promotion.
//
// Kargo does a great deal of work unattended, and its failure mode is
// SILENCE. A Warehouse that stopped discovering, a promotion that errored on a
// transient DNS blip three days ago, a pin that updates a key the chart no
// longer reads -- none of these produce an alert, a red check, or an unhealthy
// Application. The pipeline simply stops delivering, and everything that
// reports on it stays green, because every individual object is fine.
//
// That is not a criticism of Kargo. It is the shape of any system whose job is
// to make changes that would otherwise not happen: when it stops, what you
// observe is the absence of an event, and nothing observes absences by
// default.
//
// Measured, on one cluster, on one night:
//
//   - four Stages had sat on Errored promotions for THREE DAYS after a DNS
//     lookup failed mid-step. Four addons silently stopped receiving updates.
//     Nothing said so; every Application was Synced and Healthy throughout.
//   - the `kubectl` target writes seven keys into kyverno's values file, and
//     kyverno 3.9.0 stops reading six of them. Two different targets, one
//     dependency, nothing connecting them.
//   - nine open promotion pull requests were duplicates of four newer ones.
//   - eight promotions sat Running against pull requests that had been closed.
//
// # What this package will and will not do
//
// It READS. The chart's ClusterRole has no create, update, patch or delete verb
// anywhere, and says that a feature which seems to need one is a signal to
// reconsider the feature. Supervising the pipeline does not need one: every
// finding here is an observation, and every remedy is a command a human runs.
//
// Which makes the REMEDY the most valuable field on a finding. Recovering from
// each of the states above took an hour of reading Kargo's source, and the
// answers are not guessable: `kargo.akuity.io/abort=true` is silently ignored
// where `{"action":"terminate"}` works; a Warehouse refresh re-discovers
// artifacts but will never re-run a promotion that already reached a terminal
// phase; a hand-written Promotion needs `generateName` WITHOUT a trailing dot
// or the webhook rejects it on RFC1123. A supervisor that reports a problem
// and not its cure has moved the work rather than done it.
package pipeline

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Severity is how loudly a finding should be read.
type Severity string

const (
	// Blocking: the pipeline is not delivering something it was asked to
	// deliver, and will not start again on its own.
	Blocking Severity = "blocking"
	// Degraded: it is still working, but something will bite later -- a pin
	// writing to a key nothing reads, a duplicate pull request hiding a real
	// one.
	Degraded Severity = "degraded"
	// Note: worth knowing, nothing to do today.
	Note Severity = "note"
)

func (s Severity) rank() int {
	switch s {
	case Blocking:
		return 0
	case Degraded:
		return 1
	default:
		return 2
	}
}

// Kind identifies a finding's class, for metrics and for grouping. Stable
// strings: an alert rule names one.
type Kind string

const (
	KindWedged        Kind = "wedged_promotion"
	KindStalled       Kind = "stalled_warehouse"
	KindDeadPin       Kind = "dead_pin"
	KindOrphanedPR    Kind = "promotion_without_pr"
	KindSupersededPR  Kind = "superseded_pr"
	KindNeverPromoted Kind = "freight_never_promoted"
	KindVerifyStuck   Kind = "verification_stuck"
)

// Finding is one thing wrong with the pipeline.
type Finding struct {
	Kind     Kind
	Severity Severity
	// Subject is what the finding is about, in the operator's vocabulary:
	// a Stage name, a Warehouse name, a pull request.
	Subject string
	// Summary is one sentence, and the only line a reader is guaranteed to
	// read. It states the SITUATION, never the mechanism.
	Summary string
	// Detail is the evidence: what was observed, with numbers.
	Detail string
	// Remedy is the exact command. Not a description of a command -- the
	// command, ready to paste, because every one of these took an hour to
	// find the first time.
	Remedy string
	// Since is how long the situation has held, when that is knowable. A
	// promotion that failed four minutes ago and one that failed three days
	// ago are different problems wearing the same words.
	Since time.Duration
}

// Report is a whole sweep.
type Report struct {
	At         time.Time
	Findings   []Finding
	Namespaces []string
	// Checked records what the sweep actually managed to look at, so that a
	// clean report cannot be confused with a sweep that could not look. This
	// package's whole subject is the difference between those two.
	Checked Checked
}

// Checked is the sweep's own accounting. Every count here is a claim that
// something WAS examined; a zero with an unset flag means nobody looked.
type Checked struct {
	Stages       int
	Warehouses   int
	Promotions   int
	PullRequests int
	// PinsScanned is how many tracked (file, key) pairs were resolved against
	// a real checkout. Zero means the pin check did not run -- which is not
	// the same as "every pin is live".
	PinsScanned int
	// Notes explains anything the sweep could not do.
	Notes []string
}

// Worst is the highest severity present, and "" when the report is clean.
func (r *Report) Worst() Severity {
	worst := Severity("")
	for _, f := range r.Findings {
		if worst == "" || f.Severity.rank() < worst.rank() {
			worst = f.Severity
		}
	}
	return worst
}

// Clean reports whether the sweep found nothing. It is deliberately NOT
// "len(Findings) == 0 means all is well": a sweep that examined nothing also
// has no findings, and the two must never render the same.
func (r *Report) Clean() bool {
	return len(r.Findings) == 0 && r.Checked.Stages > 0
}

// Sort orders findings the way they should be read: worst first, then by kind
// so that a class of problem reads as a class, then by subject for stability
// between sweeps -- a report that reshuffles itself is a report nobody diffs.
func (r *Report) Sort() {
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Severity != b.Severity {
			return a.Severity.rank() < b.Severity.rank()
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Subject < b.Subject
	})
}

// Counts totals findings by kind and severity, for the metrics surface.
func (r *Report) Counts() map[Kind]map[Severity]int {
	out := map[Kind]map[Severity]int{}
	for _, f := range r.Findings {
		if out[f.Kind] == nil {
			out[f.Kind] = map[Severity]int{}
		}
		out[f.Kind][f.Severity]++
	}
	return out
}

// human renders a duration the way an operator says it: the largest unit that
// still carries information. "72h0m0s" is a number a machine wrote.
func human(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}
