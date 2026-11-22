import { detach, evaluate, isAttached, send } from "./cdp.js";
import { ActionError } from "./errors.js";
import { installOverlay } from "./overlay.js";
import { formatSnapshot } from "./snapshot.js";
import {
  clearRefs,
  dropSession,
  getMapping,
  getRefs,
  groupTab,
  requireTab,
  setMapping,
  setRefs,
  tabInfo,
  waitForLoad,
} from "./tabs.js";

const NAVIGATE_TIMEOUT_MS = 20000;
const SNAPSHOT_MAX_CHARS = 40000;
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

function capText(text, max) {
  return text.length > max ? `${text.slice(0, max)}\n[truncated]` : text;
}

function requireString(args, key) {
  const v = args[key];
  if (typeof v !== "string" || v === "") throw new ActionError("invalid_args", `${key} must be a non-empty string`);
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

async function navigate({ sessionId, sessionName, args }) {
  const url = parseHttpUrl(args.url);
  const mapping = await getMapping(sessionId);
  let tabId;
  if (!mapping) {
    const tab = await chrome.tabs.create({ url, active: true });
    tabId = tab.id;
    const groupId = await groupTab(tabId, sessionName);
    await setMapping(sessionId, { tabId, groupId, sessionName });
    await waitForLoad(tabId, NAVIGATE_TIMEOUT_MS);
  } else {
    ({ tabId } = await requireTab(sessionId));
    const loaded = waitForLoad(tabId, NAVIGATE_TIMEOUT_MS);
    await chrome.tabs.update(tabId, { url });
    await loaded;
  }
  await clearRefs(tabId);
  return tabInfo(tabId);
}

async function snapshot({ sessionId }) {
  const { tabId } = await requireTab(sessionId);
  await send(tabId, "Accessibility.enable");
  const { nodes } = await send(tabId, "Accessibility.getFullAXTree");
  const { text, refs } = formatSnapshot(nodes ?? []);
  await setRefs(tabId, refs);
  return { ...(await tabInfo(tabId)), text: capText(text, SNAPSHOT_MAX_CHARS) };
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

const HANDLERS = {
  navigate,
  snapshot,
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
