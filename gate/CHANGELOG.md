# Changelog

All notable changes to `gitops-gate`. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

## [Unreleased]

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

### Fixed

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

- **The image copies the module it builds.** The Dockerfile hand-picked
  `gate/` into the build stage, and the day the gate first imported a sibling
  package -- `migrate`, in the very release that shipped consumer-aware
  blocking -- the v0.9.0 image build died on `no required module provides
  package .../migrate` while CI's `go build ./...` stayed green: CI builds
  the checkout, the image builds the COPY list, and only one of them follows
  the import graph. The build stage now copies the module wholesale, as the
  agent's Dockerfile always has. The image workflow's scope filter learned
  the same lesson: `migrate/` now rebuilds the gate image too, so a change to
  the shared scanner cannot ship a stale gate.

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

### Added

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

### Changed

- Moved into the Bosun repository and now shares one Go module with the agent
  (`github.com/JamesAtIntegratnIO/bosun`, package directory `gate/`). Licensed
  **PolyForm Internal Use 1.0.0** rather than Apache 2.0.

  The point is not tidiness. The gate and the agent are joined by contracts
  nothing could test across a repository boundary -- the report marker, and the
  rule that any version the agent writes must appear verbatim in the gate's
  rendered report. The first of those was broken for the life of the project.

- Published as `ghcr.io/jamesatintegratnio/gitops-gate`, **multi-arch**
  (amd64 + arm64). It was amd64-only, justified by reasoning about cluster
  nodes -- which the gate never runs on. Its consumers are CI runners and
  developer laptops.

- The Dockerfile builds from the **repository root** now, since go.mod lives
  there: `docker build -f gate/Dockerfile .`


### Added

- `ReportMarker` — `diff -report` now leads its output with
  `<!-- gitops-gate -->`, and a test asserts it on both a blocking report and a
  green one.

  This is a contract, not decoration: a triage agent finds the gate's verdict
  by searching a pull request's comments for that string. It previously lived
  in one shell script in the local proving ground and in **no CI adapter at
  all**, so a report published by CI was one no agent could locate. Emitting it
  from the binary makes any adapter that posts the report verbatim correct by
  construction, and makes the same bug unavailable to the GitLab and Bitbucket
  adapters.

  Adapters must no longer prepend it themselves or they will publish two.

### Fixed

- Chart diff no longer reports a chart's whole resource set as removed and
  re-added when the two versions disagree about stamping
  `metadata.namespace`. Whether a chart sets it varies between versions of the
  same chart -- podinfo omits it at 6.7.0 and sets it at 6.14.1 -- and a
  namespaced resource without it lands in the Application's destination
  namespace anyway, so that is now its identity. On a real 6.7.0 -> 6.14.1
  bump the report went from 5 added + 5 removed to the 2 resources that
  actually changed.
- Helm **test** hooks are excluded. They are never applied by a sync, and they
  are the one place charts routinely generate a random name, so all three of
  podinfo's test pods appeared as added AND removed on every render. Other
  hooks are applied and are still reported.

### Added

- **chart-diff** (`diff -repo <path>`) — every chart whose version moved is
  rendered at BOTH versions, with that Application's own value files and
  inline `valuesObject`, and the resources are compared. Turns "cert-manager
  moved to v1.22.0" into "adds two RBAC objects, changes six CRDs and three
  Deployments". Helm's per-object version stamps are excluded from the
  comparison: hashing them reported 101 of 105 resources as changed on one
  bump, burying the 15 that had.

- **`type: rendered`** — reads manifests already committed to git and diffs
  them at RESOURCE level: added, removed, changed, and `apiVersion` changed
  called out separately as the one that blocks. Supports ArgoCD's source
  hydrator output, Kargo's rendered promotion branches, or any CI job that
  commits its render. See docs/rendered-manifests.md.

- **Source model.** A repository's manifests are obtained through a list of
  sources -- `manifests`, `helm`, `kustomize`, `argocd-bootstrap` -- which can
  be combined. The previous version understood exactly one topology (an
  app-of-apps ApplicationSet rendering a chart) and was silently blind to every
  other, including committed ApplicationSets and plain Applications, which are
  the most common ArgoCD layouts there are.
- `argocd-bootstrap` resolves its source path the way ArgoCD does: a directory
  with `Chart.yaml` is a chart, anything else is read recursively as manifests.
  The canonical gitops-bridge bootstrap is the second kind.
- Both the singular `source:` and multi-source `sources:` Application template
  forms are read. gitops-bridge uses the singular.
- Plain `Application` manifests are read, with `destination.server` resolved
  against the inventory so they key the same way generated ones do.
- Concurrent rendering, and `argocd:` on sources and clusters for fleets
  running more than one ArgoCD.
- `scope: cluster | fleet` for per-cluster renders, because whether an
  ApplicationSet expands fleet-wide depends on hub-and-spoke versus
  per-cluster ArgoCD, and guessing is silent.
- Topology fixtures covering each shape, plus a 50-cluster fleet.

- `render` — expands both levels of the ApplicationSet hierarchy into the flat
  set of Applications a cluster would end up with, including the bootstrap
  Applications themselves.
- `diff` — compares two renders. Blocks on cluster-targeting changes and on a
  source changing underneath an unchanged Application; reports version changes
  without blocking.
- `diff` separates a brand-new addon (`introduced`, non-blocking) from an
  existing addon gaining or losing a cluster (`targeting`, blocking). Only the
  second is the leak; blocking on the first would make every new-addon pull
  request red for no reason and train people to override the check.
- `validate` — schema validation of every rendered stream via kubeconform.
- `clusters export` — regenerates the cluster inventory from live ArgoCD
  cluster Secrets, with `-check` for drift detection.
- Support for both ApplicationSet templating dialects, chosen from the
  ApplicationSet's own `goTemplate` field rather than guessed.
- Generators that cannot be expanded (git, matrix, list) produce an explicit
  "not covered" warning rather than silently reporting full coverage.
