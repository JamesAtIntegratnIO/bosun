# Adding a CI provider

**Check that you need one first.** Since [ADR
0008](../../adr/0008-the-gate-moves-in-cluster.md) the agent runs the gate
in-cluster by default (`gate.mode: cluster`) — no CI adapter, no checked-in
inventory. An adapter is for the fallback: fork pull requests, a gate that
must keep answering while the cluster is down, or the gate with no agent at
all.

The gate is a container with an exit code, so an adapter is thin by
construction. There is no plugin interface to implement — just four things to
get right.

## What an adapter has to do

**1. Check out both revisions.** The gate compares the merge base against the
head. Most CI systems default to a shallow single-revision checkout, which is
not enough; you need enough history to reach the base commit.

**2. Take the config from the head at both revisions.** `.gitops-gate.yaml` and
the cluster inventory describe *how* to render, not *what* to render. The base
revision may predate them entirely — the pull request that introduces the gate
is the obvious case. Use the head's copy for both renders, or the base render
fails on a repository that was fine.

**3. Run render twice, then diff.**

```bash
gitops-gate render -repo . -out targets-base.json   # at the base
gitops-gate render -repo . -out targets-head.json   # at the head
gitops-gate diff -base targets-base.json -head targets-head.json \
  -repo . -report report.md -json render-diff.json
```

`-repo` on the diff is load-bearing: it enables chart-diff, and without it the
gate goes green on exactly the class of change it exists to catch. The
reference adapter omitted it once — see
[`ci/github/README.md`](../../ci/github/README.md).

**4. Turn the exit code into a commit status.**

| Code | Status | Meaning |
|---|---|---|
| `0` | success | No blocking change. |
| `1` | failure | Targeting moved, or validation failed. |
| `2` | error | The gate could not run. |

Map `2` to a distinct state if your host has one. "This change is bad" and "the
gate is broken" want opposite reactions, and a CI system that renders them
identically teaches people to ignore the check.

## Report one aggregate status

Whatever jobs you split the work into, report a single status. Branch
protection then names one check, and adding a job later never requires editing
the protection rule — a step that is easy to forget, and whose omission
silently drops your gate.

## Post `report.md` as a pull-request comment

Verbatim, and **before** the step that fails the build. The triage agent in
CI mode reads the gate's verdict by listing the pull request's comments and
taking the newest one that begins with the marker the report's first line
carries — a comment is the one artifact surface every git host has.

The comment is the only verdict channel. Publishing `render-diff.json` to an
artifact store instead does not work: nothing fetches it, so an adapter that
treats it as the handoff publishes a verdict no agent can find. The full
contract is [`ci/README.md`](../../ci/README.md).

## Two things adapters get wrong

**A push made with the CI system's own token usually does not re-trigger CI.**
Most hosts suppress it to prevent loops. If the agent pushes a fix with that
token, the gate never re-runs, the status stays red at its previous conclusion,
and an automated merge waits on a result that will never change. Use a separate
credential for agent pushes.

**Gate latency is added to every automated merge.** A bot waiting to merge polls
on a fixed interval, so a slow gate slows every bump. Skip the expensive
chart-render diff when no chart version changed — that is the common case.
