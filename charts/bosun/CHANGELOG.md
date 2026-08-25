# Changelog

All notable changes to the `bosun` chart. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

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
