# Onboarding

How to put bosun onto a GitOps repository, start to finish. Six steps, each
ending in a state you can verify before taking the next.

The agent runs the gate itself ([ADR
0008](../adr/0008-the-gate-moves-in-cluster.md)): it polls your open pull
requests, renders base and head against the live cluster inventory read from
ArgoCD's API, and posts the `addons-gate` status and report comment on its own.
There is no CI workflow to install, no cluster-inventory snapshot to export and
keep fresh, and no paths filter to hand-edit.

## 1. What you need before anything

- **A repository the gate can read**: ArgoCD `Application`/`ApplicationSet`
  manifests, an app-of-apps bootstrap, a Helm chart or a kustomization, in any
  mix. The gate renders what the repository declares; it assumes no layout.
- **A bot identity on your git host**, rather than your own account. Every
  comment and commit carries the identity's name and avatar, and a reviewer
  should be able to tell the bot from a colleague at a glance. On GitHub, a
  GitHub App is the better shape: it has a face of its own and mints its own
  tokens. A dedicated bot user with a fine-grained PAT also works.

  Scopes: **Contents (write), Pull requests (write), Issues (write, for
  labels), Commit statuses (write), Metadata (read)**, and deliberately **not
  Workflows**. Without the Workflows permission the host rejects any push
  touching `.github/workflows/**`, which makes "the agent cannot edit CI" a
  server-side guarantee rather than a policy the agent is asked to respect.
- **A model endpoint**, anything OpenAI- or Anthropic-compatible, hosted or
  local. There is no default; the values file must name one.
- **Three Secrets in the agent's namespace**, created by whatever secret
  manager you already run. The chart consumes existing Secrets by name and
  creates none. One holds the git credential (token, or App private key), one
  the model API key, one the ArgoCD account token the gate reads the inventory
  with.

  All four are read once at start-up by the binary's configuration loader, and
  each one goes to exactly one client: the git host, the model provider,
  ArgoCD. None of them is part of a prompt, and nothing the model returns is
  consulted about them. What follows is about how the process is handed them,
  which is a separate question from who sees them.

  There are two ways. By default they arrive as environment variables, which is
  not a private place: `kubectl exec -- env` prints them,
  `/proc/<pid>/environ` holds them, and every child process inherits the whole
  environment. This service shells out to git and to helm, so a GitHub App
  private key sits in the environment of binaries with no business seeing it.
  `credentials.mountAsFiles: true` projects each one into a read-only file from
  the same Secret it already names and sets a path instead
  (`GIT_TOKEN_FILE`, `GITHUB_APP_PRIVATE_KEY_FILE`, `LLM_API_KEY_FILE`,
  `ARGOCD_TOKEN_FILE`, `PROMOTION_TOKEN_FILE`), so a child inherits a path
  rather than a secret.

  It is off by default because of the image, not the risk. `_FILE` is a
  convention rather than a Kubernetes feature: the kubelet mounts the file and
  sets the variable to a path, and opening that path is code in the binary. An
  image built before that code existed leaves every credential unset, and the
  pod refuses to start naming configuration you can see is present. Check your
  image supports it, then set it.

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

- **The gate reads from ArgoCD's API**, not from the cluster Secrets those
  clusters are stored in. Mint an account token, put it in
  `gate.argocd.existingSecret`, and give it exactly three read lines in
  `argocd-rbac-cm`:

  ```
  p, bosun, clusters, get, *, allow
  p, bosun, applications, get, */*, allow
  p, bosun, applicationsets, get, */*, allow
  ```

  The first is the cluster inventory. The other two are what your repository
  deploys, which the gate derives rather than reading out of a file you keep in
  step by hand ([ADR 0012](../adr/0012-the-repo-stops-repeating-the-ship.md)).
  The API redacts the credential block; a Secret read could not, because RBAC
  has no way to say "the labels but not the data". The cost is a credential to
  rotate and a component that can be down on its own, since argocd-server is
  not up whenever the apiserver is.
- **The NetworkPolicy has two halves and the chart can only write one.** The
  chart's own policy needs your model endpoint and, for `standard` flavor, the
  apiserver's real endpoints (`kubectl get endpoints kubernetes -n default`; a
  ClusterIP in an ipBlock matches nothing, because DNAT happens first). The
  *other* half is the **Kargo controller's** egress policy, which must permit
  this namespace and port. Missing it shows up as a hang with zero bytes rather
  than an error.
