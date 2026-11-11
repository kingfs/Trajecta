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