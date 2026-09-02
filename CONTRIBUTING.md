# Contributing

Bosun has to run on somebody else's cluster, in somebody else's CI, against
somebody else's model. Two rules keep it that way.

## Rule 1: no environment assumptions

Nothing in `charts/` or the service may assume:

| Not assumed | How it is handled |
|---|---|
| A cluster name, domain, or namespace | A value. No example.com, no `the-cluster`. |
| A secret manager | The chart consumes an **existing Secret by name**. ExternalSecret, Vault Agent, SOPS and friends belong to the consumer. |
| A CNI | Standard `NetworkPolicy` by default; `CiliumNetworkPolicy` with `toFQDNs` behind an opt-in flag. |
| A git host | `git.provider` is a value, and the service goes through the `GitProvider` interface. |
| A model provider | `LLMProvider`, with **no default**. The values file must name one. |
| A repository layout | `triage.allowPaths` is a value. The service knows nothing about any particular repo. |

## Rule 1a: the contracts between the halves are the fragile part

The gate, the agent and the charts are joined by **wire contracts**, and every
one of them has broken silently at least once.

The Go dependency runs one way only: `package main` (the agent) and `cluster`
import `gate`, and `gate` imports `migrate` for the report format both sides
read. Nothing points back, so the gate cannot import the agent, and neither
touches the charts. That single direction is what ADR 0008 bought when it moved
the gate in-cluster, and it is why the shared report vocabulary lives in `gate`
and `migrate` rather than being spelled out twice. What follows are the
contracts no compiler is watching. Most cross a process boundary; one is a
list inside this one that has to stay complete on its own:

