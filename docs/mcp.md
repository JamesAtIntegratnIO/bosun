# The MCP surface

Bosun computes the expensive facts in the promotion loop: why a Stage silently
stopped promoting, why one pull request is blocked, what the agent is doing
about it, and the exact command that ends each of those situations. Until this
surface existed it published them as prose — a comment on a pull request, a
page behind a port-forward, a metrics endpoint. That is right for a person
reading a page and useless to the agents people actually work through, which
were left parsing markdown written for somebody else.

This is a fourth exit for facts that already exist. It is a read-only
[MCP](https://modelcontextprotocol.io) server, on a port of its own, behind a
bearer token, off until an operator turns it on. **Nothing here computes
anything**: every tool answers from the snapshot the last sweep left in memory,
so a call reaches no cluster, no git host and no model, and a chatty client
cannot spend an install's rate limit. Nothing here mutates anything either,
because no tool does and none is planned — the chart's ClusterRole has no write
verb anywhere.

## The tools

Each one is described the same way below: **what it answers**, then **what it
takes**, then whatever else is true of that tool alone. Two things hold across
all of them and are not restated per tool. Every result names the repository it
is about, so a client holding tokens for several installs cannot mix two
fleets. Every result carries when the sweep behind it ran and how many seconds
ago, so a client can decide whether to trust the answer or wait for the next
sweep.

A tool that asks about one pull request takes its number, and a `repository`
that is optional: an install watches exactly one, so the argument exists to be
checked rather than chosen, and naming a different one is refused rather than
answered.

What each one says before anything has looked is one rule for the whole
surface, and it is in
[before the first sweep](#before-the-first-sweep-an-absence-is-not-a-zero).

### `pipeline_report`

**Answers** what the last pipeline sweep found: promotions that ended without
delivering, Warehouses that stopped discovering, verifications that will not
re-run, tracked pins that write nowhere. Each finding carries a typed kind and
severity, how long it has held, and where one exists the paste-ready command
that recovers it. Worst first.

**Takes** no arguments.

### `gate_status`

**Answers** the queue: every open pull request the last gate sweep saw, with
the verdict standing against each head commit — the state, whether it blocks,
and the blocker breakdown as counts per kind.

**Takes** no arguments.

The call before `gate_verdict`: the one that says which pull request to ask
about.

### `gate_verdict`

**Answers** why one pull request is blocked, or why it is not. The blocker
counts and every finding behind them, with dropped API versions carried as
fields — which definition, which versions it stopped serving, which one
survives, and the kind of manifest that has to move. Each finding says whether
an edit in the repository could clear it, and the list of what the gate could
not render travels beside them, because a clean verdict over a partial render
is a narrower claim.

**Takes** a `pullRequest` number, and an optional `repository`.

### `triage_status`

**Answers** what the agent is doing about one pull request: the phase it is in,
the automatic fix attempts spent against the cap, and the labels standing on
it. Use it to tell an agent that is still working from one that has finished
and will not try again.

**Takes** a `pullRequest` number, and an optional `repository`.

It is the one answer with two clocks: the phase is this process's own current
state, while the labels and the attempt count are as old as the sweep the
result names.

## No resources, and no sampling

The surface offers tools and nothing else. No resources — the tempting one is
the supervisor's markdown report served whole, and it is a composite of trusted
and untrusted text with no field boundary to hang provenance on, which is the
thing the typed tools exist to avoid. No sampling — a caller that could ask
this process to call a model could spend the install's budget and steer its
prompts.

## Before the first sweep, an absence is not a zero

The rule the whole surface is built around: **"nothing found" is
unrepresentable unless something actually looked.**

The HTTP surfaces say this with a `503` before the first sweep completes. JSON
has one shape that carries the same distinction, and it is absence.

Two rules hold whatever tool is asked:

- **Every result carries `swept`**, and `sweptAt`/`ageSeconds` are absent until
  it is true.
- **A collection is absent until something has looked**, and empty only after
  something looked and found nothing. A client reading the second as the first
  is making the most expensive mistake this project exists to catch.

Then one entry per tool, because what there is to be absent differs:

- `pipeline_report` publishes `findings` as **absent** before the first sweep
  and as an **empty list** after a sweep that found nothing. Beside them
  travels the sweep's own accounting of what it managed to examine, so a report
  with no findings can prove it looked; `clean` is true only when it did.
- `gate_status` publishes `open` as absent when nothing has listed pull
  requests — before the first sweep, or after one that could not reach the git
  host — and empty only when a sweep listed them and there were none. A sweep
  that failed says so in a field of its own, and the pull requests an earlier
  sweep saw are published beside that error rather than dropped: stale evidence
  beats none, as long as it is labelled stale.
- `gate_verdict` answers a pull request with no verdict standing **as such**,
  and never as a passing one. There are several ways to have no verdict — a
  render in flight, a verdict on the git host this process did not re-run, a
  gate that could not run, a sweep that could not list — and each has its own
  state and its own sentence.
- `triage_status` always answers with a phase, because this process always
  knows what it is running, and publishes the labels and the attempt count only
  for a pull request the last sweep saw — absent rather than zero, so nothing
  here claims on its behalf that it has spent nothing. Three of its phases are
  the three ways of not knowing: `unswept` (nothing has looked), `unknown` (the
  sweep could not list) and `absent` (the sweep looked and did not see this one
  open).

One consequence worth knowing before it surprises anybody: with
`supervise.enabled: false` there is no sweep to read, so `pipeline_report`
truthfully reports that nothing has looked for as long as that stays true. The
pod logs a line saying so at start-up, because from the client's side that is
indistinguishable from an install whose first sweep has not finished yet.

## Turning it on

```yaml
mcp:
  enabled: true
  existingSecret: bosun-mcp   # required; this chart never creates a Secret
  tokenKey: token
  allowFrom:
    - namespace: gateway-system
      podSelector: {app: envoy}
service:
  mcpPort: 8082               # the default
```

```bash
kubectl -n bosun create secret generic bosun-mcp \
  --from-literal=token="$(head -c 32 /dev/urandom | base64)"
```

**Off by default, and not as a preference.** Upgrading an install must not open
a new programmatic API on it, so `helm upgrade` from a release before this one
changes nothing: no port on the Service, no peer in the NetworkPolicy, no
variable in the Deployment.

**It is on its own port.** `service.port` also answers
`POST /v1/promotion-opened`, the endpoint that spends money and writes to the
repository. A NetworkPolicy and a gateway both draw their lines at the port, so
"expose the read-only tools" stays a smaller decision than "expose the endpoint
that edits your repository" only because the two never share a listener.

The chart writes no route for this port — publishing it beyond the cluster is a
Gateway or Ingress an operator writes, in front of a port whose peer list is
`mcp.allowFrom`. Look at it over a port-forward first, which is how to decide
whether to publish it at all:

```bash
kubectl -n bosun port-forward deploy/bosun 8082 &
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://localhost:8082/mcp
```

The transport is streamable HTTP with a JSON response body, at `/mcp`, `POST`
only: every tool answers from a snapshot the process already holds, so there is
nothing to push and a `GET` would open a stream a client holds forever to
receive nothing.

Outside the chart, the settings are environment variables: `MCP`, `MCP_ADDR`
(default `:8082`), `MCP_TOKEN`, and
`MCP_DANGEROUSLY_SERVE_WITHOUT_AUTHENTICATION`. The token also reads from
`MCP_TOKEN_FILE`, which is the form to prefer and the form the chart writes: a
credential the process reads from a mounted file is not in its environment for
anything that dumps one to copy.

## The token

**Without a token the listener does not start.** The chart refuses to render,
and the binary refuses to start the listener on top of that, which is the half
that covers a Deployment somebody edits by hand. Neither is a fatal error for
the pod: the gate, the triage endpoint and the sweep are all still worth
running, so the refusal is a WARNING at every start-up rather than a crash.

That is deliberately unlike `promotionAuth`, whose caller is Kargo inside the
cluster and whose unauthenticated form predates the setting. This is the one
listener built to be reached from outside the cluster, where "a token nobody
set" and "an open API" are the same thing.

It is one shared secret, compared in constant time, and the `Bearer` scheme is
required rather than tolerated: this header is written by clients, gateways and
proxies this project has never seen, and "any credential-shaped string, however
framed" is a wider door than the specification asks for. The ladder past a
static token — a gateway-fronted SSO, then a token verifier — waits for the
audit log to show enough distinct consumers to justify it. Every tool call is
logged before it runs, with the caller's address and with the tool name and its
arguments quoted, so a caller cannot forge a line in the log an operator reads
to find out who asked what.

`mcp.dangerouslyServeWithoutAuthentication` is the way past the token, and it
is spelled to be uncomfortable to type and impossible to skim past. There are
real reasons to want it — a gateway in front that already authenticates every
request, a laptop-bound experiment — and it exists so those people say so on
purpose rather than discovering that leaving the token empty works. The
start-up log says what it is doing, in those words, every time.

## What it discloses

**What it reveals is what the status page and the report comment reveal.**
Every result names the repository; past that, one entry per tool, and together
they are the list to weigh before publishing the port:

- `pipeline_report` — your Stage and Warehouse names, the namespaces the sweep
  examined, and the findings with their remedies: the evidence quotes Kargo's
  own error strings, and a remedy command names the namespace and the Stage it
  acts on.
- `gate_status` — the titles of the open pull requests, the head commits they
  stand against, the blocker counts standing on them, and what stopped a sweep
  that could not list.
- `gate_verdict` — the same for the one asked about, plus your Application,
  rendered-object and cluster names, the chart versions on either side of the
  bump, the helm and schema error strings, the repository paths of the
  manifests still declaring a dropped version, and the values keys the bump
  stops reading.
- `triage_status` — the labels standing on a pull request, and the attempts it
  has spent against the cap.

It serves **no credential**, no prompt, and no rendered diff — no field path
from any tool result can reach one, and that is a compile-time property rather
than a filter, described in
[the safety model](safety-model.md#what-the-mcp-surface-may-reveal-and-what-it-cannot).

What it does serve is **operational metadata**: the same class of org-internal
fact a cluster-wide read of Applications exposes.
[The status page](../charts/bosun/README.md#the-status-page) carries the same
kind of disclosure over a shorter list — the repository, the open pull
requests, your Stage and Warehouse names, the namespaces, and the findings with
their remedies — and it stops there: the gate-side detail behind a verdict, and
the labels and attempt counts beside it, appear only here. On both it is what
to weigh before publishing beyond the cluster. Treat the token as read access
to your pipeline's status handed to a program.

The difference between the two surfaces is who reads them and what they hold.
These answers land in somebody's coding agent, which usually has a shell, a
checkout, and tools bosun refuses for itself.

## One install, one trust domain

An install serves exactly one trust domain, and this surface serves that view
flat and whole: there is no per-project, per-caller or per-Application
filtering here, and a second trust domain means a second install.

The argument is not restated here.
[ADR 0014](../adr/0014-an-install-serves-one-trust-domain.md) has it, including
what a filtered verdict would have to miscount in order to exist.

## What is enforced, and what is left to the client

Enforced, in code, on every call:

- **No mutation.** There is no mutating tool, and the ClusterRole underneath
  has no write verb to build one on.
- **No live read.** Every answer is a snapshot; a request reaches no cluster,
  no git host and no model.
- **No configuration reach.** The `mcp` package imports the result types and
  the redactor and nothing else, so no field path from a result can arrive at a
  credential — checked structurally, over the paths no request exercises.
- **Redaction**, applied where a byte reaches the wire rather than in each
  handler, as a second line under that rule.
- **Composed remedies.** A command in a `remedy` field is assembled by bosun's
  own code from pieces checked against a grammar; a piece that fails costs the
  finding its remedy rather than producing a suspect command.
- **Origin tagging.** Every free-text field that can carry somebody else's
  words says whose they are: `bosun`, or `bosun-quoting-` something. Tool
  descriptions are constants, so nothing from a cluster reaches the field a
  client hands its model as instructions.
- **Stamp stripping.** The gate's own comment-marker grammar is broken in every
  response, so a client that writes bosun's text back onto a pull request
  cannot be made to relay a forged verdict with it.

Not enforced, and stated rather than implied: **bosun cannot make a careless
client safe.** Text sanitised to harmlessness does not exist, and this surface
does not offer it. What it guarantees is provenance labelling and instructions
that are bosun's own or absent. A client that treats an origin-tagged quotation
as an instruction has made a decision bosun cannot take back — the residual
risk is real, it lands in whatever tools that client holds, and the labelling is
what a careful one fences on.

The full account, including the one block of typed facts that is vetted rather
than tagged, is in
[the safety model](safety-model.md#what-the-mcp-surface-may-reveal-and-what-it-cannot).

## See also

- [The pipeline supervisor](supervisor.md) — where the findings come from.
- [Chart: bosun](../charts/bosun/README.md#the-mcp-surface) — the values, the
  refusals, and the network path.
- [ADR 0014](../adr/0014-an-install-serves-one-trust-domain.md) — one trust
  domain per install, and why the readout is not filtered.
