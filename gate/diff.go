package gate

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// ChangeKind is what happened to an Application's targeting or its source.
//
// A named type rather than a string with a comment listing the values, because
// the comment named four of these while the code assigned all nine, and the
// three it omitted (project, namespace, source-type) are the ones a reader
// would go looking for the meaning of.
type ChangeKind string

const (
	// Targeting.
	ChangeAdded      ChangeKind = "added"
	ChangeRemoved    ChangeKind = "removed"
	ChangeMoved      ChangeKind = "moved"
	ChangeIntroduced ChangeKind = "introduced"
	// The source itself.
	ChangeSource     ChangeKind = "source"
	ChangeSourceType ChangeKind = "source-type"
	ChangeProject    ChangeKind = "project"
	ChangeNamespace  ChangeKind = "namespace"
	// Reported, never blocking.
	ChangeVersion ChangeKind = "version"
)

type Change struct {
	Kind    ChangeKind `json:"kind"`
	Cluster string     `json:"cluster"`
	App     string     `json:"app"`
	AppSet  string     `json:"appset,omitempty"`
	From    string     `json:"from,omitempty"`
	To      string     `json:"to,omitempty"`
	Detail  string     `json:"detail,omitempty"`
}

type DiffResult struct {
	// Targeting changes block the merge: an Application appearing on or
	// vanishing from a cluster is the failure mode that reading a values diff
	// does not reveal.
	Targeting []Change `json:"targeting"`
	// Introduced covers whole ApplicationSets that did not exist before, and
	// does not block. Adding an addon is a deliberate act by the author of the
	// pull request; the dangerous case is an addon that already existed
	// quietly changing which clusters it reaches. Blocking on both would make
	// every new-addon PR red for a reason nobody needs to investigate, and a
	// check that is routinely overridden stops being a check.
	Introduced []Change `json:"introduced"`
	// Versions are reported, not blocked; a version bump is the point.
	Versions []Change `json:"versions"`
	// Other covers a source moving between chart and path, or a project or
	// namespace change: not a targeting change, but not routine either.
	Other []Change `json:"other"`

	// Objects are resource-level differences, present only when the
	// repository renders manifests into git. This is the evidence a
	// reviewer, or a triage agent, needs: a version number says a
	// chart moved, whereas "removed two containers, added a DaemonSet and
	// four CRDs" says what will happen.
	Objects  []ObjectChange `json:"objects,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`

	// Suppressed is checks that did not run because the configuration told
	// them not to, each with the reason.
	//
	// Separate from Warnings, which mean "the gate tried and could not". This
	// means "the gate was told not to", and by a file read from the pull
	// request's own head, so a change can switch a check off, and the report
	// that results has to say which one, or the project's own "cannot act
	// without saying so" rule holds everywhere except where it matters most.
	Suppressed []string `json:"suppressed,omitempty"`

	// Scope is how the set of things rendered was arrived at: how many
	// sources were derived from how many live Applications, and any root
	// rendered from ArgoCD's applied spec rather than from this repository.
	//
	// Not folded into Suppressed, which means "the gate was told not to
	// look". This means "here is what the gate looked at", and it is on every
	// report rather than only on the interesting ones, because scope now
	// depends on cluster state: an ArgoCD serving a smaller fleet than
	// yesterday produces a smaller scope, a correct "no change" within it, and
	// no other symptom. A reader who can see the scope can notice that; a
	// reader who cannot, cannot.
	Scope []string `json:"scope,omitempty"`

	// Unrenderable is the repair contract behind the renderFailed findings in
	// Objects: the Applications whose chart will not render at the new
	// version, each with the head Row a repair has to pull and render.
	//
	// Deliberately not in the report. The report is prose and half of it is
	// strings a chart chose; this is a value the agent reads in process, from
	// the same run that computed it, which is what ADR 0008 bought by moving
	// the gate in-cluster. Nothing here has to survive a round trip through
	// markdown, so nothing here can be spelled by a chart.
	Unrenderable []Unrenderable `json:"unrenderable,omitempty"`

	// SchemaFailures is manifests the target cluster's schemas reject, set by
	// the caller that ran validation.
	//
	// It lives on the result rather than beside it because the headline, the
	// machine-readable marker and the commit status are all derived from this
	// struct. Kept outside, a run blocked only by schema validation published
	// a report headlined "No blocking findings" next to a red cross, the
	// report and the status disagreeing about the same run.
	SchemaFailures int `json:"schemaFailures,omitempty"`
}

