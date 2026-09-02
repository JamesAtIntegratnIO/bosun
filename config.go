package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

// Config is the agent's whole configuration, read from the environment so a
// chart can supply it and a Secret can supply the values that matter.
//
// Every credential also reads from a file, named by the same variable with a
// _FILE suffix, and that is the form to prefer: see envSecret for what an
// environment variable is visible to and who inherits it.
//
// There is no default model provider on purpose. A component that installs
// cleanly and then quietly starts spending money against a vendor the operator
// did not choose is a bad default, however convenient.
type Config struct {
	Addr string

	// Web serves the status page on its own listener at WebAddr.
	//
	// A second port rather than a path on the first, because the first port
	// also answers POST /v1/promotion-opened, and "expose the read-only page"
	// must never be one routing mistake away from "expose the endpoint that
	// spends money and writes to the repository". A NetworkPolicy and a
	// gateway both draw their lines at the port, so the separation has to
	// exist there to exist at all.
	Web     bool
	WebAddr string

	// WebTheme is which of the site's two treatments the page renders in.
	//
	// An operator's choice and not a reader's, because the page cannot offer
	// the reader one. It carries no script -- that is what lets a gateway put
	// a strict content policy in front of it -- and it refreshes itself every
	// minute, so the CSS-only toggle that would work without script would be
	// wiped on every refresh. A cookie would work, and costs a route, a
	// redirect and a Set-Cookie on a page that currently sets nothing.
	//
	// So the choice moves to where this install's other decisions already
	// live. `auto` follows the reader's system preference, which is the right
	// default and is what the page did before this existed; `dark` and
	// `light` stamp the same `data-theme` attribute the site uses, and win
	// over the system preference in both directions.
	WebTheme WebThemeName

	// Version is what the operator deployed, shown on the status page. The
	// chart passes its appVersion; empty falls back to the binary's own build
	// stamp, which an image built without its .git directory does not have.
	Version string

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

	// GateConcurrency caps parallel renders, and is the host's answer rather
	// than the gated repository's. Zero leaves whatever the repository's own
	// config file says.
	//
	// Same reasoning as the egress deny-list, and ADR 0012 makes it explicit:
	// the renders run in this pod, against this pod's limits, and how hard to
	// work is a decision about this cluster rather than about the repository
	// being reviewed.
	GateConcurrency int
	// GateValidate* are the operator's schema-validation policy. Each is
	// tri-state: unset leaves the repository's config file alone, which is
	// what keeps an install that configured validation in its own file
	// working unchanged.
	GateValidateEnabled              *bool
	GateValidateIgnoreMissingSchemas *bool
	GateValidateSchemaLocations      []string
	GateValidateSkipKinds            []string

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

	// EgressDeny are hosts the agent must not contact. Empty permits every
	// public host, which is the default: the agent reads public metadata about
	// public artifacts, and every outbound request is logged.
	EgressDeny []string

	// EgressAllowPrivate are internal networks the agent may reach after all,
	// as a CIDR or a single address.
	//
	// Empty is the safe default and the common case, because nothing this
	// reads lives on the cluster's own network. It is not the only case: an
	// internal chart museum or a proxy on an RFC1918 address is a real
	// deployment, and without this there is no way to say so, since
	// egress.DefaultDenyNetworks is closed whatever EgressDeny says. The
	// symptom of a missing entry is a refusal that names the network.
	EgressAllowPrivate []string

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

	// MCP serves the read-only MCP surface on its own listener at MCPAddr.
	//
	// A third port, for the reason the status page has a second one: a
	// NetworkPolicy and a gateway both draw their lines at the port, so a
	// read-only surface stays smaller than the endpoint that spends money and
	// writes to the repository only if the two never share a listener.
	//
	// Off by default, and unlike the status page that is not a preference. An
	// upgrade must not open a new authenticated programmatic API on an install
	// whose operator has not decided to have one.
	MCP     bool
	MCPAddr string

	// MCPToken is the bearer token the MCP listener requires.
	//
	// Empty means the listener does not start, which is deliberately unlike
	// PromotionToken. That endpoint's caller is Kargo inside the cluster and
	// its unauthenticated form predates the setting, so requiring a token
	// there would break every install that upgraded into it. This surface is
	// new, and it is built to be reached from OUTSIDE the cluster, where "a
	// token nobody set" and "an open API" are the same thing.
	MCPToken string

	// MCPAllowUnauthenticated runs the listener with no authentication at all.
	//
	// The escape hatch, and it is spelled to be regretted on sight:
	// MCP_DANGEROUSLY_SERVE_WITHOUT_AUTHENTICATION. It exists because somebody
	// genuinely does want it -- a gateway in front that already authenticates,
	// a laptop-bound experiment -- and because the alternative to an explicit
	// hatch is people discovering that an empty token works.
	//
	// It is never what an omission reaches. An unset token does not fall back
	// to this; it stops the listener.
	MCPAllowUnauthenticated bool

	// PromotionToken is the bearer token POST /v1/promotion-opened requires.
	//
	// Empty means unauthenticated, which is what every deployment before this
	// setting existed was, so it cannot become an error without breaking
	// them. It is announced at start-up instead: the endpoint's payload names
	// the pull request the agent will edit and the files it will read into a
	// published prompt, and a NetworkPolicy admits a whole namespace.
	PromotionToken string
	// MaxConcurrentTriage bounds simultaneous triages.
	MaxConcurrentTriage int

	// secretEnv is the name of every environment variable LoadConfig read a
	// credential from, recorded as it read them. See SecretEnv.
	//
	// Unexported and set only by LoadConfig, because a Config assembled by
	// hand -- which most tests do -- read nothing from the environment and has
	// nothing to declare. The zero value is an empty list, and an empty list
	// strips nothing, which is the same answer os.Environ() gave before any of
	// this existed.
	secretEnv []string
}

