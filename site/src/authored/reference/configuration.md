---
title: Configuration
description: Every chart value, the environment variable it becomes, and its default, plus the values that are load-bearing enough to be worth a paragraph.
---

The chart is the supported surface; the environment variables are what the
binary reads. Both are listed because you will meet both: the chart
when you install, and the env vars when you read a pod spec at three in the
morning.

The machine-checkable version of this page is
[`charts/bosun/values.schema.json`](https://github.com/JamesAtIntegratnIO/bosun/blob/main/charts/bosun/values.schema.json),
which `helm install` validates against before it renders anything.

:::note
**REQUIRED** below means the chart refuses to render without it, or the process
refuses to start. Both failures are deliberately loud.

Three values are Secret *keys* rather than settings: the chart reads the value
at that key and passes it as an environment variable. Those rows show the
variable with an arrow, because the startup error names the variable and not the
chart value. `missing required configuration: GIT_TOKEN` means `git.tokenKey`
does not match a key in `git.existingSecret`.
:::

## Git host

| Value | Env | Default | |
|---|---|---|---|
| `git.provider` | `GIT_PROVIDER` | `github` | `github`, `gitea` implemented; `gitlab`, `bitbucket` are extension points |
| `git.owner` | `GIT_OWNER` | n/a | **REQUIRED** |
| `git.repo` | `GIT_REPO` | n/a | **REQUIRED** |
| `git.repoURL` | `GIT_REPO_URL` | n/a | **REQUIRED**. Clone URL, reachable from the cluster |
| `git.existingSecret` | n/a | n/a | **REQUIRED**. Existing Secret holding the credential; the chart creates none |
| `git.tokenKey` | → `GIT_TOKEN` | `token` | Key within that Secret. Its **value** becomes `GIT_TOKEN`, which is the name the startup error uses |
| `git.apiBase` | `GIT_API_BASE` | *(unset)* | See below; it means different things per host |
| `git.insecureSkipTLSVerify` | `GIT_INSECURE_SKIP_TLS_VERIFY` | `false` | Scoped to the agent's git client and its push clone, never global |
| `git.author.name` | `GIT_AUTHOR_NAME` | *(derived)* | See below; leave empty unless you have a reason |
| `git.author.email` | `GIT_AUTHOR_EMAIL` | *(derived)* | " |
| `git.app.appId` | `GITHUB_APP_ID` | *(unset)* | Set to authenticate as a GitHub App instead of a token |
| `git.app.installationId` | `GITHUB_APP_INSTALLATION_ID` | *(discovered)* | Optional; discovered from the repository when unset |
| `git.app.existingSecret` | n/a | `git.existingSecret` | Secret holding the App private key |
| `git.app.privateKeyKey` | → `GITHUB_APP_PRIVATE_KEY` | `private-key` | Key within it. Its **value** becomes `GITHUB_APP_PRIVATE_KEY` |

When `git.app.appId` is set, `GIT_TOKEN` is **not set at all**: installation
tokens are minted from the key at runtime and live about an hour, so there is
nothing static to hold.

### `git.apiBase` is host-specific

The hosts differ, so the value does:

- **github**: the API *root*, `https://ghe.example.com/api/v3`
- **gitea**: the **instance** root, `https://gitea.example.com`. The client
  appends `/api/v1` itself, and also needs that root to build a push remote.

### Leave `git.author` empty

Empty means the agent derives it. As a GitHub App that is its own bot identity
(`<slug>[bot] <id+slug[bot]@users.noreply.github.com>`), which is what makes the
pushed commits attribute to the App's avatar rather than to a stranger.

If you do set an email, never use a `users.noreply.github.com` address that is
not your bot's own: that namespace **belongs to GitHub accounts**. An earlier
default of `bosun@users.noreply.github.com` attributed every commit the first
live repair pushed, avatar and all, to an unrelated account named `bosun`.

## Model

| Value | Env | Default | |
|---|---|---|---|
| `llm.provider` | `LLM_PROVIDER` | n/a | **REQUIRED**. `openai` or `anthropic` |
| `llm.model` | `LLM_MODEL` | n/a | **REQUIRED** |
| `llm.baseURL` | `LLM_BASE_URL` | *(unset)* | Required for `openai`; optional for `anthropic` |
| `llm.reasoningEffort` | `LLM_REASONING_EFFORT` | *(unset)* | Passed through where supported; leave unset otherwise |
| `llm.timeout` | `LLM_TIMEOUT` | `10m` | |
| `llm.existingSecret` | n/a | *(unset)* | Omit entirely for an unauthenticated local endpoint |
| `llm.apiKeyKey` | → `LLM_API_KEY` | `api-key` | Key within that Secret. Its **value** becomes `LLM_API_KEY` |

There is **no default provider**. `openai` reaches OpenAI, Azure OpenAI, LM
Studio, Ollama, vLLM, llama.cpp and LiteLLM; `anthropic` reaches Anthropic and
gateways presenting the Messages API. See
[Model providers](/reference/llm-providers/) for how to choose a model, and why
the score to optimise is *unsafe actions = 0* rather than accuracy.

## The gate

| Value | Env | Default | |
|---|---|---|---|
| `gate.checkName` | `GATE_CHECK_NAME` | `addons-gate` | Must match your branch protection rule |
| `gate.forkPRs` | `GATE_FORK_PRS` | `false` | Render pull requests whose head is in another repository |
| `gate.poll` | `GATE_POLL` | `30s` | Paces the sweep for pull requests with no verdict yet |
| `gate.argocd.baseURL` | `ARGOCD_BASE_URL` | *(none)* | **Required.** The ArgoCD API the inventory is read from; see below |
| `gate.argocd.podPort` | *(NetworkPolicy only)* | `8080` | The port **argocd-server's pod** listens on. Not the port in `baseURL`; see below |
| `gate.argocd.existingSecret` | `ARGOCD_TOKEN` | *(none)* | **Required.** Secret holding the ArgoCD account token, key `tokenKey` |
| `gate.argocd.caSecret` | `ARGOCD_CA_FILE` | *(none)* | Secret holding the CA that verifies argocd-server, key `caKey` |
| `gate.argocd.insecureSkipTLSVerify` | `ARGOCD_INSECURE_SKIP_TLS_VERIFY` | `false` | Accept any certificate from argocd-server |

### `gate.argocd` is where the inventory comes from

The gate reads four fields per cluster, name, server, labels and annotations,
from `GET /api/v1/clusters` on the ArgoCD API, which serves them with the
credential block redacted. Mint the token with

```bash
argocd account generate-token --account bosun
```

and give it one line in `argocd-rbac-cm`, `p, bosun, clusters, get, *, allow`,
and nothing else.

**It is the API and not the cluster Secrets those clusters are stored in
because that read cannot be made small enough.** Kubernetes RBAC has no
predicate for "the labels but not the data": there are no deny rules,
`resourceNames` does not apply to `list` (a list request carries no name for
the authorizer to match), and a label selector in the request is a filter the
apiserver applies *after* authorising, so a token holding such a Role could
drop it and read every Secret in the namespace. The chart creates no Role over
Secrets at all.

What it costs, stated as plainly as the grant it replaces: a credential to
rotate, a component that can be down on its own (the apiserver is up whenever
the cluster is; argocd-server is not), and its own TLS story, because
argocd-server serves its own certificate rather than the one the kubelet mounts
into every pod. The chart adds the NetworkPolicy egress rule for the ArgoCD
namespace itself, because argocd-server is a ClusterIP and forgetting that
hangs with zero bytes.

### `gate.argocd.podPort` is the pod's port, not the URL's

The rule the chart adds opens `gate.argocd.podPort`, and **that is normally not
the port in `baseURL`.** A NetworkPolicy matches the destination port of the
packet, and a ClusterIP is DNAT'd to the backend pod's port *before* policy is
evaluated: whatever port the Service published, the packet reaching the rule is
addressed to the pod, on `8080`.

Setting it to the Service port instead renders clean, passes `helm lint`,
passes the chart's schema, and then drops every packet. There is no error at
either end: the connection hangs for the full HTTP timeout, and the pod dies at
start-up saying argocd-server is unreachable, which is true and points nowhere
near the values file.

`8080` is argocd-server's container port in the upstream argo-cd chart, and it
does not move with `server.insecure`: the Service publishes both 80 and 443
against the same container port, and argocd-server decides per connection
whether to speak TLS on it. So the two common installs differ only in the URL,
`http://argocd-server.argocd.svc` with nothing to verify or
`https://argocd-server.argocd.svc` with `caSecret`, and both want `podPort:
8080`. Confirm yours:

```bash
kubectl -n argocd get svc argocd-server -o jsonpath='{.spec.ports[*].targetPort}{"\n"}'
```

The chart writes bosun's *egress*. argocd-server's *ingress* policy lives in
your ArgoCD release, and until both exist the connection is dropped with
nothing logged at either end; the
[chart README](https://github.com/JamesAtIntegratnIO/bosun/blob/main/charts/bosun/README.md)
carries a copy-pasteable rule for it.

### `gate.forkPRs` is off for a reason

The render runs `helm` over the pull request's content, **inside your
cluster**. Whose content that is should be an operator's decision. Off, a fork
pull request gets an `error` status naming this value: a refusal you can see,
rather than a required check that never reports.

## Triage

| Value | Env | Default | |
|---|---|---|---|
| `triage.allowPaths` | `ALLOW_PATHS` | `[]` | **Where the agent may ever write.** Empty refuses everything, and the process refuses to start with it |
| `triage.denyPaths` | `DENY_PATHS` | `[]` | *Added to* the built-in deny-list; cannot subtract from it |
| `triage.maxAttempts` | `MAX_ATTEMPTS` | `2` | Attempt cap, tracked by pull-request label |
| `triage.explainGreen` | `EXPLAIN_GREEN` | `true` | Explain green gates on held pull requests |
| `triage.migrateDroppedVersions` | `MIGRATE_DROPPED_VERSIONS` | `true` | The deterministic apiVersion repair. **No model involved** |
| `triage.structuralMigration` | `STRUCTURAL_MIGRATION` | `true` | The document-reshape path, for bumps where swapping the version is not the whole job |
| `triage.migrateMaxDocs` | `MIGRATE_MAX_DOCS` | `5` | Cap on documents reshaped in one pass |
| `triage.egressDeny` | `EGRESS_DENY` | `[]` | Hosts the upstream lookup must never reach |
| `triage.upstreamNotes.enabled` | `UPSTREAM_NOTES` | `true` | Fetch publisher release notes for the explain and escalate paths |
| `triage.upstreamNotes.maxReleases` | `UPSTREAM_MAX_RELEASES` | `5` | |
| `triage.upstreamNotes.maxCommits` | `UPSTREAM_MAX_COMMITS` | `10` | |
| `triage.upstreamNotes.maxBodyChars` | `UPSTREAM_MAX_BODY_CHARS` | `4000` | |

### `triage.allowPaths` is the whole write surface

An empty allowlist refuses everything and the process **refuses to start** with
one. A service that can write nowhere and does not say so looks broken later,
for a reason nobody will find.

Set it to the tree the agent may repair, typically `[addons/**]`. It is a
standing grant and deliberately coarse; the per-request bound is `Scope`, set
from the promotion's own file list, and both must pass.

### The deny-list cannot be shrunk

`triage.denyPaths` **adds** to a built-in list that configuration cannot remove
from. Every entry is a way to make a red gate green without fixing anything:

```
.github/**            the workflows that run the gate
.gitops-gate.yaml     what the gate renders, and how
.gitops-gate/**       the cluster inventory it compares against
delivery/**           the kit itself, including this agent and its prompt
.gitlab-ci.yml        the GitLab and Bitbucket equivalents
bitbucket-pipelines.yml
**/kargo-projects/**  the merge policy and version constraints
**/kargo-pipelines/** the promotion pipelines themselves
```

The matcher understands `**` at the start of a pattern, at the end, or at both,
**not in the middle**. A wildcard inside a directory name (`**/kargo-*/**`) is
not something the deny-list can express.

### `triage.upstreamNotes` never feeds the write path

Release notes and upstream commits are fetched **only** on the paths that
produce prose: the green-gate explanation and an escalation. The mechanical
path, the one that writes files, does not fetch them, so they are not in the
evidence string the applier corroborates against.

Without that rule, a commit message containing `v1.5.0` would make `v1.5.0` a
corroborated value to write. See
[ADR 0005](/decisions/0005-testimony-is-not-evidence/).

## Live cluster reads

| Value | Env | Default | |
|---|---|---|---|
| `liveReads.enabled` | `LIVE_READS` | `false` | Off by default: everything else the agent reads is public or already in the pull request |
| `liveReads.scope` | *(RBAC only)* | `groups` | `groups` or `wide` |
| `liveReads.apiGroups` | *(RBAC only)* | `[]` | The groups granted under `groups` scope |
| `liveReads.argocdNamespace` | `LIVE_READS_ARGOCD_NS` | `argocd` | Also the namespace the chart opens egress to for argocd-server |

Live reads are `get` and `list` only. The chart's ClusterRole has no `create`,
`update`, `patch` or `delete` verb anywhere.

:::danger[`scope: wide` can read Secrets]
With `groups`, the core API group is never granted beyond `pods, events`, and
the chart creates no namespaced Secret Role anywhere. With `wide` the chart
grants
`apiGroups: ["*"]`, which **includes Secrets**. RBAC has no deny rules and no
way to subtract the core group, which is why "everything except Secrets" is not
a setting this chart can offer.
:::

## The supervisor

| Value | Env | Default | |
|---|---|---|---|
| `supervise.enabled` | `SUPERVISE_PIPELINE` | `true` | |
| `supervise.interval` | `SUPERVISE_INTERVAL` | `10m` | |
| `metrics.serviceMonitor.enabled` | n/a | `false` | Scrape `/metrics` |

Read-only: three LISTs and a shallow clone, using the Kargo read the ClusterRole
already grants. Both `/pipeline` and `/metrics` answer `503` before the first
sweep completes, deliberately: a scraper reading zeroes from a supervisor that
has not looked yet would record "nothing is wrong" as a measurement.

See [The pipeline supervisor](/concepts/supervisor/) for what it looks for and
the two alert rules worth having, including the one that fires when the
supervisor *itself* goes quiet.

## Network policy

| Value | Default | |
|---|---|---|
| `networkPolicy.enabled` | `true` | |
| `networkPolicy.flavor` | `standard` | |
| `networkPolicy.kargoNamespace` | `kargo` | Which namespace may call the triage hook |
| `networkPolicy.egress.dnsNamespace` | `kube-system` | |
| `networkPolicy.egress.namespaces` | `[]` | |
| `networkPolicy.egress.ipBlocks` | `[]` | Your model endpoint goes here |
| `networkPolicy.egress.apiServer.ipBlocks` | `[]` | The apiserver's **real** endpoints |
| `networkPolicy.egress.fqdns` | `[]` | Registries the upstream lookup may reach |
| `networkPolicy.egress.fqdnPatterns` | `[]` | |
| `networkPolicy.egress.allowPublicHTTPS` | `false` | Your git host |
| `networkPolicy.egress.allowInternet` | `false` | |

:::caution[Two halves, and the chart writes one]
For `flavor: standard` the egress policy needs the apiserver's **real
endpoints**, from `kubectl get endpoints kubernetes -n default`. A ClusterIP in
an `ipBlock` matches nothing, because DNAT happens before policy evaluation.

The other half is the **Kargo controller's** egress policy, which must permit
this namespace and port. The chart cannot write it. Missing it shows up as a
hang with zero bytes rather than an error.
:::

## Deployment shape

| Value | Env | Default |
|---|---|---|
| `image.repository` | n/a | **REQUIRED** |
| `image.tag` / `image.digest` | n/a | *(appVersion)*; prefer a digest |
| `image.pullPolicy` | n/a | `IfNotPresent` |
| `replicaCount` | n/a | `1` |
| `service.port` | `AGENT_ADDR` | `8080` |
| `branding.name` | `AGENT_BRAND` | `Bosun` |
| `serviceAccount.create` / `.name` | n/a | `true` / *(fullname)* |
| `rbac.create` | n/a | `true` |
| `resources` | n/a | `25m` CPU / `64Mi` requested, `512Mi` memory limit |
| `nodeSelector`, `tolerations`, `affinity`, `podAnnotations`, `priorityClassName` | n/a | standard |

The pod spec also carries `CLONE_ROOT=/work`, which is not a chart value. It is
where the agent and the gate clone a pull request's branch, backed by an
`emptyDir` so the root filesystem can stay read-only. Nothing there is worth
persisting: a restart mid-triage starts clean rather than resuming.

`branding.mark` is **deprecated and ignored** since 0.17.0. Comments no longer
carry an identity header at all. Authenticating as a GitHub App puts the name
and avatar above every comment already, and a bold header under that was the
agent introducing itself twice. Still accepted so setting it does not fail an
upgrade.

:::note[There is no Ingress]
Kargo calls this in-cluster. Nothing else needs to reach it, and publishing
something that can spend money and write to your repository would be gratuitous
exposure, so the chart renders no Ingress or HTTPRoute at all.
:::

## The gate's own config file

`.gitops-gate.yaml` lives in the **repository being gated**, not in this chart.
It is documented separately in the
[`.gitops-gate.yaml` reference](/gate/config-reference/).
