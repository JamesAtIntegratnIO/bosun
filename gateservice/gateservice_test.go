package gateservice

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
)

// The gate service is a loop the agent owns, so these tests are about the
// properties a CI job could only document and hope for: every head commit gets
// a verdict, a broken gate is an error rather than a failure, the report is
// posted once, and the verdict the triage consumes is the one this run
// produced, not one scraped off a comment.

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
	gs        *Service
	git       *gitprovider.Fake
	checkouts int
}

// newGateHarness wires the service to two directories on disk standing in for
// the base and head revisions, the same substitution the triage tests make
// for their single checkout.
func newGateHarness(t *testing.T, baseFiles, headFiles map[string]string) *gateHarness {
	t.Helper()
	base, head := t.TempDir(), t.TempDir()
	writeGateRepo(t, base, baseFiles)
	writeGateRepo(t, head, headFiles)

	h := &gateHarness{git: &gitprovider.Fake{}}
	h.gs = &Service{
		Git:       h.git,
		Inventory: func(context.Context) (*gate.Inventory, error) { return testInventory(), nil },
		CheckName: "addons-gate",
		Poll:      time.Millisecond,
		Log:       t.Logf,
		Checkout: func(context.Context, *gitprovider.PullRequest) (*Compared, error) {
			h.checkouts++
			return &Compared{
				Worktrees: gate.Worktrees{Base: base, Head: head},
				BaseRev:   "basecafe", HeadRev: "headcafe",
				Cleanup: func() {},
			}, nil
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

// requireTool skips rather than fails when an external binary is absent, the
// same trade gate's own suite makes and for the same reason: a suite that
// depends on the developer's PATH is a suite nobody runs. REQUIRE_TOOLS=1
// turns the skip into a failure, and CI sets it.
//
// A second copy rather than an export, because the two packages share nothing
// else and a testing helper exported from gate would be part of gate's API.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		if os.Getenv("REQUIRE_TOOLS") != "" {
			t.Fatalf("%s is not on PATH and REQUIRE_TOOLS is set: "+
				"this seam must be exercised here, not skipped", name)
		}
		t.Skipf("%s is not on PATH; skipping", name)
	}
}

