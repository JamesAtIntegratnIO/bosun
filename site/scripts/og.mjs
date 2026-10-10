// Generates public/og.png, the 1200x630 card link previews show.
//
// Run it with `npm run og` after changing the wording or the mark; the output
// IS committed, so the site build stays a pure `astro build` and CI does not
// need to rasterise anything. Regenerating produces a byte-identical file when
// nothing changed, so it is safe to run on a whim.

import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import sharp from 'sharp'

const SITE = dirname(dirname(fileURLToPath(import.meta.url)))
const REPO = dirname(SITE)

const W = 1200
const H = 630

// The design system's dark surface, the same values as src/styles/theme.css:
// ground, border, text, said, dim, faint. No gradients; Signal stays in marks.
const GROUND = '#0a0c10'
const BORDER = '#393e43'
const TEXT = '#f3f7fc'
const SAID = '#cfd5dc'
const DIM = '#acb2b9'
const FAINT = '#8d939a'
const SANS = 'Inter, system-ui, Helvetica, Arial, sans-serif'
const MONO = 'DejaVu Sans Mono, Menlo, monospace'

const card = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
  <rect width="${W}" height="${H}" fill="${GROUND}"/>

  <text x="196" y="136" font-family="${SANS}" font-size="22" font-weight="600" letter-spacing="3" fill="${DIM}">
    THE CREW FOR ARGO AND KARGO
  </text>

  <text x="80" y="268" font-family="${SANS}" font-size="72" font-weight="700" letter-spacing="-1.5" fill="${TEXT}">
    One line changed. Four CRDs
  </text>
  <text x="80" y="352" font-family="${SANS}" font-size="72" font-weight="700" letter-spacing="-1.5" fill="${DIM}">
    stopped serving the API.
  </text>

  <text x="80" y="428" font-family="${SANS}" font-size="27" fill="${SAID}">
    A gate that renders what a change deploys and blocks what breaks,
  </text>
  <text x="80" y="466" font-family="${SANS}" font-size="27" fill="${SAID}">
    and an agent that repairs what is provable and escalates the rest.
  </text>

  <line x1="80" y1="530" x2="${W - 80}" y2="530" stroke="${BORDER}" stroke-width="2"/>
  <text x="80" y="584" font-family="${SANS}" font-size="26" font-weight="600" fill="${TEXT}">
    Bosun <tspan font-weight="400" fill="${FAINT}">by Integratn</tspan>
  </text>
  <text x="${W - 80}" y="584" text-anchor="end" font-family="${MONO}" font-size="22" fill="${DIM}">
    bosun.integratn.io
  </text>
</svg>`

// The avatar is a square navy badge; rounding it here matches the design
// system's radius, which the site gives the same mark in its header.
const MARK = 96
const roundedMask = Buffer.from(
  `<svg xmlns="http://www.w3.org/2000/svg" width="${MARK}" height="${MARK}">
     <rect width="${MARK}" height="${MARK}" rx="8" ry="8" fill="#fff"/>
   </svg>`
)

const mark = await sharp(join(REPO, 'docs/avatar/bosun.png'))
  .resize(MARK, MARK)
  .composite([{ input: roundedMask, blend: 'dest-in' }])
  .png()
  .toBuffer()

const out = join(SITE, 'public/og.png')
await sharp(Buffer.from(card))
  .composite([{ input: mark, top: 64, left: 80 }])
  .png({ compressionLevel: 9 })
  .toFile(out)

console.log(`og: wrote ${out} (${W}x${H})`)
