package edits

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/safepath"
)

// The deny-list is this package's central promise, "never edit the gate",
// "never weaken a merge policy to go green", and it was a promise about
// Strings. A tracked symlink at a permitted path made every one of those
// entries a suggestion.
func TestASymlinkAtAPermittedPathCannotReachADeniedFile(t *testing.T) {
	root := repo(t, map[string]string{
		"addons/values.yaml":          sample,
		".github/workflows/gate.yaml": "on: pull_request\njobs:\n  gate:\n    version: 1.0.0\n",
	})
	link := filepath.Join(root, "addons", "linked.yaml")
	if err := os.Symlink(filepath.Join(root, ".github", "workflows", "gate.yaml"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res, err := Apply(root, openPolicy(), []Edit{
		{Path: "addons/linked.yaml", Key: "jobs.gate.version", From: "1.0.0", To: "9.9.9"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("a symlink reached a denied file: %+v", res.Applied)
	}
	if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, safepath.ErrSymlink.Error()) {
		t.Errorf("want the symlink refusal, got %+v", res.Rejected)
	}
	got, _ := os.ReadFile(filepath.Join(root, ".github", "workflows", "gate.yaml"))
	if strings.Contains(string(got), "9.9.9") {
		t.Error("the workflow was rewritten through the link")
	}
}

// `from` is documented, schema-required and prompted as the check that stops a
// model editing a file it has not read. An empty one used to skip it entirely,
// which made "" a skeleton key for every scalar in every permitted file.
func TestAnEmptyFromIsNotASkeletonKey(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": sample})
	res, err := Apply(root, openPolicy(), []Edit{
		{Path: "addons/values.yaml", Key: "metallb.enabled", From: "", To: "false"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf(`from:"" overwrote a scalar it never read: %+v`, res.Applied)
	}
	if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, "refusing to overwrite") {
		t.Errorf("want the optimistic-concurrency refusal, got %+v", res.Rejected)
	}
}

// The other half of the same rule: a scalar that is empty matches ""
// exactly, so tightening the check did not make empty values uneditable.
func TestAnActuallyEmptyScalarStillMatches(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": "metallb:\n  note: \"\"\n"})
	res, err := Apply(root, openPolicy(), []Edit{
		{Path: "addons/values.yaml", Key: "metallb.note", From: "", To: "set"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 {
		t.Fatalf("an empty scalar must still be editable: %+v", res.Rejected)
	}
	got, _ := os.ReadFile(filepath.Join(root, "addons", "values.yaml"))
	if want := "metallb:\n  note: \"set\"\n"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Replacing the first text on the line that looked like the old value picked
// the wrong token in two ordinary shapes. Both are here because both produce a
// file that parses, renders, and is not what the edit said it was.
func TestTheEditLandsOnTheKeyItNames(t *testing.T) {
	for _, tc := range []struct {
		name, file, key, from, to, want string
	}{
		{
			name: "flow mapping with a repeated value",
			file: "spec: {a: old, b: old}\n",
			key:  "spec.b", from: "old", to: "new",
			want: "spec: {a: old, b: new}\n",
		},
		{
			name: "the key and the value are the same word",
			file: "version: version\n",
			key:  "version", from: "version", to: "1.2.3",
			want: "version: 1.2.3\n",
		},
		{
			name: "the value also appears in the key",
			file: "enabled: enabled\n",
			key:  "enabled", from: "enabled", to: "false",
			want: "enabled: false\n",
		},
		{
			name: "double quotes are preserved",
			file: "image:\n  tag: \"1.2.3\"   # pinned\n",
			key:  "image.tag", from: "1.2.3", to: "1.2.4",
			want: "image:\n  tag: \"1.2.4\"   # pinned\n",
		},
		{
			name: "single quotes are preserved",
			file: "image:\n  tag: '1.2.3'\n",
			key:  "image.tag", from: "1.2.3", to: "1.2.4",
			want: "image:\n  tag: '1.2.4'\n",
		},
		{
			name: "a value repeated on the same line as its own key",
			file: "tags: {tag: tag, other: tag}\n",
			key:  "tags.other", from: "tag", to: "final",
			want: "tags: {tag: tag, other: final}\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := repo(t, map[string]string{"addons/values.yaml": tc.file})
			res, err := Apply(root, openPolicy(), []Edit{
				{Path: "addons/values.yaml", Key: tc.key, From: tc.from, To: tc.to},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Applied) != 1 {
				t.Fatalf("not applied: %+v", res.Rejected)
			}
			got, _ := os.ReadFile(filepath.Join(root, "addons", "values.yaml"))
			if string(got) != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// A block scalar's value is not on the line the node reports, so any match
// there is a coincidence. Refused rather than guessed at.
func TestABlockScalarIsRefusedRatherThanGuessedAt(t *testing.T) {
	root := repo(t, map[string]string{"addons/values.yaml": "script: |\n  echo hello\n"})
	res, err := Apply(root, openPolicy(), []Edit{
		{Path: "addons/values.yaml", Key: "script", From: "echo hello\n", To: "echo bye\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 0 {
		t.Fatalf("a block scalar was rewritten: %+v", res.Applied)
	}
	if len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, "block scalar") {
		t.Errorf("want the block-scalar refusal, got %+v", res.Rejected)
	}
}
