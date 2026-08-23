package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// sourceRepo reads org.opencontainers.image.source off an artifact and returns
// it as "owner/repo".
//
// Four hops, because that is where OCI puts it:
//
//	token   -> anonymous pull token for this repository
//	index   -> the multi-arch index (or a plain manifest, for single-arch)
//	child   -> one platform's manifest, if it was an index
//	config  -> the image config blob, whose Labels carry the annotation
//
// Anonymous throughout. These are public artifacts, and a resolver that needed
// credentials for every upstream registry would be a credential-management
// problem wearing an explanation feature's clothes.
func (g *GitHubReleases) sourceRepo(ctx context.Context, artifact, version string) (string, error) {
	host, repo, ref := splitRef(artifact)
	// A promotion names the artifact WITHOUT a tag -- the tag is the thing
	// being promoted, and arrives separately. Resolving `latest` instead 404s
	// on every registry that does not publish one, which is most of them.
	if ref == "latest" && version != "" {
		ref = version
	}
	if host == "" {
		return "", fmt.Errorf("not an OCI reference: %q", artifact)
	}

	tok, err := g.registryToken(ctx, host, repo)
	if err != nil {
		return "", err
	}

	man, err := g.manifest(ctx, host, repo, ref, tok)
	if err != nil {
		return "", err
	}
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
			return "", fmt.Errorf("index for %s has no platform manifest", artifact)
		}
		if man, err = g.manifest(ctx, host, repo, child, tok); err != nil {
			return "", err
		}
	}
	if man.Config.Digest == "" {
		return "", fmt.Errorf("no image config for %s", artifact)
	}

	var cfg struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := g.getJSON(ctx,
		fmt.Sprintf("https://%s/v2/%s/blobs/%s", host, repo, man.Config.Digest),
		tok, "", &cfg); err != nil {
		return "", err
	}

	src := cfg.Config.Labels["org.opencontainers.image.source"]
	if src == "" {
		return "", fmt.Errorf("%s publishes no org.opencontainers.image.source", artifact)
	}
	return githubPath(src)
}

type ociManifest struct {
	Config struct {
		Digest string `json:"digest"`
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
		return fmt.Errorf("%s: %s", url, resp.Status)
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
		return fmt.Errorf("%s: %s", req.URL, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
