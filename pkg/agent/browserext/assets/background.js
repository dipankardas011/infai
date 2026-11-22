import * as activity from "./lib/activity.js";
import { forgetTab, withTimeout } from "./lib/cdp.js";
import { browserName } from "./lib/client.js";
import { ActionError } from "./lib/errors.js";
import { runAction } from "./lib/actions.js";
import { mutate, write } from "./lib/store.js";
import { clearRefs } from "./lib/tabs.js";

const EXTENSION_VERSION = "0.1.0";
const PING_INTERVAL_MS = 20000;
const BACKOFF_START_MS = 1000;
const BACKOFF_MAX_MS = 30000;
const ACTION_TIMEOUT_MS = 40000;
// Keep replies under the daemon's frame limit, and say so when they are not,
// rather than letting the socket cut the conversation.
// Mirrors the daemon's frame cap (vision.MaxTotalBytes), so an oversized reply
// is refused here with a reason instead of severing the socket.
const MAX_REPLY_BYTES = 16 * 1024 * 1024;
// Screenshots are uploaded over HTTP and the socket reply carries only their
// id, so no reply frame ever holds megabytes.

let config = null;
let label = "";
let token = "";
let instance = "";
let ws = null;
let connecting = false;
let backoffMs = BACKOFF_START_MS;
let pingTimer = null;
let reconnectTimer = null;
let stayAwakeTimer = null;
let busyTimer = null;
let busy = 0;

async function loadConfig() {
  if (!config) {
    const res = await fetch(chrome.runtime.getURL("config.json"));
    config = await res.json();
  }
  return config;
}

function setConn(state, extra = {}) {
  return write("conn", {
    state,
    daemon_url: config?.daemon_url ?? "",
    profile_label: label || config?.profile_label || "",
    at: Date.now(),
    ...extra,
  });
}

// The profile label lives in extension storage, not config.json: only the user
// knows which Chrome profile the extension was loaded into, and the side panel
// lets them set it.
async function loadLabel(cfg) {
  const stored = await chrome.storage.local.get("profile_label");
  return String(stored.profile_label ?? "").trim() || String(cfg.profile_label ?? "").trim();
}

// A stable id per browser profile (storage.local is per profile) makes each
// profile its own client on the daemon, and lets a reconnect replace its own
// stale socket.
async function loadInstance() {
  const stored = await chrome.storage.local.get("instance_id");
  if (stored.instance_id) return stored.instance_id;
  const id = crypto.randomUUID();
  await chrome.storage.local.set({ instance_id: id });
  return id;
}

function base64Bytes(b64) {
  const bin = atob(b64);
  const bytes = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
  return bytes;
}

// uploadImage posts the captured bytes to the daemon and returns the id the
// tool result will reference. The daemon validates it like any attachment.
async function uploadImage(result) {
  const base = (config?.daemon_url ?? "").replace(/\/+$/, "");
  const res = await fetch(`${base}/v1/browser/screenshot`, {
    method: "POST",
    headers: { "Content-Type": "application/octet-stream", "X-Infai-Browser-Token": token },
    body: base64Bytes(result.data),
  });
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  const { id } = await res.json();
  const { data, ...rest } = result;
  return { ...rest, image_id: id };
}

function hello() {
  return {
    type: "hello",
    token,
    profile: label,
    instance,
    browser: browserName(),
    extension_version: EXTENSION_VERSION,
  };
}

// The token is not a query parameter: proxy access logs would capture it.
// It travels in the hello frame, which the daemon requires before anything else.
function wsUrl(cfg) {
  const base = cfg.daemon_url.replace(/\/+$/, "").replace(/^http(s?):/i, "ws$1:");
  return `${base}/v1/browser/connect`;
}

function sendMessage(msg) {
  if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(msg));
}

