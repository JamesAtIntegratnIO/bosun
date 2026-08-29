// Package safepath resolves a repository-relative path inside a checkout.
//
// Three places in this service take a path from somewhere untrusted, the
// promotion payload, the model's proposed edits, the gate's report, join it to
// a temporary checkout and then read or write it. Each had grown its own
// containment test, each was lexical, and each was therefore wrong in the same
// way: filepath.Rel answers a question about strings, and os.ReadFile asks a
// question about the filesystem.
//
// A repository may track symbolic links. `charts/app/values.yaml` passes every
// lexical test ever written and, if the checkout holds it as a link, resolves
// to `/var/run/secrets/kubernetes.io/serviceaccount/token` or to
// `.github/workflows/gate.yml`. The first is read into a prompt that gets
// published; the second is written, which is the deny-list, the rule that
// stops the agent editing the gate that judges it, silently not holding.
//
// So this package refuses links rather than resolving them. Resolving would
// keep the escape out of the filesystem but not out of the repository, and an
// allowed path linked to a denied one is exactly the bypass worth closing. A
// GitOps tree has no legitimate need for symlinked manifests, and "refused,
// here is why" is a better answer than a silently redirected write.
package safepath

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrEscapes is a path that leaves the checkout lexically.
var ErrEscapes = errors.New("path escapes the checkout")

// ErrSymlink is a path that leaves it through the filesystem instead.
var ErrSymlink = errors.New("path crosses a symbolic link")

// Resolve returns the absolute path rel names inside root, or an error saying
// which containment rule it broke.
//
// rel is slash-separated, as every caller's input is: promotion payloads, the
// model's `path` field and the gate's report all speak repository paths.
//
// A component that does not exist yet is not an error, callers write files
// as well as read them, but every component that does exist must be a real
// directory or file rather than a link.
func Resolve(root, rel string) (string, error) {
	// The checkout itself commonly sits under a symlinked temporary
	// directory, /tmp is /private/tmp on darwin, so root is resolved once and
	// the per-component test applies only below it. Without this every path
	// on a developer's machine would be refused as crossing a link, and the
	// fix somebody reached for under deadline would be to delete the test.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		// Root may not exist in a unit test that never made one. Fall back to
		// the lexical form: the component walk below finds nothing.
		realRoot = filepath.Clean(root)
	}

	full := filepath.Join(realRoot, filepath.FromSlash(rel))
	within, err := filepath.Rel(realRoot, full)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrEscapes, rel)
	}
	if within == "." {
		return "", fmt.Errorf("%w: %s names the checkout itself", ErrEscapes, rel)
	}

	// Every component, not just the last one. A link at `charts` redirects
	// everything under it, and checking only the leaf would call that
	// contained.
	cur := realRoot
	for _, part := range strings.Split(within, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				// Nothing here to redirect anything, and nothing below it can
				// exist either. A write to a new file is legitimate.
				break
			}
			return "", fmt.Errorf("%s: %w", rel, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %s", ErrSymlink, rel)
		}
	}
	return full, nil
}

// IsLink reports whether an already-resolved path is a symbolic link.
//
// For the walkers, which arrive holding a path they built themselves from a
// directory walk rather than one somebody handed them. Resolve is the right
// call for input; this is the right call for output.
func IsLink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}