func (d *DiffResult) Blocking() bool {
	if len(d.Targeting) > 0 || len(d.Other) > 0 || d.SchemaFailures > 0 {
		return true
	}
	// An API version moving under an existing resource is a migration, and
	// migrations are the class of change that renders perfectly and breaks at
	// runtime. Objects appearing or changing are reported but not blocked;
	// that is what a version bump legitimately does.
	for _, o := range d.Objects {
		// Both are migrations. The first is an object whose own apiVersion
		// moved; the second is a CustomResourceDefinition that stopped serving
		// one, which the first cannot see because the CRD object itself is
		// apiextensions.k8s.io/v1 on both sides.
		if o.Kind == ObjectAPIVersionMoved && !o.PartOfMigration {
			return true
		}
		// A dropped served version blocks exactly while manifests in this
		// repository still declare it; they are what breaks at apply. A
		// finding whose consumers were counted at zero is reported and does
		// not block, which is what lets a repair that moves every consumer
		// turn this red green on the re-run. Not scanned means not counted:
		// "we could not look" blocks, for the same reason a bodiless CRD is
		// reported as changed rather than claimed safe.
		if o.Kind == ObjectCRDVersionRemoved && (!o.ConsumersKnown || len(o.ConsumerFiles) > 0) {
			return true
		}
		// A setting the new chart no longer reads. It renders green by
		// construction, helm ignores an unknown value, so if this does not
		// block, nothing anywhere will ever mention it.
		if o.Kind == ObjectValuesKeyDropped && len(o.Keys) > 0 {
			return true
		}
		// The chart will not render at the version this change moves to.
		// Every other finding here is "what merging this does"; this one is
		// "merging this leaves an Application that cannot sync", and it is
		// the only one where the gate has no diff to show because there was
		// nothing to diff.
		if o.Kind == ObjectRenderFailed {
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
	// event, not two, report it as a move so the reviewer sees the shape of
	// what happened rather than two unrelated-looking lines.
	//
	// Only when there is one candidate on each side. Pairing two departures
	// with two arrivals is a guess: nothing in the render says which arrival
	// corresponds to which departure, and asserting one anyway produced a
	// sentence the reader had no way to check. Worse, both slices were built
	// by ranging a map, so the guess was not even stable; identical input
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
		sortRows(removed)
		sortRows(added)

		if len(removed) == 1 && len(added) == 1 {
			r, a := removed[0], added[0]
			res.Targeting = append(res.Targeting, Change{
				// The AppSet, not the head Application. The Application name
				// carries the cluster, so naming the head one describes a
				// departure by something that did not exist before the change,
				// which reads as the gate being wrong about its own report.
				// The ApplicationSet is the identity that survives the move.
				Kind: ChangeMoved, AppSet: appset, App: appset,
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
				Kind: ChangeRemoved, AppSet: appset, App: r.App, Cluster: r.Cluster,
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
				c.Kind = ChangeAdded
				c.Detail = "newly generated for this cluster -- this addon already existed and has gained a cluster"
				res.Targeting = append(res.Targeting, c)
			} else {
				c.Kind = ChangeIntroduced
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
				Kind: ChangeSourceType, Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Describe(), To: h.Describe(),
				Detail: "the kind of source changed",
			})
		case b.Chart != h.Chart || b.ChartRepo != h.ChartRepo || b.Path != h.Path:
			res.Other = append(res.Other, Change{
				Kind: ChangeSource, Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Describe(), To: h.Describe(),
				Detail: "the source itself changed, not just its version",
			})
		case b.Project != h.Project:
			res.Other = append(res.Other, Change{
				Kind: ChangeProject, Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Project, To: h.Project, Detail: "ArgoCD project changed",
			})
		case b.Namespace != h.Namespace:
			res.Other = append(res.Other, Change{
				Kind: ChangeNamespace, Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Namespace, To: h.Namespace, Detail: "destination namespace changed",
			})
		case b.Version != h.Version:
			res.Versions = append(res.Versions, Change{
				Kind: ChangeVersion, Cluster: h.Cluster, App: h.App, AppSet: h.AppSet,
				From: b.Version, To: h.Version,
			})
		}
	}

	res.Objects = diffObjects(base.Objects, head.Objects, head.ValuesLeaves)
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

// sortRows makes an order out of one that came from a Go map. Without it the
// same two rows can be reported in either order, and a diff that describes
// itself differently on identical input is a diff nobody can diff.
//
// A verb, like sortChanges below: `byCluster` read as a grouping that returns
// something, and it sorts its argument in place.
func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Cluster != rows[j].Cluster {
			return rows[i].Cluster < rows[j].Cluster
		}
		return rows[i].App < rows[j].App
	})
}

