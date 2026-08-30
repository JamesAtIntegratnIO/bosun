package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/internal/helmtest"
)

// The missing half of `additionalProperties: false`.
//
// The schema plus hack/lint.sh already catches one direction: a key in
// values.yaml that the schema does not declare fails the lint. Nothing catches
// the other, and it is the one that reaches a cluster. A template reading
// `.Values.newThing` that neither values.yaml nor values.schema.json declares
// renders as the empty string, and on this chart an empty environment variable
// is not a missing setting -- it is how a setting turns itself OFF, everywhere
// at once, on every install that upgrades.
//
// There is no exception list here, and an exception list is not what to add if
// this fails. Measured when this was written: 89 paths in charts/bosun, 20 in
// charts/kargo-pipelines, and every one of the 109 resolved.
func TestEveryValueATemplateReadsIsDeclared(t *testing.T) {
	total := 0
	for _, chart := range []string{"bosun", "kargo-pipelines"} {
		dir := helmtest.Dir(t, chart)
		schema := readSchema(t, filepath.Join(dir, "values.schema.json"))

		for _, ref := range valueRefs(t, filepath.Join(dir, "templates")) {
			total++
			if declaredIn(schema, strings.Split(ref.path, ".")) {
				continue
			}
			t.Errorf("charts/%s/templates reads .Values.%s and values.schema.json does not declare it (%s).\n"+
				"Declare it in both charts/%s/values.schema.json and charts/%s/values.yaml.\n"+
				"An undeclared value is not a render error: helm substitutes the empty string, "+
				"the chart installs, and the setting is off on every install that upgrades.",
				chart, ref.path, ref.where, chart, chart)
		}
	}

	// The self-check, in web/palette_test.go's mould. If the templates are ever
	// restructured -- a values block moved behind an include, a different
	// spelling for the root scope -- this walk finds nothing and reports two
	// empty sets as agreement, which reads exactly like a pass.
	if total < 50 {
		t.Fatalf("found only %d .Values references across both charts; the templates "+
			"no longer spell values in a way valueRef matches, and this test is now "+
			"proving nothing. Update the pattern rather than lowering this number.", total)
	}
}

type valueRef struct{ path, where string }

// valueRefs finds every .Values path a template reads.
//
// The `$`/`.` alternation is what makes this scope-independent and is why no
// template parser is needed: inside a `with` or a `range` the dot is rebound,
// so the templates spell the root scope `$.Values` -- charts/kargo-pipelines
// does exactly this in stage.yaml. Matching only `.Values` would silently miss
// every value read inside a loop, which on that chart is most of them.
var valuePattern = regexp.MustCompile(`[$.]Values\.([A-Za-z0-9_]+(?:\.[A-Za-z0-9_]+)*)`)

func valueRefs(t *testing.T, dir string) []valueRef {
	t.Helper()
	seen := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range valuePattern.FindAllStringSubmatch(string(body), -1) {
			if _, ok := seen[m[1]]; !ok {
				seen[m[1]] = filepath.Base(path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]valueRef, 0, len(seen))
	for p, where := range seen {
		out = append(out, valueRef{path: p, where: where})
	}
	return out
}

func readSchema(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s is not valid JSON: %v", path, err)
	}
	return out
}

// declaredIn walks one dotted path down the schema's `properties`.
//
// A node that declares no `properties` but is an open object ends the walk
// successfully: `global` and the free-form label and annotation maps are
// objects whose keys are the operator's to choose, and a template reaching into
// one is reading a value the schema has already said it will not enumerate.
func declaredIn(schema map[string]any, segments []string) bool {
	node := schema
	for _, seg := range segments {
		props, ok := node["properties"].(map[string]any)
		if !ok {
			return isOpenObject(node)
		}
		next, ok := props[seg].(map[string]any)
		if !ok {
			return false
		}
		node = next
	}
	return true
}

func isOpenObject(node map[string]any) bool {
	if ap, ok := node["additionalProperties"]; ok {
		allowed, isBool := ap.(bool)
		return !isBool || allowed
	}
	return node["type"] == "object"
}
