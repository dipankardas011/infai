// Side panel: renders state the service worker keeps in chrome.storage.session
// and talks to the daemon's HTTP API directly for approvals and cancels.

const $ = (id) => document.getElementById(id);

// The TUI's spinner, so a working session looks the same in both places.
const SPINNER = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"];

const config = await loadConfig();
const approvalCards = new Map();
const sessionCards = new Map();
let profileLabel = "";
let state = { conn: null, approvals: {}, sessions: {}, activity: [] };

async function loadConfig() {
  try {
    const res = await fetch(chrome.runtime.getURL("config.json"));
    return await res.json();
  } catch {
    return { daemon_url: "", token: "" };
  }
}

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  Object.assign(node, props);
  for (const child of children) if (child !== null && child !== undefined && child !== false) node.append(child);
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

// Plays the card's exit animation before taking it out of the list.
function retire(card) {
  if (card.classList.contains("leaving")) return;
  card.style.height = `${card.offsetHeight}px`;
  card.classList.add("leaving");
  const done = () => card.remove();
  card.addEventListener("animationend", done, { once: true });
  setTimeout(done, 600);
}

function clip(s, n = 200) {
  const str = String(s);
  return str.length > n ? `${str.slice(0, n - 1)}…` : str;
}

function clockTime(value) {
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function hostOf(url) {
  try {
    return new URL(url).host;
  } catch {
    return url ?? "";
  }
}

// --- header ---------------------------------------------------------------

const CONN_LABELS = {
  connected: "connected",
  connecting: "connecting",
  reconnecting: "offline",
  not_configured: "not paired",
};

function code(text) {
  return el("code", { textContent: text });
}

// What the user should do next, in words; never a bare socket close code.
function notice(connState, conn) {
  if (connState === "not_configured") {
    return {
      kind: "bad",
      title: "not paired with a daemon",
      body: ["Start ", code("infaiw server"), " — it writes this folder's config — then hit ↻ Reload on this extension in chrome://extensions."],
    };
  }
  if (connState === "reconnecting") {
    const secs = conn?.retry_at ? Math.max(0, Math.ceil((conn.retry_at - Date.now()) / 1000)) : null;
    const span = conn?.retry_at && conn?.at ? conn.retry_at - conn.at : 0;
    return {
      kind: "warn",
      title: `can't reach the daemon${secs !== null ? ` · retrying in ${secs}s` : ""}`,
      body: [
        "Either ",
        code("infaiw server"),
        ` isn't running at ${hostOf(config.daemon_url)}, or it restarted with a new pairing token — then reload this extension.`,
      ],
      progress: span > 0 ? Math.max(0, Math.min(1, (conn.retry_at - Date.now()) / span)) : null,
    };
  }
  if (connState === "connected" && !profileLabel) {
    return {
      kind: "ok",
      title: "connected · name this profile",
      body: ["Click ", code("profile"), " above so ", code("infaiw browser status"), " can tell your Chrome profiles apart."],
    };
  }
  return null;
}

function renderHeader() {
  const conn = state.conn;
  const connState = !config.token ? "not_configured" : (conn?.state ?? "unknown");
  $("conn").dataset.state = connState;
  $("conn-text").textContent = CONN_LABELS[connState] ?? "…";
  $("profile").textContent = profileLabel || "unnamed";
  $("daemon").textContent = hostOf(config.daemon_url) || "–";
  $("daemon").title = config.daemon_url || "";

  const n = notice(connState, conn);
  $("notice").hidden = !n;
  if (!n) return;
  $("notice").dataset.kind = n.kind;
  $("notice-title").textContent = n.title;
  $("notice-body").replaceChildren(...n.body);
  $("notice-bar").hidden = n.progress == null;
  if (n.progress != null) $("notice-bar").firstElementChild.style.transform = `scaleX(${n.progress})`;
}

function toggleLabelForm(open) {
  const form = $("label-form");
  form.hidden = !open;
  if (open) {
    $("label-input").value = profileLabel;
    $("label-input").focus();
    $("label-input").select();
  }
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

// Splits a browser tool call into the verb the user approves and what it acts on.
function describeAction(toolName, rawArgs) {
  const args = parseArgs(rawArgs);
  const action = String(toolName ?? "").replace(/^browser[_.]?/, "") || "act";
  if (args === null) return { verb: action, detail: clip(rawArgs ?? ""), ref: "" };
  const ref = args.ref ? String(args.ref) : "";
  switch (action) {
    case "navigate":
      return { verb: "open", detail: String(args.url ?? ""), ref: "" };
    case "click":
      return { verb: "click", detail: "", ref };
    case "type":
      return { verb: "type", detail: `${JSON.stringify(clip(args.text ?? ""))}${args.submit ? " then ⏎" : ""} into`, ref };
    case "select":
      return { verb: "choose", detail: `${JSON.stringify(clip(args.value ?? ""))} in`, ref };
    default: {
      const rest = { ...args };
      delete rest.ref;
      return { verb: action, detail: Object.keys(rest).length ? clip(JSON.stringify(rest)) : "", ref };
    }
  }
}

function buildApprovalCard(a) {
  const fn = a.tool_call?.function ?? {};
  const { verb, detail, ref } = describeAction(fn.name, fn.arguments);
  const msg = el("p", { className: "msg", hidden: true });
  const approveBtn = el("button", { className: "chip ok" }, "approve", el("span", { className: "key", textContent: "a" }));
  const denyBtn = el("button", { className: "chip bad" }, "deny", el("span", { className: "key", textContent: "d" }));
  const reasonBtn = el("button", { className: "chip warn" }, "deny with reason", el("span", { className: "key", textContent: "r" }));
  const reasonBox = el("textarea", { placeholder: "tell the agent why — ⌘/ctrl+enter sends, esc closes" });
  const sendBtn = el("button", { className: "chip warn", textContent: "send" });
  const reasonRow = el("div", { className: "reason", hidden: true }, reasonBox, sendBtn);
  const buttons = [approveBtn, denyBtn, reasonBtn, sendBtn];
  const tabLine = el("div", { className: "sub", textContent: `session ${a.session_name || a.session_id}` });

  const card = el(
    "div",
    { className: "card approval" },
    el(
      "div",
      { className: "card-head" },
      el("div", { className: "who" }, el("span", { className: "tool-chip", textContent: verb }), el("span", { className: "name", textContent: a.session_name || a.session_id })),
      el("span", { className: "time", textContent: clockTime(a.created_at) }),
    ),
    el(
      "div",
      { className: "action" },
      el("span", { className: "verb", textContent: "▸" }),
      el("span", {}, detail ? `${detail}${ref ? " " : ""}` : "", ref ? el("span", { className: "ref", textContent: `[${ref}]` }) : null, !detail && !ref ? verb : null),
    ),
    tabLine,
    el("div", { className: "buttons" }, approveBtn, denyBtn, reasonBtn),
    reasonRow,
    msg,
  );

  const mapping = state.sessions?.[a.session_id];
  if (mapping) {
    chrome.tabs
      .get(mapping.tabId)
      .then((tab) => (tabLine.textContent = `on ${tab.title || hostOf(tab.url)} — outlined on the page`))
      .catch(() => {});
  }

  const showMsg = (text, kind) => {
    msg.textContent = text;
    msg.className = `msg ${kind}`;
    msg.hidden = !text;
  };

  const decide = async (decision, reason) => {
    buttons.forEach((b) => (b.disabled = true));
    card.classList.add("deciding");
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
        showMsg("already answered elsewhere.", "info");
        setTimeout(() => removeApproval(a.id), 1500);
        return;
      }
      showMsg(await errorText(res), "error");
    } catch (err) {
      showMsg(`request failed: ${err.message}`, "error");
    }
    card.classList.remove("deciding");
    buttons.forEach((b) => (b.disabled = false));
  };

  const openReason = (open = reasonRow.hidden) => {
    reasonRow.hidden = !open;
    if (open) reasonBox.focus();
  };
  const sendReason = () => {
    const reason = reasonBox.value.trim();
    if (!reason) {
      showMsg("a reason is required.", "error");
      reasonBox.focus();
      return;
    }
    decide("deny_with_reason", reason);
  };

  approveBtn.addEventListener("click", () => decide("approve"));
  denyBtn.addEventListener("click", () => decide("deny"));
  reasonBtn.addEventListener("click", () => openReason());
  sendBtn.addEventListener("click", sendReason);
  reasonBox.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) sendReason();
    if (e.key === "Escape") openReason(false);
  });

  card.controls = {
    approve: () => !approveBtn.disabled && decide("approve"),
    deny: () => !denyBtn.disabled && decide("deny"),
    reason: () => openReason(true),
  };
  return card;
}

