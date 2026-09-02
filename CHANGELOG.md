# Changelog

All notable changes to `bosun`. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

## [Unreleased]

### Fixed

- **A merge gate that abstained because a file's inode moved.** `git fetch`
  ends by spawning `git maintenance run --auto --detach`, which daemonises and
  outlives the fetch that started it. Every checkout here is fetched into more
  than once -- `EnsureHead` pins the commit under judgement, then `MergeBase`
  walks a ladder of deepening fetches, up to six inside a second -- so one of
  those background passes is in flight during the next fetch by construction,
  not by bad luck.

  A pass that decides to repack rewrites `.git/shallow` through a lock and a
  rename, which is a new inode even where the content is unchanged. Meanwhile
  a shallow fetch reads that file while building its request, negotiates with
  the host, and re-stats it before taking the lock; git compares inode, size
  and mtime, so a file that moved in between is `fatal: shallow file has
  changed since we read it` and the fetch dies with 128. Nothing is damaged --
  git is refusing to act on a read it can no longer trust -- but `MergeBase`
  returned the error, and the gate then had no revision to diff against and
  declined to judge a pull request that was fine.

  Every git command in a checkout now runs under `maintenance.auto=false` and
  `gc.auto=0` (both keys, because which one a fetch consults moved between git
  versions), and nothing here wanted the maintenance: these checkouts are
  cloned for one pull request and deleted once it has been answered, so
  repacking one buys nothing and cost this. A fetch that hits the message
  anyway -- an operator's own `gc`, a clone root shared with a second run --
  is retried once, because the error means nothing was written and bosun does
  not own every process on the machine.

  Seen first as an intermittent CI failure, on a test whose clone was never
  shallow to begin with: `git clone --depth 1 /some/path` ignores the depth,
  hardlinks the whole object store, and says so in a warning on stderr that
  nothing was reading. Those clones go through `file://` now and fail if what
  comes back is a complete history, so the ladder they exist to check is the
  ladder that runs.

### Added

- **A documentation page for the MCP surface.** The tools and what each
  answers, what a caller gets before the first sweep and why an absence there is
  not an empty result, turning it on, and the token -- one page, beside the
  supervisor's and the status page's, rather than the paragraphs it had been
  spread across. [`docs/mcp.md`](docs/mcp.md), on the site as *The MCP surface*,
  and in the README's component table so a reader finds it from the front page.

  The disclosure is stated where somebody deciding whether to publish the port
  will read it, beside the status page's own note and saying the same thing over
  a wider list: operational metadata is served -- Stage and Application names,
  chart versions, findings and their remedies, pull-request titles and labels,
  and the helm and schema error strings the page does not carry -- and no
  credential is. The difference between the two surfaces is who reads them and
  what they hold.

  The safety model's section for this surface now separates what is enforced --
  no mutation, no live read, no configuration reach, redaction, composed
  remedies, origin tagging, stamp stripping -- from the risk that remains, which
  is stated in words rather than implied: bosun cannot make a careless client
  safe, because text sanitised to harmlessness does not exist. What it
  guarantees is provenance labelling and instructions that are bosun's own or
  absent. The trust model cites
  [ADR 0014](adr/0014-an-install-serves-one-trust-domain.md) rather than
  restating its argument: the pages say only that an install's view is served
  flat and whole, with no per-project or per-caller filtering anywhere, and
  point at the record for why.

- **`gate_status` and `triage_status`: the queue, and what the agent is doing
  about one of it.** `gate_status` answers what bosun's last gate sweep saw
  across every open pull request: each one with the state standing against its
  head commit, whether it blocks, and the blocker breakdown as counts per kind.
  `gate_verdict` is still where the findings behind one of those counts live,
  and this is the call before it -- the one that says which pull request to ask
  about.

  Beside the queue travels the sweep's own failure. A gate that cannot reach
  the git host has two symptoms: a line in a log nobody is reading, and a queue
  that says nothing is open, forever, which is the reading a caller will take.
  So an empty queue is published only by a sweep that actually listed, and a
  sweep that could not says so in a field of its own. A queue held over from an
  earlier sweep is published rather than dropped -- it is evidence, and a
  caller with stale evidence is better off than one with none -- with the error
  beside it saying it is older than the sweep time above it.

  `triage_status` answers what the agent is doing on one pull request right
  now: the phase it is in, how many automatic fix attempts it has spent against
  its cap, and the labels standing on the pull request. That is the difference
  between an agent still working and one that has finished and will not try
  again, which is the same distinction its commit status exists to draw for a
  person. A pull request the agent is not working is answered as such and never
  as an error: not working one is the resting state, and the attempt count is
  what says whether it ever did.

  The phase and the labels come from two different clocks, and the result says
  so. What is running is this process's own state, current to the microsecond;
  the labels and therefore the attempt count are as old as the last gate sweep,
  because a tool call may reach no git host at all. So the labels now ride
  along in the sweep's snapshot rather than being fetched on request, and the
  attempt count is made with the agent's own arithmetic rather than a second
  reading of the same label prefix -- the cap remembers under a name that
  follows the brand, and two counts of it would disagree exactly on a renamed
  install, where one says an attempt remains and the other has already
  escalated.

  Labels are the first text on this surface that anybody with write access to
  the repository chooses, so they carry an origin of their own rather than
  being folded into the pull-request author's. Bosun writes some of them, and
  that is not a reason to publish any of them as bosun's own: a per-label guess
  is one a hostile label imitates by choosing bosun's prefix.

- **`gate_verdict`: why a pull request is blocked, as data.** A platform
  engineer whose pull request is red asks their coding agent why, and the agent
  asks bosun. What comes back is the verdict standing against the head commit:
  the blocker breakdown as counts per kind, every finding behind those counts,
  and the dropped API versions as fields -- which definition, which versions it
  stopped serving, which one survives, and the kind of manifest that has to
  move. Each finding says whether an edit in the repository could clear it, so
  an agent stops hunting for one that does not exist, and the list of what the
  gate could not render travels beside them, because a clean verdict over a
  partial render is a narrower claim than a clean verdict over a whole one.

  Answering this used to mean scraping the pull-request comment and parsing
  `<!-- gitops-gate:… -->` stamps out of it, which made an internal wire format
  into a public contract and got the reader nothing that was typed. Nothing new
  is computed: the answer comes from the snapshot the last gate sweep already
  holds, so a request reaches no git host, no cluster and no model.

  **A pull request with no verdict standing is answered as such, and never as a
  passing one.** There are six ways to have no verdict -- no sweep has run, the
  sweep could not list pull requests, the sweep ran and this pull request was
  not open, a render is in flight, a verdict already stood on the git host and
  was not re-litigated, the gate could not run -- and each has its own state
  and its own sentence, so a client never has to read two fields to tell them
  apart. The findings field is *absent* in all of them, and *empty* only when
  the gate looked and found nothing.

- **Provenance tagging, and the rule every later tool inherits.** This is where
  text bosun did not write first enters an MCP result, and that is most of its
  weight: a verdict carries chart-rendered object names, helm and schema error
  strings, and pull-request titles. All of it lands in another model's context,
  and that model usually holds tools bosun refuses for itself -- so a hostile
  release note does not need to jailbreak bosun's model, only to be delivered
  by it to a better-armed one.

  Facts travel in typed fields a string cannot forge, and free text travels
  tagged with where it came from: a rendered chart, helm, the schema validator,
  the render as a whole, this repository, the cluster, a pull request's author,
  or bosun itself. The contract a client can rely on is that instructions in a
  result are bosun's own or absent. The dropped-version detail is the one block
  that carries no tag, because it is what a repair acts on with no person in
  between: every field of it is matched against the repair contract's own
  grammars first, and a finding whose fields do not hold their shape is
  published without them rather than with them labelled.

  What is not on offer is sanitised text. It does not exist, and claiming it
  would be the more dangerous lie.

- **The gate's stamp grammar is stripped from every MCP response.** The gate
  keeps its memory inside its own pull-request comment -- the last verdict, the
  head it judged, the migration a repair performs -- because a gate with no
  database has nowhere else to put it. A client of this surface reads a verdict
  and writes prose onto a pull request, so a stamp smuggled through a
  chart-rendered object name would make that client a forgery relay,
  republishing a verdict the gate never reached against a commit it never
  judged. The HTML comment delimiters are broken where a byte reaches the wire,
  visibly rather than silently: an object whose name contains an HTML comment
  is worth somebody looking at the chart that produced it.

- **A read-only MCP server, on a listener of its own, with its first tool.**
  Bosun computes the most expensive facts in the promotion loop -- why a Stage
  silently stopped promoting, and the exact command that unsticks it -- and
  until now published them only as prose: a comment on a pull request, a page
  behind a port-forward, a metrics endpoint. That is right for a person reading
  a page and useless to the agents people actually work through, which were
  left scraping markdown written for somebody else.

  `pipeline_report` answers with the last sweep's findings as typed values:
  kind, severity, subject, the evidence with its numbers, how long the
  situation has held, and where one exists the paste-ready command that
  recovers it, worst first. Beside them travels the sweep's own accounting of
  what it examined, so a report with no findings can prove it looked.

  Nothing is computed. Every answer comes from the snapshot the sweep already
  holds, so a request reaches no git host, no cluster and no model, and a
  chatty client cannot spend an install's rate limit. Nothing mutates, because
  no tool does and none is planned: the ClusterRole has no write verb and the
  reason is written down.

  Three rules land here rather than being retrofitted onto the three tools
  that follow. **Honest absence is structural**: before the first sweep the
  `findings` field is ABSENT rather than empty, and the result says in words
  that nothing has looked -- the same distinction the HTTP surfaces make with
  a 503, in the one shape JSON can carry it. **Every result is
  repository-stamped** and carries the sweep timestamp and the answer's age.
  **Facts are typed and text is tagged**: severities, kinds, counts and
  durations are fields a string cannot forge, and every free-text field says
  whether bosun wrote all of it or quoted a cluster inside it, because these
  answers land in agents holding tools bosun refuses for itself.

  Two controls keep credentials off the surface, in that order. The primary
  one is a compile-time rule -- the `mcp` package imports the result types and
  the redactor and nothing else, so no field path from any tool result reaches
  a credential, and a reflection walk over the registered result types keeps
  it true on paths no request exercises. The secondary one is the process
  redactor from the prefactor before this, applied at the single point where
  a byte reaches the wire rather than by each handler.

  The transport is a hand-written JSON-RPC handler rather than the official Go
  SDK, and that was the SDK's dependency graph rather than its API: it adds
  eight modules to a `go.mod` with four direct requirements, among them
  `golang.org/x/oauth2` and through it a client for the cloud metadata service
  at 169.254.169.254 -- which answers instance credentials to anything that
  asks, and which this project's own NetworkPolicy excepts by name for exactly
  that reason. The auth check sits behind an interface either way, so the
  verifier rung after the static token is additive.

- **Every remedy is now composed from pieces bosun validated.** A remedy is
  the highest-stakes string this project emits, because it is built to be run,
  and the names interpolated into one come from a Kargo CRD `pipeline`
  deliberately does not vendor. Every command builder now checks its object
  names against the RFC1123 subdomain grammar, its repository paths and yq
  keys against grammars of their own, and emits the finding WITHOUT a remedy
  rather than a suspect one when a piece fails. Kubernetes validates these
  names itself, so this is expected never to fire -- which is exactly why it
  is cheap to enforce and loud when an upstream assumption stops holding. It
  applies to the status page and the markdown report too, not only to MCP:
  there is one place remedies are composed, so there is one place they are
  checked.

- **`web.theme`, so the page's treatment is a deployment decision.** `auto`
  (the default) follows the reader's system preference, which is what the page
  did before; `dark` and `light` stamp `data-theme` on the document -- the same
  attribute the site's own toggle writes -- and beat that preference in both
  directions.

  **There is no toggle on the page, and this is what replaces one.** A toggle
  needs somewhere to remember the answer and this page has nowhere: it carries
  no script, which is what lets a gateway put a strict content policy in front
  of it, and it refreshes itself every minute, so the CSS-only toggle that
  works without script would be wiped on every refresh. A cookie would work
  and costs a route, a redirect and a `Set-Cookie` on a page that today sets
  nothing. So the choice moves to where this install's other decisions live.

  Refused rather than defaulted, in both places it can be set: the chart's
  schema rejects anything but the three at render time, so a typo fails
  `helm template` instead of reaching a pod, and `WEB_THEME` set directly is
  refused at start-up. A page rendered in a theme nobody chose, saying nothing
  about it, is the quiet kind of wrong this agent exists to notice elsewhere.

  Light is now written twice in the stylesheet -- once under the media query,
  guarded so an explicit `dark` beats a light system, and once under
  `[data-theme='light']` so an explicit light beats a dark one. Plain CSS
  cannot express that in one rule; a test fails if the two blocks stop
  agreeing.

- **The supervisor says which metric stopped a Stage, and what it is holding.**
  A `verification_stuck` finding used to report that a verification failed and
  then hand back two `kubectl` commands telling the reader to go and look at
  the AnalysisRun's `metricResults` -- the one question they already had. It
  now reads that run and answers it: the metric by name, whether it errored or
  failed, and what it last reported.

  The distinction is the useful part. A metric that **errored** never got an
  answer and one that **failed** got the wrong answer, and telling somebody to
  fix their thresholds when their Prometheus is unreachable costs them the
  afternoon this exists to save. That is the exact shape of the three findings
  this feature came from: `argo-cd`, `cert-manager` and `open-webui` held for
  three days by one NetworkPolicy that dropped every `verify.apps` query, which
  a human found by reading AnalysisRuns.

  **"An AnalysisRun with no timeout holds the queue indefinitely" is now a
  reading rather than an assertion.** It was written into every long-running
  verification finding whether or not it was true of that run. Argo Rollouts
  runs a metric `count` times, and a metric with an `interval` and no `count`
  measures until something stops it -- so the finding says which metric that
  is, or says the run has no unbounded metric and is slow rather than endless.

- **A wedged Stage names what stopped arriving.** "stopped receiving artifacts"
  is true and unusable; a freight name is a hash, so printing that instead
  would read as detail while saying less. The finding now names the image tag
  or chart version the freight carries -- the half that matches the pull
  request in the reader's other tab -- falling back to Kargo's alias, and to
  the word `artifacts`, when it cannot.

  **Both reads are one GET of one named object**, for the Stages a finding is
  actually about. Kargo creates a Freight per discovery and prunes none of
  them, so listing them would be the most expensive read in the service by an
  order of magnitude, to print two. A healthy sweep makes neither request. A
  refused or pruned read costs the detail and never the finding: every one is
  produced exactly as before, and the report's notes say which read did not
  land.

  Needs `freights` and `analysisruns`, which the chart has granted since it
  existed and nothing has used until now.

### Changed

- **The gate's verdict is enumerable, and its breakdown is that list added
  up.** `DiffResult.Findings` names every reason the gate has an opinion --
  what it is about, its contribution to the breakdown, whether it blocks, and
  whether an edit in the repository could clear it -- and `Blockers` now folds
  over it rather than walking the result a second time. Two walks over the same
  findings is how a caller ends up holding a count of three and a list of two,
  with no way to tell which half is lying.

- **Schema validation returns the failures, not a count of them.** The report
  comment always named the rejected manifests; the structured verdict could
  only say `schema=3`, which reads exactly like a bug in whatever produced the
  number. `gate.ValidateManifests` now returns one `SchemaFailure` per
  rejection, and the prose and the slice are two renderings of one pass.

- **Redaction is one thing the process owns, not a helper each git provider
  reaches for.** A credential is taken out of text by `redact`: primed once at
  start-up with every secret `config.go` loaded, and read through
  `redact.Text` by any surface holding text it is about to log, post, or wrap
  into an error.

  The reasoning was already written down one level lower, on an unexported
  helper the two git providers shared: no push embeds a credential in its
  remote URL any more, but git quotes back whatever the server says and a
  misconfigured host can echo a credential it was sent, so what git prints on
  a failed push is not safe to forward unread. None of that is about git.
  helm is a subprocess whose stderr becomes an error; the ArgoCD, Kubernetes
  and model clients all turn responses into text somebody reads. Every one of
  them was reached through a credential.

  **What a pull request comment says is unchanged**, with one exception below.
  Both push call sites still name the token they were built with, so a
  provider constructed outside the composition root redacts its own credential
  whether or not anything primed the process, and GitHub still names the
  installation token minted for that one push, which start-up never saw.

  Two rules the package exists to hold, and the second is the exception. An
  unset credential -- and most installs configure only some of the five -- is
  dropped rather than used, because `strings.ReplaceAll(s, "", m)` is a rule
  that matches everywhere and turns every string in the process into confetti.
  And a credential containing another is replaced whole, longest first. That
  one is a real change: the old `redactErr(redactErr(s, tok), g.Token)` ran
  the two in argument order, so where an installation token and the static one
  shared a prefix the shorter went first and left the longer one's tail
  standing in text now carrying a marker that claimed it was handled.

  **Three derived tests, because the list is the half that rots.**
  `redaction_test.go` walks `config.go`'s syntax tree for every call to
  `envSecret`, loads a `Config` whose every credential is a distinct sentinel,
  and fails if the redactor lets one through; it also walks `main.go`, so
  deleting the one line that primes the process fails rather than passing.
  And a second walk in the same file derives from the mechanism instead of the
  call sites: a function that starts `git` and then quotes what git wrote to
  stderr must pass that text through `redact.Text`. That last one is the
  incident the deleted helper's own comment described -- gitea called it,
  github inlined two `ReplaceAll` calls, and only one of the two was reviewed
  when the rules last changed.

- **`helm`, `kustomize` and `kubeconform` redact their stderr too.** The rule
  that every subprocess's stderr goes through the redactor arrived narrow twice
  and was widened twice, and both corrections were the same correction. It
  first qualified a function only if it handed git a push credential; then, on
  the command being git. The binary was never the reason.

  What makes a subprocess's stderr dangerous is that this process starts it
  while holding credentials. `cmd.Env` is nil at nearly every call site, and a
  nil `Env` means the child gets `os.Environ()` -- so a chart render runs with
  `GIT_TOKEN`, `ARGOCD_TOKEN` and the model key in its environment, and so does
  `kubeconform`. A plugin, a debug flag or a chart hook that prints its
  environment puts them on stderr, and stderr is what these three quoted into
  an error that reaches a log and the gate's published report. The other half
  is the one git already demonstrated: a chart render pulls from a registry
  over somebody else's network, and a host that echoes a request header back
  inside an error body is echoing a credential it was sent.

  The rule is simpler for losing the binary, not more complicated: one
  condition where there were two, and `gitstderr_test.go` is now
  `subprocess_stderr_test.go` because the old name had stopped being true. Its
  self-check went from eight call sites to eleven -- the CRD read, the
  kubeconform run, and `gate/sources.go`'s runner, which is one function that
  is `helm`, `kustomize build` or `kubectl kustomize` depending on which
  binary its caller handed it.

  **Redaction is the second line here and not the first.** The first would be
  to stop handing every subprocess every credential this process loaded, which
  is a change to what `helm` runs with rather than to what bosun prints, and it
  is #122 for that reason.

