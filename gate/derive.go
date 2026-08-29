package gate

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Derivation is what a live ArgoCD says this repository deploys.
//
// The types live here rather than in the package that produces them because
// the gate is the consumer and the dependency runs one way: `cluster` imports
// `gate`, never the reverse, so a shared vocabulary between them has to be
// spelt on this side. It is the same reason the report format lives in
// `migrate` rather than being written out twice.
//
// Nothing in here is content. Sources say where to render from and roots say
// which objects exist; every byte that gets rendered still comes out of the
// pull request's checkout, except for the one case LiveRoot exists to cover.
type Derivation struct {
	// Sources are the render strategies derived from Applications.
	Sources []Source

	// Roots are the ApplicationSets nothing in ArgoCD created. They are
	// reachable by no other route, so they are carried by identity and the
	// caller decides, per root, whether this repository holds a copy.
	Roots []LiveRoot

	// Applications and ApplicationSets are what was read, before filtering,
	// so the report can say how large a world the scope was derived from.
	Applications, ApplicationSets int

	// Warnings are the things skipped and why. A source the derivation could
	// not turn into a render is a blind spot, and a blind spot that announces
	// itself is survivable.
	Warnings []string
}

// LiveRoot is one ApplicationSet ArgoCD serves that nothing in ArgoCD created.
type LiveRoot struct {
	Kind, Name, Namespace string

	// Object is the applied spec, used only when this repository turns out
	// not to contain the manifest. It is the previous answer to the question
	// the gate is asking, so it is the fallback and never the preference.
	Object map[string]any
}

// Identity is how a root is matched against a manifest in the checkout, and
// against an entry in the config file.
func (r LiveRoot) Identity() string {
	return strings.Join([]string{r.Kind, r.Namespace, r.Name}, "/")
}

// FindManifest looks through a checkout for the file declaring one object,
// identified the way Kubernetes identifies it: kind, namespace and name.
//
// The scan is deliberately tolerant. A gitops repository holds Helm templates,
// Kustomize patches and CI configuration beside its manifests, and none of
// those are valid YAML documents on their own: a scan that treated an
// unparseable file as an error would fail on the first `{{- if }}` it met, and
// that is not a hypothetical, it is how the first version of this failed. A
// file this cannot read is a file that does not declare the object being
// looked for, which is the same answer a reader would give.
//
// An empty namespace matches any, because a manifest committed without one
// takes its namespace from wherever it is applied, and refusing to match it
// would send every such root down the live-spec fallback.
func FindManifest(root, kind, namespace, name string) (string, bool, error) {
	var found string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be walked is not a reason to abandon
			// the scan; it is a part of the tree with nothing in it for us.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if found != "" {
			return filepath.SkipAll
		}
		switch filepath.Ext(p) {
		case ".yaml", ".yml", ".json":
		default:
			return nil
		}
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			return nil
		}
		objs, parseErr := parseStream(raw)
		if parseErr != nil {
			return nil
		}
		for _, o := range objs {
			if declares(o, kind, namespace, name) {
				rel, relErr := filepath.Rel(root, p)
				if relErr != nil {
					return nil
				}
				found = filepath.ToSlash(rel)
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return "", false, fmt.Errorf("scanning %s for %s/%s: %w", root, kind, name, err)
	}
	return found, found != "", nil
}

// declares reports whether one parsed document is the object being looked for.
func declares(o map[string]any, kind, namespace, name string) bool {
	if k, _ := o["kind"].(string); k != kind {
		return false
	}
	md, _ := o["metadata"].(map[string]any)
	if md == nil {
		return false
	}
	if n, _ := md["name"].(string); n != name {
		return false
	}
	if namespace == "" {
		return true
	}
	ns, _ := md["namespace"].(string)
	return ns == "" || ns == namespace
}
