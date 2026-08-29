package gate

import (
	"context"
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
		{
			name: "a chart beginning with a dash is a flag, not a chart",
			repo: "", chart: "--post-renderer=/tmp/x",
			wantErr: "helm reads it as a flag",
		},
		{
			name: "so is a repository beginning with a dash",
			repo: "--repo=http://attacker.example", chart: "thing",
			wantErr: "helm reads it as a flag",
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

// Helm's parser accepts flags interspersed with positionals, so a chart name
// beginning with a dash is not a chart name: --post-renderer makes helm exec an
// external binary. Execution here is argv and never a shell, so the whole class
// is flag injection, and the cheapest defence is refusing the shape.
func TestFlagLikeValuesAreRefusedWhereverTheyReachArgv(t *testing.T) {
	if err := RefuseFlagLike("chart version", "--post-renderer=/tmp/x"); err == nil {
		t.Error("a version beginning with a dash must be refused")
	}
	if err := RefuseFlagLike("chart version", "  -v1.2.3"); err == nil {
		t.Error("leading whitespace must not smuggle a dash past the check")
	}
	if err := RefuseFlagLike("chart version", "1.2.3-rc1"); err != nil {
		t.Errorf("a dash inside a version is ordinary: %v", err)
	}
}

// The chart-diff path builds helm arguments from a Row the pull request wrote.
func TestRenderChartVersionRefusesAFlagLikeVersion(t *testing.T) {
	_, err := renderChartVersion(context.Background(), t.TempDir(), Row{
		App: "thing", Chart: "thing", ChartRepo: "https://charts.example.com",
		Version: "--post-renderer=/tmp/x",
	})
	if err == nil || !strings.Contains(err.Error(), "helm reads it as a flag") {
		t.Fatalf("want the version refused, got %v", err)
	}
}

// `helm show` is the one helm invocation that does not go through
// HelmChartArgs, so it carries the same check itself.
func TestHelmShowRefusesAFlagLikeVersion(t *testing.T) {
	_, err := helmShow(context.Background(), t.TempDir(), "values", Row{
		Chart: "thing", ChartRepo: "https://charts.example.com",
		Version: "--post-renderer=/tmp/x",
	})
	if err == nil || !strings.Contains(err.Error(), "helm reads it as a flag") {
		t.Fatalf("want the version refused, got %v", err)
	}
}
