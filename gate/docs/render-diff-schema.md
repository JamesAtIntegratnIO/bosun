# The diff result

What the gate's diff reports, and the contract the agent reads. `Assemble`
returns it as a `DiffResult` and the agent consumes it in-process; the JSON
below is the struct's own shape, shown because it is the compact way to see
the whole contract at once. Nothing writes it to a file: the `render-diff.json`
artifact was the CLI's, and went with it
([ADR 0010](../../adr/0010-the-cli-goes-too.md)).

The field names are still an interface. The report the agent publishes is
rendered from these buckets, and the deterministic repair reads findings back
out of them.

```json
{
  "targeting": [
    {
      "kind": "added",
      "cluster": "tenant",
      "app": "goldilocks-tenant",
      "appset": "goldilocks",
      "to": "https://charts.example/goldilocks 11.0.0",
      "detail": "newly generated for this cluster"
    }
  ],
  "introduced": [
    {
      "kind": "introduced",
      "cluster": "hub",
      "app": "kargo-observability-hub",
      "appset": "kargo-observability",
      "to": "charts/kargo-observability (path)",
      "detail": "new addon, first appearance"
    }
  ],
  "versions": [
    {
      "kind": "version",
      "cluster": "hub",
      "app": "cert-manager-hub",
      "appset": "cert-manager",
      "from": "v1.21.1",
      "to": "v1.22.0"
    }
  ],
  "other": [],
  "warnings": ["addons: generators[1]: git generator is not expanded ..."]
}
```

## The four buckets, and why they are separate

**`targeting`**: an Application is generated for a different set of clusters
than before. **Blocking.** This is the finding the gate exists for, because it
is the one a reviewer cannot get from the text diff: the selector did not
change, the set of clusters it matches did.

`kind` is one of:

| `kind` | Meaning |
|---|---|
| `added` | An ApplicationSet that already existed is now generated for a cluster it was not. This is the leak. |
| `removed` | No longer generated. This is a silent uninstall; ArgoCD will prune it. |
| `moved` | Both, for the same ApplicationSet. Reported as one change, because reporting it as an unrelated add plus an unrelated remove buries the actual shape of what happened. |

**`introduced`**: a whole ApplicationSet that did not exist before, one entry
per Application it generates. **Not blocking.** Adding an addon is a deliberate
act by the author of the pull request; the dangerous case is an addon that
*already existed* quietly changing which clusters it reaches. Blocking on both
would make every new-addon pull request red for a reason nobody needs to
investigate, and a check people override by habit stops functioning as a check.

The entries carry what the diff does not. `cluster` says where the new
Application lands and `to` says what it renders from. The entry count is the
number of Applications the change creates, one per cluster the generator
matched. The report prints the bucket as a **New addons** table, and when
nothing else changed the verdict headline counts it: *No blocking findings —
1 new Application, first appearance*.

No rendered-resource changes are reported alongside them. Chart-diff pairs an
Application's base and head rows to render its chart at both versions, and a
first appearance has only a head row.

**`versions`**: same Application, same clusters, different `targetRevision`.
**Not blocking**, because this is the point of an automated bump pipeline.
Blocking here would park every automated merge forever.

**`other`**: same Application and clusters, but something structural moved,
such as the chart itself, the source type, the ArgoCD project or the
destination namespace. **Blocking.** A chart swapped underneath an unchanged
Application name is a different addon, and the version column will not say
so.

## `warnings`

Generators the gate could not expand: `git`, `matrix`, `list`. The
Applications they generate are **not** covered by any of the above.

This is deliberately loud. A gate that quietly skips what it cannot handle
reports "no targeting change" with exactly the same words whether it checked
everything or nothing, and the reader has no way to tell which. Anything
consuming the result should surface warnings rather than filtering them out.
