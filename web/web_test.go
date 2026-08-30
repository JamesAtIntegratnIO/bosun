package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/pipeline"
)

// The page is the report with a browser in front of it, so what these tests
// guard is the contract that makes that safe and honest: everything the
// cluster or a pull request wrote is escaped on the way through, the two
// machine formats never change shape underneath the scripts that read them,
// and the states nobody should confuse, "nothing wrong" and "nobody looked",
// never render the same.

func testReport() *pipeline.Report {
	return &pipeline.Report{
		At: time.Now().Add(-90 * time.Second),
		Findings: []pipeline.Finding{{
			Kind:     pipeline.KindWedged,
			Severity: pipeline.Blocking,
			Subject:  "argo-cd",
			Summary:  `Stage argo-cd <script>alert(1)</script> has stopped`,
			Detail:   "The latest promotion errored 3 days ago & nothing retried it.",
			Remedy:   `kubectl -n kargo-pipelines annotate promotion x 'kargo.akuity.io/abort={"action":"terminate"}'`,
			Since:    72 * time.Hour,
		}},
		Namespaces: []string{"kargo-pipelines"},
		Checked:    pipeline.Checked{Stages: 4, Warehouses: 4, Promotions: 12, PullRequests: 2},
	}
}

func testServer(rep *pipeline.Report) *Server {
	s := &Server{
		Brand:      "Bosun",
		Version:    "0.29.0",
		Repo:       "example/platform",
		RepoLink:   "https://github.com/example/platform",
		CheckName:  "addons-gate",
		Model:      "openai/example-model",
		GatePoll:   30 * time.Second,
		SweepEvery: 10 * time.Minute,
		Clusters:   2,
		Features:   []Feature{{Name: "Explain green gates", On: true}, {Name: "Live cluster reads", On: false}},
		EgressLine: "Every outbound request is logged.",
		Gate: func() GateStatus {
			return GateStatus{
				SweptAt: time.Now().Add(-10 * time.Second),
				Open: []GatePR{{Number: 7, Title: "chore: bump <b>podinfo</b>",
					URL: "https://git.example/pr/7", State: "passing"}},
				Held: 1,
			}
		},
		Triage: func() TriageStatus {
			return TriageStatus{InFlight: []int{7}, Done: 5, Failed: 1}
		},
	}
	if rep != nil {
		s.Report = func() *pipeline.Report { return rep }
	}
	return s
}

func getPage(t *testing.T, s *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Page()(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("the page always has something true to say; got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content type %q", ct)
	}
	return rec.Body.String()
}

