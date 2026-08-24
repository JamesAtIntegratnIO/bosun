# 7. Structure from the schema, data from the document

- **Status:** accepted
- **Date:** 2026-08-24

## Context

[ADR 0001](0001-structured-edits-not-agentic-loop.md) drew a line: the model
proposes, the harness disposes. A proposed edit names a file, a key, a `from`
and a `to`, and the applier refuses it unless the path is allowed, the `from`
matches what the file holds, and any version-shaped `to` appears in the evidence
the model was shown. "Never edit the gate" and "never invent a version" are
therefore properties of the code.

That works because a scalar edit is *corroborable*. The file either contains the
`from` or it does not.

The deterministic repair added in 0.9.0 needs no model at all: when a chart
stops serving a CRD version, the gate names the kind, the dropped versions and
the survivor, and every declaring manifest gets its `apiVersion` line rewritten.
Arithmetic, not judgement.

It is arithmetic only while the two versions are **compatible**. A chart that
moves `spec.store` to `spec.secretStoreRef.name` between `v1beta1` and `v1`
leaves, after the swap, a document that parses, applies, and has `store` pruned
by the apiserver on the way in. The render is fine. The gate is green. The value
is gone, and nothing in the repository, the render or the gate can see it.

Enumerating every upstream's structural changes is not possible. In James's
words, revising the original no-model-edits rule:

> If we can do a deterministic edit without the agent great. But if we can't we
> should let the agent take a crack at it… what if the structure of the secret
> resource inside that api change also changed. We can't plan for every
> structure change like that. Instead the agent should go read existing api and
> the new api we are migrating to and then apply the appropriate edits.

The proposal surface therefore widens from a scalar to a whole document. The
question is what replaces corroboration, because corroboration cannot survive
the move: the whole point is that the shape changed, so there is no `from` to
match.

## Decision

**The model is a translator between two schemas it is shown.** It is given the
old schema, the new schema, one document, and the specific ways that document
does not fit. It returns the same object, shaped for the new schema. It is not
asked whether the migration is wise, which fields matter, or to improve anything
on the way past.

**The guarantees move from the proposal to the output**, and get stricter:

- **Identity.** `apiVersion` is the target the gate already named; `kind`,
  `metadata.name` and `metadata.namespace` are byte-identical to the original. A
  migration that renames the object is a second change riding inside one, and
  the gate would count it as an object appearing and another vanishing.
- **Schema validity.** The proposal is walked against the target schema by the
  same code that found the problem. This is the apiserver's own objection,
  raised before the apply rather than after it.
- **Value provenance.** Every scalar leaf in the proposal appears as a scalar
  leaf in the **original**, or is dictated by the target schema itself — a
  default, an enum member, a const. Field *names* come from the schema; *data*
  comes only from the document. This is the document-level analogue of "never
  invent a version", and it is what makes the translator claim true rather than
  hoped for.

And one report rather than a check: values present in the original and absent
from the proposal are **listed** in the comment. Some of those are correct — a
field the target no longer accepts has to go somewhere, and sometimes nowhere.
A human reads that list; nothing is dropped silently.

**A refusal refuses everything.** If any document in the pass fails, *nothing*
is pushed — not even the plain swaps that were fine. This is not the obvious
choice and it is the important one: the swap alone makes the gate **green**,
because no manifest declares a dropped version any more, while a document the
target schema rejects sits in the tree waiting to be pruned. A partial push
would produce a green gate over a broken change, which is the exact failure this
service exists to find.

**Schemas are fetched, never remembered.** The old one comes from the
CustomResourceDefinition installed in the cluster right now — which is why this
depends on [ADR 0006](0006-live-reads-are-scoped-by-group.md), and why it must
run pre-merge, since after the merge that shape is gone. The new one comes from
rendering the chart at the version being promoted to. The live CRD's own copy of
the target version is a fallback and is labelled as one in the comment; it is
usually right, and "usually right" is exactly the sort of claim that belongs on
the page rather than in an assumption.

**Bounded to where the deterministic path already had authority.** Only files
the swap rewrote, only top-level documents, capped per pull request, inside the
same attempt cap. `Restructurer` is a second interface, type-asserted: a
provider without it degrades to the plain swap.

## Consequences

**Good.** The class of bump that renders green and breaks at runtime — the
class this whole project exists for — now has an automatic answer where one is
safely available, and a refusal carrying the rejected proposal where it is not.
The failure mode is an escalation with a diff, never a partial write.

**Bad.** A reshaped document is **re-serialised**, so comments inside that
document do not survive. The diff in the comment shows exactly what changed, but
a reader who kept a note in a manifest loses it. That is a real cost and there
is no version of this that avoids it: preserving comments means surgical line
edits, and a line edit is precisely what cannot express a change of shape.

**Bad.** Nested and embedded manifests are out of scope. `migrate` deliberately
reaches into `extraObjects:` lists and block scalars — 13 of 27 declaring files
in the incident this was built from held the declaration somewhere other than
the top level — because swapping one value on one line inside a values file is
safe. *Replacing a document* inside one is not: it means re-serialising a file
whose every remaining line would move. Those are reported as skipped and reach a
human.

**Also.** The agent image now carries helm, for the same reason the gate's does:
rendering has to match what the cluster's own Helm does, and pinning a library
is a slower way to drift away from that.

## Alternatives rejected

- **Keep the no-model-edits rule and escalate every structural change.** The
  status quo, and the reason this exists: those escalations were correct, and
  the human's answer was the same mechanical translation every time.
- **Let the model return a patch instead of a document.** A patch has to be
  applied, and applying is where a partial write lives. A whole document can be
  checked before anything is written.
- **Ask the model to say whether it is confident.** A confidence field is a
  channel for talking the harness out of a refusal. `MigrationSchema` has two
  fields and no room for negotiation.
- **Push the successful documents and escalate the rest.** Produces a green gate
  over a broken change. See above.
- **Take both schemas from the chart render.** The old shape is not in the new
  chart. It is only in the cluster, and only until this merges.
