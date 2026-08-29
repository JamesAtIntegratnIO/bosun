---
title: What Bosun is
description: The gap Bosun closes, the two halves that close it, and what it will and will not do, before you install anything.
---

Bosun is two components and one loop. A **gate** that renders what a pull
request deploys and blocks the changes that break things, and an
**agent** that reads the gate's verdict, repairing what is provable, proposing
fixes where a model's judgement is enough, and handing a human everything that
needs a decision.

It sits beside Argo (the ship) and Kargo (the cargo).

## The gap it closes

Kargo is very good at producing change. Left alone it opens more pull requests
than anyone can read, and merges a good share of them on a **version-shaped
policy**: patch bumps of trusted charts merge themselves, majors wait for a
human.

That policy cannot see what is in the diff, and the dangerous changes look
exactly like the boring ones:

- A one-line version bump that renders perfectly and stops serving an API every
  manifest in your repository still declares.
- A values-layer edit that adds or removes a whole cluster from an addon's
  scope. The selector did not change; the labels it matches did.
- A chart that moves a field between API versions, so a plain version swap
  leaves a document the apiserver silently prunes on the way in.

None of these are visible in a text diff, and none of them fail at merge time.
They fail at apply time, or later, or quietly forever.

## The two halves, and why they live together

| Piece | What it does |
|---|---|
| **The gate** | The inspection round. Renders your ApplicationSets at base and at head, fails on a cluster-targeting change, an apiVersion migration, or a CRD dropping a served version your manifests still declare; diffs the old and new chart render down to the field; schema-validates the result. |
| **The agent** | The rounds and the repair. Acts on the verdict: migrates manifests off dropped API versions deterministically, fixes what the rendered diff *proves* is mechanical, explains what a green gate cannot show, and escalates the rest as a handoff. |

They are one loop, inspect then repair, joined by contracts that nothing else
checks. The agent finds the gate's verdict by searching pull-request
comments for a marker the gate emits, and any version it writes must appear
*verbatim* in the gate's rendered report.

Both of those broke, silently, while the two halves were separate packages. A
boundary is safe where its contract can be tested; put the two sides in
different repositories and no CI run can ever check them together.

## What it will and will not do

**Fixes autonomously**, only where the render diff *proves* the cause: a chart
default that flipped, a coupled pin, and, with no model involved at all, a CRD
that stopped serving a version. Every manifest still declaring one is migrated
to the version the gate says survives, and the re-run gate re-counts the
consumers to confirm the repair.

**Reshapes, under a harness**, where that swap alone would leave a field the
new schema silently prunes. A model is asked for the migrated document, one
document at a time. The proposal is refused whole unless it keeps the object's
identity, fits the target schema, and invents no value; a refusal escalates
rather than half-applying.

**Escalates** an apiVersion change in the *rendered output*, a document
migration the harness refused, a removed CRD, a dropped subchart, an upstream
note mentioning a schema or database migration, a version skip the chart itself
refuses, or any fix needing a file outside the addon's own tree.

It comments, labels, and stops. **It never closes a pull request**, never merges
one, and never touches the cluster.

:::note[The calibration is deliberate]
Biased toward escalation. A wrong escalation costs a human two minutes. A
wrong mechanical fix passes the render, passes the gate, and fails when the
apiserver sees it, which is the failure this system exists to prevent. See
[Mechanical or escalate](/concepts/classification/).
:::

## The safety model is code, not prompt

The model never applies. It returns a structured verdict and a proposal:
scalar edits for a mechanical fix, and, where swapping an apiVersion would
leave a document the new schema silently prunes, the complete migrated
document. The service applies what survives its own checks, behind a path
allowlist and a deny-list its own configuration cannot remove from.

A proposed document is the one place the model authors file content. It is
refused whole unless it keeps the object's identity, fits the target schema,
and contains no value that is not in the original or dictated by the schema
itself, and what lands is re-serialised from the structure the harness
validated rather than the model's text. The model supplies structure; the
original document supplies data.

So *"never edit the gate, never weaken a policy to go green"* is an invariant
the service enforces, not an instruction the model is asked to respect. A model
that ignores the prompt entirely still cannot touch CI configuration.

The full table of guarantees and the mechanism enforcing each one is in
[Safety model](/concepts/safety-model/). The reasoning is
[ADR 0001](/decisions/0001-structured-edits-not-agentic-loop/), and
[ADR 0007](/decisions/0007-structure-from-the-schema-data-from-the-document/)
for the document path.

## Where the gate runs

Since [ADR 0008](/decisions/0008-the-gate-moves-in-cluster/) the agent **is**
the gate. It polls your open pull requests, renders base and head against the
live cluster inventory read from ArgoCD's API, and posts the `addons-gate`
status and report comment itself. There is no CI workflow to install, no
checked-in cluster inventory to go stale, and no paths filter to hand-edit.

The same engine also ships as a container with an exit code, for a run from a
workstation before pushing.

## What it needs

- **Kargo** 1.11 or newer, or anything else that can POST a promotion event.
- **ArgoCD**, whose API serves the live cluster inventory the gate renders
  against, on an account token with `clusters, get`.
- A **git host**: `github` or `gitea` today, behind a small interface.
- An **OpenAI- or Anthropic-compatible model endpoint**. There is no default, on
  purpose: a service that silently starts spending money against a vendor you
  did not choose is a bad default.

Nothing here hardcodes a cluster, domain, namespace, CNI, secret manager, git
host or model provider. Those are values.

## Status

Running in production on the platform it was built for. Measured **10/10
classification, 10/10 full pass, 0 unsafe actions** on the eval cases against
`qwen/qwen3.8-27b`, a local model on a workstation, and 8/9 with zero unsafe
actions against a 9B. Every case is a real incident.

## Next

- [Quickstart](/start/quickstart/): a working install in about fifteen minutes.
- [The loop, end to end](/start/the-loop/): one pull request, walked all the way through.
- [Onboarding](/start/onboarding/): the full path, six verifiable steps.
