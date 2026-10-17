package report

import "html/template"

var pageTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<title>infai report · {{.Title}}</title>
<style>
:root{
  color-scheme:dark;
  --topbar:3.25rem;
  --sidebar:19rem;
  --bg:#272e33;
  --deep:#21282c;
  --surface:#2e383c;
  --raised:#374145;
  --raised-2:#414b50;
  --line:#414b50;
  --line-soft:#384247;
  --text:#d3c6aa;
  --muted:#9da9a0;
  --faint:#859289;
  --red:#e67e80;
  --orange:#e69875;
  --yellow:#dbbc7f;
  --green:#a7c080;
  --aqua:#83c092;
  --blue:#7fbbb3;
  --purple:#d699b6;
  --font-sans:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",Inter,"Helvetica Neue","Cantarell","Ubuntu","Noto Sans",Arial,sans-serif;
  --font-mono:ui-monospace,"SF Mono","Cascadia Code","JetBrains Mono","Fira Code","Iosevka",Menlo,Consolas,"DejaVu Sans Mono",monospace;
  --fs-micro:.8125rem;
  --fs-xs:.875rem;
  --fs-sm:.9375rem;
  --fs-base:1rem;
  --fs-md:1.25rem;
  --fs-lg:1.5rem;
  --s1:.25rem;
  --s2:.5rem;
  --s3:.75rem;
  --s4:1rem;
  --s5:1.5rem;
  --s6:2rem;
  --s7:3rem;
  --s8:4rem;
  --r-sm:.375rem;
  --r-md:.625rem;
  --r-lg:.875rem;
  --step:1.5rem;
  --gutter:1.625rem;
}
*{box-sizing:border-box}
html{font-size:100%;scroll-behavior:smooth;-webkit-text-size-adjust:100%;text-size-adjust:100%}
body{
  margin:0;
  background-color:var(--bg);
  background-image:radial-gradient(72rem 34rem at 76% -12%,rgba(127,187,179,.06),transparent 62%);
  background-repeat:no-repeat;
  color:var(--text);
  font-family:var(--font-sans);
  font-size:var(--fs-base);
  line-height:1.6;
  font-variant-ligatures:none;
  -webkit-font-smoothing:antialiased;
  -moz-osx-font-smoothing:grayscale;
}
button{font:inherit;color:inherit}
a{color:var(--blue);text-decoration:none;text-underline-offset:.18em}
a:hover{text-decoration:underline}
::selection{background:rgba(167,192,128,.26)}
[hidden]{display:none!important}

/* Shell and navigation */
.shell{display:grid;grid-template-columns:var(--sidebar) minmax(0,1fr);min-height:100vh}
.sidebar{position:fixed;inset:0 auto 0 0;width:var(--sidebar);background:var(--deep);border-right:1px solid var(--line-soft);display:flex;flex-direction:column;z-index:20}
.brand{padding:var(--s5) var(--s5) var(--s4);border-bottom:1px solid var(--line-soft)}
.brand-lockup{display:flex;align-items:center;gap:var(--s3)}
.brand-mark{display:block;width:2rem;height:2rem;flex:0 0 auto;border-radius:.5rem;box-shadow:0 0 0 1px var(--line-soft)}
.brand strong{display:block;color:var(--text);font-size:1.5rem;font-weight:700;letter-spacing:-.035em;line-height:1.1}
.brand span{display:block;margin-top:var(--s2);color:var(--faint);font-family:var(--font-mono);font-size:var(--fs-xs);line-height:1.55}
.side-scroll{overflow:auto;overflow-x:hidden;padding:var(--s4);flex:1;scrollbar-width:thin;scrollbar-color:var(--raised-2) transparent}
.side-title{margin:var(--s5) 0 var(--s3);padding:0 var(--s2);color:var(--faint);font-size:var(--fs-xs);font-weight:600;letter-spacing:.13em;text-transform:uppercase}
.side-scroll>.side-title:first-child{margin-top:0}
.side-subtitle{margin:var(--s5) var(--s2) var(--s2);color:var(--faint);font-size:var(--fs-xs);font-weight:600;letter-spacing:.1em;text-transform:uppercase}
.session-switcher{display:grid;gap:var(--s1)}
.session-button{width:100%;display:grid;grid-template-columns:1.25rem minmax(0,1fr);gap:var(--s3);text-align:left;color:var(--muted);background:transparent;border:1px solid transparent;border-radius:var(--r-md);padding:var(--s3);cursor:pointer;overflow:hidden;transition:background .15s ease,border-color .15s ease,color .15s ease}
.session-button:hover{background:var(--surface);color:var(--text)}
.session-button.active{background:rgba(167,192,128,.09);border-color:rgba(167,192,128,.22);color:var(--text);box-shadow:inset .125rem 0 var(--green)}
.session-icon{color:var(--green);font-size:var(--fs-base);line-height:1.4}
.session-button>span:last-child{min-width:0;overflow:hidden}
.session-name{display:block;font-weight:600;font-size:var(--fs-sm);line-height:1.45;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.session-kind{display:block;margin-top:.125rem;color:var(--faint);font-family:var(--font-mono);font-size:var(--fs-xs);line-height:1.5;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}

/* Sidebar jump lists */
.tree{list-style:none;margin:0;padding:0}
.tree li{padding:0}
.tree-link{display:flex;align-items:center;gap:var(--s2);width:100%;border:0;background:transparent;color:var(--muted);padding:var(--s2);border-radius:var(--r-sm);text-align:left;cursor:pointer;overflow:hidden;font-size:var(--fs-sm);line-height:1.45;transition:background .15s ease,color .15s ease}
.tree-link:hover,.tree-link.selected{background:var(--surface);color:var(--text)}
.tree-link.active{color:var(--green)}
.tree-link.off{color:var(--faint)}
.tree-link.head{color:var(--green);background:rgba(167,192,128,.09);font-weight:600}
.tree-link.here{box-shadow:inset .125rem 0 var(--yellow);color:var(--text)}
.tree-dot{width:.5rem;height:.5rem;border:1.5px solid currentColor;border-radius:50%;flex:0 0 auto;opacity:.85}
.tree-link.fork .tree-dot{border-radius:1px;transform:rotate(45deg);color:var(--purple)}
.tree-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}

