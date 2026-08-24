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
	GitProvider string // github | gitea
	// GitAPIBase means different things per host, because the hosts do:
	// on GitHub it is the API root (.../api/v3 for Enterprise), on Gitea it
	// is the INSTANCE root and the client appends /api/v1 itself, because it
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
	LLMProvider        string // openai | anthropic
	LLMBaseURL         string
	LLMModel           string
	LLMKey             string
	LLMReasoningEffort string
	LLMTimeout         time.Duration

	// Identity. The agent signs its comments and commits with this, and it is
	// deliberately NOT the same thing as the account the token belongs to --
	// a reviewer should be able to tell a bot's comment from a colleague's at
	// a glance, and the token's owner is whoever minted it.
	Brand     string
	BrandMark string

	// Behaviour.
	CheckName string
	// GateReportAuthor is the only account whose gate report the agent will
	// read. The gate publishes its verdict as a pull-request comment, and a
	// comment is something anybody with write access can write -- including a
	// comment carrying the gate's own marker and a report that says everything
	// is fine. See Triage.GateReportAuthor.
	//
	// "*" trusts any author, which is the behaviour that existed before this
	// value and is still the only thing a host with no stable bot identity can
	// express.
	GateReportAuthor string
	MaxAttempts      int
	GateWait         time.Duration
	GatePoll         time.Duration
	Explain          bool
	Migrate          bool
	// App authentication. When AppID is set the agent acts as a GitHub App
	// installation instead of as the owner of a token -- see gitprovider.AppAuth
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
	// OFF by default, and that is a deliberate asymmetry with everything else
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
		BrandMark:                os.Getenv("AGENT_BRAND_MARK"),
		GitProvider:              env("GIT_PROVIDER", "github"),
		GitInsecureSkipTLSVerify: os.Getenv("GIT_INSECURE_SKIP_TLS_VERIFY") == "true",
		GitAPIBase:               os.Getenv("GIT_API_BASE"),
		GitOwner:                 os.Getenv("GIT_OWNER"),
		GitRepo:                  os.Getenv("GIT_REPO"),
		GitRepoURL:               os.Getenv("GIT_REPO_URL"),
		GitToken:                 os.Getenv("GIT_TOKEN"),
		// No defaults. Empty means "derive": as a GitHub App, the bot's own
		// identity; otherwise the provider's collision-proof fallback. The old
		// default email lived in the `<username>@users.noreply.github.com`
		// namespace, which BELONGS to GitHub accounts -- so every commit was
		// attributed, avatar and all, to the unrelated account named `bosun`.
		AuthorName:  os.Getenv("GIT_AUTHOR_NAME"),
		AuthorEmail: os.Getenv("GIT_AUTHOR_EMAIL"),

		LLMProvider:        os.Getenv("LLM_PROVIDER"),
		LLMBaseURL:         os.Getenv("LLM_BASE_URL"),
		LLMModel:           os.Getenv("LLM_MODEL"),
		LLMKey:             os.Getenv("LLM_API_KEY"),
		LLMReasoningEffort: os.Getenv("LLM_REASONING_EFFORT"),

		CheckName: env("GATE_CHECK_NAME", "addons-gate"),
		CloneRoot: env("CLONE_ROOT", ""),
	}

	// Defaulted per host rather than globally, because the answer is a fact
	// about the host and not a preference. A gate running in GitHub Actions
	// comments through `github.token` and therefore as `github-actions[bot]`,
	// every time, on every repository -- so GitHub gets a default that is
	// simply correct. Gitea Actions has no equivalent fixed identity: the
	// report arrives as whichever user minted the CI token, which this cannot
	// know. Defaulting that to a GitHub name would break every Gitea install
	// on upgrade in the name of a check it could not perform.
	c.GateReportAuthor = os.Getenv("GATE_REPORT_AUTHOR")
	if c.GateReportAuthor == "" && c.GitProvider == "github" {
		c.GateReportAuthor = "github-actions[bot]"
	}

	var err error
	if c.MaxAttempts, err = envInt("MAX_ATTEMPTS", 2); err != nil {
		return nil, err
	}
	if c.GateWait, err = envDur("GATE_WAIT", 10*time.Minute); err != nil {
		return nil, err
	}
	// Default ON. The agent's whole complaint about itself was that it only
	// spoke when something was wrong, and a green gate on a chart bump still
	// changed something worth reading.
	c.AppID = os.Getenv("GITHUB_APP_ID")
	c.AppPrivateKey = os.Getenv("GITHUB_APP_PRIVATE_KEY")
	c.AppInstallID = os.Getenv("GITHUB_APP_INSTALLATION_ID")
	c.Explain = os.Getenv("EXPLAIN_GREEN") != "false"
	// Default ON. The repair is deterministic, answers to the same deny-list
	// and allowlist as every other write, and the re-run gate re-counts what
	// it did -- the reasons to switch it off are operational, not safety.
	c.Migrate = os.Getenv("MIGRATE_DROPPED_VERSIONS") != "false"
	// Default ON, and soft: everything it needs can fail without consequence
	// beyond a less-informed explanation that says it is less informed.
	c.Upstream = os.Getenv("UPSTREAM_NOTES") != "false"
	if c.UpstreamMaxReleases, err = envInt("UPSTREAM_MAX_RELEASES", 5); err != nil {
		return nil, err
	}
	if c.UpstreamMaxBodyChars, err = envInt("UPSTREAM_MAX_BODY_CHARS", 4000); err != nil {
		return nil, err
	}
	if c.UpstreamMaxCommits, err = envInt("UPSTREAM_MAX_COMMITS", upstream.MaxCompareCommits); err != nil {
		return nil, err
	}
	if c.GatePoll, err = envDur("GATE_POLL", 30*time.Second); err != nil {
		return nil, err
	}
	if c.LLMTimeout, err = envDur("LLM_TIMEOUT", 10*time.Minute); err != nil {
		return nil, err
	}
	// Default ON. It runs only where the deterministic repair already had
	// authority, over files policy already permitted, and the checks in front
	// of it are stricter than anywhere else in this service -- so the reasons
	// to switch it off are operational rather than about safety.
	c.Structural = os.Getenv("STRUCTURAL_MIGRATION") != "false"
	if c.MaxRestructured, err = envInt("MIGRATE_MAX_DOCS", 5); err != nil {
		return nil, err
	}
	c.LiveReads = os.Getenv("LIVE_READS") == "true"
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
		"LLM_PROVIDER": c.LLMProvider, "LLM_MODEL": c.LLMModel,
	}
	// A credential is required; WHICH one depends on how the agent
	// authenticates. App auth sets no GIT_TOKEN at all -- installation tokens
	// are minted per use -- so requiring the token unconditionally made a
	// correctly-configured App a pod that would not start:
	//
	//     configuration: missing required configuration: GIT_TOKEN
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
	case "openai":
		if c.LLMBaseURL == "" {
			return fmt.Errorf("LLM_BASE_URL is required for the openai provider (it is what makes a self-hosted model work)")
		}
	case "anthropic":
	default:
		return fmt.Errorf("unknown LLM_PROVIDER %q (openai or anthropic)", c.LLMProvider)
	}

	switch c.GitProvider {
	case "github", "gitea":
	default:
		return fmt.Errorf("GIT_PROVIDER %q is not implemented yet -- see docs/git-providers.md", c.GitProvider)
	}

	// An empty allowlist means the agent can write nothing. That is the safe
	// default, but running with it is almost certainly a misconfiguration, so
	// say so at startup rather than silently refusing every fix later.
	if len(c.AllowPaths) == 0 {
		return fmt.Errorf("ALLOW_PATHS is empty: the agent could never apply any fix")
	}
	return nil
}

// NormalizeLegacyAuthor clears the author identity this project shipped as
// its chart default for its whole early life -- `bosun
// <bosun@users.noreply.github.com>` -- which by now sits copied into
// consumers' values files as though somebody chose it. Nobody did, and it is
// the noreply address of an unrelated GitHub account: honoring it kept
// attributing pushed commits to a stranger THROUGH the release that fixed the
// default, because an explicit value beats a default and the value was the
// old default, fossilised. Cleared, the App derives its own bot identity and
// token mode falls back to an address that maps to nobody.
//
// Returns whether it cleared anything, so the caller can say so in the log --
// silently rewriting configuration is its own bug.
func (c *Config) NormalizeLegacyAuthor() bool {
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
