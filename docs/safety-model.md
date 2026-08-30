# Safety model

The agent can write to a repository and spend money. Code bounds both, not
instructions to a model.

## The model proposes; the harness applies

The model returns structured answers: a classification, an explanation, and on
each of the three paths that write, a proposal. It has no file-edit tools, no
shell and no path to the repository. The harness writes every byte that reaches
a branch, and only after the proposal has passed the checks below.

**The model does author file content**, and that boundary is narrower than it
sounds. The new value of a scalar edit is written to the line as the model
spelled it. A reshaped manifest and a migrated values file are whole documents
it wrote. What it never does is *apply* any of them, decide which files are
writable, or reach a path the deny-list covers. "The model does not edit files"
is a claim this page does not make, and no other page should either:
[ADR 0001](../adr/0001-structured-edits-not-agentic-loop.md)'s title was written
in a scope two later decisions widened.

Nor is it one call. One classifies the pull request. The reshape calls the
model again per document, up to `triage.migrateMaxDocs`, and the values
migration once per Application whose chart will not render.

A model holding file-edit tools can make a red gate green by deleting the
check, and from outside that looks the same as a repair. Handing it a whole
document to write is a smaller grant than that, and the difference is that a
document is checked against a schema the model does not control.

## A proposal is either scalar edits or one migrated document

Which of the two it is decides how the harness checks it.

**Scalar edits** name a file, a key, a `from` and a `to`. The harness
corroborates each against the file it names: the key must already resolve to an
existing scalar, and the `from` must equal what the file holds.

**A migrated document** is one whole object, reshaped for a target schema, and
the harness uses it where a plain `apiVersion` swap would leave behind fields
the target schema prunes. The model authors the complete document here, file
content rather than a value to swap, so corroboration does not apply: a reshape
leaves no `from` to match. Three deterministic checks run on the output
instead.

| Check | What it requires |
|---|---|
| Identity | `apiVersion` equals the target the gate named; `kind`, `metadata.name` and `metadata.namespace` are byte-identical to the original |
| Schema validity | the proposal is walked against the target schema by the same code that found the problem |
| Value provenance | every scalar leaf appears as a scalar leaf in the original, or is dictated by the target schema itself: a default, an enum member, a const |

What lands is re-serialised from the validated structure rather than written
back as the model's text, so the bytes on disk are a function of a structure
the harness checked. The model supplies structure; the original document
supplies data.
[ADR 0007](../adr/0007-structure-from-the-schema-data-from-the-document.md)
records why the proposal surface includes whole documents and what replaced
corroboration.

### When the document is a chart's values

The same shape, pointed at a chart's `values.schema.json` instead of a CRD's,
and repairing a louder failure: helm checks that schema before it templates
anything, so a repository whose values a chart has outgrown does not render at
all. Removing a key, renaming a key and adding a key are the three operations
that fixes, and a scalar edit can express none of them.

Two of the three checks are unchanged. Two things are not.

**The first check is survival rather than identity.** A values document has no
kind and no name to preserve. What it has is everything the new chart still
declares, and all of that must come through byte-identical — which is also
what stops a displaced value landing on a key that already had one.

**What lands is a plan, not the document.** ADR 0007 re-serialises a migrated
manifest and pays for it in comments; a manifest is usually a document of its
own, so that is a fair price. A repository's chart values are usually a subtree
of a file that also holds thirty other addons, and the values that carry a note
beside them are exactly the ones somebody had to reason about. So the harness
diffs the original against the validated proposal into a plan — remove a key,
rename a key, set a key — and applies each one on that key's own lines, the way
a scalar edit is written. The model never names a file, a key or an operation;
it returns a document, and the plan is computed from two structures that were
checked first.

And one guarantee this path has that the manifest path cannot: **the chart is
rendered with the proposal before anything is written.** A migrated manifest is
judged by a schema walk; a migrated values file is judged by the program that
refused the original.

Where the repair ends is decided before a model is asked anything. A key the
target schema requires, that the values do not supply and that the schema names
no default, `const` or single-member `enum` for, has an answer only a person
holds; that escalates with the key named.
[ADR 0013](../adr/0013-a-values-migration-is-a-plan-not-a-document.md) records
the boundary and what it costs.

## Migrating off a dropped version involves no model at all

