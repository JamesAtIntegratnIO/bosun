package pipeline

import (
	"fmt"
	"strings"
	"time"
)

// Thresholds. Deliberately generous: this package's credibility rests on
// never crying wolf, and every one of these situations is still true an hour
// later if it was true at all.
const (
	// stalenessFactor multiplies a Warehouse's own interval. A Warehouse is
	// stale only after it has missed TWO discoveries -- one missed sweep is a
	// slow registry, two is a Warehouse that stopped.
	stalenessFactor = 2
	// verifyStuck is how long a verification may hold a Stage before it is
	// worth a look. Kargo runs these as AnalysisRuns with their own timeouts;
	// this catches the ones with none.
	verifyStuck = 45 * time.Minute
	// orphanGrace is how long a promotion may run against a pull request that
	// is not open before it counts as stranded.
	//
	// It exists because "not open" covers MERGED as well as closed, and the
	// seconds between a merge and Kargo noticing it are exactly when a
	// promotion is doing the right thing. Reporting that window as a problem
	// is a false alarm on every successful promotion this repository makes,
	// which would be the fastest possible way to teach someone to ignore this.
	//
	// Caught by running it: the first live sweep flagged bosun's own promotion
	// three minutes after its pull request merged. A genuinely stranded one
	// waits indefinitely -- the ones observed had been running for hours -- so
	// the grace costs nothing real.
	orphanGrace = 15 * time.Minute
	// pendingStuck is how long a promotion may sit Pending. Pending means
	// queued behind something -- usually a verification -- and a queue that
	// never drains is the pipeline stopped.
	//
	// Two hours because a promotion queued behind a normal verification clears
	// in minutes, and one queued behind a long AnalysisRun in tens of minutes.
	// Past two hours nothing is draining, and the Stage is reporting no error
	// while it happens -- which is precisely the class of failure that produces
	// no event and so needs a timer to find.
	pendingStuck = 2 * time.Hour
)

// Detect runs every detector over one snapshot.
//
// Ordering note: detectors do not consult each other. A Stage can legitimately
// produce two findings -- a wedged promotion AND a dead pin -- and collapsing
// them would hide whichever the collapser thought less important. Reports are
// sorted, not deduplicated.
func Detect(s *Snapshot) *Report {
	r := &Report{At: s.Now, Checked: Checked{
		Stages:       len(s.Stages),
		Warehouses:   len(s.Warehouses),
		Promotions:   len(s.Promotions),
		PullRequests: len(s.OpenPRs),
		Notes:        append([]string(nil), s.Notes...),
	}}
	seen := map[string]bool{}
	for _, st := range s.Stages {
		if st.Namespace != "" && !seen[st.Namespace] {
			seen[st.Namespace] = true
			r.Namespaces = append(r.Namespaces, st.Namespace)
		}
	}

	r.Findings = append(r.Findings, detectWedged(s)...)
	r.Findings = append(r.Findings, detectStalled(s)...)
	r.Findings = append(r.Findings, detectOrphanedPromotions(s)...)
	r.Findings = append(r.Findings, detectSupersededPRs(s)...)
	r.Findings = append(r.Findings, detectVerificationStuck(s)...)
	r.Findings = append(r.Findings, detectPendingStuck(s)...)

	pins, scanned := detectDeadPins(s)
	r.Findings = append(r.Findings, pins...)
	r.Checked.PinsScanned = scanned
	if s.FileHas == nil {
		r.Checked.Notes = append(r.Checked.Notes,
			"pins were not checked: no checkout was available, so a tracked key that writes nowhere would not have been found")
	}

	r.Sort()
	return r
}

