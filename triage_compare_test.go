package main

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// A handoff is somebody's next twenty minutes. "The chart removed its
// ClusterRole and no release note explains why" hands over a search; the same
// sentence with the commit that removed it hands over an answer.
//
// These are about WHERE that reaches, and -- more carefully -- where it does
// not. The mechanical path must never see it: an edit's evidence is the gate
// report alone, and a commit message that happens to contain a version number
// must not become corroboration for writing one.

// comparingUpstream is a resolver that also answers the compare question, so
// the type assertion in upstreamFor has something to find.
type comparingUpstream struct {
	fakeUpstream
	compare *upstream.Compare
	terms   []string
	calls   int
}

func (c *comparingUpstream) Compare(_ context.Context, _, _, _ string, terms []string) (*upstream.Compare, error) {
	c.calls++
	c.terms = terms
	return c.compare, nil
}

const rbacRemovedReport = "<!-- gitops-gate -->\n### Resources\n\n" +
	"**Removed (2)**\n\n- `ClusterRole/explorer`\n- `ClusterRoleBinding/explorer`\n\n" +
	"**Added (1)**\n\n- `Role/explorer`\n"

func withCompare(h *harness, c *upstream.Compare) *comparingUpstream {
	up := &comparingUpstream{
		fakeUpstream: fakeUpstream{notes: &upstream.Notes{
			SourceRepo: "example-org/explorer",
			Note:       "No upstream release notes: none tagged in this range.",
		}},
		compare: c,
	}
	h.triage.Upstream = up
	return up
}

func realCompare() *upstream.Compare {
	return &upstream.Compare{
		Range: "v0.5.8...v1.0.0", URL: "http://x/compare", Total: 42,
		Relevant: []upstream.Commit{{
			SHA: "ccccccccccc", Message: "fix: drop the ClusterRole, ship a Role",
			URL: "http://x/commit/c",
		}},
		Files: []string{"charts/explorer/templates/clusterrole.yaml"},
		Note:  "Upstream commits from example-org/explorer, v0.5.8...v1.0.0.",
	}
}

func TestAnEscalationCarriesTheCommitsThatExplainTheFinding(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: rbacRemovedReport}}
	up := withCompare(h, realCompare())
	h.model.Verdict = &llm.Verdict{
		Classification:   llm.ClassEscalate,
		Summary:          "The chart replaced cluster-scoped RBAC with namespaced RBAC.",
		EscalationReason: "removed RBAC",
	}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("posted %d comments", len(h.git.Posted))
	}
	body := h.git.Posted[0]
	for _, want := range []string{
		"What upstream says",
		"drop the ClusterRole",
		"http://x/commit/c",
		"charts/explorer/templates/clusterrole.yaml",
		"testimony",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the handoff does not carry %q:\n%s", want, body)
		}
	}
	// Aimed by the gate's own findings, not by the model.
	if !containsAll(up.terms, "explorer", "ClusterRole") {
		t.Errorf("the search was aimed at %v, not at what the gate found", up.terms)
	}
}

// The absence has to read as an absence. A handoff with no upstream section and
// no explanation of why is a reader assuming nobody looked.
func TestAnEscalationWithNothingUpstreamSaysNothingRatherThanAnEmptySection(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: rbacRemovedReport}}
	withCompare(h, &upstream.Compare{
		Range: "v0.5.8...v1.0.0", Total: 300,
		Note: "300 commit(s) between v0.5.8...v1.0.0 in example-org/explorer, and none of them mentions what the gate found.",
	})
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "Removed RBAC."}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.git.Posted[0], "What upstream says") {
		t.Fatalf("an empty upstream section was rendered:\n%s", h.git.Posted[0])
	}
}

// The rule that keeps the safety story intact. A commit message is testimony
// and testimony is never evidence for a write -- so the mechanical path does
// not even pay for the lookup.
func TestTheMechanicalPathNeverReadsUpstream(t *testing.T) {
	h := newHarness(t)
	up := withCompare(h, realCompare())
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Move the metallb pin with the chart.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion", From: "0.16.0", To: "0.16.1",
		}},
	}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("the fix did not land: %+v", h.git.Pushes)
	}
	if up.calls != 0 || up.fakeUpstream.calls != 0 {
		t.Fatalf("upstream was read on the path that writes files (%d notes, %d compares)",
			up.fakeUpstream.calls, up.calls)
	}
}

// A resolver that only reads releases keeps working. The compare question lives
// on a second interface precisely so a third-party Resolver does not stop
// compiling because a feature it does not implement arrived.
func TestAResolverThatCannotCompareStillExplains(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: rbacRemovedReport}}
	h.triage.Upstream = &fakeUpstream{notes: &upstream.Notes{
		SourceRepo: "example-org/explorer",
		Releases:   []upstream.Release{{Tag: "v1.0.0", Body: "Breaking changes."}},
		Note:       "Upstream notes from example-org/explorer.",
	}}
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "Removed RBAC."}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.git.Posted[0], "What upstream says") {
		t.Fatal("a resolver with no Compare method produced a commits section")
	}
}

// A green gate flagged for a second look is the other place a human is about to
// spend time, so it gets the same handoff.
func TestAFlaggedGreenGateCarriesTheCommitsToo(t *testing.T) {
	h := newHarness(t)
	h.triage.Explain = true
	h.git.Check = gitprovider.CheckSuccess
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: rbacRemovedReport}}
	withCompare(h, realCompare())
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassEscalate,
		Summary:        "The chart replaced cluster-scoped RBAC with namespaced RBAC.",
	}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	body := h.git.Posted[0]
	if !strings.Contains(body, "drop the ClusterRole") {
		t.Errorf("the flagged explanation does not carry the commits:\n%s", body)
	}
	// Provenance, always: this one had commits and no release notes, and a
	// reader deciding how much to trust it needs to know which.
	if !strings.Contains(body, "no release note in this range explains it") {
		t.Errorf("the provenance does not say what it was grounded in:\n%s", body)
	}
}

func containsAll(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, s := range list {
			if s == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
