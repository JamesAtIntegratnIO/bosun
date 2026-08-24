package upstream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// An artifact reference is not always one string, and for charts it never was.
//
// The promotion pipeline builds a chart's artifact as `repoURL SPACE chartName`
// (kargo-pipelines' stage.yaml). For an OCI chart the name is empty, so the
// value trims down to a bare URL and every OCI path worked. For a CLASSIC Helm
// repository the name is set, so the string is
//
//	https://kyverno.github.io/kyverno kyverno
//
// which this package treated as a single OCI reference and turned into
// `https://https/v2//kyverno.github.io/kyverno/manifests/latest`. Twenty of the
// fifty-three artifacts in the real target list are that shape -- including
// metallb, kyverno, cilium, cert-manager, external-secrets, argo-cd, authentik
// and trivy-operator-explorer, which is to say every chart in the eval suite
// and the one this feature was designed around.

// parseArtifact splits a promotion's artifact into a reference and, for a
// chart, its name.
func parseArtifact(artifact string) (ref, chart string) {
	fields := strings.Fields(strings.TrimSpace(artifact))
	switch len(fields) {
	case 0:
		return "", ""
	case 1:
		return fields[0], ""
	default:
		return fields[0], fields[1]
	}
}

// isHelmRepoURL reports whether a reference is a classic Helm repository rather
// than an OCI one.
func isHelmRepoURL(ref string) bool {
	return strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://")
}

// maxIndexBytes caps an index.yaml read.
//
// A classic Helm repository has no per-chart endpoint: the whole index is the
// only thing on offer, and some are enormous -- prometheus-community's runs to
// tens of megabytes because it lists every version of every chart it has ever
// published. Reading one of those to answer "where does this come from" is not
// a trade worth making, so it is bounded and the failure is a sentence.
const maxIndexBytes = 16 << 20

// helmIndexSource reads a classic Helm repository's index and returns the
// source repository the chart declares.
//
// This is the exact analogue of the OCI label: `helm push` maps Chart.yaml's
// `sources[0]` into an OCI annotation, and `helm repo index` copies the same
// field into index.yaml. Same declaration by the same publisher, in the format
// their distribution channel uses.
func (g *GitHubReleases) helmIndexSource(ctx context.Context, repoURL, chart, version string) (string, error) {
	if chart == "" {
		return "", fmt.Errorf("%s is a Helm repository and the promotion did not name a chart in it", repoURL)
	}
	u := strings.TrimRight(repoURL, "/") + "/index.yaml"

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	cl := g.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 45 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s: %s", u, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes))
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", u, err)
	}
	if len(body) >= maxIndexBytes {
		return "", fmt.Errorf("%s is larger than %dMiB, which is more than this will read to find one label",
			u, maxIndexBytes>>20)
	}

	var index struct {
		Entries map[string][]struct {
			Version string   `json:"version"`
			Home    string   `json:"home"`
			Sources []string `json:"sources"`
		} `json:"entries"`
	}
	if err := yaml.Unmarshal(body, &index); err != nil {
		return "", fmt.Errorf("parsing %s: %w", u, err)
	}
	versions, ok := index.Entries[chart]
	if !ok || len(versions) == 0 {
		return "", fmt.Errorf("%s publishes no chart called %q", u, chart)
	}

	// The exact version when it is there, and the newest entry otherwise. An
	// index is newest-first by convention, and a `sources:` field almost never
	// changes between versions -- so falling back is far better than refusing
	// to answer because a pin has already moved on.
	pick := versions[0]
	for _, v := range versions {
		if version != "" && normalise(v.Version) == normalise(version) {
			pick = v
			break
		}
	}
	for _, s := range pick.Sources {
		if repo, err := githubPath(s); err == nil {
			return repo, nil
		}
	}
	if pick.Home != "" {
		// `home` is a documentation link as often as a repository, so it is a
		// fallback rather than a first choice -- but for a great many charts it
		// is the only thing set, and it is usually the GitHub project.
		if repo, err := githubPath(pick.Home); err == nil {
			return repo, nil
		}
	}
	return "", fmt.Errorf("chart %s %s in %s declares no GitHub `sources` or `home`", chart, pick.Version, u)
}

// dockerHubRef normalises a Docker Hub short reference.
//
// `redis`, `linuxserver/sonarr` and `redimp/otterwiki` are all in the real
// target list, and this package refused them as "not an OCI reference" on the
// principle that guessing a registry is the same class of mistake as guessing a
// repository.
//
// The principle is right and it does not apply here. A short reference is not
// ambiguous -- Docker's own convention gives it exactly one meaning, and the
// promotion pipeline is HANDING us the reference rather than us inferring one
// from a name. What stays refused is a string that is not a reference at all.
func dockerHubRef(ref string) (string, bool) {
	if ref == "" || strings.Contains(ref, "://") {
		return "", false
	}
	first := strings.SplitN(ref, "/", 2)[0]
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return "", false // already carries a registry host
	}
	if strings.Count(ref, "/") > 1 {
		return "", false // too deep to be a Docker Hub short form
	}
	if !strings.Contains(ref, "/") {
		// A bare name is an official image, which lives under `library`.
		return "docker.io/library/" + ref, true
	}
	return "docker.io/" + ref, true
}

// registryHost maps a reference's host to the one that serves the v2 API.
//
// `docker.io` is a website. The registry API lives at `registry-1.docker.io`,
// and asking docker.io for a manifest returns HTML -- which surfaced as
// `invalid character '<' looking for beginning of value`, an error that names
// neither the host nor the problem. The auth host was already mapped here; the
// registry host was not.
func registryHost(host string) string {
	if host == "docker.io" {
		return "registry-1.docker.io"
	}
	return host
}
