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

**Fleet**:
Every Application an install's ArgoCD serves, and the cluster each lands on,
as one live reading saw them. Read on every gate run to decide what to render
and retained whole, so it is broader than what the gate renders: an
Application of a repository this install does not gate is in the fleet. What
each one renders from is not in it, and comes from the render expansion.
_Avoid_: inventory (that is the cluster set the gate expands generators
against, which is a different list with a different reader), cluster, estate

**Render expansion**:
The Applications a gate run rendered the gated repository into, at the
revision that run started from, each with the chart, chart repository, pinned
version, source type and ApplicationSet behind it. Its own clock and its own
reach: it describes a git revision rather than a control plane, and it covers
what the repository defines rather than what ArgoCD serves. Joined onto the
fleet by name and cluster, and never a source of rows.
_Avoid_: render (that is the act), expansion alone (an ApplicationSet expands
against clusters, which is one step inside this), manifest set

**Live reading**:
One read of what ArgoCD serves, made by a gate run rather than by a sweep. Its
own clock: an install with no open pull request renders nothing and therefore
reads nothing, so a fleet can be older than the sweep that publishes it, and
every row says when it was observed.
_Avoid_: snapshot (overloaded: every read surface serves one), scan, poll

**Honest absence**:
The rule that "nothing found" must be unrepresentable unless something looked.
A sweep that examined nothing publishes no zeroes, a surface answers 503 before
the first sweep, and no filtered view may miscount what it hides.
