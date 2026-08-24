package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Default locations of the projected service account, which the chart mounts
// by never turning it off. Overridable so tests need no filesystem.
const (
	saDir       = "/var/run/secrets/kubernetes.io/serviceaccount"
	defaultHost = "https://kubernetes.default.svc"
	// maxItems bounds a count. Past it the answer becomes a floor, which is
	// still a useful sentence -- "at least 2500 objects are live on a version
	// this chart stops serving" ends the same conversation as the exact
	// number.
	maxItems = 2500
	pageSize = 500
)

// APIServer reads the Kubernetes API from inside the cluster.
//
// The token is re-read from disk ON EVERY REQUEST, and that is not caution.
// Projected service-account tokens are BOUND tokens: they expire in about an
// hour and the kubelet rewrites the file in place. A client that read it once
// at start-up works beautifully for fifty minutes and then 401s forever, which
// on a service called a few times a day means it looks fine in every test and
// is broken in production by lunchtime. The GitHub client already learned this
// with App installation tokens; this is the same shape and the same answer.
//
// The CA is cached, because that one does not rotate under a running pod.
type APIServer struct {
	// Host is the apiserver. Defaults to the in-cluster Service DNS name.
	Host string
	// Dir is the mounted service-account directory.
	Dir string
	// ArgoCDNamespace is where Applications live.
	ArgoCDNamespace string
	// HTTP is injectable for tests. When set, the CA is not read.
	HTTP *http.Client

	once sync.Once
	cl   *http.Client
	err  error
}

func (a *APIServer) Name() string { return "kubernetes" }

func (a *APIServer) dir() string {
	if a.Dir != "" {
		return a.Dir
	}
	return saDir
}

func (a *APIServer) host() string {
	if a.Host != "" {
		return strings.TrimRight(a.Host, "/")
	}
	return defaultHost
}

