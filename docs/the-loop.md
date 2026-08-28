# The loop, end to end

One pull request, walked from the moment a new chart version exists to the
moment the change is verified running. Every piece of this repository appears
exactly once, doing the one thing it does. Component references and
configuration live in their own documents; this one is the story they belong
to.

The worked example is real, generalised: `external-secrets` moving
`0.10.3 → 2.9.0`, the promotion that taught this system its most important
lesson. A one-line diff. Green everywhere, at first. It would have broken
every manifest in the repository still declaring the API versions the new
chart stops serving.

## The cast

| Piece | Job | Never does |
|---|---|---|
| **Kargo** | notices new versions, opens the pull request, merges it when policy allows | judge what the change does |
| **the gate** (`gate/`, in CI) | renders what the repository actually deploys, at base and head, and diffs it; publishes the report and the `addons-gate` check | fix anything, call a model |
| **Bosun** (the agent, in-cluster) | reads the gate's verdict; repairs what is provable, explains what is not, escalates what needs a decision | close a PR, merge a PR, fail a check, touch the cluster |
| **branch protection** | requires `addons-gate`, so a blocking finding is an unmergeable pull request | — |
| **ArgoCD** | reconciles main into the cluster after the merge | anything before the merge |
| **the verification** (AnalysisRun) | asks the metrics whether what deployed is actually healthy | block a merge — it gates the *next* promotion in a chain |

## 1. A version appears

A Kargo Warehouse watching the chart repository discovers `2.9.0`. The Stage
rewrites the one pinned line in the target list, pushes a branch, and opens a
pull request. The visible diff is a version number.

Whether anyone is asked to look depends on the target's merge policy: patch
bumps of trusted charts merge themselves once the gate is green; anything the
policy holds — a major boundary, an artifact whose failure mode is silence —
waits for a human, and those held pull requests are exactly the ones Kargo's
promotion also POSTs to Bosun.

## 2. The gate renders the truth

CI runs the gate twice — once at the base revision, once at the head — and
each run expands every bootstrap ApplicationSet for every cluster in the
inventory into the full set of Applications the repository generates. The
diff of those two renders is what the pull request *actually does*, which a
one-line text diff cannot show. Because the version moved, the gate also
pulls the chart at both versions and diffs the rendered resources, down to
the fields that changed.

For our example the render says: eleven CRDs added, twenty-five resources
changed — and four CustomResourceDefinitions **stop serving** `v1alpha1` and
`v1beta1`. The gate scans the repository for manifests still declaring those
versions, finds them, and blocks:

> **A CustomResourceDefinition stopped serving a version** — anything still
> declaring it breaks on apply.
>
> - `CustomResourceDefinition/externalsecrets.external-secrets.io`: no longer
>   serves `v1alpha1, v1beta1` — `ExternalSecret` manifests must move to `v1`
>   - **N manifest(s) in this repository still declare a dropped version** —
>     blocking until they move: …

Everything the gate publishes lands in two artifacts on the pull request: a
report comment led by an invisible marker, and the `addons-gate` check. The
report is the wire format for everything downstream — the agent has no
channel to the gate except reading it.

## 3. Bosun reads the verdict

Kargo's promotion POSTed the pull-request context to Bosun the moment it
opened, so the agent is already waiting for `addons-gate` to settle. Its own
commit status says so — `pending`, "reading addons-gate" — because a reader
must be able to tell *working* from *done with nothing to say*.

The gate is red. Four things can happen, in strict order of preference.

**The deterministic repair.** If the only blocking finding is dropped served
versions, no judgement is needed: the report names the consumer kind, the
dropped versions, the destination, and the rule for finding the declaring
manifests. Bosun rewrites every one of them — apiVersion values only,
preserving quoting and comments, deny-list and allowlist consulted for every
file — and pushes the migration to the pull request's branch. **No model is
involved.** The gate re-runs on the new commit, counts the consumers again,
finds none, and goes green. The red healed, and the thing that verified the
repair is the same scanner that demanded it.

