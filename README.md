# Bosun

⚓ **The crew for Argo and Kargo.** Gating, triage and visibility for a GitOps
pipeline that merges its own pull requests.

> A boatswain's job is not repair — it is *inspection and repair*: daily rounds
> of the hull, rigging and cables, fixing what they find on their own
> authority, and reporting to the captain what they cannot. The inspection is
> the larger half, and it is what makes the repair authority safe to grant.
>
> Bosun sits beside Argo (the ship) and Kargo (the cargo).

| Piece | What it does |
|---|---|
| [`gate/`](gate) | **The inspection round.** Renders your ApplicationSets at base and at head, fails when an app's *cluster targeting* changes, diffs the old and new chart render, schema-validates the result. A container with an exit code — run it from any CI. |
| the agent *(this module's root)* | **The repair.** Reads a red gate, explains it on the pull request, and fixes what the rendered diff *proves* is mechanical. Escalates everything else. |
| [`charts/kargo-pipelines`](charts/kargo-pipelines) | Warehouses and Stages from one target list, with multi-stage promotion chains, verification gating and the triage hook that calls the agent. |
| [`charts/bosun`](charts/bosun) | Runs the agent in-cluster, triggered by Kargo rather than polled. |

Kargo is very good at producing change. Left alone it opens more pull requests
than anyone can read, and merges a good share of them on a version-shaped
policy that cannot see what is in the diff. This closes those two gaps.

> **Not here:** `kargo-observability`, which turns Kargo's own state into
> Prometheus metrics and alerts. It shares no contract with the gate or the
> agent, works for anyone running Kargo whether or not they want either, and
> belongs to nothing in this repository. It stays in the platform repository
> it was written for.

**Status: running in production.** Measured 9/9 on the eval cases against
`qwen/qwen3.8-27b` and 8/9 with zero unsafe actions against a 9B model on a
workstation. See [`evals/`](evals) and [`local/`](local).

## Why the two halves live together

The gate and the agent are one loop — inspect, then repair — and they are
joined by contracts that nothing else checks: the agent finds the gate's
verdict by searching pull-request comments for a marker the gate emits, and
any version it writes must appear *verbatim* in the gate's rendered report.

Both of those broke, silently, while the two halves were separate packages.
A boundary is safe where its contract can be tested; put the two sides in
different repositories and no CI run can ever check them together.

## What it will and will not do

**Fixes autonomously** — only where the render diff *proves* the cause: a chart
default that flipped, a coupled pin, a metrics port that moved under a policy
that still names it.

**Escalates** — an API version change, a removed CRD, a dropped subchart, an
upstream note mentioning a schema or database migration, a version skip the
chart itself refuses, or any fix needing a file outside the addon's own tree.

It comments, labels, and stops. **It never closes a pull request.**

## The safety model is code, not prompt

The model does not edit files. It returns a structured verdict and a proposed
edit set; the service applies those edits deterministically behind a path
allowlist and a deny-list its own configuration cannot remove from.

So *"never edit the gate, never weaken a policy to go green"* is an invariant
the service enforces, not an instruction the model is asked to respect. A model
that ignores the prompt entirely still cannot touch CI configuration.

See [`adr/0001-structured-edits-not-agentic-loop.md`](adr/0001-structured-edits-not-agentic-loop.md).

## Install

```bash
helm install bosun oci://ghcr.io/jamesatintegratnio/charts/bosun \
  --namespace bosun --create-namespace \
  -f my-values.yaml
```

The gate runs in CI, not the cluster:

```bash
docker run --rm -v "$PWD:/repo" -w /repo \
  ghcr.io/jamesatintegratnio/gitops-gate:main \
  diff -base targets-base.json -head targets-head.json -repo . -report report.md
```

Ready-made CI adapters are in [`ci/`](ci). Multi-arch, so it runs on an arm64
laptop as well as an amd64 runner — a gate you cannot reproduce locally is a
gate nobody reproduces before pushing.

The chart deploys the service, its RBAC and both halves of its NetworkPolicy.
It consumes **existing Secrets by name** — bring your own secret manager. See
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
- A **gate** that posts a report comment and a commit status on the pull
  request. Bosun reads that comment; it does not render anything itself.
- A **git host** — `github` or `gitea` today, behind a four-method interface.
- An **OpenAI- or Anthropic-compatible model endpoint**. There is no default,
  on purpose: a service that silently starts spending money against a vendor
  you did not choose is a bad default.

Nothing here hardcodes a cluster, domain, namespace, CNI, secret manager, git
host or model provider. Those are values.

## Reference

| | |
|---|---|
| [`docs/safety-model.md`](docs/safety-model.md) | allowlist, deny-list, attempt cap — what is enforced where |
| [`docs/classification.md`](docs/classification.md) | mechanical vs escalate, with worked examples |
| [`docs/prompt-contract.md`](docs/prompt-contract.md) | the prompt the eval numbers come from |
| [`docs/llm-providers.md`](docs/llm-providers.md) | the `LLMProvider` interface; adding one |
| [`docs/git-providers.md`](docs/git-providers.md) | the `GitProvider` interface; adding one |
| [`gate/README.md`](gate/README.md) | the gate: what it checks, and what it deliberately does not |
| [`ci/`](ci) | CI adapters, and the contract an adapter must satisfy |
| [`local/`](local) | a disposable cluster that runs the whole flow, and replays nine real incidents against the live agent |
| [`adr/`](adr) | why it is built this way, and what each decision cost |

## Development

```bash
go test ./...          # unit tests and the eval suite
go test ./evals/...    # just the evals
hack/lint.sh           # helm lint + values schema validation
```

The [local proving ground](local) builds a throwaway cluster and replays nine
incidents that really happened to the platform this was built for — real pull
requests, the live model, real commits pushed by the service:

```bash
cd local
export LLM_BASE_URL=http://<your-host>:1234/v1
make up
make scenarios
```

## License

[PolyForm Internal Use 1.0.0](LICENSE).

**Run it for your own business, commercially, in production, without asking
anyone.** If Kargo is opening pull requests you have not got time to read, this
is for you and there is nothing to negotiate.

**You may not distribute it.** Not sold, not bundled into a product, not
offered as a hosted service, not handed on. Providing it to third parties in
any form needs a separate licence — ask, that is the intended path.

Stricter than "do not sell it": it rules out free redistribution too, so no
public forks and no deploying it on a client's behalf. Installing the chart and
image from the registry is *use*, not distribution.

## Provenance

Developed inside [`gitops_homelab_2_0`](https://github.com/JamesAtIntegratnIO/gitops_homelab_2_0)
as `delivery/`, and extracted here in two steps on 2026-08-23: the agent and
its chart first, then the gate, both Kargo charts, the CI adapters and the
proving ground. The agent was called `delivery-agent` until that day.

That platform repository is still the reference consumer — it runs the gate in
CI and the agent in-cluster, and every incident in [`evals/`](evals) came from
it.
