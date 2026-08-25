package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
)

// The cluster half of a brief.
//
// Everything the gate knows is a property of TEXT. It renders a repository at
// two revisions and compares, so "3 manifests still declare a version this
// chart stops serving" is a fact about the repository and the only fact it can
// have. Whether anything is actually stored on that version is a different
// question, it usually decides whether a human needs to be woken up, and CI
// structurally cannot answer it. This service runs in the cluster; ADR 0002
// said that was the point and this is the first code that spends it.
//
// Gathered by CODE, labelled fact, and never asserted by a model. The model is
// shown the result the same way it is shown the gate report.

// liveFacts is what the cluster said about one promotion.
type liveFacts struct {
	CRDs []liveCRD
	Apps []liveApp
	// Note carries a bound that was hit, so a brief never implies it checked
	// more than it did.
	Note string
}

// liveCRD is one CustomResourceDefinition the report is about, and how many
// objects are stored on the versions in question.
type liveCRD struct {
	// Name is <plural>.<group>, as the report prints it.
	Name string
	// Versions are the versions counted -- the ones the chart stops serving,
	// or every served version when the definition is going away entirely.
	Versions []string
	// Counts parallels Versions.
	Counts []cluster.Count
}

// Total sums the counted versions, and says whether anybody was allowed to
// look at all of them. A partial answer must not be presented as a total: the
// whole value of "0 live objects" is that it ends a conversation, and it can
// only do that if it means what it says.
func (c liveCRD) Total() (n int, known, atLeast bool) {
	known = len(c.Counts) > 0
	for _, x := range c.Counts {
		if !x.Known {
			known = false
			continue
		}
		n += x.N
		atLeast = atLeast || x.AtLeast
	}
	return n, known, atLeast
}

func (c liveCRD) String() string {
	n, known, atLeast := c.Total()
	if !known {
		// Name the first thing that stopped it, rather than a generic
		// "unknown" -- the note is the fix instruction for an operator who
		// scoped the ClusterRole by API group.
		for _, x := range c.Counts {
			if !x.Known {
				return x.Note
			}
		}
		return "not checked"
	}
	if atLeast {
		return fmt.Sprintf("at least %d live object(s)", n)
	}
	return fmt.Sprintf("%d live object(s)", n)
}

// liveApp is one Application the promotion said it would verify.
type liveApp struct {
	Name   string
	Health cluster.Health
}

// Any reports whether there is anything worth printing.
func (f *liveFacts) Any() bool {
	return f != nil && (len(f.CRDs) > 0 || len(f.Apps) > 0)
}

// maxLiveCRDs bounds the walk. A report naming thirty removed definitions would
// otherwise become ninety API calls on a path a human is already waiting on.
const maxLiveCRDs = 8

