/*
 * The pure half of the per-model connectivity test.
 *
 * The browser test can only reach the statuses its mock happens to return, and
 * the interesting table here is much wider than that: eleven failure states that
 * have to keep their own words and their own tone. These assertions pin the
 * whole table once, plus the payload shape (no credential) and the one
 * actionable suggestion the UI is allowed to offer.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  applyResponsesDisabled,
  buildModelTestPayload,
  disableResponsesPayload,
  disableResponsesSuggestion,
  MODEL_TEST_STATUSES,
  modelTestChips,
  modelTestStatusLabelKey,
  modelTestStatusMarker,
  modelTestStatusMeta,
  modelTestStatusTone,
  protocolLabel,
  summarizeModelTest,
  testLatencyText,
} from "../src/lib/modelTest.js";

// The real dictionaries interpolate `{ms}` / `{seconds}`; a stand-in translator
// keeps the assertion about which key was chosen rather than about i18next.
function fakeTranslate(key, values = {}) {
  if (key === "channelDetail.testLatency") return `${values.ms} ms`;
  if (key === "channelDetail.testLatencySeconds") return `${values.seconds} s`;
  return key;
}

describe("buildModelTestPayload", () => {
  it("sends the channel and model with auto protocol by default", () => {
    assert.deepEqual(buildModelTestPayload({ channelID: "openai-primary", model: "gpt-5" }), {
      channel_id: "openai-primary",
      model: "gpt-5",
      protocol: "auto",
    });
  });

  it("accepts an explicit protocol", () => {
    assert.equal(buildModelTestPayload({ channelID: "c", model: "m", protocol: "responses" }).protocol, "responses");
  });

  it("normalizes missing values and never carries a credential", () => {
    const payload = buildModelTestPayload({ channelID: undefined, model: null });
    assert.deepEqual(payload, { channel_id: "", model: "", protocol: "auto" });
    assert.deepEqual(Object.keys(payload).sort(), ["channel_id", "model", "protocol"]);
    assert.doesNotMatch(JSON.stringify(payload), /api[_-]?key|authorization/i);
  });
});

describe("model test status table", () => {
  it("covers every status the API can report", () => {
    for (const status of MODEL_TEST_STATUSES) {
      const meta = modelTestStatusMeta(status);
      assert.ok(["green", "gold", "danger", "default"].includes(meta.tone), `${status} has no valid tone`);
      assert.ok(["check", "cross", "skip"].includes(meta.marker), `${status} has no valid marker`);
      assert.ok(meta.labelKey, `${status} has no label key`);
      assert.match(meta.labelKey, /^channelDetail\./);
    }
  });

  it("marks only ok as a check and only skipped/unsupported as skips", () => {
    assert.equal(modelTestStatusMarker("ok"), "check");
    for (const status of MODEL_TEST_STATUSES) {
      if (status === "ok") continue;
      if (status === "skipped" || status === "unsupported_protocol") {
        assert.equal(modelTestStatusMarker(status), "skip", status);
      } else {
        assert.equal(modelTestStatusMarker(status), "cross", status);
      }
    }
  });

  it("uses green for success and danger for hard failures", () => {
    assert.equal(modelTestStatusTone("ok"), "green");
    for (const status of [
      "invalid_response",
      "auth_failed",
      "model_not_found",
      "endpoint_not_supported",
      "invalid_request",
      "upstream_5xx",
    ]) {
      assert.equal(modelTestStatusTone(status), "danger", status);
    }
  });

  it("uses gold for transient failures and default for not-run protocols", () => {
    for (const status of ["rate_limited", "timeout", "network_error"]) {
      assert.equal(modelTestStatusTone(status), "gold", status);
    }
    assert.equal(modelTestStatusTone("skipped"), "default");
    assert.equal(modelTestStatusTone("unsupported_protocol"), "default");
  });

  it("labels the incident status as a missing endpoint", () => {
    assert.equal(modelTestStatusLabelKey("endpoint_not_supported"), "channelDetail.testStatusEndpointMissing");
  });

  it("is case/whitespace insensitive", () => {
    assert.equal(modelTestStatusTone("  OK "), "green");
    assert.equal(modelTestStatusLabelKey("Timeout"), "channelDetail.testStatusTimeout");
  });

  it("falls back to the raw value for a status the UI does not know", () => {
    assert.deepEqual(modelTestStatusMeta("teapot"), { tone: "default", marker: "cross", labelKey: null });
    assert.equal(modelTestStatusMeta("").labelKey, null);
  });
});

describe("protocolLabel", () => {
  it("names the two protocols the incident is about", () => {
    assert.equal(protocolLabel("chat_completions"), "Chat Completions");
    assert.equal(protocolLabel("responses"), "Responses");
  });

  it("falls back to a spaced wire name and an empty string", () => {
    assert.equal(protocolLabel("realtime_websocket"), "realtime websocket");
    assert.equal(protocolLabel(""), "");
    assert.equal(protocolLabel(undefined), "");
  });
});

describe("testLatencyText", () => {
  it("keeps sub-second probes in milliseconds", () => {
    assert.equal(testLatencyText(fakeTranslate, 314), "314 ms");
    assert.equal(testLatencyText(fakeTranslate, 0), "0 ms");
  });

  it("switches to seconds once the probe passes a second", () => {
    assert.equal(testLatencyText(fakeTranslate, 1500), "1.5 s");
  });

  it("renders nothing for a missing or negative latency", () => {
    assert.equal(testLatencyText(fakeTranslate, null), "");
    assert.equal(testLatencyText(fakeTranslate, undefined), "");
    assert.equal(testLatencyText(fakeTranslate, -1), "");
    assert.equal(testLatencyText(fakeTranslate, Number.NaN), "");
  });
});

describe("modelTestChips", () => {
  const result = {
    results: [
      {
        protocol: "chat_completions",
        status: "ok",
        method: "POST",
        path: "/v1/chat/completions",
        http_status: 200,
        latency_ms: 314,
        error: "",
        hint: "",
      },
      {
        protocol: "responses",
        status: "endpoint_not_supported",
        method: "POST",
        path: "/v1/responses",
        http_status: 404,
        latency_ms: 88,
        error: "404 page not found",
        hint: "The channel declares Responses support but the endpoint is not served.",
      },
      { protocol: "embeddings", status: "skipped" },
    ],
  };

  it("projects one chip per protocol with marker, tone and latency", () => {
    const chips = modelTestChips(result);
    assert.equal(chips.length, 3);
    assert.deepEqual(
      chips.map((chip) => [chip.protocol, chip.label, chip.tone, chip.marker]),
      [
        ["chat_completions", "Chat Completions", "green", "check"],
        ["responses", "Responses", "danger", "cross"],
        ["embeddings", "Embeddings", "default", "skip"],
      ],
    );
    assert.equal(chips[0].latencyMs, 314);
    assert.equal(chips[1].latencyMs, 88);
    assert.equal(chips[2].latencyMs, null);
  });

  it("keeps the HTTP status and the diagnostic text for the chip", () => {
    const [, responses] = modelTestChips(result);
    assert.equal(responses.httpStatus, 404);
    assert.match(responses.title, /\/v1\/responses/);
    assert.match(responses.title, /404 page not found/);
  });

  it("tolerates a payload without results or with malformed entries", () => {
    assert.deepEqual(modelTestChips({}), []);
    assert.deepEqual(modelTestChips({ results: "nope" }), []);
    assert.equal(modelTestChips({ results: [null] })[0].status, "");
  });
});

describe("summarizeModelTest", () => {
  const base = {
    duration_ms: 412,
    results: [
      { protocol: "chat_completions", status: "ok", latency_ms: 300 },
      { protocol: "responses", status: "endpoint_not_supported", latency_ms: 12 },
      { protocol: "embeddings", status: "skipped" },
    ],
  };

  it("maps the top-level status to its summary tone and key", () => {
    assert.equal(summarizeModelTest({ ...base, status: "ok" }).tone, "green");
    assert.equal(summarizeModelTest({ ...base, status: "partial" }).tone, "gold");
    assert.equal(summarizeModelTest({ ...base, status: "failed" }).tone, "danger");
    assert.equal(summarizeModelTest({ ...base, status: "unsupported" }).tone, "default");
    assert.equal(summarizeModelTest({ ...base, status: "partial" }).summaryLabelKey, "channelDetail.testSummaryPartial");
  });

  it("counts worked, failed and skipped protocols", () => {
    const summary = summarizeModelTest({ ...base, status: "partial" });
    assert.equal(summary.total, 3);
    assert.equal(summary.ok, 1);
    assert.equal(summary.failed, 1);
    assert.equal(summary.skipped, 1);
    assert.equal(summary.durationMs, 412);
  });

  it("survives an empty or unknown result", () => {
    const summary = summarizeModelTest({});
    assert.deepEqual([summary.total, summary.ok, summary.failed, summary.skipped], [0, 0, 0, 0]);
    assert.equal(summary.summaryLabelKey, null);
    assert.equal(summary.tone, "default");
    assert.equal(summary.durationMs, null);
  });
});

describe("disableResponsesSuggestion", () => {
  const suggestion = {
    kind: "disable_capability",
    capability: "responses",
    scope: "model",
    message: "Responses is not served for this model.",
  };

  it("finds the model-scoped Responses suggestion", () => {
    assert.deepEqual(disableResponsesSuggestion({ suggestions: [{ kind: "other" }, suggestion] }), suggestion);
  });

  it("rejects every similar-looking suggestion that is not actionable", () => {
    assert.equal(disableResponsesSuggestion({ suggestions: [] }), null);
    assert.equal(disableResponsesSuggestion({}), null);
    assert.equal(disableResponsesSuggestion({ suggestions: [{ ...suggestion, scope: "channel" }] }), null);
    assert.equal(disableResponsesSuggestion({ suggestions: [{ ...suggestion, capability: "chat_completions" }] }), null);
    assert.equal(disableResponsesSuggestion({ suggestions: [{ ...suggestion, kind: "enable_capability" }] }), null);
    assert.equal(disableResponsesSuggestion({ suggestions: [null] }), null);
  });

  it("patches only the per-model Responses switch", () => {
    assert.deepEqual(disableResponsesPayload(), { supports_responses: false });
  });

  it("applies the local echo: declared off, action gone, observation kept", () => {
    const result = {
      status: "partial",
      declared: { chat_completions: true, responses: true, embeddings: false },
      results: [{ protocol: "responses", status: "endpoint_not_supported", http_status: 404 }],
      suggestions: [suggestion, { kind: "keep" }],
    };
    const applied = applyResponsesDisabled(result);
    assert.equal(applied.declared.responses, false);
    assert.equal(applied.declared.chat_completions, true);
    assert.equal(applied.responses_disabled, true);
    assert.deepEqual(applied.suggestions, [{ kind: "keep" }]);
    // The observation is history; it is not rewritten.
    assert.deepEqual(applied.results, result.results);
    assert.equal(disableResponsesSuggestion(applied), null);
  });

  it("does not mutate the result it is handed", () => {
    const result = { declared: { responses: true }, suggestions: [suggestion] };
    applyResponsesDisabled(result);
    assert.equal(result.declared.responses, true);
    assert.equal(result.suggestions.length, 1);
    assert.equal(result.responses_disabled, undefined);
  });
});
