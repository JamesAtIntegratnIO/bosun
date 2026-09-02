# Bosun

The crew for Argo and Kargo: a gate that renders what a pull request deploys
and an agent that repairs, explains, or escalates what the gate finds. This
glossary pins the terms the code and docs use with a specific meaning.

## Language

**Install**:
One deployed bosun agent, meaning one process and one chart release, bound to
one Argo/Kargo control plane and, today, one repository. The unit of
isolation: an install serves one trust domain, and anything entitled to ask it
sees everything it holds.
_Avoid_: instance, tenant, deployment (overloaded with the Kubernetes kind)

**Trust domain**:
The set of parties entitled to an install's whole view. Crossing one means
another install, on shared or separate infrastructure alike. Who holds the
install's token defines it, and cluster or namespace lines do not.
_Avoid_: tenant, org, team (a trust domain may span teams, or split an org)

**Horizon**:
The reach of an install: its repository binding and the RBAC granted to its
inbound credentials set it, and filtering its outputs does not. "The intake
sets the horizon."
_Avoid_: scope (overloaded), visibility, view

**Repository**:
The one gitops repository an install watches and writes to. The clean
partition key for everything bosun produces: a pull request, and therefore a
verdict, belongs to one.
_Avoid_: project (a Kargo Project is a namespace; an ArgoCD AppProject is a
policy object; neither is this)

**Verdict**:
The gate's claim about one pull request against the whole fleet render. It has
no seams narrower than its repository: it cannot be filtered by Kargo project,
ArgoCD AppProject, or caller without misstating its own blocker counts.
_Avoid_: report (the report is the published rendering of a verdict), result

**Operational metadata**:
The facts bosun's read surfaces reveal: Stage and Application names, chart
versions, findings, remedies, pull-request titles. Org-internal data, the same
class a cluster-wide read of Applications exposes, and not a secret between
teams of the same org. A credential is not operational metadata.
_Avoid_: telemetry, sensitive data

**Honest absence**:
The rule that "nothing found" must be unrepresentable unless something looked.
A sweep that examined nothing publishes no zeroes, a surface answers 503 before
the first sweep, and no filtered view may miscount what it hides.
