/*
 * Unit tests for the model-catalogue presentation helpers.
 *
 * The square and the detail page share these so the same Argus record renders
 * the same way in both places. The interesting rules are the fallbacks: a
 * registry name repeats the provider, a description exists in two languages
 * that are filled in independently, and the context/output limits have to fall
 * back to what the channel declares when the catalogue has no record.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  maxChannelValue,
  modelCapabilityChips,
  modelContextWindow,
  modelDescription,
  modelDisplayName,
  modelMaxOutput,
  safeExternalURL,
} from "../src/lib/modelFacts.js";

const translate = (key) => `t:${key}`;

describe("modelDisplayName", () => {
  it("strips the provider prefix the registry puts in front of the name", () => {
    const facts = { name: "OpenAI: GPT-5", provider: "OpenAI" };
    assert.equal(modelDisplayName({ model: "gpt-5" }, facts), "GPT-5");
  });

  it("keeps a name whose prefix is not the provider", () => {
    const facts = { name: "OpenAI: GPT-5", provider: "Azure" };
    assert.equal(modelDisplayName({ model: "gpt-5" }, facts), "OpenAI: GPT-5");
  });

  it("prefers the configured display name when there is no catalogue record", () => {
    assert.equal(modelDisplayName({ model: "custom", display_name: "My Model" }, null), "My Model");
    assert.equal(modelDisplayName({ model: "custom", display_name: "custom" }, null), "custom");
    assert.equal(modelDisplayName({ model: "custom" }, null), "custom");
  });
});

describe("modelDescription", () => {
  it("selects the description for the active language", () => {
    const facts = { description: "English", description_cn: "中文" };
    assert.equal(modelDescription(facts, "zh-CN"), "中文");
    assert.equal(modelDescription(facts, "en"), "English");
  });

  it("falls back across the two languages and then to the summary", () => {
    assert.equal(modelDescription({ description: "English" }, "zh-CN"), "English");
    assert.equal(modelDescription({ description_cn: "中文" }, "en"), "中文");
    assert.equal(modelDescription({ summary: "Summary" }, "en"), "Summary");
    assert.equal(modelDescription(null, "en"), "");
  });
});

describe("modelCapabilityChips", () => {
  it("hides the bits every chat model has and translates the rest", () => {
    const facts = { capabilities: ["TextIn", "TextOut", "Chat", "FunctionCall", "ImageIn"] };
    assert.deepEqual(modelCapabilityChips(facts, translate), ["t:models.capability.functionCall", "t:models.capability.imageIn"]);
  });

  it("passes an unknown capability through under its raw name", () => {
    assert.deepEqual(modelCapabilityChips({ capabilities: ["Telepathy"] }, translate), ["Telepathy"]);
    assert.deepEqual(modelCapabilityChips(null, translate), []);
  });
});

describe("model limits", () => {
  const channels = [
    { context_window: 128000, max_output_tokens: 16384 },
    { context_window: 200000, max_output_tokens: 8192 },
    {},
  ];

  it("prefers the catalogue limits and otherwise takes the widest channel claim", () => {
    assert.equal(modelContextWindow({ context_length: 400000 }, channels), 400000);
    assert.equal(modelContextWindow(null, channels), 200000);
    assert.equal(modelMaxOutput({ max_output: 128000 }, channels), 128000);
    assert.equal(modelMaxOutput(null, channels), 16384);
    assert.equal(maxChannelValue(channels, "context_window"), 200000);
    assert.equal(maxChannelValue([], "context_window"), 0);
  });
});

describe("safeExternalURL", () => {
  it("keeps only http and https links", () => {
    assert.equal(safeExternalURL("https://example.com/model"), "https://example.com/model");
    assert.equal(safeExternalURL("HTTP://example.com"), "HTTP://example.com");
    assert.equal(safeExternalURL("javascript:alert(1)"), "");
    assert.equal(safeExternalURL("data:text/html,hello"), "");
    assert.equal(safeExternalURL("  "), "");
    assert.equal(safeExternalURL(null), "");
  });
});
