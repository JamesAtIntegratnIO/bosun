package cluster

import (
	"context"
	"fmt"
	"strings"
)

// The Applications and ApplicationSets ArgoCD serves, and the URL comparison
// that decides which of them belong to the repository being gated.
//
// This is the read half of ADR 0012: live supplies the pointers and the root
// identities, the pull request's checkout supplies the content. Nothing here
// renders anything or decides what to render; it reports what ArgoCD says is
// deployed, in the smallest shape that answers that question. Nothing calls
// either read yet, and the chart does not ask for the RBAC they need until
// something does.
//
// Both endpoints ride the same account token and the same client as
// ClusterInventory. The grant is two lines in `argocd-rbac-cm` and no
// Kubernetes RBAC at all, which is the reason this route exists rather than
// listing the CRs through the apiserver.

// trackingAnnotation is how ArgoCD marks an object it created and manages.
//
// Its absence is the whole of the root test. Measured on the production
// install, 2 of 60 ApplicationSets lacked it and both were the
// Terraform-applied bootstraps: everything else in the fleet was created by
// something ArgoCD already tracks. So an ApplicationSet without it is a root,
// reachable by no other means, and one with it is reachable by following what
// serves it.
const trackingAnnotation = "argocd.argoproj.io/tracking-id"

// AppSource is one entry of an Application's `spec.sources`, reduced to the
// fields a render needs.
//
// `spec.source` (singular) and `spec.sourceHydrator.drySource` decode into
// this same type, because they are the same shape wearing three names and a
// caller that had to know which one it was holding would get it wrong once.
type AppSource struct {
	RepoURL        string `json:"repoURL"`
	Path           string `json:"path"`
	TargetRevision string `json:"targetRevision"`

	// Chart is set instead of Path when the source is a chart from a
	// repository rather than a directory in one. A source with a Chart points
	// at somebody else's artifact, not at the gated repository's content.
	Chart string `json:"chart"`

	// Ref names this source so the Application's other sources can address it
	// as `$ref/…`.
	// This is what retires the repository-wide `valuesRef` guess: the
	// Application says which of its own sources holds its values.
	Ref string `json:"ref"`

	Directory *AppDirectory `json:"directory"`
	Helm      *AppHelm      `json:"helm"`
}

// AppDirectory is ArgoCD's `directory` block: how it walks a path that holds
// plain manifests rather than a chart.
type AppDirectory struct {
	Recurse bool   `json:"recurse"`
	Include string `json:"include"`
	Exclude string `json:"exclude"`
}

// AppHelm is the part of ArgoCD's `helm` block that changes what renders.
type AppHelm struct {
	ValueFiles []string `json:"valueFiles"`
	// ValuesObject is values written inline in the Application rather than
	// held in a file. There is no file in the checkout to read for these, so
	// a render that ignored them would render a chart nobody deploys.
	ValuesObject map[string]any `json:"valuesObject"`
}

// Application is one ArgoCD Application, reduced to what derivation reads.
type Application struct {
	Name      string
	Namespace string
	Project   string

	// TrackingID is the tracking annotation's value, empty when absent.
	TrackingID string

	// Sources is every source this Application has, with the singular
	// `spec.source` folded in as a one-element list so callers have one shape.
	Sources []AppSource

	// DrySource is `spec.sourceHydrator.drySource`: where the manifests are
	// written by hand, as opposed to the hydrated branch ArgoCD syncs from.
	// It is the source that points at the repository a pull request is opened
	// against, so it has to be matched like any other.
	DrySource *AppSource
}

// ApplicationSet is one ArgoCD ApplicationSet.
//
// The whole object is kept rather than a decoded subset, because expanding one
// means handing it to the gate's own ApplicationSet expander, which reads the
// same map a committed manifest parses into. Decoding it into a struct here
// would be a second parser for a shape that already has one, and the two would
// drift.
type ApplicationSet struct {
	Name      string
	Namespace string

	// TrackingID empty means this is a root: nothing created it, so nothing
	// leads to it, so it is reachable only by being named.
	TrackingID string

	// Object is the ApplicationSet as served, `apiVersion` through `status`.
	Object map[string]any
}

// IsRoot reports whether nothing in ArgoCD created this ApplicationSet.
func (a ApplicationSet) IsRoot() bool { return a.TrackingID == "" }

