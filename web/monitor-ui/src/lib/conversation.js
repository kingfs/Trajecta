/*
 * The conversation model behind the trace page's 会话 tab.
 *
 * A trace used to be rendered as "the recorder's messages" - a flat list the
 * parser happened to extract - which answered "what was said" but could not
 * answer "where": an audit finding points at a node of the Observation IR
 * (`trace#<id>#node#<node>#path#$.input[159]`) and nothing in that flat list had
 * an address to jump to. The IR is the structure that does have addresses, one
 * per node, stable across reparses, so the conversation is rebuilt from it and
 * every rendered step carries the node it came from.
 *
 * Everything here is pure: the shape of a step, the failure rules, the anchors,
 * and the target-string grammar. The component next to it only draws.
 */

// Node kinds that are a step of the conversation. The IR knows more than these
// (tool declarations, usage, and whatever a provider invented this week); a
// declaration belongs to the request's tool list and the rest has no place in a
// dialogue, so they are filtered out by the two sets below rather than by
// name-matching each renderer.
export const CONVERSATION_KINDS = [
  "instruction",
  "message",
  "text",
  "reasoning",
  "refusal",
  "code",
  "code_result",
  "file",
  "image",
  "citation",
  "safety",
  "error",
  "tool_call",
  "tool_result",
  "server_tool_call",
  "server_tool_result",
  "unknown",
];

const CALL_KINDS = new Set(["tool_call", "server_tool_call", "code"]);
const RESULT_KINDS = new Set(["tool_result", "server_tool_result", "code_result"]);

// Top-level nodes that are not part of the dialogue.
const NON_STEP_KINDS = new Set(["tool_declaration", "usage"]);

// A status that means the call or the tool run did not succeed. The list is the
// one TrajectoryMiner and this console's own status helpers already agree on, so
// a "cancelled" call is red in both views.
const FAILURE_STATUS = new Set([
  "error",
  "failed",
  "failure",
  "fail",
  "timeout",
  "timedout",
  "cancelled",
  "canceled",
  "rejected",
  "aborted",
  "killed",
  "incomplete",
]);

export function nodeKind(node) {
  return String(node?.normalized_type || node?.normalizedType || "unknown");
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function nodeText(node) {
  return String(node?.text_preview || node?.textPreview || node?.text || "");
}

export function nodeRaw(node) {
  const raw = node?.raw;
  if (isObject(raw) || Array.isArray(raw)) {
    return raw;
  }
  if (typeof raw === "string" && raw.trim()) {
    try {
      return JSON.parse(raw);
    } catch {
      return raw;
    }
  }
  return null;
}

// The same call is written differently by each protocol family, and the UI is
// handed the provider's own JSON: OpenAI chat nests it under `function`, Gemini
// under `functionCall` / `functionResponse`, Anthropic and the Responses API put
// it at the top level. Reading all three here is what keeps a tool card from
// rendering "No arguments" for two providers out of three.
function protocolCallOf(raw) {
  if (!isObject(raw)) {
    return {};
  }
  const keys = [
    // calls
    "function",
    "functionCall",
    "toolCall",
    "function_call",
    "tool_call",
    // results
    "functionResponse",
    "toolResponse",
    "function_response",
    "tool_response",
  ];
  for (const key of keys) {
    if (isObject(raw[key])) {
      return raw[key];
    }
  }
  return {};
}

function callIDOf(step) {
  const raw = isObject(step.raw) ? step.raw : {};
  const nested = protocolCallOf(raw);
  return String(raw.call_id || raw.tool_call_id || raw.callId || raw.tool_use_id || raw.id || nested.id || nested.call_id || "").trim();
}

export function toolNameOf(step) {
  const raw = isObject(step.raw) ? step.raw : {};
  const nested = protocolCallOf(raw);
  return String(raw.name || raw.function_name || raw.tool_name || nested.name || step.text || "").trim();
}

export function toolArgumentsOf(step) {
  const raw = isObject(step.raw) ? step.raw : {};
  const nested = protocolCallOf(raw);
  const value = raw.arguments ?? raw.args ?? raw.input ?? raw.parameters ?? nested.arguments ?? nested.args ?? nested.input ?? nested.parameters;
  if (value === undefined || value === null || value === "") {
    return "";
  }
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (trimmed.startsWith("{") || trimmed.startsWith("[")) {
      try {
        return JSON.stringify(JSON.parse(trimmed), null, 2);
      } catch {
        return value;
      }
    }
    return value;
  }
  return JSON.stringify(value, null, 2);
}

