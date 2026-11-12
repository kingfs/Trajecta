import { expect, test } from "@playwright/test";

// The channel-plugin surface: the plugins page's vendor cards and detail
// drawer, the enable/disable flow (including the in-use confirmation with
// rendered interpolation), and the creation dialog's plan selector with its
// link icon into the same drawer. Every endpoint is mocked and the write bodies
// are captured, so "one plugin card, one channel, one key, both protocol
// endpoints" is proven from the requests.
let pluginPatches = [];
let channelCreates = [];
let pluginState;

function alibabaPlugin(overrides = {}) {
  return {
    id: "alibaba",
    name: "Alibaba Bailian-China",
    name_zh: "阿里百炼-中国",
    region: "cn",
    description: "Pay-as-you-go DashScope endpoints.",
    description_zh: "按量付费 DashScope 端点。",
    docs_url: "https://help.aliyun.com/zh/model-studio/",
    keys_url: "https://bailian.console.aliyun.com",
    enabled: false,
    preset: {
      id: "alibaba",
      name: "Alibaba Bailian-China",
      name_zh: "阿里百炼-中国",
      kind: "vendor",
      region: "cn",
      endpoints: {
        chat_completions: "https://dashscope.aliyuncs.com/compatible-mode/v1",
        messages: "https://dashscope.aliyuncs.com/apps/anthropic",
      },
      protocols: ["chat_completions", "messages"],
      auth: { mode: "api_key", header: "Authorization", scheme: "Bearer" },
      models: { list_path: "/models", protocol: "chat_completions" },
      plans: [
        {
          id: "coding",
          name: "Coding Plan",
          name_zh: "Coding Plan",
          description: "Subscription coding plan with a dedicated sk-sp- key.",
          description_zh: "订阅制编程套餐：专用 sk-sp- key。",
          endpoints: {
            chat_completions: "https://coding.dashscope.aliyuncs.com/v1",
            messages: "https://coding.dashscope.aliyuncs.com/apps/anthropic",
          },
          key_prefix: "sk-sp-",
          coding: true,
          no_list: true,
        },
        {
          id: "token",
          name: "Token Plan",
          endpoints: {
            chat_completions: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1",
            messages: "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic",
          },
          key_prefix: "sk-sp-",
          no_list: true,
        },
      ],
    },
    ...overrides,
  };
}

function catalogPayload() {
  const preset = alibabaPlugin().preset;
  return { items: pluginState.enabled ? [preset] : [] };
}

test.beforeEach(async ({ page }) => {
  pluginPatches = [];
  channelCreates = [];
  pluginState = alibabaPlugin();
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "en");
  });
  await page.routeWebSocket(/\/api\/events\/ws/, () => {});
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const method = route.request().method();

    if (path === "/api/auth/status") {
      return route.fulfill({ json: { auth_required: false } });
    }
    if (path === "/api/events/summary") {
      return route.fulfill({ json: { unread: 0, total: 0, by_severity: {} } });
    }
    if (path === "/api/about") {
      return route.fulfill({ json: { version: "test", commit: "test", branch: "test", build_date: "2026-01-01", repository: "https://github.com/kingfs/Trajecta" } });
    }
    if (path === "/api/plugins" && method === "GET") {
      return route.fulfill({ json: { items: [pluginState] } });
    }
    if (path === "/api/plugins/alibaba" && method === "PATCH") {
      const body = route.request().postDataJSON();
      pluginPatches.push(body);
      if (!body.enabled && pluginState.enabled_channel_count > 0 && !body.force) {
        return route.fulfill({
          status: 409,
          json: {
            error: "plugin_in_use",
            message: "plugin has enabled channels",
            enabled_channel_count: pluginState.enabled_channel_count,
            enabled_model_count: pluginState.enabled_model_count,
            channels: [{ id: "bailian-coding", name: "Bailian Coding", enabled: true, enabled_model_count: 2 }],
          },
        });
      }
      pluginState = { ...pluginState, enabled: Boolean(body.enabled) };
      return route.fulfill({ json: pluginState });
    }
    if (path === "/api/catalog/providers") {
      return route.fulfill({ json: catalogPayload() });
    }
    if (path === "/api/provider-presets") {
      return route.fulfill({
        json: {
          items: ["alibaba"],
          presets: [{ id: "alibaba", name: "Alibaba Bailian-China", protocol_family: "openai_compatible", routing_profile: "openai_default", allowed_profiles: ["openai_default"], endpoints: alibabaPlugin().preset.endpoints, protocols: ["chat_completions", "messages"] }],
          defaults: { protocol_families: ["openai_compatible"], routing_profiles: ["openai_default"], model_discovery: ["list_models", "disabled"] },
        },
      });
    }
    if (path === "/api/channels" && method === "GET") {
      return route.fulfill({ json: { items: [] } });
    }
    if (path === "/api/channels" && method === "POST") {
      const body = route.request().postDataJSON();
      channelCreates.push(body);
      return route.fulfill({ json: { id: "created", ...body, summary: {}, trends: [] } });
    }
    return route.fulfill({ json: {} });
  });
});

