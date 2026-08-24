package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Everything here is about one distinction: an answer nobody was allowed to
// give, versus an answer. Both are zero if you are careless, and the careless
// version is the dangerous one -- "0 live objects on the dropped versions" is
// the sentence that ends a conversation, and it must never be produced by not
// having looked.

func serverFor(t *testing.T, h http.Handler) *APIServer {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	for name, content := range map[string]string{
		"token": "tok-1", "namespace": "bosun",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &APIServer{Host: srv.URL, Dir: dir, HTTP: srv.Client()}
}

func page(items int, cont string) string {
	raw := make([]json.RawMessage, items)
	for i := range raw {
		raw[i] = json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(map[string]any{
		"items":    raw,
		"metadata": map[string]string{"continue": cont},
	})
	return string(b)
}

// The apiserver sets metadata.remainingItemCount only for etcd-served lists --
// not the default watch-cache path -- and documents it as best-effort. A count
// that trusted it would silently under-report and then present the number as a
// fact, so the pages get walked.
func TestItWalksThePagesRatherThanTrustingAHintTheApiserverMayNotSet(t *testing.T) {
	pages := 0
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pages++
		if r.URL.Query().Get("continue") == "" {
			fmt.Fprint(w, page(500, "next"))
			return
		}
		fmt.Fprint(w, page(7, ""))
	}))

	got := a.CountLive(context.Background(), "external-secrets.io", "v1beta1", "externalsecrets")
	if !got.Known || got.N != 507 || got.AtLeast {
		t.Fatalf("count = %+v, want a known 507", got)
	}
	if pages != 2 {
		t.Fatalf("read %d pages, want 2", pages)
	}
}

// The difference this type exists for.
func TestNotPermittedIsNotZero(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))

	got := a.CountLive(context.Background(), "kyverno.io", "v1", "policies")
	if got.Known {
		t.Fatalf("a refusal was reported as an answer: %+v", got)
	}
	if !strings.Contains(got.String(), "not permitted") || !strings.Contains(got.String(), "kyverno.io") {
		t.Fatalf("the note does not say what could not be checked: %q", got.String())
	}
}

// A CustomResourceDefinition removed outright is exactly when "is anything
// still using this" is most worth answering, and exactly when an apiextensions
// lookup would 404. Asking for the collection instead gives a real answer.
func TestAnApiTheClusterDoesNotServeIsAnAnswerAndNotARefusal(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	got := a.CountLive(context.Background(), "kyverno.io", "v2alpha1", "policyexceptions")
	if !got.Known {
		t.Fatalf("a served-nowhere API was reported as unchecked: %+v", got)
	}
	if !strings.Contains(got.String(), "does not serve") {
		t.Fatalf("the note does not say what was found: %q", got.String())
	}
}

// A floor ends the same conversation as a total. Walking a hundred thousand
// objects to turn "a lot" into a number would not.
func TestAVeryLargeCountBecomesAFloorAndSaysSo(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, page(500, "more"))
	}))

	got := a.CountLive(context.Background(), "g", "v1", "things")
	if !got.Known || !got.AtLeast {
		t.Fatalf("count = %+v, want a floor", got)
	}
	if !strings.HasPrefix(got.String(), "at least ") {
		t.Fatalf("a floor was printed as a total: %q", got.String())
	}
}

// The core group is the one irregularity in the whole API surface, and the one
// thing a hand-rolled client gets wrong.
func TestTheCoreGroupLivesUnderApiAndNotApis(t *testing.T) {
	var path string
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		fmt.Fprint(w, page(0, ""))
	}))
	a.CountLive(context.Background(), "", "v1", "pods")
	if path != "/api/v1/pods" {
		t.Fatalf("asked for %q", path)
	}
}

// Projected service-account tokens are BOUND: they expire in about an hour and
// the kubelet rewrites the file in place. A client that read the token once
// works for fifty minutes and then 401s forever, which on a service called a
// few times a day looks fine in every test.
func TestTheTokenIsReReadOnEveryRequest(t *testing.T) {
	var seen []string
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		fmt.Fprint(w, page(0, ""))
	}))

	a.CountLive(context.Background(), "g", "v1", "things")
	if err := os.WriteFile(filepath.Join(a.Dir, "token"), []byte("tok-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	a.CountLive(context.Background(), "g", "v1", "things")

	if len(seen) != 2 || seen[0] != "Bearer tok-1" || seen[1] != "Bearer tok-2" {
		t.Fatalf("credentials sent: %v -- a rotated token was not picked up", seen)
	}
}

// "This Application was already Degraded before your bump" is the single most
// useful thing that can be said to somebody looking at a red gate.
func TestApplicationHealthIsReadFromTheLiveObject(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/namespaces/argocd/applications/external-secrets-host") {
			t.Errorf("asked for %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": map[string]any{
				"health": map[string]string{"status": "Degraded"},
				"sync":   map[string]string{"status": "OutOfSync"},
			},
		})
	}))

	got := a.AppHealth(context.Background(), "external-secrets-host")
	if !got.Known || got.Status != "Degraded" || got.Sync != "OutOfSync" {
		t.Fatalf("health = %+v", got)
	}
	if got.String() != "Degraded / OutOfSync" {
		t.Fatalf("rendered %q", got.String())
	}
}

// An Application a promotion says it will verify and the cluster does not have
// is a finding, not an absence of one.
func TestAMissingApplicationSaysSoRatherThanGoingQuiet(t *testing.T) {
	a := serverFor(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	got := a.AppHealth(context.Background(), "nope")
	if !got.Known || !strings.Contains(got.String(), "no Application nope") {
		t.Fatalf("health = %+v", got)
	}
}