export function toolOutputOf(step) {
  const text = nodeText(step);
  if (text) {
    return { text, structured: false };
  }
  const raw = isObject(step.raw) ? step.raw : {};
  // Gemini wraps the payload of a function response, so its own object is
  // unwrapped before the generic output fields are tried.
  const nested = protocolCallOf(raw);
  const value =
    raw.output ??
    raw.content ??
    raw.stdout ??
    raw.result ??
    nested.response ??
    nested.output ??
    nested.content ??
    nested.result;
  if (value === undefined || value === null) {
    return { text: "", structured: false };
  }
  if (typeof value === "string") {
    return { text: value, structured: false };
  }
  return { text: JSON.stringify(value, null, 2), structured: true };
}

/**
 * nodeFailed answers the one question the row has to answer at a glance.
 *
 * The precedence is the contract the IR parsers already follow: an explicit
 * error flag wins, then a non-zero exit code, then a status word. A node that
 * says nothing about its outcome is not a failure - guessing from the text
 * would paint a conversation red for quoting an error message.
 */
export function nodeFailed(node) {
  const raw = nodeRaw(node);
  if (isObject(raw)) {
    if (raw.is_error === true || raw.isError === true) {
      return true;
    }
    const exit = raw.exit_code ?? raw.exitCode;
    if (exit !== undefined && exit !== null && exit !== "" && Number.isFinite(Number(exit)) && Number(exit) !== 0) {
      return true;
    }
    if (isObject(raw.error) || typeof raw.error === "string") {
      if (typeof raw.error === "string" ? raw.error.trim() : true) {
        return true;
      }
    }
    if (FAILURE_STATUS.has(String(raw.status || "").toLowerCase())) {
      return true;
    }
  }
  return FAILURE_STATUS.has(String(node?.status || "").toLowerCase());
}

function partFromNode(node) {
  return {
    id: String(node?.id || ""),
    parentID: String(node?.parent_id || node?.parentId || ""),
    path: String(node?.path || ""),
    kind: nodeKind(node),
    role: String(node?.role || ""),
    providerType: String(node?.provider_type || node?.providerType || ""),
    text: nodeText(node),
    raw: nodeRaw(node),
    failed: nodeFailed(node),
  };
}

function partsText(step) {
  return step.parts
    .map((part) => part.text)
    .filter(Boolean)
    .join("\n\n");
}

// A call entry is a tool call that arrived as a *part* of a message - which is
// how every OpenAI chat trace records one: the assistant message holds the
// `tool_calls` array, so the call is a child of the message node, not a node of
// its own. It is still an action, with arguments and a result to show, so it
// becomes an entry on the step rather than a paragraph of the message.
function callEntry(node) {
  return { ...partFromNode(node), results: [], parts: [] };
}

function descendantsOf(node, childrenOf) {
  const out = [];
  const walk = (parent) => {
    for (const child of childrenOf.get(String(parent.id || "")) || []) {
      out.push(child);
      walk(child);
    }
  };
  walk(node);
  return out;
}

/**
 * buildConversationSteps turns the flattened Observation IR into the list the
 * tab renders.
 *
 * A step is a top-level node (`depth: 0`), i.e. one item of the request's
 * conversation array or one item of the response's output. Everything under it
 * is carried by the step: text and reasoning become parts, a tool call becomes
 * a call entry, and a tool result becomes a result of the call it answers. The
 * nesting is not uniformly one level deep - a chat response is
 * `$.choices[0]` > `.message` > `.content` - so the whole subtree is walked
 * rather than just the direct children.
 */
export function buildConversationSteps(nodes = []) {
  if (!Array.isArray(nodes)) {
    return [];
  }
  const rows = nodes.filter((node) => node && typeof node === "object");
  const childrenOf = new Map();
  for (const node of rows) {
    const parentID = String(node.parent_id || node.parentId || "");
    if (!parentID) {
      continue;
    }
    const siblings = childrenOf.get(parentID);
    if (siblings) {
      siblings.push(node);
    } else {
      childrenOf.set(parentID, [node]);
    }
  }

  const units = [];
  for (const node of rows) {
    if (Number(node.depth || 0) !== 0) {
      continue;
    }
    const kind = nodeKind(node);
    if (NON_STEP_KINDS.has(kind)) {
      continue;
    }
    if (RESULT_KINDS.has(kind)) {
      units.push({ ...partFromNode(node), isResultUnit: true, parts: [], results: [] });
      continue;
    }
    const step = { ...partFromNode(node), index: Number(node.index || 0), parts: [], calls: [], results: [] };
    for (const child of descendantsOf(node, childrenOf)) {
      const childKind = nodeKind(child);
      if (CALL_KINDS.has(childKind)) {
        const entry = callEntry(child);
        for (const grandchild of descendantsOf(child, childrenOf)) {
          entry.parts.push(partFromNode(grandchild));
        }
        step.calls.push(entry);
        continue;
      }
      if (RESULT_KINDS.has(childKind)) {
        step.results.push({ ...partFromNode(child), isResultUnit: true, parts: [], results: [] });
        continue;
      }
      step.parts.push(partFromNode(child));
    }
    // A response container - the chat `choice`, the Responses output item - has
    // no role of its own: the role is on the message inside it, and that is the
    // one a reader needs on the card.
    if (!step.role) {
      step.role = step.parts.find((part) => part.role)?.role || "";
    }
    units.push(step);
  }
  return mergeToolResults(units);
}

