package gate

import (
	"fmt"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The verdict as a list rather than as a page.
//
// A DiffResult has always been able to render itself two ways: prose for a
// person, and a counted breakdown for the repair. Neither is what a program
// asking "why is this pull request blocked" can use. The prose is markdown
// written for somebody else and half of every line is a name a chart chose;
// the breakdown says `consumers=4` and cannot say which four.
//
// So this is the third rendering, and it is the one the other two are now
// derived from where they can be: one finding per reason the gate has an
// opinion, each carrying its own contribution to the breakdown. Blockers folds
// over it, which is what stops a count and the list behind it disagreeing
// about the same run.
//
// What is deliberately NOT here: added, removed and changed objects, version
// moves, and new addons. Those are what the change does, they are already in
// the report's tables, and none of them can ever block. A list that carried
// them would be a rendered diff wearing a findings list's name, and the first
// caller to iterate it looking for problems would find a thousand.

// FindingKind is the class of one gate finding, and the field a caller
// branches on.
//
// A named type over a string for the reason ObjectChangeKind is one: the
// values are a published vocabulary, and a comment listing them falls behind
// the code. These are the eight buckets migrate.Blockers counts, with the two
// dropped-version buckets folded into one kind -- consumers counted and
// consumers not counted are the same finding with different evidence, and
// which bucket it lands in is ConsumersScanned rather than a second name.
type FindingKind string

const (
	// FindingTargeting: an Application generated for a different set of
	// clusters than before.
	FindingTargeting FindingKind = "targeting"
	// FindingSource: the source itself moved -- chart to path, a different
	// repository, a different project or destination namespace.
	FindingSource FindingKind = "source"
	// FindingAPIVersion: a rendered object whose own apiVersion moved.
	FindingAPIVersion FindingKind = "apiVersion"
	// FindingDroppedVersion: a CustomResourceDefinition that stopped serving
	// versions, and the manifests in this repository still declaring them.
	FindingDroppedVersion FindingKind = "droppedVersion"
	// FindingValuesDropped: settings this repository makes that the new chart
	// version no longer declares. Helm ignores an unknown value rather than
	// failing on it, so these stop applying while everything stays green.
	FindingValuesDropped FindingKind = "valuesDropped"
	// FindingUnrenderable: an Application whose chart will not render at the
	// version this change moves it to.
	FindingUnrenderable FindingKind = "unrenderable"
	// FindingSchema: a rendered manifest the target cluster's schemas reject.
	FindingSchema FindingKind = "schema"
)

// RepositorySideRemedy reports whether an edit somewhere in the gated
// repository could clear findings of this kind.
//
// It is the per-finding half of migrate.Blockers.RepoSideRemedy, and the two
// are held together by a test rather than by a shared function, because the
// aggregate lives in migrate and migrate cannot see this package. The two
// kinds that answer false are the ones where the manifest is wrong in a way
// that needs an author rather than a version swap: an object whose apiVersion
// moved because the chart moved it, and a manifest a schema rejects.
//
// This is what stops a caller hunting for an edit that does not exist. An
// agent handed a red gate with nothing but these in it has one useful move,
// which is to say so.
func (k FindingKind) RepositorySideRemedy() bool {
	switch k {
	case FindingAPIVersion, FindingSchema:
		return false
	default:
		return true
	}
}

// Finding is one reason the gate has an opinion about a pull request, in the
// form a read surface publishes.
//
// Wide, with kind-specific fields, exactly as ObjectChange is and for the same
// reason: seven small types would put the caller's switch in the type system
// and everything else -- the ordering, the counting, the walk -- into seven
// branches. Kind says which fields mean anything.
//
// Every string on it is somebody else's text. Subject and Detail quote a
// rendered object's name, Reason quotes helm or kubeconform verbatim, and the
// only field composed entirely by bosun is Kind. Whoever publishes this owes
// the reader an origin on each one; the Dropped block is the exception, and
// its comment says why.
type Finding struct {
	Kind FindingKind `json:"kind"`

	// Count is this finding's contribution to the blocker breakdown, and the
	// number of things wrong rather than the number of findings: a definition
	// four manifests still declare counts four, because four files have to
	// change. Zero means reported and not blocking -- a dropped version
	// nothing declares, an apiVersion move that is the repair rather than a
	// new migration.
	Count int `json:"count"`

	// Blocking is Count > 0, on the wire so that a caller does not have to
	// know that rule. Both are published because the count is what a
	// breakdown adds up and the boolean is what a client branches on, and a
	// client made to derive one from the other will one day derive it wrong.
	Blocking bool `json:"blocking"`

	// Subject is what the finding is about, in the vocabulary the report uses:
	// an Application name, or `Kind/name in namespace` for a rendered object.
	Subject string `json:"subject"`
	// Cluster is where, when the finding belongs to one.
	Cluster string `json:"cluster,omitempty"`
	// Source is which rendered stream produced it. Schema findings only.
	Source string `json:"source,omitempty"`

	// From and To are the two sides of the move, when the finding is one: two
	// chart versions, two clusters, two apiVersions.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`

	// Detail is bosun's own sentence about the finding, with names in it.
	Detail string `json:"detail,omitempty"`

	// Reason is what a tool said when it refused, verbatim: helm on an
	// unrenderable chart, kubeconform on a rejected manifest. Its own field
	// rather than folded into Detail because it is the one string here that
	// bosun did not compose any part of.
	Reason string `json:"reason,omitempty"`

	// Keys are the settings the new chart version stopped declaring.
	// FindingValuesDropped only.
	Keys []string `json:"keys,omitempty"`

	// ConsumerFiles are the repository manifests still declaring a dropped
	// version, and ConsumersScanned records that the repository was looked at
	// at all. FindingDroppedVersion only, and the pair is the difference
	// between "nothing depends on it" and "we could not look" -- which is
	// also which blocker bucket this finding lands in.
	ConsumerFiles    []string `json:"consumerFiles,omitempty"`
	ConsumersScanned bool     `json:"consumersScanned,omitempty"`

	// Dropped is the migration this finding demands, and it is present only
	// when every field of it passed the contract's own grammars.
	//
	// The distinction matters more here than anywhere else on the verdict. A
	// caller reading these fields is being told which manifests move to which
	// apiVersion, and it is the one thing on this surface a program acts on
	// without a person in between. The grammars are what make it a typed fact
	// rather than a sentence a chart could have written: nothing that passes
	// them can hold a space, a backtick or a newline. A finding whose fields
	// do not hold their shape keeps its prose and loses this block, which is
	// the same trade migrate.DroppedBlock makes for the same reason.
	Dropped *migrate.Dropped `json:"dropped,omitempty"`
}

// Findings enumerates every reason this result has an opinion, in report
// order: the sections a reader sees top to bottom.
//
// Ordering is part of the contract. A caller that renders the first finding is
// rendering the one the report leads with, and a list that came out in map
// order would put a settings drop above an Application that will not render at
// all.
func (d *DiffResult) Findings() []Finding {
	var out []Finding

	for _, c := range d.Targeting {
		out = append(out, withBlocking(Finding{
			Kind: FindingTargeting, Count: 1,
			Subject: c.App, Cluster: c.Cluster,
			From: c.From, To: c.To, Detail: c.Detail,
		}))
	}
	for _, c := range d.Other {
		out = append(out, withBlocking(Finding{
			Kind: FindingSource, Count: 1,
			Subject: c.App, Cluster: c.Cluster,
			From: c.From, To: c.To, Detail: c.Detail,
		}))
	}

	// The object findings, grouped the way the report groups them rather than
	// in the order diffObjects happened to produce: an Application that will
	// not render is the finding that says the Application does not work at
	// all, and it goes first for the same reason Verdict names it first.
	for _, kind := range []ObjectChangeKind{
		ObjectRenderFailed, ObjectCRDVersionRemoved, ObjectAPIVersionMoved, ObjectValuesKeyDropped,
	} {
		for _, o := range d.Objects {
			if o.Kind != kind {
				continue
			}
			if f, ok := objectFinding(o); ok {
				out = append(out, f)
			}
		}
	}

	for _, s := range d.Schema {
		out = append(out, withBlocking(Finding{
			Kind: FindingSchema, Count: 1,
			Subject: s.Kind + "/" + s.Name, Source: s.Source,
			Detail: "the target cluster's schemas reject this manifest",
			Reason: s.Message,
		}))
	}
	return out
}

// objectFinding maps one rendered-object change onto a finding, or reports
// that it is not one of the classes the breakdown counts.
func objectFinding(o ObjectChange) (Finding, bool) {
	switch o.Kind {
	case ObjectRenderFailed:
		return withBlocking(Finding{
			Kind: FindingUnrenderable, Count: 1,
			Subject: o.Object, Cluster: o.Cluster, From: o.From, To: o.To,
			Detail: "the chart will not render at the version this change moves it to, " +
				"so there is nothing to diff and nothing that will sync",
			Reason: o.Reason,
		}), true

	case ObjectCRDVersionRemoved:
		f := Finding{
			Kind:    FindingDroppedVersion,
			Subject: o.Object, Cluster: o.Cluster, From: o.From, To: o.To,
			ConsumerFiles:    append([]string(nil), o.ConsumerFiles...),
			ConsumersScanned: o.ConsumersKnown,
		}
		// Not scanned counts as one, because the definition itself is the
		// finding: nothing counted the manifests, so the only honest unit is
		// the thing that was not looked into.
		if o.ConsumersKnown {
			f.Count = len(o.ConsumerFiles)
			f.Detail = fmt.Sprintf("%s no longer served; %s in this repository still declare a dropped version",
				plural(countVersions(o.From), "version"),
				plural(len(o.ConsumerFiles), "manifest"))
		} else {
			f.Count = 1
			f.Detail = "a served version is gone and this repository could not be scanned for " +
				"manifests still declaring it, so nothing here claims that none do"
		}
		if dr, ok := droppedFromChange(o); ok && dr.WellFormed() {
			f.Dropped = &dr
		}
		return withBlocking(f), true

	case ObjectAPIVersionMoved:
		f := Finding{
			Kind: FindingAPIVersion, Count: 1,
			Subject: o.Object, Cluster: o.Cluster, From: o.From, To: o.To,
			Detail: "this object's own apiVersion moved, which renders cleanly and can " +
				"break at apply",
		}
		if o.PartOfMigration {
			// The repair, not a new migration. Reported so a caller can see
			// the move it asked for, counted at nothing so that a pull request
			// fixing a dropped served version can go green -- the first live
			// repair proved that one by migrating 27 manifests and turning
			// its own gate red.
			f.Count = 0
			f.Detail = "this object's apiVersion moved to the version a dropped-version " +
				"finding in this same verdict demands, so it is the migration rather than a new one"
		}
		return withBlocking(f), true

	case ObjectValuesKeyDropped:
		return withBlocking(Finding{
			Kind: FindingValuesDropped, Count: len(o.Keys),
			Subject: o.Object, Cluster: o.Cluster, From: o.From, To: o.To,
			Keys: append([]string(nil), o.Keys...),
			Detail: fmt.Sprintf("%s set here that the new chart version no longer declares; "+
				"helm ignores an unknown value rather than failing on it, so they stop applying "+
				"while the render stays green", plural(len(o.Keys), "setting")),
		}), true
	}
	return Finding{}, false
}

// countVersions counts the served versions a dropped-version finding names.
//
// The same comma-separated field droppedFromChange parses, counted the same
// way: empty entries dropped. strings.Split on an empty string returns a slice
// of one, so counting its length directly reports "1 version no longer served"
// about a finding that names none.
func countVersions(from string) int {
	var n int
	for _, v := range strings.Split(from, ",") {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	return n
}

// withBlocking sets the derived half of a finding, in the one place it is
// derived.
//
// Not `blocking`, which would sit one screen from (*DiffResult).Blocking and
// read like the same question asked of a finding. This one answers nothing; it
// fills a field in.
func withBlocking(f Finding) Finding {
	f.Blocking = f.Count > 0
	return f
}

// Summary is the whole verdict as data: what it says, why, and what it could
// not look at.
//
// It exists because the gate's answer now outlives the process that renders
// it. gateservice holds one of these per open pull request so a read surface
// can publish the verdict standing against a head commit without re-running
// anything, and holding the DiffResult itself would keep every rendered
// object's field diff in memory for the sake of a breakdown and a list.
//
// Nothing on it is computed here that is not computed for the report. It is
// the same four answers -- the headline, the breakdown, the findings, the
// coverage -- taken once.
type Summary struct {
	// Blocking and Headline are Verdict's two returns: whether this stops the
	// merge, and the one line that says why.
	Blocking bool
	Headline string

	// Blockers is the counted breakdown, the same one the report's marker
	// carries.
	Blockers migrate.Blockers

	// Findings is every reason behind those counts, in report order.
	Findings []Finding

	// NotCovered is what the gate could not read, and therefore did not
	// judge: a chart that would not render at the revision this change starts
	// from, a values surface it could not compare, a repository scan that
	// failed, an ApplicationSet that would not expand.
	//
	// Distinct from the findings above, and the distinction is the reason it
	// is published at all. A finding is "we looked and this is wrong"; this is
	// "we did not look here", and a clean verdict beside a non-empty one of
	// these is a narrower claim than a clean verdict beside an empty one. A
	// caller that cannot see this list cannot tell them apart.
	//
	// Each entry is a sentence the gate composed, already escaped for the
	// markdown report it also goes into.
	NotCovered []string

	// BaseRev and HeadRev are the two revisions this verdict is the
	// difference between.
	BaseRev string
	HeadRev string
}

// Summarise renders the verdict as data, off one walk of the findings.
//
// One walk matters. Verdict is the breakdown in words and the breakdown is the
// findings added up, so asking for all three through the exported methods
// would enumerate the findings three times -- and hand a caller three chances
// to hold two halves of one verdict that came from different walks.
func (d *DiffResult) Summarise() *Summary {
	findings := d.Findings()
	blockers := blockersOf(findings)
	blocking, headline := d.verdict(blockers)
	out := &Summary{
		Blocking: blocking,
		Headline: headline,
		Blockers: blockers,
		Findings: findings,
		BaseRev:  d.BaseRev,
		HeadRev:  d.HeadRev,
	}
	for _, w := range d.Warnings {
		out.NotCovered = append(out.NotCovered, string(w))
	}
	return out
}
