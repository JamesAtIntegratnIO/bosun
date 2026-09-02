// Builds site/src/content/docs/ from two sources, and owns that directory
// completely: it is wiped on every run and is NOT in git.
//
// 1. The repository's own markdown: docs/, adr/, gate/docs/, the chart and
//    CI READMEs. Those files stay where they are and stay readable on GitHub;
//    this script is the only thing that knows they are also a website.
// 2. site/src/authored/: pages that exist only on the site (the landing
//    page, the quickstart, the reference tables). Those ARE in git.
//
// The interesting half is link rewriting. A doc that says
// `[ADR 0008](../adr/0008-the-gate-moves-in-cluster.md)` has to keep working
// when GitHub renders it AND resolve to /decisions/0008-the-gate-moves-in-cluster/
// on the site. Every relative link is resolved against the source file's own
// directory, looked up in the page map below, and rewritten, either to a site route
// if that file is published here, and to a GitHub URL if it is not. A link
// that silently 404s is the failure mode this replaces.

import { readFileSync, writeFileSync, mkdirSync, rmSync, cpSync, existsSync, readdirSync, statSync } from 'node:fs'
import { dirname, join, posix, extname } from 'node:path'
import { fileURLToPath } from 'node:url'

const SITE = dirname(dirname(fileURLToPath(import.meta.url)))
const REPO = dirname(SITE)
const OUT = join(SITE, 'src/content/docs')
const AUTHORED = join(SITE, 'src/authored')

const GITHUB = 'https://github.com/JamesAtIntegratnIO/bosun'
const BRANCH = 'main'

// ---------------------------------------------------------------------------
// The page map. `src` is repo-relative; `out` is the route, without extension.
// `title` overrides the source's H1 (which is stripped, because Starlight renders the
// title from frontmatter, and two H1s on a page is a real accessibility bug).
// ---------------------------------------------------------------------------
const PAGES = [
  // --- Start here ---------------------------------------------------------
  {
    src: 'docs/the-loop.md',
    out: 'start/the-loop',
    title: 'The loop, end to end',
    description:
      'One pull request walked from a version appearing to the change verified running, with every piece of the system doing its one job.',
  },
  {
    src: 'docs/onboarding.md',
    out: 'start/onboarding',
    title: 'Onboarding',
    description:
      'Putting bosun onto a GitOps repository, start to finish. Six steps, each ending in a state you can verify before taking the next.',
  },

  // --- Concepts -----------------------------------------------------------
  {
    src: 'docs/safety-model.md',
    out: 'concepts/safety-model',
    title: 'Safety model',
    description:
      'What the agent is prevented from doing, and by which mechanism. Every guarantee is enforced in code, not requested of a model.',
  },
  {
    src: 'docs/classification.md',
    out: 'concepts/classification',
    title: 'Mechanical or escalate',
    description:
      'The judgement the agent makes, with the worked examples the eval cases encode, including the ones reclassified after they went wrong live.',
  },
  {
    src: 'docs/prompt-contract.md',
    out: 'concepts/prompt-contract',
    title: 'The prompt contract',
    description:
      'The reasoning behind the prompt, the five levers that make a small local model viable, and the measurements that produced them.',
  },
  {
    src: 'docs/supervisor.md',
    out: 'concepts/supervisor',
    title: 'The pipeline supervisor',
    description:
      'Sweeping the Kargo pipeline for the promotions that never happened, because nothing about a promotion that did not occur produces an event.',
  },
  {
    src: 'docs/mcp.md',
    out: 'concepts/mcp',
    title: 'The MCP surface',
    description:
      'The read-only tool surface: the tools, what a caller gets before the first sweep, the token, and what it discloses to whoever you publish it to.',
  },

  // --- The gate -----------------------------------------------------------
  {
    src: 'gate/README.md',
    out: 'gate/index',
    title: 'The gate',
    description:
      'The deterministic half. Renders what the repository deploys at base and head, diffs it, and blocks the changes that break things.',
  },
  {
    src: 'gate/docs/config-reference.md',
    out: 'gate/config-reference',
    title: 'Configuring the gate',
    description:
      'Why most repositories need no config file at all, and the full schema for the cases derivation cannot reach: roots, sources, selectors and scope.',
  },
  {
    src: 'gate/docs/rendered-manifests.md',
    out: 'gate/rendered-manifests',
    title: 'Rendered manifests',
    description:
      'Gating a repository that commits rendered output, and why ArgoCD’s source hydrator cannot gate a merge.',
  },
  {
    src: 'gate/docs/render-diff-schema.md',
    out: 'gate/render-diff-schema',
    title: 'The diff result',
    description: 'The contract between the gate and the agent that consumes its verdict: four buckets, and why each blocks or does not.',
  },

  // --- Reference ----------------------------------------------------------
  {
    src: 'charts/bosun/README.md',
    out: 'reference/chart-bosun',
    title: 'Chart: bosun',
    description: 'Running the agent in-cluster: Deployment, Service, RBAC and both halves of the NetworkPolicy.',
  },
  {
    src: 'charts/kargo-pipelines/README.md',
    out: 'reference/chart-kargo-pipelines',
    title: 'Chart: kargo-pipelines',
    description: 'Warehouses and Stages from one target list, with promotion chains, verification gating and the triage hook.',
  },
  {
    src: 'charts/kargo-pipelines/docs/targets.md',
    out: 'reference/pipeline-targets',
    title: 'Pipeline targets',
    description: 'The target list the kargo-pipelines chart turns into Warehouses and Stages.',
  },
  {
    src: 'charts/kargo-pipelines/docs/chaining.md',
    out: 'reference/pipeline-chaining',
    title: 'Promotion chains',
    description: 'Multi-stage promotion, and how a verification result gates the next stage rather than a merge.',
  },
  {
    src: 'docs/llm-providers.md',
    out: 'reference/llm-providers',
    title: 'Model providers',
    description:
      'Two implementations reach the whole field. The interface, the structured-output contract, and how to choose a model.',
  },
  {
    src: 'docs/git-providers.md',
    out: 'reference/git-providers',
    title: 'Git providers',
    description: 'The gitprovider.Provider interface, the token permissions it needs, and what a new implementation has to get right.',
  },

  // --- Decisions ----------------------------------------------------------
  ...adrPages(),

  // --- Project ------------------------------------------------------------
  {
    src: 'local/README.md',
    out: 'project/proving-ground',
    title: 'The proving ground',
    description:
      'A disposable cluster that runs the whole flow, and replays the recorded incidents against the live agent.',
  },
  {
    src: 'CONTRIBUTING.md',
    out: 'project/contributing',
    title: 'Contributing',
    description: 'The toolchain, the test suite, and what a change has to prove before it lands.',
  },
]

