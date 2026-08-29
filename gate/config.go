package gate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// Config is.gitops-gate.yaml; everything the gate needs to know about a
// repository it has never seen. The gate itself knows nothing about any
// particular layout; this file is the whole of that knowledge.
type Config struct {
	// Sources describe how to obtain the Applications and ApplicationSets this
	// repository defines. A repository usually has more than one shape at
	// once, some things committed as plain YAML, some rendered from a chart,
	// so this is a list of strategies rather than a single mode.
	Sources []Source `json:"sources"`

	// Bootstraps is the older single-strategy form, kept working. It is
	// exactly equivalent to one `type: argocd-bootstrap` source each.
	Bootstraps []Bootstrap `json:"bootstraps"`

	// Roots are paths to root ApplicationSets that live in this repository.
	//
	// The one fact ArgoCD cannot supply, and the whole reason a config file
	// still exists. A root is an ApplicationSet nothing in ArgoCD created, so
	// it carries no tracking annotation and there is nothing to follow to it;
	// naming its file here means its edits are gated from this pull request's
	// content rather than from the spec that is already applied, and means a
	// root this pull request *introduces* is rendered at all.
	//
	// Each entry suppresses the live-spec fallback for the identity its file
	// declares, so naming a root that also exists live renders it once, from
	// head.
	Roots []string `json:"roots"`

	// ValuesRef is the `ref:` name a multi-source Application gives its values
	// source, so `$values/…` paths map back to repo paths.
	//
	// Only consulted for sources written in this file. A derived source
	// resolves `$ref/…` through the ref the Application itself declares, which
	// is per-Application and exact where this is one guess applied to all of
	// them at once.
	ValuesRef string `json:"valuesRef"`

	// Concurrency caps parallel renders. Fleets are the reason this exists: a
	// fifty-cluster inventory is fifty chart renders per revision, and serial
	// execution turns a ninety-second gate into a coffee break.
	//
	// ParseConfig defaults this to 8 and clamps nothing. Read it through
	// workers() rather than directly, for two reasons: a Config built as a
	// literal rather than parsed leaves it zero, and a zero-capacity
	// semaphore is not "no limit", it is a channel nobody can send on; and
	// this number comes out of the repository being gated, so workers() caps
	// it at maxConcurrency before anything acts on it.
	Concurrency int `json:"concurrency"`

	// Validate controls schema validation.
	Validate ValidateConfig `json:"validate"`

	// Egress is the operator's outbound deny-list, applied before helm pulls a
	// remote chart. Nil is open.
	//
	// Not from the config file; it is the host's policy, not the reviewed
	// repository's, and a pull request that could widen its own egress rules
	// would be the deny-list configuring itself.
	Egress EgressPolicy `json:"-"`

	// Log records outbound destinations. Nil keeps no record, which an
	// operator should have to choose rather than get by omission.
	Log func(string, ...any) `json:"-"`

	// ClustersExport holds the one key left under a name that has outlived
	// its export: knownAbsentLabels is render configuration, read by
	// Inventory.Validate. The key stays `clustersExport` because renaming it
	// would break every config that sets it, for tidiness.
	ClustersExport ClustersExportConfig `json:"clustersExport"`
}

// SourceType is how a source yields Applications and ApplicationSets.
type SourceType string

const (
	// SourceManifests reads committed YAML. Applications and ApplicationSets
	// checked into a directory is the most common ArgoCD layout there is, and
	// the one the first version of this gate could not read at all.
	SourceManifests SourceType = "manifests"

	// SourceHelm renders a chart, optionally once per matching cluster when
	// its values depend on cluster metadata.
	SourceHelm SourceType = "helm"

	// SourceKustomize builds an overlay.
	SourceKustomize SourceType = "kustomize"

	// SourceRendered reads manifests that are already rendered in git, the
	// rendered-manifests pattern, whether produced by ArgoCD's source
	// hydrator, by Kargo's helm-template/kustomize-build promotion steps, or
	// by any CI job that commits its output.
	//
	// These are deployed objects, not Applications. That makes an
	// object-level diff possible: which Deployments, CRDs and NetworkPolicies
	// appear, vanish or change. It is the strongest signal available,
	// because it is the real answer rather than a reconstruction of it.
	SourceRendered SourceType = "rendered"

	// SourceArgoCDBootstrap derives a helm source from an app-of-apps
	// ApplicationSet, the gitops-bridge shape, where cluster metadata on the
	// ArgoCD cluster Secret drives which chart is rendered and with what.
	SourceArgoCDBootstrap SourceType = "argocd-bootstrap"

	// SourceDirectory reads one path with ArgoCD's own `directory` semantics:
	// recurse or not, and an include/exclude glob pair.
	//
	// Distinct from `manifests`, which takes glob patterns of this
	// repository's choosing. This one takes the path an Application points at
	// and the rules ArgoCD applies to it, so what the gate reads is what
	// ArgoCD reads rather than an approximation somebody maintains by hand.
	// It is the shape most derived Applications land in.
	SourceDirectory SourceType = "directory"

	// SourceLive carries manifests the caller supplies rather than any this
	// repository holds.
	//
	// It has exactly one producer: the fallback for an untracked
	// ApplicationSet whose manifest is not in the gated repository, where
	// the applied spec is the only copy there is. Configuration cannot set it,
	// and ParseConfig refuses it by name, because a source that renders
	// whatever it was handed would be a way to put content into the verdict
	// that no revision of this repository contains.
	SourceLive SourceType = "live"
)

