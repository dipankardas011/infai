import { forgetTab } from "./lib/cdp.js";
import { ActionError } from "./lib/errors.js";
import { runAction } from "./lib/actions.js";
import { mutate, write } from "./lib/store.js";
import { clearRefs } from "./lib/tabs.js";

const EXTENSION_VERSION = "0.1.0";
const PING_INTERVAL_MS = 20000;
const BACKOFF_START_MS = 1000;
const BACKOFF_MAX_MS = 30000;

let config = null;
let label = "";
let token = "";
let ws = null;
let connecting = false;
let backoffMs = BACKOFF_START_MS;
let pingTimer = null;
let reconnectTimer = null;
let stayAwakeTimer = null;

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
      sendMessage({ type: "hello", token, profile: label, extension_version: EXTENSION_VERSION });
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
  const run = prev.then(() => executeRequest(msg));
  sessionQueues.set(key, run);
  run.then(() => {
    if (sessionQueues.get(key) === run) sessionQueues.delete(key);
  });
}

async function executeRequest(msg) {
  let reply;
  try {
    const result = await runAction(msg);
    reply = { type: "response", id: msg.id, ok: true, result: result ?? {} };
  } catch (err) {
    const code = err instanceof ActionError ? err.code : "internal";
    if (code === "internal") console.error("infai: action failed", msg.action, err);
    reply = { type: "response", id: msg.id, ok: false, error: { code, message: String(err?.message ?? err) } };
  }
  try {
    sendMessage(reply);
  } catch (err) {
    console.error("infai: could not send response", err);
  }
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
    case "session_status":
      if (!data.session_id) return;
      await mutate("session_status", (all) => ({
        ...all,
        [data.session_id]: { session_name: data.session_name ?? "", status: data.status ?? "", at: Date.now() },
      }));
      break;
    default:
      console.info("infai: unhandled event", event);
  }
}

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
        sendMessage({ type: "hello", token, profile: label, extension_version: EXTENSION_VERSION });
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
