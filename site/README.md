# The documentation site

[bosun.integratn.io](https://bosun.integratn.io), built with Astro and
Starlight and deployed to GitHub Pages by
[`.github/workflows/pages.yaml`](../.github/workflows/pages.yaml).

## The one thing to understand

**The markdown in this repository is the source of truth, and it does not live
here.** `docs/`, `adr/`, `gate/docs/` and the chart READMEs stay exactly where
they are and stay readable on GitHub;
[`scripts/sync-docs.mjs`](scripts/sync-docs.mjs) copies them into
`src/content/docs/` at build time.

`src/content/docs/` is **generated and gitignored**. Editing a file in there
edits something the next build deletes.

| To change | Edit |
|---|---|
| The wording of an existing doc | the real file: `docs/safety-model.md`, `adr/0008-….md`, and so on |
| Which docs are published, their route, title or description | the `PAGES` map in `scripts/sync-docs.mjs` |
| The sidebar | `astro.config.mjs` |
| A page that exists only on the site | `src/authored/`: the landing page, quickstart, configuration, troubleshooting, FAQ, licence |
| Colours, type, tables, asides | `src/styles/theme.css` |
| The landing page's layout | `src/authored/index.mdx`, `src/components/Hero.astro`, `src/components/LoopDiagram.astro`, `src/styles/landing.css` |
| The link-preview card | `scripts/og.mjs`, then `npm run og` (output is committed) |

## Working on it

```bash
npm install
npm run dev          # sync, then serve at localhost:4321
npm run build        # sync, then build to dist/
npm run check:links  # after a build
```

`npm run dev` syncs once at startup. A change to a doc **outside** `site/`
will not hot-reload; re-run the command.

## Link rewriting is the interesting part

A doc that says `[ADR 0008](../adr/0008-the-gate-moves-in-cluster.md)` has to
work in two places at once. The sync script resolves every relative link against
its source file's directory and then rewrites it:

- to a **site route** if that file is published here
  (`/decisions/0008-the-gate-moves-in-cluster/`), or
- to a **GitHub URL** if it is not: source files, `evals/`, `local/`, the
  `LICENSE`.

Both halves fail silently when they are wrong, which is why
[`scripts/check-links.mjs`](scripts/check-links.mjs) resolves every internal
link against the pages that got built, and every rewritten GitHub link
against the working tree. It runs in CI on every pull request.

`hack/check_links.py`, the repository's own one-way link rule, deliberately
**skips `site/`**, because the rule here is the opposite one: pages in
`src/authored/` link by absolute site route, which that checker exists to
reject.

## Adding a page

**A page that documents the software** goes in the repository proper, next to
what it documents, and gets an entry in the `PAGES` map plus a line in the
sidebar.

**A page that only makes sense on a website**, such as a landing page or a
quickstart that stitches several docs together, goes in `src/authored/` as
`.md` (or `.mdx` if it needs a component) and gets a sidebar line.

Generated pages are written as plain `.md` on purpose: these docs are full of
`{{metadata.labels.x}}` placeholders and bare `<` in prose, both of which MDX
would try to evaluate.

## Deploying

Pushes to `main` that touch `site/` or any published markdown trigger
`pages.yaml`. The paths filter there has to stay in step with the `PAGES` map:
a doc that is published but not listed is a doc whose fix reaches GitHub and
never reaches the site.

The custom domain is `public/CNAME`. Changing it means changing `site:` in
`astro.config.mjs` and the URL in `scripts/og.mjs` too.
