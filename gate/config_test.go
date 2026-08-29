package gate

import (
	"strings"
	"testing"
)

// Render and ChartDiff size their semaphore from this. A zero would make the
// channel unbuffered, so the first worker blocks on a send nobody receives,
// an exported entry point that hangs forever instead of erroring.
func TestWorkersNeverReturnsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *Config
		want int
	}{
		{"nil config", nil, defaultConcurrency},
		{"zero value literal", &Config{}, defaultConcurrency},
		{"negative", &Config{Concurrency: -3}, defaultConcurrency},
		{"one", &Config{Concurrency: 1}, 1},
		{"configured", &Config{Concurrency: 32}, 32},
	} {
		if got := tc.cfg.workers(); got != tc.want {
			t.Errorf("%s: workers() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// The list an operator is shown when they leave `type` unset has to be the
// list the switch above accepts. Hand-written, it fell behind the
// const block: `rendered` was added and the message kept offering four of the
// five, so the one type they could not discover was the one the error existed
// to teach them.
func TestSourceTypeListNamesEveryAcceptedType(t *testing.T) {
	got := sourceTypeList()
	for _, want := range []SourceType{
		SourceManifests, SourceRendered, SourceHelm, SourceKustomize, SourceArgoCDBootstrap,
	} {
		if !strings.Contains(got, string(want)) {
			t.Errorf("%q is accepted but not offered: %q", want, got)
		}
	}

	// And every type it offers must be accepted, validate's
	// default branch rejects anything the const block does not declare.
	for _, tc := range sourceTypes {
		s := &Source{Name: "s", Type: tc, Path: "p", Chart: "c", Paths: []string{"p"}}
		if err := s.validate("cfg.yaml", 0); err != nil {
			t.Errorf("%q is offered but rejected: %v", tc, err)
		}
	}
}

// The number is read from the repository being gated, and every worker is a
// helm subprocess with a chart download and a temporary directory behind it,
// running in the gate's own pod beside every other open pull request's render.
//
// It parses rather than erroring: refusing would fail a pull request over a
// field that has nothing to do with its diff, and the value is capped where it
// is acted on instead. The cap lives in workers() rather than ParseConfig
// because a Config built as a literal reaches the semaphore too, and a bound
// only the parser applied would be one the gate's own callers could skip.
func TestWorkersClampsWhatTheGatedRepositoryAsksFor(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
concurrency: 5000
sources:
  - {name: appsets, type: manifests, paths: ["appsets/*.yaml"]}
`), ".gitops-gate.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 5000 {
		t.Errorf("the file's own value should survive parsing, got %d", cfg.Concurrency)
	}
	if got := cfg.workers(); got != maxConcurrency {
		t.Errorf("workers() = %d, want the %d cap", got, maxConcurrency)
	}

	for _, tc := range []struct {
		name string
		cfg  *Config
		want int
	}{
		{"at the cap", &Config{Concurrency: maxConcurrency}, maxConcurrency},
		{"one over", &Config{Concurrency: maxConcurrency + 1}, maxConcurrency},
		{"under it", &Config{Concurrency: 4}, 4},
	} {
		if got := tc.cfg.workers(); got != tc.want {
			t.Errorf("%s: workers() = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// `sources[].argocd` scoped a source to one ArgoCD instance in a fleet running
// several, matched against a field on each Cluster. Nothing ever set that
// field: the live inventory comes from one ArgoCD's own API, which does not
// report which install served it, so every cluster carried the empty string
// and any source naming an instance matched none of them. A helm source that
// matches no cluster is already a hard error, so the key failed a run rather
// than quietly narrowing one, and removing it takes nothing away from a
// configuration that works today.
//
// Strict parsing is what makes the removal safe to ship: a config still
// setting it stops at parse with the key named, rather than being ignored.
func TestTheRemovedArgoCDSourceKeyIsRejectedByName(t *testing.T) {
	_, err := ParseConfig([]byte(`
sources:
  - {name: eu, type: manifests, paths: ["appsets/*.yaml"], argocd: eu}
`), ".gitops-gate.yaml")
	if err == nil {
		t.Fatal("a config setting the removed key must not parse")
	}
	if !strings.Contains(err.Error(), "argocd") {
		t.Errorf("the error must name the key it is refusing: %v", err)
	}
}
