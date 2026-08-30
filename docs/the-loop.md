# The loop, end to end

One pull request, from a new chart version appearing to the change verified
running. Every component appears once, doing the one thing it does; component
reference and configuration live in their own documents.

The worked example is a real promotion, generalised: `external-secrets` moving
`0.10.3 → 2.10.0`. The visible diff is one line and every check starts green,
and it would have broken every manifest in the repository still declaring the
API versions the new chart stops serving.

## The components

| Piece | Job | Never does |
|---|---|---|
| **Kargo** | notices new versions, opens the pull request, merges it when policy allows | judge what the change does |
| **the gate** (`gate/`) | renders what the repository deploys, at base and head, and diffs it; publishes the report and the `addons-gate` check | fix anything, call a model |
| **Bosun** (the agent, in-cluster) | reads the gate's verdict; repairs what is provable, explains what is not, escalates what needs a decision | close a PR, merge a PR, fail a check, touch the cluster |
| **branch protection** | requires `addons-gate`, so a blocking finding is an unmergeable pull request | nothing |
| **ArgoCD** | reconciles main into the cluster after the merge | anything before the merge |
| **the verification** (AnalysisRun) | asks the metrics whether what deployed is healthy | block a merge; it gates the *next* promotion in a chain |

## 1. A version appears

A Kargo Warehouse watching the chart repository discovers `2.10.0`. The Stage
rewrites the one pinned line in the target list, pushes a branch, and opens a
pull request. The visible diff is a version number.

The target's merge policy decides whether anyone is asked to look. Patch bumps
of trusted charts merge themselves once the gate is green. Anything the policy
holds, such as a major boundary or an artifact whose failure mode is silence,
waits for a human, and those held pull requests are the ones Kargo's promotion
also POSTs to Bosun.

## 2. The gate renders the truth

The agent runs the gate in-cluster, against the live cluster inventory read
from ArgoCD's API ([ADR
0008](../adr/0008-the-gate-moves-in-cluster.md)). The same engine also ships as
a container with an exit code, for a run from a workstation before pushing:
same renders, same report, same check name.

The gate runs twice, once at the merge base -- the last commit this branch and
`main` share, not `main`'s current tip, which moves whenever anything else
merges -- and once at the head. Each run expands every bootstrap ApplicationSet
for every cluster in the inventory into the full set of Applications the
repository generates. The diff of those two renders is what the pull request
does, which a one-line text diff cannot show. Because the version moved, the
gate also pulls the chart at both versions and diffs the rendered resources,
down to the fields that changed; it does the same when the version held still
and a values file the Application layers was edited, which is the only way an
addon whose chart lives in a registry is covered at all.

For our example the render says: eleven CRDs added, twenty-one resources
changed, and four CustomResourceDefinitions **stop serving** a version this
repository still declares — `v1beta1` on all four, `v1alpha1` on three. The
gate scans the repository for manifests declaring those versions, finds
twenty-eight, and blocks:

> **A CustomResourceDefinition stopped serving a version.** Anything still
> declaring it breaks on apply.
>
> - `CustomResourceDefinition/externalsecrets.external-secrets.io`: no longer
>   serves `v1alpha1, v1beta1`; `ExternalSecret` manifests must move to `v1`
>   - **N manifest(s) in this repository still declare a dropped version**,
>     blocking until they move: …

