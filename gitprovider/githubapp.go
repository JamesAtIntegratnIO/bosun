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
	defer resp.Body.Close()
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

// parseKey accepts both PEM encodings GitHub has issued over the years.
func parseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("the app private key is not PEM " +
			"(a common cause is a secret holding the base64 of the PEM rather than the PEM)")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing the app private key: %w", err)
	}
	k, ok := any.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the app private key is not RSA")
	}
	return k, nil
}
