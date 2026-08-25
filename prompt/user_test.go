package prompt

import (
	"errors"
	"strings"
	"testing"
)

// This builder exists because the eval suite had its own copy and the two had
// diverged. The shape both sides depend on is worth pinning.
func TestUserRendersTheInventoryForm(t *testing.T) {
	got := User(UserInput{
		Header: "PULL REQUEST #7: bump metallb",
		Report: "the gate is RED",
		Files: []File{
			{Path: "b.yaml", Data: []byte("k: two\n")},
			{Path: "a.yaml", Data: []byte("metallb:\n  defaultVersion: 0.16.0\n")},
		},
		Inventory: true,
	})

	if !strings.HasPrefix(got, "PULL REQUEST #7: bump metallb\n\nthe gate is RED\n\n") {
		t.Errorf("header and report must lead:\n%s", got)
	}
	if !strings.Contains(got, "0.16.0") {
		t.Errorf("the inventory must carry the values:\n%s", got)
	}
	// Sorted, so the same case produces the same prompt on every run.
	if strings.Index(got, "a.yaml") > strings.Index(got, "b.yaml") {
		t.Errorf("files must be sorted by path:\n%s", got)
	}
	if !strings.HasSuffix(got, closing) {
		t.Errorf("the instruction must come last:\n%s", got)
	}
}

// The ablation the eval suite measures: whole files instead of the scalar
// inventory. Without an inventory a model invents a key path and paraphrases a
// value, and the applier throws the result away.
func TestUserRendersTheWholeFileForm(t *testing.T) {
	got := User(UserInput{
		Header: "PULL REQUEST: x", Report: "r",
		Files:     []File{{Path: "a.yaml", Data: []byte("k: v\n")}},
		Inventory: false,
	})
	if !strings.Contains(got, "--- a.yaml ---\nk: v\n") {
		t.Errorf("want the file pasted whole:\n%s", got)
	}
}

// A file that will not read is named, not dropped. This prompt is also the
// evidence string the applier corroborates proposed versions against.
func TestUserNamesFilesThatCouldNotBeRead(t *testing.T) {
	got := User(UserInput{
		Header: "h", Report: "r",
		Files: []File{
			{Path: "ok.yaml", Data: []byte("k: v\n")},
			{Path: "gone.yaml", Err: errors.New("no such file")},
		},
		Inventory: true,
	})
	if !strings.Contains(got, "gone.yaml") || !strings.Contains(got, "could not be read") {
		t.Errorf("the unreadable file must be named:\n%s", got)
	}
	if strings.Contains(got, "ok.yaml could not be read") {
		t.Errorf("the readable file must not be listed as skipped:\n%s", got)
	}
}
