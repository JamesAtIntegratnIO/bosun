package upstream

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveResolve points the real resolver at a real registry. Skipped unless
// UPSTREAM_LIVE_ARTIFACT names one, so `go test ./...` stays hermetic and
// offline.
//
// It exists because the bug this file's fixtures pin was not findable from the
// fixtures: the resolver did exactly what its tests said, against a shape of
// artifact nobody had put in front of it. A one-command way to ask a real
// registry "what do you actually publish, and can we read it" is the cheapest
// guard against the next version of that.
//
//	UPSTREAM_LIVE_ARTIFACT=oci://ghcr.io/org/charts/thing \
//	UPSTREAM_LIVE_FROM=0.12.0 UPSTREAM_LIVE_TO=0.13.0 \
//	go test ./upstream -run LiveResolve -v
func TestLiveResolve(t *testing.T) {
	artifact := os.Getenv("UPSTREAM_LIVE_ARTIFACT")
	if artifact == "" {
		t.Skip("set UPSTREAM_LIVE_ARTIFACT to resolve against a real registry")
	}
	from, to := os.Getenv("UPSTREAM_LIVE_FROM"), os.Getenv("UPSTREAM_LIVE_TO")

	g := &GitHubReleases{Token: os.Getenv("UPSTREAM_LIVE_TOKEN"), MaxReleases: 3, MaxBodyChars: 400}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	repo, err := g.sourceRepo(ctx, artifact, to)
	if err != nil {
		t.Fatalf("sourceRepo(%s): %v", artifact, err)
	}
	t.Logf("source repository: %s", repo)

	n, _ := g.Notes(ctx, artifact, from, to)
	t.Logf("notes: %d release(s) -- %s", len(n.Releases), n.Note)
	for _, r := range n.Releases {
		t.Logf("  %s", r.Tag)
	}

	terms := []string{"Deployment", "env"}
	if raw := os.Getenv("UPSTREAM_LIVE_TERMS"); raw != "" {
		terms = strings.Split(raw, ",")
	}
	c, _ := g.Compare(ctx, artifact, from, to, terms)
	t.Logf("compare: %s -- %d commit(s) total, %d relevant, truncated=%v", c.Range, c.Total, len(c.Relevant), c.Truncated)
	t.Logf("  note: %s", c.Note)
	for _, cm := range c.Relevant {
		t.Logf("  %s %s", cm.SHA, cm.Message)
	}
}