function sortedApprovals() {
  return Object.values(state.approvals ?? {}).sort((x, y) =>
    String(x.created_at ?? "").localeCompare(String(y.created_at ?? "")),
  );
}

// Cards are keyed by id and only added/removed, never rebuilt, so a
// half-typed deny reason survives unrelated storage updates.
function renderApprovals() {
  const list = sortedApprovals();
  const live = new Set(list.map((a) => a.id));
  for (const [id, card] of approvalCards) {
    if (!live.has(id)) {
      retire(card);
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
  $("approval-keys").hidden = list.length === 0;
  const count = $("approval-count");
  if (count.textContent !== String(list.length)) {
    count.textContent = String(list.length);
    // Forcing a reflow between clearing and restoring restarts the pop.
    count.style.animation = "none";
    void count.offsetWidth;
    count.style.animation = "";
  }
  count.hidden = list.length === 0;
}

// a / d / r answer the oldest approval, like the TUI band.
document.addEventListener("keydown", (e) => {
  if (e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return;
  const oldest = sortedApprovals()[0];
  const card = oldest && approvalCards.get(oldest.id);
  if (!card) return;
  const run = { a: card.controls.approve, d: card.controls.deny, r: card.controls.reason }[e.key];
  if (!run) return;
  e.preventDefault();
  run();
});

// --- sessions ---------------------------------------------------------------

async function jumpToTab(tabId) {
  const tab = await chrome.tabs.update(tabId, { active: true });
  await chrome.windows.update(tab.windowId, { focused: true });
}

function buildSessionCard(sessionId) {
  const name = el("span", { className: "name" });
  const favicon = el("img", { className: "favicon", alt: "" });
  const stateEl = el("span", { className: "state" });
  const tabLine = el("div", { className: "sub" });
  const last = el("div", { className: "last", hidden: true });
  const msg = el("p", { className: "msg", hidden: true });
  const showMsg = (text, kind) => {
    msg.textContent = text;
    msg.className = `msg ${kind}`;
    msg.hidden = !text;
  };

  const jumpBtn = el("button", { className: "chip", textContent: "jump to tab" });
  const cancelBtn = el("button", { className: "chip bad", textContent: "stop turn" });
  cancelBtn.addEventListener("click", async () => {
    cancelBtn.disabled = true;
    try {
      const res = await post(`/v1/sessions/${encodeURIComponent(sessionId)}/cancel`);
      showMsg(res.ok ? "stop requested." : await errorText(res), res.ok ? "info" : "error");
    } catch (err) {
      showMsg(`request failed: ${err.message}`, "error");
    }
    cancelBtn.disabled = false;
  });

  const card = el(
    "div",
    { className: "card session" },
    el("div", { className: "card-head" }, el("div", { className: "who" }, favicon, name), stateEl),
    tabLine,
    last,
    el("div", { className: "buttons" }, jumpBtn, cancelBtn),
    msg,
  );

  let tabId = null;
  jumpBtn.addEventListener("click", () =>
    jumpToTab(tabId).catch((err) => showMsg(`cannot open tab: ${err.message}`, "error")),
  );

  card.update = ({ mapping, entry, busy, waiting }) => {
    name.textContent = mapping?.sessionName || entry?.session_name || sessionId;
    card.dataset.busy = String(busy);
    stateEl.className = `state${busy ? " busy" : waiting ? " waiting" : ""}`;
    stateEl.replaceChildren(
      ...(busy
        ? [el("span", { className: "spinner", textContent: SPINNER[0] }), "working"]
        : waiting
          ? ["● needs you"]
          : ["idle"]),
    );
    if (entry) {
      last.hidden = false;
      last.replaceChildren(el("span", { className: "verb", textContent: entry.action }), ` ${entry.detail}`);
    }
    tabId = mapping?.tabId ?? null;
    jumpBtn.disabled = tabId === null;
    if (tabId === null) {
      tabLine.textContent = "no tab open";
      favicon.removeAttribute("src");
      return;
    }
    chrome.tabs
      .get(tabId)
      .then((tab) => {
        tabLine.textContent = tab.title ? `${tab.title} · ${hostOf(tab.url)}` : hostOf(tab.url) || `tab ${tab.id}`;
        if (tab.favIconUrl && favicon.src !== tab.favIconUrl) favicon.src = tab.favIconUrl;
      })
      .catch(() => (tabLine.textContent = "tab closed"));
  };
  return card;
}

function renderSessions() {
  const mappings = state.sessions ?? {};
  const activity = state.activity ?? [];
  const latest = new Map();
  const busy = new Set();
  for (const e of activity) {
    if (!latest.has(e.session_id)) latest.set(e.session_id, e);
    if (e.state === "running") busy.add(e.session_id);
  }
  const waiting = new Set(Object.values(state.approvals ?? {}).map((a) => a.session_id));
  // A session shows while it holds a tab or is mid-action.
  const ids = [...new Set([...Object.keys(mappings), ...busy])].sort();
  const live = new Set(ids);

  for (const [id, card] of sessionCards) {
    if (!live.has(id)) {
      retire(card);
      sessionCards.delete(id);
    }
  }
  for (const id of ids) {
    let card = sessionCards.get(id);
    if (!card) {
      card = buildSessionCard(id);
      sessionCards.set(id, card);
      $("sessions").append(card);
    }
    card.update({ mapping: mappings[id], entry: latest.get(id), busy: busy.has(id), waiting: waiting.has(id) });
  }
  $("sessions-empty").hidden = ids.length > 0;
}

// --- activity ---------------------------------------------------------------

const activityRows = new Map();

function fillActivityRow(li, e) {
  li.dataset.state = e.state;
  const took = e.state === "running" ? el("span", { className: "spinner", textContent: SPINNER[0] }) : e.ms != null ? `${e.ms}ms` : "";
  li.replaceChildren(
    el("span", { className: "t", textContent: clockTime(e.at) }),
    el(
      "span",
      { className: "what", title: `${e.session_name} · ${e.action} ${e.detail}` },
      el("span", { className: "verb", textContent: e.action }),
      el("span", { className: "detail", textContent: e.detail ? ` ${e.detail}` : "" }),
    ),
    el("span", { className: "took" }, took),
    ...(e.error ? [el("span", { className: "err", textContent: clip(e.error, 160) })] : []),
  );
}

function renderActivity() {
  const list = (state.activity ?? []).slice(0, 25);
  const live = new Set(list.map((e) => e.id));
  for (const [id, li] of activityRows) {
    if (!live.has(id)) {
      li.remove();
      activityRows.delete(id);
    }
  }
  // Newest first: walk backwards so each new row is prepended in order.
  for (let i = list.length - 1; i >= 0; i--) {
    const e = list[i];
    let li = activityRows.get(e.id);
    if (!li) {
      li = el("li");
      activityRows.set(e.id, li);
      $("activity").prepend(li);
    }
    if (li.dataset.state !== e.state) fillActivityRow(li, e);
  }
  $("activity-empty").hidden = list.length > 0;
}

// --- wiring -----------------------------------------------------------------

const RENDERERS = {
  conn: [renderHeader],
  approvals: [renderApprovals, renderSessions],
  sessions: [renderSessions],
  activity: [renderActivity, renderSessions],
};

$("profile-btn").addEventListener("click", () => toggleLabelForm($("label-form").hidden));
$("label-input").addEventListener("keydown", (e) => {
  if (e.key === "Escape") toggleLabelForm(false);
});
$("label-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  const value = $("label-input").value.trim().slice(0, 40);
  try {
    await chrome.runtime.sendMessage({ type: "set_label", label: value });
    profileLabel = value;
    toggleLabelForm(false);
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
    state[key] = change.newValue ?? (key === "conn" ? null : key === "activity" ? [] : {});
    RENDERERS[key].forEach((fn) => toRun.add(fn));
  }
  toRun.forEach((fn) => fn());
});

const stored = await chrome.storage.session.get(Object.keys(RENDERERS));
state = { ...state, ...stored };
profileLabel = String((await chrome.storage.local.get("profile_label")).profile_label ?? "");
renderHeader();
renderApprovals();
renderSessions();
renderActivity();

// Keeps the retry countdown current.
setInterval(renderHeader, 1000);

let frame = 0;
setInterval(() => {
  frame = (frame + 1) % SPINNER.length;
  for (const s of document.querySelectorAll(".spinner")) s.textContent = SPINNER[frame];
}, 80);

// Wakes a suspended service worker so it reconnects.
chrome.runtime.sendMessage({ type: "connect" }).catch(() => {});