// detectWedged finds Stages whose most recent promotion ended without
// delivering, and which therefore will not try again.
//
// THIS IS THE ONE THAT MATTERS MOST, and the one nothing else reports. Kargo
// creates a Promotion per freight; when it reaches a terminal phase it is over
// for good. Auto-promotion does not retry it, because from the controller's
// point of view that freight HAS been promoted -- the attempt simply failed.
// So a single transient error at the wrong moment stops that Stage receiving
// artifacts permanently, and every Application it manages stays Synced and
// Healthy on the old version, which is exactly what "nothing is wrong" looks
// like from every other angle.
//
// Observed: four Stages, three days, one DNS lookup.
func detectWedged(s *Snapshot) []Finding {
	var out []Finding
	byStage := s.promotionsByStage()
	for _, st := range s.Stages {
		ps := byStage[st.Name]
		if len(ps) == 0 {
			continue
		}
		latest := ps[0]
		if !Unsuccessful(latest.Phase) {
			continue
		}
		// A failure the Stage has since recovered from is history: some later
		// promotion succeeded, or the Stage is already running this freight.
		if st.CurrentFreight != "" && st.CurrentFreight == latest.Freight {
			continue
		}
		// An older promotion that DID deliver this freight means the Stage
		// has it; the later failure was a retry of something already done.
		delivered := false
		for _, p := range ps[1:] {
			if p.Phase == PhaseSucceeded && p.Freight == latest.Freight {
				delivered = true
				break
			}
		}
		if delivered {
			continue
		}
		age := latest.Age(s.Now)
		why := strings.TrimSpace(latest.Message)
		if why == "" {
			why = "no message was recorded"
		}
		out = append(out, Finding{
			Kind:     KindWedged,
			Severity: Blocking,
			Subject:  st.Name,
			Since:    age,
			Summary: fmt.Sprintf("%s stopped receiving artifacts %s ago and will not retry on its own",
				st.Name, human(age)),
			Detail: fmt.Sprintf(
				"Promotion `%s` ended %s and nothing has promoted this freight since. A terminal promotion is "+
					"final: auto-promotion does not re-run one, because the freight has been promoted as far as "+
					"the controller is concerned — the attempt merely failed. Every Application this Stage "+
					"manages stays Synced and Healthy on the previous version, which is why nothing else reports it.\n\n"+
					"Why it ended: %s",
				latest.Name, strings.ToLower(latest.Phase), why),
			Remedy: promoteCmd(st.Namespace, st.Name, latest.Freight),
		})
	}
	return out
}

// promoteCmd is the exact YAML that re-runs a promotion.
//
// Every detail here is load-bearing and none of it is guessable. A Warehouse
// refresh does NOT do this -- it re-discovers artifacts, and freight that
// already carries a terminal promotion is never auto-promoted again. And
// `generateName` must NOT end in a dot: the webhook computes Kargo's own name
// from it and then validates the generateName itself as RFC1123, which a
// trailing dot fails.
func promoteCmd(ns, stage, freight string) string {
	if ns == "" {
		ns = "<namespace>"
	}
	return fmt.Sprintf(`# Re-run it. A Warehouse refresh will NOT: it re-discovers artifacts,
# and freight with a terminal promotion is never auto-promoted again.
kubectl create -f - <<'EOF'
apiVersion: kargo.akuity.io/v1alpha1
kind: Promotion
metadata:
  generateName: %s          # no trailing dot: the webhook validates this as RFC1123
  namespace: %s
spec:
  stage: %s
  freight: %s
EOF`, stage, ns, stage, freight)
}

// detectStalled finds Warehouses that have stopped discovering.
//
// A Warehouse that cannot reach its registry reports Ready=False and keeps its
// last discovery forever. Nothing downstream changes: no new freight means no
// new promotions means no pull requests, which is indistinguishable from
// "everything is already up to date" unless someone knows when it last looked.
func detectStalled(s *Snapshot) []Finding {
	var out []Finding
	for _, w := range s.Warehouses {
		switch {
		case !w.Ready && w.ReadyReason != "":
			out = append(out, Finding{
				Kind:     KindStalled,
				Severity: Blocking,
				Subject:  w.Name,
				Summary:  fmt.Sprintf("Warehouse %s is not discovering artifacts (%s)", w.Name, w.ReadyReason),
				Detail: fmt.Sprintf("No new freight means no promotions and no pull requests, which looks "+
					"exactly like everything being up to date.\n\n%s", strings.TrimSpace(w.ReadyMessage)),
				Remedy: refreshCmd(w.Namespace, w.Name),
			})
		case w.Interval > 0 && !w.DiscoveredAt.IsZero():
			age := s.Now.Sub(w.DiscoveredAt)
			if age > time.Duration(stalenessFactor)*w.Interval {
				out = append(out, Finding{
					Kind:     KindStalled,
					Severity: Degraded,
					Subject:  w.Name,
					Since:    age,
					Summary: fmt.Sprintf("Warehouse %s last discovered %s ago, on a %s interval",
						w.Name, human(age), human(w.Interval)),
					Detail: "It has missed at least two sweeps. That is long enough not to be a slow " +
						"registry, and a Warehouse that stopped looking is a pipeline that stopped delivering.",
					Remedy: refreshCmd(w.Namespace, w.Name),
				})
			}
		}
	}
	return out
}

func refreshCmd(ns, name string) string {
	if ns == "" {
		ns = "<namespace>"
	}
	return fmt.Sprintf(`kubectl -n %s annotate warehouse %s \
  kargo.akuity.io/refresh="$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)" --overwrite`, ns, name)
}

