package pipeline

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/internal/nametest"
)

// The guarantee, as a property rather than as a list.
//
// remedy_test.go asserts it by example: a corpus somebody wrote down, run
// through the detectors, and every remedy checked for the names in it. That
// corpus is the shapes somebody thought of, and the value of this file is the
// ones nobody did -- a name whose only fault is a trailing hyphen, a segment
// one character past 63, a Unicode homoglyph in the middle of an otherwise
// ordinary Stage name.
//
// Two properties, and they are different claims:
//
//   - one shape per fixture, which fixes the expected answer exactly: every
//     interpolated name stands or falls together, so a remedy that carries a
//     name must be present and one that would have carried an illegal name
//     must be absent. That is the two-sided assertion, and it fails whether
//     the check is removed, weakened, or quietly tightened past what Kargo
//     writes.
//
//   - mixed shapes per fixture, which fixes nothing about presence and asserts
//     only what the guarantee actually says: no command carries a segment
//     outside the grammar. That is the half that catches a builder validating
//     one piece and interpolating another, which the first property cannot see
//     because the piece it validated was illegal too.
//
// The oracle is Kubernetes' own validators, in internal/nametest, and
// deliberately not safeName. A property test that computes its expectation
// with the function under test agrees with that function about a grammar
// somebody has just weakened.
//
// # What this file does not cover
//
// Object names, and not the other two grammars a remedy interpolates. The file
// path and the yq key in the dead-pin command are held at bosun's own literals
// throughout, which is what makes the assertion above two-sided: an illegal
// Stage name has to cost exactly the remedies that would have carried it and
// leave the ones that would not alone, and a fixture where every string is
// hostile cannot tell that from a detector that gave up wholesale.
//
// Those two stay covered by example, in TestARepositoryPathOutsideItsGrammarYieldsNoRemedy.
// Generalising them is a different job with a different oracle: safePath and
// safeKey are bosun's own grammars rather than a standard somebody else
// validates, so there is no independent authority to check them against, and a
// generated test would have to restate the rule it is testing.

const (
	// corpusSeed and mixedSeed fix what gets generated. Two seeds rather than
	// one so the two properties are not looking at the same names.
	corpusSeed = 0x626f73756e
	mixedSeed  = 0x6b6172676f
	// casesPerShape is how many times each shape is drawn with a fresh random
	// core. Fifteen shapes, so this is 15x cases; raising it widens the search
	// and costs milliseconds.
	casesPerShape = 8
	mixedCases    = 200
	// remedyPositions is how many distinct names one fixture interpolates: one
	// per field of remedyNames. Written here because a corpus has to be asked
	// for a size before there is a struct to measure, and derived from the
	// struct in namesFrom, which is where the two can be compared.
	remedyPositions = 11
)

// remedyNames is one fixture's worth of interpolated positions: one field for
// every place in a snapshot whose value can reach a command.
//
// Named fields rather than indices into a slice, because a fixture that put
// the freight where the namespace goes still detects findings and still
// composes remedies, and would assert the property about a world nobody has.
type remedyNames struct {
	namespace     string
	wedged        string
	verifying     string
	pending       string
	orphaned      string
	warehouse     string
	promoWedged   string
	promoPending  string
	promoOrphaned string
	freight       string
	verifyID      string
}

