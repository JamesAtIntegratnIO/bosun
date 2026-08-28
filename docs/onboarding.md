# Onboarding

How to put bosun onto a GitOps repository, start to finish. Six steps, each
ending in a state you can verify before taking the next.

This is the **cluster-mode** path — the default since [ADR
0008](../adr/0008-the-gate-moves-in-cluster.md), where the agent runs the gate
itself: it polls your open pull requests, renders base and head against the
live ArgoCD cluster inventory, and posts the `addons-gate` status and report
comment on its own. There is no CI workflow to install, no cluster-inventory
snapshot to export and keep fresh, and no paths filter to hand-edit. The
[fallback appendix](#appendix-ci-mode-and-the-cli) covers the CI shape, which
still exists and still works — it is simply no longer the price of admission.

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
  mode: cluster               # the default, stated here so it is a decision
triage:
  allowPaths: [addons/**]     # where the agent may ever write a fix
```

Two things to know at this step, both loud rather than silent:

- **Cluster mode reads the ArgoCD cluster Secrets** — they are the live
  inventory the gate renders against, and they also carry cluster
  credentials. The chart scopes the grant to a namespaced Role (get/list,
  ArgoCD namespace only, cluster mode only). This is the one grant in the
  chart worth stopping on, and it cannot be made smaller: RBAC has no way to
  say "the labels but not the data". Two ways out if you will not make it.
  `gate.inventorySource: argocd` reads the same four fields from ArgoCD's own
  API, which redacts the credentials, and the Role stops being created — the
  cost is an ArgoCD account token with `clusters, get` and a second component
  that can be down. Or use [CI mode](#appendix-ci-mode-and-the-cli) and keep
  the checked-in snapshot.
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
**not** need the `clusters:` key or a `.gitops-gate/` directory in cluster
mode — that is the checked-in snapshot, and it exists only for the CLI and CI.

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

There is no fourth step. CI mode needs one — every open pull request has to be
rebased so the workflow fires on it — but the cluster-mode gate polls, so open
pull requests get their verdict on the next sweep untouched. The same property
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
run, so there is no snapshot to keep current — CI mode needs a cron with
cluster access running `clusters export -check` for exactly that reason, and
cluster mode does not.

What remains is ordinary operations: watch the agent's log, and treat an
`error`-state `addons-gate` ("the gate could not run") as a page. It is the
gate reporting that *it* is broken, which is deliberately distinct from "this
change is bad".

---

## Appendix: CI mode and the CLI

`gate.mode: ci` keeps the original shape, whole. Use it when:

- **The repository takes fork pull requests.** Cluster mode refuses them by
  default (rendering a stranger's helm values inside your cluster is a trust
  decision, and the refusal is an `error` status naming `gate.forkPRs`). A
  CI runner is a throwaway sandbox; the cluster is not.
- **You will not grant the Secret read**, or want the gate to keep answering
  while the cluster itself is down.
- **You want the gate with no agent at all** — it is still a container with
  an exit code, runnable from any CI.

The CI path needs everything cluster mode deleted: the checked-in inventory
(`gitops-gate clusters export`, plus `clustersExport.ignoreKeys` /
`knownAbsentLabels` until `-check` stops flapping, plus somewhere with
cluster access to run that check on a schedule), the workflow from
[`ci/github/`](../ci/github), and the rollout order in
[`ci/github/README.md`](../ci/github/README.md). The contract an adapter must
satisfy — including posting the report comment the agent scrapes for its
verdict — is [`ci/README.md`](../ci/README.md).

The CLI is the same engine either way, and works against the snapshot for
local pre-push runs:

```bash
gitops-gate render -repo . -out targets.json
gitops-gate diff -base targets-base.json -head targets-head.json -repo .
```

## Appendix: migrating from CI mode to cluster mode

For a repository already running the CI gate:

1. Upgrade the chart with `gate.mode: cluster` (and nothing else changed).
   Both gates now answer with the same check name; last writer wins per
   commit, and the agent skips commits that already carry a verdict — the
   overlap is noise, not conflict.
2. Watch one pull request get its status from the agent (the log line names
   the sweep).
3. Delete, in one commit, everything only the CI shape needed:
   `.github/workflows/validate-addons.yaml` (or your host's equivalent), any
   gate-image pin file it read, `.gitops-gate/` and the `clusters:` /
   `clustersExport:` keys in `.gitops-gate.yaml`, and whatever ran the
   scheduled drift check.
4. Branch protection does not change — same check name, same rule.
