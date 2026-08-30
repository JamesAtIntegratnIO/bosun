# 1. The model proposes structured edits; it does not edit files

- **Status:** accepted — **scope widened by
  [ADR 0007](0007-structure-from-the-schema-data-from-the-document.md)** on
  2026-08-24 and by
  [ADR 0013](0013-a-values-migration-is-a-plan-not-a-document.md) on 2026-08-29
- **Date:** 2026-08-22

> **Read this with 0007 and 0013.** The rule below still holds where it
> matters: the model does not apply, and the harness does. What changed is the
> size of what it may propose. This ADR assumed a proposal is always a *scalar*
> edit — path, key, `from`, `to` — because a scalar is corroborable against the
> file. 0007 widened the proposal to a whole migrated document for the case a
> version swap cannot reach, and replaced `from`-matching with three
> deterministic checks on the output. 0013 widened it again to a whole values
> document, checked by those three plus a render of the chart itself.
>
> **Do not quote this title as a guarantee.** On both widened paths the model
> authors file content, and on the scalar path the `to` it names is written to
> the line verbatim, so "it does not edit files" is true only in the sense it
> was written in: the model applies nothing and chooses no path. Pages that
> shortened it to "the model does not edit files" were wrong, and one of them
> was the front page. The claim to make instead is the one in
> [`docs/safety-model.md`](../docs/safety-model.md): the model proposes, the
> harness applies, and each path names what checked the proposal.

## Context

The agent has to fix a class of broken pull request: a chart minor flipped a
default we depend on, a pin has to move with another pin, a metrics port moved
while a NetworkPolicy still names the old number. Fixing these means changing
files in the repository.

The obvious build is an agentic coding loop — hand the model file-read and
file-edit tools and let it work. Two things argue against it.

**Portability.** An agentic loop is easiest on an SDK tied to one model vendor.
This package has to run against whatever the operator already has: a hosted
API, a cloud-provider gateway, or something self-hosted behind an
OpenAI-compatible endpoint. A loop built on one vendor's tool-use protocol
makes the others second-class.

**Safety.** With file-edit tools, "never edit the gate" and "never weaken a
merge policy to go green" are instructions in a prompt. A model that can edit
any file can turn a red gate green by deleting the check — which is exactly
the failure mode that matters, because it is indistinguishable from success.

## Decision

The model returns a **structured verdict and a proposed edit set** — path, key,
old value, new value, rationale. The agent applies those edits itself, behind a
path allowlist and a per-pull-request attempt cap.

Two provider implementations cover the field: one speaking the Anthropic
Messages API, one speaking OpenAI chat completions. Between them they reach
hosted Anthropic, Bedrock, Vertex, OpenAI, Azure OpenAI, vLLM, Ollama and
anything behind a LiteLLM-style proxy.

## Consequences

**Good.** The safety boundary is code. The agent cannot edit the gate because
the allowlist rejects the path, not because the prompt asked it not to. Any
provider that can return structured output works. Every proposed change is
inspectable before it is applied, and loggable after.

**Bad.** Some real fixes cannot be expressed as an edit set — anything needing
a new file, a large restructure, or a change whose shape depends on reading
several files first. Those escalate to a human instead of being attempted.
That is the intended trade: the escalation path is cheap, and a wrong
autonomous fix that renders clean is expensive.

**Also.** We give up the SDK's loop and own the prompt/parse/apply cycle. That
is a few hundred lines, and it is the part we most want to control.

## Alternatives rejected

- **Agentic loop on a single vendor's SDK.** Less code, handles messier fixes,
  and would have made self-hosted models a second-class path. Rejected on
  portability first, safety second.
- **One provider implementation behind a proxy.** Making an operator run a
  gateway before they can run the agent is a poor first-use experience.