// namesFrom fills every position from one draw, and refuses to build a fixture
// that would leave one empty.
//
// Both halves of the self-check are load-bearing, and neither is the length
// check the obvious version of this function stops at. A field added to
// remedyNames and forgotten here keeps the zero value; nametest.Valid("") is
// true, because an empty name means "not known" and every builder replaces it
// with bosun's own placeholder. So the new position would assert nothing, the
// suite would stay green, and the thing that went uncovered is a place a name
// reaches a command.
func namesFrom(t *testing.T, names []string) remedyNames {
	t.Helper()
	if fields := reflect.TypeOf(remedyNames{}).NumField(); fields != remedyPositions {
		t.Fatalf("remedyNames has %d fields and remedyPositions says %d. Every field is a place "+
			"a name reaches a command, so raise the constant and fill the new one in below.",
			fields, remedyPositions)
	}
	if len(names) != remedyPositions {
		t.Fatalf("a fixture needs %d names and was handed %d", remedyPositions, len(names))
	}
	out := remedyNames{
		namespace:     names[0],
		wedged:        names[1],
		verifying:     names[2],
		pending:       names[3],
		orphaned:      names[4],
		warehouse:     names[5],
		promoWedged:   names[6],
		promoPending:  names[7],
		promoOrphaned: names[8],
		freight:       names[9],
		verifyID:      names[10],
	}
	v := reflect.ValueOf(out)
	for i := range v.NumField() {
		if v.Field(i).String() == "" {
			t.Fatalf("remedyNames.%s was left empty. An unnamed position is the one case this "+
				"corpus cannot test: it is legal by construction, and stands in for a name that "+
				"was never generated.", reflect.TypeOf(out).Field(i).Name)
		}
	}
	return out
}

// The repository's own strings, held at values a real gitops repository has.
//
// The dead-pin remedy interpolates a file path and a yq key and no object name
// at all, and those two have their own grammars for the reasons remedy.go
// gives. Pinning them here is what lets this file assert the sharper claim: an
// illegal Stage name costs exactly the remedies that would have carried it,
// and leaves the ones that would not alone. A test that made everything
// illegal at once could not tell that from a detector that gave up wholesale.
const (
	fixturePath = "addons/kyverno/values.yaml"
	fixtureKey  = "image.tag"
	// fixtureStage is a Stage name bosun wrote, for the one join that needs a
	// name Kargo could actually have produced. See the pull requests below.
	fixtureStage = "external-secrets"
)

// remedyCarriesAName is which kinds' commands interpolate an object name.
//
// False is not "no remedy": the superseded-pull-request command is built from
// pull request numbers and the dead-pin command from the two literals above,
// so both must be present no matter how hostile the names around them are.
// The self-check in each test below pins this map to allKinds, so a detector
// added next year is a kind missing from here rather than one nobody asserted.
var remedyCarriesAName = map[Kind]bool{
	KindWedged:       true,
	KindStalled:      true,
	KindOrphanedPR:   true,
	KindVerifyStuck:  true,
	KindPendingStuck: true,
	KindDeadPin:      false,
	KindSupersededPR: false,
}

