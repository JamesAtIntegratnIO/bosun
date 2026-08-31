package agent

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
)

// The attempt cap has exactly one memory: a label on the pull request. Both
// repair paths used to push first and label afterwards, logging a label
// failure and carrying on, so a token with push permission and no permission
// to label repaired, failed to record it, counted zero attempts on the next
// run and repaired again, indefinitely.
//
// Reserving the label first inverts every failure: a cap that cannot be
// recorded is a pull request that gets a human instead of a commit.
func TestTheAttemptCapRefusesToPushWhenItCannotBeRecorded(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	h.git.LabelErr = errors.New("403 Resource not accessible by integration")
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Move the metallb pin with the chart.",
		Reasoning:      "The rendered diff proves the default changed.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
		}},
	}

	// escalate also labels, so with a host refusing every label this returns
	// the error. What matters is what did not happen.
	_ = h.triage.Run(context.Background(), promotion())

	if len(h.git.Pushes) != 0 {
		t.Fatalf("pushed a fix the attempt cap could not record: %+v", h.git.Pushes)
	}
	if len(h.git.LabelAttempts) == 0 {
		t.Fatal("the attempt label was never even tried")
	}
	if !strings.HasPrefix(h.git.LabelAttempts[0], "bosun/attempt-") &&
		!strings.HasPrefix(h.git.LabelAttempts[0], labelAttempt) {
		t.Errorf("the first write must be the attempt label, got %q", h.git.LabelAttempts[0])
	}
}

// The ordering is the fix, so it is asserted directly rather than only through
// the failure case: the label must be on the pull request before the push, or
// a crash between the two loses the attempt.
func TestTheAttemptIsRecordedBeforeTheFixIsPushed(t *testing.T) {
	h := newHarness(t)
	h.git.Check = gitprovider.CheckFailure
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Move the metallb pin with the chart.",
		Reasoning:      "The rendered diff proves the default changed.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
		}},
	}
	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("want one push, got %d", len(h.git.Pushes))
	}
	if len(h.git.Labelled) == 0 || !strings.Contains(h.git.Labelled[0], "attempt") {
		t.Errorf("the attempt label must be written first, got %v", h.git.Labelled)
	}
}

// What the cap has spent is readable from outside this package, and it is the
// same arithmetic the cap itself does.
//
// A read surface publishes "attempts used against the cap" so a caller can
// tell an agent that will try again from one that is finished. That number is
// counted from labels under a prefix that follows the brand, and a second
// implementation of the count would be a second answer: one that says the
// agent will retry while this one has already escalated. So there is one
// method, this package's own path calls it too, and the label prefix stays
// private.
func TestWhatTheCapHasSpentIsReadableFromOutside(t *testing.T) {
	for _, tc := range []struct {
		name   string
		brand  string
		labels []string
		want   int
	}{
		{"nothing yet", "", nil, 0},
		{"one attempt", "", []string{"bosun/attempt-1"}, 1},
		{"attempts and other labels", "", []string{
			"dependencies", "bosun/attempt-1", "bosun/attempt-2", "bosun/escalated"}, 2},
		{"a renamed agent counts its own", "Deckhand",
			[]string{"deckhand/attempt-1", "bosun/attempt-1"}, 1},
		{"a label that merely mentions the prefix", "",
			[]string{"needs bosun/attempt-1"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &Triage{Brand: tc.brand, MaxAttempts: 2}
			if got := tr.AttemptsUsed(tc.labels); got != tc.want {
				t.Errorf("want %d attempts used, got %d, from %v", tc.want, got, tc.labels)
			}
		})
	}
}

// And there is exactly one place that counts, so the published number and the
// number that stops a repair cannot drift apart.
//
// Derived from this package's own syntax tree rather than from a string search
// for today's call: what has to stay true is that the raw counter has one
// caller, not that one line reads the way it reads now. A second call site
// added anywhere in this package fails this the day it is written, which a
// grep for a spelling cannot do.
func TestOnlyOneThingCountsWhatTheCapHasSpent(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("could not parse this package: %v", err)
	}
	pkg, ok := pkgs["agent"]
	if !ok {
		t.Fatal("this walk did not find package agent; it is reading the wrong directory")
	}

	callers := map[string]int{}
	calls := 0
	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "attemptsSoFar" {
					calls++
					callers[fn.Name.Name]++
				}
				return true
			})
		}
	}

	// The self-check, and not optional: a walk that finds no calls compares
	// nothing and reads exactly like a pass.
	if calls == 0 {
		t.Fatal("this walk found no call to attemptsSoFar; the counter has been renamed or " +
			"moved and this test is proving nothing. Fix the walk rather than deleting it.")
	}
	for name, n := range callers {
		if name != "AttemptsUsed" {
			t.Errorf("%s calls attemptsSoFar directly (%d time(s)).\n"+
				"AttemptsUsed is the one count, because a read surface publishes it as what "+
				"the agent has spent against the cap. Two counts is how a caller is told an "+
				"attempt remains by one half while the other has already escalated -- and "+
				"they disagree only on a renamed install, where the prefix differs.", name, n)
		}
	}
}
