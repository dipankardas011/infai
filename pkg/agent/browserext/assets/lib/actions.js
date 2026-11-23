import { detach, evaluate, evaluateAwait, isAttached, send, withTimeout } from "./cdp.js";
import { ActionError } from "./errors.js";
import { installOverlay } from "./overlay.js";
import { formatSnapshot } from "./snapshot.js";
import {
  clearRefs,
  dropSession,
  enableLifecycle,
  getMapping,
  getRefs,
  groupTab,
  requireTab,
  setMapping,
  getShotScale,
  setRefs,
  setShotScale,
  tabInfo,
  waitForLoad,
} from "./tabs.js";

const NAVIGATE_TIMEOUT_MS = 20000;
const SNAPSHOT_MAX_CHARS = 40000;
const SNAPSHOT_MAX_PAGES = 10;
// One pageful is less than a viewport so no element is skipped between pages.
const SCROLL_PAGE_RATIO = 0.75;
const SETTLE_MS = 300;
// A scroll starts work the page did not do before: a lazy list fetches, a
// section plays its entrance. The offset is already final (SCROLL_FN scrolls
// instantly), but what the next read sees needs a moment that is longer than a
// typical entrance animation and the first paint of a lazily loaded row.
const SCROLL_SETTLE_MS = 1000;
const READ_MAX_CHARS = 20000;
const WAIT_DEFAULT_MS = 10000;
const WAIT_MAX_MS = 30000;
const WAIT_POLL_MS = 250;
const CLICK_SETTLE_MS = 500;
// Caps how long a cursor glide may delay a real input.
const GLIDE_MAX_MS = 900;
const OBJECT_GROUP = "infai";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const INNER_TEXT = "document.body ? document.body.innerText : ''";
// Instant, because a page that opts into `scroll-behavior: smooth` animates an
// ordinary scroll: reading scrollY right after reports the old offset, so the
// scroll looks like a no-op while it is still travelling.
const SCROLL_TO = `function(top) { window.scrollTo({ top, behavior: "instant" }); return window.scrollY; }`;
const SCROLL_DELTA = `Math.round(window.innerHeight * ${SCROLL_PAGE_RATIO})`;
const INSTALL_OVERLAY = `(${installOverlay.toString()})()`;
const CLEAR_OVERLAY = "window.__infaiOverlay?.clear()";
const TEARDOWN_OVERLAY = "window.__infaiOverlay?.teardown()";

// The overlay is presentation only: a page that blocks it (or a failure while
// drawing it) must never fail the action the user approved.
async function cosmetic(fn) {
  try {
    return await fn();
  } catch (err) {
    if (err?.code === "stale_ref" || err?.code === "tab_closed") throw err;
    console.debug("infai: overlay skipped", err);
    return undefined;
  }
}

async function showOverlay(tabId) {
  await evaluate(tabId, INSTALL_OVERLAY);
}

// Glides the agent cursor to (x, y), plays a press, and waits until it lands.
async function glideTo(tabId, x, y) {
  await cosmetic(async () => {
    await showOverlay(tabId);
    const ms = await evaluate(tabId, `window.__infaiOverlay.point(${Number(x)}, ${Number(y)}, true)`);
    await sleep(Math.min(Number(ms) || 0, GLIDE_MAX_MS));
  });
}

const GLIDE_TO_NODE_FN = `function() {
  const r = this.getBoundingClientRect();
  return window.__infaiOverlay.point(r.left + Math.min(r.width / 2, 16), r.top + r.height / 2, true);
}`;

async function glideToNode(tabId, backendNodeId) {
  await cosmetic(async () => {
    await showOverlay(tabId);
    const ms = await callOnNode(tabId, backendNodeId, GLIDE_TO_NODE_FN);
    await sleep(Math.min(Number(ms) || 0, GLIDE_MAX_MS));
  });
}

