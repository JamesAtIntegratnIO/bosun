package cluster

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The whole Kargo reader was untested while pipeline/detect.go, which consumes
// what it produces, was at 99%. Every detector in that file reasons about
// fields decoded here, so a field that stops arriving is a detector that
// silently stops firing -- with the fixtures still green.

// serveJSON answers every request with the same body.
func serveJSON(t *testing.T, body string) *APIServer {
	t.Helper()
	return serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// The `yaml-update` steps are the authoritative list of which files and keys
// promotions actually rewrite -- which is what the dead-pin detector compares
// against the repository.
func TestStagesCarryTheirPromotionUpdates(t *testing.T) {
	a := serveJSON(t, `{"items":[{
		"metadata":{"name":"cert-manager","namespace":"addons"},
		"spec":{"promotionTemplate":{"spec":{"steps":[
			{"uses":"git-clone","config":{"path":"ignored"}},
			{"uses":"yaml-update","config":{"path":"./repo/values.yaml",
				"updates":[{"key":"certManager.defaultVersion"},{"key":""}]}},
			{"uses":"yaml-update","config":{"path":"./repo/other.yaml","updates":[]}}
		]}}},
		"status":{
			"freightHistory":[{"items":{"w":{"name":"freight-abc"}},
				"verificationHistory":[{"id":"run-1","phase":"Failed"}]}],
			"conditions":[{"type":"Ready","status":"False","reason":"VerificationFailed",
				"message":"the analysis said no","lastTransitionTime":"2020-01-01T00:00:00Z"}]}}]}`)

	got, err := a.Stages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d stages", len(got))
	}
	st := got[0]
	if st.Name != "cert-manager" || st.Namespace != "addons" {
		t.Errorf("got %+v", st)
	}
	// Only yaml-update steps, and only those with at least one non-empty key:
	// a step that rewrites nothing is not a pin the detector can check.
	if len(st.Updates) != 1 || st.Updates[0].Path != "./repo/values.yaml" {
		t.Fatalf("want one update step, got %+v", st.Updates)
	}
	if len(st.Updates[0].Keys) != 1 || st.Updates[0].Keys[0] != "certManager.defaultVersion" {
		t.Errorf("empty keys must be dropped, got %v", st.Updates[0].Keys)
	}
	if st.CurrentFreight != "freight-abc" {
		t.Errorf("got %q", st.CurrentFreight)
	}
	// The verification id is what `kargo reverify` takes; without it the
	// remedy for a stuck verification is a paragraph instead of a command.
	if st.VerificationID != "run-1" || st.VerificationPhase != "Failed" {
		t.Errorf("got %q / %q", st.VerificationID, st.VerificationPhase)
	}
	if st.Ready {
		t.Error("Ready=False must not decode as ready")
	}
	if st.ReadyReason != "VerificationFailed" || st.ReadyMessage != "the analysis said no" {
		t.Errorf("got %q / %q", st.ReadyReason, st.ReadyMessage)
	}
	if st.ReadySince <= 0 {
		t.Error("ReadySince is what every staleness threshold compares against")
	}
}

// A Warehouse that is Ready and has discovered nothing since last week is the
// failure that produces no event and no error.
func TestWarehousesCarryTheDiscoveryTimestampAndLatest(t *testing.T) {
	a := serveJSON(t, `{"items":[{
		"metadata":{"name":"charts","namespace":"addons"},
		"spec":{"interval":"5m"},
		"status":{
			"conditions":[{"type":"Ready","status":"True"}],
			"discoveredArtifacts":{"discoveredAt":"2024-05-01T10:00:00Z",
				"charts":[{"versions":["1.2.3","1.2.2"]}]}}}]}`)

	got, err := a.Warehouses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	w := got[0]
	if !w.Ready || w.Interval != 5*time.Minute {
		t.Errorf("got ready=%v interval=%v", w.Ready, w.Interval)
	}
	if w.Latest != "1.2.3" {
		t.Errorf("the newest version is the one worth reporting, got %q", w.Latest)
	}
	if w.DiscoveredAt.IsZero() {
		t.Error("the discovery timestamp is the whole point of this read")
	}
}