// writeFields renders one object's changed leaves.
//
// The fields a reader chose come first and unfolded; the chart's own stay
// behind a fold whose summary says whether opening it can matter. Read back
// from a live report: ten lines, one signal, and the reader's question --
// does this touch anything I set? -- answerable only by reading all ten. The
// re-run after a repair rewrites the same comment, so every line saved is
// saved on every rendering of it.
//
// Folded, not filtered. The marking is a heuristic, the report is also the
// evidence the model's prompt carries, and the one line worth reading on the
// bump that prompted this was not values-linked -- a filter would have
// hidden it, a fold prices it at one click.
func writeFields(w io.Writer, o ObjectChange) {
	if len(o.Fields) == 0 {
		return
	}
	var yours, chartOwn []FieldChange
	for _, f := range o.Fields {
		if f.SetHere {
			yours = append(yours, f)
		} else {
			chartOwn = append(chartOwn, f)
		}
	}

	if len(yours) > 0 {
		fmt.Fprintf(w, "  Values this repository sets:\n\n")
		for _, f := range yours {
			fieldLine(w, f)
		}
		fmt.Fprintln(w)
		if len(chartOwn) == 0 {
			if o.Truncated > 0 {
				fmt.Fprintf(w, "  …and %d more field(s) beyond the report's cap\n", o.Truncated)
			}
			return
		}
	}

	label := fmt.Sprintf("%d field", len(chartOwn))
	if len(chartOwn) != 1 {
		label += "s"
	}
	switch {
	case len(yours) > 0:
		label = fmt.Sprintf("%d more, the chart's own", len(chartOwn))
	case o.ValuesChecked:
		// The line that lets a reader stop here. "No field marked" from a
		// diff that never looked would be the "we could not look" confusion
		// all over again, which is what ValuesChecked exists to rule out.
		label += ", none of them a value this repository sets"
	}
	if o.Truncated > 0 {
		label += fmt.Sprintf(" (+%d more)", o.Truncated)
	}
	fmt.Fprintf(w, "  <details><summary>%s</summary>\n\n", label)
	for _, f := range chartOwn {
		fieldLine(w, f)
	}
	fmt.Fprintf(w, "\n  </details>\n")
}

// fieldLine renders one changed leaf as a report bullet. The `[+]`/`[-]`
// suffix is the aligned-list form: the finding is membership, not position.
func fieldLine(w io.Writer, f FieldChange) {
	switch {
	case strings.HasSuffix(f.Path, "[+]"):
		fmt.Fprintf(w, "  - `%s`: gained `%s`\n", inline(strings.TrimSuffix(f.Path, "[+]")), inline(f.To))
	case strings.HasSuffix(f.Path, "[-]"):
		fmt.Fprintf(w, "  - `%s`: lost `%s`\n", inline(strings.TrimSuffix(f.Path, "[-]")), inline(f.From))
	case f.From == "":
		fmt.Fprintf(w, "  - `%s`: set to `%s`\n", inline(f.Path), inline(f.To))
	case f.To == "":
		fmt.Fprintf(w, "  - `%s`: removed (was `%s`)\n", inline(f.Path), inline(f.From))
	default:
		fmt.Fprintf(w, "  - `%s`: `%s` → `%s`\n", inline(f.Path), inline(f.From), inline(f.To))
	}
}

