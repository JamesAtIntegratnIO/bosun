// bosun triages automated dependency-bump pull requests.
//
// Kargo calls it when a pull request opens. It reads the pre-merge gate,
// explains a red one, and fixes the cases the rendered diff proves -- a chart
// default that flipped, a pin that must move with another, a port a policy
// still names. Everything else it hands to a human.
//
// The model never WRITES. It returns a structured proposal -- a verdict with a
// set of scalar edits, or one whole document reshaped for a schema that moved
// its fields -- and this process applies it, behind an allowlist, a from-value
// check and a corroboration check for a scalar, and behind identity,
// schema-validity and value-provenance checks for a document. So "never edit
// the gate", "never invent a version" and "never invent data" are properties of
// the code, not requests in a prompt. See docs/safety-model.md,
// docs/prompt-contract.md and adr/0007.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/JamesAtIntegratnIO/bosun/cluster"
	"github.com/JamesAtIntegratnIO/bosun/edits"
	"github.com/JamesAtIntegratnIO/bosun/gitprovider"
	"github.com/JamesAtIntegratnIO/bosun/llm"
	"github.com/JamesAtIntegratnIO/bosun/upstream"
)

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags|log.LUTC)

	cfg, err := LoadConfig()
	if err != nil {
		logger.Fatalf("configuration: %v", err)
	}
	if cfg.NormalizeLegacyAuthor() {
		logger.Print("ignoring the legacy author bosun <bosun@users.noreply.github.com>: " +
			"that is the noreply address of an unrelated GitHub account; deriving the commit identity instead")
	}

	var model llm.Provider
	switch cfg.LLMProvider {
	case "openai":
		model = &llm.OpenAI{
			BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMKey, Model: cfg.LLMModel,
			ReasoningEffort: cfg.LLMReasoningEffort, Timeout: cfg.LLMTimeout,
		}
	case "anthropic":
		model = &llm.Anthropic{
			BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMKey, Model: cfg.LLMModel,
			Timeout: cfg.LLMTimeout,
		}
	}

	// LoadConfig has already rejected any provider not handled here, so a
	// nil git would be a programming error rather than a configuration one.
	var git gitprovider.Provider
	// The credential the UPSTREAM reader uses. The same credential as the git
	// client's, and not the same object: under App auth it exists only as a
	// function, because installation tokens are minted per use and the config's
	// static token is empty.
	var upstreamToken func(context.Context) (string, error)
	switch cfg.GitProvider {
	case "gitea":
		git = &gitprovider.Gitea{
			BaseURL: cfg.GitAPIBase, Owner: cfg.GitOwner, Repo: cfg.GitRepo,
			Token: cfg.GitToken, Username: cfg.GitOwner,
			AuthorName: cfg.AuthorName, AuthorEmail: cfg.AuthorEmail,
			InsecureSkipTLSVerify: cfg.GitInsecureSkipTLSVerify,
		}
	default:
		gh := &gitprovider.GitHub{
			APIBase: cfg.GitAPIBase, Owner: cfg.GitOwner, Repo: cfg.GitRepo,
			Token: cfg.GitToken, AuthorName: cfg.AuthorName, AuthorEmail: cfg.AuthorEmail,
		}
		// Acting as an App is about IDENTITY, not access. A token grants the
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
			// quietly does nothing -- which is the failure mode this whole
			// service keeps finding in itself.
			if _, err := app.Token(context.Background()); err != nil {
				log.Fatalf("github app authentication failed: %v", err)
			}
			gh.TokenSource = app.Token
			// Without this the upstream reader ran anonymously against
			// api.github.com -- 60 requests an hour per IP -- from the moment
			// the agent became an App, because it was handed cfg.GitToken and
			// App mode leaves that empty. The failure surfaced as "no upstream
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

	t := &Triage{
		Git: git, LLM: model,
		Brand: cfg.Brand, BrandMark: cfg.BrandMark,
		Policy:           edits.Policy{Allow: cfg.AllowPaths, Deny: cfg.DenyPaths},
		CheckName:        cfg.CheckName,
		GateReportAuthor: cfg.GateReportAuthor,
		MaxAttempts:      cfg.MaxAttempts,
		GateWait:         cfg.GateWait,
		GatePoll:         cfg.GatePoll,
		Explain:          cfg.Explain,
		Migrate:          cfg.Migrate,
		Structural:       cfg.Structural,
		MaxRestructured:  cfg.MaxRestructured,
		Upstream:         upstreamResolver(cfg, upstreamToken),
		CloneRoot:        cfg.CloneRoot,
		RepoURL:          cfg.GitRepoURL,
		Log:              func(f string, a ...any) { logger.Printf(f, a...) },
	}

	if cfg.LiveReads {
		reader := &cluster.APIServer{ArgoCDNamespace: cfg.LiveReadsArgoCDNamespace}
		// Fail at start-up, the same rule as the App's key -- and here it
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
			logger.Fatalf("live cluster reads are enabled and the apiserver could not be read: %v\n"+
				"  the NetworkPolicy needs an explicit egress rule for the apiserver: a ClusterIP is "+
				"DNAT'd before policy is evaluated, so kubernetes.default.svc is not reachable by "+
				"default and the symptom is a hang with zero bytes", err)
		}
		ns, _ := reader.Namespace()
		logger.Printf("reading the cluster read-only from %s (Applications in %s)",
			ns, cfg.LiveReadsArgoCDNamespace)
		t.Cluster = reader
	} else {
		logger.Print("live cluster reads are off; briefs say what the repository holds " +
			"and nothing about what is running")
	}

	// Said at start-up, because the alternative is a deployment that silently
	// believes any comment carrying the gate's marker and nobody finding out
	// until it matters.
	if t.GateReportAuthor == "" || t.GateReportAuthor == "*" {
		logger.Printf("gate reports are read from ANY author: set gate.reportAuthor " +
			"to the account your gate comments as")
	} else {
		logger.Printf("gate reports are read only from %q", t.GateReportAuthor)
	}

	srv := &Server{Triage: t, Log: logger, Timeout: cfg.LLMTimeout + 5*time.Minute}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/promotion-opened", srv.PromotionOpened)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	srv.Wait()
	logger.Print("stopped")
}

