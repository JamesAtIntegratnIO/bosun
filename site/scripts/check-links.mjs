// Asserts every internal link in the BUILT site resolves to a page or asset
// that exists, and that every link rewritten to a GitHub URL points at a path
// that is in the repository.
//
// This is the check that earns the link rewriting in sync-docs.mjs. That
// script turns `../adr/0008-....md` into either a site route or a GitHub blob
// URL depending on whether the file is published here, and both halves of
// that decision fail silently. A wrong route is a 404 nobody clicks in
// review; a wrong GitHub path is a 404 on someone else's server.
//
// Run after `npm run build`. Exits non-zero with the offending pages listed.

import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs'
import { join, relative, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const SITE = dirname(dirname(fileURLToPath(import.meta.url)))
const REPO = dirname(SITE)
const DIST = join(SITE, 'dist')

const GITHUB_PREFIX = 'https://github.com/JamesAtIntegratnIO/bosun/'

if (!existsSync(DIST)) {
  console.error('check-links: no dist/, run `npm run build` first')
  process.exit(1)
}

function walk(dir, acc = []) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) walk(full, acc)
    else acc.push(full)
  }
  return acc
}

const allFiles = walk(DIST)
const assets = new Set(allFiles.map((f) => '/' + relative(DIST, f).split('\\').join('/')))
const pages = new Set(
  [...assets].filter((p) => p.endsWith('index.html')).map((p) => p.replace(/index\.html$/, ''))
)

// Pagefind's index and Astro's hashed bundles are emitted by the build itself
// and are not authored links; checking them adds noise, not safety.
const IGNORED_PREFIXES = ['/_astro/', '/pagefind/']

const problems = []

for (const file of allFiles.filter((f) => f.endsWith('.html'))) {
  const from = relative(DIST, file)
  const html = readFileSync(file, 'utf8')

  for (const [, href] of html.matchAll(/(?:href|src)="([^"]+)"/g)) {
    // --- internal ---------------------------------------------------------
    if (href.startsWith('/')) {
      const path = href.split('#')[0].split('?')[0]
      if (!path || IGNORED_PREFIXES.some((p) => path.startsWith(p))) continue
      const asPage = path.endsWith('/') ? path : path + '/'
      if (pages.has(asPage) || assets.has(path)) continue
      problems.push(`${from}  ->  ${href}   (no such page or asset)`)
      continue
    }

    // --- rewritten to the repository on GitHub ----------------------------
    if (href.startsWith(GITHUB_PREFIX)) {
      const m = href.match(/^.*?\/(?:blob|tree)\/main\/([^#?]+)/)
      if (!m) continue
      const repoPath = decodeURIComponent(m[1]).replace(/\/$/, '')
      if (existsSync(join(REPO, repoPath))) continue
      problems.push(`${from}  ->  ${href}   (not in the repository)`)
    }
  }
}

const unique = [...new Set(problems)].sort()

if (unique.length) {
  console.error(`check-links: ${unique.length} broken link(s)\n`)
  for (const p of unique) console.error('  ' + p)
  process.exit(1)
}

console.log(`check-links: ok, ${pages.size} pages, every internal and repository link resolves`)
