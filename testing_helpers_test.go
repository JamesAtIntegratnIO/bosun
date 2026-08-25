package main

import (
	"log"
	"testing"
)

// testLogger sends the service's log lines to the test's own output, so a
// failure carries the run that produced it instead of leaving it on a stream
// nobody captured.
func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(testWriter{t}, "", 0)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(string(p))
	return len(p), nil
}