// Server accepts Kargo's call and gets out of the way.
type Server struct {
	Triage  *Triage
	Log     *log.Logger
	Timeout time.Duration

	wg sync.WaitGroup
	// inFlight collapses duplicate calls for the same pull request. Kargo
	// retries a step whose response it did not like, and a retry must not
	// start a second triage of the same PR.
	mu       sync.Mutex
	inFlight map[int]bool

	// runFn is the work the handler dispatches. Defaults to Triage.Run; tests
	// substitute it so the handler's concurrency behaviour can be exercised
	// without a git host or a model behind it.
	runFn func(Promotion) error
}

func (s *Server) run(ctx context.Context, p Promotion) error {
	if s.runFn != nil {
		return s.runFn(p)
	}
	return s.Triage.Run(ctx, p)
}

// PromotionOpened answers 202 immediately and does the work on a goroutine.
//
// This is not an optimisation. Kargo's `http` promotion step is synchronous,
// so a handler that blocked would put a model round trip -- minutes, on a
// local model -- inside the critical path of every promotion.
func (s *Server) PromotionOpened(w http.ResponseWriter, r *http.Request) {
	var p Promotion
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
		s.inFlight = map[int]bool{}
	}
	if s.inFlight[p.PRNumber] {
		s.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"already in progress"}`))
		return
	}
	s.inFlight[p.PRNumber] = true
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inFlight, p.PRNumber)
			s.mu.Unlock()
			if rec := recover(); rec != nil {
				s.Log.Printf("PR %d: triage panicked: %v", p.PRNumber, rec)
			}
		}()

		// Detached from the request context on purpose: the response has
		// already gone back to Kargo, so cancelling with it would abort every
		// triage immediately.
		ctx, cancel := context.WithTimeout(context.Background(), s.Timeout)
		defer cancel()

		start := time.Now()
		if err := s.run(ctx, p); err != nil {
			s.Log.Printf("PR %d: triage failed after %s: %v", p.PRNumber, time.Since(start).Round(time.Second), err)
			return
		}
		s.Log.Printf("PR %d: triage done in %s", p.PRNumber, time.Since(start).Round(time.Second))
	}()
}

// Wait blocks until in-flight triage finishes.
func (s *Server) Wait() { s.wg.Wait() }

// upstreamResolver reads what maintainers wrote, when it can.
//
// GitHub-only, and deliberately so: it reuses the token and the api.github.com
// egress the agent already has for reading the gate. The registry hops needed
// to find WHICH GitHub repository an artifact comes from are the only new
// network surface, and they fail softly -- an artifact whose registry is not
// reachable produces an explanation grounded in the render alone, which is
// what this did before upstream notes existed.
func upstreamResolver(cfg *Config, tokenSource func(context.Context) (string, error)) upstream.Resolver {
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
	}
}
