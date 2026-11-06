/*
 * The pure half of interface auto-detection.
 *
 * The browser spec can only show the states its mock returns; these assertions
 * pin the projection rules that matter across the whole payload vocabulary:
 * which interfaces a row has, how the channel-level union is computed (enabled
 * models only), how the request body is built (no credential, no model for the
 * detect-all call), and how a detect response is counted.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  activeInterfaceChips,
  buildModelDetectPayload,
  channelInterfaceUnion,
  detectAppliedFields,
  detectAppliedLabels,
  detectProtocolChips,
  INTERFACE_KEYS,
  interfaceChips,
  interfaceChipsOrUnknown,
  summarizeDetectResult,
  unknownInterfaceChip,
} from "../src/lib/interfaces.js";

describe("interfaceChips", () => {
  it("projects every known interface with its active flag", () => {
    const chips = interfaceChips({
      chat_completions: true,
      native_responses: true,
      responses_via_chat: false,
      messages: true,
      generate_content: false,
    });
    assert.deepEqual(chips.map((chip) => chip.key), INTERFACE_KEYS);
    assert.deepEqual(chips.map((chip) => chip.active), [true, true, false, true, false]);
    for (const chip of chips) {
      assert.match(chip.labelKey, /^channels\./);
      assert.ok(["accent", "green", "gold", "default"].includes(chip.tone));
    }
  });

  it("returns the same shape for a missing or malformed interfaces value", () => {
    for (const value of [undefined, null, {}, "nope", [], 0]) {
      const chips = interfaceChips(value);
      assert.equal(chips.length, INTERFACE_KEYS.length);
      assert.equal(chips.filter((chip) => chip.active).length, 0);
    }
  });

  it("treats truthy non-boolean values as inactive", () => {
    const chips = interfaceChips({ chat_completions: "yes", native_responses: 1 });
    assert.equal(chips.filter((chip) => chip.active).length, 0);
  });
});

describe("activeInterfaceChips / interfaceChipsOrUnknown", () => {
  it("keeps only the served interfaces", () => {
    const active = activeInterfaceChips({ chat_completions: true, messages: true, native_responses: false });
    assert.deepEqual(active.map((chip) => chip.key), ["chat_completions", "messages"]);
  });

  it("falls back to the unknown chip instead of an empty row", () => {
    assert.deepEqual(interfaceChipsOrUnknown({}), [unknownInterfaceChip()]);
    assert.deepEqual(interfaceChipsOrUnknown(undefined), [unknownInterfaceChip()]);
    const known = interfaceChipsOrUnknown({ chat_completions: true });
    assert.deepEqual(known.map((chip) => chip.key), ["chat_completions"]);
  });

  it("does not share the unknown chip object between callers", () => {
    const first = unknownInterfaceChip();
    first.key = "mutated";
    assert.equal(unknownInterfaceChip().key, "unknown");
  });
});

describe("channelInterfaceUnion", () => {
  it("unions the interfaces of enabled models only", () => {
    const union = channelInterfaceUnion([
      { enabled: true, interfaces: { chat_completions: true, native_responses: false } },
      { enabled: true, interfaces: { chat_completions: false, native_responses: true } },
      { enabled: false, interfaces: { messages: true } },
    ]);
    assert.deepEqual(union.map((chip) => chip.key), ["chat_completions", "native_responses"]);
  });

  it("does not let a disabled model contribute an interface", () => {
    const union = channelInterfaceUnion([
      { enabled: false, interfaces: { responses_via_chat: true } },
      { enabled: true, interfaces: { chat_completions: true } },
    ]);
    assert.deepEqual(union.map((chip) => chip.key), ["chat_completions"]);
  });

  it("returns no chips when the channel has no enabled model", () => {
    assert.deepEqual(channelInterfaceUnion([]), []);
    assert.deepEqual(channelInterfaceUnion([{ enabled: false, interfaces: { chat_completions: true } }]), []);
    assert.deepEqual(channelInterfaceUnion(undefined), []);
  });

  it("falls back to the unknown chip when nothing is detected yet", () => {
    assert.deepEqual(channelInterfaceUnion([{ enabled: true, interfaces: {} }]), [unknownInterfaceChip()]);
    assert.deepEqual(channelInterfaceUnion([{ enabled: true }]), [unknownInterfaceChip()]);
  });
});

describe("buildModelDetectPayload", () => {
  it("names the channel and model and defaults overwrite to false", () => {
    assert.deepEqual(buildModelDetectPayload({ channelID: "openai-primary", model: "gpt-5" }), {
      channel_id: "openai-primary",
      overwrite: false,
      model: "gpt-5",
    });
  });

  it("carries overwrite=true for the explicit per-model action", () => {
    assert.equal(buildModelDetectPayload({ channelID: "c", model: "m", overwrite: true }).overwrite, true);
  });

  it("omits the model for the channel-wide detect-all call", () => {
    const payload = buildModelDetectPayload({ channelID: "openai-primary" });
    assert.deepEqual(payload, { channel_id: "openai-primary", overwrite: false });
    assert.equal("model" in payload, false);
  });

  it("never carries a credential and normalizes missing values", () => {
    const payload = buildModelDetectPayload({});
    assert.deepEqual(payload, { channel_id: "", overwrite: false });
    assert.deepEqual(Object.keys(payload).sort(), ["channel_id", "overwrite"]);
    assert.doesNotMatch(JSON.stringify(payload), /api[_-]?key|authorization/i);
  });
});

describe("detectProtocolChips", () => {
  it("reuses the connectivity-test status table for protocol outcomes", () => {
    const chips = detectProtocolChips({
      protocols: [
        { protocol: "chat_completions", status: "ok", http_status: 200, latency_ms: 120 },
        { protocol: "responses", status: "endpoint_not_supported", http_status: 404, latency_ms: 14 },
        { protocol: "messages", status: "skipped" },
      ],
    });
    assert.deepEqual(
      chips.map((chip) => [chip.protocol, chip.label, chip.tone, chip.marker]),
      [
        ["chat_completions", "Chat Completions", "green", "check"],
        ["responses", "Responses", "danger", "cross"],
        ["messages", "Messages", "default", "skip"],
      ],
    );
    assert.equal(chips[0].latencyMs, 120);
    assert.equal(chips[2].latencyMs, null);
  });

  it("tolerates a payload without protocols", () => {
    assert.deepEqual(detectProtocolChips({}), []);
    assert.deepEqual(detectProtocolChips({ protocols: "nope" }), []);
    assert.equal(detectProtocolChips({ protocols: [null] })[0].status, "");
  });
});

describe("detectAppliedFields / detectAppliedLabels", () => {
  it("lists only the supports columns that were written", () => {
    assert.deepEqual(detectAppliedFields({ applied: { supports_chat_completions: true, supports_responses: false } }), ["chat_completions"]);
    assert.deepEqual(detectAppliedFields({ applied: { supports_chat_completions: true, supports_responses: true } }), ["chat_completions", "responses"]);
    assert.deepEqual(detectAppliedFields({ applied: { supports_responses: false } }), []);
    assert.deepEqual(detectAppliedFields({}), []);
    assert.deepEqual(detectAppliedFields({ applied: "nope" }), []);
  });

  it("maps the written columns back to the interface chip labels", () => {
    assert.deepEqual(detectAppliedLabels({ applied: { supports_chat_completions: true, supports_responses: true } }), [
      "channels.interfaceChat",
      "channels.interfaceNativeResponses",
    ]);
  });
});

describe("summarizeDetectResult", () => {
  const result = {
    channel_id: "openai-primary",
    overwrite: true,
    duration_ms: 512,
    items: [
      { model: "gpt-5", applied: { supports_chat_completions: true, supports_responses: true }, protocols: [{ protocol: "chat_completions", status: "ok" }] },
      { model: "gpt-4.1", applied: {}, skipped_reason: "no interfaces detected", protocols: [] },
    ],
    warnings: ["one protocol timed out"],
  };

  it("counts written fields, changed rows and skipped rows", () => {
    const summary = summarizeDetectResult(result);
    assert.equal(summary.total, 2);
    assert.equal(summary.appliedCount, 2);
    assert.equal(summary.changedItems, 1);
    assert.equal(summary.skippedItems, 1);
    assert.equal(summary.overwrite, true);
    assert.equal(summary.durationMs, 512);
    assert.deepEqual(summary.warnings, ["one protocol timed out"]);
  });

  it("survives an empty or malformed result", () => {
    const summary = summarizeDetectResult({});
    assert.deepEqual(
      [summary.total, summary.appliedCount, summary.changedItems, summary.skippedItems],
      [0, 0, 0, 0],
    );
    assert.equal(summary.overwrite, false);
    assert.equal(summary.durationMs, null);
    assert.deepEqual(summary.warnings, []);
  });
});
