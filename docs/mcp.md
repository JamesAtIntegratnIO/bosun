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

Where bosun's own model is what asked for the human, the entry also carries
`escalationReason`: the sentence the model gave for stopping. It is tagged
`bosun-quoting-model`, and that origin exists for this field alone.

Everything else you fence on this surface is an identifier or a program's
output. A chart-rendered name, helm's refusal, a validator's verdict, a title
somebody typed: bosun either wrote the sentence around it or quoted a string a
program produced and you could reproduce. This one is prose, written by a
model, explaining something. That is what an injected instruction wants to look
like, and it arrives in the field an on-call agent reads first.

**What a client is expected to do with it.** Render it inside the same fence
you put every other `bosun-quoting-` field in, and show it to whoever picks the
job up: it is evidence about the work. Do not lift it into a heading, a commit
message, a prompt, or anything you hand your own model as an instruction.

Absence means bosun holds no reason, and never that the agent stopped for none.
Bosun escalates on process facts of its own, meaning a push that failed or an
attempt cap that is spent, and holds no model sentence for those; a reason is
dropped when the pull request leaves the queue, and none survives a restart.
What the agent said is on the pull request, in the comment it wrote.

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

### `inventory`

**Answers** what this fleet runs: every Application bosun's last live reading
of ArgoCD served, with the cluster each one lands on and the chart each one
renders from. Use it to answer "where does this run" and "what version is it
on" without a cluster credential of your own.

**Takes** no arguments.

**Names and versions only.** No manifest, no values file, no values leaf and no
rendered object crosses this boundary, by any argument, and the result type has
nowhere to put one. A structural check covers that, instead of leaving it to
what a handler happens to fill in. This is the tool most able to become a manifest
proxy one small field at a time, so the line is drawn on the type. The render
this joins from carries the values files and the values leaves behind every
Application, and they stop where the row is copied.

**Bosun does not filter the rows by repository.** They are every Application
the install's ArgoCD credentials can see, which on most control planes is more
than the one repository this install gates. An install's intake sets its
horizon, and its readout follows, per
[ADR 0014](../adr/0014-an-install-serves-one-trust-domain.md). So narrow the
ArgoCD account to narrow this, and do not ask the tool for less.

**It is the one result whose age is not the sweep's**, and this is the sentence
to read before trusting a row. The reading is made when the gate *renders* a
pull request, not when the sweep runs, so an install with nothing open makes
none: the fleet stays as old as the last pull request, and on a brand-new
install there is no reading at all. Every row therefore carries its own
`observedAt` and `observedAgeSeconds` beside the result's `sweptAt`, and no
tool call is allowed to go and refresh it. That staleness is published rather
than solved; if it becomes the thing operators complain about, the honest fix
is a scheduled fleet read, which is a change to what the sweep does rather than
to what a tool call may do.

**A row has two sources, and it says which gave it what.** The live reading
knows which Applications exist and where they land; it does not know what any
of them renders from. That comes from the gate's *render expansion*, which is a
different observation of a different thing: the repository at the revision the
last run started from, rather than the control plane as it is now. Neither
source answers the other's question, so both are kept and every row carries
both stamps.

The merge rule, which is what a client is relying on:

- **The live reading decides which rows exist.** Nothing the expansion knows
  adds one.
- **A row the expansion did not know of has no `renders` block at all**, and
  never another Application's chart. The rows are every Application the ArgoCD
  account can see, the expansion covers what the gated repository defines, and
  the first is routinely wider than the second.
- **An Application the expansion knows and the reading does not have is no
  row.** The expansion describes an older revision, and a fleet member that is
  not there is the worse of the two errors.
- **Two Applications of one name on one cluster leave both without chart
  detail.** That is what apps-in-any-namespace permits, and the namespace that
  would tell them apart is not a field both observations have: the expansion
  knows where an Application deploys *to*, and the reading knows where the
  Application object *lives*.
- **Every row carries `observedIn`, `observedAt` and `observedAgeSeconds`
  twice**, once for its identity and once inside `renders`. A row whose two
  halves were observed hours apart says so, and reading one number for both
  would be trusting the fresher stamp for the staler half.

`renders` carries `sourceType` (`helm` or `path`), and for a chart the
`chart`, `chartRepository` and `version` pinned to it, plus the
`applicationSet` it was generated from. An Application the repository commits
directly was generated by nothing and carries no `applicationSet`; one that
renders a directory carries no chart. Both absences are answers.

**Everything under `renders` came out of `helm template`**, which applies
nothing, so it never reached an apiserver and has no object's grammar behind
it. It is tagged `bosun-quoting-chart` for that reason, where a row's own name
and cluster are tagged `bosun-quoting-cluster`. The one thing the expansion
knows that never reaches you is the Application name it rendered: that is the
key the two observations are joined on, and the name you read is always the
reading's.

**`chartDetail` is a claim about the expansion, not about the rows.**
`expanded: false` means no run has rendered one, so the charts are not
unpinned and nothing has read what they render from. `expanded: true` with no
row carrying `renders` is its own answer: a render was read and knew none of
the Applications ArgoCD is serving.

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
  instead of the sweep reading it from the world. An entry's `escalationReason`
  is absent whenever no reason is held for that pull request, which is a
  narrower claim than "the model was silent": bosun also stops for reasons of
  its own, forgets a reason when the pull request leaves the queue, and holds
  none across a restart.
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
- `inventory` publishes `applications` when a live reading has been made and
  withholds it otherwise, and **the sweep decides nothing about that**. The two
  are separate claims on one result. No reading yet: the rows are absent, and
  the sentence says whether a sweep has completed too, because "nothing has
  looked at all" and "the gate has swept but rendered nothing" are different
  situations on the same install. A reading was made: `applications` is present,
  and **empty** only when that reading served no Application at all. The
  separation is load-bearing rather than tidy: a sweep stamps itself only once
  every pull request it started has been answered, and the reading happens
  inside that, so a process that gated its rows on the sweep would deny holding
  a fleet it was holding. Absence inside a row is its own answer too: a row with
  no `cluster` is one whose destination resolved to no cluster the inventory
  knows, which is two ArgoCD reads disagreeing rather than an Application with
  nowhere to go, and a row with no `renders` is one the gate's last render did
  not know of rather than an Application that pins nothing. `chartDetail` says
  which of those two absences a reader is looking at, because `expanded: false`
  is "nothing has rendered" and `expanded: true` with no `renders` anywhere is
  "something rendered and knew none of these".

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
- `handoff_queue`: which pull requests the agent gave up on, everything
  `gate_verdict` reveals about each of them, and, where bosun's own model is
  what asked for the human, the sentence it gave for stopping. That sentence is
  the one thing on this surface a model wrote rather than a program, and it is
  the model's reading of your change, so weigh it as you would the explanation
  it already writes onto the pull request.
- `verdict_history`: the verdicts the gate reached on that pull request's
  earlier head commits, with their commits and the gate's headlines, which name
  blocker counts and kinds.
- `inventory`: your fleet, meaning Application names, the namespaces those
  objects live in, and the cluster names they land on, plus the chart each one
  renders, the chart repository it is served from, the version pinned to it and
  the ApplicationSet it was generated from. Every Application the install's
  ArgoCD account can list, and not only those of the gated repository, so weigh
  this entry if that account is broader than the team holding the token.

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
  client hands its model as instructions. Text a model wrote gets an origin of
  its own, `bosun-quoting-model`, rather than being folded into an origin that
  means an identifier or a program's output.
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
