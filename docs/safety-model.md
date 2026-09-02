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
| Cannot publish a credential a host quoted back at it | every credential `config.go` loads primes one process-wide redactor at start-up, plus one embedded in `GIT_REPO_URL`, which arrives as part of a URL rather than as a credential and reaches a remote as one, and text this process did not author goes through it before it is logged, posted, or wrapped into an error. Today that is what every subprocess prints: both pushes, including the installation token minted for that one push which start-up never saw; every clone, fetch, merge-base and diff, which talk to a host this process authenticated to; and the chart renders and schema checks, which talk to a registry and inherit every credential on the way. This is the second line and not the first: the first is that each credential is handed to one client and nothing else holds a reference to reach. It is also the narrow claim: redaction removes this process's own secrets from a string, and does not make a string safe. The binary is not what makes a child's stderr dangerous; repeating what a host it authenticated to said is. The row below is the first line |
| Cannot hand a credential to a subprocess that has no use for one | every `cmd.Env` in this process is built from `childenv`, which is `os.Environ()` with the name of every variable `config.go` read a credential from taken out, both spellings, because `GIT_TOKEN_FILE` names a path and a child that can read the path can read the credential, plus `GIT_REPO_URL`, which arrives as part of a URL rather than as a credential and is the one that may carry one. It was nil at every call site but five, and a nil `Env` gives the child `os.Environ()` verbatim, so `helm template`, `kustomize build` and `kubeconform` each ran holding `GIT_TOKEN`, `ARGOCD_TOKEN`, the model key, the promotion and MCP tokens and the App private key. The five that did set it appended to `os.Environ()`, which added one scoped credential on top of all of them. This is the **first** line and redaction is the second: redaction filters what a child prints, and a child that writes its environment to a file, sends it somewhere, or is a chart's own `helm` plugin has published a credential without printing a byte. A denylist rather than an allowlist, and that choice carries weight: `helm` needs `HOME`, `PATH`, `XDG_*`, `HELM_*`, `SSL_CERT_FILE`, the proxy variables and whatever a self-hosted install configured for its registry, and an allowlist that missed one would break a chart render in a deployment nobody here can see, with a gate abstaining for a reason no log explains. Removing this process's own credentials has no such failure mode. The commands that do need one still get theirs, added on top of that base and scoped to the one remote they authenticate |
| Cannot hand a credential to anything that reads a command line | a credential an operator embedded in `GIT_REPO_URL` never reaches git's argv, where `/proc/<pid>/cmdline` is world-readable. `gitprovider.Remote` splits that URL into the address git is given and an `http.<remote>.extraHeader` it is handed through the environment instead, which `/proc` exposes only to the owner. The remote stored as `origin` is clean, so the credential is attached per command, to the commands that contact a host and to no others, which is ArgoCD's arrangement for ArgoCD's reason. Structural rather than reviewed, in one direction: a derived test permits only `gitprovider` to run a git command that contacts a remote, and `agent`, `gateservice` and `supervisor` hold a `Remote` where each used to hold a URL string, so there is nothing in those packages left to pass. The configured string still exists in `config.go`, and `gitprovider.GitHub` still holds it to build a push remote from. That path strips the userinfo itself, and has since before this. The same URL is stripped before it becomes the status page's repository link |
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

## Disclosure and limits of the MCP surface

The read-only MCP listener serves the sweep's own findings to programmatic
callers. It is off by default, it refuses to start without a bearer token, and
it sits on a port of its own, so admitting a client to it never admits one to
the endpoint that spends money and writes to the repository.
[The MCP surface](mcp.md) covers the tools and how to turn it on.

**No tool mutates anything, and none reads anything live.** There is no
mutating tool and no write verb in the ClusterRole to build one on, so bosun
cannot answer a client that asks. Every result comes from the snapshot the last
sweep left in memory, so a call reaches no cluster, no git host and no model. A
chatty client cannot spend an install's rate limit, and a hostile one cannot
use this surface to make bosun's credentials issue a request on its behalf.

The surface serves its view whole. Bosun applies no per-project or per-caller
filtering to the readout here or on any other surface, for the reasons in
[ADR 0014](../adr/0014-an-install-serves-one-trust-domain.md).