Migrating manifests off a CRD version a bump stopped serving takes no
judgement: the consumer kind, the dropped versions and the surviving
destination are all computed from the rendered CRDs. The `migrate` package
reads them back, the same package that wrote them, and rewrites nothing but
apiVersion values matching them, only when that finding is the gate's only
blocking one.

It does not read them out of the prose, and it used to. Half of a report
bullet is a sentence the gate composes and half is the name of an object some
chart rendered, so a chart putting a backtick or a newline in `metadata.name`
writes a whole finding line of its own — and a finding line was the contract
the migrator executed, against policy-allowed manifests, to whatever
destination the chart named. The gate now emits a
`<!-- gitops-gate:dropped … -->` block built from its own structured findings
and `migrate.ParseReport` reads that instead. Every field in it is anchored to
a shape that cannot spell a space or a `>`, so no value can end the comment
early or forge a second entry, and a report carrying the marker is never
scraped for prose as well: a fallback "just in case" would reopen the way in.
The report a person reads and the instruction a repair runs are the same facts
and deliberately not the same bytes.

The guarantees below still hold where they apply: the deny-list and the
allowlist answer for every file, the rewrite is a value replacement on the
scalar's own line, the attempt cap counts these pushes too, and the re-run gate
re-counts the consumers itself. The one deliberate difference is `Scope`:
consumers are by definition files the promotion did not touch, and the gate
named them rather than a model.

## What is enforced, and where

