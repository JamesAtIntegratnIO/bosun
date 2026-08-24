package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// sourceRepo reads org.opencontainers.image.source off an artifact and returns
// it as "owner/repo".
//
// Anonymous throughout. These are public artifacts, and a resolver that needed
// credentials for every upstream registry would be a credential-management
// problem wearing an explanation feature's clothes.
func (g *GitHubReleases) sourceRepo(ctx context.Context, artifact, version string) (string, error) {
	// A chart's artifact is `repoURL SPACE chartName`; an image's is just a
	// reference. Splitting first is what lets the two kinds of chart
	// repository go to the two places their publishers actually declare a
	// source.
	ref, chart := ParseArtifact(artifact)
	if IsHelmRepo(ref) {
		return g.helmIndexSource(ctx, ref, chart, version)
	}

	labels, err := g.artifactLabels(ctx, artifact, version)
	if err != nil {
		return "", err
	}
	src := labels["org.opencontainers.image.source"]
	if src == "" {
		return "", fmt.Errorf("%s publishes no org.opencontainers.image.source", artifact)
	}
	return githubPath(src)
}

// helmConfigMediaType is what `helm push` writes as an OCI artifact's config.
// It is Chart.yaml metadata -- name, version, description -- and it has no
// `config.Labels` map, because it is not an image config and never claimed to
// be.
const helmConfigMediaType = "application/vnd.cncf.helm.config.v1+json"

// artifactLabels walks the registry and returns everything the publisher said
// about an artifact, from BOTH of the places OCI lets them say it.
//
// This read one place for its whole life, and that place is the wrong one for
// most of what a Kargo pipeline promotes.
//
//	IMAGES      keep them in the image config blob, as Docker-style `Labels`.
//	HELM CHARTS keep them in the MANIFEST ANNOTATIONS. `helm push` maps
//	            Chart.yaml's `sources[0]` to org.opencontainers.image.source
//	            there, and its config blob is Chart.yaml metadata with no
//	            Labels map at all.
//
// So every chart promotion resolved to "publishes no
// org.opencontainers.image.source" -- a sentence that is not merely unhelpful
// but FALSE, and false in the direction that sends a reader to go and check
// their chart's metadata. This project's own chart publishes that label
// correctly and was reported as not publishing it, on the pull request that
// upgraded the agent to the release which said so.
func (g *GitHubReleases) artifactLabels(ctx context.Context, artifact, version string) (map[string]string, error) {
	plain, _ := ParseArtifact(artifact)
	if IsHelmRepo(plain) {
		// A classic Helm repository has no registry API. Saying so beats
		// `splitRef` reading "https:" as a hostname, which is what produced
		// `https://https/v2//...` and an error naming neither the artifact nor
		// the problem.
		return nil, fmt.Errorf("%s is a Helm repository, not an OCI registry", plain)
	}
	host, repo, ref := splitRef(plain)
	// A promotion names the artifact WITHOUT a tag -- the tag is the thing
	// being promoted, and arrives separately. Resolving `latest` instead 404s
	// on every registry that does not publish one, which is most of them.
	if ref == "latest" && version != "" {
		ref = version
	}
	if host == "" {
		return nil, fmt.Errorf("not an OCI reference: %q", artifact)
	}
	// docker.io is a website; the v2 API is at registry-1.docker.io. Asking
	// the wrong one returns HTML, which surfaced as "invalid character '<'".
	host = registryHost(host)

	tok, err := g.registryToken(ctx, host, repo)
	if err != nil {
		return nil, err
	}

	man, err := g.manifest(ctx, host, repo, ref, tok)
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	mergeLabels(out, man.Annotations)

	// An index points at per-platform manifests. Any of them carries the same
	// label, so take the first with a real platform rather than preferring one
	// -- an arm64-only publisher is as valid as an amd64 one.
	if len(man.Manifests) > 0 {
		child := ""
		for _, m := range man.Manifests {
			if m.Platform.Architecture != "" && m.Platform.Architecture != "unknown" {
				child = m.Digest
				break
			}
		}
		if child == "" {
			// No child declares a real architecture. That is not necessarily a
			// broken index: a single-manifest index and some publishers' output
			// carry `platform: null`, and the label sits on the one child
			// regardless. Following it beats refusing to look.
			child = man.Manifests[0].Digest
		}
		if child == "" {
			return nil, fmt.Errorf("index for %s has no manifest to follow", artifact)
		}
		if man, err = g.manifest(ctx, host, repo, child, tok); err != nil {
			return nil, err
		}
		// A child's annotations are more specific than the index's.
		mergeLabels(out, man.Annotations)
	}

	// The blob is where an IMAGE keeps its labels. Skipped for a Helm chart,
	// whose config blob is Chart.yaml metadata by declared media type and
	// cannot carry Labels -- which also spares the blob hop entirely, and with
	// it the second registry host a blob redirect needs in an egress
	// allow-list.
	if man.Config.Digest == "" || man.Config.MediaType == helmConfigMediaType {
		return out, nil
	}

	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := g.getJSON(ctx,
		fmt.Sprintf("https://%s/v2/%s/blobs/%s", host, repo, man.Config.Digest),
		tok, "", &cfg); err != nil {
		// Annotations already in hand are a complete answer for many
		// artifacts. Losing them because the blob hop was blocked or slow
		// would reintroduce the exact silence this fix removes.
		if len(out) > 0 {
			return out, nil
		}
		return nil, err
	}
	// Labels win over annotations: they are the image-native form, and they
	// are the source that already worked for every image promotion.
	mergeLabels(out, cfg.Config.Labels)
	return out, nil
}

