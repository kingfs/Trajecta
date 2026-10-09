import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  buildConversationSteps,
  conversationStats,
  conversationText,
  describeConversationNode,
  distinctParts,
  findConversationTarget,
  formatEvidenceTarget,
  kindLabel,
  nodeFailed,
  parseConversationAnchor,
  stepHaystack,
  toolArgumentsOf,
  toolNameOf,
  toolOutputOf,
} from "../src/lib/conversation.js";

/*
 * These cover the conversation model, not the drawing of it: which nodes become
 * steps, when a call and its result are one card, what counts as a failure, and
 * the anchor grammar the audit findings are written in.
 */

function node(overrides) {
  return { depth: 0, provider_type: "openai", index: 0, ...overrides };
}

// The flattened shape the observation endpoint returns: depth 0 for the items
// of the conversation, depth 1 for the parts inside them.
function parsedTrace() {
  return [
    node({ id: "n_instructions", normalized_type: "instruction", role: "system", path: "$.instructions", index: 0, text_preview: "You are a careful agent." }),
    node({ id: "n_in_0", normalized_type: "message", role: "user", path: "$.input[0]", index: 0, text_preview: "list the files" }),
    node({ id: "n_in_0_c0", parent_id: "n_in_0", depth: 1, normalized_type: "text", path: "$.input[0].content[0]", index: 0, text_preview: "list the files" }),
    node({
      id: "n_in_1",
      normalized_type: "tool_call",
      role: "assistant",
      path: "$.input[1]",
      index: 1,
      text_preview: "exec_command",
      raw: { name: "exec_command", call_id: "call_1", arguments: '{"cmd":"ls -la"}' },
    }),
    node({ id: "n_in_2", normalized_type: "tool_result", role: "tool", path: "$.input[2]", index: 2, text_preview: "total 12\ndrwxr-xr-x", raw: { call_id: "call_1", status: "ok" } }),
    node({ id: "n_out_0", normalized_type: "reasoning", path: "$.output[0]", index: 0, text_preview: "They asked for a listing." }),
    node({ id: "n_out_1", normalized_type: "message", role: "assistant", path: "$.output[1]", index: 1, text_preview: "Here is the listing." }),
    node({ id: "n_out_2", normalized_type: "unknown", path: "$.output[2]", index: 2, text_preview: "" }),
  ];
}

describe("buildConversationSteps", () => {
  it("turns the flat node list into steps in payload order", () => {
    const steps = buildConversationSteps(parsedTrace());
    assert.deepEqual(
      steps.map((step) => step.path),
      ["$.instructions", "$.input[0]", "$.input[1]", "$.output[0]", "$.output[1]", "$.output[2]"],
    );
    assert.deepEqual(
      steps.map((step) => step.kind),
      ["instruction", "message", "tool_call", "reasoning", "message", "unknown"],
    );
  });

  it("merges a tool result into the call above it, keeping both anchors", () => {
    const steps = buildConversationSteps(parsedTrace());
    const call = steps.find((step) => step.id === "n_in_1");
    assert.equal(call.results.length, 1);
    assert.equal(call.results[0].id, "n_in_2");
    assert.equal(call.results[0].text, "total 12\ndrwxr-xr-x");
    // The result is not a step of its own any more.
    assert.equal(steps.some((step) => step.id === "n_in_2"), false);
  });

  it("keeps a result that belongs to a different call as its own step", () => {
    const steps = buildConversationSteps([
      node({ id: "a", normalized_type: "tool_call", path: "$.input[0]", raw: { call_id: "call_1", name: "one" } }),
      node({ id: "b", normalized_type: "tool_result", path: "$.input[1]", raw: { call_id: "call_2" } }),
    ]);
    assert.equal(steps.length, 2);
    assert.equal(steps[0].results.length, 0);
  });

  it("pairs a result with no id with the call directly above it", () => {
    const steps = buildConversationSteps([
      node({ id: "a", normalized_type: "tool_call", path: "$.input[0]", raw: { name: "one" } }),
      node({ id: "b", normalized_type: "tool_result", path: "$.input[1]" }),
    ]);
    assert.equal(steps.length, 1);
    assert.equal(steps[0].results[0].id, "b");
  });

  it("merges a code block with its result", () => {
    const steps = buildConversationSteps([
      node({ id: "c", normalized_type: "code", path: "$.output[0]", text_preview: "print(1)" }),
      node({ id: "d", normalized_type: "code_result", path: "$.output[1]", text_preview: "1" }),
    ]);
    assert.equal(steps.length, 1);
    assert.equal(steps[0].results.length, 1);
  });

  it("leaves declarations and usage out of the conversation", () => {
    const steps = buildConversationSteps([
      node({ id: "t", normalized_type: "tool_declaration", path: "$.tools[0]" }),
      node({ id: "u", normalized_type: "usage", path: "$.usage" }),
      node({ id: "m", normalized_type: "message", path: "$.input[0]", text_preview: "hi" }),
    ]);
    assert.deepEqual(
      steps.map((step) => step.id),
      ["m"],
    );
  });

  it("attaches depth-1 parts to the step that owns them", () => {
    const steps = buildConversationSteps(parsedTrace());
    const message = steps.find((step) => step.id === "n_in_0");
    assert.equal(message.parts.length, 1);
    assert.equal(message.parts[0].id, "n_in_0_c0");
    // A part whose parent is missing does not invent a step.
    const orphan = buildConversationSteps([node({ id: "x", depth: 1, parent_id: "gone", path: "$.input[9].content[0]" })]);
    assert.deepEqual(orphan, []);
  });

  it("survives a missing or malformed node list", () => {
    assert.deepEqual(buildConversationSteps(undefined), []);
    assert.deepEqual(buildConversationSteps("nope"), []);
  });
});

