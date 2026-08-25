package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
)

// The gate service is the CI adapter rebuilt as a loop the agent owns, so
// these tests are about the properties the adapter had to document and hope
// for: every head commit gets a verdict, a broken gate is an error rather
// than a failure, the report is posted once, and the verdict the triage
// consumes is the one this run produced -- not one scraped off a comment.

const gateConfig = `sources:
  - name: apps
    type: manifests
    paths: ["apps/*.yaml"]
`

func appManifest(name, server, version string) string {
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: %s
  namespace: argocd
spec:
  project: default
  destination:
    server: %s
    namespace: %s
  source:
    repoURL: https://stefanprodan.github.io/podinfo
    chart: podinfo
    targetRevision: %s
`, name, server, name, version)
}

func writeGateRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func testInventory() *gate.Inventory {
	return &gate.Inventory{Clusters: []gate.Cluster{
		{Name: "local", Server: "https://kubernetes.default.svc",
			Labels:      map[string]string{"argocd.argoproj.io/secret-type": "cluster"},
			Annotations: map[string]string{}},
		{Name: "edge", Server: "https://edge.example:6443",
			Labels:      map[string]string{"argocd.argoproj.io/secret-type": "cluster"},
			Annotations: map[string]string{}},
	}}
}

type gateHarness struct {
	gs        *GateService
	git       *gitprovider.Fake
	checkouts int
}

// newGateHarness wires the service to two directories on disk standing in for
// the base and head revisions -- the same substitution the triage tests make
// for their single checkout.
func newGateHarness(t *testing.T, baseFiles, headFiles map[string]string) *gateHarness {
	t.Helper()
	base, head := t.TempDir(), t.TempDir()
	writeGateRepo(t, base, baseFiles)
	writeGateRepo(t, head, headFiles)

	h := &gateHarness{git: &gitprovider.Fake{}}
	h.gs = &GateService{
		Git:       h.git,
		Inventory: func(context.Context) (*gate.Inventory, error) { return testInventory(), nil },
		CheckName: "addons-gate",
		Poll:      time.Millisecond,
		Log:       t.Logf,
		Checkout: func(context.Context, *gitprovider.PullRequest) (string, string, func(), error) {
			h.checkouts++
			return base, head, func() {}, nil
		},
	}
	return h
}

func gatePR(sha string) *gitprovider.PullRequest {
	return &gitprovider.PullRequest{
		Number: 7, Branch: "kargo/podinfo", BaseBranch: "main", HeadSHA: sha,
	}
}

func lastStatus(t *testing.T, git *gitprovider.Fake) gitprovider.Status {
	t.Helper()
	if len(git.Statuses) == 0 {
		t.Fatal("no commit status was set; a gate that says nothing is the failure this service replaces")
	}
	return git.Statuses[len(git.Statuses)-1]
}

func TestAVersionBumpIsGreenAndReported(t *testing.T) {
	h := newGateHarness(t,
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")},
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.1")},
	)

	out := h.gs.Ensure(context.Background(), gatePR("c0ffee"))
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.State != gitprovider.CheckSuccess {
		t.Fatalf("a version bump must not block; got %s", out.State)
	}
	if h.git.Statuses[0].State != gitprovider.StatePending {
		t.Fatal("the first status must be pending -- a verdict-shaped silence while rendering is the CI adapter's bug, not this one's")
	}
	if s := lastStatus(t, h.git); s.State != gitprovider.StateSuccess || !strings.Contains(s.Description, "1 version change") {
		t.Fatalf("status %s %q", s.State, s.Description)
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("the report is for humans and must be posted; got %d comments", len(h.git.Posted))
	}
	if !strings.HasPrefix(h.git.Posted[0], gate.ReportMarker) {
		t.Fatal("the comment must lead with the marker, or nothing downstream can find it")
	}
	if !strings.Contains(h.git.Posted[0], "<!-- gitops-gate:head c0ffee -->") {
		t.Fatal("the comment must be stamped with the head commit, or a restarted pod reposts it")
	}
	if !strings.Contains(out.Report, "6.7.1") {
		t.Fatal("the in-process report must carry the same evidence the comment does")
	}
}

func TestATargetingChangeBlocks(t *testing.T) {
	h := newGateHarness(t,
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")},
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://edge.example:6443", "6.7.0")},
	)

	out := h.gs.Ensure(context.Background(), gatePR("beef01"))
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if out.State != gitprovider.CheckFailure {
		t.Fatalf("an Application moving clusters is the change this gate exists to block; got %s", out.State)
	}
	if s := lastStatus(t, h.git); s.State != gitprovider.StateFailure {
		t.Fatalf("status %s %q", s.State, s.Description)
	}
	if len(h.git.Posted) != 1 {
		t.Fatal("a blocking verdict with no report hands a human a red X and nothing else")
	}
}

func TestNoChangeIsGreenAndSilent(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig,
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)

	out := h.gs.Ensure(context.Background(), gatePR("d0d0"))
	if out.State != gitprovider.CheckSuccess {
		t.Fatalf("got %s: %v", out.State, out.Err)
	}
	if s := lastStatus(t, h.git); !strings.Contains(s.Description, "no change to what gets deployed") {
		t.Fatalf("status %q", s.Description)
	}
	if len(h.git.Posted) != 0 {
		t.Fatal("a no-change render must not comment -- that is how a useful report becomes noise people collapse")
	}
}

func TestABrokenInventoryIsAnErrorNotAVerdict(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig,
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	h.gs.Inventory = func(context.Context) (*gate.Inventory, error) {
		return nil, fmt.Errorf("secrets is forbidden")
	}

	out := h.gs.Ensure(context.Background(), gatePR("bad"))
	if out.Err == nil {
		t.Fatal("a gate that could not look must not produce a verdict")
	}
	if s := lastStatus(t, h.git); s.State != gitprovider.StateError {
		t.Fatalf("'the gate is broken' and 'this change is bad' want opposite reactions; got %s %q", s.State, s.Description)
	}
}

func TestAMissingConfigIsAnError(t *testing.T) {
	files := map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)

	out := h.gs.Ensure(context.Background(), gatePR("nocfg"))
	if out.Err == nil {
		t.Fatal("no config means the gate cannot know what to render, which is exit 2, not a green")
	}
	if s := lastStatus(t, h.git); s.State != gitprovider.StateError || !strings.Contains(s.Description, ".gitops-gate.yaml") {
		t.Fatalf("the error should name the missing file: %s %q", s.State, s.Description)
	}
}

func TestOneRunServesEveryCaller(t *testing.T) {
	h := newGateHarness(t,
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")},
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.1")},
	)

	pr := gatePR("c0ffee")
	first := h.gs.Ensure(context.Background(), pr)
	second := h.gs.Ensure(context.Background(), pr)
	if h.checkouts != 1 {
		t.Fatalf("the sweep and a Kargo-triggered triage arriving together must share one run; rendered %d times", h.checkouts)
	}
	if first != second {
		t.Fatal("both callers must read the same verdict")
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("one run, one report; got %d", len(h.git.Posted))
	}
}

func TestARestartedPodDoesNotRepostTheReport(t *testing.T) {
	h := newGateHarness(t,
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")},
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.1")},
	)
	// What a previous life of this pod already said, found the same way a
	// reader would: by the head stamp.
	h.git.Comments = []gitprovider.Comment{{Author: "bosun",
		Body: gate.ReportMarker + "\n<!-- gitops-gate:head c0ffee -->\nrendered before the restart"}}

	h.gs.Ensure(context.Background(), gatePR("c0ffee"))
	if len(h.git.Posted) != 0 {
		t.Fatal("the report for this commit already stands; a restart must not say it twice")
	}
}

func TestTheSweepGatesEveryOpenPullRequest(t *testing.T) {
	h := newGateHarness(t,
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")},
		map[string]string{".gitops-gate.yaml": gateConfig,
			"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.1")},
	)
	h.git.OpenPRs = []gitprovider.PullRequest{*gatePR("c0ffee")}

	h.gs.sweep(context.Background())
	if h.checkouts != 1 {
		t.Fatalf("an open pull request with no verdict must be rendered; rendered %d times", h.checkouts)
	}

	// The same sweep again: the verdict stands, nothing re-runs.
	h.gs.sweep(context.Background())
	if h.checkouts != 1 {
		t.Fatalf("a commit with a verdict must not be re-rendered every poll; rendered %d times", h.checkouts)
	}

	// A new head commit is new work.
	h.git.OpenPRs = []gitprovider.PullRequest{*gatePR("facade")}
	h.gs.sweep(context.Background())
	if h.checkouts != 2 {
		t.Fatalf("a pushed commit must be re-gated -- this is what replaces 'the bot token must re-trigger CI'; rendered %d times", h.checkouts)
	}
}

func TestASettledStatusIsNotRelitigated(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig,
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	h.git.OpenPRs = []gitprovider.PullRequest{*gatePR("50171ed")}
	// A verdict already on the commit -- a previous pod, or a CI adapter
	// still running during a migration.
	h.git.Check = gitprovider.CheckSuccess

	h.gs.sweep(context.Background())
	if h.checkouts != 0 {
		t.Fatal("a standing verdict is not re-litigated by a restarted pod")
	}
}

// A verdict answers a commit and the commit does not change, so it is kept.
// A FAILURE TO RUN is not a verdict: its cause is usually cluster-side, and
// the fix for those is not a commit. Cached forever, the error status would
// outlive its own cause and clear only when somebody pushed.
func TestABrokenGateTriesAgainWhenItsCauseMayHaveBeenFixed(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig,
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	h.git.OpenPRs = []gitprovider.PullRequest{*gatePR("re7r1ed")}

	// The RBAC for the inventory has not been granted yet.
	denied := true
	h.gs.Inventory = func(context.Context) (*gate.Inventory, error) {
		if denied {
			return nil, fmt.Errorf("secrets is forbidden")
		}
		return testInventory(), nil
	}

	h.gs.sweep(context.Background())
	if s := lastStatus(t, h.git); s.State != gitprovider.StateError {
		t.Fatalf("a gate that could not look must say so; got %s %q", s.State, s.Description)
	}

	// Not on the very next poll, though: a genuinely broken gate must not
	// re-render every ten seconds for as long as the pull request is open.
	before := len(h.git.Statuses)
	h.gs.sweep(context.Background())
	if len(h.git.Statuses) != before {
		t.Fatal("a broken gate retried immediately is a busy loop")
	}

	// The operator grants the permission. No commit is pushed -- the head is
	// the same -- so the gate trying again is the only thing that can clear it.
	denied = false
	h.gs.mu.Lock()
	h.gs.results["re7r1ed"].retryAfter = time.Now().Add(-time.Second)
	h.gs.mu.Unlock()

	h.gs.sweep(context.Background())
	if s := lastStatus(t, h.git); s.State != gitprovider.StateSuccess {
		t.Fatalf("fixing the cause should not need a push; got %s %q", s.State, s.Description)
	}
}

func TestAForkPullRequestIsRefusedNotIgnored(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig,
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)
	pr := *gatePR("f04ked")
	pr.FromFork = true
	h.git.OpenPRs = []gitprovider.PullRequest{pr}

	h.gs.sweep(context.Background())
	if h.checkouts != 0 {
		t.Fatal("fork content must not be rendered in-cluster unless the operator said so")
	}
	s := lastStatus(t, h.git)
	if s.State != gitprovider.StateError || !strings.Contains(s.Description, "fork") {
		t.Fatalf("an unreported required check blocks the merge with no explanation -- the paths-filter trap in a new hat; got %s %q", s.State, s.Description)
	}

	// And it is not retried. Refusing to render fork content is a decision,
	// not a failure, so it carries no retry deadline -- otherwise every sweep
	// would repost the same refusal for the life of the pull request.
	before := len(h.git.Statuses)
	h.gs.sweep(context.Background())
	if len(h.git.Statuses) != before {
		t.Fatal("a policy refusal must be stated once, not on every poll")
	}
}

// The default checkout, against a real repository on disk: the head arrives
// as a clone of the pull request's branch, the base as a worktree at the base
// branch's CURRENT tip -- fetched by name, which any host serves, rather than
// by SHA, which some refuse.
func TestTwoRevisionCheckout(t *testing.T) {
	origin := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := append([]string{"-C", origin, "-c", "user.name=test", "-c", "user.email=test@test"}, args...)
		if out, err := execGit(cmd...); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(origin, "pin.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "--quiet", "-m", "base")
	run("checkout", "--quiet", "-b", "kargo/bump")
	if err := os.WriteFile(filepath.Join(origin, "pin.yaml"), []byte("version: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("commit", "--quiet", "-am", "bump")

	gs := &GateService{RepoURL: origin, Log: t.Logf}
	base, head, cleanup, err := gs.checkout(context.Background(),
		&gitprovider.PullRequest{Branch: "kargo/bump", BaseBranch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	for path, want := range map[string]string{
		filepath.Join(head, "pin.yaml"): "version: 2\n",
		filepath.Join(base, "pin.yaml"): "version: 1\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s holds %q, want %q", path, got, want)
		}
	}
}

func execGit(args ...string) (string, error) {
	out, err := exec.Command("git", args...).CombinedOutput()
	return string(out), err
}

// The triage reading the verdict in-process: no gate comment on the pull
// request, no check to poll -- the evidence is handed over, not scraped back.
func TestTriageReadsTheVerdictInProcess(t *testing.T) {
	h := newHarness(t)
	// Strip away everything the CI path needed: no report comment, no check.
	h.git.Comments = nil
	h.git.Check = gitprovider.CheckMissing

	h.triage.Gate = &GateService{Git: h.git, CheckName: "addons-gate", Log: t.Logf}
	h.triage.Gate.store(h.git.PR.HeadSHA, &gateOutcome{
		State:  gitprovider.CheckFailure,
		Report: gateReport,
	})
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassEscalate,
		Summary:        "This is a migration.", Reasoning: "The CRD schema changed.",
		EscalationReason: "The upgrade needs a CRD migration.",
	}

	if err := h.triage.Run(context.Background(), Promotion{PRNumber: 42, Files: []string{valuesPath}}); err != nil {
		t.Fatal(err)
	}
	if h.git.CheckCalls != 0 {
		t.Fatal("an in-process verdict must not be waited for through the check API")
	}
	if len(h.git.Posted) != 1 || !strings.Contains(h.git.Posted[0], "Needs a human.") {
		t.Fatalf("the red verdict must reach the model and the human: %v", h.git.Posted)
	}
	if !strings.Contains(h.model.User, "0.16.1") {
		t.Fatal("the model must be shown the same report the gate produced in-process")
	}
}

// A broken in-process gate resolves the triage status rather than leaving it
// pending forever -- the same rule every other error path answers to.
func TestTriageSurfacesABrokenInProcessGate(t *testing.T) {
	h := newHarness(t)
	h.git.Comments = nil
	h.triage.Gate = &GateService{Git: h.git, CheckName: "addons-gate", Log: t.Logf}
	h.triage.Gate.store(h.git.PR.HeadSHA, &gateOutcome{Err: fmt.Errorf("secrets is forbidden")})

	_ = h.triage.Run(context.Background(), Promotion{PRNumber: 42})
	s := h.git.Statuses[len(h.git.Statuses)-1]
	if s.State != gitprovider.StateSuccess || !strings.Contains(s.Description, "did not finish") {
		t.Fatalf("a broken gate must resolve the advisory status with the reason: %s %q", s.State, s.Description)
	}
}

// A pull request blocked for a reason that is neither targeting nor source
// used to get "0 targeting change(s), 0 other source change(s)" beside a red
// cross -- the most-read surface saying nothing changed, exactly when it most
// needed to say what did.
func TestAFailingStatusSaysWhyItFailed(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  *gate.DiffResult
		want string
	}{
		{
			name: "an apiVersion that moved",
			res:  &gate.DiffResult{Objects: []gate.ObjectChange{{Kind: "apiVersion", Object: "PodDisruptionBudget/x"}}},
			want: "1 object whose own apiVersion moved",
		},
		{
			name: "settings the bump stops reading",
			res: &gate.DiffResult{Objects: []gate.ObjectChange{
				{Kind: "valuesKeyDropped", Object: "kyverno", Keys: []string{"a", "b", "c"}},
			}},
			want: "3 settings this bump stops reading",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, headline := tc.res.Verdict()
			got := strings.TrimPrefix(headline, "Blocking — ")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("status reason %q does not contain %q", got, tc.want)
			}
			if strings.Contains(got, "0 targeting change(s)") {
				t.Fatalf("the status must not report a count of zero as its reason: %q", got)
			}
			// GitHub rejects descriptions past 140 characters.
			if len(got) > 140 {
				t.Fatalf("status reason is %d chars, over the host limit: %q", len(got), got)
			}
		})
	}
}
