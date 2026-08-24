# Safety model

The agent can write to a repository and spend money. Both are bounded by code,
not by instructions to a model.

## The model never writes anything

It is asked one question and returns one structured answer: a classification,
an explanation, and — only for the mechanical case — a list of proposed scalar
edits. This process decides what, if anything, happens next.

That is the whole design. A model with file-edit tools can make a red gate
green by deleting the check, and that failure is indistinguishable from success.

## Widened once, deliberately, and written down

The rule above said "never edits a FILE" until 2026-08-24. It now says never
*writes*, and the difference is one wave of work: the proposal surface widened
from a scalar edit to a whole migrated document, because a chart that moves a
field between API versions cannot be repaired by rewriting one line and nobody
can enumerate every upstream's structural changes in advance.

What did not widen is who writes. The harness still applies, and the checks in
front of a document are stricter than the ones in front of a scalar — three of
them, all deterministic, all on the OUTPUT rather than the proposal, because the
whole point of a reshape is that there is no `from` value left to match. See
[`adr/0007-structure-from-the-schema-data-from-the-document.md`](../adr/0007-structure-from-the-schema-data-from-the-document.md).

## The deterministic repair involves no model at all

Migrating manifests off a CRD version a bump stopped serving is not a
judgement: the gate's report names the consumer kind, the dropped versions and
the surviving destination, all computed from the rendered CRDs. The `migrate`
package parses that line back — the same package that wrote it — and rewrites
nothing but apiVersion values matching it, only when that finding is the gate's
*only* blocking one.

The guarantees below still hold where they apply: the deny-list and the
allowlist answer for every file, the rewrite is a value replacement on the
scalar's own line, the attempt cap counts these pushes too, and the re-run
gate re-counts the consumers itself. The one deliberate difference is `Scope`:
consumers are by definition files the promotion did not touch, and it is the
gate — not a model — that named them.

## What is enforced, and where