| Guarantee | Mechanism |
|---|---|
| Cannot edit CI config, the gate, or the merge policy | `edits.DefaultDeny`, checked before any write and not overridable by configuration |
| Cannot edit outside the configured area | `Policy.Allow`; an empty allowlist refuses everything, and the process refuses to start with one |
| Cannot edit a file this change did not touch | `Policy.Scope`, the paths `git diff` reports between the merge base and this head. `Allow` is a standing grant and deliberately coarse; `Scope` is what *this* pull request is about. Without it the prompt asks for the files this pull request may change while the applier accepts anything under the standing grant, which makes a guarantee into an instruction. It used to be the promotion body's own `files` array, which is a claim about the pull request rather than a fact about it: an empty one turned the narrowing off entirely, since `Scope` is enforced only when it is non-empty, and any other one widened it back to whatever the standing grant permits. The body still carries `files` and the prompt still asks the model for those; nothing is enforced from it. A pull request whose base cannot be read, or that changes no file at all, escalates without writing, because the alternative on that path is the unscoped applier this replaced. The base is the *merge base* and not the base branch's tip, which is the same correction the gate's own base side needed: diffed against a tip that moves, every file any other pull request merged after this branch was cut joined the scope, so the guarantee held exactly as long as nothing else merged |
| Cannot overwrite a value it misread | the edit's `from` must equal what the file holds, compared unconditionally: an empty `from` matches an empty scalar and nothing else |
| Cannot rewrite a neighbouring token | the replacement is anchored to the scalar's own line **and column**, so `b` in `{a: old, b: old}` and the value in `version: version` are the tokens that change |
| Cannot invent a version | version-shaped values must appear in the evidence the model was shown |
| Cannot add or restructure with a scalar edit | the key must already resolve to an existing scalar. Restructuring is only available on the document path, under the three checks above |
| Cannot escape the checkout | `safepath.Resolve`, on every read and every write. A lexical test answers a question about strings while `ReadFile` asks one about the filesystem, so containment is both: `..` is rejected, and a path is refused outright if any component of it is a symbolic link. A tracked link at a permitted path is what would otherwise reach a Secret mounted in the pod, or a denied file elsewhere in the repository |
| Cannot retry forever | attempt cap, tracked by pull-request label. The label is written **before** the push and the push is refused if it cannot be written, so a token that may push and may not label escalates instead of looping |
| Cannot write to the default branch | the only push path targets the pull request's own branch |
| Cannot block a merge | its commit status is never a failure state, whatever the verdict. A red status here would make the agent a second gate; the description carries the meaning instead of the colour. It is `pending` while triage runs and `success` once there is a verdict. Pending on a check nobody requires blocks nothing, and it is what stops "still thinking" reading as "nothing to say" |
| Cannot reach an internal network | `egress.DefaultDenyNetworks`, applied on the dialler: loopback, link-local (169.254/16, and so the cloud metadata service), RFC1918, CGNAT and the IPv6 equivalents. Not a default with an override — the same shape as `edits.DefaultDeny` — and an operator opens one network at a time by naming it in `triage.egressAllowPrivate`. Because it is checked where the address is known rather than on the host string, it holds for a name whose owner points it into that space after the string was read, for `http://2852039166/`, and for `[::ffff:a9fe:a9fe]`. Three things it does not cover, and the two sections below say which |
| Cannot reach a public host an operator forbade | `triage.egressDeny`, matched on the host, with every outbound request logged and every refusal logged with the rule that stopped it. This is accountability after the fact, not permission before it: the public default is open, on purpose, and the paragraphs below are the honest version of what a NetworkPolicy adds to it |
| Cannot be aimed at a host this repository does not name | a promotion's `artifact` field becomes an outbound request, and its host is the caller's string verbatim. Before one is made, the checkout is searched for that host; nothing in the repository naming it means no request at all and a note saying so. Both sinks ask it: the upstream lookup, and the schema probe that runs `helm template` against the version being promoted to. The second matters more than it looks, because it is the one that downloads an archive, and because helm resolves and dials for itself where an `http.Client` guard cannot follow. A substring search rather than a parse of a named field, because `repoURL` is Argo CD's spelling and `spec.url` is Flux's and picking one would be an assumption about repository layout. Without it the endpoint fetches whatever it is told to and publishes the outcome on a pull request. Every other failure degrades the same way: a render-only explanation that says what it could not read |
| Cannot present a guess as a source | the upstream repository comes from the publisher's own `org.opencontainers.image.source` label, never from parsing a registry path. A guessed repository returns another project's notes, which a reader cannot distinguish from the right ones |
| Cannot turn testimony into evidence for a write | release notes and upstream commits are fetched **only** on the paths that produce prose, the green-gate explanation and an escalation. The mechanical path, the one that writes files, does not fetch them, so they are not in the evidence string the applier corroborates against. Without that, a commit message containing `v1.5.0` would make `v1.5.0` a corroborated value to write |
| Cannot choose its own supporting evidence | `migrate.Subjects` decides which upstream commits are shown, from the kinds and resource names in the gate's own findings, matched by string against commit messages and diff paths. If the model picked the commits that support its conclusion, the check and the claim would come from the same source |
| Cannot compare a range it cannot establish | a chart version and the git tags of the project it packages frequently use different numbering. Refs come from the project's own release tags or from the publisher's recorded build revision; when neither meets the promotion's versions, no comparison is made and the note says why. Two refs picked out of the wrong numbering return real commits from a range that is not this one |
| Cannot mutate the cluster | live reads are `get` and `list` only, and the chart's ClusterRole has no `create`, `update`, `patch` or `delete` verb anywhere. They are off by default: everything else the agent reads is public or already in the pull request, and this reads the operator's cluster |
| Cannot send its own credentials to the model | the git token, the GitHub App private key, the ArgoCD token, the promotion token and the model API key are read once at start-up by `config.go` and handed to one client each. `prompt/` names none of them, and nothing that builds a prompt has a reference to reach. The API key is the only one that touches the provider at all, as the `Authorization` header of the call; it is not in the body. The model is sent the pull request, the gate's report and the rendered output, and it returns a verdict and a proposal |
| Cannot publish a credential a host quoted back at it | every credential `config.go` loads primes one process-wide redactor at start-up, and text this process did not author goes through it before it is logged, posted, or wrapped into an error. Today that is what `git` prints on a failed push, on both providers, including the installation token minted for that one push, which start-up never saw. This is the second line and not the first: the first is that each credential is handed to one client and nothing else holds a reference to reach. It is also the narrow claim -- redaction removes this process's own secrets from a string, and does not make a string safe |
| Cannot read a Secret | with `liveReads.enabled: false` the core API group is not granted at all, and with `liveReads.scope: groups` it is granted no further than `pods, events`. The chart creates no Role over Secrets anywhere. `liveReads.scope: wide` grants `apiGroups: ["*"]` cluster-wide and **can** read Secrets everywhere. RBAC has no deny rules and no way to subtract the core group, which is why "everything except Secrets" is not a setting |
| Cannot be given a Secret read it does not need | the gate's cluster inventory comes from ArgoCD's API, not from the cluster Secrets those clusters are stored in. That read could not be made small enough: the gate wants four fields, and RBAC has no predicate for "the labels but not the data". There are no deny rules, `resourceNames` does not apply to `list`, and the apiserver applies the request's label selector *after* authorising. `GET /api/v1/clusters` serves the same four fields with the credential block redacted, which draws that line. The trade is real: an ArgoCD account token to mint and rotate, and a component that can be down on its own |
| Cannot present "nobody looked" as "nothing found" | `cluster.Count` carries a `Known` flag and its rendering prefers the note over the number. A refusal, an unreachable apiserver, or a count where one version answered and another did not all say what was *not* checked. The prompt tells the model in those words that "not permitted to check" is neither zero nor evidence of safety |
| Cannot invent data when it reshapes a document | the harness refuses a proposed document migration unless every scalar value in it appears in the original document or is dictated by the target schema itself: a default, an enum member, a const. Field names come from the schema; data comes only from the document. This is the document-level analogue of "cannot invent a version", and it keeps the model translating the document rather than adding to it |
| Cannot change what an object is while reshaping it | `apiVersion` must equal the target the gate named, and `kind`, `metadata.name` and `metadata.namespace` must be byte-identical to the original. A renamed object is a second change riding inside a migration |
| Cannot propose a document that still does not fit | the proposal is walked against the target schema by the same code that found the problem, so the apiserver's objection is raised before the apply |
| Cannot half-migrate a pull request | if any document in a pass is refused, **nothing** is pushed, including the plain swaps that were fine. The swap alone turns the gate green, because no manifest declares a dropped version any more, while a document the target schema rejects waits to be pruned. A partial push hides a broken change behind a green gate |
| Cannot retune a setting a values migration was not about | the survival check: every value the target chart version still declares must be at the same path in the proposal, byte-identical. A setting quietly changed on the way past is a second change riding inside the migration, and it is also how a renamed key would overwrite one that already had a value |
| Cannot write a values migration the chart still refuses | the chart is pulled and rendered with the proposed values before anything is written, and a proposal helm will not template is refused. This is the one check that catches a key the chart *renamed* being dropped as though the chart had removed it, whenever the chart insists on the key it renamed |
| Cannot guess a required value nothing supplies | a key the target schema requires, that the values do not hold and that the schema names no default, `const` or single-member `enum` for, escalates **before** the model is called. There is nothing to derive an answer from, so asking for one is asking for an invention |
| Cannot reformat a values file it edits | the write is a plan of key operations, each applied on that key's own lines through `yaml.Node`, so comments, indentation and quoting elsewhere in the file are untouched. A key inside a flow mapping, a value that is not a scalar, and a section that does not exist are all refused rather than improvised |
| Cannot write to a values file it cannot uniquely identify | the file and the prefix a chart's values live under are discovered by matching the keys the plan touches, with their current values, against the files this change may write to. Zero matches or several is a refusal: a wrong guess here edits somebody else's addon |
| Cannot drop a value silently | values present in the original and absent from the proposal are listed in the comment. Some are correct, because a field the target no longer accepts has to go somewhere and sometimes nowhere, and all are visible |
| Cannot forge a marker in what it publishes | both halves find their own comments by substring-matching an HTML comment: `<!-- gitops-gate -->` for the report, `<!-- gitops-gate:head <sha> -->` for "this commit already has a verdict", `<!-- bosun:explanation -->` for "this pull request has already been explained". The model's summary, reasoning and notes are the one place a pull request's own words are reproduced verbatim, so `<!--` and `-->` are escaped there and each field is capped, with the truncation said out loud. A forged head stamp would not produce a wrong verdict; it would make the gate's own duplicate-suppression swallow the next one, so the commit ends with no verdict on it and nothing saying so. **The rest of the comment is not escaped.** Paths, values, table cells and the folded diffs come from the repository rather than the model, and escaping a diff would misreport the file, so a repository file containing a stamp can still forge that same suppression |
| Cannot act without saying so | every exit path publishes a commit status, including the ones that do nothing and the ones that error. Without one, "nothing needed triage", "never called" and "crashed" are the same observation from outside |

