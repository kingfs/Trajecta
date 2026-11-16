import { expect, test } from "@playwright/test";

// The detection-plugin surface: the plugins page's detection tab, its
// algorithm cards (kind/contract/lane tags), the enable/disable flow including
// the hot-lane mapping confirmation with rendered interpolation, and the
// detail drawer's rule catalog plus secretmask's PII settings switch. Every
// endpoint is mocked and the write bodies are captured, so the merged settings
// PATCH and the explicit force flag are proven from the requests.
//
// The observability surfaces are mocked the same way: GET stats feeds the
// collapsible stats section (counters, 30-day rule tally, store sizes) and
// POST secretmask/test feeds the read-only rule sandbox dialog; the request
// bodies are captured so the pasted body is proven to reach the endpoint.
let detectorPatches = [];
let detectorState;
let mappingCount = 0;
let statsRequests = [];
let detectorStats;
let statsError = "";
let sandboxRequests = [];
let sandboxFindings;
let sandboxError = null;

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
  statsRequests = [];
  statsError = "";
  detectorStats = {
    hot: {
      hot_calls: 120,
      ir_projected: 30,
      ir_skipped_no_marker: 80,
      ir_skipped_over_size: 2,
      ir_parse_failed: 1,
      ir_decode_failed: 0,
      ir_skipped_other: 0,
      byte_fallbacks: 7,
      field_runs: 5,
      sealed_spans: 9,
      mask_candidates: 14,
      mask_events: 11,
      scanner_errors: 0,
      size_cap_hits: 2,
    },
    rules: [
      { rule: "national_id.cn", kind: "national_id", hits: 4, last_seen: "2026-11-12T08:00:00Z" },
      { rule: "openai.project_key", kind: "api_key", hits: 9, last_seen: "2026-11-10T08:00:00Z" },
    ],
    suppressions: 3,
    mappings: 12,
  };
  sandboxRequests = [];
  sandboxError = null;
  sandboxFindings = [
    {
      rule: "openai.project_key",
      vendor: "OpenAI",
      kind: "api_key",
      start: 15,
      end: 66,
      score: 90,
      confidence: "high",
      reasons: "prefix",
      maskable: true,
      suppressed: false,
      stand_in: "sk-proj-FAKE",
    },
    {
      rule: "national_id.cn",
      kind: "national_id",
      start: 80,
      end: 98,
      score: 80,
      confidence: "high",
      reasons: "",
      maskable: true,
      suppressed: true,
    },
  ];
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
    if (path === "/api/detector-plugins/stats" && method === "GET") {
      statsRequests.push(path);
      if (statsError) {
        return route.fulfill({ status: 500, json: { error: statsError } });
      }
      return route.fulfill({ json: detectorStats });
    }
    if (path === "/api/detector-plugins/secretmask/test" && method === "POST") {
      const body = route.request().postDataJSON();
      sandboxRequests.push(body);
      if (sandboxError) {
        return route.fulfill({ status: sandboxError.status, json: { error: sandboxError.message } });
      }
      return route.fulfill({ json: { body_length: (body.body || "").length, findings: sandboxFindings } });
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

test("the stats section lazy-loads counters, the rule tally and store sizes", async ({ page }) => {
  await page.goto("/plugins?tab=detection");
  // Collapsed by default: the database-backed read is not paid for until the
  // section is expanded.
  expect(statsRequests).toEqual([]);
  const section = page.locator(".collapse-card", { hasText: "Detection stats" });
  await section.getByRole("button", { name: /Detection stats/ }).click();
  await expect(section.locator(".stat-card", { hasText: "Hot-path calls" }).locator("strong")).toHaveText("120");
  await expect(section.locator(".stat-card", { hasText: "IR projected" }).locator("strong")).toHaveText("30");
  await expect(section.locator(".stat-card", { hasText: "Mask candidates" }).locator("strong")).toHaveText("14");
  await expect(section.locator(".stat-card", { hasText: "Replacements" }).locator("strong")).toHaveText("11");
  await expect(section.locator(".stat-card", { hasText: "Suppression entries" }).locator("strong")).toHaveText("3");
  await expect(section.locator(".stat-card", { hasText: "Stand-in mappings" }).locator("strong")).toHaveText("12");
  expect(statsRequests.length).toBeGreaterThan(0);
  // The tally is sorted by replacement count descending, not by server order.
  const rows = section.locator(".detector-stats-table tbody tr");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(0)).toContainText("openai.project_key");
  await expect(rows.nth(0)).toContainText("9");
  await expect(rows.nth(1)).toContainText("national_id.cn");
  await expect(rows.nth(1)).toContainText("4");
});

