# 10. The CLI goes too: one binary ships the gate

- **Status:** accepted
- **Date:** 2026-08-28
- **Amends:** [ADR 0009](0009-one-gate-one-inventory.md)

## Context

ADR 0009 removed CI mode and the Secret read, and kept the `gitops-gate` CLI
"unchanged, along with `clusters export` and the checked-in snapshot it
maintains" — the workstation path, for checking a change before pushing.

Pricing what that kept: a second `main` package, a second Dockerfile, a second
published image, the snapshot exporter and its filter machinery, the
`clusters` and `clustersExport.ignoreKeys` config keys, and half of
`image.yaml` — the scope job and the retag job existed entirely so that two
images with different rebuild triggers could share one workflow.

Against that cost, the evidence of use. The one production install has no
Kargo pin for the gate image any more, no CI workflow that runs it, and no
snapshot checked in; its own config comments read "there is no export". The
CLI's remaining audience was a workstation run that nobody was running.

And the kept path contradicted the ADR that kept it. 0009's title is one gate,
one inventory — but the CLI rendered against a checked-in snapshot, which is a
second inventory in exactly the sense 0009 removed, and one that is stale by
construction once nothing maintains it. A local verdict that can disagree with
the in-cluster verdict is worse than no local verdict: the disagreement reads
as the gate being wrong, and teaches people to argue with it.

## Decision

**The gate ships in one binary: the agent's.** `gate/cmd/gitops-gate`,
`gate/Dockerfile`, `ExportClusters`, `LoadInventory`, the snapshot format and
the export filter are removed. `image.yaml` publishes one image, with no scope
job to decide which and no retag job to keep an unchanged one still.

**The config loses the keys only the CLI read.** `clusters` and
`clustersExport.ignoreKeys` are gone; a config still carrying either fails at
parse naming the unknown key, which is where a config should learn that what
it sets no longer exists. `clustersExport.knownAbsentLabels` stays, unrenamed:
it is render configuration that was always read in both modes, and renaming a
key every live install sets would break them for tidiness.

**The record consolidates.** `gate/CHANGELOG.md` is folded into the
repository's `CHANGELOG.md`, each entry under the release that first shipped
it, because the gate never had a version line of its own: the numbers on the
`gitops-gate` images were the agent's `appVersion`, stamped on at release.
The published images (`0.18.5` through `0.23.0`, and the `main-*` tags) stay
in the registry as history; nothing publishes over them and nothing new
arrives.

**The Secret decode stays, as ground truth.** `InventoryFromSecrets` no longer
has a production caller, and is kept anyway: the cluster Secrets are ArgoCD's
own storage for the inventory's facts, and the fidelity tests decode one
through it to prove the API read reports the same cluster the Secret defines.
That test is what caught the API stripping `managed-by`, and it keeps
guarding the only inventory left.

## What it costs

**There is no local run of the gate at all.** The pre-push check is now
opening the pull request — a draft, if the point is to look before anyone
else does — and reading the verdict the agent posts within its poll interval.
That is a real regression in ceremony for a real gain in truth: the verdict
rendered is the verdict that counts, against the live inventory, with no
snapshot to be quietly wrong. A future need for a workstation run is a reason
to resurrect the CLI from history, not to maintain it on speculation.

**An install that set the removed keys breaks loudly at the next run.** The
strict parser rejects `clusters:` and `clustersExport.ignoreKeys:` by name,
and until the line is deleted the gate posts `error` on open pull requests.
Loud and named is the acceptable shape of this breakage; the alternative,
ignoring keys that used to mean something, is how configuration rots.

## Consequences

One binary, one image, one changelog, and one inventory in fact as well as in
name. `image.yaml` shrinks from a three-job workflow reasoning about diffs to
a single build. Onboarding loses its last aside: there is no CLI appendix to
skip, and nothing in `.gitops-gate.yaml` exists for a tool the reader will
never run.
