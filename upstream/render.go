package upstream

import (
	"fmt"
	"strings"
)

// Render puts the maintainers' words in a prompt, labelled as
// testimony rather than as the render.
//
// The distinction is the point. The gate report is computed, somebody rendered
// both versions and diffed them. Release notes are claimed; somebody wrote down
// what they meant to do. An explanation that blurs the two will state an
// intention as an outcome, fluently, and the reader cannot tell.
//
// It lives here, in the package that owns Notes, rather than beside the one
// caller that used to hold it, because it is now measured as well as used: the
// eval suite scores the explain prompt against this exact block. A copy in the
// suite would drift from the copy that ships, and the first symptom would be a
// grounding score that describes a prompt nobody is given.
func Render(n *Notes) string {
	var b strings.Builder
	b.WriteString("\n\nUPSTREAM RELEASE NOTES\n\n")
	if !n.Any() {
		note := ""
		if n != nil {
			note = n.Note
		}
		fmt.Fprintf(&b, "None. %s\n", note)
		if !n.compare().Any() {
			b.WriteString("\nSay what the render changed and do not supply a reason.\n")
		}
	} else {
		where := "in their releases"
		if n.Origin != "" && n.Origin != OriginReleases {
			where = "in " + n.Origin
		}
		fmt.Fprintf(&b, "%s What the maintainers wrote %s, newest first. This is what\n"+
			"they SAY they changed; the gate report above is what actually rendered.\n\n",
			n.Note, where)
		for _, r := range n.Releases {
			title := r.Tag
			if r.Name != "" && r.Name != r.Tag {
				title = r.Tag + " -- " + r.Name
			}
			fmt.Fprintf(&b, "--- %s ---\n%s\n\n", title, r.Body)
		}
	}
	b.WriteString(renderCompare(n.compare()))
	return b.String()
}

func (n *Notes) compare() *Compare {
	if n == nil {
		return nil
	}
	return n.Compare
}

// renderCompare puts the upstream commits in the prompt, under the same
// testimony label as the release notes and for the same reason.
//
// A commit message is a claim about a change, written by whoever made it. It is
// usually a better claim than a changelog entry, nobody polishes it, and it
// sits next to the code, but it is still not the render, and a prompt that let
// the two blur would have the model reporting an intention as an outcome.
//
// The interesting negative gets said too. "Two hundred commits and none of them
// mentions this" is a real finding about a bump, and an empty section that just
// disappeared would have read as "nothing was looked for".
func renderCompare(c *Compare) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nUPSTREAM COMMITS\n\n")
	if !c.Any() {
		fmt.Fprintf(&b, "None. %s\n\nDo not supply a reason the sources above do not give you.\n", c.Note)
		return b.String()
	}
	fmt.Fprintf(&b, "%s What the maintainers CHANGED between the two tags -- not what they\n"+
		"said they changed. Still testimony: a commit message is a claim, and the gate\n"+
		"report above is the only computed fact here.\n\n", c.Note)
	if c.Total > 0 {
		fmt.Fprintf(&b, "%d commit(s) in %s", c.Total, c.Range)
		if c.Truncated {
			b.WriteString(" (more than could be read)")
		}
		b.WriteString("; these mention what the gate found")
		if c.Capped {
			b.WriteString(", showing the first few")
		}
		b.WriteString(":\n\n")
	}
	for _, cm := range c.Relevant {
		fmt.Fprintf(&b, "- %s %s\n", cm.SHA, cm.Message)
	}
	if len(c.Files) > 0 {
		b.WriteString("\nFiles the upstream diff touched that name the same things:\n\n")
		for _, f := range c.Files {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	b.WriteString("\n")
	return b.String()
}
