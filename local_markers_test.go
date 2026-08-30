package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/gate"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
	"github.com/JamesAtIntegratnIO/bosun/migrate"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// Rule 1a's FIRST contract, leaking back out into a tree nothing executes.
//
// The gate finds the report it already published by searching pull-request
// comments for a marker it emits. CONTRIBUTING records how that broke the first
// time: "the marker lived in one demo script and in nothing that published a
// report, so no reader could ever find one". The marker has a constant now, and
// the demo scripts under local/ still spell it by hand -- they drive a real
// cluster through curl and python, so they cannot import it.
//
// local/ is 13 scripts and about 1,500 lines that CI has never once run. A
// marker renamed in Go leaves them scanning for a string nothing writes, and
// the symptom is a demo that hangs on a wait_for until its deadline and then
// says the gate never commented -- which is exactly what a broken gate looks
// like. The next person spends the afternoon in the wrong half of the system.
//
// So the direction is: every marker literal the scripts contain must be one
// this repository actually publishes. The scripts are not required to use them
// all; they are required not to invent one.
func TestEveryMarkerTheDemoScriptsScanForIsOnePublished(t *testing.T) {
	published := []string{
		gate.ReportMarker,
		migrate.DroppedMarker,
		migrate.BlockersMarker,
		pipeline.ReportMarker,
		gateservice.StampHead,
		gateservice.StampVerdict,
		gateservice.StampWas,
	}

	found := 0
	for _, hit := range markerLiterals(t, filepath.Join(helmtest.Root(t), "local")) {
		found++
		if publishes(published, hit.text) {
			continue
		}
		t.Errorf("%s scans for the marker %q and nothing in this repository publishes it.\n"+
			"  published: %s\n\n"+
			"A demo scanning for a string no reader writes does not fail: it waits out its "+
			"deadline and reports that the gate never commented, which is indistinguishable "+
			"from a broken gate. Fix the script, or -- if the constant moved -- fix both.",
			hit.where, hit.text, strings.Join(published, ", "))
	}

	// The self-check. These scripts are the one place in the repository where a
	// contract is restated in another language, so a scan that stops finding
	// anything is a scan that has stopped being the guard.
	if found < 2 {
		t.Fatalf("found only %d marker literals under local/; the demo scripts no longer "+
			"spell the markers in a way this scan recognises, and the guard is now vacuous.",
			found)
	}
}

type markerHit struct{ text, where string }

// markerLiterals finds every marker-shaped string in a tree.
//
// Two spellings, because the scripts use both: the whole HTML comment, and the
// bare `gitops-gate:<word>` prefix that a python `in` test uses to tell one
// stamp from another.
//
// The bare form is deliberately only `gitops-gate:`. Every stamp constant
// carries that prefix, while the `bosun:` markers are always written as
// complete HTML comments and are caught by fullMarker -- and a bare `bosun:` is
// how these scripts spell an image tag (`bosun:local`), which is not a marker
// and must not be read as one.
var (
	fullMarker = regexp.MustCompile(`<!--\s*(?:gitops-gate|bosun)[^>]*?-->`)
	stampName  = regexp.MustCompile(`gitops-gate:[a-z]+`)
)

func markerLiterals(t *testing.T, dir string) []markerHit {
	t.Helper()
	var out []markerHit
	seen := map[string]bool{}

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// The scripts and the values they feed to the cluster. Markdown is
		// prose about the system rather than a program that scans for one.
		switch filepath.Ext(path) {
		case ".sh", ".py", ".yaml", ".yml":
		default:
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(filepath.Dir(dir), path)
		for _, re := range []*regexp.Regexp{fullMarker, stampName} {
			for _, m := range re.FindAllString(string(body), -1) {
				key := rel + "\x00" + m
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, markerHit{text: m, where: rel})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].where < out[j].where })
	return out
}

// publishes reports whether a literal names one of the markers this repository
// writes. A bare stamp name matches the constant it prefixes.
func publishes(published []string, literal string) bool {
	for _, p := range published {
		if literal == p || strings.Contains(p, literal) {
			return true
		}
	}
	return false
}
