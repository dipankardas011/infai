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

// Grouping is cosmetic; failures (e.g. tab in a popup window) are ignored.
export async function groupTab(tabId, sessionName) {
  try {
    const groupId = await chrome.tabs.group({ tabIds: [tabId] });
    await chrome.tabGroups.update(groupId, { title: `▸ infaiw · ${sessionName}`, color: "green" });
    return groupId;
  } catch (err) {
    console.warn("infai: could not group tab", err);
    return null;
  }
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

// Resolves once the tab finishes a navigation that started after this call.
// Events alone are racy (a stale "complete" from the previous page, or events
// fired before a freshly created tab is known), so it also polls the tab and
// accepts an already-complete tab after a short grace period, which covers
// same-document navigations that never enter "loading".
export function waitForLoad(tabId, timeoutMs) {
  return new Promise((resolve, reject) => {
    const startedAt = Date.now();
    let started = false;
    let done = false;

    const finish = (err) => {
      if (done) return;
      done = true;
      clearTimeout(timer);
      clearInterval(poll);
      chrome.tabs.onUpdated.removeListener(onUpdated);
      chrome.tabs.onRemoved.removeListener(onRemoved);
      if (err) reject(err);
      else resolve();
    };
    const onUpdated = (id, info) => {
      if (id !== tabId) return;
      if (info.status === "loading" || info.url) started = true;
      if (info.status === "complete" && started) finish();
    };
    const onRemoved = (id) => {
      if (id === tabId) finish(new ActionError("tab_closed", "the session's tab was closed"));
    };
    const poll = setInterval(async () => {
      try {
        const tab = await chrome.tabs.get(tabId);
        if (tab.status === "loading") started = true;
        else if (tab.status === "complete" && (started || Date.now() - startedAt > 2000)) finish();
      } catch {
        finish(new ActionError("tab_closed", "the session's tab was closed"));
      }
    }, 500);
    const timer = setTimeout(
      () => finish(new ActionError("timeout", `page did not finish loading within ${timeoutMs / 1000}s`)),
      timeoutMs,
    );

    chrome.tabs.onUpdated.addListener(onUpdated);
    chrome.tabs.onRemoved.addListener(onRemoved);
  });
}