function callEntriesOf(step) {
  const entries = [];
  if (CALL_KINDS.has(step.kind)) {
    entries.push(step);
  }
  for (const call of step.calls || []) {
    entries.push(call);
  }
  return entries;
}

/**
 * mergeToolResults puts a call and its result in one card.
 *
 * They are two nodes - they were two lines of the request or the response - but
 * they are one action to a reader: what was run, and what came back. The result
 * keeps its own anchor inside the card, so an audit finding pointing at either
 * node still lands on it. A result that names a call this conversation does not
 * hold stays where it was rather than being attached to the wrong one.
 */
function mergeToolResults(units) {
  const merged = [];
  const ownerByCallID = new Map();
  let lastCallStep = null;
  for (const unit of units) {
    if (!unit.isResultUnit) {
      const entries = callEntriesOf(unit);
      if (entries.length) {
        lastCallStep = unit;
        // The id-less call is registered under the empty key as well, so a
        // result that names an id can still find a call that has none.
        for (const entry of entries) {
          ownerByCallID.set(callIDOf(entry) || "", unit);
        }
      }
      merged.push(unit);
      continue;
    }
    const resultID = callIDOf(unit);
    let owner = resultID ? ownerByCallID.get(resultID) : lastCallStep;
    if (!owner && resultID && lastCallStep && callEntriesOf(lastCallStep).some((entry) => !callIDOf(entry))) {
      owner = lastCallStep;
    }
    const entry = owner
      ? callEntriesOf(owner).find((candidate) => (callIDOf(candidate) || "") === (resultID || "")) || null
      : null;
    if (entry) {
      entry.results = entry.results || [];
      entry.results.push(unit);
      continue;
    }
    merged.push(unit);
  }
  return merged;
}

/**
 * conversationText is what a reader sees for a step, whichever kind it is.
 * A tool call's *text* is its function name, so its readable body is its
 * arguments; a result's is its output.
 */
export function conversationText(step) {
  if (CALL_KINDS.has(step.kind)) {
    return toolArgumentsOf(step);
  }
  if (RESULT_KINDS.has(step.kind)) {
    return toolOutputOf(step).text;
  }
  return step.text || partsText(step);
}

// The parts worth their own block: a part whose text is already the step's text
// is the same sentence twice (the IR emits a message and the text inside it),
// and a part that is empty carries nothing.
export function distinctParts(step) {
  const seen = new Set([step.text]);
  return step.parts.filter((part) => {
    const text = part.text;
    if (text && seen.has(text)) {
      return false;
    }
    if (text) {
      seen.add(text);
    }
    return Boolean(text) || Boolean(part.raw);
  });
}

function haystackOf(step, chunks) {
  chunks.push(step.text, step.role, step.path, conversationText(step), toolNameOf(step));
  const called = CALL_KINDS.has(step.kind) ? toolArgumentsOf(step) : "";
  if (called) {
    chunks.push(called);
  }
  for (const part of step.parts || []) {
    chunks.push(part.text);
  }
  for (const call of step.calls || []) {
    haystackOf(call, chunks);
  }
  for (const result of step.results || []) {
    haystackOf(result, chunks);
  }
  return chunks;
}

export function stepHaystack(step) {
  return haystackOf(step, []).filter(Boolean).join("\n").toLowerCase();
}

