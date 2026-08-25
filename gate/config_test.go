package gate

import (
	"strings"
	"testing"
)

// Render and ChartDiff size their semaphore from this. A zero would make the
// channel unbuffered, so the first worker blocks on a send nobody receives --
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
// list the switch above actually accepts. Hand-written, it fell behind the
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

	// And every type it offers must actually be accepted -- validate's
	// default branch rejects anything the const block does not declare.
	for _, tc := range sourceTypes {
		s := &Source{Name: "s", Type: tc, Path: "p", Chart: "c", Paths: []string{"p"}}
		if err := s.validate("cfg.yaml", 0); err != nil {
			t.Errorf("%q is offered but rejected: %v", tc, err)
		}
	}
}