// Namespace is the namespace the pod runs in, from the mounted file.
func (a *APIServer) Namespace() (string, error) {
	b, err := os.ReadFile(a.dir() + "/namespace")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// token is the current bearer token. Read per call: see the type comment.
func (a *APIServer) token() (string, error) {
	b, err := os.ReadFile(a.dir() + "/token")
	if err != nil {
		return "", fmt.Errorf("reading the service account token: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func (a *APIServer) client() (*http.Client, error) {
	if a.HTTP != nil {
		return a.HTTP, nil
	}
	a.once.Do(func() {
		pem, err := os.ReadFile(a.dir() + "/ca.crt")
		if err != nil {
			a.err = fmt.Errorf("reading the cluster CA: %w", err)
			return
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			a.err = fmt.Errorf("the mounted cluster CA is not a certificate")
			return
		}
		// No InsecureSkipVerify escape hatch, deliberately. The CA is mounted
		// into every pod by the kubelet; a deployment that cannot verify the
		// apiserver with it has a problem that skipping verification hides.
		a.cl = &http.Client{
			Timeout:   20 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
		}
	})
	return a.cl, a.err
}

// Check proves the reader works, at start-up rather than on the first pull
// request.
//
// Same rule as the App's key: a misconfiguration should be a pod that will not
// start, which somebody notices, rather than a triage that quietly says "not
// permitted to check" forever -- which is a sentence this deliberately makes
// harmless, and therefore one nobody would chase.
func (a *APIServer) Check(ctx context.Context) error {
	var out struct {
		Versions []string `json:"versions"`
	}
	if err := a.get(ctx, "/version", &out); err != nil {
		// /version is unauthenticated on most clusters, so a failure here is
		// the network or the CA rather than RBAC. Both are worth dying on.
		return fmt.Errorf("reading %s/version: %w", a.host(), err)
	}
	if _, err := a.token(); err != nil {
		return err
	}
	return nil
}

func (a *APIServer) get(ctx context.Context, path string, out any) error {
	cl, err := a.client()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.host()+path, nil)
	if err != nil {
		return err
	}
	if tok, err := a.token(); err == nil && tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &statusError{Path: path, Code: resp.StatusCode, Status: resp.Status}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// statusError carries the code, so "not permitted" and "not served" can be
// told apart without matching on prose. They mean opposite things: one is a
// question nobody was allowed to ask, the other is an answer.
type statusError struct {
	Path   string
	Code   int
	Status string
}

func (e *statusError) Error() string { return fmt.Sprintf("%s: %s", e.Path, e.Status) }

func code(err error) int {
	var se *statusError
	if ok := asStatus(err, &se); ok {
		return se.Code
	}
	return 0
}

func asStatus(err error, target **statusError) bool {
	for err != nil {
		if se, ok := err.(*statusError); ok {
			*target = se
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// CountLive walks the collection and counts it.
//
// A bounded LIST rather than a trust of metadata.remainingItemCount: the
// apiserver only sets that field for etcd-served lists, not the default
// watch-cache path, and documents it as best-effort. Reading its absence as
// zero would under-count and then present the wrong number as a fact, which is
// the one thing a live read must never do.
func (a *APIServer) CountLive(ctx context.Context, group, version, plural string) Count {
	where := gvk(group, version, plural)
	n, cont := 0, ""
	for {
		path := listPath(group, version, plural) + fmt.Sprintf("?limit=%d", pageSize)
		if cont != "" {
			path += "&continue=" + url.QueryEscape(cont)
		}
		var l list
		if err := a.get(ctx, path, &l); err != nil {
			switch code(err) {
			case http.StatusForbidden, http.StatusUnauthorized:
				// The honest answer, and a soft one. An operator who scoped
				// the ClusterRole to named API groups will see this for a
				// group they did not list, which is a values edit rather than
				// an incident.
				return Count{Note: "not permitted to check " + where}
			case http.StatusNotFound:
				// The cluster does not serve it at all. For a CRD removed
				// outright that is the answer, not a failure -- and it is
				// exactly the case where asking apiextensions instead would
				// have 404'd too.
				return Count{Known: true, Note: "the cluster does not serve " + where}
			default:
				return Count{Note: fmt.Sprintf("could not check %s (%v)", where, err)}
			}
		}
		n += len(l.Items)
		cont = l.Metadata.Continue
		if cont == "" {
			return Count{N: n, Known: true}
		}
		if n >= maxItems {
			// A floor ends the same conversation as a total. Walking a
			// hundred thousand objects to turn "a lot" into a number would
			// not.
			return Count{N: n, Known: true, AtLeast: true}
		}
	}
}

// CRD reads the versions a CustomResourceDefinition currently serves.
func (a *APIServer) CRD(ctx context.Context, name string) CRD {
	var crd struct {
		Spec struct {
			Versions []struct {
				Name   string `json:"name"`
				Served bool   `json:"served"`
				Schema struct {
					OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	path := "/apis/apiextensions.k8s.io/v1/customresourcedefinitions/" + url.PathEscape(name)
	if err := a.get(ctx, path, &crd); err != nil {
		switch code(err) {
		case http.StatusForbidden, http.StatusUnauthorized:
			return CRD{Note: "not permitted to read CustomResourceDefinitions"}
		case http.StatusNotFound:
			// Already absent. Nothing is stored under it, which is an answer
			// and a reassuring one.
			return CRD{Known: true, Note: "the cluster has no " + name}
		default:
			return CRD{Note: fmt.Sprintf("could not read %s (%v)", name, err)}
		}
	}
	out := CRD{Known: true, Schemas: map[string]map[string]any{}}
	for _, v := range crd.Spec.Versions {
		if v.Served {
			out.Versions = append(out.Versions, v.Name)
		}
		// Schemas for every version, served or not. The version a document is
		// migrating OFF may already be unserved in this cluster while the
		// repository still declares it, and that is the shape most worth
		// having.
		if len(v.Schema.OpenAPIV3Schema) > 0 {
			out.Schemas[v.Name] = v.Schema.OpenAPIV3Schema
		}
	}
	return out
}

// AppHealth reads one ArgoCD Application.
func (a *APIServer) AppHealth(ctx context.Context, name string) Health {
	ns := a.ArgoCDNamespace
	if ns == "" {
		ns = "argocd"
	}
	var app struct {
		Status struct {
			Health struct {
				Status string `json:"status"`
			} `json:"health"`
			Sync struct {
				Status string `json:"status"`
			} `json:"sync"`
		} `json:"status"`
	}
	path := fmt.Sprintf("/apis/argoproj.io/v1alpha1/namespaces/%s/applications/%s",
		url.PathEscape(ns), url.PathEscape(name))
	if err := a.get(ctx, path, &app); err != nil {
		switch code(err) {
		case http.StatusForbidden, http.StatusUnauthorized:
			return Health{Note: "not permitted to read Applications in " + ns}
		case http.StatusNotFound:
			// Worth its own sentence: an Application the promotion says it
			// will verify and the cluster does not have is a finding, not an
			// absence of one.
			return Health{Known: true, Note: "no Application " + name + " in " + ns}
		default:
			return Health{Note: fmt.Sprintf("could not read Application %s (%v)", name, err)}
		}
	}
	h := Health{Status: app.Status.Health.Status, Sync: app.Status.Sync.Status, Known: true}
	if h.Status == "" && h.Sync == "" {
		return Health{Known: true, Note: "Application " + name + " reports no status yet"}
	}
	return h
}
