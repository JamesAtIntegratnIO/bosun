# Changelog

All notable changes to `bosun`. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

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
## [0.9.1] - 2026-08-24

### Fixed

- **The v0.9.0 gitops-gate image never existed.** Its Dockerfile copied only
  `gate/` into the build and the gate now imports `migrate/`, so the release
  published the agent image and died on the gate's -- see the gate changelog.
  This release exists to run the release machinery over the fixed Dockerfile:
  the version path publishes both images, so v0.9.1 is the first tag since
  the repair feature whose gate image is real. Nothing about the agent binary
  changed since 0.9.0.
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
