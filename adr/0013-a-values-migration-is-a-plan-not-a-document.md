# 13. A values migration is a plan, not a document

- **Status:** accepted
- **Date:** 2026-08-29

## Context

A chart bump can break a repository in a way neither repair the agent has can
touch.

Kargo raised `bosun` from 0.20.0 to 0.25.1. The values had been carrying
`gate.mode`, `gate.wait`, `gate.inventorySource` and `gate.argocd.port` since
before those keys were removed, the new chart's `values.schema.json` refuses
them, and `helm template` fails outright. The Application cannot sync. A human
fixed it by hand, and the agent's own comment had named all four keys
correctly, because there was nothing it could do with them.

Neither existing write path can express the fix:

| Needed | Operation | `edits` | `migrate` |
|---|---|---|---|
| `gate.mode`, `gate.wait`, `gate.inventorySource` | delete a key | no — nothing to put in `to` | no — it swaps `apiVersion` values |
| `gate.argocd.port` → `podPort` | rename a key | no — a delete plus an add | no |
| a newly required key | add a key | no — the key must already exist | no |

`edits` cannot be widened to cover them, and the reason is
[ADR 0001](0001-structured-edits-not-agentic-loop.md)'s: corroboration **is**
the `from` match. A deletion has no `from`→`to` to check, so an `edits` that
could delete would be an `edits` with an uncorroborated operation in it. That
is not a bug in `edits`; it is the wrong mechanism.

[ADR 0007](0007-structure-from-the-schema-data-from-the-document.md) already
solved this shape of problem for manifests: the model authors a whole document,
and the harness validates it against a **target schema** and re-serialises from
the validated structure rather than from the model's text. The parallel is
close enough to be suspicious.

| CRD migration (0007) | Values migration |
|---|---|
| Target schema from the rendered CRD | Target schema from the chart's `values.schema.json` |
| `apiVersion` must equal the gate's target | The chart version is the gate's target by construction |
| `kind`/`name`/`namespace` byte-identical | Every value the new chart still declares stays byte-identical |
| Every scalar leaf is in the original, or dictated by the schema | Same rule, unchanged |
| Re-serialised from the validated map | **No** — see the decision |

## Decision

**The values migration is the structural path, with one change: the harness
writes a plan, not a document.**

### The schema is the chart's own, and it is already in hand

The target schema is `values.schema.json` from the chart at the version the
change moves to. The gate already pulls that chart to render it and `helm show`
is already wired, so the file arrives through `helm pull --untar` from the same
host, the same chart and the same version the gate has already reached. No new
egress, no new trust, and the same policy check.

The base version's schema comes the same way, and is what makes a value
*displaced*: the check that a value is allowed to appear somewhere new only if
the target schema no longer accepts where it was.

### The three refusals, one of them new

A proposal is written only if it passes all three, and they are about the
output rather than the proposal:

- **Survival.** Every value the target schema *still accepts* is at the same
  path in the proposal, byte-identical. This replaces 0007's identity check and
  does the same job: a migration that quietly retunes a setting it was not
  asked about is a second change riding inside one. It is also what stops a
  displaced value landing on a key that already had one.
- **Schema validity.** The proposal is walked against the target schema by the
  same code that found the problem. This is helm's own objection, raised before
  the render rather than during it.
- **Value provenance**, positional and unchanged from 0007. A value in the
  proposal came from the same path in the original, from a path the target
  schema rejects, or from the target schema itself — a default, an enum member,
  a const.

### And a fourth guarantee the CRD path cannot have

**The chart is re-rendered with the proposed values before anything is
written.** If it still does not render, nothing is. A migrated CRD document is
judged by a schema walk and hope; a migrated values file is judged by the
program that refused it.

### Where repair ends and escalation begins

**A required key whose value the schema does not dictate is an escalation, and
it is decided before the model is called.** If the target schema requires a
field, the original does not supply it, and the schema names no default, no
`const` and no single-member `enum`, then no honest answer exists and asking
for one invites an invented value. `metrics.serviceMonitor.namespace` had no
correct answer derivable from the chart — the human who fixed 0.25.1 chose
`monitoring` from context the chart does not contain, which is exactly the
judgement this line reserves for a person.

