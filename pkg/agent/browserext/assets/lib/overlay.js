// The on-page presence of the agent: a cursor that glides to what it is about
// to touch, and a spotlight with corner brackets around an element awaiting
// approval. installOverlay is serialized and evaluated inside the page, so it
// must not reference anything outside its own body.
//
// It renders into a shadow root with a constructable stylesheet: page CSS
// cannot restyle it, and neither a style-src CSP nor Trusted Types blocks it
// (no inline <style>, no innerHTML).

export function installOverlay() {
  const HOST_ID = "__infai_overlay__";
  const existing = window.__infaiOverlay;
  if (existing && document.getElementById(HOST_ID)) return true;
  existing?.teardown();

  const GREEN = "#a7c080";
  const INK = "#0d1310";
  const EASE = "cubic-bezier(0.16, 1, 0.3, 1)";
  const IDLE_FADE_MS = 6000;

  const sheet = new CSSStyleSheet();
  sheet.replaceSync(`
    :host { all: initial; }
    * { box-sizing: border-box; }
    .layer { position: fixed; inset: 0; pointer-events: none; z-index: 2147483647;
      font: 500 12px/1.4 "Geist Mono", ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
    .spot { position: fixed; border-radius: 6px; opacity: 0;
      box-shadow: 0 0 0 100vmax rgba(9, 14, 11, 0.42);
      transition: opacity .35s ${EASE}, left .45s ${EASE}, top .45s ${EASE}, width .45s ${EASE}, height .45s ${EASE}; }
    .spot.on { opacity: 1; }
    .ring { position: absolute; inset: -6px; border-radius: 8px;
      animation: glow 1.8s ease-in-out infinite; }
    .corner { position: absolute; width: 14px; height: 14px; border: 2px solid ${GREEN};
      transition: transform .5s ${EASE}; }
    .tl { left: -6px; top: -6px; border-right: 0; border-bottom: 0; border-top-left-radius: 6px; transform: translate(-10px, -10px); }
    .tr { right: -6px; top: -6px; border-left: 0; border-bottom: 0; border-top-right-radius: 6px; transform: translate(10px, -10px); }
    .bl { left: -6px; bottom: -6px; border-right: 0; border-top: 0; border-bottom-left-radius: 6px; transform: translate(-10px, 10px); }
    .br { right: -6px; bottom: -6px; border-left: 0; border-top: 0; border-bottom-right-radius: 6px; transform: translate(10px, 10px); }
    .spot.on .corner { transform: none; }
    .tag { position: absolute; left: -6px; display: flex; flex-direction: column; gap: 2px;
      padding: 7px 10px 8px; min-width: 210px; max-width: min(420px, 90vw);
      background: ${INK}; color: #dde5d1; border: 1px solid ${GREEN}; border-radius: 4px;
      box-shadow: 0 12px 32px -10px rgba(0, 0, 0, .6), 0 0 0 4px rgba(167, 192, 128, .12);
      opacity: 0; transform: translateY(6px); transition: opacity .35s ${EASE} .1s, transform .45s ${EASE} .1s; }
    .tag.above { bottom: calc(100% + 14px); }
    .tag.below { top: calc(100% + 14px); }
    .spot.on .tag { opacity: 1; transform: none; }
    .tag .line { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .tag .brand { color: ${GREEN}; font-weight: 700; }
    .tag .blink { color: ${GREEN}; animation: blink 1.1s step-end infinite; }
    .tag .hint { color: #80907f; font-weight: 400; font-size: 11px; }
    .cursor { position: fixed; left: 0; top: 0; opacity: 0;
      transition-property: transform, opacity; transition-timing-function: ${EASE};
      filter: drop-shadow(0 4px 10px rgba(0, 0, 0, .45)); }
    .cursor.on { opacity: 1; }
    .cursor svg { display: block; transition: transform .12s ease-out; transform-origin: 3px 3px; }
    .cursor.press svg { transform: scale(.82); }
    .badge { position: absolute; left: 18px; top: 20px; padding: 1px 7px; border-radius: 999px;
      background: ${GREEN}; color: ${INK}; font-weight: 700; font-size: 11px; white-space: nowrap; }
    .ripple { position: fixed; width: 12px; height: 12px; margin: -6px 0 0 -6px; border-radius: 50%;
      border: 2px solid ${GREEN}; animation: ripple .6s ${EASE} forwards; }
    @keyframes glow { 0%, 100% { box-shadow: 0 0 0 0 rgba(167, 192, 128, .55); }
      50% { box-shadow: 0 0 0 7px rgba(167, 192, 128, 0); } }
    @keyframes blink { 0%, 55% { opacity: 1; } 56%, 100% { opacity: 0; } }
    @keyframes ripple { from { transform: scale(1); opacity: 1; } to { transform: scale(4.5); opacity: 0; } }
    @media (prefers-reduced-motion: reduce) { * { animation: none !important; transition-duration: 0s !important; } }
  `);

  const make = (tag, cls, parent) => {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    parent?.append(node);
    return node;
  };

  const host = document.createElement("div");
  host.id = HOST_ID;
  const root = host.attachShadow({ mode: "closed" });
  root.adoptedStyleSheets = [sheet];
  const layer = make("div", "layer", root);

  const spot = make("div", "spot", layer);
  make("div", "ring", spot);
  for (const c of ["tl", "tr", "bl", "br"]) make("div", `corner ${c}`, spot);
  const tag = make("div", "tag above", spot);
  const line = make("div", "line", tag);
  const brand = make("span", "brand", line);
  brand.textContent = "▸ infaiw ";
  const verb = make("span", "", line);
  make("span", "blink", line).textContent = "_";
  make("div", "hint", tag).textContent = "waiting for your ok in the side panel";

  const cursor = make("div", "cursor", layer);
  const NS = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(NS, "svg");
  svg.setAttribute("width", "22");
  svg.setAttribute("height", "24");
  svg.setAttribute("viewBox", "0 0 22 24");
  const arrow = document.createElementNS(NS, "path");
  arrow.setAttribute("d", "M3 2.5 L3 19.5 L7.6 15.4 L10.6 22 L13.6 20.7 L10.7 14.2 L17 14.2 Z");
  arrow.setAttribute("fill", GREEN);
  arrow.setAttribute("stroke", INK);
  arrow.setAttribute("stroke-width", "1.6");
  arrow.setAttribute("stroke-linejoin", "round");
  svg.append(arrow);
  cursor.append(svg);
  make("span", "badge", cursor).textContent = "infaiw";

  (document.body || document.documentElement).append(host);

  // The cursor enters from the bottom-right, like a hand reaching in.
  let pos = { x: window.innerWidth - 60, y: window.innerHeight - 60 };
  let target = null;
  let raf = 0;
  let idleTimer = 0;

  const place = (x, y, ms) => {
    cursor.style.transitionDuration = `${ms}ms, 250ms`;
    cursor.style.transform = `translate(${x - 3}px, ${y - 3}px)`;
    cursor.classList.add("on");
    pos = { x, y };
    clearTimeout(idleTimer);
    idleTimer = setTimeout(() => {
      if (!target) cursor.classList.remove("on");
    }, IDLE_FADE_MS);
  };
  place(pos.x, pos.y, 0);

  const glideMs = (x, y) => {
    const d = Math.hypot(x - pos.x, y - pos.y);
    return d < 2 ? 0 : Math.round(Math.min(750, 260 + d * 0.5));
  };

  const track = () => {
    if (!target) return;
    if (!target.isConnected) {
      api.clear();
      return;
    }
    const r = target.getBoundingClientRect();
    spot.style.left = `${r.left - 4}px`;
    spot.style.top = `${r.top - 4}px`;
    spot.style.width = `${r.width + 8}px`;
    spot.style.height = `${r.height + 8}px`;
    const below = r.top < 90;
    tag.classList.toggle("below", below);
    tag.classList.toggle("above", !below);
  };
  const onViewport = () => {
    cancelAnimationFrame(raf);
    raf = requestAnimationFrame(track);
  };
  window.addEventListener("scroll", onViewport, { capture: true, passive: true });
  window.addEventListener("resize", onViewport, { passive: true });

  const api = {
    focus(el, label) {
      target = el;
      verb.textContent = label;
      track();
      requestAnimationFrame(() => spot.classList.add("on"));
      const r = el.getBoundingClientRect();
      const x = r.left + Math.min(r.width / 2, 24);
      const y = r.top + r.height / 2;
      place(x, y, glideMs(x, y));
    },
    clear() {
      target = null;
      spot.classList.remove("on");
      place(pos.x, pos.y, 0);
    },
    // Glides to (x, y) in viewport pixels and, with press, plays a click.
    // Returns how long the caller should wait before the real input lands.
    point(x, y, press) {
      const ms = glideMs(x, y);
      place(x, y, ms);
      if (!press) return ms;
      setTimeout(() => {
        cursor.classList.add("press");
        const ring = make("div", "ripple", layer);
        ring.style.left = `${x}px`;
        ring.style.top = `${y}px`;
        ring.addEventListener("animationend", () => ring.remove(), { once: true });
        setTimeout(() => cursor.classList.remove("press"), 140);
      }, ms);
      return ms + 120;
    },
    teardown() {
      clearTimeout(idleTimer);
      cancelAnimationFrame(raf);
      window.removeEventListener("scroll", onViewport, { capture: true });
      window.removeEventListener("resize", onViewport);
      host.remove();
      if (window.__infaiOverlay === api) delete window.__infaiOverlay;
    },
  };
  window.__infaiOverlay = api;
  return true;
}