// mergeLabels copies non-empty entries, so a later, more specific source
// overrides an earlier one without an empty string erasing a real value.
func mergeLabels(dst, src map[string]string) {
	for k, v := range src {
		if v != "" {
			dst[k] = v
		}
	}
}

type ociManifest struct {
	// Annotations are where a Helm chart's org.opencontainers.image.* live,
	// and increasingly where an image's do too.
	Annotations map[string]string `json:"annotations"`
	Config      struct {
		Digest string `json:"digest"`
		// MediaType distinguishes an image config, which has Labels, from a
		// Helm chart config, which is Chart.yaml metadata and never will.
		MediaType string `json:"mediaType"`
	} `json:"config"`
	Manifests []struct {
		Digest   string `json:"digest"`
		Platform struct {
			Architecture string `json:"architecture"`
			OS           string `json:"os"`
		} `json:"platform"`
	} `json:"manifests"`
}

func (g *GitHubReleases) manifest(ctx context.Context, host, repo, ref, tok string) (*ociManifest, error) {
	var m ociManifest
	const accept = "application/vnd.oci.image.index.v1+json," +
		"application/vnd.docker.distribution.manifest.list.v2+json," +
		"application/vnd.oci.image.manifest.v1+json," +
		"application/vnd.docker.distribution.manifest.v2+json"
	err := g.getJSON(ctx,
		fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, ref), tok, accept, &m)
	return &m, err
}

// registryToken gets an anonymous pull token.
//
// The Docker registry auth dance in miniature: an unauthenticated request is
// answered with a WWW-Authenticate header naming a token service, and the
// token it hands back is what the real request carries. Registries that need
// no token at all simply return 200 to the first call, which is why an error
// here is not fatal -- the caller retries unauthenticated.
func (g *GitHubReleases) registryToken(ctx context.Context, host, repo string) (string, error) {
	svc := host
	if host == "docker.io" || host == "registry-1.docker.io" {
		svc = "registry.docker.io"
	}
	u := fmt.Sprintf("https://%s/token?scope=repository:%s:pull&service=%s", authHost(host), repo, svc)
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := g.getJSON(ctx, u, "", "", &out); err != nil {
		return "", nil // not fatal: try the request unauthenticated
	}
	if out.Token != "" {
		return out.Token, nil
	}
	return out.AccessToken, nil
}

func authHost(host string) string {
	switch host {
	case "docker.io", "registry-1.docker.io":
		return "auth.docker.io"
	default:
		return host
	}
}

