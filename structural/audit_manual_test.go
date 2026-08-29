package structural

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestAuditLiveObjects runs the detector against real objects from a real
// cluster, under the schema the apiserver already accepted them with.
//
// Every finding it produces is a false positive by construction: the object is
// live, so the schema admitted it. That makes this the one audit that needs no
// judgement about what the right answer is, the right answer is always "no
// findings", and it is the only way to learn whether a hand-rolled walker
// survives contact with the constructs real CustomResourceDefinitions use.
//
// A false finding is not harmless. It calls the model on a document that was
// fine, and puts a reshaped document and a diff in front of a human for no
// reason.
//
//	STRUCTURAL_AUDIT_CRDS=crds.json STRUCTURAL_AUDIT_OBJECTS=objects.jsonl \
//	go test ./structural -run AuditLive -v
func TestAuditLiveObjects(t *testing.T) {
	crdPath, objPath := os.Getenv("STRUCTURAL_AUDIT_CRDS"), os.Getenv("STRUCTURAL_AUDIT_OBJECTS")
	if crdPath == "" || objPath == "" {
		t.Skip("set STRUCTURAL_AUDIT_CRDS and STRUCTURAL_AUDIT_OBJECTS")
	}

	raw, err := os.ReadFile(crdPath)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []struct {
			Spec struct {
				Group string `json:"group"`
				Names struct {
					Plural string `json:"plural"`
				} `json:"names"`
				Versions []struct {
					Name   string `json:"name"`
					Schema struct {
						OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
					} `json:"schema"`
				} `json:"versions"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]Schema{}
	for _, c := range list.Items {
		for _, v := range c.Spec.Versions {
			if len(v.Schema.OpenAPIV3Schema) > 0 {
				schemas[c.Spec.Names.Plural+"."+c.Spec.Group+"/"+v.Name] = Schema(v.Schema.OpenAPIV3Schema)
			}
		}
	}
	t.Logf("schemas loaded: %d", len(schemas))

	f, err := os.Open(objPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	checked, clean := 0, 0
	byReason := map[string]int{}
	examples := map[string]string{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 8<<20), 64<<20)
	for sc.Scan() {
		var row struct {
			CRD     string         `json:"crd"`
			Version string         `json:"version"`
			Obj     map[string]any `json:"obj"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		schema, ok := schemas[row.CRD+"/"+row.Version]
		if !ok {
			continue
		}
		checked++
		fs := Check(row.Obj, schema)
		if len(fs) == 0 {
			clean++
			continue
		}
		for _, finding := range fs {
			// Group by the shape of the complaint, not the exact path: one
			// walker bug shows up as hundreds of paths.
			key := fmt.Sprintf("%s at %s", finding.Kind, topLevel(finding.Path))
			byReason[key]++
			if _, seen := examples[key]; !seen {
				examples[key] = fmt.Sprintf("%s %s -> %s", row.CRD, row.Version, finding)
			}
		}
	}

	t.Logf("\n==== %d live objects checked, %d clean, %d with FALSE findings ====",
		checked, clean, checked-clean)
	keys := make([]string, 0, len(byReason))
	for k := range byReason {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return byReason[keys[i]] > byReason[keys[j]] })
	for _, k := range keys {
		t.Logf("  %5d  %-34s  e.g. %s", byReason[k], k, examples[k])
	}
}

func topLevel(path string) string {
	if i := strings.IndexAny(path, ".["); i > 0 {
		return path[:i] + ".*"
	}
	if path == "" {
		return "(root)"
	}
	return path
}

