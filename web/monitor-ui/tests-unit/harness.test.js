/*
 * Unit tests for the client-harness presentation helpers.
 *
 * The backend classifies the harness, but the badge label, the visible chips
 * and the "filter by this harness" link are all derived in the browser. These
 * cases pin the fallbacks: a payload with no display name still reads as words,
 * the tags the badge already states are not repeated as chips, and a filter
 * link keeps the reader's other query parameters while resetting the page.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  KNOWN_HARNESS_IDS,
  harnessBadgeLabel,
  harnessConfidenceLabel,
  harnessDisplayName,
  harnessFilterHref,
  harnessFilterSearch,
  harnessKindLabel,
  harnessTagLabel,
  harnessTooltip,
  parseHarnessTag,
  prettifyHarnessName,
  visibleHarnessTags,
} from "../src/lib/harness.js";

function searchOf(href) {
  return new URL(href, "http://monitor.local").searchParams;
}

// A translator that only knows the English names the locale tables carry. It
// proves the helpers prefer a translation and fall back when the table has no
// entry, without importing the whole i18n runtime.
const TRANSLATIONS = {
  "harness.kind.agent-cli": "Agent CLI",
  "harness.kind.ide-extension": "IDE extension",
  "harness.confidence.high": "high confidence",
  "harness.capability.tool-use": "Tool use",
  "harness.sdk.anthropic-typescript": "Anthropic TypeScript SDK",
};
function translate(key) {
  return TRANSLATIONS[key] ?? key;
}

describe("prettifyHarnessName", () => {
  it("turns a canonical id into words", () => {
    assert.equal(prettifyHarnessName("claude-code"), "Claude Code");
    assert.equal(prettifyHarnessName("codex-cli"), "Codex CLI");
    assert.equal(prettifyHarnessName("cursor"), "Cursor");
  });

  it("keeps known brand and acronym spellings", () => {
    assert.equal(prettifyHarnessName("openai-python"), "OpenAI Python");
    assert.equal(prettifyHarnessName("google-genai"), "Google GenAI");
    assert.equal(prettifyHarnessName("sdk"), "SDK");
  });

  it("handles separators and empty input", () => {
    assert.equal(prettifyHarnessName("ide_extension"), "IDE Extension");
    assert.equal(prettifyHarnessName("  "), "");
    assert.equal(prettifyHarnessName(), "");
  });
});

describe("harnessBadgeLabel", () => {
  it("prefers the backend display name and appends the version", () => {
    assert.equal(
      harnessBadgeLabel({ client_harness: "claude-code", client_harness_name: "Claude Code", client_harness_version: "2.0.14" }),
      "Claude Code 2.0.14",
    );
    assert.equal(harnessBadgeLabel({ client_harness: "claude-code", client_harness_name: "Claude Code" }), "Claude Code");
  });

  it("falls back to a prettified id when the name is missing", () => {
    assert.equal(harnessBadgeLabel({ client_harness: "codex-cli", client_harness_version: "0.51.0" }), "Codex CLI 0.51.0");
  });

  it("is empty without a classification", () => {
    assert.equal(harnessBadgeLabel({}), "");
    assert.equal(harnessBadgeLabel({ client_harness_name: "Claude Code" }), "");
    assert.equal(harnessDisplayName({}), "");
  });
});

describe("parseHarnessTag and visibleHarnessTags", () => {
  it("splits a tag on its first colon", () => {
    assert.deepEqual(parseHarnessTag("capability:tool-use"), { raw: "capability:tool-use", prefix: "capability", value: "tool-use" });
    assert.equal(parseHarnessTag("kind:agent-cli").prefix, "kind");
    assert.equal(parseHarnessTag(""), null);
    assert.equal(parseHarnessTag("no-colon"), null);
    assert.equal(parseHarnessTag(":value"), null);
    assert.equal(parseHarnessTag("prefix:"), null);
  });

  it("drops the redundant harness and kind tags, keeping capability and sdk", () => {
    const tags = [
      "capability:tool-use",
      "harness:claude-code",
      "kind:agent-cli",
      "sdk:anthropic-typescript",
      "capability:streaming",
    ];
    assert.deepEqual(visibleHarnessTags(tags), [
      "capability:tool-use",
      "sdk:anthropic-typescript",
      "capability:streaming",
    ]);
  });

  it("deduplicates, ignores junk and tolerates a missing field", () => {
    assert.deepEqual(visibleHarnessTags(["capability:vision", "capability:vision", "kind:sdk"]), ["capability:vision"]);
    assert.deepEqual(visibleHarnessTags(["", null, undefined, "garbage"]), []);
    assert.deepEqual(visibleHarnessTags(undefined), []);
    assert.deepEqual(visibleHarnessTags("capability:vision"), []);
  });
});

describe("labels", () => {
  it("translates known kind, confidence and capability values", () => {
    assert.equal(harnessKindLabel("agent-cli", translate), "Agent CLI");
    assert.equal(harnessConfidenceLabel("high", translate), "high confidence");
    assert.equal(harnessTagLabel("capability:tool-use", translate), "Tool use");
    assert.equal(harnessTagLabel("sdk:anthropic-typescript", translate), "Anthropic TypeScript SDK");
  });

  it("falls back to the raw value when the table has no entry", () => {
    assert.equal(harnessKindLabel("future-kind", translate), "future-kind");
    assert.equal(harnessConfidenceLabel("certain", translate), "certain");
    assert.equal(harnessTagLabel("capability:nano-banana", translate), "nano-banana");
    assert.equal(harnessTagLabel("kind:agent-cli", translate), "agent-cli");
    assert.equal(harnessTagLabel("garbage", translate), "");
  });

  it("renders the raw value when no translator is supplied", () => {
    assert.equal(harnessKindLabel("agent-cli"), "agent-cli");
    assert.equal(harnessTagLabel("capability:tool-use"), "tool-use");
  });
});

describe("harnessTooltip", () => {
  it("lists id, kind, confidence and the visible tags", () => {
    const tooltip = harnessTooltip(
      {
        client_harness: "claude-code",
        client_harness_kind: "agent-cli",
        client_harness_confidence: "high",
        client_harness_tags: ["harness:claude-code", "kind:agent-cli", "capability:tool-use", "sdk:anthropic-typescript"],
      },
      translate,
    );
    assert.equal(tooltip, "claude-code · Agent CLI · high confidence · Tool use · Anthropic TypeScript SDK");
  });

  it("skips absent parts and is empty without a harness", () => {
    assert.equal(harnessTooltip({ client_harness: "cursor" }), "cursor");
    assert.equal(harnessTooltip({}), "");
  });
});

describe("harnessFilterSearch / harnessFilterHref", () => {
  it("sets the harness and preserves the other query parameters", () => {
    const params = searchOf(harnessFilterHref("/traces", "?q=gpt&provider=openai&model=gpt-4&window=7d&page=6", "claude-code"));
    assert.equal(params.get("harness"), "claude-code");
    assert.equal(params.get("q"), "gpt");
    assert.equal(params.get("provider"), "openai");
    assert.equal(params.get("model"), "gpt-4");
    assert.equal(params.get("window"), "7d");
    assert.equal(params.get("page"), "1");
  });

  it("resets the page even when the search has no other parameters", () => {
    assert.equal(harnessFilterSearch("", "codex-cli"), "harness=codex-cli&page=1");
    assert.equal(harnessFilterSearch("?tab=requests&page=12", "codex-cli"), "tab=requests&page=1&harness=codex-cli");
  });

  it("replaces an existing harness instead of appending one", () => {
    const params = searchOf(harnessFilterHref("/traces", "?harness=codex-cli&page=3", "claude-code"));
    assert.deepEqual(params.getAll("harness"), ["claude-code"]);
    assert.equal(params.get("page"), "1");
  });

  it("drops the parameter when asked to clear it and tolerates a leading question mark", () => {
    const params = searchOf(harnessFilterHref("/traces", "harness=claude-code&page=2", ""));
    assert.equal(params.get("harness"), null);
    assert.equal(params.get("page"), "1");
  });

  it("builds an absolute path for the traffic page", () => {
    assert.match(harnessFilterHref("/traces", "?window=30d", "claude-code"), /^\/traces\?/);
    assert.match(harnessFilterHref("", "", "claude-code"), /^\/traces\?/);
  });
});

describe("KNOWN_HARNESS_IDS", () => {
  it("is the filter vocabulary: unique, non-empty canonical ids", () => {
    assert.ok(KNOWN_HARNESS_IDS.length >= 20);
    assert.equal(new Set(KNOWN_HARNESS_IDS).size, KNOWN_HARNESS_IDS.length);
    for (const id of KNOWN_HARNESS_IDS) {
      assert.match(id, /^[a-z0-9-]+$/);
    }
  });

  it("covers the agent CLIs a reader filters by most", () => {
    for (const id of ["claude-code", "codex-cli", "deepseek-harness", "factory-droid", "kimi-code", "pi", "cursor"]) {
      assert.ok(KNOWN_HARNESS_IDS.includes(id), `missing ${id}`);
    }
  });
});