| Guarantee | Mechanism |
|---|---|
| Cannot edit CI config, the gate, or the merge policy | `edits.DefaultDeny`, checked before any write and not overridable by configuration |
| Cannot edit outside the configured area | `Policy.Allow`; an empty allowlist refuses everything, and the process refuses to start with one |
| Cannot edit a file this change did not touch | `Policy.Scope`, set per request from the promotion's own file list. `Allow` is a standing grant and deliberately coarse; `Scope` is what *this* pull request is about. Before it existed the prompt said "the files this pull request may change" while the applier accepted anything under the standing grant — an instruction where there should be a guarantee. |
| Cannot overwrite a value it misread | the edit's `from` must equal what the file holds |
| Cannot invent a version | version-shaped values must appear in the evidence the model was shown |
| Cannot add or restructure | the key must already resolve to an existing scalar |
| Cannot escape the repository | path traversal is rejected after cleaning |
| Cannot retry forever | attempt cap, tracked by pull-request label |
| Cannot write to the default branch | the only push path targets the pull request's own branch |
| Cannot block a merge | its commit status is never a failure state, whatever the verdict. A red status here would make the agent a second gate; the description carries the meaning instead of the colour. It is `pending` while triage runs and `success` once there is a verdict — pending on a check nobody requires blocks nothing, and it is what stops "still thinking" reading as "nothing to say" |
| Cannot reach anything it was not given | upstream lookup talks to `api.github.com` and to the registries named in `networkPolicy.egress.fqdns`, and nothing else. Every failure degrades to a render-only explanation that says so |
| Cannot present a guess as a source | the upstream repository comes from the publisher's own `org.opencontainers.image.source` label, never from parsing a registry path. A guessed repository returns another project's notes, which reads exactly like the truth |
| Cannot turn testimony into evidence for a write | release notes and upstream commits are fetched **only** on the paths that produce prose — the green-gate explanation and an escalation. The mechanical path, the one that writes files, does not fetch them, so they are not in the evidence string the applier corroborates against. Without that, a commit message containing `v1.5.0` would make `v1.5.0` a corroborated value to write |
| Cannot choose its own supporting evidence | which upstream commits are shown is decided by `migrate.Subjects` — the kinds and resource names in the gate's own findings — matched by string against commit messages and diff paths. Asking the model which commits support its conclusion would be a second opinion from the same opinion |
| Cannot compare a range it cannot establish | a chart version and the git tags of the project it packages are frequently different numbering. Refs come from the project's own release tags or from the publisher's recorded build revision; when neither meets the promotion's versions, no comparison is made and the note says why. Two refs picked out of the wrong numbering return real commits from a range that is not this one |
| Cannot mutate the cluster | live reads are `get` and `list` only, and the chart's ClusterRole has no `create`, `update`, `patch` or `delete` verb anywhere. They are off by default: everything else the agent reads is public or already in the pull request, and this reads the operator's cluster |
| Cannot read a Secret | with `liveReads.scope: groups` the core API group is never granted beyond `pods, events`, so Secrets are unreadable by construction. `wide` grants `apiGroups: ["*"]` and **can** read them — RBAC has no deny rules and no way to subtract the core group, which is why "everything except Secrets" is not a setting |
| Cannot present "nobody looked" as "nothing found" | `cluster.Count` carries a `Known` flag and its rendering prefers the note over the number. A refusal, an unreachable apiserver, or a count where one version answered and another did not all say what was *not* checked. The prompt tells the model in those words that "not permitted to check" is not zero and not evidence of safety |
| Cannot invent DATA when it reshapes a document | a proposed document migration is refused unless every scalar value in it appears in the original document or is dictated by the target schema itself — a default, an enum member, a const. Field names come from the schema; data comes only from the document. This is the document-level analogue of "cannot invent a version", and it is what makes the model a translator rather than an author |
| Cannot change what an object IS while reshaping it | `apiVersion` must equal the target the gate named, and `kind`, `metadata.name` and `metadata.namespace` must be byte-identical to the original. A renamed object is a second change riding inside a migration |
| Cannot propose a document that still does not fit | the proposal is walked against the target schema by the same code that found the problem — the apiserver's own objection, raised before the apply |
| Cannot half-migrate a pull request | if any document in a pass is refused, **nothing** is pushed, including the plain swaps that were fine. The swap alone turns the gate green, because no manifest declares a dropped version any more, while a document the target schema rejects waits to be pruned. A partial push is a green gate over a broken change |
| Cannot drop a value silently | values present in the original and absent from the proposal are listed in the comment. Some are correct — a field the target no longer accepts has to go somewhere, sometimes nowhere — and all are visible |
| Cannot act without saying so | every exit path publishes a commit status, including the ones that do nothing and the ones that error. "Nothing needed triage", "I was never called" and "I crashed" used to be the same observation from outside |

## Why the deny-list is not configurable

Every entry is a way to make a red gate green without fixing anything:

```
.github/**            the workflows that run the gate
.gitops-gate.yaml     what the gate renders, and how
.gitops-gate/**       the cluster inventory it compares against
delivery/**           the kit itself, including this agent and its prompt
**/kargo-*/**         the merge policy and version constraints
```

An operator can add to the deny-list. They cannot remove from it.

## Why the attempt cap is a label

Labels live on the pull request, so the cap survives a restart, a rescheduled
pod, and a second replica. In-memory state would reset every time the pod
moved, which is exactly when a loop would be most expensive.

## Failure is always visible

Three rules, because a silent agent is worse than none:

- A refused edit is reported in the pull-request comment, with the reason. A
  silent refusal would let a reader believe a fix had been applied.
- A `mechanical` verdict that applies nothing escalates. The model may be
  wrong; the outcome is still a human being asked.
- A model that is unreachable, slow or misconfigured produces a comment saying
  so. Silence would be indistinguishable from "nothing was wrong".

## What it does not do

It never closes a pull request, never merges one, never touches the cluster.
Its RBAC is read-only, and its entire write surface is a bot branch that still
has to pass the gate and the merge policy to reach anywhere.
