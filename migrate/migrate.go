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

// OtherBlockers is the PRE-MARKER FALLBACK for the same question
// Blockers.OtherThanDropped answers, and should only be reached for a report
// from a gate old enough not to emit the machine-readable breakdown.
//
// It scrapes three prose headings, so it answers a slightly different question
// than the structured count and the two have already drifted: gate/diff.go
// prints HeadingAPIVersion whenever any apiVersion object is present, while
// Blockers.APIVersion excludes the ones marked PartOfMigration. After a partial
// repair the deterministic path was therefore skipped on the retry the attempt
// cap exists to allow.
//
// Callers should prefer ParseBlockers and fall back to this only when it
// returns false.
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

// PreferredOrder is every version, most-preferred first, by the same ranking
// PreferredVersion uses.
//
// Exported for the structural migration, which wants the NEWEST of several
// dropped versions -- the one a document is most likely to have been written
// against, and therefore the one whose schema best describes the shape being
// left behind. Sharing the ranking rather than re-deriving it is the same rule
// as sharing the report format: two orderings would eventually disagree.
func PreferredOrder(versions []string) []string {
	if len(versions) == 0 {
		return nil
	}
	out := append([]string(nil), versions...)
	sort.Slice(out, func(i, j int) bool {
		return PreferredVersion([]string{out[i], out[j]}) == out[i] && out[i] != out[j]
	})
	return out
}

// Blockers counts each reason the gate is red, separately, because they do not
// all have the same answer.
//
// The distinction that matters is whether a reason has a REPOSITORY-SIDE
// remedy. Manifests declaring a dropped version do -- move them, which the
// agent does deterministically. A targeting or source change does -- a human
// edits the values that caused it. An object whose own apiVersion moved does
// NOT: the chart renders it, nothing in the repository declares it, and there
// is no edit anyone can make. Telling a reader "this needs a human" without
// telling them nothing can be done in the repository wastes the search.
type Blockers struct {
	Targeting int `json:"targeting"`
	Source    int `json:"source"`
	// APIVersion is objects the CHART renders whose apiVersion moved, and that
	// is not part of a migration the repair is performing.
	APIVersion int `json:"apiVersion"`
	// Consumers is manifests IN THIS REPOSITORY still declaring a version a
	// definition stopped serving.
	Consumers int `json:"consumers"`
	// Unscanned is definitions whose consumers could not be counted. "We could
	// not look" blocks, and is not the same as "we looked and found none".
	Unscanned int `json:"unscanned"`
	// ValuesDropped is settings this repository makes that the new chart
	// version no longer declares. Helm ignores an unknown value instead of
	// failing on it, so these stop applying while everything stays green.
	ValuesDropped int `json:"valuesDropped"`
	// Schema is manifests the target cluster's schemas reject. Like
	// APIVersion, it has no remedy the agent performs: the manifest is wrong
	// in a way that needs an author, not a version swap.
	Schema int `json:"schema"`
}

// OtherThanDropped reports whether anything blocks other than a dropped served
// version: a targeting change, a source change, or an object whose own
// apiVersion moved. A repair that runs anyway would fix the fixable half and
// leave a red gate implying it had not.
//
// Counted from the structured breakdown, so an apiVersion object the repair is
// already migrating does not read as an unrelated blocker.
func (b Blockers) OtherThanDropped() bool {
	return b.Targeting > 0 || b.Source > 0 || b.APIVersion > 0 || b.Schema > 0
}

func (b Blockers) Any() bool {
	return b.Targeting+b.Source+b.APIVersion+b.Consumers+b.Unscanned+b.ValuesDropped+b.Schema > 0
}

// RepoSideRemedy reports whether anything a person or an agent could change in
// this repository would clear the gate.
func (b Blockers) RepoSideRemedy() bool {
	return b.Targeting > 0 || b.Source > 0 || b.Consumers > 0 || b.Unscanned > 0 || b.ValuesDropped > 0
}

// The rendered-object changes are grouped under one heading per class. The
// labels and the heading format live here for the same reason ReportMarker
// does: the gate writes them, this package reads them, and a reader that
// re-derives the format from memory drifts from the writer silently.
//
// It drifted once already. A reader matching the bullets without tracking
// which group it was in treated an ADDED CustomResourceDefinition as a removed
// one -- all three groups render a bullet of exactly the same shape.
const (
	GroupAdded   = "Added"
	GroupRemoved = "Removed"
	GroupChanged = "Changed"
)

// ObjectGroupHeading is the heading above one class of object change.
func ObjectGroupHeading(label string, n int) string {
	return fmt.Sprintf("**%s (%d)**", label, n)
}

// objectGroup matches any of the three group headings, so a scanner knows when
// it has left the one it cares about.
var objectGroup = regexp.MustCompile(`^\*\*(Added|Removed|Changed) \(\d+\)\*\*$`)

// crdBullet matches an object bullet naming a CustomResourceDefinition. The
// optional " in <namespace>" is there for the same reason it is on reportLine:
// chart-diff stamps every object with the Application's destination namespace,
// cluster-scoped or not.
var crdBullet = regexp.MustCompile(
	"^- `CustomResourceDefinition/([a-z0-9][a-z0-9.-]*\\.[a-z0-9.-]+)(?: in [^`]+)?`$")

// ParseRemovedCRDs returns the definitions the report lists as removed
// outright -- gone entirely, not merely no longer serving a version.
//
// The two are different findings and only the second carries its own versions,
// which is why this exists alongside ParseReport rather than inside it.
//
// Only bullets inside the Removed group are returned. A reader that matched
// the bullet shape alone fired on added and changed definitions too, and every
// hit costs a live apiserver lookup on a path a human is waiting on.
func ParseRemovedCRDs(report string) []string {
	var out []string
	inRemoved := false
	for _, raw := range strings.Split(report, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if g := objectGroup.FindStringSubmatch(line); g != nil {
			inRemoved = g[1] == GroupRemoved
			continue
		}
		// Any new section ends the group, whether or not another one starts.
		if strings.HasPrefix(line, "###") {
			inRemoved = false
			continue
		}
		if !inRemoved {
			continue
		}
		if m := crdBullet.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// BlockersMarker prefixes the machine-readable breakdown. Same argument as the
// headings above: the gate emits it, this package reads it, and one definition
// means they cannot disagree.
const BlockersMarker = "<!-- gitops-gate:blockers "

// ParseBlockers reads the gate's machine-readable breakdown of why it is red.
//
// The second return is false when the report carries no breakdown at all,
// which is what a report from an older gate looks like. That case must not be
// mistaken for "no blockers": the caller falls back to its previous behaviour
// rather than concluding the gate is green, because a wrong answer here means
// the agent decides nothing can be repaired and says so with confidence.
func ParseBlockers(report string) (Blockers, bool) {
	i := strings.Index(report, BlockersMarker)
	if i < 0 {
		return Blockers{}, false
	}
	rest := report[i+len(BlockersMarker):]
	if j := strings.Index(rest, "-->"); j >= 0 {
		rest = rest[:j]
	}
	var b Blockers
	into := map[string]*int{
		"targeting": &b.Targeting, "source": &b.Source, "apiVersion": &b.APIVersion,
		"consumers": &b.Consumers, "unscanned": &b.Unscanned,
		"valuesDropped": &b.ValuesDropped, "schema": &b.Schema,
	}
	found := false
	for _, field := range strings.Fields(rest) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		p, ok := into[k]
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		*p, found = n, true
	}
	return b, found
}
