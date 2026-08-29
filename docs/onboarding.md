# Onboarding

How to put bosun onto a GitOps repository, start to finish. Six steps, each
ending in a state you can verify before taking the next.

The agent runs the gate itself ([ADR
0008](../adr/0008-the-gate-moves-in-cluster.md)): it polls your open pull
requests, renders base and head against the live cluster inventory read from
ArgoCD's API, and posts the `addons-gate` status and report comment on its own.
There is no CI workflow to install, no cluster-inventory snapshot to export and
keep fresh, and no paths filter to hand-edit. The [CLI
appendix](#appendix-the-cli) covers running the same engine from a
workstation.

## 1. What you need before anything

- **A repository the gate can read**: ArgoCD `Application`/`ApplicationSet`
  manifests, an app-of-apps bootstrap, a Helm chart or a kustomization — any
  mix. The gate renders what the repository declares; it assumes no layout.
- **A bot identity on your git host.** Not your own account — every comment
  and commit carries the identity's name and avatar, and a reviewer should be
  able to tell the bot from a colleague at a glance. On GitHub, a GitHub App
  is the better shape (it has a face of its own and mints its own tokens);
  a dedicated bot user with a fine-grained PAT also works.

  Scopes: **Contents (write), Pull requests (write), Issues (write — labels),
  Commit statuses (write), Metadata (read)** — and deliberately **not
  Workflows**. Without the Workflows permission the host rejects any push
  touching `.github/workflows/**`, which makes "the agent cannot edit CI" a
  server-side guarantee rather than a policy the agent is asked to respect.
- **A model endpoint** — anything OpenAI- or Anthropic-compatible, hosted or
  local. There is no default; the values file must name one.
- **Two Secrets in the agent's namespace**, created by whatever secret
  manager you already run — the chart consumes existing Secrets by name and
  creates none. One holds the git credential (token, or App private key), one
  the model API key.

## 2. Install the agent

```bash
helm install bosun oci://ghcr.io/jamesatintegratnio/charts/bosun \
  --namespace bosun --create-namespace \
  -f my-values.yaml
```

The values that matter, with the full contract in
[`charts/bosun/README.md`](../charts/bosun/README.md):

```yaml
git:
  provider: github
  owner: you
  repo: your-gitops-repo
  repoURL: https://github.com/you/your-gitops-repo.git
  existingSecret: bosun-github
llm:
  provider: openai            # or anthropic
  baseURL: http://your-endpoint:1234/v1
  model: your-model
  existingSecret: bosun-llm
gate:
  argocd:
    baseURL: https://argocd-server.argocd.svc   # required; no default
    existingSecret: bosun-argocd                # the account token
triage:
  allowPaths: [addons/**]     # where the agent may ever write a fix
```

Two things to know at this step, both loud rather than silent:

- **The gate reads its inventory from ArgoCD's API**, not from the cluster
  Secrets those clusters are stored in. Mint an account token, give it
  `clusters, get` in `argocd-rbac-cm` and nothing else, and put it in
  `gate.argocd.existingSecret`. The API redacts the credential block; a Secret
  read could not, because RBAC has no way to say "the labels but not the data".
  The cost is a credential to rotate and a component that can be down on its
  own — argocd-server is not up whenever the apiserver is.
- **The NetworkPolicy has two halves and the chart can only write one.** The
  chart's own policy needs your model endpoint and, for `standard` flavor,
  the apiserver's real endpoints (`kubectl get endpoints kubernetes -n
  default` — a ClusterIP in an ipBlock matches nothing, because DNAT happens
  first). The *other* half is the **Kargo controller's** egress policy, which
  must permit this namespace and port — the symptom of missing it is a hang
  with zero bytes, not an error.

**Verify:** the pod starts — it refuses to start if it cannot read the
apiserver or the inventory, rather than running degraded — and the log says
`gate: in-cluster, polling for open pull requests every 30s`.

## 3. Commit `.gitops-gate.yaml`

One file at the repository root, telling the gate what to render. A typical
one is under ten lines:

```yaml
sources:
  - name: apps
    type: manifests
    paths: ["apps/*.yaml"]
  - name: bootstrap
    type: argocd-bootstrap
    path: bootstrap/addons.yaml
```

Source types (`manifests`, `argocd-bootstrap`, `helm`, `kustomize`,
`rendered`) and every optional key are in
[`gate/docs/config-reference.md`](../gate/docs/config-reference.md). You do
**not** need the `clusters:` key or a `.gitops-gate/` directory — that is the
checked-in snapshot, and it exists only for the CLI.

**Verify:** open this change as a pull request. The gate should render it,
post an `addons-gate` status, and — since the config is read from the pull
request's head — gate the very pull request that introduces it. Read the
report comment; the **Not covered** section is the honest list of what the
gate could not expand, and the time to care about it is now, not after
protection is on.

## 4. Protect the branch

Order matters, and step 2 is the one most often skipped:

1. Merge the config. Watch the gate answer a handful of real pull requests.
2. **Only then** make `addons-gate` — and only `addons-gate` — a required
   check on your default branch.
3. If you are the only human committer: with classic protection, leave
   *Include administrators* unticked; with rulesets, add a bypass for your
   own account and **not** for the bot. The bypass is also your answer for
   the day the cluster is down and a merge is urgent — a required check that
   cannot report is the gate failing loudly, and the human override for it
   should exist before it is needed.

There is no fourth step. A CI workflow would need one — every open pull request
has to be rebased so the workflow fires on it — but the gate polls, so open pull
requests get their verdict on the next sweep untouched. The same property
removes the requirement that the bot token be able to re-trigger CI: a pushed
fix is a new head commit, and the sweep gates it because it is there.

## 5. Wire the trigger

The gate now runs on every pull request. The *triage* — reading a red gate,
repairing what is provable, escalating the rest — still wants Kargo's context
(artifact, from, to, the files the promotion touched), which arrives as a
POST when the promotion opens the pull request:

```yaml
# charts/kargo-pipelines values — the half that is off by default:
triage:
  enabled: true
  url: http://bosun.bosun.svc:8080/v1/promotion-opened
```

`triage.enabled: false` is the chart's default. Forgetting it is the most
common day-one mistake, and its symptom is not an error: the agent deploys, the
gate answers, and no triage ever fires. **Verify:**

```bash
kubectl get stages -A -o json | grep -c promotion-opened
```

Zero means the hook is not rendered into your Stages.

## 6. Keep it healthy

Nothing here needs a schedule. The live inventory is read fresh on every gate
run, so there is no snapshot to keep current. The `clusters export -check` drift
cron that a snapshot would need does not exist here.

What remains is ordinary operations: watch the agent's log, and treat an
`error`-state `addons-gate` ("the gate could not run") as a page. It is the
gate reporting that *it* is broken, which is deliberately distinct from "this
change is bad".

---

## Appendix: the CLI

The gate is the same engine from a workstation, against the checked-in
snapshot, for a local run before pushing:

```bash
gitops-gate clusters export -out .gitops-gate/clusters.yaml
gitops-gate render -repo . -out targets.json
gitops-gate diff -base targets-base.json -head targets-head.json -repo .
```

The snapshot is a CLI-only concern: the in-cluster gate reads the inventory
live and never opens that file.
