package safepath

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func tree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "charts", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "charts", "app", "values.yaml"), []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// The lexical test this replaced passed every one of these, and then ReadFile
// and WriteFile answered a different question than the one that was asked.
func TestASymlinkedFileIsNotContained(t *testing.T) {
	root := tree(t)
	secret := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(secret, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "charts", "app", "linked.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Resolve(root, "charts/app/linked.yaml"); !errors.Is(err, ErrSymlink) {
		t.Errorf("want ErrSymlink, got %v", err)
	}
}

// A link at a directory redirects everything under it, so checking only the
// leaf would call the whole subtree contained.
func TestASymlinkedDirectoryIsNotContained(t *testing.T) {
	root := tree(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "values.yaml"), []byte("a: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "elsewhere")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Resolve(root, "elsewhere/values.yaml"); !errors.Is(err, ErrSymlink) {
		t.Errorf("want ErrSymlink, got %v", err)
	}
}

// The bypass that matters most: a link that never leaves the repository, but
// leaves the part of it the deny-list permits.
func TestALinkToADeniedPathInsideTheRepositoryIsStillRefused(t *testing.T) {
	root := tree(t)
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, ".github", "workflows", "gate.yml")
	if err := os.WriteFile(target, []byte("on: pull_request\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "charts", "app", "gate.yaml")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Resolve(root, "charts/app/gate.yaml"); !errors.Is(err, ErrSymlink) {
		t.Errorf("want ErrSymlink, got %v", err)
	}
}

func TestLexicalEscapesAreStillRefused(t *testing.T) {
	root := tree(t)
	for _, rel := range []string{"../../../etc/passwd", "charts/../../escape.yaml", "./../outside.yaml", "."} {
		if _, err := Resolve(root, rel); !errors.Is(err, ErrEscapes) {
			t.Errorf("%s: want ErrEscapes, got %v", rel, err)
		}
	}
}

// An absolute path joins to root and lands inside it, which is the behaviour
// every caller already relied on, worth pinning so a future "improvement" to
// Join does not quietly turn it into an escape.
func TestOrdinaryPathsResolve(t *testing.T) {
	root := tree(t)
	for _, rel := range []string{"charts/app/values.yaml", "charts/app/../app/values.yaml", "charts/app/new.yaml"} {
		got, err := Resolve(root, rel)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("%s: want an absolute path, got %q", rel, got)
		}
	}
}
