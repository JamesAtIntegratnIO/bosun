package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

func escalateVerdict() *llm.Verdict {
	return &llm.Verdict{
		Classification:   llm.ClassEscalate,
		Summary:          "This needs a human.",
		Reasoning:        "n/a",
		EscalationReason: "Mixed blocking findings.",
	}
}

// The gate's report for external-secrets 0.10.3 -> 2.9.0, the promotion this
// path exists for: the only blocking finding is CRDs that stopped serving
// versions, and the lines carry the repair contract the gate now emits.
const droppedVersionReport = `<!-- gitops-gate -->
### Resources

**A CustomResourceDefinition stopped serving a version** — anything still declaring it breaks on apply.

- ` + "`CustomResourceDefinition/externalsecrets.external-secrets.io in external-secrets`" + `: no longer serves ` + "`v1alpha1, v1beta1`" + ` — ` + "`ExternalSecret`" + ` manifests must move to ` + "`v1`" + `
- ` + "`CustomResourceDefinition/clustersecretstores.external-secrets.io in external-secrets`" + `: no longer serves ` + "`v1beta1`" + ` — ` + "`ClusterSecretStore`" + ` manifests must move to ` + "`v1`" + `

### Versions

| Application | Cluster | From | To |
|---|---|---|---|
| ` + "`external-secrets`" + ` | host | ` + "`0.10.3`" + ` | ` + "`2.9.0`" + ` |
`

const externalSecretBefore = `apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: token
`

func migrateHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.triage.Migrate = true
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: droppedVersionReport}}
	return h
}

func (h *harness) writeFile(t *testing.T, rel, content string) {
	t.Helper()
	full := filepath.Join(h.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The whole feature in one test: a red gate whose only cause is dropped served
// versions gets its consumers rewritten and pushed -- deterministically. The
// model is never consulted, because there is nothing to judge: the gate named
// the versions, the survivor and the kind, and the rest is arithmetic.
func TestARedGateWithDroppedVersionsIsRepairedWithoutTheModel(t *testing.T) {
	h := migrateHarness(t)
	h.writeFile(t, "addons/external-secrets/externalsecret.yaml", externalSecretBefore)

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}

	if h.model.Calls != 0 {
		t.Errorf("the deterministic path must not consult the model; it was called %d time(s)", h.model.Calls)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("want one pushed migration, got %d", len(h.git.Pushes))
	}
	tree := h.git.Pushes[0].Tree
	migrated := tree["addons/external-secrets/externalsecret.yaml"]
	if !strings.Contains(migrated, "apiVersion: external-secrets.io/v1\n") {
		t.Errorf("the consumer was not migrated:\n%s", migrated)
	}
	if !strings.Contains(h.git.Pushes[0].Message, "migrate 1 manifest(s) off dropped API version(s)") {
		t.Errorf("commit message: %q", h.git.Pushes[0].Message)
	}
	if len(h.git.Labelled) != 1 || h.git.Labelled[0] != "bosun/attempt-1" {
		t.Errorf("want the attempt counted, got %v", h.git.Labelled)
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("want one comment, got %v", h.git.Posted)
	}
	comment := h.git.Posted[0]
	for _, want := range []string{
		"Pushed a migration to `kargo/metallb`.",
		"`addons/external-secrets/externalsecret.yaml`",
		"deterministic repair, no model",
	} {
		if !strings.Contains(comment, want) {
			t.Errorf("comment missing %q:\n%s", want, comment)
		}
	}
	// A counter on the only attempt there will ever be describes a sequence
	// that did not happen; it belongs in the comment only when it is a RE-try.
	if strings.Contains(comment, "attempt") {
		t.Errorf("the first and only attempt should not be numbered:\n%s", comment)
	}
	// The identity header went with the GitHub App: the host renders the name
	// and avatar above every comment already.
	if strings.Contains(comment, "**Bosun**") || strings.Contains(comment, "⚓") {
		t.Errorf("comment should carry no identity header:\n%s", comment)
	}
	// The file list is the commit's, written out again. Present, but not at
	// the cost of pushing the live-cluster facts off the bottom.
	if !strings.Contains(comment, "<details><summary><b>Migrated</b>") {
		t.Errorf("the migrated-file table should be collapsed:\n%s", comment)
	}
	last := h.git.Statuses[len(h.git.Statuses)-1]
	if last.State != gitprovider.StateSuccess || !strings.Contains(last.Description, "migrated 1 manifest(s)") {
		t.Errorf("want a resolved status describing the migration, got %+v", last)
	}
}

// The scope check is deliberately absent on this path -- consumers are files
// the promotion did not touch -- but the deny-list is not, and a repair that
// policy refuses everywhere is an escalation, not a silent success.
func TestAMigrationRefusedEverywhereEscalates(t *testing.T) {
	h := migrateHarness(t)
	// The only consumer sits outside the allowlist (`addons/**`), so policy
	// refuses it -- same machinery that stops a model's stray edit.
	h.writeFile(t, "terraform/externalsecret.yaml", externalSecretBefore)

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}

	if len(h.git.Pushes) != 0 {
		t.Fatalf("nothing may be pushed when everything was refused: %+v", h.git.Pushes)
	}
	if !has(h.git.Labelled, labelNeedsHuman) {
		t.Errorf("want %s, got %v", labelNeedsHuman, h.git.Labelled)
	}
	if len(h.git.Posted) != 1 || !strings.Contains(h.git.Posted[0], "policy refuses every one of them") {
		t.Fatalf("want the refusal explained, got %v", h.git.Posted)
	}
	if !strings.Contains(h.git.Posted[0], "`terraform/externalsecret.yaml`") {
		t.Errorf("the refused file must be named:\n%s", h.git.Posted[0])
	}
}

