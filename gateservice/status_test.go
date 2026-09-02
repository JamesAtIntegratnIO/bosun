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

// The labels standing on a pull request ride along in the snapshot.
//
// The sweep has them in hand -- the pull requests it lists carry them -- and
// the read surfaces that publish them may not reach the git host at all: a
// tool call answers from this snapshot or it answers nothing. Fetching them on
// request would make a read surface's answer depend on a token, a network hop
// and a rate limit, which is the property this whole snapshot exists to avoid.
func TestTheSnapshotCarriesTheLabelsTheSweepSaw(t *testing.T) {
	files := map[string]string{".gitops-gate.yaml": gateConfig}
	h := newGateHarness(t, files, files)
	pr := gatePR("50171ed")
	pr.Labels = []string{"bosun/attempt-1", "bosun/escalated"}
	h.git.OpenPRs = []gitprovider.PullRequest{*pr}

	h.gs.sweep(context.Background())

	st := h.gs.Status()
	if len(st.Open) != 1 {
		t.Fatalf("the sweep saw one open pull request; status shows %d", len(st.Open))
	}
	got := st.Open[0].Labels
	if len(got) != 2 || got[0] != "bosun/attempt-1" || got[1] != "bosun/escalated" {
		t.Fatalf("the snapshot must carry what the sweep saw standing on the pull request, "+
			"got %v", got)
	}
}

// The verdicts a pull request's own comment recorded ride along in the
// snapshot too.
//
// The publish path parses those stamps out of the existing comment on every
// run, because that is how the gate carries its memory forward, and until this
// crossing existed it threw the parse away at the end of the function. A
// reader that wanted "has this gate been flapping?" had to fetch the comment
// and parse an internal format back out of it -- which is a git-host call a
// tool call may not make, and a private format made public by whoever guessed
// at it.
func TestTheSnapshotCarriesTheVerdictsTheCommentRecorded(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()

	// Two publishes, so the second one has an earlier verdict to read: the
	// red pass, which the repair then edited over.
	red := &gitprovider.PullRequest{Number: 7, HeadSHA: "cfb553ee5c23"}
	gs.comment(ctx, red, reportFor(true, "Blocking — 4 manifests still declaring a dropped API version", "red"))
	green := &gitprovider.PullRequest{Number: 7, HeadSHA: "36fd60989cea"}
	gs.comment(ctx, green, reportFor(false, "No blocking findings — 2 versions changed", "green"))

	// A verdict already standing on the host, so the sweep lists and snapshots
	// without running the gate: the history has to survive a sweep that judges
	// nothing, because that is most sweeps.
	f.OpenPRs = []gitprovider.PullRequest{{Number: 7, HeadSHA: "36fd60989cea"}}
	f.Check = gitprovider.CheckSuccess
	gs.sweep(ctx)

	st := gs.Status()
	if st.HistoryCap != MaxHistory {
		t.Errorf("the snapshot must carry the cap it applied, got %d", st.HistoryCap)
	}
	if len(st.Open) != 1 {
		t.Fatalf("the sweep saw one open pull request; status shows %d", len(st.Open))
	}
	got := st.Open[0].History
	if got == nil {
		t.Fatal("the comment was read and its earlier verdict was dropped again, which is " +
			"the whole thing this crossing exists to stop")
	}
	if len(*got) != 1 {
		t.Fatalf("want the one earlier verdict, got %d: %+v", len(*got), *got)
	}
	row := (*got)[0]
	if row.SHA != "cfb553ee5c23" {
		t.Errorf("the earlier verdict must name the commit it judged, got %q", row.SHA)
	}
	if !row.Blocking {
		t.Error("the earlier verdict blocked, and a history that forgets that cannot be " +
			"counted for flips")
	}
	if row.Headline != "Blocking — 4 manifests still declaring a dropped API version" {
		t.Errorf("the history must say WHAT was wrong, got %q", row.Headline)
	}
}