async function connect() {
  if (connecting || (ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN))) return;
  connecting = true;
  clearTimeout(reconnectTimer);
  reconnectTimer = null;
  try {
    let cfg;
    try {
      cfg = await loadConfig();
    } catch (err) {
      await setConn("not_configured", { error: `cannot read config.json: ${err.message}` });
      return;
    }
    if (!cfg.token || !cfg.daemon_url) {
      await setConn("not_configured");
      return;
    }
    label = await loadLabel(cfg);
    instance = await loadInstance();
    token = cfg.token ?? "";
    await setConn("connecting");
    let socket;
    try {
      socket = new WebSocket(wsUrl(cfg));
    } catch (err) {
      scheduleReconnect(err.message);
      return;
    }
    ws = socket;
    socket.onopen = () => {
      backoffMs = BACKOFF_START_MS;
      stopStayAwake();
      sendMessage(hello());
      // Approvals cached from a previous connection may belong to a daemon that
      // has since restarted; they would sit in the panel forever otherwise.
      chrome.storage.session.set({ approvals: {} }).catch(() => {});
      pingTimer = setInterval(() => sendMessage({ type: "ping" }), PING_INTERVAL_MS);
      setConn("connected");
    };
    socket.onmessage = (ev) => onDaemonMessage(ev.data);
    // onerror is always followed by onclose, so reconnect only from onclose.
    socket.onclose = (ev) => {
      if (ws !== socket) return;
      ws = null;
      clearInterval(pingTimer);
      pingTimer = null;
      scheduleReconnect(ev.reason || `closed (code ${ev.code})`);
    };
  } finally {
    connecting = false;
  }
}

function scheduleReconnect(reason) {
  if (reconnectTimer) return;
  const delay = backoffMs;
  backoffMs = Math.min(backoffMs * 2, BACKOFF_MAX_MS);
  setConn("reconnecting", { error: reason, retry_at: Date.now() + delay });
  startStayAwake();
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    connect();
  }, delay);
}

// Without an open WebSocket the worker can be suspended after ~30s idle,
// silently dropping the backoff timer. Any extension API call resets the idle
// timer (Chrome 110+), which avoids needing the alarms permission.
function startStayAwake() {
  if (stayAwakeTimer) return;
  stayAwakeTimer = setInterval(() => chrome.runtime.getPlatformInfo(), 20000);
}

function stopStayAwake() {
  clearInterval(stayAwakeTimer);
  stayAwakeTimer = null;
}

function onDaemonMessage(raw) {
  let msg;
  try {
    msg = JSON.parse(raw);
  } catch {
    console.warn("infai: ignoring non-JSON message");
    return;
  }
  switch (msg?.type) {
    case "request":
      if (msg.id !== undefined) enqueueRequest(msg);
      break;
    case "event":
      handleEvent(msg.event, msg.data ?? {}).catch((err) => console.error("infai: event handling failed", err));
      break;
    case "pong":
      break;
    case "client_id":
      // The daemon's id is authoritative: it adds a suffix when another profile
      // shares this label, which the panel cannot know on its own.
      chrome.storage.session.set({ client_id: String(msg.id ?? "") }).catch(() => {});
      break;
    default:
      console.warn("infai: unknown message type", msg?.type);
  }
}

// Actions for one session run one at a time so e.g. a click never races the
// snapshot that produced its ref.
const sessionQueues = new Map();

function enqueueRequest(msg) {
  const key = String(msg.session_id ?? "");
  const prev = sessionQueues.get(key) ?? Promise.resolve();
  const run = prev.then(() => executeRequest(msg), () => executeRequest(msg));
  sessionQueues.set(key, run);
  run
    .catch((err) => console.error("infai: action queue failed", err))
    .finally(() => {
      if (sessionQueues.get(key) === run) sessionQueues.delete(key);
    });
}

// MV3 terminates an idle service worker after ~30s. A slow action (a capture
// waiting on a frame, a page that never finishes loading) would be killed
// mid-flight and never reply, which the daemon can only report as a timeout.
// One cheap API call every 20s keeps the worker alive until the action answers.
function holdWorker() {
  busy++;
  chrome.runtime.getPlatformInfo().catch(() => {});
  if (busy === 1) busyTimer = setInterval(() => chrome.runtime.getPlatformInfo().catch(() => {}), 20000);
}

function releaseWorker() {
  busy = Math.max(0, busy - 1);
  if (busy === 0) {
    clearInterval(busyTimer);
    busyTimer = null;
  }
}