// The accessibility tree covers the whole document, so a long outline is split
// into pages of whole lines. Every line lands in exactly one page, so walking
// page 1..total cannot lose content. Only the refs that appear in the returned
// page stay valid.
function paginate(text, max) {
  const pages = [];
  let current = "";
  for (const line of text.split("\n")) {
    let rest = line;
    while (rest.length > max) {
      if (current) {
        pages.push(current);
        current = "";
      }
      pages.push(rest.slice(0, max));
      rest = rest.slice(max);
    }
    if (current && current.length + 1 + rest.length > max) {
      pages.push(current);
      current = "";
    }
    current = current ? `${current}\n${rest}` : rest;
  }
  if (current) pages.push(current);
  return pages.length > 0 ? pages : [""];
}

function snapshotChunk(text, refs, page, max) {
  const pages = paginate(text, max);
  const want = Math.min(Math.max(1, page), pages.length);
  let chunk = pages[want - 1];
  const tokens = new Set(chunk.split(/[^A-Za-z0-9]+/));
  for (const ref of Object.keys(refs)) if (!tokens.has(ref)) delete refs[ref];
  if (pages.length > 1) {
    const more = want < pages.length ? `; call browser_snapshot with page=${want + 1} for the rest` : "";
    chunk = `[page ${want} of ${pages.length}${more}]` + String.fromCharCode(10) + chunk;
  }
  return { text: chunk, page: want, total: pages.length };
}

function requireString(args, key) {
  const v = args[key];
  if (typeof v !== "string" || v === "") throw new ActionError("invalid_args", `${key} must be a non-empty string`);
  return v;
}

function requireEnum(args, key, allowed, fallback) {
  const v = args[key];
  if (v === undefined || v === null || v === "") return fallback;
  if (!allowed.includes(v)) throw new ActionError("invalid_args", `${key} must be one of: ${allowed.join(", ")}`);
  return v;
}

function optionalLimit(args, key, fallback, max) {
  const v = args[key];
  if (v === undefined || v === null) return fallback;
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) throw new ActionError("invalid_args", `${key} must be a positive number`);
  return Math.min(Math.floor(n), max);
}

function parseHttpUrl(raw) {
  let url;
  try {
    url = new URL(raw);
  } catch {
    throw new ActionError("invalid_args", "url must be an absolute http(s) URL");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new ActionError("invalid_args", "url must use http or https");
  }
  return url.href;
}

async function resolveRef(tabId, args) {
  const ref = requireString(args, "ref");
  const refs = await getRefs(tabId);
  const backendNodeId = refs?.[ref];
  if (backendNodeId === undefined) throw new ActionError("stale_ref", "page changed, take a new snapshot");
  return backendNodeId;
}

// Runs a node-targeted CDP command; a vanished node means the snapshot is stale.
async function nodeCall(tabId, method, params) {
  try {
    return await send(tabId, method, params);
  } catch (err) {
    if (!err.cdp) throw err;
    if (/box model|layout object/i.test(err.message)) {
      throw new ActionError("invalid_args", "element is not visible on the page");
    }
    throw new ActionError("stale_ref", "page changed, take a new snapshot");
  }
}

// Calls fn (a function-declaration string) with `this` bound to the node.
async function callOnNode(tabId, backendNodeId, fn, args = []) {
  const { object } = await nodeCall(tabId, "DOM.resolveNode", { backendNodeId, objectGroup: OBJECT_GROUP });
  try {
    const res = await send(tabId, "Runtime.callFunctionOn", {
      objectId: object.objectId,
      functionDeclaration: fn,
      arguments: args.map((value) => ({ value })),
      returnByValue: true,
    });
    if (res.exceptionDetails) {
      throw new ActionError("internal", `page script failed: ${res.exceptionDetails.exception?.description ?? res.exceptionDetails.text}`);
    }
    return res.result?.value;
  } finally {
    send(tabId, "Runtime.releaseObjectGroup", { objectGroup: OBJECT_GROUP }).catch(() => {});
  }
}

// A new session tab comes to the front only when the user is not on a page of
// their own; otherwise several sessions would keep yanking the visible tab away.
const IDLE_PAGE = /^(about:blank|chrome:\/\/newtab\/?|edge:\/\/newtab\/?|brave:\/\/newtab\/?|chrome-search:\/\/local-ntp\/.*)?$/;

async function userIsIdle() {
  const [current] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  return !current || IDLE_PAGE.test(current.url || current.pendingUrl || "");
}

