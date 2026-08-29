# Bosun

<p align="center">
  <img src="docs/avatar/bosun.png" alt="Bosun, an octopus in a sailor cap, wrench raised, inside a life ring" width="220"/>
</p>

<p align="center">
  <strong><a href="https://bosun.integratn.io">Documentation</a></strong> ·
  <a href="https://bosun.integratn.io/start/quickstart/">Quickstart</a> ·
  <a href="https://bosun.integratn.io/start/the-loop/">The loop</a> ·
  <a href="https://bosun.integratn.io/reference/configuration/">Configuration</a>
</p>

⚓ **The crew for Argo and Kargo.**

Kargo is very good at producing change. Left alone it opens more pull requests
than anyone can read, and merges a good share of them on a version-shaped
policy that cannot see what is in the diff. The dangerous ones look exactly
like the boring ones: a one-line version bump that renders perfectly and stops
serving an API every manifest in your repository still declares.

Bosun closes both gaps: a **gate** that renders what the pull request deploys
and blocks the changes that break things, and an **agent** that reads the
gate's verdict, repairing what is provable without any model at all, proposing
fixes a policy engine applies where a model's judgement is enough, and handing
a human everything that needs a decision, with the file, the key and the choice
named.

**Start with [the loop, end to end](docs/the-loop.md)**: one pull request
walked from a version appearing to the change verified running, with every
piece doing its one job.

> A boatswain's job is inspection and repair: daily rounds of the hull, rigging
> and cables, fixing what they find on their own authority, and reporting to
> the captain what they cannot. The inspection is the larger half, and it is
> what makes the repair authority safe to grant.
>
> Bosun sits beside Argo (the ship) and Kargo (the cargo).

| Piece | What it does |
|---|---|
| [`gate/`](gate) | **The inspection round.** Renders your ApplicationSets at base and at head, fails on a *cluster-targeting* change, an apiVersion migration, or a CRD dropping a served version your manifests still declare; diffs the old and new chart render down to the field; schema-validates the result. One engine, two faces: the agent runs it in-cluster against the live inventory by default, and the same code ships as a container with an exit code for local runs and CI. |
| [`agent/`](agent) | **The rounds and the repair.** Acts on the verdict: migrates manifests off dropped API versions deterministically, fixes what the rendered diff *proves* is mechanical, explains what a green gate cannot show, and escalates the rest as a handoff. |
| [`gateservice/`](gateservice) | Runs the gate in-process for every open pull request, on a timer, and publishes the verdict, so the agent reads it as a value instead of scraping its own comment. |
| [`supervisor/`](supervisor) | Sweeps the Kargo pipeline for the promotions that *never happened*. Nothing about one produces an event, so a timer is the only way to see it. |
| [`prompt/`](prompt) | What the model is told, and the constant the eval suite scores. |
| [`charts/kargo-pipelines`](charts/kargo-pipelines) | Warehouses and Stages from one target list, with multi-stage promotion chains, verification gating and the triage hook that calls the agent. |
| [`charts/bosun`](charts/bosun) | Runs the agent in-cluster, triggered by Kargo rather than polled. |
| [`site/`](site) | The documentation site. Builds **from the markdown in this repository**: `docs/`, `adr/`, the chart and CI READMEs stay where they are and stay readable on GitHub. |

**Status: running in production.** Measured **10/10 classification, 10/10 full
pass, 0 unsafe actions** on the eval cases against `qwen/qwen3.8-27b`, a local
model on a workstation, and 8/9 with zero unsafe actions against a 9B. Every
case is a real incident. See [`evals/`](evals) and [`local/`](local).

## Why the two halves live together

The gate and the agent are one loop, inspect then repair, and they are joined
by contracts that nothing else checks: the agent finds the gate's verdict by
searching pull-request comments for a marker the gate emits, and any version it
writes must appear *verbatim* in the gate's rendered report.

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

It comments, labels, and stops. **It never closes a pull request.**

## The safety model is code, not prompt

The model never **applies**. It returns a structured verdict and a proposal:
scalar edits for a mechanical fix, and, where swapping an apiVersion would
leave a document the new schema silently prunes, the complete migrated
document. The service applies what survives its own checks, behind a path
allowlist and a deny-list its own configuration cannot remove from.

A proposed document is a real widening: the model is authoring file content
rather than naming a value to swap. The harness bounds it instead of trusting
it. It refuses the proposal *whole* unless it keeps the object's identity
(`apiVersion` equal to the one the gate named; `kind`, `name` and `namespace`
byte-identical), fits the target schema, and contains no value that is not
either at that same path in the original, displaced by the schema change, or
dictated by the schema itself. What lands is re-serialised from the structure
the harness validated rather than written back as the model's text. The model
supplies structure; the original document supplies data.

So *"never edit the gate, never weaken a policy to go green"* is an invariant
the service enforces, not an instruction the model is asked to respect. A model
that ignores the prompt entirely still cannot touch CI configuration.

See [`adr/0001-structured-edits-not-agentic-loop.md`](adr/0001-structured-edits-not-agentic-loop.md)
for the original line, and
[`adr/0007-structure-from-the-schema-data-from-the-document.md`](adr/0007-structure-from-the-schema-data-from-the-document.md)
for where it moved.

## Install

**[`docs/onboarding.md`](docs/onboarding.md) is the whole path**: six steps,
each ending in a state you can verify. The short version:

