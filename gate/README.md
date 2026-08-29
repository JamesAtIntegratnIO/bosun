# gitops-gate

The deterministic half of the delivery gate. One Go package that answers one
question about a pull request: **does this change what actually gets deployed,
and is what it produces still valid?**

The same engine ships in two forms, reaching the same verdict. The agent
imports it and runs it in-cluster against the live cluster inventory, read from
ArgoCD's API ([ADR 0008](../adr/0008-the-gate-moves-in-cluster.md)) — that is
how every pull request gets its verdict, and onboarding does not involve this
directory at all. It also ships as a CLI ([`cmd/gitops-gate`](cmd/gitops-gate))
whose exit code is the verdict, for running locally before a push. The CLI
renders against a checked-in inventory snapshot, which is what `clusters
export` maintains.

> **Status: shipped and judging every pull request** on the platform it was
> built for, published as `ghcr.io/jamesatintegratnio/gitops-gate`.
> [`CHANGELOG.md`](CHANGELOG.md) records what has changed since. No model is
> involved at this layer: the AI half lives in the agent that calls this
> package.

## Subcommands

| Command | Does |
|---|---|
| `render` | Renders every bootstrap ApplicationSet declared in `.gitops-gate.yaml`, for every cluster in the inventory, expanding the generators. Emits a normalized target table. |
| `diff` | Compares two target tables. With `-repo`, also renders every chart whose version moved — at both versions — and diffs the resources, down to the fields that changed. Emits the report and `render-diff.json`. |
| `validate` | Schema-validates every rendered stream. |
| `clusters export` | Regenerates the CLI's inventory snapshot from the live ArgoCD cluster Secrets. **Workstation only** — shells out to `kubectl` against a kubeconfig, and `kubectl` is not in the gate's image. |

### What the image carries

`helm` and `kubeconform`, both pinned. Nothing else. Two paths therefore need a
binary the image does not have, and both say so rather than failing obscurely:

| Path | Needs | Where it runs |
|---|---|---|
| `clusters export` | `kubectl` | a workstation, against a kubeconfig. The in-cluster gate reads the same four fields from ArgoCD's API instead, and needs no snapshot. |
| a `kustomize` source in `.gitops-gate.yaml` | `kustomize` **or** `kubectl` | a workstation. Not in-cluster. |

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
| `2` | The gate itself could not run (bad config, unreachable chart repo). Distinct from `1` so a caller can tell "this change is bad" from "the gate is broken". |

## Reference

- [`docs/config-reference.md`](docs/config-reference.md) — the full `.gitops-gate.yaml` schema
- [`docs/render-diff-schema.md`](docs/render-diff-schema.md) — the JSON contract the agent consumes
- [`docs/rendered-manifests.md`](docs/rendered-manifests.md) — the rendered-manifests pattern, and why ArgoCD's source hydrator cannot gate a merge
