package gateservice

import (
	"context"
	"errors"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

// Status feeds the status page, whose whole subject is the difference between
// "nothing is wrong" and "nobody looked". So these tests are about honesty at
// the edges: a service that has not swept says never, a sweep that could not
// list says why, and a pull request the sweep deliberately skipped still
// reports the verdict standing on it.

func TestStatusBeforeAnySweepClaimsNothing(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig}
	h := newGateHarness(t, files, files)

	st := h.gs.Status()
	if !st.SweptAt.IsZero() {
		t.Fatal("a service that has not swept must not carry a sweep time")
	}
	if len(st.Open) != 0 || st.Err != "" {
		t.Fatalf("the zero status is the honest one; got %+v", st)
	}
}

func TestStatusReportsWhatTheSweepSaw(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig,
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	pr := gatePR("50171ed")
	pr.Title, pr.URL = "chore: bump podinfo to 6.7.1", "https://git.example/pr/7"
	h.git.OpenPRs = []gitprovider.PullRequest{*pr}
	// A verdict already standing on the host, so the sweep skips the commit;
	// the snapshot must still say what that verdict was rather than shrug
	// about the pull request it deliberately did not re-litigate.
	h.git.Check = gitprovider.CheckSuccess

	h.gs.sweep(context.Background())

	st := h.gs.Status()
	if st.SweptAt.IsZero() {
		t.Fatal("a completed sweep must be dated")
	}
	if len(st.Open) != 1 {
		t.Fatalf("the sweep saw one open pull request; status shows %d", len(st.Open))
	}
	got := st.Open[0]
	if got.Number != 7 || got.Title != pr.Title || got.URL != pr.URL {
		t.Fatalf("the page links what the sweep saw; got %+v", got)
	}
	if got.State != "passing" {
		t.Fatalf("a standing success must read as passing, got %q", got.State)
	}
}

func TestStatusRecordsAListingFailure(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig}
	h := newGateHarness(t, files, files)
	h.git.ListErr = errors.New("the host said 401")

	h.gs.sweep(context.Background())

	st := h.gs.Status()
	if st.Err == "" {
		t.Fatal("a gate that cannot list pull requests must say so, or the page reads 'nothing open' forever")
	}
	if st.SweptAt.IsZero() {
		t.Fatal("a failed sweep still happened, and the page dates it")
	}

	// And a sweep that recovers clears the complaint rather than wearing it.
	h.git.ListErr = nil
	h.gs.sweep(context.Background())
	if st := h.gs.Status(); st.Err != "" {
		t.Fatalf("a recovered sweep must not keep reporting the old failure, got %q", st.Err)
	}
}

func TestStatusMarksARefusedRunAsError(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig,
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	pr := *gatePR("f04ked")
	pr.FromFork = true
	h.git.OpenPRs = []gitprovider.PullRequest{pr}

	h.gs.sweep(context.Background())

	st := h.gs.Status()
	if len(st.Open) != 1 || st.Open[0].State != "error" {
		t.Fatalf("a gate that could not run is an error, not a verdict; got %+v", st.Open)
	}
	if st.Held != 1 {
		t.Fatalf("the refusal is held so it is not reposted every poll; held %d", st.Held)
	}
}
