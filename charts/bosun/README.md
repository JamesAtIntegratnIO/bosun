# Bosun

> **Bosun** was called `delivery-agent` until 2026-08-23. The name changed;
> the job did not. The split a bosun draws — routine repairs on their own
> authority, serious damage reported to the captain — is the split this
> component draws between a mechanical fix and an escalation.


Runs [`bosun`](../..) in-cluster: Deployment,
Service, RBAC and NetworkPolicy.

## Install

Two Secrets and a values file. The chart creates neither Secret — how they get
there is yours to choose.

```yaml
image:
  repository: ghcr.io/you/bosun
  digest: sha256:...          # prefer a digest to a moving tag
git:
  owner: you
  repo: platform
  repoURL: https://github.com/you/platform.git
  existingSecret: bosun-git
llm:
  provider: openai            # no default; you must choose
  baseURL: http://model.internal:1234/v1
  model: your-model
gate:
  reportAuthor: ""            # empty = per-host default; see below
triage:
  allowPaths: [addons/**]     # empty means it can fix nothing
networkPolicy:
  kargoNamespace: kargo
  egress:
    ipBlocks:
      - {cidr: 10.1.2.3/32, port: 8000}   # your model endpoint
    allowPublicHTTPS: true                   # your git host
```

Then point the pipelines chart's triage hook at it:

```yaml
triage:
  enabled: true
  url: http://<release>-bosun.<namespace>.svc:8080/v1/promotion-opened
```

## Where the gate runs

`gate.mode: cluster` (the default) makes the agent the gate: it polls the
open pull requests, renders base and head against the live ArgoCD cluster
inventory, and posts the `gate.checkName` status and report comment itself.
Nothing to install in CI, no inventory snapshot to keep fresh — and two costs
stated plainly. The ServiceAccount gets **get/list on Secrets in the ArgoCD
namespace** (the inventory lives in the ArgoCD cluster Secrets, which also
hold cluster credentials; `rbac.create` scopes the grant to a namespaced
Role that exists only in this mode). And the render runs over pull-request
content in-cluster, so **fork pull requests are refused** with an `error`
status unless `gate.forkPRs` says otherwise.

### Where the inventory comes from

`gate.inventorySource: secrets` (the default) is the grant above: the
inventory *is* the ArgoCD cluster Secrets, read from the apiserver with the
pod's own ServiceAccount. One credential, and nothing in the path that can be
down by itself.

`gate.inventorySource: argocd` reads the same four fields — name, server,
labels, annotations — from `GET /api/v1/clusters` on the ArgoCD API, which
serves them with the credential block redacted. **The Role stops being
created.** The reason to want this is that the Secret grant cannot be made
smaller: the gate reads four fields, and RBAC has no predicate for "the labels
but not the data" — there are no deny rules, `resourceNames` does not apply to
`list`, and the label selector the gate sends is a filter the apiserver
applies *after* authorising, so a token holding that Role can drop it and read
`argocd-secret` and every repository credential beside it. ArgoCD's own API
draws the line RBAC cannot.

What it costs, as plainly as the grant it replaces:

```bash
argocd account generate-token --account bosun
```

```yaml
gate:
  inventorySource: argocd
  argocd:
    baseURL: https://argocd-server.argocd.svc
    existingSecret: bosun-argocd   # key `token`
    caSecret: bosun-argocd-ca      # or insecureSkipTLSVerify: true
```

and in `argocd-rbac-cm`, the smallest policy that answers the question:

```
p, bosun, clusters, get, *, allow
```

A second credential to mint, store and rotate, bearer-equivalent for whatever
its ArgoCD RBAC permits — give it that one line and nothing else, or it is a
bigger credential than the Secret read it replaced. A second component that
can be down: the Secrets are readable whenever the apiserver is; argocd-server
is not. And a second TLS story, because argocd-server serves its own
certificate rather than the one the kubelet mounts into every pod — hence
`caSecret`, or `insecureSkipTLSVerify` if nobody can produce that CA. The
chart adds the NetworkPolicy egress rule for the ArgoCD namespace itself,
because argocd-server is a ClusterIP and forgetting it hangs with zero bytes.

A trade, not a free win — which is why it is a value and not the default.

`gate.mode: ci` is the original shape — the gate runs in CI
([`ci/`](../../ci)), the agent waits on the check and reads the report from a
comment. The fallback for public repositories taking fork pull requests, and
for clusters that will not grant the Secret read. Everything below about
`gate.reportAuthor` applies to this mode; in cluster mode the verdict never
travels through a comment, so there is nothing to authenticate.

## Whose report the agent believes (ci mode)

The gate publishes its verdict as a pull-request comment carrying a marker, and
the agent reads that comment to decide what to do. A comment is a surface
anybody with write access can publish to, so the marker alone is not a
provenance — `gate.reportAuthor` is the account the report has to come from.

Left empty it defaults per host: `github-actions[bot]` on GitHub, because a gate
running in GitHub Actions comments through `github.token` and therefore as that
account; **unchecked on Gitea**, which has no equivalent fixed identity — set it
to whichever user minted your CI token. `"*"` reads the report whoever wrote it.

If your gate comments as something else — a bot user, a PAT's owner — the
symptom is the agent saying it ignored a report and naming the author it saw.
That message is the fix instruction.

## What upstream says

`triage.upstreamNotes` reads two things from the artifact's source project.

**Release notes** say what the maintainers meant to change. **Commits between
the two tags** say what they did — and they answer the question release notes
routinely do not. A chart drops its `ClusterRole` and ships a release note about
performance; the render proves the removal and cannot explain it, and the best
the agent can say is "no release note explains why". The commit that deleted the
template says exactly why.

