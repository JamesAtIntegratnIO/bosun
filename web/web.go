// Package web serves the status page: one HTML page that says what this agent
// is, what it is watching, and what the pipeline sweep found, remedies
// included.
//
// It exists because the report already had readers and no surface. /pipeline
// has served markdown since the supervisor was written, and markdown in a
// browser tab is source code; the people who most need the report, whoever is
// wondering why an addon has not updated in three days, are exactly the people
// who will not port-forward and pipe curl through a renderer to find out.
//
// The page is read-only and self-contained, one response, no scripts, no
// external assets, and it renders entirely from state the process already
// holds. Loading it costs no git API call, no model call and no cluster read,
// so a browser tab left open on refresh spends nothing but the render. That is
// also the security posture: the page can be exposed through a gateway
// (the chart binds it to its own port so that exposing it can never expose
// POST /v1/promotion-opened), and what it reveals is operational state, the
// repository's name, open pull request titles, finding text, never a
// credential, a prompt, or a diff.
package web

import (
	_ "embed"
	"fmt"
	"html/template"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// Server renders the page. Every field is data the composition root already
// had in its hands; the three funcs are snapshots and must stay cheap, they
// run on every page load.
type Server struct {
	// Identity. Brand is what the agent calls itself; Version is what the
	// operator deployed (the chart passes its appVersion), with the build's
	// own VCS stamp as fallback.
	Brand   string
	Version string

	// Theme is "dark" or "light" to stamp `data-theme` on the document, or
	// empty to leave the reader's system preference in charge. main.go maps
	// the chart's `auto` to empty here: the page has no third palette, so
	// "follow the system" is the absence of the attribute rather than a value
	// of it, and keeping that distinction at the boundary means the template
	// never has to know the word.
	Theme string

	// What it watches. RepoLink may be empty (an ssh-only clone URL has no
	// browsable address) and the page then names the repository without
	// linking it.
	Repo      string
	RepoLink  string
	CheckName string
	Model     string

	// Cadence. SweepEvery zero means the pipeline supervisor is off, and the
	// page says so rather than showing an empty report; GatePoll is the
	// gate's own interval.
	SweepEvery time.Duration
	GatePoll   time.Duration

	// Clusters is the inventory size read from ArgoCD at start-up. Labelled
	// as such on the page: it is not re-read per load, for the same reason
	// nothing else is.
	Clusters int

	// Features is the on/off posture of everything the agent can be told to
	// do, and EgressLine is one sentence about where it may go. Composed by
	// the caller, because the caller is the one that read the config.
	Features   []Feature
	EgressLine string

	// Report is the last pipeline sweep, nil before the first or when
	// supervision is off. Gate and Triage are the other two jobs' own
	// accounts of themselves. Any of the three may be nil and the page
	// renders what it has.
	Report func() *pipeline.Report
	Gate   func() GateStatus
	Triage func() TriageStatus
}

// Feature is one switchable behaviour, named the way the values file names it.
type Feature struct {
	Name string
	On   bool
}

// GateStatus is the gate sweep as the page shows it. A local type rather than
// gateservice's so this package depends on nothing that can dial: it renders
// values, and the composition root does the adapting.
type GateStatus struct {
	SweptAt time.Time
	// Err is what stopped the last sweep from listing pull requests, "" when
	// it ran. Shown verbatim: a gate that cannot list would otherwise read as
	// "nothing open" forever.
	Err  string
	Open []GatePR
	// Held is verdicts cached in memory; Running is renders in flight.
	Held    int
	Running int
}

// GatePR is one open pull request and the verdict standing on its head.
type GatePR struct {
	Number int
	Title  string
	URL    string
	// State: passing | failing | error | running | unknown.
	State string
}

// TriageStatus is the promotion endpoint's account of itself.
type TriageStatus struct {
	// InFlight and Queued are pull request numbers: the triages running now,
	// and the promotions that arrived while one was already running.
	InFlight []int
	Queued   []int
	// Done counts triages run since the process started, Failed the subset
	// that errored. They reset with the pod, and the page says "since
	// start-up" so nobody reads them as history.
	Done   int
	Failed int
}

// Page serves the status page. Always 200: a page that also carries the gate
// and the triage has something true to say before the first sweep, and the
// banner states "no sweep yet" rather than hiding behind a 503.
func (s *Server) Page() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := pageTmpl.Execute(w, s.view()); err != nil {
			// Nothing useful is left to do: the 200 and the Content-Type
			// are already on the wire, so this cannot become an error page.
			// In practice it is the viewer's connection going away
			// mid-render, which is not an event worth a line anywhere.
			_ = err
			return
		}
	}
}