Two things set it apart from the surfaces above, and both come from who reads
it. Its answers land in another agent, and that agent holds a shell, a
checkout, and tools bosun refuses for itself.

**No field path from a tool result can reach a credential, and the compiler
enforces that rather than a filter.** The `mcp` package imports the result
types and the redactor and nothing else, so it cannot reach a client, a
configuration, or a file. A reflection walk over every registered result type
keeps that true on the paths no request exercises, because a behavioural test
samples only what a handler happened to produce. Underneath sits the process
redactor, applied at the single point where a byte reaches the wire. The
primary control keeps a credential out of a result; the redactor is the second
line for text whose contents nobody chose.

**Instructions in a result are bosun's own or absent.** Bosun's code composes a
remedy from pieces checked against a grammar before it emits the command. A
piece that fails costs the finding its remedy, and no suspect command reaches
the wire. Every other free-text field carries an origin saying whether bosun
wrote all of it or quoted somebody else inside it, and tool descriptions are
constants, so nothing from a cluster reaches the field a client hands its model
as instructions.

The origins are a closed vocabulary, and a client fences on the shape rather
than on the list: a field is `bosun`, or it is `bosun-quoting-` something.
Today the something is a cluster, this repository, a rendered chart, helm, the
schema validator, the render as a whole, a pull request's author, or a label
standing on a pull request. Only the first claims bosun wrote every byte.

Bosun tags a label as somebody else's even though it writes some of them.
Attempt labels are the agent's own, a `needs-human` is a maintainer's, and in a
repository where anybody may label they are anybody's. So the field carries one
origin saying the weakest true thing about it. A per-label judgement would
invite a hostile label to imitate it by choosing bosun's prefix.

**Bosun vets a verdict's typed facts instead of tagging them.** One block on
the gate verdict carries no origin, on purpose. The dropped-served-version
detail, which definition, which versions are gone, which one survives, which
kind of manifest moves, is what a repair acts on with no person in between, so
a tag would be the wrong instrument. Bosun matches every field of it against
the repair contract's own grammars, which admit no space, no backtick and no
newline. A finding whose fields do not hold their shape loses the block rather
than getting it labelled. Absence there means bosun would not vouch for the
detail, and never that there is none.

**One answer is read off the git host rather than computed here, and it says
so.** `verdict_history` publishes the verdicts the gate reached on a pull
request's earlier head commits, and those rows are the parse of the stamps in
the gate's own comment on it. Bosun wrote that sentence, and it sits somewhere
any repository writer can edit before bosun reads it back. The headlines are
tagged `bosun` because bosun composed them, and the result carries the source of
the rows for that reason: a client that needs to know who could have touched a
string is told, in a field, that this one came out of a pull-request comment.
It is the one place on this surface where the set of people who can choose the
bytes is the set of people who can write to the gated repository.

**Bosun strips the gate's own stamp grammar from every response.** The gate
keeps its memory inside its pull-request comment, because a gate with no
database has nowhere else to put it. It writes the last verdict, the head it
judged and the migration a repair performs as HTML comments, and reads them
back on the next run. A client of this surface reads a verdict and writes prose
onto a pull request. Smuggle a stamp through a chart-rendered object name and
that client becomes a forgery relay, republishing a verdict the gate never
reached against a commit it never judged. Nobody in that chain is compromised,
so bosun breaks the HTML comment delimiters where a byte reaches the wire
instead of blaming the client. It breaks them visibly and does not delete them:
an object whose name contains an HTML comment is worth somebody looking at the
chart that produced it.

**Bosun caps the length of free-text fields.** The client's context is a
resource this surface can spend without seeing the bill, and nothing upstream
bounds a helm error or a release note. A field that got cut says so, because a
note that happens to end in an ellipsis would otherwise look the same as one
bosun stopped copying.

**The surface does not offer sanitised text.** Bosun cannot make a careless
client safe, and text sanitised to harmlessness does not exist. It guarantees
provenance labelling, and instructions that are bosun's own or absent. A client
that treats an origin-tagged quotation as an instruction has made a decision
bosun cannot take back, in whatever tools that client holds. The residual risk
is real, and it is stated here rather than left implied.

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