async function executeRequest(msg) {
  let reply;
  const logId = activity.started(msg);
  holdWorker();
  try {
    // Last-resort bound: the daemon gives up at 45s, so an action must answer
    // before then even if one of its steps hangs.
    let result = await withTimeout(runAction(msg), ACTION_TIMEOUT_MS, `the action did not finish within ${ACTION_TIMEOUT_MS / 1000}s`);
    if (result && typeof result.data === "string" && result.data !== "") {
      try {
        result = await uploadImage(result);
      } catch (err) {
        throw new ActionError("upload_failed", `the screenshot could not be uploaded to the daemon: ${err?.message ?? err}`);
      }
    }
    reply = { type: "response", id: msg.id, ok: true, result: result ?? {} };
    activity.finished(logId);
  } catch (err) {
    const code = err instanceof ActionError ? err.code : "internal";
    if (code === "internal") console.error("infai: action failed", msg.action, err);
    reply = { type: "response", id: msg.id, ok: false, error: { code, message: String(err?.message ?? err) } };
    activity.finished(logId, err?.message ?? err);
  } finally {
    releaseWorker();
  }
  let frame;
  try {
    frame = JSON.stringify(reply);
    console.debug("infai: reply", msg.action, frame.length, "bytes");
  } catch (err) {
    frame = JSON.stringify({
      type: "response",
      id: msg.id,
      ok: false,
      error: { code: "internal", message: `the reply could not be serialized: ${err?.message ?? err}` },
    });
  }
  if (frame.length > MAX_REPLY_BYTES) {
    const mb = (frame.length / (1024 * 1024)).toFixed(1);
    frame = JSON.stringify({
      type: "response",
      id: msg.id,
      ok: false,
      error: {
        code: "reply_too_large",
        message: `the reply was ${mb} MB, over the ${MAX_REPLY_BYTES / (1024 * 1024)} MB frame limit; retry with a smaller request`,
      },
    });
  }
  if (!ws || ws.readyState !== WebSocket.OPEN) {
    console.error("infai: could not send response: the daemon socket is closed");
    return;
  }
  ws.send(frame);
}

async function handleEvent(event, data) {
  switch (event) {
    case "approval_requested":
      if (!data.id) return;
      await mutate("approvals", (all) => ({ ...all, [data.id]: data }));
      break;
    case "approval_resolved":
      await mutate("approvals", (all) => {
        if (!(data.req_id in all)) return undefined;
        delete all[data.req_id];
        return all;
      });
      break;
    default:
      console.info("infai: unhandled event", event);
  }
}

// The toolbar icon carries the pending-approval count, so a waiting agent is
// visible even with the side panel closed.
async function updateBadge() {
  const { approvals = {} } = await chrome.storage.session.get("approvals");
  const n = Object.keys(approvals).length;
  await chrome.action.setBadgeText({ text: n ? String(n) : "" });
}

chrome.action.setBadgeBackgroundColor({ color: "#dbbc7f" }).catch(() => {});
chrome.action.setBadgeTextColor?.({ color: "#0d1310" }).catch(() => {});

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === "session" && "approvals" in changes) updateBadge().catch(() => {});
});

// The mapping stays until the session acts on it: requireTab then reports
// tab_closed (the user closed it) instead of no_tab (never navigated).
chrome.tabs.onRemoved.addListener((tabId) => {
  forgetTab(tabId);
  clearRefs(tabId);
});

chrome.tabs.onUpdated.addListener((tabId, info) => {
  if (info.url || info.status === "loading") clearRefs(tabId);
});

chrome.debugger.onDetach.addListener((source) => {
  if (source.tabId !== undefined) forgetTab(source.tabId);
});

chrome.runtime.onStartup.addListener(() => connect());
chrome.runtime.onInstalled.addListener(() => connect());

// The side panel pings on open so a suspended worker wakes and reconnects.
chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  if (msg?.type === "connect") {
    connect();
    return;
  }
  if (msg?.type === "set_label") {
    label = String(msg.label ?? "").trim().slice(0, 40);
    chrome.storage.local.set({ profile_label: label }).then(() => {
      // The daemon learns the label from hello, so re-send it when connected.
      if (ws && ws.readyState === WebSocket.OPEN) {
        sendMessage(hello());
      }
      sendResponse?.({ ok: true, label });
    });
    return true;
  }
});

chrome.sidePanel.setPanelBehavior({ openPanelOnActionClick: true }).catch((err) => {
  console.warn("infai: setPanelBehavior failed", err);
});

connect();
