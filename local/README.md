# Local proving ground

A disposable cluster where the whole delivery flow runs end to end: a chart
version is discovered, promoted, written onto a branch, opened as a pull
request, gated, triaged by the agent, merged, reconciled, verified, and
observed.

It exists so the flow can be exercised outside production. Without it the
gate, promotion chaining and agent triage are only ever tested one merge at a
time, against a live repository.

## What it builds

| Piece | What runs it |
|---|---|
| kind cluster, ArgoCD, Gitea, ingress | [idpbuilder](https://cnoe.io/docs/reference-implementation/local) |
| cert-manager, Argo Rollouts, Prometheus, Grafana, Kargo | **ArgoCD**, from `sample-repo/platform/` |
| bosun | its **image built from this working tree**, its chart installed from `../charts/bosun` |
| kargo-pipelines | helm, from `../charts/kargo-pipelines` in **this working tree** |
| the repository under test | `sample-repo/`, pushed into Gitea |

Everything of this repository's own comes **from your working tree**, not from
a release — the agent image is built locally and force-rolled, the charts
install from the checkout. A proving ground that tests the last published
version is testing the past, and this one exists precisely to prove the change
you have not shipped yet.

**The platform is reconciled, not installed.** Installing it with
`helm upgrade --install` would be shorter, but this project's pattern is
app-of-apps, and a proving ground that installs its own platform by hand is not
proving the pattern it exists to demonstrate. `helm list -A` should show exactly
two releases, and both are there only because they are built from your checkout
and there is no git ref for ArgoCD to point at:

```
bosun            bosun   bosun-0.16.0
kargo-pipelines  kargo   kargo-pipelines-0.1.2
```

Two things shape `platform/`:

- **`platform/`, not `apps/`.** The gate's sources are `apps/*.yaml`, so a demo
  pull request renders podinfo and not a fifty-object monitoring chart at two
  versions. It also keeps the real cert-manager separate from the
  `apps/cert-manager.yaml` the structural demo writes at v1.5.5 — a 2021 chart
  it needs the gate to *render*, never to install. One line in
  `.gitops-gate.yaml` if you ever want the platform gated too.
- **Sync waves order applies; they do not defer validation.** The ArgoCD
  ServiceMonitor started life as a manifest beside these Applications, in a
  later wave than the chart that installs its CRD. ArgoCD validates every task
  in an operation *before the first wave runs*, so it was not applied late — it
  was an unknown kind that invalidated the whole sync, and not one child
  Application was created. The root said only `one or more synchronization
  tasks are not valid`. It is a value of the monitoring chart now
  (`prometheus.additionalServiceMonitors`), created by the chart that owns its
  own CRD, which removes the ordering question instead of sequencing around it.

## What the agent is installed with

Everything the chart ships, on. Installing the agent with its cautious
defaults — no live reads, no report-author trust, no egress past the git host
and the model — would make this a proving ground for a configuration nobody
runs.

| Setting | Here | Why it is not a default |
|---|---|---|
| `liveReads.enabled` | on, `groups` scope | "everything except the core group" is not expressible in Kubernetes RBAC, so the API groups this cluster ships CRDs for are named. Secrets stay unreadable. |
| `networkPolicy.egress.apiServer` | discovered | read from the `kubernetes` Service's own endpoints. A ClusterIP is DNAT'd before policy evaluation, so an ipBlock naming it matches nothing. |
| `gate.argocd` | an ArgoCD account minted here | The gate reads its inventory from ArgoCD's API, and the account gets `clusters, get` and nothing else — the same three steps the chart README asks an operator for. |
| `networkPolicy.egress.allowPublicHTTPS` | on | the upstream lookup has to reach a registry at all. |
| `triage.egressDeny` | one host | so the refusal path is exercised rather than described. |

**The NetworkPolicy is enforced here.** kindnet in this cluster implements
NetworkPolicy — measured, not assumed: a busybox pod reaches `1.1.1.1` with no
policy and hangs under a `deny-all`. So these rules are load-bearing, and a
wrong apiserver endpoint produces a crash loop that names its cause rather than
a silent hang.

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

A ninth step — asserting that every `kargo_*` metric **returns rows** rather
than merely parsing — lives with `kargo-observability`, which is not part of
this repository and shares no contract with the gate or the agent. The
assertion is worth copying if you run that component: an alert expression can
parse against a live Prometheus and match nothing for hours, because
kube-state-metrics prefixes custom-resource metrics unless told not to.

## The scenarios

```bash
make demo              # a green gate, promoted and merged
make demo-cluster-gate # the gate with no CI anywhere: renders, blocks, re-gates
make demo-triage       # a red gate the agent refuses to fix, and says why
make demo-structural   # a red gate the swap alone cannot fix
make demo-egress       # a host the agent is told not to visit
```

No act runs a gate. Every one of them changes the sample repository, opens a
pull request and waits for the **agent's own** verdict — the status and the
report comment it publishes from its sweep. `make demo-cluster-gate` is the
one that asserts that directly, on three properties a CI workflow could not
have by construction: a comment-only change answered by a render rather than a
paths guess, the report posted by the agent itself, and a pushed fix re-gated
because the commit exists rather than because a token was minted right.

`make demo-structural` is the one that needs the whole stack at once. It pins
cert-manager `v1.5.5` with a `cert-manager.io/v1alpha2` Certificate that has
been correct for years, bumps to `v1.6.0` — which stops serving `v1alpha2`,
`v1alpha3` and `v1beta1` — and lets the agent repair it.

Swapping the `apiVersion` line alone leaves a document that parses, applies,
and has six fields pruned by the apiserver on the way in. The render is fine.
The gate is green. The certificate has quietly lost its key algorithm, size and
encoding, its email SANs, its URI SANs and its subject organization. So the
model is shown the old schema (from the CustomResourceDefinition the cluster
serves **right now** — after the merge it is gone) and the new one (by rendering
the chart at the target version), and asked to translate. Every proposal is then
checked for identity, schema-validity and value provenance before a byte is
written.

## Where this is a stand-in rather than the real thing

**The ArgoCD token is minted with `insecureSkipTLSVerify`.** idpbuilder's
argocd-server serves a certificate signed by a CA that exists nowhere a pod can
reach, so the agent is told to accept it. A real install gives the chart
`gate.argocd.caSecret` instead. Everything else about that path — the account,
its one RBAC line, the API it reads — is what an operator does.

**The recorded incidents are not replayed here.** They were, through a verdict
fed to the agent as a comment; the agent gates in-process now and reads no such
comment, so there is nothing to feed. The fixtures are still scored by the eval
suite in [`../evals`](../evals), against the same prompts.

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
- **The demo was running a gate binary from before the feature it proved.** The
  script that ran the gate built the binary only when `/tmp/gitops-gate` did not
  exist. The one sitting there predated `objectFrom` carrying the rendered body
  by eight hours, so chart-diff produced body-less objects and the CRD-version
  detection could not fire. Nothing errored: the gate rendered both versions,
  diffed them and reported ten objects "changed" with no fields, which is
  indistinguishable from a gate that looked and found nothing. The same trap is
  live for the agent image, which is why the kit forces a rollout restart.
- **A wait loop that read the previous run's verdict.** `tail -n +$BEFORE`
  starts *at* line `$BEFORE`, and the last line of a previous run is reliably
  its own `triage done`. The triage demo declared "it pushed nothing" about a
  pull request the agent escalated correctly twenty seconds later.
- **A diff that hid the value it preserved.** The reshape comment's diff was a
  set difference on line text, so a value that moves without changing column was
  printed on neither side. `organization: [Example Platform Team]` becoming
  `subject.organizations: [Example Platform Team]` rendered as the key being
  deleted into an empty field, above a "Values not carried across" line. It is a
  real diff with context now.
- **kindnet enforces NetworkPolicy.** Worth knowing before you assume a local
  cluster cannot test egress rules: it can, and this one does.
- **kube-state-metrics reads its config once, at startup.** Changing the
  ConfigMap changes nothing until it restarts.
- **Verification silently requires Prometheus to scrape ArgoCD.** The
  AnalysisTemplate queries `argocd_app_info`; idpbuilder ships ArgoCD's metrics
  Services but no ServiceMonitor, so every AnalysisRun failed with an empty
  message. `count(argocd_app_info)` went 0 -> 6 once one existed, and the
  verification query started returning 1.

## `make demo-egress`

This covers the half of "egress is open, logged and deniable" that a working
deployment never shows you: a deny rule only proves itself by stopping
something that otherwise works. It forbids `*.docker.io`, opens a pull request
the agent will escalate — the escalate path reaches for upstream notes, and
reaching for them starts by asking the registry who publishes the artifact —
and asserts two things:

```
outbound REFUSED auth.docker.io (egress deny rule "*.docker.io")
outbound REFUSED registry-1.docker.io (egress deny rule "*.docker.io")
PR 88: escalated: unexplained namespace move
```

that the refusal **names the rule that caused it**, and that the triage still
**reached a verdict without what it could not read**. A blocked host must
shorten the brief, not end the run. It changes the running deployment and puts
it back, including on failure, and verifies the restore against the deployment's
own spec rather than a log line — during a rollout there are two Running pods
and `logs deploy/...` picks one of them.

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
| ...and the fields moved too | yes | **the model writes the migration, three checks decide whether it lands** |
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
