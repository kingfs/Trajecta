/*
 * The pure half of the channel-plugin catalog: bilingual display selection and
 * the plan projection that feeds the channel creation form. The browser spec
 * can only show the states its mock returns; these assertions pin the rules
 * that matter for every plugin: the active language wins with a fallback, a
 * plan overrides only the endpoints it declares, and the default plan keeps the
 * preset's pay-as-you-go surface.
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  localizedName,
  localizedDescription,
  planDisplayName,
  planDisplayDescription,
  projectPlan,
  planKeyPlaceholder,
} from "../src/lib/plugins.js";

const alibaba = {
  id: "alibaba",
  name: "Alibaba Bailian-China",
  name_zh: "阿里百炼-中国",
  notes: "Pay-as-you-go DashScope endpoints.",
  notes_zh: "按量付费 DashScope 端点。",
  endpoints: {
    chat_completions: "https://dashscope.aliyuncs.com/compatible-mode/v1",
    messages: "https://dashscope.aliyuncs.com/apps/anthropic",
  },
  models: { list_path: "/models", no_list: false },
  plans: [
    {
      id: "coding",
      name: "Coding Plan",
      name_zh: "Coding Plan",
      description: "Subscription coding plan.",
      description_zh: "订阅制编程套餐。",
      key_prefix: "sk-sp-",
      coding: true,
      no_list: true,
      endpoints: {
        chat_completions: "https://coding.dashscope.aliyuncs.com/v1",
        messages: "https://coding.dashscope.aliyuncs.com/apps/anthropic",
      },
    },
    {
      id: "token",
      name: "Token Plan",
      key_prefix: "sk-sp-",
      no_list: true,
      endpoints: {
        chat_completions: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
        messages: "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic",
      },
    },
  ],
};

describe("catalog localization", () => {
  it("prefers the active language and falls back across languages", () => {
    assert.equal(localizedName(alibaba, "zh-CN"), "阿里百炼-中国");
    assert.equal(localizedName(alibaba, "en"), "Alibaba Bailian-China");
    assert.equal(localizedName({ id: "x", name: "X" }, "zh-CN"), "X");
    assert.equal(localizedName(null, "en"), "");
  });

  it("localizes notes and plan facts with fallbacks", () => {
    assert.equal(localizedDescription(alibaba, "zh-CN"), "按量付费 DashScope 端点。");
    assert.equal(localizedDescription(alibaba, "en"), "Pay-as-you-go DashScope endpoints.");
    assert.equal(localizedDescription({ description: "d", description_zh: "中" }, "zh-CN"), "中");
    assert.equal(planDisplayName(alibaba.plans[0], "zh-CN"), "Coding Plan");
    assert.equal(planDisplayDescription(alibaba.plans[0], "zh-CN"), "订阅制编程套餐。");
    assert.equal(planDisplayDescription(alibaba.plans[1], "zh-CN"), "");
  });
});

describe("projectPlan", () => {
  it("keeps the preset defaults for the empty plan", () => {
    const projected = projectPlan(alibaba, "");
    assert.equal(projected.plan, null);
    assert.equal(projected.endpoints.chat_completions, "https://dashscope.aliyuncs.com/compatible-mode/v1");
    assert.equal(projected.endpoints.messages, "https://dashscope.aliyuncs.com/apps/anthropic");
    assert.deepEqual(projected.protocols.sort(), ["chat_completions", "messages"]);
    assert.equal(projected.noList, false);
  });

  it("overrides only the endpoints the plan declares", () => {
    const projected = projectPlan(alibaba, "coding");
    assert.equal(projected.plan.id, "coding");
    assert.equal(projected.endpoints.chat_completions, "https://coding.dashscope.aliyuncs.com/v1");
    assert.equal(projected.endpoints.messages, "https://coding.dashscope.aliyuncs.com/apps/anthropic");
    assert.equal(projected.noList, true);
  });

  it("ignores an unknown plan and does not mutate the preset", () => {
    const projected = projectPlan(alibaba, "missing");
    assert.equal(projected.plan, null);
    assert.equal(projected.endpoints.chat_completions, alibaba.endpoints.chat_completions);
    assert.equal(alibaba.endpoints.chat_completions, "https://dashscope.aliyuncs.com/compatible-mode/v1");
  });

  it("hints the documented key prefix of the selected plan", () => {
    assert.equal(planKeyPlaceholder(projectPlan(alibaba, "coding").plan), "sk-sp-...");
    assert.equal(planKeyPlaceholder(projectPlan(alibaba, "").plan), "sk-...");
    assert.equal(planKeyPlaceholder(null), "sk-...");
  });
});