export function buildToolMessageSummary(message, declaredTools = []) {
  if (message.tool_calls?.length) {
    const summaries = message.tool_calls.map((call) => {
      const name = call.function?.name || "tool";
      const match = findDeclaredToolForCall(call, declaredTools);
      return match?.name ? `${name} matched declared tool` : name;
    });
    return `Tools: ${summaries.join(", ")}`;
  }
  if (message.message_type === "tool_result") {
    const label = message.name || message.tool_call_id || "tool result";
    return `Tool result: ${label}`;
  }
  return "";
}

function getDeclaredToolName(tool = {}) {
  return (
    tool.name ||
    tool.function?.name ||
    tool.function_name ||
    tool.tool_name ||
    tool.title ||
    ""
  );
}

function getDeclaredToolDescription(tool = {}) {
  return tool.description || tool.function?.description || "";
}

function getDeclaredToolParameters(tool = {}) {
  return (
    tool.parameters ||
    tool.function?.parameters ||
    tool.input_schema ||
    tool.inputSchema ||
    ""
  );
}

export function normalizeDeclaredTool(tool = {}, index = 0) {
  const name = getDeclaredToolName(tool) || `tool_${index + 1}`;
  const source = tool.source || (tool.input_schema || tool.inputSchema ? "anthropic" : tool.function ? "openai" : tool.type || "tool");
  return {
    ...tool,
    id: tool.id || `${name || "tool"}-${index}`,
    type: tool.type || "function",
    source,
    name,
    description: getDeclaredToolDescription(tool),
    parameters: getDeclaredToolParameters(tool),
  };
}

export function collectTraceToolCalls(detail) {
  if (!detail) {
    return [];
  }
  const calls = [];
  for (const message of detail.messages || []) {
    for (const call of message.tool_calls || []) {
      calls.push(call);
    }
  }
  for (const call of detail.tool_calls || []) {
    calls.push(call);
  }
  return calls;
}

/*
 * The output a call produced, keyed by the call id it answers.
 *
 * A tool call and its result are two messages in a chat trace, and the result
 * names the call with `tool_call_id`. The conversation tab merges the two into
 * one card because it walks the Observation IR; the 工具调用 tab reads the
 * recorder's flat messages, so it needs this lookup to show the pair in its own
 * dialog without walking the IR twice.
 */
export function collectToolOutputs(detail) {
  const outputs = new Map();
  for (const message of detail?.messages || []) {
    const type = String(message.message_type || "");
    if (type !== "tool_result" && type !== "function_call_output" && message.role !== "tool") {
      continue;
    }
    const id = message.tool_call_id || message.toolCallId || "";
    if (id && !outputs.has(id)) {
      outputs.set(id, message);
    }
  }
  return outputs;
}

function normalizeToolName(value = "") {
  return String(value || "").trim().toLowerCase();
}

export function isSameToolName(left = "", right = "") {
  return normalizeToolName(left) !== "" && normalizeToolName(left) === normalizeToolName(right);
}

export function findDeclaredToolForCall(call, declaredTools = []) {
  const name = call?.function?.name || "";
  return declaredTools.find((tool) => isSameToolName(getDeclaredToolName(tool), name)) || null;
}

/*
 * JSON arrives in two shapes: the recorder keeps the text the client sent, and
 * the parsed observation hands back the decoded object. A `<pre>` renders a
 * string, so a surface has to normalise both before it prints - handing it an
 * object is a React child error, not a formatting nit - and pretty-printing it
 * is what makes a schema or an argument list readable.
 */
export function formatJSONValue(value, fallback = "{}") {
  if (value === null || value === undefined || value === "") {
    return fallback;
  }
  if (typeof value === "string") {
    try {
      return JSON.stringify(JSON.parse(value), null, 2);
    } catch {
      return value;
    }
  }
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

export function buildToolSchemaSummary(parameters = "") {
  if (!parameters) {
    return "No schema";
  }
  let payload = parameters;
  if (typeof parameters === "string") {
    try {
      payload = JSON.parse(parameters);
    } catch {
      return "Schema available";
    }
  }
  const properties = payload?.properties ? Object.keys(payload.properties) : [];
  if (properties.length) {
    return `${properties.length} field${properties.length > 1 ? "s" : ""}: ${properties.slice(0, 3).join(", ")}${properties.length > 3 ? "..." : ""}`;
  }
  return payload?.type ? `Schema type ${payload.type}` : "JSON schema";
}
