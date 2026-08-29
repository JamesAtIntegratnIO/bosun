package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// Config is the agent's whole configuration, read from the environment so a
// chart can supply it and a Secret can supply the two values that matter.
//
// There is no default model provider on purpose. A component that installs
// cleanly and then quietly starts spending money against a vendor the operator
// did not choose is a bad default, however convenient.
type Config struct {
	Addr string

	// Git host.
	GitProvider GitProviderName
	// GitAPIBase means different things per host, because the hosts do:
	// on GitHub it is the API root (.../api/v3 for Enterprise), on Gitea it
	// is the instance root and the client appends /api/v1 itself, because it
	// also needs that root to build a push remote.
	GitAPIBase  string
	GitOwner    string
	GitRepo     string
	GitRepoURL  string
	GitToken    string
	AuthorName  string
	AuthorEmail string
	// GitInsecureSkipTLSVerify allows a self-signed certificate on a
	// self-hosted host. Scoped to the git client; never process-wide.
	GitInsecureSkipTLSVerify bool

	// Model.
	LLMProvider        LLMProviderName
	LLMBaseURL         string
	LLMModel           string
	LLMKey             string
	LLMReasoningEffort string
	LLMTimeout         time.Duration

	// Identity. The agent signs its comments and commits with this, and it is
	// deliberately not the same thing as the account the token belongs to; a
	// reviewer should be able to tell a bot's comment from a colleague's at a
	// glance, and the token's owner is whoever minted it.
	Brand string

	// Behaviour.
	CheckName string
	// GateForkPRs lets the gate render fork pull requests. Off by
	// default: rendering runs helm over the pull request's content, inside
	// the cluster, and whose content that is should be an operator's call.
	GateForkPRs bool
	// ArgoCDBaseURL is the ArgoCD API server the inventory is read from, e.g.
	// https://argocd-server.argocd.svc. Required: there is no other source.
	//
	// It is the ArgoCD API rather than the cluster Secrets those clusters are
	// stored in because the Secret read cannot be made small enough. The gate
	// wants four fields, name, server, labels, annotations, and RBAC has no
	// predicate for "the labels but not the data": no deny rules,
	// `resourceNames` does not apply to `list`, and the request's label
	// selector is a filter the apiserver applies after authorising. GET
	// /api/v1/clusters serves the same four fields with the credential block
	// redacted, so the authorisation happens somewhere that can draw the line.
	ArgoCDBaseURL string
	// ArgoCDToken authenticates to it: an ArgoCD account token, which needs
	// `clusters, get` in ArgoCD's own RBAC and nothing else.
	ArgoCDToken string
	// ArgoCDCAFile verifies argocd-server. Empty uses the system roots.
	ArgoCDCAFile string
	// ArgoCDInsecureSkipTLSVerify accepts any certificate from it, the
	// answer for the default self-signed argocd-server certificate when
	// nobody can produce its CA.
	ArgoCDInsecureSkipTLSVerify bool
	MaxAttempts                 int
	GatePoll                    time.Duration

	// Supervise turns on the pipeline sweep: a periodic read of Kargo that
	// reports what has silently stopped. Independent of the gate, and
	// deliberately cheap; it only ever LISTs.
	Supervise      bool
	SuperviseEvery time.Duration
	Explain        bool
	Migrate        bool
	// App authentication. When AppID is set the agent acts as a GitHub App
	// installation instead of as the owner of a token, see gitprovider.AppAuth
	// for why that is about identity rather than access.
	AppID         string
	AppPrivateKey string
	AppInstallID  string
	// Upstream turns on reading the maintainers' release notes.
	Upstream             bool
	UpstreamMaxReleases  int
	UpstreamMaxBodyChars int
	// UpstreamMaxCommits caps how many upstream commits reach a prompt or a
	// comment. The commits are read only when a human is about to be handed
	// the pull request, so the cost is per-escalation rather than per-bump.
	UpstreamMaxCommits int
	AllowPaths         []string
	DenyPaths          []string
	CloneRoot          string

	// Structural turns on the schema-guided half of the deterministic repair.
	Structural bool
	// MaxRestructured caps document migrations per pull request.
	MaxRestructured int

	// EgressDeny are hosts the agent must not contact. Empty permits
	// everything, which is the default: the agent reads public metadata about
	// public artifacts, and every outbound request is logged.
	EgressDeny []string

	// LiveReads turns on reading the cluster the agent runs in: how many
	// objects are stored on a version a chart is about to stop serving, and
	// whether the Applications a promotion says it will verify were already
	// unhealthy before it.
	//
	// Off by default, and that is a deliberate asymmetry with everything else
	// here. The rest of what the agent reads is public or already in the pull
	// request; this reads the cluster, and a component that starts doing that
	// because somebody upgraded a chart has made a decision that belongs to an
	// operator.
	LiveReads bool
	// LiveReadsArgoCDNamespace is where Applications live.
	LiveReadsArgoCDNamespace string
}

