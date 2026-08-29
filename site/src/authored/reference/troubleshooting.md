---
title: Troubleshooting
description: Symptoms, causes and fixes, organised by what you observe, including the failures whose symptom is that nothing happens at all.
---

Organised by what you see rather than by what is wrong. Several of these share
one symptom, *nothing happened*, which is why they are worth writing down.

## The pod will not start

The process refuses to start rather than running degraded, on purpose. A crash
loop names its cause in the log; a degraded process does not.

| Log says | Cause | Fix |
|---|---|---|
| `missing required configuration: …` | A **required** value is unset | See [Configuration](/reference/configuration/): `git.owner`, `git.repo`, `git.repoURL`, `git.existingSecret`, `llm.provider`, `llm.model` |
| `missing required configuration: GIT_TOKEN` | The Secret exists but the **key** does not | `git.tokenKey` must name a key in `git.existingSecret`. The error names the environment variable, not the chart value |
| `missing required configuration: GITHUB_APP_PRIVATE_KEY (required with GITHUB_APP_ID)` | `git.app.appId` is set without a readable key | Check `git.app.privateKeyKey` against the Secret in `git.app.existingSecret` (defaulting to `git.existingSecret`) |
| `ALLOW_PATHS is empty: the agent could never apply any fix` | `triage.allowPaths: []` | Set it to the tree the agent may write in, e.g. `[addons/**]` |
| `LLM_BASE_URL is required for the openai provider` | `llm.provider: openai` with no `baseURL` | Set `llm.baseURL`. This is what makes a self-hosted model work |
| `unknown LLM_PROVIDER "…" (openai or anthropic)` | Typo, or a provider that does not exist | Only `openai` and `anthropic` are implemented |
| `GIT_PROVIDER "…" is not implemented yet` | `gitlab` or `bitbucket` | Those are extension points, not implementations. See [Git providers](/reference/git-providers/) |
| `ARGOCD_BASE_URL is empty` | `gate.argocd.baseURL` unset. It has no default | Set it to the ArgoCD API server, e.g. `https://argocd-server.argocd.svc` |
| `ARGOCD_TOKEN is empty` | No ArgoCD account token | `argocd account generate-token --account <account>`, and give that account `clusters, get`, `applications, get` and `applicationsets, get` in ArgoCD's RBAC |
| `the cluster inventory could not be read: … 401` | The token is wrong, expired, or its account lacks `clusters, get` | Check `p, <account>, clusters, get, *, allow` is in `argocd-rbac-cm` |
| `the ArgoCD account may not read applications` (or `applicationsets`) | The account has the inventory line but not the two the derived scope needs | Paste the line the error names into `argocd-rbac-cm`. The gate refuses rather than rendering a scope it could not see |
| `the cluster inventory could not be read: … context deadline exceeded` | Nothing refused the connection; it hung until the timeout. Almost always a NetworkPolicy naming the wrong port | `gate.argocd.podPort` must be argocd-server's **pod** port (`8080`), not the port in `baseURL`. See [Configuration](/reference/configuration/#gateargocdpodport-is-the-pods-port-not-the-urls). argocd-server's own ingress policy must admit bosun's namespace on that same port |
| `github app authentication failed` | Wrong `appId`, wrong key, or the App is not installed on the repository | Check `git.app.privateKeyKey` matches the Secret's key |

## The gate never reports on a pull request

**Check the obvious one first.** The log line on a healthy agent is:

```
gate: polling for open pull requests every 30s
```

If that line is absent, the gate is not running; go back to the table above.

| Symptom | Cause |
|---|---|
| The check never appears on **fork** pull requests, and the status says `gate.forkPRs` | Working as designed. The render runs the pull request's helm content *inside your cluster*; whose content that is is an operator's decision. Set `gate.forkPRs: true` |
| `no .gitops-gate.yaml at the head revision` | The config is read from the pull request's **head**. A pull request that predates the config, or deletes it, has nothing to render |
| The check appears but is `error` | The gate could not *run*: bad config, an unreachable chart repository. This is deliberately distinct from "this change is bad" and is worth paging on |
| Nothing on pull requests opened before install | Should not happen; the sweep picks them up. That is CI-workflow behaviour, which needs a rebase to fire |

:::caution[An `error` status is not a red gate]
`failure` means the change is blocking. `error` means the gate is broken.
Treating them the same is how a broken gate gets ignored for a week.
:::

## Triage never fires

The gate answers, the pull request is red, and the agent does nothing. One
cause accounts for most of these.

`triage.enabled: false` is the **`kargo-pipelines` chart's default**, so the
hook that POSTs promotion context to the agent is not rendered into your Stages:

