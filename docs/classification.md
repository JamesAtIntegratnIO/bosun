# Mechanical or escalate

The judgement the agent makes, with the worked examples the eval cases encode.

## Mechanical

The rendered diff **proves** the cause, and the fix is changing values that
already exist in the repository.

**A chart default flipped.** `argo-cd` 10.0.0 turns
`global.networkPolicy.create` on; this repository owns NetworkPolicies
elsewhere. Fix: pin it back to `false`. One scalar.

**Coupled pins.** `nginx-gateway-fabric` 2.6.7 requires Gateway API v1.5, and
the CRD addon is pinned at v1.4.0. Fix: move the CRD pin — *provided the exact
target version appears in the evidence*. If it does not, this becomes an
escalation, and the applier refuses the edit even if the model tries.


## Escalate

**Any apiVersion change.** external-secrets 2.x stops serving `v1beta1` while
39 manifests still declare it. Rewriting those is a migration.

**Removed CRDs or dropped subcharts.** kyverno 3.9.0 drops the cleanup
subcharts several values keys point at, with seven minors of generate-rule
change behind a `failurePolicy: Fail` webhook.

**A fix that lives outside the files the promotion touched.** MetalLB 0.16.0
moves metrics from 7472 to 9120 while a NetworkPolicy still names the old
port, and scraping stops silently. The fix is one number — but it is in a file
the bump never opened, and the promotion's own file list is what bounds the
agent.

This was classified mechanical until 2026-08-23. Two things argue against it,
and neither is about the model:

- The MetalLB target rewrites `metallb.defaultVersion` and nothing else, so
  the NetworkPolicy is never in the promotion's file list. The old eval
  fixture listed only the NetworkPolicy, which granted an authority the live
  pipeline does not — it passed for a reason that could not reproduce.
- The value is a **port**, and corroboration only covers version shapes. An
  invented port would have been written. This is the one edit with neither
  guardrail, and the quietest failure mode of any of them.

So it escalates: named precisely, with the port in the comment, for a human to
apply in one keystroke. Nothing is lost except the pretence that it was safe.

**A change the bump cannot have caused.** external-secrets 0.11.0 arrives with
the addon's destination namespace moved from `external-secrets` to
`external-secrets-system`. A chart version does not move a namespace, an ArgoCD
project, a source repository, or which clusters an Application targets. Nobody
explained the move, so nobody can say it was meant.

The trap here is that the accommodating answer is *available and tidy*: the
repository still pins a OnePassword token SecretRef to the old namespace, and
updating it makes everything agree. That is what the agent did on
gitops_homelab_2_0 #122 — one scalar, in scope, correct `from`, every guard
satisfied — and it was wrong. It entrenched an unexplained change and spent the
attempt a human needed. The gate stayed red, because the namespace was still
moved.

A mechanical fix restores what the repository already intended. It never
ratifies a change the promotion did not intend.

**One-way migrations.** authentik refuses to migrate across major.minor
releases in one step — `ensure_allowed_version()` raises before
`run_migrations()`, so the pods take the database lock, refuse, and crashloop
while the old ones keep serving.

**An unstated version.** "requires Gateway API v1.5" does not say whether to
write v1.5.0, v1.5.1 or v1.5.4.

**Anything uncertain.** A wrong escalation costs two minutes. A wrong
mechanical fix renders perfectly and breaks at runtime, which is the failure
this system exists to prevent.

## No action

The gate is red for something this pull request did not cause — a pre-existing
failure in an untouched addon, for instance. Saying so is useful; changing
something is not.

## Calibration

Deliberately biased toward escalation. The cost is asymmetric and the agent is
not the last line of defence — the gate still has to go green and the merge
policy still applies.

The eval measures this. See [prompt-contract.md](prompt-contract.md).
