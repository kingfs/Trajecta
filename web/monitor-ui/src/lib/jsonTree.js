/*
 * The protocol tab reads the parsed exchange as a tree rather than as a wall of
 * pretty-printed JSON: a request of forty messages is a list of forty collapsed
 * rows, so the response half of the page is one scroll away instead of thirty.
 *
 * The tree is assembled from the semantic nodes, not from a JSON document the
 * API does not return. Every node carries the JSON path it was read from and the
 * raw value at that path, so writing each value at its path rebuilds the payload
 * the parser saw, and the node rides along as the label for the value it
 * describes. Nodes whose payload was not JSON - an HTTP error body, a stream
 * fragment - keep their text and are rendered as that text.
 */

/* `$.messages[0].content` -> ["messages", 0, "content"]; null when the path is
   not the dot-and-bracket shape the parsers emit, so the caller can skip it
   rather than place a value somewhere wrong. */
export function splitJSONPath(path = "") {
  const trimmed = String(path ?? "").trim();
  if (!trimmed.startsWith("$")) {
    return null;
  }
  const rest = trimmed.slice(1);
  const parts = [];
  const pattern = /\.([^.[\]]+)|\[(\d+)\]/g;
  let consumed = 0;
  let match = pattern.exec(rest);
  while (match) {
    if (match.index !== consumed) {
      return null;
    }
    parts.push(match[2] === undefined ? match[1] : Number(match[2]));
    consumed = pattern.lastIndex;
    match = pattern.exec(rest);
  }
  return consumed === rest.length ? parts : null;
}

export function joinJSONPath(parent, key) {
  if (typeof key === "number") {
    return `${parent}[${key}]`;
  }
  return parent === "$" ? `$.${key}` : `${parent}.${key}`;
}

/* `Object.entries` hands back array indices as strings, and a path has to keep
   the `[0]` form the parsers write or it will not match the node it describes. */
export function childJSONPath(parentPath, parentValue, key) {
  return joinJSONPath(parentPath, Array.isArray(parentValue) ? Number(key) : key);
}

/* The path of the value that contains this one, or "" for a top-level key. */
export function parentJSONPath(path = "") {
  const trimmed = String(path ?? "").trim();
  if (!trimmed.startsWith("$.") && !trimmed.startsWith("$[")) {
    return "";
  }
  const bracket = trimmed.lastIndexOf("[");
  if (bracket > 1 && trimmed.endsWith("]")) {
    return trimmed.slice(0, bracket);
  }
  const dot = trimmed.lastIndexOf(".");
  return dot > 1 ? trimmed.slice(0, dot) : "$";
}

/*
 * A node's raw payload is JSON text when it came off the API (`json.RawMessage`)
 * and already-decoded when it came from a test fixture. `text` is what to show
 * when it is neither: the recorder preserves a body it could not parse, and an
 * error sentence is exactly what the reader needs from it.
 */
export function parseNodePayload(raw) {
  if (raw === null || raw === undefined || raw === "") {
    return { ok: false, empty: true, text: "" };
  }
  if (typeof raw === "string") {
    try {
      return { ok: true, value: JSON.parse(raw) };
    } catch {
      return { ok: false, text: raw };
    }
  }
  return { ok: true, value: raw };
}

