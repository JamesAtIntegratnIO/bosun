---
title: Troubleshooting
description: Symptoms, causes and fixes — organised by what you actually observe, including the failures whose symptom is that nothing happens at all.
---

Organised by what you see, not by what is wrong. Several of these have the same
symptom — *nothing happened* — which is exactly why they are worth writing down.

## The pod will not start

The process refuses to start rather than running degraded, on purpose: a crash
loop with an explanation beats a quiet shrug.

| Log says | Cause | Fix |
|---|---|---|
| `missing required configuration: …` | A **REQUIRED** value is unset | See [Configuration](/reference/configuration/) — `git.owner`, `git.repo`, `git.repoURL`, `git.existingSecret`, `llm.provider`, `llm.model` |
| `ALLOW_PATHS is empty: the agent could never apply any fix` | `triage.allowPaths: []` | Set it to the tree the agent may write in, e.g. `[addons/**]` |
| `LLM_BASE_URL is required for the openai provider` | `llm.provider: openai` with no `baseURL` | Set `llm.baseURL`. This is what makes a self-hosted model work |
| `unknown LLM_PROVIDER "…" (openai or anthropic)` | Typo, or a provider that does not exist | Only `openai` and `anthropic` are implemented |
| `GIT_PROVIDER "…" is not implemented yet` | `gitlab` or `bitbucket` | Those are extension points, not implementations. See [Git providers](/reference/git-providers/) |
| `GATE_MODE "…" is not a mode (cluster or ci)` | Typo | `cluster` or `ci` |
| `SUPERVISE_PIPELINE needs apiserver access` | Supervisor on, but neither live reads nor cluster mode | Set `liveReads.enabled: true`, or `gate.mode: cluster`, or `supervise.enabled: false` |
| `no ArgoCD cluster Secrets in namespace "…"` | Wrong `liveReads.argocdNamespace`, or the RBAC grant is missing | The gate cannot expand a generator against an empty inventory. Point it at the real ArgoCD namespace |
| `secrets is forbidden` | The Role was not created, or `rbac.create: false` | Cluster mode needs get/list on Secrets in the ArgoCD namespace |
| `github app authentication failed` | Wrong `appId`, wrong key, or the App is not installed on the repository | Check `git.app.privateKeyKey` matches the Secret's key |

## The gate never reports on a pull request

**Check the obvious one first.** In cluster mode the log line on a healthy agent
is:

```
gate: in-cluster, polling for open pull requests every 30s
```

If that line is absent, the gate is not running — go back to the table above.

| Symptom | Cause |
|---|---|
| The check never appears on **fork** pull requests, and the status says `gate.forkPRs` | Working as designed. Cluster mode renders the pull request's helm content *inside your cluster*; whose content that is is an operator's decision. Set `gate.forkPRs: true` or use [`gate.mode: ci`](/gate/ci-adapters/) |
| `no .gitops-gate.yaml at the head revision` | The config is read from the pull request's **head**. A pull request that predates the config, or deletes it, has nothing to render |
| The check appears but is `error` | The gate could not *run* — bad config, an unreachable chart repository. This is deliberately distinct from "this change is bad" and is worth paging on |
| Nothing on pull requests opened before install | Should not happen in cluster mode — the sweep picks them up. It *was* the CI-mode behaviour, which needed a rebase to fire the workflow |

:::caution[An `error` status is not a red gate]
`failure` means the change is blocking. `error` means the gate is broken.
Treating them the same is how a broken gate gets ignored for a week.
:::

## Triage never fires

The gate answers, the pull request is red, and the agent does nothing. This is
the classic day-one trap and it has one common cause.

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
this namespace and port. Missing that half produces a hang, not an error.

## The agent says it ignored the gate's report

The comment names the author it saw. That is `gate.reportAuthor` doing its job:
the gate's verdict is a pull-request comment carrying a marker, and anyone who
can comment can write that marker. A forged report is not a wrong opinion — it
is an instruction wearing the gate's authority.

