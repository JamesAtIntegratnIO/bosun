package pipeline

import (
	"fmt"
	"strings"
	"time"
)

// The Kargo objects this package reads, reduced to the fields it uses.
//
// Deliberately not Kargo's own types. Vendoring them would make the largest
// dependency in this service a CRD schema it reads five fields from, which is
// the same argument the cluster package makes about client-go. It also means a
// Kargo release that adds a field cannot break this build, and a field this
// package needs but does not get arrives as a zero value the detectors
// already have to handle.

// Phase values Kargo writes. Terminal ones will never change again without a
// new Promotion object being created, which is the whole basis of the wedged
// detector.
const (
	PhasePending   = "Pending"
	PhaseRunning   = "Running"
	PhaseSucceeded = "Succeeded"
	PhaseFailed    = "Failed"
	PhaseErrored   = "Errored"
	PhaseAborted   = "Aborted"
)

// Verification phases an AnalysisRun reports. A different vocabulary from the
// promotion phases above, and deliberately so; they overlap on "Failed" and
// "Aborted" and differ everywhere else, and the near-miss is the trap: the
// promotion phase is PhaseErrored, "Errored", while a verification says
// "Error". Tidying these into the constants twenty lines up would silently
// break the verify-stuck detector.
const (
	VerifyFailed       = "Failed"
	VerifyError        = "Error"
	VerifyAborted      = "Aborted"
	VerifyInconclusive = "Inconclusive"
)

// Terminal reports whether a phase is final.
func Terminal(phase string) bool {
	switch phase {
	case PhaseSucceeded, PhaseFailed, PhaseErrored, PhaseAborted:
		return true
	}
	return false
}

// Unsuccessful is terminal and not Succeeded: the promotion is over and it did
// not deliver.
func Unsuccessful(phase string) bool {
	return Terminal(phase) && phase != PhaseSucceeded
}

// Update is one `yaml-update` step's target: the file a promotion rewrites and
// the keys it rewrites in it.
//
// Read from the cluster, not from the repository's Kargo values. That is the
// difference between "what the target list says" and "what Kargo will do", and
// they diverge exactly when something is wrong; a values file that stopped
// rendering a target produces a Stage with no update step, and a supervisor
// reading the values file would report the pipeline as healthy.
type Update struct {
	Path string
	Keys []string
}

// RepoPath strips the clone prefix Kargo's steps carry.
//
// A promotion clones into a working directory and its steps address files as
// `./repo/addons/...`. The repository itself has no `repo/` directory, so the
// first segment is dropped, but only when it is not itself a directory the
// repository has, because a repository whose top level is called `repo`
// is legal and rare, and guessing wrong there would report every pin as dead.
func (u Update) RepoPath(exists func(string) bool) string {
	p := strings.TrimPrefix(u.Path, "./")
	if exists(p) {
		return p
	}
	if i := strings.Index(p, "/"); i > 0 {
		if stripped := p[i+1:]; exists(stripped) {
			return stripped
		}
		return p[i+1:]
	}
	return p
}

type Stage struct {
	Name      string
	Namespace string
	// CurrentFreight is what the Stage is running, "" if none.
	CurrentFreight string
	// Updates are every `yaml-update` step in the promotion template.
	Updates []Update
	// Ready and its reason come from the Stage's conditions. A Stage that is
	// not Ready because a verification is running is a different situation
	// from one that is not Ready because its last promotion failed.
	Ready        bool
	ReadyReason  string
	ReadyMessage string
	// ReadySince is how long the current Ready state has held.
	ReadySince time.Duration
	// VerificationID and VerificationPhase describe the newest verification
	// of the current freight. The id is what re-runs it.
	VerificationID    string
	VerificationPhase string
	// VerificationRunNamespace and VerificationRunName address the
	// AnalysisRun that verification is, so a finding can say which metric
	// stopped this Stage instead of handing back a kubectl command that asks.
	VerificationRunNamespace string
	VerificationRunName      string
}

// StoppedByVerification reports a Stage held by its verification rather than
// by anything else.
//
// The reason string is Kargo's, and matching a substring of it is the only
// join available: the condition carries `VerificationFailed`,
// `VerificationError` and `VerificationRunning` in different releases and
// there is no enum to compare against. One method rather than the same
// substring in the detector and the collector, because the two drifting apart
// would mean reading an AnalysisRun for a Stage that never reports one, or
// reporting on a Stage whose run was never read.
func (st Stage) StoppedByVerification() bool {
	return !st.Ready && strings.Contains(strings.ToLower(st.ReadyReason), "verif")
}