export function conversationStats(steps = []) {
  const stats = { steps: steps.length, toolCalls: 0, toolResults: 0, reasoning: 0, failed: 0, truncated: 0 };
  const countCalls = (step) => {
    if (CALL_KINDS.has(step.kind)) {
      stats.toolCalls += 1;
    }
    stats.toolCalls += (step.calls || []).length;
    if (RESULT_KINDS.has(step.kind)) {
      stats.toolResults += 1;
    }
    stats.toolResults += (step.results || []).length;
    for (const call of step.calls || []) {
      stats.toolResults += (call.results || []).length;
    }
    if (step.kind === "reasoning") {
      stats.reasoning += 1;
    }
    if (step.failed || (step.results || []).some((result) => result.failed)) {
      stats.failed += 1;
    }
    if (step.kind === "unknown" && !step.text) {
      stats.truncated += 1;
    }
  };
  for (const step of steps) {
    countCalls(step);
  }
  return stats;
}

/**
 * kindLabel names a node kind for a reader. The dictionary carries one label per
 * kind the IR can emit; a kind a newer parser invented is shown as its own words
 * rather than as a missing-translation key.
 */
export function kindLabel(kind, t) {
  const key = `conversation.kind.${kind}`;
  const label = t ? t(key) : key;
  return label === key ? String(kind || "").replaceAll("_", " ") : label;
}

/**
 * describeConversationNode is the human name of the place a finding names: the
 * step number, what kind of node it is, the role or the tool it belongs to.
 * `$.input[159]` is the storage form; `#160 · tool call · exec_command` is the
 * same place, said the way the conversation shows it.
 */
export function describeConversationNode(node, t) {
  if (!node) {
    return "";
  }
  const raw = isObject(nodeRaw(node)) ? nodeRaw(node) : {};
  const name = String(raw.name || raw.function_name || raw.tool_name || "").trim();
  const kind = kindLabel(nodeKind(node), t);
  // The path, not the index: the path is what the finding shows and what the
  // reader can look for, and the conversation numbers its own steps from 1.
  const place = String(node.path || "").trim() || `#${Number(node.index ?? 0) + 1}`;
  return [place, kind, String(node.role || "").trim(), name].filter(Boolean).join(" · ");
}

/**
 * parseConversationAnchor reads the three ways a caller can name a place in a
 * conversation: a bare node id, a `$.input[159]` path, or the composed evidence
 * path an audit finding stores (`trace#…​#node#…​#path#…`).
 */
export function parseConversationAnchor(value = "") {
  const raw = String(value || "").trim();
  if (!raw) {
    return { nodeID: "", path: "" };
  }
  if (raw.includes("#")) {
    const segments = raw.split("#");
    let nodeID = "";
    let path = "";
    for (let index = 0; index < segments.length; index += 1) {
      if (segments[index] === "node") {
        nodeID = String(segments[index + 1] || "").trim();
      }
      if (segments[index] === "path") {
        path = segments.slice(index + 1).join("#").trim();
      }
    }
    return { nodeID, path };
  }
  if (raw.startsWith("$")) {
    return { nodeID: "", path: raw };
  }
  return { nodeID: raw, path: "" };
}

/**
 * formatEvidenceTarget shortens an evidence path to the part a human reads.
 *
 * `trace#ba27…#node#node_1bf9…#path#$.input[159]` is the storage form: it names
 * the trace twice and the node by a hash. What identifies the place to a reader
 * is the path, and the index inside it for a list.
 */
export function formatEvidenceTarget(value = "") {
  const { nodeID, path } = parseConversationAnchor(value);
  if (path) {
    const match = path.match(/\[(\d+)\]$/);
    if (match) {
      return `${path} · #${Number(match[1]) + 1}`;
    }
    return path;
  }
  return nodeID || String(value || "").trim();
}

/**
 * findConversationTarget answers "which anchor does this focus value name",
 * preferring the node id (exact) and falling back to the path.
 */
// Whether a step carries the address anywhere in its subtree: its own parts, a
// call entry, a result, or a part of either.
function carriedAddress(step, field, value) {
  const entries = [...(step.parts || []), ...(step.calls || []), ...(step.results || [])];
  for (const entry of entries) {
    if (entry[field] === value) {
      return true;
    }
    const nested = [...(entry.parts || []), ...(entry.results || [])];
    if (nested.some((child) => child[field] === value)) {
      return true;
    }
  }
  return false;
}

export function findConversationTarget(steps = [], focus = "") {
  const { nodeID, path } = parseConversationAnchor(focus);
  if (!nodeID && !path) {
    return "";
  }
  const matches = (step) => {
    if (nodeID && (step.id === nodeID || carriedAddress(step, "id", nodeID))) {
      return true;
    }
    if (path && (step.path === path || carriedAddress(step, "path", path))) {
      return true;
    }
    return false;
  };
  const step = steps.find(matches);
  return step ? step.id : "";
}
