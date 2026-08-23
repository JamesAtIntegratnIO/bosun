# Changelog

All notable changes to the `bosun` chart. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning is semver.

## [0.6.0]

### Added

- **`networkPolicy.egress.fqdnPatterns`** -- Cilium `matchPattern` entries,
  merged into the same rule as `fqdns`.

  A registry needs two hosts, not one. `ghcr.io` serves the manifest and
  redirects the blob -- which is where an image's labels live, and therefore
  where the upstream-notes lookup has to go -- to
  `pkg-containers.githubusercontent.com`. Allowing only the registry host means
  that fetch hangs until it times out. Observed in production: the agent
  reported `context deadline exceeded (Client.Timeout exceeded while awaiting
  headers)` and fell back to a render-only explanation, which is indistinguishable
  from an artifact that simply has no release notes.

  Some of these hosts are a SET rather than a name -- quay serves blobs from
  `cdn01.quay.io`, `cdn02…` -- and `matchName` cannot express that without
  asserting how many there are. Guessing at the membership is how the
  allow-list silently stops covering something, so patterns are the honest
  shape.

## [0.1.0] - 2026-08-23

First release from the standalone repository. Extracted from
`gitops_homelab_2_0`, where this was developed as `delivery/` and called
`delivery-agent` until 2026-08-23. Now licensed
[PolyForm Internal Use 1.0.0](../../LICENSE) rather than Apache 2.0.

### Added

- `branding.name` and `branding.mark`. The agent signs its comments, commits
  and attempt labels with this. It is deliberately NOT the account its token
  belongs to -- give it a dedicated bot user or a GitHub App, or every comment
  carries the name of whoever minted the token and reads like a colleague's.

- `git.provider: gitea`, and `git.insecureSkipTLSVerify` for a self-hosted host
  with a private or self-signed certificate.
- `networkPolicy.egress.namespaces` — egress to an in-cluster destination by
  namespace selector. This CANNOT be expressed as an `ipBlock`: a Service's
  ClusterIP is DNAT'd to a pod IP before policy evaluation, so a rule naming
  the ClusterIP matches nothing and the connection hangs with zero bytes and
  no error. Found by running the agent against an in-cluster Gitea.

- Deployment, Service, ServiceAccount, read-only ClusterRole and NetworkPolicy
  for the in-cluster triage agent.
- `networkPolicy.flavor: cilium` additionally emits a `CiliumNetworkPolicy`
  with `toFQDNs`, which names the hosts the agent may reach rather than a range
  that happens to contain them.
- `values.schema.json`, and template-level `fail` for the cross-field
  requirements a schema cannot express.

### Notes

- **No Ingress or HTTPRoute is rendered, by design.** Only Kargo calls this
  service, and publishing something that can spend money and write to your
  repository would be gratuitous exposure.
- **RBAC is read-only.** No create, update, patch or delete verb appears
  anywhere. The agent observes the cluster and writes to pull requests.
- **The chart never creates a Secret.** It takes the name of an existing one,
  so ExternalSecret, Vault Agent, SOPS or `kubectl create` all work and none is
  assumed.
- **`llm.provider` has no default** and the chart refuses to render without it.
- **`triage.allowPaths` must be non-empty** — an empty allowlist means the
  agent could never apply a fix, and failing at render time is better than
  discovering it when a fix is silently refused.
- The Kargo controller's **own** egress policy must permit this service. A
  controller allowed `0.0.0.0/0 except RFC1918` cannot reach a ClusterIP, and
  the symptom is a hang with zero bytes rather than an error.
