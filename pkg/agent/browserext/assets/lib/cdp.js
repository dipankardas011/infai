import { ActionError } from "./errors.js";

const COMMAND_TIMEOUT_MS = 15000;
const attached = new Set();

export function isAttached(tabId) {
  return attached.has(tabId);
}

export function forgetTab(tabId) {
  attached.delete(tabId);
}

async function ensureAttached(tabId) {
  if (attached.has(tabId)) return;
  try {
    await chrome.debugger.attach({ tabId }, "1.3");
  } catch (err) {
    const msg = String(err?.message ?? err);
    if (/no tab with given id/i.test(msg)) throw new ActionError("tab_closed", "the session's tab was closed");
    if (!/already attached/i.test(msg)) throw new ActionError("debugger", `cannot attach debugger: ${msg}`);
    // After a service-worker restart our in-memory set is empty but the
    // attachment may still be ours; a command only succeeds if it is.
    try {
      await chrome.debugger.sendCommand({ tabId }, "Runtime.evaluate", { expression: "1" });
    } catch {
      throw new ActionError("debugger", `cannot attach debugger: ${msg}`);
    }
  }
  attached.add(tabId);
}

export async function detach(tabId) {
  attached.delete(tabId);
  try {
    await chrome.debugger.detach({ tabId });
  } catch {
    // Not attached or tab gone; nothing to undo.
  }
}

// Sends a CDP command, attaching lazily. Protocol-level failures are thrown as
// ActionError("internal") with cdp=true so callers can reclassify them (e.g.
// a missing node becomes stale_ref).
export async function send(tabId, method, params = {}) {
  await ensureAttached(tabId);
  let timer;
  const timeout = new Promise((_, reject) => {
    timer = setTimeout(
      () => reject(new ActionError("timeout", `${method} timed out after ${COMMAND_TIMEOUT_MS / 1000}s`)),
      COMMAND_TIMEOUT_MS,
    );
  });
  try {
    return await Promise.race([chrome.debugger.sendCommand({ tabId }, method, params), timeout]);
  } catch (err) {
    if (err instanceof ActionError) throw err;
    const msg = String(err?.message ?? err);
    if (/no tab with given id/i.test(msg)) throw new ActionError("tab_closed", "the session's tab was closed");
    if (/not attached|detached while handling/i.test(msg)) {
      attached.delete(tabId);
      throw new ActionError("debugger", `debugger detached: ${msg}`);
    }
    const cdpErr = new ActionError("internal", `${method}: ${msg}`);
    cdpErr.cdp = true;
    throw cdpErr;
  } finally {
    clearTimeout(timer);
  }
}

export async function evaluate(tabId, expression) {
  const res = await send(tabId, "Runtime.evaluate", { expression, returnByValue: true });
  if (res.exceptionDetails) {
    throw new ActionError("internal", `page script failed: ${res.exceptionDetails.exception?.description ?? res.exceptionDetails.text}`);
  }
  return res.result?.value;
}
