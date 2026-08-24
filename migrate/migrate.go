// Package migrate turns one red gate into a repair: a CustomResourceDefinition
// that stopped serving a version, and the manifests still declaring it.
//
// It is shared by the gate and the agent ON PURPOSE, and that sharing is the
// safety argument. The gate uses Scan to decide whether a dropped version
// blocks -- it blocks exactly while manifests in the repository still declare
// it -- and Line to write the report. The agent uses ParseReport to read that
// line back and Migrate to rewrite the declaring manifests. One scanner, one
// line format, two callers: the inspection and the repair cannot disagree
// about what a consumer is, and the re-run gate independently verifies the
// repair by counting again.
//
// The agent side involves no model. Everything it needs -- the kind, the
// dropped versions, the version that remains -- is computed by the gate and
// carried in the report line, so the rewrite is a deterministic function of
// evidence, not a proposal to be corroborated.
//
// By extension this package owns READING the gate's report, not only the
// repair: Line writes a finding, ParseReport reads it back, and Subjects names
// what a whole report is about so an upstream search can be aimed at it. They
// live together because they are one format, and two files that each believe
// they know it is how a change to the report becomes a silent no-op somewhere
// else.
package migrate

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Dropped is one CustomResourceDefinition that stopped serving versions, and
// where its consumers must move.
type Dropped struct {
	// CRD is the CustomResourceDefinition's own name: <plural>.<group>. The
	// API machinery guarantees that shape, which is why Group needs no second
	// source of truth.
	CRD string
	// Group is the API group consumers declare, derived from CRD.
	Group string
	// Kind is what a consuming manifest writes in its `kind:` field --
	// spec.names.kind, not the plural.
	Kind string
	// Versions are the served versions that are gone.
	Versions []string
	// Target is the served version consumers must move to.
	Target string
}

// Report-section headings the gate emits and the agent looks for. They live
// here so both sides read the same bytes by construction -- the same reason
// the gate's ReportMarker lives in the gate binary rather than in each CI
// adapter.
const (
	HeadingTargeting  = "### Cluster targeting changed"
	HeadingSource     = "### Source changed"
	HeadingAPIVersion = "**API version changed**"
)

// OtherBlockers reports whether the gate's report contains a blocking finding
// other than a dropped served version: a targeting change, a source change, or
// an object whose own apiVersion moved. A repair that runs anyway would fix
// the fixable half and leave a red gate implying it had not -- the agent
// escalates those instead.
func OtherBlockers(report string) bool {
	return strings.Contains(report, HeadingTargeting) ||
		strings.Contains(report, HeadingSource) ||
		strings.Contains(report, HeadingAPIVersion)
}

// Line renders one dropped-version finding as the report bullet.
//
// When the consumer kind and the surviving version are known the line carries
// them, and that suffix is what makes the finding repairable: the agent's
// parser accepts nothing less. A known kind with NO survivor is a CRD removed
// outright -- there is nowhere to move, so the line says so and the parser
// deliberately cannot act on it. Without either it falls back to the plain
// statement.
func Line(object, dropped, kind, target string) string {
	switch {
	case kind != "" && target != "":
		return fmt.Sprintf("- `%s`: no longer serves `%s` — `%s` manifests must move to `%s`",
			object, dropped, kind, target)
	case kind != "":
		return fmt.Sprintf("- `%s`: **removed outright** — every `%s` manifest breaks on apply, and there is no version to move to",
			object, kind)
	default:
		return fmt.Sprintf("- `%s`: no longer serves `%s`", object, dropped)
	}
}

// reportLine is Line's inverse, anchored hard: the CRD name must be
// plural.group, the kind a bare identifier, the versions version-shaped. A
// line that drifts from the format parses as nothing rather than as almost
// the right migration.
//
// The optional " in <namespace>" exists because chart-diff stamps every object
// with the Application's destination namespace, cluster-scoped or not -- so
// the real report reads `.../externalsecrets.external-secrets.io in
// external-secrets`, and a parser blind to that would never fire on a real
// promotion.
var reportLine = regexp.MustCompile(
	"^- `CustomResourceDefinition/([a-z0-9][a-z0-9.-]*\\.[a-z0-9.-]+)(?: in [^`]+)?`: " +
		"no longer serves `([^`]+)` — `([A-Za-z][A-Za-z0-9]*)` manifests must move to `(v[0-9][A-Za-z0-9]*)`$")

// ParseReport extracts every repairable dropped-version finding from a gate
// report. Lines in the old, suffix-less format are deliberately not returned:
// they name a problem without naming the destination, and a repair must not
// guess.
func ParseReport(report string) []Dropped {
	var out []Dropped
	for _, raw := range strings.Split(report, "\n") {
		m := reportLine.FindStringSubmatch(strings.TrimRight(raw, "\r"))
		if m == nil {
			continue
		}
		crd := m[1]
		dot := strings.Index(crd, ".")
		var versions []string
		for _, v := range strings.Split(m[2], ",") {
			if v = strings.TrimSpace(v); v != "" {
				versions = append(versions, v)
			}
		}
		if dot < 0 || len(versions) == 0 {
			continue
		}
		out = append(out, Dropped{
			CRD:      crd,
			Group:    crd[dot+1:],
			Kind:     m[3],
			Versions: versions,
			Target:   m[4],
		})
	}
	return out
}

// kubeVersion matches Kubernetes API version names: v1, v2, v1beta1, v2alpha3.
var kubeVersion = regexp.MustCompile(`^v(\d+)(?:(alpha|beta)(\d+))?$`)

// PreferredVersion picks the version consumers should move to, by the same
// priority the API server uses: GA before beta before alpha, higher numbers
// first within each class. Anything that does not parse as a Kubernetes
// version sorts last, alphabetically -- deterministic even for charts that
// invent their own naming.
func PreferredVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	rank := func(v string) (class, major, minor int) {
		m := kubeVersion.FindStringSubmatch(v)
		if m == nil {
			return 0, 0, 0
		}
		major, _ = strconv.Atoi(m[1])
		switch m[2] {
		case "":
			return 3, major, 0
		case "beta":
			minor, _ = strconv.Atoi(m[3])
			return 2, major, minor
		default: // alpha
			minor, _ = strconv.Atoi(m[3])
			return 1, major, minor
		}
	}
	sorted := append([]string(nil), versions...)
	sort.Slice(sorted, func(i, j int) bool {
		ci, mi, ni := rank(sorted[i])
		cj, mj, nj := rank(sorted[j])
		if ci != cj {
			return ci > cj
		}
		if mi != mj {
			return mi > mj
		}
		if ni != nj {
			return ni > nj
		}
		return sorted[i] < sorted[j]
	})
	return sorted[0]
}
