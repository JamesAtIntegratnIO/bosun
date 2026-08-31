package mcp

import (
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// pipelineReportDescription is what a client hands its model as this tool's
// purpose.
//
// A constant, and it has to stay one. A description is the field a model
// treats as instructions, so a Stage name, a chart version or an error string
// interpolated into it would be text from a cluster arriving at the most
// trusted point in the exchange. Nothing here is computed, formatted, or
// assembled from anything the process read.
const pipelineReportDescription = "What bosun's last pipeline sweep found: promotions that " +
	"ended without delivering, Warehouses that stopped discovering, verifications that will " +
	"not re-run, and tracked pins that write nowhere. Each finding carries a typed kind and " +
	"severity, how long the situation has held, and where one exists the exact command that " +
	"recovers it. Findings are ordered worst first. The result also carries what the sweep " +
	"managed to examine, so that a report with no findings can be told apart from a sweep " +
	"that could not look. Answers from the last sweep's snapshot: it reaches no cluster, no " +
	"git host and no model, and it can change nothing."

// Report is what pipeline_report returns.
//
// The field that decides the shape of everything else is Findings, and it is a
// POINTER to a slice on purpose. Absent and empty have to be different answers:
// absent means no sweep has completed, empty means a sweep completed and found
// nothing. A plain slice with omitempty encodes both as absent, and a plain
// slice without it encodes both as `[]` -- and "nothing is wrong" read from a
// supervisor that has not looked is the single most expensive mistake a monitor
// can make. It is the same distinction the HTTP surfaces make with a 503, in
// the one shape JSON can carry it.
type Report struct {
	// Repository this report is about, "owner/repo".
	Repository string `json:"repository"`

	// Swept is whether any sweep has completed since this process started.
	Swept bool `json:"swept"`

	// SweptAt is when that sweep ran, absent before the first one.
	SweptAt *time.Time `json:"sweptAt,omitempty"`

	// AgeSeconds is how old the answer is, so a client can decide whether to
	// trust it or wait for the next sweep. Absent before the first sweep,
	// for the same reason SweptAt is.
	AgeSeconds *int64 `json:"ageSeconds,omitempty"`

	// Status is the one sentence to show a person, and it says in words what
	// the fields above say in types -- including, before the first sweep,
	// that nothing has looked yet.
	Status Text `json:"status"`

	// Clean is true only when the sweep examined something and found nothing
	// wrong. A typed answer to the question the counts below can also answer,
	// because a client should not have to add up four numbers to learn
	// whether a report with no findings is good news.
	Clean bool `json:"clean"`

	// Findings, worst first. Absent before the first sweep. See the type's
	// comment for why this is a pointer.
	Findings *[]Finding `json:"findings,omitempty"`

	// Examined is the sweep's own accounting of what it looked at. Absent
	// before the first sweep, present and possibly all zeroes after one --
	// and all zeroes is exactly the case Clean reports as false.
	Examined *Examined `json:"examined,omitempty"`
}

// Finding is one thing wrong with the pipeline.
type Finding struct {
	// Kind is the finding's class, and the field to branch on. A stable
	// string an alert rule already names: wedged_promotion, stalled_warehouse,
	// dead_pin, promotion_without_pr, superseded_pr, verification_stuck,
	// pending_promotion.
	Kind string `json:"kind"`

	// Severity is blocking, degraded or note.
	Severity string `json:"severity"`

	// Subject is what the finding is about, in the operator's vocabulary: a
	// Stage name, a Warehouse name, a pull request. An identity rather than
	// prose, which is why it carries no origin -- there is no sentence here
	// for anything to hide an instruction inside.
	Subject string `json:"subject"`

	// Summary is one sentence: the situation, never the mechanism.
	Summary Text `json:"summary"`

	// Detail is the evidence, with its numbers.
	Detail Text `json:"detail"`

	// AgeSeconds is how long the situation has held, absent when that is not
	// knowable. A promotion that failed four minutes ago and one that failed
	// three days ago are different problems wearing the same words.
	AgeSeconds *int64 `json:"ageSeconds,omitempty"`

	// Age is the same duration the way an operator says it, "3d". Beside the
	// number rather than instead of it: the number is what a client branches
	// on, the string is what it can print without reimplementing the rounding
	// every other bosun surface already agreed on.
	Age string `json:"age,omitempty"`

	// Remedy is the exact command, or absent.
	//
	// Absent is a real answer and a common one. Some findings have no
	// repository-side or cluster-side cure, and one whose subject bosun could
	// not validate loses its remedy rather than getting a suspect one -- see
	// pipeline/remedy.go. A client that finds nothing here should stop looking
	// for an edit that does not exist.
	Remedy *Remedy `json:"remedy,omitempty"`
}

// Remedy is a command composed by bosun, ready to run.
//
// Its own type rather than a Text, because it is the one field on this surface
// that is built to be executed and the guarantee behind it is stronger than a
// tag: every piece interpolated into it was checked against a grammar before
// the command was emitted at all, and a piece that failed cost the finding its
// remedy. Origin is on it anyway, and it is always bosun, so a client fencing
// by origin has nothing to special-case.
type Remedy struct {
	Command string `json:"command"`
	Origin  Origin `json:"origin"`
}

// Examined is what the sweep managed to look at.
//
// Every count is a claim that something was examined. A report whose counts
// are all zero is a sweep that could not look, which is why Clean is false
// there and why this package's ancestor renders that case as "Nothing was
// read, so nothing is claimed."
type Examined struct {
	Stages       int `json:"stages"`
	Warehouses   int `json:"warehouses"`
	Promotions   int `json:"promotions"`
	PullRequests int `json:"pullRequests"`
	// Pins is how many tracked (file, key) pairs were resolved against a real
	// checkout. Zero means the pin check did not run, which is not the same
	// as "every pin is live"; Notes says which.
	Pins int `json:"pins"`
	// Namespaces the sweep saw Stages in.
	Namespaces []string `json:"namespaces"`
	// Notes is everything the sweep could not do. Always present, empty when
	// there was nothing it could not do -- absent would be a third state
	// nothing means.
	Notes []Text `json:"notes"`
}

// pipelineReport answers from the last sweep, and from nothing else.
func (s *Server) pipelineReport() Report {
	out := Report{Repository: s.Repository}

	rep := s.Report()
	if rep == nil {
		out.Status = say(noSweepYet, OriginBosun, maxSummary)
		return out
	}

	out.SweptAt, out.AgeSeconds, out.Swept = s.stamp(rep.At)
	out.Clean = rep.Clean()
	// Bosun's own, and the tag is a claim worth checking rather than a
	// courtesy: Report.Headline composes from counts and fixed words and
	// quotes nothing the cluster wrote. TestTheStatusLineIsBosunsOwnWords is
	// what keeps that true, because the day a headline starts naming a Stage
	// is the day this tag becomes a lie in the one field a client is told it
	// may trust.
	out.Status = say(rep.Headline(), OriginBosun, maxSummary)

	findings := make([]Finding, 0, len(rep.Findings))
	for _, f := range rep.Findings {
		findings = append(findings, finding(f))
	}
	out.Findings = &findings

	ex := Examined{
		Stages:       rep.Checked.Stages,
		Warehouses:   rep.Checked.Warehouses,
		Promotions:   rep.Checked.Promotions,
		PullRequests: rep.Checked.PullRequests,
		Pins:         rep.Checked.PinsScanned,
		Namespaces:   append([]string(nil), rep.Namespaces...),
		Notes:        make([]Text, 0, len(rep.Checked.Notes)),
	}
	if ex.Namespaces == nil {
		ex.Namespaces = []string{}
	}
	for _, n := range rep.Checked.Notes {
		// Cluster-quoting: a note is bosun's sentence about a read that
		// failed, and it carries the error the client or the apiserver
		// produced.
		ex.Notes = append(ex.Notes, say(n, OriginCluster, maxNote))
	}
	out.Examined = &ex
	return out
}

// noSweepYet is what every tool says before the first sweep completes.
//
// In words as well as in the Swept field, because the two audiences differ: a
// client branches on the boolean, and the model it is speaking for reads this.
// Both have to be told the same thing, and this is the sentence that says it
// without ever being mistaken for a clean report.
const noSweepYet = "No sweep has completed yet, so nothing is claimed about this pipeline. " +
	"This is not a clean report: the supervisor has not looked. Ask again after the next sweep."

// finding maps one pipeline finding onto the wire.
//
// The mapping is deliberately dull. Everything interesting -- what is wrong,
// how long it has held, what ends it -- was decided by pipeline, which is the
// package that can be tested without a listener. This adds the origin tags and
// the caps, and it does not add a single fact.
func finding(f pipeline.Finding) Finding {
	out := Finding{
		Kind:     string(f.Kind),
		Severity: string(f.Severity),
		Subject:  f.Subject,
		// Cluster-quoting, both of them. Bosun wrote every sentence, and the
		// object names and condition messages inside them came from Kargo.
		Summary: say(f.Summary, OriginCluster, maxSummary),
		Detail:  say(f.Detail, OriginCluster, maxDetail),
	}
	if f.Since > 0 {
		secs := int64(f.Since.Seconds())
		out.AgeSeconds = &secs
		out.Age = pipeline.Human(f.Since)
	}
	if cmd := strings.TrimSpace(f.Remedy); cmd != "" {
		// Verbatim, and uncapped: see the caps in text.go for why a remedy is
		// the one free-text field that gets neither truncation nor a length
		// limit. Absent here means "no remedy exists", and nothing else may
		// make that sentence untrue.
		out.Remedy = &Remedy{Command: cmd, Origin: OriginBosun}
	}
	return out
}
