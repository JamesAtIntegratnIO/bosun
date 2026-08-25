package main

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The gate can only ever say what a repository holds. These are about the
// other half -- and, more carefully, about the difference between a zero that
// was counted and a zero that is the shape of nobody having looked.

func droppedReport() string {
	return gate.ReportMarker + "\n### Resources\n\n" +
		"**A CustomResourceDefinition stopped serving a version**\n\n" +
		migrate.Line("CustomResourceDefinition/externalsecrets.external-secrets.io",
			"v1alpha1, v1beta1", "ExternalSecret", "v1") + "\n"
}

func TestABriefSaysHowManyObjectsAreLiveOnTheDroppedVersions(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: droppedReport()}}
	h.triage.Cluster = &cluster.Fake{
		Counts: map[string]cluster.Count{
			"external-secrets.io/v1alpha1/externalsecrets": {Known: true},
			"external-secrets.io/v1beta1/externalsecrets":  {N: 4, Known: true},
		},
	}
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "A CRD stops serving versions."}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	// It reaches the model as fact, clearly labelled.
	if !strings.Contains(h.model.User, "LIVE CLUSTER (fact, read-only)") {
		t.Fatal("the live block never reached the prompt")
	}
	if !strings.Contains(h.model.User, "4 live object(s)") {
		t.Fatalf("the count is not in the prompt:\n%s", h.model.User)
	}
	// And it reaches the human.
	if !strings.Contains(h.git.Posted[0], "What is actually running") {
		t.Fatalf("the handoff carries no live section:\n%s", h.git.Posted[0])
	}
}

// The whole value of "0 live objects" is that it ends a conversation, and it
// can only do that if it never means "nobody checked".
func TestNotPermittedIsNeverPrintedAsZero(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: droppedReport()}}
	// An empty fake refuses everything, which is what an API group nobody put
	// in liveReads.apiGroups does in production.
	h.triage.Cluster = &cluster.Fake{}
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "A CRD stops serving versions."}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	body := h.git.Posted[0]
	if strings.Contains(body, "0 live object") {
		t.Fatalf("a refusal was printed as a count of zero:\n%s", body)
	}
	if !strings.Contains(body, "not permitted to check") {
		t.Fatalf("the brief does not say what was not checked:\n%s", body)
	}
	// And the model is told what that phrase means, because the tempting
	// reading of it is "fine".
	if !strings.Contains(h.model.User, "NOBODY LOOKED") {
		t.Fatal("the prompt does not tell the model that a refusal is not a zero")
	}
}

// A partial answer must not become a total. One version counted and one
// refused is not "4 live objects", it is "we do not know".
func TestAPartialCountIsNotPresentedAsATotal(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: droppedReport()}}
	h.triage.Cluster = &cluster.Fake{
		Counts: map[string]cluster.Count{
			"external-secrets.io/v1beta1/externalsecrets": {N: 4, Known: true},
		},
	}
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "x"}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.git.Posted[0], "4 live object") {
		t.Fatalf("a count with a hole in it was reported as a total:\n%s", h.git.Posted[0])
	}
}

// A chart that stops shipping a definition ENTIRELY names the object and not
// its versions, so the versions have to come from the cluster -- which still
// has the definition, because this runs before the change is applied.
func TestARemovedDefinitionGetsItsVersionsFromTheCluster(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: gate.ReportMarker +
		"\n### Resources\n\n**Removed (1)**\n\n- `CustomResourceDefinition/policyexceptions.kyverno.io`\n"}}
	fake := &cluster.Fake{
		CRDs:   map[string]cluster.CRD{"policyexceptions.kyverno.io": {Versions: []string{"v2beta1"}, Known: true}},
		Counts: map[string]cluster.Count{"kyverno.io/v2beta1/policyexceptions": {Known: true}},
	}
	h.triage.Cluster = fake
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "A CRD is going away."}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(fake.CRDCalls) != 1 || fake.CRDCalls[0] != "policyexceptions.kyverno.io" {
		t.Fatalf("the definition was not read: %v", fake.CRDCalls)
	}
	if !strings.Contains(h.git.Posted[0], "0 live object(s)") {
		t.Fatalf("the count is missing:\n%s", h.git.Posted[0])
	}
}

// verifyApps has been on the wire since the first release and nothing read it.
// "Already Degraded before your bump" is the most useful sentence available to
// somebody looking at a red gate.
func TestTheApplicationsThePromotionNamedAreReported(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: gateReport}}
	h.triage.Cluster = &cluster.Fake{
		Apps: map[string]cluster.Health{
			"external-secrets-host": {Status: "Degraded", Sync: "OutOfSync", Known: true},
		},
	}
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "x"}

	p := promotion()
	p.VerifyApps = []string{"external-secrets-host"}
	if err := h.triage.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.git.Posted[0], "Degraded / OutOfSync") {
		t.Fatalf("the Application's live state is missing:\n%s", h.git.Posted[0])
	}
}

// Off is off. A deployment that has not opted in makes no cluster calls at
// all, and its briefs read exactly as they did before this existed.
func TestWithNoReaderNothingIsClaimedAboutTheCluster(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: droppedReport()}}
	h.triage.Cluster = nil
	h.model.Verdict = &llm.Verdict{Classification: llm.ClassEscalate, Summary: "x"}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"What is actually running", "LIVE CLUSTER"} {
		if strings.Contains(h.git.Posted[0]+h.model.User, s) {
			t.Fatalf("a live section appeared with no reader configured: %q", s)
		}
	}
}

// The deterministic repair's own comment carries it too: "27 manifests moved,
// and 0 objects were live on the version they moved off" is the sentence that
// says the repair was complete.
func TestTheMigrationCommentCarriesTheLiveCount(t *testing.T) {
	h := newHarness(t)
	h.triage.Migrate = true
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: droppedReport()}}
	h.triage.Cluster = &cluster.Fake{
		Counts: map[string]cluster.Count{
			"external-secrets.io/v1alpha1/externalsecrets": {Known: true},
			"external-secrets.io/v1beta1/externalsecrets":  {Known: true},
		},
	}
	h.writeFile(t, "addons/external-secrets/externalsecret.yaml", externalSecretBefore)

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Posted) == 0 || !strings.Contains(h.git.Posted[0], "What is actually running") {
		t.Fatalf("the migration comment carries no live section:\n%v", h.git.Posted)
	}
	if h.model.Calls != 0 {
		t.Fatal("the deterministic path consulted the model")
	}
}
