# Changelog

All notable changes to `bosun`. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

## [Unreleased]

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