**The reshape.** Sometimes swapping the version is not the whole job: the new
schema would silently prune a field the old one carried, so a document that
parses and applies quietly loses a value, and the render, the gate and the
repository all look fine. Enumerating every upstream's structural changes is
not possible, so here — and only here — a model is asked for the migrated
document itself, one document at a time and capped per pull request. It is
still not the thing that writes. The proposal is refused *whole* unless it
keeps the object's identity, fits the target schema, and contains no value that
is not either at that same path in the original, displaced by the schema change,
or dictated by the schema itself; what lands is re-serialised from the structure
the harness validated, never the model's own text. A refusal escalates, and
values dropped along the way are listed in the comment even when dropping them
was right. See [ADR 0007](../adr/0007-structure-from-the-schema-data-from-the-document.md).

**The mechanical fix.** For a red the render *proves* — a chart default this
repository needs pinned back, a coupled pin the new version requires — the
model is asked to classify and to propose scalar edits. It never touches a
file: it selects from an inventory of editable keys, and the applier enforces
what the prompt merely describes — deny-list, promotion scope, `from`-value
equality, and the rule that a version-shaped value must appear verbatim in
the gate's report. What survives that gauntlet is committed to the branch;
the gate re-runs and judges the result. An attempt cap, tracked by label,
bounds the loop.

**The handoff.** Everything else — a targeting change, a moved namespace, a
migration the evidence does not fully specify — is a human's decision, and
the comment is written as a handoff rather than an announcement: which file
and key to open, what the choice is, and the one fact that stopped a
mechanical fix. The `needs-human` label goes on, and Bosun stops. It never
closes the pull request, and its status is never a failure — the gate is the
gate; the agent is crew.

## 4. And when the gate is green

A green gate is a verdict on the render, not on the bump — the most dangerous
promotions render perfectly. So on held pull requests Bosun also explains
green gates: what the version bump actually changed, grounded in the render
diff and, when the publisher's image labels lead to them, the maintainers'
own release notes. A green render that still warrants eyes — a major boundary
crossed, RBAC that vanished, notes describing a manual step — gets **Worth a
look before merging** and the label, and blocks nothing.

This is also where the loop improves itself. The example on this page was
first caught *here*, by the model flagging a version distance no render
reveals — and within hours that judgement was code: the gate's deterministic
served-version rule, then the deterministic repair. Escalations become gate
rules. The model is the scout; the gate is where knowledge hardens.

## 5. Merge, reconcile, verify

The merge is Kargo's when policy allows and a human's otherwise; either way
`addons-gate` is required, so nothing blocking merges. ArgoCD reconciles main
into the cluster. Then the promotion's verification asks the metrics whether
the Applications the target names are actually Synced and Healthy — and in a
promotion chain, that answer is what unlocks the next stage, so "it merged"
and "it works" stay different facts.

## Watching it run

The [local proving ground](../local) builds a disposable cluster and runs
this entire page against it — `make demo` for the happy path, `make
demo-triage` for a pull request the gate refuses, `make scenarios` to replay
the recorded red-gate incidents in [`evals/`](../evals) against the live agent.
(The explain-path cases in that suite are not replayed there — they need a green
gate, and the scenario script seeds a red one.)

## Where each piece is specified

- [safety-model.md](safety-model.md) — what is enforced, and by which mechanism
- [classification.md](classification.md) — mechanical vs escalate, with worked examples
- [prompt-contract.md](prompt-contract.md) — the prompts, and the measurements behind them
- [`gate/README.md`](../gate/README.md) — the gate's subcommands, blocking rules and exit codes
- [`charts/kargo-pipelines/README.md`](../charts/kargo-pipelines/README.md) — targets, chains and the triage hook
- [`charts/bosun/README.md`](../charts/bosun/README.md) — running the agent in-cluster