The rule stated plainly: **the harness repairs what the chart's own schema
proves, and escalates what needs an author.**

### The write is a plan, and this is the part that differs from 0007

0007 re-serialises the validated document, and pays for it: comments inside a
migrated manifest do not survive, and the ADR says so as a cost with no way
around it. That cost is acceptable for a manifest, which is usually a document
of its own. It is not acceptable here.

A repository's chart values are rarely a file. They are a subtree of a file
that also holds thirty other addons, and the values that are annotated are
exactly the ones somebody had to reason about. Re-serialising the enclosing
file to change three keys would reformat every line of it and discard every
note in it, which is the same objection `edits` and `migrate` both already
make in their package comments.

So the harness does not write the model's document. It **diffs the original
against the validated proposal into a plan** of three operations — remove a
key, rename a key, set a key — and applies each one surgically through
`yaml.Node`, the way `edits` writes a scalar: the key's own lines change and
nothing else in the file moves.

The model never names a file, a key or an operation. It returns a document;
the plan is computed from two structures the harness has already validated.
That is a stronger position than `edits`, where the model does name the key.

### Finding the keys

An operation names a dotted path in the *values* document, and the file holds
it under some prefix — `""` for a values file the Application passes with
`-f`, `bosun.valuesObject` for an addon in a chart-of-charts. The prefix is
discovered, not configured: for each key the plan touches, the candidate files
are searched for an entry whose path ends in that key and whose value matches,
and the prefixes are intersected. **A prefix that is not unique across the
candidate files is a refusal**, not a guess.

## Consequences

**Good.** The bump this came from now has an automatic answer, and it is the
boring mechanical one: three keys removed, one renamed, proved by re-rendering
the chart. The class of failure — a chart the repository has outgrown — is
common, dull, and was reaching a human every time.

**Good.** Comments and formatting survive, so the diff a reviewer reads is the
three lines that changed. 0007's stated cost does not apply here, and the
mechanism that avoids it is available to 0007 in principle, if the appetite
ever exists to plan a manifest migration rather than re-serialise it.

**Bad.** A key the chart *renamed* that the model fails to move becomes a lost
setting: the render succeeds, the gate goes green, and the value is gone. That
is the exact failure this project exists to prevent, arriving through the door
this ADR opens. Three things stand against it — the model is shown both
schemas, every value not carried across is named in the comment, and a human
reviews the pull request — and none of them is a guarantee. This is the
residual risk, and it is stated rather than mitigated away.

**Bad.** A value that is not a scalar cannot be moved. A whole object changing
its parent is a remove plus an insert of a block, and the line editor writes
scalars. Refused and escalated, with the key named.

**Bad.** A flow mapping (`{a: 1, b: 2}`) cannot be edited by this path. The
line surgery works on block mappings; a key inside a flow mapping is refused.

**Cost.** One `helm pull` per version and one extra `helm template` per repair,
on a path that only runs for a pull request that is already red.

## Alternatives rejected

- **Extend `edits` with a delete operation.** Corroboration is the `from`
  match. An operation with no `from` is an operation with no check, and it
  would sit in the package whose whole documented purpose is that everything
  in it has one.
- **Re-serialise the values file, as 0007 does for manifests.** A manifest is
  usually a document; chart values are usually a subtree of a shared file. The
  diff would be the whole file and the annotations would be gone.
- **Let the model return the plan directly.** A plan is applied, and applying
  is where a partial write lives. Worse, a plan names keys, and a model naming
  a key to delete is a model deleting a key: the whole reason the document is
  the proposal surface is that the plan can then be *derived* rather than
  trusted.
- **Delete every rejected key deterministically, with no model at all.** This
  is nearly right — the evidence for removing `gate.mode` is computed from the
  chart, not asserted — and it is wrong for renames, which are indistinguishable
  from removals without judgement. Deleting `port` when the chart renamed it to
  `podPort` renders green and silently drops the setting.
- **Guess the rename by name similarity.** `port` → `podPort` is easy and
  `store` → `secretStoreRef.name` is not, and a heuristic that is right most of
  the time writes silently when it is wrong.
- **Ask the model to fill a required key it cannot derive.** That is the one
  place invention is guaranteed, because there is nothing to invent from. See
  the boundary above.
