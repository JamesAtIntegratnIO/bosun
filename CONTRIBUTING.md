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
contracts that still cross a process boundary, where no compiler is watching:

| Contract | How it broke |
|---|---|
| The gate finds the report it already published by searching comments for `<!-- gitops-gate -->`, so a re-run edits that comment instead of adding one | The marker lived in one demo script and in nothing that published a report, so no reader could ever find one |
| Any version the agent writes must appear verbatim in the gate's report | Still untested. Change how the report renders a version and every mechanical fix silently becomes an escalation |
| `kargo-pipelines` POSTs a promotion body the agent's handler must parse | Untested |

This is why both halves live in one repository. A boundary is safe where its
contract can be tested; across two repositories, no CI run can check both
sides. **A change to either side of a contract needs a test that sees both**,
and adding one is worth more than almost any feature.

## Rule 2: the safety model lives in code

The model returns a verdict and a proposal: scalar edits, or a complete
migrated document on the structural path. It applies neither, and it does not
choose what it is allowed to touch.

Any change that moves an invariant from `edits/` or `agent/` into the prompt is
a regression, however well the prompt performs. A prompt is a request; the
allowlist is a guarantee. See
[`adr/0001-structured-edits-not-agentic-loop.md`](adr/0001-structured-edits-not-agentic-loop.md)
and [`docs/safety-model.md`](docs/safety-model.md).

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
| `prompt/` | what the model is told, and what the eval suite scores |
| `edits/`, `migrate/`, `structural/` | the three ways a file gets written, each behind its own refusals |
| `cluster/`, `gitprovider/`, `llm/`, `upstream/`, `egress/` | the outside world, one seam each, every one with a fake |
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
