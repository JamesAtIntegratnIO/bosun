# `.gitops-gate.yaml`

The gate knows nothing about any particular repository. This file is the
whole of that knowledge, and it lives at the root of the repository being gated.

```yaml
valuesRef: values
concurrency: 8

sources:
  # Committed YAML: Applications and ApplicationSets alike.
  - name: appsets
    type: manifests
    paths: ["clusters/*/appsets/*.yaml", "apps/**/*.yaml"]

  # An app-of-apps ApplicationSet: the gitops-bridge shape.
  - name: addons
    type: argocd-bootstrap
    path: bootstrap/addons.yaml

  # A chart, rendered once per cluster when its values depend on cluster
  # metadata.
  - name: platform
    type: helm
    chart: charts/platform
    valueFiles:
      - "envs/{{metadata.labels.environment}}/values.yaml"
    selector:
      matchLabels: {cluster_role: control-plane}
    scope: cluster        # or fleet (default)

  - name: overlays
    type: kustomize
    path: overlays/production

clustersExport:
  knownAbsentLabels: [aws_cluster_name]

validate:
  enabled: true
  ignoreMissingSchemas: true
```

## `sources`

A repository is rarely one shape. After a few years it is ApplicationSets
committed as YAML, *plus* a chart that renders more of them, *plus* an overlay
somebody added. So this is a list of strategies, not a mode.

| `type` | Needs | Reads |
|---|---|---|
| `manifests` | `paths` (globs) | committed `Application` and `ApplicationSet` YAML |
| `argocd-bootstrap` | `path` | an app-of-apps ApplicationSet, following it to whatever it points at |
| `helm` | `chart`, optional `valueFiles` | a rendered chart |
| `kustomize` | `path` | `kustomize build`, falling back to `kubectl kustomize` |
| `rendered` | `paths` (globs) | manifests already rendered into git, diffed at resource level. See [rendered-manifests.md](rendered-manifests.md) |

`chart` and `valueFiles` may contain `{{metadata.labels.x}}` and
`{{metadata.annotations.y}}`, resolved per cluster, which is how a
per-environment values layout is expressed without enumerating every
combination. A value file whose placeholders do not resolve for a given cluster
is not that cluster's file, matching ArgoCD's `ignoreMissingValueFiles`.

`selector.matchLabels` limits which clusters a source renders for. `argocd`
scopes a source to one ArgoCD instance in a fleet that runs several.

### `scope`

Only meaningful for a source rendered per cluster.

- **`fleet`** (default): ApplicationSets expand against the whole inventory.
  Correct for hub-and-spoke, where one ArgoCD holds every cluster and an
  ApplicationSet rendered under one cluster's values can still generate
  Applications for others.
- **`cluster`**: they expand only against the cluster they were rendered for.
  Correct where each cluster runs its own ArgoCD and only ever sees itself.

Getting this wrong is quiet rather than loud, which is why it is explicit:
under `fleet`, a chart rendered per cluster yields the same ApplicationSet name
several times with different contents, and whichever arrives first wins.

### `argocd-bootstrap` follows what it finds

The bootstrap's source path is resolved the way ArgoCD resolves it: a directory
containing `Chart.yaml` is rendered as a chart, anything else is read as a
directory of manifests. The canonical gitops-bridge bootstrap is the second
kind: it points at a directory and applies every ApplicationSet YAML in it with
`directory.recurse: true`.

Both the singular `source:` and the multi-source `sources:` template forms are
read. gitops-bridge uses the singular.

## `bootstraps` (older form)

```yaml
bootstraps:
  - {name: control-plane, path: bootstrap/addons.yaml}
```

Exactly equivalent to one `type: argocd-bootstrap` source each, and still
supported. Each entry names an ApplicationSet that generates the Applications
that render the ApplicationSets that generate everything else. Two levels is
the usual "app of apps of addons" shape, and the gate walks both.

`name` is cosmetic, used in output. It defaults to the file's base name.

## `concurrency`

Parallel renders, default 8. Fleets are the reason: fifty clusters is fifty
chart renders per revision, and serial execution turns a ninety-second gate
into something people route around.

## `valuesRef`

The `ref:` name your bootstrap ApplicationSet gives its values source. Multi-source
Applications refer to it as `$values/…` in `valueFiles`, and the gate has to strip
that prefix to find the file on disk. Defaults to `values`.

## `clustersExport.knownAbsentLabels`

Label keys a selector matches on that no cluster is expected to carry. Without
this the gate refuses to render: a selector matching on a label the inventory
has never seen renders a fraction of the real Applications and then reports
"no targeting change" with total confidence, so the mismatch is an error rather
than a wrong answer. List a label here only when the absence is deliberate; an
addon inherited from upstream whose selector is never satisfied on this fleet.

The key's name has outlived the export subcommand it was written for. It stays
because renaming it would break every config that sets it, for tidiness.

## `validate`

`ignoreMissingSchemas` is mandatory in practice rather than a convenience.
CRDs outside the large projects appear in no published schema catalogue, and
without this one unknown kind fails a run that had nothing wrong with it.

The cost is that those kinds are then **not validated at all**. The gate
reports how many kinds it skipped, so the gap is visible rather than assumed
away.

## The cluster inventory

There is nothing to configure. The agent reads the inventory live from
ArgoCD's API on every run ([ADR
0008](../../adr/0008-the-gate-moves-in-cluster.md)), so generators resolve
selectors against the cluster labels ArgoCD reports at that moment, and there
is no snapshot to keep current. The checked-in snapshot and the `clusters:`
key that named it went with the CLI
([ADR 0010](../../adr/0010-the-cli-goes-too.md)).
