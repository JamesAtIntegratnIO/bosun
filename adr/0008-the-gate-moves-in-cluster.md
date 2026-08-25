# 8. The gate moves in-cluster; CI becomes the fallback

- **Status:** accepted
- **Date:** 2026-08-24
- **Amends:** [ADR 0002](0002-triage-in-cluster-not-ci.md)

## Context

ADR 0002 put the deterministic checks in CI on one load-bearing sentence: *"CI
is where the checkout already is, and the gate needs a working tree at two
revisions."* Both halves of that premise have since died in this codebase's
own commits.

The agent grew a checkout of its own — it clones the pull request's branch on
every triage, because authoring an edit needs a working tree. It grew commit
statuses, because every outcome needed to leave a trace on the pull request.
It grew a poll loop, waiting on the gate's check. And it grew live cluster
access, for the facts CI structurally cannot have. Every capability the CI
placement was chosen for, the agent now carries anyway.

Meanwhile the CI placement's costs compounded into the bulk of what an
operator must build by hand to adopt this system:

- **The checked-in cluster inventory.** CI cannot reach the cluster, so the
  gate renders against a snapshot — the file its own config reference calls
  "the gate's weakest joint". It needs a cluster-side export to create, an
  `ignoreKeys`/`knownAbsentLabels` iteration to stabilise, a scheduled check
  to notice drift, and it re-opens all three every time the fleet changes.
  The reference consumer wrote 191 lines of bespoke Go just to notice when it
  had gone stale.
- **The adapter.** A workflow to copy and edit, a paths filter whose failure
  mode is a permanently blocked pull request, an image reference to pin, and
  — because a bot token without the Workflows permission cannot push to
  `.github/workflows/` — a one-key `gate-image.yaml` file plus a `sed` line
  to read it back. GitLab and Bitbucket adapters exist as README stubs.
- **The report contract.** The verdict travels through a pull-request comment
  the agent scrapes back off the host, which forced the `reportAuthor` trust
  check into existence — a comment is a surface anyone with write access can
  forge — and cost a re-run of the whole loop every time the report's shape
  and its readers drifted.
- **The re-trigger trap.** A fix pushed with the default CI token does not
  re-run CI, so the gate never re-answers and the promotion waits forever —
  which is why the docs demand a dedicated bot identity before anything works.

## Decision

**The agent runs the gate.** `gate.mode: cluster` — the default — polls the
open pull requests; for every head commit without a verdict it renders base
and head with the engine the CLI uses (one Go package now, imported by both),
against an inventory read live from the ArgoCD cluster Secrets, and publishes
the same `addons-gate` status and the same marker-led report comment the CI
adapter published. Branch protection does not change. The triage reads the
verdict in-process; the comment is for humans.

There is no relevance filter. CI needed one because minutes are billed and a
skipped required check never reports; in-process, every pull request is
rendered and "no change to what gets deployed" is an answer instead of a
guess.

**CI mode survives whole** as `gate.mode: ci`, and the CLI is unchanged — the
same engine behind a shell for local runs and for the fallback.

## What it costs

**A pull-request check now depends on cluster reachability.** ADR 0002
rejected this shape for exactly that, and the objection was real: if the
agent is down, `addons-gate` never reports and merges block. Three things
paid it down. The check's subject *is* the cluster — the window where the
gate is unreachable and merging cluster changes is urgent is the window where
a human should be deciding anyway, with the branch protection bypass that
already exists for it. The CI adapter remains a working fallback, switchable
by one value. And the failure is loud — a required check stuck pending — not
a wrong answer.

**Reading the ArgoCD cluster Secrets is a real grant.** They carry cluster
credentials, and RBAC cannot grant the labels without the data. The chart
scopes it to a namespaced Role, get/list, existing only in cluster mode, with
a comment that says what it is the price of. An operator who will not pay it
sets `gate.mode: ci` and keeps the snapshot.

**Rendering pull-request content in-cluster is a trust decision**, so fork
pull requests are refused by default — with an `error` status naming
`gate.forkPRs`, never an unreported check — and public repositories taking
fork contributions should stay on CI mode, where the render runs in a
throwaway runner.

## Consequences

The second objection ADR 0002 recorded against this shape — "leaves
contributors without a local way to run it" — is answered by the split that
enabled the move: the engine is a library, the CLI is one caller of it, and
`gitops-gate render|diff|validate` works exactly as before.

Onboarding collapses to: install the chart, protect `addons-gate`, commit a
sources-only `.gitops-gate.yaml`. The inventory snapshot, its export loop,
its drift check, the CI workflow, the paths filter, the image pin and the
bot-token-retrigger rule all leave the adopter's plate — and a pushed fix is
re-gated because it is a new head commit, not because a token was minted
correctly. See [docs/onboarding.md](../docs/onboarding.md).