| Contract | How it broke | What sees both sides now |
|---|---|---|
| The gate finds the report it already published by searching comments for `<!-- gitops-gate -->`, so a re-run edits that comment instead of adding one | The marker lived in one demo script and in nothing that published a report, so no reader could ever find one | `agent/comment_test.go` (no marker can be forged) and `local_markers_test.go` (no demo script scans for one nothing publishes) |
| Any version the agent writes must appear verbatim in the gate's report | Change how the report renders a version and every mechanical fix silently becomes an escalation | `gate/versioncontract_test.go` — a real `DiffResult` through the real `Report` through a real `edits.Apply` |
| `kargo-pipelines` POSTs a promotion body the agent's handler must parse | Malformed from inception until 2026-08-23, and silent throughout: the step is `continueOnError` | `promotionbody_test.go` — the rendered chart's own keys against `agent.Promotion`, and through `PromotionOpened` |
| The chart's environment is the one `config.go` reads | Not yet, but nothing was watching: no Go test had ever read `charts/` | `config_chart_test.go`, both directions, both sets derived |
| The ClusterRole covers the API reads the code makes | Not yet. A missing grant is a 403 that `cluster/` turns into a soft, honest sentence, so nothing would ever look | `chart_rbac_test.go` against `cluster.Reads()`, which the request paths are built from |
| The prompt, the schema and the struct state one response shape | The hand-written list meant to hold two of them together omitted `escalationReason` | `llm/contract_test.go`, all four statements, every list derived |
| Every credential `config.go` reads primes the process redactor | Not yet. A credential added to `Config` and wired to its one client compiles, passes, and has no symptom until a host echoes it back inside an error string | `redaction_test.go`, both sets derived: the credentials from `config.go`'s syntax tree, the coverage from a real `Config` of sentinels through `redact.Text`, and `main`'s own call from its syntax tree, so deleting the priming fails rather than passing |
| Every subprocess redacts what it wrote to stderr | Yes, and the rule arrived too narrow twice: first it qualified a function only if it called `pushAuthEnv`, which left the fetch, the ladder and three clones quoting git verbatim into a published report; then it qualified on the command being git, which left `helm`, `kustomize` and `kubeconform` doing the same. The binary was never the reason | `subprocess_stderr_test.go`, deriving from the mechanism instead of the call sites: a function that starts a subprocess and reads the buffer it gave it, across every package |
| No subprocess is handed a credential this process loaded | Yes, everywhere. `cmd.Env` was nil at every call site but five, and a nil `Env` gives the child `os.Environ()` verbatim, so `helm`, `kustomize` and `kubeconform` each ran holding every token this install was configured with. The five that set it appended to `os.Environ()`, adding one scoped credential on top of all of them | `subprocess_env_test.go`, deriving from the mechanism the way the stderr rule does: a function that builds an `exec.Cmd` must assign its `Env`, and from `childenv` instead of `os.Environ()`, because a check that only asked whether `Env` was set is one those five already passed. The names derive from `config.go`'s own `envSecret` calls and `LoadConfig` records them as it reads them, so the line that reads a credential strips it, which holds only while every credential is read in straight-line code, so that is a rule of its own; `main`'s own call is read from its syntax tree, so deleting the priming fails instead of passing; plus `gitprovider/childenv_test.go` and `gate/childenv_test.go`, which read a real child's environment back out of a shim instead of asserting against the slice |
| No credential this process holds reaches a command line | Yes, twice. The push spelled its token into the remote URL until it was moved into the environment, and a credential in `GIT_REPO_URL` went the same way on three separate clones after that | `gitremote_test.go`, deriving from git's own remote-facing subcommands that only `gitprovider` may run one; plus `pushremote_test.go` and `cloneargv_test.go`, which replace git with a shim and read the argv the kernel was actually given |
| The shape of every MCP tool result, which somebody else's agent parses | Not yet, and this side of it cannot break loudly: the other half is a client in a repository this CI will never run. A renamed field, or an absence that becomes an empty array, is a silent break there and a passing build here | `mcp/testdata/*.json` through `mcp/contract_test.go`: golden files, so a schema cannot drift without a reviewer seeing the diff, and `mcp_credentials_test.go`, which walks the result types from `mcp.Tools()` and the credential names from `config.go`'s syntax tree |
| The gate's verdict, copied into `mcp`'s own shapes because `mcp` may not import `gate` | Not yet. A field the composition root forgets to copy goes missing from every answer, and neither package has a compiler or a test that can see the other half | `mcp_gate_test.go`: a real `DiffResult` through the real `Summarise` and the real adapter, with the blocker breakdown compared field by field off the struct instead of by name |
| What the agent has spent on a pull request, published by `mcp` and counted by `agent` | Not yet. The cap's only memory is a label under a prefix that follows the brand, so a second count of it agrees on every install but a renamed one, where it reports attempts remaining on a pull request already escalated, and a caller acts on that by waiting | `mcp_triage_test.go`: the crossing driven with a renamed agent and both prefixes in the fixture, so a count that hard-coded the default name fails; `agent/attemptcap_test.go` holds the cap's own path to the same method |
| The label the agent writes when it gives up is the label `handoff_queue` selects on | Not yet, and it would look like good news. A rename on either side leaves a queue that is empty forever, on the one tool whose empty answer is "nobody is waiting on you". The symptom is an on-call agent that stops reporting handoffs, while the people in them wait for somebody who was told there was nobody | `mcp_handoff_test.go`: the label read from `agent/triage.go`'s own syntax tree and driven through the real handler, with a second pull request in the fixture that carries no label, so a queue that ignored labels fails too |
| No stamp the gate publishes into a pull-request comment survives a trip through the MCP surface | Not yet, and it would not look like a break: a client that relays a smuggled stamp onto a pull request republishes a verdict the gate never reached, and the next gate run reads it back as its own memory | `mcp_stamps_test.go`: every `<!-- gitops-gate…` constant in the module, found by walking its syntax trees, driven through the real handler |
| What ArgoCD says this control plane runs, copied from the live reading into `gateservice`'s snapshot and again into `mcp`'s own shapes | Not yet. A field either copy forgets is absent from every `inventory` answer, and the reader who would notice is a platform agent asking which cluster an Application lands on, who has no way to tell a missing row from a fleet that does not have one | `gateservice/fleet_test.go`: the two rows walked field for field off the source struct, with the one field the resolution consumes named and the rest required to have a counterpart; `mcp_fleet_test.go` walks the second copy the same way, driven through the real adapter |
| Every segment interpolated into a remedy matches the RFC1123 grammar before the command is composed | Not yet, and the compiler cannot see it: a remedy is a string, and a builder that interpolates a piece it did not check compiles and reads correctly. The other half of this contract is a client's agent that may run the command under its own credentials, in a repository this CI will never see | `pipeline/remedy_property_test.go`: generated names either side of the grammar, with the expectation decided by Kubernetes' own validators instead of by the check under test, and asserted two-sidedly so a grammar tightened past what Kargo writes fails too; `mcp/remedy_property_test.go` holds the same claim about the trip out |

This is why both halves live in one repository. A boundary is safe where its
contract can be tested; across two repositories, no CI run can check both
sides. **A change to either side of a contract needs a test that sees both**,
and adding one is worth more than almost any feature.

Two rules those tests are held to, because a contract test that goes stale is
worse than none — it reads like coverage:

- **Derive the list, never write it.** Every check above enumerates its subject
  from the artefact itself: the switches from `values.schema.json`, the
  environment from `config.go`'s syntax tree, the fields from a struct's tags.
  A hand-written list of things to check is what the 0.25.0 ClusterRole was
  *fixed with*, and five entries with nothing forcing a sixth is how it stayed
  broken.
- **Every derivation carries a self-check.** If the walk stops finding what it
  reads, it fails loudly rather than comparing two empty sets and reporting
  agreement, which is indistinguishable from a pass.