func sortChanges(c []Change) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Cluster != c[j].Cluster {
			return c[i].Cluster < c[j].Cluster
		}
		return c[i].App < c[j].App
	})
}

// maxListed bounds any one list in the report.
//
// Twelve for the same reason maxDroppedListed is: the finding is "these
// changed", and forty entries make that point no better than a dozen do, while
// a comment nobody opens makes it not at all. Named because it appeared as a
// bare 12 twice in this file, beside a maxDroppedListed the same size, so a
// reader had to check whether the three were the same decision or a
// coincidence.
const maxListed = maxDroppedListed

// ReportMarker leads every report, and it is load-bearing rather than
// decorative. It is how the gate finds the report it already published on a
// pull request and rewrites that one instead of adding another, and a report
// published without it is one nobody can find again.
//
// It lives here, in the binary, rather than in whatever publishes the report:
// a magic string every publisher has to remember is one most of them will get
// wrong. Emitting it means anything that posts the report verbatim is correct
// by construction. It renders as nothing in every markdown surface.
const ReportMarker = "<!-- gitops-gate -->"

// NothingChanged is the report's answer when the render is identical on both
// sides, not "nothing blocking", but nothing at all.
//
// Exported for the same reason ReportMarker is. The agent decides whether to
// spend a model call explaining a change by looking for this sentence, and it
// used to look for its own copy of it: a private literal in package main, one
// wording change away from an agent that explains every unchanged render or
// explains none of them, with nothing failing to say so.
const NothingChanged = "No change to what gets deployed"

// SaysNothingChanged reports whether a published gate report is the
// nothing-changed one. The CI path only ever has the rendered text, so the
// question has to be answerable from it.
func SaysNothingChanged(report string) bool {
	return strings.Contains(report, NothingChanged)
}

// Blockers counts the reasons this result is blocking. The type lives in the
// migrate package with the rest of the report's format, so that the writer and
// the reader cannot drift.
func (d *DiffResult) Blockers() migrate.Blockers {
	var b migrate.Blockers
	b.Targeting = len(d.Targeting)
	b.Source = len(d.Other)
	for _, o := range d.Objects {
		switch {
		case o.Kind == ObjectAPIVersionMoved && !o.PartOfMigration:
			b.APIVersion++
		case o.Kind == ObjectCRDVersionRemoved && !o.ConsumersKnown:
			b.Unscanned++
		case o.Kind == ObjectCRDVersionRemoved && len(o.ConsumerFiles) > 0:
			b.Consumers += len(o.ConsumerFiles)
		case o.Kind == ObjectValuesKeyDropped:
			b.ValuesDropped += len(o.Keys)
		case o.Kind == ObjectRenderFailed:
			b.Unrenderable++
		}
	}
	b.Schema = d.SchemaFailures
	return b
}

// droppedContract is every dropped-version finding a repair can act on, in the
// form the agent executes: the kind consumers declare, the versions that are
// gone, and the one they must move to.
//
// Built from the findings themselves through droppedFromChange, the same
// function AnnotateConsumers scans with, so what the report instructs and what
// the gate counted are one derivation rather than two.
//
// A definition removed outright is left out. It has no surviving version, so
// there is nowhere to move and no rewrite to perform; the prose says so, and
// the contract deliberately cannot express it.
func (d *DiffResult) droppedContract() []migrate.Dropped {
	var out []migrate.Dropped
	for _, o := range d.Objects {
		if o.Kind != ObjectCRDVersionRemoved {
			continue
		}
		if dr, ok := droppedFromChange(o); ok && dr.Target != "" {
			out = append(out, dr)
		}
	}
	return out
}