// detectOrphanedPromotions finds promotions running against a pull request
// that no longer exists.
//
// A promotion's last step waits for its pull request to merge. Close that pull
// request -- superseded, or replaced by a rebase -- and the promotion keeps
// waiting, holding the Stage's queue behind it. Kargo does notice eventually,
// but "eventually" was measured in tens of minutes, and until then the Stage
// accepts nothing else.
//
// The join is Kargo's own branch convention; neither object records the other.
func detectOrphanedPromotions(s *Snapshot) []Finding {
	if len(s.OpenPRs) == 0 && len(s.Promotions) == 0 {
		return nil
	}
	open := map[string]bool{}
	for _, pr := range s.OpenPRs {
		open[pr.Branch] = true
	}
	// Without any open pull request at all, we cannot tell "closed underneath
	// it" from "the collector could not list pull requests". Saying nothing is
	// the honest answer.
	if len(open) == 0 {
		return nil
	}
	var out []Finding
	for _, p := range s.Promotions {
		if p.Phase != PhaseRunning || open[p.Branch()] {
			continue
		}
		age := p.Age(s.Now)
		// A pull request that merged moments ago is also "not open". See
		// orphanGrace.
		if age < orphanGrace {
			continue
		}
		out = append(out, Finding{
			Kind:     KindOrphanedPR,
			Severity: Blocking,
			Subject:  p.Stage,
			Since:    age,
			Summary: fmt.Sprintf("%s has been waiting %s for a pull request that is no longer open",
				p.Stage, human(age)),
			Detail: fmt.Sprintf("Promotion `%s` is Running and its branch `%s` has no open pull request. "+
				"Its last step waits for that pull request to merge, so it will hold this Stage's queue "+
				"until it times out — nothing else promotes past it in the meantime.",
				p.Name, p.Branch()),
			Remedy: abortCmd(p.Namespace, p.Name),
		})
	}
	return out
}

// abortCmd carries the one string that works.
//
// `kargo.akuity.io/abort=true` is accepted by the API and silently does
// nothing: the value is parsed as a request object, and a bare `true` is not
// one. There is no error, no event and no log line -- the promotion simply
// keeps running, which is indistinguishable from the annotation not having
// been applied. Measured the hard way.
func abortCmd(ns, promotion string) string {
	if ns == "" {
		ns = "<namespace>"
	}
	return fmt.Sprintf(`# NOTE: abort=true is silently ignored. It must be the request object.
kubectl -n %s annotate promotion %s \
  'kargo.akuity.io/abort={"action":"terminate"}' --overwrite`, ns, promotion)
}

// detectSupersededPRs finds a Stage with more than one open promotion pull
// request.
//
// Only the newest can ever merge -- the others promote freight the Stage has
// moved past -- but they stay open, gather gate runs and triage comments, and
// crowd out the pull requests a human still has to read. Nine of them had
// accumulated against four Stages.
func detectSupersededPRs(s *Snapshot) []Finding {
	byStage := map[string][]PullRequest{}
	for _, pr := range s.OpenPRs {
		if st := StageOfBranch(pr.Branch); st != "" {
			byStage[st] = append(byStage[st], pr)
		}
	}
	var out []Finding
	for stage, prs := range byStage {
		if len(prs) < 2 {
			continue
		}
		newest := prs[0]
		for _, pr := range prs {
			if pr.Number > newest.Number {
				newest = pr
			}
		}
		var older []string
		for _, pr := range prs {
			if pr.Number != newest.Number {
				older = append(older, fmt.Sprintf("#%d", pr.Number))
			}
		}
		out = append(out, Finding{
			Kind:     KindSupersededPR,
			Severity: Degraded,
			Subject:  stage,
			Summary: fmt.Sprintf("%s has %s open, and only the newest can merge",
				stage, plural(len(prs), "promotion pull request")),
			Detail: fmt.Sprintf("#%d is current; %s promote freight this Stage has already moved past. "+
				"They cannot merge, but they still collect gate runs and triage comments, and they crowd "+
				"the list a human reads.", newest.Number, joinAnd(older)),
			Remedy: fmt.Sprintf("gh pr close %s --comment 'superseded by #%d'",
				strings.Join(trimHashes(older), " "), newest.Number),
		})
	}
	return out
}

func trimHashes(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, strings.TrimPrefix(s, "#"))
	}
	return out
}

