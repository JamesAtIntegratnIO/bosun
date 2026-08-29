package gate

import (
	"strings"
	"testing"

	"github.com/JamesAtIntegratnIO/bosun/egress"
)

type denyAll struct{ rule string }

func (d denyAll) Denied(host string) (string, bool) { return d.rule, true }

type denyNone struct{}

func (denyNone) Denied(string) (string, bool) { return "", false }

// The destination rule lives in egress now, because two private copies fed the
// same deny check and disagreed about a chart repository written without a
// scheme, which is a real destination, and one of them skipped it.
func TestAChartRepositoryWithoutASchemeIsStillADestination(t *testing.T) {
	for _, tc := range []struct{ ref, want string }{
		{"oci://ghcr.io/org/chart", "ghcr.io"},
		// The case that was being skipped: helm is handed this as
		// oci://ghcr.io/... moments later.
		{"ghcr.io/akuity/kargo-charts", "ghcr.io"},
		{"https://charts.example.io/stable", "charts.example.io"},
		// A bare chart name reaches nothing on its own and must not be
		// checked as though it were a hostname.
		{"podinfo", ""},
		{"", ""},
	} {
		if got := egress.HostOf(tc.ref); got != tc.want {
			t.Errorf("HostOf(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

// And the check that consumes it must refuse the scheme-less form too.
func TestEgressCheckRefusesASchemelessDeniedHost(t *testing.T) {
	cfg := &Config{Egress: denyAll{rule: "ghcr.io"}}
	if reason := cfg.egressCheck("ghcr.io/akuity/kargo-charts", "chart", "1.0.0"); reason == "" {
		t.Fatal("a scheme-less repository must not bypass the deny-list")
	}
}

// helm is a subprocess, so the egress transport cannot see inside it. Without
// this check the in-cluster gate, the default deployment, pulled remote
// charts with no policy applied and no record kept.
func TestEgressCheckRefusesADeniedHost(t *testing.T) {
	cfg := &Config{Egress: denyAll{rule: "*.example.io"}}
	reason := cfg.egressCheck("oci://ghcr.io/org/chart", "chart", "1.0.0")
	if !strings.Contains(reason, "denied by policy") || !strings.Contains(reason, "ghcr.io") {
		t.Errorf("want a denial naming the host and the rule, got %q", reason)
	}
}

func TestEgressCheckLogsWhatItPermits(t *testing.T) {
	var logged []string
	cfg := &Config{
		Egress: denyNone{},
		Log:    func(f string, a ...any) { logged = append(logged, f) },
	}
	if reason := cfg.egressCheck("https://charts.example.io", "podinfo", "6.0.0"); reason != "" {
		t.Fatalf("want the call permitted, got %q", reason)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "outbound helm") {
		t.Errorf("a permitted destination must still be recorded, got %v", logged)
	}
}

// Nil is open: the standalone gitops-gate binary has no operator policy to
// consult, and must not start refusing charts because of it.
func TestNoPolicyIsOpen(t *testing.T) {
	for _, cfg := range []*Config{nil, {}} {
		if reason := cfg.egressCheck("oci://ghcr.io/org/chart", "chart", "1.0.0"); reason != "" {
			t.Errorf("an unset policy must be open, got %q", reason)
		}
	}
}

// A bare chart name reaches nothing on its own, the repository is where the
// host lives, so it must not be treated as a destination.
func TestALocalChartNameIsNotADestination(t *testing.T) {
	cfg := &Config{Egress: denyAll{rule: "everything"}}
	if reason := cfg.egressCheck("podinfo", "podinfo", "6.0.0"); reason != "" {
		t.Errorf("a bare chart name is not a host, got %q", reason)
	}
}
