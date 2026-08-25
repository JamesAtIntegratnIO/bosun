package gate

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

type Change struct {
	Kind    string `json:"kind"` // added | removed | version | moved
	Cluster string `json:"cluster"`
	App     string `json:"app"`
	AppSet  string `json:"appset,omitempty"`
	From    string `json:"from,omitempty"`
	To      string `json:"to,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type DiffResult struct {
	// Targeting changes block the merge: an Application appearing on or
	// vanishing from a cluster is the failure mode that reading a values diff
	// does not reveal.
	Targeting []Change `json:"targeting"`
	// Introduced covers whole ApplicationSets that did not exist before, and
	// does NOT block. Adding an addon is a deliberate act by the author of the
	// pull request; the dangerous case is an addon that already existed
	// quietly changing which clusters it reaches. Blocking on both would make
	// every new-addon PR red for a reason nobody needs to investigate, and a
	// check that is routinely overridden stops being a check.
	Introduced []Change `json:"introduced"`
	// Versions are reported, not blocked -- a version bump is the point.
	Versions []Change `json:"versions"`
	// Other covers a source moving between chart and path, or a project or
	// namespace change: not a targeting change, but not routine either.
	Other []Change `json:"other"`

	// Objects are resource-level differences, present only when the
	// repository renders manifests into git. This is the evidence a reviewer
	// -- or a triage agent -- actually needs: a version number says a chart
	// moved, whereas "removed two containers, added a DaemonSet and four
	// CRDs" says what will happen.
	Objects  []ObjectChange `json:"objects,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
}

func (d *DiffResult) Blocking() bool {
	if len(d.Targeting) > 0 || len(d.Other) > 0 {
		return true
	}
	// An API version moving under an existing resource is a migration, and
	// migrations are the class of change that renders perfectly and breaks at
	// runtime. Objects appearing or changing are reported but not blocked --
	// that is what a version bump legitimately does.
	for _, o := range d.Objects {
		// Both are migrations. The first is an object whose own apiVersion
		// moved; the second is a CustomResourceDefinition that stopped serving
		// one, which the first cannot see because the CRD object itself is
		// apiextensions.k8s.io/v1 on both sides.
		if o.Kind == "apiVersion" && !o.PartOfMigration {
			return true
		}
		// A dropped served version blocks exactly while manifests in this
		// repository still declare it -- they are what breaks at apply. A
		// finding whose consumers were counted at zero is reported and does
		// not block, which is what lets a repair that moves every consumer
		// turn this red green on the re-run. Not scanned means not counted:
		// "we could not look" blocks, for the same reason a bodiless CRD is
		// reported as changed rather than claimed safe.
		if o.Kind == "crdVersionRemoved" && (!o.ConsumersKnown || len(o.ConsumerFiles) > 0) {
			return true
		}
		// A setting the new chart no longer reads. It renders green by
		// construction -- helm ignores an unknown value -- so if this does not
		// block, nothing anywhere will ever mention it.
		if o.Kind == "valuesKeyDropped" && len(o.Keys) > 0 {
			return true
		}
	}
	return false
}