func LoadConfig() (*Config, error) {
	// Collected so one bad boolean names itself rather than reaching a struct
	// literal that cannot return an error.
	var boolErr error
	b := func(k string, def bool) bool {
		v, err := envBool(k, def)
		if err != nil && boolErr == nil {
			boolErr = err
		}
		return v
	}
	// Same trick for the credentials, which can fail on a file that is not
	// there.
	//
	// It is also where the names are recorded, and that is the point of the
	// closure being the only way this function reads a credential: the list of
	// variables to keep out of every subprocess's environment is the list of
	// calls made here, so a credential added below is stripped by the same
	// line that reads it rather than by somebody remembering. See SecretEnv.
	var secretErr error
	var secretEnv []string
	secret := func(k string) string {
		// Both spellings, because envSecret reads both: K_FILE names a path,
		// and a child that can read the path can read the credential.
		secretEnv = append(secretEnv, k, k+"_FILE")
		v, err := envSecret(k)
		if err != nil && secretErr == nil {
			secretErr = err
		}
		return v
	}
	c := &Config{
		Addr:                     env("AGENT_ADDR", ":8080"),
		WebAddr:                  env("WEB_ADDR", ":8081"),
		WebTheme:                 WebThemeName(env("WEB_THEME", "auto")),
		Version:                  os.Getenv("AGENT_VERSION"),
		Brand:                    env("AGENT_BRAND", "Bosun"),
		GitProvider:              GitProviderName(env("GIT_PROVIDER", "github")),
		GitInsecureSkipTLSVerify: b("GIT_INSECURE_SKIP_TLS_VERIFY", false),
		GitAPIBase:               os.Getenv("GIT_API_BASE"),
		GitOwner:                 os.Getenv("GIT_OWNER"),
		GitRepo:                  os.Getenv("GIT_REPO"),
		GitRepoURL:               os.Getenv("GIT_REPO_URL"),
		GitToken:                 secret("GIT_TOKEN"),
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
		LLMKey:             secret("LLM_API_KEY"),
		LLMReasoningEffort: os.Getenv("LLM_REASONING_EFFORT"),

		CheckName: env("GATE_CHECK_NAME", "addons-gate"),
		CloneRoot: env("CLONE_ROOT", ""),
	}
	c.GateForkPRs = b("GATE_FORK_PRS", false)
	c.ArgoCDBaseURL = os.Getenv("ARGOCD_BASE_URL")
	c.ArgoCDToken = secret("ARGOCD_TOKEN")
	c.ArgoCDCAFile = os.Getenv("ARGOCD_CA_FILE")
	c.ArgoCDInsecureSkipTLSVerify = b("ARGOCD_INSECURE_SKIP_TLS_VERIFY", false)

	var err error
	if c.MaxAttempts, err = envInt("MAX_ATTEMPTS", 2); err != nil {
		return nil, err
	}
	c.AppID = os.Getenv("GITHUB_APP_ID")
	c.AppPrivateKey = secret("GITHUB_APP_PRIVATE_KEY")
	c.AppInstallID = os.Getenv("GITHUB_APP_INSTALLATION_ID")
	// Default on. The agent's whole complaint about itself was that it only
	// spoke when something was wrong, and a green gate on a chart bump still
	// changed something worth reading.
	c.Explain = b("EXPLAIN_GREEN", true)
	// Default on. The repair is deterministic, answers to the same deny-list
	// and allowlist as every other write, and the re-run gate re-counts what
	// it did, the reasons to switch it off are operational, not safety.
	c.Migrate = b("MIGRATE_DROPPED_VERSIONS", true)
	// Default on, and soft: everything it needs can fail without consequence
	// beyond a less-informed explanation that says it is less informed.
	c.Upstream = b("UPSTREAM_NOTES", true)
	if c.UpstreamMaxReleases, err = envInt("UPSTREAM_MAX_RELEASES", 5); err != nil {
		return nil, err
	}
	if c.UpstreamMaxBodyChars, err = envInt("UPSTREAM_MAX_BODY_CHARS", 4000); err != nil {
		return nil, err
	}
	if c.UpstreamMaxCommits, err = envInt("UPSTREAM_MAX_COMMITS", upstream.MaxCompareCommits); err != nil {
		return nil, err
	}
	// Default on: the page is read-only, renders only what the process holds,
	// and reaches nobody until something in the cluster routes to its port.
	c.Web = b("WEB", true)
	c.Supervise = b("SUPERVISE_PIPELINE", true)
	if c.SuperviseEvery, err = envDur("SUPERVISE_INTERVAL", 10*time.Minute); err != nil {
		return nil, err
	}
	if c.GatePoll, err = envDur("GATE_POLL", 30*time.Second); err != nil {
		return nil, err
	}
	if c.GateConcurrency, err = envInt("GATE_CONCURRENCY", 0); err != nil {
		return nil, err
	}
	if c.GateValidateEnabled, err = envBoolOpt("GATE_VALIDATE_ENABLED"); err != nil {
		return nil, err
	}
	if c.GateValidateIgnoreMissingSchemas, err = envBoolOpt("GATE_VALIDATE_IGNORE_MISSING_SCHEMAS"); err != nil {
		return nil, err
	}
	c.GateValidateSchemaLocations = envList("GATE_VALIDATE_SCHEMA_LOCATIONS")
	c.GateValidateSkipKinds = envList("GATE_VALIDATE_SKIP_KINDS")
	if c.LLMTimeout, err = envDur("LLM_TIMEOUT", 10*time.Minute); err != nil {
		return nil, err
	}
	// Default on. It runs only where the deterministic repair already had
	// authority, over files policy already permitted, and the checks in front
	// of it are stricter than anywhere else in this service, so the reasons
	// to switch it off are operational rather than about safety.
	c.Structural = b("STRUCTURAL_MIGRATION", true)
	if c.MaxRestructured, err = envInt("MIGRATE_MAX_DOCS", 5); err != nil {
		return nil, err
	}
	c.LiveReads = b("LIVE_READS", false)
	c.LiveReadsArgoCDNamespace = env("LIVE_READS_ARGOCD_NS", "argocd")
	c.EgressDeny = envList("EGRESS_DENY")
	c.EgressAllowPrivate = envList("EGRESS_ALLOW_PRIVATE")
	c.AllowPaths = envList("ALLOW_PATHS")
	c.DenyPaths = envList("DENY_PATHS")

	// The shared secret the promotion endpoint requires, when there is one.
	c.PromotionToken = strings.TrimSpace(secret("PROMOTION_TOKEN"))

	// The MCP surface. Off unless an operator said otherwise, which is the one
	// default on this listener that is not negotiable: everything else here is
	// a preference, and this is the difference between an upgrade and a new
	// programmatic API somebody did not ask for.
	c.MCP = b("MCP", false)
	c.MCPAddr = env("MCP_ADDR", ":8082")
	// TrimSpace for the reason every credential gets it: a mounted Secret
	// carries its file's trailing newline, and a token that differs from the
	// operator's by one invisible byte fails as "unauthorized", which is the
	// symptom of a dozen unrelated mistakes.
	c.MCPToken = strings.TrimSpace(secret("MCP_TOKEN"))
	c.MCPAllowUnauthenticated = b("MCP_DANGEROUSLY_SERVE_WITHOUT_AUTHENTICATION", false)
	if c.MaxConcurrentTriage, err = envInt("MAX_CONCURRENT_TRIAGE", 4); err != nil {
		return nil, err
	}
	if c.MaxConcurrentTriage <= 0 {
		return nil, fmt.Errorf("MAX_CONCURRENT_TRIAGE must be positive, got %d", c.MaxConcurrentTriage)
	}

	if secretErr != nil {
		return nil, secretErr
	}
	if boolErr != nil {
		return nil, boolErr
	}
	// After every read, so that it is the whole list. A credential read below
	// this line would not be in it, which is what the derived test in
	// subprocess_env_test.go is for: it walks this file for every envSecret
	// call and fails if what LoadConfig reports back is missing one.
	//
	// GIT_REPO_URL is the entry no walk of envSecret can find, and it is here
	// for the same reason Secrets returns what is inside it. It is read with a
	// bare os.Getenv because it is not a credential -- it is the repository,
	// and it is in the chart and half the log lines -- but an operator may
	// have written one into it, and a child handed the URL is handed whatever
	// is in the URL. Nothing this process starts needs it: every git command
	// here is given the remote explicitly, with the credential split out into
	// a scoped header.
	c.secretEnv = append(secretEnv, "GIT_REPO_URL")
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

	// Refused rather than defaulted. A typo here renders a page in the theme
	// the operator did not choose and says nothing about it, which is the
	// quiet kind of wrong this whole component exists to notice elsewhere.
	//
	// The empty string is the one exception, and it is not a fourth value: it
	// is the zero value of a Config nobody set this on. `env` turns an unset
	// or empty WEB_THEME into "auto" before this runs, so "" cannot reach
	// here from the environment -- only from a Config built in code, where
	// rejecting a field the caller never mentioned would be the wrong answer.
	switch c.WebTheme {
	case "", WebThemeAuto, WebThemeDark, WebThemeLight:
	default:
		return fmt.Errorf("unknown WEB_THEME %q (auto, dark or light)", c.WebTheme)
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

	// Gitea has no public API to fall back to: without a base URL every read
	// goes nowhere, and PushFix refuses outright. GitHub defaults to the
	// public API on purpose, so this is a Gitea rule rather than a general one.
	if c.GitProvider == GitGitea && strings.TrimSpace(c.GitAPIBase) == "" {
		return fmt.Errorf("GIT_API_BASE is required for the gitea provider, e.g. https://gitea.example.com")
	}

	// An empty allowlist means the agent can write nothing. That is the safe
	// default, but running with it is almost certainly a misconfiguration, so
	// say so at startup rather than silently refusing every fix later.
	if len(c.AllowPaths) == 0 {
		return fmt.Errorf("ALLOW_PATHS is empty: the agent could never apply any fix")
	}
	return nil
}

// Secrets is every credential this configuration loaded, for priming the
// process redactor at start-up.
//
// One list, here, beside the fields it names, rather than assembled at the
// composition root: a credential added to Config is added ten lines from this
// function, and the reviewer who adds it is the one who can see what is
// missing. main only forwards what this returns.
//
// It is the whole set and not the subset that could plausibly be echoed back.
// Which credential a misconfigured host quotes into its response is not
// bosun's to predict, and the cost of an extra entry is one more string
// comparison per redacted message.
//
// Unset entries are the normal case -- App auth leaves GitToken empty, token
// auth leaves AppPrivateKey empty, and PromotionToken is optional -- and the
// redactor drops them, because an empty secret used as a rule matches
// everywhere. See redaction_test.go, which derives this list from config.go's
// own syntax tree rather than trusting it.
//
// The last entry is the one that walk cannot find. GitRepoURL is read with a
// bare os.Getenv because it is not a credential -- it is the repository, it is
// in the chart and in half the log lines -- but an operator may have written
// one into it, and every clone, fetch and merge-base in this process is
// pointed at that URL. What is in it is a credential this process hands to a
// remote, whatever variable it arrived in.
//
// Asked of gitprovider.Remote rather than parsed here, because that is the
// type that already takes this URL apart to keep the credential out of argv.
// Two implementations of "what in this string is the secret" is one of them
// being wrong, and the one that is wrong is silent.
func (c *Config) Secrets() []string {
	secrets := []string{
		c.GitToken,
		c.AppPrivateKey,
		c.LLMKey,
		c.ArgoCDToken,
		c.PromotionToken,
		c.MCPToken,
	}
	return append(secrets, gitprovider.NewRemote(c.GitRepoURL).Secrets()...)
}

// SecretEnv is every environment variable this configuration read a credential
// from, for keeping them out of the environment of every subprocess this
// process starts.
//
// The names, where Secrets is the values, and the two answer different
// questions. Secrets is what must not appear in text bosun publishes;
// SecretEnv is what must not be in the environment of `helm template`, of
// `kubeconform`, or of a chart's own helm plugin -- which is this process's
// child, with this process's environment, and which redaction cannot reach at
// all. A child that writes its environment to a file has published a
// credential without printing a byte.
//
// Both spellings of each, and GIT_REPO_URL, which no walk of envSecret can
// find. LoadConfig assembles it as it reads, so this is a list of the calls
// that were made rather than a list somebody maintains; a Config built by hand
// read nothing and returns nothing, which strips nothing.
func (c *Config) SecretEnv() []string { return c.secretEnv }

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

// envSecret reads a credential from K, or from the file named by K_FILE, and
// is the only way this file reads one.
//
// Both forms are read here, at start-up, before anything is served. A
// credential goes from this function to the one client that needs it and
// nowhere else; none of them is ever part of a prompt.
//
// The file form exists because an environment variable is not a private place.
// `kubectl exec -- env` prints it, /proc/<pid>/environ holds it, and a crash
// dump carries it. Every child process used to inherit the whole environment
// too -- this service shells out to git and to helm, so a GitHub App private
// key delivered as GITHUB_APP_PRIVATE_KEY was in the environment of binaries
// with no business seeing it -- and that half is closed: childenv strips both
// spellings of every variable read here out of every subprocess this process
// starts, `_FILE` included, because a path is not a secret only to something
// that cannot read the file at the end of it. The readers this cannot reach
// are the ones that were always the file form's real argument: this process's
// own /proc entry, and anybody who can exec into the pod.
//
// _FILE is a convention rather than a platform feature, which is worth knowing
// when the chart moves ahead of the image: the kubelet mounts the file and
// sets the variable to a path, and the ReadFile below is the only thing that
// opens it. A binary without this function sees a path and no credential.
//
// Setting both forms is an error rather than a documented precedence. Two
// credentials where one is wanted is a question with no right answer, and
// picking the wrong one fails as a rejected token, which is the symptom of a
// dozen unrelated mistakes and points at none of them.
//
// The trailing newline goes. A Secret mounted as a file ends in one, `echo -n
// > key` does not, and a token carrying a stray \n is refused by every host in
// exactly the way a wrong token is.
func envSecret(k string) (string, error) {
	path := strings.TrimSpace(os.Getenv(k + "_FILE"))
	if path == "" {
		return os.Getenv(k), nil
	}
	if os.Getenv(k) != "" {
		return "", fmt.Errorf("%s and %s_FILE are both set: unset one, they cannot both be the credential", k, k)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		// Here, loudly, rather than as an empty string. An unreadable mount
		// that falls back to empty arrives at validate() as "missing required
		// configuration", which sends an operator to check a Secret key that
		// is fine and never at the volume that is not.
		return "", fmt.Errorf("%s_FILE: %w", k, err)
	}
	v := strings.TrimRight(string(blob), "\r\n")
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s_FILE %s is empty: a credential that reads as \"not configured\" is worse than one that is missing", k, path)
	}
	return v, nil
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
func envBool(k string, def bool) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv(k))); v {
	case "":
		return def, nil
	case "1", "t", "true", "yes", "on":
		return true, nil
	case "0", "f", "false", "no", "off":
		return false, nil
	default:
		// A typo is a configuration error, not a false. `EXPLAIN_GREEN=treu`
		// used to read as off, so a setting somebody deliberately turned on
		// was silently off and the only symptom was an agent that said
		// nothing, which is what this service exists to notice about other
		// people's systems.
		return false, fmt.Errorf("%s: %q is not a boolean (true/false, yes/no, on/off, 1/0)", k, v)
	}
}