Which commits is decided by code, from the kinds and resource names in the
gate's own findings — never by the model. They are read on the paths that
produce **prose**: the green-gate explanation, and an escalation. The
mechanical path — the one that writes files — never reads them, because an
edit's evidence is the gate report alone.

No new egress: it is `api.github.com`, the same host the gate's checks come
from. `maxCommits` caps how many reach a prompt or a comment.

## When swapping the version is not the whole job

`triage.structuralMigration` (on by default) is the second half of the
deterministic repair.

A chart that moves `spec.store` to `spec.secretStoreRef.name` between two API
versions leaves, after a plain apiVersion swap, a document that parses, applies,
and has that field **pruned by the apiserver on the way in**. The render is
fine. The gate goes green. The value is gone, and nothing in the repository can
see it.

Nobody can enumerate every upstream's structural changes in advance, so the
model is shown the old schema, the new schema and the document, and asked to
translate. What makes that safe is not the prompt — every proposal is checked
before anything is written:

| Check | Refuses |
|---|---|
| identity | a changed `apiVersion`, `kind`, `metadata.name` or `metadata.namespace` |
| schema validity | a proposal the target schema still does not accept |
| value provenance | any value not at that path in the original, not displaced by the schema change, and not dictated by the target schema |

A refusal refuses **everything** — not even the plain swaps are pushed. The swap
alone turns the gate green, because no manifest declares a dropped version any
more, while a document the schema rejects waits to be pruned.

**It needs `liveReads`** (the shape being *left* is only in the CRD installed
right now — after the merge it is gone) and egress to your chart registry (the
shape being arrived at comes from rendering the chart at the target version).
Without either it falls back to the plain swap and the comment says which check
it could not make.

Two costs worth knowing before you leave it on. A reshaped document is
re-serialised, so **comments inside that document do not survive**; the folded
diff in the comment shows exactly what changed. And nested manifests — one
inside an `extraObjects:` list or a block scalar — are **skipped and escalated**,
because replacing a document inside a values file means re-serialising a file
whose every remaining line would move.

See [`adr/0007-structure-from-the-schema-data-from-the-document.md`](../../adr/0007-structure-from-the-schema-data-from-the-document.md).

## What is actually running

`liveReads` lets a brief carry facts the gate structurally cannot have:

```
- externalsecrets.external-secrets.io on v1beta1 — 0 live object(s)
- Application external-secrets-host — Degraded / OutOfSync
```

The gate renders a repository and compares, so everything it knows is a
property of text. "Three manifests still declare a version this chart stops
serving" is a fact about the repository. Whether anything is *stored* on that
version usually decides whether a human needs waking, and CI cannot answer it.

**Off by default**, unlike everything else here — the rest of what the agent
reads is public or already in the pull request, and this reads your cluster.

**`scope` has two settings, because two are what RBAC can express.** "Everything
except the core group" is the intent most people have and it cannot be written
down: there are no deny rules, and `apiGroups: ["*"]` includes the core group,
which contains Secrets.

| `scope` | Grants | Secrets |
|---|---|---|
| `groups` (default) | `get`/`list` on the API groups you list | unreadable — the core group is never granted |
| `wide` | `get`/`list` on everything | **readable** |

With `groups`, an unlisted group shows up in the brief as *"not permitted to
check"* — honest, harmless, and a one-line values fix. A refusal is never
printed as a zero.

**It needs egress the chart cannot infer.** The apiserver is
`kubernetes.default.svc`, and a ClusterIP **cannot** be an `ipBlock` — it is
DNAT'd to a real endpoint before policy is evaluated, so a rule naming it
matches nothing and the connection hangs with zero bytes. Give the real
endpoints:

```bash
kubectl get endpoints kubernetes -n default
```

```yaml
networkPolicy:
  egress:
    apiServer:
      ipBlocks:
        - {cidr: 198.51.100.11/32, port: 6443}
```

With `flavor: cilium` you need none of that — the policy names the apiserver as
an entity, which survives a control-plane node being replaced.

The pod **refuses to start** if it cannot read the API, so a missing rule
produces a crash loop that names its cause rather than a permanent silent
hang.

See [`adr/0006-live-reads-are-scoped-by-group.md`](../../adr/0006-live-reads-are-scoped-by-group.md).

## The other half of the network path

This chart writes the policy governing what reaches the agent. It cannot write
the **Kargo controller's** egress policy, which is the half most often
missed.

A controller allowed `0.0.0.0/0` with RFC1918 excepted — a common shape, since
it usually only needs to reach registries — cannot reach a ClusterIP at all.
The symptom is a hang with zero bytes, not an error, so it reads as a slow
agent rather than a blocked one. Add an explicit rule for this service's
namespace and port.

## Shape

- **Read-only RBAC.** `get`/`list`/`watch` on Kargo CRDs, ArgoCD Applications
  and AnalysisRuns, pods and events. No `create`, `update`, `patch` or `delete`
  anywhere — the agent observes the cluster and writes to pull requests, never
  to the cluster.
- **Not exposed.** No Ingress or HTTPRoute. Only Kargo calls it, in-cluster.
  Publishing it would be gratuitous exposure of something that can spend money
  and write to your repository.
- **Two halves of the network path.** The agent's namespace must admit Kargo's
  controller, *and* the controller's own egress policy must permit the agent.
  Missing the second half presents as a hang with zero bytes, not an error.
- **Secrets by reference.** The chart takes the name of an existing Secret. How
  it gets there — ExternalSecret, Vault Agent, SOPS, `kubectl create` — belongs
  to whoever installs this.
- **No default model provider.** `llm.provider` must be set explicitly. See
  [`adr/0004-provider-interfaces.md`](../../adr/0004-provider-interfaces.md).
