# 12. The repository stops repeating the ship

- **Status:** accepted
- **Date:** 2026-08-29
- **Extends:** [ADR 0009](0009-one-gate-one-inventory.md)

## Context

`.gitops-gate.yaml` is described in its own reference as "the whole of that
knowledge": the gate knows nothing about any particular repository, so the file
carries everything. That was true when the gate rendered against a checked-in
snapshot and could see nothing else. It has not been true since
[ADR 0008](0008-the-gate-moves-in-cluster.md) put the gate in the cluster, and
each release since has taken another key out of the file because something live
already knew the answer. `clusters` and `clustersExport.ignoreKeys` went with
the CLI in [ADR 0010](0010-the-cli-goes-too.md). `sources[].argocd` went in the defect pass that preceded this ADR, because
nothing had ever populated the field it matched on.

Sorting what is left by where the answer actually lives:

| Key | What it is | Who knows it |
|---|---|---|
| `sources` | where the Applications and ApplicationSets are | ArgoCD, exactly, for everything it serves |
| `valuesRef` | the `ref:` name a multi-source Application gives its values source | the Application itself, in `sources[].ref` |
| `concurrency`, `validate` | how hard to render, and whether to schema-check | the operator running the gate, not the repository being gated |
| `clustersExport.knownAbsentLabels` | labels a selector matches on that nothing carries | still the repository's, and rarely needed now that absence operators no longer demand presence |

Only the last row is knowledge the repository has and nothing else does. The
first two are a second copy of what ArgoCD is already serving, maintained by
hand, and a second copy that drifts is the failure this codebase refuses
everywhere else — it is the whole argument of 0009, applied to the inventory
rather than to the sources.

**Measured against the production install, ArgoCD v3.5.1.** The numbers below
decided this; they are recorded because a later reader will want to know
whether the shape held or the fleet was unusual.

- **Untracked means root.** Exactly 2 of 60 live ApplicationSets carry no
  `argocd.argoproj.io/tracking-id` annotation, and both are the Terraform-applied
  bootstraps. Every other ApplicationSet in the fleet was created by something
  ArgoCD is already tracking, so the annotation partitions the fleet into
  "reachable by following what ArgoCD serves" and "the roots", with no third
  case.
- **Following live and following the file agree.** The live bootstrap
  ApplicationSet spec is structurally identical to the committed one, and both
  produce 63 rows across 58 ApplicationSets. Deriving only the leaf
  Applications, with no root expansion at all, loses exactly the 2 bootstrap
  rows.
- **The config file is sometimes pure duplication.** In the split-repository
  pattern, where roots live in an infrastructure repository and the content
  they point at lives in a second one, every line of `.gitops-gate.yaml` in the
  content repository restates a pointer ArgoCD is already serving. That
  repository needs no file at all. This is the case that turned the design from
  file-first with derivation as a fallback into the reverse.
- **`?repo=` cannot be used as the filter.** ArgoCD v3.5.1's `FilterByRepoP`
  compares `spec.source.repoURL` by exact string equality against
  `sources[0]` only. Pointed at the gated repository it returned 7 of 65
  Applications: it misses every multi-source Application whose first source is
  the chart, and every spelling difference. Normalising URLs here, across all
  of `source`, `sources[*]` and `sourceHydrator.drySource`, is not a nicety —
  it is the prerequisite that makes derivation correct at all.
- **Nothing needs a write.** `POST /api/v1/applicationsets/generate` would
  expand a generator server-side and is exactly the wrong shape: it needs
  `applicationsets, create`, and the chart's ClusterRole has no create verb
  anywhere. Derivation is built out of two reads instead.
- **The grant is two lines of ArgoCD RBAC and no Kubernetes RBAC at all.**
  `applications, get` and `applicationsets, get` in `argocd-rbac-cm`, on the
  same account token that already holds `clusters, get`. No new credential, no
  new Role, no new mount.
- **Roots are edited.** 18 commits in the production repository touched the
  bootstrap tree. Selector surgery on a root is real traffic, `terraform plan`
  cannot expand a generator, and a root's edit therefore has to be gated from
  the pull request's own content rather than from what is currently applied.