// envBoolOpt reads a bool that may not be set at all.
//
// Distinct from envBool, which takes a default, because here "false" and "not
// set" are different answers: a chart value defaulting to false would turn
// validation off for every install that had switched it on in its own config
// file, and the only symptom would be a report that stopped mentioning
// schemas.
func envBoolOpt(k string) (*bool, error) {
	if strings.TrimSpace(os.Getenv(k)) == "" {
		return nil, nil
	}
	v, err := envBool(k, false)
	if err != nil {
		return nil, err
	}
	return &v, nil
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

// envDur reads an interval or a timeout, and every duration this file reads is
// one of those, so zero and negative are rejected here rather than at each
// call site.
//
// Zero is not a faster poll; it is no wait at all. `GATE_POLL=0` turned the
// gate's sweep and the agent's wait loop into spins that call the git host as
// fast as it answers, and the symptom, a rate-limited token, points nowhere
// near the setting that caused it.
func envDur(k string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %s", k, d)
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

// WebThemeName is the same shape for the same reason: validated in one switch
// and rendered in another file. A value the validator accepts and the template
// does not is a page that silently ignores what the values file asked for.
type WebThemeName string

const (
	// WebThemeAuto emits no attribute at all, which is what leaves the page's
	// media query in charge. It is not a third palette.
	WebThemeAuto  WebThemeName = "auto"
	WebThemeDark  WebThemeName = "dark"
	WebThemeLight WebThemeName = "light"
)