// Verdict is the report's own answer, in one line, so a reader knows what they
// are looking at before they read anything else.
//
// This exists because a report that only lists findings reads the same when
// it is blocking and when it is not. Two of them on one pull request, a red
// one and the green one after a repair, were indistinguishable at a glance,
// and the failed pass looked like a duplicate of the pass that succeeded
// rather than the thing that had to be fixed.
//
// The wording deliberately avoids the parser's heading strings, which live in
// the migrate package and are matched with strings.Contains: a headline that
// happened to contain "**API version changed**" would make the agent believe
// there was an unrepairable blocker in every report that mentioned one.
func (d *DiffResult) Verdict() (blocking bool, headline string) {
	bl := d.Blockers()
	var why []string
	// First, because it is the only finding that says the Application does
	// not work at all. Everything below it describes a change; this describes
	// an absence of one, and a reader who stops after the first clause has
	// still read the thing that matters most.
	if n := bl.Unrenderable; n > 0 {
		why = append(why, fmt.Sprintf("%s whose chart will not render at the new version",
			plural(n, "Application")))
	}
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
	if n := bl.Schema; n > 0 {
		why = append(why, fmt.Sprintf("%s the target schemas reject", plural(n, "manifest")))
	}
	if len(why) == 0 {
		// Not blocking. Say what did change, because "nothing blocking" and
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
			return false, NothingChanged
		}
	}
	return true, "Blocking — " + joinAnd(why)
}

