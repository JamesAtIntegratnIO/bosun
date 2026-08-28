# Safety model

The agent can write to a repository and spend money. Both are bounded by code,
not by instructions to a model.

## The model proposes; the harness applies

The model is called once per pull request. It returns one structured answer: a
classification, an explanation, and — on the mechanical path only — a proposal.
It has no file-edit tools and no path to the repository. Every byte that
reaches a branch is written by the harness, and only after the proposal has
passed the checks below.

The reason is that a model holding file-edit tools can make a red gate green by
deleting the check, and that failure is indistinguishable from success.

## A proposal is either scalar edits or one migrated document

Which of the two it is decides how the harness checks it.

**Scalar edits** name a file, a key, a `from` and a `to`. Each is corroborated
against the file it names: the key must already resolve to an existing scalar,
and the `from` must equal what the file holds.

**A migrated document** is one whole object, reshaped for a target schema, and
it is used where a plain `apiVersion` swap would leave behind fields the target
schema prunes. The model authors the complete document here — file content, not
a value to swap — so corroboration does not apply: a reshape leaves no `from`
to match. Three deterministic checks run on the output instead.

| Check | What it requires |
|---|---|
| Identity | `apiVersion` equals the target the gate named; `kind`, `metadata.name` and `metadata.namespace` are byte-identical to the original |
| Schema validity | the proposal is walked against the target schema by the same code that found the problem |
| Value provenance | every scalar leaf appears as a scalar leaf in the original, or is dictated by the target schema itself — a default, an enum member, a const |

What lands is re-serialised from the validated structure rather than written
back as the model's text, so the bytes on disk are a function of a structure
the harness checked. Structure may come from the model; data may not.
[ADR 0007](../adr/0007-structure-from-the-schema-data-from-the-document.md)
records why the proposal surface includes whole documents and what replaced
corroboration.

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
| Cannot edit a file this change did not touch | `Policy.Scope`, set per request from the promotion's own file list. `Allow` is a standing grant and deliberately coarse; `Scope` is what *this* pull request is about. Without it the prompt asks for the files this pull request may change while the applier accepts anything under the standing grant — an instruction where there should be a guarantee |
| Cannot overwrite a value it misread | the edit's `from` must equal what the file holds |
| Cannot invent a version | version-shaped values must appear in the evidence the model was shown |
| Cannot add or restructure with a scalar edit | the key must already resolve to an existing scalar. Restructuring is only available on the document path, under the three checks above |
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
| Cannot read a Secret, outside one namespace | with `liveReads.scope: groups` the core API group is never granted **cluster-wide** beyond `pods, events`. The one exception is the inventory grant in the row below, which is namespaced and removable. `liveReads.scope: wide` grants `apiGroups: ["*"]` cluster-wide and **can** read Secrets everywhere — RBAC has no deny rules and no way to subtract the core group, which is why "everything except Secrets" is not a setting |
| Cannot be given a Secret read it does not need | cluster mode's inventory grant is get/list on Secrets in the ArgoCD namespace, and it cannot be made smaller: the gate wants four fields, and RBAC has no predicate for "the labels but not the data" — no deny rules, `resourceNames` does not apply to `list`, and the request's label selector is a filter the apiserver applies *after* authorising. `gate.inventorySource: argocd` reads the same four fields from ArgoCD's own API, which draws that line, and the chart stops rendering the Role. It is a trade rather than a win: an ArgoCD account token instead, and a component that can be down on its own. The default is still `secrets`, so the grant exists unless an operator removes it |
| Cannot present "nobody looked" as "nothing found" | `cluster.Count` carries a `Known` flag and its rendering prefers the note over the number. A refusal, an unreachable apiserver, or a count where one version answered and another did not all say what was *not* checked. The prompt tells the model in those words that "not permitted to check" is not zero and not evidence of safety |
| Cannot invent data when it reshapes a document | a proposed document migration is refused unless every scalar value in it appears in the original document or is dictated by the target schema itself — a default, an enum member, a const. Field names come from the schema; data comes only from the document. This is the document-level analogue of "cannot invent a version": it is what keeps the model a translator of the document rather than a source of new values |
| Cannot change what an object is while reshaping it | `apiVersion` must equal the target the gate named, and `kind`, `metadata.name` and `metadata.namespace` must be byte-identical to the original. A renamed object is a second change riding inside a migration |
| Cannot propose a document that still does not fit | the proposal is walked against the target schema by the same code that found the problem — the apiserver's own objection, raised before the apply |
| Cannot half-migrate a pull request | if any document in a pass is refused, **nothing** is pushed, including the plain swaps that were fine. The swap alone turns the gate green, because no manifest declares a dropped version any more, while a document the target schema rejects waits to be pruned. A partial push is a green gate over a broken change |
| Cannot drop a value silently | values present in the original and absent from the proposal are listed in the comment. Some are correct — a field the target no longer accepts has to go somewhere, sometimes nowhere — and all are visible |
| Cannot act without saying so | every exit path publishes a commit status, including the ones that do nothing and the ones that error. Without one, "nothing needed triage", "never called" and "crashed" are the same observation from outside |

## Why the deny-list is not configurable

Every entry is a way to make a red gate green without fixing anything:

```
.github/**            the workflows that run the gate
.gitops-gate.yaml     what the gate renders, and how
.gitops-gate/**       the cluster inventory it compares against
delivery/**           the kit itself, including this agent and its prompt
.gitlab-ci.yml        the GitLab and Bitbucket equivalents of .github/**
bitbucket-pipelines.yml
**/kargo-projects/**  the merge policy and version constraints
**/kargo-pipelines/** the promotion pipelines themselves
```

These are the patterns as enforced. The matcher understands `**` at the start
of a pattern, at the end, or at both — not in the middle — so a wildcard in the
directory name itself (`**/kargo-*/**`) is not something the deny-list can say.

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