// detectVerificationStuck finds a Stage that a verification has stopped.
//
// TWO SITUATIONS, and they read differently on purpose.
//
// A verification still RUNNING after long enough is a Stage nothing promotes
// past while an AnalysisRun with no timeout takes its time.
//
// A verification that FAILED or ERRORED is worse and much easier to miss,
// because it is over. Kargo does not re-run it: that freight has been
// verified, and the answer was no. The Stage sits `Ready=False` forever,
// declines every promotion behind it, and every Application it manages stays
// Synced and Healthy on the version it already had. Measured: three Stages
// held for three days by an AnalysisRun that could not reach Prometheus
// because a NetworkPolicy excepted RFC1918 and Prometheus is a ClusterIP.
//
// Nothing about that produces an alert, which is why it needs one.
func detectVerificationStuck(s *Snapshot) []Finding {
	var out []Finding
	for _, st := range s.Stages {
		if st.Ready || !strings.Contains(strings.ToLower(st.ReadyReason), "verif") {
			continue
		}
		over := isTerminalVerification(st.VerificationPhase)
		// A verification still running is only worth reporting once it has
		// run long enough to be stuck. One that already failed is worth
		// reporting immediately -- it is not going to change.
		if !over && st.ReadySince > 0 && st.ReadySince < verifyStuck {
			continue
		}
		f := Finding{
			Kind:     KindVerifyStuck,
			Subject:  st.Name,
			Since:    st.ReadySince,
			Severity: Degraded,
			Detail:   strings.TrimSpace(st.ReadyMessage),
		}
		if over {
			f.Severity = Blocking
			f.Summary = fmt.Sprintf("%s stopped promoting %s ago: its verification ended %s and Kargo will not re-run it",
				st.Name, human(st.ReadySince), strings.ToLower(st.VerificationPhase))
			f.Detail += "\n\nThat freight has been verified and the answer was no, so nothing retries. " +
				"The Stage declines every promotion behind it while every Application it manages stays " +
				"Synced and Healthy on the version it already had."
			f.Remedy = reverifyCmd(st.Namespace, st.Name, st.VerificationID)
		} else {
			f.Summary = fmt.Sprintf("%s has been verifying for %s, and nothing promotes past it meanwhile",
				st.Name, human(st.ReadySince))
			f.Detail += "\n\nAn AnalysisRun with no timeout holds the Stage's queue indefinitely, and the " +
				"Stage reports only that it is not Ready."
			f.Remedy = fmt.Sprintf("kubectl -n %s get analysisruns --sort-by=.metadata.creationTimestamp | tail -5\n"+
				"kubectl -n %s get analysisrun <name> -o jsonpath='{.status.metricResults}'",
				orNS(st.Namespace), orNS(st.Namespace))
		}
		out = append(out, f)
	}
	return out
}

func isTerminalVerification(phase string) bool {
	switch phase {
	case "Failed", "Error", "Aborted", "Inconclusive":
		return true
	}
	return false
}

// reverifyCmd re-runs a verification that has already answered.
//
// The id is not optional and not discoverable from the Stage's conditions --
// it lives in `status.freightHistory[0].verificationHistory[0].id`, which is
// three levels deeper than anyone looks. Fixing the underlying cause does
// NOTHING on its own: the verification is over, and the Stage stays stuck
// until something asks it again. Proved by fixing a NetworkPolicy and watching
// three Stages not move.
func reverifyCmd(ns, stage, id string) string {
	ns = orNS(ns)
	if id == "" {
		return fmt.Sprintf(`# find the verification id, then ask for it again:
kubectl -n %s get stage %s -o jsonpath='{.status.freightHistory[0].verificationHistory[0].id}'
kubectl -n %s annotate stage %s 'kargo.akuity.io/reverify={"id":"<id>"}' --overwrite`, ns, stage, ns, stage)
	}
	return fmt.Sprintf(`# Fixing the cause is not enough: the verification is over, and the Stage
# stays stuck until something asks it again.
kubectl -n %s annotate stage %s \
  'kargo.akuity.io/reverify={"id":"%s"}' --overwrite`, ns, stage, id)
}

func orNS(ns string) string {
	if ns == "" {
		return "<namespace>"
	}
	return ns
}