func Diff(base, head *Table) *DiffResult {
	res := &DiffResult{}

	// ApplicationSets present before this change. An ApplicationSet missing
	// from this set is newly introduced, not newly leaking.
	baseAppSets := map[string]bool{}
	for _, r := range base.Rows {
		baseAppSets[r.AppSet] = true
	}

	baseByKey := map[string]Row{}
	for _, r := range base.Rows {
		baseByKey[r.Key()] = r
	}
	headByKey := map[string]Row{}
	for _, r := range head.Rows {
		headByKey[r.Key()] = r
	}

	// An app that vanishes from one cluster and appears on another is one
	// event, not two -- report it as a move so the reviewer sees the shape of
	// what happened rather than two unrelated-looking lines.
	//
	// Only when there is one candidate on each side. Pairing two departures
	// with two arrivals is a guess: nothing in the render says which arrival
	// corresponds to which departure, and asserting one anyway produced a
	// sentence the reader had no way to check. Worse, both slices were built
	// by ranging a map, so the guess was not even stable -- identical input
	// could describe two different moves on two runs.
	removedByApp := map[string][]Row{}
	addedByApp := map[string][]Row{}

	for k, b := range baseByKey {
		if _, ok := headByKey[k]; !ok {
			removedByApp[b.AppSet] = append(removedByApp[b.AppSet], b)
		}
	}
	for k, h := range headByKey {
		if _, ok := baseByKey[k]; !ok {
			addedByApp[h.AppSet] = append(addedByApp[h.AppSet], h)
		}
	}

	for appset, removed := range removedByApp {
		added := addedByApp[appset]
		byCluster(removed)
		byCluster(added)

		if len(removed) == 1 && len(added) == 1 {
			r, a := removed[0], added[0]
			res.Targeting = append(res.Targeting, Change{
				// The AppSet, not the head Application. The Application name
				// carries the cluster, so naming the head one describes a
				// departure by something that did not exist before the change
				// -- which reads as the gate being wrong about its own report.
				// The ApplicationSet is the identity that survives the move.
				Kind: "moved", AppSet: appset, App: appset,
				From: r.Cluster, To: a.Cluster,
				Detail: fmt.Sprintf("ApplicationSet no longer generates for %s; now generates for %s",
					r.Cluster, a.Cluster),
			})
			addedByApp[appset] = nil
			continue
		}

		// Ambiguous, or one-sided. Say what happened and let the reviewer draw
		// the line, rather than drawing an arbitrary one for them. Anything in
		// `added` is left for the loop below to report on its own terms.
		for _, r := range removed {
			res.Targeting = append(res.Targeting, Change{
				Kind: "removed", AppSet: appset, App: r.App, Cluster: r.Cluster,
				From: r.Describe(), Detail: "no longer generated for this cluster",
			})
		}
	}
	for appset, added := range addedByApp {
		for _, a := range added {
			c := Change{
				AppSet: appset, App: a.App, Cluster: a.Cluster, To: a.Describe(),
			}
			if baseAppSets[appset] {
				c.Kind = "added"
				c.Detail = "newly generated for this cluster -- this addon already existed and has gained a cluster"
				res.Targeting = append(res.Targeting, c)
			} else {
				c.Kind = "introduced"
				c.Detail = "new addon, first appearance"
				res.Introduced = append(res.Introduced, c)
			}
		}
	}

	for k, h := range headByKey {
		b, ok := baseByKey[k]
		if !ok {
			continue
		}
		switch {
		case b.SourceType != h.SourceType:
			res.Other = append(res.Other, Change{
				Kind: "source-type", Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Describe(), To: h.Describe(),
				Detail: "the kind of source changed",
			})
		case b.Chart != h.Chart || b.ChartRepo != h.ChartRepo || b.Path != h.Path:
			res.Other = append(res.Other, Change{
				Kind: "source", Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Describe(), To: h.Describe(),
				Detail: "the source itself changed, not just its version",
			})
		case b.Project != h.Project:
			res.Other = append(res.Other, Change{
				Kind: "project", Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Project, To: h.Project, Detail: "ArgoCD project changed",
			})
		case b.Namespace != h.Namespace:
			res.Other = append(res.Other, Change{
				Kind: "namespace", Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Namespace, To: h.Namespace, Detail: "destination namespace changed",
			})
		case b.Version != h.Version:
			res.Versions = append(res.Versions, Change{
				Kind: "version", Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Version, To: h.Version,
			})
		}
	}

	res.Objects = diffObjects(base.Objects, head.Objects)
	markMigrationConsistent(res.Objects)

	sortChanges(res.Targeting)
	sortChanges(res.Introduced)
	sortChanges(res.Versions)
	sortChanges(res.Other)

	seen := map[string]bool{}
	for _, w := range append(append([]string{}, base.Warnings...), head.Warnings...) {
		if !seen[w] {
			seen[w] = true
			res.Warnings = append(res.Warnings, w)
		}
	}
	return res
}

// byCluster makes an order out of one that came from a Go map. Without it the
// same two rows can be reported in either order, and a diff that describes
// itself differently on identical input is a diff nobody can diff.
func byCluster(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Cluster != rows[j].Cluster {
			return rows[i].Cluster < rows[j].Cluster
		}
		return rows[i].App < rows[j].App
	})
}