// hostileSnapshot is one world built entirely from generated names, arranged
// so that every detector fires.
//
// The same arrangement as the by-example test in remedy_test.go, with one
// difference that matters: nothing here is derived from another name by
// concatenation. `bad + "-verify"` is how the example fixture gets a second
// Stage, and for a name that ends in a hyphen it produces a name that is
// perfectly legal -- which would make this file expect a remedy the detector
// was right to withhold, and read as a bug in the check.
func hostileSnapshot(n remedyNames) *Snapshot {
	branch := func(stage string, i int) string {
		return "kargo/promotion/" + stage + ".01j9x.a1b2c" + strconv.Itoa(i)
	}
	return &Snapshot{
		Now: now,
		Stages: []Stage{
			// Wedged, and promoting into a file that does not set the key.
			{Name: n.wedged, Namespace: n.namespace, Ready: true,
				Updates: []Update{{Path: fixturePath, Keys: []string{fixtureKey}}}},
			// Held by a verification that has already answered.
			{Name: n.verifying, Namespace: n.namespace, Ready: false,
				ReadyReason: "VerificationFailed", ReadySince: 3 * time.Hour,
				VerificationPhase: VerifyFailed, VerificationID: n.verifyID},
			// A promotion queued behind something for longer than anything drains in.
			{Name: n.pending, Namespace: n.namespace, Ready: true},
			// Running against a branch whose pull request was closed under it.
			{Name: n.orphaned, Namespace: n.namespace, Ready: true},
		},
		Warehouses: []Warehouse{{
			Name: n.warehouse, Namespace: n.namespace, Ready: false,
			ReadyReason: "RegistryUnreachable",
		}},
		Promotions: []Promotion{
			{Name: n.promoWedged, Namespace: n.namespace, Stage: n.wedged, Freight: n.freight,
				Phase: PhaseErrored, CreatedAt: ago(72 * time.Hour)},
			{Name: n.promoPending, Namespace: n.namespace, Stage: n.pending, Freight: n.freight,
				Phase: PhasePending, CreatedAt: ago(4 * time.Hour)},
			{Name: n.promoOrphaned, Namespace: n.namespace, Stage: n.orphaned, Freight: n.freight,
				Phase: PhaseRunning, CreatedAt: ago(4 * time.Hour), StartedAt: ago(4 * time.Hour)},
		},
		// Two open pull requests for one Stage: only the newest can merge, and
		// neither is the branch the orphaned promotion is waiting on.
		//
		// Twice over, and the second pair is deliberately not generated. The
		// superseded detector joins pull requests to Stages through Kargo's
		// branch convention -- everything before the first dot -- and a
		// generated name that begins with one, `../../etc/x`, puts the first
		// dot at position zero and groups nothing. That is a name no Stage can
		// have rather than a defect in the join, but the fixture still has to
		// reach the detector on every draw, so the literal pair guarantees the
		// finding and the generated pair keeps the hostile branch flowing
		// through it.
		OpenPRs: []PullRequest{
			{Number: 11, Branch: branch(n.wedged, 1)},
			{Number: 12, Branch: branch(n.wedged, 2)},
			{Number: 21, Branch: branch(fixtureStage, 1)},
			{Number: 22, Branch: branch(fixtureStage, 2)},
		},
		// The file is there and the key is not, which is the dead-pin case.
		FileHas: func(path, _ string) (bool, error) {
			if path != fixturePath {
				return false, errNoSuchFile
			}
			return false, nil
		},
	}
}

// byKind groups a report's findings, and fails if a detector this fixture is
// meant to reach produced nothing.
//
// The self-check, and not optional: a fixture that stops reaching a detector
// leaves every assertion below asserting nothing about it and reads exactly
// like a pass. It is also the first acceptance criterion in its own right --
// a name bosun cannot vouch for costs the remedy and not the finding, because
// dropping the finding would trade a forgeable command for a Stage nobody is
// told has stopped.
func byKind(t *testing.T, r *Report, because string) map[Kind][]Finding {
	t.Helper()
	out := map[Kind][]Finding{}
	for _, f := range r.Findings {
		out[f.Kind] = append(out[f.Kind], f)
	}
	for _, k := range allKinds {
		if _, ok := remedyCarriesAName[k]; !ok {
			t.Fatalf("the %s detector is not in remedyCarriesAName, so nothing here knows whether "+
				"its remedy interpolates an object name. Say which it is -- every kind's remedy is a "+
				"command somebody is meant to run.", k)
		}
		if len(out[k]) == 0 {
			t.Fatalf("no %s finding for %s. Either the fixture stopped reaching that detector, or a "+
				"name outside the grammar suppressed the finding along with its remedy; the second "+
				"would leave a stopped pipeline unreported to buy a command nobody gets either way.", k, because)
		}
	}
	return out
}

