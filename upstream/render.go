package upstream

import (
	"fmt"
	"strings"
)

// Render puts the maintainers' words in a prompt, clearly labelled as
// TESTIMONY rather than as the render.
//
// The distinction is the point. The gate report is COMPUTED -- somebody
// rendered both versions and diffed them. Release notes are CLAIMED -- somebody
// wrote down what they meant to do. An explanation that blurs the two will
// state an intention as an outcome, fluently, and the reader cannot tell.
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
		fmt.Fprintf(&b, "None. %s\n\nSay what the render changed and do not supply a reason.\n", note)
		return b.String()
	}
	fmt.Fprintf(&b, "%s What the maintainers wrote, newest first. This is what they SAY they\n"+
		"changed; the gate report above is what actually rendered.\n\n", n.Note)
	for _, r := range n.Releases {
		title := r.Tag
		if r.Name != "" && r.Name != r.Tag {
			title = r.Tag + " -- " + r.Name
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n\n", title, r.Body)
	}
	return b.String()
}
