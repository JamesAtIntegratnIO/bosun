package gate

import (
	"encoding/json"
	"strings"
	"testing"
)

// These strings are a wire contract: they go out in the JSON report and the
// agent reads them. Typing the fields must not have moved any of them.
func TestEnumWireValuesAreUnchanged(t *testing.T) {
	for got, want := range map[ChangeKind]string{
		ChangeAdded: "added", ChangeRemoved: "removed", ChangeMoved: "moved",
		ChangeIntroduced: "introduced", ChangeSource: "source",
		ChangeSourceType: "source-type", ChangeProject: "project",
		ChangeNamespace: "namespace", ChangeVersion: "version",
	} {
		if string(got) != want {
			t.Errorf("ChangeKind %q must serialise as %q", got, want)
		}
	}
	for got, want := range map[ObjectChangeKind]string{
		ObjectAdded: "added", ObjectRemoved: "removed", ObjectChanged: "changed",
		ObjectAPIVersionMoved: "apiVersion", ObjectCRDVersionRemoved: "crdVersionRemoved",
		ObjectValuesKeyDropped: "valuesKeyDropped",
	} {
		if string(got) != want {
			t.Errorf("ObjectChangeKind %q must serialise as %q", got, want)
		}
	}
	for got, want := range map[RowSource]string{RowHelm: "helm", RowPath: "path"} {
		if string(got) != want {
			t.Errorf("RowSource %q must serialise as %q", got, want)
		}
	}
}

// The JSON keys matter as much as the values.
func TestDiffResultJSONKeysAreUnchanged(t *testing.T) {
	raw, err := json.Marshal(&DiffResult{
		Targeting: []Change{{Kind: ChangeAdded, Cluster: "hub", App: "a"}},
		Objects:   []ObjectChange{{Kind: ObjectRemoved, Object: "Service/s in x"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"kind":"added"`, `"cluster":"hub"`, `"app":"a"`,
		`"kind":"removed"`, `"object":"Service/s in x"`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("missing %s in %s", want, raw)
		}
	}
}
