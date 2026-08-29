# The prompt contract

The agent's job is narrow and its output is checked, which is what makes a
small local model viable. This is the reasoning behind the prompt in
`prompt/`, and the measurements that produced it.

## The edit format is where small models fail

Classification is easy. Every model tried gets "is this a chart default flip or
a one-way migration" right almost every time.

Producing a *usable edit* is the hard part. Asked for "the fix", a model will
return:

- a file path in the `key` field,
- several lines of YAML in `from` and `to`,
- a paraphrase of the current value rather than the value.

The applier rejects all three, so nothing gets fixed and the run is wasted.
Every lever below exists to prevent that.

## Lever 1: an inventory of editable scalars

The single biggest change. Instead of pasting file contents, the agent extracts
every scalar and presents key/value pairs in the form an edit must use:

```
FILE addons/environments/production/addons/addons.yaml -- editable scalars (key = value):
  metallb.defaultVersion = 0.16.0
  metallb.valuesObject.speaker.frr.enabled = true
  metallb.valuesObject.frrk8s.enabled = true
```

This converts *generation* into *selection*. A key that does not exist becomes
inexpressible, and the model copies `from` out of text it was just shown rather
than reconstructing it, so the applier's equality check passes instead of
rejecting a paraphrase.

Measured on a 9B model across the case set: 6/9 full pass without the
inventory, 8/9 with it. The failure it removes is the dangerous one, a
*partial* fix, where one of two required edits lands and the result renders
green while still being wrong.

## Lever 2: worked examples of the edit contract

The schema descriptions alone are not enough; models skip them. The prompt
carries correct and incorrect examples side by side, and names the four ways an
edit gets rejected. That turned the malformed-shape failure from routine into
absent.

## Lever 3: version corroboration in code

A model told *"requires Gateway API v1.5"* will write `v1.5.0` when the answer
was `v1.5.1`. This is the worst failure available to us: it passes the render
and the gate, and the apiserver rejects it after the merge.

Telling the model not to do this **does not work**. Measured: with an explicit
rule in the prompt forbidding it, a 9B model still invented the patch version.

So the guarantee lives in `edits.Policy.Evidence`. Any version-shaped value an
edit writes must appear verbatim in the material the model was shown. If it
does not, the edit is refused, and the run escalates instead.

Only version-shaped values are corroborated. Booleans and ports are exempt on
purpose: `"false"` rarely appears in a failure report, and corroborating it
would reject the most common mechanical fix there is.

## Lever 4: an empty result escalates

If a `mechanical` verdict produces zero applied edits, every one rejected, the
agent escalates rather than reporting success. This is what converts
miscalibration into a safe outcome without anyone intervening: the model can be
wrong about the classification, and the result is still a human being asked.

## Lever 5: refusing a well-formed fix that points the wrong way

Levers 1 to 4 make a *correct* fix expressible and a *malformed* one harmless.
None of them asks whether a well-formed fix points the right way, which is a
distinct failure and one the first live run of the mechanical path hit.

That run met a bump whose render also moved an addon's namespace, and the agent
updated a reference to match it. One scalar, in scope, correct `from`. Every
guard in the table below was satisfied, because a guard checks the *shape* of
an edit and this edit's shape was perfect. Its direction was wrong: it
accommodated a change nobody had explained, and burned an attempt doing so.

The prompt had made that reading reasonable. It said each pull request "moves
one pinned version" and then described only reds the version had caused, so
"make the pull request self-consistent" followed. It now names the changes a
version *cannot* cause, a namespace, a project, a source, cluster targeting,
and says outright that making the rest of the repository agree with one is the
wrong answer even when it is the tidy one.

**The suite could not have caught this.** All three mechanical cases are
accommodations, flip a default back, move a coupled pin forward, where making
the render agree with the bump is right. None of them asks the agent to refuse
anything, so a model that accommodates unconditionally scores full marks.
`namespace-moved-under-a-bump` is the first case where the correct answer is to
decline, and it is a transcript of the live failure rather than an invention.

## Lever 6: define each field by its reader

Read back from the first live escalations on real held promotions: every one
said the same thing three times. The headline printed the escalation reason,
the summary paraphrased it, and the reasoning restated both before restating
the gate report, and none of the three named a file a human could open. The
reader already knows an escalation happened, because the label and the headline
said so before the model's words began.

Two levers, one in code and one in the prompt. The renderer now prints the
verdict marker once and sends `escalationReason` to the commit status instead
of the comment, so the model *cannot* duplicate it there. And the prompt
defines the fields by their reader: summary is the decision in one sentence,
reasoning is the handoff (the file and key to open, the choice the human faces,
the one fact that stopped a mechanical fix), escalationReason is a status
label. It also bans restating the report that sits directly above the comment.

Measured after the change on qwen3.8-27b: **classification 10/10, full pass
10/10, UNSAFE 0** (3m13s). The three accommodation cases still classify
mechanical, so telling the model to spend its words on the handoff did not push
it toward escalating everything. The numbers cannot measure the prose itself;
that is judged the same way the repetition was found, by reading the next live
escalations.

## What the model is never trusted with

Neither the prompt nor the model decides any of this:

