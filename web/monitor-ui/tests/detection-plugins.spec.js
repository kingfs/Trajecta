import { expect, test } from "@playwright/test";

// The detection-plugin surface: the plugins page's detection tab, its
// algorithm cards (kind/contract/lane tags), the enable/disable flow including
// the hot-lane mapping confirmation with rendered interpolation, and the
// detail drawer's rule catalog plus secretmask's PII settings switch. Every
// endpoint is mocked and the write bodies are captured, so the merged settings
// PATCH and the explicit force flag are proven from the requests.
let detectorPatches = [];
let detectorState;
let mappingCount = 0;

function secretmaskPlugin(overrides = {}) {
  return {
    id: "secretmask",
    name: "Credential & PII masking",
    name_zh: "凭据与个人数据遮蔽",
    description: "The compiled credential/PII DFA plus the name-anchored assignment and DSN passes.",
    description_zh: "编译期凭据/个人数据 DFA，外加环境变量赋值与 DSN 原生扫描。",
    kind: "credential_masking",
    contract: "scanner",
    lanes: ["hot", "audit"],
    points: ["request.system", "request.message", "request.tool_call.arguments", "response.output"],
    builtin: true,
    default_enabled: true,
    rules: [
      { name: "openai.project_key", kind: "api_key", vendor: "OpenAI", report_only: false },
      { name: "national_id.cn", kind: "national_id", report_only: false },
    ],
    enabled: true,
    settings: { disabled_rules: ["mobile.cn"] },
    ...overrides,
  };
}

function indicatorsPlugin(overrides = {}) {
  return {
    id: "access.indicators",
    name: "Access indicators",
    name_zh: "访问指示符",
    description: "The separate path/URL indicator machine. Report-only.",
    description_zh: "独立的路径/URL 指示符机器。仅报告。",
    kind: "access_indicator",
    contract: "scanner",
    lanes: ["audit"],
    points: ["request.tool_call.arguments", "response.tool_call.arguments"],
    builtin: true,
    default_enabled: false,
    rules: [{ name: "access.local_path", kind: "path", report_only: true }],
    enabled: false,
    ...overrides,
  };
}

function toolErrorPlugin(overrides = {}) {
  return {
    id: "tool.error",
    name: "Tool errors",
    name_zh: "工具错误",
    description: "Detects failed tool executions surfaced in the exchange.",
    description_zh: "检测交换中呈现的工具执行失败。",
    kind: "tool",
    contract: "observation",
    lanes: ["audit"],
    builtin: true,
    default_enabled: true,
    enabled: true,
    ...overrides,
  };
}

test.beforeEach(async ({ page }) => {
  detectorPatches = [];
  mappingCount = 0;
  detectorState = [secretmaskPlugin(), indicatorsPlugin(), toolErrorPlugin()];
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
    if (path === "/api/detector-plugins" && method === "GET") {
      return route.fulfill({ json: { items: detectorState } });
    }
    const patchMatch = path.match(/^\/api\/detector-plugins\/(.+)$/);
    if (patchMatch && method === "PATCH") {
      const id = decodeURIComponent(patchMatch[1]);
      const body = route.request().postDataJSON();
      detectorPatches.push({ id, ...body });
      const item = detectorState.find((plugin) => plugin.id === id);
      if (!item) {
        return route.fulfill({ status: 404, json: { error: "plugin not found" } });
      }
      if (body.enabled === false && item.lanes.includes("hot") && mappingCount > 0 && !body.force) {
        return route.fulfill({
          status: 409,
          json: {
            error: "plugin_in_use",
            message: "plugin has stand-in mappings; disabling it stops masking new values. Confirm with force.",
            mapping_count: mappingCount,
          },
        });
      }
      if (body.enabled !== undefined) item.enabled = Boolean(body.enabled);
      if (body.settings !== undefined) item.settings = body.settings;
      return route.fulfill({ json: item });
    }
    return route.fulfill({ json: {} });
  });
});

test("the detection tab lists algorithm cards with kind, contract and lane tags", async ({ page }) => {
  await page.goto("/plugins");
  await page.getByRole("tab", { name: "Detection plugins" }).click();
  await expect(page).toHaveURL(/tab=detection/);
  await expect(page.locator(".plugin-card")).toHaveCount(3);
  const card = page.locator(".plugin-card", { hasText: "Credential & PII masking" });
  await expect(card.getByText("Credential masking", { exact: true })).toBeVisible();
  await expect(card.getByText("Scanner", { exact: true })).toBeVisible();
  await expect(card.getByText("Hot path", { exact: true })).toBeVisible();
  await expect(card.getByText("Audit", { exact: true })).toBeVisible();
  await expect(card.getByRole("switch")).toBeChecked();
  const indicators = page.locator(".plugin-card", { hasText: "Access indicators" });
  await expect(indicators.getByRole("switch")).not.toBeChecked();
  await expect(indicators.getByText("Audit", { exact: true })).toBeVisible();
});

test("the enable switch PATCHes enabled:true without opening the drawer", async ({ page }) => {
  await page.goto("/plugins?tab=detection");
  const card = page.locator(".plugin-card", { hasText: "Access indicators" });
  const toggle = card.getByRole("switch");
  await expect(toggle).not.toBeChecked();
  await toggle.click();
  await expect(toggle).toBeChecked();
  await expect(page.locator(".plugin-drawer")).toBeHidden();
  expect(detectorPatches).toEqual([{ id: "access.indicators", enabled: true }]);
});