async function navigate({ sessionId, sessionName, args }) {
  const url = parseHttpUrl(args.url);
  const mapping = await getMapping(sessionId);
  let tabId;
  if (!mapping) {
    const tab = await chrome.tabs.create({ url, active: await userIsIdle() });
    tabId = tab.id;
    const groupId = await groupTab(tabId, sessionName);
    await setMapping(sessionId, { tabId, groupId, sessionName });
    await enableLifecycle(tabId);
    await waitForLoad(tabId, NAVIGATE_TIMEOUT_MS);
  } else {
    ({ tabId } = await requireTab(sessionId));
    await enableLifecycle(tabId);
    const loaded = waitForLoad(tabId, NAVIGATE_TIMEOUT_MS);
    await chrome.tabs.update(tabId, { url });
    await loaded;
  }
  await clearRefs(tabId);
  return tabInfo(tabId);
}

// Reads the page outline, page by page for long documents. Reading page N also
// scrolls on to page N first, which is what triggers content that lazy-loads as
// you scroll.
async function snapshot({ sessionId, args }) {
  const { tabId } = await requireTab(sessionId);
  const page = optionalLimit(args, "page", 1, SNAPSHOT_MAX_PAGES);
  await send(tabId, "Accessibility.enable");
  for (let i = 1; i < page; i++) {
    await evaluate(tabId, `(${SCROLL_TO})(window.scrollY + ${SCROLL_DELTA})`);
    await sleep(SETTLE_MS);
  }
  const { nodes } = await send(tabId, "Accessibility.getFullAXTree");
  const { text, refs } = formatSnapshot(nodes ?? []);
  const chunk = snapshotChunk(text, refs, page, SNAPSHOT_MAX_CHARS);
  await setRefs(tabId, refs);
  return { ...(await tabInfo(tabId)), page: chunk.page, pages: chunk.total, text: chunk.text };
}

async function readPage({ sessionId, args }) {
  const maxChars = optionalLimit(args, "max_chars", READ_MAX_CHARS, READ_MAX_CHARS);
  const { tabId } = await requireTab(sessionId);
  const text = String((await evaluate(tabId, INNER_TEXT)) ?? "");
  return { ...(await tabInfo(tabId)), text: text.slice(0, maxChars) };
}

async function waitFor({ sessionId, args }) {
  const needle = requireString(args, "text");
  const timeoutMs = optionalLimit(args, "timeout_ms", WAIT_DEFAULT_MS, WAIT_MAX_MS);
  const { tabId } = await requireTab(sessionId);
  const expr = `document.body ? document.body.innerText.includes(${JSON.stringify(needle)}) : false`;
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    try {
      if ((await evaluate(tabId, expr)) === true) return { ...(await tabInfo(tabId)), found: true };
    } catch (err) {
      // The page may be mid-navigation (no execution context); keep polling.
      if (err.code !== "internal") throw err;
    }
    if (Date.now() >= deadline) return { ...(await tabInfo(tabId)), found: false };
    await sleep(WAIT_POLL_MS);
  }
}