- **GitHub, gate in Actions** — leave `gate.reportAuthor` empty; the per-host
  default is `github-actions[bot]`.
- **Gitea, or a gate commenting as a bot user or PAT owner** — set it to that
  account explicitly. Gitea Actions has no fixed identity the chart could
  default to.
- **You genuinely cannot name one account** — `"*"` reads the report whoever
  wrote it. Make it a decision in your values file rather than an absence.

## The model returns nothing, or nonsense

### Empty responses from a reasoning model

Verified against LM Studio serving `qwen3.6-35b-a3b`: the schema-constrained
JSON arrived in `message.reasoning_content` with `message.content` **empty**. A
client reading only `content` sees nothing and reports a broken model.

The `openai` implementation already handles this — it tries `content`, then
`reasoning_content`, then `reasoning`. If you wrote your own provider, do the
same; this is how most llama.cpp-derived servers behave with a reasoning model.

### It classifies fine but nothing gets fixed

Expected, and safe. A `mechanical` verdict that applies zero edits **escalates**
rather than reporting success. The comment lists every refused edit with its
reason. Common reasons:

| Refusal | Meaning |
|---|---|
| `from` did not match | The model paraphrased the current value instead of copying it |
| Not corroborated | A version-shaped value that does not appear verbatim in the evidence. This one is doing exactly its job — see [ADR 0005](/decisions/0005-testimony-is-not-evidence/) |
| Outside scope | The fix lives in a file the promotion never touched |
| Denied path | It tried to edit CI config, the gate, or the merge policy |

### Choose a different model

Score in this order: **unsafe actions must be zero** — anything above zero
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
| Wedged promotion | `kargo.akuity.io/abort=true` — **silently ignored**, because the value is parsed as a request object | `kargo.akuity.io/abort={"action":"terminate"}` |
| Wedged promotion | A **Warehouse refresh**. It re-discovers artifacts; freight carrying a terminal promotion is never auto-promoted again | Abort, then re-promote |
| Failed verification | Fixing the cause. The verification is over; the Stage stays stuck | `kargo.akuity.io/reverify={"id":"…"}`, id from `status.freightHistory[0].verificationHistory[0].id` |
| Hand-written Promotion rejected | A `generateName` with a trailing dot — the webhook validates it as RFC1123 | Drop the trailing dot |

See [The pipeline supervisor](/concepts/supervisor/), and add the
`PipelineSupervisorSilent` alert — a supervisor whose entire subject is silent
failure has to be able to fail loudly itself.

## Commits are attributed to the wrong person

A token belongs to whoever minted it, so comments and commits arrive under that
person's name and avatar.

- **Set `git.app.appId`** and give it a private key. The App comments as
  `yourapp[bot]`, with its own avatar, and its tokens expire hourly.
- **Leave `git.author.name` and `git.author.email` empty.** Empty means derived,
  which as an App is its own bot identity.
- **Never set an `@users.noreply.github.com` address that is not your bot's
  own.** That namespace belongs to GitHub accounts: every commit the first live
  repair pushed was attributed, avatar and all, to an unrelated account named
  `bosun`.

## Pushes do not re-trigger the gate

CI-mode only. Most hosts suppress workflow triggers for pushes made with the CI
system's own token — so if the agent pushes with that token, the gate never
re-runs, the status stays red at its previous conclusion, and the promotion
waits on a result that will never change.

Use a separate credential for the agent. Cluster mode does not have this
problem: a pushed fix is a new head commit, and the sweep gates it because it is
there.

## Still stuck

- [Configuration](/reference/configuration/) — every value and what it does
- [Onboarding](/start/onboarding/) — the six steps, each with a verification
- [The proving ground](/project/proving-ground/) — reproduce it on a disposable cluster
- [Open an issue](https://github.com/JamesAtIntegratnIO/bosun/issues)