## What the NetworkPolicy enforces depends on your CNI

The egress rows above are the agent's own guarantees, in Go, on its own
dialler. The chart's NetworkPolicy is the second layer, and it is weaker than
its values file reads:

- `networkPolicy.egress.fqdns` and `networkPolicy.egress.fqdnPatterns` render
  **only** under `networkPolicy.flavor: cilium`, inside a
  `CiliumNetworkPolicy`. The default is `flavor: standard`, and a standard
  NetworkPolicy has no way to name a host at all, so with the default flavor
  those two keys are read by nothing.
- On `standard`, `networkPolicy.egress.allowPublicHTTPS: true` renders
  `cidr: 0.0.0.0/0` on port 443, excepting the RFC1918 blocks, link-local
  (169.254/16) and CGNAT (100.64/10) — the same space
  `egress.DefaultDenyNetworks` closes. That is any public host, not a named
  list. It is worth having anyway: this layer governs helm and git too, which
  are subprocesses the agent's own dialler never sees.
- `triage.egressDeny` is a list the agent applies to itself and logs. It is a
  record of where it went and a way to forbid a destination by name, not a
  boundary a compromised process is held inside.

So "it talks to the registries you named and nothing else" is true on Cilium
and false on standard, and this document said it flatly for both. Where the
cluster can express an FQDN allow-list, prefer it. Where it cannot, the honest
description is: any public host on 443, with a logged record of which ones,
and the internal networks closed underneath by `egress.DefaultDenyNetworks`
whatever the policy says.
[ADR 0011](../adr/0011-public-is-open-internal-is-closed.md) records why that
split is the trade rather than an oversight, and what it costs.

