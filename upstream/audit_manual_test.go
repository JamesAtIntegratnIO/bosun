package upstream

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestAuditArtifacts points the resolver at every artifact in a real promotion
// target list and reports what it can and cannot read.
//
// It exists because every bug found in this package so far has been the same
// bug: reality had a shape the fixtures did not, and the code was only ever
// aimed at one artifact at a time. A list of the artifacts a pipeline ACTUALLY
// promotes is the cheapest way to find that out before somebody deploys.
//
//	UPSTREAM_AUDIT_FILE=/path/to/artifacts.txt go test ./upstream -run Audit -v
func TestAuditArtifacts(t *testing.T) {
	path := os.Getenv("UPSTREAM_AUDIT_FILE")
	if path == "" {
		t.Skip("set UPSTREAM_AUDIT_FILE to a list of artifact references")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	g := &GitHubReleases{Token: os.Getenv("UPSTREAM_LIVE_TOKEN"), MaxReleases: 1, MaxBodyChars: 200}
	ok, bad := 0, 0
	var failures []string

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		ref := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sc.Text()), ","))
		if ref == "" || ref == "it" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		// A real promotion always carries a version; the audit has to find one
		// or it measures the absence of a tag rather than the resolver.
		repo, err := g.sourceRepo(ctx, ref, g.someTag(ctx, ref))
		cancel()
		if err != nil {
			bad++
			failures = append(failures, fmt.Sprintf("%-62s %v", ref, err))
			continue
		}
		ok++
		t.Logf("OK   %-62s -> %s", ref, repo)
	}
	t.Logf("\n==== resolved %d, failed %d of %d ====", ok, bad, ok+bad)
	for _, f := range failures {
		t.Logf("FAIL %s", f)
	}
}

// someTag finds any real tag for an artifact, so the audit exercises the path a
// promotion takes rather than 404ing on `latest`. Empty for a classic Helm
// repository, where the index itself carries every version.
func (g *GitHubReleases) someTag(ctx context.Context, artifact string) string {
	plain, _ := parseArtifact(artifact)
	if isHelmRepoURL(plain) {
		return ""
	}
	host, repo, _ := splitRef(plain)
	if host == "" {
		return ""
	}
	host = registryHost(host)
	tok, _ := g.registryToken(ctx, host, repo)
	var out struct {
		Tags []string `json:"tags"`
	}
	if err := g.getJSON(ctx, fmt.Sprintf("https://%s/v2/%s/tags/list?n=200", host, repo), tok, "", &out); err != nil {
		return ""
	}
	// The last tag a registry lists is usually the newest, and any real tag is
	// enough: this is asking "can the label be read at all".
	for i := len(out.Tags) - 1; i >= 0; i-- {
		// cosign publishes signatures and attestations as tags named after the
		// digest they cover. Picking one measures the signature rather than the
		// image.
		if t := out.Tags[i]; t != "" && !strings.HasSuffix(t, ".sig") &&
			!strings.HasSuffix(t, ".att") && !strings.HasPrefix(t, "sha256-") {
			return t
		}
	}
	return ""
}
