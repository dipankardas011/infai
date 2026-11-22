import { send } from "./cdp.js";
import { ActionError } from "./errors.js";
import { mutate, read } from "./store.js";

// session_id -> {tabId, groupId, sessionName}
const SESSIONS = "sessions";
// tabId -> {ref: backendDOMNodeId}
const REFS = "refs";

export function getMapping(sessionId) {
  return read(SESSIONS).then((all) => all[sessionId] ?? null);
}

export function setMapping(sessionId, mapping) {
  return mutate(SESSIONS, (all) => ({ ...all, [sessionId]: mapping }));
}

export function dropSession(sessionId) {
  return mutate(SESSIONS, (all) => {
    if (!(sessionId in all)) return undefined;
    delete all[sessionId];
    return all;
  });
}

// Returns the session's live mapping, or throws no_tab / tab_closed.
export async function requireTab(sessionId) {
  const mapping = await getMapping(sessionId);
  if (!mapping) throw new ActionError("no_tab", "session has no tab yet; navigate first");
  try {
    await chrome.tabs.get(mapping.tabId);
  } catch {
    await dropSession(sessionId);
    await clearRefs(mapping.tabId);
    throw new ActionError("tab_closed", "the session's tab was closed");
  }
  return mapping;
}

export async function tabInfo(tabId) {
  try {
    const tab = await chrome.tabs.get(tabId);
    return { tab_id: tab.id, url: tab.url ?? tab.pendingUrl ?? "", title: tab.title ?? "" };
  } catch {
    throw new ActionError("tab_closed", "the session's tab was closed");
  }
}

// Grouping is cosmetic. Tab groups only exist in normal windows, so a popup or
// app window is skipped silently instead of reporting a Chrome error.
export async function groupTab(tabId, sessionName) {
  try {
    const tab = await chrome.tabs.get(tabId);
    const window = await chrome.windows.get(tab.windowId);
    if (window.type && window.type !== "normal") return null;
    const groupId = await chrome.tabs.group({ tabIds: [tabId] });
    await chrome.tabGroups.update(groupId, { title: `▸ infaiw · ${sessionName}`, color: "green" });
    return groupId;
  } catch (err) {
    console.debug("infai: could not group tab", err);
    return null;
  }
}

// The scale the latest screenshot was captured with: a point given in image
// pixels is divided by it to get the CSS pixels the input events need.
const SHOTS = "shots";

export async function getShotScale(tabId) {
  const all = await read(SHOTS);
  return all[tabId] ?? 1;
}

export function setShotScale(tabId, scale) {
  return mutate(SHOTS, (all) => ({ ...all, [tabId]: scale }));
}

export async function getRefs(tabId) {
  const all = await read(REFS);
  return all[tabId] ?? null;
}

export function setRefs(tabId, refs) {
  return mutate(REFS, (all) => ({ ...all, [tabId]: refs }));
}

export function clearRefs(tabId) {
  return mutate(REFS, (all) => {
    if (!(tabId in all)) return undefined;
    delete all[tabId];
    return all;
  });
}

// Chrome's own lifecycle events are the accurate "load finished" signal:
// networkAlmostIdle means the page stopped fetching. Enabling the domain needs
// the debugger, so it is best effort; the tab-status fallback below covers a
// missed event (the page may have gone idle before we attached).
export async function enableLifecycle(tabId) {
  try {
    await send(tabId, "Page.enable");
    await send(tabId, "Page.setLifecycleEventsEnabled", { enabled: true });
  } catch {
    // No debugger available: waitForLoad falls back to tab status.
  }
}

// loadGraceMs bounds how long a completed page is observed for network idle
// before it is accepted as loaded, so a page that never goes idle still returns.
const loadGraceMs = 800;

// Resolves once the tab finishes a navigation that started after this call and
// its network settles. Rejects on tab close or timeout.
export function waitForLoad(tabId, timeoutMs) {
  return new Promise((resolve, reject) => {
    const startedAt = Date.now();
    let started = false;
    let done = false;
    let graceTimer = null;

    const finish = (err) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      clearTimeout(graceTimer);
      clearInterval(poll);
      chrome.tabs.onUpdated.removeListener(onUpdated);
      chrome.tabs.onRemoved.removeListener(onRemoved);
      chrome.debugger.onEvent.removeListener(onLifecycle);
      if (err) reject(err);
      else resolve();
    };

    const onLifecycle = (source, method, params) => {
      if (source.tabId !== tabId || method !== "Page.lifecycleEvent") return;
      if (params?.name === "networkAlmostIdle") finish();
    };

    // A completed tab whose idle event was missed still finishes, after a short
    // observation window that lets late requests start.
    const onComplete = () => {
      if (graceTimer || done) return;
      graceTimer = setTimeout(() => finish(), loadGraceMs);
    };

    const onUpdated = (id, info) => {
      if (id !== tabId) return;
      if (info.status === "loading" || info.url) started = true;
      if (info.status === "complete" && started) onComplete();
    };
    const onRemoved = (id) => {
      if (id === tabId) finish(new ActionError("tab_closed", "the session's tab was closed"));
    };
    const poll = setInterval(async () => {
      try {
        const tab = await chrome.tabs.get(tabId);
        if (tab.status === "loading") started = true;
        else if (tab.status === "complete" && (started || Date.now() - startedAt > 2000)) onComplete();
      } catch {
        finish(new ActionError("tab_closed", "the session's tab was closed"));
      }
    }, 250);
    const timer = setTimeout(
      () => finish(new ActionError("timeout", `page did not finish loading within ${timeoutMs / 1000}s`)),
      timeoutMs,
    );

    chrome.tabs.onUpdated.addListener(onUpdated);
    chrome.tabs.onRemoved.addListener(onRemoved);
    chrome.debugger.onEvent.addListener(onLifecycle);
  });
}