// Freight is one freight, named rather than identified.
//
// A freight name is a hash: `f-7c3d9a1` is a correct answer to "which
// freight" and no answer at all to "what is stuck". Kargo records both an
// alias and the artifacts, and a finding that says
// "carrying ghcr.io/org/app:v1.4.0" is one a reader can match against the
// pull request in front of them without a second window.
type Freight struct {
	Name      string
	Namespace string
	Alias     string
	Artifacts []string
}

// Describe names the freight the way a reader would, degrading rather than
// failing: the artifacts if they are known, the alias if they are not, and
// the bare name if neither is -- which is what every finding said before any
// of this was readable.
//
// One of the three, never a pair. "stopped receiving mellow-mongoose
// (ghcr.io/argoproj/argocd:v2.13.1)" fits in a summary line and spends half of
// it on a word that identifies the freight to a reader who is already looking
// at the finding about it. The artifact is the part that matches what is in
// front of them; the alias is what stands in when there is no artifact.
func (f Freight) Describe() string {
	if len(f.Artifacts) == 0 {
		if f.Alias != "" {
			return f.Alias
		}
		return f.Name
	}
	// Two, then a count. A freight assembled from a monorepo carries a dozen
	// images and naming all of them turns one line of a finding into a
	// screen; naming none of them was the problem.
	shown := f.Artifacts
	extra := 0
	if len(shown) > 2 {
		shown, extra = shown[:2], len(shown)-2
	}
	out := strings.Join(shown, ", ")
	if extra > 0 {
		out += fmt.Sprintf(" and %s", plural(extra, "other"))
	}
	return out
}

// Verification is the AnalysisRun behind a Stage's verification.
//
// Carried on the Snapshot rather than read by a detector, so detectors stay
// pure functions of it and every sentence they produce about a verification
// can be reproduced in a test without a cluster.
type Verification struct {
	Name      string
	Namespace string
	Phase     string
	Message   string
	Metrics   []VerifyMetric
}

// VerifyMetric is one metric of a verification.
type VerifyMetric struct {
	Name    string
	Phase   string
	Message string
	Failed  int
	Error   int
	// Unbounded means the metric has an interval and no count, so it measures
	// until something stops it. That is the shape that holds a Stage's queue
	// forever, and it is the difference between telling somebody their
	// verification is slow and telling them it will never end.
	Unbounded bool
}

// Failing is the metric worth naming: the first that errored or failed, and
// otherwise the first that can never finish. False when the run explains
// nothing, which the caller must answer by saying less.
func (v Verification) Failing() (VerifyMetric, bool) {
	for _, m := range v.Metrics {
		if m.Error > 0 || m.Failed > 0 {
			return m, true
		}
	}
	for _, m := range v.Metrics {
		if m.Unbounded {
			return m, true
		}
	}
	return VerifyMetric{}, false
}

// Because names a metric's outcome in the words its two tallies mean. An
// errored metric never got an answer and a failed one got the wrong answer,
// and the fix for each is in a different building.
func (m VerifyMetric) Because() string {
	switch {
	case m.Error > 0 && m.Failed > 0:
		return fmt.Sprintf("`%s` could not be measured %s and failed %s",
			m.Name, plural(m.Error, "time"), plural(m.Failed, "time"))
	case m.Error > 0:
		return fmt.Sprintf("`%s` could not be measured at all (%s)", m.Name, plural(m.Error, "attempt"))
	case m.Failed > 0:
		return fmt.Sprintf("`%s` measured and failed %s", m.Name, plural(m.Failed, "time"))
	case m.Unbounded:
		return fmt.Sprintf("`%s` has an interval and no count", m.Name)
	}
	// Unreachable through Failing, which only ever returns a metric one of
	// the cases above describes. Quoted like the rest so a future caller that
	// reaches it does not get the one sentence with a bare name in it.
	return "`" + m.Name + "`"
}

type Warehouse struct {
	Name      string
	Namespace string
	// Interval is the discovery period. Zero means Kargo's default, which
	// this package does not guess at, an unknown interval disables the
	// staleness check for that Warehouse rather than inventing a threshold.
	Interval     time.Duration
	DiscoveredAt time.Time
	Ready        bool
	ReadyReason  string
	ReadyMessage string
	// Latest is the newest artifact version discovered, for context in a
	// finding. Empty is normal for a Warehouse that has discovered nothing.
	Latest string
}

