---
title: FAQ
description: The questions that come up before installing, about trust, cost, model choice, blast radius, and what happens when a piece of this is down.
---

## Does an AI model decide what gets merged?

No. Two separate answers, and both matter.

**The gate is deterministic.** It renders, diffs and schema-validates. No model
is involved, and branch protection requires the gate rather than the agent.
Whether a pull request *can* merge is decided by code that does not call a
model.

**The agent's status is never a failure**, whatever its verdict. A red status
from the agent would block merges on a check nobody chose. It is `pending`
while triage runs
and `success` once there is a verdict; the description carries the meaning
rather than the colour.

So the model's influence is bounded to three things: proposing scalar edits a
deterministic applier may refuse, proposing a migrated document three
deterministic checks may refuse, and writing prose in a comment.

## What can the agent write?

A branch. Specifically the pull request's own branch, never the default branch,
and only files that pass **both** `triage.allowPaths` (a standing grant) and
`Scope` (the promotion's own file list for this request), and that are not on a
deny-list configuration cannot remove from.

The model itself writes nothing. On the mechanical path it proposes scalar
edits; on the structural path it authors a complete migrated document, which
the harness parses, checks for identity, schema validity and value provenance,
and re-serialises from the validated structure. Neither reaches a branch except
through the applier.

Everything it pushes still has to pass the gate and the merge policy to reach
anywhere. See [Safety model](/concepts/safety-model/) for the full table of
guarantees and the mechanism enforcing each.

## Can it edit the gate to make itself pass?

No, twice over.

`edits.DefaultDeny` refuses `.github/**`, `.gitops-gate.yaml`, `.bosun.yaml`,
the kargo pipelines and projects trees, and the CI config of three hosts. An
operator can *add* to that list; nothing can remove from it.

Separately, the recommended token has **no Workflows permission**, so GitHub
itself rejects any push touching `.github/workflows/**`. Two independent
mechanisms, one of which is enforced by someone else's server.

## Will it close or merge my pull requests?

Never. The `gitprovider.Provider` interface deliberately has neither a merge nor
a close method. It comments, labels, pushes to a bot branch, and stops.

## What does it cost to run?

The task is small and the output is checked, so this does not need a frontier
model. Measured on the nine-case eval with a **9B**: 8/9 classification, 8/9
full pass, **0 unsafe**. The published numbers, 10/10, 10/10 and 0 unsafe, come
from a 27B running on one workstation.

A local endpoint is a first-class path here rather than a workaround. If you run
one, the marginal cost of the agent is the pod: 25m CPU and 64Mi requested.

There is no default provider on purpose, so it cannot start spending money
against a vendor you did not choose.

## Do I have to give it my cluster credentials?

It needs an **ArgoCD account token with `clusters, get`** and nothing else. The
gate reads four fields per cluster, name, server, labels and annotations, from
`GET /api/v1/clusters`, which serves them with the credential block redacted.

It is the API rather than the cluster Secrets those clusters are stored in
because that Secret read could not be made small enough: RBAC has no way to say
"the labels but not the data", so a Role that could read the labels could read
`argocd-secret` beside them. The chart creates no Role over Secrets at all. The
cost is a credential to mint and rotate, and a component that can be down on
its own: argocd-server is not up whenever the apiserver is.

Beyond that, live reads are **off by default**, and are `get` and `list` only.
The ClusterRole has no `create`, `update`, `patch` or `delete` verb anywhere.

## Can it read my Secrets?

**No.** The chart creates no Role or ClusterRole granting `get`/`list` on
Secrets anywhere. The one read that used to need it, the gate's cluster
inventory, comes from ArgoCD's API instead, which is the question above.

With `liveReads.scope: groups` (the default) the core API group is never granted
beyond `pods, events`.

**Unless you set `scope: wide`**, which grants `apiGroups: ["*"]` and therefore
**includes Secrets, everywhere**. RBAC has no deny rules and no way to subtract
the core group, which is why "everything except Secrets" is not a setting this
chart can offer. If that matters to you, do not use `wide`.

## What happens when the model endpoint is down?

You get a comment saying so. A model that is unreachable, slow or misconfigured
produces a visible failure, because silence would be indistinguishable from
"nothing was wrong".

The gate is unaffected, because it never calls a model, so pull requests keep
getting their verdict. You lose the explanation and the mechanical fix, and
keep the inspection.

## What happens when the cluster is down?

The gate is the agent, so a required `addons-gate` check will not report and
merges block. That is the gate failing loudly, which is the correct behaviour,
and it means the human override should exist *before* it is needed.

Leave yourself a bypass: with classic branch protection, *Include
administrators* unticked; with rulesets, a bypass for your own account and
**not** for the bot.

## Does it work with GitLab or Bitbucket?

Not yet. GitHub and Gitea are implemented and exercised; GitLab and Bitbucket
are extension points behind a ten-method interface. See [Git
providers](/reference/git-providers/).

## Do I need Kargo?

For the **gate**, no. It renders and diffs a pull request; where the pull
request came from is not its business.

For the **agent's triage**, yes in practice. It wants the promotion context
(artifact, from, to, the files the promotion touched) that arrives as a POST
when the promotion opens the pull request. Anything that can POST that shape to
`/v1/promotion-opened` works; Kargo is what does it today.

## Do I need ArgoCD?

Yes. ArgoCD's clusters *are* the inventory the gate expands ApplicationSets
against, and the agent reads them from ArgoCD's API on every run.

## Why are the gate and the agent in one repository?

Because they are joined by contracts nothing else checks: any version the agent
writes must appear verbatim in the gate's rendered report, and the report's own
vocabulary, the marker, the verdict stamp and the blocker breakdown, is read
back by the code that publishes it.

Both of those broke, silently, while the two halves were separate packages. A
boundary is safe where its contract can be tested.

## Why does it escalate things it could fix?

Deliberate calibration. A wrong escalation costs a human two minutes. A wrong
mechanical fix passes the render, passes the gate, and fails when the apiserver
sees it, which is the failure this system exists to prevent.

Two cases were *moved* to escalation after going wrong on live pull requests: a
port change outside the promotion's file list, and a namespace move the agent
accommodated instead of rejecting. Both are written up in [Mechanical or
escalate](/concepts/classification/). Demoting them cost no repair that was
ever correct; each one names the file and the value, so a human applies it in
one keystroke.

## Can I use it without the model at all?

Yes. `triage.enabled: false` is the chart's default: the agent renders, gates
and reports every pull request with no model configured and no LLM call ever
made. What you cannot do is run the gate without the agent; the standalone
`gitops-gate` CLI and its image were retired once the agent was the only
consumer left ([ADR 0010](/decisions/0010-the-cli-goes-too/)).

## Can I redistribute it, fork it publicly, or run it for a client?

No to all three. The licence is
[PolyForm Internal Use 1.0.0](/project/licence/): run it for your own business,
commercially, in production, without asking anyone, but you may not distribute
it, and that rules out free redistribution too.

Providing it to third parties in any form needs a separate licence. Ask; that is
the intended path.
