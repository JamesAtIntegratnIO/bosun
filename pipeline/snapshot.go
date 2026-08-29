package pipeline

import (
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
	// Notes carries anything the collector could not read.
	Notes []string
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
