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
const pageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta http-equiv="refresh" content="60">
<link rel="icon" href="data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 100 100%22><text y=%22.9em%22 font-size=%2290%22>⚓</text></svg>">
<title>{{.Brand}} — promotion pipeline</title>
<style>
:root {
  --bg: #f6f8fa; --card: #ffffff; --fg: #1f2328; --muted: #59636e;
  --line: #d1d9e0; --link: #0969da;
  --ok: #1a7f37; --ok-bg: #dafbe1;
  --blocking: #d1242f; --blocking-bg: #ffebe9;
  --degraded: #9a6700; --degraded-bg: #fff8c5;
  --neutral: #59636e; --neutral-bg: #eff2f5;
  --code-bg: #f0f2f4;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0d1117; --card: #161b22; --fg: #e6edf3; --muted: #8d96a0;
    --line: #30363d; --link: #4493f8;
    --ok: #3fb950; --ok-bg: #12261e;
    --blocking: #f85149; --blocking-bg: #2d1214;
    --degraded: #d29922; --degraded-bg: #272115;
    --neutral: #8d96a0; --neutral-bg: #1c2128;
    --code-bg: #0d1117;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0; background: var(--bg); color: var(--fg);
  font: 15px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
}
a { color: var(--link); text-decoration: none; }
a:hover { text-decoration: underline; }
main { max-width: 1040px; margin: 0 auto; padding: 24px 16px 48px; }
header { display: flex; justify-content: space-between; align-items: baseline; flex-wrap: wrap; gap: 4px 16px; margin-bottom: 16px; }
header h1 { font-size: 20px; margin: 0; }
header h1 .anchor { margin-right: 6px; }
.sub, .stamp, .muted { color: var(--muted); }
.sub { margin: 2px 0 0; font-size: 13px; }
.stamp { font-size: 13px; }
.banner { border: 1px solid var(--line); border-left: 6px solid var(--neutral); background: var(--card); border-radius: 8px; padding: 14px 18px; margin-bottom: 16px; }
.banner h2 { margin: 0; font-size: 17px; }
.banner p { margin: 6px 0 0; color: var(--muted); }
.banner.ok { border-left-color: var(--ok); background: var(--ok-bg); }
.banner.blocking { border-left-color: var(--blocking); background: var(--blocking-bg); }
.banner.degraded { border-left-color: var(--degraded); background: var(--degraded-bg); }
.grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); align-items: start; gap: 12px; margin-bottom: 12px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 14px 16px; }
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
article.finding { background: var(--card); border: 1px solid var(--line); border-radius: 8px; padding: 12px 16px; margin-bottom: 10px; }
article.finding h3 { margin: 0; font-size: 15px; display: inline; }
article.finding .chip { margin-left: 8px; }
article.finding .detail { margin: 8px 0 0; white-space: pre-line; }
pre { background: var(--code-bg); border: 1px solid var(--line); border-radius: 6px; padding: 10px 12px; margin: 10px 0 2px; overflow-x: auto; font: 13px/1.45 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
footer { border-top: 1px solid var(--line); margin-top: 24px; padding-top: 12px; font-size: 13px; color: var(--muted); }
footer p { margin: 4px 0; }
</style>
</head>
<body>
<main>
<header>
  <div>
    <h1><span class="anchor">⚓</span>{{.Brand}}</h1>
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
