# The MCP surface

Bosun computes the expensive facts in the promotion loop: why a Stage stopped
promoting, why one pull request is blocked, what the agent is doing about it,
and the command that ends each situation. Until this surface existed it
published them as prose: a comment on a pull request, a page behind a
port-forward, a metrics endpoint. A person reading a page gets what they need
from that. An agent has to parse markdown somebody wrote for a human.

This is a fourth exit for facts that already exist. It is a read-only
[MCP](https://modelcontextprotocol.io) server, on a port of its own, behind a
bearer token, off until an operator turns it on. **Nothing here computes
anything**: every tool answers from the snapshot the last sweep left in memory,
so a call reaches no cluster, no git host and no model, and a chatty client
cannot spend an install's rate limit. Nothing here mutates anything either. No
tool does, none is planned, and the chart's ClusterRole has no write verb to
build one on.

## The tools

Each one below gives **what it answers**, then **what it takes**, then whatever
else holds for that tool alone. Two properties hold across all of them, so they
are stated once here. Every result names the repository it is about, so a
client holding tokens for several installs cannot mix two fleets. Every result
carries when the sweep behind it ran and how many seconds ago, so a client can
decide whether to trust the answer or wait for the next sweep.

A tool that asks about one pull request takes its number, plus an optional
`repository`. An install watches one repository, so the argument exists to be
checked. Name a different one and the tool refuses.

The answer each tool gives before anything has looked is one rule for the whole
surface, in [absences and zeroes](#absences-and-zeroes).

### `pipeline_report`

**Answers** what the last pipeline sweep found: promotions that ended without
delivering, Warehouses that stopped discovering, verifications that will not
re-run, tracked pins that write nowhere. Each finding carries a typed kind and
severity, how long it has held, and where one exists the paste-ready command
that recovers it. Worst first.

**Takes** no arguments.

### `gate_status`

**Answers** the queue: every open pull request the last gate sweep saw, with
the verdict standing against each head commit. The state, whether it blocks,
and the blocker breakdown as counts per kind.

**Takes** no arguments.

Call this before `gate_verdict`. It says which pull request to ask about.

### `gate_verdict`

**Answers** why one pull request is blocked, or why it is not. The blocker
counts and every finding behind them, with dropped API versions carried as
fields: which definition, which versions it stopped serving, which one
survives, and the kind of manifest that has to move. Each finding says whether
an edit in the repository could clear it. Alongside the findings, the gate
lists what it could not render, because a clean verdict over a partial render
is a narrower claim.

**Takes** a `pullRequest` number, and an optional `repository`.

### `triage_status`

**Answers** what the agent is doing about one pull request: the phase it is in,
the automatic fix attempts spent against the cap, and the labels standing on
it. Use it to tell an agent that is still working from one that has finished
and will not try again.

**Takes** a `pullRequest` number, and an optional `repository`.

This is the one answer with two clocks. The phase is this process's current
state. The labels and the attempt count are as old as the sweep the result
names.

### `handoff_queue`

**Answers** which open pull requests are waiting on a human: the ones the last
gate sweep saw carrying the `needs-human` label, which is what the agent
applies when it stops short of a mechanical fix and will not act again on its
own. Each one carries the verdict standing against its head commit, meaning the
state, the blocker breakdown and every finding behind it, plus how many
automatic fix attempts it has already spent against its cap.

**Takes** an optional `repository`.

The findings ride with each entry here, and not in `gate_status`, because
different people read the two lists. You scan a queue to choose what to ask
about next, so multiplying every rendered object's name across rows you are
about to discard spends your context for nothing. This list is the work itself.
Every entry on it is a job somebody has already been asked to do, and the files
and settings a repair would have touched are what the handoff is about. The
coverage the run lost stays on `gate_verdict`, because it qualifies a verdict
instead of describing the job.

### `verdict_history`

**Answers** what the gate said about one pull request on each of its earlier
head commits: the commit, whether that verdict blocked, and the gate's own
headline for it. Newest first, and the result says so instead of leaving a
client to infer it. Use it to tell a push that fixed something from a gate that
changed its mind, and to count flips instead of reading headlines.

**Takes** a `pullRequest` number, and an optional `repository`.

This is the one answer bosun reads off the git host instead of computing here.
A gate with no database keeps its memory as HTML comments inside its own
comment on the pull request, the only per-pull-request storage a git host
offers. The publish path parses those stamps on every run, and this tool
publishes the parse instead of dropping it. So the result names that comment as
its source, which anybody who can write to the repository can edit, and
publishes the cap on how many verdicts the comment remembers beside the
entries. As many entries as the cap means bosun dropped older ones, and the
history on the wire is not the pull request's whole life.

Two things follow from where the entries come from, and neither is hidden:

- **The entries are what that comment recorded, not every head the pull
  request has had.** The gate refreshes them when it publishes onto the
  comment. A head commit whose run was short-circuited, meaning a verdict
  already stood on the git host and this process did not re-litigate it, is
  missing from them, as is the verdict standing now.
- **An entry's `headCommit` is absent when what the comment held is not
  written the way a commit is.** It is the field a caller lines up against its
  own git log, and it carries no origin to fence it by, so bosun holds it to a
  hash's alphabet. The entry itself still gets published, because losing it
  would lose the flip. Absence there means bosun would not vouch for the value,
  and never that the verdict had no commit.

## No resources, and no sampling

The surface offers tools and nothing else. The tempting resource is the
supervisor's markdown report served whole, and it mixes trusted and untrusted
text with no field boundary to hang provenance on, which the typed tools exist
to avoid. Sampling stays out too: a caller that could ask this process to call
a model could spend the install's budget and steer its prompts.

## Absences and zeroes

The rule the whole surface is built around: **"nothing found" is
unrepresentable unless something looked.**

The HTTP surfaces say this with a `503` before the first sweep completes. JSON
has one shape that carries the same distinction, and it is absence.

Two rules hold whatever tool is asked:

- **Every result carries `swept`**, and `sweptAt`/`ageSeconds` stay absent
  until it is true.
- **A collection is absent until something has looked**, and empty only after
  something looked and found nothing. Read the second as the first and you make
  the most expensive mistake this project exists to catch.

Then one entry per tool, because what there is to be absent differs:

- `pipeline_report` publishes `findings` as **absent** before the first sweep
  and as an **empty list** after a sweep that found nothing. Alongside them the
  sweep gives its own accounting of what it managed to examine, so a report
  with no findings can prove it looked; `clean` is true only when it did.
- `gate_status` publishes `open` as absent when nothing has listed pull
  requests, which covers the state before the first sweep and after one that
  could not reach the git host. It is empty only when a sweep listed them and
  there were none. A sweep that failed says so in a field of its own, and the
  pull requests an earlier sweep saw sit beside that error rather than getting
  dropped: stale evidence beats none, as long as it is labelled stale.
- `gate_verdict` answers a pull request with no verdict standing **as such**,
  and never as a passing one. There are four ways to have no verdict: a render
  in flight, a verdict on the git host this process did not re-run, a gate that
  could not run, a sweep that could not list. Each has its own state and its
  own sentence.
- `triage_status` always answers with a phase, because this process knows what
  it is running, and publishes the labels and the attempt count only for a pull
  request the last sweep saw. Absent there rather than zero, so nothing claims
  on the agent's behalf that it has spent no attempts. Three of its phases are
  the three ways of not knowing: `unswept` (nothing has looked), `unknown` (the
  sweep could not list) and `absent` (the sweep looked and did not see this one
  open).
- `handoff_queue` publishes `waiting` under `gate_status`'s rule, held-over
  queue included, and the stakes are higher: absent when nothing has listed pull
  requests, empty only when a sweep listed them and none carried the label. An
  empty queue from a gate that could not reach the git host would say "nobody is
  waiting on you" to the one caller who acts on that by going home. So where an
  earlier sweep's listing survives, bosun publishes it beside the error, and the
  sentence beside it says the queue is older than the sweep time above it. The
  attempt cap gets published either way, because an operator configured it
  instead of the sweep reading it from the world.
- `verdict_history` publishes `entries` only under the state `recorded`, which
  means a comment was read: **empty** then says the comment recorded no earlier
  verdict, and **absent** says there was no comment to read a history from.
  Neither is a claim that the gate has never blocked the pull request. Its
  `unswept` and `absent` are `gate_verdict`'s, meaning what they mean there.
  Its `unknown` is wider than `gate_verdict`'s: there it is only "the sweep
  could not list", here it is that **and** the ordinary "the sweep saw this
  pull request and no comment has been read for it", which is the common answer
  rather than the broken one. `sweepError` is what separates the two, and the
  `status` sentence says which is which.

One consequence worth knowing before it surprises you: with
`supervise.enabled: false` there is no sweep to read, so `pipeline_report`
reports that nothing has looked for as long as that stays true. The pod logs a
line saying so at start-up, because from the client's side that state looks
the same as an install whose first sweep has not finished.

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

The chart writes no route for this port. To publish it beyond the cluster you
write a Gateway or Ingress in front of a port whose peer list is
`mcp.allowFrom`. Look at it over a port-forward first, before you decide
whether to publish it at all:

```bash
kubectl -n bosun port-forward deploy/bosun 8082 &
curl -sS -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://localhost:8082/mcp
```

The transport is streamable HTTP with a JSON response body, at `/mcp`, `POST`
only. Every tool answers from a snapshot the process already holds, so there is
nothing to push, and a `GET` would open a stream a client holds forever to
receive nothing.

Outside the chart, the settings are environment variables: `MCP`, `MCP_ADDR`
(default `:8082`), `MCP_TOKEN`, and
`MCP_DANGEROUSLY_SERVE_WITHOUT_AUTHENTICATION`. The token also reads from
`MCP_TOKEN_FILE`. Prefer that form, and it is the form the chart writes: a
credential the process reads from a mounted file stays out of its environment,
where anything that dumps one could copy it.

## The token

**Without a token the listener does not start.** The chart refuses to render,
and the binary refuses to start the listener on top of that, which covers a
Deployment somebody edits by hand. Neither is a fatal error for the pod. The
gate, the triage endpoint and the sweep are all still worth running, so the
refusal is a WARNING at every start-up rather than a crash.

`promotionAuth` works differently on purpose: its caller is Kargo inside the
cluster, and its unauthenticated form predates the setting. This is the one
listener built to be reached from outside the cluster, where "a token nobody
set" and "an open API" are the same thing.

It is one shared secret, compared in constant time. The `Bearer` scheme is
required rather than tolerated, because clients, gateways and proxies this
project has never seen write this header, and "any credential-shaped string,
however framed" is a wider door than the specification asks for. The ladder
past a static token, a gateway-fronted SSO and then a token verifier, waits for
the audit log to show enough distinct consumers to justify it. Bosun logs every
tool call before it runs, with the caller's address and with the tool name and
its arguments quoted, so a caller cannot forge a line in the log an operator
reads to find out who asked what.

`mcp.dangerouslyServeWithoutAuthentication` is the way past the token, and it
is spelled to be uncomfortable to type and impossible to skim past. There are
real reasons to want it, such as a gateway in front that already authenticates
every request, or a laptop-bound experiment. It exists so those people say so
on purpose rather than discovering that leaving the token empty works. The
start-up log says what it is doing, in those words, every time.

## What it discloses

**This surface reveals what the status page and the report comment reveal.**
Every result names the repository; past that, one entry per tool, and together
they are the list to weigh before publishing the port:

- `pipeline_report`: your Stage and Warehouse names, the namespaces the sweep
  examined, and the findings with their remedies. The evidence quotes Kargo's
  own error strings, and a remedy command names the namespace and the Stage it
  acts on.
- `gate_status`: the titles of the open pull requests, the head commits they
  stand against, the blocker counts standing on them, and what stopped a sweep
  that could not list.
- `gate_verdict`: the same for the one asked about, plus your Application,
  rendered-object and cluster names, the chart versions on either side of the
  bump, the helm and schema error strings, the repository paths of the
  manifests still declaring a dropped version, and the values keys the bump
  stops reading.
- `triage_status`: the labels standing on a pull request, and the attempts it
  has spent against the cap.
- `handoff_queue`: which pull requests the agent gave up on, and everything
  `gate_verdict` reveals about each of them.
- `verdict_history`: the verdicts the gate reached on that pull request's
  earlier head commits, with their commits and the gate's headlines, which name
  blocker counts and kinds.

It serves **no credential**, no prompt, and no rendered diff. No field path
from any tool result can reach one, and the compiler enforces that rather than
a filter, as described in
[the safety model](safety-model.md#disclosure-and-limits-of-the-mcp-surface).

It serves **operational metadata**: the same class of org-internal fact a
cluster-wide read of Applications exposes.
[The status page](../charts/bosun/README.md#the-status-page) carries the same
kind of disclosure over a shorter list. It names the repository, the open pull
requests, your Stage and Warehouse names, the namespaces, and the findings with
their remedies, and stops there. The gate-side detail behind a verdict, and the
labels and attempt counts beside it, appear only here. On both surfaces this is
what to weigh before publishing beyond the cluster. Treat the token as read
access to your pipeline's status handed to a program.

The two surfaces differ in who reads them and what they hold. These answers
land in somebody's coding agent, and that agent holds a shell, a checkout, and
tools bosun refuses for itself.

## One install, one trust domain

An install serves one trust domain, and this surface serves that view flat and
whole. There is no per-project, per-caller or per-Application filtering here,
and a second trust domain means a second install.

[ADR 0014](../adr/0014-an-install-serves-one-trust-domain.md) carries the
argument, including what a filtered verdict would have to miscount in order to
exist.

## Enforced in code, and left to the client

Bosun enforces these on every call:

- **No mutation.** There is no mutating tool, and the ClusterRole underneath
  has no write verb to build one on.
- **No live read.** Every answer is a snapshot; a request reaches no cluster,
  no git host and no model.
- **No configuration reach.** The `mcp` package imports the result types and
  the redactor and nothing else, so no field path from a result can arrive at a
  credential. A structural check covers the paths no request exercises.
- **Redaction**, applied where a byte reaches the wire rather than in each
  handler, as a second line under that rule.
- **Composed remedies.** Bosun's own code assembles a command in a `remedy`
  field from pieces checked against a grammar. A piece that fails costs the
  finding its remedy, and no suspect command reaches the wire.
- **Origin tagging.** Every free-text field that can carry somebody else's
  words says whose they are: `bosun`, or `bosun-quoting-` something. Tool
  descriptions are constants, so nothing from a cluster reaches the field a
  client hands its model as instructions.
- **Stamp stripping.** Bosun breaks the gate's own comment-marker grammar in
  every response, so a client that writes bosun's text back onto a pull request
  cannot be made to relay a forged verdict with it.

One thing stays unenforced, and it is stated here rather than implied: **bosun
cannot make a careless client safe.** Text sanitised to harmlessness does not
exist, and this surface does not offer it. It guarantees provenance labelling,
and instructions that are bosun's own or absent. A client that treats an
origin-tagged quotation as an instruction has made a decision bosun cannot take
back. The residual risk is real, it lands in whatever tools that client holds,
and a careful client fences on the labelling.

[The safety model](safety-model.md#disclosure-and-limits-of-the-mcp-surface)
carries the full account, including the one block of typed facts bosun vets
rather than tags.

## See also

- [The pipeline supervisor](supervisor.md), where the findings come from.
- [Chart: bosun](../charts/bosun/README.md#the-mcp-surface), for the values,
  the refusals, and the network path.
- [ADR 0014](../adr/0014-an-install-serves-one-trust-domain.md), for one trust
  domain per install, and why bosun does not filter the readout.