async function click({ sessionId, args }) {
  const { tabId } = await requireTab(sessionId);
  const backendNodeId = await resolveRef(tabId, args);
  await nodeCall(tabId, "DOM.scrollIntoViewIfNeeded", { backendNodeId });
  const { model } = await nodeCall(tabId, "DOM.getBoxModel", { backendNodeId });
  const q = model.content;
  const x = (q[0] + q[2] + q[4] + q[6]) / 4;
  const y = (q[1] + q[3] + q[5] + q[7]) / 4;
  await glideTo(tabId, x, y);
  await send(tabId, "Input.dispatchMouseEvent", { type: "mouseMoved", x, y, button: "none" });
  await send(tabId, "Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await send(tabId, "Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
  await sleep(CLICK_SETTLE_MS);
  return tabInfo(tabId);
}

async function typeText({ sessionId, args }) {
  if (typeof args.text !== "string") throw new ActionError("invalid_args", "text must be a string");
  const { tabId } = await requireTab(sessionId);
  const backendNodeId = await resolveRef(tabId, args);
  await glideToNode(tabId, backendNodeId);
  await nodeCall(tabId, "DOM.focus", { backendNodeId });
  await callOnNode(tabId, backendNodeId, "function(){ if (this.select) this.select(); }");
  await send(tabId, "Input.insertText", { text: args.text });
  if (args.submit) {
    const key = { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 };
    await send(tabId, "Input.dispatchKeyEvent", { type: "keyDown", ...key, text: "\r" });
    await send(tabId, "Input.dispatchKeyEvent", { type: "keyUp", ...key });
  }
  return tabInfo(tabId);
}

const SELECT_FN = `function(value) {
  if (this.tagName !== "SELECT") return "not_select";
  const opts = Array.from(this.options);
  const match = opts.find((o) => o.value === value) ||
    opts.find((o) => o.label === value || o.text.trim() === value);
  if (!match) return "no_match";
  this.value = match.value;
  this.dispatchEvent(new Event("input", { bubbles: true }));
  this.dispatchEvent(new Event("change", { bubbles: true }));
  return "ok";
}`;

async function selectOption({ sessionId, args }) {
  if (typeof args.value !== "string") throw new ActionError("invalid_args", "value must be a string");
  const { tabId } = await requireTab(sessionId);
  const backendNodeId = await resolveRef(tabId, args);
  await glideToNode(tabId, backendNodeId);
  const outcome = await callOnNode(tabId, backendNodeId, SELECT_FN, [args.value]);
  if (outcome === "not_select") throw new ActionError("invalid_args", "element is not a <select>; use click instead");
  if (outcome === "no_match") throw new ActionError("invalid_args", `no option matches ${JSON.stringify(args.value)}`);
  return tabInfo(tabId);
}

const FOCUS_FN = "function(label) { window.__infaiOverlay.focus(this, label); }";

async function highlight({ sessionId, args }) {
  const label = typeof args.label === "string" && args.label !== "" ? args.label : "wants to act here";
  const { tabId } = await requireTab(sessionId);
  // A point approval has no element to outline: park the cursor on the pixel.
  if (!args.ref && Number.isFinite(Number(args.x)) && Number.isFinite(Number(args.y))) {
    const scale = (await getShotScale(tabId)) || 1;
    const x = Number(args.x) / scale;
    const y = Number(args.y) / scale;
    await cosmetic(async () => {
      await showOverlay(tabId);
      await evaluate(tabId, `window.__infaiOverlay.point(${x}, ${y}, false)`);
    });
    return tabInfo(tabId);
  }
  const backendNodeId = await resolveRef(tabId, args);
  await nodeCall(tabId, "DOM.scrollIntoViewIfNeeded", { backendNodeId }).catch((err) => {
    if (err.code === "stale_ref") throw err;
  });
  await showOverlay(tabId);
  await callOnNode(tabId, backendNodeId, FOCUS_FN, [label]);
  return tabInfo(tabId);
}

async function clearHighlight({ sessionId }) {
  let tabId;
  try {
    ({ tabId } = await requireTab(sessionId));
  } catch (err) {
    if (err.code === "no_tab" || err.code === "tab_closed") return {};
    throw err;
  }
  try {
    await evaluate(tabId, CLEAR_OVERLAY);
  } catch (err) {
    // Pages we cannot attach to (chrome://, Web Store) cannot hold a highlight.
    if (err.code !== "debugger") throw err;
  }
  return tabInfo(tabId);
}

async function releaseSession({ sessionId }) {
  const mapping = await getMapping(sessionId);
  if (!mapping) return {};
  const { tabId } = mapping;
  if (isAttached(tabId)) await evaluate(tabId, TEARDOWN_OVERLAY).catch(() => {});
  await detach(tabId);
  await chrome.tabs.remove(tabId).catch(() => {});
  await dropSession(sessionId);
  await clearRefs(tabId);
  return {};
}

function base64ToBytes(b64) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

function bytesToBase64(bytes) {
  let bin = "";
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
  }
  return btoa(bin);
}

