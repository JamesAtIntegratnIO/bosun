# Changelog

All notable changes to `bosun`. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

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