describe("step content", () => {
  it("reads a tool call as its name and its arguments", () => {
    const [call] = buildConversationSteps([
      node({ id: "a", normalized_type: "tool_call", path: "$.input[0]", text_preview: "exec_command", raw: { name: "exec_command", arguments: '{"cmd":"ls"}' } }),
    ]);
    assert.equal(toolNameOf(call), "exec_command");
    assert.equal(toolArgumentsOf(call), '{\n  "cmd": "ls"\n}');
    assert.equal(conversationText(call), '{\n  "cmd": "ls"\n}');
  });

  it("leaves non-JSON arguments alone", () => {
    const [call] = buildConversationSteps([node({ id: "a", normalized_type: "tool_call", path: "$.input[0]", raw: { name: "x", arguments: "ls -la" } })]);
    assert.equal(toolArgumentsOf(call), "ls -la");
  });

  // A chat trace records the call inside the assistant message, and the result
  // as the next message. The real parser emits exactly this shape, so the model
  // treats a call that is a child of a step as an action of that step.
  it("merges a result into a call that arrived inside a message", () => {
    const steps = buildConversationSteps([
      node({ id: "m1", normalized_type: "message", role: "user", path: "$.messages[0]", text_preview: "list the files" }),
      node({ id: "m2", normalized_type: "message", role: "assistant", path: "$.messages[1]", raw: { role: "assistant", content: null, tool_calls: [{ id: "call_1" }] } }),
      node({
        id: "m2_call",
        parent_id: "m2",
        depth: 1,
        normalized_type: "tool_call",
        path: "$.messages[1].tool_calls[0]",
        text_preview: "exec_command",
        raw: { id: "call_1", type: "function", function: { name: "exec_command", arguments: '{"cmd":"ls -la"}' } },
      }),
      node({ id: "m3", normalized_type: "tool_result", role: "tool", path: "$.messages[2]", raw: { role: "tool", tool_call_id: "call_1", content: "permission denied" } }),
    ]);

    assert.equal(steps.length, 2);
    const assistant = steps[1];
    assert.equal(assistant.calls.length, 1);
    assert.equal(toolNameOf(assistant.calls[0]), "exec_command");
    assert.equal(assistant.calls[0].results.length, 1);
    assert.equal(toolOutputOf(assistant.calls[0].results[0]).text, "permission denied");
    // The message itself said nothing, so the card shows the call, not raw JSON.
    assert.equal(conversationText(assistant), "");
    const stats = conversationStats(steps);
    assert.equal(stats.toolCalls, 1);
    assert.equal(stats.toolResults, 1);
  });

  it("finds an address carried by a call inside a message", () => {
    const steps = buildConversationSteps([
      node({ id: "m2", normalized_type: "message", role: "assistant", path: "$.messages[1]" }),
      node({ id: "m2_call", parent_id: "m2", depth: 1, normalized_type: "tool_call", path: "$.messages[1].tool_calls[0]", raw: { id: "call_1" } }),
    ]);
    assert.equal(findConversationTarget(steps, "m2_call"), "m2");
    assert.equal(findConversationTarget(steps, "$.messages[1].tool_calls[0]"), "m2");
  });

  // A chat response is `$.choices[0]` > `.message` > `.content`: the text is two
  // levels below the step, and the role is on the message in between.
  it("reads a response nested two levels deep", () => {
    const steps = buildConversationSteps([
      node({ id: "c0", normalized_type: "message", path: "$.choices[0]", raw: { index: 0 } }),
      node({ id: "c0_m", parent_id: "c0", depth: 1, normalized_type: "message", role: "assistant", path: "$.choices[0].message", raw: { role: "assistant" } }),
      node({ id: "c0_t", parent_id: "c0_m", depth: 2, normalized_type: "text", role: "assistant", path: "$.choices[0].message.content", text_preview: "I could not list the files." }),
    ]);
    assert.equal(steps.length, 1);
    assert.equal(steps[0].role, "assistant");
    assert.equal(conversationText(steps[0]), "I could not list the files.");
    assert.equal(findConversationTarget(steps, "c0_t"), "c0");
  });

  it("leaves a result whose call is missing in place", () => {
    const steps = buildConversationSteps([
      node({ id: "in_0", normalized_type: "message", role: "user", path: "$.input[0]", text_preview: "hi" }),
      node({ id: "res", normalized_type: "tool_result", role: "tool", path: "$.input[1]", raw: { call_id: "call_9", content: "orphan" } }),
    ]);
    assert.equal(steps.length, 2);
    assert.equal(steps[1].kind, "tool_result");
  });

  it("reads the call the way each protocol family writes it", () => {
    // OpenAI chat: nested under `function`.
    const [chat] = buildConversationSteps([
      node({ id: "a", normalized_type: "tool_call", path: "$.messages[1].tool_calls[0]", raw: { id: "call_1", type: "function", function: { name: "exec_command", arguments: '{"cmd":"ls"}' } } }),
    ]);
    assert.equal(toolNameOf(chat), "exec_command");
    assert.equal(toolArgumentsOf(chat), '{\n  "cmd": "ls"\n}');

    // Gemini: nested under `functionCall`, which spells its arguments `args`.
    const [gemini] = buildConversationSteps([
      node({ id: "b", normalized_type: "tool_call", path: "$.contents[0].parts[0]", raw: { functionCall: { name: "read_file", args: { path: "/etc/shadow" } } } }),
    ]);
    assert.equal(toolNameOf(gemini), "read_file");
    assert.equal(toolArgumentsOf(gemini), '{\n  "path": "/etc/shadow"\n}');

    // Anthropic: top level, with its own id field.
    const [anthropic] = buildConversationSteps([
      node({ id: "c", normalized_type: "tool_call", path: "$.content[0]", raw: { type: "tool_use", id: "toolu_1", name: "exec_command", input: { cmd: "ls" } } }),
    ]);
    assert.equal(toolNameOf(anthropic), "exec_command");
    assert.equal(toolArgumentsOf(anthropic), '{\n  "cmd": "ls"\n}');
  });

  it("pairs a Gemini function response with the call it answers", () => {
    const steps = buildConversationSteps([
      node({ id: "a", normalized_type: "tool_call", path: "$.contents[0].parts[0]", raw: { functionCall: { name: "read_file", args: {} } } }),
      node({ id: "b", normalized_type: "tool_result", path: "$.contents[1].parts[0]", raw: { functionResponse: { name: "read_file", response: { error: "permission denied" } } } }),
    ]);
    assert.equal(steps.length, 1);
    assert.equal(toolOutputOf(steps[0].results[0]).text, '{\n  "error": "permission denied"\n}');
  });

  it("falls back to the raw output when a result has no text", () => {
    const [call] = buildConversationSteps([
      node({ id: "a", normalized_type: "tool_call", path: "$.input[0]", raw: { name: "x" } }),
      node({ id: "b", normalized_type: "tool_result", path: "$.input[1]", raw: { output: { rows: 3 } } }),
    ]);
    assert.equal(toolOutputOf(call.results[0]).text, '{\n  "rows": 3\n}');
    assert.equal(toolOutputOf(call.results[0]).structured, true);
  });

  it("keeps one block per distinct sentence", () => {
    const steps = buildConversationSteps(parsedTrace());
    const message = steps.find((step) => step.id === "n_in_0");
    // The part repeats the message text, so it is an address, not a second block.
    assert.deepEqual(distinctParts(message), []);
    const reasoning = steps.find((step) => step.id === "n_out_0");
    assert.deepEqual(distinctParts(reasoning), []);
  });

  it("searches across the step's own text, its parts and its results", () => {
    const steps = buildConversationSteps(parsedTrace());
    const call = steps.find((step) => step.id === "n_in_1");
    assert.match(stepHaystack(call), /exec_command/);
    assert.match(stepHaystack(call), /ls -la/);
    assert.match(stepHaystack(call), /total 12/);
    assert.match(stepHaystack(call), /\$\.input\[1\]/);
  });
});

