/*
 * The per-model "quick connectivity test" is a read with a consequence: it asks
 * the backend to send one minimal request per declared protocol to the channel's
 * model endpoint and report what came back. This module is the part of that
 * feature that can be exercised without a browser or a network - the request
 * payload, the status -> tone/marker/label table, the chip projection and the
 * detection of the one actionable suggestion (turn Responses off for this
 * model).
 *
 * The status vocabulary comes from the API and is deliberately wider than
 * "ok/failed": a model that answers chat but 404s `/v1/responses` is neither.
 * Keeping the mapping here means the unit test can pin every value once instead
 * of a browser test having to click through twelve mocked upstream responses.
 */

/**
 * Every result status the endpoint can report. `MODEL_TEST_STATUS_META` must
 * cover all of them; `tests-unit/modelTest.test.js` walks this list.
 */
export const MODEL_TEST_STATUSES = [
  "ok",
  "invalid_response",
  "auth_failed",
  "model_not_found",
  "endpoint_not_supported",
  "invalid_request",
  "rate_limited",
  "timeout",
  "network_error",
  "upstream_5xx",
  "skipped",
  "unsupported_protocol",
];

// `marker` is the non-colour signal the chip draws (`check`, `cross`, `skip`),
// so a reader who cannot tell the green and red fills apart still sees the
// outcome. `tone` is the chip's colour class; it never carries the meaning on
// its own.
const MODEL_TEST_STATUS_META = {
  ok: { tone: "green", marker: "check", labelKey: "channelDetail.testStatusOk" },
  invalid_response: { tone: "danger", marker: "cross", labelKey: "channelDetail.testStatusInvalidResponse" },
  auth_failed: { tone: "danger", marker: "cross", labelKey: "channelDetail.testStatusAuthFailed" },
  model_not_found: { tone: "danger", marker: "cross", labelKey: "channelDetail.testStatusModelNotFound" },
  endpoint_not_supported: { tone: "danger", marker: "cross", labelKey: "channelDetail.testStatusEndpointMissing" },
  invalid_request: { tone: "danger", marker: "cross", labelKey: "channelDetail.testStatusInvalidRequest" },
  rate_limited: { tone: "gold", marker: "cross", labelKey: "channelDetail.testStatusRateLimited" },
  timeout: { tone: "gold", marker: "cross", labelKey: "channelDetail.testStatusTimeout" },
  network_error: { tone: "gold", marker: "cross", labelKey: "channelDetail.testStatusNetworkError" },
  upstream_5xx: { tone: "danger", marker: "cross", labelKey: "channelDetail.testStatusUpstreamError" },
  skipped: { tone: "default", marker: "skip", labelKey: "channelDetail.testStatusSkipped" },
  unsupported_protocol: { tone: "default", marker: "skip", labelKey: "channelDetail.testStatusUnsupportedProtocol" },
};

const FALLBACK_STATUS_META = { tone: "default", marker: "cross", labelKey: null };

const SUMMARY_META = {
  ok: { tone: "green", labelKey: "channelDetail.testSummaryOk" },
  partial: { tone: "gold", labelKey: "channelDetail.testSummaryPartial" },
  failed: { tone: "danger", labelKey: "channelDetail.testSummaryFailed" },
  unsupported: { tone: "default", labelKey: "channelDetail.testSummaryUnsupported" },
};

// Protocol names are wire names, not prose: "Chat Completions" is what the
// provider's own documentation calls it in every language. Unknown additions
// (a future protocol) fall back to the underscored name with a space.
const PROTOCOL_LABELS = {
  chat_completions: "Chat Completions",
  responses: "Responses",
  completions: "Completions",
  messages: "Messages",
  embeddings: "Embeddings",
  models: "Models",
  tokenize: "Tokenize",
};

/**
 * The request body. `protocol: "auto"` lets the backend probe every protocol the
 * channel declares; the UI does not pick one. The API key never travels here -
 * the channel already holds it - and the unit test pins that.
 *
 * @param {{channelID?: string, model?: string, protocol?: string}} input
 * @returns {{channel_id: string, model: string, protocol: string}}
 */
export function buildModelTestPayload({ channelID = "", model = "", protocol = "auto" } = {}) {
  return {
    channel_id: String(channelID || ""),
    model: String(model || ""),
    protocol: protocol || "auto",
  };
}

/**
 * The status -> presentation table, with a defensive fallback so a backend that
 * grows a status before the UI does still renders the raw value rather than
 * nothing.
 *
 * @param {string} status
 * @returns {{tone: string, marker: "check"|"cross"|"skip", labelKey: string|null}}
 */
export function modelTestStatusMeta(status) {
  const key = String(status || "").trim().toLowerCase();
  return MODEL_TEST_STATUS_META[key] || FALLBACK_STATUS_META;
}

export function modelTestStatusTone(status) {
  return modelTestStatusMeta(status).tone;
}

export function modelTestStatusMarker(status) {
  return modelTestStatusMeta(status).marker;
}

export function modelTestStatusLabelKey(status) {
  return modelTestStatusMeta(status).labelKey;
}