// writeFields renders one object's changed leaves, folded.
func writeFields(w io.Writer, o ObjectChange) {
	if len(o.Fields) == 0 {
		return
	}
	label := fmt.Sprintf("%d field", len(o.Fields))
	if len(o.Fields) != 1 {
		label += "s"
	}
	if o.Truncated > 0 {
		label += fmt.Sprintf(" (+%d more)", o.Truncated)
	}
	fmt.Fprintf(w, "  <details><summary>%s</summary>\n\n", label)
	for _, f := range o.Fields {
		switch {
		case f.From == "":
			fmt.Fprintf(w, "  - `%s`: set to `%s`\n", f.Path, f.To)
		case f.To == "":
			fmt.Fprintf(w, "  - `%s`: removed (was `%s`)\n", f.Path, f.From)
		default:
			fmt.Fprintf(w, "  - `%s`: `%s` → `%s`\n", f.Path, f.From, f.To)
		}
	}
	fmt.Fprintf(w, "\n  </details>\n")
}

func sortChanges(c []Change) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Cluster != c[j].Cluster {
			return c[i].Cluster < c[j].Cluster
		}
		return c[i].App < c[j].App
	})
}

// ReportMarker leads every report, and it is load-bearing rather than
// decorative. A triage agent finds the gate's verdict by looking for this
// string among the pull request's comments, and an adapter that publishes the
// report without it has published something nobody can find.
//
// It lives here, in the binary, for one reason: every CI adapter would
// otherwise have to remember an undocumented magic string, and three of the
// four did not. Emitting it means any adapter that posts the report verbatim
// is correct by construction. It renders as nothing in every markdown surface,
// including a CI job summary.
const ReportMarker = "<!-- gitops-gate -->"

// Verdict is the report's own answer, in one line, so a reader knows what they
// are looking at before they read anything else.
//
// This exists because a report that merely LISTS findings reads the same when
// it is blocking and when it is not. Two of them on one pull request -- a red
// one and the green one after a repair -- were indistinguishable at a glance,
// and the failed pass looked like a duplicate of the pass that succeeded
// rather than the thing that had to be fixed.
//
// The wording deliberately avoids the parser's heading strings, which live in
// the migrate package and are matched with strings.Contains: a headline that
// happened to contain "**API version changed**" would make the agent believe
// there was an unrepairable blocker in every report that mentioned one.
// Blockers counts the reasons this result is blocking. The type lives in the
// migrate package with the rest of the report's format, so that the writer and
// the reader cannot drift.
func (d *DiffResult) Blockers() migrate.Blockers {
	var b migrate.Blockers
	b.Targeting = len(d.Targeting)
	b.Source = len(d.Other)
	for _, o := range d.Objects {
		switch {
		case o.Kind == "apiVersion" && !o.PartOfMigration:
			b.APIVersion++
		case o.Kind == "crdVersionRemoved" && !o.ConsumersKnown:
			b.Unscanned++
		case o.Kind == "crdVersionRemoved" && len(o.ConsumerFiles) > 0:
			b.Consumers += len(o.ConsumerFiles)
		case o.Kind == "valuesKeyDropped":
			b.ValuesDropped += len(o.Keys)
		}
	}
	return b
}

func (d *DiffResult) Verdict() (blocking bool, headline string) {
	bl := d.Blockers()
	var why []string
	if n := bl.Targeting; n > 0 {
		why = append(why, fmt.Sprintf("%s now generated for a different set of clusters", plural(n, "Application")))
	}
	if n := bl.Source; n > 0 {
		why = append(why, fmt.Sprintf("%s moved", plural(n, "source")))
	}
	apiMoved, consumers, unscanned := bl.APIVersion, bl.Consumers, bl.Unscanned
	if apiMoved > 0 {
		why = append(why, fmt.Sprintf("%s whose own apiVersion moved", plural(apiMoved, "object")))
	}
	if consumers > 0 {
		why = append(why, fmt.Sprintf("%s still declaring a dropped API version", plural(consumers, "manifest")))
	}
	if unscanned > 0 {
		why = append(why, fmt.Sprintf("%s whose consumers could not be counted", plural(unscanned, "definition")))
	}
	if n := bl.ValuesDropped; n > 0 {
		why = append(why, fmt.Sprintf("%s this bump stops reading", plural(n, "setting")))
	}
	if len(why) == 0 {
		// Not blocking. Say what DID change, because "nothing blocking" and
		// "nothing changed" are different answers and only one of them means
		// the reader can stop reading.
		switch {
		case len(d.Versions) > 0:
			return false, fmt.Sprintf("No blocking findings — %s changed, nothing else",
				plural(len(d.Versions), "version"))
		case len(d.Introduced) > 0:
			return false, fmt.Sprintf("No blocking findings — %s, first appearance",
				plural(len(d.Introduced), "new Application"))
		case len(d.Objects) > 0:
			return false, fmt.Sprintf("No blocking findings — %s changed",
				plural(len(d.Objects), "rendered object"))
		default:
			return false, "No change to what gets deployed"
		}
	}
	return true, "Blocking — " + join(why)
}

