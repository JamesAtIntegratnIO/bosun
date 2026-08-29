package gate

import (
	"errors"
	"fmt"
	"strings"
)

// ErrFlagLike is a value helm would read as a flag rather than as itself.
var ErrFlagLike = errors.New("begins with a dash, so helm reads it as a flag rather than as a value")

// RefuseFlagLike rejects a value that is about to be placed in helm's argv as
// a bare token.
//
// Helm's parser accepts flags interspersed with positionals, so a chart named
// `--post-renderer=/tmp/x` is not a chart name at all: it is helm being told to
// exec a binary. Execution here is argv and never a shell, so this is flag
// injection rather than command injection, but the chart, the repository and
// the version all reach argv from either the promotion payload or the pull
// request's own `.gitops-gate.yaml`, and neither of those has a trust level
// established anywhere in this program. Refusing the whole shape costs one
// comparison and does not depend on knowing which flags helm happens to have.
func RefuseFlagLike(what, v string) error {
	if strings.HasPrefix(strings.TrimSpace(v), "-") {
		return fmt.Errorf("%s %q %w", what, v, ErrFlagLike)
	}
	return nil
}

// HelmChartArgs turns a chart repository and chart name into the leading
// arguments of a `helm template` invocation.
//
// One implementation because there were two, in packages that do not import
// each other, and each carried its own comment about the same bug: prepending
// `oci://` to whatever it was given built
// `oci://https://kyverno.github.io/kyverno kyverno` for a classic Helm
// repository and failed with `invalid repository`. Half the artifacts in a real
// target list are that shape, and they include external-secrets, kyverno and
// cert-manager, the charts that drop CRD versions, which is to say
// every promotion the schema probe exists for.
//
// The four cases, in the order they are tested:
//
//   - a classic Helm repository (http:// or https://) takes the chart name plus
//     --repo, rather than a pre-added repository, so nothing has to mutate the
//     runner's helm config;
//   - an oci:// repository is the chart reference, with the chart name appended
//     only when the repository does not already end in it;
//   - no repository at all is a chart the caller has already made resolvable;
//   - anything else is an OCI reference written without its scheme.
func HelmChartArgs(repo, chart string) ([]string, error) {
	repo = strings.TrimSpace(repo)
	chart = strings.TrimSpace(chart)

	if err := RefuseFlagLike("chart repository", repo); err != nil {
		return nil, err
	}
	if err := RefuseFlagLike("chart", chart); err != nil {
		return nil, err
	}

	switch {
	case strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "http://"):
		if chart == "" {
			return nil, fmt.Errorf("%s is a Helm repository and no chart in it was named", repo)
		}
		return []string{chart, "--repo", repo}, nil

	case strings.HasPrefix(repo, "oci://"):
		return []string{ociChartRef(repo, chart)}, nil

	case repo == "":
		if chart == "" {
			return nil, fmt.Errorf("neither a chart repository nor a chart name was given")
		}
		return []string{chart}, nil

	default:
		return []string{ociChartRef("oci://"+repo, chart)}, nil
	}
}

// ociChartRef joins an OCI repository to a chart name without doubling it.
//
// ArgoCD accepts a repository URL that already ends in the chart name alongside
// a `chart` field naming the same thing. Appending regardless produced
// `.../charts/bosun/bosun`, a 403, and an addon silently dropped from
// resource-level coverage.
func ociChartRef(repo, chart string) string {
	repo = strings.TrimRight(repo, "/")
	if chart == "" || strings.HasSuffix(repo, "/"+chart) {
		return repo
	}
	return repo + "/" + chart
}