// liveFor gathers the cluster facts a brief may carry.
//
// Never fails, never blocks anything. A reader that is not configured, not
// permitted, or cannot reach the apiserver produces a brief without a live
// section -- which is the brief that existed before this, and still useful.
func (t *Triage) liveFor(ctx context.Context, p Promotion, report string) *liveFacts {
	if t.Cluster == nil {
		return nil
	}
	f := &liveFacts{}

	// Two kinds of finding, and only the first carries its own versions.
	want := map[string][]string{}
	var order []string
	remember := func(name string, versions []string) {
		if _, seen := want[name]; !seen {
			order = append(order, name)
		}
		want[name] = append(want[name], versions...)
	}
	for _, d := range migrate.ParseReport(report) {
		remember(d.CRD, d.Versions)
	}
	// Removed outright -- the finding that names a definition and no versions.
	// Read through migrate rather than re-matched here: this file used to
	// carry a third regexp for the gate's bullet format, and because it did
	// not track which group the bullet was in, an ADDED definition sent us to
	// the apiserver to ask what was running.
	for _, name := range migrate.ParseRemovedCRDs(report) {
		remember(name, nil)
	}

	for i, name := range order {
		if i >= maxLiveCRDs {
			f.Note = fmt.Sprintf("checked the first %d of %d definitions", maxLiveCRDs, len(order))
			break
		}
		dot := strings.Index(name, ".")
		if dot < 0 {
			continue
		}
		plural, group := name[:dot], name[dot+1:]

		versions := dedupeSorted(want[name])
		if len(versions) == 0 {
			// The definition is going away entirely and the report does not
			// say under which versions anything is stored. Pre-merge it is
			// still installed, so the cluster can say -- and the cluster is
			// the only honest source, since the versions a repository declares
			// are not necessarily the versions objects live on.
			crd := t.Cluster.CRD(ctx, name)
			if !crd.Known || len(crd.Versions) == 0 {
				f.CRDs = append(f.CRDs, liveCRD{
					Name: name, Counts: []cluster.Count{{Note: crd.Note}},
				})
				continue
			}
			versions = crd.Versions
		}

		lc := liveCRD{Name: name, Versions: versions}
		for _, v := range versions {
			lc.Counts = append(lc.Counts, t.Cluster.CountLive(ctx, group, v, plural))
		}
		f.CRDs = append(f.CRDs, lc)
	}

	// verifyApps has been on the wire since the first version of this service
	// and nothing has ever read it. "This Application was already Degraded
	// before your bump" is the single most useful thing that can be said to
	// somebody looking at a red gate, and it was one field away the whole time.
	for _, name := range p.VerifyApps {
		if name = strings.TrimSpace(name); name != "" {
			f.Apps = append(f.Apps, liveApp{Name: name, Health: t.Cluster.AppHealth(ctx, name)})
		}
	}
	if !f.Any() {
		return nil
	}
	return f
}

// dedupeSorted trims, drops blanks and sorts. The gate has a dedupeOrdered
// that does none of those; the names carry the difference so learning one does
// not mispredict the other.
func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.TrimSpace(s); s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// renderLive is the comment section. Short, because a live fact is one line
// and a table of one-line facts is a table nobody reads.
func renderLive(f *liveFacts) string {
	if !f.Any() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n**What is actually running**\n\n")
	for _, c := range f.CRDs {
		if len(c.Versions) > 0 {
			fmt.Fprintf(&b, "- `%s` on `%s` — %s\n", c.Name, strings.Join(c.Versions, "`, `"), c)
		} else {
			fmt.Fprintf(&b, "- `%s` — %s\n", c.Name, c)
		}
	}
	for _, a := range f.Apps {
		fmt.Fprintf(&b, "- Application `%s` — %s\n", a.Name, a.Health)
	}
	if f.Note != "" {
		fmt.Fprintf(&b, "- %s\n", f.Note)
	}
	b.WriteString("\n<sub>Read from the cluster, read-only, before this change is applied.</sub>\n")
	return b.String()
}

// promptLive is the same facts for the model, under a label that says which
// kind of thing they are.
//
// The prompt already distinguishes the gate report (computed) from release
// notes and commits (claimed). This is a third category and the strongest one:
// nobody wrote it down, it was measured. It is labelled so, and the last line
// exists because the dangerous reading of "0" is not that it is wrong but that
// it is silently substituted for "nobody checked".
func promptLive(f *liveFacts) string {
	if !f.Any() {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nLIVE CLUSTER (fact, read-only)\n\n")
	b.WriteString("Measured in the cluster this repository deploys to, before this change is\n" +
		"applied. Not testimony: nobody wrote this down, it was counted.\n\n")
	for _, c := range f.CRDs {
		if len(c.Versions) > 0 {
			fmt.Fprintf(&b, "- %s on %s: %s\n", c.Name, strings.Join(c.Versions, ", "), c)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", c.Name, c)
		}
	}
	for _, a := range f.Apps {
		fmt.Fprintf(&b, "- Application %s: %s\n", a.Name, a.Health)
	}
	if f.Note != "" {
		fmt.Fprintf(&b, "- %s\n", f.Note)
	}
	b.WriteString("\n\"not permitted to check\" means NOBODY LOOKED. It is not zero, and it is not\n" +
		"evidence that anything is safe. Say what was not checked rather than what it\n" +
		"would have shown.\n")
	return b.String()
}
