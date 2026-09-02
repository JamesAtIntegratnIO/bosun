# Changelog

All notable changes to the `bosun` chart. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

Every version below 0.9.2 reached `main` but never reached the registry: the
oldest chart ghcr serves is 0.9.2. The workflow published on a `v*` tag until
[`1266a07`](https://github.com/JamesAtIntegratnIO/bosun/commit/1266a07) moved
it to publish on a push to `main`. Most of those versions carry a `v*` tag
with no artifact behind it; 0.6.0 was never tagged at all. Entries marked
**never published** were bumped on a branch and bumped again before merging,
so the version after them is what shipped.

## [0.35.0]

**No values change, and no template change.** `appVersion` moves, which is what
this release is: the agent it deploys serves two more MCP tools. An install that
has `mcp.enabled: false`, which is every install that has not said otherwise, is
unaffected in every respect.

### Added

- **`handoff_queue` on the MCP surface.** Every open pull request the last gate
  sweep saw carrying the `needs-human` label -- the label the agent applies when
  it stops short of a mechanical fix -- each with the verdict standing against
  its head commit, every finding behind it, and the automatic fix attempts it
  has already spent against its cap. It answers from the sweep's snapshot like
  every other tool here, so it reaches no cluster, no git host and no model.

  An empty queue is published only by a sweep that actually listed pull
  requests. A sweep that could not says so in a field of its own, because
  "nobody is waiting on you" is the one answer somebody acts on by going home.

  The disclosure notes gain their entry: what this tool reveals is what
  `gate_verdict` reveals, about the pull requests the agent gave up on.

- **`inventory` on the MCP surface.** Every Application the last live reading of
  ArgoCD served, with the cluster each one lands on -- the answer to "where does
  this run" that otherwise needs a cluster credential of its own. Names and
  clusters only: no manifest, no values file and no rendered object crosses that
  boundary.

  Its age is not the sweep's. The reading is made when the gate renders a pull
  request, so an install with none open makes none, and every row carries when
  it was observed rather than borrowing the sweep's stamp.

  The disclosure notes gain their entry, and it is the one worth reading twice
  before publishing the port: unlike every other tool this one is not scoped to
  the gated repository, so it lists every Application the ArgoCD account you
  gave bosun can see.

## [0.34.1]

**Prose only.** No values change, no template change, and `appVersion` does not
move: the chart deploys the same image 0.34.0 does.

### Changed

- **The MCP surface's section points at its own documentation page.** The four
  tools, the honest-absence behaviour, the token and what publishing the port
  discloses now live on one page beside the supervisor's, rather than being
  spread across this README, a package doc and the safety model. This section
  keeps the values, the refusals and the network path, which are what somebody
  reading a chart README came for, and links to the rest.

## [0.34.0]

**No values change, and no template change.** `appVersion` moves, which is what
this release is: the agent it deploys serves two more MCP tools. An install
that has `mcp.enabled: false`, which is every install that has not said
otherwise, is unaffected in every respect.

### Added

- **`gate_status` and `triage_status` on the MCP surface.** The queue, and what
  the agent is doing about one of it. `gate_status` answers what the last sweep
  saw across every open pull request -- each with the state standing against its
  head commit, whether it blocks, and the blocker breakdown as counts -- and,
  when the sweep could not list pull requests at all, the error that stopped it.
  A gate that cannot reach your git host otherwise has one symptom a caller can
  see, which is a queue that reads empty forever.

  `triage_status` answers what the agent is doing on one pull request now: the
  phase, how many automatic fix attempts it has spent against its cap, and the
  labels standing on the pull request. That is the difference between an agent
  still working and one that has finished and will not try again -- the same
  distinction its commit status draws for a person.

  Both compute nothing: the answers come from the snapshots the sweeps already
  hold, so they cost no git API call, no cluster read and no model call.

  **They reveal what the status page already reveals**, to whoever holds the
  token, plus one thing it does not: the labels standing on a pull request.
  Those are operational metadata like the rest -- the same strings anybody with
  read access to the repository can see -- and they travel tagged as text
  somebody else wrote, because anyone who can label a pull request chooses them.

## [0.33.0]

**No values change, and no template change.** `appVersion` moves, which is what
this release is: the agent it deploys serves a second MCP tool. An install that
has `mcp.enabled: false`, which is every install that has not said otherwise,
is unaffected in every respect.

### Added

- **`gate_verdict` on the MCP surface.** An engineer's coding agent can now ask
  why a pull request is blocked and get the answer as data: the blocker
  breakdown as counts per kind, every finding behind those counts, and the
  dropped API versions as fields -- which definition, which versions it stopped
  serving, which one survives, and the kind of manifest that has to move.
  Findings say whether an edit in the repository could clear them, so a caller
  stops hunting for one that does not exist.

  Like `pipeline_report`, it computes nothing: the answer comes from the
  snapshot the last gate sweep already holds, so it costs no git API call, no
  cluster read and no model call.

  **It reveals what the report comment on a pull request already reveals**, to
  whoever holds the token: your Application and object names, chart versions,
  helm and schema error strings, and pull-request titles. It is the same
  operational metadata the status page renders, in a shape a program can read.

## [0.32.0]

`appVersion` moves with it: this is the chart for the agent that serves the
sweep's findings to an agent over MCP.

### Added

- **`mcp`, a read-only MCP surface on a third port.** One tool today,
  `pipeline_report`, answering with the last sweep's findings as typed values
  an agent can branch on rather than markdown it has to parse. It computes
  nothing -- every answer comes from the snapshot the sweep already holds --
  so a request costs no git API call, no cluster read and no model call, and
  nothing it serves can change anything.

  **Off by default, and that is not a preference the way `web.enabled` is.**
  Upgrading an install must not open a new programmatic API on it. `helm
  upgrade` over 0.31.0 changes nothing: no port on the Service, no peer in the
  NetworkPolicy, no variable in the Deployment.

  **The listener does not start without a token.** `mcp.existingSecret` names
  the Secret holding it, this chart refuses to render without one, and the
  binary refuses to start the listener without one on top of that -- the
  second is for the Deployment somebody edits by hand. That is deliberately
  unlike `promotionAuth`, whose caller is Kargo inside the cluster and whose
  unauthenticated form predates that setting; this surface is built to be
  reached from outside the cluster, where "a token nobody set" and "an open
  API" are the same thing. The token reads through the same path as every
  other credential, so `credentials.mountAsFiles` and a mounted Secret's
  trailing newline work without special handling.

  `mcp.dangerouslyServeWithoutAuthentication` is the way past that, and it is
  spelled to be uncomfortable to type. It exists so the one operator who
  genuinely wants an unauthenticated read API -- behind a gateway that already
  authenticates, say -- says so on purpose rather than discovering that an
  empty token works.

  `mcp.allowFrom` is who may reach the port when the NetworkPolicy is on, empty
  by default and refused at render time while the surface is enabled: the same
  shape and the same honest default as `web.allowFrom`.

- **`service.mcpPort`** (default 8082), its own port for the reason `webPort`
  is: both a NetworkPolicy and a gateway draw their lines at the port, so a
  read-only surface stays smaller than the endpoint that spends money and
  writes to your repository only if the two never share a listener.

## [0.31.0]

`appVersion` moves with it: this is the chart for the agent that reads the
AnalysisRun behind a stopped Stage and the freight behind a wedged one.

### Removed

- **BREAKING: `projects` and `analysistemplates` leave the ClusterRole.**
  Neither has ever been read by anything. Both arrived whole in the commit
  that extracted this chart from the repository it grew up in -- `git log -L`
  over that rule block returns exactly one commit -- so they were copied, not
  reserved for a plan.

  **Breaking because narrowing a published ClusterRole is.** An install that
  has bound its own ServiceAccount to this role and leaned on either grant
  loses it on upgrade. Bosun itself does not, at any values setting.

  Which direction the change runs is the whole argument for doing it now
  rather than leaving two harmless-looking lines in place. Widening this role
  is a patch and narrowing it is a breaking change, so granting a resource
  against a feature nobody has written yet books a breaking change for
  something that may never arrive. `freights` and `analysisruns` are the
  counter-example and they stay: 0.31.0 is the release that reads them.

### Changed

- **`freights` and `analysisruns` are now load-bearing**, having been granted
  and unused since the chart existed. The supervisor GETs one object of each,
  by a reference the Stage itself records, so a stopped Stage's finding names
  the metric that stopped it and the artifact it is holding rather than
  handing back a `kubectl` command that asks.

  **Only matters to an install with `rbac.create: false`.** A hand-written
  role without these two does not fail: every finding is produced exactly as
  before, minus the detail, and the sweep's report carries a note saying which
  read was refused. Adding them turns the detail back on.

## [0.30.0]

### Added

- **`web.theme`: which of the two treatments the page renders in.** `auto`
  (the default, and what the page did before this existed) follows the
  reader's own system preference; `dark` and `light` stamp `data-theme` on the
  document and beat that preference in both directions. Both are the palette
  from the project's site.

  ```yaml
  web:
    theme: dark
  ```

  **An operator's setting because the page cannot offer the reader one.** A
  toggle needs somewhere to remember the answer and this page has nowhere: it
  carries no script, which is what lets a gateway put a strict content policy
  in front of it, and it refreshes itself every minute, so the CSS-only toggle
  that works without script would be wiped on every refresh. Set it when the
  reader's preference is the wrong input -- a wall-mounted dashboard, or a
  screenshot that has to look the same for everyone.

  A value that is not one of the three is refused twice: by the schema at
  render time, so a typo fails `helm template` rather than reaching a pod, and
  by the agent at start-up if `WEB_THEME` is set directly.

- **`appVersion` 0.30.0**, for the value above.

## [0.29.1]

- **`appVersion` 0.29.1: the status page wears the project's own colours.**
  0.29.0 shipped it in GitHub's palette with an anchor emoji for a mark, which
  made this page a second place Bosun's branding was decided. It is not one.
  Every colour now maps to a token in the site's `theme.css`, dark is the base
  for the site's stated reason -- the badge is navy -- and the header and
  favicon are the site's own mark, served from `/mark.svg`. Two tests fail if
  either drifts from `site/`. No chart template or value changes; the version
  moves because the image does.

## [0.29.0]

### Added

- **`web`: the status page, and the two ways to publish it.** The pipeline
  report renders itself as a page now, and this is where it gets a port, a
  route and a network rule.

  It listens on `service.webPort` (8081), and the second port is the whole
  design rather than a detail. `service.port` also answers
  `POST /v1/promotion-opened`, the endpoint that names the pull request the
  agent edits and the files it reads into a published prompt. Both a
  NetworkPolicy and a gateway draw their lines at the port, so "publish the
  read-only page" is a smaller decision than "publish the endpoint that spends
  money and writes to your repository" only because the two never share a
  listener. Every route this chart renders targets `webPort`; nothing renders a
  route for `service.port` and nothing ever will.

  ```yaml
  web:
    httpRoute:
      enabled: true
      parentRefs: [{name: external, namespace: gateway-system, sectionName: https}]
      hostnames: [bosun.example.com]
    allowFrom:
      - namespace: gateway-system
        podSelector: {app: envoy}
  ```

  **`httpRoute` first, `ingress` second, and no nginx-specific anything.**
  Ingress is feature-frozen: past a host and a path, every behaviour is a
  controller annotation and the manifest stops describing what it does, and
  `ingress-nginx` is in maintenance mode besides. The `ingress` block exists
  for clusters without Gateway API and is otherwise the second choice.

  **`web.allowFrom` is empty by default, so enabling the page publishes it to
  nobody** until you name where from. That is the honest default for a surface
  whose entire purpose is being reached from outside the namespace, and each
  entry ANDs its namespace with its podSelector, the same shape as
  `kargoPodSelector`.

  The page has no authentication of its own. It reveals operational state --
  the repository's name, open pull request titles, Stage and Warehouse names,
  findings and remedies -- and no credential, prompt or rendered diff.
  Whatever your gateway puts in front of it is the authentication.

- **Five values are refused rather than rendered**, each because what it
  produces points nowhere near its own cause: `httpRoute.enabled` with no
  `parentRefs` (a route with no parent attaches to nothing and renders clean),
  `ingress.enabled` with no `className` (an unclassed Ingress is claimed by
  whichever controller claims those) or no `hosts` (an Ingress with no rules is
  accepted and routes nothing), a published route with `networkPolicy.enabled`
  and `allowFrom` empty (a timeout that blames the gateway), and a route with
  `web.enabled: false` (nothing listening behind it).

- **`AGENT_VERSION`.** The chart passes its own `appVersion` to the pod, which
  is what puts a version on the status page: the image is built without its
  `.git` directory, so the binary cannot prove its own and the chart is the one
  thing that knows.

### Changed

- **`appVersion` 0.29.0**, for the page and the second listener.

## [0.28.2]

- **`appVersion` 0.28.2: the values mark stops firing on the addon's own
  name.** 0.28.0's first live report filed a kyverno bump's aggregation-label
  churn under "Values this repository sets", because the repository's values
  say `kyverno` and the mark's substring form found it inside every label the
  chart stamps with the addon's name. An Application's identity tokens --
  chart, release, App, namespace -- are now equality-only. No chart template
  or value changes; the version moves because the image does.

## [0.28.1]

- **`appVersion` 0.28.1: a `values.yaml` in a directory source no longer reds
  every pull request.** A document declaring no `kind` is not offered to
  kubeconform, so it cannot count toward the schema blocker; skipped
  documents are named and counted in the report instead. No chart template or
  value changes; the version moves because the image does.

## [0.28.0]

- **`appVersion` 0.28.0: the gate report stops burying its signal.** Field
  diffs align scalar lists (one inserted flag is one `gained` line, not a
  shifted array), drop namespace stamps that change no applied byte, and
  surface the fields whose values the repository itself sets above a fold
  whose summary -- `N fields, none of them a value this repository sets` --
  is the whole read for most bumps. No chart template or value changes; the
  version moves because the image does.

## [0.27.0]

### Changed

- **`triage.structuralMigration` now covers a chart your values have
  outgrown.** The flag documented one repair: a document an apiVersion swap
  left in a shape the target CRD schema rejects. It now covers the mirror case
  as well, which is the more common one. A chart version that adds
  `values.schema.json`, or tightens the one it had, refuses settings a
  repository has been making for years, and helm checks that schema before it
  templates anything, so the Application does not render at all.

  The same shape and one more guarantee: the chart is pulled and **rendered
  with the answer** before anything is written, which is a check the document
  path cannot have. What lands is a plan of key operations applied on each
  key's own lines, so the comments and formatting in a values file survive. A
  key the new schema requires and names no value for is escalated with the key
  named, before the model is asked anything.

  No new value and nothing to set: an install with the flag on already has it,
  and one with it off is unaffected. It needs the same registry egress the
  document path does, and unlike that path it does not need `liveReads`,
  because both schemas come from the chart. See
  [ADR 0013](https://github.com/JamesAtIntegratnIO/bosun/blob/main/adr/0013-a-values-migration-is-a-plan-not-a-document.md).

- **`appVersion` moves to 0.27.0, which is what makes 0.26.0's chart mean
  anything.** The derivation work shipped a chart that still named the agent
  from before it, so this release is the one that carries both it and the
  values migration.

## [0.26.0]

### Added

- **`gate.concurrency` and `gate.validate.*`.** How hard the gate works and
  what it schema-checks are now values here rather than keys in the gated
  repository's own config file, on the same line the egress deny-list is on:
  the renders happen in this pod, against this pod's limits, beside every other
  open pull request's, so they are decisions about your cluster rather than
  about the repository under review.

  Every key is `null` or empty by default, and unset means "leave the gated
  repository's file alone" -- an install that configured either in its
  `.gitops-gate.yaml` keeps exactly what it had. The environment variable is
  emitted only when the value is set, because an empty string reads as `false`
  and would have switched validation off for precisely those installs.
  `concurrency` is capped at 32 whatever either side asks for.

### Changed

- **The ArgoCD account needs two more read lines.** Alongside
  `clusters, get`, it now needs `applications, get, */*` and
  `applicationsets, get, */*` in `argocd-rbac-cm`. The gate derives what the
  repository deploys from what ArgoCD serves rather than from a file the
  repository keeps in step by hand
  ([ADR 0012](https://bosun.integratn.io/decisions/0012-the-repo-stops-repeating-the-ship/)),
  and without them it refuses to run rather than rendering a scope it could not
  see. The refusal names the exact line to add.

  **This is a breaking upgrade for the RBAC, and only for the RBAC.** No
  Kubernetes RBAC changes, no new credential, no new mount: the same account
  token, with two more lines beside the one it already has. An install that
  upgrades without adding them gets an `error` status on open pull requests
  naming the missing policy, which is the loud direction to be wrong in.

## [0.25.1]

### Fixed

- **The ClusterRole was not YAML when `liveReads.enabled` was true.** A
  template comment closed with `-}}`, which trims the following newline along
  with the indentation, so the first live-reads rule began on the end of the
  previous rule's line. Any install with the feature on failed to render, on
  0.24.0 and 0.25.0 both. `liveReads` is off by default, so a default install
  was never affected.

  Nothing caught it because nothing rendered it. `helm lint`, the schema check
  and the portability test all render `ci/lint-values.yaml`, helm merges every
  `ci/*-values.yaml` with repeated `-f`, and so those files describe exactly
  one install: the default one, with this feature off. It surfaced when a real
  consumer's values were rendered by hand.

  `hack/portability-test.sh` now renders the default-off branches too, and
  since helm parses what it renders, an unparseable document fails the check.
  Verified against the broken template before the fix went in.

## [0.25.0]

### Changed

- **`appVersion` moves to 0.25.0, which is the point of this bump.** 0.24.0
  carried the chart half of a security review and deliberately left
  `appVersion` alone, so it published pointing at an image built before that
  review: the NetworkPolicy, RBAC and schema changes landed and none of the
  agent's did. A consumer leaving `image.tag` unset, which is what this chart
  expects, got the new policy around the old binary.

  Nothing in the templates changed here. Take this version rather than 0.24.0;
  0.24.0 is not wrong, it is half-applied.

  **The agent skips 0.24.0.** Chart 0.24.0 is already published meaning "the
  security chart, on the old agent", so releasing an agent 0.24.0 would leave
  two different things answering to that number and only one of them carrying
  the agent fixes. The chart and the agent are versioned independently and this
  is the case where saying so out loud is cheaper than the collision: both are
  0.25.0, there is no v0.24.0 tag, and there never will be.

  The agent side is a breaking change of its own, and the root changelog is
  where it is written down: internal address space is closed to outbound
  requests unless named in `triage.egressAllowPrivate`, and the edit scope is
  read from the pull request's diff rather than from the promotion body.

## [0.24.0]

A security review of the chart. `appVersion` stays at 0.23.0: nothing here cuts
a release. **Superseded by 0.25.0**, which is the same chart pointed at an
image that has the matching agent fixes.

### Added

- **`triage.egressAllowPrivate`.** Private address space is now closed in the
  agent itself, at the dial, whatever the NetworkPolicy permits: loopback,
  link-local, the RFC1918 blocks, CGNAT and the IPv6 equivalents. Nothing
  removes that list, so this is how an operator whose chart repository,
  registry or proxy sits on their own network names it. Without the entry the
  request is refused, the log names the network, and the brief says it had no
  evidence. Rendered as `EGRESS_ALLOW_PRIVATE`, joined with commas, beside the
  `triage.egressDeny` it complements.

- **`credentials.mountAsFiles`** (default `false`). Projects the git token, the
  GitHub App private key, the model API key, the ArgoCD token and the promotion
  token out of the same Secrets they already name into read-only files under
  `/etc/bosun/credentials`, and sets `GIT_TOKEN_FILE` and friends instead of the
  environment variables. An environment variable is readable through
  `kubectl exec -- env`, `/proc/<pid>/environ` and a crash dump, and every child
  process inherits it; this agent shells out to git and to helm.

  Exactly one form is rendered per credential. The agent refuses to start when
  both are set, so there is no upgrade window where the two disagree.

  Off by default because of the image rather than the risk: the `_FILE`
  variants are read by the agent, so an image without them treats every
  credential as unset and the pod refuses to start over configuration that
  looks present. Turn it on once your `image.tag` reads them.

  The volume is mode `0444`, not `0400`. A secret volume's files are owned by
  root unless a pod `fsGroup` says otherwise and this pod runs as 10001, so
  `0400` would be a credential the process cannot open.

- **`networkPolicy.kargoPodSelector`** (default `{}`, today's behaviour). The
  ingress rule admitted every pod in `kargoNamespace`, not the Kargo
  controller, because a peer with only a `namespaceSelector` means the
  namespace. Set the controller's labels to narrow it; both selectors go in one
  peer so they are ANDed. `promotionAuth` answers the same question at the
  application layer, and neither replaces the other.

- **`networkPolicy.egress.dnsPodSelector`** (default `{}`, today's behaviour).
  The same narrowing for the DNS rule, from `dnsNamespace` to the resolver's
  own pods.

- **`metrics.serviceMonitor.namespace`.** The namespace Prometheus scrapes
  from. **Required when the ServiceMonitor and the NetworkPolicy are both
  enabled**, and refused at render time when it is missing.

### Changed

- **The scrape's ingress rule names a namespace.** It was
  `namespaceSelector: {}` with `app.kubernetes.io/name: prometheus`, and a pod
  label is chosen by whoever creates the pod: any workload in any namespace
  could label itself that way and reach the port. The port serves the whole
  HTTP surface, `POST /v1/promotion-opened` included, so the rule admitted
  exactly what the rule above it exists to restrict. Serving `/metrics` on a
  container port of its own would make the question smaller, and that is a
  change in the service rather than in this chart.

  **Breaking for an install with `metrics.serviceMonitor.enabled` and no
  `namespace`**: the render fails naming the value. One line to fix, and it
  fails loudly rather than continuing to admit the cluster.

- **`allowPublicHTTPS` excepts link-local and CGNAT** as well as the three
  RFC1918 blocks: `169.254.0.0/16`, which contains the cloud metadata service
  at 169.254.169.254, and `100.64.0.0/10`, which is pod or node addressing on
  more than one managed platform. The list now matches
  `egress.DefaultDenyNetworks` in the service.

- **The pods, events and apps-workload reads are tied to `liveReads`.** They
  were unconditional under `rbac.create`, so an install with `liveReads` and
  `supervise` off still granted cluster-wide read of every pod spec, and
  third-party pod specs routinely carry secret material as literal env values.
  No code path reads them with `liveReads` off: the supervisor uses the Kargo
  reads, and `CountLive` only ever visits the `<plural>.<group>` coordinates the
  gate's report names, which are never the core group. The Kargo and argoproj
  reads stay unconditional; they are what the chart is for.

  Not breaking: nothing that reads them runs with the feature off.

- **`values.schema.json` is strict at the top level.** `additionalProperties`
  was true there, so `livereads` for `liveReads` or `networkpolicy` for
  `networkPolicy` validated cleanly and silently left a default-on feature on.
  Helm's own injected keys (`global`, and the `enabled` a parent chart's
  `condition` sets) are enumerated, and `metrics` gets the properties it never
  had.

- **The DNS egress rule says why it is there.** helm runs as a subprocess over
  pull-request content and a chart can call sprig's `getHostByName`; helm
  resolves it and the answer renders into the published report, which makes an
  arbitrary name lookup an exfiltration channel out of a pod holding a git
  token, a model key and an App key. No Go process can portably impose a
  resolver on a child, so this rule is the only place it can be pointed
  somewhere accountable. Nothing else in the file opens 53. What remains is the
  cluster resolver's own forwarding, which is a policy in your DNS release.

## [0.23.2]

### Changed

- **Comments in the templates and descriptions in `values.schema.json`** get
  the same voice pass the README and `values.yaml` already had. The Helm
  template comments were missed the first time because they are
  `{{- /* ... */ -}}` rather than `#`. No template, value, default or schema
  constraint changed, and `appVersion` stays at 0.23.0.

## [0.23.1]

### Changed

- **Documentation, throughout.** `README.md` and the comments in `values.yaml`
  and the templates lose the em dashes, emphasis capitals and filler adverbs.
  Two corrections came out of the reading: the `liveReads` brief sample now
  matches what `agent/live.go` emits, and the `git.app` note no longer opens on
  a dash fragment. No template, value or default changed, and `appVersion` stays
  at 0.23.0: the version moves because the README and values ship inside the
  published chart, and 0.23.0 is already in the registry.

## [0.23.0]

### Added

- **`promotionAuth.existingSecret` / `promotionAuth.tokenKey`.** The bearer
  token `POST /v1/promotion-opened` requires. That endpoint's payload names the
  pull request the agent edits and the files it reads into a prompt it
  publishes, and the NetworkPolicy admits the whole namespace — which is as
  narrow as a NetworkPolicy gets.

  Opt-in, so an upgrade does not stop answering Kargo. Left unset the endpoint
  is open and the pod logs a warning saying so at every start-up. Set the same
  value on the promotion side with kargo-pipelines `triage.authorization`:

  ```yaml
  # bosun
  promotionAuth:
    existingSecret: bosun-promotion
    tokenKey: token
  ```

- **`maxConcurrentTriage`** (default `4`). Bounds simultaneous triages. Each is
  a clone, a helm render and a model call, so the ceiling is about the pod's
  memory and your git host's rate limit rather than throughput.

### Changed

- **BREAKING (agent, not values): `git.apiBase` is required for `gitea`.** There
  is no public Gitea to default to, so an empty value was a pod that started
  healthy and could not read a pull request. The process now refuses to start
  and names the setting.

- **BREAKING (agent, not values): an invalid boolean is a configuration error.**
  `explainGreen: treu` read as `false`, so a setting somebody deliberately
  turned on was silently off. Accepted words are unchanged
  (`true/false`, `yes/no`, `on/off`, `1/0`); anything else fails at start-up.

- **BREAKING (agent, not values): `gate.poll` must be positive.** `0` is not a
  faster poll but no wait at all, and it spun the sweep against the git host's
  API as fast as it answered.

  No values file needs editing for any of the three unless it already carries
  one of those mistakes, in which case it was not doing what it looked like.

## [0.22.0]

### Removed

- **BREAKING: `gate.mode`, `gate.wait`, `gate.reportAuthor` and
  `gate.inventorySource` are gone.** The agent is the gate, and the gate reads
  its cluster inventory from the ArgoCD API. There is no second placement and no
  second source. See
  [ADR 0009](https://github.com/JamesAtIntegratnIO/bosun/blob/main/adr/0009-one-gate-one-inventory.md).

  Migration:

  ```diff
   gate:
  -  mode: cluster
  -  wait: 10m
  -  reportAuthor: ""
  -  inventorySource: secrets
     argocd:
  +    baseURL: https://argocd-server.argocd.svc
  +    existingSecret: bosun-argocd
  ```

  `gate` is `additionalProperties: false`, so a values file carrying any of the
  four keys fails at install time naming the key, rather than quietly
  configuring nothing.

  If you were on `gate.mode: ci`, the CI half has no replacement in this chart:
  the agent gates every open pull request itself, under the same `gate.checkName`
  status, so branch protection does not change — but a repository taking **fork**
  pull requests has only `gate.forkPRs` to decide with, since the render runs
  in-cluster.

- **The namespaced Role and RoleBinding on the ArgoCD namespace.** This chart
  now creates no Role or ClusterRole granting `get`/`list` on Secrets anywhere.
  It was the one grant in here worth stopping on and it could not be made
  smaller: RBAC has no predicate for "the labels but not the data", so a token
  that could read the cluster labels could read `argocd-secret` beside them.
  `GET /api/v1/clusters` serves the same four fields with the credential block
  redacted, so the authorisation happens somewhere that can draw the line.

### Changed

- **BREAKING: `gate.argocd.baseURL` and `gate.argocd.existingSecret` are
  required, and `baseURL` has no default.** They are how the gate reads its
  inventory, and there is no other way now.

  `baseURL` lost its `https://argocd-server.argocd.svc` default on purpose. It
  is a fact about your install, and a plausible-but-wrong address does not fail
  where you would look for it: the connection hangs for the full HTTP timeout
  and the pod dies at start-up saying argocd-server is unreachable. A required
  value fails at `helm upgrade`, naming the value.

  Mint the token, and give the account one line in `argocd-rbac-cm`:

  ```bash
  argocd account generate-token --account bosun
  ```

  ```
  p, bosun, clusters, get, *, allow
  ```

- **The apiserver egress rules follow `supervise.enabled` rather than
  `gate.mode`.** The pipeline sweep and `liveReads` are the two readers of the
  apiserver now; the gate is not one of them. Both default on, so a default
  install renders the same rules it did before.

## [0.21.0]

### Changed

- **BREAKING: `gate.argocd.port` is now `gate.argocd.podPort`, and the default
  moves from `443` to `8080`.**

  Migration, if you set the old key:

  ```diff
  -    port: 443
  +    podPort: 8080
  ```

  The value is written into this chart's NetworkPolicy egress rule to the
  ArgoCD namespace, and it has to be **argocd-server's container port**, not
  the Service port that appears in `gate.argocd.baseURL`. A NetworkPolicy
  matches the destination port of the packet, and a ClusterIP is DNAT'd to
  the backend pod's port *before* policy is evaluated — so by the time the
  rule is matched the packet is addressed to the pod, on 8080, whatever port
  the Service published.

  Both halves of this were wrong. `443` is not argocd-server's container port
  in any standard install, and the comment above it read as though `port`
  belonged to `baseURL` — so setting the two consistently, at 80 or at 443,
  was the natural thing to do, and it renders clean, passes `helm lint`,
  passes the schema and then drops every packet. There is no error at either
  end: the connection hangs for the full HTTP timeout and the pod dies at
  start-up saying argocd-server is unreachable, which is true and points
  nowhere near the values file.

  **Renamed rather than just re-defaulted**, deliberately, and the rename is
  the safer of the two. `gate.argocd` is `additionalProperties: false` in
  `values.schema.json`, so an existing values file carrying `port:` now fails
  at install time with `additional properties 'port' not allowed` — an error
  in front of whoever is running the upgrade. Keeping the name and changing
  the default would have silently overridden a deliberate `443` for anyone who
  had one, and silently left a wrong `80` in place for everyone who had that;
  either way the operator learns nothing. `podPort` also says what the value
  is, which `port` never did: it was added in 0.20.0, has had one release to
  acquire consumers, and this is the last cheap moment to fix the name.

  `8080` is argocd-server's container port in the upstream argo-cd chart and
  does not move with `server.insecure` — the Service publishes both 80 and 443
  against the same container port.

### Added

- **The chart README documents the half this chart cannot write.** It emits
  bosun's *egress* to the ArgoCD namespace; argocd-server's *ingress* policy
  lives in your ArgoCD release, and until both exist the connection is dropped
  with nothing logged at either end — the same argument the README already
  makes for the Kargo controller's egress, and now made in the same place,
  with a copy-pasteable rule that names the pod port for the same reason.

- **A worked example for each of the two common installs** —
  `server.insecure: true` behind a gateway, and argocd-server terminating its
  own TLS. They differ only in `baseURL`; both want `podPort: 8080`, which is
  the confusion this release exists to remove.

## [0.20.1]

### Changed

- **Documentation only.** The chart's README no longer offers `gate.mode: ci`
  as the only escape from the ArgoCD Secret grant — `gate.inventorySource:
  argocd`, added in 0.20.0, removes that grant without leaving cluster mode,
  and the `gate.mode: ci` entry now describes what it is actually for.

  No template, value or default changed; the chart version moves with
  `appVersion`, as in 0.19.0.

## [0.20.0]

### Added

- **`gate.inventorySource`** — `secrets` (the default, unchanged behaviour) or
  `argocd`. The cluster-mode inventory can now come from `GET /api/v1/clusters`
  on the ArgoCD API instead of from the cluster Secrets, and **when it does,
  the namespaced Role on the ArgoCD namespace is no longer created.**

  The Secret grant cannot be made smaller. The gate reads four fields — name,
  server, labels, annotations — and RBAC has no predicate for "the labels but
  not the data": there are no deny rules, `resourceNames` does not apply to
  `list`, and the label selector the gate sends is a filter the apiserver
  applies *after* authorising. ArgoCD's API draws that line, serving the same
  four fields with the credential block redacted.

  It is a trade rather than a win, and both halves are stated in `values.yaml`:
  an ArgoCD account token to mint, store and rotate (`clusters, get` and
  nothing else), a component that can be down on its own, and a second TLS
  story — `gate.argocd.caSecret` or `gate.argocd.insecureSkipTLSVerify`,
  because argocd-server serves its own certificate. The chart emits the
  NetworkPolicy egress rule for the ArgoCD namespace itself; argocd-server is
  a ClusterIP, and forgetting it hangs with zero bytes.

## [0.19.0]

### Changed

- **`home` points at [bosun.integratn.io](https://bosun.integratn.io)**, the
  documentation site, rather than at the source repository — which is what
  `sources` already says. No template, value or default changed; the chart
  version moves with `appVersion`.

## [0.18.5] - 2026-08-28

### Changed

- **appVersion 0.18.5.** The agent advances the pull request's head SHA after
  it pushes a repair, so the status it writes afterwards lands on the commit
  that is now the branch head. Before this the verdict went to the pre-push
  SHA, leaving the real head with a green gate and no verdict, and a required
  check that could never be satisfied. No chart template, value or default
  changed.

## [0.18.4] - 2026-08-25

### Changed

- **appVersion 0.18.4.** Carries the three gaps a re-review found in the
  security work that shipped in 0.18.x. No chart template, value or default
  changed; the fixes are in the agent image.

## [0.18.3] - 2026-08-24

### Changed

- **appVersion 0.18.3.** The supervisor stops reporting a merged pull request
  as a lost one. No chart template, value or default changed.

## [0.18.2] - 2026-08-24

### Changed

- **appVersion 0.18.2.** Every finding kind is emitted even at zero, so an
  alert rule can compare against it and a graph can return to the axis. A
  metric that only appears when it fires cannot be alerted on. No chart
  template, value or default changed.

## [0.18.1] - 2026-08-24

### Changed

- **appVersion 0.18.1.** A failed verification is over rather than pending, and
  the finding carries `kargo.akuity.io/reverify` rather than a refresh that
  does nothing. No chart template, value or default changed.

## [0.18.0] - 2026-08-24

### Added

- **`supervise.enabled` (default `true`) and `supervise.interval` (default
  `10m`)**, rendered as `SUPERVISE_PIPELINE` and `SUPERVISE_INTERVAL`, with
  both keys constrained in `values.schema.json`. This is the pipeline sweep:
  the promotions that never happened. Nothing about a promotion that did not
  occur produces an event, so a timer is the only way to see it.

  It is read-only and needs no new permission. The Kargo read the ClusterRole
  already grants covers it, and `metrics.serviceMonitor.enabled` decides
  whether `/metrics` gets scraped.

## [0.17.2] - 2026-08-24

### Changed

- **appVersion 0.17.2.** The gate finds its own report comment by the marker it
  stamps rather than by the comment's author, so the report is still found when
  the token that posted it is not the token reading it. No chart template, value
  or default changed.

## [0.17.1] - 2026-08-24

### Changed

- **appVersion 0.17.1.** A failing commit status says why it failed rather than
  reporting a bare failure. No chart template, value or default changed.

## [0.17.0] - 2026-08-24

### Deprecated

- **`branding.mark` is ignored.** The deployment no longer sets
  `AGENT_BRAND_MARK`, and comments carry no identity header at all.
  Authenticating as a GitHub App already puts the name and avatar above every
  comment, so a bold mark underneath was the agent introducing itself twice.
  The value is still accepted, so setting it does not fail an upgrade; it does
  nothing. The footer still names the agent and says whether a model was
  involved.

## [0.16.1] - 2026-08-24

### Fixed

- **A writable `/tmp`**, as an `emptyDir` mounted beside the read-only root
  filesystem. chart-diff writes an Application's inline values to a temporary
  file before rendering, and with no writable `/tmp` that open fails `EROFS`.
  The gate does not treat it as fatal: it reports the Application as one it
  could not render at both versions, so the symptom was silently reduced
  coverage on every version bump rather than an error. In the CI placement the
  runner supplied `/tmp` and nobody had to think about it.

## [0.16.0]

### Added

- **`gate.mode`** — `cluster` (the new default) or `ci`. In cluster mode the
  agent *is* the gate: it polls the open pull requests, renders base and head
  against the live ArgoCD cluster inventory, and posts the `gate.checkName`
  status and report comment itself. Nothing to install in CI, no checked-in
  cluster inventory to go stale. `ci` is the previous behaviour, kept whole,
  for public repositories taking fork pull requests and for clusters that will
  not grant the Secret read below.

- **`gate.forkPRs`** — off by default. In cluster mode the render runs helm
  over the pull request's content, inside the cluster, so whose content that
  is should be an operator's decision. Off, a fork pull request gets an
  `error` status naming this value rather than an unreported required check.

- **A namespaced Role on the ArgoCD namespace**, created only when
  `gate.mode` is `cluster`. This is the one grant in this chart worth stopping
  on: the live inventory *is* the ArgoCD cluster Secrets, and RBAC cannot
  grant the labels without the data. `get`/`list`, that namespace only, gone
  again in `ci` mode.

### Changed

- **The default is a behaviour change on upgrade.** With `gate.mode`
  unset you get the in-cluster gate, and the agent will refuse to start if it
  cannot read the inventory — loudly, rather than gating against a world it
  cannot see. Set `gate.mode: ci` to keep exactly what 0.15.x did.

- **Apiserver egress and the ArgoCD namespace follow either switch.** They
  were conditional on `liveReads.enabled` alone; the cluster-mode gate reads
  the apiserver through the same door, so both now render when either feature
  asks for them.

## [0.15.2]

No change to this chart's surface. The version tracks `appVersion`; the agent
stopped reporting schema-dictated respellings as values it had dropped. See the
repository CHANGELOG.

## [0.15.1]

**Never published.** The chart version was bumped on a branch and bumped again
before it merged, so 0.15.2 is what reached the registry. The entry stays
because the change it describes shipped inside 0.15.2.


No change to this chart's surface -- no new value, no template change. The
version moves because it tracks `appVersion`, and the agent gained a fix: the
reshape comment's diff no longer hides a value that moved without changing
column. See the repository CHANGELOG.

## [0.15.0]

### Added

- **`networkPolicy.egress.allowInternet`** and **`triage.egressDeny`** -- reach
  the internet on 443, log every destination, forbid hosts by name.

  The allow-list it replaces was correct and was a full-time job: every chart
  repository, registry blob CDN and redirect target had to be named before the
  agent could read it, and three separate incidents in this project added a host
  after the fact. The symptom each time was a two-minute timeout and a brief
  saying it had no evidence -- the quiet failure this component exists to end.

  `allowInternet` emits `toFQDNs: [matchPattern: "*"]` on 443 for the `cilium`
  flavor; `standard` keeps `allowPublicHTTPS`, since an ipBlock cannot express
  "any name". `egressDeny` takes an exact host or a `*.suffix` pattern, and a
  pattern forbids the apex too.

  **This widens what the agent may READ, not what it may DO.** It still writes
  only to the pull request's own branch, still refuses paths on a deny-list it
  cannot configure away, and still never mutates the cluster.

## [0.14.2]

### Fixed

- The compare range is chosen by version rather than by the order the release
  list arrived in. No values change; see the agent changelog.

## [0.14.1]

### Fixed

- A structural check that fell back to the cluster's own copy of the target
  schema now says so in the comment. No values change; see the agent changelog.

## [0.14.0]

### Added

- **Upstream notes work for classic Helm repositories** -- twenty of the
  fifty-three artifacts in a real promotion target list, and previously none of
  them. No values change; see the agent changelog.

## [0.13.2]

### Fixed

- A fully-read commit range no longer reports itself as "more than could be
  read". No values change; see the agent changelog.

## [0.13.1]

### Fixed

- **Upstream notes never worked for Helm charts**, which is most of what a Kargo
  pipeline promotes, and they no longer require GitHub Releases to exist at all
  -- a changelog in the repository is read when there is no Release, and the
  commits between two tags when there is neither. No values change; see the
  agent changelog.

## [0.13.0]

### Added

- **`triage.structuralMigration`** (default `true`) and
  **`triage.migrateMaxDocs`** (default `5`) -- the second half of the
  deterministic repair, for the bumps where swapping the apiVersion is not the
  whole job.

  A chart that moves `spec.store` to `spec.secretStoreRef.name` between two
  versions leaves, after a plain swap, a document that parses, applies, and has
  that field pruned by the apiserver on the way in. The render is fine, the gate
  goes green, and the value is gone.

  The model is shown both schemas and the document and asked to translate. Every
  proposal is checked before anything is written -- identity, schema validity,
  and value provenance -- and a refusal refuses the whole push, including the
  plain swaps that were fine, because a swap alone turns the gate green over a
  document the apiserver will silently prune.

  **Needs `liveReads.enabled`** (the shape being LEFT is only in the
  CustomResourceDefinition installed right now) and egress to your chart
  registry (the shape being arrived at comes from rendering the chart at the
  target version -- the hosts the upstream-notes lookup already needs). Without
  either it degrades to the plain swap and says which check it could not make.

  Two costs, stated because they are permanent: a reshaped document is
  **re-serialised**, so comments inside it do not survive; and manifests nested
  in an `extraObjects:` list or a block scalar are **skipped and escalated**
  rather than reshaped.

### Changed

- **The agent image now carries `helm`**, the same version the gate's image
  does. Rendering has to match what the cluster's own Helm does, and two
  components rendering the same chart with different Helms is a difference
  nobody would think to look for.

## [0.12.0]

### Added

- **`liveReads`** -- read the cluster, read-only, so a brief can say what is
  RUNNING and not only what the repository declares.

  ```
  - externalsecrets.external-secrets.io on v1beta1 -- 0 live object(s)
  - Application external-secrets-host -- Degraded / OutOfSync
  ```

  The chart has shipped a read-only ClusterRole since its first release and no
  Go code had ever used it. This is that role finally being spent.

  **Off by default**, unlike everything else in this chart. The rest of what
  the agent reads is public or already in the pull request; this reads your
  cluster.

  **`liveReads.scope` has two settings because two are what RBAC can express.**
  "Everything except the core group" is the intent most people have here and it
  cannot be written down -- there are no deny rules, and `apiGroups: ["*"]`
  includes the core group, which contains Secrets. `groups` (default) grants
  only the API groups you list, so Secrets stay unreadable by construction; an
  unlisted group shows up in the brief as "not permitted to check". `wide`
  grants everything and **can read Secret contents**.

  **It needs egress this chart cannot infer.** The apiserver is
  `kubernetes.default.svc`, and a ClusterIP CANNOT be an ipBlock -- it is DNAT'd
  to a real endpoint before policy is evaluated, so a rule naming it matches
  nothing and the symptom is a hang with zero bytes. Give the real endpoints
  under `networkPolicy.egress.apiServer.ipBlocks`, or use `flavor: cilium`,
  which now emits `toEntities: [kube-apiserver]` and needs none of it. **The pod
  refuses to start if it cannot read the API**, so a missing rule is a crash
  loop with an explanation rather than a permanent quiet shrug.

  See [`adr/0006-live-reads-are-scoped-by-group.md`](../../adr/0006-live-reads-are-scoped-by-group.md).

### Changed

- `networkPolicy.egress.apiServer.ipBlocks` is a new key on an object that is
  `additionalProperties: false`. Nothing existing moves.

## [0.11.0]

**Never published.** Bumped on a branch and bumped again before it merged, so
0.12.0 is what reached the registry. The entry stays because the change it
describes shipped inside 0.12.0.

### Added

- **`triage.upstreamNotes.maxCommits`** (default `10`) -- how many upstream
  COMMITS between the two tags may reach a prompt or a comment.

  Commits answer the question release notes routinely do not. A chart drops its
  `ClusterRole` and ships a release note about performance; the render proves
  the removal and cannot explain it, and the best the agent could say was "no
  release note explains why". The commit that deleted the template says exactly
  why, in a sentence nobody wrote for a changelog.

  **No new egress.** It is `api.github.com`, the same host the gate's checks are
  read from. At most two extra calls, and only on the paths that produce prose
  for a human -- the green-gate explanation and an escalation. The mechanical
  path, the one that writes files, never reads them.

  Set it to `0` to fall back to the built-in cap; switch the whole feature off
  with `triage.upstreamNotes.enabled: false` as before.

### Fixed

- **Upstream reads were anonymous under App authentication.** The resolver was
  handed the static `GIT_TOKEN`, which App mode leaves empty by design --
  installation tokens are minted per use. So from the release that made the
  agent a GitHub App, every upstream read went out unauthenticated against
  `api.github.com`'s 60-requests-an-hour-per-IP limit, and the failure surfaced
  as "no upstream release notes", which is also what an artifact that publishes
  none looks like. The credential is now fetched per call. Rate limiting also
  says so in its own sentence rather than hiding inside "could not read the
  releases", which sends a reader off to check whether the project publishes any.

## [0.10.0]

### Added

- **`gate.reportAuthor`** -- the account whose gate report the agent will
  believe.

  The gate publishes its verdict as a pull-request comment carrying a marker,
  and until now the marker was the whole of the check. Anyone who can comment
  on the pull request can write that marker, and the report under it is what
  the agent reads to decide which manifests to rewrite, which version strings
  it will accept as corroborated, and what it tells the model actually
  rendered. A forged report is not a wrong opinion; it is an instruction
  wearing the gate's authority.

  **Empty means per-host default**, because the answer is a fact about the host
  rather than a preference:

  | Host | Default | Why |
  |---|---|---|
  | `github` | `github-actions[bot]` | a gate in GitHub Actions comments through `github.token`, so it is that account every time |
  | `gitea` | unchecked | Gitea Actions has no equivalent fixed identity -- the report arrives as whichever user minted the CI token |

  Set it to `"*"` to read the report whoever wrote it. That is the behaviour
  that existed before this value, and on some hosts it is the only expressible
  answer -- but it should be a decision in a values file rather than an
  absence.

  **If your GitHub gate does not comment through Actions** -- a bot user, a
  PAT's owner -- set this to that account. The symptom of getting it wrong is
  the agent reporting that it ignored a report and naming the author it saw,
  which is the fix instruction.

## [0.9.3] - 2026-08-23

### Changed

- **appVersion 0.9.3.** A removed CustomResourceDefinition is inspected rather
  than listed, and a legacy commit author in configuration is ignored rather
  than honoured. No chart template, value or default changed.

## [0.9.2] - 2026-08-23

### Fixed

- **`git.author.name` and `git.author.email` default to empty**, and the
  deployment sets `GIT_AUTHOR_NAME` and `GIT_AUTHOR_EMAIL` only when they carry
  a value. Empty means the agent derives its own identity, which as a GitHub
  App is `<slug>[bot]`.

  The old default was `bosun@users.noreply.github.com`. That namespace belongs
  to GitHub accounts, so every commit the first live repair pushed was
  attributed, avatar and all, to an unrelated account named `bosun`. If you set
  an email here, never use a `users.noreply.github.com` address that is not
  your bot's own.

## [0.6.0]

### Added

- **`networkPolicy.egress.fqdnPatterns`** -- Cilium `matchPattern` entries,
  merged into the same rule as `fqdns`.

  A registry needs two hosts, not one. `ghcr.io` serves the manifest and
  redirects the blob -- which is where an image's labels live, and therefore
  where the upstream-notes lookup has to go -- to
  `pkg-containers.githubusercontent.com`. Allowing only the registry host means
  that fetch hangs until it times out. Observed in production: the agent
  reported `context deadline exceeded (Client.Timeout exceeded while awaiting
  headers)` and fell back to a render-only explanation, which is indistinguishable
  from an artifact that simply has no release notes.

  Some of these hosts are a SET rather than a name -- quay serves blobs from
  `cdn01.quay.io`, `cdn02…` -- and `matchName` cannot express that without
  asserting how many there are. Guessing at the membership is how the
  allow-list silently stops covering something, so patterns are the honest
  shape.

## [0.1.0] - 2026-08-23

First release from the standalone repository. Extracted from
`gitops_homelab_2_0`, where this was developed as `delivery/` and called
`delivery-agent` until 2026-08-23. Now licensed
[PolyForm Internal Use 1.0.0](../../LICENSE) rather than Apache 2.0.

### Added

- `branding.name` and `branding.mark`. The agent signs its comments, commits
  and attempt labels with this. It is deliberately NOT the account its token
  belongs to -- give it a dedicated bot user or a GitHub App, or every comment
  carries the name of whoever minted the token and reads like a colleague's.

- `git.provider: gitea`, and `git.insecureSkipTLSVerify` for a self-hosted host
  with a private or self-signed certificate.
- `networkPolicy.egress.namespaces` — egress to an in-cluster destination by
  namespace selector. This CANNOT be expressed as an `ipBlock`: a Service's
  ClusterIP is DNAT'd to a pod IP before policy evaluation, so a rule naming
  the ClusterIP matches nothing and the connection hangs with zero bytes and
  no error. Found by running the agent against an in-cluster Gitea.

- Deployment, Service, ServiceAccount, read-only ClusterRole and NetworkPolicy
  for the in-cluster triage agent.
- `networkPolicy.flavor: cilium` additionally emits a `CiliumNetworkPolicy`
  with `toFQDNs`, which names the hosts the agent may reach rather than a range
  that happens to contain them.
- `values.schema.json`, and template-level `fail` for the cross-field
  requirements a schema cannot express.

### Notes

- **No Ingress or HTTPRoute is rendered, by design.** Only Kargo calls this
  service, and publishing something that can spend money and write to your
  repository would be gratuitous exposure.
- **RBAC is read-only.** No create, update, patch or delete verb appears
  anywhere. The agent observes the cluster and writes to pull requests.
- **The chart never creates a Secret.** It takes the name of an existing one,
  so ExternalSecret, Vault Agent, SOPS or `kubectl create` all work and none is
  assumed.
- **`llm.provider` has no default** and the chart refuses to render without it.
- **`triage.allowPaths` must be non-empty** — an empty allowlist means the
  agent could never apply a fix, and failing at render time is better than
  discovering it when a fix is silently refused.
- The Kargo controller's **own** egress policy must permit this service. A
  controller allowed `0.0.0.0/0 except RFC1918` cannot reach a ClusterIP, and
  the symptom is a hang with zero bytes rather than an error.
