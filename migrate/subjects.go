package migrate

import (
	"regexp"
	"sort"
	"strings"
)

// objectBullet matches the object lines the gate writes under its Added,
// Removed and Changed headings: a bullet whose whole content is a backticked
// `Kind/name`, optionally stamped with the destination namespace.
//
// It lives beside reportLine for the same reason reportLine lives here: the
// gate writes this shape in gate/diff.go and something has to read it back,
// and two files that each believe they know the format is how a report change
// becomes a silent no-op somewhere else.
var objectBullet = regexp.MustCompile(
	"^\\s*- `([A-Za-z][A-Za-z0-9]*)/([^`]+?)(?: in [^`]+)?`\\s*$")

// Subjects names the things a gate report is ABOUT: the kinds and the resource
// names in its findings, plus the kind and group of any dropped API version.
//
// It exists to aim an upstream search. A finding like "the chart removed its
// ClusterRole" is something the render proves and cannot explain, and the
// commits between the two upstream tags usually can -- but only if something
// deterministic decides which commits are about this. These are the terms that
// decision is made from.
//
// DETERMINISTIC, and that is the whole point. The alternative is handing the
// model the range and asking which commits support its conclusion, which is not
// evidence: it is a second opinion from the same opinion. Here the gate's own
// findings choose, and the model is shown the result.
//
// Names beat kinds. `trivy-operator-explorer` appears in an upstream commit
// message only when that commit is about it; `Deployment` appears in half of
// them, which is why an ordering exists and callers take the front of the list.
func Subjects(report string) []string {
	var names, kinds []string
	seen := map[string]bool{}
	add := func(dst *[]string, s string) {
		s = strings.TrimSpace(s)
		// One and two characters match everything. A term that matches
		// everything selects nothing, it just makes the selection look large.
		if len(s) < 3 || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		*dst = append(*dst, s)
	}

	for _, raw := range strings.Split(report, "\n") {
		line := strings.TrimRight(raw, "\r")
		if m := objectBullet.FindStringSubmatch(line); m != nil {
			add(&kinds, m[1])
			add(&names, m[2])
		}
	}
	// The dropped-version findings carry their own structure and are already
	// parsed; re-reading them out of prose would be a second parser for one
	// format.
	for _, d := range ParseReport(report) {
		add(&kinds, d.Kind)
		add(&names, d.CRD)
		add(&names, d.Group)
	}

	sort.Strings(names)
	sort.Strings(kinds)
	return append(names, kinds...)
}
