# 9. One gate, one inventory: CI mode and the Secret read are removed

- **Status:** accepted
- **Date:** 2026-08-28
- **Amends:** [ADR 0008](0008-the-gate-moves-in-cluster.md)

## Context

ADR 0008 moved the gate into the agent and kept `gate.mode: ci` "whole, as the
fallback". ADR 0008 also accepted the ArgoCD cluster Secret read as the price of
the in-cluster inventory, and 0.20.0 added `gate.inventorySource: argocd` as an
opt-in way out of it.

Four months of both shapes coexisting is enough evidence to price the
coexistence rather than the features.

**Two gate placements is two of everything.** The CI shape's verdict travels
through a pull-request comment the agent scrapes back off the host — which is
why `gate.reportAuthor` exists at all, because a comment is a surface anyone
with write access can forge. That trust check, the marker contract it defends,
`gate.wait` and the poll loop behind it, `CheckMissing`-treated-as-pending, and
three adapter READMEs written against documentation and never run, are all cost
carried by every reader of this codebase for a path the default install does not
take. Two of the three adapters were unproven when they were written and were
unproven when they were deleted.

**Two inventory sources is two answers to one question.** The gate's verdict
depends on which cluster labels it thinks exist, and a selector matches on those
maps. Two decoders for the same facts is a defect waiting for a key that one
trims and the other does not — which is not hypothetical: running both against a
real ArgoCD is what found that the API strips ArgoCD's own `managed-by`
annotation while the Secrets carry it. The fix then was to make both agree. The
cheaper fix is to have one.

**The Secret grant was never defensible; it was only default.** It is get/list
on Secrets in the ArgoCD namespace, and it cannot be made smaller — RBAC has no
predicate for "the labels but not the data", `resourceNames` does not apply to
`list`, and the label selector the gate sends is a filter the apiserver applies
*after* authorising. A token holding that Role can drop the selector and read
`argocd-secret` and every repository credential beside it. Leaving it as the
default meant every install that did not read the values file carefully got it.

## Decision

**The gate runs in the agent, and nowhere else.** `gate.mode` is gone, with
`gate.wait`, `gate.reportAuthor`, the report-comment scraping path, the
`waitForGate` poll loop, and the `ci/` adapters and their documentation.

**The inventory comes from the ArgoCD API, and nowhere else.**
`gate.inventorySource` is gone. `gate.argocd.baseURL` and
`gate.argocd.existingSecret` are required, with no default for either — an
address that is plausible but wrong fails at start-up with a timeout that points
nowhere near the values file, so the operator names it. The chart renders no
Role or ClusterRole granting `get`/`list` on Secrets anywhere.

The `gitops-gate` CLI is unchanged, along with `clusters export` and the
checked-in snapshot it maintains. That snapshot is now a CLI-only concern: it is
how a change is checked from a workstation before pushing, and the agent never
opens it.

## What it costs

**A pull-request check depends on two components being up, not one.** ADR 0008
already accepted the first — the agent — and answered it with the branch
protection bypass and a loud failure. This adds argocd-server, which the
apiserver-backed read did not need. The window where argocd-server is down and
merging a cluster change is urgent is a window where a human should be deciding.

**There is no fallback for a repository taking fork pull requests.** The render
runs helm over the pull request's content inside the cluster, `gate.forkPRs` is
off by default, and a CI runner is no longer available as the throwaway sandbox
for that content. A public repository taking fork contributions has to decide
whether to turn `gate.forkPRs` on, and that is now the only answer this project
offers.

**The proving ground lost the incident replay.** `make scenarios` fed the agent
the recorded gate report from each incident in `evals/` as a comment; with the
gate in-process there is nothing to feed, and reproducing fourteen upstream
chart versions locally would prove nothing extra. The fixtures are unchanged and
still scored by `go test ./evals/...`. The demo acts that change the sample
repository — a targeting move, a dropped API version, an escalation — now wait on
the agent's own verdict, which is closer to production than they were.

## Consequences

Onboarding drops a decision rather than gaining one: there is no mode to choose
and no inventory source to weigh. It gains a required credential, which the
chart refuses to install without.

An upgrade from 0.20.x is not silent. `gate.mode`, `gate.wait`,
`gate.reportAuthor` and `gate.inventorySource` are rejected by the chart's
schema, so an install carrying any of them fails at `helm upgrade` and names the
key — which is the correct place for a values file to be told that what it
configures no longer exists.