- **A credential in `GIT_REPO_URL` no longer reaches argv, or the status page.**
  The redaction above stopped a configured credential being published in an
  error. It did not stop it being *handed out*: three clones passed that URL to
  git as an argument, and `/proc/<pid>/cmdline` is world-readable, so for the
  length of each clone the token was there for `ps` and for anything that logs
  a command line. This is the same exposure `pushAuthEnv` was written to close
  on the push path, and `pushRemote`'s own comment had already named it.

  **The shape is ArgoCD's**, because it solved this for the same reason and its
  answer is worth copying: the URL it stores as `origin` is the raw one with no
  credentials in it, and the credential is attached per-command through the
  environment, by the commands that actually contact a host and by no others.
  Nothing is written into the checkout's `.git/config`, so a checkout that
  leaks is not a checkout that carries a token. What is not copied is the
  transport -- ArgoCD supplies the credential through `GIT_ASKPASS` and a
  helper script on disk, and this process already has a way to hand git a
  credential through the environment, the `http.<remote>.extraHeader` the push
  has used since it stopped putting tokens in argv.

  So `gitprovider.Remote`: a configured URL split into the address git is given
  and the environment that authenticates it, and a type rather than two strings
  because a call site that took one and forgot the other is a clone that
  silently stops authenticating. `agent`, `gateservice` and `supervisor` hold
  one of these now instead of a URL string, so there is no longer a repository
  URL in those packages for anything to pass to git by accident.

  **Three clones became one.** No two agreed -- the agent's was not quiet, the
  supervisor's passed `--branch` only when it had one -- and each was a
  separate place to get this wrong; `gitprovider.Clone`
  is the only one now. `EnsureHead` and `MergeBase` take the remote too, because
  a clone that no longer embeds the credential in `origin` leaves nothing in the
  checkout for the fetches that follow to authenticate with -- which is exactly
  why ArgoCD attaches credentials per command rather than once.

  **The status page published it too**, and that was worse: `GIT_REPO_URL` went
  to the page verbatim, so an install with a token in that URL rendered it as
  the repository link, on the listener whose whole design is that it is safe to
  expose. Nothing about the link said so. Found while doing the work above,
  fixed with it.

  **Two derived guards.** One is the rule this needed: a git command that names
  a remote-facing subcommand may only live in `gitprovider`, the package that
  owns the credential. The subcommands are a list about git rather than about
  our call sites, which is the distinction that matters, but it is still a list
  and the test says so. It
  is keyed on the mechanism rather than on the three call sites, and
  deliberately does not require the function to call `exec` itself, because the
  shape it has to catch is the one that was here: `gitRun(ctx, "clone", …)`,
  with a helper three lines away starting the subprocess. The other is the
  argv guarantee checked against reality, with a shim standing in for git that
  records what the kernel was actually asked to run -- the same test the push
  path has had since it moved its own token out of argv.

