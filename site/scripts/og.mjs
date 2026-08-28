// Generates public/og.png -- the 1200x630 card link previews show.
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

// The palette, same values as src/styles/theme.css.
const NAVY = '#0a1622'
const NAVY_MID = '#14293e'
const TEAL = '#2e8fa0'
const SKY = '#94d8db'
const CORAL = '#e0705a'
const TEXT = '#c6d4e0'
const MUTED = '#8296ab'

const card = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
  <defs>
    <linearGradient id="bg" x1="0" y1="0" x2="1" y2="1">
      <stop offset="0" stop-color="${NAVY}"/>
      <stop offset="1" stop-color="${NAVY_MID}"/>
    </linearGradient>
    <radialGradient id="glowTeal" cx="0.18" cy="0.28" r="0.55">
      <stop offset="0" stop-color="${TEAL}" stop-opacity="0.42"/>
      <stop offset="1" stop-color="${TEAL}" stop-opacity="0"/>
    </radialGradient>
    <radialGradient id="glowCoral" cx="0.88" cy="0.12" r="0.5">
      <stop offset="0" stop-color="${CORAL}" stop-opacity="0.3"/>
      <stop offset="1" stop-color="${CORAL}" stop-opacity="0"/>
    </radialGradient>
  </defs>

  <rect width="${W}" height="${H}" fill="url(#bg)"/>
  <rect width="${W}" height="${H}" fill="url(#glowTeal)"/>
  <rect width="${W}" height="${H}" fill="url(#glowCoral)"/>

  <!-- waterline, the same motif the H2 rules use -->
  <path d="M0,556 Q150,532 300,556 T600,556 T900,556 T1200,556 L1200,630 L0,630 Z"
        fill="${TEAL}" fill-opacity="0.10"/>
  <path d="M0,586 Q150,564 300,586 T600,586 T900,586 T1200,586 L1200,630 L0,630 Z"
        fill="${TEAL}" fill-opacity="0.14"/>

  <text x="196" y="150" font-family="Space Grotesk, Helvetica, Arial, sans-serif"
        font-size="24" font-weight="600" letter-spacing="3.4" fill="${SKY}">
    THE CREW FOR ARGO AND KARGO
  </text>

  <text x="80" y="268" font-family="Space Grotesk, Helvetica, Arial, sans-serif"
        font-size="76" font-weight="700" letter-spacing="-2.2" fill="#f2f6f9">
    The pull request renders green.
  </text>
  <text x="80" y="356" font-family="Space Grotesk, Helvetica, Arial, sans-serif"
        font-size="76" font-weight="700" letter-spacing="-2.2" fill="${CORAL}">
    It still breaks the cluster.
  </text>

  <text x="80" y="436" font-family="Helvetica, Arial, sans-serif" font-size="27" fill="${TEXT}">
    A gate that renders what a change actually deploys and blocks what breaks —
  </text>
  <text x="80" y="474" font-family="Helvetica, Arial, sans-serif" font-size="27" fill="${TEXT}">
    and an agent that repairs what is provable and escalates the rest.
  </text>

  <text x="80" y="576" font-family="Helvetica, Arial, sans-serif" font-size="24" fill="${MUTED}">
    bosun.integratn.io
  </text>
</svg>`

// The avatar is a square navy badge; rounding it here matches the corner
// radius the site gives the same mark in its header.
const MARK = 96
const roundedMask = Buffer.from(
  `<svg xmlns="http://www.w3.org/2000/svg" width="${MARK}" height="${MARK}">
     <rect width="${MARK}" height="${MARK}" rx="20" ry="20" fill="#fff"/>
   </svg>`
)

const mark = await sharp(join(REPO, 'docs/avatar/bosun.png'))
  .resize(MARK, MARK)
  .composite([{ input: roundedMask, blend: 'dest-in' }])
  .png()
  .toBuffer()

const out = join(SITE, 'public/og.png')
await sharp(Buffer.from(card))
  .composite([{ input: mark, top: 78, left: 80 }])
  .png({ compressionLevel: 9 })
  .toFile(out)

console.log(`og: wrote ${out} (${W}x${H})`)
