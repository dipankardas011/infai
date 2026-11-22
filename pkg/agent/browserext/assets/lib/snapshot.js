// Turns a CDP Accessibility.getFullAXTree node list into a compact, indented
// text outline plus a ref -> backendDOMNodeId map for interactive elements.

const INTERACTIVE_ROLES = new Set([
  "button",
  "link",
  "textbox",
  "searchbox",
  "combobox",
  "checkbox",
  "radio",
  "switch",
  "menuitem",
  "menuitemcheckbox",
  "menuitemradio",
  "tab",
  "option",
  "slider",
  "spinbutton",
  "listbox",
  "treeitem",
]);

// InlineTextBox duplicates its StaticText parent; the others carry no content.
const ALWAYS_SKIP = new Set(["InlineTextBox", "LineBreak", "none", "presentation"]);

const MAX_FIELD_CHARS = 120;

function text(prop) {
  const v = prop?.value;
  if (v === undefined || v === null) return "";
  return String(v).replace(/\s+/g, " ").trim();
}

function quote(s) {
  const clipped = s.length > MAX_FIELD_CHARS ? s.slice(0, MAX_FIELD_CHARS - 1) + "…" : s;
  return `"${clipped.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

// Returns {line, interactive} for a node worth emitting, or null to skip it.
// context is the nearest named ancestor's name, used to drop StaticText that
// merely repeats it (e.g. the label inside a button).
function describe(node, context) {
  const role = text(node.role) || "unknown";
  if (ALWAYS_SKIP.has(role)) return null;

  const name = text(node.name);
  const value = text(node.value);
  const valueSuffix = value ? ` value=${quote(value)}` : "";

  if (INTERACTIVE_ROLES.has(role)) {
    return { line: `${role} ${quote(name)}${valueSuffix}`, interactive: true };
  }
  if (role === "RootWebArea") return { line: `${role} ${quote(name)}`, interactive: false };
  if (!name) return null;
  if (role === "StaticText" && context.includes(name)) return null;
  return { line: `${role} ${quote(name)}${valueSuffix}`, interactive: false };
}

export function formatSnapshot(nodes) {
  const refs = {};
  if (!Array.isArray(nodes) || nodes.length === 0) return { text: "", refs };

  const byId = new Map(nodes.map((n) => [n.nodeId, n]));
  const root =
    nodes.find((n) => n.parentId === undefined || n.parentId === null || !byId.has(n.parentId)) ??
    nodes[0];

  const lines = [];
  const seen = new Set();
  let nextRef = 1;
  // Iterative DFS: real pages can nest deeply enough to make recursion risky.
  const stack = [{ id: root.nodeId, depth: 0, context: "" }];

  while (stack.length > 0) {
    const { id, depth, context } = stack.pop();
    if (seen.has(id)) continue;
    seen.add(id);
    const node = byId.get(id);
    if (!node) continue;

    let childDepth = depth;
    let childContext = context;

    if (!node.ignored) {
      const desc = describe(node, context);
      if (desc) {
        let line = desc.line;
        const backendId = node.backendDOMNodeId;
        if (desc.interactive && backendId !== undefined && backendId !== null) {
          const ref = `e${nextRef++}`;
          refs[ref] = backendId;
          line = `${ref} ${line}`;
        }
        lines.push("  ".repeat(depth) + line);
        childDepth = depth + 1;
      }
      const name = text(node.name);
      if (name) childContext = name;
    }

    const kids = Array.isArray(node.childIds) ? node.childIds : [];
    for (let i = kids.length - 1; i >= 0; i--) {
      stack.push({ id: kids[i], depth: childDepth, context: childContext });
    }
  }

  return { text: lines.join("\n"), refs };
}