func (g *GitHubReleases) getJSON(ctx context.Context, url, token, accept string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	cl := g.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return newHTTPError(url, resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// splitRef breaks a reference into host, repository and tag-or-digest.
//
// Accepts the oci:// prefix Kargo uses for charts, and a bare
// name:tag@digest. A reference with no host is rejected rather than assumed to
// be Docker Hub: this resolver is used on artifacts a pipeline names
// explicitly, and guessing a registry is the same class of mistake as guessing
// a repository.
func splitRef(ref string) (host, repo, tag string) {
	ref = strings.TrimPrefix(ref, "oci://")
	// A Docker Hub short form is not ambiguous -- convention gives it exactly
	// one meaning -- and the pipeline is handing us the reference rather than
	// us inferring one. What stays refused is a string that is not a reference.
	if full, ok := dockerHubRef(ref); ok {
		ref = full
	}
	if at := strings.Index(ref, "@"); at >= 0 {
		tag, ref = ref[at+1:], ref[:at]
	}
	slash := strings.Index(ref, "/")
	if slash < 0 {
		return "", "", ""
	}
	host, rest := ref[:slash], ref[slash+1:]
	if !strings.Contains(host, ".") && !strings.Contains(host, ":") && host != "localhost" {
		return "", "", ""
	}
	if colon := strings.LastIndex(rest, ":"); colon >= 0 && !strings.Contains(rest[colon:], "/") {
		if tag == "" {
			tag = rest[colon+1:]
		}
		rest = rest[:colon]
	}
	if tag == "" {
		tag = "latest"
	}
	return host, rest, tag
}

// githubPath turns a source URL into "owner/repo", and refuses anything that
// is not GitHub. A GitLab or Gitea source URL is a perfectly good answer to
// "where does this come from" and a useless one to this resolver, which can
// only read GitHub releases.
func githubPath(src string) (string, error) {
	s := strings.TrimSuffix(strings.TrimSpace(src), ".git")
	for _, p := range []string{"https://github.com/", "http://github.com/", "git@github.com:"} {
		if strings.HasPrefix(s, p) {
			parts := strings.Split(strings.TrimPrefix(s, p), "/")
			if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
				return parts[0] + "/" + parts[1], nil
			}
		}
	}
	return "", fmt.Errorf("source %q is not a GitHub repository", src)
}

// getJSONReq runs a prepared request. Shared so the GitHub call and the
// registry calls have one place that decides timeouts and error shape.
func (g *GitHubReleases) getJSONReq(req *http.Request, out any) error {
	cl := g.HTTP
	if cl == nil {
		cl = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return newHTTPError(req.URL.String(), resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// httpError carries the status alongside the message, so a caller can tell
// "rate limited" from "not found" without matching on prose.
//
// The distinction earns its place: rate limiting is a credential problem with
// a fix, and every other failure here is an ordinary absence. Reporting them
// with the same sentence -- which is what happened before, because everything
// was fmt.Errorf -- sends a reader to check whether a project publishes
// releases when the answer is that they were there and nobody was allowed to
// read them.
type httpError struct {
	URL        string
	StatusCode int
	Status     string
	// Limited is set for a rate-limit refusal. GitHub says so in two ways: 429,
	// and -- more often -- a 403 whose X-RateLimit-Remaining is zero.
	Limited bool
}

func (e *httpError) Error() string { return fmt.Sprintf("%s: %s", e.URL, e.Status) }

func newHTTPError(url string, resp *http.Response) error {
	e := &httpError{URL: url, StatusCode: resp.StatusCode, Status: resp.Status}
	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		e.Limited = true
	case http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			e.Limited = true
		}
	}
	return e
}

// isRateLimited reports whether an error was the API refusing on quota.
//
// Never retried anywhere. A retry loop against a rate limit is how one
// explanation becomes a thousand requests and a longer ban; the honest answer
// is a shorter explanation that says why it is shorter.
func isRateLimited(err error) bool {
	var he *httpError
	return errors.As(err, &he) && he.Limited
}

// artifactRevision reads the commit the publisher recorded when they built
// this version of the artifact.
//
// `org.opencontainers.image.revision` is the escape hatch for the case that
// defeats every other approach: a chart whose version numbering has nothing to
// do with the git tags of the project it packages. A SHA needs no arithmetic to
// be correct, and a publisher that sets the label has already answered the
// question this would otherwise be guessing at.
//
// Empty rather than an error when the label is absent -- an artifact without it
// is ordinary, not broken.
func (g *GitHubReleases) artifactRevision(ctx context.Context, artifact, version string) (string, error) {
	labels, err := g.artifactLabels(ctx, artifact, version)
	if err != nil {
		return "", err
	}
	return labels["org.opencontainers.image.revision"], nil
}