```bash
kubectl get stages -A -o json | grep -c promotion-opened
```

Zero means the hook is not there. Fix it in the pipelines chart's values, not
the agent's:

```yaml
triage:
  enabled: true
  url: http://bosun.bosun.svc:8080/v1/promotion-opened
```

### If the hook is rendered and it still hangs

**A hang with zero bytes is the NetworkPolicy.** The chart writes its own
policy; it cannot write the Kargo controller's egress policy, which must permit
this namespace and port. Missing that half produces a hang rather than an
error.

## The model returns nothing, or nonsense

### Empty responses from a reasoning model

Verified against LM Studio serving `qwen3.6-35b-a3b`: the schema-constrained
JSON arrived in `message.reasoning_content` with `message.content` **empty**. A
client reading only `content` sees nothing and reports a broken model.

The `openai` implementation already handles this: it tries `content`, then
`reasoning_content`, then `reasoning`. If you wrote your own provider, do the
same. Most llama.cpp-derived servers behave this way with a reasoning model.

### It classifies fine but nothing gets fixed

Expected, and safe. A `mechanical` verdict that applies zero edits **escalates**
rather than reporting success. The comment lists every refused edit with its
reason. Common reasons:

| Refusal | Meaning |
|---|---|
| `from` did not match | The model paraphrased the current value instead of copying it |
| Not corroborated | A version-shaped value that does not appear verbatim in the evidence. The refusal is the guarantee working. See [ADR 0005](/decisions/0005-testimony-is-not-evidence/) |
| Outside scope | The fix lives in a file the promotion never touched |
| Denied path | It tried to edit CI config, the gate, or the merge policy |

### Choose a different model

Score in this order. **Unsafe actions must be zero**, and anything above zero
disqualifies a model at any accuracy. Then classification, then full pass. A
model with mediocre classification and zero unsafe is usable; it escalates more
than it needs to, which costs a human two minutes. See
[Model providers](/reference/llm-providers/).

## The supervisor returns 503

Before the first sweep completes, both `/pipeline` and `/metrics` answer `503`.
That is deliberate: a scraper reading zeroes from a supervisor that has not
looked yet would record "nothing is wrong" as a measurement.

Wait one `supervise.interval` (default `10m`), then:

```bash
kubectl -n bosun port-forward deploy/bosun 8080 &
curl -s localhost:8080/pipeline
```

## Kargo has stopped promoting and nothing is red

This is the supervisor's whole subject. Its failure mode is silence: a Warehouse
that stopped discovering, a promotion that errored on a transient blip, a
verification that cannot reach Prometheus. None produce an alert, a red check,
or an unhealthy Application.

Four remedies that are not guessable, and that the supervisor's findings carry
verbatim:

| Situation | The thing that does not work | What does |
|---|---|---|
| Wedged promotion | `kargo.akuity.io/abort=true`, **silently ignored** because the value is parsed as a request object | `kargo.akuity.io/abort={"action":"terminate"}` |
| Wedged promotion | A **Warehouse refresh**. It re-discovers artifacts; freight carrying a terminal promotion is never auto-promoted again | Abort, then re-promote |
| Failed verification | Fixing the cause. The verification is over; the Stage stays stuck | `kargo.akuity.io/reverify={"id":"…"}`, id from `status.freightHistory[0].verificationHistory[0].id` |
| Hand-written Promotion rejected | A `generateName` with a trailing dot, which the webhook validates as RFC1123 | Drop the trailing dot |

See [The pipeline supervisor](/concepts/supervisor/), and add the
`PipelineSupervisorSilent` alert. A supervisor whose subject is silent failure
has to be able to fail loudly itself.

## Commits are attributed to the wrong person

A token belongs to whoever minted it, so comments and commits arrive under that
person's name and avatar.

- **Set `git.app.appId`** and give it a private key. The App comments as
  `yourapp[bot]`, with its own avatar, and its tokens expire hourly.
- **Leave `git.author.name` and `git.author.email` empty.** Empty means derived,
  which as an App is its own bot identity.
- **Never set an `@users.noreply.github.com` address that is not your bot's
  own.** That namespace belongs to GitHub accounts. An earlier default of
  `bosun@users.noreply.github.com` attributed the first live repair's commits,
  avatar and all, to an unrelated account named `bosun`.

## Still stuck

- [Configuration](/reference/configuration/): every value and what it does
- [Onboarding](/start/onboarding/): the six steps, each with a verification
- [The proving ground](/project/proving-ground/): reproduce it on a disposable cluster
- [Open an issue](https://github.com/JamesAtIntegratnIO/bosun/issues)