// An unparseable interval disables the staleness check for that Warehouse
// rather than inventing a threshold for it.
func TestAnUnparseableIntervalLeavesItZero(t *testing.T) {
	a := serveJSON(t, `{"items":[{"metadata":{"name":"w"},"spec":{"interval":"soon"},
		"status":{"discoveredArtifacts":{}}}]}`)
	got, err := a.Warehouses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Interval != 0 {
		t.Errorf("got %v, want zero", got[0].Interval)
	}
}

// Images are the fallback when a Warehouse tracks no charts.
func TestWarehouseFallsBackToAnImageTag(t *testing.T) {
	a := serveJSON(t, `{"items":[{"metadata":{"name":"w"},"spec":{},
		"status":{"discoveredArtifacts":{"images":[{"references":[{"tag":"v9"}]}]}}}]}`)
	got, err := a.Warehouses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Latest != "v9" {
		t.Errorf("got %q", got[0].Latest)
	}
}

// A Pending promotion has no StartedAt -- it has not begun -- so its age comes
// from creation, and that age is what separates a moving queue from a stopped
// one.
func TestPromotionsCarryBothTimestamps(t *testing.T) {
	a := serveJSON(t, `{"items":[
		{"metadata":{"name":"p.running","creationTimestamp":"2024-05-01T09:00:00Z"},
		 "spec":{"stage":"s","freight":"f"},
		 "status":{"phase":"Running","startedAt":"2024-05-01T09:01:00Z","message":"going"}},
		{"metadata":{"name":"p.pending","creationTimestamp":"2024-05-01T08:00:00Z"},
		 "spec":{"stage":"s","freight":"f2"},"status":{"phase":"Pending"}}]}`)

	got, err := a.Promotions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].Phase != "Running" || got[0].StartedAt.IsZero() || got[0].Message != "going" {
		t.Errorf("got %+v", got[0])
	}
	if !got[1].StartedAt.IsZero() {
		t.Error("a Pending promotion has not started")
	}
	if got[1].CreatedAt.IsZero() {
		t.Error("creation is the only timestamp a Pending promotion has, and its age depends on it")
	}
}

// A supervisor that silently read the first page would report a wedged Stage
// as healthy because its promotion was on page two.
func TestListAllWalksEveryPage(t *testing.T) {
	var seen []string
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query().Get("continue"))
		if r.URL.Query().Get("continue") == "" {
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"a"},"spec":{},"status":{}}],
				"metadata":{"continue":"next"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"b"},"spec":{},"status":{}}],
			"metadata":{"continue":""}}`))
	}))

	got, err := a.Promotions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("both pages must arrive, got %+v", got)
	}
	if len(seen) != 2 || seen[1] != "next" {
		t.Errorf("the continue token must be sent back, got %v", seen)
	}
}

// "Kargo is not installed here" and "no Stages found" are different sentences
// with different actions.
func TestKargoAvailable(t *testing.T) {
	up := serveJSON(t, `{"resources":[]}`)
	if !up.KargoAvailable(context.Background()) {
		t.Error("a served API must read as available")
	}

	down := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the server could not find the requested resource", http.StatusNotFound)
	}))
	if down.KargoAvailable(context.Background()) {
		t.Error("a 404 on the API group means Kargo is not installed")
	}
}

// A read that fails must reach the sweep, which turns it into a note naming
// the detector that therefore did not run.
func TestAFailedReadIsReturnedNotSwallowed(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	for name, read := range map[string]func() error{
		"stages":     func() error { _, err := a.Stages(context.Background()); return err },
		"warehouses": func() error { _, err := a.Warehouses(context.Background()); return err },
		"promotions": func() error { _, err := a.Promotions(context.Background()); return err },
	} {
		err := read()
		if err == nil {
			t.Errorf("%s: a 403 must not read as an empty list", name)
			continue
		}
		// A sentence naming the collection, not a bare status code: this ends
		// up in the report as "X could not be read, so Y would not have been
		// found", and "403" there tells an operator less than "not permitted
		// to list stages" does.
		if !strings.Contains(err.Error(), "not permitted") || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: the error must say what could not be read: %v", name, err)
		}
	}
}
