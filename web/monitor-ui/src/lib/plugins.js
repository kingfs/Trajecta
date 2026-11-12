// Catalog localization and plan projection, shared by the plugins page and the
// channel creation dialog. The catalog API ships bilingual facts (name/name_zh,
// notes/notes_zh, plan descriptions); these helpers pick the active language
// with a fallback so a missing translation never renders an empty card.

export function localizedName(item, language) {
  if (!item) return "";
  if (language === "en") return item.name || item.id || "";
  return item.name_zh || item.name || item.id || "";
}

export function localizedDescription(item, language) {
  if (!item) return "";
  if (language === "en") return item.notes || item.description || "";
  return item.notes_zh || item.description_zh || item.notes || item.description || "";
}

export function planDisplayName(plan, language) {
  if (!plan) return "";
  if (language === "en") return plan.name || plan.id || "";
  return plan.name_zh || plan.name || plan.id || "";
}

export function planDisplayDescription(plan, language) {
  if (!plan) return "";
  if (language === "en") return plan.description || "";
  return plan.description_zh || plan.description || "";
}

// projectPlan merges the selected plan's endpoints over the preset defaults and
// reports the protocols the channel will serve. planId "" keeps the preset
// defaults (pay-as-you-go). The result feeds the same form fields a catalog
// card click fills, so plan switching and card selection share one code path.
export function projectPlan(preset, planId) {
  const endpoints = { ...(preset?.endpoints || {}) };
  let plan = null;
  if (planId) {
    plan = (preset?.plans || []).find((entry) => entry.id === planId) || null;
    if (plan) Object.assign(endpoints, plan.endpoints || {});
  }
  const protocols = Object.keys(endpoints).filter((protocol) => endpoints[protocol]);
  const noList = plan ? Boolean(plan.no_list) : Boolean(preset?.models?.no_list);
  return { endpoints, protocols, plan, noList };
}

// planKeyPlaceholder hints the documented key shape of the selected plan
// (sk-sp- for the Alibaba coding/token plans) without validating it client-side;
// the server remains the authority.
export function planKeyPlaceholder(plan) {
  if (plan?.key_prefix) return `${plan.key_prefix}...`;
  return "sk-...";
}

// The canonical protocol display order, shared by the drawer's endpoint lists.
export const PROTOCOL_ORDER = ["chat_completions", "responses", "messages", "gemini"];

// orderedEndpoints projects an endpoint map onto [protocol, url] pairs in the
// canonical order, dropping empty entries.
export function orderedEndpoints(endpoints) {
  return PROTOCOL_ORDER.filter((protocol) => endpoints?.[protocol]).map((protocol) => [protocol, endpoints[protocol]]);
}

// pluginDrawerFacts normalizes the two shapes that open the detail drawer — a
// plugins-page item (meta + nested preset) and a catalog card (the preset
// itself) — into one fact set the drawer renders.
export function pluginDrawerFacts(item, { fromCatalog = false } = {}) {
  if (!item) return null;
  const preset = fromCatalog ? item : (item.preset || {});
  return {
    id: item.id,
    name: item.name,
    name_zh: item.name_zh,
    region: item.region || preset.region || "",
    description: fromCatalog ? item.notes : item.description,
    description_zh: fromCatalog ? item.notes_zh : item.description_zh,
    docs_url: item.docs_url || preset.docs_url || "",
    keys_url: item.keys_url || preset.keys_url || "",
    endpoints: preset.endpoints || {},
    protocols: preset.protocols || [],
    plans: preset.plans || [],
    models: preset.models || {},
  };
}