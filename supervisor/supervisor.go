// Package supervisor runs the pipeline sweep on a timer and serves what it
// found.
//
// The job nobody asks for. Every other part of this system answers a question
// somebody raised -- is this pull request safe, what did this bump change --
// and each is triggered by an event. This one asks whether the pull requests
// that SHOULD exist are being opened at all, and nothing about a promotion
// that never happened produces an event. A timer is the only way to see it.
//
// pipeline decides what is wrong; this decides when to look and how to hand
// the answer to an operator or a scrape.
package supervisor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// Supervisor runs the pipeline sweep on an interval and holds the last report
// for the endpoints to serve.
//
// It is a SECOND JOB for this agent, and deliberately independent of the
// first. Triage answers a pull request that exists; this answers the question
// nobody asked, which is whether the pull requests that should exist do.
// Nothing about a promotion that never happened produces an event, so the only
// way to notice is to look on a timer.
type Supervisor struct {
	Collector *pipeline.Collector
	Every     time.Duration
	Log       func(string, ...any)

	// Checkout produces a tree to resolve tracked pins against, and is the
	// difference between the pin check running and quietly never running.
	//
	// It is the DEFAULT BRANCH, not a pull request: a pin that writes nowhere
	// is a property of what is merged, and asking the question of whichever
	// branch happened to be open would answer about a proposal instead. Nil
	// disables the check, and the report says so rather than reporting no
	// dead pins.
	Checkout func(context.Context) (string, func(), error)

	mu   sync.RWMutex
	last *pipeline.Report
	// prev is the previous sweep's headline, so the log says something only
	// when the answer CHANGES. A supervisor that reprints an unchanged report
	// every ten minutes teaches people to filter it out, and then it is not a
	// supervisor.
	prev string
}

func (s *Supervisor) logf(f string, a ...any) {
	if s.Log != nil {
		s.Log(f, a...)
	}
}

func (s *Supervisor) interval() time.Duration {
	if s.Every > 0 {
		return s.Every
	}
	return 10 * time.Minute
}

// Run sweeps until the context is cancelled. The first sweep is immediate: an
// agent that restarts during an incident should not wait out an interval
// before saying what it can see.
func (s *Supervisor) Run(ctx context.Context) {
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.interval()):
		}
	}
}

func (s *Supervisor) sweep(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	// A checkout that fails is a note on the report, never a skipped sweep:
	// the cluster half is worth having on its own, and the report is explicit
	// about what it could not look at.
	if s.Checkout != nil {
		dir, cleanup, err := s.Checkout(ctx)
		if err != nil {
			s.Collector.RepoRoot = ""
			s.logf("pipeline: could not check out the repository (%v); pins were not checked", err)
		} else {
			s.Collector.RepoRoot = dir
			defer func() {
				cleanup()
				s.Collector.RepoRoot = ""
			}()
		}
	}

	r := s.Collector.Sweep(ctx)
	s.mu.Lock()
	s.last = r
	changed := r.Headline() != s.prev
	s.prev = r.Headline()
	s.mu.Unlock()

	if !changed {
		return
	}
	s.logf("pipeline: %s", r.Headline())
	// On a change, print the blocking findings in full. These are the ones
	// where somebody has to do something, and the log is the only surface
	// that reaches an operator without them going to look for it.
	for _, f := range r.Findings {
		if f.Severity != pipeline.Blocking {
			continue
		}
		s.logf("pipeline: %s", f.Summary)
		for _, line := range strings.Split(strings.TrimSpace(f.Remedy), "\n") {
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
				s.logf("pipeline:   %s", t)
			}
		}
	}
	for _, n := range r.Checked.Notes {
		s.logf("pipeline: could not check everything -- %s", n)
	}
}

// Report is the last sweep, or nil before the first one completes.
func (s *Supervisor) Report() *pipeline.Report {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

// Handler serves the report as markdown for a human and Prometheus text for a
// scraper.
func (s *Supervisor) Handler(format string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		r := s.Report()
		if r == nil {
			// 503 rather than an empty 200. A scraper that reads zeroes from
			// a supervisor which has not swept yet would record "nothing is
			// wrong" as a measurement, and this package exists because that
			// is the most expensive thing a monitor can do.
			http.Error(w, "no sweep has completed yet", http.StatusServiceUnavailable)
			return
		}
		if format == "metrics" {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			r.Metrics(w)
			return
		}
		// ?format=text is the same report without the markdown, for an
		// operator reading it in a terminal while fixing it -- which is what
		// Report.Text was written for and had no caller to reach it through.
		if req.URL.Query().Get("format") == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			r.Text(w)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		r.Render(w)
	}
}

// ShallowCheckout clones the default branch, shallowly, into the same writable
// scratch the gate uses.
//
// Depth one and no worktrees: the pin check reads files and asks nothing about
// history. On the repository this was built against that is a two-second clone,
// which is why the sweep can afford to do it every time rather than hold a
// working copy that would drift from the branch it claims to describe.
func ShallowCheckout(repoURL, branch, root string) func(context.Context) (string, func(), error) {
	return func(ctx context.Context) (string, func(), error) {
		dir, err := os.MkdirTemp(root, "pipeline")
		if err != nil {
			return "", func() {}, err
		}
		cleanup := func() { _ = os.RemoveAll(dir) }
		args := []string{"clone", "--quiet", "--depth", "1"}
		if branch != "" {
			args = append(args, "--branch", branch)
		}
		args = append(args, repoURL, dir)
		c := exec.CommandContext(ctx, "git", args...)
		var errb strings.Builder
		c.Stderr = &errb
		if err := c.Run(); err != nil {
			cleanup()
			return "", func() {}, fmt.Errorf("git clone: %w: %s", err, strings.TrimSpace(errb.String()))
		}
		return dir, cleanup, nil
	}
}
