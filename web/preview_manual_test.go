package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// TestWritePreview dumps the page to WEB_PREVIEW for a look in a browser.
// Skipped unless that is set: this is a design tool, not an assertion.
func TestWritePreview(t *testing.T) {
	path := os.Getenv("WEB_PREVIEW")
	if path == "" {
		t.Skip("set WEB_PREVIEW=<file> to write the page")
	}
	rep := &pipeline.Report{
		At: time.Now().Add(-4 * time.Minute),
		Findings: []pipeline.Finding{
			{Kind: pipeline.KindWedged, Severity: pipeline.Blocking, Subject: "argo-cd",
				Summary: "Stage argo-cd has stopped promoting",
				Detail:  "The latest promotion for freight 7f3a ended Errored 3 days ago after a DNS lookup failed mid-step.\nEvery Application is Synced and Healthy on the older version, which is why nothing else has mentioned it.",
				Remedy:  "kubectl -n kargo-pipelines annotate promotion argo-cd-01j 'kargo.akuity.io/abort={\"action\":\"terminate\"}'\nkubectl -n kargo-pipelines create -f - <<'YAML'\napiVersion: kargo.akuity.io/v1alpha1\nkind: Promotion\nmetadata:\n  generateName: argo-cd-retry-\nYAML",
				Since:   72 * time.Hour},
			{Kind: pipeline.KindVerifyStuck, Severity: pipeline.Blocking, Subject: "cert-manager",
				Summary: "cert-manager's verification cannot reach Prometheus, and the Stage is holding",
				Detail:  "AnalysisRun cert-manager-9x2 errored 3 days ago: dial tcp 10.43.2.11:9090: i/o timeout.",
				Remedy:  "kubectl -n kargo-pipelines annotate stage cert-manager 'kargo.akuity.io/reverify={\"id\":\"01j9\"}'",
				Since:   71 * time.Hour},
			{Kind: pipeline.KindDeadPin, Severity: pipeline.Degraded, Subject: "kyverno",
				Summary: "6 of the kubectl target's 7 pins write to keys kyverno 3.9.0 no longer reads",
				Detail:  "addons/kyverno/values.yaml has no admissionController.replicas; the yaml-update step reports success and changes nothing.",
				Remedy:  "# confirm, then remove the dead keys from the target's yaml-update step\nyq '.admissionController' addons/kyverno/values.yaml",
				Since:   19 * time.Hour},
			{Kind: pipeline.KindSupersededPR, Severity: pipeline.Note, Subject: "PR #612",
				Summary: "2 open promotion pull requests for external-secrets; only the newest can merge",
				Detail:  "#612 and #619 both promote external-secrets. #612 is 4 days older.",
				Remedy:  "gh pr close 612"},
		},
		Namespaces: []string{"kargo-pipelines"},
		Checked: pipeline.Checked{Stages: 14, Warehouses: 14, Promotions: 96, PullRequests: 5, PinsScanned: 41,
			Notes: []string{"the repository could not be checked out, so pins were not resolved against a real tree"}},
	}
	rep.Sort()

	s := &Server{
		Brand: "Bosun", Version: "0.29.0",
		Repo: "example/platform", RepoLink: "https://github.com/example/platform",
		CheckName: "addons-gate", Model: "anthropic/claude-sonnet-5",
		GatePoll: 30 * time.Second, SweepEvery: 10 * time.Minute, Clusters: 6,
		Features: []Feature{
			{Name: "Explain green gates", On: true},
			{Name: "Migrate dropped versions", On: true},
			{Name: "Structural migration", On: true},
			{Name: "Upstream release notes", On: true},
			{Name: "Live cluster reads", On: false},
			{Name: "Gate fork pull requests", On: false},
		},
		EgressLine: "Every outbound request is logged; internal networks are refused at the dial; 2 public hosts are denied by name.",
		Report:     func() *pipeline.Report { return rep },
		Gate: func() GateStatus {
			return GateStatus{SweptAt: time.Now().Add(-12 * time.Second), Held: 5, Running: 1,
				Open: []GatePR{
					{Number: 619, Title: "chore(external-secrets): 0.14.2 -> 0.15.0", URL: "https://github.com/example/platform/pull/619", State: "failing"},
					{Number: 618, Title: "chore(podinfo): 6.7.0 -> 6.7.1", URL: "https://github.com/example/platform/pull/618", State: "passing"},
					{Number: 617, Title: "chore(kyverno): 3.8.4 -> 3.9.0 which is a much longer pull request title than any of the others so the column has to wrap", URL: "https://github.com/example/platform/pull/617", State: "running"},
					{Number: 612, Title: "chore(external-secrets): 0.14.2 -> 0.14.9", URL: "https://github.com/example/platform/pull/612", State: "error"},
				}}
		},
		Triage: func() TriageStatus {
			return TriageStatus{InFlight: []int{617}, Queued: []int{619}, Done: 38, Failed: 2}
		},
	}
	rec := httptest.NewRecorder()
	s.Page()(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if err := os.WriteFile(path, rec.Body.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s", path)
}
