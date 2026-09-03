# Bosun

> **Bosun** was called `delivery-agent` until 2026-08-23. The name changed;
> the job did not. A bosun makes routine repairs on their own authority and
> reports serious damage to the captain, which is the split this component
> draws between a mechanical fix and an escalation.

Runs [`bosun`](../..) in-cluster: Deployment, Service, RBAC and
NetworkPolicy.

## Install

Three Secrets and a values file: git, the model endpoint, and the ArgoCD
account token the gate reads its inventory with. The chart creates none of
them; how they get there is yours to choose.

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
  # Only if a chart repository or registry it reads sits on a private
  # address; those go through a dialler that refuses RFC1918, link-local and
  # CGNAT whatever the NetworkPolicy permits. e.g. [10.42.0.0/16]
  egressAllowPrivate: []
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
fresh, and since
[ADR 0012](https://bosun.integratn.io/decisions/0012-the-repo-stops-repeating-the-ship/)
no config file in the gated repository either: what to render is derived from
the Applications and ApplicationSets ArgoCD serves, and every byte that renders
comes from the pull request's own checkout.

Two costs come with that. The render runs over pull-request content
in-cluster, so **fork pull requests are refused** with an `error` status unless
`gate.forkPRs` says otherwise. And the scope now depends on cluster state: an
ArgoCD that is down fails loud, while one serving a smaller fleet than
yesterday produces a smaller scope and a confident "no change" within it, which
is why every report carries a **What was rendered** line.

### How hard it works, and what it checks

`gate.concurrency` and `gate.validate.*` are here rather than in the gated
repository's own file, on the same line the egress deny-list is on: the renders
happen in this pod, against this pod's limits, beside every other open pull
request's. Every key is unset by default, and unset leaves whatever that
repository's file says; set one and it wins. `concurrency` is capped at 32
whatever either side asks for, because each worker is a helm subprocess with a
chart download behind it.

### Where the inventory comes from

`gate.argocd` is required. The gate reads four fields, name, server, labels
and annotations, from `GET /api/v1/clusters` on the ArgoCD API, which serves
them with the credential block redacted.

It is the API and not the cluster Secrets those fields live in because that
Secret read cannot be made small enough. RBAC has no predicate for "the labels
but not the data": there are no deny rules, `resourceNames` does not apply to
`list`, and the label selector the gate would send is a filter the apiserver
applies *after* authorising, so a token holding such a Role could drop it and
read `argocd-secret` and every repository credential beside it. ArgoCD's own
API draws the line RBAC cannot, and **this chart creates no Role over Secrets
at all.**

```bash
argocd account generate-token --account bosun
```

```yaml
gate:
  argocd:
    baseURL: https://argocd-server.argocd.svc   # REQUIRED
    podPort: 8080                  # the pod's port, not the URL's; see below
    existingSecret: bosun-argocd   # REQUIRED, key `token`
    caSecret: bosun-argocd-ca      # or insecureSkipTLSVerify: true
```

and in `argocd-rbac-cm`, the smallest policy that answers the questions:

```
p, bosun, clusters, get, *, allow
p, bosun, applications, get, */*, allow
p, bosun, applicationsets, get, */*, allow
```

Three reads. The first is the cluster inventory. The other two are what the
gated repository deploys, which the gate derives from ArgoCD rather than from a
file the repository keeps in step by hand
([ADR 0012](https://bosun.integratn.io/decisions/0012-the-repo-stops-repeating-the-ship/));
without them it refuses to run rather than rendering a scope it could not see,
and the refusal names the line to add.

What it costs, as plainly as the grant it replaces:

- A credential to mint, store and rotate, bearer-equivalent for whatever its
  ArgoCD RBAC permits. Give it those three lines and nothing else.
- A component that can be down on its own. The apiserver is up whenever the
  cluster is; argocd-server is not.
- Its own TLS story, because argocd-server serves its own certificate rather
  than the one the kubelet mounts into every pod. Hence `caSecret`, or
  `insecureSkipTLSVerify` if nobody can produce that CA.
- A network path with two ends and a port that catches people: the next
  section, and the last one on this page.

### `gate.argocd.podPort` is the pod's port, not the URL's

The chart writes the NetworkPolicy egress rule to the ArgoCD namespace itself,
because argocd-server is a ClusterIP and forgetting it hangs with zero bytes.
The port that rule opens is `gate.argocd.podPort`, and **it is normally not the
port in `baseURL`.**

A NetworkPolicy matches the destination port of the packet, and a ClusterIP is
DNAT'd to the backend pod's port *before* policy is evaluated. Whatever port
the Service published, 80 or 443, the packet reaching the rule is addressed to
the pod, on `8080`.

So a values file setting `podPort` to the port in `baseURL` renders clean,
passes `helm lint`, passes the chart's schema, and then drops every packet.
There is no error at either end: the connection hangs for the full HTTP
timeout, and the pod dies at start-up saying argocd-server is unreachable,
which is true and points nowhere near the values file.

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
| `server.insecure: true`, behind a gateway | `http://argocd-server.argocd.svc` | `8080` | none to verify; leave `caSecret` empty |
| argocd-server terminating its own TLS | `https://argocd-server.argocd.svc` | `8080` | `caSecret`, or `insecureSkipTLSVerify: true` |

The insecure row is the one worth reading twice: nothing in `baseURL` mentions
a port, the Service answers on 80, and the policy still has to say 8080.

## What upstream says

`triage.upstreamNotes` reads two things from the artifact's source project.

**Release notes** say what the maintainers meant to change. **Commits between
the two tags** say what they did, which is the question release notes routinely
leave open. A chart drops its `ClusterRole` and ships a release note about
performance; the render proves the removal and cannot explain it, and the best
the agent can say is "no release note explains why". The commit that deleted
the template says exactly why.

Which commits is decided by code, from the kinds and resource names in the
gate's own findings, never by the model. They are read on the paths that
produce **prose**: the green-gate explanation, and an escalation. The
mechanical path, the one that writes files, never reads them, because an edit's
evidence is the gate report alone.

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
translate. The prompt is not what makes that safe. Every proposal is checked
before anything is written:

| Check | Refuses |
|---|---|
| identity | a changed `apiVersion`, `kind`, `metadata.name` or `metadata.namespace` |
| schema validity | a proposal the target schema still does not accept |
| value provenance | any value not at that path in the original, not displaced by the schema change, and not dictated by the target schema |

A refusal refuses **everything**, and not even the plain swaps are pushed. The
swap alone turns the gate green, because no manifest declares a dropped version
any more, while a document the schema rejects waits to be pruned.

**It needs `liveReads`** (the shape being *left* is only in the CRD installed
right now, and after the merge it is gone) and egress to your chart registry
(the shape being arrived at comes from rendering the chart at the target
version). Without either it falls back to the plain swap and the comment says
which check it could not make.

Two costs worth knowing before you leave it on. A reshaped document is
re-serialised, so **comments inside that document do not survive**; the folded
diff in the comment shows exactly what changed. And a nested manifest, one
inside an `extraObjects:` list or a block scalar, is **skipped and escalated**,
because replacing a document inside a values file means re-serialising a file
whose every remaining line would move.

See [`adr/0007-structure-from-the-schema-data-from-the-document.md`](../../adr/0007-structure-from-the-schema-data-from-the-document.md).

### The other half: a chart your values have outgrown

The same flag covers the mirror case, and it is the more common one. A chart
version that adds `values.schema.json`, or tightens the one it had, refuses
settings your repository has been making for years — and helm checks that
schema before it templates anything, so the Application does not render at all.
The gate blocks on it; this is what repairs it.

The model is shown your values and the chart's new schema and returns the
migrated values. Three checks again, one of them different:

| Check | Refuses |
|---|---|
| survival | a setting the new chart version still declares that came back changed, moved, or missing |
| schema validity | values the new schema still rejects |
| value provenance | any value not at that path in your values, not displaced by the schema change, and not dictated by the chart's schema |

Then **the chart is rendered with the answer**, and a proposal helm will not
template is refused. That is a guarantee the document path cannot have, and it
is what catches a key the chart *renamed* being dropped as though it had been
removed.

Where it stops: a key the new schema **requires** and says nothing about the
value of — no default, no `const`, no single legal value — is escalated with
the key named, before the model is asked anything. There is nothing to derive
an answer from, and a plausible one renders perfectly.

What lands is not the document. The harness turns the difference into a plan —
remove a key, rename a key, set a key — and applies each one on that key's own
lines, so **your comments, indentation and quoting survive** and the diff is
the keys that changed. Three shapes are refused rather than improvised: a key
inside a flow mapping (`{a: 1, b: 2}`), a value that is not a scalar, and a
section that does not exist yet. A values file whose keys this cannot uniquely
find among the files the change may write to is refused too, because a wrong
guess edits a different addon.

See [`adr/0013-a-values-migration-is-a-plan-not-a-document.md`](../../adr/0013-a-values-migration-is-a-plan-not-a-document.md).

## What is running

`liveReads` lets a brief carry facts the gate structurally cannot have:

```
- externalsecrets.external-secrets.io on v1beta1 — 0 live object(s)
- Application external-secrets-host — Degraded / OutOfSync
```

The gate renders a repository and compares, so everything it knows is a
property of text. "Three manifests still declare a version this chart stops
serving" is a fact about the repository. Whether anything is *stored* on that
version usually decides whether a human needs waking, and CI cannot answer it.

**Off by default**, unlike everything else here. The rest of what the agent
reads is public or already in the pull request, and this reads your cluster.

**`scope` has two settings, because two are what RBAC can express.** "Everything
except the core group" is the intent most people have and it cannot be written
down: there are no deny rules, and `apiGroups: ["*"]` includes the core group,
which contains Secrets.

| `scope` | Grants | Secrets |
|---|---|---|
| `groups` (default) | `get`/`list` on the API groups you list | unreadable; the core group is granted nothing beyond the pods and events this feature brings |
| `wide` | `get`/`list` on everything | **readable** |

With `groups`, an unlisted group shows up in the brief as *"not permitted to
check"*: honest, harmless, and a one-line values fix. A refusal is never
printed as a zero.

**It needs egress the chart cannot infer.** The apiserver is
`kubernetes.default.svc`, and a ClusterIP **cannot** be an `ipBlock`. It is
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

With `flavor: cilium` you need none of that: the policy names the apiserver as
an entity, which survives a control-plane node being replaced.

The pod **refuses to start** if it cannot read the API, so a missing rule
produces a crash loop that names its cause rather than a permanent silent
hang.

See [`adr/0006-live-reads-are-scoped-by-group.md`](../../adr/0006-live-reads-are-scoped-by-group.md).

## The status page

One read-only HTML page: what this agent is watching, which pull requests have
a verdict and which is being rendered right now, what the pipeline sweep found,
and the exact command that ends each finding.

The report has been served on `/pipeline` as markdown since the supervisor was
written, and markdown in a browser tab is source code. The people who most need
it, whoever is wondering why an addon has not updated in three days, are exactly
the people who will not port-forward and pipe `curl` through a renderer. So the
same report now renders itself.

**It is on its own port, and that is the entire reason it can be published.**
`service.port` also answers `POST /v1/promotion-opened`, the endpoint that names
the pull request the agent edits and the files it reads into a published prompt.
A NetworkPolicy and a gateway both draw their lines at the port, so "expose the
read-only page" stays a smaller decision than "expose the endpoint that spends
money and writes to your repository" only because the two never share a
listener.

```yaml
web:
  enabled: true             # the default
  httpRoute:
    enabled: true
    parentRefs: [{name: external, namespace: gateway-system, sectionName: https}]
    hostnames: [bosun.example.com]
  allowFrom:
    - namespace: gateway-system     # who may reach the page's port
      podSelector: {app: envoy}
```

**Gateway API, not `ingress-nginx`.** An `ingress` block is here for clusters
without Gateway API, and it is the second choice: Ingress is feature-frozen, so
everything past a host and a path is a controller-specific annotation and the
manifest stops describing what it does. `ingress-nginx` in particular is in
maintenance mode. An HTTPRoute names the Gateway it attaches to, and the policy
that gateway carries, TLS, authentication, rate limits, belongs to whoever runs
it.

**Neither route adds authentication, and the page has none of its own.** It
reveals operational state: the repository's name, the titles of open pull
requests, your Stage and Warehouse names, the namespaces the sweep examined,
and the findings with their remedies. No credential, no prompt, no rendered
diff. Treat it as read access to your pipeline's status, and put your gateway's
authentication in front of it if that is more than you want published.

`web.theme` picks which of the two treatments it renders in: `auto` (the
default) follows the reader's own system preference, `dark` and `light` stamp
`data-theme` on the document and beat that preference in both directions. Both
are the palette from [the project's site](https://bosun.integratn.io), and a
value that is not one of the three is refused by the schema at render time.

It is an operator's setting because the page cannot offer the reader one. A
toggle needs somewhere to remember the answer and this page has nowhere: it
carries no script, which is what lets you put a strict content policy in front
of it, and it refreshes itself every minute, so the CSS-only toggle that works
without script would be wiped on every refresh. Set it when the reader's
preference is the wrong input -- a wall-mounted dashboard, or a screenshot that
has to look the same for everyone.

Five values are refused rather than rendered, each because the failure it
produces points nowhere near its cause:

| Refused | Because |
|---|---|
| `httpRoute.enabled` with no `parentRefs` | a route with no parent attaches to no Gateway, renders clean, and serves nothing |
| `ingress.enabled` with no `className` | an unclassed Ingress is claimed by whichever controller claims unclassed Ingresses |
| `ingress.enabled` with no `hosts` | an Ingress with no rules is accepted and routes nothing; the page answers the default backend instead of 404ing anywhere you would look |
| a route published, `networkPolicy.enabled`, `allowFrom` empty | nothing may reach the page's port; the symptom is a timeout that blames the gateway |
| a route with `web.enabled: false` | there is nothing listening behind it |

The cross-namespace `parentRefs` above still needs the Gateway's own listener
to admit routes from this namespace (`allowedRoutes.namespaces`), which is the
same shape as every other far end in the section below: this chart cannot write
it, and without it the route is simply not accepted.

Without a route, the page is still there on a port-forward, which is how to
look at it before deciding whether to publish it at all:

```bash
kubectl -n bosun port-forward deploy/bosun 8081 &
open http://localhost:8081
```

## The MCP surface

The same facts the page renders, served to the readers who are not people.

An on-call engineer's coding agent asks `pipeline_report` what has stopped
promoting and gets each finding as typed values: the kind to branch on, the
severity, the subject, the evidence with its numbers, how long it has held,
and where one exists the paste-ready command that recovers it, worst first.
Alongside them the sweep gives its own accounting of what it examined, so a
report with no findings can prove it looked instead of returning nothing.

A platform engineer whose pull request is blocked asks `gate_verdict` why, and
gets the blocker breakdown as counts per kind, every finding behind those
counts, and the dropped API versions as fields: which definition, which
versions it stopped serving, which one survives, and the kind of manifest that
has to move. Each finding says whether an edit in the repository could clear
it, so an agent stops hunting for one that does not exist. The gate also lists
what it could not render, because a clean verdict over a partial render is a
narrower claim than a clean verdict over a whole one. Bosun stamps the answer
with the head commit it judged, so you can tell a stale answer from a current
one.

An engineer asks `gate_status` for the queue and gets every open pull request
the last sweep saw, each with the state standing against its head commit,
whether it blocks, and the blocker breakdown as counts. **A sweep that could
not list pull requests says so in a field of its own.** The other symptom of a
gate that cannot reach your git host is a queue that reads "nothing open"
forever, so only a sweep that listed publishes an empty queue, and a queue held
over from an earlier sweep carries the error that says it is stale.

An engineer asks `triage_status` what the agent is doing about one pull request
and gets the phase, the automatic fix attempts spent against the cap, and the
labels standing on it: still working, or finished and not trying again. A pull
request the agent is not working gets that answer instead of an error. The
phase is current; the labels and the attempt count are as old as the sweep the
answer names, because a tool call reaches no git host.

An engineer whose merge would not go green asks `verdict_history` what the gate
said on the pull request's earlier head commits and gets each one as data: the
commit, whether that verdict blocked, and the gate's own headline for it,
newest first. It tells a push that fixed something from a gate that changed its
mind, and it is the one answer here read off your git host rather than computed
in the pod. A gate with no database keeps its memory as HTML comments inside
its own comment on the pull request, so the result names that comment as its
source and publishes the cap on how many verdicts it remembers.

A platform engineer's agent asks `inventory` what this fleet runs and gets
every Application the last live reading of ArgoCD served, with the cluster each
one lands on. It answers what otherwise needs a cluster credential of its own,
from a reading this pod already makes on every gate run and used to throw away.
**Names and clusters only**: no manifest, no values file and no rendered object
crosses that boundary, by any argument, and the result type has nowhere to put
one. **Its age is not the sweep's**, and that is the sentence to read before
trusting a row. The gate makes the reading when it renders a pull request, so
an install with none open makes none and the fleet stays as old as the last
one. Every row carries when it was observed, so you can read the staleness
instead of assuming it, and no tool call may go and refresh it.

**A pull request with no verdict standing gets that answer**, and never a
passing one. There are four ways to have no verdict: a render in flight, a
verdict already standing on the git host that this process did not re-run, a
gate that could not run at all, a sweep that could not list pull requests. Each
gets its own state and its own sentence. The findings field is *absent* in all
four, and *empty* only when the gate looked and found nothing.

It computes nothing. Every answer comes from the snapshot the last sweep left
in memory, so a request reaches no git host, no cluster and no model, and a
chatty client cannot spend your rate limit. Nothing it serves can change
anything. There is no mutating tool and there is not going to be one: the
ClusterRole this chart writes has no write verb anywhere, and a feature that
seems to need one is a signal to reconsider the feature.

```yaml
mcp:
  enabled: true
  existingSecret: bosun-mcp   # required; this chart never creates a Secret
  tokenKey: token
  allowFrom:
    - namespace: gateway-system
      podSelector: {app: envoy}
service:
  mcpPort: 8082               # the default
```

```bash
kubectl -n bosun create secret generic bosun-mcp   --from-literal=token="$(head -c 32 /dev/urandom | base64)"
```

**Off by default, and that is not a preference the way `web.enabled` is.**
Upgrading an install must not open a new programmatic API on it, so `helm
upgrade` from a release before this one changes nothing: no port on the
Service, no peer in the NetworkPolicy, no variable in the Deployment.

**Without a token the listener does not start.** The chart refuses to render,
and the binary refuses to start the listener on top of that, which covers a
Deployment somebody edits by hand. `promotionAuth` works differently on
purpose: its caller is Kargo inside the cluster, and its unauthenticated form
predates the setting. This is the one listener built to be reached from outside
the cluster, where "a token nobody set" and "an open API" are the same thing.

`mcp.dangerouslyServeWithoutAuthentication` is the way past that, and it is
spelled to be uncomfortable to type and impossible to skim past. There are real
reasons to want it, such as a gateway in front that already authenticates every
request, or a laptop-bound experiment. It exists so those people say so on
purpose rather than discovering that leaving the token empty works.

**It is on its own port, for the same reason the page is, and it matters more
here.** `service.port` answers the endpoint that spends money and writes to
your repository; this is the surface you are most likely to publish beyond the
namespace. A NetworkPolicy and a gateway both draw their lines at the port.

**This surface reveals what the page and the report comment reveal.** Every
result names the repository; past that, one entry per tool, and together they
are the list to weigh before you publish the port:

- `pipeline_report`: your Stage and Warehouse names, the namespaces the sweep
  examined, and the findings with their remedies. The evidence quotes Kargo's
  own error strings, and a remedy command names the namespace and the Stage it
  acts on.
- `gate_status`: the titles of the open pull requests, the head commits they
  stand against, the blocker counts standing on them, and what stopped a sweep
  that could not list.
- `gate_verdict`: the same for the one asked about, plus your Application,
  rendered-object and cluster names, the chart versions on either side of the
  bump, the helm and schema error strings, the repository paths of the
  manifests still declaring a dropped version, and the values keys the bump
  stops reading.
- `triage_status`: the labels standing on a pull request, and the attempts it
  has spent against the cap.
- `handoff_queue`: which pull requests the agent gave up on, and everything
  `gate_verdict` reveals about each of them.
- `verdict_history`: the verdicts the gate reached on that pull request's
  earlier head commits, with their commits and the gate's headlines, which name
  blocker counts and kinds.
- `inventory`: your fleet, meaning Application names, the namespaces those
  objects live in, the cluster names they land on, and what each one renders --
  the chart, the chart repository serving it, the version pinned to it and the
  ApplicationSet it was generated from. Every Application the ArgoCD account
  you gave bosun can list, not only the ones belonging to the repository it
  gates, so weigh this one against how broad that account is. It carries no
  values file and no values leaf: the render those come from is where the chart
  names are read, and they stop there.

No credential, no prompt, no rendered diff. The two surfaces differ in who
reads them and what they hold. These answers land in somebody's coding agent,
and that agent holds a shell and a checkout, so treat the token as read access
to your pipeline's status handed to a program.

**Text bosun did not write travels tagged.** A verdict quotes names a chart
chose and errors a tool produced, and a client of this surface holds tools
bosun refuses for itself, so every free-text field carries an origin saying
whose words they are: `bosun`, or `bosun-quoting-` something. The contract is
that instructions in a result are bosun's own or absent. The tag is not an
offer of sanitised text, which does not exist. It is the labelling a careful
client needs to fence the rest. See
[the safety model](../../docs/safety-model.md#disclosure-and-limits-of-the-mcp-surface),
and [the MCP surface](../../docs/mcp.md) for the tools, the token, and the
trust model the answers are served under.

One value is refused rather than rendered:

| Refused | Because |
|---|---|
| `mcp.enabled` with no `existingSecret` and no explicit hatch | the listener would not start; the pod runs, the Service publishes a port, and the only symptom is one WARNING in a log nobody is reading |

`allowFrom` is not a second row, on purpose. Empty admits nobody, and that is a
working configuration: a port-forward reaches the pod through the kubelet,
which NetworkPolicy does not govern, so the paragraph below is how to look at
this surface before you name anyone.

Point a client at `http://<host>:8082/mcp` over streamable HTTP, with the token
as a bearer credential. Before deciding whether to publish it, look at it over
a port-forward:

```bash
kubectl -n bosun port-forward deploy/bosun 8082 &
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json'   -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'   http://localhost:8082/mcp
```

## The halves of the network path this chart cannot write

This chart writes the policy governing what reaches the agent and what the
agent may reach. Every path it takes part in has a far end whose policy lives
in somebody else's release, and a missing rule at that end presents the same
way each time: a hang with zero bytes, not an error, so it reads as a slow
agent rather than a blocked one.

**The Kargo controller's egress**, which is the half most often missed. A
controller allowed `0.0.0.0/0` with RFC1918 excepted, a common shape since it
usually only needs to reach registries, cannot reach a ClusterIP at all. Add an
explicit rule for this service's namespace and port.

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

## Narrowing what this install grants

Five settings, all of them defaulting to what the chart did before 0.24.0
except the last. Each closes something a default install grants and does not
use.

**Credentials as files rather than environment.** An environment variable is
readable through `kubectl exec -- env`, `/proc/<pid>/environ` and a crash dump,
and every child process inherits the whole environment; this service shells out
to git and to helm.

```yaml
credentials:
  mountAsFiles: true
```

The same Secrets and the same keys, projected read-only into
`/etc/bosun/credentials`, with `GIT_TOKEN_FILE` and its four siblings set
instead of the variables. A child process then inherits a path rather than a
secret. Exactly one form is rendered per credential, and start-up fails if both
are set.

Either way the values are loaded once at start-up by the binary's
configuration loader and go only to the git host, the model provider and
ArgoCD, each over its own client. No credential is part of a prompt or of
anything published.

**Your image has to support the `_FILE` variants**: `_FILE` is a convention,
not a Kubernetes feature, so the kubelet mounts the file and sets the variable
to a path while opening that path is the binary's own code. Under an older
image every credential is unset, and the pod refuses to start over
configuration you can see is present.

**Which pods may call this, not which namespace.** A NetworkPolicy peer with
only a `namespaceSelector` admits every pod in that namespace, so the ingress
rule means "anything sharing Kargo's namespace" rather than "Kargo". The labels
are a fact about your Kargo release:

```yaml
networkPolicy:
  kargoPodSelector:
    app.kubernetes.io/name: kargo
    app.kubernetes.io/component: controller
  egress:
    dnsPodSelector:
      k8s-app: kube-dns
```

`dnsPodSelector` does the same for DNS, and DNS is the rule that matters most
here: helm runs as a subprocess over pull-request content, a chart in a pull
request can call sprig's `getHostByName`, and helm resolves it and renders the
answer into the report the agent publishes. Nothing in the service can stop
that, because a Go process cannot portably impose a resolver on a child. This
rule is the only place that lookup is held to a resolver you run. Wrong labels
drop every name lookup, which looks like a git host that is down.

**Where the scrape comes from.** With `metrics.serviceMonitor.enabled` and
`networkPolicy.enabled`, `metrics.serviceMonitor.namespace` is required and the
render fails without it. The rule used to be a pod label alone, and a pod label
is chosen by whoever creates the pod: anything in any namespace could label
itself `app.kubernetes.io/name: prometheus` and reach a port that serves the
whole HTTP surface, `POST /v1/promotion-opened` included.

**A registry or chart repository on your own network.** The agent refuses
private address space at the dial, whatever the NetworkPolicy permits, and no
configuration removes that list. Name your network to open it:

```yaml
triage:
  egressAllowPrivate: [10.42.0.0/16]
```

Without the entry the request is refused, the log names the network, and the
brief degrades to saying it had no evidence.

**Cluster-wide pod read follows `liveReads`.** See below.

## Shape

- **Read-only RBAC.** `get`/`list`/`watch` on Kargo CRDs, ArgoCD Applications
  and AnalysisRuns. Pods, events and the apps workloads come with
  `liveReads.enabled` and are not granted without it: nothing reads them with
  the feature off, and a third-party pod spec routinely carries secret material
  as a literal env value. No `create`, `update`, `patch` or `delete` anywhere.
  The agent observes the cluster and writes to pull requests, never to the
  cluster.
- **The port Kargo calls is not exposed.** No Ingress or HTTPRoute is ever
  rendered for `service.port`; publishing it would be gratuitous exposure of
  something that can spend money and write to your repository. The read-only
  status page has a port of its own and can be published, which is why it has
  one. See [The status page](#the-status-page).
- **Every network path has a far end this chart cannot write.** The agent's
  namespace must admit Kargo's controller *and* the controller's own egress
  policy must permit the agent; the same is true of argocd-server's ingress,
  which the gate's inventory read needs. A missing far end presents as a hang
  with zero bytes, not an error.
- **Secrets by reference.** The chart takes the name of an existing Secret. How
  it gets there, whether ExternalSecret, Vault Agent, SOPS or `kubectl create`,
  belongs to whoever installs this.
- **No default model provider.** `llm.provider` must be set explicitly. See
  [`adr/0004-provider-interfaces.md`](../../adr/0004-provider-interfaces.md).
