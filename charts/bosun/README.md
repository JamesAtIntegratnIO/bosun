# Bosun

> **Bosun** was called `delivery-agent` until 2026-08-23. The name changed;
> the job did not. The split a bosun draws — routine repairs on their own
> authority, serious damage reported to the captain — is the split this
> component draws between a mechanical fix and an escalation.


Runs [`bosun`](../..) in-cluster: Deployment,
Service, RBAC and NetworkPolicy.

## Install

Three Secrets and a values file — git, the model endpoint, and the ArgoCD
account token the gate reads its inventory with. The chart creates none of them;
how they get there is yours to choose.

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
  argocd:
    baseURL: https://argocd-server.argocd.svc  # no default; see below
    existingSecret: bosun-argocd               # the account token
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

The agent is the gate. It polls the open pull requests, renders base and head
against the live cluster inventory, and posts the `gate.checkName` status and
report comment itself. Nothing to install in CI, no inventory snapshot to keep
fresh — and one cost stated plainly: the render runs over pull-request content
in-cluster, so **fork pull requests are refused** with an `error` status unless
`gate.forkPRs` says otherwise.

### Where the inventory comes from

`gate.argocd` is required. The gate reads four fields — name, server, labels,
annotations — from `GET /api/v1/clusters` on the ArgoCD API, which serves them
with the credential block redacted.

It is the API and not the cluster Secrets those fields live in because that
Secret read cannot be made small enough. RBAC has no predicate for "the labels
but not the data" — there are no deny rules, `resourceNames` does not apply to
`list`, and the label selector the gate would send is a filter the apiserver
applies *after* authorising, so a token holding such a Role could drop it and
read `argocd-secret` and every repository credential beside it. ArgoCD's own API
draws the line RBAC cannot, and **this chart creates no Role over Secrets at
all.**

```bash
argocd account generate-token --account bosun
```

```yaml
gate:
  argocd:
    baseURL: https://argocd-server.argocd.svc   # REQUIRED
    podPort: 8080                  # the POD's port, not the URL's — see below
    existingSecret: bosun-argocd   # REQUIRED, key `token`
    caSecret: bosun-argocd-ca      # or insecureSkipTLSVerify: true
```

and in `argocd-rbac-cm`, the smallest policy that answers the question:

```
p, bosun, clusters, get, *, allow
```

What it costs, as plainly as the grant it replaces. A credential to mint, store
and rotate, bearer-equivalent for whatever its ArgoCD RBAC permits — give it
that one line and nothing else. A component that can be down on its own: the
apiserver is up whenever the cluster is; argocd-server is not. Its own TLS
story, because argocd-server serves its own certificate rather than the one the
kubelet mounts into every pod — hence `caSecret`, or `insecureSkipTLSVerify` if
nobody can produce that CA. And a network path with two ends and a port that
catches people — the next section, and the last one on this page.

### `gate.argocd.podPort` is the pod's port, not the URL's

The chart writes the NetworkPolicy egress rule to the ArgoCD namespace itself,
because argocd-server is a ClusterIP and forgetting it hangs with zero bytes.
The port that rule opens is `gate.argocd.podPort`, and **it is normally not the
port in `baseURL`.**

A NetworkPolicy matches the destination port of the packet, and a ClusterIP is
DNAT'd to the backend pod's port *before* policy is evaluated. Whatever port
the Service published — 80, 443 — the packet reaching the rule is addressed to
the pod, on `8080`.

So a values file setting `podPort` to the port in `baseURL` renders clean,
passes `helm lint`, passes the chart's schema, and then drops every packet.
There is no error at either end: the connection hangs for the full HTTP
timeout, and the pod dies at start-up saying argocd-server is unreachable —
true, and pointing nowhere near the values file.

`8080` is argocd-server's container port in the upstream argo-cd chart and it
does not move with `server.insecure`: the Service publishes both 80 and 443
against the same container port, and argocd-server decides per connection
whether to speak TLS on it. Confirm yours:

```bash
kubectl -n argocd get svc argocd-server -o jsonpath='{.spec.ports[*].targetPort}{"\n"}'
```

Which is why the two common installs differ only in the URL:

| | `baseURL` | `podPort` | TLS |
|---|---|---|---|
| `server.insecure: true`, behind a gateway | `http://argocd-server.argocd.svc` | `8080` | none to verify — leave `caSecret` empty |
| argocd-server terminating its own TLS | `https://argocd-server.argocd.svc` | `8080` | `caSecret`, or `insecureSkipTLSVerify: true` |

The insecure row is the one worth reading twice: nothing in `baseURL` mentions
a port, the Service answers on 80, and the policy still has to say 8080.

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

## The halves of the network path this chart cannot write

This chart writes the policy governing what reaches the agent and what the
agent may reach. Every path it takes part in has a far end whose policy lives
in somebody else's release, and a missing rule at that end presents the same
way each time: a hang with zero bytes, not an error, so it reads as a slow
agent rather than a blocked one.

**The Kargo controller's egress**, which is the half most often missed. A
controller allowed `0.0.0.0/0` with RFC1918 excepted — a common shape, since it
usually only needs to reach registries — cannot reach a ClusterIP at all. Add
an explicit rule for this service's namespace and port.

**argocd-server's ingress**, which the gate's inventory read needs. The chart
emits the agent's *egress* to the ArgoCD namespace; argocd-server's own
ingress policy belongs to your ArgoCD release, and until both exist the
connection is dropped with nothing logged at either end. If your ArgoCD
namespace has no ingress policy at all there is nothing to do here; if it has
one, it needs this:

```yaml
# In your ArgoCD release, not this chart.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: bosun-to-argocd-server
  namespace: argocd
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: argocd-server
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: <the namespace bosun runs in>
      ports:
        - protocol: TCP
          # The pod port, for the reason given under `gate.argocd.podPort`:
          # this rule is matched after the ClusterIP has been DNAT'd away.
          port: 8080
```

## Shape

- **Read-only RBAC.** `get`/`list`/`watch` on Kargo CRDs, ArgoCD Applications
  and AnalysisRuns, pods and events. No `create`, `update`, `patch` or `delete`
  anywhere — the agent observes the cluster and writes to pull requests, never
  to the cluster.
- **Not exposed.** No Ingress or HTTPRoute. Only Kargo calls it, in-cluster.
  Publishing it would be gratuitous exposure of something that can spend money
  and write to your repository.
- **Every network path has a far end this chart cannot write.** The agent's
  namespace must admit Kargo's controller *and* the controller's own egress
  policy must permit the agent; the same is true of argocd-server's ingress,
  which the gate's inventory read needs. A missing far end presents as a hang
  with zero bytes, not an error.
- **Secrets by reference.** The chart takes the name of an existing Secret. How
  it gets there — ExternalSecret, Vault Agent, SOPS, `kubectl create` — belongs
  to whoever installs this.
- **No default model provider.** `llm.provider` must be set explicitly. See
  [`adr/0004-provider-interfaces.md`](../../adr/0004-provider-interfaces.md).
