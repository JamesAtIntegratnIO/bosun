package gitprovider

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// AppAuth authenticates as a GitHub App installation rather than as a person.
//
// THIS IS ABOUT IDENTITY, not permissions. A fine-grained token works fine and
// grants exactly the same access -- but it belongs to whoever minted it, so
// every comment the agent writes carries that person's name and avatar, and
// every reader has to notice the "automated triage, not a review" footer to
// learn otherwise. An agent indistinguishable from a colleague at a glance is
// a problem in the seconds before someone acts on what it said.
//
// An App has a face: comments arrive from `yourapp[bot]`, with its own avatar,
// in its own timeline entry. Nobody has to be told.
//
// Two other things follow, both of which matter more than they sound:
//
//   - INSTALLATION TOKENS EXPIRE, in an hour, and are minted on demand from a
//     key. A leaked one is a bad hour rather than a standing grant. The PAT
//     this replaces had no expiry at all.
//   - The App is its own principal, so revoking it does not disturb the
//     person who created it, and its actions are attributable to it alone.
//
// No JWT library. The token exchange is a signed header, a signed claim set
// and an HTTP call; pulling in a dependency to do that in a service whose
// whole argument is that it is small enough to audit would be a poor trade.
type AppAuth struct {
	// AppID is the App's numeric id, from its settings page.
	AppID string
	// PrivateKey is the PEM the App issued. PKCS#1 or PKCS#8.
	PrivateKey []byte
	// InstallationID is optional. Left empty it is discovered from the
	// repository, which removes a value that can be silently wrong -- an
	// installation id belonging to another org fails as a 404 on a call that
	// looks otherwise correct.
	InstallationID string
	Owner, Repo    string
	APIBase        string
	HTTP           *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
	instID string
}

func (a *AppAuth) base() string {
	if a.APIBase != "" {
		return strings.TrimSuffix(a.APIBase, "/")
	}
	return "https://api.github.com"
}

