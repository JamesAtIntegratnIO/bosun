# The pipeline supervisor

The gate answers a pull request that exists. The supervisor answers the
question nobody asks: **whether the pull requests that should exist are being
opened at all.**

Kargo does a great deal of work unattended, and its failure mode is silence. A
Warehouse that stopped discovering, a promotion that errored on a transient
blip, a verification that cannot reach Prometheus — none of these produce an
alert, a red check, or an unhealthy Application. The pipeline stops delivering
and every signal anyone watches stays green, because every individual object
really is fine.

That is not a criticism of Kargo. It is the shape of any system whose job is to
make changes that would otherwise not happen: when it stops, what you observe
is the *absence* of an event, and nothing observes absences by default.

## What it found on its first live sweep

Against one cluster, with no tuning:

| Finding | Held for |
|---|---|
| `argo-cd` — verification failed, Stage promoting nothing | 3 days |
| `cert-manager` — verification cannot reach Prometheus | 3 days |
| `open-webui-image` — same | 3 days |

The cause of the last two was a NetworkPolicy: `allow-controller-egress`
permits `0.0.0.0/0:443` minus the RFC1918 ranges, and Prometheus is a
ClusterIP. Every `verify.apps` query had been dropped since the rule was
written. Nothing had noticed, because a failed AnalysisRun does not fail a
promotion — the Stage simply goes `Ready=False` and declines to start the next
one.

## What it looks for

| Kind | What it means |
|---|---|
| `wedged_promotion` | The latest promotion ended without delivering. **It will never retry** — a terminal promotion is final, and auto-promotion does not re-run one, because from the controller's view that freight *has* been promoted; the attempt merely failed. |
| `stalled_warehouse` | Not discovering, or has missed two of its own intervals. No new freight means no promotions and no pull requests, which looks exactly like being up to date. |
| `verification_stuck` | A verification is holding a Stage's queue and not finishing. |
| `dead_pin` | A `yaml-update` key the target file does not have. The step writes nothing, reports success, and the pin looks maintained forever. |
| `promotion_without_pr` | Running against a pull request that is no longer open, holding the queue until it times out. |
| `superseded_pr` | More than one open promotion pull request for a Stage. Only the newest can merge; the rest collect gate runs and crowd the list. |

## The remedy is the point

Recovering from each of these took an hour of reading Kargo's source, and none
of the answers are guessable. So every finding carries the exact command:

- `kargo.akuity.io/abort=true` is **silently ignored**. The value is parsed as
  a request object; a bare `true` is not one. No error, no event, no log line —
  the promotion just keeps running. It must be
  `kargo.akuity.io/abort={"action":"terminate"}`.
- A **Warehouse refresh does not re-run a promotion.** It re-discovers
  artifacts. Freight that already carries a terminal promotion is never
  auto-promoted again, so a refresh on a wedged Stage does nothing at all and
  looks like it worked.
- A hand-written Promotion needs `generateName` **without** a trailing dot. The
  webhook computes Kargo's own name from it and then validates the
  `generateName` itself as RFC1123, which a trailing dot fails.

## Turning it on

```yaml
supervise:
  enabled: true      # the default
  interval: 10m
metrics:
  serviceMonitor:
    enabled: true    # /metrics now serves something
```

It is **read-only** — three LISTs and a shallow clone — and uses the Kargo read
the chart's ClusterRole already grants. There is no new permission.

Two endpoints:

```bash
kubectl -n bosun port-forward deploy/bosun 8080 &
curl -s localhost:8080/pipeline   # the report, as markdown
curl -s localhost:8080/metrics    # findings, ages, and the sweep timestamp
```

Both answer `503` before the first sweep completes, deliberately: a scraper
that read zeroes from a supervisor which has not looked yet would record
"nothing is wrong" as a measurement.

## Alerting

The obvious rule is worth having:

```yaml
- alert: PromotionPipelineBlocked
  expr: sum(bosun_pipeline_findings{severity="blocking"}) > 0
  for: 15m
  annotations:
    summary: "{{ $value }} Stage(s) have stopped promoting"
    description: "curl the agent's /pipeline for the findings and the exact remedy."
```

**This one matters more:**

```yaml
- alert: PipelineSupervisorSilent
  expr: absent(bosun_pipeline_sweep_timestamp_seconds)
        or time() - bosun_pipeline_sweep_timestamp_seconds > 3600
  for: 15m
  annotations:
    summary: "The pipeline supervisor has stopped looking"
    description: "Nothing is reporting on promotion health. Absence of findings is not evidence."
```

A supervisor whose entire subject is silent failure has to be able to fail
loudly itself. Without that second rule, one that stopped sweeping looks
exactly like a pipeline with nothing wrong — which is the joke this component
would rather not be.

`bosun_pipeline_checked{resource="stages"}` is the same guard one level down: a
sweep that read zero Stages found no problems, and must never be read as having
proved anything. Every finding kind is emitted even at zero, so a rule can
compare and a graph can return to the axis.

## What it will not do

It does not create, update, patch or delete anything. The chart's ClusterRole
has no such verb and says that a feature which seems to need one is a signal to
reconsider the feature — and supervising does not need one. Auto-retrying a
wedged promotion would mean write access to Kargo, and an agent that can
re-run promotions unattended is a different, larger trust decision than one
that tells you which command to run.

The remedy in the finding is that decision, made the small way.