test("disabling a hot-lane plugin with mappings asks before stopping masking", async ({ page }) => {
  mappingCount = 12;
  await page.goto("/plugins?tab=detection");
  const card = page.locator(".plugin-card", { hasText: "Credential & PII masking" });
  await card.getByRole("switch").click();
  // The 409 opens the confirmation with the mapping count and rendered
  // (single-brace) interpolation.
  const dialog = page.getByRole("dialog").filter({ hasText: "Disable check" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByText('Disable "Credential & PII masking"?')).toBeVisible();
  await expect(dialog.getByText("The masking vault currently holds 12 stand-in mappings.")).toBeVisible();
  expect(detectorPatches).toEqual([{ id: "secretmask", enabled: false, force: false }]);
  // The operator acknowledges; the force flag is explicit.
  await dialog.getByRole("button", { name: "Disable anyway" }).click();
  await expect(dialog).toBeHidden();
  expect(detectorPatches).toEqual([
    { id: "secretmask", enabled: false, force: false },
    { id: "secretmask", enabled: false, force: true },
  ]);
  await expect(card.getByRole("switch")).not.toBeChecked();
});

test("cancelling the disable check keeps the plugin enabled", async ({ page }) => {
  mappingCount = 4;
  await page.goto("/plugins?tab=detection");
  const card = page.locator(".plugin-card", { hasText: "Credential & PII masking" });
  await card.getByRole("switch").click();
  const dialog = page.getByRole("dialog").filter({ hasText: "Disable check" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toBeHidden();
  expect(detectorPatches).toEqual([{ id: "secretmask", enabled: false, force: false }]);
  await expect(card.getByRole("switch")).toBeChecked();
});

test("the drawer shows the rule catalog and the PII switch merges settings", async ({ page }) => {
  await page.goto("/plugins?tab=detection");
  const card = page.locator(".plugin-card", { hasText: "Credential & PII masking" });
  await card.click();
  const drawer = page.locator(".plugin-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText("Execution")).toBeVisible();
  await expect(drawer.getByText("Builtin", { exact: true })).toBeVisible();
  await expect(drawer.getByText("2 rules")).toBeVisible();
  await expect(drawer.getByText("openai.project_key")).toBeVisible();
  await expect(drawer.getByText("OpenAI", { exact: true })).toBeVisible();
  // PII masking defaults on; the stored disabled_rules must survive the write.
  const pii = drawer.getByRole("switch", { name: "Mask personal data (PII)" });
  await expect(pii).toBeChecked();
  await pii.click();
  await expect(pii).not.toBeChecked();
  expect(detectorPatches).toEqual([
    { id: "secretmask", settings: { disabled_rules: ["mobile.cn"], disable_pii: true } },
  ]);
  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
});

test("the drawer shows the plugin IR scope and the unscoped fallback", async ({ page }) => {
  await page.goto("/plugins?tab=detection");
  await page.locator(".plugin-card", { hasText: "Credential & PII masking" }).click();
  const drawer = page.locator(".plugin-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText("IR scope")).toBeVisible();
  await expect(drawer.locator(".detector-point", { hasText: "request.message" })).toBeVisible();
  await expect(drawer.locator(".detector-point", { hasText: "response.output" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
  // An observation plugin with no declared points shows the unscoped fallback.
  await page.locator(".plugin-card", { hasText: "Tool errors" }).click();
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText("Not field-scoped: acts on every text node.")).toBeVisible();
});

test("report-only rules are badged and observation plugins list no rules", async ({ page }) => {
  await page.goto("/plugins?tab=detection");
  await page.locator(".plugin-card", { hasText: "Access indicators" }).click();
  const drawer = page.locator(".plugin-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText("access.local_path")).toBeVisible();
  await expect(drawer.getByText("Report only")).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
  await page.locator(".plugin-card", { hasText: "Tool errors" }).click();
  await expect(drawer).toBeVisible();
  await expect(drawer.getByText("Observation", { exact: true })).toBeVisible();
  await expect(drawer.getByText("This plugin exposes no rule list.")).toBeVisible();
});

test("the search box filters detection plugins by either language name", async ({ page }) => {
  await page.goto("/plugins?tab=detection");
  const search = page.getByRole("searchbox");
  await search.fill("遮蔽");
  await expect(page.locator(".plugin-card")).toHaveCount(1);
  await expect(page.locator(".plugin-card")).toContainText("Credential & PII masking");
  await search.fill("TOOL.ERROR");
  await expect(page.locator(".plugin-card")).toHaveCount(1);
  await expect(page.locator(".plugin-card")).toContainText("Tool errors");
  await search.fill("no-such-detector");
  await expect(page.locator(".plugin-card")).toHaveCount(0);
  await expect(page.getByText("No plugin matches the search")).toBeVisible();
  await search.fill("");
  await expect(page.locator(".plugin-card")).toHaveCount(3);
});

test("Chinese console localizes the detection tab, cards and disable check", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "zh-CN");
  });
  mappingCount = 3;
  await page.goto("/plugins?tab=detection");
  const card = page.locator(".plugin-card", { hasText: "凭据与个人数据遮蔽" });
  await expect(card).toBeVisible();
  await expect(card.getByText("凭据遮蔽", { exact: true })).toBeVisible();
  await expect(card.getByText("热路径", { exact: true })).toBeVisible();
  await card.getByRole("switch").click();
  const dialog = page.getByRole("dialog").filter({ hasText: "停用检查" });
  await expect(dialog.getByText("停用「凭据与个人数据遮蔽」？")).toBeVisible();
  await expect(dialog.getByText("当前遮蔽映射表中有 3 条映射。")).toBeVisible();
  await dialog.getByRole("button", { name: "仍要停用" }).click();
  await expect(dialog).toBeHidden();
  await expect(card.getByRole("switch")).not.toBeChecked();
});