func (a *AppAuth) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// Token returns a valid installation token, minting one when the cached token
// is gone or nearly so.
//
// Refreshed a minute early on purpose: a token that expires mid-triage would
// fail the push, not the read, which is the most expensive moment to discover
// it.
func (a *AppAuth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Now().Before(a.expiry.Add(-time.Minute)) {
		return a.token, nil
	}

	jwt, err := a.jwt()
	if err != nil {
		return "", fmt.Errorf("signing the app JWT: %w", err)
	}
	inst, err := a.installation(ctx, jwt)
	if err != nil {
		return "", err
	}

	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := a.call(ctx, http.MethodPost,
		fmt.Sprintf("%s/app/installations/%s/access_tokens", a.base(), inst), jwt, &out); err != nil {
		return "", fmt.Errorf("minting an installation token: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("github returned an empty installation token")
	}
	a.token, a.expiry = out.Token, out.ExpiresAt
	return a.token, nil
}

// BotIdentity returns the commit author that attributes to the App itself:
// `<slug>[bot]` and `<id>+<slug>[bot]@users.noreply.github.com`, the exact
// format GitHub uses for its own bots.
//
// This exists because commit emails are unauthenticated display-matching, and
// the first live repair proved what that means: a commit authored with the
// default `bosun@users.noreply.github.com` rendered on the pull request under
// the avatar of the unrelated GitHub account named `bosun` -- the
// `<username>@users.noreply.github.com` namespace BELONGS to accounts, and an
// email in it that is not yours attributes your commit to a stranger. The
// comments were already the App's; the commits said someone else wrote them.
//
// The slug comes from GET /app (JWT -- the one endpoint family app JWTs are
// for) and the bot user's numeric id from GET /users/<slug>[bot] with an
// installation token.
func (a *AppAuth) BotIdentity(ctx context.Context) (name, email string, err error) {
	jwt, err := a.jwt()
	if err != nil {
		return "", "", fmt.Errorf("signing the app JWT: %w", err)
	}
	var app struct {
		Slug string `json:"slug"`
	}
	if err := a.call(ctx, http.MethodGet, a.base()+"/app", jwt, &app); err != nil {
		return "", "", fmt.Errorf("reading the app's own identity: %w", err)
	}
	if app.Slug == "" {
		return "", "", fmt.Errorf("github returned an app with no slug")
	}
	name = app.Slug + "[bot]"

	tok, err := a.Token(ctx)
	if err != nil {
		return "", "", err
	}
	var user struct {
		ID int64 `json:"id"`
	}
	if err := a.call(ctx, http.MethodGet, a.base()+"/users/"+url.PathEscape(name), tok, &user); err != nil {
		return "", "", fmt.Errorf("resolving the bot user %s: %w", name, err)
	}
	if user.ID == 0 {
		return "", "", fmt.Errorf("github returned no id for %s", name)
	}
	return name, fmt.Sprintf("%d+%s@users.noreply.github.com", user.ID, name), nil
}

// installation resolves which installation to act as, once, and remembers it.
func (a *AppAuth) installation(ctx context.Context, jwt string) (string, error) {
	if a.InstallationID != "" {
		return a.InstallationID, nil
	}
	if a.instID != "" {
		return a.instID, nil
	}
	var out struct {
		ID int64 `json:"id"`
	}
	// Asks "which installation covers THIS repository", so a misconfigured id
	// is not a thing that can exist.
	if err := a.call(ctx, http.MethodGet,
		fmt.Sprintf("%s/repos/%s/%s/installation", a.base(), a.Owner, a.Repo), jwt, &out); err != nil {
		return "", fmt.Errorf("finding the app installation on %s/%s "+
			"(is the app installed there?): %w", a.Owner, a.Repo, err)
	}
	a.instID = fmt.Sprintf("%d", out.ID)
	return a.instID, nil
}

func (a *AppAuth) call(ctx context.Context, method, url, jwt string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// jwt builds the short-lived assertion that proves possession of the App key.
//
// GitHub rejects an expiry more than ten minutes out and is unforgiving about
// clock skew, so this backdates iat by a minute and asks for nine.
func (a *AppAuth) jwt() (string, error) {
	key, err := parseKey(a.PrivateKey)
	if err != nil {
		return "", err
	}
	now := time.Now()
	header := `{"alg":"RS256","typ":"JWT"}`
	claims := fmt.Sprintf(`{"iat":%d,"exp":%d,"iss":%q}`,
		now.Add(-time.Minute).Unix(), now.Add(9*time.Minute).Unix(), a.AppID)

	signing := b64(header) + "." + b64(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// parseKey accepts both PEM encodings GitHub has issued, and repairs the one
// way a valid key reliably arrives broken.
//
// PEM is line-structured, and secret stores are not. A key pasted into a
// single-line field -- which is the default in most vaults, including
// 1Password -- arrives with every newline gone. It is still the right key,
// byte for byte, and pem.Decode refuses it.
//
// This cost a production crash-loop, and the error message even guessed the
// wrong cause: it blamed base64, because that is the failure everyone writes
// the message for, while the real key was a perfectly good PEM flattened to
// one line. Rebuilding the line breaks is deterministic, so the honest thing
// is to do it rather than make the next person find out the same way.
func parseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		block, _ = pem.Decode(rewrap(pemBytes))
	}
	if block == nil {
		return nil, fmt.Errorf("the app private key is not PEM: it must begin " +
			"-----BEGIN ... PRIVATE KEY----- and contain base64 (a secret holding " +
			"the base64 OF the PEM, rather than the PEM, is the usual cause)")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing the app private key: %w", err)
	}
	k, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the app private key is not RSA")
	}
	return k, nil
}

// rewrap restores the line structure of a PEM whose newlines were stripped.
//
// Returns the input unchanged when it cannot find a BEGIN/END pair, so a blob
// that is genuinely not PEM still fails with the message that says so.
func rewrap(in []byte) []byte {
	s := strings.TrimSpace(string(in))
	begin := strings.Index(s, "-----BEGIN ")
	if begin < 0 {
		return in
	}
	hdrEnd := strings.Index(s[begin+11:], "-----")
	if hdrEnd < 0 {
		return in
	}
	kind := s[begin+11 : begin+11+hdrEnd]
	header := "-----BEGIN " + kind + "-----"
	footer := "-----END " + kind + "-----"

	bodyStart := begin + len(header)
	footIdx := strings.Index(s, footer)
	if footIdx < 0 || footIdx <= bodyStart {
		return in
	}
	// Everything between the markers, with all whitespace removed. What
	// remains is the base64 payload, whatever the vault did to the layout.
	body := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, s[bodyStart:footIdx])

	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	for len(body) > 64 {
		b.WriteString(body[:64])
		b.WriteByte('\n')
		body = body[64:]
	}
	if body != "" {
		b.WriteString(body)
		b.WriteByte('\n')
	}
	b.WriteString(footer)
	b.WriteByte('\n')
	return []byte(b.String())
}
