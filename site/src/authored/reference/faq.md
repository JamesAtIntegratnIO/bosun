---
title: FAQ
description: The questions that come up before installing — about trust, cost, model choice, blast radius, and what happens when a piece of this is down.
---

## Does an AI model decide what gets merged?

No. Two separate answers, and both matter.

**The gate is deterministic.** It renders, diffs and schema-validates. No model
is involved, and it is the gate — not the agent — that branch protection
requires. Whether a pull request *can* merge is decided by code that does not
call a model.

**The agent's status is never a failure**, whatever its verdict. A red status
from the agent would make it a second gate. It is `pending` while triage runs
and `success` once there is a verdict; the description carries the meaning
rather than the colour.

So the model's influence is bounded to: proposing edits that a deterministic
applier may refuse, and writing prose in a comment.

## What can the agent actually write?

A branch. Specifically the pull request's own branch, never the default branch,
and only files that pass **both** `triage.allowPaths` (a standing grant) and
`Scope` (the promotion's own file list for this request), and that are not on a
deny-list configuration cannot remove from.

Everything it pushes still has to pass the gate and the merge policy to reach
anywhere. See [Safety model](/concepts/safety-model/) for the full table of
guarantees and the mechanism enforcing each.

## Can it edit the gate to make itself pass?

No, twice over.

`edits.DefaultDeny` refuses `.github/**`, `.gitops-gate.yaml`,
`.gitops-gate/**`, the kargo pipelines and projects trees, and the CI config of
three hosts. An operator can *add* to that list; nothing can remove from it.

Separately, the recommended token has **no Workflows permission**, so GitHub
itself rejects any push touching `.github/workflows/**`. Two independent
mechanisms, one of which is enforced by someone else's server.

## Will it close or merge my pull requests?

Never. The `gitprovider.Provider` interface deliberately has neither a merge nor
a close method. It comments, labels, pushes to a bot branch, and stops.

## What does it cost to run?

The task is small and the output is checked, so this does not need a frontier
model. Measured on the nine-case eval with a **9B**: 8/9 classification, 8/9
full pass, **0 unsafe**. The published numbers — 10/10, 10/10, 0 unsafe — come
from a 27B running on one workstation.

A local endpoint is a first-class path here, not a workaround. If you run one,
the marginal cost of the agent is the pod: 25m CPU and 64Mi requested.

There is no default provider on purpose, so it cannot start spending money
against a vendor you did not choose.

## Do I have to give it my cluster credentials?

Cluster mode needs **get/list on Secrets in the ArgoCD namespace** — those
Secrets are the live inventory the gate renders against, and they also carry
cluster credentials. The chart scopes that grant to a namespaced Role, in that
namespace only, in cluster mode only.

It is the one grant in the chart worth stopping on, and it cannot be made
smaller: RBAC has no way to say "the labels but not the data". Two ways out if
you will not make it. `gate.inventorySource: argocd` reads the same four fields
from ArgoCD's own API, which redacts the credentials, and the Role stops being
created — the cost is an ArgoCD account token with `clusters, get` and a second
component that can be down. Or `gate.mode: ci` keeps the original shape whole:
the gate runs as a container in your CI, and the agent reads the verdict from a
comment.

Beyond that, live reads are **off by default**, and are `get` and `list` only —
the ClusterRole has no `create`, `update`, `patch` or `delete` verb anywhere.

## Can it read my Secrets?

With `liveReads.scope: groups` (the default), no — the core API group is never
granted beyond `pods, events`, so Secrets are unreadable by construction.

With `scope: wide` it grants `apiGroups: ["*"]`, and that **includes Secrets**.
RBAC has no deny rules and no way to subtract the core group, which is precisely
why "everything except Secrets" is not a setting this chart can offer. If that
matters to you, do not use `wide`.

## What happens when the model endpoint is down?

You get a comment saying so. A model that is unreachable, slow or misconfigured
produces a visible failure, because silence would be indistinguishable from
"nothing was wrong".

The gate is unaffected — it never calls a model — so pull requests keep getting
their verdict. You lose the explanation and the mechanical fix, not the
inspection.

## What happens when the cluster is down?

In cluster mode the gate is the agent, so a required `addons-gate` check will
not report and merges block. That is the gate failing loudly, which is the
correct behaviour — but it means the human override should exist *before* it is
needed.

Leave yourself a bypass: with classic branch protection, *Include
administrators* unticked; with rulesets, a bypass for your own account and
**not** for the bot.

If you need a gate that outlives the cluster, that is what `gate.mode: ci` is
for.

## Does it work with GitLab or Bitbucket?

Not yet. GitHub and Gitea are implemented and exercised; GitLab and Bitbucket
are extension points behind a ten-method interface. The gate itself is
host-agnostic — it is a container with an exit code — so the CI half already
works anywhere. See [Git providers](/reference/git-providers/).

## Do I need Kargo?

For the **gate**, no. It renders and diffs a pull request; where the pull
request came from is not its business, and it runs from any CI as a container.

For the **agent's triage**, effectively yes — it wants the promotion context
(artifact, from, to, the files the promotion touched) that arrives as a POST
when the promotion opens the pull request. Anything that can POST that shape to
`/v1/promotion-opened` works; Kargo is what does it today.

## Do I need ArgoCD?

For cluster mode, yes — the ArgoCD cluster Secrets *are* the inventory the gate
expands ApplicationSets against, whether it reads them directly or through
ArgoCD's API (`gate.inventorySource`). In CI mode that inventory is a
checked-in snapshot instead, maintained by `gitops-gate clusters export`.

## Why are the gate and the agent in one repository?

Because they are joined by contracts nothing else checks: the agent finds the
gate's verdict by searching comments for a marker the gate emits, and any
version it writes must appear verbatim in the gate's rendered report.

Both of those broke, silently, while the two halves were separate packages. A
boundary is safe where its contract can be tested.

## Why does it escalate things it could obviously fix?

Deliberate calibration. A wrong escalation costs a human two minutes. A wrong
mechanical fix renders perfectly and breaks at runtime, which is the failure
this system exists to prevent.

Two cases were *moved* to escalation after going wrong on live pull requests —
a port change outside the promotion's file list, and a namespace move the agent
accommodated instead of rejecting. Both are written up in
[Mechanical or escalate](/concepts/classification/), including what was lost by
demoting them, which was nothing except the pretence that they were safe.

## Can I use it without the agent at all?

Yes. The gate is a container with an exit code:

```bash
docker run --rm -v "$PWD:/repo" -w /repo \
  ghcr.io/jamesatintegratnio/gitops-gate:main \
  diff -base targets-base.json -head targets-head.json -repo . -report report.md
```

Multi-arch, so it runs on an arm64 laptop as well as an amd64 runner — a gate
you cannot reproduce locally is a gate nobody reproduces before pushing.
Ready-made adapters are in [CI adapters](/gate/ci-adapters/).

## Can I redistribute it, fork it publicly, or run it for a client?

No to all three. The licence is
[PolyForm Internal Use 1.0.0](/project/licence/): run it for your own business,
commercially, in production, without asking anyone — but you may not distribute
it, and that rules out free redistribution too.

Providing it to third parties in any form needs a separate licence. Ask; that is
the intended path.