## Rule 2: the safety model lives in code

The model returns a verdict and a proposal: scalar edits, a complete migrated
manifest, or a complete values document. It applies none of them, and it does
not choose what it is allowed to touch.

Any change that moves an invariant from `edits/` or `agent/` into the prompt is
a regression, however well the prompt performs. A prompt is a request; the
allowlist is a guarantee. See
[`adr/0001-structured-edits-not-agentic-loop.md`](adr/0001-structured-edits-not-agentic-loop.md)
and [`docs/safety-model.md`](docs/safety-model.md).

### How to describe that boundary, and how not to

**Never write that the model does not edit, write, or touch files.** It authors
the content of three of the four write paths, and on the scalar path the value
it names is written to the line exactly as it spelled it. Every short version
of the claim has shipped somewhere and every one of them was wrong; the first
reached the landing page, the second the composition root, the third this
repository's own FAQ:

| Do not write | Because |
|---|---|
| "the model does not edit files" | it authors whole manifests and whole values documents |
| "the model never writes" | the `to` of a scalar edit lands verbatim |
| "the model itself writes nothing" | same, and it writes the prose in every comment |
| "**the** deterministic repair", where the article implies the only one | only `migrate` is model-free; `structural` and `valuesmigrate` are not |
| "the model is called once per pull request" | the reshape calls it per document, values per Application |

Write the narrow claims instead, all of which hold: **the model applies
nothing**, it **has no file-edit tool, no shell and no path to the repository**,
it **does not decide which files are writable**, and **the harness writes every
byte that reaches a branch**. Then name what checked the proposal, because that
is the part that differs per path and the part a reader is actually asking
about.

When a new write path lands, grep for the phrases above before opening the pull
request. The four that describe the boundary are `README.md`,
`docs/safety-model.md`, `site/src/authored/index.mdx` and
`site/src/authored/reference/faq.md`, and they drift together.

## Everything documents itself

- `README.md`: what it is, what it needs, how to install
- `charts/bosun/values.schema.json`: the machine-checkable values contract
- `CHANGELOG.md`: keep-a-changelog style, one per unit
- `docs/`: reference material too long for a README
- `adr/`: anything load-bearing, with the context, the decision, and what it
  costs

A change that alters behaviour and does not touch the relevant documentation is
incomplete.

## Where things live

One package per decision, each with a doc comment saying what it owns and what
it deliberately does not:

| Package | Owns |
|---|---|
| `agent/` | judging one pull request, and what to say about it |
| `gate/` | rendering the repository and diffing it, with no git host and no model |
| `gateservice/` | running the gate in-process, per open pull request, on a timer |
| `supervisor/` | the pipeline sweep: the promotions that never happened |
| `mcp/` | the read-only tool surface, and the shape of what it hands back. The one listener built to be reached from outside the cluster, so it imports the result types and the redactor and nothing else |
| `prompt/` | what the model is told, and what the eval suite scores |
| `edits/`, `migrate/`, `structural/`, `valuesmigrate/` | the four ways a file gets written, each behind its own refusals |
| `internal/` | fixtures two packages need and nobody outside this module should have. Today: a chart repository on loopback, because a chart directory cannot express two versions of one chart; the charts this repository ships, rendered and parsed; and object names either side of the RFC1123 grammar, because `pipeline` and `mcp` assert the same thing about them, and a corpus written twice is two corpora |
| `cluster/`, `gitprovider/`, `llm/`, `upstream/`, `egress/` | the outside world, one seam each, every one with a fake |
| `redact/` | taking this process's own credentials out of text before it leaves. One redactor, primed at start-up, read by any surface |
| `childenv/` | the environment every subprocess is started with: this process's own, minus the variables a credential was read from. Primed at start-up beside the redactor, and the other half of the same question: `redact` filters what a child may publish, and this decides what the child is handed |
| root | the composition root: read the environment, build one of each, wire, serve |

There are two `main` packages, in two shapes, and the shapes mean different
things:

- **root** is the module's only shipped binary. Idiomatic Go for a module that
  ships one thing; `cmd/bosun/` would say there are several.
- **`evals/export/`** is a development tool, beside the thing it exports rather
  than in a top-level `cmd/`, because it is useless without it.

A new binary belongs in whichever of those two it is.

## The toolchain

```bash
nix develop     # or `direnv allow`, which does it on cd
```

Go, kubectl, kind, kubeconform and helm, at the versions this repository is
meant to be built and tested with. **helm and kubeconform are pinned to the
versions the images carry**, and taken from the same upstream releases rather
than from nixpkgs, which currently ships Helm 4.

