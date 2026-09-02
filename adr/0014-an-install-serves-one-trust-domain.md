# 14. An install serves one trust domain: its intake sets the horizon

- **Status:** proposed
- **Date:** 2026-08-30

## Context

The MCP server is bosun's first authenticated read surface built for
programmatic callers, and designing it forced a question: should bosun follow
Kargo and ArgoCD into per-project, per-user visibility, filtering what a caller
sees by which projects, applications, or repositories they hold permissions on?
The question is real. One deployment target is an organization whose divisions
serve separate customers and cannot see each other's operations. This record
answers it for every read surface bosun has, and not for MCP alone.

Three facts settle it.

**A verdict has no seams narrower than its repository.** The gate's unit is the
pull request, and a pull request does not respect project boundaries: one edit
to a shared values file reshapes an ApplicationSet that renders into many Kargo
projects on many clusters. A per-project filter over that verdict has three
options and each one breaks something. Show the whole verdict on any overlap,
and the filter leaks whatever a shared component touches, which in a gitops
repository is most things. Redact the other projects' findings, and the
blockers line miscounts what the visible findings show, violating the "honest
absence" rule by design. Hide cross-project verdicts, and you withhold the pull
requests a team most needs. A repository partitions cleanly where a project
does not: a pull request belongs to one repository.

**An install is already bound one-to-one to its world.** `GIT_OWNER`/`GIT_REPO`
are singular, `ARGOCD_BASE_URL` and its token are singular, and ADR 0009 is
titled "one gate, one inventory". Bosun has no user concept anywhere; the only
identities in the process are its own service credentials. Per-caller filtering
would import users, sessions, and a second RBAC into a system whose
architecture expresses isolation a different way.

**The permission systems worth mirroring already exist and already enforce.**
An ArgoCD account token can be project-scoped; a Kargo read can be bound
per-namespace. Bosun sees whatever an operator's RBAC lets its credentials see,
filtered at the source of truth, by the systems whose job that is, audited
where their administrators already look.

## Decision

**An install serves one trust domain: the set of parties entitled to its whole
view.** Crossing a trust domain means another install, on shared or separate
infrastructure alike.

**An install's intake sets its horizon, and its readout follows.** The
repository binding and the RBAC granted to its inbound credentials, the ArgoCD
token and the Kargo RoleBindings, define what an install can see. Every read
surface (MCP, the status page, `/pipeline`, `/metrics`) serves that view flat
and whole. Divisions on shared infrastructure each run an install whose ArgoCD
token and Kargo bindings are scoped to their slice.

The hierarchical fleet is the same rule composed. A management control plane
whose Argo/Kargo deploys the cluster-role and environment apps across the fleet
runs an install whose trust domain is the platform team and whose horizon is
those apps on every cluster. Each child cluster running its own Argo/Kargo for
its app teams runs its own install, bound to that plane and serving those
teams. One install per Argo/Kargo control plane, which ADR 0009 already forces,
since a child cluster's Argo is its own inventory. The parent-child
relationship stays in the infrastructure: no bosun models it, and no bosun
answers for another. The operational corollary is token custody. An app team's
consumers hold their cluster's install token and not the management install's,
and what the platform layer did on their cluster is a question for the platform
team's surfaces.

**The repository is the one scope key bosun honors**, because it is the one key
its core artifact partitions by. In anticipation of one install watching
several repositories of one trust domain, MCP result schemas carry `repo` from
the first release, and per-PR tool arguments accept an optional `repo`
qualifier defaulting to the install's only one. A field is cheap now; a
breaking argument change later is not. If scoped credentials ever become worth
their weight, the shape is a `repos` claim on the verifier-rung JWT, and that
does not move the auth ladder's timing: within any one install, every caller
holds the same single privilege.

## Rejected, each with the scenario behind it

- **Per-project filtering.** The shared-values-file scenario above. No answer
  exists that is at once useful, honest, and tight.
- **Per-user identity with a mirrored ACL.** Mirroring the git host's or
  Kargo's permissions means making authorization decisions from a cache of
  someone else's RBAC: a second policy store that drifts, checked at read time
  against a truth that moved. Kargo can do project RBAC because its projects
  are namespaces and Kubernetes itself enforces them. Bosun's derived artifacts
  live in process memory, where no such substrate exists.
- **One filtered install spanning trust domains.** A single process holding
  every division's credentials behind a result filter multiplies the T1 blast
  radius by the number of domains. One redaction bug or one confused deputy
  erases the boundary between customers.
- **An aggregator over installs.** A parent that fans out to every install
  holds every domain's token and re-answers a question MCP clients already
  answer by listing N servers. It also adds a second place for honest absence
  to break, because an aggregator must tell "division down" from "division
  unqueried" and nothing checks that it does.
- **A shared repository across trust domains.** No read-side control, bosun's
  or anyone's, satisfies the isolation requirement when one pull request spans
  both domains. The remedy is a repository split, and this record exists in
  part so that engagement conversation starts from the split instead of from a
  filter that cannot work.

## Consequences

- The MCP plan's flat trust model stands for every install, and the static
  token → gateway SSO → JWT-verifier ladder keeps its timing.
- Packaging takes the pressure: an organization running several installs needs
  them cheap and legible, which means per-domain chart values and a topology
  page instead of new auth code.
- The status page needs no change. It already serves an install's whole view,
  which this record makes the contract instead of a coincidence.