## helm is a subprocess, and no Go code contains it

The gate deletes `env`, `expandenv` and `getHostByName` from the sprig function
map before it renders an ApplicationSet. That template comes from the pull
request under review, while the process holds the git token, the model key and
the App private key, and the rendered output is published into a comment:
`{{ env "GIT_TOKEN" }}` left in is a credential read with a publishing
side-channel.

That strip covers the **in-process** renderer only. `helm template` runs as a
child process and Helm removes `env` and `expandenv` and nothing else, so a
chart committed by a pull request can call `{{ getHostByName "…" }}` and the
address it resolves renders into the published report. The address helm
connects to is outside the dialler too: `egress` checks a chart reference's
host and logs it, and then helm resolves and dials on its own. Two smaller
cases sit beside it — behind a proxy the address checked is the proxy's, and a
caller supplying its own `RoundTripper` keeps its own dialler.

Which chart helm is pointed at is a different question from what that chart
does once it renders, and only the first one is answered. The schema probe
refuses an artifact the checkout does not name, so the subprocess is not a way
to reach a host of the caller's choosing. A chart the repository legitimately
tracks is still rendered, and the function is still in its function map.

None of the three is closed here, and the honest thing is to say so rather than
describe the boundary as if it held. A Go process cannot portably put its child
in a network namespace or behind a captive resolver; containment for the helm
subprocess is a NetworkPolicy question. That is the one place the second layer
is doing work the first cannot: an `allowPublicHTTPS` rule excepts the same
internal space at the pod, where helm and git are inside it too. What it does
not do is stop `getHostByName` from putting a resolved public address into the
report, and nothing here does.

## Why the deny-list is not configurable

Every entry is a way to make a red gate green without fixing anything:

```
.github/**            the workflows that run the gate
.gitops-gate.yaml     what the gate renders, and how
.bosun.yaml           the same file under the name the gate is moving to
delivery/**           the kit itself, including this agent and its prompt
.gitlab-ci.yml        the GitLab and Bitbucket equivalents of .github/**
bitbucket-pipelines.yml
**/kargo-projects/**  the merge policy and version constraints
**/kargo-pipelines/** the promotion pipelines themselves
```

These are the patterns as enforced. The matcher understands `**` at the start
of a pattern, at the end, or at both, but not in the middle, so a wildcard in
the directory name itself (`**/kargo-*/**`) is not something the deny-list can
say.

The test is applied in both directions, because a list that only ever grows
stops describing what it protects. `.gitops-gate/**` was the inventory-snapshot
directory, and nothing has read it since
[ADR 0010](../adr/0010-the-cli-goes-too.md) removed the CLI: an entry that
guards nothing still reads as a guarantee, so it is off the list rather than
kept as decoration. `.bosun.yaml` is the filename the gate's config is moving
to, and it is denied before anything reads it, because a guard that arrives
after the reader is a window with the guarantee off.

