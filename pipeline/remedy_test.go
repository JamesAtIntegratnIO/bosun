package pipeline

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

var errNoSuchFile = errors.New("no such file")

// hostile is the corpus. Every entry is a name Kubernetes' own validation
// would refuse, and every entry ends a shell command and starts another one.
//
// They are not hypothetical in the way they look. The names in a Snapshot come
// from CRDs this package deliberately does not vendor, through a Kargo release
// this build has never seen, and the remedy is the one string on a finding
// that exists to be run. What defends it is that these names are RFC1123
// labels by Kubernetes' own rules -- which is exactly why enforcing it is
// cheap, and why the enforcement is loud when the assumption stops holding.
var hostile = []string{
	"stage; rm -rf /",
	"stage$(id)",
	"stage`id`",
	"stage\nkubectl delete ns kube-system",
	"stage'--all",
	"../../etc/passwd",
	"UPPER",
	"-leading-dash",
	"trailing-dash-",
	strings.Repeat("a", 64),
	"",
}

// A hostile name yields the finding without a remedy, never a remedy with the
// name in it.
//
// The finding still has to be emitted. Dropping it would turn a name bosun
// cannot vouch for into a Stage nobody is told has stopped, which trades a
// forgeable command for a silent pipeline -- and silence is this package's
// whole subject.
func TestAHostileNameYieldsAFindingWithNoRemedy(t *testing.T) {
	for _, name := range hostile {
		if name == "" {
			// An unnamed Stage produces no finding to hang a remedy on; the
			// empty namespace is covered by its own test below.
			continue
		}
		t.Run(name, func(t *testing.T) {
			s := &Snapshot{
				Now:    now,
				Stages: []Stage{{Name: name, Namespace: "addons", Ready: true}},
				Promotions: []Promotion{{
					Name: "p", Namespace: "addons", Stage: name, Freight: "f08f1c9",
					Phase: PhaseErrored, CreatedAt: ago(72 * time.Hour),
				}},
			}
			f := findingOf(t, Detect(s), KindWedged)
			if f.Remedy != "" {
				t.Fatalf("a name outside the label grammar reached a remedy:\n%s", f.Remedy)
			}
			if f.Summary == "" {
				t.Error("the finding itself must survive: a Stage bosun cannot compose a " +
					"command for is still a Stage that has stopped delivering")
			}
		})
	}
}

// Every remedy in a report built entirely of hostile names is empty, whichever
// detector produced it.
//
// The per-detector tests above and below name the ones that exist today. This
// one is the property, and it is the half that covers a detector written next
// year by someone who composed its command the obvious way.
//
// The kinds it must reach come from allKinds, which TestEveryKindHasAMetricSeries
// already pins to the const block. So a new detector is a new kind, a new kind
// is a row this fixture does not produce, and the self-check below fails with
// the kind named rather than passing over a detector nobody tested.
func TestNoRemedyInAHostileReportCarriesAHostileName(t *testing.T) {
	// One name, used everywhere, so a remedy that leaked any piece of the
	// snapshot fails on a substring rather than on a guess about which piece.
	bad := "evil; rm -rf /"
	branch := func(stage string, n int) string {
		return "kargo/promotion/" + stage + ".01j9x.a1b2c" + strconv.Itoa(n)
	}
	s := &Snapshot{
		Now: now,
		Stages: []Stage{
			// Wedged, and its promotion target's pin writes nowhere.
			{Name: bad, Namespace: bad, Ready: true,
				Updates: []Update{{Path: bad + "/values.yaml", Keys: []string{bad}}}},
			// Held by a verification that has already answered.
			{Name: bad + "-verify", Namespace: bad, Ready: false,
				ReadyReason: "VerificationFailed", ReadySince: 3 * time.Hour,
				VerificationPhase: VerifyFailed, VerificationID: bad},
			// A promotion queued behind it for longer than anything drains in.
			{Name: bad + "-pending", Namespace: bad, Ready: true},
			// Running against a branch whose pull request was closed under it.
			{Name: bad + "-orphan", Namespace: bad, Ready: true},
		},
		Warehouses: []Warehouse{{Name: bad, Namespace: bad, Ready: false, ReadyReason: bad}},
		Promotions: []Promotion{
			{Name: bad, Namespace: bad, Stage: bad, Freight: bad, Phase: PhaseErrored,
				CreatedAt: ago(72 * time.Hour)},
			{Name: bad + "-pending", Namespace: bad, Stage: bad + "-pending", Freight: bad,
				Phase: PhasePending, CreatedAt: ago(4 * time.Hour)},
			// Running against a branch no open pull request carries. Its own
			// Stage, because a Stage's newest promotion is what decides
			// whether it reads as wedged, and a Running one on top of the
			// Errored one above would take that finding away.
			{Name: bad + "-orphan.01j9x.a1b2c9", Namespace: bad, Stage: bad + "-orphan", Freight: bad,
				Phase: PhaseRunning, CreatedAt: ago(4 * time.Hour), StartedAt: ago(4 * time.Hour)},
		},
		// Two open pull requests for one Stage: only the newest can merge.
		OpenPRs: []PullRequest{
			{Number: 11, Branch: branch(bad, 1)},
			{Number: 12, Branch: branch(bad, 2)},
		},
		// The file is there and the key is not, which is the dead-pin case.
		FileHas: func(path, _ string) (bool, error) {
			if path != bad+"/values.yaml" {
				return false, errNoSuchFile
			}
			return false, nil
		},
	}

	r := Detect(s)
	got := map[Kind]bool{}
	for _, f := range r.Findings {
		got[f.Kind] = true
		if strings.Contains(f.Remedy, bad) {
			t.Errorf("the %s remedy interpolated a name outside the grammar:\n%s", f.Kind, f.Remedy)
		}
	}

	// The self-check, and not optional: a fixture that stops reaching a
	// detector leaves this test asserting nothing about it and reads exactly
	// like a pass.
	for _, k := range allKinds {
		if !got[k] {
			t.Errorf("this fixture produced no %s finding, so nothing here checked that "+
				"detector's remedy. Extend the snapshot until it does -- every kind's remedy "+
				"is a command somebody is meant to run.", k)
		}
	}
}