// Oldest first, which is the order the comment records and the order the
// renderer reads.
//
// Stated here because the read surface publishes the reverse and cannot see
// this side: a client is told newest-first, and the reversal is only correct
// while this is the order it reverses.
func TestTheSnapshotsHistoryIsOldestFirst(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()

	for _, sha := range []string{"aaaa1111", "bbbb2222", "cccc3333", "dddd4444"} {
		gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: sha},
			reportFor(true, "Blocking — "+sha, "body"))
	}

	f.OpenPRs = []gitprovider.PullRequest{{Number: 7, HeadSHA: "dddd4444"}}
	f.Check = gitprovider.CheckSuccess
	gs.sweep(ctx)

	got := gs.Status().Open[0].History
	if got == nil {
		t.Fatal("no history was carried, so the ordering below was never read")
	}
	var shas []string
	for _, row := range *got {
		shas = append(shas, row.SHA)
	}
	want := []string{"aaaa1111", "bbbb2222", "cccc3333"}
	if len(shas) != len(want) {
		t.Fatalf("want the three earlier verdicts, got %v", shas)
	}
	for i := range want {
		if shas[i] != want[i] {
			t.Fatalf("the history must be oldest first, got %v", shas)
		}
	}
}

// A pull request whose comment nothing has read carries no history, rather
// than an empty one.
//
// The distinction the read surface publishes as two different answers: "the
// gate has said nothing before now" and "nothing has looked at what it said".
func TestAPullRequestWithNoCommentReadCarriesNoHistory(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()

	f.OpenPRs = []gitprovider.PullRequest{{Number: 7, HeadSHA: "36fd60989cea"}}
	f.Check = gitprovider.CheckSuccess
	gs.sweep(ctx)

	st := gs.Status()
	if len(st.Open) != 1 {
		t.Fatalf("the sweep saw one open pull request; status shows %d", len(st.Open))
	}
	if st.Open[0].History != nil {
		t.Fatalf("a pull request nothing has published onto must carry no history, got %+v",
			*st.Open[0].History)
	}
}

// A listing the host refused records no history, rather than recording that
// there is none.
//
// The publish still happens -- a report nobody can read is worse than a
// duplicate one -- and the empty parse it falls back on is the same empty
// parse a pull request with no gate comment produces. Storing it would publish
// "no earlier verdicts" for a pull request whose comment nothing managed to
// open.
func TestACommentListingThatFailedRecordsNoHistory(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()

	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "cfb553ee5c23"},
		reportFor(true, "Blocking — one", "red"))

	f.ListCommentsErr = errors.New("the host said 403")
	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "36fd60989cea"},
		reportFor(false, "No blocking findings", "green"))
	f.ListCommentsErr = nil

	f.OpenPRs = []gitprovider.PullRequest{{Number: 7, HeadSHA: "36fd60989cea"}}
	f.Check = gitprovider.CheckSuccess
	gs.sweep(ctx)

	if h := gs.Status().Open[0].History; h != nil {
		t.Fatalf("a refused listing must leave no claim about the history, got %+v", *h)
	}
}

// A pull request that stops being open stops being remembered.
//
// The same leak the held verdicts are pruned for, one rung out: the memory a
// merged pull request's history describes lives in a comment on the git host,
// and nothing here will ever be asked about it again.
func TestAClosedPullRequestsHistoryIsDropped(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()

	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "cfb553ee5c23"},
		reportFor(true, "Blocking — one", "red"))
	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "36fd60989cea"},
		reportFor(false, "No blocking findings", "green"))

	f.OpenPRs = []gitprovider.PullRequest{{Number: 7, HeadSHA: "36fd60989cea"}}
	f.Check = gitprovider.CheckSuccess
	gs.sweep(ctx)
	if gs.Status().Open[0].History == nil {
		t.Fatal("the history was never recorded, so the drop below proves nothing")
	}

	f.OpenPRs = nil
	gs.sweep(ctx)

	gs.mu.Lock()
	held := len(gs.history)
	gs.mu.Unlock()
	if held != 0 {
		t.Fatalf("a merged pull request's history is a slow leak; %d still held", held)
	}
}
