// Which browser this extension runs in, as the daemon and the user name it.
// Vivaldi and Arc report themselves as Chromium/Chrome, so their users tell
// them apart by the profile label instead.

const BRANDS = {
  "Google Chrome": "Chrome",
  "Microsoft Edge": "Edge",
  Brave: "Brave",
  Opera: "Opera",
};

export // Mirrors Go's slug (pkg/agent/browser/bridge.go): letters and Nd digits are
// kept, everything else separates words.
function slug(s) {
  return String(s).toLowerCase().split(/[^\p{L}\p{Nd}]+/u).filter(Boolean).join("-");
}

// The fixed half of the id; the user only picks the part after it.
export function clientPrefix() {
  return `${slug(browserName()) || "chromium"}__`;
}

// Mirrors the daemon's baseID (pkg/agent/browser/bridge.go). The daemon adds a
// suffix only when two profiles of one browser share a label.
export function clientID(label, instance) {
  return clientPrefix() + (slug(label) || String(instance).slice(0, 8));
}

export function browserName() {
  for (const { brand } of navigator.userAgentData?.brands ?? []) {
    if (BRANDS[brand]) return BRANDS[brand];
  }
  if (navigator.brave) return "Brave";
  return "Chromium";
}