// detectDeadPins finds tracked keys that write nowhere.
//
// A Kargo target names a file and a set of keys, and rewrites those keys on
// every promotion. If the key is not in the file, the write lands nowhere and
// the promotion still succeeds -- so the pin looks maintained forever while
// having no effect at all.
//
// The two ways this happens are both invisible:
//
//   - the file moved or was renamed, and the target still names the old path;
//   - the CHART stopped reading the key, someone removed it from the values
//     file, and the target that writes it belongs to a DIFFERENT artifact.
//     Observed exactly: the `kubectl` target writes seven keys into kyverno's
//     values file, and kyverno 3.9.0 stops declaring six of them. Two targets,
//     one dependency, nothing connecting them.
//
// Returns the findings and how many (file, key) pairs were actually resolved,
// because a pin check that could not read the checkout must not be reported as
// a pin check that found nothing.
func detectDeadPins(s *Snapshot) ([]Finding, int) {
	if s.FileHas == nil {
		return nil, 0
	}
	exists := func(p string) bool {
		_, err := s.FileHas(p, "")
		return err == nil
	}
	scanned := 0
	var out []Finding
	for _, st := range s.Stages {
		for _, u := range st.Updates {
			path := u.RepoPath(exists)
			var dead []string
			missingFile := false
			for _, k := range u.Keys {
				has, err := s.FileHas(path, k)
				if err != nil {
					missingFile = true
					break
				}
				scanned++
				if !has {
					dead = append(dead, k)
				}
			}
			switch {
			case missingFile:
				out = append(out, Finding{
					Kind:     KindDeadPin,
					Severity: Degraded,
					Subject:  st.Name,
					Summary: fmt.Sprintf("%s promotes into `%s`, which this branch does not have",
						st.Name, path),
					Detail: fmt.Sprintf("The promotion rewrites %s in a file that is not there. The step "+
						"does not fail on a missing key, so the promotion will keep succeeding and keep "+
						"changing nothing.", plural(len(u.Keys), "key")),
					Remedy: fmt.Sprintf("# point the target at the file's new home, or drop it:\n"+
						"grep -rn 'file: .*%s' --include=values.yaml .", trimDir(path)),
				})
			case len(dead) > 0:
				out = append(out, Finding{
					Kind:     KindDeadPin,
					Severity: Degraded,
					Subject:  st.Name,
					Summary: fmt.Sprintf("%s rewrites %s in `%s` that the file does not set",
						st.Name, plural(len(dead), "key"), path),
					Detail: fmt.Sprintf("A `yaml-update` step whose key is absent writes nothing and still "+
						"reports success, so this pin looks maintained on every promotion while having no "+
						"effect.\n\nKeys that write nowhere:\n%s\n\nThe usual cause is a chart that stopped "+
						"declaring them — and the target that writes them is often for a DIFFERENT artifact "+
						"than the chart that reads them, which is why nothing connects the two.",
						bullets(dead)),
					Remedy: fmt.Sprintf("# confirm, then remove them from the target's `keys:` list:\n"+
						"yq '%s' %s", u.Keys[0], path),
				})
			}
		}
	}
	return out, scanned
}

func bullets(in []string) string {
	var b strings.Builder
	for _, s := range in {
		fmt.Fprintf(&b, "- `%s`\n", s)
	}
	return strings.TrimRight(b.String(), "\n")
}

func trimDir(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// detectPendingStuck finds a promotion that has sat Pending long enough that
// the queue is not draining.
//
// Pending is the phase with no symptom. The Stage reports no error, every
// Application it manages stays Synced and Healthy on the version it already
// had, and the promotion that would move it never starts -- so nothing about
// this produces an event, which is the whole reason the supervisor is a timer.
//
// One finding per Stage, on the OLDEST pending promotion. A wedged queue backs
// up behind a single blocker, and reporting each promotion in the queue
// separately would turn one problem into a page of them.
func detectPendingStuck(s *Snapshot) []Finding {
	var out []Finding
	for _, st := range s.Stages {
		ps := s.promotionsByStage()[st.Name]

		// Newest first, so the last pending one is the oldest.
		var oldest *Promotion
		queued := 0
		for i := range ps {
			if ps[i].Phase != PhasePending {
				continue
			}
			queued++
			oldest = &ps[i]
		}
		if oldest == nil {
			continue
		}
		age := oldest.Age(s.Now)
		if age < pendingStuck {
			continue
		}

		detail := strings.TrimSpace(oldest.Message)
		if detail == "" {
			detail = "Kargo recorded no message, which is normal for Pending -- it is a queue position, not a failure."
		}
		if queued > 1 {
			detail += fmt.Sprintf("\n\n%d promotions are queued on this Stage; this is the oldest.", queued)
		}
		out = append(out, Finding{
			Kind:     KindPendingStuck,
			Severity: Blocking,
			Subject:  st.Name,
			Since:    age,
			Summary: fmt.Sprintf("%s has had a promotion Pending for %s, so nothing is reaching this Stage",
				st.Name, human(age)),
			Detail: detail,
			Remedy: fmt.Sprintf("kubectl -n %s get promotions --sort-by=.metadata.creationTimestamp | tail -5\n"+
				"kubectl -n %s describe promotion %s",
				orNS(oldest.Namespace), orNS(oldest.Namespace), oldest.Name),
		})
	}
	return out
}
