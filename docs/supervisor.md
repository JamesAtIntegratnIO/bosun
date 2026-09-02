# The pipeline supervisor

The gate answers a pull request that exists. The supervisor answers a different
question: **whether the pull requests that should exist are being opened at
all.**

Kargo does a great deal of work unattended, and its failure mode is silence. A
Warehouse that stopped discovering, a promotion that errored on a transient
blip, a verification that cannot reach Prometheus: none of these produce an
alert, a red check, or an unhealthy Application. The pipeline stops delivering
and every signal anyone watches stays green, because every individual object is
fine.

This is not a criticism of Kargo. It is the shape of any system whose job is to
make changes that would otherwise not happen: when it stops, what you observe
is the *absence* of an event, and nothing observes absences by default.

## What it found on its first live sweep

Against one cluster, with no tuning:

| Finding | Held for |
|---|---|
| `argo-cd`: verification failed, Stage promoting nothing | 3 days |
| `cert-manager`: verification cannot reach Prometheus | 3 days |
| `open-webui-image`: same | 3 days |

A NetworkPolicy caused the last two: `allow-controller-egress` permits
`0.0.0.0/0:443` minus the RFC1918 ranges, and Prometheus is a ClusterIP. Every
`verify.apps` query had been dropped since the rule was written. Nothing had
noticed, because a failed AnalysisRun does not fail a promotion. The Stage goes
`Ready=False` and declines to start the next one.

## What it looks for

| Kind | What it means |
|---|---|
| `wedged_promotion` | The latest promotion ended without delivering. **It will never retry.** A terminal promotion is final, and auto-promotion does not re-run one, because from the controller's view that freight *has* been promoted, even though the attempt failed. |
| `stalled_warehouse` | Not discovering, or has missed two of its own intervals. No new freight means no promotions and no pull requests, which a dashboard cannot distinguish from being up to date. |
| `verification_stuck` | A verification is holding a Stage's queue. If it already **failed**, it is over. Kargo does not re-run it, so the Stage is stuck permanently. |
| `dead_pin` | A `yaml-update` key the target file does not have. The step writes nothing, reports success, and the pin looks maintained forever. |
| `promotion_without_pr` | Running against a pull request that is no longer open, holding the queue until it times out. |
| `superseded_pr` | More than one open promotion pull request for a Stage. Only the newest can merge; the rest collect gate runs and crowd the list. |

## Every finding carries its remedy

None of these recoveries are guessable from Kargo's API, and each took an hour
of reading its source to establish. So every finding carries the exact command,
and the non-obvious behaviours behind them are:

- `kargo.akuity.io/abort=true` is **silently ignored**. The value is parsed as
  a request object, and a bare `true` is not one. No error, no event, no log
  line; the promotion keeps running. It must be
  `kargo.akuity.io/abort={"action":"terminate"}`.
- A **Warehouse refresh does not re-run a promotion.** It re-discovers
  artifacts. Freight that already carries a terminal promotion is never
  auto-promoted again, so a refresh on a wedged Stage does nothing at all and
  looks like it worked.
- A hand-written Promotion needs `generateName` **without** a trailing dot. The
  webhook computes Kargo's own name from it and then validates the
  `generateName` itself as RFC1123, which a trailing dot fails.
- **Fixing the cause of a failed verification does not restart it.** The
  verification is over; the Stage stays stuck until something asks again with
  `kargo.akuity.io/reverify={"id":"…"}`. The id lives at
  `status.freightHistory[0].verificationHistory[0].id`. Proved by fixing the
  NetworkPolicy above and watching all three Stages not move.

## Turning it on

```yaml
supervise:
  enabled: true      # the default
  interval: 10m
metrics:
  serviceMonitor:
    enabled: true    # /metrics now serves something
```

It is **read-only**, three LISTs and a shallow clone, and it uses the Kargo
read the chart's ClusterRole already grants. There is no new permission.

Two endpoints:

```bash
kubectl -n bosun port-forward deploy/bosun 8080 &
curl -s localhost:8080/pipeline   # the report, as markdown
curl -s localhost:8080/metrics    # findings, ages, and the sweep timestamp
```

Both answer `503` before the first sweep completes, deliberately: a scraper
that read zeroes from a supervisor which has not looked yet would record
"nothing is wrong" as a measurement.

## Reading it without curl

The same report renders itself as a web page, on a port of its own:

```bash
kubectl -n bosun port-forward deploy/bosun 8081 &
open http://localhost:8081
```

Markdown in a browser tab is source code, and the people who most need this
report, whoever is wondering why an addon has not updated in three days, are
exactly the people who will not port-forward and pipe `curl` through a
renderer. The page carries every finding with its remedy, plus the gate's open
pull requests and what the triage is doing right now. `/pipeline` on that port
serves the page to a browser and the markdown to everything else, so no
existing script changes.

The 503 rule survives the move, on the formats where it matters: a machine
asking before the first sweep still gets 503, and a human gets a page that says
"no sweep has completed yet" in words.

The page is read-only, has no authentication of its own, and reveals
operational state, Stage names, pull request titles, findings, and no
credentials. The chart can publish it through a Gateway API HTTPRoute or an
Ingress; see the chart README.

## Reading it from an agent

The same findings again, typed, for the reader who is not a person. An on-call
engineer's coding agent asks `pipeline_report` over MCP and gets each finding
as fields it can branch on: the kind, the severity, the subject, the evidence
with its numbers, how long it has held, and where one exists the paste-ready
command. Worst first, and as fields instead of the markdown above, which the
agent would have to parse.

```yaml
mcp:
  enabled: true
  existingSecret: bosun-mcp
```

The 503 rule survives this move too, in the shape JSON can carry it: before the
first sweep the `findings` field is **absent** instead of empty, and the result
says in words that nothing has looked. A reader would take an empty list for
"nothing is wrong", which is the measurement this whole package exists to stop
anything recording.

Alongside the findings the sweep gives its own accounting, so a report with no
findings can prove it looked; `clean` is true only when it did. It costs no API
call anywhere, because it answers from the same snapshot the page renders.

The other tools answer the questions either side of a finding: `gate_status`,
`gate_verdict`, `triage_status`, `handoff_queue` and `verdict_history`. Like
the page, the surface is read-only and reveals operational metadata and no
credential. It is off by default and refuses to start without a bearer token,
because somebody built it to be reached from outside the cluster.
[The MCP surface](mcp.md) covers the tools, the token, and what publishing it
discloses.

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

A supervisor whose subject is silent failure has to fail loudly itself. Without
that second rule, a supervisor that has stopped sweeping publishes the same
zero as a pipeline with nothing wrong.

`bosun_pipeline_checked{resource="stages"}` is the same guard one level down: a
sweep that read zero Stages found no problems, and must never be read as having
proved anything. Every finding kind is emitted even at zero, so a rule can
compare and a graph can return to the axis.

## What it will not do

It does not create, update, patch or delete anything. The chart's ClusterRole
has no such verb, and a feature that seems to need one is a signal to
reconsider the feature; supervising does not need one.

Auto-retrying a wedged promotion would mean write access to Kargo. An agent
that re-runs promotions unattended is a larger trust decision than one that
reports which command to run, and the remedy in each finding is what makes the
smaller decision sufficient.