// And a legitimate name still gets its remedy, including the shapes that look
// like they would fail.
//
// A Kargo Promotion is named `<stage>.<freight>.<hash>`, so an object name in
// this package is a DNS subdomain rather than a single label, and a check that
// admitted only labels would take the remedy off every wedged Stage in
// production. The dot is the difference between a control and an outage.
func TestTheNamesKargoActuallyWritesKeepTheirRemedy(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "external-secrets", Namespace: "addons", Ready: true}},
		Promotions: []Promotion{{
			Name: "external-secrets.01abc.f08f1c9", Namespace: "addons", Stage: "external-secrets",
			Freight: "f08f1c9", Phase: PhaseErrored, CreatedAt: ago(72 * time.Hour),
		}},
	}
	if f := findingOf(t, Detect(s), KindWedged); f.Remedy == "" {
		t.Fatal("a Stage named the way Kargo names one lost its remedy; the grammar is too narrow")
	}

	s = &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "argo-cd", Namespace: "addons", Ready: true}},
		Promotions: []Promotion{{
			Name: "argo-cd.01j9x.a1b2c3", Namespace: "addons", Stage: "argo-cd", Freight: "a1b2c3",
			Phase: PhaseRunning, CreatedAt: ago(4 * time.Hour), StartedAt: ago(4 * time.Hour),
		}},
		// One open pull request, for a different branch: the detector stays
		// silent when it can see none at all, because it cannot then tell a
		// closed pull request from a git host it could not reach.
		OpenPRs: []PullRequest{{Number: 7, Branch: "kargo/promotion/other.01j9x.999999"}},
	}
	if f := findingOf(t, Detect(s), KindOrphanedPR); f.Remedy == "" {
		t.Fatal("a Promotion named the way Kargo names one -- with dots -- lost its remedy")
	}
}

// An empty namespace is not a hostile name: every builder replaces it with a
// placeholder bosun wrote, and the finding keeps the remedy that names it.
func TestAnUnknownNamespaceKeepsThePlaceholderRemedy(t *testing.T) {
	s := &Snapshot{
		Now:    now,
		Stages: []Stage{{Name: "kyverno", Ready: true}},
		Promotions: []Promotion{{
			Name: "kyverno.01abc.dd21", Stage: "kyverno", Freight: "dd21",
			Phase: PhaseFailed, CreatedAt: ago(72 * time.Hour),
		}},
	}
	f := findingOf(t, Detect(s), KindWedged)
	if !strings.Contains(f.Remedy, "<namespace>") {
		t.Fatalf("an unknown namespace must still yield the remedy with bosun's own placeholder:\n%s", f.Remedy)
	}
}

// The repository's own strings go through a grammar too.
//
// A file path and a yq expression are not object names and Kubernetes
// validates neither, so the label grammar is the wrong shape for them. They
// still reach a command another agent may run, and they come from a promotion
// target in somebody's values file, so they get the narrowest grammar that
// admits what a real one looks like.
func TestARepositoryPathOutsideItsGrammarYieldsNoRemedy(t *testing.T) {
	s := &Snapshot{
		Now: now,
		Stages: []Stage{{Name: "kyverno", Namespace: "addons", Ready: true,
			Updates: []Update{{Path: "addons/kyverno/values.yaml", Keys: []string{"image.tag; id"}}}}},
		// The file is there and the key is not, which is the dead-pin case.
		FileHas: func(path, _ string) (bool, error) {
			if path != "addons/kyverno/values.yaml" {
				return false, errNoSuchFile
			}
			return false, nil
		},
	}
	f := findingOf(t, Detect(s), KindDeadPin)
	if f.Remedy != "" {
		t.Fatalf("a yq expression outside the grammar reached a remedy:\n%s", f.Remedy)
	}
}