test("plugin cards show vendor facts and open the detail drawer", async ({ page }) => {
  await page.goto("/plugins");
  const card = page.locator(".plugin-card", { hasText: "Alibaba Bailian-China" });
  await expect(card).toBeVisible();
  // The card carries the vendor facts, not channel-association stats.
  await expect(card.getByText("Pay-as-you-go DashScope endpoints.")).toBeVisible();
  await expect(card.getByRole("link", { name: "Docs" })).toBeVisible();
  await expect(card.getByRole("link", { name: "API keys" })).toBeVisible();
  await expect(card.getByText("{{channels}}", { exact: false })).toHaveCount(0);
  // Clicking the card opens the drawer with interfaces and plan endpoints.
  await card.click();
  const drawer = page.locator(".plugin-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText("Supported interfaces")).toBeVisible();
  await expect(drawer.getByText("https://dashscope.aliyuncs.com/compatible-mode/v1")).toBeVisible();
  await expect(drawer.locator(".plugin-plan-head strong", { hasText: "Coding Plan" })).toBeVisible();
  await expect(drawer.getByText("https://coding.dashscope.aliyuncs.com/apps/anthropic")).toBeVisible();
  await expect(drawer.getByText("sk-sp-…").first()).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
});

test("the enable switch toggles without opening the drawer", async ({ page }) => {
  await page.goto("/plugins");
  const card = page.locator(".plugin-card", { hasText: "Alibaba Bailian-China" });
  const toggle = card.getByRole("switch");
  await expect(toggle).not.toBeChecked();
  await toggle.click();
  await expect(toggle).toBeChecked();
  await expect(page.locator(".plugin-drawer")).toBeHidden();
  expect(pluginPatches).toEqual([{ enabled: true }]);
});

test("disabling a plugin with enabled channels asks before hiding the cards", async ({ page }) => {
  pluginState = alibabaPlugin({ enabled: true, enabled_channel_count: 1, enabled_model_count: 2 });
  await page.goto("/plugins");
  const card = page.locator(".plugin-card", { hasText: "Alibaba Bailian-China" });
  await card.getByRole("switch").click();
  // The 409 opens the confirmation with the affected channel and rendered
  // (single-brace) interpolation.
  const dialog = page.getByRole("dialog").filter({ hasText: "Disable check" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText("Bailian Coding")).toBeVisible();
  await expect(dialog.getByText("1 enabled channels and 2 enabled models currently use this plugin.")).toBeVisible();
  await expect(dialog.getByText("2 enabled models", { exact: true })).toBeVisible();
  await expect(dialog.getByText("Disable channels first")).toBeVisible();
  expect(pluginPatches).toEqual([{ enabled: false, force: false }]);
  // The operator acknowledges; the force flag is explicit.
  await dialog.getByRole("button", { name: "Disable anyway" }).click();
  await expect(dialog).toBeHidden();
  expect(pluginPatches).toEqual([{ enabled: false, force: false }, { enabled: false, force: true }]);
  await expect(card.getByRole("switch")).not.toBeChecked();
});

test("creating a channel from a plan sends one key and both protocol endpoints", async ({ page }) => {
  pluginState = alibabaPlugin({ enabled: true });
  await page.goto("/channels");
  await page.getByRole("button", { name: "New channel" }).click();
  const dialog = page.locator(".provider-create-modal");
  await dialog.locator(".provider-catalog-card", { hasText: "Alibaba Bailian-China" }).click();
  // The compact selection row replaces the old endpoint/description summary.
  await expect(dialog.locator(".provider-selected-summary")).toHaveCount(0);
  await expect(dialog.locator(".provider-selected-row")).toContainText("Alibaba Bailian-China");
  // The link icon opens the plugin drawer without losing the form.
  await dialog.getByRole("button", { name: "View channel plugin details" }).click();
  const drawer = page.locator(".plugin-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText("https://coding.dashscope.aliyuncs.com/v1")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
  // Switch to the coding plan.
  await dialog.getByRole("combobox").click();
  await page.getByRole("option", { name: "Coding Plan" }).click();
  await dialog.getByPlaceholder("sk-sp-...").fill("sk-sp-test-key");
  await dialog.getByRole("button", { name: "Create channel" }).click();
  await expect(dialog).toBeHidden();
  expect(channelCreates).toHaveLength(1);
  const body = channelCreates[0];
  expect(body.provider_preset).toBe("alibaba");
  expect(body.plan).toBe("coding");
  expect(body.name).toBe("Alibaba Bailian-China");
  expect(body.base_url).toBe("https://coding.dashscope.aliyuncs.com/v1");
  expect(body.endpoints.messages).toBe("https://coding.dashscope.aliyuncs.com/apps/anthropic");
  expect(body.capabilities.messages).toBe(true);
  expect(body.capabilities.chat_completions).toBe(true);
  expect(body.api_key).toBe("sk-sp-test-key");
});

test("an empty catalog points the operator to the plugins page", async ({ page }) => {
  await page.goto("/channels");
  await page.getByRole("button", { name: "New channel" }).click();
  const dialog = page.locator(".provider-create-modal");
  await expect(dialog.getByText("The official catalog is empty: no channel plugin is enabled yet.")).toBeVisible();
  await dialog.getByRole("link", { name: "Enable plugins" }).click();
  await expect(page).toHaveURL(/\/plugins$/);
});

test("Chinese console shows the short vendor-region card and drawer", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "zh-CN");
  });
  await page.goto("/plugins");
  const card = page.locator(".plugin-card", { hasText: "阿里百炼-中国" });
  await expect(card).toBeVisible();
  await expect(card.getByText("中国", { exact: true })).toBeVisible();
  await expect(card.getByText("按量付费 DashScope 端点。")).toBeVisible();
  await expect(card.getByRole("link", { name: "文档" })).toBeVisible();
  await expect(card.getByRole("link", { name: "API Key 管理" })).toBeVisible();
  await card.click();
  const drawer = page.locator(".plugin-drawer");
  await expect(drawer.getByText("支持的接口")).toBeVisible();
  await expect(drawer.getByText("套餐", { exact: true })).toBeVisible();
  await expect(drawer.getByText("订阅制编程套餐：专用 sk-sp- key。")).toBeVisible();
});