// Source is one way to obtain manifests.
type Source struct {
	Name string     `json:"name"`
	Type SourceType `json:"type"`

	// Paths are glob patterns, for `manifests`.
	Paths []string `json:"paths"`

	// Path is a directory, for `kustomize` and `argocd-bootstrap`.
	Path string `json:"path"`

	// Chart and ValueFiles are for `helm`. Both may contain {{.name }},
	// {{.labels.x }} and {{.annotations.y }} placeholders, resolved per
	// cluster, which is what makes a per-environment values layout
	// expressible without listing every combination by hand.
	Chart      string   `json:"chart"`
	ValueFiles []string `json:"valueFiles"`

	// ValuesInline is values written into an Application rather than into a
	// file, ArgoCD's `helm.valuesObject`. There is no file in the checkout to
	// read for these, so a render that ignored them would render a chart
	// nobody deploys. Written to a temporary file and passed as one more `-f`,
	// last, which is the precedence ArgoCD gives it.
	ValuesInline map[string]any `json:"valuesInline"`

	// Recurse, Include and Exclude are ArgoCD's `directory` semantics, for
	// `directory`. Recurse false reads the path's own files and no
	// subdirectory; the two globs are matched against each file's path
	// relative to the source path, exclude winning over include.
	Recurse bool   `json:"recurse"`
	Include string `json:"include"`
	Exclude string `json:"exclude"`

	// Objects are manifests supplied by the caller, for `live`. Not settable
	// from configuration, which is what the missing tag says.
	Objects []map[string]any `json:"-"`

	// Selector limits which clusters this source is rendered for. Omitted, a
	// helm source renders once with no cluster context; a manifests or
	// kustomize source is cluster-independent anyway.
	Selector *SourceSelector `json:"selector"`

	// Scope decides which clusters the ApplicationSets from a per-cluster
	// render expand against.
	//
	//  fleet (default): the whole inventory. Correct for hub-and-spoke,
	//  where one ArgoCD holds every cluster and an ApplicationSet rendered
	//  under one cluster's values can still generate Applications for
	//  others. This is the gitops-bridge shape.
	//  cluster: only the cluster it was rendered for. Correct where each
	//  cluster runs its own ArgoCD and therefore only ever sees itself.
	//
	// Getting this wrong is quiet rather than loud, which is why it is
	// explicit: under `fleet` a chart rendered per cluster yields the same
	// ApplicationSet name several times with different contents, and whichever
	// arrives first wins.
	Scope string `json:"scope"`
}

// SourceSelector is a label selector over the cluster inventory.
type SourceSelector struct {
	MatchLabels map[string]string `json:"matchLabels"`
}

type Bootstrap struct {
	// Path to the bootstrap ApplicationSet manifest, relative to the repo root.
	Path string `json:"path"`
	// Name is a short label used in output. Defaults to the file's base name.
	Name string `json:"name"`
}

type ClustersExportConfig struct {
	// KnownAbsentLabels are label keys a selector matches on that no cluster
	// is expected to carry. Without this the gate refuses to render, on the
	// grounds that it usually means the selector and the fleet have drifted
	// apart.
	KnownAbsentLabels []string `json:"knownAbsentLabels"`
}

type ValidateConfig struct {
	// Enabled turns kubeconform on. Requires the binary on PATH.
	Enabled bool `json:"enabled"`
	// SchemaLocations are passed to kubeconform as -schema-location.
	SchemaLocations []string `json:"schemaLocations"`
	// IgnoreMissingSchemas is almost always required: CRDs from smaller
	// projects are not in any published schema catalogue, and without this a
	// single unknown kind fails the whole run.
	IgnoreMissingSchemas bool `json:"ignoreMissingSchemas"`
	// SkipKinds are kinds to skip entirely.
	SkipKinds []string `json:"skipKinds"`
}

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return ParseConfig(raw, path)
}

