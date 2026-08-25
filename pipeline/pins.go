package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"sigs.k8s.io/yaml"
)

// FileKeys resolves "does this file set this key?" against a checkout.
//
// Only the FIRST YAML document is read, and that is not a simplification --
// it is the same rule Kargo's `yaml-update` follows. A tracked key that lives
// in the second document of a multi-document file is silently never written,
// which is one of the ways a pin dies, and reading every document here would
// hide exactly that.
type FileKeys struct {
	root string

	mu    sync.Mutex
	cache map[string]map[string]any
	bad   map[string]error
}

func NewFileKeys(root string) *FileKeys {
	return &FileKeys{root: root, cache: map[string]map[string]any{}, bad: map[string]error{}}
}

// Has answers for one path and dotted key. An empty key asks only whether the
// file is readable, which is how the caller resolves Kargo's clone prefix.
//
// The error means "this file could not be read", never "the key is absent" --
// the two have different findings and must not collapse.
func (f *FileKeys) Has(path, key string) (bool, error) {
	doc, err := f.load(path)
	if err != nil {
		return false, err
	}
	if key == "" {
		return true, nil
	}
	return lookup(doc, key), nil
}

func (f *FileKeys) load(path string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if doc, ok := f.cache[path]; ok {
		return doc, nil
	}
	if err, ok := f.bad[path]; ok {
		return nil, err
	}
	clean := filepath.Clean(path)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
		err := fmt.Errorf("refusing to read outside the checkout: %s", path)
		f.bad[path] = err
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(f.root, clean))
	if err != nil {
		f.bad[path] = err
		return nil, err
	}
	// First document only, deliberately: see the type comment.
	first := firstDocument(string(raw))
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(first), &doc); err != nil {
		f.bad[path] = err
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	f.cache[path] = doc
	return doc, nil
}

// firstDocument returns the first YAML document's text.
//
// The subtlety is which `---` ENDS the first document rather than beginning
// it. A separator that has nothing but comments and blank lines before it is a
// leading marker -- and files in the wild open with a licence header or an
// explanation far more often than they open with content. Reading such a file
// as "document one is five comments" makes every key in it look absent, which
// is a false dead-pin report for the whole file.
//
// Found exactly that way: ten Deployments flagged as missing
// `spec.template.spec.containers.0.image`, all of which had it, all of which
// began with a comment block and a separator.
func firstDocument(s string) string {
	lines := strings.Split(s, "\n")
	start, content := 0, false
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		if t == "---" {
			if !content {
				// Everything so far was comments or blank: this opens the
				// first document rather than closing it.
				start = i + 1
				continue
			}
			return strings.Join(lines[start:i], "\n")
		}
		if t != "" && !strings.HasPrefix(t, "#") {
			content = true
		}
	}
	return strings.Join(lines[start:], "\n")
}

// lookup walks a dotted path, supporting the numeric segments Kargo's key
// syntax allows for list elements.
//
// A path this cannot follow returns TRUE, not false. That is deliberate and it
// is the difference between a useful check and a noisy one: "absent" is a
// claim that produces a finding, so it may only be made about a path that was
// genuinely walked. Anything unusual is left alone.
func lookup(doc map[string]any, key string) bool {
	var node any = doc
	for _, seg := range strings.Split(key, ".") {
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[seg]
			if !ok {
				return false
			}
			node = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(n) {
				return true // not a path this understands; make no claim
			}
			node = n[i]
		default:
			// A scalar with path left to walk: the key genuinely is not there.
			return false
		}
	}
	return true
}