// Applications reads every Application ArgoCD serves.
//
// Deliberately unfiltered. ArgoCD's own `?repo=` filter looked like the right
// tool and is not: v3.5.1 compares `spec.source.repoURL` by exact string
// equality against the first source only, so pointed at the production
// repository it returned 7 of 65 Applications, missing every multi-source
// Application whose first source is a chart and every URL spelt differently
// from the one asked for. Filtering happens here, after normalisation, across
// every source.
func (a *ArgoCD) Applications(ctx context.Context) ([]Application, error) {
	var out struct {
		Items []struct {
			Metadata objectMeta `json:"metadata"`
			Spec     struct {
				Project        string      `json:"project"`
				Source         *AppSource  `json:"source"`
				Sources        []AppSource `json:"sources"`
				SourceHydrator *struct {
					DrySource *AppSource `json:"drySource"`
				} `json:"sourceHydrator"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := a.get(ctx, "/api/v1/applications", needApplications, &out); err != nil {
		return nil, fmt.Errorf("reading Applications from the ArgoCD API at %s: %w", a.base(), err)
	}

	apps := make([]Application, 0, len(out.Items))
	for _, item := range out.Items {
		app := Application{
			Name:       item.Metadata.Name,
			Namespace:  item.Metadata.Namespace,
			Project:    item.Spec.Project,
			TrackingID: item.Metadata.Annotations[trackingAnnotation],
			Sources:    item.Spec.Sources,
		}
		// ArgoCD accepts either the singular or the plural and stores what it
		// was given, so both arrive on the wire in the wild. Folding the
		// singular in here means every reader below sees a list.
		if item.Spec.Source != nil {
			app.Sources = append([]AppSource{*item.Spec.Source}, app.Sources...)
		}
		if item.Spec.SourceHydrator != nil {
			app.DrySource = item.Spec.SourceHydrator.DrySource
		}
		apps = append(apps, app)
	}
	return apps, nil
}

// ApplicationSets reads every ApplicationSet ArgoCD serves.
func (a *ArgoCD) ApplicationSets(ctx context.Context) ([]ApplicationSet, error) {
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := a.get(ctx, "/api/v1/applicationsets", needApplicationSets, &out); err != nil {
		return nil, fmt.Errorf("reading ApplicationSets from the ArgoCD API at %s: %w", a.base(), err)
	}

	sets := make([]ApplicationSet, 0, len(out.Items))
	for _, obj := range out.Items {
		name, ns, ann := metaOf(obj)
		sets = append(sets, ApplicationSet{
			Name:       name,
			Namespace:  ns,
			TrackingID: ann[trackingAnnotation],
			Object:     obj,
		})
	}
	return sets, nil
}

// objectMeta is the part of a Kubernetes object's metadata read here.
type objectMeta struct {
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace"`
	Annotations map[string]string `json:"annotations"`
}

// metaOf pulls name, namespace and annotations out of an object held as a map.
//
// Every step is guarded rather than asserted: this decodes whatever the API
// served, and a panic in the gate is a pull request with no verdict.
func metaOf(obj map[string]any) (name, namespace string, annotations map[string]string) {
	annotations = map[string]string{}
	md, _ := obj["metadata"].(map[string]any)
	if md == nil {
		return "", "", annotations
	}
	name, _ = md["name"].(string)
	namespace, _ = md["namespace"].(string)
	if raw, ok := md["annotations"].(map[string]any); ok {
		for k, v := range raw {
			if s, ok := v.(string); ok {
				annotations[k] = s
			}
		}
	}
	return name, namespace, annotations
}

// PointsAt reports whether any of this Application's sources is the given
// repository, comparing normalised URLs.
//
// Every source is checked, including the dry source, because which position a
// repository occupies in the list is an authoring detail: a multi-source
// Application routinely lists the chart first and the values repository
// second, and matching only the first is how ArgoCD's own filter misses most
// of the fleet.
func (app Application) PointsAt(repoURL string) bool {
	want := normaliseRepoURL(repoURL)
	if want == "" {
		return false
	}
	for _, s := range app.Sources {
		if normaliseRepoURL(s.RepoURL) == want {
			return true
		}
	}
	return app.DrySource != nil && normaliseRepoURL(app.DrySource.RepoURL) == want
}

// normaliseRepoURL reduces the spellings of one repository to a single string.
//
// The same repository is written at least three ways in a live fleet, and all
// three were observed on the production install: with and without a `.git`
// suffix, with the host or the organisation in a different case, and in scp
// form (`git@host:org/repo`) rather than as a URL. ArgoCD stores whatever it
// was given and compares by string equality, so without this a derived scope
// silently omits every Application whose author typed it differently.
//
// The path is lowercased along with the host, which is a deliberate
// over-normalisation. Git paths are case-sensitive in principle, and on every
// host this gate is aimed at (GitHub, GitLab, Bitbucket, Gitea) a repository
// cannot exist in two cases at once, so folding case can only merge spellings
// of one repository, never conflate two. Getting this wrong in the other
// direction, treating `Org/Repo` and `org/repo` as different repositories,
// drops sources from the scope with no symptom at all.
//
// An explicit port is kept, and that is the one place this deliberately stops
// short: `https://git.example:8443/org/repo` and `https://git.example/org/repo`
// are different endpoints, and a self-hosted install reachable on both is
// rarer than one where they are two different servers. Merging them would be
// wrong in the direction that puts somebody else's manifests in the scope.
//
// A string this cannot parse comes back trimmed rather than empty, so an
// unfamiliar spelling compares equal to itself and an exact match still works.
func normaliseRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	// scp-like syntax: git@host:org/repo, which is not a URL and does not
	// parse as one. Recognised before anything else because the colon it uses
	// as a separator is a port separator everywhere else.
	if !strings.Contains(s, "://") {
		if at := strings.Index(s, "@"); at >= 0 {
			if colon := strings.Index(s[at:], ":"); colon >= 0 {
				host := s[at+1 : at+colon]
				path := s[at+colon+1:]
				s = "https://" + host + "/" + strings.TrimPrefix(path, "/")
			}
		}
	}

	// The scheme is dropped rather than normalised. ssh://, https:// and git://
	// against one host are one repository, and a comparison that kept the
	// scheme would treat an Application cloned over ssh as a different source
	// from the same repository added over https.
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	// Userinfo is an access detail, not part of the repository's identity.
	if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	return strings.ToLower(strings.TrimRight(s, "/"))
}