- **How tight the outbound half can be is your CNI's answer, not the chart's.**
  On Cilium, name the hosts:

  ```yaml
  networkPolicy:
    flavor: cilium
    egress:
      fqdns: [api.github.com, ghcr.io, charts.example.com]
      fqdnPatterns: ["*.githubusercontent.com", "*.quay.io"]
  ```

  A registry serves manifests from its own host and blobs from a CDN whose
  names are a set, which is what `fqdnPatterns` is for, and a name you leave
  out fails as a two-minute timeout rather than a refusal. On the default
  `flavor: standard` those two keys render into nothing — a standard
  NetworkPolicy cannot name a host — and the reachable answer is
  `egress.allowPublicHTTPS: true`, which is any public host on 443. Choose it
  knowingly: `triage.egressDeny` then forbids destinations by name and the
  agent logs every request it makes, and the internal networks stay closed
  underneath either flavor. [The safety
  model](safety-model.md) has the full version.

**Verify:** the pod starts, refusing to start if it cannot read the apiserver
or the inventory rather than running degraded, and the log says `gate:
in-cluster, polling for open pull requests every 30s`.

## 3. Open a pull request and read the scope

There is usually nothing to commit at this step. The gate asks ArgoCD which
Applications and ApplicationSets exist, keeps the ones pointing at this
repository, and renders their paths from the pull request's own checkout. The
two RBAC lines from step 2 are the configuration.

So open any pull request, even an empty one, and read the report's **What was
rendered** section. It says how many sources were derived from how many live
Applications, which is the number to check against what you expect:

```
Derived 14 sources from 65 Applications and 60 ApplicationSets that ArgoCD
serves, and rendered 16 sources in total.
```

Two things it may also tell you, both worth acting on before protection is on:

- **A root rendered from the applied spec.** An ApplicationSet nothing in
  ArgoCD created is a root, and if no manifest in this repository declares it,
  the gate falls back to the spec ArgoCD has applied. Edits to that root are
  then invisible to the gate until they apply. If its file *is* here and the
  scan missed it, or if you are about to add one, name it:

  ```yaml
  # .bosun.yaml
  roots:
    - bootstrap/addons.yaml
  ```

- **Anything under Not covered.** The honest list of what the gate could not
  expand. The time to care about it is now.

A file is still read if you have one, and `.gitops-gate.yaml` keeps working
unchanged. Write sources by hand where derivation gets something wrong: they
take precedence over derived ones.
[`gate/docs/config-reference.md`](../gate/docs/config-reference.md) has the
whole schema, and leads with why you probably do not need it.

**Verify:** the `addons-gate` status is posted, and the scope line matches the
size of the fleet you expect this repository to reach.

## 4. Protect the branch

Order matters, and step 2 is the one most often skipped:

1. Watch the gate answer a handful of real pull requests, and check the
   derived scope on each.
2. **Only then** make `addons-gate`, and only `addons-gate`, a required check
   on your default branch.
3. If you are the only human committer: with classic protection, leave *Include
   administrators* unticked; with rulesets, add a bypass for your own account
   and **not** for the bot. The bypass is also your answer for the day the
   cluster is down and a merge is urgent. A required check that cannot report
   blocks every merge, and the human override for it should exist before it is
   needed.

There is no fourth step. A CI workflow would need one, because every open pull
request has to be rebased so the workflow fires on it, but the gate polls, so
open pull requests get their verdict on the next sweep untouched. The same
property removes the requirement that the bot token be able to re-trigger CI: a
pushed fix is a new head commit, and the sweep gates it because it is there.

## 5. Wire the trigger

The gate now runs on every pull request. Triage, which reads a red gate,
repairs what is provable and escalates the rest, still wants Kargo's context:
artifact, from, to, and the files the promotion touched. Those files go into
the prompt and no further — what a fix may write is read from the pull
request's own diff, not from the body — but the context is what turns a
generic escalation into one naming the chart and the versions. It arrives as a
POST when the promotion opens the pull request:

```yaml
# charts/kargo-pipelines values; the half that is off by default:
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
run, so there is no snapshot to keep current.

What remains is ordinary operations: watch the agent's log, and treat an
`error`-state `addons-gate` ("the gate could not run") as a page. That state
means the gate is broken, which is deliberately distinct from "this change is
bad".
