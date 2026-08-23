# Local proving ground

A disposable cluster where Bosun is exercised against **real pull requests**:
nine incidents that actually happened to the platform it was built for,
replayed one at a time, with the live model reasoning about each one and
pushing commits when it decides the fix is mechanical.

It exists because the service was previously only ever exercised in production,
one merge at a time.

## What it builds

| Piece | What runs it |
|---|---|
| kind cluster, Gitea, ArgoCD, ingress | [idpbuilder](https://cnoe.io/docs/reference-implementation/local) |
| Bosun | helm, from **this working tree** |
| the repository under test | `sample-repo/`, pushed into Gitea |

The image is **built from your working tree**, not pulled. A proving ground
that tests the last published image is testing the past.

Bosun needs a git host and a model endpoint, and nothing else — no Kargo, no
Prometheus, no gate binary. The gate's verdict is recorded rather than
rendered, for the reason below.

## Requirements

- macOS or Linux, ~8 GB free RAM, ~20 GB disk
- Homebrew (the runtime script installs colima, kind and idpbuilder)
- An OpenAI-compatible model endpoint **the cluster can reach**

```bash
export LLM_BASE_URL=http://<your-host>:1234/v1
make up
make scenarios
```

`LLM_BASE_URL` has no default on purpose. A demo that silently starts spending
money against a vendor you did not choose is a bad default.

If your endpoint is LM Studio, check that "Serve on Local Network" is on — it
binds `127.0.0.1` by default, which the cluster cannot reach. A sleeping
workstation looks exactly like a wedged model.

## What a run looks like

```bash
make scenarios                   # all nine
make scenarios CASE=metallb      # just one
```

Four of the nine are mechanical, and on those Bosun pushes a commit. MetalLB
0.16.0 swapping its FRR sidecars for a DaemonSet, for instance:

```
==> metallb-frr-defaults-flip  (expected: mechanical)
  bump metallb chart 0.15.2 -> 0.16.0
  pull request #8
  gate report posted, status gate=failure
  ok    pushed a fix: f27ac09 -> 7a11247
      -        enabled: true
      +        enabled: false
      -      enabled: true
      +      enabled: false
      Pushed a fix to `scenario/metallb-frr-defaults-flip` (attempt 1 of 2).
```

The other five are escalations and a no-action, and it should not touch those.

The summary shows whether it **edited**, not whether the edit was **right**.
The exact scalars are checked by the eval suite: `go test ./evals/...`.

## What is replayed and what is live

The gate's **report** is the recorded one from each incident, posted the way a
gate posts it — reproducing fourteen upstream chart versions locally would
prove nothing extra, and the report is Bosun's actual input either way.

The pull requests, the model, the reasoning, the classification and every
commit pushed are **live**.

The cases are not invented, and they are not written twice: each is already an
eval fixture, and this reads those same fixtures, so the thing the eval
measures and the thing you watch cannot drift apart.

## Things this turned up

Each of these is a real defect or a real gap, found by running it:

- **Bosun could not talk to Gitea at all.** `GIT_PROVIDER` accepted only
  `github`. There is a `gitprovider/gitea.go` now.
- **An in-cluster destination cannot be expressed as an ipBlock.** A ClusterIP
  is DNAT'd to a pod IP before policy evaluation, so the egress rule matched
  nothing and the connection hung with zero bytes. The chart takes
  `networkPolicy.egress.namespaces` now.
- **A rebuilt image with an unchanged tag does not roll.** Helm sees an
  identical pod spec and keeps the running pod, with the old binary in it.
  `30-agent.sh` restarts the deployment explicitly; without that you spend an
  hour debugging code that is not running.

## The most useful thing it has shown

| | Blocks the merge | Bosun's class |
|---|---|---|
| Targeting moved | yes | escalate |
| Source / project / namespace changed | yes | escalate |
| apiVersion migration | yes | **always** escalate |
| A chart default flipped | no, reported only | mechanical |
| Coupled pins | no, reported only | mechanical |
| A port moved under a policy | no, reported only | mechanical |

Everything a gate blocks on is structural, and Bosun escalates structural
changes by design. Everything it can mechanically fix is a values conflict,
which a gate reports without blocking. **The two sets barely intersect**, so
"gate red, agent fixes it" is close to a null case today.

That is a design question about where each half draws its line — not a bug in
either — and it is worth answering before Bosun is trusted to push fixes
anywhere that matters.

## Teardown

```bash
make down     # delete the cluster, keep the VM
make clean    # and stop colima
```