- **Every git command redacts what git printed, not only the two that push.**
  The rule that landed with the redactor above qualified a function only if it
  called `pushAuthEnv`, on the reasoning that a push is where a credential
  reaches git; it named `EnsureHead`'s fetch and the merge-base ladder as
  deliberately out of scope, because "nothing in them was ever given a secret
  to echo".

  That last clause was wrong, and one environment variable is what makes it
  wrong. Those commands run against `origin`, and origin's URL is
  `GIT_REPO_URL` verbatim -- the agent, the gate service and the supervisor
  each clone from it, and `gitprovider` strips the userinfo out of the push
  remote precisely because an operator may have put a credential in it. An
  install configured that way has handed a secret to every git command here:
  not through `pushAuthEnv`, not in argv, but in the conversation with the
  remote, which is the channel the original reasoning was always about. git
  repeats what the server says.

  So seven more call sites redact their stderr -- `EnsureHead`, `gitRun` and
  `gitLine` in `gitprovider`, the two clones in `agent` and `gateservice`, the
  supervisor's clone, and the diff that reads a pull request's changed files
  -- and **a credential embedded in `GIT_REPO_URL` now primes the redactor**.
  The credential and never the URL: a repository URL is in the chart, the logs
  and half the error messages, and priming the whole string would leave an
  operator reading `unable to access "***"`, which does not say which
  repository failed. With a password the username is a placeholder the host
  ignores; with no password the username is the whole credential, which is how
  a forge writes a token into a clone URL. `ssh://` primes nothing, and that
  guard is load-bearing rather than tidy -- an ssh remote's username is `git`
  on every forge in existence, and priming it would replace that substring in
  every sentence this process logs.

  The derived guard moved up to the root package as `gitstderr_test.go` and
  lost its qualifier, so it now walks every package rather than `gitprovider`
  alone; that is what found the supervisor's clone, which no version of this
  scoped to git providers would have seen. Its self-check went from two call
  sites to nine.

  **`GIT_REPO_URL` is still read with a bare `os.Getenv`**, so this one
  credential is outside the walk that derives the rest from `envSecret`, and
  `Config.Secrets` says so beside the line that adds it. Two things this does
  not do: a clone still puts that URL in argv, where a credential is visible in
  the process table (#118), and `helm`, `kustomize` and `kubeconform` still
  quote their stderr unredacted.

- **The status page wears the project's own colours.** It shipped in GitHub's
  palette -- `#0969da` links, `#0d1117` ground -- with an anchor emoji for a
  mark, none of which is Bosun's. That made the page a second place the brand
  was decided, and a second place is how a brand stops being one.

  The palette is now the site's, value for value: the badge navy, the two sea
  tones, the coral of the tentacles, the cream of the cap, each mapping to a
  token in `site/src/styles/theme.css` and named in a comment beside it. Dark
  is the base and light the override, which is the site's structure and its
  stated reason -- the badge is navy, and you read this next to a terminal.
  Inter, Space Grotesk and JetBrains Mono are named in the stacks so a reader
  who has them gets them; nothing is fetched.

  The mark is the site's own favicon, embedded and served from `/mark.svg` on
  both listeners rather than inlined -- two 6 KB copies in a page that
  refreshes every minute is not a saving. It is same-origin and served by this
  process, so the page still reaches nothing external and a gateway can still
  put a content policy in front of it without an exception for anybody's
  domain.

  **Two tests keep it honest**, because a copied palette drifts the moment
  somebody picks "close enough" for a state the original had no token for:
  one fails if any colour on the page is absent from the site's `theme.css`
  or is written outside the palette block, the other if `web/mark.svg` differs
  from `site/public/favicon.svg` by a byte.

### Added

- **The report renders itself.** `/pipeline` has served markdown since the
  supervisor was written, and markdown in a browser tab is source code, so the
  people who most need it -- whoever is wondering why an addon has not updated
  in three days -- were the people least likely to port-forward and pipe `curl`
  through a renderer. The same report is now a page: every finding with its
  remedy in a copyable block, the gate's open pull requests and the verdict
  standing on each, what the triage is doing right now, and the feature posture
  and egress stance this install is actually running under.

  It renders only state the process already holds, so a page load costs no git
  API call, no model call and no cluster read; a tab left open on the one-minute
  refresh spends nothing but the render. No script, no external asset, and every
  finding, note and pull request title is escaped, because finding text quotes
  cluster objects and a title is whatever the bump wrote.

  **It listens on a second port** (`WEB_ADDR`, `:8081`), and that is the point
  rather than a detail. The first port also answers
  `POST /v1/promotion-opened`; a NetworkPolicy and a gateway both draw their
  lines at the port, so the page can be published without publishing the
  endpoint that spends money and writes to the repository only because the two
  never share a listener.

  `/pipeline` is unchanged for everything that already reads it: markdown by
  default, `?format=text` for a terminal, and 503 before the first sweep. A
  browser gets the page instead, and before the first sweep it gets the page
  saying "no sweep has completed yet" rather than a 503, because the sentence
  a scraper must not misread is one a human should just be told.

### Fixed

- **The gate's "before" side is the merge base, not the base branch's tip.**
  A pull request that touched two files and deleted nothing was reported as
  removing two HTTPRoutes, a SnippetsFilter and two Authentik blueprints, and
  as *downgrading* an addon from 0.29.0 to 0.28.2. Another pull request had
  merged an hour earlier and added all of it, and the base
  side was rendered from `main` as it stood at that moment while the head side
  was a branch cut before it, so the whole intervening delta arrived in the
  report backwards, attributed to the pull request that was not responsible for
  any of it. A reviewer reading that would reasonably conclude the change tears
  out infrastructure.

  The tip is what a merge lands *on*, which is why it was there, and it is the
  wrong revision to diff *against*: it moves whenever anything else merges. The
  merge base is the only revision at which the two sides differ by exactly this
  pull request. It is also stable while the head is, which the outcome cache
  had been assuming all along -- keyed on the head SHA, it went on serving a
  verdict that the next unrelated merge had already invalidated.

  **The same wrong base was narrowing the agent's edit scope**, and there it is
  a safety property rather than a report. `Policy.Scope` -- "cannot edit a file
  this change did not touch" -- is the `git diff` between the base and this
  head, so every file any other pull request merged after this branch was cut
  was inside the scope. The guarantee held exactly as long as nothing else
  merged. Both halves now ask `gitprovider.MergeBase`, which deepens a shallow
  clone until there is an answer rather than guessing at
  `pull_request.base.sha`, which is the base branch's tip wearing a
  commit-shaped name.

  **The report now names both revisions it compared**, under the headline and
  in `DiffResult` as `baseRev`/`headRev`. Only the head was ever recorded, and
  a report naming one revision cannot be told apart from a report whose other
  one was wrong -- which is why diagnosing this took a git archaeology session
  from the outside rather than a glance.

- **A values-only change to a registry chart is rendered, instead of passing
  in silence.** A pull request whose entire content was one `interval` key in
  one `values.yaml` moved 39 rendered `Warehouse.spec.interval` fields, and the
  gate's report on it contained no occurrence of `Warehouse`, `interval` or the
  addon's name. It counted zero unrenderable and zero unscanned, and named
  something else entirely under "Not covered". The change was invisible, and
  nothing said so.

  Chart rendering paired only the Applications whose chart *version* moved, and
  rendered both sides of each pair from the head checkout. An Application whose
  version held still therefore produced two identical renders and an empty
  diff -- but its row is identical on both sides by construction, because a row
  records *which* values files an Application layers and not what is in them.
  So the pairing selected nothing and the blind spot was total.

  It is total in a way that matters, because an addon whose chart lives in a
  registry is reached *only* this way: derivation cannot turn somebody else's
  artifact into a path in this checkout, so no source renders it. On the
  repository this was found on, 31 of 66 Applications were in that class, and
  most addon tuning in it is a values edit with no version change.

  An Application is now paired when its version moved **or** when the bytes of
  the value files it layers differ between the two checkouts, and each side
  renders from its own checkout. The values comparison reads files rather than
  rendering, so the answer "nothing moved" -- which is nearly every Application
  on nearly every pull request -- still costs no chart pull. A values edit that
  breaks the render blocks, with the same repair contract a bad bump gets; a
  chart that renders on *neither* side is coverage lost and says so, because a
  chart that needs a cluster to render is nobody's fault and must not turn
  every pull request touching it red.

- **A value naming the addon itself no longer claims the addon's label
  churn.** The first rich report 0.28.0 rendered on a live pull request opened
  on a kyverno `ClusterRole` whose "Values this repository sets" section held
  two lines, neither of them a value the repository sets: the repository's
  kyverno values contain the string `kyverno`, and the values mark's substring
  form matched it inside `kyverno-admission-controller` and inside every
  aggregation label the bump churned. On a chart whose bump moves those
  labels, the section filled with exactly the noise the partition exists to
  fold away.

  The Application's own identity tokens -- chart name, release name, App name,
  destination namespace -- are now equality-only leaves. A value that names the
  addon distinguishes nothing, because every render of that addon carries it
  everywhere; a field whose *whole* value is one is rare, was still chosen, and
  keeps its mark. Nothing was ever hidden by the old behaviour and nothing is
  hidden by the new one -- the fold is still a fold -- but the first thing a
  reader opens no longer shows label churn under the heading that promises them
  their own settings.

- **A document with no `kind` is no longer a schema rejection.** Two
  `values.yaml` files sitting in the directory sources of a live repository
  put a standing `schema=2` on **every** pull request, on Applications those
  pull requests did not touch, while ArgoCD reported both Applications Synced
  and Healthy. `Blockers.Schema` has no repository-side remedy and blocks, so
  the cost was a gate that is red on every change -- which is a gate people
  learn to override, the one failure its own documentation warns about.

  This is the gate agreeing with itself rather than a new tolerance.
  `objectFrom` already refuses a kindless document for the resource diff, so
  the same file was invisible to every finding except this one, where it
  arrived as kubeconform's `missing 'kind' key`. Only the kindless case is
  filtered, and deliberately not every parse failure: kubeconform refusing a
  document that *has* a kind is a finding, and folding the two together is how
  this started.

  Skipped documents are named and counted in the validation report -- "Not
  validated, because they declare no `kind` and nothing would apply them" --
  because a reader has to be able to tell "skipped a values file" from
  "skipped your manifest". The report's sections are also sorted now: the map
  they came from was ranged, and this comment is rewritten on every run, so an
  unordered list was a difference between two runs that was not a difference
  in the manifests.

### Changed

- **The field diff earns its length, and says whose fields moved.** Read back
  from the first live reports: a podinfo bump listed ten changed fields, nine
  of them one inserted flag shifting a command array plus a namespace stamp
  the new chart version started writing, and the reader's actual question --
  does this touch anything I set? -- was answerable only by reading all ten.
  And the gate rewrites its one comment per run, so the repair's re-render
  printed the same wall again. Three changes, one per source of noise:

  - **Scalar lists are aligned, not compared index by index.** One flag
    inserted into a command now reads `` `command`: gained `--prefix=/` `` --
    one line, membership rather than position. Only where it is shorter: a
    replaced element keeps the one-arrow form, and lists of maps keep the
    index walk, where positional comparison is what a reader expects of
    containers.
  - **A namespace stamp equal to the destination is not a change.** The
    resolved namespace is normalised into the body before hashing, the same
    rule as version stamps: ArgoCD sends the destination either way, so a
    chart that starts writing `metadata.namespace` changes no applied byte.
    The podinfo Service, whose only "change" was that stamp, drops out of the
    report entirely.
  - **Fields whose values this repository sets surface above the fold; the
    chart's own fold away** behind a summary that says whether opening it can
    matter: `2 fields, none of them a value this repository sets` is the
    whole read for most bumps. Folded, not filtered, deliberately: the mark
    is a value-match heuristic, the report is also the evidence the model's
    prompt carries, and the one line worth reading on the bump that prompted
    this was not values-linked -- a filter would have hidden it, a fold
    prices it at one click. The summary claims "none of them yours" only
    when the values were actually compared; a diff nobody checked keeps the
    old bare count, because "we could not look" must never read as "none".

### Added

- **The gate derives what to render from ArgoCD, and `.gitops-gate.yaml`
  becomes optional.** [ADR 0012](adr/0012-the-repo-stops-repeating-the-ship.md)
  is the argument; this is the implementation. The Applications and
  ApplicationSets ArgoCD serves supply the pointers and the root identities,
  and the pull request's checkout supplies every byte that renders. A
  repository whose Applications ArgoCD already serves is now gated with nothing
  committed at all.

  **Head wins over live wherever both can answer.** An ApplicationSet whose
  manifest is in the gated repository renders from the pull request's copy, not
  from the applied spec, because the applied spec is the previous answer and
  the question is what this change does. The live spec is the fallback for one
  case: a root whose file this repository does not hold, and every report names
  those, because an edit to one is invisible until it applies.

  **`.bosun.yaml` is the new filename, and `roots:` is the one thing it is
  for.** A root ApplicationSet carries no tracking annotation, so nothing leads
  to it; naming its file gates its edits from head, and makes a root a pull
  request *introduces* render at all. `sources:` is still there for anything
  derivation gets wrong, and takes precedence over derived sources.
  `.gitops-gate.yaml` keeps working unchanged, both filenames are read, and
  both present is an error rather than a precedence rule.

  **An empty scope refuses.** No derived source and none configured is an
  error, not a green: two empty sets have no difference between them, and
  reporting that passes every pull request. A refused or unreachable ArgoCD is
  an error for the same reason the inventory's is.

  Every report gains a **What was rendered** section: how many sources were
  derived from how many live Applications, which file is in force, and which
  roots rest on the applied spec. Scope now depends on cluster state, and an
  ArgoCD serving a smaller fleet than yesterday fails quiet, so the size of the
  world the verdict was reached in is on every report rather than only the
  interesting ones.

  The probe that measured this is now a test: a fixture repository rendered
  from its committed config and from a derivation produce the same rows.

- **`sources[].type: directory`, with ArgoCD's own semantics.** `recurse`,
  `include` and `exclude`, matched against each file's path relative to the
  source path. It is what most derived Applications become, and it is
  expressible in the file too. Measured on a live install: a source carrying
  `exclude: exclude/*` had a bootstrap manifest under that path, so ignoring
  the pattern renders an ApplicationSet the cluster does not have.

- **`helm.valuesObject` reaches the render**, as `valuesInline`. Values written
  into an Application rather than into a file have nothing in the checkout to
  read, and a render that dropped them rendered a chart nobody deploys.

- **[ADR 0012](adr/0012-the-repo-stops-repeating-the-ship.md): the repository
  stops repeating the ship.** `.gitops-gate.yaml` calls itself "the whole of
  that knowledge", and has not been since the gate moved in-cluster: `sources`
  and `valuesRef` are a hand-maintained second copy of what ArgoCD is already
  serving. The decision is to derive sources from live ArgoCD by default, take
  content from the pull request's head wherever both can answer, and keep an
  optional `.bosun.yaml` for the one fact ArgoCD cannot supply -- where an
  untracked root's manifest lives in the gated repository.
  `.gitops-gate.yaml` keeps working unchanged.

  The measurements it rests on are in the ADR rather than in a commit message:
  2 of 60 live ApplicationSets are untracked and both are roots, following live
  and following the file produce the same 63 rows, ArgoCD's own `?repo=` filter
  returns 7 of 65 Applications because it compares the first source by string
  equality, and the split-repository pattern needs no file at all. What it
  costs is recorded too, including the two asymmetrical failure modes: an
  ArgoCD that is down fails loud, and an ArgoCD serving a smaller fleet than
  yesterday fails quiet.

- **`cluster.ArgoCD` can read Applications and ApplicationSets.**
  `Applications(ctx)` and `ApplicationSets(ctx)` decode `spec.source`,
  `spec.sources` (with `ref`, `directory` and `helm.valueFiles`/`valuesObject`),
  `spec.sourceHydrator.drySource` and the tracking annotation, on the same
  client and the same account token as the inventory read. **Nothing calls them
  yet**, and the chart does not ask for the two RBAC lines they need until
  something does; this lands the wire contract with the test that sees both
  sides, per Rule 1a.

  A 403 now names the exact policy line the account is missing rather than the
  resource it was refused, per endpoint, because an operator adds those lines
  one at a time and a message naming the wrong resource sends them to paste
  something that changes nothing.

- **URL normalisation for repository comparison.** One repository is written at
  least three ways in a live fleet -- with and without `.git`, in different
  case, and in scp form -- and ArgoCD stores whatever it was given. Without
  folding those, a derived scope silently omits every Application whose author
  typed the URL differently, which is exactly how ArgoCD's own filter loses
  most of a fleet. All of `source`, `sources[*]` and `drySource` are compared.

- **Recorded ArgoCD shapes under `cluster/testdata/`,** served by a fake at the
  real paths: the fleet shape (multi-source addons resolving `$values/` through
  a sibling `ref`, a hydrated Application, inline `valuesObject`, one
  repository spelt three ways, two untracked roots among four ApplicationSets)
  and the split-repository shape (`directory.recurse` with an `exclude`
  pointer, and a root whose manifest is in another repository). They are
  hand-written to the shapes the assessment measured rather than captured from
  a live install, and `cluster/testdata/README.md` says so rather than letting
  a reader assume otherwise.

- **A values migration, for a chart this repository has outgrown.** The other
  half of the unrenderable-chart fix below. Once the gate blocks on a chart that will not render,
  something has to be able to fix it, and nothing could: removing a key,
  renaming a key and adding a key are three operations `edits` has no way to
  express, and it should not be widened to -- corroboration *is* the `from`
  match, and a deletion has no `from`.

  The home is the structural path, which already solves this shape of problem
  for manifests. The model is shown the values and the chart's own
  `values.schema.json` at the version being moved to, and returns the migrated
  values; three checks decide whether they may be used at all. Two are
  [ADR 0007](adr/0007-structure-from-the-schema-data-from-the-document.md)'s
  unchanged -- schema validity, and positional value provenance. The first one
  is new: **survival**, every value the new chart still declares comes through
  byte-identical, which is what stops a setting being retuned on the way past
  and what stops a renamed key landing on one that already had a value.

  Then the chart is **rendered with the answer** before anything is written.
  That is a guarantee the manifest path cannot have -- a migrated manifest is
  judged by a schema walk, and this is judged by the program that refused the
  original -- and it is what catches a key the chart *renamed* being dropped as
  though it had been removed.

  What lands is not the document. ADR 0007 re-serialises a migrated manifest
  and pays for it in comments, which is a fair price for something that is
  usually a document of its own; a repository's chart values are usually a
  subtree of a file that also holds thirty other addons, and the values with a
  note beside them are exactly the ones somebody had to reason about. So the
  harness diffs the original against the validated proposal into a **plan** --
  remove a key, rename a key, set a key -- and applies each one on that key's
  own lines. The model never names a file, a key or an operation. See
  [ADR 0013](adr/0013-a-values-migration-is-a-plan-not-a-document.md), which
  also records where repair ends: a key the schema requires and names no value
  for is escalated, with the key named, before the model is asked anything.

  Behind `triage.structuralMigration`, which is the flag the document migration
  already uses; an operator who has not turned that on has not turned this on
  either.

  **Measured on `qwen/qwen3.8-27b`:** classification **27/27**, full pass
  **27/27**, **UNSAFE 0** across all four paths (4m59s). The previous
  measurement on this model was 22/22 and 21/22; the case behind that 21 is
  fixed below, so this is the first clean sweep the suite has recorded.

  The suite gains a fourth path and four cases for it, and the first run of
  them found the failure ADR 0013 names as the one its harness cannot catch.
  On the 0.20.0 -> 0.25.1 case the model dropped `port: 8080` rather than
  moving it to `podPort`, and every validator accepted that -- correctly,
  because dropping a key the schema refuses is exactly what the other three
  keys in that bump needed. **full pass 0/1, UNSAFE 1.**

  The fix is not a firmer instruction. The prompt already carried the whole
  new schema, with `podPort: integer` printed directly under the section
  `port` was refused from. `structural.Vacancies` now states the fact instead:
  for each refused key, what the new schema declares beside it that these
  values do not set, filtered to slots that could hold the value. That is a
  fact about two documents, derived the way the findings above it are, and its
  other half carries as much as the first -- *nothing free beside it* is what
  tells a model to stop looking for a home. Same model, same case: **full pass
  1/1, UNSAFE 0**. `docs/prompt-contract.md` carries it as Lever 7.

### Changed

- **BREAKING: the ArgoCD account needs two more read lines.**
  `applications, get, */*` and `applicationsets, get, */*` in
  `argocd-rbac-cm`, beside the `clusters, get` it already has. No Kubernetes
  RBAC change, no new credential, no new mount. An install that upgrades
  without adding them gets an `error` status naming the exact policy line,
  which is the loud direction to be wrong in.
- **`concurrency` and `validate` move to the chart** as `gate.concurrency` and
  `gate.validate.*`. The renders happen in the operator's pod, against that
  pod's limits, so how hard the gate works and what it checks are decisions
  about that cluster rather than about the repository under review, the same
  line the egress deny-list is on. Both are unset by default, and unset leaves
  the gated repository's own file alone, so an install that configured either
  in its `.gitops-gate.yaml` keeps exactly what it had.
- **`valuesRef` retires for derived sources.** A multi-source Application names
  its own values source in `sources[].ref`, so `$values/` now resolves through
  the Application that used it rather than through one repository-wide guess
  that was wrong the moment two Applications disagreed. The key is still read
  for sources written in the file.
- **The agent's deny-list drops `.gitops-gate/**` and adds `.bosun.yaml`.**
  Every entry on that list is supposed to be a way to make a red gate green
  without fixing anything, and the list is now kept to that test in both
  directions. `.gitops-gate/**` was the inventory-snapshot directory, and
  nothing has read it since [ADR 0010](adr/0010-the-cli-goes-too.md) removed
  the CLI: an entry guarding nothing still reads as a guarantee, and a list
  that only ever grows stops describing what it protects. `.bosun.yaml` is
  here for the opposite reason. It is the filename the gate's config is moving
  to, and a guard that arrives after the reader does is a window with the
  guarantee off. `docs/safety-model.md` and the site's configuration reference
  carry the same table and are updated with it.

### Removed

- **`sources[].argocd`, which never selected anything.** It scoped a source to
  one ArgoCD instance in a fleet running several, by matching against a field
  on each `Cluster`. Nothing ever set that field: since
  [ADR 0009](adr/0009-one-gate-one-inventory.md) the inventory is read from one
  ArgoCD's own API, which does not report which install served it, so every
  cluster carried the empty string and a source naming an instance matched none
  of them. A helm source that matches no cluster is already a hard error, so
  the key failed a run rather than quietly narrowing one, and removing it takes
  nothing away from a configuration that works today. Strict parsing is what
  makes that safe: a config still setting it stops at parse with the key named.
  `Cluster.argocd` goes with it, having had no writer and now no reader.

### Fixed

- **A chart that will not render at the version a pull request moves to now
  blocks, and the settings it stops reading are named even so.** Two defects,
  one bump. Kargo raised `bosun.defaultVersion` from 0.20.0 to 0.25.1 against
  values still carrying four keys the chart had removed, the new
  `values.schema.json` refused them, `helm template` failed, and the gate
  filed the whole thing under **Not covered** and published a green verdict
  on a change that could not deploy. The agent's own comment named all four
  keys correctly and had nothing to act on, because the report it was reading
  said nothing was blocking.

  A failed render was a warning, and warnings count towards no blocker --
  which is the reasoning this repository already refuses, one line away, on
  `Blockers.Unscanned`: *"we could not look" blocks, and is not the same as
  "we looked and found none"*. A render that fails at the head revision is the
  stronger case again -- not "we could not look" but "we looked and it does
  not work" -- so it is now counted as `unrenderable` and blocks. Failing at
  the **base** version is a different fact, since the repository was already
  in that state and no pull request caused it; that stays a warning under
  **Not covered**, and the two are no longer reported through one sentence
  claiming both versions failed.

  The second half is why the strictest breakage produced the weakest verdict.
  The values-surface check that exists for exactly this case sat behind the
  early return a failed render took, so a chart with no schema had its stale
  keys named and blocked on, while a chart strict enough to hard-fail had
  them named nowhere. It never needed the render -- it reads `helm show`, not
  `helm template` -- and now runs either way, so the report carries both the
  error and the list of keys.

  Consequence worth knowing before upgrading: a pull request whose chart does
  not render at the new version was green and is now red. That is the point,
  and it is also the one behaviour change here.

  The triage prompt gains a rule with it, because a newly red class is a
  newly *modelled* class. The repair here is a key deleted or renamed, which
  the edit format cannot express, so the answer is an escalation naming the
  keys -- and the prompt now says so, along with the one wrong answer that
  would otherwise pass every check the applier makes: putting the version
  back. The old version is named in the gate report, so a revert corroborates
  cleanly and undoes the promotion instead of repairing it. The eval suite
  gains the 0.20.0 -> 0.25.1 bump as a case, and it is worth being exact about
  what that measured: `qwen/qwen3.8-27b` classifies it `escalate` with the rule
  and without it, so on this model the rule changed nothing. It stays for the
  failure it names, which this corpus contains the temptation for and does not
  reproduce; the levers in `docs/prompt-contract.md` exist for the smaller
  models, and this one is recorded as unproven rather than as a win.
- **A schema render that invited the mistake its own prompt forbade.** Since
  the restructure path shipped, one case has scored correct-but-noisy: the
  model produced the right migration for `spec.store` -> `spec.secretStoreRef`
  and also wrote out `kind: SecretStore`, a default the schema already
  applies. That was recorded as the cost of doing business.

  It was the evidence. `RenderSchema` printed
  `kind: string default=SecretStore` directly beside
  `name: string (required)`, which reads as two fields to fill; and the rule
  against it, forty lines above, named a category -- OPTIONAL -- that the
  render never printed, so the only way to know a field was optional was the
  absence of a marker. The render now says
  `kind: string (optional, unset means SecretStore)`: the same schema fact
  with the reason to write it removed, at the line where the decision is made.
  A required field keeps its default printed, because that one may have to be
  filled from the schema.

  Both schema-guided prompts are measurably shorter-answered for it, and the
  suite reaches **full pass 27/27** for the first time.
  `docs/prompt-contract.md` carries it as Lever 8.
- **A selector that matches on a label being absent no longer demands that
  label be present.** `selectorKeys` handed every `matchExpressions` key to
  `Inventory.Validate`, `NotIn` and `DoesNotExist` included. Those two select
  the clusters that do *not* carry the key, so on a fleet where the key is
  simply unused every cluster matches and the render is right -- and the gate
  refused to render at all, naming a label nothing was missing. The only cure
  was listing the key under `clustersExport.knownAbsentLabels`, which is worse
  than the symptom: that entry suppresses the stale-inventory check for the key
  everywhere, including the `In` selectors where a missing label really does
  shrink a render silently, so a workaround for one selector disarmed the check
  for the rest. `matchLabels`, `In` and `Exists` are still demanded, which is
  the case the check was built for.
- **`concurrency` is capped at 32.** The value comes out of
  `.gitops-gate.yaml` in the repository being gated, and every worker it asks
  for is a helm subprocess with a chart download and a temporary directory
  behind it, running in the operator's pod beside every other open pull
  request's render. Nothing bounded it. `concurrency: 5000` still parses --
  failing a pull request over a field with nothing to do with its diff is the
  wrong trade -- and is clamped where it is acted on. The clamp lives in
  `workers()` rather than `ParseConfig` because a `Config` built as a literal
  reaches the same semaphore, and a bound only the parser applied would be one
  the gate's own callers could skip.

### Removed

- **BREAKING: `projects` and `analysistemplates` leave the chart's
  ClusterRole.** Neither was ever read. Both arrived whole in the commit that
  extracted bosun from the repository it grew up in, so they were copied
  rather than reserved for a plan, and `git log -L` over that rule block
  returns the one commit to prove it.

  Breaking because narrowing a published ClusterRole is: an install that bound
  its own ServiceAccount to it and used either grant loses it on upgrade.
  Nothing in bosun does, at any setting.

  Doing it now rather than leaving them is the point. Widening that role is a
  patch and narrowing it is a breaking change, so a resource granted against a
  feature nobody has written books a breaking change for something that may
  never arrive. The same asymmetry is why `freights` and `analysisruns` stay:
  this is the release that reads them. `charts/bosun/templates/rbac.yaml`
  carries the rule, next to the list it governs.

## [0.25.0] - 2026-08-29

### Security

A round of remediation against an OWASP-framed source review. No finding was
critical, and the write path held throughout: every scalar edit still re-checks
the deny-list, the allow-list, the scope and the `from` value independently of
the model. What follows is the edges the design never claimed.

- **Internal address space is closed to the agent, whatever the deny-list
  says.** `egress` was an open-by-default list of host strings, and a host
  string is not an address: `169.254.169.254` is also `http://2852039166/`,
  also `[::ffff:a9fe:a9fe]`, and also any name whose owner repoints it a moment
  after the string was checked. Loopback, link-local, RFC1918, CGNAT and the
  IPv6 equivalents are now in a `DefaultDenyNetworks` list no configuration
  removes, checked on the dialler where the address is finally known, so
  rebinding and alternate spellings are caught after resolution rather than
  guessed at beforehand.

  **This is a breaking default.** A deployment whose chart repository,
  registry or proxy sits on a private address is refused until that network is
  named in `triage.egressAllowPrivate` (`EGRESS_ALLOW_PRIVATE`), and the
  refusal says which network to name. Three gaps stay open and are stated in
  the package doc rather than implied away: helm is a subprocess and dials on
  its own, a proxy means the address checked is the proxy's, and a caller
  supplying its own RoundTripper keeps its own dialler.

- **The edit scope is read from the pull request, not asserted by the
  caller.** `policy.Scope` came from the promotion body's `files`, and an empty
  array disabled scope narrowing altogether, since it is only enforced when
  non-empty. It is now derived from `git diff --name-only` against the fetched
  base in the checkout the agent already has. A promotion whose base branch
  cannot be read escalates without writing rather than proceeding unscoped, and
  an empty diff is an error rather than a licence. The body still carries
  `files`; nothing trusts it.

- **Upstream lookups and the schema probe refuse an artifact the repository
  does not name.** `Artifact`, `From` and `To` reached the resolver and
  `helm template` unvalidated, so a caller could aim either at
  `169.254.169.254` or `argocd-server.argocd.svc` and read the result off the
  pull request comment. Both sinks now require the checkout itself to name the
  host the artifact resolves to. A reference with no host of its own, `redis`
  and the like, still resolves by convention rather than by the caller's
  choice.

- **Every path the gate joins to a checkout goes through `safepath`.** Six
  sites, including head-controlled `helm.valueFiles`, joined with
  `filepath.Join` and gated on nothing but an existence check, so
  `../../../../etc/….yaml` resolved outside the checkout and, if it parsed as a
  mapping, merged into values and rendered into the public comment.
  `readGlobs` now asks containment of each match rather than of the pattern.
  A value file that escapes is an error where it used to be a silent skip.

- **The repair contract travels in a machine block, not in prose.** The agent
  regex-parsed the human report to build the migration contract, and part of
  that report was rendered object names. A chart could name an object so the
  bullet reproduced the parser's line exactly and forge a `Dropped{…}` the
  deterministic migrator would then execute. The gate emits
  `<!-- gitops-gate:dropped … -->`, `migrate.ParseReport` reads that and never
  falls back to prose when the marker is present, and object names, values and
  paths are escaped where the report renders them. `metadata.name` is
  deliberately not validated against RFC1123: Kubernetes has no single name
  rule, and a subdomain check would drop objects real clusters accept.

- **Model prose cannot forge a published marker.** Summary, Reasoning and
  Notes are escaped at `<!--`/`-->` and length-bounded before they reach a pull
  request comment. Seven markers were forgeable, and the sharpest was not the
  report marker: `gateservice` returns early when it finds its own head stamp,
  so echoed text could have made the gate silently skip a verdict. The
  delimiters are escaped rather than today's markers blocklisted, because a
  blocklist is one new marker away from being wrong. File-derived text in
  folded diffs and table cells is still unescaped.

- **Gate subprocesses can be cancelled.** `helm`, `kubectl` and `kubeconform`
  ran under `exec.Command`, so `gateservice`'s context deadline cancelled
  nothing and a stalled render held a chart-diff slot indefinitely. One choke
  point now runs them under `exec.CommandContext` with a three-minute
  per-invocation bound, and the context threads through `Render`, `ChartDiff`,
  `ValidateManifests`, `Assemble` and `ExportClusters`, which all take a
  leading `context.Context`.

- **A chart, repository or version beginning with `-` is refused.** Helm's
  parser reads an interspersed `-…` as a flag wherever it appears, and
  `--post-renderer` makes helm exec a binary. The tokens that arrive from a
  promotion payload are checked before they land in argv.

- **The git push credential left the command line.** It was a positional
  `https://x-access-token:{tok}@…` remote, visible in `/proc/<pid>/cmdline` and
  to `ps` for the push's lifetime. It now travels as a URL-scoped
  `http.<remote>.extraHeader` supplied through `GIT_CONFIG_*` in the
  environment, on both the GitHub and Gitea providers. Not `-c`, which would
  put it straight back into argv.

- **Credentials can be mounted as files.** `GIT_TOKEN`,
  `GITHUB_APP_PRIVATE_KEY`, `LLM_API_KEY`, `ARGOCD_TOKEN` and
  `PROMOTION_TOKEN` each take a `_FILE` variant holding a path. Trailing
  newline trimmed, so a Secret mount works as-is; setting both forms for one
  credential is a start-up error, as is a path that cannot be read or a file
  that is empty. The plain form still works, so an upgrade is not forced.

- **The supply chain is pinned, verified and signed.** Every third-party
  Action moves to a full commit SHA with a version comment; helm and
  kubeconform are checked against per-architecture sha256 digests before
  extraction, in both Dockerfiles and in CI; base images are digest-pinned and
  alpine moves 3.21 to 3.24; images and charts are signed with keyless cosign
  and publish provenance and SBOM attestations; `image.yaml` gains a
  least-privilege top-level `permissions` block. `.github/dependabot.yml`
  covers github-actions, gomod, docker and npm, and `govulncheck ./...` runs in
  CI.

- **`go.mod` moves to Go 1.26.6.** Five standard-library advisories were
  reachable from this code on 1.26.5 and are fixed there, so this is what makes
  the new `govulncheck` step pass. `GOTOOLCHAIN` is `auto`, so the dev shell
  fetches it rather than refusing to build.

- **The chart narrows what may reach the agent, and what the agent may
  resolve.** `charts/bosun` 0.24.0 adds `networkPolicy.kargoPodSelector` so the
  triage hook can be restricted to the Kargo controller rather than to every
  workload in its namespace, `networkPolicy.egress.dnsPodSelector` so DNS
  egress can be narrowed to the resolver pods, and the two blocks missing from
  the `allowPublicHTTPS` except list, `169.254.0.0/16` and `100.64.0.0/10`.
  Both selectors default to today's behaviour. `metrics.serviceMonitor` now
  requires a `namespace`, which is breaking for the installs that turned it on;
  see the chart's own changelog for why an optional value would have left the
  hole open. The baseline `pods`/`events`/`apps` grants move inside
  `liveReads.enabled`, where the only code that reads them lives, and both
  schemas set top-level `additionalProperties: false` so a misspelled
  `livereads` is a render error rather than a silently defaulted feature.

- **The safety model says what it enforces.** Its FQDN egress guarantee held
  only on the Cilium flavor: `fqdns` and `fqdnPatterns` render inside
  `{{- if eq $np.flavor "cilium" }}`, so on the default `standard` flavor they
  were silently ignored while `allowPublicHTTPS` permitted any public host on
  443. The row is replaced by three that each name what is actually enforced,
  plus a section on what the NetworkPolicy can and cannot do per CNI and a
  section stating that `helm template` is a subprocess no Go code contains.
  [ADR 0011](adr/0011-public-is-open-internal-is-closed.md) records the egress
  decision.

- **`hack/portability-test.sh` checks CI's tool pins too.** Its own comment in
  `ci.yaml` claimed the versions there were already checked against
  `flake.nix`; `check_pin` read the Dockerfile and stopped, so CI's copy was
  asserted by nothing. A CI that lints with a helm no image ships is the same
  locally-true, globally-wrong verdict from the other direction.

- **Not closed, and said rather than implied.** `helm template` runs as a
  subprocess and Helm strips only `env` and `expandenv` from sprig, so a chart
  committed to a pull request can call `{{ getHostByName "…" }}` and the
  address it resolves renders into the published report. `gate/template.go`
  removes that function from the in-process ApplicationSet renderer only. A Go
  process cannot portably put its child in a network namespace or behind a
  captive resolver, so containment for this is a NetworkPolicy question, and
  `networkPolicy.egress.dnsPodSelector` is as far as a chart can take it: the
  cluster resolver still forwards what it cannot answer. Which chart helm
  renders is bounded, because the schema probe refuses an artifact the checkout
  does not name; what that chart does once rendered is not.

### Added

- **Node 22 in the dev shell.** The site is a required check and its build was
  not runnable from `nix develop`, so `npm run build` and `npm run check:links`
  only ever ran on the CI runner. `npm run og` regenerates a committed PNG, so
  it wanted a pinned Node too. `CONTRIBUTING.md` lists both under Checks now.

### Changed

- **Every doc and code comment in the repository is edited for one voice.** No
  em dashes, no emphasis capitals, no filler adverbs, no story framing, and an
  aphorism used once rather than on eight pages. This is prose only: `git diff`
  on `*.go` touches comment lines and nothing else, and the report strings the
  agent parses back are unchanged. `adr/` and the changelogs keep their own
  voice.
- **The site's own source gets the voice pass the docs already had.** The
  landing page, the Hero and LoopDiagram components, the Astro config, the sync
  and link-check scripts, the stylesheets, the Helm template comments, the
  chart schema descriptions, the demo narration in `local/scripts/`, and
  `flake.nix`. The first pass matched `*.md` and `#`/`//` comments, so `.mdx`,
  `.astro`, `.mjs`, `.css`, `.tpl` and Helm's `{{- /* ... */ -}}` were never
  looked at.

### Removed

- **BREAKING: the standalone `gitops-gate` CLI and its image.**
  [ADR 0010](adr/0010-the-cli-goes-too.md) is the argument. The gate ships in
  one binary -- the agent's -- and `gate/cmd/gitops-gate`, `gate/Dockerfile`,
  `clusters export`, the checked-in inventory snapshot, and the image
  workflow's scope and retag jobs are gone with it. The published
  `gitops-gate` images stay in the registry as history; nothing publishes
  over them and nothing new arrives.

  `.gitops-gate.yaml` loses the keys only the CLI read: `clusters` and
  `clustersExport.ignoreKeys` are now rejected at parse, by name. Delete the
  line and nothing else changes. `clustersExport.knownAbsentLabels` stays,
  unrenamed, because it was always render configuration and renaming a key
  every live install sets would break them for tidiness.

  The gate's own changelog retired with the CLI. Its entries are folded into
  this file under the releases that first shipped them -- 0.2.0, 0.7.0,
  0.8.0, 0.9.1 through 0.9.3, and 0.16.0, checked by tag ancestry -- because
  the gate never had a version line of its own: the numbers its images
  carried were the agent's `appVersion`, stamped on at release.

### Fixed

- **The quickstart documented `gate.mode: cluster`, removed in 0.22.0.** It is
  not in `values.schema.json`, so the values file that page hands you fails at
  install time naming the key. The same page linked to an appendix on CI mode
  that is now the CLI appendix.
- **`docs/git-providers.md` warned that a repeated method count goes stale and
  then stated one.** The count is right (ten, checked against
  `gitprovider/provider.go`); the contradiction is gone.
- **`site/src/authored/reference/configuration.md` carried a dangling `own.`**
  from an earlier edit, with the word missing from the sentence above it.
- **`agent/comment.go` had two doc comments for `renderMigration`**, the second
  restating and contradicting the first.
- **The landing page claimed eight ADRs and five levers.** There are nine ADRs
  in `adr/`, and `docs/prompt-contract.md` has six levers. Both counts were
  stale rather than wrong when written.
- **The landing page's safety strip described the model's proposal as an edit
  set.** It has been able to return a complete migrated document since
  [ADR 0007](adr/0007-structure-from-the-schema-data-from-the-document.md); the
  page now says so.
- **The link-preview card and the landing hero disagreed.** Both now lead on
  the same finding, and `og.png` is regenerated from the pinned Node.
- **`charts/bosun/CHANGELOG.md` was missing twelve published versions.**
  0.9.2, 0.9.3, 0.16.1, 0.17.0 through 0.17.2 and 0.18.0 through 0.18.5 are all
  in the registry and had no entry. Reconstructed from git: each version's
  commit, date and chart diff, so the entries say what changed in the chart
  rather than what changed that release. Every version ghcr serves is now
  documented.

  Four headings describe versions that never published, and the file now says
  which and why. 0.11.0 and 0.15.1 were bumped on a branch and bumped again
  before merging, so 0.12.0 and 0.15.2 are what shipped. Everything below
  0.9.2 predates the move from tag-triggered to push-triggered publishing.
- **`charts/kargo-pipelines/CHANGELOG.md` filed 0.1.0 under `[Unreleased]`.**
  `f6ef4de` created the chart at `version: 0.1.0` and wrote that section;
  `86af473` cut 0.1.1 and left the earlier one labelled. 0.1.0 has been in the
  registry since 2026-08-23, so the file said "unreleased" about a published
  version. Its two `0.2.x` dates were also a day ahead, written from UTC where
  every other entry in the file uses local time.

## [0.23.0] - 2026-08-28

Seven defects from an external security review, and the checks that pin them.

### Security

- **Path containment is a filesystem test, not a string test.** `edits`, the
  agent's prompt builder and the migration walker each held their own lexical
  check; a tracked symlink at a permitted path passed all three and resolved
  wherever it pointed — a Secret mounted in the pod, read into a prompt that
  gets published, or a `.github/**` file under a write the deny-list exists to
  refuse. They now share `safepath.Resolve`, which rejects any path crossing a
  symbolic link rather than resolving it: resolving would keep the escape out of
  the filesystem and not out of the repository.

- **An empty `from` no longer skips the value check.** The comparison was
  conditional, so `"from": ""` overwrote any scalar in any permitted file while
  the schema, the prompt and the package documentation all promised the current
  value was checked. It is compared unconditionally; an empty scalar matches
  `""` and nothing else.

- **A scalar edit lands on the token it names.** The rewrite replaced the first
  text on the line resembling the old value, so an edit to `b` in
  `{a: old, b: old}` rewrote `a`, and one to the value in `version: version`
  rewrote the key. It is anchored to the YAML node's column, preserves the
  quoting style through the swap, and refuses block scalars rather than
  guessing.

- **A verdict names the commit that was inspected.** Checkouts clone a branch
  while verdicts are keyed to the head SHA read moments earlier, so a push in
  that window had the gate render one commit and publish the answer against
  another. `gitprovider.EnsureHead` fetches the exact commit where the host
  serves it and aborts the run where it does not.

- **The attempt cap fails closed.** Both repair paths pushed first and wrote the
  attempt label after, logging failures — so a token with push permission and no
  permission to label repaired, failed to record it, and counted zero attempts
  forever. The label is reserved before the push and the push is refused without
  it. The Gitea label lookup is paged, so an existing label past the hundredth
  no longer breaks it.

- **GitHub Enterprise pushes where it reads.** `APIBase` supported Enterprise
  while the push remote was hardcoded to `github.com`, sending the fix and a
  live installation token at whatever holds that `owner/repo` on the public
  host. The remote is built from `GIT_REPO_URL`.

- **`POST /v1/promotion-opened` can require a bearer token.** Set
  `promotionAuth.existingSecret` on the bosun chart and `triage.authorization`
  on kargo-pipelines. Opt-in, so an upgrade does not stop answering Kargo;
  unset, the pod logs a warning at every start-up. The endpoint's payload names
  the pull request the agent edits and the files it reads into a published
  prompt, and a NetworkPolicy admits a whole namespace.

### Fixed

- **A new promotion for a busy pull request is run, not dropped.** Deduplication
  keyed on the pull-request number alone answered `202 already in progress` and
  discarded the payload. Retries are identified by `PromotionID`; a different
  promotion is held as pending and run once, newest wins.

### Changed

- **BREAKING: `GIT_API_BASE` / `git.apiBase` is required for Gitea.** There is
  no public instance to default to, so an empty value was a pod that started
  healthy and could not read a pull request.

- **BREAKING: an invalid boolean is a configuration error.** `EXPLAIN_GREEN=treu`
  read as `false`, so a setting somebody deliberately turned on was silently off.

- **BREAKING: durations must be positive.** `GATE_POLL=0` is not a faster poll;
  it spun the sweep against the git host's API as fast as it answered.

- **`MAX_CONCURRENT_TRIAGE` / `maxConcurrentTriage`** bounds simultaneous
  triages, default 4. Each is a clone, a helm render and a model call.

## [0.22.0] - 2026-08-28

The gate runs in one place and reads its inventory from one source.
[ADR 0009](adr/0009-one-gate-one-inventory.md) is the argument; this is what
moved.

### Removed

- **BREAKING: `GATE_MODE` / `gate.mode`.** The agent is the gate. The CI
  placement kept its own half of the system alive — the verdict travelling
  through a pull-request comment the agent scraped back off the host, the
  `reportAuthor` trust check that existed because a comment is forgeable, the
  `GATE_WAIT` poll loop, and `CheckMissing` treated as pending — all carried by
  every reader of this codebase for a path the default install did not take.

  Gone with it: `GATE_WAIT` / `gate.wait`, `GATE_REPORT_AUTHOR` /
  `gate.reportAuthor`, the `waitForGate` and `gateReport` paths in the agent,
  and the `ci/` adapter directory with `gate/docs/adding-a-ci-provider.md`. Two
  of the three adapters were written against documentation and never run.

  Branch protection does not change: the agent posts the same `gate.checkName`
  status. A repository taking **fork** pull requests now has only `gate.forkPRs`
  to decide with, since the render runs in-cluster.

- **BREAKING: `INVENTORY_SOURCE` / `gate.inventorySource`.** The inventory comes
  from `GET /api/v1/clusters` on the ArgoCD API, and from nowhere else.

  Two decoders for the same facts was a defect waiting for a key one trimmed and
  the other did not — which is not hypothetical: running both against a real
  ArgoCD is what found that the API strips ArgoCD's own `managed-by` annotation
  while the Secrets carry it. A selector matches on those maps, so a
  disagreement is a different targeting verdict from the same cluster.

- **The chart's Role over Secrets in the ArgoCD namespace.** Nothing this chart
  renders now grants `get`/`list` on Secrets. That grant could not be made
  smaller — RBAC has no predicate for "the labels but not the data",
  `resourceNames` does not apply to `list`, and the label selector the gate sent
  was a filter the apiserver applied *after* authorising — and leaving it as the
  default meant every install that did not read the values file carefully got
  it.

- **`make scenarios` and `make demo-forged` in the proving ground.** The replay
  fed the agent the recorded gate report from each incident in `evals/` as a
  comment; the gate is in-process now and reads no such comment. The fixtures
  are unchanged and still scored by `go test ./evals/...`. The forged-report act
  had `gate.reportAuthor` as its whole subject.

### Changed

- **BREAKING: `ARGOCD_BASE_URL` and `ARGOCD_TOKEN` are required, and
  `gate.argocd.baseURL` has no chart default.** A plausible-but-wrong address
  does not fail where an operator would look for it — the connection hangs for
  the full HTTP timeout and the pod dies at start-up saying argocd-server is
  unreachable. A required value fails at `helm upgrade`, naming the value.

- **The proving ground gates through the agent.** Every demo act changes the
  sample repository, opens a pull request and waits for the verdict the agent
  publishes from its sweep — no `gate-run.sh` standing in for a CI system, and
  no seeded status or comment. `make demo-cluster-gate` no longer flips a mode;
  it asserts the three properties directly. `30-kit.sh` mints an ArgoCD account
  token with `clusters, get` the way the chart README asks an operator to.

## [0.21.0] - 2026-08-28

### Fixed

- **The NetworkPolicy value that reaches argocd-server named the wrong port,
  and its comment argued for the wrong one.** `gate.argocd.port`, added in
  0.20.0, is written into the chart's egress rule to the ArgoCD namespace. A
  NetworkPolicy matches the destination port of the packet, and a ClusterIP is
  DNAT'd to the backend pod's port *before* policy is evaluated — so that
  value has to be argocd-server's container port, and it defaulted to `443`
  while its comment read as though the port belonged to `baseURL`.

  Setting the two consistently — `baseURL` and the port both on 80 — is what
  the comment invited, and it renders clean, passes `helm lint`, passes the
  values schema, and then drops every packet. Nothing errors at either end:
  the connection hangs for the full HTTP timeout and the pod dies at start-up
  saying argocd-server is unreachable, which is true and says nothing about
  why. That is a production outage this repository had already documented the
  cause of in three other places.

  The value is now **`gate.argocd.podPort`, defaulting to `8080`** — a
  breaking values change, taken because `gate.argocd` is
  `additionalProperties: false`, so a values file carrying the old `port:`
  fails the upgrade with a named error instead of silently switching ports.
  See the [chart CHANGELOG](charts/bosun/CHANGELOG.md) for the one-line
  migration.

### Changed

- **The start-up failure now names the port trap.** `gate.mode is cluster and
  the inventory could not be read` was the operator's only signal, and with
  `inventorySource: argocd` its remedy listed everything except the thing that
  is nearly always wrong. It now says to check the NetworkPolicy ports at both
  ends — bosun's egress and argocd-server's ingress — and that the port is the
  pod's, not the URL's. It also names `gate.argocd.caSecret`, the value that
  exists, rather than `gate.argocd.caFile`, which never did.

## [0.20.0] - 2026-08-28

### Added

- **`gate.inventorySource: argocd`** — cluster mode can read the live
  inventory from `GET /api/v1/clusters` on the ArgoCD API instead of from the
  ArgoCD cluster Secrets, and **when it does, the chart no longer creates the
  namespaced Role on the ArgoCD namespace.** `secrets` remains the default and
  is unchanged.

  The grant it removes is the one this project has been honest about being
  unable to shrink. The gate reads four fields — name, server, labels,
  annotations — and Kubernetes RBAC has no predicate for "the labels but not
  the data": there are no deny rules, `resourceNames` does not apply to
  `list`, and the label selector the gate sends is a filter the apiserver
  applies *after* authorising. A token holding that Role can drop the selector
  and read `argocd-secret` and every repository credential beside it. ArgoCD's
  own API draws the line RBAC cannot.

  It is a trade rather than a win, and `values.yaml` states both halves: an
  ArgoCD account token to mint and rotate (`clusters, get` and nothing else, or
  it is a bigger credential than the read it replaced), a component that can be
  down on its own, and a second TLS story — argocd-server serves its own
  certificate rather than the one the kubelet mounts into every pod. Hence a
  value an operator sets rather than a new default.

### Fixed

- **The same cluster produced two different inventories.** Reading the ArgoCD
  API and the cluster Secrets side by side against a real ArgoCD found that
  the API strips ArgoCD's own `managed-by` annotation while the Secrets carry
  it — so the gate's targeting verdict would have depended on which source an
  operator configured. It is now dropped from both, at the single
  normalisation point the two sources share.

  Dropped rather than synthesised back on the ArgoCD side, deliberately:
  re-adding it would assert a fact the API did not report, for clusters that
  may never have carried it. The cost is stated where it is paid —
  ApplicationSet's cluster generator templates against the Secret's
  annotations verbatim, so a template naming that annotation would see it in
  production and will not see it here.

  Found by `cluster/argocd_live_manual_test.go`, which exists because the
  fixture-based test could not have found it: both sides of a fixture are
  written by the same person on the same afternoon, and a field ArgoCD
  populates differently appears in neither.

## [0.19.0] - 2026-08-28

Documentation only. No behaviour of the agent, the gate or the charts changed
in this release — the version moves because the chart's `appVersion` is what
cuts a tag, and the site should ship as something you can point at.

### Added

- **A documentation site at [bosun.integratn.io](https://bosun.integratn.io).**
  Astro and Starlight, built by [`site/`](site) and deployed to GitHub Pages.
  Searchable, cross-linked, and themed from `docs/avatar/bosun.svg` — the site
  and the mark are the same palette because they were the same decision.

  **The markdown in this repository is still the source of truth.** `docs/`,
  `adr/`, `gate/docs/`, the chart and CI READMEs stay exactly where they are and
  stay readable on GitHub; `site/scripts/sync-docs.mjs` copies them in at build
  time and `site/src/content/docs/` is generated and gitignored. The alternative
  — moving the docs into a website tree — breaks every relative link in the
  README and in code comments, and makes browsing them on GitHub worse for the
  people most likely to be reading them.

  The interesting half is the link rewriting. A doc linking to
  `../adr/0008-the-gate-moves-in-cluster.md` has to work on GitHub *and*
  resolve to `/decisions/0008-the-gate-moves-in-cluster/` on the site, so every
  relative link is resolved against its own source directory and rewritten — to
  a site route if that file is published, to a GitHub URL if it is not. Both
  halves of that decision fail silently when wrong, so
  `site/scripts/check-links.mjs` resolves every internal link against the pages
  that actually built and every rewritten GitHub link against the working tree.
  It runs on every pull request.

- **Four pages that did not exist.** [Quickstart](https://bosun.integratn.io/start/quickstart/)
  (two tracks: watch the whole loop run on a disposable cluster, or gate a real
  repository), [Configuration](https://bosun.integratn.io/reference/configuration/)
  (every chart value, the environment variable it becomes, and its default),
  [Troubleshooting](https://bosun.integratn.io/reference/troubleshooting/)
  (organised by what you observe, including the several failures whose symptom
  is that nothing happens at all), and an
  [FAQ](https://bosun.integratn.io/reference/faq/) answering the questions that
  come up before installing rather than after.

- **A `docs` job in CI.** The site builds from this repository's markdown, so a
  doc that renames a heading or moves a file breaks the *site* — previously only
  at deploy time, on main, after review. It now fails on the pull request.

### Fixed

- **`docs/the-loop.md` still said the gate runs in CI.** It had said so since
  before [ADR 0008](adr/0008-the-gate-moves-in-cluster.md) moved the gate
  in-cluster and made that the default. The cast table called it
  "(`gate/`, in CI)" and section 2 opened "CI runs the gate twice", so the one
  document most likely to be read first described a shape onboarding had
  stopped recommending. Where the gate runs is now stated once, as the value it
  is, and the walkthrough no longer claims either answer.

- **`docs/llm-providers.md` cited "the nine-case eval".** That set is ten cases
  now. The 9B measurement it reports is still accurate for the set as it stood
  when it was taken, which is what it says, with a pointer to the current
  numbers.

### Changed

- **`hack/check_links.py` skips `site/`.** The rule there is the opposite one:
  authored site pages link by absolute route (`/start/quickstart/`), which that
  checker exists to reject. They are covered by `site/scripts/check-links.mjs`
  instead, which is stricter — it knows which pages exist.

- **`hack/portability-test.sh` no longer greps `node_modules`.** The
  host-environment leak scan reads every `*.json` in the tree, and `site/`
  brought ~40MB of other people's syntax themes with it. One of them matched,
  which is not this project inheriting anyone's cluster.

- **The chart's `home` is the documentation site** rather than the source
  repository, which is what `sources` is for.

## [0.18.5] - 2026-08-25

### Fixed

- **A pushed migration left its own verdict on the wrong commit.** When the
  repair path pushes, the branch head moves — but the agent went on holding the
  pre-push SHA, so the `bosun` status it wrote afterwards landed on a commit
  that was no longer the head. The head ended up carrying a green gate and no
  verdict at all, and because `bosun` is a required check, that pull request
  could never go green again.

  The failure mode is the one this service exists to find: silence. Nothing
  errored, nothing retried, and the pull request simply sat there looking like
  the agent had died mid-run. Observed on two independent promotions —
  external-secrets under 0.18.3 and again under 0.18.4 — before it was traced.

  `PushFix` now advances `pr.HeadSHA` to the commit it just created, which is
  the branch head by definition, so every status written afterwards lands on
  it. The provider fake records the target SHA too: it did not, which is
  exactly why no test could see this. A test asserting only state and
  description passes happily while production writes to a superseded commit.

## [0.18.4] - 2026-08-25

### Fixed

- **A chart repository written without a scheme skipped the egress deny-list.**
  The rule that turns a chart reference into the host it will actually reach
  lived privately in two packages, and they disagreed on exactly the reference
  this cluster uses: `ghcr.io/akuity/kargo-charts`. One copy returned nothing
  for a reference with no `://`, so both the deny check and the outbound
  request log were skipped — and helm was handed it as `oci://ghcr.io/...`
  moments later. The other copy would hand a bare chart name to the deny check
  as though it were a hostname.

  There is one owner now, `egress.HostOf`, using the rule a registry uses: the
  first element is a host if it contains a dot or is `localhost`. A bare chart
  name reaches nothing on its own and is no longer checked as if it did.

- **A fork pull request was gated on one path and refused on the other.** The
  sweep refused them, but the sweep is not the only way in — the triage calls
  `Ensure` directly on a network-triggered promotion, so a fork pull request
  the sweep had not reached yet was rendered with helm, in-cluster, over
  content controlled from outside the repository. The refusal moved onto the
  path that does the work, so both ways in answer the same.

- **The prompt read files without checking they were in the checkout.** The
  file list arrives in the request body, and this process holds the git token,
  the LLM key and the App private key. What it reads goes into a prompt, and a
  prompt is published. The write path has made this check since it was
  written; the read path now makes it too.

### Changed

- The agent's root package is split into `agent/`, `gateservice/`, `prompt/`
  and `supervisor/`. The three shipped model prompts were unreachable from the
  eval suite, which had been reaching them through a regex scrape of the Go
  source — and had already let one shipped prompt go unmeasured. No
  configuration key or environment variable changed.

## [0.18.3] - 2026-08-25

### Fixed

- **A merged pull request is not a lost one.** `promotion_without_pr` looked
  for a promotion whose branch has no *open* pull request — but "not open"
  covers merged, and the seconds between a merge and Kargo noticing it are
  exactly when a promotion is doing the right thing. Reporting that window
  would have been a false alarm on every successful promotion, which is the
  fastest possible way to teach someone to ignore a supervisor.

  Caught by running it: the first live sweep after deploying flagged bosun's
  own promotion three minutes after its pull request merged. There is now a
  15-minute grace, which costs nothing real — the genuinely stranded ones
  observed had been running for hours.

## [0.18.2] - 2026-08-25

### Fixed

- **Removed a metric that could never fire.** `freight_never_promoted` was
  declared as a finding kind and never implemented, so it exported a permanent
  zero — a series no alert rule could trip and no graph could explain, which is
  a monitor claiming to watch something it does not. A test now asserts that
  every exported kind has a detector that can actually produce it.

## [0.18.1] - 2026-08-25

### Fixed

- **A finished verification is a different situation from a slow one, and the
  remedy is a different command.** 0.18.0 reported both as "has been verifying
  for N", which is wrong for one that already failed: it is not verifying, it
  is over. Kargo does not re-run it — that freight has been verified and the
  answer was no — so the Stage sits `Ready=False` forever, declines every
  promotion behind it, and every Application stays Synced and Healthy on the
  version it already had.

  It is now **blocking**, says so, and carries
  `kargo.akuity.io/reverify={"id":"…"}` with the id filled in. Learned the hard
  way an hour after 0.18.0 shipped: the NetworkPolicy that had been dropping
  the AnalysisRun's Prometheus queries was fixed, and all three Stages stayed
  exactly where they were. **Fixing the cause does nothing on its own.** The id
  lives at `status.freightHistory[0].verificationHistory[0].id`, three levels
  deeper than anyone looks, so a remedy without it is a paragraph rather than a
  command.

## [0.18.0] - 2026-08-25

### Added

- **A supervisor for the promotion pipeline.** The gate answers a pull request
  that exists; this answers whether the pull requests that *should* exist are
  being opened at all. Nothing about a promotion that never happened produces
  an event, so it looks on a timer.

  It finds: a Stage whose promotion ended without delivering and **will never
  retry** (a terminal promotion is final, and auto-promotion does not re-run
  one); a Warehouse that stopped discovering or has missed two of its own
  intervals; a verification holding a Stage's queue; a tracked `yaml-update`
  key the target file does not have, which writes nothing and reports success;
  a promotion running against a closed pull request; and duplicate promotion
  pull requests where only the newest can merge.

  **On its first live sweep against a real cluster it found three Stages that
  had been promoting nothing for three days** — two of them because
  `allow-controller-egress` permits `0.0.0.0/0:443` minus RFC1918 and
  Prometheus is a ClusterIP, so every `verify.apps` query had been dropped
  since the rule was written. A failed AnalysisRun does not fail a promotion:
  the Stage goes `Ready=False` and quietly stops, and every Application stays
  Synced and Healthy on the version it already had.

  **Every finding carries the exact command**, because none of them are
  guessable: `kargo.akuity.io/abort=true` is silently ignored where the request
  object works; a Warehouse refresh will never re-run a promotion that reached
  a terminal phase; a hand-written Promotion needs `generateName` *without* a
  trailing dot or the webhook rejects it on RFC1123.

  Read-only — three LISTs and a shallow clone, using the Kargo read the
  ClusterRole already grants. No new permission, and no write verb.

- **`/metrics` now serves something.** `metrics.serviceMonitor` has existed as
  a values knob scraping a 404. It now carries findings by kind and severity,
  how long each has held, what the sweep actually read, and a sweep timestamp.
  Alert on the timestamp's *absence* as well as on findings: a supervisor whose
  subject is silent failure has to be able to fail loudly itself. Rules in
  [`docs/supervisor.md`](docs/supervisor.md).

- **`/pipeline`** serves the report as markdown. Both endpoints answer `503`
  before the first sweep, deliberately — a scraper reading zeroes from a
  supervisor that has not looked would record "nothing is wrong" as a
  measurement.

### Fixed

- **`firstDocument` treated a leading separator as a terminator.** A file
  opening with a comment block and `---` parsed as "document one is five
  comments", which made every key in it look absent. Caught by the first live
  sweep, which reported ten Deployments as missing a key they all had.

## [0.17.2] - 2026-08-25

### Fixed

- **One report per pull request, actually.** 0.17.0 looked for its own report
  by comparing a comment's author to `Name()` — which is the *provider's* name
  (`github`), never the account. It never matched, so every run posted a new
  report and a repaired pull request still carried two twenty-thousand-character
  comments: the exact thing the change was meant to end.

  It shipped because the fake agreed with the mistake — it wrote comments
  authored by its own `Name()`, so the match succeeded in every test and failed
  on every real pull request. The fake now records an account, and the agent
  finds its report by the verdict stamp only it writes. A report posted by a CI
  adapter carries no stamp, is correctly not ours to rewrite, and gets one
  posted beside it.

## [0.17.1] - 2026-08-25

### Fixed

- **A failing `addons-gate` status now says why it failed.** It counted only
  targeting and source changes, so a pull request blocked for any other reason
  — an apiVersion that moved, settings the bump stops reading — got
  `0 targeting change(s), 0 other source change(s)` beside a red cross. The
  most-read surface on the pull request reported nothing changed, on the one
  occasion it most needed to say what did. It now carries the same verdict the
  report leads with. Found immediately, on the first live red after 0.17.0.

## [0.17.0] - 2026-08-25

### Added

- **The gate finds settings a bump stops reading.** Helm ignores a value it
  does not recognise rather than failing on it, so a chart that renames or
  removes a key takes the setting with it and renders perfectly — the values
  file did not change, the chart did. Measured on kyverno 3.2.8 → 3.9.0: 48 of
  the 77 values the consuming repository sets are no longer declared, six of
  them keys Kargo rewrites on every image promotion. That bump gated **green**
  before this.

  The chart's declared surface is read from its own `values.yaml` and, when
  present, a helm-docs README table. Absence only counts when the OLD version's
  surface accounted for at least 90% of what the repository already sets —
  below that, a chart we cannot read cannot prove an absence, and the check
  says nothing. Blocking, and printed above the resource diffs, because it is
  the finding with no other symptom.

  Measured for false positives rather than hoped: authentik 2025.12.4 →
  2026.2.3, trivy-operator 0.35.0 → 0.36.0 and kube-prometheus-stack 88.5.3 →
  88.5.4 all report zero.

- **The report leads with its verdict**, red or green, naming what blocks and
  how much of it — and carries a machine-readable breakdown of why, so an agent
  reading it does not have to infer that from prose written for a person. CI
  adapters that post the report verbatim carry it for free.

### Changed

- **One gate report per pull request, rewritten in place**, carrying the
  verdicts that came before it. Posting a fresh report per head commit left a
  repaired pull request with two twenty-thousand-character comments that
  differed only in a verdict neither stated. Editing alone would have deleted
  the failed pass, so the body keeps a bounded history of what each head was
  judged to be.

- **A red with no repository-side fix is answered without a model.** The gate
  blocks when an object the CHART renders moves apiVersion, which is right —
  somebody should look — but there is no edit to propose. Asking a model to
  explain that produced a paragraph restating the report with the one useful
  sentence buried in it.

- **Comments no longer carry an identity header.** Authenticating as a GitHub
  App puts the name and avatar above every comment; a bold "⚓ Bosun" under
  that was the agent introducing itself twice. The footer still records whether
  a model was involved, which the host cannot supply.

- **The attempt counter appears only on a retry.** "(attempt 1 of 2)" on every
  comment described a sequence that had not happened. The cap itself is
  unchanged — the label remains its only memory across a restart.

- **The migrated-file table is collapsed.** Twenty-seven rows restating the
  commit's own file list pushed the live-cluster facts off the bottom, and
  those are the part only an in-cluster agent can supply.

### Deprecated

- `branding.mark` is ignored. Still accepted so no upgrade fails on a values
  file that sets it.

## [0.16.1] - 2026-08-25

### Fixed

- **A writable `/tmp`, which cluster mode needs and the pod did not have.**
  `readOnlyRootFilesystem: true` with `/work` as the only writable mount meant
  chart-diff's `os.CreateTemp("", "inline-*.yaml")` failed EROFS for any
  Application carrying inline values. The gate does not treat that as fatal —
  it reports the Application as *"could not render at both versions, so its
  resource changes are NOT covered"* — so the effect was **silently reduced
  coverage on every version bump**, not an error. CI mode never showed it
  because the runner supplied `/tmp`. Found on the first real bumps after a
  live CI-to-cluster migration.

  No Go change; the version moves only to keep chart and appVersion in
  lockstep, which is what lets a consumer leave `image.tag` unset.

## [0.16.0] - 2026-08-24

### Changed

- **The agent is the gate.** `gate.mode: cluster` — the new default — runs
  the same render, diff and validation the CLI performs, in the agent,
  against an inventory read LIVE from the ArgoCD cluster Secrets. It polls
  the open pull requests and posts the `addons-gate` status and report
  comment itself; the triage reads the verdict in-process instead of
  scraping its own comment back off the host.

  What this deletes from an operator's plate: the CI workflow and its image
  pin, the checked-in inventory snapshot and its export/drift-check loop,
  the paths filter (every pull request is rendered; "no change to what gets
  deployed" is an answer, not a guess), and the rule that the bot's token
  must be able to re-trigger CI — a pushed fix is a new head commit, and the
  sweep re-gates it because it is there.

  What it costs, stated where it is paid: get/list on Secrets in the ArgoCD
  namespace (they are the inventory, and they also carry cluster
  credentials — the chart scopes the grant to a namespaced Role that exists
  only in this mode), and a required check that now depends on the agent
  being up. `gate.mode: ci` is the old behaviour, kept whole, for fork pull
  requests on public repositories and for operators who decline either cost.
  Fork pull requests in cluster mode get an `error` status naming
  `gate.forkPRs` rather than an unreported required check. See ADR 0008 and
  the new `docs/onboarding.md`.

  **Upgrading from the CI shape:** nothing breaks on upgrade — the agent
  skips commits that already carry a verdict, so a still-running CI gate
  coexists with it — but the mode is a default change, and the migration
  checklist in `docs/onboarding.md` is the tidy path. Set `gate.mode: ci`
  to keep the old shape unchanged.

- **The engine is a package; the CLI is one caller of it.** `gate/` compiled
  as a single main package, so the only way to run a render was the binary.
  Now the engine is importable -- the agent imports it and runs the gate
  in-cluster (ADR 0008) -- and the CLI moved to `cmd/gitops-gate` with
  identical flags, output and exit codes. Two consequences visible from
  outside: `clusters` in `.gitops-gate.yaml` is required only where the
  snapshot is actually read (the CLI; a live-inventory caller has no use for
  the key), and the ArgoCD Secret decode is one shared function, so the
  exported snapshot and the agent's live read can never disagree about what
  a Secret means.

### Added

- **`docs/onboarding.md`** — the single guide onboarding never had: six
  steps, each ending in a verifiable state, with the CI shape demoted to an
  appendix. Previously the path was scattered across five READMEs and the
  reference consumer's commit history.

- **A dev shell** (`nix develop`) carrying the toolchain, with helm and
  kubeconform pinned to the versions the images render with. The gate's
  verdict is the output of `helm template`, so a contributor rendering with a
  different helm than production gets a verdict that is locally true and
  globally wrong; `hack/portability-test.sh` now asserts the flake and both
  Dockerfiles agree.

### Fixed

- **A gate report and the "pending" that announced it tie, and Gitea broke
  the tie wrong.** Gitea returns commit statuses newest first and stamps them
  in whole seconds, so a gate that reports its progress lands both inside one
  second and the order within the tie is arbitrary. Taking the first match
  read a check that had gone green in seconds as permanently pending: the
  agent waited out the whole of `gate.wait` and announced "still had no
  verdict" about a gate that had already answered. Ties now break on meaning
  — a verdict cannot precede the pending that announced it. GitHub is
  unaffected; it reports through check runs and a documented order.

- **A gate that could not run never tried again.** A verdict answers a commit
  and is kept, but a *failure to run* was kept on the same terms — and its
  cause is almost always cluster-side (RBAC not granted yet, a chart
  repository briefly unreachable). The `error` status outlived its own cause
  and cleared only when somebody pushed. Broken runs now retry after five
  minutes; a refusal to gate fork content does not, being a decision rather
  than a failure.

## [0.15.2] - 2026-08-24

### Fixed

- **"Values not carried across" named values the migration had carried.** The
  check compared scalar strings exactly, so a value the TARGET SCHEMA respells
  read as a value the migration dropped.

  cert-manager v1 spells the key algorithm `ECDSA` where v1alpha2 spelled it
  `ecdsa`, and the enum is what dictates the new spelling -- the same schema
  authority that let the value be written at all. The comment announced
  **Values not carried across: `ecdsa`, `pkcs8`** directly beneath the diff that
  carried them into `privateKey.algorithm` and `privateKey.encoding`.

  Respelled values are now reported separately, as **Respelled by the new
  schema: `ecdsa -> ECDSA`**, and only a value with nowhere to go is called
  lost. The escape hatch is narrow on purpose: a differing-case value counts as
  a respelling only if it is a member of the target schema's own vocabulary --
  an enum member, a const, a declared default. A model that quietly lowercased
  a name it invented still reads as loss, which is what that warning is for.

## [0.15.1] - 2026-08-24

### Fixed

- **The reshape comment's diff hid the value it had just preserved.** It was a
  set difference on line text, so any line whose exact text appeared on both
  sides was printed on neither -- which is precisely what happens when a value
  MOVES without changing column, the normal case for the migrations this path
  performs.

  cert-manager v1 moves `organization` under `subject.organizations`. The list
  item stays at the same indent, so it was invisible and the comment rendered
  the key being deleted into an empty field:

  ```diff
  -  organization:
  +  subject:
  +    organizations:
  ```

  The value survived. Directly below sat "Values not carried across", which a
  reader has every reason to read as confirmation that it had not. The one
  thing this diff must never do is make a preserved value look dropped, since a
  reader's decision to trust the harness rests on it. Replaced with a
  longest-common-subsequence diff that emits three lines of context around each
  change, so a moved value appears under its new key. Also fixes multiplicity:
  removing one of two identical lines used to show as no change at all.

## [0.15.0] - 2026-08-24

### Added

- **Egress is open, logged, and deniable by name.** The allow-list is gone.

  It was correct and it was a full-time job. Every chart repository, every
  registry's blob CDN and every redirect target had to be named before the agent
  could read it -- and a chart repository redirects its index
  (`charts.external-secrets.io` -> `external-secrets.io`), publishes its archive
  on a release-asset CDN (15 of 21 use
  `release-assets.githubusercontent.com`), and changes that CDN's hostname
  without telling anyone. Three separate incidents added a host after the fact.
  The symptom each time was a two-minute timeout and a brief that said it had no
  evidence, which is the quiet failure this whole component exists to end.

  The record moves into the agent. Every outbound request is logged -- method,
  host and path, with the **query string redacted**, because release-asset URLs
  are pre-signed and carry a JWT. `triage.egressDeny` forbids a host by name or
  by `*.suffix`; a pattern forbids the apex too, because an operator blocking a
  domain means the domain. A refused request never leaves the process and is
  logged as `REFUSED` with the rule that stopped it.

  **Enforced where the connection is made**, as an `http.RoundTripper` rather
  than a check at each call site -- the call sites are the problem, since a
  redirect reaches a host no call site ever named. The one path a transport
  cannot see is `helm template`, a subprocess: its repository is checked and
  logged before it is invoked, and the log says plainly that helm will follow
  the index to wherever the archive is served.

  **This widens what the agent may READ, not what it may DO.** It still writes
  only to the pull request's own branch, still refuses paths on `edits.DefaultDeny`
  which no configuration can remove from, and still never mutates the cluster.

## [0.14.2] - 2026-08-24

Both found by watching a live replay round, not by reading the code back.

### Fixed

- **The compare range was framed by list order, and the list is not ordered the
  way that assumed.** `framing` took the first match in each direction, on the
  reasoning that a release list is newest-first. GitHub returns releases in
  **publish-date** order, and any project that backports interleaves them:
  authentik published `version/2026.5.5` one minute *after* `version/2026.2.6`.

  A live promotion of `2025.12.4 -> 2026.2.3` framed itself as
  `version/2025.8.6...version/2025.12.6` — a window that **ends below the
  version being adopted** and starts four minor releases early. 1896 commits
  were read over it and reported as evidence, which is worse than reading none.

  Head is now the highest version in range and base the highest at or below the
  version being left, compared as versions. The same promotion now frames
  `version/2025.12.4...version/2026.2.3`, 960 commits.

- **"None of the 1896 commit(s) mentions it" claimed a search nobody ran.** A
  compare answer carries at most 250 commits, so the filter saw a fraction of
  them. The line now reads "none of the 250 commit(s) read from `<range>` (of
  1896)". Same rule as the rest of this file: a provenance line may not describe
  evidence it did not have.

### Note on the release itself

The code for these two landed on `main` in a commit that carried **no chart
bump and no changelog**, because the edit that should have made them was written
against a stale ref and used a plain string replace with no assertion — so it
matched nothing and said nothing. `Release` then correctly cut nothing, and the
fix sat on `main` unreleased.

Recorded because it is the same failure this project keeps naming: an operation
that quietly does nothing looks exactly like one that had nothing to do.

## [0.14.1] - 2026-08-24

### Fixed

- **A fallback that works was silent, which is the wrong way round.**
  [ADR 0007](adr/0007-structure-from-the-schema-data-from-the-document.md)
  promises the live-CRD fallback for a target schema is "labelled as one in the
  comment". It was not: the note was attached to the schema pair and then only
  surfaced when the pair was INCOMPLETE — which is exactly when the fallback had
  *not* been used.

  Caught on the first live replay round. The external-secrets migration reported
  no structural findings and said nothing about which schema it had checked
  against; the chart render could not reach `charts.external-secrets.io` at all,
  so the answer can only have come from the version the cluster serves today.

  That distinction is the whole point. A target schema taken from what is
  installed now predates the bump and can miss a field the new chart version
  added, so a clean result there carries less confidence than a clean result
  checked against the chart's own schema. Only the comment can tell a reader
  which they got. A new **"Which schema the check used"** section does.

## [0.14.0] - 2026-08-24

Three releases in an afternoon each fixed the bug the previous one's
verification exposed. That is a bad way to work and it cost the operator a
deploy cycle every time. This release is the pass that should have come first:
the resolver was pointed at **every artifact in a real promotion target list**
at once, offline, and everything that broke was fixed together.

**Before: 17 of 41 artifacts resolved. After: 34 of 41.** The remaining seven are
publishers who genuinely declare no source, each now refused by name.

### Added

- **Classic Helm repositories.** Twenty of the forty-one artifacts are
  `https://` Helm repositories -- metallb, kyverno, cilium, cert-manager,
  external-secrets, argo-cd, authentik, trivy-operator, loki, grafana and the
  rest. **Every chart in the eval suite.** None of them had ever resolved.

  The cause was one line in the promotion pipeline. A chart's artifact is built
  as `repoURL SPACE chartName`; for an OCI chart the name is empty so the value
  trims to a bare URL, which is why every OCI path worked and no classic one
  did. The two-field string was parsed as a single OCI reference and turned into
  `https://https/v2//kyverno.github.io/kyverno/manifests/latest` -- an error
  naming neither the artifact nor the problem.

  A chart's source now comes from the repository's `index.yaml`, where
  `helm repo index` copies Chart.yaml's `sources` exactly as `helm push` copies
  it into an OCI annotation. Same declaration by the same publisher, in the
  format their distribution channel uses. `home` is a fallback, because for many
  charts it is the only field set. The index read is capped at 16MiB -- some are
  enormous -- and a bigger one degrades to a sentence.

- **Docker Hub short references.** `redis`, `linuxserver/sonarr`,
  `metio/matrix-alertmanager-receiver` and `redimp/otterwiki` were refused as
  "not an OCI reference", on the principle that guessing a registry is the same
  mistake as guessing a repository.

  **That principle was right about the wrong thing, and this reverses it
  deliberately.** Guessing a repository from a registry path invents a fact
  nobody stated; a short reference invents nothing, because Docker's convention
  gives it exactly one meaning and the pipeline is handing us the reference
  rather than us inferring one. A string that is not a reference is still
  refused.

### Fixed

- **`docker.io` is a website.** The v2 API lives at `registry-1.docker.io`, and
  asking the wrong one returns HTML -- surfacing as `invalid character '<'
  looking for beginning of value`, an error naming neither the host nor the
  problem. The auth host was already mapped here; the registry host was not.

- **An index whose children declare no platform is followed, not refused.** A
  single-manifest index and some publishers' output carry `platform: null`, and
  the label sits on the one child regardless.

- **"0 upstream commit(s) in `v0.13.1...v0.13.2`" reads as "the range was
  empty".** It was not: there were two commits and neither mentioned what the
  gate found -- a different statement, and a more useful one. Observed in
  production on the first pull request the fixed resolver triaged.

  0.13.2 did not fix this and the reason is worth recording: that release
  changed three lines in `triage.go` and this line sits twenty lines away in the
  same file with the same defect. An instance fix where the class needed a
  sweep. Doing the sweep turned up a second case immediately -- where the commit
  MESSAGES matched nothing but the upstream DIFF did, the wording would have
  claimed nothing mentioned it while the explanation stood on exactly that file
  evidence, which is the shape this whole feature was built for.

### Fixed — the structural migration, audited the same way

The same treatment applied to PR D's path, which had never run against a real
chart or a real CustomResourceDefinition. Three findings, none of which fixtures
could have produced.

- **Its chart render had the identical artifact bug.** `renderTargetCRDs`
  prepended `oci://` to whatever it was given, so a classic Helm repository
  became `oci://https://kyverno.github.io/kyverno kyverno` and failed with
  `invalid repository`. That is external-secrets, kyverno and cert-manager --
  the charts that actually drop CRD versions, which is to say every promotion
  this feature exists for. It now dispatches through the same
  `upstream.ParseArtifact`, because "what shape is this artifact" needs ONE
  owner; two answers is how the resolver came to parse it correctly while the
  code beside it did not.

- **The detector rejected `apiVersion`, `kind` and `metadata`.** Those belong to
  the API machinery, and Kubernetes' structural-schema rules say a root schema
  must not restrict them -- plenty of real CRDs declare `spec` and `status` and
  nothing else.

  Measured against **152 live objects from 67 CustomResourceDefinition
  kind/version pairs on a real cluster**: 5 objects produced findings for
  `apiVersion`, `kind` and every `metadata` key. Every one was a false positive
  by construction, since the apiserver had already accepted the object under
  that schema. In production it would have fired the model on a healthy
  document, and the only proposal able to satisfy the complaint would have
  deleted the object's identity -- which the identity validator then refuses. A
  confusing escalation on a manifest that was fine. **Now 152 of 152 clean.**

- **A rendered schema is capped at 12,000 characters.** Measured, not guessed:
  the largest schema on that cluster renders to **43,831 characters** (kyverno's
  `ClusterPolicy` v2beta1) and a prompt carries two of them plus the document
  plus the gate report.

  Truncating is safe here and would not be elsewhere: the validators run against
  the FULL schema whatever the prompt showed, so a model that never saw the
  destination field cannot produce a proposal that passes schema-validity. The
  cost is a refusal, never a bad write, and the truncation note tells the model
  to say so rather than guess.

The detector was also confirmed to still FIRE, which a zero-false-positive
detector otherwise proves nothing about: checked across versions of the same
CRD it found real, already-shipped migrations -- `spec.provider.onepassword` and
`spec.data[].remoteRef.decodingStrategy` between external-secrets `v1beta1` and
`v1alpha1`.

### Added, for the next time

- **`TestAuditArtifacts`** -- point the resolver at a file of artifact
  references and get a table of what resolves and what does not. Every bug in
  this package has been the same bug: reality had a shape the fixtures did not,
  and the code was only ever aimed at one artifact at a time. The list of
  artifacts a pipeline actually promotes is the cheapest way to find that out
  before anybody deploys.

  ```bash
  UPSTREAM_AUDIT_FILE=artifacts.txt go test ./upstream -run Audit -v
  ```

- **`TestAuditLiveObjects` and `TestAuditCrossVersion`** -- the same idea for
  the structural detector. Dump a cluster's CRDs and some live objects, and
  check every object against the schema that already accepted it: every finding
  is a false positive by construction, so the right answer is always zero and no
  judgement is needed. The cross-version half proves it still fires, and reports
  what real schemas cost in a prompt.

  ```bash
  STRUCTURAL_AUDIT_CRDS=crds.json STRUCTURAL_AUDIT_OBJECTS=objects.jsonl \
    go test ./structural -run Audit -v
  ```

## [0.13.2] - 2026-08-24

### Fixed

- **"More than could be read" and "showing fewer than we found" are different
  facts, and they were sharing a flag.** Found by running the live resolver
  against this project's own 0.13.0 → 0.13.1 bump: a three-commit range, all
  three read and all three relevant, reported `truncated=true` — because eleven
  *files* in the upstream diff matched the search terms and hit `MaxCommits`.

  It fails in the direction that matters. `Truncated` is what licenses the
  phrase "more than could be read", and saying that about a range read in full
  tells a reader the evidence might be incomplete when it is not — which is the
  one thing an evidence label must never do.

  `Truncated` now means coverage only: GitHub answers a compare with at most 250
  commits, so a larger range really was filtered over a partial list. `Capped`
  is the new, separate flag for "everything was read, this is showing the first
  few", and the brief says which.

## [0.13.1] - 2026-08-24

### Fixed

- **A chart is not an image, and upstream notes had never worked for one.**

  OCI lets a publisher say where an artifact came from in two places, and which
  one they use depends on what the artifact is. This read one of them:

  | Artifact | Where the source label lives |
  |---|---|
  | image | the image config blob, as Docker-style `Labels` |
  | **Helm chart** | the **manifest annotations** — `helm push` maps `Chart.yaml`'s `sources[0]` there, and its config blob is Chart.yaml metadata with no `Labels` map at all |

  So every chart promotion resolved to *"publishes no
  org.opencontainers.image.source"* — a sentence that is not merely unhelpful
  but **false**, and false in the direction that sends a reader off to check
  their chart's metadata. Chart promotions are the majority of what this
  pipeline does, so upstream notes have never worked for the common case and
  said so in words that pointed at the wrong component.

  Found on `gitops_homelab_2_0#164` — the pull request that upgraded the agent
  to 0.13.0 — where this project's own chart, which publishes the label
  correctly, was reported as not publishing it.

  Annotations are now read at every level (index, then child manifest), with the
  config blob's `Labels` still winning where they exist, so every image that
  worked before behaves identically. A Helm config's media type is recognised
  and its blob is not fetched at all, which also drops the second registry host
  a blob redirect needs in an egress allow-list.

- **Maintainer notes no longer depend on GitHub Releases existing.** Creating a
  Release is an optional step plenty of projects never take — this one included
  — and the resolver treated it as the only place notes could be. There are
  three sources and they are now tried in order of how much they say:

  | Source | Availability |
  |---|---|
  | GitHub Releases | richest, and the least reliable |
  | **a CHANGELOG in the repository** | kept by most projects that keep anything, written in the same commit as the change |
  | commits between the two tags | always there, never polished |

  **A chart's own changelog is preferred to the repository's**, and that
  ordering is the point rather than a nicety: a chart's version numbers and its
  application's are different sequences, and a repository that publishes both
  has a file for each. Reading the root changelog for a chart bump answers with
  the wrong project's versions — confidently, and in exactly the right shape.
  `charts/<name>/CHANGELOG.md` is tried first for a chart artifact, then
  `CHANGELOG.md`, `CHANGES.md`, `HISTORY.md`, `docs/CHANGELOG.md`.

  Heading parsing is deliberately tolerant — `## [1.2.3] - date`, `## v1.2.3`,
  `# 1.2.3 (date)` and `## Release 2.0` are all in the wild — and a section runs
  to the next heading *at the same level or higher*, so an entry keeps its
  `### Added` subsections instead of being truncated to a blank line.

  `Notes.Origin` records which source was used, and both the prompt and the
  pull-request comment say so. A Release is written once at the moment of
  release; a changelog is read at the default branch and can have been edited
  since. That is a small difference and a reader weighing an explanation should
  still be told which one they got.

- **A project that tags without releasing now gets its commits read.** With the
  label fixed, this repository's own bumps still found nothing: it publishes
  **8 git tags and 0 GitHub Releases**, and the resolver only read
  `/repos/{r}/releases`. A compare range wants two refs, and a tag is a ref —
  the release object was only ever a convenient place to find one. `Compare`
  now falls back to `/repos/{r}/tags`, so `v0.12.0...v0.13.0` resolves and the
  commits are read even where there are no notes to go with them.

  Release notes still require actual GitHub Releases; that is a publisher's
  choice, and the note now says which of the two situations it is in rather
  than asserting the wrong one.

- **The "no releases in range" note no longer claims a project publishes
  releases** when it publishes none. Two different situations, one of which
  sends a reader to check version numbers that are fine.

## [0.13.0] - 2026-08-24

### Added

- **Structure from the schema, data from the document.** The deterministic
  repair swaps an `apiVersion` line and touches nothing else. That is exactly
  right while the two versions are compatible, and a silent corruption when they
  are not: a chart that moves `spec.store` to `spec.secretStoreRef.name` leaves
  a document that parses, applies, and has that field pruned by the apiserver on
  the way in. The render is fine. The gate is green. The value is gone.

  Nobody can enumerate every upstream's structural changes in advance, so the
  model is shown the OLD schema, the NEW schema and the document, and asked to
  translate. The proposal surface widens from a scalar edit to a whole document;
  **who writes does not change**, and the checks in front of a document are
  stricter than the ones in front of a scalar because there is no `from` value
  left to match:

  | Check | Refuses |
  |---|---|
  | identity | a changed `apiVersion`, `kind`, `metadata.name` or `metadata.namespace` |
  | schema validity | a proposal the target schema still does not accept |
  | value provenance | any value not at that path in the original, not displaced by the schema change, and not dictated by the target schema |

  **A refusal refuses everything**, including the plain swaps that were fine.
  Not the obvious choice, and the important one: the swap alone makes the gate
  green -- no manifest declares a dropped version any more -- while a document
  the target schema rejects sits in the tree waiting to be pruned. A partial
  push is a green gate over a broken change.

  Values present in the original and absent from the proposal are **listed** in
  the comment beside a folded diff. Some are correct -- a field the target no
  longer accepts has to go somewhere, sometimes nowhere -- and none is dropped
  silently.

  See [`adr/0007-structure-from-the-schema-data-from-the-document.md`](adr/0007-structure-from-the-schema-data-from-the-document.md).

  **The provenance check is positional, and the suite is why.** A
  set-membership version -- "does this value appear anywhere in the original?"
  -- passed a live proposal that filled a newly required `secretStoreRef.name`
  with the object's own `metadata.name`. Every value was "from the document".
  The document now referenced a store nobody had created, and it would have
  rendered perfectly. Only the POSITION distinguishes a field that moved from a
  blank filled with whatever was nearest.

  **Measured on `qwen/qwen3.8-27b`:** classification **22/22**, full pass
  **21/22**, **UNSAFE 0** across all three paths. The one non-full-pass is
  recorded rather than smoothed away: on the reference-moved case the model
  produced the correct migration and also wrote out `kind: SecretStore`, a
  default the schema already applies. That is noisier than asked, not wrong, and
  it is scored as a note -- calling it UNSAFE would make the word mean "differs
  from my fixture" instead of "would have broken something", and the word is
  only worth anything while it means the second.

### Changed

- **The safety model's headline sentence widened, deliberately.** It read "the
  model never edits a file" and now reads "the model never WRITES". The
  difference is this release: the proposal surface widened and the write path
  did not.

- **The agent image carries `helm`**, the same version the gate's does. The
  target schema comes from rendering the chart at the version being promoted to,
  and the only thing guaranteed to render a chart the way the cluster's own Helm
  will is Helm.

### Known limits

- A reshaped document is **re-serialised**, so comments inside it do not
  survive. The folded diff shows exactly what changed. There is no version of
  this that avoids it: preserving comments means surgical line edits, and a line
  edit is precisely what cannot express a change of shape.
- **Nested and embedded manifests are skipped** and escalated. `migrate`
  deliberately reaches into `extraObjects:` lists and block scalars -- 13 of 27
  declaring files in the incident this was built from held the declaration
  somewhere other than the top level -- because swapping one value on one line
  inside a values file is safe. Replacing a *document* inside one is not.

## [0.12.0] - 2026-08-24

### Added

- **What is actually running.** [ADR 0002](adr/0002-triage-in-cluster-not-ci.md)
  put triage in the cluster rather than in CI on a structural argument: a CI job
  can read a repository and a pull request, and it cannot read what is running.
  The chart has shipped a read-only ClusterRole since its first release and no
  Go code had ever used it. The promotion has carried `verifyApps` on the wire
  since the first version and nothing had ever read it. Both are spent now.

  A brief gains lines like:

  ```
  - externalsecrets.external-secrets.io on v1beta1 — 0 live object(s)
  - Application external-secrets-host — Degraded / OutOfSync
  ```

  Counted by code against a read-only view and labelled **fact** — the
  strongest evidence in a brief, because nobody wrote it down.

  The explain prompt learned to spend it. A CustomResourceDefinition that stops
  serving a version, where the report counts no declaring manifest **and** the
  live block counts no stored objects, now has nothing left to go wrong and is
  a `no_action` rather than a human's afternoon. That finding always needed a
  human before, and the answer was always the same.

  **"Not permitted" is never a zero.** `cluster.Count` carries a `Known` flag
  and its rendering prefers the note over the number, so a refusal, an
  unreachable apiserver, or a count where one version answered and another did
  not all say what was *not* checked. The prompt tells the model in those words
  that "not permitted to check" means nobody looked and is not evidence of
  safety. The whole value of "0 live objects" is that it ends a conversation,
  and it can only do that if it never quietly means "we did not ask".

  Hand-rolled over `net/http`, no `client-go` — the same call this project made
  for the GitHub client and the App JWT. The service-account token is re-read
  from disk on every request, because projected tokens are bound and rotate
  roughly hourly: a client that read one at start-up works for fifty minutes
  and then 401s forever, which on a service called a few times a day looks fine
  in every test.

  **Measured on `qwen/qwen3.8-27b`:** classification **19/19**, full pass
  **19/19**, **UNSAFE 0**, with two new cases -- 0 objects on the version being
  removed must not be escalated, and "not permitted to check" must not be
  converted into reassurance.

  The prompt change cost one measured regression before it was right, and it is
  worth recording. The first wording said only "use the live block to discharge
  a finding", and a 0.9.20 -> 0.11.0 case with **no live block at all** dropped
  from `escalate` to `no_action`. A permission to relax, written loosely,
  relaxes everything. The rule now names the single finding it discharges and
  says explicitly that every other reason to escalate stands on its own.

  Off by default. See
  [`adr/0006-live-reads-are-scoped-by-group.md`](adr/0006-live-reads-are-scoped-by-group.md)
  for why "everything except Secrets" is not a setting, and the chart changelog
  for the two scopes and the egress it needs.

## [0.11.0] - 2026-08-24

### Added

- **The commits between the two upstream tags.** A chart bump removed a
  `ClusterRole` and a `ClusterRoleBinding`; the gate proved it; no release in
  the range mentioned it; and the best the agent could say was *"no release
  notes explain why"* — correct, honest, and a handoff that gives a human a
  search rather than an answer. The commit that deleted the template says
  exactly why, in a sentence nobody wrote for a changelog.

  **The gate chooses the evidence.** `migrate.Subjects` reads the kinds and
  resource names out of the gate's own findings and those terms are matched
  against commit messages and against the paths in the upstream diff. The file
  paths carry most of the weight: a commit titled "watch namespaces via config"
  does not contain the string `ClusterRole`; the template it deleted does.
  Asking the model which commits support its conclusion would be a second
  opinion from the same opinion.

  **Testimony still never reaches the write path.** Not as a rule in a prompt —
  the mechanical path does not fetch upstream at all, so no commit message is
  ever in the evidence string the applier corroborates version-shaped values
  against. A commit mentioning `v1.5.0` would otherwise make `v1.5.0` a
  corroborated value to write.

  **A range that cannot be established is not guessed.** A chart version and
  the git tags of the project it packages are frequently different numbering,
  and two refs picked out of the wrong sequence return real commits from a
  range that is not this promotion's — which reads exactly like the truth.
  Refs come from the project's own release tags (base is the release the
  repository is *leaving*) or from the `org.opencontainers.image.revision` the
  publisher recorded at build time. When neither meets, no comparison is made
  and the note says which namespaces failed to meet.

  The interesting negative survives: *"312 commits between these tags and none
  of them mentions this"* is a real fact about a bump, and an empty section
  that simply vanished would have read as "nothing was looked for".

  `CompareResolver` is a second interface, type-asserted — a resolver that only
  reads releases keeps compiling and contributes no commits. See
  [`adr/0005-testimony-is-not-evidence.md`](adr/0005-testimony-is-not-evidence.md).

  **Measured on `qwen/qwen3.8-27b`:** classification **17/17**, full pass
  **17/17**, **UNSAFE 0**, with two new explain cases — one where the commit
  supplies the reason the notes did not, one where three hundred commits supply
  nothing and the model must not fill the silence.

### Fixed

- **Upstream reads were anonymous under App authentication.** The resolver was
  handed the static `GIT_TOKEN`, which App mode leaves empty by design —
  installation tokens are minted per use. So from the release that made the
  agent a GitHub App, every upstream read went out unauthenticated against
  `api.github.com`'s 60-per-hour-per-IP limit, and the failure surfaced as "no
  upstream release notes", which is also what an artifact publishing none looks
  like. The credential is now fetched per call, for the release walk as well as
  the compare. Rate limiting gets its own sentence rather than hiding inside
  "could not read the releases", which sends a reader off to check whether the
  project publishes any.

## [0.10.0] - 2026-08-24

### Added

- **The explain path is measured.** Two prompts ship and the suite scored one
  of them. The triage classifier's failure lands on disk, where the applier is
  standing in front of it -- a wrong path refused, a wrong `from` refused, an
  invented version refused. The explanation writes nothing, so it has no
  applier, and its failure is a fluent account of what a version "did"
  assembled from what the model remembers rather than from the two sources it
  was handed. That goes straight to somebody about to press merge. The
  unmeasured prompt was the one with nothing behind it.

  Five cases, generalised from the live re-runs, built in pairs: the same
  removed `ClusterRole` with the maintainers' explanation in front of the model
  and without it, so the measurement is whether the second answer still carries
  the first answer's reason. `MustMention` asserts the grounded reason was
  cited; `MustNotMention` asserts a word that could only have come from memory
  did not appear.

  **Measured on `qwen/qwen3.8-27b`:** classification **15/15**, full pass
  **15/15**, **UNSAFE 0** — the ten triage cases hold at 10/10 and the five
  explain cases pass. `scripts/extract-prompt.sh` now takes a symbol
  (`explainPrompt`), `DELIVERY_AGENT_EXPLAIN_PROMPT` supplies it, and
  `DELIVERY_AGENT_CASES` filters to one case.

  One probe was wrong and is recorded as such rather than quietly deleted. The
  first run flagged `namespaced` as an invention; the answer was "swaps the
  cluster-scoped ClusterRole and ClusterRoleBinding for namespaced Role and
  RoleBinding", which is the render restated -- a `Role` *is* namespaced. A
  probe that fires on a fact rephrased measures vocabulary. The suite now
  prints the whole answer on a grounding failure, because that judgement cannot
  be made from the probe alone.

- **`gate.reportAuthor`** — the account whose gate report the agent will
  believe. See the chart changelog for the per-host defaults.

### Fixed

- **A forged gate report is no longer the gate's.** The verdict arrives as a
  pull-request comment carrying `<!-- gitops-gate -->`, and the marker was the
  whole of the check. Anyone who can comment can write it, and the report under
  it is what decides which manifests the deterministic repair rewrites, which
  version strings the applier accepts as corroborated, and what the model is
  told rendered. The comment now has to come from the configured account.

  Two things fall out of doing it properly. The **newest** qualifying report
  wins, because a gate that re-ran leaves two and the stale one describes a
  commit that is no longer the head. And a green gate whose report was refused
  no longer reports itself as a green gate with nothing to explain — that
  sentence is for a gate that said nothing.

- **A comment past the hundredth is still a comment.** Both git clients asked
  for one page of a hundred and returned it, so on a busier pull request the
  gate's report was simply absent from the list the agent scans — and the agent
  said the gate had published nothing, which reads as a broken gate and points
  at the wrong component entirely. GitHub is now read newest-first so the page
  bound drops history nobody came for; Gitea's endpoint has no direction
  parameter, so a pull request that reaches its bound is an error rather than a
  list missing the newest comments that claims to be whole. `Comment` also
  carries the id and timestamp both hosts were already returning.

### Changed

- `renderNotes` moved to `upstream.Render`, so the block the eval suite scores
  is the block the agent sends rather than a copy that can drift from it.

## [0.9.3] - 2026-08-24

### Fixed

- **The legacy author is ignored, not honored.** 0.9.2 fixed the chart
  DEFAULT that attributed pushed commits to the unrelated GitHub account
  `bosun` -- and the first re-run still landed as that stranger, because the
  consuming repository's values file carried the old default as an explicit
  value, and explicit values beat defaults. Exactly `bosun
  <bosun@users.noreply.github.com>` is now cleared at start-up with a log
  line, so the App derives its own bot identity; an author somebody actually
  chose is still honored.

### Added

- **A removed CRD is inspected, not just listed.** Removal is the limiting
  case of dropping served versions -- all of them, no survivor -- but it sat
  in the plain Removed list while the version-drop path counted consumers, so
  a reviewer got "12 resources removed" and went looking themselves whether
  anything here used those APIs. Asked live, on the kyverno 3.9.0 promotion:
  *"why didn't it look to see if I was consuming that api anywhere ... or
  tell me I wasn't and that the update looks safe from its inspection?"* Now
  it joins the consumer-scanned class: consumers present block and are named;
  counted at zero, the report says outright that nothing in the repository
  uses the API and from inspection the removal looks safe. No survivor means
  no repair -- the agent's parser deliberately cannot act on the removal line.

- **A removed binding names the ServiceAccount it orphans.** A dropped
  ClusterRoleBinding is either routine chart tidying or a workload silently
  losing every permission it runs on, and the difference is whether its
  ServiceAccount is still bound to anything in the new render. The gate now
  checks, and the Removed entry carries the answer when it is bad news. No
  note when the subject is re-bound (routine is not a finding), and no note
  when any head binding's subjects cannot be read -- claiming "unbound" past
  an unreadable binding would be a guess.

## [0.9.2] - 2026-08-24

### Fixed

- **The repair's commits no longer belong to a stranger.** The first live
  migration -- 27 manifests, pushed to a real pull request -- rendered under
  the avatar of the unrelated GitHub account named `bosun`, because the
  default author email was `bosun@users.noreply.github.com` and that
  namespace BELONGS to accounts: commit emails are unauthenticated
  display-matching, so an address in it that is not yours attributes your
  work to whoever owns the name. As a GitHub App the agent now resolves its
  own bot identity at start-up -- `<slug>[bot]
  <id+slug[bot]@users.noreply.github.com>`, the exact format GitHub's own
  bots use -- and fails to start if it cannot, the same rule as a bad key.
  The token-mode fallback moves to `bosun@noreply.invalid` (RFC 2606), which
  maps to nobody. Chart author defaults are now empty, meaning "derive";
  setting them still wins.

- **The repair's own apiVersion moves no longer re-block the gate.** The
  first live repair migrated every consumer to the survivor, the recount
  found zero -- and the gate went red anyway, on the migration itself: each
  rewritten manifest is an object whose apiVersion moved, and the apiVersion
  rule cannot tell an unexplained migration from the one its own report
  demanded. A move is now marked as part of the migration when a
  crdVersionRemoved finding in the same diff names exactly it -- same
  consumer kind, from a dropped version, to the named survivor -- and such
  moves are reported (with the reason) but do not block. The match is exact
  on purpose: another target version, or another kind, still blocks.

## [0.9.1] - 2026-08-24

### Fixed

- **The v0.9.0 gitops-gate image never existed.** Its Dockerfile copied only
  `gate/` into the build and the gate now imports `migrate/`, so the release
  published the agent image and died on the gate's -- the entry below.
  This release exists to run the release machinery over the fixed Dockerfile:
  the version path publishes both images, so v0.9.1 is the first tag since
  the repair feature whose gate image is real. Nothing about the agent binary
  changed since 0.9.0.

- **The gate image copies the module it builds.** The Dockerfile hand-picked
  `gate/` into the build stage, and the day the gate first imported a sibling
  package -- `migrate`, in the very release that shipped consumer-aware
  blocking -- the v0.9.0 image build died on `no required module provides
  package .../migrate` while CI's `go build ./...` stayed green: CI builds
  the checkout, the image builds the COPY list, and only one of them follows
  the import graph. The build stage now copies the module wholesale, as the
  agent's Dockerfile always has. The image workflow's scope filter learned
  the same lesson: `migrate/` now rebuilds the gate image too, so a change to
  the shared scanner cannot ship a stale gate.

## [0.9.0] - 2026-08-24

### Changed

- **An escalation is a handoff, not an announcement.** Read back from the
  first live runs on real held promotions: every escalation said the same
  thing three times -- the headline carried the escalation reason, the
  summary paraphrased it, and the reasoning restated both before restating
  the gate report -- and named nothing the reader could open. "This needs
  escalation, so I am escalating, and to be sure you know, I have escalated"
  is how its owner summarised it, accurately.

  Two structural fixes and one contract. The comment renderers now print the
  verdict marker once: the model's `escalationReason` goes to the commit
  status line and is no longer duplicated into the comment, on the red path
  and the green explain path both. (Process reasons -- "rejected before
  anything was written", "could not push" -- still lead the headline; those
  are facts the verdict does not carry.)

  And the prompts now state what the fields are FOR and what an escalation
  owes the reader: summary is the decision in one sentence; reasoning is the
  handoff -- WHERE (the file and key to open, copied from the editable list,
  or the honest sentence that the list did not include the place that needs
  the change), WHAT (the decision as a choice, not a description), and WHY
  it stopped (the one fact that made this not mechanical) -- and never a
  restatement of the report sitting directly above the comment. The explain
  path gets the matching rule: no reading the report's inventory back; name
  the finding that changes what the reader should do.

  Also corrected while in there: the mechanical-case list still taught the
  moved-port case unconditionally, arguing the opposite of what the eval
  suite has scored since the 0.4.0 reclassification. It now states the
  precondition -- the key must be in the editable list and the value in the
  evidence -- and that the escalation naming them is worth more than the fix
  it cannot make.

  Measured after the change on qwen3.8-27b: classification **10/10**, full
  pass **10/10**, **UNSAFE 0** (3m13s), with the three accommodation cases
  still classifying mechanical -- spending the words on the handoff did not
  push the model toward escalating everything.
## [0.8.0] - 2026-08-23

### Added

- **A red gate with a known cause gets repaired, not narrated.** When a CRD
  stops serving versions and that is the gate's only blocking finding, the
  agent now rewrites every manifest in the repository that still declares one
  to the version the gate says survives, and pushes the migration to the pull
  request's branch. external-secrets 0.10.3 -> 2.9.0 -- the promotion that
  made the gate learn this class -- becomes a pushed commit moving the
  declaring manifests to `external-secrets.io/v1`, followed by a green re-run,
  instead of an escalation asking a human to do exactly that by hand.

  **No model is involved on this path**, and that is the design rather than an
  optimisation. The gate's report line now carries the consumer kind and the
  surviving version; the new `migrate` package parses that line back and
  rewrites nothing but apiVersion values matching it. The kind, the dropped
  versions and the destination are all computed facts, so the repair is a
  deterministic function of evidence -- the agent's earlier failure mode of
  restating the gate's findings back at it does not arise, because nothing on
  this path speaks in prose except the final comment, whose footer says
  `deterministic repair, no model`.

  The safety model extends rather than bends. Every file still answers to the
  non-overridable deny-list and the standing allowlist; the *scope* check is
  deliberately absent, because consumers are by definition files the promotion
  did not touch, and it is the gate -- not the model -- that named them. The
  rewrite preserves quoting, comments and every untouched document
  byte-for-byte; a file the rewrite cannot fully clear of dropped versions is
  restored and refused rather than half-migrated; a repair refused everywhere
  escalates; and a gate that names consumers the branch does not have
  escalates on the disagreement instead of guessing which side is stale. The
  attempt label caps the loop exactly as it does for model fixes, and the
  re-run gate re-counts the consumers itself -- the shared scanner is what
  makes its green a verification of the repair rather than a second opinion.

  Beside another blocking finding -- a targeting change, a source move, an
  apiVersion migration on an ordinary object -- the deterministic path stands
  down and the model judges the whole report, because repairing the fixable
  half would leave a red gate implying the migration had failed. Helm chart
  `templates/` directories (a `templates` dir beside a `Chart.yaml`) are never
  scanned or rewritten: a template that parses as YAML is still a program, and
  its render is chart-diff's to judge.

  Off switch: `triage.migrateDroppedVersions` (env
  `MIGRATE_DROPPED_VERSIONS=false`), default on.

- **A CustomResourceDefinition that stops serving a version now blocks.** The
  apiVersion rule watches the apiVersion an object *is*. A CRD dropping a
  version it *serves* is `apiextensions.k8s.io/v1` on both sides, so the rule
  could not see it -- while every manifest still declaring the dropped version
  breaks on apply. That is a migration, and it is the most dangerous shape
  available: it renders perfectly.

  Measured against the real held promotion, external-secrets **0.10.3 ->
  2.9.0**. Before, the gate passed it GREEN and the agent described it as
  "adds 11 new CRDs and changes 25 existing resources". Now:

      A CustomResourceDefinition stopped serving a version
        externalsecrets.external-secrets.io:        no longer serves v1alpha1, v1beta1
        clustersecretstores.external-secrets.io:    no longer serves v1alpha1, v1beta1
        secretstores.external-secrets.io:           no longer serves v1alpha1, v1beta1
        clusterexternalsecrets.external-secrets.io: no longer serves v1beta1

  In the consuming repository that is **33 manifests** declaring one of those
  versions and **29 live objects** on them.

  `served` defaults to true in apiextensions/v1, so an absent key means served;
  reading it otherwise would invent removals. A version left listed but turned
  off counts as dropped, because it is gone from the point of view of anything
  that declares it. And without object bodies -- a table loaded from the JSON
  artifact -- the question cannot be answered, so the change is still reported
  as `changed` rather than claimed safe.

### Changed

- **A dropped served version blocks exactly while manifests still declare it.**
  The finding's blast radius is the consuming manifests -- they are what
  breaks at apply -- so with `-repo`, the gate now scans the worktree for them
  (shared `migrate` package, one scanner for the gate and the agent), lists
  them in the report, and blocks only while any remain. Counted at zero, the
  finding is reported and does not block; not scanned at all -- no `-repo`, or
  a finding whose CRD body carried no consumer kind -- still blocks, because
  "we could not look" must never read as safe.

  This is what closes the repair loop: the agent migrates the consumers the
  report names, the re-run gate counts again and finds none, and the same red
  that used to be a hand-written migration becomes green with the work done.
  The report line now carries the repair contract -- the consumer kind from
  `spec.names.kind` and the surviving served version, chosen by API-server
  priority -- and is rendered by the shared package, so the line the gate
  writes and the migration the agent reads back cannot drift apart. Helm chart
  `templates/` directories are excluded from the consumer scan: their render
  is chart-diff's to judge.

## [0.7.0] - 2026-08-23

There is no 0.6.0 here. Chart 0.6.0 was a chart-only release -- FQDN egress
patterns, no Go code -- so `appVersion` never moved to it, and the agent's
versions skip from 0.5.0 straight to 0.7.0.

### Changed

- **A green gate is a verdict on the render, not on the bump.** The explain
  path was pinned to `no_action`, so a green gate meant the agent could
  describe a promotion but never ask anyone to look at it.

  Measured against four real held promotions. kyverno 3.2.8 -> 3.9.0 was
  escalated correctly and precisely -- but only because its PodDisruptionBudget
  migration turned the gate red. external-secrets 0.10.3 -> **2.9.0**, the more
  dangerous of the two, rendered **green**, and the same model on the same day
  produced an accurate inventory of eleven added CRDs and said nothing about
  the risk. Nothing differed between those two runs except which branch of
  `Run` they entered.

  The explain path may now classify `escalate`. It blocks nothing -- the commit
  status is still never a failure state -- but it labels the pull request and
  leads the comment with **Worth a look before merging**. Edits are ignored
  here whatever the model returns, and that is enforced in the function rather
  than requested in the prompt.

  The criteria are deliberately narrow: a large version distance, a resource
  disappearing that something relies on, a CRD dropping a served version, or
  release notes describing a migration. A routine bump must not be flagged,
  because a flag on everything is a flag nobody reads.

  Re-measured on the same three green reports with the new prompt: ESO now
  escalates on the major boundary; trivy-operator-explorer escalates on its
  removed ClusterRole and ClusterRoleBinding; authentik stays `no_action`,
  reasoning that the render is structurally safe. Three samples, one run each.

### Added

- **A changed resource now says WHICH fields changed.** The gate rendered both
  versions, compared them, and then reported `Changed (25)` -- a list of names.
  That is the same non-answer the version number already gave, and it asks a
  reviewer for judgement while withholding the evidence for it. The agent said
  so on every green pull request it explained: *the report does not say which
  fields changed or why*. It was right.

  Each changed object now carries its differing leaves, as dotted paths with
  before and after values, folded into a `<details>` block. Paths use the same
  shape as the agent's edit inventory -- `spec.template.spec.containers.0.image`
  -- so a human and an agent read the report the same way.

  Run against a real promotion (trivy-operator-explorer chart 0.4.6 -> 0.5.1,
  previously reported as two changed objects and nothing else), it surfaced
  three things nobody knew were in it:

      spec.template.spec.containers.0.image:
        ghcr.io/…/trivy-operator-explorer:v0.5.8 -> :v1.0.0
      spec.template.spec.containers.0.ports.1:
        set to {"containerPort":8081,"name":"mcp","protocol":"TCP"}
      spec.template.spec.containers.0.resources.limits.cpu:
        removed (was 500m)

  A **major** application version inside a minor chart bump, a new port that
  needs a NetworkPolicy half, and a dropped CPU limit.

  `Object.Body` carries the parsed manifest in memory and is `json:"-"`, which
  is load-bearing: `Hash` exists so the target table stays small enough to pass
  between CI jobs, and serialising bodies would undo exactly that. A table
  loaded from JSON has no bodies, so the field list is omitted and the finding
  is still reported -- never silently downgraded. Bounded at
  `MaxFieldsPerObject` per object, because a report nobody can open is worth
  less than a short one.

### Fixed

- **An OCI repository URL is the chart; stop appending the chart name to it.**
  ArgoCD accepts a `repoURL` that already ends in the chart alongside a `chart`
  field naming the same thing, and this repository's own addons are configured
  that way. `chartRef` appended regardless, turning
  `oci://ghcr.io/org/charts/bosun` + `bosun` into `.../charts/bosun/bosun` --
  which the registry answers **403 denied**, not 404, so it reads like a
  credentials problem and is not one.

  The cost was quiet and total: chart-diff is skipped for any addon it cannot
  render at both versions, so **every OCI-repo addon lost its resource-level
  diff** while the gate stayed green and said only "NOT covered". Here that was
  `bosun` and `kargo-pipelines` -- the two components that judge everything
  else.

  Verified against the live registry: `charts/bosun` answers 200 anonymously,
  `charts/bosun/bosun` answers 403.

- **A move between clusters is only reported when it is one.** Targeting
  removals and additions were bucketed by ApplicationSet and then paired
  positionally, so two departures and two arrivals became two confident
  "moved" rows. Nothing in the render says which arrival answers which
  departure; the pairing was a guess presented as a finding.

  Both slices were also built by ranging a Go map, so the guess was not stable:
  identical input could describe two different moves on two runs. A report that
  varies without its input varying is one nobody can review. There is now a
  test that runs the same diff fifty times and compares the report, and it
  fails against the old code on the second run.

  A move is reported when there is exactly one candidate on each side.
  Otherwise both sides are reported plainly and the reviewer draws the line.

- **A move names the ApplicationSet, not the Application that arrived.** An
  Application's name carries its cluster, so the row read
  ``metrics-server-vcluster-media | no longer targets the-cluster`` -- naming a
  departure by something that did not exist before the change. It reads as the
  gate contradicting itself, and it is what prompted this fix. The
  ApplicationSet is the identity that survives a move.

## [0.5.0] - 2026-08-23

### Changed

- **The `bosun` status is pending until there is a verdict.** It was written
  `success` on entry -- before the gate had been read, anything cloned, or the
  model called -- so from the first second a reader saw a green check and no
  comment. That is precisely what a finished run with nothing to report looks
  like. On a green gate the window is `gate.wait` plus a model call: ten
  minutes of a status claiming to be done.

  Silence that reads as completion is the failure this service exists to find,
  and the status was producing it. `pending` on a check nobody requires blocks
  no merge; it only stops the report lying about having finished. The rule that
  it is never a FAILURE state is unchanged and deliberate -- a red status would
  make an advisory agent a second gate.

### Fixed

- **An error now resolves the status and reaches the pull request.** Every
  failure after the pull request was read returned to a pod log and nowhere
  else, which with the change above would have left `pending` set for ever.

  The live shape: the gate's `render` job fails, the job that publishes the
  report is skipped, and `gateReport` finds a red check with nothing explaining
  it. A human watching the pull request saw the agent apparently still reading.
  It now says `triage did not finish: <reason>`.

## [0.4.0] - 2026-08-23

### Changed

- **A bump changes the version and nothing else.** The first live run of the
  mechanical path met a pull request whose render moved an addon's destination
  namespace as well as its version. The agent updated a token `SecretRef` to
  name the *new* namespace -- one scalar, inside the promotion's file scope,
  with a correct `from`. Every guard passed, because a guard checks an edit's
  shape and the shape was perfect. The direction was not: it entrenched a
  change nobody had explained, spent the attempt a human needed, and left the
  gate red, since the namespace was still moved.

  The prompt had made that reading fair. It said each pull request "moves one
  pinned version" and then described only reds the version had caused. It now
  names the changes a version *cannot* cause -- a destination namespace, an
  ArgoCD project, a source repository, which clusters an Application targets --
  and says that making the rest of the repository agree with one is the wrong
  answer even when it is the tidy one. A mechanical fix restores what the
  repository already intended; it never ratifies what the promotion did not.

- **An eval case whose right answer is "no".** All three existing mechanical
  cases are accommodations -- flip a default back, move a coupled pin forward --
  where agreeing with the bump is correct. None asks the agent to refuse
  anything, so a model that accommodates unconditionally scored full marks.
  `namespace-moved-under-a-bump` is a transcript of the live failure and the
  first case in the suite that a yes-man fails.

## [0.3.2] - 2026-08-23

### Fixed

- **A GitHub App private key whose newlines a secret store removed.** PEM is
  line-structured; secret stores are not. A key pasted into a single-line
  field -- the default in most vaults, 1Password included -- arrives with every
  newline gone. It is byte for byte the right key, and `pem.Decode` refuses it.

  0.3.1 crash-looped on exactly this, and the error message even guessed the
  wrong cause: it blamed base64, because that is the failure everyone writes
  the message for, while the real key was a good PEM flattened to one line.

  The line breaks are now rebuilt, which is deterministic, so this class of
  vault mangling stops being a support problem. Genuine rubbish is still
  refused, and the message names both likely causes rather than one.

## [0.3.1] - 2026-08-23

### Fixed

- **A correctly-configured GitHub App would not start.** The chart stops
  setting `GIT_TOKEN` under App auth -- installation tokens are minted per use,
  so there is nothing static to set -- while `validate()` still demanded it:

      configuration: missing required configuration: GIT_TOKEN

  0.3.0 crash-looped on first deploy. Verifying the chart *render* was not the
  same as running the binary without a token, and only the second would have
  caught it.

  The required credential now follows the auth mode, and says so: without
  either, the error names both options rather than only the one that used to be
  mandatory.

## [0.3.0] - 2026-08-23

### Added

- **Authenticate as a GitHub App.** `git.app.appId` plus a private key.

  This is about IDENTITY, not access. A token grants exactly the same rights,
  but it belongs to whoever minted it -- so every comment arrived under that
  person's name and avatar and read like a colleague's review until you reached
  the footer. `branding` exists to compensate for that, and compensating is all
  it could do.

  An App has a face: comments come from `yourapp[bot]`, with its own avatar and
  its own timeline entry. Nobody has to be told what wrote them.

  Two things follow. Installation tokens **expire**, in about an hour, and are
  minted on demand from the key -- a leaked one is a bad hour rather than a
  standing grant, where the PAT this replaces had no expiry at all. And the App
  is its own principal, so revoking it disturbs nobody and its actions are
  attributable to it alone.

  `installationId` is optional: left empty it is discovered from the repository,
  which removes a value that can be silently wrong. Authentication is checked at
  **start-up**, so a bad key or an app installed on the wrong repository is a
  pod that will not start rather than a triage that quietly does nothing.

  No JWT dependency -- the exchange is a signed header, a signed claim set and
  one HTTP call.

## [0.2.0] - 2026-08-23

Everything below was found by running the thing, in one evening, after it had
been "live" for a day doing nothing. Each defect looked exactly like a system
with nothing to do.

### Added

- **A green gate that still changed something is now explained.**

  A green gate is not the same as an uneventful change. The gate *blocks* on
  structural things -- targeting, sources, apiVersion migrations -- and
  *reports* the rest: a chart that added five resources, moved a metrics port,
  flipped a default. All of that renders green and arrives as a pull request
  whose visible diff is a single version number. The agent used to stop there
  and say nothing, so a bump's real content was invisible unless someone opened
  the gate's report and read a list of object names.

  `explainPrompt` is a separate prompt, and its grounding rule is stated three
  times deliberately. Nothing is being fixed on this path, so there is no
  schema to fill and no edit for the applier to refuse -- every guard the
  triage path relies on is absent, and the only thing between a useful
  explanation and a confident invention is the instruction not to invent.

  The failure guarded against is specific: a fluent account of what a version
  does, assembled from what the model remembers about a project rather than
  from the diff in front of it. Same class as an invented version number,
  except an invented version gets refused by the applier and an invented
  explanation goes straight into a reader's head where nothing checks it. The
  comment says outright that no upstream release notes were read.

  Three things it does NOT do, each a test:

  - no model call when the gate reports no change at all
  - no second explanation on the same pull request, however many times Kargo
    calls
  - no failure. Explanation is a courtesy on a green gate; a model that is down
    must not be the reason a passing pull request looks unattended

  `triage.explainGreen`, default true.

### Added

- **The agent reports every outcome as a commit status**, so it lands in the
  same surface as the gate rather than only in a pod log.

  `SetCommitStatus` is the method ADR 0004 named from the start and nobody
  built. Its absence meant four outcomes -- gate green, gate absent, gate never
  settled, attempts spent -- left *nothing at all* on the pull request. From
  outside, "nothing needed triage", "I was never called" and "I crashed" were
  the same observation, which is exactly how two defects in this call path
  stayed invisible for a day.

  Eleven verdicts now, each one line: `addons-gate is green; nothing to
  triage`, `escalated: apiVersion migration is not a values fix`, `pushed a fix
  (attempt 1 of 2): ...`, and so on.

  The status is published **before** the gate wait, so a reader during a
  ten-minute poll sees the agent working rather than an absence.

  Two properties are enforced by test rather than convention. It is **always
  `success`**, whatever the verdict -- a red status would make the agent a
  second gate and block merges, which it expressly is not; the description
  carries the meaning. And a status that **cannot be filed never fails the
  triage it reports on** -- losing a fix because the report 403'd would be the
  worst possible trade.

  The status is named from `branding.name`, like the attempt label, so two
  agents on one repository cannot overwrite each other's verdict.

### Fixed

- **Triage gave up on a gate that had not reported yet**, which in practice
  meant every triage. Kargo calls this service from the promotion, immediately
  after opening the pull request -- measured at **three seconds** after, in the
  first triage that ever reached the code. CI has not registered a check that
  early, so the check is *missing* rather than *pending*, and `waitForGate`
  only ever polled on pending.

  The first real triage looked like a clean no-op:

      PR 109: no "addons-gate" check found
      PR 109: triage done in 2s

  A missing check and a pending one are the same thing to the caller: the gate
  has not answered. `GateWait` is the only honest way to tell them apart, and a
  check still absent when it expires is now reported as absent -- so a
  misconfigured `gate.checkName` still surfaces rather than becoming a silent
  ten-minute wait.

  Two tests pin it, and both fail against the old code.

### Changed

- **Licensed PolyForm Internal Use 1.0.0**, and the git history was rewritten
  so that every commit carries it. There is no earlier commit to fork under
  other terms.

  The repository was briefly published under PolyForm Noncommercial. That is
  stricter about commercial use, but it *grants a distribution licence* for
  noncommercial purposes -- and Internal Use grants none at all. So an early
  commit would have carried a redistribution right the current terms withhold,
  which is the leak the rewrite closes.

  What the rewrite does not do is recall anything already fetched, and GitHub
  keeps unreferenced commits addressable by SHA. Treat this as closing the
  front door, not as an unpublish.

  The line itself: anyone may profit *using* this, nobody may profit *from*
  it. Run it for your own business, commercially, in production, without
  asking. Do not distribute it in any form, at any price.


### Added

- The rest of the delivery kit moved in: [`gate/`](gate), `charts/kargo-pipelines`,
  the CI adapters in `ci/`, ADR 0003, and a proving ground that once again runs
  the whole flow rather than half of it.

  `kargo-observability` deliberately did NOT come. It shares no contract with
  the gate or the agent, works for anyone running Kargo whether or not they
  want either, and would only have been here because it used to sit in the same
  directory. The rule for what belongs in this repository is a shared contract,
  not a shared history.

  One Go module now serves both commands, so `go test ./...` covers the gate
  and the agent in a single run -- which is the only place their contracts can
  ever be checked. Bosun is the crew for Argo and Kargo: the gate is the
  inspection round, the agent is the repair, and splitting them was reading the
  deployment topology as if it were the role.

  The agent's Dockerfile moves to `golang:1.26-alpine`: one module means the
  gate's dependencies set the floor.

  The gate itself arrived carrying everything below, developed in
  `gitops_homelab_2_0` as `delivery/gate` and previously licensed Apache 2.0.
  It is published as `ghcr.io/jamesatintegratnio/gitops-gate`, **multi-arch**
  (amd64 + arm64) -- it was amd64-only, justified by reasoning about cluster
  nodes, which the gate never runs on. Its Dockerfile builds from the
  repository root, since go.mod lives there: `docker build -f gate/Dockerfile .`

- **The gate: `render`, `diff`, `validate`.** `render` expands both levels of
  the ApplicationSet hierarchy into the flat set of Applications a cluster
  would end up with, including the bootstrap Applications themselves. `diff`
  compares two renders: it blocks on cluster-targeting changes and on a source
  changing underneath an unchanged Application, and reports version changes
  without blocking. `validate` schema-validates every rendered stream via
  kubeconform. `clusters export` regenerates the cluster inventory from live
  ArgoCD cluster Secrets, with `-check` for drift detection.

  `diff` separates a brand-new addon (`introduced`, non-blocking) from an
  existing addon gaining or losing a cluster (`targeting`, blocking). Only the
  second is the leak; blocking on the first would make every new-addon pull
  request red for no reason and train people to override the check.

  Both ApplicationSet templating dialects are supported, chosen from the
  ApplicationSet's own `goTemplate` field rather than guessed. Generators that
  cannot be expanded (git, matrix, list) produce an explicit "not covered"
  warning rather than silently reporting full coverage.

- **Source model.** A repository's manifests are obtained through a list of
  sources -- `manifests`, `helm`, `kustomize`, `argocd-bootstrap` -- which can
  be combined. The previous version understood exactly one topology (an
  app-of-apps ApplicationSet rendering a chart) and was silently blind to every
  other, including committed ApplicationSets and plain Applications, which are
  the most common ArgoCD layouts there are.

  `argocd-bootstrap` resolves its source path the way ArgoCD does: a directory
  with `Chart.yaml` is a chart, anything else is read recursively as manifests.
  The canonical gitops-bridge bootstrap is the second kind. Both the singular
  `source:` and multi-source `sources:` Application template forms are read;
  gitops-bridge uses the singular. Plain `Application` manifests are read, with
  `destination.server` resolved against the inventory so they key the same way
  generated ones do. Concurrent rendering, and `argocd:` on sources and
  clusters for fleets running more than one ArgoCD. `scope: cluster | fleet`
  for per-cluster renders, because whether an ApplicationSet expands
  fleet-wide depends on hub-and-spoke versus per-cluster ArgoCD, and guessing
  is silent. Topology fixtures cover each shape, plus a 50-cluster fleet.

- **chart-diff** (`diff -repo <path>`) -- every chart whose version moved is
  rendered at BOTH versions, with that Application's own value files and
  inline `valuesObject`, and the resources are compared. Turns "cert-manager
  moved to v1.22.0" into "adds two RBAC objects, changes six CRDs and three
  Deployments". Helm's per-object version stamps are excluded from the
  comparison: hashing them reported 101 of 105 resources as changed on one
  bump, burying the 15 that had.

- **`type: rendered`** -- reads manifests already committed to git and diffs
  them at RESOURCE level: added, removed, changed, and `apiVersion` changed
  called out separately as the one that blocks. Supports ArgoCD's source
  hydrator output, Kargo's rendered promotion branches, or any CI job that
  commits its render. See `gate/docs/rendered-manifests.md`.

- **`ReportMarker`** -- `diff -report` leads its output with
  `<!-- gitops-gate -->`, and a test asserts it on both a blocking report and a
  green one. This is a contract, not decoration: the triage finds the gate's
  verdict by searching a pull request's comments for that string. It
  previously lived in one shell script in the local proving ground and in **no
  CI adapter at all**, so a report published by CI was one no agent could
  locate.

- **Chart diff no longer reports a chart's whole resource set as removed and
  re-added** when the two versions disagree about stamping
  `metadata.namespace`. Whether a chart sets it varies between versions of the
  same chart -- podinfo omits it at 6.7.0 and sets it at 6.14.1 -- and a
  namespaced resource without it lands in the Application's destination
  namespace anyway, so that is now its identity. On a real 6.7.0 -> 6.14.1
  bump the report went from 5 added + 5 removed to the 2 resources that
  actually changed. Helm **test** hooks are excluded too: they are never
  applied by a sync, and they are the one place charts routinely generate a
  random name, so all three of podinfo's test pods appeared as added AND
  removed on every render. Other hooks are applied and are still reported.

### Changed

- `hack/extraction-test.sh` becomes `hack/portability-test.sh`. The old script
  proved `delivery/` could be lifted out of its host repository and enforced a
  one-way link rule to keep that cheap. The lift has happened, so the rule
  fails on its own fixtures; what survives is everything that was never about
  extraction -- no environment assumptions, everything renders, every link
  resolves, every unit documents itself.


### Added

- `edits.Policy.Scope` — the exact files this unit of work touched, set per
  request from the promotion's own file list. An edit outside it is refused
  even where the standing allowlist would permit it.

  The prompt has always told the model *"repository files this pull request
  may change"* and listed exactly those files. Enforcement did not: the policy
  was built once at start-up from `ALLOW_PATHS` and accepted anything under
  it — about a third of the repository in a typical install. An instruction
  where there should be a guarantee, which is the thing ADR 0001 exists to
  rule out.

  `Scope` is an exact-path test, not a glob: the promotion reports real paths,
  and widening them to patterns would hand back the looseness. Empty means
  unscoped, so callers with no notion of "the files this change touched" are
  unaffected.

  A side effect worth naming: Bosun can no longer reach its own configuration.
  `allowPaths` lives under `addons/**`, so it was previously in reach of any
  addon bump; it is not in any promotion's file list.

### Changed

- **`metrics-port-moved-under-a-netpol` is now an escalation, not a mechanical
  fix.** It was the only case whose fix lands in a different file from the
  bump, and it had neither guardrail: the file is never in the promotion's
  list, and the value is a *port*, which `versionish` does not cover, so an
  invented one would have been written.

- `evals.Case.Changed` separates "what the repository contains" from "what the
  promotion rewrote". Conflating them is why no fixture could model reality —
  three did not. `metrics-port-moved-under-a-netpol` listed only the
  NetworkPolicy the live pipeline never sends; `authentik-illegal-version-skip`
  and `unrelated-preexisting-failure` named the production addons.yaml when
  both charts are pinned in the control-plane layer. The eval harness and the
  proving ground both send `Changed` now, so what the suite measures and what
  the pipeline does cannot drift.

## [0.1.0] - 2026-08-23

First release from the standalone repository. Extracted from
`gitops_homelab_2_0`, where this was developed as `delivery/` and called
`delivery-agent` until 2026-08-23. Now licensed
[PolyForm Internal Use 1.0.0](LICENSE) rather than Apache 2.0.

### Added

- `AGENT_BRAND` / `AGENT_BRAND_MARK`. Comments lead with the mark and name, so
  a reader knows it is a bot before reaching the verdict rather than after,
  and the footer names the model and says "automated triage, not a review".
  The attempt label follows the brand too -- it was hardcoded to
  `bosun/attempt-`, and since the attempt CAP counts those labels, a
  rename would have silently reset the cap.

- `gitprovider.Gitea`. Gitea's API is deliberately GitHub-shaped, so most of
  it is the same request against a different base — but three places are not,
  and each fails silently rather than loudly: there is no check-runs API, so
  everything including Gitea Actions reports as a commit status; labels attach
  by numeric ID on older versions, and posting names returns 200 and attaches
  nothing, which would break the attempt cap into an infinite loop; and
  self-hosted is the normal case, so the instance URL is required and a
  self-signed certificate is expressible via `GIT_INSECURE_SKIP_TLS_VERIFY`.
- `GIT_INSECURE_SKIP_TLS_VERIFY`. Scoped to the git client and to the clone it
  pushes from — never to the process, and never to a global git config.

- `POST /v1/promotion-opened` — answers `202` immediately and triages
  asynchronously. Kargo's `http` step is synchronous, so a blocking handler
  would put a model round trip inside every promotion's critical path.
  Duplicate calls for the same pull request collapse, because a retried step
  must not start a second triage.
- `llm.Provider` with OpenAI chat-completions and Anthropic Messages
  implementations. `baseURL` is configuration, so self-hosted endpoints
  (LM Studio, Ollama, vLLM) are a first-class path.
- `gitprovider.Provider` with a GitHub implementation. GitLab and Bitbucket
  are documented extension points.
- `edits` — deterministic application behind a path allowlist, a `from`-value
  match, and a corroboration check on version-shaped values.
- `evals` — nine triage cases taken from real incidents, with a harness that
  scores classification, applied edits, and separately whether anything
  **unsafe** landed.

### Notes

- Measured on a 9B local model: 8/9 classification, 8/9 full pass, 0 unsafe.
  The scalar inventory is what makes that possible — 6/9 without it.
- A `mechanical` verdict whose edits are all refused escalates rather than
  reporting success. This is what turns model miscalibration into a safe
  outcome automatically.
- The model is never given file-edit tools, and the deny-list refuses CI
  config, the gate, and the merge policy regardless of how the allowlist is
  configured.
