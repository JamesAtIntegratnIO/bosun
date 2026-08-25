package gate

import (
	"strings"
	"testing"
)

// This dispatch lived in two packages that do not import each other, and each
// carried its own comment about the same bug. One implementation, one test.
func TestHelmChartArgs(t *testing.T) {
	for _, tc := range []struct {
		name, repo, chart string
		want              []string
		wantErr           string
	}{
		{
			name: "classic Helm repository takes the chart name and --repo",
			repo: "https://kyverno.github.io/kyverno", chart: "kyverno",
			want: []string{"kyverno", "--repo", "https://kyverno.github.io/kyverno"},
		},
		{
			name: "plain http is a Helm repository too",
			repo: "http://charts.internal", chart: "thing",
			want: []string{"thing", "--repo", "http://charts.internal"},
		},
		{
			name: "a Helm repository with no chart named is an error, not a guess",
			repo: "https://kyverno.github.io/kyverno", chart: "",
			wantErr: "is a Helm repository",
		},
		{
			name: "an OCI repository is the reference",
			repo: "oci://ghcr.io/org", chart: "bosun",
			want: []string{"oci://ghcr.io/org/bosun"},
		},
		{
			name: "an OCI repository already ending in the chart is not doubled",
			repo: "oci://ghcr.io/org/charts/bosun", chart: "bosun",
			want: []string{"oci://ghcr.io/org/charts/bosun"},
		},
		{
			name: "a trailing slash does not become an empty path segment",
			repo: "oci://ghcr.io/org/", chart: "bosun",
			want: []string{"oci://ghcr.io/org/bosun"},
		},
		{
			name: "no repository is a chart the caller already made resolvable",
			repo: "", chart: "podinfo",
			want: []string{"podinfo"},
		},
		{
			name: "neither is an error",
			repo: "", chart: "",
			wantErr: "neither a chart repository nor a chart name",
		},
		{
			name: "a bare reference is OCI written without its scheme",
			repo: "ghcr.io/org", chart: "bosun",
			want: []string{"oci://ghcr.io/org/bosun"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HelmChartArgs(tc.repo, tc.chart)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want an error containing %q, got %v (%v)", tc.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Join(got, " ") != strings.Join(tc.want, " ") {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// The exact failure that motivated the shared helper: prepending oci:// to a
// classic Helm repository built oci://https://... and helm answered "invalid
// repository".
func TestAClassicRepositoryNeverGetsAnOCIScheme(t *testing.T) {
	got, err := HelmChartArgs("https://charts.external-secrets.io", "external-secrets")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if strings.HasPrefix(a, "oci://https://") || strings.HasPrefix(a, "oci://http://") {
			t.Fatalf("a classic repository was given an OCI scheme: %v", got)
		}
	}
}
