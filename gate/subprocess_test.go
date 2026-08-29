package gate

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// Every helm, kustomize and kubectl invocation the gate makes goes through
// run, and run is where the deadline lives. The service already wrapped a gate
// run in context.WithTimeout, which cancelled nothing at all: a context cannot
// stop a process started with exec.Command, so a stalled `helm template`
// outlived the timeout that was supposed to bound it and held a chart-diff
// worker slot until it felt like exiting.
//
// The test binary is the subprocess, so this asserts the wiring without
// needing helm on PATH or a five-second sleep in the suite.
func TestRunStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := run(ctx, "", os.Args[0], "-test.run", "TestRunStopsWithItsContext$")
	if err == nil {
		t.Fatal("a cancelled context must stop the subprocess")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the cancellation reported, got %v", err)
	}
	// "signal: killed" alone reads like a crash and sends the reader looking
	// at the chart instead of at the clock.
	if !strings.Contains(err.Error(), "was stopped before it finished") {
		t.Errorf("the error must say why it stopped, got %q", err)
	}
}
