import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  buildPathDocument,
  childJSONPath,
  describeJSONValue,
  joinJSONPath,
  jsonKindLabel,
  parentJSONPath,
  parseNodePayload,
  previewJSONValue,
  rowTextPreviews,
  splitJSONPath,
} from "../src/lib/jsonTree.js";

/*
 * The protocol tab rebuilds the parsed payload from the semantic nodes: each
 * node carries the JSON path it was read from and the raw value there, and the
 * tree is what those values look like once they are put back in place. The
 * helpers below are what decides where a value lands, which is also what decides
 * whether the parser's name for a node shows up beside its value - a path that
 * does not match leaves the row unlabelled.
 */
describe("splitJSONPath", () => {
  it("reads the dot-and-bracket paths the parsers emit", () => {
    assert.deepEqual(splitJSONPath("$.messages[0].content"), ["messages", 0, "content"]);
    assert.deepEqual(splitJSONPath("$"), []);
    assert.deepEqual(splitJSONPath("$.stream.content[3].text"), ["stream", "content", 3, "text"]);
  });

  it("refuses a path it cannot place rather than guessing at one", () => {
    assert.equal(splitJSONPath("messages[0]"), null);
    assert.equal(splitJSONPath("$[0"), null);
    assert.equal(splitJSONPath("$['a']"), null);
    assert.equal(splitJSONPath(""), null);
  });
});

describe("childJSONPath", () => {
  it("keeps array indices in the bracket form the parsers write", () => {
    // `Object.entries` hands an array's indices back as strings, and `$.input.0`
    // matches no node - which is how a row loses its tag.
    assert.equal(childJSONPath("$.input", [], "0"), "$.input[0]");
    assert.equal(childJSONPath("$.input", [], "12"), "$.input[12]");
    assert.equal(childJSONPath("$.input[0]", {}, "role"), "$.input[0].role");
    assert.equal(joinJSONPath("$", "messages"), "$.messages");
  });
});

describe("parseNodePayload", () => {
  it("decodes the JSON text the API sends and keeps a decoded value as it is", () => {
    assert.deepEqual(parseNodePayload('{"cmd":"ls"}'), { ok: true, value: { cmd: "ls" } });
    assert.deepEqual(parseNodePayload('"permission denied"'), { ok: true, value: "permission denied" });
    assert.deepEqual(parseNodePayload({ cmd: "ls" }), { ok: true, value: { cmd: "ls" } });
  });

  it("keeps a body that is not JSON as the text it is", () => {
    assert.deepEqual(parseNodePayload("<html>502 Bad Gateway</html>"), { ok: false, text: "<html>502 Bad Gateway</html>" });
    assert.deepEqual(parseNodePayload(""), { ok: false, empty: true, text: "" });
    assert.deepEqual(parseNodePayload(null), { ok: false, empty: true, text: "" });
  });
});

describe("buildPathDocument", () => {
  const node = (overrides) => ({ parent_id: "", provider_type: "openai", role: "", raw: null, children: [], ...overrides });

  it("puts each node's value back at its path", () => {
    const { document, nodesByPath } = buildPathDocument([
      node({ id: "a", path: "$.input[0]", normalized_type: "message", role: "system", raw: { role: "system", content: "hi" } }),
      node({ id: "b", path: "$.input[1]", normalized_type: "message", role: "user", raw: { role: "user", content: "there" } }),
      node({ id: "c", path: "$.tools[0]", normalized_type: "tool_declaration", raw: { name: "exec_command" } }),
    ]);
    assert.deepEqual(document, {
      input: [
        { role: "system", content: "hi" },
        { role: "user", content: "there" },
      ],
      tools: [{ name: "exec_command" }],
    });
    // The path a row is drawn at is the key the node is looked up by, which is
    // what puts the parser's name on the row.
    assert.equal(nodesByPath.get("$.input[0]").normalized_type, "message");
    assert.equal(nodesByPath.get("$.tools[0]").id, "c");
  });

  it("lets a nested node refine the parent's value without duplicating it", () => {
    const { document } = buildPathDocument([
      node({
        id: "a",
        path: "$.input[0]",
        raw: { role: "assistant", content: null, tool_calls: [{ id: "call_1", function: { name: "exec_command" } }] },
        children: [node({ id: "b", path: "$.input[0].tool_calls[0]", normalized_type: "tool_call", raw: { id: "call_1", function: { name: "exec_command" } } })],
      }),
    ]);
    assert.deepEqual(document.input[0].tool_calls, [{ id: "call_1", function: { name: "exec_command" } }]);
  });

  it("keeps a payload that was never JSON as text", () => {
    const { document } = buildPathDocument([node({ id: "a", path: "$.error", normalized_type: "error", raw: "rate limited" })]);
    assert.deepEqual(document, { error: "rate limited" });
  });

  it("collects a node whose path is not a JSON path instead of dropping it", () => {
    const { document, unplaced } = buildPathDocument([node({ id: "root", path: "$", raw: { model: "gpt-5" } })]);
    // The entry parser's root is the whole body: its keys are the document.
    assert.deepEqual(document, { model: "gpt-5" });
    assert.equal(unplaced.length, 0);

    const detached = buildPathDocument([node({ id: "x", path: "not a path", raw: { a: 1 } })]);
    assert.equal(detached.unplaced.length, 1);
    assert.equal(detached.unplaced[0].node.id, "x");
  });
});