// The mark, byte-for-byte the site's own favicon.
//
// A copy and not a reference, because go:embed cannot reach outside this
// package and this binary must not fetch anything at runtime. mark_test.go
// fails if it drifts from site/public/favicon.svg, which is what makes the
// copy safe: the site stays the one place Bosun's branding is decided, and
// this file cannot quietly become a second one.
//
//go:embed mark.svg
var markSVG []byte

// Mark serves that file at a URL of its own rather than inlining it.
//
// Inline would be two 6 KB copies in every response -- the icon and the header
// -- on a page that refreshes itself every minute. As a separate response it is
// fetched once and cached, and it is still same-origin and still served by this
// process, so the page's "no external asset" guarantee is intact: nothing here
// reaches a CDN, and a gateway can put a content policy in front of this page
// without an exception for anybody else's domain.
//
// Cached for a day. The mark changes about never, and a stale one for a day is
// not a failure anyone would notice.
func (s *Server) Mark() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		_, _ = w.Write(markSVG)
	}
}

// PipelineHandler serves the report itself, in the caller's format.
//
// Markdown by default, exactly what every existing curl and script gets;
// ?format=text for a terminal; and the HTML page when a browser asks, which is
// what turns a port-forward plus a browser into the page with no new URL to
// know. The 503 before the first sweep stays on the machine formats: a scraper
// that reads an empty 200 records "nothing is wrong" as a measurement, but a
// human gets the page, which says "no sweep yet" in words.
func (s *Server) PipelineHandler() http.HandlerFunc {
	page := s.Page()
	return func(w http.ResponseWriter, r *http.Request) {
		f := r.URL.Query().Get("format")
		if f == "" && prefersHTML(r) {
			f = "html"
		}
		if f == "html" {
			page(w, r)
			return
		}
		var rep *pipeline.Report
		if s.Report != nil {
			rep = s.Report()
		}
		if rep == nil {
			msg := "no sweep has completed yet"
			if s.Report == nil {
				msg = "pipeline supervision is off"
			}
			http.Error(w, msg, http.StatusServiceUnavailable)
			return
		}
		if f == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			rep.Text(w)
			return
		}
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		rep.Render(w)
	}
}

// prefersHTML reports whether the client is a browser. The whole Accept
// grammar is not needed to tell a browser from curl: a browser leads with
// text/html, curl sends */*, and a wrong guess here still serves a truthful
// report in the other format.
func prefersHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// The view: everything pre-formatted, so the template stays a layout and every
// sentence lives in code that can be tested.

type view struct {
	Brand     string
	Theme     string
	Version   string
	Repo      string
	RepoLink  string
	CheckName string
	Model     string
	Now       string

	Banner      string
	BannerClass string
	BannerNote  string

	Gate     *gateView
	Triage   *triageView
	Sweep    *sweepView
	Clusters string
	Features []Feature
	Egress   string

	Sections []sectionView
	Checked  string
	Notes    []string
}

type gateView struct {
	Poll     string
	SweptAgo string
	Err      string
	Open     []gatePRView
	Summary  string
}

type gatePRView struct {
	Number int
	Title  string
	URL    string
	State  string
}

type triageView struct {
	Active string
	Queued string
	Totals string
}

type sweepView struct {
	On       bool
	Every    string
	SweptAgo string
}

type sectionView struct {
	Mark  string
	Title string
	Blurb string
	Finds []findingView
}

type findingView struct {
	Summary string
	Detail  string
	Remedy  string
	Since   string
}

func (s *Server) view() *view {
	now := time.Now()
	v := &view{
		Brand:     s.Brand,
		Theme:     s.Theme,
		Version:   s.version(),
		Repo:      s.Repo,
		RepoLink:  s.RepoLink,
		CheckName: s.CheckName,
		Model:     s.Model,
		Now:       now.UTC().Format("2006-01-02 15:04:05 UTC"),
		Clusters:  plural(s.Clusters, "cluster") + " in the inventory at start-up.",
		Features:  s.Features,
		Egress:    s.EgressLine,
	}

	if s.Gate != nil {
		g := s.Gate()
		gv := &gateView{
			Poll:     human(s.GatePoll),
			SweptAgo: ago(g.SweptAt, now),
			Err:      g.Err,
			Summary:  plural(g.Held, "verdict") + " held in memory",
		}
		if g.Running > 0 {
			gv.Summary += ", " + plural(g.Running, "render") + " in flight"
		}
		for _, pr := range g.Open {
			gv.Open = append(gv.Open, gatePRView(pr))
		}
		v.Gate = gv
	}

	if s.Triage != nil {
		t := s.Triage()
		tv := &triageView{
			Active: "Idle.",
			Totals: fmt.Sprintf("%s since start-up, %d failed", plural(t.Done, "triage"), t.Failed),
		}
		if n := len(t.InFlight); n > 0 {
			tv.Active = "Triaging " + prList(t.InFlight)
		}
		if len(t.Queued) > 0 {
			tv.Queued = "A newer promotion is waiting on " + prList(t.Queued)
		}
		v.Triage = tv
	}

	var rep *pipeline.Report
	if s.Report != nil {
		rep = s.Report()
	}
	v.Sweep = &sweepView{On: s.SweepEvery > 0, Every: human(s.SweepEvery)}

	switch {
	case s.SweepEvery == 0 && rep == nil:
		v.Banner = "Pipeline supervision is off"
		v.BannerClass = "off"
		v.BannerNote = "This page still shows the gate and the triage; nothing is watching for the promotions that never happened. supervise.enabled turns the sweep on."
	case rep == nil:
		v.Banner = "No sweep has completed yet"
		v.BannerClass = "unswept"
		v.BannerNote = "Nothing has been read, so nothing is claimed. This is not a clean bill of health."
	default:
		v.Sweep.SweptAgo = ago(rep.At, now)
		v.Banner = rep.Headline()
		v.BannerClass = bannerClass(rep)
		if rep.Clean() {
			v.BannerNote = "Every Stage has promoted its latest freight, every Warehouse is discovering on schedule, and every tracked pin writes to a key that exists."
		}
		v.Sections = sections(rep)
		v.Checked = checkedLine(rep)
		v.Notes = rep.Checked.Notes
	}
	return v
}

