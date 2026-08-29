package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// The promotion body arrives over HTTP and everything in it is a claim. These
// are about the two places a claim used to be treated as a fact: which files a
// fix may write, and where an upstream lookup is allowed to connect.

// editOutsideTheChange is permitted by the standing allowlist and lands on a
// file this pull request never touched, which is exactly the difference
// between Allow and Scope. Nothing about it is version-shaped, so a refusal
// here is the scope refusing it and not the corroboration rule.
func editOutsideTheChange() *llm.Verdict {
	return &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Turn CoreDNS off.",
		Reasoning:      "Unrelated to the promotion.",
		Edits: []llm.Edit{{
			Path: otherPath, Key: "coredns.enabled",
			From: "true", To: "false", Rationale: "Nothing asked for this.",
		}},
	}
}

// An empty files array used to switch scope narrowing off altogether, because
// edits.Policy only enforces Scope when it is non-empty. A caller who sent
// none got the standing allowlist and nothing else, which is the widest the
// applier can be.
func TestAnEmptyFileListInThePromotionDoesNotWidenWhatAFixMayWrite(t *testing.T) {
	h := newHarness(t)
	h.model.Verdict = editOutsideTheChange()

	p := promotion()
	p.Files = nil
	if err := h.triage.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	if len(h.git.Pushes) != 0 {
		t.Fatalf("an edit outside the pull request's own diff was pushed")
	}
	if got := h.other(t); !strings.Contains(got, "enabled: true") {
		t.Errorf("the untouched file was rewritten:\n%s", got)
	}
	if len(h.git.Posted) != 1 {
		t.Fatalf("posted %d comments, want the escalation", len(h.git.Posted))
	}
	if !strings.Contains(h.git.Posted[0], "outside this change") {
		t.Errorf("the refusal should name the scope:\n%s", h.git.Posted[0])
	}
}

// The other half of the same claim: a body that names more files than the
// branch holds buys nothing, because the list is not read.
func TestAFileListThePromotionInventedDoesNotWidenWhatAFixMayWrite(t *testing.T) {
	h := newHarness(t)
	h.model.Verdict = editOutsideTheChange()

	p := promotion()
	p.Files = []string{valuesPath, otherPath}
	if err := h.triage.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}

	if len(h.git.Pushes) != 0 {
		t.Fatalf("an edit the promotion body asked for, on a file the branch did not change, was pushed")
	}
	if got := h.other(t); !strings.Contains(got, "enabled: true") {
		t.Errorf("the untouched file was rewritten:\n%s", got)
	}
}

// The file the pull request did change is still writable, or the scope would
// be a way of doing nothing.
func TestAFixStillLandsOnTheFileThePullRequestChanged(t *testing.T) {
	h := newHarness(t)
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Move the metallb pin with the chart.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
		}},
	}

	p := promotion()
	p.Files = nil
	if err := h.triage.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 1 {
		t.Fatalf("pushed %d times, want one", len(h.git.Pushes))
	}
	if !strings.Contains(h.values(t), "0.16.1") {
		t.Errorf("the permitted edit did not land:\n%s", h.values(t))
	}
}

// Fail closed. A run that cannot establish what the pull request changed has
// no scope to apply, and the thing it must not do is fall back to the standing
// allowlist and write anyway.
func TestAPullRequestWhoseDiffCannotBeReadWritesNothing(t *testing.T) {
	h := newHarness(t)
	h.git.PR.BaseBranch = ""
	h.git.PR.BaseSHA = ""
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassMechanical,
		Summary:        "Move the metallb pin with the chart.",
		Edits: []llm.Edit{{
			Path: valuesPath, Key: "metallb.defaultVersion",
			From: "0.16.0", To: "0.16.1", Rationale: "The gate names this version.",
		}},
	}

	if err := h.triage.Run(context.Background(), promotion()); err != nil {
		t.Fatal(err)
	}
	if len(h.git.Pushes) != 0 {
		t.Fatal("a fix was pushed without a scope")
	}
	if !strings.Contains(h.values(t), "0.16.0") {
		t.Errorf("the file was rewritten without a scope:\n%s", h.values(t))
	}
	if !has(h.git.Labelled, labelNeedsHuman) {
		t.Errorf("a run that cannot scope itself must reach a human, labels %v", h.git.Labelled)
	}
}

// The artifact in a promotion body is a destination. Before this, the endpoint
// fetched whatever it was handed and reported the result on the pull request,
// which is a port scanner with a published answer.
func TestUpstreamIsNotFetchedForAHostTheRepositoryDoesNotName(t *testing.T) {
	for _, artifact := range []string{
		"https://169.254.169.254/charts kyverno",
		"http://argocd-server.argocd.svc/charts kyverno",
		"registry.internal.example/team/chart",
	} {
		t.Run(artifact, func(t *testing.T) {
			h := newHarness(t)
			up := &fakeUpstream{notes: &upstream.Notes{SourceRepo: "someone/else"}}
			h.triage.Upstream = up
			h.model.Verdict = &llm.Verdict{
				Classification: llm.ClassEscalate,
				Summary:        "This needs a human.",
			}

			p := promotion()
			p.Artifact = artifact
			if err := h.triage.Run(context.Background(), p); err != nil {
				t.Fatal(err)
			}
			if up.calls != 0 {
				t.Fatalf("the resolver was sent to %s, which nothing in this repository names", artifact)
			}
		})
	}
}

// And the same check has to let a real one through, or it is a way of never
// reading upstream. quay.io is in the values file this pull request changes.
func TestUpstreamIsFetchedForAHostTheRepositoryNames(t *testing.T) {
	h := newHarness(t)
	up := &fakeUpstream{notes: &upstream.Notes{SourceRepo: "metallb/metallb"}}
	h.triage.Upstream = up
	h.model.Verdict = &llm.Verdict{
		Classification: llm.ClassEscalate,
		Summary:        "This needs a human.",
	}

	p := promotion()
	p.Artifact = "quay.io/metallb/controller"
	if err := h.triage.Run(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if up.calls != 1 {
		t.Fatalf("the resolver was consulted %d times for a host the repository names", up.calls)
	}
}

// A bare reference has no host to attack: Docker Hub is where the convention
// sends it, not where the caller does.
func TestRepoReachesLetsAHostlessReferenceThrough(t *testing.T) {
	root := t.TempDir()
	for _, artifact := range []string{"metallb", "redis", "linuxserver/sonarr"} {
		if !repoReaches(root, artifact) {
			t.Errorf("%q names no host and was refused anyway", artifact)
		}
	}
	if repoReaches(root, "169.254.169.254/x/y") {
		t.Error("a host nothing in the checkout names was allowed")
	}
}

// changedFiles is what makes the scope a fact. Read straight, so a failure
// says which of the two commands could not answer.
func TestChangedFilesReadsTheBranchAgainstItsBase(t *testing.T) {
	root := checkoutOfAPullRequest(t)
	pr := &gitprovider.PullRequest{Branch: "kargo/metallb", BaseBranch: "main"}

	got, err := changedFiles(context.Background(), root, pr)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != valuesPath {
		t.Fatalf("the diff is %v, want just %s", got, valuesPath)
	}

	// A ref name is checked before it reaches git's argv, for the reason
	// EnsureHead checks a SHA: a value beginning with "-" is read as an
	// option, and this one arrives in a git host's JSON.
	pr.BaseBranch = "--upload-pack=touch /tmp/pwned"
	if _, err := changedFiles(context.Background(), root, pr); err == nil {
		t.Error("a base that is not a ref name must be refused before git sees it")
	}
}