## Decision

**The gate derives its sources from ArgoCD by default.** The Applications and
ApplicationSets ArgoCD serves supply the pointers and the root identities; the
pull request's checkout supplies every byte of content that gets rendered.
Live says *what* to render, head says *what it says*.

**Head wins over live wherever both can answer.** An ApplicationSet whose
manifest is in the gated repository is rendered from the pull request's copy,
not from the applied spec, because the applied spec is the previous answer and
the question is what this change does. The live spec is the fallback for one
case only: a root whose file is not in this repository, where head has nothing
to offer.

**`.bosun.yaml` is optional, and exists for one thing.** It names untracked
roots that live in the gated repository, so their edits gate from head instead
of from the applied spec. That is the single fact ArgoCD cannot supply, because
the annotation that would say so is exactly the one those objects lack. Sources
written in the file merge over derived ones, so anything derivation gets wrong
can be corrected by hand.

**`.gitops-gate.yaml` keeps working, unchanged.** A repository with a file
today does not have to do anything. Both filenames are read, the newer one
first; both present is an error rather than a silent precedence rule.

**An empty derivation is a refusal.** If ArgoCD serves nothing matching this
repository and no file names anything, the gate refuses rather than reporting
no change, for the same reason an unreadable inventory refuses in 0009: a
render against a world the gate could not see finds no targeting change and
waves everything through.

## What it costs

**Scope now depends on cluster state, and the two failure modes are not
symmetrical.** An ArgoCD that is down or refuses the read fails loud: the gate
errors, exactly as it does for an unreadable inventory. An ArgoCD that is up
and serving a *smaller* fleet than it did yesterday fails quiet: the gate
renders a smaller scope, correctly reports no change within it, and says
nothing about what left. The report states the derived scope on every run so
the shrink is visible to a reader; it is not detected automatically, and
comparing a run against the previous run's scope is named here as the obvious
follow-up rather than pretended into this decision.

**A brand-new in-repo root is blind on its own layer until it applies.**
A pull request that adds a root ApplicationSet has, by definition, no live
object to be found by, so nothing derives from it and the gate sees whatever
its content deploys only after the first apply. Naming it in `.bosun.yaml`
closes this, which is the reason the file exists; leaving it out is a
first-run-only gap, not a permanent one.

**Two config filenames exist during the overlap.** Two names for one file is a
cost, paid deliberately, because the alternative is a flag day on every install.
It ends when `.gitops-gate.yaml` is retired, and until then the error on finding
both is what keeps the ambiguity from becoming a silent precedence bug.

**Self-gating configuration was never load-bearing, and this records that.**
`.gitops-gate.yaml` sits in the agent's deny-list, which reads like a defence:
a pull request cannot widen what the gate looks at. It is circular. The deny
list stops *the agent* editing the file; it has never stopped a human author
from editing it in the same pull request the gate is judging, and the gate
renders head, so head's file is the one in force. The real protection is that
the file is reviewed like any other change. Deriving from live narrows the
surface rather than widening it, since the pointers stop being head-controlled
at all — but nobody should read either arrangement as a boundary that holds
against the pull request's own author.

## Consequences

The common install has no file. Onboarding's configuration step becomes two
lines of ArgoCD RBAC, and the reference page leads with "you probably need
none" rather than with a schema. A repository in the split pattern is gated
with nothing checked in at all.

`concurrency` and `validate` move to the chart, where the rest of the host's
policy already lives, on the same reasoning that keeps the egress deny-list out
of the gated repository's reach: they are the operator's decision about their
own pod, not the reviewed repository's. File keys stay honoured where the values
leave them unset.

`valuesRef` retires. A multi-source Application names its own values source in
`sources[].ref`, so `$values/` resolves from the Application that used it rather
than from a repository-wide guess that was wrong the moment two Applications
disagreed.

This is the same trade 0008, 0009 and 0010 each made and each stated: less
written down, more read live, and a loud failure when the live read is
unavailable. The gate stopped carrying a snapshot of the fleet; it now stops
carrying a snapshot of the fleet's sources.
