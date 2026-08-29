# Changelog

All notable changes to `kargo-pipelines`. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

## [0.2.1] - 2026-08-29

### Changed

- **Documentation, throughout.** `README.md`, `docs/chaining.md`,
  `docs/targets.md` and the comments in `values.yaml` and the templates lose
  the em dashes, emphasis capitals and filler adverbs. The `yaml-update` rules
  and the merge-policy table keep their reasoning. No template, value or
  default changed; the version moves because 0.2.0 is already published and
  these files ship inside the chart.

## [0.2.0] - 2026-08-28

### Added

- **`triage.authorization`.** The `Authorization` header, verbatim, on the http
  step that calls the triage service. It is the other half of bosun's
  `promotionAuth.existingSecret`: without a token that endpoint trusts every
  caller the namespace's NetworkPolicy admits, and its payload names the pull
  request the agent edits and the files it reads into a published prompt.

  Prefer a Kargo expression over a literal, so the token stays in a Secret:

  ```yaml
  triage:
    authorization: '${{ "Bearer " + secrets.bosun.token }}'
  ```

  which needs the Project to grant access to that Secret. A literal works and
  puts the token in your values file and in the rendered Stage. Empty renders no
  header, which is the previous behaviour.

## [0.1.3] - 2026-08-28

### Changed

- **Documentation only.** `docs/chaining.md` and `docs/targets.md` are edited
  for the repository's documentation voice — headings name their subject, and
  the rules keep their reasoning. No template, value or default changed.

## [0.1.2] - 2026-08-24

### Changed

- **The README stops claiming the chart does not exist.** It opened with
  "Status: not yet implemented" -- written as the contract before the
  implementation, never revisited after the chart shipped and went to work.
  Docs-only; no template changed. The version moves because the README ships
  inside the published chart, and CI is right that an unbumped change would
  merge and publish nothing.

## [0.1.1] - 2026-08-23

### Fixed

- **The triage hook has never once reached the triage service.** Every call
  died at config validation:

      invalid http config: body: Invalid type. Expected: string, given: object

  Kargo evaluates the `${{ }}` expressions in a step's config and then, in
  `pkg/expressions/json_templates.go`, unmarshals the result if it happens to
  be valid JSON. A body assembled by interpolating expressions into a JSON
  string is *by construction* always valid JSON, so it was always turned into
  an object -- and the `http` step's schema requires a string.

  The step is `continueOnError: true`, deliberately, so that a slow or absent
  triage service can never fail a promotion. The cost of that is exactly this:
  the promotion carried on, the pull request opened, and nothing anywhere said
  the call had not been made. It took the first gated promotion after the
  service went live to surface it, and only by reading the Promotion object.

  The body is now a single `quote({...})` over a native map, which is the
  pattern Kargo's own `http` step documentation uses for a JSON body:
  `quote()` on a non-string marshals it and wraps it, and Kargo strips the
  wrapping without parsing further.

  Values are native rather than pre-quoted now -- `prNumber` stays a number,
  lists stay lists -- because `json.Marshal` does the encoding instead of us.

  Rendered output is otherwise untouched: 124 objects before and after, and no
  differing line outside the 59 `body:` fields.

## [Unreleased]

Generalized from a working single-cluster chart. A repository migrating from
that chart renders byte-identical output with `nameLabel` set to its old value
— verified across 111 objects — so adopting this is a no-op until a target
actually declares `stages`.

### Added

- Documented that verification requires Prometheus to be scraping ArgoCD. The
  AnalysisTemplate queries `argocd_app_info`; with nothing scraping it, every
  AnalysisRun fails with an empty message and no component names the cause.
  Found by running a promotion on a cluster that had no ArgoCD ServiceMonitor.

- `git.insecureSkipTLSVerify`, applied to every git step. Needed more often
  than it looks: Kargo REFUSES to send credentials to a plain-HTTP endpoint
  ("refused to get credentials for insecure HTTP endpoint"), so a self-hosted
  host cannot be reached over `http://` to dodge a certificate problem — it
  has to be `https://`, and the certificate then has to be trusted or skipped.
  The failure without this is `git push` reporting `could not read Username`,
  which names neither cause.

- **Promotion chains.** A target may declare an ordered `stages` list. Each
  stage carries its own `updates` and `verify`, and downstream stages take
  their freight from the one before with `direct: false`. Kargo only offers
  *verified* freight downstream, so the gate needs no orchestration.
- `requiredSoakTime`, written on the stage doing the soaking and rendered onto
  the downstream stage's sources, where Kargo expects it.
- Per-stage `autoMerge`, so a canary can merge itself while the stage that
  reaches production still waits for a human.
- **Triage hook** — an optional `http` step firing when the pull request opens,
  carrying the freight context. `continueOnError` defaults true: a triage
  service that is down must never fail a promotion.
- Bounded `retry` on all three pull-request steps. Kargo's default
  `errorThreshold` is 1, meaning no retries at all, which turns a transient API
  error into a failed promotion.
- `values.schema.json`, including a required `git.repoURL` and canonical
  duration patterns.
- `nameLabel`, so a repository migrating from a differently-named chart does
  not churn the label on every object.

### Changed

- `git.repoURL` has no default and must be supplied.

### Notes

- The **last** stage in a chain keeps the target's bare name. Renaming the
  terminal Stage would discard its freight and verification history and make
  ArgoCD prune and recreate it.
- Each stage parses the first file of **its own** `updates`. Without that, a
  downstream stage compares against the pin its upstream already moved,
  concludes there is nothing to do, and silently never promotes.
