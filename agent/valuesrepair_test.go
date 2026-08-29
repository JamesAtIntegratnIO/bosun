package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/egress"
	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/internal/charttest"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The bump this whole change comes from, from the gate's verdict to the pushed
// file.
//
// The chart is real, served on loopback at two versions; the schema comes out
// of its tarball; the render that proves the answer is helm's. Only the model
// is a double, which is the one part of this that must not be trusted anyway.

// unrenderableReport is what the gate publishes for a chart it could not
// render: the breakdown says so, and nothing else blocks.
func unrenderableReport(from, to string) string {
	return gate.ReportMarker + "\n" +
		migrate.BlockersMarker + "targeting=0 source=0 apiVersion=0 consumers=0 unscanned=0 " +
		"unrenderable=1 valuesDropped=2 schema=0 -->\n" +
		"## 🔴 Blocking — 1 Application whose chart will not render at the new version\n\n" +
		"### The chart does not render at the new version\n\n" +
		fmt.Sprintf("**`thing-prod` on prod — `%s` → `%s`**\n\n", from, to) +
		"```text\nError: values don't meet the specifications of the schema(s) in the following chart(s):\n" +
		"thing:\n- at '': additional properties 'legacy', 'port' not allowed\n```\n"
}

// contractGate is the gate handing its verdict over as a value.
//
// The report still travels as text, because everything else downstream reads
// it that way; the repair contract does not, and that is the point of the
// double: a test that built the contract from the report would be testing a
// scrape nothing performs.
type contractGate struct {
	report       string
	unrenderable []gate.Unrenderable
}

func (g contractGate) Ensure(context.Context, *gitprovider.PullRequest) *gateservice.Outcome {
	return &gateservice.Outcome{
		State: gitprovider.CheckFailure, Report: g.report, Unrenderable: g.unrenderable,
	}
}

const addonsWithStaleValues = `# Cluster addons. Order is deliberate.
coredns:
  enabled: true
  defaultVersion: 1.11.1

thing:
  enabled: true
  defaultVersion: 2.0.0
  valuesObject:
    greeting: hello
    # We pin the port; the NetworkPolicy names it too.
    port: 8080
    legacy: true
`

// valuesHarness wires the agent to a chart repository it can really pull from,
// and to a gate that really could not render it.
func valuesHarness(t *testing.T, proposal *llm.Migration) (*harness, string) {
	return valuesHarnessOn(t, charttest.Strict, proposal)
}

func valuesHarnessOn(t *testing.T, chart func(*testing.T) string, proposal *llm.Migration) (*harness, string) {
	t.Helper()
	charttest.RequireTool(t, "helm")
	repo := chart(t)

	h := newHarness(t)
	h.triage.Structural = true
	// The chart repository is on loopback, and loopback is closed to the agent
	// by default since the security review. Naming it here is the same thing
	// an operator does for a chart museum inside their own cluster, and it is
	// the honest way to run this test: the alternative is a harness whose
	// egress policy is nil, which is a policy no deployment has.
	h.triage.Egress = egress.Policy{AllowPrivate: []string{"127.0.0.0/8"}}
	h.writeFile(t, "addons/values.yaml", addonsWithStaleValues)
	h.model.Migration = proposal

	head := gate.Row{
		Cluster: "prod", App: "thing-prod", Chart: "thing", ChartRepo: repo,
		Version: "2.0.0", SourceType: gate.RowHelm,
		ValuesInline: "greeting: hello\nport: 8080\nlegacy: true\n",
	}
	h.triage.Gate = contractGate{
		report: unrenderableReport("1.0.0", "2.0.0"),
		unrenderable: []gate.Unrenderable{{
			Head: head, From: "1.0.0",
			Reason: "Error: values don't meet the specifications of the schema(s)",
		}},
	}
	return h, repo
}

// The whole feature: a chart that will not render, a proposal the harness
// checks three ways and then proves by rendering, and a file that keeps
// everything the migration did not touch.
func TestAChartTheRepositoryOutgrewIsMigrated(t *testing.T) {
	h, _ := valuesHarness(t, &llm.Migration{
		Document: "greeting: hello\npodPort: 8080\n",
		Notes:    "port became podPort; legacy is gone from the chart.",
	})

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("want one pushed migration, got %d (comments: %v)", len(h.git.Pushes), h.git.Posted)
	}
	got := h.git.Pushes[0].Tree["addons/values.yaml"]

	// The rename kept the value; the removal took the key.
	if !strings.Contains(got, "    podPort: 8080") {
		t.Errorf("the renamed key must carry the same value:\n%s", got)
	}
	if strings.Contains(got, "legacy:") || strings.Contains(got, "port: 8080\n    legacy") {
		t.Errorf("the dropped key must be gone:\n%s", got)
	}
	// And the file is still the file. This is the whole reason the write is a
	// plan rather than a re-serialised document.
	for _, keep := range []string{
		"# Cluster addons. Order is deliberate.",
		"coredns:",
		"    # We pin the port; the NetworkPolicy names it too.",
		"    greeting: hello",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q must survive:\n%s", keep, got)
		}
	}

	comment := strings.Join(h.git.Posted, "\n")
	for _, want := range []string{
		"rename port to podPort",
		"remove legacy",
		"values migration",
		"rendered with these values before any of it was written",
	} {
		if !strings.Contains(comment, want) {
			t.Errorf("the comment must say %q:\n%s", want, comment)
		}
	}
	// A value that did not come across is named, always. The one that should
	// have been renamed and was not looks exactly like the ones the chart
	// genuinely stopped reading.
	if !strings.Contains(comment, "not carried across") {
		t.Errorf("the comment must name what was dropped:\n%s", comment)
	}
}