The example here is a bump, which is what a promotion produces. When a pull
request *adds* an addon there is no base version to compare against, and the
same two renders answer a different question: the report lists the expansion,
every Application the change creates, the cluster each one lands on, and the
chart and pinned version or repository path it renders from. Nothing blocks. A
reviewer reading one new entry in a values file has no other way to learn any
of it; [the gate's own README](../gate/README.md#new-addons) shows the table.

Everything the gate publishes lands in two artifacts on the pull request: a
report comment led by an invisible marker, and the `addons-gate` check. The
report is the wire format for everything downstream. The agent has no channel
to the gate except reading it.

## 3. Bosun reads the verdict

Kargo's promotion POSTed the pull-request context to Bosun the moment it
opened, so the agent is already waiting for `addons-gate` to settle. Its own
commit status says so, `pending`, "reading addons-gate", so that a reader can
tell *working* from *done with nothing to say*.

The gate is red. What happens next, in strict order of preference.

**The deterministic repair.** If the only blocking finding is dropped served
versions, no judgement is needed: the gate already computed the consumer kind,
the dropped versions and the destination, and it publishes them twice — as the
bullet a person reads, and as a `<!-- gitops-gate:dropped … -->` block the
repair reads. Two spellings because half of a bullet is the name of an object a
chart rendered, and a chart must not be able to write the instruction. Bosun
rewrites every declaring manifest, apiVersion values only, preserving
quoting and comments, deny-list and allowlist consulted for every file.
**The swap itself calls no model.** The gate re-runs on the pushed commit,
counts the consumers again, finds none, and goes green, so the same scanner
that demanded the repair is the one that verifies it.

**The reshape.** The swap is not always the whole job, so a second pass runs
over the files it just rewrote, before any of them are pushed. The target
schema prunes a field the old one carried, so the document parses, applies and
quietly loses a value while the render, the gate and the repository all look
fine. A model is asked for the migrated document itself, one document at a time
and capped per pull request. This is one of two paths on which a
model authors whole file content, the values migration below being the other,
and on neither does it apply what it wrote. The proposal is refused
whole unless it keeps the object's identity, fits the target schema, and
contains no value that is not either at that same path in the original,
displaced by the schema change, or dictated by the schema itself. What lands is
re-serialised from the structure the harness validated rather than written back
as the model's text. A refusal escalates and nothing is pushed, the swaps that
were fine included: half a migration turns the gate green over a document the
apiserver still prunes. Values dropped along the way are listed in the comment
even when dropping them was right. See
[ADR 0007](../adr/0007-structure-from-the-schema-data-from-the-document.md).

**The values migration.** A different red: the chart will not render at the new
version at all, because its `values.schema.json` refuses settings this
repository has been making since before the chart stopped declaring them.
Nothing above can express the fix, since removing a key, renaming one and
adding one are three operations a scalar edit has no shape for. So the model is
shown both versions' schemas and the values, and returns the whole values
document.

The harness then does three things the model has no say in: it requires every
value the new chart still declares to be at the same path, byte-identical; it
walks the proposal against the new schema; and it refuses any value the
original and the schema did not between them supply. Then it renders the chart
with the proposal. That last one is the check the manifest path cannot have,
because the program that refused the old values is the one asked whether the
new ones work.

What lands is not the document. The difference between the two becomes a plan
of three operations, remove a key, rename a key, set a key, and each is applied
on that key's own lines, so a three-key change reads as three lines and every
comment in the file survives. The file is then read back and compared to the
proposal the harness accepted; if it does not match, nothing is written. A key
the new schema *requires* and names no value for escalates before the model is
called at all, because there is nothing to derive an answer from. See
[ADR 0013](../adr/0013-a-values-migration-is-a-plan-not-a-document.md).

**The deterministic escalation.** Some reds have nothing in this repository to
change: the *chart* renders an object whose apiVersion moved, and no manifest
here declares it. The gate blocks, because somebody should look, but there is
no edit to propose. The gate's own blocker counts are enough to say so, so this
escalates without calling a model at all, and the comment says in one line that
there are no values to change. Asking a model to explain an absence produced a
paragraph restating the report with the one sentence that mattered buried in
it.

**The mechanical fix.** For a red the render *proves*, such as a chart default
this repository needs pinned back or a coupled pin the new version requires,
the model is asked to classify and to propose scalar edits. It selects from an
inventory of editable keys rather than writing a patch, and the applier
enforces what the prompt only describes: deny-list, promotion scope,
`from`-value equality, and the rule that a version-shaped value must appear
verbatim in the gate's report. What survives those checks is committed to the
branch; the gate re-runs and judges the result. An attempt cap, tracked by
label, bounds the loop.

**The handoff.** Everything else, such as a targeting change, a moved
namespace, or a migration the evidence does not fully specify, is a human's
decision, and the comment is written as a handoff rather than an announcement:
which file and key to open, what the choice is, and the one fact that stopped a
mechanical fix. The `needs-human` label goes on, and Bosun stops. It never
closes the pull request, and its status is never a failure: branch protection
requires the gate, and a second red check would block merges on a check
nobody chose.

## 4. When the gate is green

A green gate is a verdict on the render, and the render can be clean on a
promotion that still breaks something at runtime. So on held pull requests
Bosun also explains green gates: what the version bump changed, grounded in the
render diff and, when the publisher's image labels lead to them, the
maintainers' own release notes. A green render that still warrants eyes, a
major boundary crossed, RBAC that vanished, notes describing a manual step,
gets **Worth a look before merging** and the label, and blocks nothing.

Escalations on this path are where gate rules come from. This chart's dropped
versions were first caught here, on an earlier external-secrets promotion, by
the model flagging a version distance no render reveals; that judgement then
became code, the gate's deterministic served-version rule, and then the
deterministic repair that fixes the promotion on this page. Promoting a model's
one-off finding into a gate rule is how this system gets less dependent on the
model over time.

## 5. Merge, reconcile, verify

The merge is Kargo's when policy allows and a human's otherwise; either way
`addons-gate` is required, so nothing blocking merges. ArgoCD reconciles main
into the cluster. Then the promotion's verification asks the metrics whether
the Applications the target names are Synced and Healthy, and in a promotion
chain that answer is what unlocks the next stage. A merge does not advance the
chain; a healthy deployment does.

## Watching it run

The [local proving ground](../local) builds a disposable cluster and runs this
entire page against it: `make demo` for the happy path, `make demo-triage` for
a pull request the gate refuses, `make demo-structural` for one the swap alone
cannot fix. The recorded incidents in [`evals/`](../evals) are scored by `go
test ./evals/...` rather than replayed there, because the gate renders the
sample repository and there is nowhere to put a recorded verdict.

## Where each piece is specified

- [safety-model.md](safety-model.md): what is enforced, and by which mechanism
- [classification.md](classification.md): mechanical vs escalate, with worked examples
- [prompt-contract.md](prompt-contract.md): the prompts, and the measurements behind them
- [`gate/README.md`](../gate/README.md): the gate's subcommands, blocking rules and exit codes
- [`charts/kargo-pipelines/README.md`](../charts/kargo-pipelines/README.md): targets, chains and the triage hook
- [`charts/bosun/README.md`](../charts/bosun/README.md): running the agent in-cluster
