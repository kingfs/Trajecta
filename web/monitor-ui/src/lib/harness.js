/*
 * Presentation helpers for the client-harness classification.
 *
 * A trace row can say which client harness produced the request: a canonical id
 * (`claude-code`), a display name (`Claude Code`), a version, a product kind, a
 * confidence and a sorted list of stable labels (`harness:<id>`, `kind:<kind>`,
 * `sdk:<sdk>`, `capability:<capability>`). The backend owns that vocabulary;
 * this module only renders it, so it stays free of React and of i18n state and
 * can be unit-tested directly.
 *
 * Nothing here throws on a malformed payload: a trace recorded before the
 * classification existed simply has no `client_harness`, and an unknown tag
 * prefix is shown as its raw suffix rather than dropped.
 */

// Tags that restate what the badge already says. `harness:<id>` is the id in
// the badge's tooltip and `kind:<kind>` is its product class, so rendering them
// again as chips would make the detail header say the same thing three times.
const REDUNDANT_TAG_PREFIXES = new Set(["harness", "kind"]);

// The canonical harness ids the backend can emit, mirrored from
// `internal/harness/catalog.go`. The traffic filter is an exact match on this
// vocabulary, so the input offers it as suggestions; a free-typed value still
// works when the backend learns a new id first. Keep this list in sync when
// a rule is added or renamed.
export const KNOWN_HARNESS_IDS = [
  "qwen-code",
  "claude-code",
  "codex-vscode",
  "codex-app",
  "codex-cli",
  "gemini-code-assist",
  "gemini-cli",
  "opencode",
  "deepseek-harness",
  "factory-droid",
  "kimi-code",
  "pi",
  "cursor",
  "windsurf",
  "github-copilot",
  "continue",
  "cline",
  "roo-code",
  "kilo-code",
  "aider",
  "crush",
  "goose",
  "amp",
  "openhands",
  "zed",
  "litellm",
  "portkey",
  "openrouter",
];

// Product names are brands: an all-lowercase fallback such as "Openai Python"
// reads as a typo, while these words have a spelling people already know.
const WORD_LABELS = new Map([
  ["cli", "CLI"],
  ["sdk", "SDK"],
  ["ide", "IDE"],
  ["api", "API"],
  ["mcp", "MCP"],
  ["ui", "UI"],
  ["llm", "LLM"],
  ["http", "HTTP"],
  ["openai", "OpenAI"],
  ["genai", "GenAI"],
]);

// i18n keys are flat strings: i18next is configured with keySeparator off, so
// "harness.kind.agent-cli" is one literal name. The tables below intentionally
// list the whole emitted vocabulary; an id outside them falls back to the raw
// value so a newer backend does not render a missing-key marker.
const KIND_KEYS = {
  "agent-cli": "harness.kind.agent-cli",
  "ide-extension": "harness.kind.ide-extension",
  "agent-app": "harness.kind.agent-app",
  gateway: "harness.kind.gateway",
  sdk: "harness.kind.sdk",
};

const CONFIDENCE_KEYS = {
  high: "harness.confidence.high",
  medium: "harness.confidence.medium",
  low: "harness.confidence.low",
};

const CAPABILITY_KEYS = {
  streaming: "harness.capability.streaming",
  "tool-use": "harness.capability.tool-use",
  reasoning: "harness.capability.reasoning",
  "prompt-cache": "harness.capability.prompt-cache",
  vision: "harness.capability.vision",
  "responses-api": "harness.capability.responses-api",
  "structured-output": "harness.capability.structured-output",
};

const SDK_KEYS = {
  "openai-python": "harness.sdk.openai-python",
  "openai-typescript": "harness.sdk.openai-typescript",
  "openai-go": "harness.sdk.openai-go",
  "anthropic-python": "harness.sdk.anthropic-python",
  "anthropic-typescript": "harness.sdk.anthropic-typescript",
  "google-genai-python": "harness.sdk.google-genai-python",
  "google-genai-typescript": "harness.sdk.google-genai-typescript",
  "google-genai-go": "harness.sdk.google-genai-go",
  "stainless-python": "harness.sdk.stainless-python",
  "stainless-typescript": "harness.sdk.stainless-typescript",
  "stainless-go": "harness.sdk.stainless-go",
};

function harnessID(item = {}) {
  return String(item?.client_harness || "").trim();
}

function text(value = "") {
  return String(value || "").trim();
}

// Translate the label a known vocabulary value maps to; keep the raw value when
// the table has no entry. `t` defaults to the identity lookup so the helpers
// stay usable in tests without an i18n instance.
function translatedLabel(key, raw, t = (name) => name) {
  if (!key) {
    return raw;
  }
  const translated = t(key);
  return translated && translated !== key ? translated : raw;
}

/**
 * Turn a canonical id into a readable fallback label.
 *
 * Only used when the payload carries no display name: `claude-code` ->
 * `Claude Code`, `codex-cli` -> `Codex CLI`. Brand words keep their known
 * spelling; every other segment is capitalised.
 */