/**
 * A protocol's display name. `protocolLabel("chat_completions")` is the string
 * the chip prints after its marker.
 */
export function protocolLabel(protocol) {
  const key = String(protocol || "").trim().toLowerCase();
  if (PROTOCOL_LABELS[key]) {
    return PROTOCOL_LABELS[key];
  }
  if (!key) {
    return "";
  }
  return key.replaceAll("_", " ");
}

/**
 * Latency text for a chip or the block header. Sub-second probes are the common
 * case and their milliseconds are the number an operator compares, so they stay
 * in ms; a slow one switches to seconds so the chip does not grow an unreadable
 * five digits. The unit itself comes from the dictionary (`t`), which is why
 * this takes a translator rather than hardcoding "ms": the Chinese console says
 * 毫秒. `t` is the only argument beyond the number, so the mapping is still
 * testable without a browser.
 *
 * @param {(key: string, values?: Record<string, any>) => string} t
 * @param {unknown} value
 */
export function testLatencyText(t, value) {
  // `Number(null)` and `Number("")` are both 0, which would print "0 ms" for a
  // latency the payload does not carry at all.
  if (value === null || value === undefined || value === "") {
    return "";
  }
  const ms = Number(value);
  if (!Number.isFinite(ms) || ms < 0) {
    return "";
  }
  if (ms < 1000) {
    return t("channelDetail.testLatency", { ms: Math.round(ms) });
  }
  return t("channelDetail.testLatencySeconds", { seconds: (ms / 1000).toFixed(1) });
}

function chipTitle(entry) {
  return [entry.method, entry.path, entry.error, entry.hint]
    .filter((part) => part !== "" && part !== undefined && part !== null)
    .join(" · ");
}

/**
 * One entry per probed protocol, already reduced to what the chip renders. The
 * component owns the translation call, so this stays usable under `node --test`.
 *
 * @param {any} result the POST /api/provider-model-test 200 payload
 */
export function modelTestChips(result = {}) {
  const entries = Array.isArray(result?.results) ? result.results : [];
  return entries.map((entry) => {
    const status = String(entry?.status || "");
    const meta = modelTestStatusMeta(status);
    const rawLatency = entry?.latency_ms === null || entry?.latency_ms === undefined ? NaN : Number(entry.latency_ms);
    return {
      protocol: String(entry?.protocol || ""),
      label: protocolLabel(entry?.protocol),
      status,
      tone: meta.tone,
      marker: meta.marker,
      labelKey: meta.labelKey,
      latencyMs: Number.isFinite(rawLatency) && rawLatency >= 0 ? rawLatency : null,
      httpStatus: entry?.http_status ?? null,
      error: entry?.error || "",
      hint: entry?.hint || "",
      title: chipTitle(entry || {}),
    };
  });
}

/**
 * The block header: does the model work at all, and over which protocols. The
 * counts drive the summary line; the chips drive the detail.
 *
 * @param {any} result
 */
export function summarizeModelTest(result = {}) {
  const chips = modelTestChips(result);
  const ok = chips.filter((chip) => chip.marker === "check").length;
  const skipped = chips.filter((chip) => chip.marker === "skip").length;
  const status = String(result?.status || "");
  const summary = SUMMARY_META[status] || { tone: "default", labelKey: null };
  return {
    status,
    tone: summary.tone,
    summaryLabelKey: summary.labelKey,
    chips,
    total: chips.length,
    ok,
    failed: chips.length - ok - skipped,
    skipped,
    durationMs: Number.isFinite(Number(result?.duration_ms)) ? Number(result.duration_ms) : null,
  };
}

/**
 * The only suggestion the UI can act on, and only for the incident shape: chat
 * answers, `/v1/responses` does not, so the channel's declared Responses support
 * is wrong for this model. `null` when nothing is actionable.
 *
 * @param {any} result
 * @returns {{kind: string, capability: string, scope: string, message?: string}|null}
 */
export function disableResponsesSuggestion(result = {}) {
  const suggestions = Array.isArray(result?.suggestions) ? result.suggestions : [];
  return suggestions.find(
    (suggestion) =>
      suggestion?.kind === "disable_capability" &&
      suggestion?.capability === "responses" &&
      suggestion?.scope === "model",
  ) || null;
}

/** The PATCH body for the suggestion action. */
export function disableResponsesPayload() {
  return { supports_responses: false };
}

/**
 * The local echo of a successful "disable Responses" write. The probe that
 * produced the result cannot be re-run for free, so the result keeps the
 * observation but drops the action: the suggestion is applied, the declared map
 * no longer claims Responses, and the block says so.
 *
 * @param {any} result
 */
export function applyResponsesDisabled(result = {}) {
  const declared = { ...(result?.declared || {}), responses: false };
  const suggestions = Array.isArray(result?.suggestions)
    ? result.suggestions.filter(
        (suggestion) =>
          !(
            suggestion?.kind === "disable_capability" &&
            suggestion?.capability === "responses" &&
            suggestion?.scope === "model"
          ),
      )
    : [];
  return { ...result, declared, suggestions, responses_disabled: true };
}