func TestPageCarriesTheReportAndItsRemedy(t *testing.T) {
	body := getPage(t, testServer(testReport()))

	for _, want := range []string{
		"Promotion pipeline",       // the headline
		"Not delivering",           // the blocking section, same title as markdown
		"held 3d",                  // how long the situation has held
		"kargo.akuity.io/abort",    // the remedy, the most valuable field
		"Checked 4 Stages",         // the honesty section
		"https://git.example/pr/7", // the gate's open pull request, linked
		"Triaging PR #7",
		"5 triages since start-up, 1 failed",
		"2 clusters in the inventory",
		"openai/example-model",
		"0.29.0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestPageEscapesWhatTheClusterAndThePullRequestWrote(t *testing.T) {
	// Finding text quotes cluster objects and pull request titles are
	// whatever the bump wrote; the page may be exposed through a gateway, so
	// either reaching the browser unescaped is script injection into every
	// operator who looks.
	body := getPage(t, testServer(testReport()))
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("a finding summary reached the browser unescaped")
	}
	if strings.Contains(body, "<b>podinfo</b>") {
		t.Fatal("a pull request title reached the browser unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("the hostile summary should still be shown, escaped, not dropped")
	}
}

func TestPageBeforeFirstSweepClaimsNothing(t *testing.T) {
	s := testServer(nil)
	s.Report = func() *pipeline.Report { return nil }
	body := getPage(t, s)
	if !strings.Contains(body, "No sweep has completed yet") {
		t.Fatal("an unswept page must say so")
	}
	if !strings.Contains(body, "not a clean bill of health") {
		t.Fatal("an unswept page must not read as a healthy one")
	}
}

func TestPageWithSupervisionOffSaysSo(t *testing.T) {
	s := testServer(nil)
	s.SweepEvery = 0
	body := getPage(t, s)
	if !strings.Contains(body, "Pipeline supervision is off") {
		t.Fatal("supervision off and 'no sweep yet' are different situations and must not render the same")
	}
}

func TestPipelineHandlerKeepsTheMachineFormats(t *testing.T) {
	h := testServer(testReport()).PipelineHandler()

	// The default is what every existing curl and script gets: markdown,
	// marker first, so a publisher can find its own previous copy.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/pipeline", nil))
	if !strings.HasPrefix(rec.Body.String(), pipeline.ReportMarker) {
		t.Fatalf("default output must stay markdown with the marker first; got %q", rec.Body.String()[:40])
	}

	// ?format=text is the terminal form, unchanged.
	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/pipeline?format=text", nil))
	if strings.Contains(rec.Body.String(), "####") || !strings.Contains(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatal("?format=text must stay the plain form")
	}

	// A browser gets the page with no new URL to know.
	req := httptest.NewRequest("GET", "/pipeline", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	rec = httptest.NewRecorder()
	h(rec, req)
	if !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatal("a browser asking for /pipeline should get the page")
	}

	// An explicit format beats the Accept header: a browser is also how
	// somebody copies the markdown.
	req = httptest.NewRequest("GET", "/pipeline?format=markdown", nil)
	req.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	h(rec, req)
	if !strings.HasPrefix(rec.Body.String(), pipeline.ReportMarker) {
		t.Fatal("?format=markdown must win over the Accept header")
	}
}

func TestPipelineHandlerBeforeFirstSweepIs503ForMachines(t *testing.T) {
	s := testServer(nil)
	s.Report = func() *pipeline.Report { return nil }
	h := s.PipelineHandler()

	// A machine format must not serve an empty 200: a scraper would record
	// "nothing is wrong" as a measurement.
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest("GET", "/pipeline", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("markdown before the first sweep must be 503, got %d", rec.Code)
	}

	// A human gets the page, which says "no sweep yet" in words.
	req := httptest.NewRequest("GET", "/pipeline", nil)
	req.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No sweep has completed yet") {
		t.Fatalf("a browser before the first sweep gets the honest page, got %d", rec.Code)
	}
}

// The theme reaches the document, and "follow the system" stamps nothing.
func TestThemeStampsTheDocument(t *testing.T) {
	for _, tc := range []struct {
		name  string
		theme string
		want  string
	}{
		{"auto stamps no attribute", "", `<html lang="en">`},
		{"dark", "dark", `<html lang="en" data-theme="dark">`},
		{"light", "light", `<html lang="en" data-theme="light">`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{Brand: "Bosun", Theme: tc.theme}
			rec := httptest.NewRecorder()
			s.Page()(rec, httptest.NewRequest(http.MethodGet, "/", nil))

			if got := rec.Body.String(); !strings.Contains(got, tc.want) {
				t.Errorf("page does not carry %q", tc.want)
			}
			// Scoped to the element, not the document: the stylesheet
			// mentions data-theme in two selectors, and matching those would
			// make this assertion pass for the wrong reason.
			html := rec.Body.String()
			tag := html[strings.Index(html, "<html"):]
			tag = tag[:strings.Index(tag, ">")+1]
			if tc.theme == "" && strings.Contains(tag, "data-theme") {
				t.Errorf("an unset theme stamped %s; that overrides the reader's system preference", tag)
			}
		})
	}
}

// The mark is served, and served as an SVG.
func TestMarkIsServed(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Server{}).Mark()(rec, httptest.NewRequest(http.MethodGet, "/mark.svg", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("Content-Type %q, want image/svg+xml", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "<svg") {
		t.Error("body is not an SVG")
	}
}