That pin earns its keep. The gate's verdict *is* the output of `helm template`,
so the helm that produced it is part of the answer: render with a different one
and you get a verdict that is locally true and globally wrong, and nothing
about the symptom points at a version. The Dockerfile already says this where
it pins, and `hack/portability-test.sh` asserts the flake and CI agree with it,
because three copies of a version number drift.

**Bumping either one is not a one-line change.** The version is written in
`flake.nix`, `Dockerfile` and `ci.yaml`, and the Dockerfile carries a
per-architecture `sha256` beside it, because the tarballs differ per
architecture and one digest would be right for exactly one of them.
`flake.nix` has its own four hashes, one per platform the dev shell builds for.
Move all of it together. `hack/portability-test.sh` asserts that all three
agree on the version strings, so a forgotten copy fails a check rather than
waiting to be noticed. It reads versions and not digests: a stale `sha256` is
caught at build time by `sha256sum -c`, which is the other half of the same
guarantee.

The digests are checked in rather than fetched from beside the tarball. Both
projects publish a checksum file served by the host that served the tarball, so
verifying against it proves only that the two agree, which they would after a
compromised release too.

helm and kubeconform are **deliberately excluded from Dependabot**, and
`.github/dependabot.yml` says so where it excludes them. Dependabot bumps one
copy of one thing: a bumped version beside a stale checksum is a green-looking
pull request handed to whoever has to reconcile the rest. Left out, a hand bump
that forgets a digest fails on `sha256sum -c` at build time, loudly, which is
the safe direction to be wrong in.

Two tools stay outside the shell, because they manage host state a second copy
would fight over: **colima** (a VM with state in `~/.colima`) and
**idpbuilder** (not in nixpkgs). `local/scripts/00-runtime.sh` installs both
with brew, and the shell says so if they are missing.

## Checks

```bash
go test ./...              # both commands, unit tests and the eval suite
gofmt -l .                 # must print nothing
go vet ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 ./...
hack/lint.sh               # helm lint + values.schema.json validation
hack/portability-test.sh   # no environment assumptions; everything renders

cd site && npm ci          # once
npm run build              # the site, from the markdown in this repository
npm run check:links        # after a build
```

The site build is a required check and it is in the dev shell, so run it
before pushing anything that renames a heading or moves a published file.
Those break the site rather than the doc, and only at build time.

`govulncheck` runs in CI on every pull request. It is not a general scanner: it
reports only what this code actually reaches, so a vulnerability in a package
nothing calls does not fail the build and one in a path the gate takes on every
pull request does. A service whose product is telling other people their
dependencies moved should know when its own did. It runs through `go run` at a
pinned version rather than an action, because the module proxy checksums it
against `go.sum`'s database, which is a stronger pin than a commit sha and one
fewer third party holding a token in that job.

`hack/portability-test.sh` is the successor to an extraction test that enforced
a one-way link rule while this package still lived inside the platform
repository. It keeps the checks that were never about extraction: no
environment assumptions, and everything renders.

The eval suite is the thing to watch. It measures classification against
recorded incidents; a prompt or model change that moves those numbers should
say so in the changelog with the model it was measured against.

## Versioning and releases

**You do not cut releases. Bump a version and merge.**

- **A chart publishes when its own `version` reaches main.** Chart versions are
  independent of each other and of the agent's, so nothing waits on a shared
  tag. Publishing refuses to overwrite, so re-running is harmless.
- **A release is cut when `charts/bosun`'s `appVersion` changes on main.**
  `release.yaml` tags it and publishes the image at that version. `appVersion`
  is already the single source of truth for which image deploys, since the
  consuming addon leaves `image.tag` unset, so deriving the tag from the same
  field means the tag, the chart and the running image cannot disagree.
- **A merge publishes the one image this repository ships.** `image.yaml`
  builds `bosun` on any push to main that reaches code, and skips prose-only
  pushes via `paths-ignore`. The gate is a package the agent compiles in, not
  a second image ([ADR 0010](adr/0010-the-cli-goes-too.md)).

- **CI refuses a pull request that changes a chart without bumping its
  version.** Publishing already refuses to overwrite, but silently and after
  the fact: the pull request merges, publishes nothing, and looks exactly like
  a release.

That last rule comes from an incident. When releases were a version bump merged
by a person followed by someone remembering to push a tag, 0.2.0 merged
untagged: nothing between 0.1.0 and 0.3.0 was published, and the cluster ran
the first agent for a day while five fixes sat on main looking shipped.

One thing to know if you change any of this: **a tag pushed with
`GITHUB_TOKEN` does not trigger workflows.** GitHub blocks it to prevent
recursion, so `release.yaml` *calls* the image workflow rather than tagging and
hoping something notices.

## License

Contributions are accepted under the repository's
[PolyForm Internal Use 1.0.0](LICENSE) license. By opening a pull request you
agree that your contribution may be relicensed by the copyright holder,
including under commercial terms.
