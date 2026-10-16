import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  collectToolOutputs,
  findDeclaredToolForCall,
  formatJSONValue,
  normalizeDeclaredTool,
} from "../src/lib/traceTools.js";

/*
 * The declared-tool helpers read payloads that arrive in two shapes. The
 * recorder keeps the JSON text the client sent; the parsed observation hands
 * back the decoded object. Both reach the same `<pre>`, so a surface that only
 * handles one of them prints `[object Object]` or throws a React child error,
 * and the 工具调用 tab's definition dialog did exactly that.
 */
describe("formatJSONValue", () => {
  it("pretty-prints a decoded object", () => {
    assert.equal(formatJSONValue({ type: "object", properties: { cmd: { type: "string" } } }), '{\n  "type": "object",\n  "properties": {\n    "cmd": {\n      "type": "string"\n    }\n  }\n}');
  });

  it("pretty-prints JSON text", () => {
    assert.equal(formatJSONValue('{"cmd":"ls -la"}'), '{\n  "cmd": "ls -la"\n}');
  });

  it("keeps text that is not JSON as it was", () => {
    assert.equal(formatJSONValue("not json at all"), "not json at all");
  });

  it("falls back for an empty value", () => {
    assert.equal(formatJSONValue(""), "{}");
    assert.equal(formatJSONValue(null), "{}");
    assert.equal(formatJSONValue(undefined, ""), "");
  });
});

describe("collectToolOutputs", () => {
  it("keys each result by the call it answers", () => {
    const outputs = collectToolOutputs({
      messages: [
        { role: "assistant", content: "" },
        { role: "tool", content: "total 12", message_type: "tool_result", tool_call_id: "call_1" },
        { message_type: "function_call_output", toolCallId: "call_2", content: "ok" },
      ],
    });
    assert.equal(outputs.size, 2);
    assert.equal(outputs.get("call_1").content, "total 12");
    assert.equal(outputs.get("call_2").content, "ok");
  });

  it("keeps the first result for a repeated call id", () => {
    const outputs = collectToolOutputs({
      messages: [
        { role: "tool", tool_call_id: "call_1", content: "first" },
        { role: "tool", tool_call_id: "call_1", content: "second" },
      ],
    });
    assert.equal(outputs.get("call_1").content, "first");
  });

  it("returns nothing for a detail without messages", () => {
    assert.equal(collectToolOutputs(null).size, 0);
    assert.equal(collectToolOutputs({}).size, 0);
  });
});

describe("normalizeDeclaredTool", () => {
  it("reads the OpenAI wrapper", () => {
    const tool = normalizeDeclaredTool({ type: "function", function: { name: "exec", description: "run", parameters: { type: "object" } } });
    assert.equal(tool.name, "exec");
    assert.equal(tool.description, "run");
    assert.equal(tool.source, "openai");
    assert.deepEqual(tool.parameters, { type: "object" });
  });

  it("reads a flat tool and an Anthropic input schema", () => {
    assert.equal(normalizeDeclaredTool({ name: "read", description: "read a file" }).source, "tool");
    assert.equal(normalizeDeclaredTool({ name: "read", input_schema: { type: "object" } }).source, "anthropic");
  });

  it("names a tool that has none so the row still has a heading", () => {
    assert.equal(normalizeDeclaredTool({}, 2).name, "tool_3");
  });

  it("matches a declared tool by name regardless of case", () => {
    const tools = [normalizeDeclaredTool({ function: { name: "Exec_Command" } })];
    assert.equal(findDeclaredToolForCall({ function: { name: "exec_command" } }, tools)?.name, "Exec_Command");
    assert.equal(findDeclaredToolForCall({ function: { name: "other" } }, tools), null);
  });
});
