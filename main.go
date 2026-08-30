// bosun triages automated dependency-bump pull requests.
//
// Kargo calls it when a pull request opens. It reads the pre-merge gate,
// explains a red one, and fixes the cases the rendered diff proves, a chart
// default that flipped, a pin that must move with another, a port a policy
// still names. Everything else it hands to a human.
//
// The model never applies, and it authors plenty: a scalar value, one whole
// document reshaped for a schema that moved its fields, or one whole values
// document for a chart version that refuses the values in the tree. This
// process is what writes any of it, behind an allowlist, a from-value check and
// a corroboration check for a scalar; behind identity, schema-validity and
// value-provenance checks for a document; and behind those plus a render of the
// chart itself for values. So "never edit the gate", "never invent a version"
// and "never invent data" are properties of the code, not requests in a prompt.
//
// Not "the model never writes": the scalar it names is written to the line
// verbatim. It applies nothing and chooses no path, which is the narrower claim
// and the true one. See docs/safety-model.md, docs/prompt-contract.md, adr/0007
// and adr/0013.
//
// # What lives here
//
// The composition root, and nothing else. This file reads the environment,
// builds one of each collaborator, wires them together and serves; config.go
// is the reading. Every decision lives in a package that can be imported and
// tested without this one:
//
//	agent judges a pull request and writes the comment
//	gateservice runs the gate in-process, on a timer, per open pull request
//	supervisor sweeps the pipeline for the promotions that never happened
//	gate renders the repository and diffs it
//	prompt what the model is told, and what the eval suite measures
//
// The HTTP surface is here for the same reason the wiring is: Kargo POSTs a
// promotion, and turning that into a call is plumbing, not judgement.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/agent"
	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/egress"
	"github.com/JamesAtIntegratnIO/bosun/gateservice"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/pipeline"
	"github.com/JamesAtIntegratnIO/bosun/redact"
	"github.com/JamesAtIntegratnIO/bosun/supervisor"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
	"github.com/JamesAtIntegratnIO/bosun/web"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)

	cfg, err := LoadConfig()
	if err != nil {
		logger.Fatalf("configuration: %v", err)
	}
	// Before anything else is built, because everything built after this can
	// fail with a message that quotes what it was given. A credential does not
	// reach a log or a pull-request comment by being printed on purpose; it
	// reaches one by being echoed back inside somebody else's error string.
	redact.Prime(cfg.Secrets()...)
	if cfg.NormaliseLegacyAuthor() {
		logger.Print("ignoring the legacy author bosun <bosun@users.noreply.github.com>: " +
			"that is the noreply address of an unrelated GitHub account; deriving the commit identity instead")
	}

	var model llm.Provider
	switch cfg.LLMProvider {
	case LLMOpenAI:
		model = &llm.OpenAI{
			BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMKey, Model: cfg.LLMModel,
			ReasoningEffort: cfg.LLMReasoningEffort, Timeout: cfg.LLMTimeout,
		}
	case LLMAnthropic:
		model = &llm.Anthropic{
			BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMKey, Model: cfg.LLMModel,
			Timeout: cfg.LLMTimeout,
		}
	}

	// LoadConfig has already rejected any provider not handled here, so a
	// nil git would be a programming error rather than a configuration one.
	var git gitprovider.Provider
	// The credential the upstream reader uses. The same credential as the git
	// client's, and not the same object: under App auth it exists only as a
	// function, because installation tokens are minted per use and the config's
	// static token is empty.
	var upstreamToken func(context.Context) (string, error)
	switch cfg.GitProvider {
	case GitGitea:
		git = &gitprovider.Gitea{
			BaseURL: cfg.GitAPIBase, Owner: cfg.GitOwner, Repo: cfg.GitRepo,
			Token: cfg.GitToken, Username: cfg.GitOwner,
			AuthorName: cfg.AuthorName, AuthorEmail: cfg.AuthorEmail,
			InsecureSkipTLSVerify: cfg.GitInsecureSkipTLSVerify,
		}
	default:
		gh := &gitprovider.GitHub{
			APIBase: cfg.GitAPIBase, RepoURL: cfg.GitRepoURL,
			Owner: cfg.GitOwner, Repo: cfg.GitRepo,
			Token: cfg.GitToken, AuthorName: cfg.AuthorName, AuthorEmail: cfg.AuthorEmail,
		}
		// Acting as an App is about identity, not access. A token grants the
		// same rights but belongs to whoever minted it, so every comment
		// carries that person's name and avatar and reads like a colleague's
		// until you reach the footer. An App has a face of its own.
		if cfg.AppID != "" {
			app := &gitprovider.AppAuth{
				AppID:          cfg.AppID,
				PrivateKey:     []byte(cfg.AppPrivateKey),
				InstallationID: cfg.AppInstallID,
				Owner:          cfg.GitOwner,
				Repo:           cfg.GitRepo,
				APIBase:        cfg.GitAPIBase,
			}
			// Fail at start-up, not on the first pull request. A bad key or an
			// app installed on the wrong repository should be a pod that will
			// not start, which somebody notices, rather than a triage that
			// quietly does nothing, which is the failure mode this whole
			// service keeps finding in itself.
			if _, err := app.Token(context.Background()); err != nil {
				log.Fatalf("github app authentication failed: %v", err)
			}
			gh.TokenSource = app.Token
			// Without this the upstream reader ran anonymously against
			// api.github.com, 60 requests an hour per IP, from the moment the
			// agent became an App, because it was handed cfg.GitToken and App
			// mode leaves that empty. The failure surfaced as "no upstream
			// release notes", which is also what an artifact that publishes
			// none looks like.
			upstreamToken = app.Token
			// Commits carry the App's own bot identity unless the operator
			// chose one. Same fail-at-start-up rule as the token: falling back
			// silently is how the first live repair got attributed to the
			// unrelated GitHub account named `bosun`.
			if cfg.AuthorName == "" || cfg.AuthorEmail == "" {
				name, email, err := app.BotIdentity(context.Background())
				if err != nil {
					log.Fatalf("resolving the app's commit identity: %v", err)
				}
				gh.AuthorName, gh.AuthorEmail = name, email
			}
			log.Printf("authenticating as GitHub App %s, committing as %s <%s>",
				cfg.AppID, gh.AuthorName, gh.AuthorEmail)
		}
		git = gh
	}

	// Where it may go, and the record of where it went. Open to the public
	// internet with a deny-list, closed to internal address space whatever the
	// deny-list says: see the egress package for why that replaced an
	// allow-list, and for why the internal half is not configurable away.
	egressPolicy := egress.Policy{
		Deny:         cfg.EgressDeny,
		AllowPrivate: cfg.EgressAllowPrivate,
		Log:          func(f string, a ...any) { logger.Printf(f, a...) },
	}

	t := &agent.Triage{
		Git: git, LLM: model,
		Brand:           cfg.Brand,
		Policy:          edits.Policy{Allow: cfg.AllowPaths, Deny: cfg.DenyPaths},
		CheckName:       cfg.CheckName,
		MaxAttempts:     cfg.MaxAttempts,
		GatePoll:        cfg.GatePoll,
		Explain:         cfg.Explain,
		Migrate:         cfg.Migrate,
		Structural:      cfg.Structural,
		MaxRestructured: cfg.MaxRestructured,
		Upstream:        upstreamResolver(cfg, upstreamToken, egressPolicy),
		Egress:          egressPolicy,
		CloneRoot:       cfg.CloneRoot,
		RepoURL:         cfg.GitRepoURL,
		Log:             func(f string, a ...any) { logger.Printf(f, a...) },
	}

	// The apiserver reader, for liveReads (facts for briefs) and for the
	// pipeline sweep. The gate does not use it: the inventory it renders
	// against comes from the ArgoCD API.
	var reader *cluster.APIServer
	if cfg.LiveReads || cfg.Supervise {
		reader = &cluster.APIServer{ArgoCDNamespace: cfg.LiveReadsArgoCDNamespace}
		// Fail at start-up, the same rule as the App's key, and here it
		// matters more, not less. Every failure inside this reader is
		// deliberately soft: an unreachable apiserver reports "not permitted
		// to check", a sentence designed to be harmless and therefore a
		// sentence nobody would ever chase. Proving the path works once,
		// loudly, is what stops a misconfiguration becoming a permanent quiet
		// shrug.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := reader.Check(ctx)
		cancel()
		if err != nil {
			logger.Fatalf("the apiserver could not be read: %v\n"+
				"  the NetworkPolicy needs an explicit egress rule for the apiserver: a ClusterIP is "+
				"DNAT'd before policy is evaluated, so kubernetes.default.svc is not reachable by "+
				"default and the symptom is a hang with zero bytes", err)
		}
	}

	if cfg.LiveReads {
		ns, _ := reader.Namespace()
		logger.Printf("reading the cluster read-only from %s (Applications in %s)",
			ns, cfg.LiveReadsArgoCDNamespace)
		t.Cluster = reader
	} else {
		logger.Print("live cluster reads are off; briefs say what the repository holds " +
			"and nothing about what is running")
	}

	// The context the background work answers to. Cancelled at shutdown, after
	// the HTTP server has stopped taking new promotions.
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()

	// Where the inventory comes from: ArgoCD's own API, which serves the four
	// fields a generator can see with the credential block redacted.
	argo := &cluster.ArgoCD{
		BaseURL:               cfg.ArgoCDBaseURL,
		Token:                 cfg.ArgoCDToken,
		CAFile:                cfg.ArgoCDCAFile,
		InsecureSkipTLSVerify: cfg.ArgoCDInsecureSkipTLSVerify,
	}

	// Same fail-at-start-up rule as everything above: an inventory the gate
	// cannot read would otherwise surface as an `error` status on every pull
	// request, a broken required check, discovered by whoever tries to merge
	// next.
	//
	// A timeout here is nearly always the NetworkPolicy, and the value it is
	// nearly always wrong on is the port; a ClusterIP is DNAT'd before
	// policy is evaluated, so the rule has to name argocd-server's pod port
	// and not the one in the URL. Saying so here is the difference between
	// this message and a message that only repeats what the operator already
	// knows.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 30*time.Second)
	inv, err := argo.ClusterInventory(probeCtx)
	cancelProbe()
	if err != nil {
		logger.Fatalf("the cluster inventory could not be read: %v\n"+
			"  the gate reads it from the ArgoCD API, which needs a reachable argocd-server, a "+
			"certificate this can verify (gate.argocd.caSecret or gate.argocd.insecureSkipTLSVerify), "+
			"and an account token with `clusters, get`. If it timed out rather than being refused, "+
			"check the NetworkPolicy ports at BOTH ends: gate.argocd.podPort is argocd-server's POD "+
			"port (8080), not the port in gate.argocd.baseURL, and argocd-server's own ingress policy "+
			"must admit this namespace on that same port", err)
	}
	gs := &gateservice.Service{
		Git:       git,
		Inventory: argo.ClusterInventory,
		// What this repository deploys, read live rather than restated in a
		// file it maintains by hand (ADR 0012). The same account token as the
		// inventory, plus `applications, get` and `applicationsets, get`.
		Derive:      argo.Derive,
		CheckName:   cfg.CheckName,
		RepoURL:     cfg.GitRepoURL,
		CloneRoot:   cfg.CloneRoot,
		ForkPRs:     cfg.GateForkPRs,
		Poll:        cfg.GatePoll,
		Concurrency: cfg.GateConcurrency,
		Validate: gateservice.ValidatePolicy{
			Enabled:              cfg.GateValidateEnabled,
			IgnoreMissingSchemas: cfg.GateValidateIgnoreMissingSchemas,
			SchemaLocations:      cfg.GateValidateSchemaLocations,
			SkipKinds:            cfg.GateValidateSkipKinds,
		},
		Log:    func(f string, a ...any) { logger.Printf(f, a...) },
		Egress: egressPolicy,
	}
	t.Gate = gs
	go gs.Run(runCtx)
	logger.Printf("gate: polling for open pull requests every %s (%d cluster(s) in the live inventory, read from the ArgoCD API at %s)",
		cfg.GatePoll, len(inv.Clusters), cfg.ArgoCDBaseURL)

	// Two sentences because they are two different guarantees, and a reader
	// who takes the first for the whole answer is the reason DOC-01 existed:
	// the public half is a deny-list an operator maintains, the internal half
	// is closed by the package and only widened by name.
	if len(cfg.EgressDeny) == 0 {
		logger.Print("egress: every public host is permitted, and every outbound request is logged. " +
			"Set triage.egressDeny to forbid one.")
	} else {
		logger.Printf("egress: every public host except %v is permitted, and every outbound request is logged", cfg.EgressDeny)
	}
	if len(cfg.EgressAllowPrivate) == 0 {
		logger.Print("egress: internal networks are refused at the dial. " +
			"Set triage.egressAllowPrivate to name one an internal registry or proxy sits on.")
	} else {
		logger.Printf("egress: internal networks are refused at the dial, except %v", cfg.EgressAllowPrivate)
	}

	srv := &Server{
		Triage: t, Log: logger, Timeout: cfg.LLMTimeout + 5*time.Minute,
		Token: cfg.PromotionToken, MaxConcurrent: cfg.MaxConcurrentTriage,
	}
	if srv.Token == "" {
		// Loud, and every start-up. The endpoint takes a pull request number
		// and a list of files the agent will edit and read into a published
		// prompt; unauthenticated, the boundary is whatever the namespace's
		// NetworkPolicy admits, which is every workload in it.
		logger.Printf("WARNING: POST /v1/promotion-opened is unauthenticated -- " +
			"any workload the NetworkPolicy admits can trigger a triage and name the files it edits. " +
			"Set PROMOTION_TOKEN (and the matching Authorization header on Kargo's http step) to require a bearer token.")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/promotion-opened", srv.PromotionOpened)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// The supervisor. A second job, on a timer, answering the question nobody
	// asks: not "is this pull request safe" but "are the pull requests that
	// should exist being opened at all". Nothing about a promotion that never
	// happened produces an event, so a timer is the only way to see it.
	//
	// The reader is guaranteed here: it is built whenever supervision is on,
	// so there is no nil case left to log about.
	var sup *supervisor.Supervisor
	if cfg.Supervise {
		sup = &supervisor.Supervisor{
			Collector: &pipeline.Collector{Kargo: reader, PRs: git},
			Every:     cfg.SuperviseEvery,
			Log:       func(f string, a ...any) { logger.Printf(f, a...) },
			// The default branch, because a pin that writes nowhere is a
			// property of what is merged.
			Checkout: supervisor.ShallowCheckout(cfg.GitRepoURL, "", cfg.CloneRoot),
		}
	}

	// The status page: what this agent is, what it watches, and the last
	// sweep's report with its remedies. It renders only state the process
	// already holds, so serving it costs no API call anywhere.
	ws := &web.Server{
		Brand: cfg.Brand,
		// `auto` becomes the empty string, which is what stamps no attribute
		// and leaves the page's media query in charge. The two names for
		// "follow the system" meet here and nowhere else.
		Theme: func() string {
			if cfg.WebTheme == WebThemeAuto {
				return ""
			}
			return string(cfg.WebTheme)
		}(),
		Version:    cfg.Version,
		Repo:       cfg.GitOwner + "/" + cfg.GitRepo,
		RepoLink:   repoLink(cfg.GitRepoURL),
		CheckName:  cfg.CheckName,
		Model:      model.Name(),
		GatePoll:   cfg.GatePoll,
		Clusters:   len(inv.Clusters),
		Features:   features(cfg),
		EgressLine: egressLine(cfg),
		Gate:       func() web.GateStatus { return gateStatus(gs) },
		Triage:     srv.Status,
	}
	if cfg.Supervise {
		ws.SweepEvery = cfg.SuperviseEvery
		ws.Report = sup.Report
	}
	// The page is on this port too, at the root. Not the port to publish, that
	// is the web listener below, but this is the port everybody already
	// forwards to reach /pipeline and /metrics, and a read-only page is
	// strictly less than what this port already answers.
	mux.HandleFunc("GET /{$}", ws.Page())
	// The mark, beside the page on both listeners. The page names it relatively,
	// so it resolves on whichever port the reader arrived on and there is no
	// base URL to configure.
	mux.HandleFunc("GET /mark.svg", ws.Mark())
	// The same handler the web listener gets: markdown for a script, which is
	// everything /pipeline ever served, and the page for a browser arriving
	// through a port-forward.
	//
	// Registered whether or not supervision is on, because the page links here
	// and the page is served either way. Inside the block below, those links
	// 404'd on this port for an install with `supervise.enabled: false`, while
	// the same links on the web port answered the 503 that says why. The
	// handler already distinguishes "supervision is off" from "no sweep yet".
	mux.HandleFunc("GET /pipeline", ws.PipelineHandler())

	if cfg.Supervise {
		mux.HandleFunc("GET /metrics", sup.Handler("metrics"))
		go sup.Run(runCtx)
		logger.Printf("pipeline: supervising Kargo every %s; report on /pipeline (?format=text for a terminal), metrics on /metrics",
			cfg.SuperviseEvery)
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// The status page's own listener. Its own port rather than a path on the
	// main one, because the main port also answers POST /v1/promotion-opened:
	// a NetworkPolicy and a gateway both draw their lines at the port, so
	// "expose the read-only page" can only stay smaller than "expose the
	// endpoint that spends money and writes to the repository" if the two
	// never share one. Nothing else is registered here, and nothing here
	// mutates anything.
	var webSrv *http.Server
	if cfg.Web {
		webMux := http.NewServeMux()
		webMux.HandleFunc("GET /{$}", ws.Page())
		webMux.HandleFunc("GET /mark.svg", ws.Mark())
		webMux.HandleFunc("GET /pipeline", ws.PipelineHandler())
		webMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		webSrv = &http.Server{
			Addr:              cfg.WebAddr,
			Handler:           webMux,
			ReadHeaderTimeout: 10 * time.Second,
		}
		go func() {
			logger.Printf("web: status page on %s", cfg.WebAddr)
			if err := webSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Fatalf("serving the status page: %v", err)
			}
		}()
	}

	go func() {
		logger.Printf("%s listening on %s (model %s, repo %s/%s, allow %v)",
			cfg.Brand, cfg.Addr, model.Name(), cfg.GitOwner, cfg.GitRepo, cfg.AllowPaths)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("serving: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	logger.Print("shutting down; waiting for in-flight triage")
	stopRun()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	if webSrv != nil {
		_ = webSrv.Shutdown(ctx)
	}
	srv.Wait()
	logger.Print("stopped")
}

// features is the agent's switchable posture, named for the page in the same
// order values.yaml discusses them.
func features(cfg *Config) []web.Feature {
	return []web.Feature{
		{Name: "Explain green gates", On: cfg.Explain},
		{Name: "Migrate dropped versions", On: cfg.Migrate},
		{Name: "Structural migration", On: cfg.Structural},
		{Name: "Upstream release notes", On: cfg.Upstream},
		{Name: "Live cluster reads", On: cfg.LiveReads},
		{Name: "Gate fork pull requests", On: cfg.GateForkPRs},
	}
}

// egressLine is the one sentence the page says about where the agent may go.
// It compresses the two start-up log lines, and like them it keeps the two
// guarantees separate: the public half is a deny-list, the internal half is
// closed and only widened by name.
func egressLine(cfg *Config) string {
	s := "Every outbound request is logged; internal networks are refused at the dial"
	if len(cfg.EgressAllowPrivate) > 0 {
		s += " except " + strings.Join(cfg.EgressAllowPrivate, ", ")
	}
	if n := len(cfg.EgressDeny); n == 1 {
		s += "; 1 public host is denied by name"
	} else if n > 1 {
		s += fmt.Sprintf("; %d public hosts are denied by name", n)
	}
	return s + "."
}

// repoLink turns the clone URL into a browsable one where the two coincide,
// which they do on every https host this supports. An ssh remote yields no
// link, and the page then names the repository without one, rather than
// guessing at a web root that may not exist.
func repoLink(cloneURL string) string {
	u := strings.TrimSuffix(strings.TrimSpace(cloneURL), ".git")
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

// gateStatus adapts the gate's account of itself to the page's vocabulary.
// The copy is the point: web deliberately depends on nothing that can dial.
func gateStatus(gs *gateservice.Service) web.GateStatus {
	g := gs.Status()
	out := web.GateStatus{
		SweptAt: g.SweptAt, Err: g.Err,
		Held: g.Held, Running: g.Running,
	}
	for _, pr := range g.Open {
		out.Open = append(out.Open, web.GatePR(pr))
	}
	return out
}

// Server accepts Kargo's call and gets out of the way.
type Server struct {
	Triage  *agent.Triage
	Log     *log.Logger
	Timeout time.Duration

	// Token, when set, is the bearer token this endpoint requires.
	//
	// The handler used to trust every caller a NetworkPolicy admitted, which
	// is namespace-level and therefore every workload in it. The payload it
	// trusts names a pull request number and the files the agent may edit and
	// will read into a published prompt, so "anything in the namespace" was
	// the real authorization boundary on the agent's write access.
	//
	// Optional because it must be: an operator upgrading into this would
	// otherwise get a service that silently stops answering Kargo. Unset is
	// announced loudly at start-up instead.
	Token string

	// MaxConcurrent bounds triage goroutines. Zero means the default.
	MaxConcurrent int

	wg sync.WaitGroup
	// inFlight is the promotion currently being triaged for a pull request,
	// keyed by PR number. Kargo retries a step whose response it did not
	// like, and a retry must not start a second triage of the same PR.
	//
	// pending is the newest promotion that arrived while one was running.
	// Collapsing on PR number alone acknowledged a new promotion with 202 and
	// dropped it: the second Freight into a stage that already had an open
	// pull request got a verdict about the first one, and nothing ever
	// revisited it. Newest wins, and exactly one re-run follows.
	mu       sync.Mutex
	inFlight map[int]agent.Promotion
	pending  map[int]agent.Promotion
	// done and failed count triages since the process started, for the status
	// page; failed is the subset that errored. They reset with the pod, and
	// the page says so.
	done   int
	failed int

	sem     chan struct{}
	semOnce sync.Once

	// runFn is the work the handler dispatches. Defaults to agent.Triage.Run; tests
	// substitute it so the handler's concurrency behaviour can be exercised
	// without a git host or a model behind it.
	runFn func(agent.Promotion) error
}

// maxConcurrentTriage is the default ceiling on simultaneous triages. Each one
// is a clone, a helm render and a model call, so this is about the pod's
// memory and the host's rate limit rather than about throughput.
const maxConcurrentTriage = 4

func (s *Server) acquire() chan struct{} {
	s.semOnce.Do(func() {
		n := s.MaxConcurrent
		if n <= 0 {
			n = maxConcurrentTriage
		}
		s.sem = make(chan struct{}, n)
	})
	return s.sem
}

// authorized checks the bearer token when one is configured.
//
// Constant-time, because the comparison is against a shared secret and the
// caller can retry: a byte-at-a-time short circuit is a slow oracle, and this
// is one line either way.
func (s *Server) authorized(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) == 1
}

func (s *Server) run(ctx context.Context, p agent.Promotion) error {
	if s.runFn != nil {
		return s.runFn(p)
	}
	return s.Triage.Run(ctx, p)
}

// PromotionOpened answers 202 immediately and does the work on a goroutine.
//
// This is not an optimisation. Kargo's `http` promotion step is synchronous,
// so a handler that blocked would put a model round trip, minutes, on a
// local model, inside the critical path of every promotion.
func (s *Server) PromotionOpened(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var p agent.Promotion
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	if p.PRNumber <= 0 {
		http.Error(w, "prNumber is required", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	if s.inFlight == nil {
		s.inFlight = map[int]agent.Promotion{}
	}
	if s.pending == nil {
		s.pending = map[int]agent.Promotion{}
	}
	if running, busy := s.inFlight[p.PRNumber]; busy {
		// A retry of the promotion already running is the case this dedup was
		// built for, and it is identified by the promotion, not by the pull
		// request. A different promotion for the same pull request is new
		// work: held as pending and run once the current one finishes, rather
		// than acknowledged and forgotten.
		status := "already in progress"
		if !samePromotion(running, p) {
			s.pending[p.PRNumber] = p
			status = "queued behind the promotion in progress"
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprintf(w, "{%q:%q}", "status", status)
		return
	}
	s.inFlight[p.PRNumber] = p
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if rec := recover(); rec != nil {
				s.Log.Printf("PR %d: triage panicked: %v", p.PRNumber, rec)
			}
		}()

		// Bounded. Each triage is a clone, a helm render and a model call, and
		// the handler starts one per distinct pull request number with nothing
		// in between.
		sem := s.acquire()

		for cur := p; ; {
			func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				// Detached from the request context on purpose: the response
				// has already gone back to Kargo, so cancelling with it would
				// abort every triage immediately.
				ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
				defer cancel()

				start := time.Now()
				err := s.run(ctx, cur)
				s.mu.Lock()
				s.done++
				if err != nil {
					s.failed++
				}
				s.mu.Unlock()
				if err != nil {
					s.Log.Printf("PR %d: triage failed after %s: %v",
						cur.PRNumber, time.Since(start).Round(time.Second), err)
					return
				}
				s.Log.Printf("PR %d: triage done in %s", cur.PRNumber, time.Since(start).Round(time.Second))
			}()

			s.mu.Lock()
			next, queued := s.pending[cur.PRNumber]
			if !queued {
				delete(s.inFlight, cur.PRNumber)
				s.mu.Unlock()
				return
			}
			delete(s.pending, cur.PRNumber)
			s.inFlight[cur.PRNumber] = next
			s.mu.Unlock()
			s.Log.Printf("PR %d: running the promotion that arrived while the last one was in flight", cur.PRNumber)
			cur = next
		}
	}()
}

// samePromotion decides whether an incoming call is a retry of the one already
// running rather than new work.
//
// PromotionID is the identity when there is one, Kargo mints one per
// promotion and repeats it on retry. Without it there is nothing to tell a
// retry from a new event, and treating an unidentified call as a retry is the
// safe reading: the alternative re-runs triage for every duplicate delivery.
func samePromotion(a, b agent.Promotion) bool {
	if a.PromotionID == "" || b.PromotionID == "" {
		return true
	}
	return a.PromotionID == b.PromotionID
}

// Wait blocks until in-flight triage finishes.
func (s *Server) Wait() { s.wg.Wait() }

// Status is the handler's account of itself for the status page: which pull
// requests are being triaged right now, which have a newer promotion queued
// behind one, and the totals since start-up. Sorted, because a set that
// reshuffles between refreshes reads like activity.
func (s *Server) Status() web.TriageStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := web.TriageStatus{Done: s.done, Failed: s.failed}
	for n := range s.inFlight {
		st.InFlight = append(st.InFlight, n)
	}
	for n := range s.pending {
		st.Queued = append(st.Queued, n)
	}
	sort.Ints(st.InFlight)
	sort.Ints(st.Queued)
	return st
}

// upstreamResolver reads what maintainers wrote, when it can.
//
// GitHub-only, and deliberately so: it reuses the token and the api.github.com
// egress the agent already has for reading the gate. The registry hops needed
// to find which GitHub repository an artifact comes from are the only new
// network surface, and they fail softly; an artifact whose registry is not
// reachable produces an explanation grounded in the render alone, which is
// what this did before upstream notes existed.
func upstreamResolver(cfg *Config, tokenSource func(context.Context) (string, error),
	eg egress.Policy) upstream.Resolver {
	if !cfg.Upstream {
		return nil
	}
	return &upstream.GitHubReleases{
		// Both, and in that order of precedence. A static token is what token
		// mode has; a source is what App mode has, because an installation
		// token expires in about an hour and one taken at start-up is expired
		// for most of the pod's life.
		Token:        cfg.GitToken,
		TokenSource:  tokenSource,
		MaxReleases:  cfg.UpstreamMaxReleases,
		MaxBodyChars: cfg.UpstreamMaxBodyChars,
		MaxCommits:   cfg.UpstreamMaxCommits,
		Egress:       eg,
	}
}