// reencodeJPEG turns a captured PNG into a JPEG of the same pixels. Canvas is
// used because it is the only encoder a service worker has.
async function reencodeJPEG(pngBase64, quality) {
  const png = base64ToBytes(pngBase64);
  const bitmap = await createImageBitmap(new Blob([png], { type: "image/png" }));
  const canvas = new OffscreenCanvas(bitmap.width, bitmap.height);
  const ctx = canvas.getContext("2d");
  ctx.drawImage(bitmap, 0, 0);
  bitmap.close();
  const blob = await canvas.convertToBlob({ type: "image/jpeg", quality });
  if (!blob) throw new ActionError("internal", "the screenshot could not be encoded");
  return bytesToBase64(new Uint8Array(await blob.arrayBuffer()));
}

// Settles the page before a capture: load, webfonts, finite animations, and one
// painted frame. Infinite animations (spinners) are excluded, and every wait is
// bounded by the caller's budget, so this can never hang the action.
const SETTLE_FN = `async function(maxMs) {
  const deadline = Date.now() + maxMs;
  const bounded = (promise) =>
    Promise.race([promise, new Promise((resolve) => setTimeout(resolve, Math.max(0, deadline - Date.now())))]);
  if (document.readyState !== "complete") {
    await bounded(new Promise((resolve) => addEventListener("load", resolve, { once: true })));
  }
  if (document.fonts && document.fonts.ready) await bounded(document.fonts.ready);
  const animations = document.getAnimations ? document.getAnimations() : [];
  const finite = animations.filter((animation) => {
    const timing = animation.effect && animation.effect.getComputedTiming ? animation.effect.getComputedTiming() : null;
    return timing && timing.iterations !== Infinity;
  });
  if (finite.length) await bounded(Promise.all(finite.map((animation) => animation.finished.catch(() => {}))));
  await bounded(new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  return true;
}`;

// The visible viewport as an image: the clip is taken in page
// coordinates, and scale 1 makes one image pixel one CSS pixel, so the model's
// coordinates can be passed straight to click_at / type_at.
async function screenshot({ sessionId }) {
  const { tabId } = await requireTab(sessionId);
  // A capture taken mid-animation shows a half-rendered page; settle first,
  // but never fail the action if the page cannot run the probe.
  try {
    await evaluateAwait(tabId, `(${SETTLE_FN})(${SCREENSHOT_SETTLE_MS})`);
  } catch (err) {
    console.debug("infai: settle probe skipped", err);
  }
  const metrics = await send(tabId, "Page.getLayoutMetrics");
  const vp = metrics.cssVisualViewport || metrics.visualViewport;
  let width, height, pageX, pageY;
  if (vp && vp.clientWidth > 0 && vp.clientHeight > 0) {
    ({ clientWidth: width, clientHeight: height, pageX, pageY } = vp);
  } else {
    // A tab that is not laid out still reports innerWidth/innerHeight.
    const box = await evaluate(
      tabId,
      "({ w: window.innerWidth, h: window.innerHeight, x: window.scrollX || 0, y: window.scrollY || 0 })",
    );
    ({ w: width, h: height, x: pageX, y: pageY } = box);
  }
  width = Math.max(1, Math.round(width));
  height = Math.max(1, Math.round(height));
  if (width * height > SCREENSHOT_MAX_PIXELS) {
    throw new ActionError("invalid_args", `the viewport is too large to capture (${width}x${height})`);
  }
  const scale = Math.min(1, SCREENSHOT_MAX_WIDTH / width, SCREENSHOT_MAX_HEIGHT / height);
  const imageWidth = Math.max(1, Math.round(width * scale));
  const imageHeight = Math.max(1, Math.round(height * scale));
  // A hidden, minimized or background tab can hold the capture until a frame
  // exists. Try the browser surface first, then the renderer's compositor, then
  // a layout-forcing capture, each bounded, so the caller gets an answer.
  const clip = { x: Math.round(pageX), y: Math.round(pageY), width, height, scale };
  const attempts = [
    { format: "png", captureBeyondViewport: false, clip },
    { format: "png", captureBeyondViewport: false, clip, fromSurface: false },
    { format: "png", captureBeyondViewport: true, clip },
  ];
  let data = "";
  let lastErr;
  for (const params of attempts) {
    try {
      ({ data } = await withTimeout(
        send(tabId, "Page.captureScreenshot", params),
        SCREENSHOT_TIMEOUT_MS,
        "the tab did not produce a frame",
      ));
      break;
    } catch (err) {
      lastErr = err;
    }
  }
  if (!data) {
    throw new ActionError(
      "timeout",
      `the tab did not produce a frame (${lastErr?.message ?? "no reason given"}); ` +
        "bring it to the front (side panel: jump to tab) and retry",
    );
  }
  await setShotScale(tabId, scale);
  let mediaType = "image/png";
  let bytes = base64ToBytes(data).length;
  if (bytes > SCREENSHOT_JPEG_ABOVE_BYTES) {
    const jpeg = await reencodeJPEG(data, SCREENSHOT_JPEG_QUALITY);
    const jpegBytes = base64ToBytes(jpeg).length;
    if (jpegBytes < bytes) {
      data = jpeg;
      bytes = jpegBytes;
      mediaType = "image/jpeg";
    }
  }
  if (bytes > MAX_IMAGE_BYTES) {
    throw new ActionError(
      "reply_too_large",
      `the screenshot is ${(bytes / (1024 * 1024)).toFixed(1)} MB, over the ${MAX_IMAGE_BYTES / (1024 * 1024)} MB per-image limit; narrow the window and retry`,
    );
  }
  return {
    ...(await tabInfo(tabId)),
    data,
    media_type: mediaType,
    width: imageWidth,
    height: imageHeight,
    scale,
    scroll_y: Math.round(pageY),
  };
}

