package gitprovider

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/JamesAtIntegratnIO/bosun/redact"
)

// Remote is a repository URL split into the part git may be given as an
// argument and the part it must be given as environment.
//
// # Why a type rather than two strings
//
// A credential an operator wrote into GIT_REPO_URL used to travel to git the
// way it was configured: on the command line, as part of the clone's remote.
// Anything that can run `ps` on that node, or read /proc/<pid>/cmdline -- which
// is world-readable, where /proc/<pid>/environ is readable only by the owner --
// saw the token for the life of the clone. That is the exposure this type
// exists to close, and pushRemote's comment named it years before this: a
// userinfo already in GIT_REPO_URL "would otherwise be the credential in argv
// that this stopped putting there".
//
// So the two halves travel together, and a caller cannot take the URL without
// having been handed the environment that goes with it. Two strings would let
// a call site use one and forget the other, which is a clone that silently
// stops authenticating -- or worse, one that authenticates by putting the
// credential back where it came from.
//
// # The shape is ArgoCD's
//
// ArgoCD solved this for the same reason and its answer is worth copying: the
// repository URL it stores as `origin` is the raw URL with no credentials in
// it, and the credential is attached per-command through the environment, by
// the commands that actually talk to a remote (`runCredentialedCmd` wraps
// fetch, push and submodule, and not the local ones). Nothing is written into
// the checkout's .git/config, so nothing has to be cleaned up afterwards and a
// checkout that leaks is not a checkout that carries a token.
//
// What is not copied is the transport. ArgoCD supplies the credential through
// GIT_ASKPASS and a helper script that echoes GIT_USERNAME and GIT_PASSWORD,
// which needs a helper on disk in the image. This process already has a way to
// hand git a credential through the environment -- the http.<remote>.extraHeader
// that pushAuthEnv has used since the push stopped putting tokens in argv --
// so this uses that one. One mechanism, already reviewed, and no new file to
// ship.
type Remote struct {
	// url is what git is given: the configured URL with any userinfo removed.
	url string
	// auth is the environment that authenticates it, empty when the URL
	// carried no credential.
	auth []string
	// secrets is every spelling of the credential, for priming the redactor.
	secrets []string
}

// NewRemote reads a configured repository URL.
//
// http(s) only, and that guard is load-bearing rather than tidy. An ssh
// remote's username is a login name -- it is `git` on every forge in existence
// -- and its credential is a key that never appears in the URL at all, so
// there is nothing here to move and stripping the username would break the
// clone. Such a URL is returned exactly as it was configured.
func NewRemote(raw string) Remote {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.User == nil || (u.Scheme != "https" && u.Scheme != "http") {
		return Remote{url: raw}
	}

	user := u.User.Username()
	password, _ := u.User.Password()
	if strings.TrimSpace(user+password) == "" {
		return Remote{url: raw}
	}

	// The URL git is given, and the URL the header is scoped to: the same
	// string, because git matches http.<url>.extraHeader by prefix against the
	// URL it is about to contact.
	u.User = nil
	clean := u.String()

	return Remote{
		url:  clean,
		auth: authHeaderEnv(clean, user, password),
		// Both spellings. git talks to the host about the decoded credential,
		// so that is what a host can echo back; every message that quotes the
		// URL as it was configured carries the encoded one. See redact.
		secrets: credentialSpellings(raw, user, password),
	}
}

// URL is the address git is given, with no credential in it.
func (r Remote) URL() string { return r.url }

// Env is the environment that authenticates the URL, to be appended to
// os.Environ() by whoever runs a git command that contacts the remote.
//
// Empty for a remote that carries no credential, and for ssh, so a caller can
// append it unconditionally.
func (r Remote) Env() []string { return r.auth }

// Secrets is every spelling of the credential this URL carries, for priming
// the process redactor.
//
// Here rather than in config.go because this is where the URL is already being
// taken apart, and two implementations of "what in this string is the secret"
// is one of them being wrong.
func (r Remote) Secrets() []string { return r.secrets }

// basicAuth is the credential as an HTTP Basic value.
//
// A lone username is a token and git sends it with an empty password, which is
// `token:` and not `token`. Getting that wrong authenticates as nobody and the
// host says only that it refused.
func basicAuth(userPassword string) string {
	return base64.StdEncoding.EncodeToString([]byte(userPassword))
}

// credentialSpellings is the credential as git will send it and as the
// configuration spelled it.
//
// The two differ whenever an operator percent-encoded anything, and each
// reaches a different reader: the decoded form is what a host can quote back,
// the encoded form is what any message quoting GIT_REPO_URL carries.
//
// Which half of the userinfo is the secret is decided by the caller and worth
// restating here, because both branches were bugs once. With a password the
// username is a placeholder the host ignores -- `oauth2`, `x-access-token`,
// somebody's name -- and priming it would redact an ordinary word. With no
// password the username is the whole credential. An *empty* password counts as
// no password rather than as a blank secret: `https://TOKEN:@host/o/r.git` is
// what `git remote add` writes back, and reading it the other way primes the
// empty string, which the redactor drops -- so it protects nothing while
// looking like it worked.
func credentialSpellings(raw, user, password string) []string {
	decoded := user
	if password != "" {
		decoded = password
	}
	if strings.TrimSpace(decoded) == "" {
		return nil
	}
	return []string{decoded, rawURLCredential(raw)}
}

// rawURLCredential is the credential spelled the way it was configured, read
// out of the URL text rather than out of url.Parse's decoded fields.
//
// The authority is everything between the scheme and the first `/`, `?` or
// `#`, and the userinfo is what precedes the last `@` in it -- last, because a
// password may contain one unencoded and a host name may not.
func rawURLCredential(raw string) string {
	_, rest, ok := strings.Cut(raw, "://")
	if !ok {
		return ""
	}
	authority := rest
	if i := strings.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return ""
	}
	userinfo := authority[:at]
	if user, password, ok := strings.Cut(userinfo, ":"); ok && password != "" {
		return password
	} else if ok {
		return user
	}
	return userinfo
}

// Clone makes a shallow checkout of one branch of r into dir.
//
// One implementation, because there were three: the agent's triage, the gate
// service's checkout and the supervisor's sweep each built their own, and no
// two agreed -- the agent's was not quiet, the supervisor's passed `--branch`
// only when it had one. What all three shared is that they passed the
// configured URL straight to git. A credential in that URL was therefore in
// argv three times over, and a fourth clone added anywhere would have been in
// argv too, because nothing said not to.
//
// An empty branch clones the remote's default, which is what the supervisor's
// sweep wants and what `--branch ""` would refuse.
func Clone(ctx context.Context, r Remote, branch, dir string) error {
	args := []string{"clone", "--quiet", "--depth", "1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, r.URL(), dir)

	cmd := exec.CommandContext(ctx, "git", withoutBackgroundMaintenance(args...)...)
	// The credential travels here and not in args. os.Environ() first so the
	// auth entries win over anything an operator set in the pod.
	cmd.Env = append(os.Environ(), r.Env()...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone: %w: %s", err,
			snippet([]byte(redact.Text(stderr.String()))))
	}
	return nil
}
