# Contributing

Bosun has to run on somebody else's cluster, in somebody else's CI, against
somebody else's model. Two rules keep it that way.

## Rule 1 — no environment assumptions

Nothing in `charts/` or the service may assume:

| Not assumed | How it is handled |
|---|---|
| A cluster name, domain, or namespace | A value. No example.com, no `the-cluster`. |
| A secret manager | The chart consumes an **existing Secret by name**. ExternalSecret, Vault Agent, SOPS and friends belong to the consumer. |
| A CNI | Standard `NetworkPolicy` by default; `CiliumNetworkPolicy` with `toFQDNs` behind an opt-in flag. |
| A git host | `git.provider` is a value, and the service goes through the `GitProvider` interface. |
| A model provider | `LLMProvider`, with **no default**. The values file must name one. |
| A repository layout | `triage.allowPaths` is a value. The service knows nothing about any particular repo. |

## Rule 1a — the contracts between the halves are the fragile part

The gate, the agent and the charts are joined by **wire contracts**, and every
one of them has broken silently at least once.

The Go dependency runs one way only: `package main` (the agent) and `cluster`
import `gate`, and `gate` imports `migrate` for the report format both sides
read. Nothing points back — the gate cannot import the agent, and neither
touches the charts. That single direction is what ADR 0008 bought when it moved
the gate in-cluster, and it is why the shared report vocabulary lives in `gate`
and `migrate` rather than being spelled out twice. What follows are the
contracts that still cross a process boundary, where no compiler is watching:

| Contract | How it broke |
|---|---|
| The agent finds the gate's verdict by searching comments for `<!-- gitops-gate -->` *(ci mode only — in cluster mode the verdict never leaves the process, which is one reason ADR 0008 moved it)* | The marker lived in one demo script and in no CI adapter, so no agent could ever find a report CI published |
| Any version the agent writes must appear verbatim in the gate's report | Still untested. Change how the report renders a version and every mechanical fix silently becomes an escalation |
| `kargo-pipelines` POSTs a promotion body the agent's handler must parse | Untested |

This is the reason both halves live in one repository. A boundary is safe
where its contract can be tested; across two repositories, no CI run can check
both sides. **A change to either side of a contract needs a test that sees
both**, and adding one is worth more than almost any feature.

## Rule 2 — the safety model lives in code

The model returns a verdict and an edit set. It does not edit files, and it
does not choose what it is allowed to touch.

Any change that moves an invariant from `edits/` or `triage.go` into the prompt
is a regression, however well the prompt performs. A prompt is a request; the
allowlist is a guarantee. See
[`adr/0001-structured-edits-not-agentic-loop.md`](adr/0001-structured-edits-not-agentic-loop.md)
and [`docs/safety-model.md`](docs/safety-model.md).

## Everything documents itself

- `README.md` — what it is, what it needs, how to install
- `charts/bosun/values.schema.json` — the machine-checkable values contract
- `CHANGELOG.md` — keep-a-changelog style, one per unit
- `docs/` — reference material too long for a README
- `adr/` — anything load-bearing: the context, the decision, and what it costs

A change that alters behaviour and does not touch the relevant documentation is
incomplete.

## The toolchain

```bash
nix develop     # or `direnv allow`, which does it on cd
```

Go, kubectl, kind, kubeconform and helm, at the versions this repository is
meant to be built and tested with. **helm and kubeconform are pinned to the
versions the images carry**, and taken from the same upstream releases rather
than from nixpkgs — which currently ships Helm 4.

That pin is not tidiness. The gate's verdict *is* the output of `helm
template`, so the helm that produced it is part of the answer: render with a
different one and you get a verdict that is locally true and globally wrong,
looking nothing like a version problem while you chase it. Both Dockerfiles
already say this where they pin; `hack/portability-test.sh` now asserts the
flake agrees with both, because three copies of a version number is exactly
the shape that drifts.

Two tools stay outside the shell, because they manage host state a second
copy would fight over: **colima** (a VM with state in `~/.colima`) and
**idpbuilder** (not in nixpkgs). `local/scripts/00-runtime.sh` installs both
with brew, and the shell says so if they are missing.

## Checks

```bash
go test ./...              # both commands, unit tests and the eval suite
gofmt -l .                 # must print nothing
go vet ./...
hack/lint.sh               # helm lint + values.schema.json validation
hack/portability-test.sh   # no environment assumptions; everything renders
```

`hack/extraction-test.sh` is gone. It proved this package could be lifted out
of the platform repository that hosted it, and enforced a one-way link rule to
keep the lift cheap. The lift has happened — this repository *is* the package,
so a rule about escaping a directory that no longer exists fails on its own
fixtures. `hack/portability-test.sh` keeps the checks that were never about
extraction.

The eval suite is the thing to watch. It measures classification against
recorded incidents; a prompt or model change that moves those numbers should
say so in the changelog with the model it was measured against.

## Versioning and releases

**You do not cut releases. Bump a version and merge.**

- **A chart publishes when its own `version` reaches main.** Chart versions are
  independent of each other and of the agent's, so nothing waits on a shared
  tag. Publishing refuses to overwrite, so re-running is harmless.
- **A release is cut when `charts/bosun`'s `appVersion` changes on main.**
  `release.yaml` tags it and publishes the images at that version. `appVersion`
  is already the single source of truth for which image deploys -- the
  consuming addon leaves `image.tag` unset -- so deriving the tag from the same
  field means the tag, the chart and the running image cannot disagree.
- **A merge publishes only the images it changed.** `image.yaml` diffs the
  push: `gate/**` (plus `go.mod`/`go.sum`) rebuilds `gitops-gate`, anything
  else in the module rebuilds `bosun`, a release or a manual run builds both.
  Rule 1a cuts both ways -- the gate imports nothing from the agent, so a
  triage fix is not a reason to republish the gate.

  It used to publish both from one matrix, and that was not merely wasteful.
  The consumer pins the gate by `main-<sha>` and will not auto-merge it,
  because the gate judges every other promotion and a human has to read the
  bump. Republishing an identical gate under a new sha -- and it is never
  byte-identical, the revision label carries the commit -- spent that
  attention on nothing.

- **CI refuses a pull request that changes a chart without bumping its
  version.** Publishing already refuses to overwrite, but silently and after
  the fact: the pull request merges, publishes nothing, and looks exactly like
  a release.

That last one is not hypothetical. Releases used to be a pull request bumping a
version, merged by a person, followed by someone remembering to push a tag.
Nobody remembered: 0.2.0 merged and was never tagged, so nothing between 0.1.0
and 0.3.0 was published and the cluster ran the first agent for a day while
five fixes sat on main looking shipped.

One trap if you change any of this: **a tag pushed with `GITHUB_TOKEN` does not
trigger workflows.** GitHub blocks it to prevent recursion, so `release.yaml`
*calls* the image workflow rather than tagging and hoping something notices.

## License

Contributions are accepted under the repository's
[PolyForm Internal Use 1.0.0](LICENSE) license. By opening a pull request you
agree that your contribution may be relicensed by the copyright holder,
including under commercial terms.
