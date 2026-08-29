package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gate"
)

// ArgoCD reads the cluster inventory from the ArgoCD API server. It is the
// gate's only inventory source.
//
// WHY THE API AND NOT THE SECRETS those clusters are stored in. Reading them
// needs get/list on Secrets in the ArgoCD namespace, and that grant cannot be
// made smaller. The gate wants four fields -- name, server, labels,
// annotations -- and Kubernetes RBAC has no predicate that expresses "the
// labels but not the data": there are no deny rules, `resourceNames` does not
// apply to `list` (a list request carries no name for the authorizer to
// match), and the label selector in the request URL is a filter the apiserver
// applies AFTER authorising, so a token holding the Role can simply drop it
// and read argocd-secret and every repository credential beside it.
//
// ArgoCD already solved this for its own API: `GET /api/v1/clusters` serves
// exactly those four fields and redacts the credential block, so the
// authorisation happens somewhere that CAN express the distinction.
//
// WHAT IT COSTS, stated as plainly as the grant it replaces. A credential to
// mint, store and rotate -- an ArgoCD account token, which is
// bearer-equivalent for whatever that account's ArgoCD RBAC permits, so it
// gets `clusters, get` and nothing else. A dependency on the ArgoCD API
// server being up: the apiserver is reachable whenever the cluster is,
// whereas argocd-server can be down on its own. And its own TLS story,
// because argocd-server serves its own certificate rather than the one the
// kubelet mounts into every pod.
type ArgoCD struct {
	// BaseURL is the ArgoCD API server, e.g. https://argocd-server.argocd.svc.
	BaseURL string
	// Token is the ArgoCD account token. Held in memory rather than re-read
	// per call, unlike the projected service-account token: this one is a
	// static credential from a Secret, not a bound token the kubelet rewrites
	// hourly, so re-reading it would be cargo cult.
	Token string
	// CAFile verifies the ArgoCD server. Empty uses the system roots.
	CAFile string
	// InsecureSkipTLSVerify accepts any certificate. argocd-server's default
	// certificate is self-signed, so a cluster that has not given it a real
	// one needs either this or the CA above -- and which of those an operator
	// can produce is a fact about their install, not a preference this can
	// have an opinion about.
	InsecureSkipTLSVerify bool
	// HTTP is injectable for tests.
	HTTP *http.Client

	once sync.Once
	cl   *http.Client
	err  error
}

