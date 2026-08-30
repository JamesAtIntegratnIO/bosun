package web

import (
	"os"
	"testing"
)

// The embedded mark must be the site's favicon, byte for byte.
//
// web/mark.svg is a copy: go:embed cannot reach outside this package, and the
// binary must not fetch anything at runtime, so a copy was the only shape
// available. A copy is only safe while something fails when it drifts, and
// this is that something.
//
// If this fails, the fix is `cp site/public/favicon.svg web/mark.svg` -- not
// an edit to web/mark.svg. bosun.integratn.io is where the mark is decided.
func TestMarkMatchesTheSite(t *testing.T) {
	const site = "../site/public/favicon.svg"

	want, err := os.ReadFile(site)
	if err != nil {
		t.Fatalf("reading %s: %v", site, err)
	}
	if string(markSVG) != string(want) {
		t.Errorf("web/mark.svg has drifted from %s (embedded %d bytes, site %d bytes).\n"+
			"The site is where the mark is decided; copy it across rather than editing the copy:\n"+
			"\tcp site/public/favicon.svg web/mark.svg",
			site, len(markSVG), len(want))
	}
}
