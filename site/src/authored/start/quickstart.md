---
title: Quickstart
description: Two ways in — watch the whole loop run against a disposable cluster, or put the gate on a real repository. Pick the one that matches what you are trying to find out.
---

There are two things you might mean by "try Bosun", and they want different
paths.

| You want to | Take |
|---|---|
| **See it work** — watch a real pull request get gated, repaired and merged, without touching anything you own | [Track A: the proving ground](#track-a-the-proving-ground) |
| **Put it on a repository** — get the gate answering your own pull requests | [Track B: gate a real repository](#track-b-gate-a-real-repository) |

Track A needs no cluster of your own and nothing configured on your git host.
Track B is the first two steps of [Onboarding](/start/onboarding/), which is the
document to read when you are doing this for real.

Both need one thing you must supply: **an OpenAI- or Anthropic-compatible model
endpoint**. There is no default and there is not going to be one — a component
that installs cleanly and then quietly spends money against a vendor you did not
choose is a bad default. A local LM Studio or Ollama endpoint is a first-class
answer here, not a workaround; the eval numbers were measured against one.

---

## Track A: the proving ground

A disposable kind cluster running ArgoCD, Gitea, Kargo, Prometheus and Bosun,
where the entire flow runs end to end: a chart version is discovered, promoted,
written onto a branch, opened as a pull request, gated, triaged by the agent,
merged, reconciled and verified.

Everything of Bosun's own comes **from your working tree** — the agent image is
built locally, the charts install from the checkout. A proving ground that tests
the last published version is testing the past.

### What you need

- macOS or Linux, roughly **10 GB free RAM** and **20 GB disk**
- Homebrew — the runtime script installs colima, kind and idpbuilder
- A model endpoint the cluster can reach

### Bring it up

```bash
git clone https://github.com/JamesAtIntegratnIO/bosun.git
cd bosun/local
export LLM_BASE_URL=http://<your-host>:1234/v1
make up
```

`make up` builds the runtime, the cluster, the sample repository, the platform
and the kit. It takes a while the first time — most of it is idpbuilder standing
up ArgoCD and Gitea.

### Watch the loop

```bash
make demo            # the happy path: discover, promote, gate green, merge
make demo-triage     # a pull request the gate refuses, and the agent's handoff
make scenarios       # replay the recorded red-gate incidents against the live agent
```

`make scenarios` is the interesting one. Every case in
[`evals/`](https://github.com/JamesAtIntegratnIO/bosun/tree/main/evals) is a
real incident that really happened to the platform this was built for, replayed
against the live model with real commits pushed by the service.

:::note[The explain-path cases are not in that replay]
They need a green gate, and the scenario script seeds a red one. They are
covered by `go test ./evals/...`.
:::

Three more targets exercise the harder paths: `make demo-structural` (a chart that moves a field between API
versions), `make demo-forged` (a forged gate report, refused) and
`make demo-egress` (the egress deny-list refusing a host).

### Take it down

```bash
make down     # stop the cluster
make clean    # and delete everything it created
```

Full detail, including what is installed with which settings and why, is in
[The proving ground](/project/proving-ground/).

---

## Track B: gate a real repository

The goal here is the **gate answering your pull requests**. Triage, labels and
autonomous repair come after, and are deliberately not part of this first step —
watch the gate be right about a handful of real pull requests before you let
anything act on it.

### 1. Create a bot identity

Not your own account. Every comment and commit carries the identity's name and
avatar, and a reviewer should be able to tell the bot from a colleague at a
glance.

On GitHub a **GitHub App** is the better shape: it comments as `yourapp[bot]`
with a face of its own, and its installation tokens expire in about an hour
instead of never. A dedicated bot user with a fine-grained PAT also works.

Either way, these repository permissions:

| Permission | Level |
|---|---|
| Contents | read & write |
| Pull requests | read & write |
| Issues | read & write |
| Commit statuses | read & write |
| Metadata | read |
| Workflows | **none** |

That last row is load-bearing. Without the Workflows permission the host
*rejects* any push touching `.github/workflows/**`, which makes "the agent
cannot edit CI" a server-side guarantee rather than a policy the agent is asked
to respect.

### 2. Create two Secrets

The chart consumes existing Secrets by name and **creates none** — how they get
there (ExternalSecret, Vault Agent, SOPS, `kubectl`) belongs to whoever installs
it.

```bash
kubectl create namespace bosun

# the git credential: a token, or a GitHub App private key
kubectl -n bosun create secret generic bosun-git \
  --from-literal=token='<your-token>'

# the model API key — omit entirely for an unauthenticated local endpoint
kubectl -n bosun create secret generic bosun-llm \
  --from-literal=api-key='<your-key>'
```

### 3. Install the chart

```yaml
# my-values.yaml
git:
  provider: github
  owner: you
  repo: your-gitops-repo
  repoURL: https://github.com/you/your-gitops-repo.git
  existingSecret: bosun-git

llm:
  provider: openai              # or anthropic — no default
  baseURL: http://your-endpoint:1234/v1
  model: your-model
  existingSecret: bosun-llm

gate:
  mode: cluster                 # the default, stated so it is a decision

triage:
  allowPaths: []                # nothing yet — the gate first

networkPolicy:
  kargoNamespace: kargo
  egress:
    ipBlocks:
      - {cidr: 10.1.2.3/32, port: 1234}   # your model endpoint
    allowPublicHTTPS: true                # your git host
```

```bash
helm install bosun oci://ghcr.io/jamesatintegratnio/charts/bosun \
  --namespace bosun --create-namespace \
  -f my-values.yaml
```

:::caution[Two things to get right here]
**Cluster mode reads the ArgoCD cluster Secrets.** They are the live inventory
the gate renders against, and they also carry cluster credentials. The chart
scopes the grant to a namespaced Role — get/list, ArgoCD namespace only, cluster
mode only. If you will not make that grant, use
[`gate.mode: ci`](/gate/ci-adapters/).

**The NetworkPolicy has two halves and the chart can only write one.** The other
half is the Kargo controller's egress policy, which must permit this namespace
and port. The symptom of missing it is a hang with zero bytes, not an error.
:::

**Verify:** the pod starts — it refuses to start if it cannot reach the
apiserver or read the inventory, rather than running degraded — and the log
says:

```
gate: in-cluster, polling for open pull requests every 30s
```

### 4. Commit `.gitops-gate.yaml`

One file at the repository root, telling the gate what to render. A typical one
is under ten lines:

```yaml
sources:
  - name: apps
    type: manifests
    paths: ["apps/*.yaml"]
  - name: bootstrap
    type: argocd-bootstrap
    path: bootstrap/addons.yaml
```

You do **not** need the `clusters:` key or a `.gitops-gate/` directory in
cluster mode — that is the checked-in snapshot, and it exists only for the CLI
and CI. Every source type and option is in the
[`.gitops-gate.yaml` reference](/gate/config-reference/).

**Verify:** open that change as a pull request. The config is read from the pull
request's head, so the gate gates the very pull request that introduces it.
Read the report comment — the **Not covered** section is the honest list of what
the gate could not expand, and the time to care about it is now, before anything
depends on the verdict.

### 5. Then, and only then, protect the branch

Watch the gate answer a handful of real pull requests first. When you trust it,
make `addons-gate` — and only `addons-gate` — a required check on your default
branch.

If you are the only human committer, leave yourself an override: with classic
protection, leave *Include administrators* unticked; with rulesets, add a bypass
for your own account and **not** for the bot. That bypass is also your answer
for the day the cluster is down and a merge is urgent.

---

## Where to go from here

You now have the inspection half. The repair half — triage, the deterministic
migration, the escalation handoff — is steps 5 and 6 of
[Onboarding](/start/onboarding/): widen `triage.allowPaths` to the tree the
agent may write in, and wire Kargo's promotion hook so triage actually fires.

:::tip[The classic day-one trap]
`triage.enabled: false` is the `kargo-pipelines` chart's default. The agent
deploys, the gate answers, and no triage ever fires. Check with:

```bash
kubectl get stages -A -o json | grep -c promotion-opened
```

Zero means the hook is not rendered into your Stages.
:::

- [Onboarding](/start/onboarding/) — the full six-step path with the CI-mode appendix
- [Configuration](/reference/configuration/) — every value, and the env var it becomes
- [Troubleshooting](/reference/troubleshooting/) — symptoms, causes, fixes
- [The loop, end to end](/start/the-loop/) — what you just installed, as a story