```bash
helm install bosun oci://ghcr.io/jamesatintegratnio/charts/bosun \
  --namespace bosun --create-namespace \
  -f my-values.yaml
```

then protect the `addons-gate` check and commit a sources-only
`.gitops-gate.yaml`. The agent is the gate: it renders every open pull request
against the live cluster inventory, read from ArgoCD's API, and posts the
status and report itself. No CI workflow, no checked-in inventory snapshot, no
paths filter.

The same gate is also a container with an exit code, for local runs before
pushing:

```bash
docker run --rm -v "$PWD:/repo" -w /repo \
  ghcr.io/jamesatintegratnio/gitops-gate:main \
  diff -base targets-base.json -head targets-head.json -repo . -report report.md
```

Multi-arch, so it runs on an arm64 laptop as well as an amd64 machine. Nobody
reproduces a gate locally that they cannot run locally.

The chart deploys the service, its RBAC and both halves of its NetworkPolicy.
It consumes **existing Secrets by name**, so bring your own secret manager. See
[`charts/bosun/README.md`](charts/bosun/README.md) for the values contract, and
[`charts/bosun/values.schema.json`](charts/bosun/values.schema.json) for the
machine-checkable version of it.

Then point Kargo's promotion at it:

```yaml
- uses: http
  config:
    method: POST
    url: http://bosun.bosun.svc:8080/v1/promotion-opened
```

## Requirements

- **Kargo** 1.11 or newer, or anything else that can POST a promotion event.
- **ArgoCD**, whose API serves the live cluster inventory the gate renders
  against. It needs an account token with `clusters, get` and nothing else.
- A **git host**: `github` or `gitea` today, behind a small interface.
- An **OpenAI- or Anthropic-compatible model endpoint**. There is no default,
  on purpose: a service that silently starts spending money against a vendor
  you did not choose is a bad default.

Nothing here hardcodes a cluster, domain, namespace, CNI, secret manager, git
host or model provider. Those are values.

## Reference

Everything below is also published, cross-linked and searchable at
**[bosun.integratn.io](https://bosun.integratn.io)**: same files, built by
[`site/`](site).

| | |
|---|---|
| [`docs/onboarding.md`](docs/onboarding.md) | putting bosun onto a repository, start to finish |
| [`docs/the-loop.md`](docs/the-loop.md) | the whole system, walked through one pull request |
| [`docs/safety-model.md`](docs/safety-model.md) | allowlist, deny-list, attempt cap: what is enforced where |
| [`docs/classification.md`](docs/classification.md) | mechanical vs escalate, with worked examples |
| [`docs/prompt-contract.md`](docs/prompt-contract.md) | the prompt the eval numbers come from |
| [`docs/llm-providers.md`](docs/llm-providers.md) | the `llm.Provider` interface; adding one |
| [`docs/git-providers.md`](docs/git-providers.md) | the `gitprovider.Provider` interface; adding one |
| [`docs/supervisor.md`](docs/supervisor.md) | watching the promotion pipeline for what has silently stopped |
| [`gate/README.md`](gate/README.md) | the gate: what it checks, and what it deliberately does not |
| [`local/`](local) | a disposable cluster that runs the whole flow, and replays the ten recorded incidents against the live agent |
| [`adr/`](adr) | why it is built this way, and what each decision cost |

## Development

```bash
nix develop            # go, kubectl, kind, and helm pinned to the image's version
go test ./...          # unit tests and the eval suite
go test ./evals/...    # just the evals
hack/lint.sh           # helm lint + values schema validation
```

The dev shell pins helm to the version the images carry, because the gate's
verdict is the output of `helm template`. Render locally with a different helm
and the verdict changes while nothing about the symptom points at helm. See
[`CONTRIBUTING.md`](CONTRIBUTING.md#the-toolchain).

The [local proving ground](local) builds a throwaway cluster and runs the whole
flow against it: real pull requests, the live model, real commits pushed by the
service, and the gate answering from inside the cluster.

```bash
cd local
export LLM_BASE_URL=http://<your-host>:1234/v1
make up
make demo
```

## License

[PolyForm Internal Use 1.0.0](LICENSE).

**Run it for your own business, commercially, in production, without asking
anyone.** If Kargo is opening pull requests you have not got time to read, this
is for you and there is nothing to negotiate.

**You may not distribute it.** Not sold, not bundled into a product, not
offered as a hosted service, not handed on. Providing it to third parties in
any form needs a separate licence; ask, that is the intended path.

Stricter than "do not sell it": it rules out free redistribution too, so no
public forks and no deploying it on a client's behalf. Installing the chart and
image from the registry is *use*, not distribution.

## Provenance

Developed inside [`gitops_homelab_2_0`](https://github.com/JamesAtIntegratnIO/gitops_homelab_2_0)
as `delivery/`, and extracted here in two steps on 2026-08-23: the agent and
its chart first, then the gate, both Kargo charts and the proving ground. The
agent was called `delivery-agent` until that day.

That platform repository is still the reference consumer. It runs the agent
in-cluster, and every incident in [`evals/`](evals) came from it.

**Not here:** `kargo-observability`, which turns Kargo's own state into
Prometheus metrics and alerts. It shares no contract with the gate or the
agent, works for anyone running Kargo whether or not they want either, and
stays in the platform repository it was written for.