describe("parentJSONPath", () => {
  it("names the value that contains this one", () => {
    assert.equal(parentJSONPath("$.input[0].content[1]"), "$.input[0].content");
    assert.equal(parentJSONPath("$.messages[2]"), "$.messages");
    assert.equal(parentJSONPath("$.instructions"), "$");
    assert.equal(parentJSONPath("$"), "");
  });
});

describe("rowTextPreviews", () => {
  const node = (overrides) => ({ provider_type: "openai", role: "", text_preview: "", ...overrides });

  it("shows the parser's sentence for a value, not its shape", () => {
    const nodesByPath = new Map([["$.input[0]", node({ id: "a", text_preview: "list the files" })]]);
    assert.equal(rowTextPreviews(nodesByPath).get("$.input[0]"), "list the files");
  });

  it("leaves the sentence to the message rather than repeating it in its content", () => {
    // The text child of a message carries the same sentence; two rows saying it
    // is one row saying it twice.
    const nodesByPath = new Map([
      ["$.input[0]", node({ id: "a", text_preview: "list the files" })],
      ["$.input[0].content", node({ id: "b", text_preview: "list the files" })],
      ["$.input[1]", node({ id: "c", text_preview: "and these" })],
    ]);
    const previews = rowTextPreviews(nodesByPath);
    assert.equal(previews.get("$.input[0]"), "list the files");
    assert.equal(previews.get("$.input[0].content"), "");
    assert.equal(previews.get("$.input[1]"), "and these");
  });

  it("carries the text through the tree the rows are drawn from", () => {
    const { rowPreviews } = buildPathDocument([
      node({ id: "a", path: "$.input[0]", raw: { role: "user", content: "hi" }, text_preview: "hi", children: [node({ id: "b", path: "$.input[0].content", raw: "hi", text_preview: "hi" })] }),
    ]);
    assert.equal(rowPreviews.get("$.input[0]"), "hi");
    assert.equal(rowPreviews.get("$.input[0].content"), "");
  });
});

describe("describeJSONValue", () => {
  it("prefers the parser's vocabulary and never prints a bare unknown", () => {
    assert.equal(describeJSONValue({ normalized_type: "tool_call", provider_type: "function" }, {}).label, "tool_call");
    assert.equal(describeJSONValue({ normalized_type: "unknown", provider_type: "future_item" }, {}).label, "future_item");
    assert.equal(describeJSONValue({ normalized_type: "unknown", provider_type: "unknown" }, { role: "system", content: "hi" }).label, "message");
    assert.equal(describeJSONValue({}, [1, 2]).label, "array");
    assert.equal(describeJSONValue({}, "text").label, "string");
    assert.equal(describeJSONValue({}, 3).label, "number");
    assert.equal(describeJSONValue({}, null).label, "null");
    assert.equal(describeJSONValue(null, { a: 1 }).label, "object");
    assert.equal(jsonKindLabel(true), "boolean");
  });

  it("says how much a collapsed container holds", () => {
    assert.equal(previewJSONValue([1, 2, 3]), "3 items");
    assert.equal(previewJSONValue([1]), "1 item");
    assert.equal(previewJSONValue({ a: 1 }), "1 key");
    assert.equal(previewJSONValue("  two\nlines "), "two lines");
    assert.equal(previewJSONValue(null), "null");
    assert.equal(previewJSONValue("x".repeat(200)).endsWith("…"), true);
  });
});
