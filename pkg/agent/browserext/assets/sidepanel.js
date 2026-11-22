// Side panel: renders state the service worker keeps in chrome.storage.session
// and talks to the daemon's HTTP API directly for approvals and cancels.

const $ = (id) => document.getElementById(id);

const config = await loadConfig();
const approvalCards = new Map();
let profileLabel = "";
let state = { conn: null, approvals: {}, session_status: {}, sessions: {} };

async function loadConfig() {
  try {
    const res = await fetch(chrome.runtime.getURL("config.json"));
    return await res.json();
  } catch {
    return { daemon_url: "", token: "", profile_label: "" };
  }
}

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  Object.assign(node, props);
  for (const child of children) if (child !== null && child !== undefined) node.append(child);
  return node;
}

async function post(path, body) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const base = config.daemon_url.replace(/\/+$/, "");
  return fetch(`${base}${path}`, {
    method: "POST",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

async function errorText(res) {
  const raw = await res.text().catch(() => "");
  try {
    const parsed = JSON.parse(raw);
    const msg = parsed?.error?.message ?? parsed?.error ?? parsed?.message;
    if (typeof msg === "string" && msg) return `HTTP ${res.status}: ${msg}`;
  } catch {
    // Not JSON; fall through to the raw body.
  }
  return `HTTP ${res.status}${raw ? `: ${raw.slice(0, 200)}` : ""}`;
}

async function removeApproval(id) {
  const { approvals = {} } = await chrome.storage.session.get("approvals");
  if (!(id in approvals)) return;
  delete approvals[id];
  await chrome.storage.session.set({ approvals });
}

// --- header ---------------------------------------------------------------

const CONN_LABELS = {
  connected: "connected",
  connecting: "connecting",
  reconnecting: "reconnecting",
  not_configured: "not configured",
};

function renderHeader() {
  const conn = state.conn;
  const connState = !config.token ? "not_configured" : (conn?.state ?? "unknown");
  $("conn").dataset.state = connState;
  $("conn-text").textContent = CONN_LABELS[connState] ?? "unknown";
  $("profile").textContent = profileLabel || "–";
  $("daemon").textContent = config.daemon_url || "–";
  const labelInput = $("label-input");
  if (labelInput && document.activeElement !== labelInput) labelInput.value = profileLabel;

  let detail = "";
  if (!config.token) {
    detail =
      "Not paired yet: start `infaiw server` — it writes this folder's config.json — then click Reload on this extension at chrome://extensions.";
  } else if (connState === "reconnecting" && conn?.error) {
    const secs = conn.retry_at ? Math.max(0, Math.round((conn.retry_at - Date.now()) / 1000)) : null;
    detail = `${conn.error}${secs !== null ? ` – retrying in ${secs}s` : ""}`;
  } else if (connState === "not_configured" && conn?.error) detail = conn.error;
  else if (connState === "connected" && !profileLabel) {
    detail = "Connected. Set a profile label below so `infaiw browser status` can name this Chrome profile.";
  }
  $("conn-detail").textContent = detail;
  $("conn-detail").hidden = !detail;
}

// --- approvals --------------------------------------------------------------

function parseArgs(raw) {
  if (raw && typeof raw === "object") return raw;
  try {
    const parsed = JSON.parse(raw ?? "{}");
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return null;
  }
}

function clip(s, n = 200) {
  const str = String(s);
  return str.length > n ? `${str.slice(0, n - 1)}…` : str;
}

// Tool names may be a single "browser" tool with an action arg, or per-action
// tools like "browser_click"; handle both.
function describeAction(toolName, rawArgs) {
  const args = parseArgs(rawArgs);
  if (args === null) return clip(rawArgs ?? "");
  const action = args.action ?? String(toolName ?? "").replace(/^browser[_.]?/, "");
  const ref = args.ref ? ` ${args.ref}` : "";
  switch (action) {
    case "navigate":
      return `navigate ${args.url ?? ""}`;
    case "click":
      return `click${ref}`;
    case "type":
      return `type ${JSON.stringify(clip(args.text ?? ""))} into${ref}${args.submit ? " and submit" : ""}`;
    case "select":
      return `select ${JSON.stringify(clip(args.value ?? ""))} in${ref}`;
    case "highlight":
      return `highlight${ref}${args.label ? ` (${args.label})` : ""}`;
    default: {
      const rest = { ...args };
      delete rest.action;
      const body = Object.keys(rest).length ? ` ${clip(JSON.stringify(rest))}` : "";
      return `${action || toolName}${body}${args.ref && !body.includes(args.ref) ? ` (ref ${args.ref})` : ""}`;
    }
  }
}

function formatTime(value) {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString();
}

function buildApprovalCard(a) {
  const fn = a.tool_call?.function ?? {};
  const msg = el("p", { className: "msg", hidden: true });
  const approveBtn = el("button", { className: "primary", textContent: "Approve" });
  const denyBtn = el("button", { className: "danger", textContent: "Deny" });
  const reasonBtn = el("button", { textContent: "Deny with reason" });
  const reasonBox = el("textarea", { placeholder: "Tell the agent why (required)", hidden: true });
  const sendBtn = el("button", { className: "danger", textContent: "Send", hidden: true });
  const buttons = [approveBtn, denyBtn, reasonBtn, sendBtn];

  const showMsg = (text, kind) => {
    msg.textContent = text;
    msg.className = `msg ${kind}`;
    msg.hidden = !text;
  };

  const decide = async (decision, reason) => {
    buttons.forEach((b) => (b.disabled = true));
    showMsg("", "info");
    const body = { req_id: a.id, fingerprint: a.fingerprint, decision };
    if (decision === "deny_with_reason") body.reason = reason;
    try {
      const res = await post(
        `/v1/sessions/${encodeURIComponent(a.session_id)}/approvals/${encodeURIComponent(a.id)}`,
        body,
      );
      if (res.ok) {
        await removeApproval(a.id);
        return;
      }
      if (res.status === 409) {
        showMsg("Already resolved elsewhere.", "info");
        setTimeout(() => removeApproval(a.id), 1500);
        return;
      }
      showMsg(await errorText(res), "error");
    } catch (err) {
      showMsg(`Request failed: ${err.message}`, "error");
    }
    buttons.forEach((b) => (b.disabled = false));
  };

  approveBtn.addEventListener("click", () => decide("approve"));
  denyBtn.addEventListener("click", () => decide("deny"));
  reasonBtn.addEventListener("click", () => {
    reasonBox.hidden = !reasonBox.hidden;
    sendBtn.hidden = reasonBox.hidden;
    if (!reasonBox.hidden) reasonBox.focus();
  });
  sendBtn.addEventListener("click", () => {
    const reason = reasonBox.value.trim();
    if (!reason) {
      showMsg("A reason is required.", "error");
      reasonBox.focus();
      return;
    }
    decide("deny_with_reason", reason);
  });

  const created = formatTime(a.created_at);
  return el(
    "div",
    { className: "card" },
    el(
      "div",
      { className: "card-head" },
      el("span", { className: "tool", textContent: fn.name ?? "tool" }),
      el("span", { className: "sub", textContent: created }),
    ),
    el("div", { className: "sub", textContent: `session: ${a.session_name || a.session_id}` }),
    el("div", { className: "action", textContent: describeAction(fn.name, fn.arguments) }),
    el("div", { className: "buttons" }, approveBtn, denyBtn, reasonBtn),
    reasonBox,
    el("div", { className: "buttons" }, sendBtn),
    msg,
  );
}

// Cards are keyed by id and only added/removed, never rebuilt, so a
// half-typed deny reason survives unrelated storage updates.
function renderApprovals() {
  const list = Object.values(state.approvals ?? {}).sort((x, y) =>
    String(x.created_at ?? "").localeCompare(String(y.created_at ?? "")),
  );
  const live = new Set(list.map((a) => a.id));
  for (const [id, card] of approvalCards) {
    if (!live.has(id)) {
      card.remove();
      approvalCards.delete(id);
    }
  }
  for (const a of list) {
    if (approvalCards.has(a.id)) continue;
    const card = buildApprovalCard(a);
    approvalCards.set(a.id, card);
    $("approvals").append(card);
  }
  $("approvals-empty").hidden = list.length > 0;
  $("approval-count").hidden = list.length === 0;
  $("approval-count").textContent = String(list.length);
}

// --- sessions ---------------------------------------------------------------

async function jumpToTab(tabId) {
  const tab = await chrome.tabs.update(tabId, { active: true });
  await chrome.windows.update(tab.windowId, { focused: true });
}

function buildSessionCard(sessionId, mapping, status) {
  const msg = el("p", { className: "msg", hidden: true });
  const showMsg = (text, kind) => {
    msg.textContent = text;
    msg.className = `msg ${kind}`;
    msg.hidden = !text;
  };
  const tabLine = el("div", { className: "sub", textContent: mapping ? `tab ${mapping.tabId}` : "no tab" });

  const jumpBtn = el("button", { textContent: "Jump to tab", disabled: !mapping });
  jumpBtn.addEventListener("click", () =>
    jumpToTab(mapping.tabId).catch((err) => showMsg(`Cannot open tab: ${err.message}`, "error")),
  );

  const cancelBtn = el("button", { className: "danger", textContent: "Cancel turn" });
  cancelBtn.addEventListener("click", async () => {
    cancelBtn.disabled = true;
    try {
      const res = await post(`/v1/sessions/${encodeURIComponent(sessionId)}/cancel`);
      showMsg(res.ok ? "Cancel requested." : await errorText(res), res.ok ? "info" : "error");
    } catch (err) {
      showMsg(`Request failed: ${err.message}`, "error");
    }
    cancelBtn.disabled = false;
  });

  if (mapping) {
    chrome.tabs
      .get(mapping.tabId)
      .then((tab) => (tabLine.textContent = tab.title || tab.url || `tab ${tab.id}`))
      .catch(() => {});
  }

  const name = status?.session_name || mapping?.sessionName || sessionId;
  return el(
    "div",
    { className: "card" },
    el(
      "div",
      { className: "card-head" },
      el("span", { className: "tool", textContent: name }),
      status?.status ? el("span", { className: "status", textContent: status.status }) : null,
    ),
    tabLine,
    el("div", { className: "buttons" }, jumpBtn, cancelBtn),
    msg,
  );
}

function renderSessions() {
  const sessions = state.sessions ?? {};
  const statuses = state.session_status ?? {};
  const ids = [...new Set([...Object.keys(sessions), ...Object.keys(statuses)])].sort();
  $("sessions").replaceChildren(...ids.map((id) => buildSessionCard(id, sessions[id], statuses[id])));
  $("sessions-empty").hidden = ids.length > 0;
}

// --- wiring -----------------------------------------------------------------

const RENDERERS = {
  conn: renderHeader,
  approvals: renderApprovals,
  session_status: renderSessions,
  sessions: renderSessions,
};

$("label-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const value = $("label-input").value.trim().slice(0, 40);
  try {
    await chrome.runtime.sendMessage({ type: "set_label", label: value });
    profileLabel = value;
  } catch (err) {
    console.error("infai: could not save the profile label", err);
  }
  renderHeader();
});

chrome.storage.onChanged.addListener((changes, area) => {
  if (area === "local" && "profile_label" in changes) {
    profileLabel = String(changes.profile_label.newValue ?? "");
    renderHeader();
    return;
  }
  if (area !== "session") return;
  const toRun = new Set();
  for (const [key, change] of Object.entries(changes)) {
    if (!(key in RENDERERS)) continue;
    state[key] = change.newValue ?? (key === "conn" ? null : {});
    toRun.add(RENDERERS[key]);
  }
  toRun.forEach((fn) => fn());
});

const stored = await chrome.storage.session.get(Object.keys(RENDERERS));
state = { ...state, ...stored };
profileLabel = String((await chrome.storage.local.get("profile_label")).profile_label ?? "");
renderHeader();
renderApprovals();
renderSessions();
// Keeps the "retrying in Ns" countdown current.
setInterval(renderHeader, 1000);

// Wakes a suspended service worker so it reconnects.
chrome.runtime.sendMessage({ type: "connect" }).catch(() => {});
