# 6. Live cluster reads, and why "everything except Secrets" cannot be written down

- **Status:** accepted
- **Date:** 2026-08-24

## Context

[ADR 0002](0002-triage-in-cluster-not-ci.md) put triage in the cluster rather
than in CI, and the argument was structural: a CI job can read a repository and
a pull request, and it cannot read what is running. Everything the gate knows is
therefore a property of text. *"Three manifests still declare a version this
chart stops serving"* is a fact about the repository and the only kind of fact
the gate can have.

Whether anything is **stored** on that version is a different question, and it
is usually the one that decides whether a human needs waking. It went unasked
for the agent's whole life. The chart has shipped a read-only ClusterRole since
its first release and no Go code has ever used it; the promotion has carried
`verifyApps` on the wire since the first version and nothing has ever read it.

Two things had to be decided before any of it could be built.

**How much of the cluster.** The stated intent was "everything except the core
group" — count objects on any CRD, never read a Secret. That is a reasonable
sentence and it is **not expressible in Kubernetes RBAC**. There are no deny
rules, and `apiGroups: ["*"]` includes the core group `""`, which contains
Secrets. There is no subtraction.

**How to reach the apiserver.** A NetworkPolicy default-deny is in place, and
the apiserver is reached at `kubernetes.default.svc` — a ClusterIP, which is
DNAT'd to a real endpoint before policy is evaluated. A rule naming that
ClusterIP matches nothing. The symptom is a hang with zero bytes, which reads as
a slow agent rather than a blocked one. This chart's own template comment has
warned about exactly this shape of failure since before there was anything in it
that could hit it.

## Decision

**Off by default**, unlike every other feature here. The rest of what the agent
reads is public or already in the pull request. This reads the operator's
cluster, and a component that starts doing that because somebody upgraded a
chart has made a decision that was not its to make.

**Two scopes, because two are what can be written down.**

- `groups` (default when enabled) — `get` and `list` on the API groups the
  operator lists, plus `get`/`list` on `customresourcedefinitions`. The core
  group is never granted beyond the `pods, events` the chart already had, so
  Secrets stay unreadable *by construction* rather than by intent. A group
  nobody listed degrades to **"not permitted to check"** in the brief.
- `wide` — `apiGroups: ["*"]`, `resources: ["*"]`, `get` and `list`. Answers
  everything, **can read Secret contents**, and the values file says so in those
  words. Available because some operators will prefer it, and refusing to
  provide it would only mean it got written by hand with less thought.

**"Not permitted" is never zero.** `cluster.Count` carries a `Known` flag and
the render prefers the note over the number. A refusal, an unreachable
apiserver, a partial count where one version answered and another did not — all
say what was not checked. The prompt tells the model, in those words, that *not
permitted to check* means nobody looked and is not evidence of safety. The whole
value of "0 live objects" is that it ends a conversation, and it can only do
that if it never quietly means "we did not ask".

**Egress is explicit, per flavor.** `networkPolicy.egress.apiServer.ipBlocks`
takes the endpoints the `kubernetes` Service actually points at; the `cilium`
flavor emits `toEntities: [kube-apiserver]` instead, which is the idiomatic
answer and does not go stale when a control-plane node is replaced. The pod
**refuses to start** if it cannot read the API, so a missing rule is a crash
loop with an explanation rather than a permanent quiet shrug — which matters
precisely because every other failure in this reader is soft by design.

**Read-only, structurally.** `get` and `list`, no `watch`, and the sentence in
`rbac.yaml` — no create, update, patch or delete anywhere, and a feature that
seems to need one should be reconsidered — stays true.

**No `client-go`.** Four endpoints and a bearer token, hand-rolled over
`net/http`, the same call this project made for the GitHub client and the App
JWT. `client-go` would be by a wide margin the largest dependency in a service
whose whole argument is being small enough to audit.

## Consequences

**Good.** *"0 live objects on the versions this chart stops serving"* turns a
blocking finding into a merge. *"This Application was already Degraded before
your bump"* stops a human debugging the wrong change. Both are counted by code
against a read-only view and labelled fact, so they are the strongest evidence
in a brief — nobody wrote them down, they were measured.

**Bad.** An operator now maintains a list of API groups, and the failure mode of
getting it wrong is a brief that says "not permitted to check" rather than a
number. That is the intended trade: the alternative that needs no list is the
one that can read Secrets.

**Also.** The service-account token is re-read from disk on every request.
Projected tokens are bound and rotate roughly hourly; a client that read one at
start-up works for fifty minutes and then 401s forever, which on a service
called a few times a day looks fine in every test and is broken by lunchtime.
The GitHub client learned this with App installation tokens.

## Alternatives rejected

- **Wildcard minus the core group.** Not expressible. Recorded here because it
  is the first thing everyone proposes, including the person who asked for this.
- **Namespaced Roles per watched namespace.** Correct and unusable: counting
  objects on a cluster-scoped CRD needs cluster scope, and the namespace list
  would be a second thing to maintain that goes stale silently.
- **Trust `metadata.remainingItemCount`.** The apiserver sets it only for
  etcd-served lists, not the default watch-cache path, and documents it as
  best-effort. Reading its absence as "no more items" would under-count and then
  present the wrong number as a fact — the one thing a live read must never do.
- **`client-go`.** See above.
- **On by default.** Every other feature here is, and this one reads somebody's
  cluster.
