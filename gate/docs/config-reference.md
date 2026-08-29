# `.bosun.yaml`

**You probably need no file.** The gate asks ArgoCD which Applications and
ApplicationSets exist, keeps the ones pointing at the repository being gated,
and renders their paths from the pull request's own checkout
([ADR 0012](../../adr/0012-the-repo-stops-repeating-the-ship.md)). Live says
what to render; the pull request says what it says. A repository whose
Applications ArgoCD already serves is gated with nothing committed at all.

This page is the rest: the cases derivation cannot reach, and the schema for
when you want to be explicit.

## Do you need one?

| You have | You need |
|---|---|
| Applications and ApplicationSets that ArgoCD serves, pointing at this repository | nothing |
| A root ApplicationSet applied by Terraform, whose manifest is in this repository | `roots:`, one line per root |
| A root this pull request *introduces* | the same, and it is the only way it is rendered before the first apply |
| Something derivation gets wrong | `sources:`, which take precedence over derived ones |
| An existing `.gitops-gate.yaml` | nothing. It keeps working unchanged |

Both filenames are read, `.bosun.yaml` first. **Both present is an error**,
not a precedence rule: a silent precedence is how a repository ends up
maintaining the file the gate is not reading.

## `roots`

```yaml
roots:
  - bootstrap/addons.yaml
  - bootstrap/apps.yaml
```

The one fact ArgoCD cannot supply, and the reason this file still exists.

A root is an ApplicationSet nothing in ArgoCD created, usually applied by
Terraform or by hand. It carries no tracking annotation, so there is nothing to
follow to it, and the gate finds it only by scanning the checkout for a
manifest that declares it. Naming its file here does two things: it makes a
root this pull request *introduces* render at all, which no scan can do because
there is nothing live to be found by; and it removes the scan's chance of
missing one.

A root the gate cannot find in this repository is rendered from the spec ArgoCD
has applied, and the report says which. That is the previous answer to the
question being asked, so an edit to that root is invisible until it applies.
Every report names them; a name in that list is an invitation to add a line
here.

An entry naming a file that does not exist at the head revision is an error.
It is the one thing this key is for, and a typo that quietly fell back to the
applied spec would produce a green gate on exactly the change it was added to
see.

## `sources`

Only needed where derivation gets something wrong. A source written here takes
precedence over a derived one rendering the same path, and derivation still
adds anything the file does not mention.

```yaml
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

  # ArgoCD's own directory semantics, for a path it walks itself.
  - name: tenants
    type: directory
    path: tenants/a
    recurse: true
    exclude: "exclude/*"

clustersExport:
  knownAbsentLabels: [aws_cluster_name]
```

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
| `directory` | `path`, optional `recurse`, `include`, `exclude` | one path, walked the way ArgoCD walks it. This is what most derived Applications become |

`chart` and `valueFiles` may contain `{{metadata.labels.x}}` and
`{{metadata.annotations.y}}`, resolved per cluster, which is how a
per-environment values layout is expressed without enumerating every
combination. A value file whose placeholders do not resolve for a given cluster
is not that cluster's file, matching ArgoCD's `ignoreMissingValueFiles`.

`selector.matchLabels` limits which clusters a source renders for.

### `directory` semantics

`recurse: false` (the default) reads the path's own files and descends into
nothing. `include` and `exclude` are globs over each file's path relative to
`path`, with `exclude` winning, and `{a,b/*}` brace groups are expanded.

`*` and `?` stop at a path separator; `**` crosses them. Where that differs
from ArgoCD, it differs in the direction that shows: the gate renders a file
ArgoCD skips, and it appears in the report as an object nobody deployed. The
opposite error removes objects from both sides of the diff, which then finds no
difference and says so. Write `**` where "and everything below" is meant.

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

## `concurrency` and `validate` belong to the operator now

Both are chart values, `gate.concurrency` and `gate.validate.*`, on the same
reasoning that keeps the egress deny-list out of this file's reach: the renders
happen in the operator's pod, against that pod's limits, beside every other open
pull request's. How hard to work and what to check are decisions about that
cluster rather than about the repository under review.

The keys are still read here, and are still honoured **when the chart value
leaves them unset**, so an install that configured either in its own file keeps
exactly what it had. Set the value and the value wins.

`concurrency` is parallel renders, default 8, capped at 32 whatever either side
asks for: every worker is a helm subprocess with a chart download and a
temporary directory behind it. A larger number parses and is clamped rather
than refused, because failing a pull request over a field with nothing to do
with its diff is the wrong trade.

## `valuesRef`

The `ref:` name your bootstrap ApplicationSet gives its values source.
Multi-source Applications refer to it as `$values/…` in `valueFiles`, and the
gate has to strip that prefix to find the file on disk. Defaults to `values`.

**Only consulted for sources written in this file.** A derived source resolves
`$ref/…` through the ref the Application itself declares, which is exact where
this is one guess applied to every Application at once, and wrong the moment
two of them chose different names.

## `clustersExport.knownAbsentLabels`

Label keys a selector matches on that no cluster is expected to carry. Without
this the gate refuses to render: a selector matching on a label the inventory
has never seen renders a fraction of the real Applications and then reports
"no targeting change" with total confidence, so the mismatch is an error rather
than a wrong answer. List a label here only when the absence is deliberate; an
addon inherited from upstream whose selector is never satisfied on this fleet.

Only the operators that need a label **present** are checked: `matchLabels`,
`In` and `Exists`. `NotIn` and `DoesNotExist` select on absence, so on a fleet
where the key is simply unused every cluster matches and the render is right;
those keys are not demanded and do not need listing here. If you added one for
that reason, the line is now unnecessary, and removing it restores the check
for every other selector that matches on the same key.

The key's name has outlived the export subcommand it was written for. It stays
because renaming it would break every config that sets it, for tidiness.

## `validate`

Set it in the chart (`gate.validate.*`); the keys below are read here only
while the chart values leave them unset.

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

## What the gate reads from ArgoCD

Three lines in `argocd-rbac-cm`, all reads, on one account token:

```
p, bosun, clusters, get, *, allow
p, bosun, applications, get, */*, allow
p, bosun, applicationsets, get, */*, allow
```

Without the last two the gate refuses to run rather than rendering a scope it
could not see. The refusal names the line to add.

Scope depending on cluster state has a cost, and it is stated here rather than
only in the ADR. An ArgoCD that is down or refuses fails loud. An ArgoCD that
is up and serving a *smaller* fleet than yesterday fails quiet: the gate
renders the smaller scope, correctly reports no change within it, and says
nothing about what left. Every report carries a **What was rendered** line so
that a reader can see the size of the world the verdict was reached in.
