import { expect, test } from "@playwright/test";

// The XIR structure tab: static structure document + detection-plugin overlay.
// The fixture deliberately keeps the hooked parts two or three levels below a
// default-collapsed group, which is the shape that used to make the focus and
// hooks-only actions highlight nothing visible.
const STRUCTURE = {
  version: 1,
  root: "exchange",
  representations: [{ id: "wire", name: "Wire view", name_zh: "Wire 视图", description: "raw", description_zh: "原始" }],
  points: [
    { point: "exchange.finalize", section: "exchange", name: "Exchange finalize", name_zh: "交换收尾", reserved: true },
    { point: "request.envelope", section: "request", name: "Request envelope", name_zh: "请求信封", reserved: true },
    { point: "request.message", section: "request", name: "Message content", name_zh: "消息内容" },
    { point: "response.output", section: "response", name: "Response output", name_zh: "响应输出" },
  ],
  nodes: [
    { id: "exchange", kind: "root", type: "xir.Exchange", name: "Exchange", name_zh: "交换", point: "exchange.finalize", children: ["request", "response"] },
    { id: "request", kind: "section", type: "xir.Request", name: "Request", name_zh: "请求", children: ["request.envelope", "request.messages"] },
    { id: "request.envelope", kind: "group", type: "model · stream", name: "Envelope", name_zh: "信封", point: "request.envelope", children: ["request.model"] },
    { id: "request.model", kind: "field", type: "string", name: "Model", name_zh: "模型名" },
    { id: "request.messages", kind: "group", type: "[]xir.Message", name: "Messages", name_zh: "会话消息", children: ["request.message"] },
    { id: "request.message", kind: "part", type: "xir.Message", name: "Message", name_zh: "消息", point: "request.message", children: ["request.message.parts"] },
    { id: "request.message.parts", kind: "group", type: "[]xir.Part", name: "Content parts", name_zh: "内容块", children: ["request.message.text"] },
    { id: "request.message.text", kind: "part", type: "xir.Part(text)", name: "Text", name_zh: "文本", point: "request.message" },
    { id: "response", kind: "section", type: "xir.Response", name: "Response", name_zh: "响应", children: ["response.output"] },
    { id: "response.output", kind: "group", type: "[]xir.Part", name: "Output parts", name_zh: "输出内容块", point: "response.output", children: ["response.text"] },
    { id: "response.text", kind: "part", type: "xir.Part(text)", name: "Text", name_zh: "文本", point: "response.output" },
  ],
};

const PLUGINS = [
  {
    id: "secretmask", name: "Credential & PII masking", name_zh: "凭据与个人数据遮蔽",
    description: "The compiled union DFA.", description_zh: "编译期联合 DFA。",
    kind: "credential_masking", contract: "scanner", lanes: ["hot", "audit"],
    points: ["request.message", "response.output"], builtin: true, always_on: true, enabled: true,
  },
  {
    id: "access.indicators", name: "Access indicators", name_zh: "访问指示符",
    description: "The report-only indicator machine.", description_zh: "仅报告指示符机器。",
    kind: "access_indicator", contract: "scanner", lanes: ["audit"],
    points: ["request.message"], builtin: true, always_on: false, enabled: false,
  },
  {
    id: "tool.error", name: "Tool errors", name_zh: "工具错误",
    description: "Failed tool executions.", description_zh: "失败的工具执行。",
    kind: "tool", contract: "observation", lanes: ["audit"],
    points: ["response.output"], builtin: true, always_on: false, enabled: true,
  },
];

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "en");
  });
  await page.routeWebSocket(/\/api\/events\/ws/, () => {});
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (path === "/api/auth/status") return route.fulfill({ json: { auth_required: false } });
    if (path === "/api/events/summary") return route.fulfill({ json: { unread: 0, total: 0, by_severity: {} } });
    if (path === "/api/xir/structure") return route.fulfill({ json: STRUCTURE });
    if (path === "/api/detector-plugins") return route.fulfill({ json: { items: PLUGINS, capabilities: [] } });
    return route.fulfill({ json: {} });
  });
});

async function openMap(page) {
  await page.goto("/audit?tab=xir");
  await page.waitForSelector(".xir-canvas");
  await expect(page.locator(".xir-card")).not.toHaveCount(0);
}

test("the map renders the structure and an always-on plugin has no switch", async ({ page }) => {
  await openMap(page);

  const secretmask = page.locator(".xir-plugin", { hasText: "Credential & PII masking" });
  await expect(secretmask.getByText(/always on/i)).toBeVisible();
  await expect(secretmask.getByRole("switch")).toHaveCount(0);

  const indicators = page.locator(".xir-plugin", { hasText: "Access indicators" });
  await expect(indicators.getByRole("switch")).toBeVisible();
});

test("focusing a plugin expands the collapsed path to its points", async ({ page }) => {
  await openMap(page);

  // The hooked text parts are below default-collapsed groups.
  await page.locator(".xir-plugin", { hasText: "Credential & PII masking" }).click();
  await expect(page.locator(".xir-card.is-focus-match").first()).toBeVisible();
  expect(await page.locator(".xir-card.is-dimmed").count()).toBeGreaterThan(0);
});

test("hooks-only expands the kept path instead of hiding every hook", async ({ page }) => {
  await openMap(page);

  await page.getByRole("button", { name: "Hooks only" }).click();
  await expect(page.locator(".xir-card.is-hooked").first()).toBeVisible();
  await expect(page.locator(".xir-card.is-hooked").filter({ has: page.locator(".xir-point") }).first()).toBeVisible();
});

test("keyboard activation of the collapse button does not select the card", async ({ page }) => {
  await openMap(page);

  const collapse = page.locator(".xir-card button.xir-collapse").first();
  await collapse.focus();
  await collapse.press("Enter");
  await expect(page.locator(".xir-card.is-selected")).toHaveCount(0);
});

test("a reserved point is labelled in the node detail", async ({ page }) => {
  await openMap(page);

  await page.locator(".xir-card", { hasText: "Envelope" }).first().click();
  const detail = page.locator(".xir-detail");
  await expect(detail.getByText("Reserved")).toBeVisible();
  await expect(detail.getByText(/audit projection node produces/)).toBeVisible();
});
