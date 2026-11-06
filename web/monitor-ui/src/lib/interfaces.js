/*
 * Interface auto-detection is the replacement for the channel form's manual
 * capability pickers.
 *
 * A channel no longer asks the operator to tick "Responses / Chat / Tools /
 * Models / Embeddings / Tokenize" for the whole provider. Each model row instead
 * carries a computed `interfaces` object, filled by probing that model's own
 * protocol surfaces:
 *
 *   chat_completions    POST /v1/chat/completions answered
 *   native_responses    POST /v1/responses served by the provider itself
 *   responses_via_chat  /v1/responses only reachable through the local runtime
 *   messages            POST /v1/messages answered (Anthropic surface)
 *   generate_content    Gemini generateContent answered
 *
 * This module is the pure projection: which interfaces a row has, how the
 * channel-level union is computed from its enabled models, and how a
 * `POST /api/provider-model-detect` result is summarised. Rendering and the
 * translation call stay in the components, so the table can be unit-tested.
 */
import { modelTestStatusMeta, protocolLabel } from "./modelTest.js";

/** Every interface key the backend can report, in display order. */
export const INTERFACE_KEYS = [
  "chat_completions",
  "native_responses",
  "responses_via_chat",
  "messages",
  "generate_content",
];

const INTERFACE_META = {
  chat_completions: { labelKey: "channels.interfaceChat", tone: "accent" },
  native_responses: { labelKey: "channels.interfaceNativeResponses", tone: "green" },
  responses_via_chat: { labelKey: "channels.interfaceResponsesLocal", tone: "gold" },
  messages: { labelKey: "channels.interfaceMessages", tone: "accent" },
  generate_content: { labelKey: "channels.interfaceGenerate", tone: "accent" },
};

const UNKNOWN_CHIP = { key: "unknown", active: false, labelKey: "channels.interfaceUnknown", tone: "default" };

/**
 * Every known interface with its active flag. A missing or non-object
 * `interfaces` value yields the same shape, all inactive, so a caller never has
 * to check the payload shape before rendering.
 *
 * @param {any} interfaces the backend `interfaces` object
 */
export function interfaceChips(interfaces) {
  const source = interfaces && typeof interfaces === "object" ? interfaces : {};
  return INTERFACE_KEYS.map((key) => ({
    key,
    active: source[key] === true,
    labelKey: INTERFACE_META[key].labelKey,
    tone: INTERFACE_META[key].tone,
  }));
}

/** Only the interfaces this model actually serves. */
export function activeInterfaceChips(interfaces) {
  return interfaceChips(interfaces).filter((chip) => chip.active);
}

/** The "we do not know yet" chip, used instead of an empty chip row. */
export function unknownInterfaceChip() {
  return { ...UNKNOWN_CHIP };
}

/**
 * What a model card shows: its active interfaces, or the unknown chip. The
 * unknown chip is deliberate - an empty row reads as "works over nothing",
 * while "interface not detected" reads as "press detect".
 */
export function interfaceChipsOrUnknown(interfaces) {
  const active = activeInterfaceChips(interfaces);
  return active.length ? active : [unknownInterfaceChip()];
}

/**
 * The channel header's summary: the union of the interfaces of its enabled
 * models, because a channel is routable to a protocol when at least one enabled
 * model serves it.
 *
 * @param {any[]} models channel models (`models_usage` rows)
 */
export function channelInterfaceUnion(models = []) {
  const enabled = (Array.isArray(models) ? models : []).filter((model) => model?.enabled === true);
  if (!enabled.length) {
    return [];
  }
  const union = {};
  for (const key of INTERFACE_KEYS) {
    union[key] = enabled.some((model) => model?.interfaces && typeof model.interfaces === "object" && model.interfaces[key] === true);
  }
  const active = activeInterfaceChips(union);
  return active.length ? active : [unknownInterfaceChip()];
}

/**
 * The request body for `POST /api/provider-model-detect`.
 *
 * With a model, it probes that model (`overwrite: true` writes every detected
 * interface, `false` only fills unset ones and is what an enable-toggle uses).
 * Without a model, the backend probes every enabled model of the channel; there
 * the payload is just the channel id.
 *
 * The API key never travels here: the channel already stores it.
 *
 * @param {{channelID?: string, model?: string, overwrite?: boolean}} input
 * @returns {{channel_id: string, model?: string, overwrite: boolean}}
 */
export function buildModelDetectPayload({ channelID = "", model = "", overwrite = false } = {}) {
  const payload = { channel_id: String(channelID || ""), overwrite: Boolean(overwrite) };
  if (model) {
    payload.model = String(model);
  }
  return payload;
}

/**
 * One chip per protocol the detector actually tried, reusing the connectivity
 * test's status vocabulary (`ok`, `endpoint_not_supported`, `skipped`, ...).
 */
export function detectProtocolChips(item = {}) {
  const protocols = Array.isArray(item?.protocols) ? item.protocols : [];
  return protocols.map((entry) => {
    const status = String(entry?.status || "");
    const meta = modelTestStatusMeta(status);
    return {
      protocol: String(entry?.protocol || ""),
      label: protocolLabel(entry?.protocol),
      status,
      tone: meta.tone,
      marker: meta.marker,
      labelKey: meta.labelKey,
      httpStatus: entry?.http_status ?? null,
      latencyMs: entry?.latency_ms === null || entry?.latency_ms === undefined ? null : Number(entry.latency_ms),
    };
  });
}

/** The `supports_*` columns the detector wrote for this model row. */
export function detectAppliedFields(item = {}) {
  const applied = item?.applied && typeof item.applied === "object" ? item.applied : {};
  const fields = [];
  if (applied.supports_chat_completions === true) {
    fields.push("chat_completions");
  }
  if (applied.supports_responses === true) {
    fields.push("responses");
  }
  return fields;
}

/** Human label keys for `detectAppliedFields`, kept next to the interface map. */
export function detectAppliedLabels(item = {}) {
  return detectAppliedFields(item).map((field) =>
    field === "chat_completions" ? INTERFACE_META.chat_completions.labelKey : INTERFACE_META.native_responses.labelKey,
  );
}

/**
 * The block header over a detect response: how many rows changed, how many were
 * skipped and why, plus the duration. `overwrite` is echoed so the inline result
 * can say whether it replaced prior interfaces or only filled gaps.
 *
 * @param {any} result the `POST /api/provider-model-detect` 200 payload
 */
export function summarizeDetectResult(result = {}) {
  const items = Array.isArray(result?.items) ? result.items : [];
  const appliedCount = items.reduce((count, item) => count + detectAppliedFields(item).length, 0);
  const changedItems = items.filter((item) => detectAppliedFields(item).length > 0).length;
  const skippedItems = items.filter((item) => Boolean(item?.skipped_reason)).length;
  return {
    items,
    total: items.length,
    appliedCount,
    changedItems,
    skippedItems,
    overwrite: Boolean(result?.overwrite),
    durationMs: Number.isFinite(Number(result?.duration_ms)) && result?.duration_ms !== null ? Number(result.duration_ms) : null,
    warnings: Array.isArray(result?.warnings) ? result.warnings : [],
  };
}