func TestAVersionBumpIsGreenAndReported(t *testing.T) {
	// Stated, now that it matters. This test asserts a green verdict on a
	// real chart bump, and without helm both renders fail: it used to pass
	// anyway, because a failed render was a warning that counted towards
	// nothing, which is the defect this gate now blocks on.
	requireTool(t, "helm")
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
		t.Fatal("the first status must be pending -- a verdict-shaped silence while rendering is somebody else's bug, not this one's")
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
	// The fact whose absence cost a git archaeology session. A report that
	// names only the head cannot be told apart from one whose base was the
	// wrong commit, and the wrong commit's symptom -- resources this pull
	// request never touched, listed as removed -- reads as a pull request
	// tearing out infrastructure.
	for _, want := range []string{"basecafe", "headcafe"} {
		if !strings.Contains(out.Report, want) {
			t.Errorf("the report must name both revisions it compared; %q is missing:\n%s", want, out.Report)
		}
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

// A missing config file used to be the error on its own. Since ADR 0012 it is
// an ordinary shape -- most repositories need no file -- and the error is
// reserved for the thing that was always the real problem underneath it:
// nothing to render, from either the file or ArgoCD.
//
// The refusal matters more than it looks. An empty scope renders two empty
// sets, finds no difference between them, and passes every pull request with
// total confidence.
func TestNothingToRenderIsAnError(t *testing.T) {
	files := map[string]string{
		"apps/podinfo.yaml": appManifest("podinfo", "https://kubernetes.default.svc", "6.7.0")}
	h := newGateHarness(t, files, files)

	out := h.gs.Ensure(context.Background(), gatePR("nocfg"))
	if out.Err == nil {
		t.Fatal("an empty scope must be exit 2, not a green on two empty sets")
	}
	s := lastStatus(t, h.git)
	if s.State != gitprovider.StateError || !strings.Contains(s.Description, "nothing to render") {
		t.Fatalf("the error should say the scope is empty: %s %q", s.State, s.Description)
	}
	if !strings.Contains(s.Description, ".bosun.yaml") {
		t.Fatalf("it should name the file that could fix it: %q", s.Description)
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
	// A verdict already on the commit, from a previous life of this pod.
	h.git.Check = gitprovider.CheckSuccess

	h.gs.sweep(context.Background())
	if h.checkouts != 0 {
		t.Fatal("a standing verdict is not re-litigated by a restarted pod")
	}
}

// A verdict answers a commit and the commit does not change, so it is kept.
// A failure to run is not a verdict: its cause is usually cluster-side, and
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

	// Not on the very next poll, though: a broken gate must not
	// re-render every ten seconds for as long as the pull request is open.
	before := len(h.git.Statuses)
	h.gs.sweep(context.Background())
	if len(h.git.Statuses) != before {
		t.Fatal("a broken gate retried immediately is a busy loop")
	}

	// The operator grants the permission. No commit is pushed, the head is the
	// same, so the gate trying again is the only thing that can clear it.
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
	// not a failure, so it carries no retry deadline; otherwise every sweep
	// would repost the same refusal for the life of the pull request.
	before := len(h.git.Statuses)
	h.gs.sweep(context.Background())
	if len(h.git.Statuses) != before {
		t.Fatal("a policy refusal must be stated once, not on every poll")
	}
}

func execGit(args ...string) (string, error) {
	out, err := exec.Command("git", args...).CombinedOutput()
	return string(out), err
}

// A pull request blocked for a reason that is neither targeting nor source
// used to get "0 targeting change(s), 0 other source change(s)" beside a red
// cross, the most-read surface saying nothing changed, exactly when it most
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

// The gate reads.gitops-gate.yaml from the head, which is the right rule,
// but it means a pull request can switch a check off in a file the agent is
// forbidden to edit, and the report used to say nothing about it.
func TestTheReportNamesChecksThisPullRequestTurnedOff(t *testing.T) {
	const src = "sources:\n  - type: manifests\n    paths: [apps]\n"
	cfgOff := src + "validate:\n  enabled: false\n"
	cfgOn := src + "validate:\n  enabled: true\n"

	write := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".gitops-gate.yaml"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	t.Run("validation switched off is named", func(t *testing.T) {
		head := write(t, cfgOff)
		cfg, err := gate.ParseConfig([]byte(cfgOff), ".gitops-gate.yaml")
		if err != nil {
			t.Fatal(err)
		}
		got := suppressedChecks(write(t, cfgOff), head, cfg, ".gitops-gate.yaml")
		if len(got) != 1 || !strings.Contains(string(got[0]), "Schema validation") {
			t.Fatalf("want the disabled check named, got %v", got)
		}
	})

	t.Run("a config change in this pull request is named", func(t *testing.T) {
		cfg, err := gate.ParseConfig([]byte(cfgOn), ".gitops-gate.yaml")
		if err != nil {
			t.Fatal(err)
		}
		got := suppressedChecks(write(t, cfgOff), write(t, cfgOn), cfg, ".gitops-gate.yaml")
		if len(got) != 1 || !strings.Contains(string(got[0]), "changed in this pull request") {
			t.Fatalf("want the config change named, got %v", got)
		}
	})

	t.Run("an unchanged config with everything on says nothing", func(t *testing.T) {
		cfg, err := gate.ParseConfig([]byte(cfgOn), ".gitops-gate.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if got := suppressedChecks(write(t, cfgOn), write(t, cfgOn), cfg, ".gitops-gate.yaml"); len(got) != 0 {
			t.Fatalf("nothing was suppressed, but the report says %v", got)
		}
	})
}

// The sweep is not the only way in. Ensure is called directly by the triage on
// a network-triggered promotion, so a fork pull request the sweep had not
// reached yet used to be rendered, with helm, in the cluster, over content
// somebody outside the repository controls.
func TestAForkPullRequestIsRefusedOnEveryPath(t *testing.T) {
	git := &gitprovider.Fake{}
	g := &Service{Git: git, CheckName: "gate", Log: t.Logf}

	pr := &gitprovider.PullRequest{Number: 7, HeadSHA: "c0ffee", Branch: "b", FromFork: true}
	out := g.Ensure(context.Background(), pr)

	if out.Err == nil || !strings.Contains(out.Err.Error(), "fork") {
		t.Fatalf("a fork pull request must not be gated: %+v", out)
	}
	// And it says so on the pull request, because an unreported required check
	// blocks the merge with no explanation.
	if len(git.Statuses) == 0 {
		t.Fatal("the refusal must be reported")
	}
	last := git.Statuses[len(git.Statuses)-1]
	if last.State != gitprovider.StateError || !strings.Contains(last.Description, "forkPRs") {
		t.Errorf("the status must name the setting that changes it: %s %q", last.State, last.Description)
	}
}

// With forkPRs on, the operator has made the trust decision and the gate runs.
func TestForkPRsLetsItThrough(t *testing.T) {
	git := &gitprovider.Fake{}
	g := &Service{Git: git, CheckName: "gate", Log: t.Logf, ForkPRs: true,
		Inventory: func(context.Context) (*gate.Inventory, error) {
			return nil, fmt.Errorf("inventory unavailable in this test")
		}}

	out := g.Ensure(context.Background(),
		&gitprovider.PullRequest{Number: 7, HeadSHA: "c0ffee", FromFork: true})
	if out.Err == nil || strings.Contains(out.Err.Error(), "fork") {
		t.Fatalf("forkPRs must permit the run: %+v", out)
	}
}

// The suppressed lines carry the same mix the scope lines do: the gate's own
// markdown — `validate.skipKinds` in a code span, the bold lead — around
// values the config file chose. The report renders them as written, so a kind
// with a backtick in it is neutralised here, and the gate's own spans are not.
func TestASuppressedKindCannotWriteReportStructure(t *testing.T) {
	cfgYAML := "sources:\n  - type: manifests\n    paths: [apps]\n" +
		"validate:\n  enabled: true\n  skipKinds: [\"Weird`Kind\"]\n"
	cfg, err := gate.ParseConfig([]byte(cfgYAML), ".gitops-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}

	got := suppressedChecks(t.TempDir(), t.TempDir(), cfg, ".gitops-gate.yaml")
	if len(got) != 1 {
		t.Fatalf("want the one skipKinds line, got %v", got)
	}

	res := &gate.DiffResult{Suppressed: got}
	var b strings.Builder
	res.Report(&b)

	if !strings.Contains(b.String(), "(`validate.skipKinds`)") {
		t.Errorf("the gate's own code span must survive the report:\n%s", b.String())
	}
	if !strings.Contains(b.String(), `Weird\x60Kind`) {
		t.Errorf("the kind's backtick must be spelt out, visibly:\n%s", b.String())
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if n := strings.Count(line, "`"); n%2 != 0 {
			t.Fatalf("a configured kind left an unbalanced code span: %q", line)
		}
	}
}