// ParseConfig parses and normalises a config already read from somewhere, a
// file, or a git revision that is not checked out.
func ParseConfig(raw []byte, path string) (*Config, error) {
	var c Config
	if err := yaml.UnmarshalStrict(raw, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if c.ValuesRef == "" {
		c.ValuesRef = "values"
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}

	// `bootstraps` is the older form. Fold it into sources so the rest of the
	// program only has one shape to understand.
	for i, b := range c.Bootstraps {
		if b.Path == "" {
			return nil, fmt.Errorf("%s: bootstraps[%d].path is required", path, i)
		}
		name := b.Name
		if name == "" {
			name = defaultName(b.Path)
		}
		c.Sources = append(c.Sources, Source{
			Name: name, Type: SourceArgoCDBootstrap, Path: b.Path,
		})
	}
	c.Bootstraps = nil

	// No requirement that anything be listed. Since ADR 0012 the file is not
	// the only thing that says what to render, so an empty one is a
	// repository that derives its whole scope, and a file holding nothing but
	// `roots:` or `validate:` is an ordinary shape rather than a mistake. The
	// refusal for "nothing to render at all" belongs where the derivation is
	// also known, and lives there.
	for i := range c.Sources {
		if err := c.Sources[i].validate(path, i); err != nil {
			return nil, err
		}
	}
	return &c, nil
}

// validate defaults a source's name and checks that its type has the fields
// that type needs.
//
// Named for what it does rather than `normalise`: the same package used that
// word for stripping version stamps off a rendered object, and one word
// meaning two unrelated things in one package is a word a reader has to look
// up every time.
func (s *Source) validate(cfgPath string, i int) error {
	if s.Name == "" {
		switch {
		case s.Path != "":
			s.Name = defaultName(s.Path)
		case s.Chart != "":
			s.Name = defaultName(s.Chart)
		default:
			s.Name = fmt.Sprintf("sources[%d]", i)
		}
	}
	switch s.Type {
	case SourceManifests, SourceRendered:
		if len(s.Paths) == 0 {
			return fmt.Errorf("%s: source %q is type %s and needs `paths`", cfgPath, s.Name, s.Type)
		}
	case SourceHelm:
		if s.Chart == "" {
			return fmt.Errorf("%s: source %q is type helm and needs `chart`", cfgPath, s.Name)
		}
	case SourceKustomize, SourceArgoCDBootstrap, SourceDirectory:
		if s.Path == "" {
			return fmt.Errorf("%s: source %q is type %s and needs `path`", cfgPath, s.Name, s.Type)
		}
	case SourceLive:
		// validate runs on sources that came out of a file, and only on
		// those, which is what makes this the right place to refuse the one
		// type a file may not set.
		return fmt.Errorf("%s: source %q is type live, which configuration cannot set. "+
			"It is how the gate carries an ApplicationSet ArgoCD serves but this repository "+
			"does not contain", cfgPath, s.Name)
	case "":
		return fmt.Errorf("%s: source %q has no `type` (%s)", cfgPath, s.Name, sourceTypeList())
	default:
		return fmt.Errorf("%s: source %q has unknown type %q", cfgPath, s.Name, s.Type)
	}
	return nil
}

func defaultName(p string) string {
	base := filepath.Base(p)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// matches reports whether a cluster is in this source's scope.
func (s *Source) matches(c Cluster) bool {
	if s.Selector == nil {
		return true
	}
	for k, v := range s.Selector.MatchLabels {
		if c.Labels[k] != v {
			return false
		}
	}
	return true
}

// defaultConcurrency is the parallel-render cap when the config does not set
// one. Eight keeps a fifty-cluster fleet inside a gate's time budget without
// asking the host for fifty concurrent helm subprocesses.
const defaultConcurrency = 8

// maxConcurrency is the ceiling, whatever the config asks for. Each worker is
// a helm subprocess with a chart download and a temporary directory behind it,
// so the number is a request for host resources the host never agreed to: the
// value is read from the repository being gated, and the gate runs in a pod
// with a memory limit beside every other pull request's render. Thirty-two is
// well above any fleet size that renders inside a gate's time budget, so a
// config that trips this was not tuning, it was a typo or a lever someone
// found.
const maxConcurrency = 32

// workers is the render parallelism to use.
//
// Render and ChartDiff are exported and size their semaphore from this. Taking
// Concurrency straight off the struct made a zero value, any Config built as a
// literal instead of through ParseConfig, a permanent hang rather than an
// error: `make(chan struct{}, 0)` is unbuffered, so the first worker blocks on
// a send nobody will ever receive. A caller that got the config right is
// unaffected; a caller that did not gets the default instead of a deadlock.
//
// It is also the only place the value is bounded above. Both ends are checked
// here rather than in ParseConfig because ParseConfig is not on every path to
// a render: a Config built as a literal reaches the semaphore too, and a cap
// only the parser applied would be a cap the gate's own callers could skip.
func (c *Config) workers() int {
	if c == nil || c.Concurrency < 1 {
		return defaultConcurrency
	}
	if c.Concurrency > maxConcurrency {
		return maxConcurrency
	}
	return c.Concurrency
}

// sourceTypes is every value a source's `type` may take, in the order the const
// block declares them.
// SourceLive is deliberately absent: the list is what an operator may write,
// and offering a type the parser then refuses would be worse than not
// mentioning it.
var sourceTypes = []SourceType{
	SourceManifests, SourceRendered, SourceHelm, SourceKustomize, SourceArgoCDBootstrap,
	SourceDirectory,
}

// sourceTypeList renders them for the "you did not set one" error.
//
// Built from the constants rather than spelled out, because a hand-written list
// falls behind the const block silently and did: `rendered` was added and the
// message kept offering four of the five, so the one type an operator could not
// discover was the one the error was there to teach them.
func sourceTypeList() string {
	names := make([]string, len(sourceTypes))
	for i, t := range sourceTypes {
		names[i] = string(t)
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
