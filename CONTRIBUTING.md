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
go test ./...       # unit tests and the eval suite
gofmt -l .          # must print nothing
go vet ./...
hack/lint.sh        # helm lint + values.schema.json validation
```

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