test("the stats section shows a friendly empty tally and reports read failures", async ({ page }) => {
  detectorStats.rules = [];
  await page.goto("/plugins?tab=detection");
  const section = page.locator(".collapse-card", { hasText: "Detection stats" });
  await section.getByRole("button", { name: /Detection stats/ }).click();
  await expect(section.getByText("No replacements in the last 30 days")).toBeVisible();
  await expect(section.locator(".detector-stats-table")).toHaveCount(0);
  // A failed read keeps the section usable and names the server's message.
  // A full reload starts a fresh query cache (the client dedupes reads for
  // five seconds), so the second load is the one that sees the failure.
  statsError = "rule stats unavailable";
  await page.goto("/plugins?tab=detection");
  await section.getByRole("button", { name: /Detection stats/ }).click();
  await expect(section.getByText("Failed to load detection stats")).toBeVisible();
  await expect(section.getByText("rule stats unavailable")).toBeVisible();
});

test("the rule sandbox posts the pasted body and lists rule, span, confidence and stand-in", async ({ page }) => {
  await page.goto("/plugins?tab=detection");
  await page.getByRole("button", { name: "Rule sandbox" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("secretmask rule sandbox")).toBeVisible();
  // The read-only contract is stated before anything is pasted.
  await expect(dialog.getByText(/throwaway in-memory vault/)).toBeVisible();
  await expect(dialog.getByText(/nothing is written to the mapping store/)).toBeVisible();
  const run = dialog.getByRole("button", { name: "Run", exact: true });
  await expect(run).toBeDisabled();
  const bodyText = '{"api_key":"sk-proj-REALSECRETVALUE"}';
  await dialog.getByRole("textbox", { name: "Request body" }).fill(bodyText);
  await run.click();
  const rows = dialog.locator(".detector-sandbox-table tbody tr");
  await expect(rows).toHaveCount(2);
  expect(sandboxRequests).toEqual([{ body: bodyText }]);
  await expect(dialog.getByText(`2 findings (body: ${bodyText.length} bytes)`)).toBeVisible();
  const first = rows.nth(0);
  await expect(first).toContainText("openai.project_key");
  await expect(first).toContainText("OpenAI");
  await expect(first).toContainText("15–66");
  await expect(first).toContainText("high");
  await expect(first.getByText("Replaced", { exact: true })).toBeVisible();
  await expect(first.locator(".detector-sandbox-standin")).toHaveText("sk-proj-FAKE");
  const second = rows.nth(1);
  await expect(second).toContainText("national_id.cn");
  await expect(second).toContainText("80–98");
  await expect(second.getByText("Suppressed", { exact: true })).toBeVisible();
  await expect(second).toContainText("value kept");
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();
});

test("the rule sandbox shows the no-findings state and the server error message", async ({ page }) => {
  sandboxFindings = [];
  await page.goto("/plugins?tab=detection");
  await page.getByRole("button", { name: "Rule sandbox" }).click();
  const dialog = page.getByRole("dialog");
  const bodyText = "just a harmless sentence";
  await dialog.getByRole("textbox", { name: "Request body" }).fill(bodyText);
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect(dialog.getByText("No findings", { exact: true })).toBeVisible();
  await expect(dialog.getByText(`This body (${bodyText.length} bytes) matched no detection rule.`)).toBeVisible();
  // The next run fails server-side; the dialog surfaces the server's message.
  sandboxError = { status: 413, message: "sandbox body too large" };
  await dialog.getByRole("textbox", { name: "Request body" }).fill("x".repeat(64));
  await dialog.getByRole("button", { name: "Run", exact: true }).click();
  await expect(dialog.getByText("Run failed")).toBeVisible();
  await expect(dialog.getByText("sandbox body too large")).toBeVisible();
  await expect(dialog.locator(".detector-sandbox-table")).toHaveCount(0);
});

test("Chinese console localizes the stats section and the rule sandbox", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "zh-CN");
  });
  await page.goto("/plugins?tab=detection");
  const section = page.locator(".collapse-card", { hasText: "检测统计" });
  await section.getByRole("button", { name: /检测统计/ }).click();
  await expect(section.locator(".stat-card", { hasText: "热路径调用" }).locator("strong")).toHaveText("120");
  await expect(section.getByRole("heading", { name: "近 30 天按规则替换" })).toBeVisible();
  await expect(section.locator(".stat-card", { hasText: "抑制名单条目" }).locator("strong")).toHaveText("3");
  await page.getByRole("button", { name: "规则沙箱" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("secretmask 规则沙箱")).toBeVisible();
  await expect(dialog.getByText(/不写入映射表/)).toBeVisible();
  const zhBody = '{"id":"110101199003077777"}';
  await dialog.getByRole("textbox", { name: "请求 body" }).fill(zhBody);
  await dialog.getByRole("button", { name: "运行", exact: true }).click();
  const rows = dialog.locator(".detector-sandbox-table tbody tr");
  await expect(rows).toHaveCount(2);
  await expect(rows.nth(1).getByText("已抑制", { exact: true })).toBeVisible();
  await expect(dialog.getByText(`命中 2 处（body 共 ${zhBody.length} 字节）`)).toBeVisible();
});
