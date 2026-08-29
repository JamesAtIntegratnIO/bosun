# Mechanical or escalate

The judgement the agent makes, with the worked examples the eval cases encode.

## Mechanical

The rendered diff **proves** the cause, and the fix is changing values that
already exist in the repository.

**A chart default flipped.** `argo-cd` 10.0.0 turns
`global.networkPolicy.create` on; this repository owns NetworkPolicies
elsewhere. Fix: pin it back to `false`. One scalar.

**Coupled pins.** `nginx-gateway-fabric` 2.6.7 requires Gateway API v1.5, and
the CRD addon is pinned at v1.4.0. Fix: move the CRD pin, provided the exact
target version appears in the evidence. If it does not, this becomes an
escalation, and the applier refuses the edit even if the model tries.

## Repaired before a model is asked anything

This path runs before the model is called at all, so it is not a
classification. It is listed here because it handles a case that otherwise
reads like an escalation.

**A CRD stopped serving a version manifests here still declare.**
external-secrets 2.x stops serving `v1beta1` while 39 manifests still declare
it. The gate's report names the consumer kind, the dropped versions and the
version that survives, so the rewrite takes no judgement: every declaring
manifest gets its `apiVersion` value changed, and the re-run gate re-counts the
consumers to confirm the repair.

Where the swap alone is not the whole job, because a field the target schema
would silently prune, one document at a time is sent to a model, which returns
the complete migrated document. The proposal is refused whole unless it keeps
the object's identity, fits the target schema, and contains no value that is
not either at that same path in the original, displaced by the schema change,
or dictated by the schema itself. A refusal escalates; it never half-applies.
See [safety-model.md](safety-model.md) and
[ADR 0007](../adr/0007-structure-from-the-schema-data-from-the-document.md).

## Escalated before a model is asked anything

Also not a classification, and also decided from the gate's own counts.

**A blocking finding with no repository-side remedy.** The chart renders an
object whose apiVersion moved, and nothing in this repository declares it. The
gate is right to block, since the move is real and somebody should see it, but
there is no manifest to migrate and no value to change, so there is nothing to
classify. The escalation is written from the blocker counts, with no model
call.

## Escalate

**An apiVersion change in the rendered output.** The chart now emits an object
under a different apiVersion: a migration wearing a version number. This
differs from the repaired case above, where the repository's own manifests
declare a version the CRD dropped and the gate names the destination. Here the
change is in what the chart *produces*, and nothing names what it should
become.

**A document migration the harness refused.** The reshape was attempted and did
not survive its checks. The comment carries what was lost, and why.

**Removed CRDs or dropped subcharts.** kyverno 3.9.0 drops the cleanup
subcharts several values keys point at, with seven minors of generate-rule
change behind a `failurePolicy: Fail` webhook.

**A fix that lives outside the files the promotion touched.** MetalLB 0.16.0
moves metrics from 7472 to 9120 while a NetworkPolicy still names the old port,
and scraping stops without an error anywhere. The fix is one number, in a file
the bump never opened, and the pull request's own diff is what bounds the
agent.

Two things make this an escalation rather than a mechanical fix, and neither
is about the model:

- The MetalLB target rewrites `metallb.defaultVersion` and nothing else, so the
  NetworkPolicy is never in the branch's diff. An eval fixture that lists the
  NetworkPolicy grants an authority the live pipeline does not, and passes for
  a reason that cannot reproduce. The suite has no git repository to diff, so
  it sets `Scope` from the fixture's own file list; in the cluster the applier
  reads the diff itself and a promotion body claiming more is ignored.
- The value is a **port**, and corroboration only covers version shapes. An
  invented port would be written. This is the one edit with neither guardrail.

So it escalates, named precisely and with the port in the comment, for a human
to apply in one keystroke.

**A change the bump cannot have caused.** external-secrets 0.11.0 arrives with
the addon's destination namespace moved from `external-secrets` to
`external-secrets-system`. A chart version does not move a namespace, an ArgoCD
project, a source repository, or which clusters an Application targets. Nobody
explained the move, so nobody can say it was meant.

The accommodating answer is available and tidy, which is what makes it
dangerous: the repository still pins a OnePassword token SecretRef to the old
namespace, and updating it makes everything agree. The agent did exactly that
on gitops_homelab_2_0 #122, one scalar, in scope, correct `from`, every guard
satisfied, and it was wrong. It entrenched an unexplained change and spent the
attempt a human needed, and the gate stayed red because the namespace was still
moved.

A mechanical fix restores what the repository already intended. It never
ratifies a change the promotion did not intend.

**One-way migrations.** authentik refuses to migrate across major.minor
releases in one step: `ensure_allowed_version()` raises before
`run_migrations()`, so the pods take the database lock, refuse, and crashloop
while the old ones keep serving.

**An unstated version.** "requires Gateway API v1.5" does not say whether to
write v1.5.0, v1.5.1 or v1.5.4.

**Anything uncertain.** A wrong escalation costs two minutes. A wrong
mechanical fix passes the render, passes the gate, and fails when the apiserver
sees it, which is the failure this system exists to prevent.

## No action

The gate is red for something this pull request did not cause, a pre-existing
failure in an untouched addon for instance. Saying so is useful; changing
something is not.

## Calibration

Deliberately biased toward escalation. The cost is asymmetric, and the agent is
not the last line of defence: the gate still has to go green and the merge
policy still applies.

The eval measures this. See [prompt-contract.md](prompt-contract.md).
