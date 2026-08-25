package gate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// ClusterSecret is the subset of an ArgoCD cluster Secret the inventory is
// built from, in the shape the Kubernetes API serves it: `data` values are
// base64 strings on the JSON wire. Both readers of these Secrets -- the CLI
// shelling out to kubectl, and the agent reading the API server directly --
// parse into this and hand it to InventoryFromSecrets, so the two can never
// decode the same Secret two different ways.
type ClusterSecret struct {
	Metadata struct {
		Name        string            `json:"name"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Data map[string]string `json:"data"`
}

// ExportFilter trims snapshot noise from an exported inventory. The zero
// value keeps everything -- which is exactly right for a live read: nothing
// is ever diffed against a live inventory, so churn cannot cause drift and
// an extra annotation costs nothing.
type ExportFilter struct {
	// IgnoreKeys are label and annotation keys to drop. A trailing `*`
	// matches by prefix.
	IgnoreKeys []string
	// KeepAnnotations are the annotation keys to keep. Empty keeps all.
	KeepAnnotations map[string]bool
}

// NewExportFilter builds the filter a snapshot export wants: the defaults
// common to any ArgoCD install, plus whatever the config declares, plus the
// annotation keys the repository actually templates with.
func NewExportFilter(cfg *Config, repoRoot string) ExportFilter {
	f := ExportFilter{IgnoreKeys: append([]string{}, defaultNoisyKeys...)}
	if cfg != nil {
		f.IgnoreKeys = append(f.IgnoreKeys, cfg.ClustersExport.IgnoreKeys...)
	}
	if repoRoot != "" {
		if used, err := annotationsUsedBy(repoRoot); err == nil && len(used) > 0 {
			f.KeepAnnotations = used
		}
	}
	return f
}

// ExportClusters reads the ArgoCD cluster Secrets through kubectl and builds
// an inventory snapshot, stamped with the export time so a reviewer can see
// its age.
func ExportClusters(kubeContext, namespace string, filter ExportFilter) (*Inventory, error) {
	args := []string{"get", "secrets", "-n", namespace,
		"-l", "argocd.argoproj.io/secret-type=cluster", "-o", "json"}
	if kubeContext != "" {
		args = append(args, "--context="+kubeContext)
	}

	cmd := exec.Command("kubectl", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kubectl get secrets: %w\n%s", err, stderr.String())
	}

	var list struct {
		Items []ClusterSecret `json:"items"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &list); err != nil {
		return nil, fmt.Errorf("parsing kubectl output: %w", err)
	}

	inv := InventoryFromSecrets(list.Items, filter)
	if len(inv.Clusters) == 0 {
		return nil, fmt.Errorf("no cluster Secrets found in namespace %q", namespace)
	}
	inv.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	return inv, nil
}

// InventoryFromSecrets builds an inventory from ArgoCD cluster Secrets. It
// does not stamp GeneratedAt -- that is a property of a snapshot, and the
// caller taking one adds it.
func InventoryFromSecrets(items []ClusterSecret, filter ExportFilter) *Inventory {
	inv := &Inventory{}
	for _, item := range items {
		labels := filter.strip(item.Metadata.Labels)
		// Every ArgoCD cluster Secret carries this label -- it is the one the
		// Secrets are found by -- and generators in the wild routinely select
		// on it. LoadInventory adds it for snapshots that omitted it; a live
		// read never passes through LoadInventory, so it is added here too.
		if _, ok := labels["argocd.argoproj.io/secret-type"]; !ok {
			labels["argocd.argoproj.io/secret-type"] = "cluster"
		}
		c := Cluster{
			Labels: labels,
			// Annotations are trimmed to what the bootstraps actually
			// template with. Labels are NOT: they are selector inputs, and
			// which ones a future selector will match on is unknowable, so
			// dropping any would reintroduce the stale-fixture failure this
			// export exists to prevent.
			Annotations: keepOnly(filter.strip(item.Metadata.Annotations), filter.KeepAnnotations),
		}
		c.Name = decode(item.Data["name"])
		if c.Name == "" {
			c.Name = item.Metadata.Name
		}
		c.Server = decode(item.Data["server"])
		inv.Clusters = append(inv.Clusters, c)
	}
	return inv
}

// NormalizeInventory drops the generatedAt stamp so a re-export does not
// report drift purely because time passed.
func NormalizeInventory(raw []byte) string {
	var inv Inventory
	if err := yaml.Unmarshal(raw, &inv); err != nil {
		return string(raw)
	}
	inv.GeneratedAt = ""
	out, err := yaml.Marshal(inv)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func keepOnly(m map[string]string, keep map[string]bool) map[string]string {
	if len(keep) == 0 {
		return m
	}
	out := map[string]string{}
	for k, v := range m {
		if keep[k] {
			out[k] = v
		}
	}
	return out
}

// annotationsUsedBy finds every cluster annotation the repository templates
// with, so the exported inventory carries the handful that are read rather
// than the twenty that happen to exist on the Secret.
//
// Derived rather than configured: a list an operator maintains by hand is a
// list that goes wrong, and the answer is already in the repository.
//
// It scans the WHOLE repository, not just the bootstraps. Scanning only the
// bootstraps was the obvious first guess and it was wrong -- the inner
// ApplicationSets reference `cert_manager_namespace` and
// `external_dns_namespace` too, and trimming those broke their templates.
// Under-collecting here silently drops Applications from the render, so the
// scan errs wide: an annotation kept unnecessarily costs one line.
func annotationsUsedBy(repoRoot string) (map[string]bool, error) {
	re := regexp.MustCompile(`metadata\.annotations(?:\.([A-Za-z0-9_.\-]+)|\[["']([^"']+)["']\])`)
	out := map[string]bool{}

	err := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable paths are not worth failing an export over
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".yaml", ".yml", ".tpl", ".json":
		default:
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			for _, g := range m[1:] {
				if g != "" {
					out[g] = true
				}
			}
		}
		return nil
	})
	return out, err
}

func decode(s string) string {
	if s == "" {
		return ""
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// defaultNoisyKeys are keys that churn without changing what any selector or
// template sees -- a resync timestamp, a content hash. Otherwise every export
// reports drift, and a check that always fails gets switched off, which is
// worse than not having it.
//
// The defaults are the ones common to any ArgoCD install. Anything
// site-specific belongs in `clustersExport.ignoreKeys` in .gitops-gate.yaml --
// hardcoding a particular platform's annotation here would be exactly the kind
// of host coupling this package is built to avoid.
var defaultNoisyKeys = []string{
	"kubectl.kubernetes.io/last-applied-configuration",
	"reconcile.external-secrets.io/data-hash",
	"reconcile.external-secrets.io/created-by",
}

func (f ExportFilter) strip(m map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		skip := false
		for _, n := range f.IgnoreKeys {
			if k == n || (strings.HasSuffix(n, "*") && strings.HasPrefix(k, strings.TrimSuffix(n, "*"))) {
				skip = true
				break
			}
		}
		if !skip {
			out[k] = v
		}
	}
	return out
}
