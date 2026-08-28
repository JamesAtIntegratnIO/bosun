---
name: docs-voice
description: >
  The voice and accuracy rules for this repository's documentation. Use when
  writing or editing any prose in docs/, gate/docs/, charts/*/docs/,
  site/src/authored/, README.md, CONTRIBUTING.md, or any README — including
  when a code change requires a doc update. Do NOT use for adr/ or CHANGELOG
  files, which have their own voice.
---

# Documentation voice

These docs are reference material for someone operating a system that can write
to their repository. They are read by people deciding whether to trust it, and
by people debugging it at 3am. Write for those two readers.

## Applies to

`docs/`, `gate/docs/`, `charts/*/README.md`, `charts/*/docs/`, `ci/**/README.md`,
`local/README.md`, `site/src/authored/`, `README.md`, `CONTRIBUTING.md`.

**Exempt, deliberately:** `adr/` is a decision record — narrative argument with a
date on it is the genre, and rewriting a decided ADR destroys the reason later
ADRs exist. `CHANGELOG.md` is keep-a-changelog. Correct facts in both; do not
restyle them.

## Rule 1 — verify the claim before you write it

The recurring failure in this repo is not bad prose. It is a page that was true
when written and describes code that has since moved. Before describing
behaviour, read the code that implements it.

Specifically, when a doc states:

- **an interface** — read it and list every method. `docs/git-providers.md`
  warned that a stale method list was the failure mode here, then showed 6 of 10.
- **a default** — read `values.yaml` / the `env(...)` call. Do not infer it from
  another doc.
- **a guarantee** — read the enforcement. `docs/safety-model.md` claimed Secrets
  were "unreadable by construction" while the default config bound a Role
  granting `get`/`list` on them.
- **a count** — count. "The three buckets" sat over four; "four things can
  happen" over five.
- **a contract between components** — read both sides. Two adapter READMEs told
  implementers to publish artifacts "for the agent to fetch" long after the
  agent read comments instead.
- **a limitation** — check it is still one. `gate/docs/rendered-manifests.md`
  described chart-diff as future work after it shipped.

A doc that says what a thing *is for* can be written from the prose. A doc that
says what it *does* must be written from the code.

## Rule 2 — the page describes the present

No document narrates its own edit history. That belongs in the CHANGELOG or an
ADR, both of which are linked from anywhere it matters.

> **Wrong** — `## Step 4 is not optional, and it used to say the wrong thing` /
> "Until 2026-08-23 this list said *publish render-diff.json*. Every adapter here
> implemented the contract as written, so none of them posted a comment…"
>
> **Right** — `## Step 4 is not optional` / "The comment is the verdict channel.
> Publishing `render-diff.json` to an artifact store is not a substitute:
> nothing fetches that file. An adapter that skips the comment leaves the
> verdict reachable only by a human with the job summary open."

The reader needs the rule and the failure mode. They do not need to know the
document was once wrong.

An **upgrade note** is the exception, and it is different: it addresses a reader
holding an old copy, tells them what to check, and is dated. Write those as a
checklist, not a story.

## Rule 3 — no story framing

Headings name their subject. Openings say what the page is.

Cut on sight: `## The cast`, `## The acts`, "this one is the story they belong
to", "the promotion that taught this system its most important lesson", "the
model is the scout; the gate is where knowledge hardens", "the joke this
component would rather not be", "Nothing is lost except the pretence that it was
safe", "That is the whole design".

Also cut colloquial headings — `## Two things that bite`, `## The X trap`,
`## Seeing it actually fix things`. Name the thing: `## Two things adapters get
wrong`, `## Semver prereleases`, `## Replaying the recorded incidents`.

## Rule 4 — an aphorism is used once or not at all

"A crash loop with an explanation beats a quiet shrug" appeared on five pages.
The fifth reader has learned nothing and the phrase has replaced the mechanism.
State the mechanism: *"it refuses to start rather than running degraded"*, *"a
crash loop names its cause in the log; a degraded process does not"*.

Before reusing a phrase across pages, `grep` for it.

## Rule 5 — keep the reasoning, reorder it

**This is the rule that stops the other four doing damage.** The value of these
docs is that they say *why*, and name what a decision cost. Do not sterilise
them into a settings dump.

Keep: trade-offs, worked incidents, measured numbers, "this is the weakest
joint", "the cost is real and here it is". Every one of these is documentation.

The fix is order, not deletion — lead with the rule, follow with the evidence:

> **Wrong** — "This was classified mechanical until 2026-08-23. Two things argue
> against it…"
>
> **Right** — "Two things make this an escalation rather than a mechanical fix,
> and neither is about the model: …"

Incidents stay in as evidence, present tense where they describe behaviour that
still holds, past tense where they are genuinely a record ("the agent did
exactly that on gitops_homelab_2_0 #122 — one scalar, in scope, correct `from`
— and it was wrong").

## Rule 6 — say the honest thing about scope

Guarantees get qualified where the code qualifies them. "Cannot read a Secret"
became "Cannot read a Secret, outside one namespace" because that is what the
RBAC says. A guarantee stated wider than the code is worse than none: it is the
failure mode this project exists to prevent, committed in its own documentation.

Same for defaults. If a mitigation is opt-in, say so — `gate.inventorySource:
argocd` removes a grant, and the sentence has to carry that the default is still
`secrets`.

## Mechanics

- Wrap prose at ~80 columns. Tables and links may overrun.
- One `##` heading per subject; no duplicate headings in a file (they collide as
  anchors on the site — `gate/docs/config-reference.md` had two `bootstraps`).
- Prefer a table when there are more than three parallel facts.
- Bold the term being defined, not the emphasis you feel.
- No shouty caps in prose (`DATA`, `OUTPUT`). Bold or nothing.
- `docs/` is the source of truth; `site/src/content/` is generated and
  gitignored. Never edit there. Pages that exist only on the site live in
  `site/src/authored/`.

## Before you finish

1. `python3 hack/check_links.py` — the one-way link rule.
2. If you renamed a heading, grep for `#the-old-anchor` across `*.md` and
   `site/scripts/*.mjs`.
3. If you changed a fact, check whether the same fact appears on another page —
   `README.md`, `docs/`, and `site/src/authored/` state overlapping things, and
   they drift in exactly that direction. Fix all of them.
4. If you touched anything the site publishes, `cd site && npm run build &&
   npm run check:links`.
