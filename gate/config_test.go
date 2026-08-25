package gate

import "testing"

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
