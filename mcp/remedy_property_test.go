package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/internal/nametest"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// The grammar guarantee, at the boundary where it matters most.
//
// TestAHostileNameReachesTheWireWithoutARemedy makes this claim about one name
// somebody wrote down. This makes it about generated ones, spanning both sides
// of the grammar: whitespace, newlines, shell metacharacters, backticks, path
// separators, hyphens at either end, a segment one character past 63, and
// non-ASCII, along with the legal shapes Kargo actually writes.
//
// pipeline owns the grammar and its own property test covers every detector
// against it; this one covers the trip out. A remedy is the one field on this
// surface built to be run, and by the time it is JSON on a listener that
// something reachable from outside the cluster is speaking to, it may be
// pasted by a client with no hands on a keyboard. The mapping in report.go is
// four lines and adds no fact, which is exactly the kind of code that gets
// rewritten by somebody who does not know that an absent remedy is a sentence.
//
// This test file imports internal/nametest, which pulls in Kubernetes'
// validators. That is not a hole in TestThisPackageCannotReachTheOutsideWorld:
// that walk reads this package's non-test files, because the rule it enforces
// is about what the shipped package can call. A test binary is not shipped.
const (
	// wireSeed fixes what this test draws, so a counterexample it prints is one
	// a reader gets back by rerunning it.
	wireSeed = 0x77697265
	// wireCasesPerShape is smaller than pipeline's, because every case here
	// costs a supervisor sweep and an HTTP server rather than a function call,
	// and the claim is narrower: pipeline proves the grammar holds across every
	// detector, this proves the trip out preserves it.
	wireCasesPerShape = 2
	// wireNames is the Stage, its Promotion, and the freight -- the three
	// pieces the wedged remedy interpolates.
	wireNames = 3
)

func TestNoGeneratedNameOutsideTheGrammarReachesTheWireInACommand(t *testing.T) {
	// Three names per case -- the Stage, its Promotion, and the freight --
	// because those are the three the wedged remedy interpolates, and they are
	// generated together so the answer is the same for all of them. The
	// namespace stays bosun's own literal: a fixture where every piece is
	// illegal cannot tell a remedy withheld for the right reason from one
	// withheld for any reason at all.
	drawn := map[nametest.Shape]int{}
	for _, c := range nametest.Corpus(wireSeed, wireCasesPerShape, wireNames) {
		drawn[c.Shape]++
		stage, promotion, freight := c.Names[0], c.Names[1], c.Names[2]
		legal := nametest.AllValid(stage, promotion, freight)

		w := wedged()
		w.kargo.stages = append(w.kargo.stages, cluster.KargoStage{
			Name: stage, Namespace: "addons", Ready: true,
		})
		w.kargo.promotions = append(w.kargo.promotions, cluster.KargoPromotion{
			Name: promotion, Namespace: "addons", Stage: stage, Freight: freight,
			Phase: pipeline.PhaseErrored, CreatedAt: sweptAt.Add(-72 * time.Hour),
		})

		raw := newFixture(t, sweep(t, w)).call(t, "pipeline_report")
		var got Report
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("the result does not decode as a Report: %v", err)
		}
		if got.Findings == nil {
			t.Fatalf("a completed sweep carrying a %s Stage published no findings field", c.Shape)
		}

		var found bool
		for _, fi := range *got.Findings {
			if fi.Subject != stage {
				continue
			}
			found = true
			switch {
			case legal && fi.Remedy == nil:
				t.Fatalf("a %s finding for a name Kubernetes itself accepts (%s, %q) reached the "+
					"wire with no remedy; an absent remedy on this surface says no remedy exists, "+
					"and here one does", fi.Kind, c.Shape, stage)
			case !legal && fi.Remedy != nil:
				t.Fatalf("a name outside the grammar (%s, %q) reached a command on the wire:\n%s",
					c.Shape, stage, fi.Remedy.Command)
			}
		}
		// The finding itself must survive either way. Dropping it would spend
		// a stopped Stage nobody is told about to buy a command that is not
		// emitted in that case anyway.
		if !found {
			t.Fatalf("the %s Stage (%q) produced no finding at all; a name bosun cannot vouch "+
				"for costs the remedy, not the report that the pipeline has stopped", c.Shape, stage)
		}

		// Belt and braces, over every command in the response rather than the
		// one finding: a name reaches a subject and a summary legitimately,
		// and must reach no command anywhere.
		if legal {
			continue
		}
		for _, cmd := range commandsIn(t, raw) {
			for _, name := range c.Names {
				if strings.Contains(cmd, name) {
					t.Fatalf("a command in the response carries %q, which is outside the grammar:\n%s", name, cmd)
				}
			}
		}
	}

	// The self-check, and not optional: this test's whole claim is the breadth
	// of what it fed the surface, and a corpus that quietly stopped drawing a
	// shape narrows that claim without narrowing the sentence above it.
	for _, s := range nametest.Shapes {
		if drawn[s] != wireCasesPerShape {
			t.Errorf("the corpus drew %d %s cases, want %d; this test covered less than it says it did",
				drawn[s], s, wireCasesPerShape)
		}
	}
}
