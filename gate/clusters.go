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
// base64 strings on the JSON wire. Both readers of these Secrets, the CLI
// shelling out to kubectl, and the agent reading the API server directly,
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
// value keeps everything, which is exactly right for a live read: nothing
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
// annotation keys the repository templates with.
func NewExportFilter(repoRoot string, cfg *Config) ExportFilter {
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
//
// A workstation command. kubectl is not in the gate's image, which ships
// helm and kubeconform only, and this needs a kubeconfig pointing at the
// cluster anyway, which the in-cluster gate does not have and does not want.
// The in-cluster path reads the same Secrets through the apiserver directly
// (see the cluster package).
func ExportClusters(kubeContext, namespace string, filter ExportFilter) (*Inventory, error) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		return nil, fmt.Errorf("kubectl is not on PATH: `clusters export` runs on a workstation " +
			"against a kubeconfig, and is not part of the gate's image")
	}
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
// does not stamp GeneratedAt; that is a property of a snapshot, and the
// caller taking one adds it.
func InventoryFromSecrets(items []ClusterSecret, filter ExportFilter) *Inventory {
	cs := make([]Cluster, 0, len(items))
	for _, item := range items {
		c := Cluster{
			Labels:      item.Metadata.Labels,
			Annotations: item.Metadata.Annotations,
			Name:        decode(item.Data["name"]),
			Server:      decode(item.Data["server"]),
		}
		if c.Name == "" {
			c.Name = item.Metadata.Name
		}
		cs = append(cs, c)
	}
	return InventoryFromClusters(cs, filter)
}

// InventoryFromClusters normalises clusters that arrive already decoded,
// which is what a reader that is not looking at Secrets has: the ArgoCD API
// serves name, server, labels and annotations as fields, with the credential
// block redacted.
//
// This exists so the normalisation is written once. ClusterSecret's comment
// says the two Secret readers must never decode the same Secret two different
// ways; the same argument holds a fortiori for two readers looking at
// different sources for the same facts. A selector matches, or fails to match,
// on exactly these maps, so a source that trimmed one key differently would
// produce a different targeting verdict from the same cluster, and nothing
// downstream could tell.
func InventoryFromClusters(cs []Cluster, filter ExportFilter) *Inventory {
	inv := &Inventory{}
	for _, c := range cs {
		labels := filter.strip(c.Labels)
		annotations := dropManagedBy(filter.strip(c.Annotations))
		// Every ArgoCD cluster Secret carries this label; it is the one the
		// Secrets are found by, and generators in the wild routinely select
		// on it. LoadInventory adds it for snapshots that omitted it; a live
		// read never passes through LoadInventory, so it is added here too.
		//
		// The one entry that must not get it is the implicit local cluster,
		// which is backed by no Secret and carries no labels in ArgoCD either.
		// Callers hand that one over already built rather than through here.
		if _, ok := labels["argocd.argoproj.io/secret-type"]; !ok {
			labels["argocd.argoproj.io/secret-type"] = "cluster"
		}
		inv.Clusters = append(inv.Clusters, Cluster{
			Name:   c.Name,
			Server: c.Server,
			ArgoCD: c.ArgoCD,
			Labels: labels,
			// Annotations are trimmed to what the bootstraps template with.
			// Labels are not: they are selector inputs, and which ones a
			// future selector will match on is unknowable, so dropping any
			// would reintroduce the stale-fixture failure this export
			// exists to prevent.
			Annotations: keepOnly(annotations, filter.KeepAnnotations),
		})
	}
	return inv
}

// NormaliseInventory drops the generatedAt stamp so a re-export does not
// report drift purely because time passed.
func NormaliseInventory(raw []byte) string {
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
// It scans the whole repository, not just the bootstraps. Scanning only the
// bootstraps was the obvious first guess and it was wrong, the inner
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
			// Best-effort enrichment, and a miss is safe in the direction that
			// matters: a file this cannot read contributes no annotation to
			// the keep-set, and an annotation kept unnecessarily costs one
			// line, which is the trade this whole scan is built on.
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
// template sees, a resync timestamp, a content hash. Otherwise every export
// reports drift, and a check that always fails gets switched off, which is
// worse than not having it.
//
// The defaults are the ones common to any ArgoCD install. Anything
// site-specific belongs in `clustersExport.ignoreKeys` in.gitops-gate.yaml,
// hardcoding a particular platform's annotation here would be exactly the kind
// of host coupling this package is built to avoid. managedByAnnotation is
// ArgoCD's own ownership marker, and it is dropped from every inventory
// regardless of which source built it.
//
// Found by running the two sources against a real ArgoCD: the cluster Secrets
// carry it and `GET /api/v1/clusters` does not, because ArgoCD strips it on
// the way out of its own API. Left alone, the same cluster produced two
// different inventories and the gate's verdict would have depended on which
// source an operator configured; the one thing this normalisation exists to
// make impossible.
//
// It is dropped rather than synthesised on the ArgoCD side, and that direction
// is deliberate. Re-adding it would mean asserting a fact the API did not
// report, for clusters that may never have carried it, inventing data to make
// a comparison come out even, which is the habit this codebase refuses
// everywhere else.
//
// The cost, stated because it is real rather than zero: ApplicationSet's
// cluster generator templates against the Secret's annotations verbatim, so a
// template referring to `metadata.annotations.managed-by` would see it in
// production and will not see it here. Nothing sane templates with ArgoCD's
// ownership marker, and a gate that renders one key differently beats a gate
// whose answer depends on its configuration.
const managedByAnnotation = "managed-by"

func dropManagedBy(m map[string]string) map[string]string {
	delete(m, managedByAnnotation)
	return m
}

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