// plural is "1 thing" / "3 things", because "1 manifest(s)" in a headline
// reads like a machine wrote it and nobody proof-read it.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func join(parts []string) string {
	switch len(parts) {
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// Report writes a markdown summary suitable for a pull-request comment or a CI
// job summary.
func (d *DiffResult) Report(w io.Writer) {
	fmt.Fprintf(w, "%s\n", ReportMarker)
	blocking, headline := d.Verdict()
	// The breakdown, machine-readable, for the same reason ReportMarker is
	// emitted here rather than remembered by each adapter: an agent reading
	// this report should not have to infer WHY it is red from prose that was
	// written for a person. Every adapter that posts the report verbatim
	// carries it, so the CI path gets it for free.
	b := d.Blockers()
	fmt.Fprintf(w, "%stargeting=%d source=%d apiVersion=%d consumers=%d unscanned=%d valuesDropped=%d -->\n",
		migrate.BlockersMarker, b.Targeting, b.Source, b.APIVersion, b.Consumers, b.Unscanned, b.ValuesDropped)
	mark := "✅"
	if blocking {
		mark = "🔴"
	}
	fmt.Fprintf(w, "## %s %s\n\n", mark, headline)
	if len(d.Targeting) > 0 {
		// The section headings the agent keys on come from the migrate
		// package, so both sides of that contract read the same bytes by
		// construction -- the ReportMarker lesson, applied before it is
		// re-learned.
		fmt.Fprintf(w, "%s\n\n", migrate.HeadingTargeting)
		fmt.Fprintf(w, "These Applications are generated for a different set of clusters than before. ")
		fmt.Fprintf(w, "A values-layer edit can do this without the text diff showing it.\n\n")
		fmt.Fprintf(w, "| Application | Change |\n|---|---|\n")
		for _, c := range d.Targeting {
			fmt.Fprintf(w, "| `%s` | %s |\n", c.App, c.Detail)
		}
		fmt.Fprintln(w)
	}
	if len(d.Other) > 0 {
		fmt.Fprintf(w, "%s\n\n| Application | Cluster | From | To |\n|---|---|---|---|\n", migrate.HeadingSource)
		for _, c := range d.Other {
			fmt.Fprintf(w, "| `%s` | %s | `%s` | `%s` |\n", c.App, c.Cluster, c.From, c.To)
		}
		fmt.Fprintln(w)
	}
	if len(d.Introduced) > 0 {
		fmt.Fprintf(w, "### New addons\n\n")
		fmt.Fprintf(w, "First appearance, so nothing changed underneath them. Listed for review, not blocking.\n\n")
		fmt.Fprintf(w, "| Application | Cluster | Source |\n|---|---|---|\n")
		for _, c := range d.Introduced {
			fmt.Fprintf(w, "| `%s` | %s | `%s` |\n", c.App, c.Cluster, c.To)
		}
		fmt.Fprintln(w)
	}
	if len(d.Objects) > 0 {
		var api, crd, vdrop, added, removed, changed []ObjectChange
		for _, o := range d.Objects {
			switch o.Kind {
			case "apiVersion":
				api = append(api, o)
			case "crdVersionRemoved":
				crd = append(crd, o)
			case "valuesKeyDropped":
				vdrop = append(vdrop, o)
			case "added":
				added = append(added, o)
			case "removed":
				removed = append(removed, o)
			default:
				changed = append(changed, o)
			}
		}
		if len(vdrop) > 0 {
			// FIRST, deliberately. It is the finding with no other symptom:
			// the render is identical, the values file did not change, and
			// helm does not complain. If it is below three tables of resource
			// diffs, it is below the fold.
			fmt.Fprintf(w, "### Settings this bump stops reading\n\n")
			fmt.Fprintf(w, "The chart no longer declares these values, and helm ignores a value it does not "+
				"know rather than failing on it. Each one silently stops applying, and the render looks "+
				"identical either way.\n\n")
			for _, o := range vdrop {
				fmt.Fprintf(w, "- `%s`", o.Object)
				if o.Cluster != "" {
					fmt.Fprintf(w, " on %s", o.Cluster)
				}
				fmt.Fprintf(w, " — `%s` → `%s`, %s no longer read:\n", o.From, o.To, plural(len(o.Keys), "setting"))
				shown := o.Keys
				if len(shown) > maxDroppedListed {
					shown = shown[:maxDroppedListed]
				}
				for _, k := range shown {
					fmt.Fprintf(w, "  - `%s`\n", k)
				}
				if n := len(o.Keys) - len(shown); n > 0 {
					fmt.Fprintf(w, "  - …and %d more\n", n)
				}
			}
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "### Resources\n\n")
		if len(api) > 0 {
			fmt.Fprintf(w, "%s — this is a migration, not a bump.\n\n", migrate.HeadingAPIVersion)
			for _, o := range api {
				if o.PartOfMigration {
					fmt.Fprintf(w, "- `%s`: `%s` → `%s` — the move the finding below requires; the repair, not a new migration, so not blocking\n",
						o.Object, o.From, o.To)
					continue
				}
				fmt.Fprintf(w, "- `%s`: `%s` → `%s`\n", o.Object, o.From, o.To)
			}
			fmt.Fprintln(w)
		}
		if len(crd) > 0 {
			fmt.Fprintf(w, "**A CustomResourceDefinition stopped serving a version** — anything still declaring it breaks on apply.\n\n")
			for _, o := range crd {
				// The line is the repair contract: the agent parses the kind
				// and the destination version back out of it, so it is
				// rendered by the shared package rather than by one more
				// format string that could drift.
				fmt.Fprintf(w, "%s\n", migrate.Line(o.Object, o.From, o.Resource, o.To))
				blocking, clear := "blocking until they move", "no manifest in this repository declares a dropped version, so this alone does not block"
				if o.To == "" {
					blocking = "blocking until they are removed or replaced"
					clear = "no manifest in this repository uses this API, so from inspection the removal looks safe and does not block"
				}
				switch {
				case o.ConsumersKnown && len(o.ConsumerFiles) > 0:
					fmt.Fprintf(w, "  - **%d manifest(s) in this repository still declare a dropped version** — %s:\n", len(o.ConsumerFiles), blocking)
					for i, f := range o.ConsumerFiles {
						if i == 12 {
							fmt.Fprintf(w, "    - …and %d more\n", len(o.ConsumerFiles)-12)
							break
						}
						fmt.Fprintf(w, "    - `%s`\n", f)
					}
				case o.ConsumersKnown:
					fmt.Fprintf(w, "  - %s\n", clear)
				}
			}
			fmt.Fprintln(w)
		}
		for _, g := range []struct {
			label string
			items []ObjectChange
		}{{"Added", added}, {"Removed", removed}, {"Changed", changed}} {
			if len(g.items) == 0 {
				continue
			}
			fmt.Fprintf(w, "**%s (%d)**\n\n", g.label, len(g.items))
			for i, o := range g.items {
				if i == 12 {
					fmt.Fprintf(w, "- …and %d more\n", len(g.items)-12)
					break
				}
				fmt.Fprintf(w, "- `%s`\n", o.Object)
				if o.Note != "" {
					fmt.Fprintf(w, "  - %s\n", o.Note)
				}
				// The whole point of rendering both versions is knowing WHICH
				// fields moved. Reporting only that an object "changed" hands
				// the reader the same non-answer the version number already
				// gave, and asks for human eyes while withholding what those
				// eyes need. Folded, so twelve of these stay readable.
				writeFields(w, o)
			}
			fmt.Fprintln(w)
		}
	}
	if len(d.Versions) > 0 {
		fmt.Fprintf(w, "### Versions\n\n| Application | Cluster | From | To |\n|---|---|---|---|\n")
		for _, c := range d.Versions {
			fmt.Fprintf(w, "| `%s` | %s | `%s` | `%s` |\n", c.App, c.Cluster, c.From, c.To)
		}
		fmt.Fprintln(w)
	}
	if len(d.Targeting) == 0 && len(d.Other) == 0 && len(d.Versions) == 0 &&
		len(d.Introduced) == 0 && len(d.Objects) == 0 {
		fmt.Fprintf(w, "No change to what gets deployed.\n\n")
	}
	if len(d.Warnings) > 0 {
		fmt.Fprintf(w, "### Not covered\n\n")
		fmt.Fprintf(w, "The gate could not expand the following, so the Applications they generate are **not** checked:\n\n")
		for _, warn := range d.Warnings {
			fmt.Fprintf(w, "- %s\n", strings.TrimSpace(warn))
		}
		fmt.Fprintln(w)
	}
}