export function prettifyHarnessName(id = "") {
  const words = text(id).split(/[-_\s]+/).filter(Boolean);
  if (!words.length) {
    return "";
  }
  return words
    .map((word) => {
      const lower = word.toLowerCase();
      return WORD_LABELS.get(lower) || `${lower.charAt(0).toUpperCase()}${lower.slice(1)}`;
    })
    .join(" ");
}

/** Display name for a list item, preferring the backend's label over the id. */
export function harnessDisplayName(item = {}) {
  if (!harnessID(item)) {
    return "";
  }
  return text(item?.client_harness_name) || prettifyHarnessName(harnessID(item));
}

/**
 * The badge text: display name plus the client version when the traffic carried
 * one, e.g. `Claude Code 2.0.14`. Empty when the trace has no classification.
 */
export function harnessBadgeLabel(item = {}) {
  const name = harnessDisplayName(item);
  if (!name) {
    return "";
  }
  const version = text(item?.client_harness_version);
  return version ? `${name} ${version}` : name;
}

/**
 * Split `prefix:value` into its two parts. Returns null for anything without
 * both, so a stray empty tag cannot render as a blank chip.
 */
export function parseHarnessTag(tag = "") {
  const raw = text(tag);
  const separator = raw.indexOf(":");
  if (separator <= 0 || separator === raw.length - 1) {
    return null;
  }
  return {
    raw,
    prefix: raw.slice(0, separator).toLowerCase(),
    value: raw.slice(separator + 1).trim(),
  };
}

/**
 * Tags worth rendering next to the badge, in their original sorted order and
 * deduplicated. The `harness:` and `kind:` labels are dropped because the badge
 * already names the harness and its product class; `capability:` and `sdk:`
 * labels are the additional facts.
 */
export function visibleHarnessTags(tags = []) {
  if (!Array.isArray(tags)) {
    return [];
  }
  const seen = new Set();
  const visible = [];
  for (const tag of tags) {
    const parsed = parseHarnessTag(tag);
    if (!parsed || REDUNDANT_TAG_PREFIXES.has(parsed.prefix)) {
      continue;
    }
    if (seen.has(parsed.raw)) {
      continue;
    }
    seen.add(parsed.raw);
    visible.push(parsed.raw);
  }
  return visible;
}

/** Readable product class, e.g. `agent-cli` -> `Agent CLI` / `Agent CLI`. */
export function harnessKindLabel(kind = "", t = (name) => name) {
  const value = text(kind);
  if (!value) {
    return "";
  }
  return translatedLabel(KIND_KEYS[value.toLowerCase()], value, t);
}

/** Readable detection confidence. */
export function harnessConfidenceLabel(confidence = "", t = (name) => name) {
  const value = text(confidence);
  if (!value) {
    return "";
  }
  return translatedLabel(CONFIDENCE_KEYS[value.toLowerCase()], value, t);
}

/**
 * Readable label for one visible tag: a localised capability or SDK name when
 * the vocabulary is known, otherwise the raw suffix after the prefix.
 */
export function harnessTagLabel(tag = "", t = (name) => name) {
  const parsed = parseHarnessTag(tag);
  if (!parsed) {
    return "";
  }
  const keys = parsed.prefix === "capability" ? CAPABILITY_KEYS : parsed.prefix === "sdk" ? SDK_KEYS : null;
  if (!keys) {
    return parsed.value;
  }
  return translatedLabel(keys[parsed.value.toLowerCase()], parsed.value, t);
}

/**
 * Tooltip for a harness badge: the canonical id (the filter key), the localised
 * kind and confidence, then the visible capability/SDK labels.
 */
export function harnessTooltip(item = {}, t = (name) => name) {
  const id = harnessID(item);
  if (!id) {
    return "";
  }
  const parts = [id];
  const kind = harnessKindLabel(item?.client_harness_kind, t);
  if (kind) {
    parts.push(kind);
  }
  const confidence = harnessConfidenceLabel(item?.client_harness_confidence, t);
  if (confidence) {
    parts.push(confidence);
  }
  for (const tag of visibleHarnessTags(item?.client_harness_tags)) {
    const label = harnessTagLabel(tag, t);
    if (label) {
      parts.push(label);
    }
  }
  return parts.join(" · ");
}

/**
 * Query string for the traffic list filtered by one harness.
 *
 * The current query is preserved - a reader who filtered by model and window
 * keeps both - while `harness` is replaced and `page` goes back to 1, because
 * the old page number belongs to the unfiltered result set.
 */
export function harnessFilterSearch(search = "", harnessID = "") {
  const next = new URLSearchParams(text(search).replace(/^\?/, ""));
  const id = text(harnessID);
  if (id) {
    next.set("harness", id);
  } else {
    next.delete("harness");
  }
  next.set("page", "1");
  return next.toString();
}

/** Full href for the traffic list filtered by one harness. */
export function harnessFilterHref(pathname = "/traces", search = "", harnessID = "") {
  const base = text(pathname) || "/traces";
  const query = harnessFilterSearch(search, harnessID);
  return query ? `${base}?${query}` : base;
}