func bannerClass(r *pipeline.Report) string {
	switch r.Worst() {
	case pipeline.Blocking:
		return "blocking"
	case pipeline.Degraded:
		return "degraded"
	case pipeline.Note:
		return "note"
	default:
		if r.Checked.Stages == 0 {
			return "unswept"
		}
		return "ok"
	}
}

// sections groups findings the way the markdown report does, worst first,
// with the same titles and the same blurbs. Two renderings of one report must
// not have two vocabularies.
func sections(r *pipeline.Report) []sectionView {
	bySeverity := map[pipeline.Severity][]pipeline.Finding{}
	for _, f := range r.Findings {
		bySeverity[f.Severity] = append(bySeverity[f.Severity], f)
	}
	var out []sectionView
	for _, sev := range []pipeline.Severity{pipeline.Blocking, pipeline.Degraded, pipeline.Note} {
		fs := bySeverity[sev]
		if len(fs) == 0 {
			continue
		}
		sv := sectionView{
			Mark:  sev.Mark(),
			Title: sev.SectionTitle(),
			Blurb: sev.SectionBlurb(),
		}
		for _, f := range fs {
			fv := findingView{
				Summary: f.Summary,
				Detail:  strings.TrimSpace(f.Detail),
				Remedy:  strings.TrimSpace(f.Remedy),
			}
			if f.Since > 0 {
				fv.Since = "held " + human(f.Since)
			}
			sv.Finds = append(sv.Finds, fv)
		}
		out = append(out, sv)
	}
	return out
}

// checkedLine is the honesty section, the same accounting the markdown report
// closes with. A page that printed findings and stopped would be making
// exactly the mistake the supervisor exists to catch.
func checkedLine(r *pipeline.Report) string {
	c := r.Checked
	line := fmt.Sprintf("Checked %s, %s, %s, %s",
		plural(c.Stages, "Stage"),
		plural(c.Warehouses, "Warehouse"),
		plural(c.Promotions, "promotion"),
		plural(c.PullRequests, "open pull request"))
	if c.PinsScanned > 0 {
		line += " and " + plural(c.PinsScanned, "tracked pin")
	}
	if len(r.Namespaces) > 0 {
		line += " in " + strings.Join(r.Namespaces, ", ")
	}
	return line + "."
}

// version is what the operator deployed when the chart said so, and otherwise
// whatever the binary can prove about itself. The image is built without its
// .git directory, so the VCS stamp is a fallback for source builds, not the
// normal path.
func (s *Server) version() string {
	if s.Version != "" {
		return s.Version
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown build"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	var rev, dirty string
	for _, kv := range bi.Settings {
		switch kv.Key {
		case "vcs.revision":
			rev = kv.Value
		case "vcs.modified":
			if kv.Value == "true" {
				dirty = " (modified)"
			}
		}
	}
	if len(rev) >= 12 {
		return rev[:12] + dirty
	}
	return "unknown build"
}

// ago says how long past a moment is, in the unit an operator would use, and
// "never" for the zero time, which is the difference between "swept 10s ago"
// and "has not swept".
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < time.Second {
		return "just now"
	}
	return human(d) + " ago"
}

// human and plural mirror the pipeline package's own renderers: the largest
// unit that still carries information, and no "1 Stages".
func human(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// prList names pull requests the way a person would: "PR #12", or
// "PRs #12 and #34".
func prList(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	noun := "PR "
	if len(nums) > 1 {
		noun = "PRs "
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return noun + parts[0]
	default:
		return noun + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

var pageTmpl = template.Must(template.New("page").Parse(pageHTML))
