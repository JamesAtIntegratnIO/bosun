package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// OCI lets a publisher say where an artifact came from in two places, and which
// one they use depends on what kind of artifact it is. Reading one of them is
// how this feature came to report a chart that publishes the label correctly as
// publishing no label at all.
//
// Both fixtures below are the real shapes, taken from ghcr.io.

func ociServer(t *testing.T, routes map[string]any) *GitHubReleases {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/token") {
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon"})
			return
		}
		for suffix, body := range routes {
			if strings.HasSuffix(r.URL.Path, suffix) {
				_ = json.NewEncoder(w).Encode(body)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	g := &GitHubReleases{APIBase: srv.URL, HTTP: srv.Client()}
	// The OCI walk builds https://<registry>/... URLs; this points them at the
	// test server so the whole hop sequence runs rather than being stubbed at
	// the interesting end.
	g.HTTP = registryClient(t, g)
	return g
}

// A Helm chart pushed with `helm push`: the source is in the MANIFEST
// ANNOTATIONS, and the config blob is Chart.yaml metadata that has no Labels
// map and never will.
//
// This is the case that was broken, and it is most of what a Kargo pipeline
// promotes.
func TestAHelmChartPublishesItsSourceInTheManifestAnnotations(t *testing.T) {
	blobFetched := false
	g := ociServer(t, map[string]any{
		"/manifests/0.13.0": map[string]any{
			"config": map[string]any{
				"mediaType": "application/vnd.cncf.helm.config.v1+json",
				"digest":    "sha256:chartmeta",
			},
			"annotations": map[string]string{
				"org.opencontainers.image.source":  "https://github.com/example-org/bosun",
				"org.opencontainers.image.version": "0.13.0",
			},
		},
	})
	// Any blob request at all would be a regression: a Helm config cannot hold
	// Labels, and the hop needs a second registry host in an egress allow-list.
	inner := g.HTTP
	g.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/blobs/") {
			blobFetched = true
		}
		return inner.Transport.RoundTrip(r)
	})}

	repo, err := g.sourceRepo(context.Background(), "oci://ghcr.io/example-org/charts/bosun", "0.13.0")
	if err != nil {
		t.Fatalf("a chart that publishes its source was reported as not publishing it: %v", err)
	}
	if repo != "example-org/bosun" {
		t.Fatalf("resolved %q", repo)
	}
	if blobFetched {
		t.Error("fetched the config blob of a Helm chart, which cannot contain Labels")
	}
}

// The case that always worked, and must keep working: an image index, a
// platform child, and Docker-style Labels in the config blob.
func TestAnImageStillPublishesItsSourceInTheConfigBlob(t *testing.T) {
	g := ociServer(t, map[string]any{
		"/manifests/main-abc": map[string]any{
			"manifests": []any{
				map[string]any{"digest": "sha256:attest", "platform": map[string]string{"architecture": "unknown"}},
				map[string]any{"digest": "sha256:child", "platform": map[string]string{"architecture": "arm64"}},
			},
		},
		"/manifests/sha256:child": map[string]any{
			"config": map[string]any{
				"mediaType": "application/vnd.oci.image.config.v1+json",
				"digest":    "sha256:cfg",
			},
		},
		"/blobs/sha256:cfg": map[string]any{
			"config": map[string]any{"Labels": map[string]string{
				"org.opencontainers.image.source": "https://github.com/example-org/gate",
			}},
		},
	})

	repo, err := g.sourceRepo(context.Background(), "ghcr.io/example-org/gate", "main-abc")
	if err != nil {
		t.Fatal(err)
	}
	if repo != "example-org/gate" {
		t.Fatalf("resolved %q", repo)
	}
}

// A child manifest's annotations are more specific than the index's, and the
// config blob's Labels are more specific still -- that ordering is what keeps
// every image that worked before working identically.
func TestTheMostSpecificSourceWins(t *testing.T) {
	g := ociServer(t, map[string]any{
		"/manifests/v1": map[string]any{
			"annotations": map[string]string{"org.opencontainers.image.source": "https://github.com/example-org/from-index"},
			"manifests": []any{
				map[string]any{"digest": "sha256:child", "platform": map[string]string{"architecture": "amd64"}},
			},
		},
		"/manifests/sha256:child": map[string]any{
			"annotations": map[string]string{"org.opencontainers.image.source": "https://github.com/example-org/from-child"},
			"config":      map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:cfg"},
		},
		"/blobs/sha256:cfg": map[string]any{
			"config": map[string]any{"Labels": map[string]string{
				"org.opencontainers.image.source": "https://github.com/example-org/from-labels",
			}},
		},
	})

	repo, err := g.sourceRepo(context.Background(), "ghcr.io/example-org/thing", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if repo != "example-org/from-labels" {
		t.Fatalf("resolved %q, want the config blob's Labels to win", repo)
	}
}

// An artifact that genuinely says nothing still says nothing -- the message
// this fix makes rare has to stay correct when it is right.
func TestAnArtifactWithNoSourceStillSaysSo(t *testing.T) {
	g := ociServer(t, map[string]any{
		"/manifests/1.0.0": map[string]any{
			"config": map[string]any{"mediaType": "application/vnd.cncf.helm.config.v1+json", "digest": "sha256:x"},
			"annotations": map[string]string{
				"org.opencontainers.image.version": "1.0.0",
			},
		},
	})

	_, err := g.sourceRepo(context.Background(), "oci://ghcr.io/example-org/charts/quiet", "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "org.opencontainers.image.source") {
		t.Fatalf("err = %v", err)
	}
}

// Annotations already in hand are a complete answer. Losing them because the
// blob hop was blocked would reintroduce the silence this removes -- and a
// blocked blob hop is a real, previously observed failure here.
func TestAnnotationsSurviveABlockedBlobHop(t *testing.T) {
	g := ociServer(t, map[string]any{
		"/manifests/v2": map[string]any{
			"annotations": map[string]string{"org.opencontainers.image.source": "https://github.com/example-org/thing"},
			"config":      map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:missing"},
		},
	})

	repo, err := g.sourceRepo(context.Background(), "ghcr.io/example-org/thing", "v2")
	if err != nil {
		t.Fatalf("a readable annotation was thrown away when the blob failed: %v", err)
	}
	if repo != "example-org/thing" {
		t.Fatalf("resolved %q", repo)
	}
}