type Promotion struct {
	Name      string
	Namespace string
	Stage     string
	Freight   string
	Phase     string
	StartedAt time.Time
	// CreatedAt is when the object appeared, which is the only timestamp a
	// pending promotion has.
	CreatedAt time.Time
	Message   string
}

// Age is how long ago the promotion started, falling back to creation for one
// that never started.
func (p Promotion) Age(now time.Time) time.Duration {
	at := p.StartedAt
	if at.IsZero() {
		at = p.CreatedAt
	}
	if at.IsZero() {
		return 0
	}
	return now.Sub(at)
}

// Branch is the git branch a promotion pushes to.
//
// Kargo's own convention, and the join between a Kargo object and a pull
// request without either of them recording the other. A promotion whose branch
// has no open pull request has had its pull request closed underneath it,
// which leaves it Running against something that will never merge.
func (p Promotion) Branch() string { return "kargo/promotion/" + p.Name }

// StageOfBranch is Branch's inverse: the Stage a promotion branch belongs to.
// Returns "" for a branch this convention does not describe.
func StageOfBranch(branch string) string {
	const prefix = "kargo/promotion/"
	if !strings.HasPrefix(branch, prefix) {
		return ""
	}
	// <stage>.<ulid>.<short-sha>; the stage is everything before the first
	// dot, and stage names cannot contain one.
	rest := branch[len(prefix):]
	if i := strings.Index(rest, "."); i > 0 {
		return rest[:i]
	}
	return rest
}

// PullRequest is the little this package needs to know about one.
type PullRequest struct {
	Number int
	Title  string
	Branch string
}

// Snapshot is everything one sweep looks at. Detectors are pure functions of
// it, so every situation this package describes can be reproduced in a test
// without a cluster, a git host, or a clock.
type Snapshot struct {
	Now        time.Time
	Stages     []Stage
	Warehouses []Warehouse
	Promotions []Promotion
	OpenPRs    []PullRequest

	// RepoRoot is a checkout to resolve pins against. Empty disables the pin
	// check, and the report says so rather than reporting no dead pins.
	RepoRoot string
	// FileHas answers "does this file set this key?". Injected so the
	// detector stays pure; nil disables the pin check.
	FileHas func(path, key string) (bool, error)
	// Freight is what the sweep could name, keyed "<namespace>/<name>". A
	// miss is ordinary: freight is read for the handful of names a finding
	// will print, and anything absent degrades to the bare hash.
	Freight map[string]Freight
	// Verifications are the AnalysisRuns behind stopped Stages, keyed the
	// same way. Same rule: a miss says less, never something untrue.
	Verifications map[string]Verification

	// Notes carries anything the collector could not read.
	Notes []string
}

// FreightNamed is the freight for a reference, and whether it was readable.
func (s *Snapshot) FreightNamed(namespace, name string) (Freight, bool) {
	f, ok := s.Freight[namespace+"/"+name]
	return f, ok
}

// Carrying is what a freight holds, in one phrase, or "" when the freight was
// not readable and the sentence has to be written without it. Never the bare
// hash: a summary that says "stopped receiving f-7c3d9a1" reads as detail
// while telling a reader strictly less than "stopped receiving artifacts".
func (s *Snapshot) Carrying(namespace, name string) string {
	f, ok := s.FreightNamed(namespace, name)
	if !ok || len(f.Artifacts) == 0 {
		return ""
	}
	return f.Describe()
}

// VerificationOf is the AnalysisRun a Stage's verification is, if it was
// readable.
func (s *Snapshot) VerificationOf(st Stage) (Verification, bool) {
	v, ok := s.Verifications[st.VerificationRunNamespace+"/"+st.VerificationRunName]
	return v, ok
}

// promotionsByStage groups newest-first.
func (s *Snapshot) promotionsByStage() map[string][]Promotion {
	out := map[string][]Promotion{}
	for _, p := range s.Promotions {
		out[p.Stage] = append(out[p.Stage], p)
	}
	for k := range out {
		ps := out[k]
		// Newest first. CreatedAt is the reliable one; StartedAt is unset
		// until a promotion begins.
		for i := 0; i < len(ps); i++ {
			for j := i + 1; j < len(ps); j++ {
				if ps[j].CreatedAt.After(ps[i].CreatedAt) {
					ps[i], ps[j] = ps[j], ps[i]
				}
			}
		}
		out[k] = ps
	}
	return out
}