function isPlainObject(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isContainer(value) {
  return Array.isArray(value) || isPlainObject(value);
}

/* Write `value` at `parts`, creating the containers on the way. A container that
   already holds a different shape is replaced: the parser saw a child there, so
   the child is the truth about that path. */
function placePath(root, parts, value) {
  if (!parts) {
    return false;
  }
  if (!parts.length) {
    if (isPlainObject(value)) {
      Object.assign(root, value);
      return true;
    }
    root.$ = value;
    return true;
  }
  let current = root;
  for (let index = 0; index < parts.length - 1; index += 1) {
    const key = parts[index];
    const wanted = typeof parts[index + 1] === "number" ? [] : {};
    const existing = current[key];
    if (!isContainer(existing) || Array.isArray(existing) !== Array.isArray(wanted)) {
      current[key] = wanted;
    }
    current = current[key];
  }
  current[parts[parts.length - 1]] = value;
  return true;
}

/*
 * Returns the payload as one document plus the node that describes each path.
 * Nodes the parser nested (a message's content, a call inside a message) are
 * already inside their parent's raw value, so placing them again is idempotent;
 * when they differ, the deeper node - the parsed one - wins.
 */
/*
 * What a row says while it is closed. The parsers already extracted the text of
 * a value - the sentence of a message, the name of a declared tool - and a reader
 * looking at a request wants that sentence, not "2 keys". A nested node that
 * carries the same text as its parent has nothing to add, so it falls back to the
 * shape of its value; that is what keeps the text from appearing twice under the
 * message that holds it.
 */
export function rowTextPreviews(nodesByPath) {
  const previews = new Map();
  for (const [path, node] of nodesByPath) {
    const preview = String(node?.text_preview || node?.textPreview || "").trim();
    if (!preview) {
      continue;
    }
    const parentPath = parentJSONPath(path);
    const parent = parentPath ? nodesByPath.get(parentPath) : null;
    const parentPreview = String(parent?.text_preview || parent?.textPreview || "").trim();
    previews.set(path, parentPreview === preview ? "" : preview);
  }
  return previews;
}

export function buildPathDocument(nodes = []) {
  const document = {};
  const nodesByPath = new Map();
  const unplaced = [];
  const visit = (list) => {
    for (const node of list || []) {
      const payload = parseNodePayload(node?.raw);
      const parts = splitJSONPath(node?.path);
      let placed = false;
      if (parts) {
        placed = payload.ok ? placePath(document, parts, payload.value) : placePath(document, parts, payload.text);
      }
      if (node?.path) {
        nodesByPath.set(node.path, node);
      }
      if (!placed && (payload.ok || payload.text)) {
        unplaced.push({ node, payload });
      }
      visit(node?.children);
    }
  };
  visit(nodes);
  return { document, nodesByPath, unplaced, rowPreviews: rowTextPreviews(nodesByPath) };
}

const JSON_KIND_LABELS = {
  string: "string",
  number: "number",
  boolean: "boolean",
  null: "null",
};

/* What kind of JSON the value is, as the identifier the parsers use for it. */
export function jsonKindLabel(value) {
  if (value === null) {
    return JSON_KIND_LABELS.null;
  }
  if (Array.isArray(value)) {
    return "array";
  }
  return JSON_KIND_LABELS[typeof value] || "object";
}

/*
 * The label a value is shown under. The parser's own vocabulary comes first -
 * `tool_call` says more than `object` - and `unknown` is never shown: when the
 * parser had no name for a value, the shape it is looking at is the honest
 * answer. An object with a `role` is a message in every protocol parsed here,
 * which is also what the parser calls it once it recognises one.
 */
export function describeJSONValue(node, value) {
  const normalized = String(node?.normalized_type || "").trim();
  if (normalized && normalized !== "unknown") {
    return { label: normalized, mapped: true };
  }
  const provider = String(node?.provider_type || "").trim();
  if (provider && provider !== "unknown") {
    return { label: provider, mapped: true };
  }
  if (isPlainObject(value) && typeof value.role === "string" && value.role) {
    return { label: "message", mapped: false };
  }
  return { label: jsonKindLabel(value), mapped: false };
}

/* A one-line rendering of a value, the way a JSON viewer shows the row it has
   not expanded. */
export function previewJSONValue(value, limit = 120) {
  let text;
  if (Array.isArray(value)) {
    return `${value.length} ${value.length === 1 ? "item" : "items"}`;
  }
  if (isPlainObject(value)) {
    const keys = Object.keys(value);
    return `${keys.length} ${keys.length === 1 ? "key" : "keys"}`;
  }
  if (value === null) {
    return "null";
  }
  if (typeof value === "string") {
    text = value;
  } else {
    text = String(value);
  }
  const flat = text.replace(/\s+/g, " ").trim();
  return flat.length > limit ? `${flat.slice(0, limit)}…` : flat;
}
