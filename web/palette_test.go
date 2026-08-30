package web

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var hex = regexp.MustCompile(`#[0-9a-fA-F]{6}`)

// Every colour on the page must be a colour the site declares.
//
// The page restates the palette rather than importing it -- it is one
// self-contained document with no build step and no stylesheet to link, which
// is what lets a gateway publish it without a content policy conversation. So
// the values are copied, and a copied palette drifts the moment someone picks
// "close enough" for a state the original had no token for. That is how a
// second Bosun palette gets created, one plausible hex at a time.
//
// This is the check that stops it. It reads the page's own :root blocks and
// asserts every value appears in site/src/styles/theme.css, which is where the
// palette is decided and whose header says where those colours came from: the
// badge navy, the two sea tones, the coral of the tentacles, the cream of the
// cap.
//
// If this fails, the fix is to use the site's token, or to add one to the site
// first and take it from there. It is not to add an exception here.
func TestPaletteComesFromTheSite(t *testing.T) {
	const themePath = "../site/src/styles/theme.css"

	theme, err := os.ReadFile(themePath)
	if err != nil {
		t.Fatalf("reading %s: %v", themePath, err)
	}
	declared := map[string]bool{}
	for _, c := range hex.FindAllString(string(theme), -1) {
		declared[strings.ToLower(c)] = true
	}

	// The palette lives between <style> and the first real rule; everything
	// after it is layout, which carries no colours of its own.
	start := strings.Index(pageHTML, "<style>")
	end := strings.Index(pageHTML, "* { box-sizing")
	if start < 0 || end < 0 || end < start {
		t.Fatal("could not find the palette block in pageHTML; if the template moved, move this with it")
	}

	seen := map[string]bool{}
	for _, c := range hex.FindAllString(pageHTML[start:end], -1) {
		c = strings.ToLower(c)
		if seen[c] {
			continue
		}
		seen[c] = true
		if !declared[c] {
			t.Errorf("%s is on the page but not in %s -- use the site's token, or add one there first", c, themePath)
		}
	}
	if len(seen) == 0 {
		t.Error("found no colours in the palette block, so this test proved nothing")
	}
}

// Nothing outside the palette block may hard-code a colour, or the block stops
// being the one place to look and the test above stops covering the page.
func TestNoColoursOutsideThePalette(t *testing.T) {
	end := strings.Index(pageHTML, "* { box-sizing")
	if end < 0 {
		t.Fatal("could not find the end of the palette block")
	}
	if got := hex.FindAllString(pageHTML[end:], -1); len(got) > 0 {
		t.Errorf("colour literals outside the palette block: %v -- name them in :root and use var()", got)
	}
}

// The two light blocks must declare exactly the same thing.
//
// Light is written twice on purpose. An explicit `light` has to beat a dark
// system preference, and a system light preference has to lose to an explicit
// `dark`, and plain CSS cannot express "either of these selectors" across a
// media-query boundary in one rule. So there are two rules, and the risk is
// the ordinary one with duplication: somebody tunes one and not the other, and
// the page renders differently depending on a preference the operator already
// overrode.
//
// The fix when this fails is to make them agree, not to relax this.
func TestLightBlocksAgree(t *testing.T) {
	media := declarations(t, ":root:not([data-theme='dark']) {")
	explicit := declarations(t, ":root[data-theme='light'] {")

	if len(media) == 0 {
		t.Fatal("found no declarations in the media-query light block")
	}
	if len(media) != len(explicit) {
		t.Fatalf("the light blocks declare different numbers of properties: media %d, explicit %d", len(media), len(explicit))
	}
	for prop, want := range media {
		if got := explicit[prop]; got != want {
			t.Errorf("%s is %q under the media query but %q under [data-theme='light']", prop, want, got)
		}
	}
}

// declarations pulls `--name: value` pairs out of the rule opened by sel.
func declarations(t *testing.T, sel string) map[string]string {
	t.Helper()
	i := strings.Index(pageHTML, sel)
	if i < 0 {
		t.Fatalf("no rule opens with %q; if the template moved, move this with it", sel)
	}
	rest := pageHTML[i+len(sel):]
	end := strings.Index(rest, "}")
	if end < 0 {
		t.Fatalf("rule %q is never closed", sel)
	}
	out := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		if c := strings.Index(line, "/*"); c >= 0 {
			line = line[:c]
		}
		name, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.HasPrefix(name, "--") {
			continue
		}
		out[name] = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), ";"))
	}
	return out
}