// argoCluster is the subset of ArgoCD's Cluster the inventory is built from.
//
// The `config` field is deliberately absent. ArgoCD redacts it before serving
// it, but leaving it out of the struct entirely means this cannot hold a
// credential even for the lifetime of a decode, and a reader checking whether
// the claim above is true can settle it by reading this type.
type argoCluster struct {
	Name        string            `json:"name"`
	Server      string            `json:"server"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
}

// ClusterInventory reads the inventory the gate expands generators against.
//
// It returns an error for the same reason the Secret-backed reader does, and
// the reason is worth repeating rather than cross-referencing: an unreadable
// inventory does not make a verdict poorer, it makes it WRONG. A render
// against a world the gate could not see finds no targeting change and waves
// everything through. Every other read in this package degrades to "not
// permitted to check"; this one refuses.
func (a *ArgoCD) ClusterInventory(ctx context.Context) (*gate.Inventory, error) {
	var out struct {
		Items []argoCluster `json:"items"`
	}
	if err := a.get(ctx, "/api/v1/clusters", &out); err != nil {
		return nil, fmt.Errorf("reading clusters from the ArgoCD API at %s: %w", a.base(), err)
	}

	cs := make([]gate.Cluster, 0, len(out.Items))
	for _, item := range out.Items {
		// ArgoCD synthesises the implicit local cluster into this list rather
		// than omitting it, and it arrives with no labels -- the same entry
		// the Secret-backed reader invents when it finds no Secrets at all.
		// It must NOT pass through InventoryFromClusters, which stamps every
		// entry with the secret-type label that only a real Secret carries: a
		// selector matching on that label excludes the local cluster in
		// ArgoCD, and an inventory that said otherwise would target a
		// cluster ArgoCD would not.
		if isImplicitLocal(item) {
			continue
		}
		cs = append(cs, gate.Cluster{
			Name:        item.Name,
			Server:      item.Server,
			Labels:      item.Labels,
			Annotations: item.Annotations,
		})
	}

	// Same filter as the live Secret read -- the zero value, which keeps
	// everything. Filtering exists to stabilise a snapshot against churn, and
	// a live read is never diffed against anything.
	inv := gate.InventoryFromClusters(cs, gate.ExportFilter{})
	if len(inv.Clusters) == 0 {
		return implicitLocalCluster(), nil
	}
	return inv, nil
}

// isImplicitLocal recognises the entry ArgoCD adds for the cluster it runs in.
//
// Identified by shape rather than by name, because the name is not reliably
// `in-cluster` and the address is: ArgoCD's local cluster is the internal
// apiserver address with no labels, and a local cluster somebody registered a
// real Secret for carries the secret-type label like any other.
func isImplicitLocal(c argoCluster) bool {
	return strings.TrimRight(c.Server, "/") == "https://kubernetes.default.svc" && len(c.Labels) == 0
}

func (a *ArgoCD) base() string { return strings.TrimRight(a.BaseURL, "/") }

func (a *ArgoCD) client() (*http.Client, error) {
	if a.HTTP != nil {
		return a.HTTP, nil
	}
	a.once.Do(func() {
		tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
		switch {
		case a.InsecureSkipTLSVerify:
			// An escape hatch the apiserver reader deliberately does not have,
			// and the asymmetry is not an inconsistency. The kubelet mounts a
			// CA that verifies the apiserver into every pod, so skipping
			// verification there only ever hides a problem. Nothing mounts a
			// CA for argocd-server, whose default certificate is self-signed
			// -- refusing to talk to it would not make an install safer, it
			// would make this source unusable on the setup it is most needed
			// on.
			tlsCfg.InsecureSkipVerify = true
		case a.CAFile != "":
			pem, err := os.ReadFile(a.CAFile)
			if err != nil {
				a.err = fmt.Errorf("reading the ArgoCD CA %s: %w", a.CAFile, err)
				return
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				a.err = fmt.Errorf("%s is not a certificate", a.CAFile)
				return
			}
			tlsCfg.RootCAs = pool
		}
		a.cl = &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}
	})
	return a.cl, a.err
}

func (a *ArgoCD) get(ctx context.Context, path string, out any) error {
	cl, err := a.client()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.base()+path, nil)
	if err != nil {
		return err
	}
	if a.Token != "" {
		req.Header.Set("Authorization", "Bearer "+a.Token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The two failures worth naming, because the fix differs and the
		// status code is the only thing that tells them apart.
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("%s: the ArgoCD token was rejected (expired, or minted in another ArgoCD)", resp.Status)
		case http.StatusForbidden:
			return fmt.Errorf("%s: the ArgoCD account may not list clusters -- it needs `p, <account>, clusters, get, *, allow`", resp.Status)
		}
		return &statusError{Path: path, Code: resp.StatusCode, Status: resp.Status}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// implicitLocalCluster is what an ArgoCD managing only the cluster it runs in
// looks like: no Secret at all, because the local cluster is implicit.
// ArgoCD's own clusters generator still includes it, as `in-cluster` with no
// labels, so an inventory that mirrors ArgoCD says the same thing. No labels
// is faithful, not lazy: a selector that matches on a label this entry lacks
// excludes it in ArgoCD too, and the inventory validator will say so out loud.
//
// It is written here rather than read from anywhere because it is the one
// entry the API does not return.
func implicitLocalCluster() *gate.Inventory {
	return &gate.Inventory{Clusters: []gate.Cluster{{
		Name:        "in-cluster",
		Server:      "https://kubernetes.default.svc",
		Labels:      map[string]string{},
		Annotations: map[string]string{},
	}}}
}
