// A short rolling log of the actions the daemon asked for, so the side panel
// can show what the agent is doing right now and what it just did. The daemon
// never pushes per-session status; this log is the panel's only live signal.

import { mutate } from "./store.js";

const KEY = "activity";
const MAX_ENTRIES = 40;

// Plumbing actions the user did not ask about; they would drown the feed.
const QUIET = new Set(["highlight", "clear_highlight", "release_session"]);

// The panel names actions the way the agent's tools are named, so a screenshot
// reads as a read rather than as an unexplained "screenshot".
const LABELS = {
  snapshot: "read · elements",
  read: "read · text",
  screenshot: "read · screenshot",
  click: "act · click",
  type: "act · type",
  select: "act · select",
  click_at: "act · click (point)",
  type_at: "act · type (point)",
  navigate: "navigate",
  scroll: "scroll",
};

function summarize(action, args) {
  const ref = args.ref ? ` ${args.ref}` : "";
  switch (action) {
    case "navigate":
      return String(args.url ?? "");
    case "type":
      return `${JSON.stringify(String(args.text ?? "").slice(0, 60))} →${ref}${args.submit ? " ⏎" : ""}`;
    case "select":
      return `${JSON.stringify(String(args.value ?? "").slice(0, 60))} in${ref}`;
    case "wait_for":
      return JSON.stringify(String(args.text ?? "").slice(0, 60));
    default:
      return ref.trim();
  }
}

export function started(msg) {
  if (QUIET.has(msg.action)) return null;
  const args = msg.args && typeof msg.args === "object" ? msg.args : {};
  const entry = {
    id: String(msg.id),
    session_id: String(msg.session_id ?? ""),
    session_name: String(msg.session_name || msg.session_id || ""),
    action: LABELS[msg.action] ?? String(msg.action ?? ""),
    detail: summarize(msg.action, args),
    state: "running",
    at: Date.now(),
  };
  mutate(KEY, (list) => [entry, ...list].slice(0, MAX_ENTRIES), []).catch((err) =>
    console.error("infai: activity log write failed", err),
  );
  return entry.id;
}

export function finished(id, error) {
  if (id === null) return;
  mutate(
    KEY,
    (list) =>
      list.map((e) =>
        e.id === id
          ? { ...e, state: error ? "error" : "ok", ms: Date.now() - e.at, error: error ? String(error) : undefined }
          : e,
      ),
    [],
  ).catch((err) => console.error("infai: activity log write failed", err));
}