func LoadConfig() (*Config, error) {
	c := &Config{
		Addr:                     env("AGENT_ADDR", ":8080"),
		Brand:                    env("AGENT_BRAND", "Bosun"),
		GitProvider:              GitProviderName(env("GIT_PROVIDER", "github")),
		GitInsecureSkipTLSVerify: envBool("GIT_INSECURE_SKIP_TLS_VERIFY", false),
		GitAPIBase:               os.Getenv("GIT_API_BASE"),
		GitOwner:                 os.Getenv("GIT_OWNER"),
		GitRepo:                  os.Getenv("GIT_REPO"),
		GitRepoURL:               os.Getenv("GIT_REPO_URL"),
		GitToken:                 os.Getenv("GIT_TOKEN"),
		// No defaults. Empty means "derive": as a GitHub App, the bot's own
		// identity; otherwise the provider's collision-proof fallback. The old
		// default email lived in the `<username>@users.noreply.github.com`
		// namespace, which belongs to GitHub accounts, so every commit was
		// attributed, avatar and all, to the unrelated account named `bosun`.
		AuthorName:  os.Getenv("GIT_AUTHOR_NAME"),
		AuthorEmail: os.Getenv("GIT_AUTHOR_EMAIL"),

		LLMProvider:        LLMProviderName(os.Getenv("LLM_PROVIDER")),
		LLMBaseURL:         os.Getenv("LLM_BASE_URL"),
		LLMModel:           os.Getenv("LLM_MODEL"),
		LLMKey:             os.Getenv("LLM_API_KEY"),
		LLMReasoningEffort: os.Getenv("LLM_REASONING_EFFORT"),

		CheckName: env("GATE_CHECK_NAME", "addons-gate"),
		CloneRoot: env("CLONE_ROOT", ""),
	}
	c.GateForkPRs = envBool("GATE_FORK_PRS", false)
	c.ArgoCDBaseURL = os.Getenv("ARGOCD_BASE_URL")
	c.ArgoCDToken = os.Getenv("ARGOCD_TOKEN")
	c.ArgoCDCAFile = os.Getenv("ARGOCD_CA_FILE")
	c.ArgoCDInsecureSkipTLSVerify = envBool("ARGOCD_INSECURE_SKIP_TLS_VERIFY", false)

	var err error
	if c.MaxAttempts, err = envInt("MAX_ATTEMPTS", 2); err != nil {
		return nil, err
	}
	c.AppID = os.Getenv("GITHUB_APP_ID")
	c.AppPrivateKey = os.Getenv("GITHUB_APP_PRIVATE_KEY")
	c.AppInstallID = os.Getenv("GITHUB_APP_INSTALLATION_ID")
	// Default on. The agent's whole complaint about itself was that it only
	// spoke when something was wrong, and a green gate on a chart bump still
	// changed something worth reading.
	c.Explain = envBool("EXPLAIN_GREEN", true)
	// Default on. The repair is deterministic, answers to the same deny-list
	// and allowlist as every other write, and the re-run gate re-counts what
	// it did, the reasons to switch it off are operational, not safety.
	c.Migrate = envBool("MIGRATE_DROPPED_VERSIONS", true)
	// Default on, and soft: everything it needs can fail without consequence
	// beyond a less-informed explanation that says it is less informed.
	c.Upstream = envBool("UPSTREAM_NOTES", true)
	if c.UpstreamMaxReleases, err = envInt("UPSTREAM_MAX_RELEASES", 5); err != nil {
		return nil, err
	}
	if c.UpstreamMaxBodyChars, err = envInt("UPSTREAM_MAX_BODY_CHARS", 4000); err != nil {
		return nil, err
	}
	if c.UpstreamMaxCommits, err = envInt("UPSTREAM_MAX_COMMITS", upstream.MaxCompareCommits); err != nil {
		return nil, err
	}
	c.Supervise = envBool("SUPERVISE_PIPELINE", true)
	if c.SuperviseEvery, err = envDur("SUPERVISE_INTERVAL", 10*time.Minute); err != nil {
		return nil, err
	}
	if c.GatePoll, err = envDur("GATE_POLL", 30*time.Second); err != nil {
		return nil, err
	}
	if c.LLMTimeout, err = envDur("LLM_TIMEOUT", 10*time.Minute); err != nil {
		return nil, err
	}
	// Default on. It runs only where the deterministic repair already had
	// authority, over files policy already permitted, and the checks in front
	// of it are stricter than anywhere else in this service, so the reasons
	// to switch it off are operational rather than about safety.
	c.Structural = envBool("STRUCTURAL_MIGRATION", true)
	if c.MaxRestructured, err = envInt("MIGRATE_MAX_DOCS", 5); err != nil {
		return nil, err
	}
	c.LiveReads = envBool("LIVE_READS", false)
	c.LiveReadsArgoCDNamespace = env("LIVE_READS_ARGOCD_NS", "argocd")
	c.EgressDeny = envList("EGRESS_DENY")
	c.AllowPaths = envList("ALLOW_PATHS")
	c.DenyPaths = envList("DENY_PATHS")

	return c, c.validate()
}

