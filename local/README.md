# Local proving ground

A disposable cluster where the whole delivery flow runs end to end: a chart
version is discovered, promoted, written onto a branch, opened as a pull
request, gated, triaged by the agent, merged, reconciled, verified, and
observed.

It exists because everything in this package was previously only ever exercised
in production, one merge at a time. The gate had never seen a real pull request.
No promotion had traversed a chain. The agent had never triaged anything.

## What it builds

| Piece | What runs it |
|---|---|
| kind cluster, ArgoCD, Gitea, ingress | [idpbuilder](https://cnoe.io/docs/reference-implementation/local) |
| cert-manager, Argo Rollouts, Prometheus, Grafana, Kargo | helm |
| bosun | its **image built from this working tree**, its chart installed from `../charts/bosun` |
| kargo-pipelines | helm, from `../charts/kargo-pipelines` in **this working tree** |
| the repository under test | `sample-repo/`, pushed into Gitea |

Everything of this repository's own comes **from your working tree**, not from
a release — the agent image is built locally and force-rolled, the charts
install from the checkout. A proving ground that tests the last published
version is testing the past, and this one exists precisely to prove the change
you have not shipped yet.

## Requirements

- macOS or Linux, ~10 GB free RAM, ~20 GB disk
- Homebrew (the runtime script installs colima, kind and idpbuilder)
- An OpenAI-compatible model endpoint the cluster can reach

```bash
export LLM_BASE_URL=http://<your-host>:1234/v1
make up
make demo
```

`LLM_BASE_URL` has no default on purpose. A demo that silently starts spending
money against a vendor you did not choose is a bad default.

## The flow, and what each step proves

1. **Discovery** — a Warehouse finds a new podinfo chart version
2. **Promotion** — the Stage rewrites the pin and pushes a branch
3. **Pull request** — opened against Gitea. Real PR, real API, not a stand-in
4. **Gate** — renders base and head, diffs the resources, posts a report
   comment and a `gate` commit status
5. **Triage** — the agent reads that comment and decides
6. **Merge**
7. **Reconcile** — ArgoCD syncs podinfo to the new version
8. **Verify** — the AnalysisRun asks Prometheus whether the app is healthy

There used to be a step 9, asserting that every `kargo_*` metric **returned
rows** rather than merely parsing — an assertion a production incident earned,
where every alert expression parsed against a live Prometheus and matched
nothing for hours because kube-state-metrics prefixes custom-resource metrics
unless told not to. It went with `kargo-observability`, which is not part of
this repository: it shares no contract with the gate or the agent.

## Where this is a stand-in rather than the real thing

**The gate runs as a binary, not as CI.** idpbuilder ships no Actions runner, so
[`scripts/gate-run.sh`](scripts/gate-run.sh) invokes the same binary with the
same inputs and produces the same two artifacts a CI adapter would — the report
comment and the commit status. Everything else is the real component.

## Things this turned up

Each of these is a real defect or a real gap, found by running the thing:

- **The agent could not talk to Gitea at all.** `GIT_PROVIDER` accepted only
  `github`. There is a `gitprovider/gitea.go` now.
- **Kargo refuses to send credentials over plain HTTP.** The controller logs
  `refused to get credentials for insecure HTTP endpoint`; the promotion fails
  at `git push` with `could not read Username`, which names nothing. So the
  git host has to be HTTPS, which for a self-hosted instance means a
  certificate — hence `git.insecureSkipTLSVerify` on kargo-pipelines.
- **An in-cluster destination cannot be expressed as an ipBlock.** A ClusterIP
  is DNAT'd to a pod IP before policy evaluation, so the agent's egress rule
  matched nothing and the connection hung with zero bytes. The chart takes
  `networkPolicy.egress.namespaces` now.
- **kube-state-metrics reads its config once, at startup.** Changing the
  ConfigMap changes nothing until it restarts.
- **Verification silently requires Prometheus to scrape ArgoCD.** The
  AnalysisTemplate queries `argocd_app_info`; idpbuilder ships ArgoCD's metrics
  Services but no ServiceMonitor, so every AnalysisRun failed with an empty
  message. `count(argocd_app_info)` went 0 -> 6 once one existed, and the
  verification query started returning 1.

## Seeing it actually fix things

`make scenarios` replays the **ten** recorded incidents from
[`../evals`](../evals) as real pull requests against the live in-cluster
agent, and prints a case-by-case table of what it did against what the case
expects. The gate report each one posts is **recorded** — reproducing
fourteen upstream chart versions locally would prove nothing extra — but the
agent, the model, the reasoning and every commit it pushes are live. The
scenarios read the same fixtures the eval suite scores, which is what stops
the thing the eval measures and the thing you watch from drifting apart.

## What the agent will and will not fix

`make demo-triage` opens a pull request the gate **refuses** -- a bump
carrying a changed destination namespace -- and the agent escalates rather
than fixing it. That is worth understanding before you call it a limitation of
the model. Measured here against both
`qwen/qwen3.5-9b` and `qwen/qwen3.8-27b`, each independently escalated with a
sound argument -- the 27B's was *"the cause is not provable from the rendered
diff alone"*, which is precisely the judgement the prompt asks for.

The deeper reason is structural, and finding it is the most useful thing this
proving ground has done — because answering it reshaped the system:

| | Blocks the merge | What the agent does |
|---|---|---|
| Targeting moved | yes | escalate |
| Source / project / namespace changed | yes | escalate |
| apiVersion migration on an object | yes | escalate |
| A CRD stops serving a declared version | yes, **while consumers remain** | **deterministic repair, no model** |
| A chart default flipped | no, reported only | mechanical fix |
| Coupled pins | no, reported only | mechanical fix |
| Anything a green render cannot reveal | no | explain, and flag when it warrants eyes |

The original finding read: everything the gate blocks on is structural, the
agent escalates structural changes by design, and everything it can fix is a
values conflict the gate reports without blocking — *the two sets barely
intersect, so "gate red, agent fixes it" is close to a null case*. That was
true, and the answer was not to make the model braver. It was to teach the
gate a red the harness can repair: a dropped served version blocks exactly
while manifests still declare it, the report names the destination, and the
repair — rewriting those manifests — is a deterministic function of the
report, verified by the gate's own recount on the re-run. The rows where the
agent escalates are still escalations *by design*: those are the changes no
version bump can cause, and no one should want a model ratifying them.

## Running it a second time

`make demo` **consumes** the Freight it promotes. Run it again as-is and it
fails at step 2 with `a promotion exists (waited 240s)`, because the Stage is
already fulfilled and Kargo has nothing left to promote.

`make seed` alone does not fix that. It force-pushes `sample-repo/` back over
`main` — discarding the merge you just watched land — but leaves Kargo holding
the Freight it already promoted. Both sides have to go back:

```bash
make reset && make demo
```

## Teardown

```bash
make down     # delete the cluster, keep the VM
make clean    # and stop colima
```