// A name outside the grammar costs exactly the remedies that would have
// carried it, and a name inside it keeps every one.
//
// Both directions, because the two failures are opposite and both are real. A
// check that has been removed or weakened emits a command with an
// unvalidatable name in it, which is the incident. A check that has been
// tightened past what Kargo writes silently takes the remedy off every wedged
// Stage in production, which is a control causing the outage it was meant to
// prevent -- and it fails no test that only feeds it hostile input.
func TestAGeneratedNameOutsideTheGrammarCostsItsRemedyAndNothingElse(t *testing.T) {
	drawn := map[nametest.Shape]int{}
	for _, c := range nametest.Corpus(corpusSeed, casesPerShape, remedyPositions) {
		drawn[c.Shape]++
		legal := nametest.AllValid(c.Names...)
		for _, name := range c.Names {
			if nametest.Valid(name) != legal {
				t.Fatalf("the %s case mixes legal and illegal names, so nothing here can say whether "+
					"a remedy should exist: %q", c.Shape, name)
			}
		}

		n := namesFrom(t, c.Names)
		found := byKind(t, Detect(hostileSnapshot(n)), "names of shape "+string(c.Shape))
		for kind, fs := range found {
			want := legal || !remedyCarriesAName[kind]
			for _, f := range fs {
				switch {
				case want && f.Remedy == "":
					t.Fatalf("a %s finding lost its remedy for a name Kubernetes itself accepts (%s, %q). "+
						"A grammar narrower than the one that admitted the object takes the command off "+
						"every real Stage in this state.", kind, c.Shape, f.Subject)
				case !want && f.Remedy != "":
					t.Fatalf("a %s finding kept its remedy for a name outside the grammar (%s, %q):\n%s",
						kind, c.Shape, f.Subject, f.Remedy)
				}
			}
		}

		// And over the commands that survived a world made entirely of illegal
		// names, which today is the dead-pin one and the superseded-pull-request
		// one: both are composed from the literals above and from pull request
		// numbers, so neither should have picked anything up.
		//
		// It cannot fail as the detectors stand -- every other remedy was
		// asserted empty ten lines up, so there is nothing left for this to
		// scan. It is here for the version of deadKeyCmd that starts naming the
		// Stage it is about, which is a reasonable thing to want and would leak
		// on the first hostile name. The general form of the claim is the test
		// below; this is the cheap guard on the two kinds that keep a command
		// no matter how hostile the world around them is.
		if legal {
			continue
		}
		for _, f := range found {
			for _, fi := range f {
				for _, name := range c.Names {
					if fi.Remedy != "" && strings.Contains(fi.Remedy, name) {
						t.Fatalf("the %s remedy carries %q, which is outside the grammar:\n%s",
							fi.Kind, name, fi.Remedy)
					}
				}
			}
		}
	}

	for _, s := range nametest.Shapes {
		if drawn[s] != casesPerShape {
			t.Errorf("the corpus drew %d %s cases, want %d; this test covered less than it says it did",
				drawn[s], s, casesPerShape)
		}
	}
}

// No command carries a generated name outside the grammar, whatever mixture of
// legal and illegal names the world it was composed from held.
//
// This is the guarantee stated the way it is actually written, and it is the
// half the test above cannot make. There, every position is illegal at once,
// so a builder that validates its Stage name and interpolates an unvalidated
// namespace still returns an empty command: the name it did check was illegal
// too. Here most fixtures are mixed, so that builder emits a command, and the
// unvalidated piece is in it.
func TestNoCommandCarriesAGeneratedNameOutsideTheGrammar(t *testing.T) {
	both := 0
	for _, names := range nametest.Mixed(mixedSeed, mixedCases, remedyPositions) {
		if !nametest.AllValid(names...) && anyValid(names) {
			both++
		}
		n := namesFrom(t, names)
		found := byKind(t, Detect(hostileSnapshot(n)), "a mixture of legal and illegal names")
		for _, fs := range found {
			for _, f := range fs {
				if f.Remedy == "" {
					continue
				}
				for _, name := range names {
					if nametest.Valid(name) || !strings.Contains(f.Remedy, name) {
						continue
					}
					t.Fatalf("the %s remedy interpolated %q, which is outside the grammar. Some "+
						"other piece of this command was validated and this one was not:\n%s",
						f.Kind, name, f.Remedy)
				}
			}
		}
	}

	// The self-check: a corpus that happened to draw every position from one
	// side of the grammar would assert nothing this file does not already.
	if both < mixedCases/4 {
		t.Fatalf("only %d of %d fixtures held both a legal and an illegal name; this test is not "+
			"exercising the case it exists for", both, mixedCases)
	}
}

func anyValid(names []string) bool {
	for _, n := range names {
		if nametest.Valid(n) {
			return true
		}
	}
	return false
}