| Guarantee | Enforced by |
|---|---|
| Cannot edit the gate, CI, or the merge policy | path deny-list, before any write |
| Cannot edit outside the configured area | path allow-list |
| Cannot overwrite a value it misread | `from` must match the file |
| Cannot invent a version | corroboration against the evidence |
| Cannot add keys with a scalar edit | the key must already resolve to a scalar |
| Cannot escape the repository | path traversal check |
| Cannot try forever | attempt cap, tracked by label |
| Cannot invent data when reshaping a document | every value must be in the original or dictated by the target schema |
| Cannot rename what it reshapes | identity fields byte-identical |
| Cannot half-migrate | any refusal in a pass refuses the whole push |

That table is why a 9B model is an acceptable choice here. The model is not
reliable. Every way it can be wrong is either cheap or refused before it
reaches disk.

## Re-running the measurements

The eval cases are real incidents, not invented ones. Three prompts ship and
all three are measured; each case names the path it belongs to. Run them
against any OpenAI-compatible endpoint:

```bash
DELIVERY_AGENT_LIVE=http://localhost:1234/v1 DELIVERY_AGENT_MODELS=your-model go test ./evals -run Eval -v -timeout 60m
```

The prompts are **imported** from `prompt/`, not passed in, so the thing scored
and the thing shipped are the same constant and the compiler enforces it. Do
not reintroduce a bridge that passes them in by another route: an earlier one
scraped the Go source into three environment variables and supplied an empty
string whenever a constant was renamed, so a shipped prompt went unmeasured
while the suite reported a confident number for the two it still found.

Add `DELIVERY_AGENT_NO_INVENTORY=1` to reproduce the lever-1 ablation.

Score three things, in order of importance:

1. **UNSAFE**: did the wrong thing reach somewhere nothing checks it? This must
   be zero.
2. **classification**: is the judgement right?
3. **full pass**: did exactly the right edits land, and did the explanation
   stay inside its evidence?

A model with UNSAFE 0 is usable even if its classification is mediocre; a model
with UNSAFE above 0 is not usable at any accuracy.

### Four kinds of evidence, and the prompt says which is which

1. **The gate report**: fact. Somebody rendered both versions and diffed them.
2. **The live cluster**: fact, and the strongest one. Nobody wrote it down; it
   was *counted*, in the cluster this repository deploys to, before the change
   is applied. Only present when `liveReads` is on.
3. **Release notes**: testimony. Somebody wrote down what they meant to do.
4. **Upstream commits**: testimony, of a different quality. Somebody wrote down
   what they were doing while doing it.

The live block **discharges exactly one finding** and the prompt says so in
those words: a CRD that stops serving a version, where the report counts no
declaring manifest *and* the block counts no stored objects, has nothing left
to go wrong. Every other reason to escalate stands on its own. A major boundary
crossed is still a migration with a version number, whatever is running.

That scoping earns its place. The first version of this section said only "use
it to discharge a finding", and the measured cost was immediate: a
0.9.20 → 0.11.0 case with **no live block at all** dropped from `escalate` to
`no_action`. A permission to relax, written loosely, relaxes everything.

**"Not permitted to check" is not zero.** It appears verbatim in the block, and
the block itself tells the model that it means nobody looked and is not
evidence of safety. The value of "0 live objects" is that it ends a
conversation, and it can only do that if it never quietly means "we did not
ask".

The third kind exists because of the findings the second cannot explain. A
chart drops its `ClusterRole` and ships a release note about performance; the
render proves the removal and cannot say why, and the honest answer, "the
report does not say why", is correct and hands the reader a search. The commit
that deleted the template says exactly why.

**Which commits is decided by code.** `migrate.Subjects` reads the kinds and
resource names out of the gate's own findings, and matches those terms against
commit messages and against the paths in the upstream diff. The model is shown
the result; it never picks its own evidence, which would let it choose what
corroborates it.

**The mechanical path never sees any of it.** The path does not fetch upstream
at all, rather than fetching it and being told not to use it. Upstream is read
on the paths that produce prose: the green-gate explanation, and an escalation.
An edit is corroborated against the evidence string the model was shown, so a
commit message that happens to contain `v1.5.0` would make `v1.5.0` a
corroborated value to write. Keeping testimony out of that string is a property
of the code rather than a rule in a prompt.

### What UNSAFE means on each path

The two prompts fail in different places, so the word has to mean different
things, and it means anything at all because only one of them has something
standing in front of it.

**Triage** writes to disk, behind the applier. UNSAFE is an edit that
*landed*: a wrong classification whose edits were refused costs a human two
minutes, while a wrong edit that lands renders green and fails on apply.

**Explain** writes nothing. Its output is a sentence, and it goes to somebody
about to press merge, where nothing checks it. So UNSAFE here is an **invented
reason**: a claim in neither the gate report nor the release notes. That is the
same class of error as an invented version number, except the applier refuses
an invented version and nothing refuses an invented explanation.

The explain cases probe for it in pairs. The same removed `ClusterRole` appears
twice, once with the maintainers' explanation in front of the model and once
without, and the measurement is whether the second answer still contains the
first answer's reason. `MustMention` asserts the grounded reason was cited;
`MustNotMention` asserts a distinctive word that could only have arrived from
memory did not. A test in the suite checks the probes themselves: every
`MustNotMention` string must be absent from the evidence the case supplies, or
it is measuring the fixture rather than the model.