describe("nodeFailed", () => {
  it("reads the flag, the exit code and the status", () => {
    assert.equal(nodeFailed(node({ raw: { is_error: true } })), true);
    assert.equal(nodeFailed(node({ raw: { exit_code: 2 } })), true);
    assert.equal(nodeFailed(node({ raw: { exit_code: 0 } })), false);
    assert.equal(nodeFailed(node({ raw: { status: "cancelled" } })), true);
    assert.equal(nodeFailed(node({ status: "error" })), true);
  });

  it("does not guess from the text", () => {
    assert.equal(nodeFailed(node({ text_preview: "Error: no such file", raw: { status: "ok" } })), false);
    assert.equal(nodeFailed(node({})), false);
  });
});

describe("conversationStats", () => {
  it("counts what the summary row shows", () => {
    const stats = conversationStats(buildConversationSteps(parsedTrace()));
    assert.equal(stats.steps, 6);
    assert.equal(stats.toolCalls, 1);
    assert.equal(stats.toolResults, 1);
    assert.equal(stats.reasoning, 1);
    assert.equal(stats.failed, 0);
  });

  it("counts a failed result against its call", () => {
    const steps = buildConversationSteps([
      node({ id: "a", normalized_type: "tool_call", path: "$.input[0]", raw: { name: "x", call_id: "c1" } }),
      node({ id: "b", normalized_type: "tool_result", path: "$.input[1]", raw: { call_id: "c1", status: "failed" }, text_preview: "boom" }),
    ]);
    assert.equal(conversationStats(steps).failed, 1);
  });
});

