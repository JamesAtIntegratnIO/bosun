# gitops-gate

The deterministic half of the delivery gate. One Go package that answers one
question about a pull request: **does this change what actually gets deployed,
and is what it produces still valid?**

It has two faces, same engine, same verdict. The agent imports it and runs it
in-cluster against the live ArgoCD inventory — the default since [ADR
0008](../adr/0008-the-gate-moves-in-cluster.md), and the reason onboarding no
longer involves this directory at all. And it ships as a CLI
([`cmd/gitops-gate`](cmd/gitops-gate)) whose exit code is the verdict, for
running locally before a push and for the CI fallback; adapters in
[`../ci`](../ci) are thin wrappers that pass a workspace in and turn the exit
code into a commit status. The CLI renders against a checked-in inventory
snapshot, which is what `clusters export` maintains.

> **Status: shipped and judging every pull request** on the platform it was
> built for, published as `ghcr.io/jamesatintegratnio/gitops-gate`. This README
> opened as the contract the implementation was written against; the
> implementation caught up and the contract grew — see
> [`CHANGELOG.md`](CHANGELOG.md) for what it has learned since. The AI half is
> deliberately *not* here: it lives in the agent that calls this package.

## Subcommands

| Command | Does |
|---|---|
| `render` | Renders every bootstrap ApplicationSet declared in `.gitops-gate.yaml`, for every cluster in the inventory, expanding the generators. Emits a normalized target table. |
| `diff` | Compares two target tables. With `-repo`, also renders every chart whose version moved — at both versions — and diffs the resources, down to the fields that changed. Emits the report and `render-diff.json`. |
| `validate` | Schema-validates every rendered stream. |
| `clusters export` | Regenerates the cluster inventory from live ArgoCD cluster Secrets. |

## What blocks, and why

| Finding | Blocks | Because |
|---|---|---|
| Cluster targeting changed | yes | a values-layer edit can add or remove a whole cluster from an addon's scope without the text diff showing it — the selector did not change, the labels it matches did. Rendering both sides and diffing the *expanded* result is the only way to see it |
| Source, project or namespace changed | yes | no version bump can cause these, so nobody has explained them |
| An object's apiVersion moved | yes | a migration wearing a version number — it renders perfectly and breaks at runtime |
| A CRD stops serving a version **that manifests in the repository still declare** | yes, while any remain | those manifests break at apply. The report names the consumer kind, the surviving version and the declaring files — which is exactly the contract [the agent's deterministic repair](../docs/safety-model.md) executes, and the recount on the re-run is what verifies it. Counted at zero, the finding is reported and does not block |
| Resources added, removed, changed; versions moved | no, reported | that is what a version bump legitimately does — reported with per-field diffs so the reviewer judges evidence, not a count |

## Exit codes

| Code | Meaning |
|---|---|
| `0` | No blocking change. |
| `1` | Blocking change — see the table above. |
| `2` | The gate itself could not run (bad config, unreachable chart repo). Distinct from `1` so CI can tell "this change is bad" from "the gate is broken". |

## Reference

- [`docs/config-reference.md`](docs/config-reference.md) — the full `.gitops-gate.yaml` schema
- [`docs/render-diff-schema.md`](docs/render-diff-schema.md) — the JSON contract the agent consumes
- [`docs/adding-a-ci-provider.md`](docs/adding-a-ci-provider.md)
- [`docs/rendered-manifests.md`](docs/rendered-manifests.md) — the rendered-manifests pattern, and why ArgoCD's source hydrator cannot gate a merge
