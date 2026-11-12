/*
 * Unit tests for the i18next configuration.
 *
 * These cover the messages themselves rather than any screen: the three
 * settings in lib/i18nOptions.js each exist because a specific string renders
 * wrong without them, and a rendering test would only catch the one string it
 * happens to look at. Run with `bun run test:unit`.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";
import i18next from "i18next";

import en from "../src/locales/en.js";
import zhCN from "../src/locales/zh-CN.js";
import { baseOptions, languageOptions, normalizeLanguage, supportedLanguages } from "../src/lib/i18nOptions.ts";

const messages = { en, "zh-CN": zhCN };

async function translator(language) {
  const instance = i18next.createInstance();
  await instance.init({
    ...baseOptions(),
    lng: language,
    resources: Object.fromEntries(Object.entries(messages).map(([code, table]) => [code, { translation: table }])),
  });
  return instance.getFixedT(language);
}

describe("locale tables", () => {
  it("defines the same keys in every language", () => {
    const zhKeys = Object.keys(zhCN).sort();
    for (const [language, table] of Object.entries(messages)) {
      assert.deepEqual(Object.keys(table).sort(), zhKeys, `${language} key set differs from zh-CN`);
    }
  });

  it("maps every key to a non-empty string", () => {
    for (const [language, table] of Object.entries(messages)) {
      for (const [key, value] of Object.entries(table)) {
        assert.equal(typeof value, "string", `${language}/${key} is not a string`);
        assert.notEqual(value.trim(), "", `${language}/${key} is empty`);
      }
    }
  });

  it("never uses double-brace placeholders", () => {
    // The configured delimiters are single braces; a {{name}} placeholder is
    // emitted literally (the plugins-page stats incident), so no table may
    // contain one.
    for (const [language, table] of Object.entries(messages)) {
      for (const [key, value] of Object.entries(table)) {
        assert.ok(!/\{\{[^}]+\}\}/.test(value), `${language}/${key} uses {{...}}: ${value}`);
      }
    }
  });

});

describe("lookup behaviour", () => {
  it("treats a dotted key as a flat name, not a path", async () => {
    const t = await translator("en");
    assert.equal(t("metric.totalDuration"), "total duration");
    assert.equal(t("traceDetail.eventCount", { count: "4" }), "4 event(s)");
  });

  it("interpolates single-brace placeholders", async () => {
    const t = await translator("en");
    assert.equal(t("providers.missingUsage", { count: "3" }), "3 missing usage");
    assert.equal(t("traceDetail.callID", { id: "call_1" }), "call id call_1");
  });

  it("interpolates the plugin disable-check messages", async () => {
    const enT = await translator("en");
    assert.equal(enT("plugins.disableStats", { channels: 1, models: 2 }), "1 enabled channels and 2 enabled models currently use this plugin.");
    assert.equal(enT("plugins.disableConfirmTitle", { name: "Alibaba Bailian-China" }), 'Disable "Alibaba Bailian-China"?');
    const zhT = await translator("zh-CN");
    assert.equal(zhT("plugins.conflictModels", { models: 2 }), "2 个启用模型");
    assert.equal(zhT("plugins.viewDetails", { name: "阿里百炼-中国" }), "查看 阿里百炼-中国 详情");
  });

  it("interpolates the detection-plugin disable-check messages", async () => {
    const enT = await translator("en");
    assert.equal(enT("plugins.detectionMappingCount", { count: 12 }), "The masking vault currently holds 12 stand-in mappings.");
    assert.equal(enT("plugins.detectionRulesCount", { count: 3 }), "3 rules");
    const zhT = await translator("zh-CN");
    assert.equal(zhT("plugins.detectionMappingCount", { count: 12 }), "当前遮蔽映射表中有 12 条映射。");
    assert.equal(zhT("plugins.detectionRulesCount", { count: 3 }), "共 3 条规则");
    assert.equal(zhT("plugins.tabDetection"), "检测插件");
  });

  it("leaves a value that is only a brace pair alone", async () => {
    // The delimiter pair must require a non-empty name, or this message - the
    // English placeholder for "there is no JSON here" - would render as "".
    const t = await translator("en");
    assert.equal(t("lineage.noJSON"), "{}");
  });

  it("resolves the base key when a count has no plural form", async () => {
    const t = await translator("en");
    assert.equal(t("requests.failures", { count: "1" }), "1 failures");
    assert.equal(t("requests.failures", { count: "7" }), "7 failures");
  });

  it("does not reach for a fallback language", async () => {
    // fallbackLng is off on purpose: i18next loads every language in the
    // fallback chain through the backend, which would fetch a second locale
    // chunk for every reader. Completeness is enforced by the test above, so a
    // miss can only mean a key no table defines.
    const instance = i18next.createInstance();
    await instance.init({
      ...baseOptions(),
      lng: "zh-CN",
      resources: { en: { translation: en }, "zh-CN": { translation: { "common.apply": "应用" } } },
    });
    const t = instance.getFixedT("zh-CN");
    assert.equal(t("common.apply"), "应用");
    assert.equal(t("common.reset"), "common.reset");
    assert.equal(baseOptions().fallbackLng, false);
  });

  it("returns the key when no language defines it", async () => {
    const t = await translator("en");
    assert.equal(t("no.such.key"), "no.such.key");
  });
});

describe("language selection", () => {
  it("accepts the shipped languages and falls back to the default", () => {
    for (const option of languageOptions) {
      assert.equal(normalizeLanguage(option.value), option.value);
    }
    assert.deepEqual(supportedLanguages, ["zh-CN", "en"]);
    assert.equal(normalizeLanguage("de"), "zh-CN");
    assert.equal(normalizeLanguage(undefined), "zh-CN");
  });
});