describe("anchors", () => {
  it("reads every form a finding can store", () => {
    assert.deepEqual(parseConversationAnchor("node_1bf9bdb9db5e30ad"), { nodeID: "node_1bf9bdb9db5e30ad", path: "" });
    assert.deepEqual(parseConversationAnchor("$.input[159]"), { nodeID: "", path: "$.input[159]" });
    assert.deepEqual(parseConversationAnchor("trace#ba27#node#node_1b#path#$.input[159]"), { nodeID: "node_1b", path: "$.input[159]" });
    assert.deepEqual(parseConversationAnchor("trace#ba27#path#$.input[159]"), { nodeID: "", path: "$.input[159]" });
    assert.deepEqual(parseConversationAnchor("trace#ba27"), { nodeID: "", path: "" });
    assert.deepEqual(parseConversationAnchor(""), { nodeID: "", path: "" });
    // A path containing a hash of its own keeps all of it.
    assert.deepEqual(parseConversationAnchor("trace#t#node#n#path#$.input[0].x#y"), { nodeID: "n", path: "$.input[0].x#y" });
  });

  it("shortens a target to what a human reads", () => {
    assert.equal(formatEvidenceTarget("trace#ba27#node#node_1b#path#$.input[159]"), "$.input[159] · #160");
    assert.equal(formatEvidenceTarget("trace#ba27#node#node_1b"), "node_1b");
    assert.equal(formatEvidenceTarget(""), "");
  });

  it("resolves a target to the step that holds it", () => {
    const steps = buildConversationSteps(parsedTrace());
    assert.equal(findConversationTarget(steps, "n_in_0"), "n_in_0");
    assert.equal(findConversationTarget(steps, "$.input[0]"), "n_in_0");
    assert.equal(findConversationTarget(steps, "trace#t#node#n_in_0#path#$.input[0]"), "n_in_0");
    // A part or a merged result resolves to the step that renders it.
    assert.equal(findConversationTarget(steps, "n_in_0_c0"), "n_in_0");
    assert.equal(findConversationTarget(steps, "n_in_2"), "n_in_1");
    assert.equal(findConversationTarget(steps, "$.nowhere"), "");
    assert.equal(findConversationTarget(steps, ""), "");
  });

  it("names a node the way the conversation shows it", () => {
    const t = (key) => (key === "conversation.kind.tool_call" ? "tool call" : key);
    assert.equal(
      describeConversationNode({ normalized_type: "tool_call", path: "$.input[159]", index: 159, role: "assistant", raw: { name: "exec_command" } }, t),
      "$.input[159] · tool call · assistant · exec_command",
    );
    // Without a path the payload position is all there is to name it by.
    assert.equal(describeConversationNode({ normalized_type: "message", index: 2 }, t), "#3 · message");
    // A node the finding named but the observation no longer holds says nothing.
    assert.equal(describeConversationNode(null, t), "");
  });

  it("falls back to the kind's own words for a kind it has no label for", () => {
    assert.equal(kindLabel("brand_new_kind", (key) => key), "brand new kind");
    assert.equal(kindLabel("message", (key) => (key === "conversation.kind.message" ? "message" : key)), "message");
  });
});