func (c *Config) validate() error {
	var missing []string
	need := map[string]string{
		"GIT_OWNER": c.GitOwner, "GIT_REPO": c.GitRepo,
		"GIT_REPO_URL": c.GitRepoURL,
		"LLM_PROVIDER": string(c.LLMProvider), "LLM_MODEL": c.LLMModel,
	}
	// A credential is required; which one depends on how the agent
	// authenticates. App auth sets no GIT_TOKEN at all, installation tokens
	// are minted per use, so requiring the token unconditionally made a
	// correctly-configured App a pod that would not start:
	//
	//  configuration: missing required configuration: GIT_TOKEN
	//
	// The chart had already stopped setting it. This is the other half.
	switch {
	case c.AppID != "":
		if strings.TrimSpace(c.AppPrivateKey) == "" {
			missing = append(missing, "GITHUB_APP_PRIVATE_KEY (required with GITHUB_APP_ID)")
		}
	default:
		if strings.TrimSpace(c.GitToken) == "" {
			missing = append(missing, "GIT_TOKEN (or GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY)")
		}
	}
	for k, v := range need {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}

	switch c.LLMProvider {
	case LLMOpenAI:
		if c.LLMBaseURL == "" {
			return fmt.Errorf("LLM_BASE_URL is required for the openai provider (it is what makes a self-hosted model work)")
		}
	case LLMAnthropic:
	default:
		return fmt.Errorf("unknown LLM_PROVIDER %q (openai or anthropic)", c.LLMProvider)
	}

	switch c.GitProvider {
	case GitGitHub, GitGitea:
	default:
		return fmt.Errorf("GIT_PROVIDER %q is not implemented yet -- see docs/git-providers.md", c.GitProvider)
	}

	// Checked here rather than left to the reader's start-up probe, because a
	// missing URL or token is a values mistake with a one-line fix and this is
	// where the other one-line fixes are named. The probe still runs: it
	// catches the failures this cannot see, like a token ArgoCD rejects.
	if c.ArgoCDBaseURL == "" {
		return fmt.Errorf("ARGOCD_BASE_URL is empty: the gate reads the cluster inventory " +
			"from the ArgoCD API and needs its address, e.g. https://argocd-server.argocd.svc")
	}
	if c.ArgoCDToken == "" {
		return fmt.Errorf("ARGOCD_TOKEN is empty: mint one with " +
			"`argocd account generate-token --account <account>`")
	}

	// An empty allowlist means the agent can write nothing. That is the safe
	// default, but running with it is almost certainly a misconfiguration, so
	// say so at startup rather than silently refusing every fix later.
	if len(c.AllowPaths) == 0 {
		return fmt.Errorf("ALLOW_PATHS is empty: the agent could never apply any fix")
	}
	return nil
}

// NormaliseLegacyAuthor clears the author identity this project shipped as
// its chart default for its whole early life, `bosun
// <bosun@users.noreply.github.com>`, which by now sits copied into consumers'
// values files as though somebody chose it. Nobody did, and it is the noreply
// address of an unrelated GitHub account: honoring it kept attributing pushed
// commits to a stranger through the release that fixed the default, because
// an explicit value beats a default and the value was the old default,
// fossilised. Cleared, the App derives its own bot identity and token mode
// falls back to an address that maps to nobody.
//
// Returns whether it cleared anything, so the caller can say so in the log;
// silently rewriting configuration is its own bug.
func (c *Config) NormaliseLegacyAuthor() bool {
	if c.AuthorEmail != "bosun@users.noreply.github.com" {
		return false
	}
	c.AuthorName, c.AuthorEmail = "", ""
	return true
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envBool reads a boolean with a default, and is the only way this file reads
// one.
//
// `os.Getenv(k) == "true"` cannot express a default: that idiom is always
// off-unless-set, so a setting that should be on unless somebody turns it off
// has no way to say so, which is why seven reads here had drifted into two
// idioms, `== "true"` and `!= "false"`. They disagreed about everything except
// the exact strings "true" and "false": `LIVE_READS=1` was off, and
// `EXPLAIN_GREEN=no` was on.
func envBool(k string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(k))) {
	case "":
		return def
	case "1", "t", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envInt(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return n, nil
}

func envDur(k string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return d, nil
}

func envList(k string) []string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// GitProviderName and LLMProviderName are the two settings whose value selects
// a code path.
//
// Named types with const blocks rather than bare strings with a trailing
// comment. Each is validated in one switch and dispatched in another, in a
// different file, and a bare string lets those two drift silently, a value
// the validator accepts and the dispatcher does not is a pod that starts
// healthy and then does nothing.
type GitProviderName string

const (
	GitGitHub GitProviderName = "github"
	GitGitea  GitProviderName = "gitea"
)

type LLMProviderName string

const (
	LLMOpenAI    LLMProviderName = "openai"
	LLMAnthropic LLMProviderName = "anthropic"
)
