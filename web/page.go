package web

// The page, as one document. No script, no external asset, no build step: the
// template below and the process's own state are the entire dependency list,
// which is what lets the chart expose this behind any gateway without a
// content policy conversation. Styling follows the system's light/dark choice
// because the page has no storage to remember one of its own.
//
// Everything dynamic arrives pre-formatted in the view; the template ranges
// and branches and says nothing. html/template escapes every finding, title
// and note on the way through, which matters here more than most places:
// finding text quotes cluster objects, and pull request titles are whatever
// the bump wrote.
//
// THE PALETTE IS THE SITE'S, and it is not restated from taste. Every value
// below maps to a token in site/src/styles/theme.css, whose own header says
// those colours come from the mark: the badge navy, the two sea tones, the
// coral of the tentacles, the cream of the cap. There is one Bosun palette and
// bosun.integratn.io is where it is decided; this page is a consumer of it. If
// a colour here has no counterpart there, it is a bug rather than a variation.
//
// Dark is the base and light is the override, which is the site's structure
// and for the site's stated reason -- the badge is navy, and you read this
// next to a terminal.
//
// WHICH TREATMENT IS THREE-STATE, and the shape below is what makes all three
// reachable. Left alone the page follows the reader's system preference. The
// chart's `web.theme` stamps `data-theme` on <html> -- the same attribute the
// site's own toggle writes -- and an explicit value has to beat the media
// query in BOTH directions: `dark` on a reader whose system asks for light,
// and `light` on one whose system asks for dark. So the media query is
// guarded with :not([data-theme='dark']) and the explicit light block is
// repeated after it. Defining a colour only inside the media query would
// leave `data-theme="light"` half-applied on a dark system, which renders as
// dark text on dark ground rather than as an error.
//
// The fonts are named and not fetched. Inter, Space Grotesk and JetBrains Mono
// are what the site loads; a reader who has them gets them, and everyone else
// gets the system stack behind them. Loading them would mean an external asset
// on a page whose whole publishing story is that it has none.
const pageHTML = `<!doctype html>
<html lang="en"{{with .Theme}} data-theme="{{.}}"{{end}}>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="60">
<link rel="icon" type="image/svg+xml" href="mark.svg">
<title>{{.Brand}} — promotion pipeline</title>
<style>
/* Dark, and the base rather than a media query -- see the note above the
   template. Names on the left are this page's; names in the comments are the
   site's tokens they come from. */
:root {
  --bg: #0a1622;            /* --bo-navy-900 */
  --card: #122435;          /* --bo-surface-solid */
  --fg: #c6d4e0;            /* --sl-color-text */
  --strong: #f2f6f9;        /* --sl-color-white */
  --muted: #8296ab;         /* --sl-color-gray-3 */
  --line: #1d3346;          /* --sl-color-gray-6 */
  --link: #94d8db;          /* --bo-teal-300, the site's text-accent */
  --ok: #8fe0ab;            /* --sl-color-green-high */
  --ok-bg: #0f2f24;         /* --sl-color-green-low */
  --blocking: #f2a48e;      /* --bo-coral-300 */
  --blocking-bg: #3a1a15;   /* --sl-color-red-low */
  --degraded: #f0cf9a;      /* --sl-color-orange-high */
  --degraded-bg: #33260f;   /* --sl-color-orange-low */
  --neutral: #8296ab;
  --neutral-bg: #16283a;    /* --sl-color-gray-7 */
  --code-bg: #0c1a27;       /* --bo-code-bg */
  --radius: 10px;           /* --bo-radius */
  --font: "Inter Variable", Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif;
  --font-display: "Space Grotesk Variable", "Space Grotesk", var(--font);
  --font-mono: "JetBrains Mono Variable", "JetBrains Mono", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
/* Light: the site's paper-and-cream treatment, not an inversion of the above. */
@media (prefers-color-scheme: light) {
  :root:not([data-theme='dark']) {
    --bg: #fbf7ee;          /* --bo-paper */
    --card: #ffffff;        /* --bo-surface-solid */
    --fg: #2c4258;          /* --sl-color-text */
    --strong: #14293e;      /* --bo-navy-700 */
    --muted: #5b7690;       /* --sl-color-gray-3 */
    --line: #e3e0d4;        /* --sl-color-gray-6 */
    --link: #1b5c6d;        /* --bo-teal-700, the site's text-accent */
    --ok: #1c5840;          /* --sl-color-green-high */
    --ok-bg: #d6eee1;       /* --sl-color-green-low */
    --blocking: #7a2a20;    /* --bo-coral-700 */
    --blocking-bg: #f8dfd8; /* --sl-color-red-low */
    --degraded: #7a5219;    /* --sl-color-orange-high */
    --degraded-bg: #f6e9d1; /* --sl-color-orange-low */
    --neutral: #5b7690;
    --neutral-bg: #efeade;  /* --sl-color-gray-7 */
    --code-bg: #f3eee0;     /* --bo-code-bg */
  }
}
/* And again for an explicit light, which must beat a dark system preference.
   Byte-identical to the block above by construction; TestLightBlocksAgree fails
   if they ever stop agreeing. */
:root[data-theme='light'] {
  --bg: #fbf7ee;          /* --bo-paper */
  --card: #ffffff;        /* --bo-surface-solid */
  --fg: #2c4258;          /* --sl-color-text */
  --strong: #14293e;      /* --bo-navy-700 */
  --muted: #5b7690;       /* --sl-color-gray-3 */
  --line: #e3e0d4;        /* --sl-color-gray-6 */
  --link: #1b5c6d;        /* --bo-teal-700, the site's text-accent */
  --ok: #1c5840;          /* --sl-color-green-high */
  --ok-bg: #d6eee1;       /* --sl-color-green-low */
  --blocking: #7a2a20;    /* --bo-coral-700 */
  --blocking-bg: #f8dfd8; /* --sl-color-red-low */
  --degraded: #7a5219;    /* --sl-color-orange-high */
  --degraded-bg: #f6e9d1; /* --sl-color-orange-low */
  --neutral: #5b7690;
  --neutral-bg: #efeade;  /* --sl-color-gray-7 */
  --code-bg: #f3eee0;     /* --bo-code-bg */
}
* { box-sizing: border-box; }
body {
  margin: 0; background: var(--bg); color: var(--fg);
  font: 15px/1.5 var(--font);
}
a { color: var(--link); text-decoration: none; }
a:hover { text-decoration: underline; }
main { max-width: 1040px; margin: 0 auto; padding: 24px 16px 48px; }
header { display: flex; justify-content: space-between; align-items: baseline; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 16px; }
header .lockup { display: flex; align-items: center; gap: 10px; }
/* The badge lockup at its intended size. It is a rounded square with its own
   navy ground, so it needs no ring or shadow from this page. */
header .mark { width: 32px; height: 32px; border-radius: 7px; flex: none; }
header h1 { font-size: 20px; margin: 0; font-family: var(--font-display); letter-spacing: -.01em; color: var(--strong); }
.banner h2, section.findings > h2, article.finding h3 { font-family: var(--font-display); color: var(--strong); }
.sub, .stamp, .muted { color: var(--muted); }
.sub { margin: 2px 0 0; font-size: 13px; }
.stamp { font-size: 13px; }
.banner { border: 1px solid var(--line); border-left: 6px solid var(--neutral); background: var(--card); border-radius: var(--radius); padding: 14px 18px; margin-bottom: 16px; }
.banner h2 { margin: 0; font-size: 17px; }
.banner p { margin: 6px 0 0; color: var(--muted); }
.banner.ok { border-left-color: var(--ok); background: var(--ok-bg); }
.banner.blocking { border-left-color: var(--blocking); background: var(--blocking-bg); }
.banner.degraded { border-left-color: var(--degraded); background: var(--degraded-bg); }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); align-items: start; gap: 12px; margin-bottom: 12px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: var(--radius); padding: 14px 16px; }
.card h2 { margin: 0 0 8px; font-size: 13px; text-transform: uppercase; letter-spacing: .05em; color: var(--muted); }
.card p { margin: 4px 0; }
.card .err { color: var(--blocking); }
/* The gate's own card is a table, so it gets the width a table needs rather
   than a third of the row: a wrapped pull request title reads as four rows. */
.card.wide { margin-bottom: 12px; }
table.prs { width: 100%; border-collapse: collapse; margin: 8px 0 4px; }
table.prs td { padding: 5px 10px 5px 0; border-top: 1px solid var(--line); vertical-align: top; }
table.prs td:first-child { white-space: nowrap; }
table.prs td.title { width: 100%; overflow-wrap: anywhere; }
table.prs td:last-child { padding-right: 0; text-align: right; }
.chip { display: inline-block; font-size: 12px; line-height: 1; padding: 4px 8px; border-radius: 999px; background: var(--neutral-bg); color: var(--neutral); white-space: nowrap; }
.chip.passing { background: var(--ok-bg); color: var(--ok); }
.chip.failing, .chip.error { background: var(--blocking-bg); color: var(--blocking); }
.chip.running { background: var(--degraded-bg); color: var(--degraded); }
ul.features { list-style: none; margin: 4px 0; padding: 0; }
ul.features li { margin: 2px 0; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; margin-right: 8px; background: var(--neutral); vertical-align: 1px; }
.dot.on { background: var(--ok); }
section.findings { margin-bottom: 20px; }
section.findings > h2 { font-size: 16px; margin: 0 0 2px; }
section.findings > .blurb { margin: 0 0 10px; color: var(--muted); }
article.finding { background: var(--card); border: 1px solid var(--line); border-radius: var(--radius); padding: 12px 16px; margin-bottom: 10px; }
article.finding h3 { margin: 0; font-size: 15px; display: inline; }
article.finding .chip { margin-left: 8px; }
article.finding .detail { margin: 8px 0 0; white-space: pre-line; }
pre { background: var(--code-bg); border: 1px solid var(--line); border-radius: 6px; padding: 10px 12px; margin: 10px 0 2px; overflow-x: auto; font: 13px/1.45 var(--font-mono); }
footer { border-top: 1px solid var(--line); margin-top: 24px; padding-top: 12px; font-size: 13px; color: var(--muted); }
footer p { margin: 4px 0; }
</style>
</head>
<body>
<main>
<header>
  <div>
    <div class="lockup">
      <img class="mark" src="mark.svg" width="32" height="32" alt="">
      <h1>{{.Brand}}</h1>
    </div>
    <p class="sub">
      watching {{if .RepoLink}}<a href="{{.RepoLink}}">{{.Repo}}</a>{{else}}{{.Repo}}{{end}}
      · check “{{.CheckName}}” · model {{.Model}} · {{.Version}}
    </p>
  </div>
  <p class="stamp">as of {{.Now}} · refreshes every minute</p>
</header>

<section class="banner {{.BannerClass}}">
  <h2>{{.Banner}}</h2>
  {{with .BannerNote}}<p>{{.}}</p>{{end}}
</section>

{{with .Gate}}
<section class="card wide">
  <h2>Gate</h2>
  <p class="muted">swept {{.SweptAgo}}{{with .Poll}}, polling every {{.}}{{end}}</p>
  {{with .Err}}<p class="err">The last sweep could not list pull requests: {{.}}</p>{{end}}
  {{if .Open}}
  <table class="prs">
    {{range .Open}}<tr>
      <td>{{if .URL}}<a href="{{.URL}}">#{{.Number}}</a>{{else}}#{{.Number}}{{end}}</td>
      <td class="title">{{.Title}}</td>
      <td><span class="chip {{.State}}">{{.State}}</span></td>
    </tr>{{end}}
  </table>
  {{else}}<p class="muted">No open pull requests.</p>{{end}}
  <p class="muted">{{.Summary}}</p>
</section>
{{end}}

<div class="grid">
{{with .Triage}}
  <section class="card">
    <h2>Triage</h2>
    <p>{{.Active}}</p>
    {{with .Queued}}<p>{{.}}</p>{{end}}
    <p class="muted">{{.Totals}}</p>
  </section>
{{end}}
{{with .Sweep}}
  <section class="card">
    <h2>Pipeline sweep</h2>
    {{if .On}}
    <p class="muted">swept {{if .SweptAgo}}{{.SweptAgo}}{{else}}never{{end}}, every {{.Every}}</p>
    {{else}}
    <p class="muted">Off. Nothing is watching for the promotions that never happened.</p>
    {{end}}
  </section>
{{end}}
  <section class="card">
    <h2>Posture</h2>
    <ul class="features">
    {{range .Features}}<li><span class="dot{{if .On}} on{{end}}"></span>{{.Name}}</li>
    {{end}}</ul>
    {{with .Egress}}<p class="muted">{{.}}</p>{{end}}
    <p class="muted">{{.Clusters}}</p>
  </section>
</div>

{{range .Sections}}
<section class="findings">
  <h2>{{.Mark}} {{.Title}}</h2>
  <p class="blurb">{{.Blurb}}</p>
  {{range .Finds}}
  <article class="finding">
    <h3>{{.Summary}}</h3>{{with .Since}}<span class="chip">{{.}}</span>{{end}}
    {{with .Detail}}<p class="detail">{{.}}</p>{{end}}
    {{with .Remedy}}<pre><code>{{.}}</code></pre>{{end}}
  </article>
  {{end}}
</section>
{{end}}

<footer>
  {{with .Checked}}<p>{{.}}</p>{{end}}
  {{range .Notes}}<p>⚠ Could not check everything — {{.}}</p>
  {{end}}
  <p>This report as <a href="/pipeline?format=markdown">markdown</a> or <a href="/pipeline?format=text">plain text</a>. Metrics stay on the in-cluster port.</p>
</footer>
</main>
</body>
</html>
`