// TestAuditCrossVersion is the other half, and without it the first half is
// worthless: a detector that never fires has no false positives either.
//
// Real CustomResourceDefinitions that serve several versions are real schema
// migrations, already shipped by their authors. Checking a live object of one
// version against another version's schema is the exact question the structural
// migration asks, on data nobody made up.
//
// It also measures what those schemas cost in a prompt, which decides whether
// the model call is worth making at all.
func TestAuditCrossVersion(t *testing.T) {
	crdPath, objPath := os.Getenv("STRUCTURAL_AUDIT_CRDS"), os.Getenv("STRUCTURAL_AUDIT_OBJECTS")
	if crdPath == "" || objPath == "" {
		t.Skip("set STRUCTURAL_AUDIT_CRDS and STRUCTURAL_AUDIT_OBJECTS")
	}
	schemas, order := loadSchemas(t, crdPath)

	f, err := os.Open(objPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	pairs, withFindings, biggest := 0, 0, 0
	var biggestName string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 8<<20), 64<<20)
	for sc.Scan() {
		var row struct {
			CRD     string         `json:"crd"`
			Version string         `json:"version"`
			Obj     map[string]any `json:"obj"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		for _, other := range order[row.CRD] {
			if other == row.Version {
				continue
			}
			target, ok := schemas[row.CRD+"/"+other]
			if !ok {
				continue
			}
			pairs++
			fs := Check(row.Obj, target)
			if len(fs) > 0 {
				withFindings++
				if withFindings <= 8 {
					t.Logf("  %s %s -> %s: %d finding(s), first: %s",
						row.CRD, row.Version, other, len(fs), fs[0])
				}
			}
			if n := len(RenderSchema(target)); n > biggest {
				biggest, biggestName = n, row.CRD+"/"+other
			}
		}
	}
	t.Logf("\n==== %d cross-version checks, %d produced findings ====", pairs, withFindings)
	t.Logf("largest rendered schema: %d chars (~%d tokens) -- %s", biggest, biggest/4, biggestName)
}

func loadSchemas(t *testing.T, path string) (map[string]Schema, map[string][]string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []struct {
			Spec struct {
				Group string `json:"group"`
				Names struct {
					Plural string `json:"plural"`
				} `json:"names"`
				Versions []struct {
					Name   string `json:"name"`
					Schema struct {
						OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
					} `json:"schema"`
				} `json:"versions"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]Schema{}
	order := map[string][]string{}
	for _, c := range list.Items {
		key := c.Spec.Names.Plural + "." + c.Spec.Group
		for _, v := range c.Spec.Versions {
			if len(v.Schema.OpenAPIV3Schema) > 0 {
				schemas[key+"/"+v.Name] = Schema(v.Schema.OpenAPIV3Schema)
				order[key] = append(order[key], v.Name)
			}
		}
	}
	return schemas, order
}

// TestAuditForwardMigrations is the direction a real migration goes: a document
// written for an old version, checked against the newest one the cluster
// serves. Anything it finds is a genuine incompatibility the chart's own
// authors shipped, the exact case the structural migration exists for, and the
// only honest way to demonstrate it without inventing a schema.
func TestAuditForwardMigrations(t *testing.T) {
	crdPath, objPath := os.Getenv("STRUCTURAL_AUDIT_CRDS"), os.Getenv("STRUCTURAL_AUDIT_OBJECTS")
	if crdPath == "" || objPath == "" {
		t.Skip("set STRUCTURAL_AUDIT_CRDS and STRUCTURAL_AUDIT_OBJECTS")
	}
	schemas, order := loadSchemas(t, crdPath)

	f, err := os.Open(objPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 8<<20), 64<<20)
	found := 0
	for sc.Scan() {
		var row struct {
			CRD     string         `json:"crd"`
			Version string         `json:"version"`
			Obj     map[string]any `json:"obj"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue
		}
		vs := order[row.CRD]
		if len(vs) < 2 {
			continue
		}
		newest := vs[len(vs)-1]
		if newest == row.Version {
			continue
		}
		target, ok := schemas[row.CRD+"/"+newest]
		if !ok {
			continue
		}
		fs := Check(row.Obj, target)
		if len(fs) == 0 {
			continue
		}
		found++
		name, _ := row.Obj["metadata"].(map[string]any)
		nm := ""
		if name != nil {
			nm, _ = name["name"].(string)
		}
		t.Logf("INCOMPATIBLE  %s  %s -> %s  (object %q)", row.CRD, row.Version, newest, nm)
		for _, x := range fs {
			t.Logf("      %s", x)
		}
	}
	t.Logf("\n==== %d forward-incompatible objects ====", found)
}
