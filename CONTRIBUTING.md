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

There are no code dependencies between the gate, the agent and the charts.
They are joined by **wire contracts**, and every one of them has broken
silently at least once:

| Contract | How it broke |
|---|---|
| The agent finds the gate's verdict by searching comments for `<!-- gitops-gate -->` | The marker lived in one demo script and in no CI adapter, so no agent could ever find a report CI published |
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

## Versioning

The chart and the image are versioned independently, semver. The chart's
`appVersion` tracks the image it deploys. Tagging `v<x.y.z>` publishes both:
the image to `ghcr.io/jamesatintegratnio/bosun` and the chart to
`oci://ghcr.io/jamesatintegratnio/charts/bosun`.

## License

Contributions are accepted under the repository's
[PolyForm Internal Use 1.0.0](LICENSE) license. By opening a pull request you
agree that your contribution may be relicensed by the copyright holder,
including under commercial terms.
