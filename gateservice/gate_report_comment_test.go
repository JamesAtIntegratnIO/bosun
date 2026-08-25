package gateservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
)

func reportFor(blocking bool, headline, body string) string {
	mark := "✅"
	if blocking {
		mark = "🔴"
	}
	return gate.ReportMarker + "\n## " + mark + " " + headline + "\n\n" + body + "\n"
}

func commentHarness(t *testing.T) (*Service, *gitprovider.Fake) {
	t.Helper()
	f := &gitprovider.Fake{PR: &gitprovider.PullRequest{Number: 7}}
	return &Service{Git: f, Log: t.Logf}, f
}

// The shape the whole change exists for: a pull request that was red, got
// repaired, and is now green must end up with ONE report saying it is green
// and still recording that it was not.
func TestARepairedPullRequestKeepsOneReportAndRemembersTheRed(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()

	red := &gitprovider.PullRequest{Number: 7, HeadSHA: "cfb553ee5c23"}
	gs.comment(ctx, red, reportFor(true, "Blocking — 27 manifests still declaring a dropped API version", "the long red report"))

	if len(f.Posted) != 1 {
		t.Fatalf("first run should post one report; got %d", len(f.Posted))
	}

	green := &gitprovider.PullRequest{Number: 7, HeadSHA: "36fd60989cea"}
	gs.comment(ctx, green, reportFor(false, "No blocking findings — 2 versions changed", "the long green report"))

	if len(f.Posted) != 1 {
		t.Fatalf("the repaired run must rewrite the report, not post a second: %d posted", len(f.Posted))
	}
	if len(f.Updated) != 1 {
		t.Fatalf("expected one in-place rewrite; got %d", len(f.Updated))
	}
	if len(f.Comments) != 1 {
		t.Fatalf("a pull request should carry ONE gate report; got %d", len(f.Comments))
	}

	body := f.Comments[0].Body
	if !strings.Contains(body, "✅ No blocking findings") {
		t.Fatalf("the visible verdict must be the current one:\n%s", body)
	}
	if strings.Contains(body, "the long red report") {
		t.Fatalf("the superseded report body should be gone, not accumulated:\n%s", body)
	}
	// The complaint this fixes: the failed pass left no trace that it failed.
	if !strings.Contains(body, "Earlier verdicts on this pull request (1)") {
		t.Fatalf("the failed pass must survive being edited over:\n%s", body)
	}
	if !strings.Contains(body, "🔴 Blocking — 27 manifests still declaring a dropped API version") {
		t.Fatalf("the history must say WHAT was wrong, not merely that something was:\n%s", body)
	}
	if !strings.Contains(body, "`cfb553ee`") {
		t.Fatalf("the history must name the commit it judged:\n%s", body)
	}
}

func TestTheSameCommitIsNeverWrittenTwice(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()
	pr := &gitprovider.PullRequest{Number: 7, HeadSHA: "c0ffee11"}

	gs.comment(ctx, pr, reportFor(true, "Blocking — 1 Application now generated for a different set of clusters", "x"))
	gs.comment(ctx, pr, reportFor(true, "Blocking — 1 Application now generated for a different set of clusters", "x"))

	if len(f.Posted) != 1 || len(f.Updated) != 0 {
		t.Fatalf("a restart must not rewrite an unchanged verdict: posted=%d updated=%d", len(f.Posted), len(f.Updated))
	}
}

// Ten is the cap; an eleventh verdict must drop the oldest rather than grow
// the comment without bound.
func TestHistoryIsBounded(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()
	for i := range 14 {
		sha := string(rune('a'+i)) + "0000000"
		gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: sha},
			reportFor(i%2 == 0, "Blocking — "+sha, "body"))
	}
	body := f.Comments[0].Body
	if strings.Count(body, stampWas) > maxHistory {
		t.Fatalf("history is unbounded: %d rows", strings.Count(body, stampWas))
	}
	if strings.Contains(body, "a0000000") {
		t.Fatalf("the oldest verdict should have aged out:\n%s", body)
	}
}

// If the host refuses the edit, a report nobody can read is worse than a
// duplicate one.
func TestAFailedRewriteStillPublishes(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()
	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "aaaa1111"}, reportFor(true, "Blocking — one", "x"))
	f.UpdateErr = errors.New("403 from the host")
	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "bbbb2222"}, reportFor(false, "No blocking findings", "y"))
	if len(f.Posted) != 2 {
		t.Fatalf("a refused edit must fall back to posting; posted=%d", len(f.Posted))
	}
}

// The bug this guards shipped because the fake agreed with the mistake: the
// agent looked for its own report by comparing the comment's author to
// Name(), which is the PROVIDER's name ("github"), never the account. The
// fake wrote comments authored by its own Name(), so the match succeeded in
// every test and failed on every real pull request -- which then carried two
// twenty-thousand-character reports, the exact thing the change was for.
func TestTheReportIsFoundByItsStampNotItsAuthor(t *testing.T) {
	gs, f := commentHarness(t)
	f.CommentAuthor = "bosun-mate[bot]" // an account, and not "fake"
	ctx := context.Background()

	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "3826221cb213"},
		reportFor(true, "Blocking — 27 manifests still declaring a dropped API version", "red"))
	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "20908b7f0000"},
		reportFor(false, "No blocking findings — 2 versions changed", "green"))

	if len(f.Comments) != 1 {
		t.Fatalf("a repaired pull request must carry ONE report; got %d", len(f.Comments))
	}
	if len(f.Updated) != 1 {
		t.Fatalf("the second run must rewrite the first; updated=%d posted=%d", len(f.Updated), len(f.Posted))
	}
	if !strings.Contains(f.Comments[0].Body, "🔴 Blocking — 27 manifests") {
		t.Fatalf("the red it used to be must survive:\n%s", f.Comments[0].Body)
	}
}

// A report this agent did not write carries no verdict stamp, so it is not
// ours to rewrite -- we post our own beside it rather than failing.
func TestAForeignReportIsNotRewritten(t *testing.T) {
	gs, f := commentHarness(t)
	ctx := context.Background()
	f.Comments = []gitprovider.Comment{{
		ID: 99, Author: "github-actions[bot]",
		Body: gate.ReportMarker + "\nposted by a CI adapter, with no verdict stamp",
	}}
	gs.comment(ctx, &gitprovider.PullRequest{Number: 7, HeadSHA: "abcdef12"},
		reportFor(true, "Blocking — 1 object whose own apiVersion moved", "x"))

	if len(f.Updated) != 0 {
		t.Fatal("a comment we did not write must not be rewritten")
	}
	if len(f.Posted) != 1 {
		t.Fatalf("we must still publish our own; posted=%d", len(f.Posted))
	}
}