// Every ADR becomes a page, ordered by its number, titled from its own H1.
function adrPages() {
  const dir = join(REPO, 'adr')
  if (!existsSync(dir)) return []
  return readdirSync(dir)
    .filter((f) => f.endsWith('.md'))
    .sort()
    .map((f) => {
      const slug = f.replace(/\.md$/, '')
      const h1 = firstHeading(readFileSync(join(dir, f), 'utf8'))
      return {
        src: `adr/${f}`,
        out: `decisions/${slug}`,
        title: h1 ?? slug,
        description: `Architecture decision record ${slug.slice(0, 4)}: ${(h1 ?? slug).replace(/^ADR \d+[:.]?\s*/i, '')}`,
      }
    })
}

function firstHeading(md) {
  const m = md.match(/^#\s+(.+)$/m)
  return m ? m[1].trim() : null
}

// repo path -> site route, used by the link rewriter.
const ROUTES = new Map(
  PAGES.map((p) => [p.src, '/' + p.out.replace(/\/index$/, '') + '/'])
)

// Repo paths that have no page here but are worth linking somewhere sensible.
// Anything not listed falls through to a GitHub blob/tree URL, which is
// the correct destination for source files.
const EXTERNAL_OVERRIDES = new Map([
  ['README.md', '/'],
  ['CHANGELOG.md', `${GITHUB}/blob/${BRANCH}/CHANGELOG.md`],
])

// ---------------------------------------------------------------------------

function rewriteLinks(body, srcPath) {
  const srcDir = posix.dirname(srcPath)

  // Markdown inline links and images: [text](target) / ![alt](target)
  return body.replace(/(!?\[[^\]]*\])\(([^)\s]+)(\s+"[^"]*")?\)/g, (whole, label, target, title) => {
    if (/^(https?:|mailto:|#|\/)/.test(target)) return whole

    const [rawPath, anchor] = splitAnchor(target)
    if (!rawPath) return whole

    const repoPath = posix.normalize(posix.join(srcDir, rawPath)).replace(/^\.\//, '')

    let dest
    if (ROUTES.has(repoPath)) {
      dest = ROUTES.get(repoPath) + (anchor ?? '')
    } else if (EXTERNAL_OVERRIDES.has(repoPath)) {
      dest = EXTERNAL_OVERRIDES.get(repoPath) + (anchor ?? '')
    } else if (whole.startsWith('!')) {
      // An image the site has to serve; sync-assets copies these.
      dest = '/' + posix.basename(repoPath)
    } else {
      const abs = join(REPO, repoPath)
      const isDir = existsSync(abs) && statSync(abs).isDirectory()
      const kind = isDir || extname(repoPath) === '' ? 'tree' : 'blob'
      dest = `${GITHUB}/${kind}/${BRANCH}/${repoPath}${anchor ?? ''}`
    }

    return `${label}(${dest}${title ?? ''})`
  })
}

function splitAnchor(target) {
  const i = target.indexOf('#')
  if (i === -1) return [target, null]
  if (i === 0) return [null, target]
  return [target.slice(0, i), target.slice(i)]
}

function yamlString(s) {
  return JSON.stringify(String(s))
}

function build() {
  rmSync(OUT, { recursive: true, force: true })
  mkdirSync(OUT, { recursive: true })

  // Authored pages first, so a generated page can never silently overwrite one.
  if (existsSync(AUTHORED)) cpSync(AUTHORED, OUT, { recursive: true })

  let count = 0
  for (const page of PAGES) {
    const abs = join(REPO, page.src)
    if (!existsSync(abs)) {
      console.warn(`  ! skipped ${page.src}: not found`)
      continue
    }

    let body = readFileSync(abs, 'utf8')

    // Strip the source H1; the title comes from frontmatter.
    body = body.replace(/^#\s+.+\r?\n+/, '')
    body = rewriteLinks(body, page.src)

    // Generated pages are plain .md on purpose. These files are full of
    // `{{metadata.labels.x}}` placeholders and bare `<` in prose, both of
    // which MDX would try to evaluate as expressions and JSX. Authored pages
    // in src/authored/ are .mdx and may use components.
    const frontmatter = [
      '---',
      `title: ${yamlString(page.title)}`,
      `description: ${yamlString(page.description)}`,
      // "Edit this page" has to land on the real file in the repository, not
      // on the generated copy, which is not in git at all.
      `editUrl: ${yamlString(`${GITHUB}/edit/${BRANCH}/${page.src}`)}`,
      '---',
      '',
    ].join('\n')

    const dest = join(OUT, page.out + '.md')
    mkdirSync(dirname(dest), { recursive: true })
    writeFileSync(dest, frontmatter + '\n' + body)
    count++
  }

  console.log(`sync-docs: ${count} pages from the repository, plus site/src/authored/`)
}

build()
