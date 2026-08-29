# gate

The deterministic half of the delivery gate. One Go package that answers one
question about a pull request: **does this change what gets deployed, and is
what it produces still valid?**

The agent imports this package and runs it in-cluster against the live cluster
inventory, read from ArgoCD's API ([ADR 0008](../adr/0008-the-gate-moves-in-cluster.md)).
That is how every pull request gets its verdict. No model is involved at this
layer: the AI half lives in the agent that calls this package.

There is no second way to run it. The standalone `gitops-gate` CLI and its
image were retired by [ADR 0010](../adr/0010-the-cli-goes-too.md) once the gate
in the agent was the only consumer left; the engine's history from that era is
folded into the repository's [CHANGELOG.md](../CHANGELOG.md).

## What it does

`Render` expands every source it is given, for every cluster in the inventory,
expanding the generators, and emits a normalized target table. Those sources
are derived from the Applications and ApplicationSets ArgoCD serves
([ADR 0012](../adr/0012-the-repo-stops-repeating-the-ship.md)), merged with
whatever the gated repository's own `.bosun.yaml` adds; live supplies the
pointers, and the pull request's checkout supplies every byte that renders. `Assemble` compares two target tables; when a repository root is
available, it also renders every chart whose version moved, at both versions,
and diffs the resources down to the fields that changed. `ValidateManifests`
schema-validates every rendered stream with kubeconform. The rendered report
and the `DiffResult` it comes from are the contract the agent consumes.

It shells out to `helm` and `kubeconform` rather than vendoring their
libraries: chart rendering has to match what the cluster's own Helm does, and
pinning a library version is a slower way to drift away from that. Both ship
in the agent's image, pinned.

## What blocks, and why

| Finding | Blocks | Because |
|---|---|---|
| Cluster targeting changed | yes | a values-layer edit can add or remove a whole cluster from an addon's scope without the text diff showing it. The selector did not change; the labels it matches did. Rendering both sides and diffing the *expanded* result is the only way to see it |
| Source, project or namespace changed | yes | no version bump can cause these, so nobody has explained them |
| An object's apiVersion moved | yes | a migration wearing a version number, which passes the render and fails when the apiserver sees it |
| The chart will not render at the **new** version | yes | the Application cannot sync once this merges. Failing at the *base* version is a different fact -- the repository was already in that state, and there is no diff to compute either way -- so that one stays a coverage warning under **Not covered** |
| A setting this repository makes that the new chart version no longer declares | yes | helm ignores an unknown value rather than failing on it, so the setting stops applying while the render, the values file and the text diff all stay identical. Measured at 48 of 77 settings on one kyverno bump |
| A CRD stops serving a version **that manifests in the repository still declare** | yes, while any remain | those manifests break at apply. The report names the consumer kind, the surviving version and the declaring files, which is the contract [the agent's deterministic repair](../docs/safety-model.md) executes, and the recount on the re-run is what verifies it. Counted at zero, the finding is reported and does not block |
| Resources added, removed, changed; versions moved | no, reported | that is what a version bump legitimately does, reported with per-field diffs so the reviewer judges evidence rather than a count |

## Reference

- [`docs/config-reference.md`](docs/config-reference.md): why most repositories need no config file, and the full `.bosun.yaml` schema for the ones that do
- [`docs/render-diff-schema.md`](docs/render-diff-schema.md): the diff result the agent consumes, bucket by bucket
- [`docs/rendered-manifests.md`](docs/rendered-manifests.md): the rendered-manifests pattern, and why ArgoCD's source hydrator cannot gate a merge