// plural is "1 thing" / "3 things", because "1 manifest(s)" in a headline
// reads like a machine wrote it and nobody proof-read it.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// inline neutralises a value the gate did not write before it goes into the
// report. Object names, field paths, field values and cluster names are all
// chosen by whatever chart or repository produced them, and the report is
// markdown: a backtick closes the code span the value sits in, and a newline
// starts a line of its own. A name holding either writes report structure, and
// a name that writes structure can write a finding, including one the agent
// reads back as an instruction to rewrite manifests.
//
// Escaped rather than dropped, and visibly. `\x60` is a name doing something
// strange, which is worth a reader's eyes; a silently trimmed name is one
// nobody can look up in the chart that produced it.
func inline(s string) string {
	if strings.IndexFunc(s, unwritable) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unwritable(r) {
			fmt.Fprintf(&b, "\\x%02x", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// unwritable is what the report's own syntax uses: the backtick that ends a
// code span, and every control character, the newline and the carriage return
// among them, that ends a line or a table row.
func unwritable(r rune) bool { return r == '`' || r < 0x20 || r == 0x7f }

// maxFencedLines bounds one fenced block. helm's own errors run to a handful
// of lines; a chart is free to make one run to thousands, and a pull-request
// comment has a size limit that a single finding must not be able to spend.
const maxFencedLines = 20

// fenced neutralises a multi-line value the gate did not write, for a fenced
// code block.
//
// Same argument as inline, with one difference that is the whole reason it is
// a second function: inside a fence the newline is the block's own structure
// rather than something a value can forge with, so it survives, and helm's
// error stays the shape helm printed it in. The backtick does not, and that is
// what keeps a value from closing the fence and writing report structure of
// its own -- a finding, or an instruction the agent reads back.
func fenced(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	over := 0
	if len(lines) > maxFencedLines {
		over, lines = len(lines)-maxFencedLines, lines[:maxFencedLines]
	}
	for i, line := range lines {
		lines[i] = inline(line)
	}
	if over > 0 {
		lines = append(lines, fmt.Sprintf("…and %d more line(s)", over))
	}
	return strings.Join(lines, "\n")
}

// joinAnd is an English list. Same name and shape as pipeline.joinAnd, two
// packages that never import each other, each rendering findings for a human,
// and a shared helper package for one function would cost more than the
// duplicate does.
func joinAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
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
//
// Write errors are not checked here and it has no error to return, which is
// deliberate: both callers render into a strings.Builder, whose Write cannot
// fail, and then perform one real write they do check, gateservice into the
// comment body, the CLI through writeReport. Checking forty individual writes
// into a buffer would be forty branches that cannot be taken, in place of the
// one that can. A third caller streaming this straight at a file would be
// giving up that guarantee and should buffer like the other two.
func (d *DiffResult) Report(w io.Writer) {
	fmt.Fprintf(w, "%s\n", ReportMarker)
	blocking, headline := d.Verdict()
	// The breakdown, machine-readable, for the same reason ReportMarker is
	// emitted here rather than remembered by each adapter: an agent reading
	// this report should not have to infer why it is red from prose that was
	// written for a person. Every adapter that posts the report verbatim
	// carries it, so the CI path gets it for free.
	b := d.Blockers()
	fmt.Fprintf(w, "%stargeting=%d source=%d apiVersion=%d consumers=%d unscanned=%d unrenderable=%d valuesDropped=%d schema=%d -->\n",
		migrate.BlockersMarker, b.Targeting, b.Source, b.APIVersion, b.Consumers, b.Unscanned,
		b.Unrenderable, b.ValuesDropped, b.Schema)
	// The repair contract, from the structured findings rather than from the
	// prose that describes them. The prose cannot carry it: half of a bullet
	// is a rendered object's name, the chart chooses that name, and a name
	// carrying a backtick or a newline writes a whole finding of its own. What
	// it writes is an instruction to rewrite manifests to a version the chart
	// picked. Written even when there is nothing to say, because its presence
	// is what tells the reader this gate does not keep the contract in prose.
	fmt.Fprint(w, migrate.DroppedBlock(d.droppedContract()))
	mark := "✅"
	if blocking {
		mark = "🔴"
	}
	fmt.Fprintf(w, "## %s %s\n\n", mark, headline)
	if len(d.Targeting) > 0 {
		// The section headings the agent keys on come from the migrate
		// package, so both sides of that contract read the same bytes by
		// construction, the ReportMarker lesson, applied before it is
		// re-learned.
		fmt.Fprintf(w, "%s\n\n", migrate.HeadingTargeting)
		fmt.Fprintf(w, "These Applications are generated for a different set of clusters than before. ")
		fmt.Fprintf(w, "A values-layer edit can do this without the text diff showing it.\n\n")
		fmt.Fprintf(w, "| Application | Change |\n|---|---|\n")
		for _, c := range d.Targeting {
			fmt.Fprintf(w, "| `%s` | %s |\n", inline(c.App), inline(c.Detail))
		}
		fmt.Fprintln(w)
	}
	if len(d.Other) > 0 {
		fmt.Fprintf(w, "%s\n\n| Application | Cluster | From | To |\n|---|---|---|---|\n", migrate.HeadingSource)
		for _, c := range d.Other {
			fmt.Fprintf(w, "| `%s` | %s | `%s` | `%s` |\n", inline(c.App), inline(c.Cluster), inline(c.From), inline(c.To))
		}
		fmt.Fprintln(w)
	}
	if len(d.Introduced) > 0 {
		fmt.Fprintf(w, "### New addons\n\n")
		fmt.Fprintf(w, "First appearance, so nothing changed underneath them. Listed for review, not blocking.\n\n")
		fmt.Fprintf(w, "| Application | Cluster | Source |\n|---|---|---|\n")
		for _, c := range d.Introduced {
			fmt.Fprintf(w, "| `%s` | %s | `%s` |\n", inline(c.App), inline(c.Cluster), inline(c.To))
		}
		fmt.Fprintln(w)
	}
	if len(d.Objects) > 0 {
		var api, crd, vdrop, unrendered, added, removed, changed []ObjectChange
		for _, o := range d.Objects {
			// Written with the constants rather than their values. Six
			// literals repeating six exported names is six chances for a
			// renamed kind to land silently in `changed`, which is the one
			// bucket that accepts anything.
			switch o.Kind {
			case ObjectAPIVersionMoved:
				api = append(api, o)
			case ObjectCRDVersionRemoved:
				crd = append(crd, o)
			case ObjectValuesKeyDropped:
				vdrop = append(vdrop, o)
			case ObjectRenderFailed:
				unrendered = append(unrendered, o)
			case ObjectAdded:
				added = append(added, o)
			case ObjectRemoved:
				removed = append(removed, o)
			default:
				changed = append(changed, o)
			}
		}
		if len(unrendered) > 0 {
			// Above everything, including the settings drop. The rest of the
			// report describes what merging this would change; this says the
			// Application will not come up at all, and nothing below it is
			// worth reading first.
			fmt.Fprintf(w, "### The chart does not render at the new version\n\n")
			fmt.Fprintf(w, "`helm template` refuses these at the version this change moves them to, "+
				"with the values this repository sets. There are no resource changes listed for them "+
				"below, because nothing rendered to compare: the gate looked, and it does not work.\n\n")
			for _, o := range unrendered {
				fmt.Fprintf(w, "**`%s`", inline(o.Object))
				if o.Cluster != "" {
					fmt.Fprintf(w, " on %s", inline(o.Cluster))
				}
				fmt.Fprintf(w, " — `%s` → `%s`**\n\n", inline(o.From), inline(o.To))
				fmt.Fprintf(w, "```text\n%s\n```\n\n", fenced(o.Reason))
			}
		}
		if len(vdrop) > 0 {
			// Early, deliberately, and above every resource table. It is the
			// finding with no other symptom: the render is identical, the
			// values file did not change, and helm does not complain. Below
			// three tables of resource diffs it is below the fold. The only
			// section that goes higher is the one that says nothing rendered
			// at all, which is a symptom on its own.
			fmt.Fprintf(w, "### Settings this bump stops reading\n\n")
			fmt.Fprintf(w, "The chart no longer declares these values, and helm ignores a value it does not "+
				"know rather than failing on it. Each one silently stops applying, and the render looks "+
				"identical either way.\n\n")
			if len(unrendered) > 0 {
				// Said only when both sections are on the page, because
				// without the one above it the paragraph describes an
				// exception to a rule the reader has no example of. With it,
				// the previous paragraph is flatly contradicted by the error
				// printed a screen higher, and a report that argues with
				// itself is one nobody trusts the rest of.
				fmt.Fprintf(w, "Except where it did not: a chart strict enough to refuse a key it has "+
					"stopped declaring is listed above, and the two findings there are one fact seen "+
					"from two sides.\n\n")
			}
			for _, o := range vdrop {
				fmt.Fprintf(w, "- `%s`", inline(o.Object))
				if o.Cluster != "" {
					fmt.Fprintf(w, " on %s", inline(o.Cluster))
				}
				fmt.Fprintf(w, " — `%s` → `%s`, %s no longer read:\n", inline(o.From), inline(o.To), plural(len(o.Keys), "setting"))
				shown := o.Keys
				if len(shown) > maxDroppedListed {
					shown = shown[:maxDroppedListed]
				}
				for _, k := range shown {
					fmt.Fprintf(w, "  - `%s`\n", inline(k))
				}
				if n := len(o.Keys) - len(shown); n > 0 {
					fmt.Fprintf(w, "  - …and %d more\n", n)
				}
			}
			fmt.Fprintln(w)
		}
		// Guarded, because two of the buckets above are not resources. A
		// report whose only finding is a settings drop used to print this
		// heading over nothing at all.
		if len(api)+len(crd)+len(added)+len(removed)+len(changed) > 0 {
			fmt.Fprintf(w, "### Resources\n\n")
		}
		if len(api) > 0 {
			fmt.Fprintf(w, "%s — this is a migration, not a bump.\n\n", migrate.HeadingAPIVersion)
			for _, o := range api {
				if o.PartOfMigration {
					fmt.Fprintf(w, "- `%s`: `%s` → `%s` — the move the finding below requires; the repair, not a new migration, so not blocking\n",
						inline(o.Object), inline(o.From), inline(o.To))
					continue
				}
				fmt.Fprintf(w, "- `%s`: `%s` → `%s`\n", inline(o.Object), inline(o.From), inline(o.To))
			}
			fmt.Fprintln(w)
		}
		if len(crd) > 0 {
			fmt.Fprintf(w, "**A CustomResourceDefinition stopped serving a version** — anything still declaring it breaks on apply.\n\n")
			for _, o := range crd {
				// The finding a person reads. It says the same thing the
				// contract block above says, and it is still rendered by the
				// shared package rather than by one more format string that
				// could drift: the agent falls back to reading this shape
				// from a gate too old to have written a block.
				fmt.Fprintf(w, "%s\n", migrate.Line(inline(o.Object), inline(o.From), inline(o.Resource), inline(o.To)))
				blocking, clear := "blocking until they move", "no manifest in this repository declares a dropped version, so this alone does not block"
				if o.To == "" {
					blocking = "blocking until they are removed or replaced"
					clear = "no manifest in this repository uses this API, so from inspection the removal looks safe and does not block"
				}
				switch {
				case o.ConsumersKnown && len(o.ConsumerFiles) > 0:
					fmt.Fprintf(w, "  - **%d manifest(s) in this repository still declare a dropped version** — %s:\n", len(o.ConsumerFiles), blocking)
					for i, f := range o.ConsumerFiles {
						if i == maxListed {
							fmt.Fprintf(w, "    - …and %d more\n", len(o.ConsumerFiles)-maxListed)
							break
						}
						fmt.Fprintf(w, "    - `%s`\n", inline(f))
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
		}{{migrate.GroupAdded, added}, {migrate.GroupRemoved, removed}, {migrate.GroupChanged, changed}} {
			if len(g.items) == 0 {
				continue
			}
			// Written through migrate so the group a bullet sits in stays
			// readable: all three groups render an identical bullet, and the
			// only thing separating "this definition was added" from "this
			// definition is gone" is the heading above it.
			fmt.Fprintf(w, "%s\n\n", migrate.ObjectGroupHeading(g.label, len(g.items)))
			for i, o := range g.items {
				if i == maxListed {
					fmt.Fprintf(w, "- …and %d more\n", len(g.items)-maxListed)
					break
				}
				fmt.Fprintf(w, "- `%s`\n", inline(o.Object))
				if o.Note != "" {
					fmt.Fprintf(w, "  - %s\n", inline(o.Note))
				}
				// The whole point of rendering both versions is knowing which
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
			fmt.Fprintf(w, "| `%s` | %s | `%s` | `%s` |\n", inline(c.App), inline(c.Cluster), inline(c.From), inline(c.To))
		}
		fmt.Fprintln(w)
	}
	if len(d.Targeting) == 0 && len(d.Other) == 0 && len(d.Versions) == 0 &&
		len(d.Introduced) == 0 && len(d.Objects) == 0 {
		fmt.Fprintf(w, "%s.\n\n", NothingChanged)
	}
	if len(d.Suppressed) > 0 {
		fmt.Fprintf(w, "### Turned off by this pull request's configuration\n\n")
		fmt.Fprintf(w, "The gate reads its configuration from the head revision, so a change can "+
			"switch a check off. These did not run:\n\n")
		for _, sup := range d.Suppressed {
			fmt.Fprintf(w, "- %s\n", inline(strings.TrimSpace(sup)))
		}
		fmt.Fprintln(w)
	}
	if len(d.Scope) > 0 {
		fmt.Fprintf(w, "### What was rendered\n\n")
		for _, s := range d.Scope {
			fmt.Fprintf(w, "- %s\n", inline(strings.TrimSpace(s)))
		}
		fmt.Fprintln(w)
	}
	if len(d.Warnings) > 0 {
		fmt.Fprintf(w, "### Not covered\n\n")
		fmt.Fprintf(w, "The gate could not expand the following, so the Applications they generate are **not** checked:\n\n")
		for _, warn := range d.Warnings {
			fmt.Fprintf(w, "- %s\n", inline(strings.TrimSpace(warn)))
		}
		fmt.Fprintln(w)
	}
}
