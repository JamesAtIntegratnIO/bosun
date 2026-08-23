# Bosun

⚓ **The judgement half of a GitOps delivery gate.** It reads a red gate,
explains it on the pull request, and fixes the cases that are mechanically
provable from the rendered diff. Everything else it escalates to a human.

Triggered by Kargo's `http` promotion step — event-driven, never polled.

> A bosun is the crew member who makes routine repairs on their own authority
> and reports serious damage to the captain. That is exactly the split this
> service draws between a mechanical fix and an escalation. It sits beside
> Argo (the ship) and Kargo (the cargo).

**Status: running in production.** Measured 9/9 on the eval cases against
`qwen/qwen3.8-27b` and 8/9 with zero unsafe actions against a 9B model on a
workstation. See [`evals/`](evals) and [`local/`](local).

## The problem

Kargo is very good at producing change. Left alone it opens more dependency-bump
pull requests than anyone can read, and merges a good share of them on a
version-shaped policy that cannot see what is actually in the diff.

A gate that renders both sides and diffs the *result* catches the dangerous
ones — but now every red gate needs a human to read it. Most of those reds are
mechanical: a chart minor flipped a default you depend on, a pin has to move
with another pin, a metrics port moved while a NetworkPolicy still names the old
number. Bosun handles that class and leaves the rest alone.

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
| [`local/`](local) | a disposable cluster that replays nine real incidents against the live service |
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

[PolyForm Noncommercial 1.0.0](LICENSE). Free for any noncommercial purpose —
personal projects, research, education, charities, government. **Commercial use
requires a separate license from the copyright holder.** Ask.

## Provenance

Extracted from [`gitops_homelab_2_0`](https://github.com/JamesAtIntegratnIO/gitops_homelab_2_0)
at `336d8df`, where it was developed as `delivery/images/bosun` and
`delivery/charts/bosun`. It was called `delivery-agent` until 2026-08-23.
The gate it reads, the Kargo pipeline chart that calls it, and the
observability chart beside it still live there under `delivery/`.
