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
pointers, and the pull request's checkout supplies every byte that renders.
`Assemble` compares two target tables; given both checkouts it also renders
every Application this change moves -- the chart version bumped, or a values
file it layers edited -- on both sides, and diffs the resources down to the
fields that changed. `ValidateManifests` schema-validates every rendered stream
with kubeconform. The rendered report and the `DiffResult` it comes from are
the contract the agent consumes.

The two sides are the **merge base** and the head, not the base branch's tip
and the head. The tip is what a merge lands on and it is the wrong revision to
diff against: it moves whenever anything else merges, and each commit it gained
since the branch was cut then appears in the report, backwards, as this pull
request's doing. The merge base is the only revision at which the two sides
differ by exactly this pull request, and the report names both of them.

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
| The chart will not render at the **head** revision | yes | the Application cannot sync once this merges. Failing at the *base* revision too is a different fact -- the repository was already in that state, and there is no diff to compute either way -- so that one stays a coverage warning under **Not covered**. On a values-only change the base is asked first, because a chart that renders nowhere outside a cluster fails on both sides and is nobody's fault |
| A setting this repository makes that the new chart version no longer declares | yes | helm ignores an unknown value rather than failing on it, so the setting stops applying while the render, the values file and the text diff all stay identical. Measured at 48 of 77 settings on one kyverno bump |
| A CRD stops serving a version **that manifests in the repository still declare** | yes, while any remain | those manifests break at apply. The report names the consumer kind, the surviving version and the declaring files, which is the contract [the agent's deterministic repair](../docs/safety-model.md) executes, and the recount on the re-run is what verifies it. Counted at zero, the finding is reported and does not block |
| Resources added, removed, changed; versions moved | no, reported | that is what a version bump legitimately does, reported with per-field diffs so the reviewer judges evidence rather than a count |
| An addon that did not exist at the base revision | no, reported | the author meant to add it, so there is nothing to warn anyone about. The row is printed for the [expansion](#new-addons) it carries, which the text diff does not contain |

## New addons

Not every pull request is a bump. When one adds an addon there is no base
version to compare against, so the report carries a **New addons** table rather
than a finding:

| Application | Cluster | Source |
|---|---|---|
| `kargo-observability-hub` | hub | `charts/kargo-observability (path)` |

One row per generated Application. An addon that reaches four clusters prints
four rows, which is how you see that it reaches four. `Source` is the chart
repository, chart and pinned version for a Helm source, and the directory for a
path source.

The expansion is what a reviewer cannot get anywhere else. A new addon arrives
as one entry in a values file or one new directory, and what that entry
becomes -- how many Applications, on which clusters, from which chart at which
version -- is the ApplicationSet's business rather than the diff's. A
pull-request description reading "add kargo-observability" is true and tells
you none of it.

The report lists no resource changes underneath. Chart-diff renders an
Application's chart at both of its versions, and a first appearance has only
one, which is what the section means by "nothing changed underneath them".

An addon that *already existed* and gains a cluster is a different finding, and
it blocks. The gate tells the two apart by whether the ApplicationSet was there
at the base revision; [`docs/render-diff-schema.md`](docs/render-diff-schema.md)
has both.

## Reference

- [`docs/config-reference.md`](docs/config-reference.md): why most repositories need no config file, and the full `.bosun.yaml` schema for the ones that do
- [`docs/render-diff-schema.md`](docs/render-diff-schema.md): the diff result the agent consumes, bucket by bucket
- [`docs/rendered-manifests.md`](docs/rendered-manifests.md): the rendered-manifests pattern, and why ArgoCD's source hydrator cannot gate a merge
