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

function callIDOf(step) {
  const raw = isObject(step.raw) ? step.raw : {};
  return String(raw.call_id || raw.tool_call_id || raw.callId || raw.id || "").trim();
}

export function toolNameOf(step) {
  const raw = isObject(step.raw) ? step.raw : {};
  return String(raw.name || raw.function_name || raw.tool_name || step.text || "").trim();
}

export function toolArgumentsOf(step) {
  const raw = isObject(step.raw) ? step.raw : {};
  const value = raw.arguments ?? raw.args ?? raw.input ?? raw.parameters;
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
  const value = raw.output ?? raw.content ?? raw.stdout ?? raw.result;
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

/**
 * buildConversationSteps turns the flattened Observation IR into the list the
 * tab renders.
 *
 * A step is a top-level node (`depth: 0`), i.e. one item of the request's
 * conversation array or one item of the response's output. Its children are
 * content parts: the IR emits both a message and the text inside it, so parts
 * are carried as anchors and only rendered when they add content of their own.
 */
export function buildConversationSteps(nodes = []) {
  if (!Array.isArray(nodes)) {
    return [];
  }
  const steps = [];
  const byId = new Map();
  for (const node of nodes) {
    const depth = Number(node?.depth || 0);
    if (depth === 0) {
      if (NON_STEP_KINDS.has(nodeKind(node))) {
        continue;
      }
      const step = { ...partFromNode(node), index: Number(node?.index || 0), parts: [], results: [] };
      byId.set(step.id, step);
      steps.push(step);
      continue;
    }
    if (depth === 1) {
      const parent = byId.get(String(node?.parent_id || node?.parentId || ""));
      if (parent) {
        parent.parts.push(partFromNode(node));
      }
    }
  }
  return mergeToolPairs(steps);
}

/**
 * mergeToolPairs puts a call and its result in one card.
 *
 * They are two nodes - they were two lines of the request or the response - but
 * they are one action to a reader: what was run, and what came back. The result
 * keeps its own anchor inside the card, so an audit finding pointing at either
 * node still lands on it.
 */
function mergeToolPairs(steps) {
  const merged = [];
  for (const step of steps) {
    const previous = merged[merged.length - 1];
    if (previous && RESULT_KINDS.has(step.kind) && CALL_KINDS.has(previous.kind)) {
      const resultID = callIDOf(step);
      const callID = callIDOf(previous);
      // A result without an id pairs with the call right above it; two results
      // that both name a different call do not pair at all.
      if (!resultID || !callID || resultID === callID) {
        previous.results.push(step);
        continue;
      }
    }
    merged.push(step);
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

export function stepHaystack(step) {
  const chunks = [step.text, step.role, step.path, conversationText(step)];
  for (const part of step.parts) {
    chunks.push(part.text);
  }
  for (const result of step.results) {
    chunks.push(result.text, conversationText(result));
  }
  return chunks.filter(Boolean).join("\n").toLowerCase();
}

export function conversationStats(steps = []) {
  const stats = { steps: steps.length, toolCalls: 0, toolResults: 0, reasoning: 0, failed: 0, truncated: 0 };
  for (const step of steps) {
    if (CALL_KINDS.has(step.kind)) {
      stats.toolCalls += 1;
    }
    if (RESULT_KINDS.has(step.kind)) {
      stats.toolResults += 1;
    }
    stats.toolResults += step.results.length;
    if (step.kind === "reasoning") {
      stats.reasoning += 1;
    }
    if (step.failed || step.results.some((result) => result.failed)) {
      stats.failed += 1;
    }
    if (step.kind === "unknown" && !step.text) {
      stats.truncated += 1;
    }
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
export function findConversationTarget(steps = [], focus = "") {
  const { nodeID, path } = parseConversationAnchor(focus);
  if (!nodeID && !path) {
    return "";
  }
  const matches = (step) => {
    if (nodeID) {
      if (step.id === nodeID) {
        return true;
      }
      if (step.parts.some((part) => part.id === nodeID)) {
        return true;
      }
      if (step.results.some((result) => result.id === nodeID || result.parts.some((part) => part.id === nodeID))) {
        return true;
      }
    }
    if (path) {
      if (step.path === path) {
        return true;
      }
      if (step.parts.some((part) => part.path === path)) {
        return true;
      }
      if (step.results.some((result) => result.path === path || result.parts.some((part) => part.path === path))) {
        return true;
      }
    }
    return false;
  };
  const step = steps.find(matches);
  return step ? step.id : "";
}