// The gate counted consumers; this branch has none. Whichever of the two is
// stale, guessing is not the answer -- say so and hand it to a human.
func TestAGateAndBranchDisagreementEscalates(t *testing.T) {
	h := migrateHarness(t)

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("nothing to migrate means nothing to push")
	}
	if !has(h.git.Labelled, labelNeedsHuman) {
		t.Errorf("want %s, got %v", labelNeedsHuman, h.git.Labelled)
	}
	if len(h.git.Posted) != 1 || !strings.Contains(h.git.Posted[0], "disagree") {
		t.Fatalf("want the disagreement stated, got %v", h.git.Posted)
	}
}

// A repairable line next to an unexplained targeting change is not a repair
// opportunity: fixing the fixable half would leave a red gate implying the
// migration had not worked. The model path judges those.
func TestOtherBlockersDisableTheDeterministicPath(t *testing.T) {
	h := migrateHarness(t)
	h.writeFile(t, "addons/external-secrets/externalsecret.yaml", externalSecretBefore)
	h.git.Comments = []gitprovider.Comment{{Author: "gitops-gate", Body: droppedVersionReport +
		"\n### Cluster targeting changed\n\n| Application | Change |\n|---|---|\n| `x` | moved |\n"}}
	h.model.Verdict = escalateVerdict()

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.Calls != 1 {
		t.Errorf("the model must judge a mixed report, calls=%d", h.model.Calls)
	}
	if len(h.git.Pushes) != 0 {
		t.Errorf("no migration may be pushed beside another blocker: %+v", h.git.Pushes)
	}
}

// Migrate off is the pre-0.8.0 behaviour: the report goes to the model.
func TestTheMigrationPathCanBeSwitchedOff(t *testing.T) {
	h := migrateHarness(t)
	h.triage.Migrate = false
	h.writeFile(t, "addons/external-secrets/externalsecret.yaml", externalSecretBefore)
	h.model.Verdict = escalateVerdict()

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.Calls != 1 {
		t.Errorf("with Migrate off the model judges, calls=%d", h.model.Calls)
	}
	if len(h.git.Pushes) != 0 {
		t.Errorf("nothing may be pushed: %+v", h.git.Pushes)
	}
}

// The counter is suppressed on the first attempt and stated on a re-try,
// because only then is it telling the reader something.
func TestTheAttemptCounterAppearsOnlyOnARetry(t *testing.T) {
	tr := &Triage{MaxAttempts: 2}
	if got := tr.attemptSuffix(1); got != "" {
		t.Errorf("first attempt should be unnumbered, got %q", got)
	}
	if got := tr.attemptSuffix(2); got != " (attempt 2 of 2)" {
		t.Errorf("a retry should say so, got %q", got)
	}
}

// A red whose every cause is the chart's own rendering has no repository-side
// fix. The agent used to ask a model to explain that, and got a paragraph
// restating the gate report with the one useful sentence buried in it.
func TestARedWithNoRepositorySideFixIsAnsweredWithoutTheModel(t *testing.T) {
	for _, tc := range []struct {
		name    string
		b       migrate.Blockers
		noModel bool
	}{
		{"only the chart's own apiVersion move", migrate.Blockers{APIVersion: 1}, true},
		{"several of them", migrate.Blockers{APIVersion: 3}, true},
		{"manifests to migrate is a repository fix", migrate.Blockers{APIVersion: 1, Consumers: 2}, false},
		{"settings to remove is a repository fix", migrate.Blockers{ValuesDropped: 48}, false},
		{"targeting is a repository fix", migrate.Blockers{Targeting: 1}, false},
		{"unscanned still wants a human to look here", migrate.Blockers{Unscanned: 1}, false},
		{"nothing blocking is not this path", migrate.Blockers{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.b.Any() && !tc.b.RepoSideRemedy()
			if got != tc.noModel {
				t.Fatalf("deterministic-escalation = %v, want %v for %+v", got, tc.noModel, tc.b)
			}
		})
	}

	reason := noRemedyReason(migrate.Blockers{APIVersion: 1})
	for _, want := range []string{
		"apiVersion",
		"Nothing in this repository declares them",
		"nothing here to rewrite",
	} {
		if !strings.Contains(reason, want) {
			t.Errorf("reason missing %q:\n%s", want, reason)
		}
	}
}

// The stamp the gate writes and the parser that reads it are one format; a
// report from an older gate carries no stamp, and that must not be mistaken
// for "no blockers".
func TestBlockersRoundTripThroughTheReport(t *testing.T) {
	var b strings.Builder
	(&gate.DiffResult{Objects: []gate.ObjectChange{
		{Kind: "apiVersion", Object: "PodDisruptionBudget/x"},
		{Kind: "valuesKeyDropped", Object: "kyverno", Keys: []string{"a", "b"}},
	}}).Report(&b)

	got, ok := migrate.ParseBlockers(b.String())
	if !ok {
		t.Fatal("the report must carry a machine-readable breakdown")
	}
	if got.APIVersion != 1 || got.ValuesDropped != 2 {
		t.Fatalf("round trip lost counts: %+v", got)
	}
	if _, ok := migrate.ParseBlockers("<!-- gitops-gate -->\nan older gate said nothing"); ok {
		t.Fatal("a report with no breakdown must report absence, not zero")
	}
}