/* Top bar and page frame */
.content{grid-column:2;min-width:0}
.topbar{position:sticky;top:0;z-index:10;background:rgba(33,40,44,.88);backdrop-filter:blur(16px) saturate(140%);border-bottom:1px solid var(--line-soft);padding:var(--s3) var(--s6);display:flex;align-items:center;gap:var(--s3);min-height:var(--topbar)}
.menu{display:none;background:var(--surface);border:1px solid var(--line);color:var(--yellow);border-radius:var(--r-sm);padding:.375rem .625rem;font-size:var(--fs-xs);cursor:pointer}
.top-title{font-weight:600;font-size:var(--fs-sm);min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.generated{color:var(--faint);font-size:var(--fs-xs);font-weight:500;text-transform:uppercase;letter-spacing:.12em;white-space:nowrap}
.pos{color:var(--muted);font-family:var(--font-mono);font-size:var(--fs-xs);font-variant-numeric:tabular-nums;white-space:nowrap}
.actions{margin-left:auto;display:flex;gap:var(--s2)}
.actions button{background:transparent;color:var(--muted);border:1px solid var(--line);border-radius:var(--r-sm);padding:.375rem .75rem;font-size:var(--fs-xs);cursor:pointer;transition:color .15s,border-color .15s,background .15s}
.actions button:hover{color:var(--text);border-color:var(--faint);background:var(--surface)}
.progress{position:absolute;left:0;right:0;bottom:-1px;height:2px;pointer-events:none}
.progress i{display:block;height:100%;width:0;background:var(--green)}

/* Session hero */
.session-panel{display:none;max-width:80rem;margin:0 auto;padding:var(--s7) var(--s6) var(--s8)}
.session-panel.active{display:block}
.hero{border-bottom:1px solid var(--line-soft);padding-bottom:var(--s5);margin-bottom:var(--s5)}
.eyebrow{display:flex;align-items:center;gap:var(--s2);color:var(--green);font-size:var(--fs-micro);font-weight:600;letter-spacing:.16em;text-transform:uppercase}
.eyebrow:before{content:"";width:.375rem;height:.375rem;border-radius:50%;background:var(--green);box-shadow:0 0 0 .25rem rgba(167,192,128,.12)}
.hero h1{font-weight:600;font-size:clamp(2.25rem,1.5rem + 2.4vw,3.5rem);letter-spacing:-.035em;line-height:1.08;margin:var(--s4) 0 var(--s5);color:#e9e3cf;overflow-wrap:anywhere}
.counts{display:flex;flex-wrap:wrap;gap:var(--s2)}
.count{display:inline-flex;align-items:center;color:var(--faint);background:var(--surface);border:1px solid var(--line-soft);border-radius:999px;padding:.1875rem .625rem;font-family:var(--font-mono);font-size:var(--fs-xs);line-height:1.6;white-space:nowrap}

/* Overview cards */
.overview{display:grid;grid-template-columns:minmax(0,1.4fr) minmax(0,1fr);gap:var(--s5)}
.panel{background:var(--surface);border:1px solid var(--line-soft);border-radius:var(--r-lg);padding:var(--s5);min-width:0}
.panel h2{margin:0 0 var(--s4);color:#e2dbc4;font-size:var(--fs-md);font-weight:600;letter-spacing:-.01em}
.panel p{margin:0 0 var(--s3);color:var(--muted);font-size:var(--fs-sm);line-height:1.7}
.panel p:last-child{margin-bottom:0}
.facts{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:var(--s2) var(--s5);margin:0;font-size:.875rem;line-height:1.55}
.facts dt{color:var(--faint);padding-top:.0625rem}
.facts dd{margin:0;color:var(--muted);font-family:var(--font-mono);overflow-wrap:anywhere}
.dim{color:var(--faint);font-size:var(--fs-xs);line-height:1.7}

/* Last-turn usage */
.usage{margin-top:var(--s5);background:linear-gradient(180deg,var(--surface),rgba(46,56,60,.6));border:1px solid var(--line-soft);border-radius:var(--r-lg);padding:var(--s5)}
.usage .side-title{margin:0 0 var(--s4);padding:0;color:var(--yellow);font-size:var(--fs-micro)}
.usage>.dim{font-family:var(--font-mono)}
.usage-numbers{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));margin:0 0 var(--s5)}
.metric{padding:0 var(--s5);border-left:1px solid var(--line-soft)}
.metric:first-child{padding-left:0;border-left:0}
.metric strong{display:block;color:var(--text);font-family:var(--font-mono);font-size:clamp(1.5rem,1.1rem + .9vw,2rem);font-weight:500;letter-spacing:-.02em;line-height:1.15;font-variant-numeric:tabular-nums}
.metric span{display:block;margin-top:var(--s2);color:var(--faint);font-size:var(--fs-micro);font-weight:500;letter-spacing:.1em;text-transform:uppercase}
.bar-row{display:grid;grid-template-columns:8.5rem minmax(0,1fr) minmax(9rem,auto);gap:var(--s3);align-items:center;margin:var(--s3) 0;color:var(--muted);font-size:var(--fs-xs)}
.usage-progress-bar{height:.4375rem;background:var(--deep);border:1px solid var(--line-soft);border-radius:999px;overflow:hidden}
.usage-progress-fill{display:block;height:100%;border-radius:999px;background:var(--green)}
.usage-progress-fill.completion{background:var(--blue)}
.usage-progress{display:block;width:100%;height:.625rem;border:1px solid var(--line);background:var(--deep);appearance:none}
.usage-progress::-webkit-progress-bar{background:var(--deep)}
.usage-progress::-webkit-progress-value{background:var(--green)}
.usage-progress.completion::-webkit-progress-value{background:var(--blue)}
.usage-progress::-moz-progress-bar{background:var(--green)}
.usage-progress.completion::-moz-progress-bar{background:var(--blue)}
.usage-progress.sr-only{width:1px;height:1px}
.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
.bar-label{color:var(--muted);font-size:var(--fs-xs);font-variant-numeric:tabular-nums}
.overflow{color:var(--red)}

/* Timeline heading and filters */
.timeline-heading{margin:var(--s7) 0 var(--s2);color:#e2dbc4;font-size:var(--fs-md);font-weight:600;letter-spacing:-.01em}
.timeline-note{margin:0 0 var(--s4);color:var(--faint);font-size:var(--fs-xs);line-height:1.7}
.timeline{background:transparent;border:0;padding:0}
.tl-tools{position:sticky;top:var(--topbar);z-index:5;display:flex;flex-wrap:wrap;gap:var(--s2);align-items:center;background:rgba(39,46,51,.92);backdrop-filter:blur(14px);padding:var(--s3) 0;margin-bottom:var(--s3);border-bottom:1px solid var(--line-soft)}
.tl-tools input{flex:1 1 12rem;min-width:0;background:var(--deep);color:var(--text);border:1px solid var(--line);border-radius:var(--r-sm);padding:.5rem .75rem;font:inherit;font-size:var(--fs-sm)}
.tl-tools input::placeholder{color:var(--faint)}
.chip{background:transparent;color:var(--muted);border:1px solid var(--line);border-radius:999px;padding:.3rem .7rem;font-size:var(--fs-xs);cursor:pointer;transition:color .15s,border-color .15s,opacity .15s}
.chip:hover{color:var(--text);border-color:var(--faint)}
.chip[aria-pressed="false"]{opacity:.45}
.tl-count{margin-left:auto;color:var(--faint);font-family:var(--font-mono);font-size:var(--fs-xs);font-variant-numeric:tabular-nums}

/* Branch rail */
.tl-sep{display:grid;grid-template-columns:calc(var(--depth) * var(--step) + var(--gutter)) minmax(0,1fr);margin:var(--s5) 0 var(--s3);--sep:var(--purple)}
.tl-sep.main{--sep:var(--green)}
.tl-sep-rail{position:relative;min-height:1.5rem}
.tl-sep-main{display:flex;align-items:center;gap:var(--s3);min-width:0}
.tl-sep-pill{display:inline-flex;align-items:center;gap:.375rem;flex:0 0 auto;color:var(--sep);border:1px solid var(--sep);border-radius:999px;padding:.25rem .75rem .25rem .625rem;font-size:var(--fs-micro);font-weight:700;letter-spacing:.1em;text-transform:uppercase;background:var(--deep);white-space:nowrap}
.tl-sep-pill b{font-size:var(--fs-sm);line-height:1}
.tl-sep-pill i{font-style:normal;color:var(--muted);font-weight:400;letter-spacing:.04em;text-transform:none}
.tl-sep-wave{flex:1;height:.5rem;color:var(--sep);background:currentColor;opacity:.7;-webkit-mask:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='8' viewBox='0 0 16 8'%3E%3Cpath d='M0 4C4 0 4 0 8 4S12 8 16 4' fill='none' stroke='black' stroke-width='1.6'/%3E%3C/svg%3E") repeat-x 0 50%/16px 8px;mask:url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='8' viewBox='0 0 16 8'%3E%3Cpath d='M0 4C4 0 4 0 8 4S12 8 16 4' fill='none' stroke='black' stroke-width='1.6'/%3E%3C/svg%3E") repeat-x 0 50%/16px 8px;animation:tide 2.8s linear infinite}
@keyframes tide{to{-webkit-mask-position:16px 50%;mask-position:16px 50%}}
@media(prefers-reduced-motion:reduce){.tl-sep-wave{animation:none}}

.tl-row{display:grid;grid-template-columns:calc(var(--depth) * var(--step) + var(--gutter)) minmax(0,1fr);position:relative;margin:0 0 var(--s2);scroll-margin-top:calc(var(--topbar) + 4rem)}
.tl-rail{position:relative;min-height:1.5rem}
.tl-stem,.tl-guide,.tl-elbow,.tl-node{position:absolute;display:block}
.tl-stem{top:-.5rem;bottom:-.5rem;left:calc(var(--depth) * var(--step) + .6875rem);width:2px;background:var(--line-soft)}
.tl-guide{top:-.5rem;bottom:-.5rem;left:calc(var(--level) * var(--step) + .6875rem);width:2px;background:var(--line)}
.tl-elbow{left:calc((var(--depth) - 1) * var(--step) + .6875rem);top:1.4375rem;width:var(--step);height:2px;background:var(--faint);opacity:.6}
.tl-node{left:calc(var(--depth) * var(--step) + .4375rem);top:1.125rem;width:.625rem;height:.625rem;border-radius:50%;background:var(--faint);box-shadow:0 0 0 .1875rem var(--surface);z-index:1}
.tl-row.user .tl-node{background:var(--blue)}
.tl-row.assistant .tl-node{background:var(--green)}
.tl-row.tool .tl-node{background:var(--orange)}
.tl-row.system .tl-node{background:var(--purple)}
.tl-row.compaction .tl-node{background:var(--aqua)}
.tl-row.sidecar .tl-node{background:var(--yellow)}
.tl-row.off .tl-node{opacity:.55}
.tl-row.head .tl-node{box-shadow:0 0 0 .1875rem var(--yellow)}
.tl-row.fork .tl-node{border-radius:1px;transform:rotate(45deg)}

.tl-box{border:1px solid var(--line-soft);border-left:3px solid var(--muted);border-radius:var(--r-md);min-width:0;background:var(--surface);overflow:hidden;transition:border-color .15s,background .15s,box-shadow .15s}
.tl-box[open]{border-color:var(--line);box-shadow:0 .375rem 1.25rem rgba(0,0,0,.12)}
.tl-row.user .tl-box{border-left-color:var(--blue)}
.tl-row.assistant .tl-box{border-left-color:var(--green)}
.tl-row.tool .tl-box{border-left-color:var(--orange)}
.tl-row.system .tl-box{border-left-color:var(--purple)}
.tl-row.compaction .tl-box{border-left-color:var(--aqua)}
.tl-row.sidecar .tl-box{border-left-color:var(--yellow)}
.tl-row.off .tl-box{border-left-style:dashed;opacity:.8}
.tl-row.head .tl-box{background:#333f39;border-color:rgba(167,192,128,.32)}
.tl-row.flash .tl-box{animation:flash 1.2s ease-out}
@keyframes flash{0%{box-shadow:0 0 0 2px var(--yellow)}100%{box-shadow:0 0 0 0 transparent}}

.tl-summary{display:block;position:relative;padding:.75rem 2.75rem .75rem 1rem;cursor:pointer;list-style:none;border-radius:var(--r-md)}
.tl-summary:after{content:"";position:absolute;right:1rem;top:1.25rem;width:.4375rem;height:.4375rem;border-top:1.5px solid var(--faint);border-right:1.5px solid var(--faint);transform:rotate(45deg);transition:transform .15s ease,border-color .15s}
.tl-box[open]>.tl-summary:after{transform:rotate(135deg);border-color:var(--yellow)}
.tl-summary::-webkit-details-marker{display:none}
.tl-summary:hover{background:#333d41}
.tl-line{display:flex;align-items:baseline;gap:.375rem;line-height:1.55}
.tl-line+.tl-line{margin-top:var(--s1)}
.tl-cursor{width:1.1ch;flex:0 0 auto;color:var(--yellow);font-weight:700}
.tl-fork{color:var(--purple);flex:0 0 auto}
.tl-body{min-width:0;overflow-wrap:anywhere}
.tl-role{font-family:var(--font-mono);font-size:var(--fs-xs);font-weight:600;white-space:nowrap}
.tl-role.user{color:var(--blue)}
.tl-role.assistant{color:var(--green)}
.tl-role.thinking{color:var(--faint);font-weight:500}
.tl-role.tool_call{color:var(--purple)}
.tl-role.tool_result{color:var(--orange)}
.tl-role.system{color:var(--purple)}
.tl-role.event{color:var(--orange)}
.tl-text{color:var(--text);font-size:var(--fs-base)}
.tl-role.thinking+.tl-text{color:var(--muted)}

.tl-detail{padding:var(--s4) var(--s5) var(--s5);border-top:1px solid var(--line-soft);margin-left:0;background:rgba(33,40,44,.35);border-radius:0 0 var(--r-md) var(--r-md)}
.tl-meta{display:flex;gap:var(--s3);align-items:center;flex-wrap:wrap;color:var(--faint);font-family:var(--font-mono);font-size:var(--fs-xs);padding:0 0 var(--s4)}
.badge{display:inline-flex;align-items:center;background:var(--raised);border-radius:.25rem;padding:.125rem .4375rem;font-family:var(--font-mono);font-size:var(--fs-micro);font-weight:600;text-transform:uppercase;letter-spacing:.08em;line-height:1.5;vertical-align:middle}
.badge.head{background:var(--green);color:var(--deep);font-weight:800}
.badge.fork{color:var(--purple)}
.badge.branch{color:var(--purple);border:1px solid var(--purple)}
.badge.main{color:var(--green);border:1px solid var(--green)}
.badge.off{color:var(--muted)}
.badge.ok{color:var(--green)}
.badge.err{color:var(--red)}

/* Markdown and payloads */
.markdown{margin-top:var(--s4);max-width:78ch;overflow-wrap:anywhere;font-size:var(--fs-base);line-height:1.72}
.markdown>*:first-child{margin-top:0}
.markdown>*:last-child{margin-bottom:0}
.markdown h1,.markdown h2,.markdown h3,.markdown h4{color:#e2dbc4;font-weight:600;letter-spacing:-.01em;line-height:1.3}
.markdown h1{font-size:1.5em;margin:1.2em 0 .5em}
.markdown h2{font-size:1.25em;margin:1.3em 0 .5em}
.markdown h3{font-size:1.08em;margin:1.2em 0 .4em}
.markdown p{margin:.7em 0}
.markdown ul,.markdown ol{margin:.7em 0;padding-left:1.4em}
.markdown li{margin:.25em 0}
.markdown pre,.call pre,details pre{background:var(--deep);border:1px solid var(--line-soft);border-radius:var(--r-sm);padding:var(--s4);overflow:auto;white-space:pre-wrap;word-break:break-word;font-family:var(--font-mono);font-size:.875rem;line-height:1.75}
.markdown code{color:var(--yellow);font-family:var(--font-mono);font-size:.9em}
.markdown :not(pre)>code{background:var(--deep);border:1px solid var(--line-soft);border-radius:.25rem;padding:.05em .3em}
.markdown blockquote{margin:.8em 0;padding:.1em 0 .1em .9em;border-left:2px solid var(--purple);color:var(--muted)}
.markdown table{display:block;max-width:100%;overflow:auto;border-collapse:collapse;font-size:var(--fs-sm)}
.markdown th,.markdown td{border:1px solid var(--line);padding:.4rem .7rem;text-align:left}
.markdown th{background:var(--raised);color:var(--yellow);font-weight:600}
.thinking{border-left:2px solid var(--line);padding-left:var(--s4);color:var(--muted)}
.markdown.thinking{font-size:var(--fs-sm)}

.call{background:var(--deep);border:1px solid var(--line-soft);border-left:3px solid var(--purple);border-radius:var(--r-sm);padding:var(--s4);margin-top:var(--s4)}
.call b{color:var(--purple);font-family:var(--font-mono);font-size:var(--fs-sm);font-weight:600}
.call-head{display:flex;gap:var(--s2);align-items:baseline;flex-wrap:wrap;overflow-wrap:anywhere;padding-bottom:var(--s3);border-bottom:1px solid var(--line-soft);min-width:0}
.call-head .dim{font-family:var(--font-mono);font-size:var(--fs-micro);overflow-wrap:anywhere}
.command{margin:var(--s3) 0 0;background:transparent;border:0;padding:.375rem 0;white-space:pre-wrap;word-break:break-word;font-family:var(--font-mono);font-size:var(--fs-sm);line-height:1.75}
.command .prompt{color:var(--yellow);margin-right:.4rem}
.args{display:grid;grid-template-columns:max-content minmax(0,1fr);gap:var(--s2) var(--s4);margin:var(--s3) 0 0;font-size:var(--fs-xs)}
.args dt{color:var(--faint);font-family:var(--font-mono)}
.args dd{margin:0;white-space:pre-wrap;word-break:break-word;font-family:var(--font-mono)}
.tool-result{margin-top:var(--s3)}
.tool-output{margin:var(--s3) 0 0;background:var(--deep);border:1px solid var(--line-soft);border-radius:var(--r-sm);padding:var(--s4);white-space:pre-wrap;word-break:break-word;font-family:var(--font-mono);font-size:.875rem;line-height:1.7;max-height:26rem;overflow:auto;scrollbar-width:thin;scrollbar-color:var(--raised-2) transparent}
.arg{display:flex;gap:var(--s3);padding:.15rem 0;color:var(--muted);font-size:var(--fs-xs)}
.arg-key{min-width:6rem;color:var(--faint);font-family:var(--font-mono)}
.tl-detail details{margin-top:var(--s3)}
.tl-detail details>summary{cursor:pointer;color:var(--faint);font-size:var(--fs-xs);padding:.4rem 0;letter-spacing:.01em}

/* Media */
.media-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(14rem,1fr));gap:var(--s3);margin-top:var(--s3)}
.media{background:var(--deep);border:1px solid var(--line-soft);border-radius:var(--r-md);padding:.625rem}
.media img{display:block;width:100%;max-height:30rem;object-fit:contain;border:1px solid var(--line-soft);border-radius:var(--r-sm);cursor:zoom-in}
.media-meta{margin-top:var(--s2);color:var(--faint);font-family:var(--font-mono);font-size:var(--fs-xs);line-height:1.6;overflow-wrap:anywhere}
.lightbox{position:fixed;inset:0;display:none;place-items:center;background:rgba(18,22,24,.94);backdrop-filter:blur(8px);z-index:100;padding:var(--s6);cursor:zoom-out}
.lightbox.open{display:grid}
.lightbox img{max-width:96vw;max-height:90vh;object-fit:contain;border-radius:var(--r-sm)}
.lightbox button{position:absolute;right:var(--s5);top:var(--s5);background:var(--surface);border:1px solid var(--line);color:var(--text);border-radius:var(--r-sm);padding:.5rem .75rem;font-size:var(--fs-xs);cursor:pointer}

/* Copy and keyboard affordances */
.copyable{position:relative}
.copy{position:absolute;top:.5rem;right:.5rem;background:var(--raised);color:var(--muted);border:1px solid var(--line);border-radius:.25rem;padding:.125rem .5rem;font-size:var(--fs-micro);cursor:pointer;opacity:0;transition:opacity .12s}
.copyable:hover .copy,.copy:focus-visible,.copy.done{opacity:1}
.copy.done{color:var(--green);border-color:var(--green)}
:focus-visible{outline:2px solid var(--yellow);outline-offset:2px}
kbd{background:var(--deep);border:1px solid var(--line);border-bottom-width:2px;border-radius:.25rem;padding:.0625rem .375rem;font-family:var(--font-mono);font-size:var(--fs-micro);color:var(--muted)}

@media(max-width:68.75em){
  .session-panel{padding-left:var(--s5);padding-right:var(--s5)}
  .overview{grid-template-columns:minmax(0,1fr)}
  .metric{padding:0 var(--s4)}
}
@media(max-width:51.25em){
  .shell{display:block}
  .sidebar{transform:translateX(-100%);transition:transform .22s ease;width:min(88vw,20rem);box-shadow:1rem 0 2.5rem rgba(0,0,0,.4)}
  body.nav-open .sidebar{transform:none}
  .content{grid-column:auto}
  .menu{display:block}
  .topbar{padding:var(--s3) var(--s4)}
  .topbar .generated{display:none}
  .session-panel{padding:var(--s5) var(--s4) var(--s8)}
  .hero h1{font-size:clamp(1.9rem,1.4rem + 3.4vw,2.5rem)}
  .usage,.panel{padding:var(--s4)}
  .usage-numbers{grid-template-columns:repeat(2,minmax(0,1fr));gap:var(--s4) 0}
  .metric{padding:0 var(--s4)}
  .metric:nth-child(odd){padding-left:0;border-left:0}
  .metric strong{font-size:1.5rem}
  .bar-row{grid-template-columns:minmax(0,1fr);gap:var(--s2)}
  .bar-label{grid-column:1}
  .actions{display:none}
  .facts{grid-template-columns:minmax(0,1fr)}
  .facts dd{margin-bottom:var(--s2)}
  .tl-summary{padding-right:2.25rem}
  .tl-detail{padding:var(--s3) var(--s3) var(--s4)}
  .tl-sep-main{flex-wrap:wrap}
  .tl-sep-pill{padding:.2rem .6rem;font-size:var(--fs-micro)}
  .tl-tools input{flex-basis:100%}
  .tl-count{font-size:var(--fs-micro)}
  .copy{opacity:1}
  .brand{padding-top:var(--s5)}
}
</style>
</head>
<body>
<div class="shell">
<aside class="sidebar" id="sidebar">
<div class="brand"><div class="brand-lockup"><svg class="brand-mark" viewBox="0 0 512 512" aria-hidden="true" focusable="false"><rect width="512" height="512" rx="113" fill="#11171a"/><path fill="#d3c6aa" d="M96 192h64v64h-64zM352 192h64v64h-64zM96 256h64v64h-64zM224 256h64v64h-64zM352 256h64v64h-64zM96 320h64v64h-64zM224 320h64v64h-64zM352 320h64v64h-64zM96 384h320v64h-320z"/><path fill="#83c092" d="M96 64h64v64h-64zM352 64h64v64h-64zM224 128h64v64h-64z"/></svg><strong>infaiw</strong></div><span>branch timeline · {{.Generated}}</span></div>
<div class="side-scroll">
<h2 class="side-title">Sessions</h2>
<nav class="session-switcher" aria-label="Session switcher">
{{range .Sessions}}<button class="session-button{{if .Root}} active{{end}}" type="button" data-session-target="{{.DOMID}}" aria-pressed="{{if .Root}}true{{else}}false{{end}}"><span class="session-icon">{{.Icon}}</span><span><span class="session-name">{{.Title}}</span><span class="session-kind">{{if .Root}}root · {{else}}offspring · {{end}}{{.Kind}}</span></span></button>{{end}}
</nav>
<h2 class="side-title">Jump to</h2>
{{range .Sessions}}<div class="branch-outline{{if .Root}} active{{end}}" data-outline-for="{{.DOMID}}"{{if not .Root}} hidden{{end}}><div class="side-subtitle">user turns</div><ul>{{range .Jumps}}<li><button type="button" class="tree-link{{if .Event.Active}} active{{else}} off{{end}}{{if .Event.Head}} head{{end}}{{if .Event.Fork}} fork{{end}}" data-scroll-event="{{.Event.DOMID}}" title="{{.Event.Label}} {{.Event.ShortID}}"><span class="tree-dot"></span><span class="tree-label">{{with index .Event.Displays 0}}{{.Text}}{{end}}</span></button></li>{{end}}</ul><div class="side-subtitle">graph landmarks</div><ul>{{range .Tree}}<li><button type="button" class="tree-link{{if .Event.Active}} active{{else}} off{{end}}{{if .Event.Head}} head{{end}}{{if .Event.Fork}} fork{{end}}" data-scroll-event="{{.Event.DOMID}}" title="{{.Event.Label}} {{.Event.ShortID}}"><span class="tree-dot"></span><span>{{.Event.Icon}}</span><span class="tree-label">{{.Event.Label}}{{if .Event.ForkPoint}} · {{.Event.ChildCount}} arms{{end}}{{if .Event.Head}} · HEAD{{end}}</span></button></li>{{end}}</ul></div>{{end}}
</div>
</aside>
<div class="content">
<header class="topbar"><button class="menu" type="button" aria-controls="sidebar" aria-expanded="false">☰ sessions</button><span class="top-title" data-top-title>{{.Title}}</span><span class="generated">complete branch report</span><span class="pos" data-pos></span><span class="actions"><button type="button" data-details="open">expand all</button><button type="button" data-details="close">collapse all</button></span><span class="progress" aria-hidden="true"><i data-progress></i></span></header>
<main>
{{range .Sessions}}<article class="session-panel{{if .Root}} active{{end}}" id="{{.DOMID}}" data-session-index="{{.Index}}">
<section class="hero"><div class="eyebrow">{{if .Root}}root session{{else}}offspring session{{end}} · {{.Kind}}</div><h1>{{.Title}}</h1><div class="counts">{{range .Counts}}<span class="count">{{.Key}}: {{.Value}}</span>{{end}}</div></section>
<div class="overview"><section class="panel"><h2>Session details</h2><dl class="facts">{{range .Meta}}<dt>{{.Key}}</dt><dd>{{.Value}}</dd>{{end}}</dl></section><section class="panel"><h2>Field guide</h2><p>Each event expands into the lines it produced: <span class="tl-role thinking">thinking:</span>, <span class="tl-role assistant">assistant:</span>, <span class="tl-role tool_call">tool_call:</span>, <span class="tl-role tool_result">tool_result:</span>.</p><p class="dim"><span class="tl-cursor">*</span> marks the current event. <span class="tl-fork">⎇</span> marks a branch arm. Click a line to open the full payload.</p><p class="dim">Keys: <kbd>j</kbd> <kbd>k</kbd> move between events · <kbd>Enter</kbd> open · <kbd>/</kbd> filter · <kbd>Esc</kbd> close.</p></section></div>
{{with .UsageProgress}}<section class="usage usage-progress-panel" data-feature="usage-progress" data-usage-progress><h2 class="side-title">Last-turn usage</h2>{{if not .Present}}<p class="dim">No last-turn token usage was recorded for this session.</p>{{end}}<div class="usage-numbers"><div class="metric"><strong>{{.Prompt}}</strong><span>prompt</span></div><div class="metric"><strong>{{.Completion}}</strong><span>completion</span></div><div class="metric"><strong>{{.Total}}</strong><span>total</span></div><div class="metric"><strong>{{.Window}}</strong><span>context window</span></div></div><div class="bar-row"><span>prompt / context</span><div class="usage-progress-bar prompt-context-progress" role="progressbar" aria-label="Prompt context usage progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow="{{printf "%.1f" .PromptPercent}}"><span class="usage-progress-fill usage-fill" style="width:{{printf "%.2f" .PromptPercent}}%"></span></div><progress class="usage-progress sr-only" max="100" value="{{printf "%.2f" .PromptPercent}}">{{printf "%.1f" .PromptPercent}}%</progress><span class="bar-label{{if .Overflow}} overflow{{end}}">{{.PromptLabel}}{{if .Overflow}} · exceeds window{{end}}</span></div><div class="bar-row"><span>completion</span><div class="usage-progress-bar completion-progress" role="progressbar" aria-label="Completion usage progress relative to context window" aria-valuemin="0" aria-valuemax="100" aria-valuenow="{{printf "%.1f" .CompletionPercent}}"><span class="usage-progress-fill completion" style="width:{{printf "%.2f" .CompletionPercent}}%"></span></div><progress class="usage-progress sr-only" max="100" value="{{printf "%.2f" .CompletionPercent}}">{{printf "%.1f" .CompletionPercent}}%</progress><span class="bar-label">{{.CompletionLabel}}</span></div><div class="dim">cached {{.Cached}} · cache write {{.CacheWrite}} · reasoning {{.Reasoning}}</div></section>{{end}}
<h2 class="timeline-heading">Branch timeline</h2><p class="timeline-note"><span class="tl-cursor">*</span> marks the current event. <span class="tl-fork">⎇</span> marks a fork. Click any line to open its full payload.</p>
<div class="tl-tools" role="search"><input type="search" data-filter-text placeholder="filter events…  ( / )" aria-label="Filter events by text"><button type="button" class="chip" data-filter-role="user" aria-pressed="true">▸ user</button><button type="button" class="chip" data-filter-role="assistant" aria-pressed="true">◆ assistant</button><button type="button" class="chip" data-filter-role="thinking" aria-pressed="true">thinking</button><button type="button" class="chip" data-filter-role="tools" aria-pressed="true">⚙ tools</button><span class="tl-count" data-filter-count></span><button type="button" class="chip" data-filter-reset hidden>reset</button></div>
<div class="timeline">{{range .Events}}{{if .Enter}}<div class="tl-sep{{if not .Fork}} main{{end}}" style="--depth:{{.Depth}}"><div class="tl-sep-rail">{{range .Guides}}<span class="tl-guide" style="--level:{{.}}"></span>{{end}}</div><div class="tl-sep-main"><span class="tl-sep-pill"><b>⎇</b>{{if .Fork}}branch<i>fork of {{.ForkFrom}}</i>{{else}}main<i>continues</i>{{end}}</span><span class="tl-sep-wave" aria-hidden="true"></span></div></div>{{end}}<div class="tl-row {{.Class}}{{if not .Active}} off{{end}}{{if .Head}} head{{end}}{{if .Fork}} fork{{end}}" id="{{.DOMID}}" data-event-id="{{.ID}}" data-parent-id="{{.ParentID}}" data-index="{{.Index}}" data-roles="{{range .Displays}}{{.Role}} {{end}}" style="--depth:{{.Depth}}">
<div class="tl-rail"><span class="tl-stem"></span>{{range .Guides}}<span class="tl-guide" style="--level:{{.}}"></span>{{end}}{{if .Enter}}<span class="tl-elbow"></span>{{end}}<span class="tl-node"></span></div>
<details class="tl-box">
<summary class="tl-summary">{{range .Displays}}<span class="tl-line"><span class="tl-cursor">{{if .Current}}*{{end}}</span><span class="tl-body">{{range .Badges}}<span class="badge {{.}}">{{.}}</span> {{end}}<span class="tl-role {{.Role}}">{{.Role}}:</span> <span class="tl-text">{{.Text}}</span></span></span>{{end}}</summary>
<div class="tl-detail">
<div class="tl-meta">{{if .Head}}<span class="badge head">HEAD</span>{{end}}{{if not .Active}}<span class="badge off">off branch</span>{{end}}{{if .Fork}}<span class="badge fork">⌁ fork of {{.ForkFrom}}</span>{{end}}{{if .Status}}<span class="badge {{if eq .Status "success"}}ok{{else}}err{{end}}">{{.Status}}</span>{{end}}{{if .ParentID}}<a href="#{{.ParentDOMID}}" data-scroll-event="{{.ParentDOMID}}">parent {{.ParentShort}}</a>{{end}}{{if .BlobHash}}<span>blob {{.BlobHash}}</span>{{end}}{{if .ForkPoint}}<span>{{.ChildCount}} arms leave this event</span>{{end}}</div>
{{if .Unresolved}}<div class="dim">payload unavailable — showing the stored preview only</div>{{end}}{{if .Reasoning}}<div class="markdown thinking">{{.Reasoning}}</div>{{end}}{{if and .Text (not .Result)}}<div class="markdown">{{.Text}}</div>{{end}}{{if .Summary}}<div class="markdown">{{.Summary}}</div>{{end}}{{if .Checklist}}<details><summary>task checklist</summary><pre>{{.Checklist}}</pre></details>{{end}}
{{with .Result}}<div class="tool-result">{{if .ExitCode}}<span class="badge {{if .Failed}}err{{else}}ok{{end}}">exit {{.ExitCode}}</span>{{end}}<pre class="tool-output">{{.Output}}</pre>{{range .Rows}}<div class="arg"><span class="arg-key">{{.Key}}</span><span class="arg-value">{{.Value}}</span></div>{{end}}</div>{{end}}
{{range .ToolCalls}}<div class="call" id="{{.Anchor}}"><div class="call-head"><b>⚙ {{.Name}}</b><span class="dim">{{.ID}}</span></div>{{if .Command}}<pre class="command"><span class="prompt">$</span> {{.Command}}</pre>{{end}}{{if .ArgRows}}<dl class="args">{{range .ArgRows}}<dt>{{.Key}}</dt><dd>{{.Value}}</dd>{{end}}</dl>{{end}}<details><summary>raw arguments</summary><pre>{{.Args}}</pre></details></div>{{end}}{{if .ToolCallID}}<div class="dim">answers call {{if .CallLink}}<a href="#{{.CallLink}}">{{.ToolCallID}}</a>{{else}}{{.ToolCallID}}{{end}}</div>{{end}}
{{if .Images}}<div class="media-grid">{{range .Images}}<div class="media">{{if .Src}}<img src="{{.Src}}" alt="{{.Name}}" loading="lazy" data-lightbox>{{end}}<div class="media-meta">{{.Name}} · {{.MediaType}} · {{.Size}} bytes{{if .Width}} · {{.Width}}×{{.Height}}{{end}}{{if .Prompt}} · prompt: {{.Prompt}}{{end}}</div></div>{{end}}</div>{{end}}
<details><summary>full payload · raw JSON</summary><pre>{{.Raw}}</pre></details>
</div></details></div>{{end}}</div>
</article>{{end}}
</main></div></div>
<div class="lightbox" role="dialog" aria-modal="true" aria-label="Expanded image" tabindex="-1"><button type="button" aria-label="Close image">close ×</button><img alt=""></div>
<script>
(function(){
'use strict';
const body=document.body,
panels=[...document.querySelectorAll('.session-panel')],
buttons=[...document.querySelectorAll('[data-session-target]')],
outlines=[...document.querySelectorAll('[data-outline-for]')],
menu=document.querySelector('.menu'),
topTitle=document.querySelector('[data-top-title]'),
pos=document.querySelector('[data-pos]'),
bar=document.querySelector('[data-progress]'),
filters=new Map(),
groupOf={user:'user',assistant:'assistant',thinking:'thinking',tool_call:'tools',tool_result:'tools'};
let currentRow=null,cursor=null,cursorAt=0,raf=0;
const header=document.querySelector('.topbar');
new ResizeObserver(()=>document.documentElement.style.setProperty('--topbar',header.getBoundingClientRect().height+'px')).observe(header);
function activePanel(){return document.querySelector('.session-panel.active')}
function visibleRows(p){return [...p.querySelectorAll('.tl-row:not([hidden])')]}
function selectSession(id){
  let title='';
  panels.forEach(p=>{const on=p.id===id;p.classList.toggle('active',on);if(on){const h=p.querySelector('h1');if(h)title=h.textContent}});
  buttons.forEach(b=>{const on=b.dataset.sessionTarget===id;b.classList.toggle('active',on);b.setAttribute('aria-pressed',String(on))});
  outlines.forEach(o=>{const on=o.dataset.outlineFor===id;o.classList.toggle('active',on);o.hidden=!on});
  if(title)topTitle.textContent=title;
  body.classList.remove('nav-open');menu.setAttribute('aria-expanded','false');
  window.scrollTo({top:0,behavior:'instant'});cursor=null;sync();
}
buttons.forEach(b=>b.addEventListener('click',()=>selectSession(b.dataset.sessionTarget)));
// An event or call inside a closed <details> is unreachable until its ancestors open.
function openAncestors(el){for(let n=el.parentElement;n;n=n.parentElement){if(n.tagName==='DETAILS')n.open=true}}
function jump(id){
  const el=document.getElementById(id);if(!el)return false;
  const panel=el.closest('.session-panel'),row=el.closest('.tl-row');
  if(panel&&!panel.classList.contains('active'))selectSession(panel.id);
  if(row&&row.hidden&&panel&&filters.has(panel))filters.get(panel).reset();
  openAncestors(el);
  el.scrollIntoView({behavior:'smooth',block:'center'});
  if(row){row.classList.remove('flash');void row.offsetWidth;row.classList.add('flash')}
  document.querySelectorAll('.tree-link.selected').forEach(n=>n.classList.remove('selected'));
  document.querySelectorAll('[data-scroll-event="'+CSS.escape(id)+'"]').forEach(n=>n.classList.add('selected'));
  body.classList.remove('nav-open');
  return true;
}
document.querySelectorAll('[data-scroll-event]').forEach(el=>el.addEventListener('click',e=>{e.preventDefault();e.stopPropagation();jump(el.dataset.scrollEvent)}));
document.addEventListener('click',e=>{
  const a=e.target.closest('a[href^="#"]');
  if(!a||a.dataset.scrollEvent)return;
  let id='';try{id=decodeURIComponent(a.getAttribute('href').slice(1))}catch(x){return}
  if(id&&jump(id))e.preventDefault();
});
menu.addEventListener('click',e=>{const open=body.classList.toggle('nav-open');e.currentTarget.setAttribute('aria-expanded',String(open))});
document.addEventListener('click',e=>{if(body.classList.contains('nav-open')&&!e.target.closest('.sidebar')&&!e.target.closest('.menu'))body.classList.remove('nav-open')});
// Expanding opens the event boxes only; the raw payloads inside stay shut so the page stays readable.
document.querySelectorAll('[data-details]').forEach(b=>b.addEventListener('click',()=>{
  const open=b.dataset.details==='open',p=activePanel();if(!p)return;
  p.querySelectorAll('.tl-row:not([hidden]) .tl-box').forEach(d=>d.open=open);
  if(!open)p.querySelectorAll('.tl-row .tl-detail details').forEach(d=>d.open=false);
}));
// Filter: free text over each event's whole box, plus role chips.
panels.forEach(p=>{
  const input=p.querySelector('[data-filter-text]'),chips=[...p.querySelectorAll('[data-filter-role]')],
  count=p.querySelector('[data-filter-count]'),resetBtn=p.querySelector('[data-filter-reset]'),
  rows=[...p.querySelectorAll('.tl-row')],seps=[...p.querySelectorAll('.tl-sep')],cache=new WeakMap();
  if(!input)return;
  function haystack(r){let t=cache.get(r);if(t===undefined){t=r.querySelector('.tl-box').textContent.toLowerCase();cache.set(r,t)}return t}
  function apply(){
    const q=input.value.trim().toLowerCase(),on={};
    chips.forEach(c=>{on[c.dataset.filterRole]=c.getAttribute('aria-pressed')==='true'});
    let shown=0;
    rows.forEach(r=>{
      const groups=r.dataset.roles.trim().split(/\s+/).map(x=>groupOf[x]||'other');
      const show=groups.some(g=>g==='other'||on[g])&&(!q||haystack(r).indexOf(q)>=0);
      r.hidden=!show;if(show)shown++;
    });
    seps.forEach(s=>{const n=s.nextElementSibling;s.hidden=!!(n&&n.hidden)});
    count.textContent=shown===rows.length?rows.length+' events':shown+' of '+rows.length+' events';
    resetBtn.hidden=!q&&chips.every(c=>c.getAttribute('aria-pressed')==='true');
    sync();
  }
  function reset(){input.value='';chips.forEach(c=>c.setAttribute('aria-pressed','true'));apply()}
  input.addEventListener('input',apply);
  chips.forEach(c=>c.addEventListener('click',()=>{c.setAttribute('aria-pressed',String(c.getAttribute('aria-pressed')!=='true'));apply()}));
  resetBtn.addEventListener('click',reset);
  filters.set(p,{apply:apply,reset:reset});
  apply();
});
// Where am I: current event, scroll progress, and the matching entry in the sidebar jump list.
function sync(){
  raf=0;
  const p=activePanel();if(!p)return;
  const max=document.documentElement.scrollHeight-window.innerHeight;
  bar.style.width=(max>0?Math.min(100,window.scrollY/max*100):0)+'%';
  const rows=visibleRows(p);
  if(!rows.length){pos.textContent='no matching events';currentRow=null;return}
  let cur=rows[0];
  for(const r of rows){if(r.getBoundingClientRect().top<=140)cur=r;else break}
  currentRow=cur;
  pos.textContent='#'+cur.dataset.index+' / '+p.querySelectorAll('.tl-row').length;
  document.querySelectorAll('[data-outline-for="'+p.id+'"] ul').forEach(ul=>{
    let here=null;
    ul.querySelectorAll('.tree-link').forEach(l=>{
      const t=document.getElementById(l.dataset.scrollEvent);
      if(t&&(t===cur||(t.compareDocumentPosition(cur)&Node.DOCUMENT_POSITION_FOLLOWING)))here=l;
    });
    ul.querySelectorAll('.tree-link').forEach(l=>l.classList.toggle('here',l===here));
  });
}
window.addEventListener('scroll',()=>{if(!raf)raf=requestAnimationFrame(sync)},{passive:true});
window.addEventListener('resize',()=>{if(!raf)raf=requestAnimationFrame(sync)});
// Keyboard: j/k walk the visible events and focus the row, so Enter/Space opens it natively.
function step(dir){
  const p=activePanel();if(!p)return;
  const rows=visibleRows(p);if(!rows.length)return;
  const base=(cursor&&Date.now()-cursorAt<700&&rows.includes(cursor))?cursor:currentRow;
  let i=rows.indexOf(base);
  i=i<0?0:Math.max(0,Math.min(rows.length-1,i+dir));
  cursor=rows[i];cursorAt=Date.now();
  jump(cursor.id);
  const s=cursor.querySelector('summary');if(s)s.focus({preventScroll:true});
}
// Copy buttons on commands, outputs, code blocks and raw payloads.
function copyText(text){
  if(navigator.clipboard&&window.isSecureContext)return navigator.clipboard.writeText(text);
  return new Promise((resolve,reject)=>{
    const a=document.createElement('textarea');a.value=text;a.style.position='fixed';a.style.opacity='0';
    document.body.appendChild(a);a.select();
    try{document.execCommand('copy')?resolve():reject()}catch(x){reject(x)}
    a.remove();
  });
}
document.querySelectorAll('.tl-detail pre').forEach(pre=>{
  const wrap=document.createElement('div'),btn=document.createElement('button');
  wrap.className='copyable';btn.type='button';btn.className='copy';btn.textContent='copy';btn.setAttribute('aria-label','Copy to clipboard');
  btn.addEventListener('click',()=>{
    const text=pre.classList.contains('command')?pre.textContent.replace(/^\$ /,''):pre.textContent;
    copyText(text).then(()=>{btn.textContent='copied';btn.classList.add('done')},()=>{btn.textContent='failed'}).then(()=>setTimeout(()=>{btn.textContent='copy';btn.classList.remove('done')},1200));
  });
  pre.parentNode.insertBefore(wrap,pre);wrap.appendChild(pre);wrap.appendChild(btn);
});
const box=document.querySelector('.lightbox'),boxImg=box.querySelector('img');
function closeBox(){box.classList.remove('open');boxImg.removeAttribute('src')}
document.querySelectorAll('img[data-lightbox]').forEach(img=>img.addEventListener('click',()=>{boxImg.src=img.src;boxImg.alt=img.alt;box.classList.add('open');box.focus()}));
box.addEventListener('click',e=>{if(e.target===box||e.target.closest('button'))closeBox()});
document.addEventListener('keydown',e=>{
  if(e.key==='Escape'){
    closeBox();body.classList.remove('nav-open');
    const f=document.activeElement;if(f&&f.matches&&f.matches('[data-filter-text]'))f.blur();
    return;
  }
  if(e.metaKey||e.ctrlKey||e.altKey)return;
  const t=e.target;
  if(t&&(t.tagName==='INPUT'||t.tagName==='TEXTAREA'||t.isContentEditable))return;
  if(box.classList.contains('open'))return;
  if(e.key==='/'){const p=activePanel(),i=p&&p.querySelector('[data-filter-text]');if(i){e.preventDefault();i.focus();i.select()}}
  else if(e.key==='j')step(1);
  else if(e.key==='k')step(-1);
});
if(location.hash.length>1){
  let id='';try{id=decodeURIComponent(location.hash.slice(1))}catch(x){}
  if(id)setTimeout(()=>jump(id),0);
}
sync();
})();
</script>
</body></html>
`))