// The check that makes the model a translator. A value from nowhere is refused
// even though the document it sits in fits the schema perfectly.
func TestAValueFromNowhereIsRefused(t *testing.T) {
	h, _ := valuesHarness(t, &llm.Migration{
		Document: "greeting: goodbye\npodPort: 8080\n",
		Notes:    "tidied the greeting on the way past",
	})

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatalf("nothing may be written: %v", h.git.Pushes[0].Tree)
	}
	comment := strings.Join(h.git.Posted, "\n")
	if !strings.Contains(comment, "refused") {
		t.Errorf("the comment must say it was refused:\n%s", comment)
	}
	if !hasLabel(h.git.Labelled, labelNeedsHuman) {
		t.Errorf("a refusal is a handoff, got labels %v", h.git.Labelled)
	}
}

// Survival. A setting the new chart still accepts, quietly retuned on the way
// past, is a second change riding inside this one.
func TestASettingTheChartStillAcceptsMayNotMove(t *testing.T) {
	h, _ := valuesHarness(t, &llm.Migration{
		Document: "podPort: 9090\n",
		Notes:    "moved the port",
	})

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatalf("nothing may be written: %v", h.git.Pushes[0].Tree)
	}
	if !strings.Contains(strings.Join(h.git.Posted, "\n"), "greeting") {
		t.Errorf("the refusal must name the setting that vanished:\n%s", strings.Join(h.git.Posted, "\n"))
	}
}

// The guarantee the manifest path cannot have, doing the job the ADR says it
// does.
//
// This proposal passes every static check: `port` and `legacy` are both keys
// the new schema rejects, so dropping them is allowed; nothing the schema
// still accepts moved; no value came from nowhere. It is also the residual
// risk the ADR names -- a key the chart RENAMED, dropped instead of moved,
// which renders green and silently loses the setting. Except that it does not
// render, because the chart insists on the key its schema only types, and the
// harness asks the chart before it writes anything.
func TestAProposalThatStillDoesNotRenderIsRefused(t *testing.T) {
	h, _ := valuesHarness(t, &llm.Migration{
		Document: "greeting: hello\n",
		Notes:    "dropped both keys the new schema rejects",
	})

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatalf("nothing may be written: %v", h.git.Pushes[0].Tree)
	}
	if want := "still does not render"; !strings.Contains(strings.Join(h.git.Posted, "\n"), want) {
		t.Errorf("the refusal must say the chart itself refused it:\n%s", strings.Join(h.git.Posted, "\n"))
	}
}

// The flag. An operator who has not turned document migration on has not
// turned this on either, and the run falls through to the model's own verdict.
func TestTheValuesMigrationIsBehindTheStructuralFlag(t *testing.T) {
	h, _ := valuesHarness(t, &llm.Migration{Document: "greeting: hello\npodPort: 8080\n"})
	h.triage.Structural = false
	h.model.Verdict = escalateVerdict()

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.MigrationCalls != 0 {
		t.Errorf("no migration may be proposed with the flag off, got %d", h.model.MigrationCalls)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("nothing may be written with the flag off")
	}
}

// Where repair ends. The chart requires a key neither the repository nor the
// chart itself can answer for, and no model is asked: there is nothing to
// derive an answer from, so asking for one is asking for an invention.
func TestARequiredKeyNobodyCanDeriveEscalatesBeforeTheModel(t *testing.T) {
	h, _ := valuesHarnessOn(t, charttest.Underivable, &llm.Migration{
		Document: "greeting: hello\nnamespace: monitoring\npodPort: 8080\n",
		Notes:    "picked a namespace",
	})

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if h.model.MigrationCalls != 0 {
		t.Errorf("nothing may be asked of the model here, got %d call(s)", h.model.MigrationCalls)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("nothing may be written")
	}
	comment := strings.Join(h.git.Posted, "\n")
	for _, want := range []string{"namespace", "needs a person"} {
		if !strings.Contains(comment, want) {
			t.Errorf("the escalation must say %q:\n%s", want, comment)
		}
	}
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