// The mouse the human sees move is the agent cursor in the overlay; the real
// input lands after it arrives, so a point action never looks like a teleport.
async function clickPoint(tabId, x, y) {
  await glideTo(tabId, x, y);
  await send(tabId, "Input.dispatchMouseEvent", { type: "mouseMoved", x, y, button: "none" });
  await send(tabId, "Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await send(tabId, "Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
}

// pointOf turns a point in the latest screenshot's pixels into CSS pixels,
// using the scale that screenshot was captured with.
async function pointOf(tabId, args) {
  const x = Number(args.x);
  const y = Number(args.y);
  if (!Number.isFinite(x) || !Number.isFinite(y) || x < 0 || y < 0) {
    throw new ActionError("invalid_args", "x and y must be non-negative numbers");
  }
  const scale = (await getShotScale(tabId)) || 1;
  return { x: Math.round(x / scale), y: Math.round(y / scale) };
}

async function clickAt({ sessionId, args }) {
  const { tabId } = await requireTab(sessionId);
  const { x, y } = await pointOf(tabId, args);
  await clickPoint(tabId, x, y);
  await sleep(CLICK_SETTLE_MS);
  return { ...(await tabInfo(tabId)), x, y };
}

async function typeAt({ sessionId, args }) {
  if (typeof args.text !== "string") throw new ActionError("invalid_args", "text must be a string");
  const { tabId } = await requireTab(sessionId);
  const { x, y } = await pointOf(tabId, args);
  await clickPoint(tabId, x, y);
  await send(tabId, "Input.insertText", { text: args.text });
  if (args.submit) {
    const key = { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 };
    await send(tabId, "Input.dispatchKeyEvent", { type: "keyDown", ...key, text: "\r" });
    await send(tabId, "Input.dispatchKeyEvent", { type: "keyUp", ...key });
  }
  await sleep(CLICK_SETTLE_MS);
  return { ...(await tabInfo(tabId)), x, y };
}

const SCROLL_TIMES_MAX = 20;
// A viewport capture beyond this is a sign of a broken layout, not a page.
const SCREENSHOT_MAX_PIXELS = 4_000_000;
const SCREENSHOT_TIMEOUT_MS = 15000;
// Long enough for a short entrance animation or a webfont reflow, short enough
// that a spinner never delays the capture for more than this.
const SCREENSHOT_SETTLE_MS = 1500;
// A capture fits inside this box so a screenshot always has a predictable cost
// and aspect ratio is preserved (nothing is cropped or distorted). The factor is
// stored so point actions map back to CSS pixels exactly.
const SCREENSHOT_MAX_WIDTH = 1280;
const SCREENSHOT_MAX_HEIGHT = 800;
// The harness accepts images up to 5 MiB (vision.MaxImageBytes) and only PNG or
// JPEG. A page screenshot above this PNG size is re-encoded as JPEG, which is
// far smaller and still lossless enough to read text.
const MAX_IMAGE_BYTES = 5 * 1024 * 1024;
const SCREENSHOT_JPEG_ABOVE_BYTES = 1_500_000;
const SCREENSHOT_JPEG_QUALITY = 0.82;

const SCROLL_FN = `function(to, times) {
  const doc = document.scrollingElement || document.documentElement;
  const vh = window.innerHeight;
  const maxY = Math.max(0, (doc.scrollHeight || 0) - vh);
  const step = Math.max(1, Math.round(vh * ${SCROLL_PAGE_RATIO}));
  const before = window.scrollY;
  const target = to === "top" ? 0
    : to === "bottom" ? maxY
    : before + (to === "up" ? -step : step) * times;
  window.scrollTo({ top: Math.max(0, Math.min(maxY, target)), behavior: "instant" });
  return { before, after: window.scrollY, max: maxY };
}`;

// For a page whose own scrolling is off (a fixed layout with the content in an
// inner box), scroll the scrollable element under the viewport centre instead.
const SCROLL_INNER_FN = `function(to, times) {
  const vh = window.innerHeight;
  const step = Math.max(1, Math.round(vh * ${SCROLL_PAGE_RATIO}));
  const stack = document.elementsFromPoint(Math.round(window.innerWidth / 2), Math.round(vh / 2));
  const box = stack.find((el) => el !== document.scrollingElement &&
    el !== document.documentElement && el !== document.body &&
    /^(auto|scroll|overlay)$/.test(getComputedStyle(el).overflowY) &&
    el.scrollHeight > el.clientHeight + 1);
  if (!box) return null;
  const before = box.scrollTop;
  const max = Math.max(0, box.scrollHeight - box.clientHeight);
  const target = to === "top" ? 0
    : to === "bottom" ? max
    : before + (to === "up" ? -step : step) * times;
  box.scrollTo({ top: Math.max(0, Math.min(max, target)), behavior: "instant" });
  return { before, after: box.scrollTop, max, container: box.tagName.toLowerCase() + (box.id ? "#" + box.id : "") };
}`;

// Reports what actually moved: a page already at its edge does not move and the
// model must know that. A window that cannot move at all, or a window that did
// not move while not at its edge, is scrolled through its inner box instead, and
// the result names which one moved.
async function scroll({ sessionId, args }) {
  const to = requireEnum(args, "to", ["down", "up", "top", "bottom"], "down");
  const times = optionalLimit(args, "times", 1, SCROLL_TIMES_MAX);
  const { tabId } = await requireTab(sessionId);
  const call = `(${JSON.stringify(to)}, ${times})`;
  let pos = await evaluate(tabId, `(${SCROLL_FN})${call}`);
  // A window with nothing to scroll (max 0) always defers to the inner box;
  // otherwise only a window that could move but did not is worth a second try.
  const edge = (to === "down" || to === "bottom") ? pos.before >= pos.max : pos.before <= 0;
  if (pos.after === pos.before && (pos.max === 0 || !edge)) {
    const inner = await evaluate(tabId, `(${SCROLL_INNER_FN})${call}`);
    if (inner) pos = inner;
  }
  await sleep(SCROLL_SETTLE_MS);
  return { ...(await tabInfo(tabId)), ...pos, container: pos.container || "" };
}

const HANDLERS = {
  navigate,
  snapshot,
  screenshot,
  scroll,
  click_at: clickAt,
  type_at: typeAt,
  read: readPage,
  wait_for: waitFor,
  click,
  type: typeText,
  select: selectOption,
  highlight,
  clear_highlight: clearHighlight,
  release_session: releaseSession,
};

export async function runAction(msg) {
  const handler = Object.hasOwn(HANDLERS, msg.action) ? HANDLERS[msg.action] : null;
  if (!handler) throw new ActionError("invalid_args", `unknown action: ${msg.action}`);
  if (typeof msg.session_id !== "string" || msg.session_id === "") {
    throw new ActionError("invalid_args", "session_id is required");
  }
  const args = msg.args && typeof msg.args === "object" ? msg.args : {};
  return handler({ sessionId: msg.session_id, sessionName: msg.session_name || msg.session_id, args });
}
