package gitprovider

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T, pkcs8 bool) []byte {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if pkcs8 {
		der, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			t.Fatal(err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	return pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

// GitHub has issued App keys in both encodings over the years, and a key that
// will not parse is a pod that will not start.
func TestParsesBothKeyEncodings(t *testing.T) {
	for _, pkcs8 := range []bool{false, true} {
		if _, err := parseKey(testKey(t, pkcs8)); err != nil {
			t.Errorf("pkcs8=%v: %v", pkcs8, err)
		}
	}
}

// The most common way to get this wrong is to store the base64 of the PEM
// rather than the PEM, which yields a blob that is not PEM at all. The error
// says so rather than making someone guess.
func TestANonPEMKeySaysWhatIsProbablyWrong(t *testing.T) {
	_, err := parseKey([]byte(base64.StdEncoding.EncodeToString(testKey(t, false))))
	if err == nil {
		t.Fatal("base64-of-PEM should not parse")
	}
	if !strings.Contains(err.Error(), "base64") {
		t.Errorf("the error should name the likely cause, got %q", err)
	}
}

// The JWT must be something GitHub will accept: three parts, RS256, and an
// expiry inside the ten minutes GitHub allows.
func TestTheJWTIsShapedTheWayGitHubRequires(t *testing.T) {
	a := &AppAuth{AppID: "123", PrivateKey: testKey(t, false)}
	tok, err := a.jwt()
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("want three JWT segments, got %d", len(parts))
	}
	var hdr struct{ Alg, Typ string }
	raw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if err := json.Unmarshal(raw, &hdr); err != nil || hdr.Alg != "RS256" {
		t.Fatalf("header must be RS256, got %s (%v)", raw, err)
	}
	var claims struct {
		Iat, Exp int64
		Iss      string
	}
	raw, _ = base64.RawURLEncoding.DecodeString(parts[1])
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "123" {
		t.Errorf("iss should be the app id, got %q", claims.Iss)
	}
	// GitHub rejects an expiry more than ten minutes out, and is unforgiving
	// about clock skew in the other direction.
	if d := time.Until(time.Unix(claims.Exp, 0)); d > 10*time.Minute || d < time.Minute {
		t.Errorf("exp is %v away; GitHub allows at most 10m", d)
	}
	if claims.Iat > time.Now().Unix() {
		t.Error("iat must be backdated, or clock skew rejects the assertion")
	}
}

// Installation tokens live about an hour. Minting one per API call would be
// absurd; holding one past expiry fails the push, which is the most expensive
// moment to find out.
func TestTheInstallationTokenIsCachedAndRefreshedEarly(t *testing.T) {
	var mints int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			mints++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation", "expires_at": time.Now().Add(time.Hour)})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := &AppAuth{AppID: "123", PrivateKey: testKey(t, false),
		Owner: "o", Repo: "r", APIBase: srv.URL, HTTP: srv.Client()}

	for i := 0; i < 3; i++ {
		tok, err := a.Token(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if tok != "ghs_installation" {
			t.Fatalf("got %q", tok)
		}
	}
	if mints != 1 {
		t.Errorf("a valid token must be reused, minted %d times", mints)
	}

	// A token near expiry is replaced before it can fail mid-triage.
	a.expiry = time.Now().Add(30 * time.Second)
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mints != 2 {
		t.Errorf("a token expiring in 30s must be refreshed, minted %d times", mints)
	}
}

// The installation id is discovered from the repository, so it cannot be
// configured wrongly. An app installed nowhere near this repository must say
// that, not fail somewhere later with a 404 on an ordinary-looking call.
func TestAnUninstalledAppSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	a := &AppAuth{AppID: "123", PrivateKey: testKey(t, false),
		Owner: "o", Repo: "r", APIBase: srv.URL, HTTP: srv.Client()}
	_, err := a.Token(context.Background())
	if err == nil {
		t.Fatal("an uninstalled app must fail")
	}
	if !strings.Contains(err.Error(), "is the app installed") {
		t.Errorf("the error should point at the likely cause, got %q", err)
	}
}

// The exact shape that crash-looped production: a valid PEM whose newlines a
// secret store removed. Byte for byte the right key, and pem.Decode refuses it.
func TestAPEMFlattenedToOneLineStillParses(t *testing.T) {
	good := testKey(t, false)
	flat := strings.ReplaceAll(string(good), "\n", "")
	if strings.Contains(flat, "\n") {
		t.Fatal("fixture is not flat")
	}
	k, err := parseKey([]byte(flat))
	if err != nil {
		t.Fatalf("a flattened PEM must still parse: %v", err)
	}
	orig, err := parseKey(good)
	if err != nil {
		t.Fatal(err)
	}
	if k.N.Cmp(orig.N) != 0 {
		t.Fatal("repaired key differs from the original")
	}
}

// Spaces instead of newlines is the other way vaults mangle a PEM.
func TestAPEMWithSpacesForNewlinesStillParses(t *testing.T) {
	flat := strings.ReplaceAll(string(testKey(t, true)), "\n", " ")
	if _, err := parseKey([]byte(flat)); err != nil {
		t.Fatalf("a space-separated PEM must still parse: %v", err)
	}
}

// Repair must not turn genuine rubbish into a confusing success, and the error
// still has to name the likely cause.
func TestRepairDoesNotRescueSomethingThatIsNotAKey(t *testing.T) {
	for _, bad := range [][]byte{
		[]byte("not a key at all"),
		[]byte(base64.StdEncoding.EncodeToString(testKey(t, false))),
		[]byte("-----BEGIN RSA PRIVATE KEY----- ohno -----END RSA PRIVATE KEY-----"),
		{},
	} {
		if _, err := parseKey(bad); err == nil {
			t.Errorf("should have been refused: %.40q", bad)
		}
	}
}

// The commit identity is the App's own bot account, in the exact format
// GitHub attributes: `<slug>[bot]` and `<id>+<slug>[bot]@users.noreply`.
// Anything else in that namespace attributes the commit to whoever owns the
// name, measured live, when the default `bosun@users.noreply.github.com`
// rendered the first repair under a stranger's avatar.
func TestBotIdentityIsTheAppsOwn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app":
			_ = json.NewEncoder(w).Encode(map[string]any{"slug": "bosun-mate"})
		case strings.HasSuffix(r.URL.Path, "/installation"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42})
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token": "ghs_installation", "expires_at": time.Now().Add(time.Hour)})
		case strings.Contains(r.URL.Path, "/users/"):
			if !strings.Contains(r.URL.Path, "bosun-mate%5Bbot%5D") &&
				!strings.Contains(r.URL.Path, "bosun-mate[bot]") {
				t.Errorf("looked up the wrong user: %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 4694})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	a := &AppAuth{AppID: "123", PrivateKey: testKey(t, false),
		Owner: "o", Repo: "r", APIBase: srv.URL, HTTP: srv.Client()}

	name, email, err := a.BotIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if name != "bosun-mate[bot]" {
		t.Errorf("name = %q", name)
	}
	if email != "4694+bosun-mate[bot]@users.noreply.github.com" {
		t.Errorf("email = %q", email)
	}
}