An operator can add to the deny-list. They cannot remove from it.

## Why the attempt cap is a label

Labels live on the pull request, so the cap survives a restart, a rescheduled
pod, and a second replica. In-memory state would reset every time the pod
moved, which is when a loop would be most expensive.

The label is the cap's only memory, so it is reserved before the fix is pushed
rather than recorded after. A token with permission to push and none to label
would otherwise push, fail to label, count zero attempts on the next run and
repair again: a model call and a commit per iteration, with no ceiling. When
the label cannot be written, nothing is pushed and the pull request is handed
to a human with that reason.

## Who may call the promotion endpoint

`POST /v1/promotion-opened` takes a pull-request number, the artifact and the
versions it moved between, and the list of files the promotion rewrote. That
list is read into the prompt the agent publishes; it is no longer what bounds
the applier, which reads the pull request's own diff instead, so a caller
naming a wider set than the branch holds buys nothing by it. The artifact is
still a string that decides where an outbound request goes, which is why the
checkout has to name its host before one is made. The chart's
NetworkPolicy limits callers to the namespace, which is the granularity a
NetworkPolicy has, so every workload in it qualifies.

Set `promotionAuth.existingSecret` on the bosun chart and
`triage.authorization` on kargo-pipelines to require a bearer token. It is
opt-in: leaving it unset keeps the endpoint open, and the pod says so in its
log at every start-up.

## What the MCP surface may reveal, and what it cannot

The read-only MCP listener serves the sweep's own findings to programmatic
callers. It is off by default, it refuses to start without a bearer token, and
it is on a port of its own so that admitting a client to it never admits one to
the endpoint that spends money and writes to the repository.

Two things about it differ from every surface above, and both come from who is
reading. Its answers land in another agent -- one that usually holds a shell, a
checkout, and tools bosun refuses for itself.

**No field path from a tool result can reach a credential, and that is a
compile-time rule rather than a filter.** The `mcp` package imports the result
types and the redactor and nothing else, so it cannot reach a client, a
configuration, or a file. A reflection walk over every registered result type
keeps it true on the paths no request exercises, because a behavioural test can
only sample what a handler happened to produce. Underneath that sits the
process redactor, applied at the single point where a byte reaches the wire:
the primary control is that a credential cannot be in a result, and this is the
second line for the text whose contents nobody chose.

**Instructions in a result are bosun's own or absent.** A remedy is composed
only by bosun's code, from pieces checked against a grammar before the command
is emitted at all -- a piece that fails costs the finding its remedy rather than
producing a suspect command. Every other free-text field carries an origin
saying whether bosun wrote all of it or quoted a cluster inside it, and tool
descriptions are constants, so nothing from a cluster reaches the field a client
hands its model as instructions.

**What it does not offer is sanitised text.** Bosun cannot make a careless
client safe, and text sanitised to harmlessness does not exist. What it
guarantees is provenance labelling and bosun-authored instructions only; a
client that treats an origin-tagged quotation as an instruction has made a
decision bosun cannot take back. The residual risk is real and it is stated
here rather than left implied.

## Why a verdict names a commit

Every checkout clones a branch; every verdict is published against the head SHA
the host reported moments before. A push landing between those two operations
would have the gate render one commit and publish the answer against another.
After cloning, `gitprovider.EnsureHead` compares `HEAD` with that SHA, fetches
the exact commit where the host serves it, and aborts the run where it does
not.

## Failure is always visible

Three rules, because an agent that fails silently costs more than one that does
not run:

- The pull-request comment reports a refused edit, with the reason. A silent
  refusal would let a reader believe a fix had been applied.
- A `mechanical` verdict that applies nothing escalates. The model may be
  wrong; the outcome is still a human being asked.
- A model that is unreachable, slow or misconfigured produces a comment saying
  so. Silence would look the same as "nothing was wrong".

## What it does not do

It never closes a pull request, never merges one, never touches the cluster.
Its RBAC is read-only, and its entire write surface is a bot branch that still
has to pass the gate and the merge policy to reach anywhere.
