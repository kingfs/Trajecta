/*
 * Model-catalogue presentation helpers.
 *
 * The model square and the model detail page render the same Argus record, and
 * both have to answer the same three questions about it: what is this model
 * called in a UI (the registry name is "Provider: Model"), which localized
 * description to show, and which capability bits are worth a chip. Keeping the
 * answers here is what stops the two screens from drifting - the square once
 * showed the raw bit names and the detail page would have been the second
 * place to translate them.
 */

// The raw Argus capability names describe modalities and call surfaces. Text-in
// and text-out are true of every chat model and the "Chat" bit adds nothing a
// reader does not already assume, so only the discriminating bits are shown.
const HIDDEN_CAPABILITIES = new Set(["TextIn", "TextOut", "Chat", "SystemPrompt"]);

const CAPABILITY_LABELS = {
  ImageIn: "models.capability.imageIn",
  ImageOut: "models.capability.imageOut",
  AudioIn: "models.capability.audioIn",
  AudioOut: "models.capability.audioOut",
  VideoIn: "models.capability.videoIn",
  VideoOut: "models.capability.videoOut",
  FileIn: "models.capability.fileIn",
  FileOut: "models.capability.fileOut",
  FunctionCall: "models.capability.functionCall",
  JsonMode: "models.capability.jsonMode",
  Embedding: "models.capability.embedding",
  Rerank: "models.capability.rerank",
  TTS: "models.capability.tts",
  ASR: "models.capability.asr",
  Multimodal: "models.capability.multimodal",
};

// Argus names are "Provider: Model", which repeats the provider line above the
// title. Strip that prefix when it is the provider we are already showing.
export function modelDisplayName(item, facts) {
  if (!facts) {
    return item.display_name && item.display_name !== item.model ? item.display_name : item.model;
  }
  let name = (facts.name || "").trim();
  const prefix = `${facts.provider}:`;
  if (name.startsWith(prefix)) {
    name = name.slice(prefix.length).trim();
  }
  return name || item.display_name || item.model;
}

export function modelDescription(facts, language) {
  if (!facts) {
    return "";
  }
  const primary = language === "zh-CN" ? facts.description_cn : facts.description;
  return (primary || facts.description || facts.description_cn || facts.summary || "").trim();
}

export function modelCapabilityChips(facts, t) {
  if (!facts?.capabilities?.length) {
    return [];
  }
  return facts.capabilities
    .filter((capability) => !HIDDEN_CAPABILITIES.has(capability))
    .map((capability) => (CAPABILITY_LABELS[capability] ? t(CAPABILITY_LABELS[capability]) : capability));
}

export function maxChannelValue(channels, key) {
  let max = 0;
  for (const channel of channels || []) {
    const value = Number(channel[key] || 0);
    if (value > max) {
      max = value;
    }
  }
  return max;
}

export function modelContextWindow(facts, channels) {
  return Number(facts?.context_length || 0) || maxChannelValue(channels, "context_window");
}

export function modelMaxOutput(facts, channels) {
  return Number(facts?.max_output || 0) || maxChannelValue(channels, "max_output_tokens");
}

// Catalogue links are rendered as anchors. The registry's URLs come from a
// reviewed dependency, but a link is still attacker-shaped input from the
// browser's point of view, so only http(s) survives; anything else is treated
// as no link at all rather than as a clickable javascript: URL.
export function safeExternalURL(value) {
  const url = String(value || "").trim();
  return /^https?:\/\//i.test(url) ? url : "";
}
