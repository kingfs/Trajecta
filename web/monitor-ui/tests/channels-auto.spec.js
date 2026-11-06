import { expect, test } from "@playwright/test";

// The channel page's auto-detection surface: the terminology rename, the
// simplified create form, the computed interface chips, the per-model detect
// action, the enable-triggered detect and the channel-wide detect. Every
// endpoint the page reads or writes is mocked, and the captured detect requests
// are asserted directly so "the button calls the right thing with the right
// body" is proven rather than inferred from the DOM.
let detectRequests = [];

function interfaceObject(overrides = {}) {
  return {
    chat_completions: false,
    native_responses: false,
    responses_via_chat: false,
    messages: false,
    generate_content: false,
    ...overrides,
  };
}

function channelListPayload() {
  return {
    items: [
      {
        id: "openai-primary",
        name: "OpenAI Primary",
        provider_preset: "openai",
        source: "manual",
        enabled: true,
        mode: "proxy",
        base_url: "https://api.openai.example/v1",
        model_count: 3,
        enabled_model_count: 2,
        summary: { request_count: 12, failed_request: 1, total_tokens: 23000, success_rate: 90 },
        trends: [],
        last_probe_status: "success",
      },
    ],
  };
}

function channelDetailPayload() {
  return {
    id: "openai-primary",
    name: "OpenAI Primary",
    provider_preset: "openai",
    source: "manual",
    enabled: true,
    mode: "proxy",
    api_type: "chat_completions",
    protocol_family: "openai_compatible",
    base_url: "https://api.openai.example/v1",
    secret_storage_mode: "encrypted-local",
    api_key_hint: "sk-...test",
    headers: {},
    model_count: 3,
    enabled_model_count: 2,
    summary: { request_count: 12, failed_request: 1, missing_usage_request: 0, total_tokens: 23000, success_rate: 90 },
    trends: [],
    recent_failures: [],
    recent_probe_runs: [],
    models_usage: [
      {
        model: "gpt-5",
        enabled: true,
        source: "manual",
        summary: {},
        interfaces: interfaceObject({ chat_completions: true, native_responses: true }),
      },
      {
        model: "gpt-4.1",
        enabled: true,
        source: "manual",
        summary: {},
        interfaces: interfaceObject({ chat_completions: true, responses_via_chat: true, messages: true }),
      },
      {
        // Discovered but disabled, and never detected: the card shows the
        // unknown chip until the operator enables it.
        model: "gpt-new",
        enabled: false,
        source: "discovered",
        summary: {},
      },
    ],
  };
}

function modelDetailPayload() {
  return {
    model: {
      display_name: "gpt-5",
      enabled_channel_count: 2,
      channel_count: 2,
      summary: { request_count: 12, failed_request: 0, missing_usage_request: 0, total_tokens: 23000 },
    },
    trends: [],
    channels: [
      {
        channel_id: "openai-primary",
        source: "manual",
        enabled: true,
        interfaces: interfaceObject({ chat_completions: true, native_responses: true }),
        supports_responses: true,
        supports_chat_completions: true,
        summary: { request_count: 10, failed_request: 0, missing_usage_request: 0, total_tokens: 20000 },
      },
      {
        channel_id: "backup-channel",
        source: "manual",
        enabled: true,
        interfaces: interfaceObject({ chat_completions: true, responses_via_chat: true }),
        supports_responses: null,
        supports_chat_completions: null,
        summary: { request_count: 2, failed_request: 0, missing_usage_request: 0, total_tokens: 3000 },
      },
    ],
  };
}

function detectPayload(body) {
  const now = new Date().toISOString();
  const base = {
    channel_id: body.channel_id,
    overwrite: body.overwrite === true,
    duration_ms: 210,
    started_at: now,
    completed_at: now,
    warnings: [],
  };
  if (!body.model) {
    // Detect-all: one item per enabled model.
    return {
      ...base,
      items: [
        {
          model: "gpt-5",
          wire_model: "gpt-5",
          protocol_family: "openai_compatible",
          protocols: [{ protocol: "chat_completions", status: "ok", http_status: 200, latency_ms: 120 }],
          applied: { supports_chat_completions: true, supports_responses: true },
          skipped_reason: "",
        },
        {
          model: "gpt-4.1",
          wire_model: "gpt-4.1",
          protocol_family: "openai_compatible",
          protocols: [{ protocol: "chat_completions", status: "ok", http_status: 200, latency_ms: 90 }],
          applied: { supports_chat_completions: true, supports_responses: false },
          skipped_reason: "",
        },
      ],
    };
  }
  return {
    ...base,
    items: [
      {
        model: body.model,
        wire_model: body.model,
        protocol_family: "openai_compatible",
        protocols: [
          { protocol: "chat_completions", status: "ok", method: "POST", path: "/v1/chat/completions", http_status: 200, latency_ms: 120 },
          { protocol: "responses", status: "endpoint_not_supported", method: "POST", path: "/v1/responses", http_status: 404, latency_ms: 14 },
        ],
        applied: { supports_chat_completions: true, supports_responses: false },
        skipped_reason: body.model === "gpt-new" ? "model has no detectable interface" : "",
      },
    ],
  };
}

test.beforeEach(async ({ page }) => {
  detectRequests = [];
  // The smoke suite's convention: the console defaults to Chinese, so pin the
  // language the English assertions are written in.
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "en");
  });
  await page.routeWebSocket(/\/api\/events\/ws/, () => {
    // A socket that stays open and silent; the console reconnects otherwise.
  });
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
    if (path === "/api/channels" && method === "GET") {
      return route.fulfill({ json: channelListPayload() });
    }
    if (path === "/api/channels/openai-primary" && method === "GET") {
      return route.fulfill({ json: channelDetailPayload() });
    }
    if (path === "/api/channels/openai-primary" && method === "PATCH") {
      return route.fulfill({ json: { ...channelDetailPayload(), ...route.request().postDataJSON() } });
    }
    if (path === "/api/channels/openai-primary/models/gpt-new" && method === "PATCH") {
      return route.fulfill({ json: { model: "gpt-new", enabled: route.request().postDataJSON().enabled, source: "discovered" } });
    }
    if (path === "/api/provider-presets") {
      return route.fulfill({
        json: {
          items: ["openai"],
          presets: [{ id: "openai", protocol_family: "openai_compatible", routing_profile: "openai_default", allowed_profiles: ["openai_default"] }],
          defaults: { protocol_families: ["openai_compatible"], routing_profiles: ["openai_default"], model_discovery: ["list_models"] },
        },
      });
    }
    if (path === "/api/provider-model-detect" && method === "POST") {
      const body = route.request().postDataJSON();
      detectRequests.push(body);
      // The channel already owns the credential; a detect request must never
      // carry it.
      expect(JSON.stringify(body)).not.toContain("sk-test-secret");
      expect(JSON.stringify(body)).not.toMatch(/api[_-]?key|authorization/i);
      return route.fulfill({ json: detectPayload(body) });
    }
    if (path === "/api/models/gpt-5" && method === "GET") {
      return route.fulfill({ json: modelDetailPayload() });
    }
    if (path === "/api/models/gpt-5/spec-lookup") {
      return route.fulfill({ json: {} });
    }
    // Any other read (sidebar counts and the like) answers an empty payload so
    // the page under test is the only thing the assertions can see.
    return route.fulfill({ json: {} });
  });
});

test("channels use channel terminology and a simplified create form", async ({ page }) => {
  await page.goto("/channels");
  await expect(page.getByRole("heading", { name: "Channels", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "Channels", exact: true }).first()).toBeVisible();
  await expect(page.getByRole("button", { name: "New channel" })).toBeVisible();

  await page.getByRole("button", { name: "New channel" }).click();
  await expect(page.getByRole("heading", { name: "Create channel" })).toBeVisible();
  await expect(page.getByLabel("Name")).toBeVisible();
  await expect(page.getByLabel("Base URL")).toBeVisible();
  await expect(page.getByLabel("API key")).toBeVisible();
  await expect(page.getByRole("switch", { name: "Enabled" })).toBeVisible();
  // Interfaces are detected per model, so the form has no manual capability,
  // mode or discovery controls - not in the primary form and not in advanced.
  await expect(page.getByText("Model interfaces are detected automatically; enabling a model probes Chat Completions and Responses.")).toBeVisible();
  await page.getByRole("button", { name: "Advanced options" }).click();
  await expect(page.getByLabel("API mode")).toHaveCount(0);
  await expect(page.getByLabel("Model discovery")).toHaveCount(0);
  await expect(page.getByLabel("Responses API")).toHaveCount(0);
  await expect(page.getByLabel("Chat Completions", { exact: true })).toHaveCount(0);
});

test("channel detail shows interface chips, detects one model and auto-detects on enable", async ({ page }) => {
  await page.goto("/channels/openai-primary");
  await expect(page.getByRole("heading", { name: "OpenAI Primary" })).toBeVisible();

  // Header union over the enabled models: chat + native Responses from gpt-5,
  // local Responses from gpt-4.1.
  const headerChips = page.locator(".detail-title-block .channel-interface-chips");
  await expect(headerChips).toContainText("Chat Completions");
  await expect(headerChips).toContainText("Native Responses");
  await expect(headerChips).toContainText("Responses via local runtime");

  // Per-model chips, and the unknown chip for a never-detected model.
  const gpt5Card = page.locator(".provider-model-card").filter({ hasText: "gpt-5" }).first();
  await expect(gpt5Card.locator(".channel-interface-chips")).toContainText("Native Responses");
  const gptNewCard = page.locator(".provider-model-card").filter({ hasText: "gpt-new" }).first();
  await expect(gptNewCard.locator(".channel-interface-chips")).toContainText("Interface not detected");

  // Switching a model on triggers the fill-only detect after the PATCH. This
  // runs before any action that raises a toast: on the mobile viewport a
  // bottom toast can cover the card and swallow the click.
  const detectAfterEnable = page.waitForRequest(
    (request) =>
      request.method() === "POST" &&
      new URL(request.url()).pathname === "/api/provider-model-detect" &&
      request.postDataJSON()?.model === "gpt-new" &&
      request.postDataJSON()?.overwrite === false,
  );
  await page.getByRole("switch", { name: "gpt-new enabled" }).click();
  assertDetectBody((await detectAfterEnable).postDataJSON(), { channel_id: "openai-primary", model: "gpt-new", overwrite: false });

  // The toolbar action detects every enabled model with only the channel id.
  const detectAllRequest = page.waitForRequest(
    (request) => request.method() === "POST" && new URL(request.url()).pathname === "/api/provider-model-detect" && !request.postDataJSON()?.model,
  );
  await page.getByRole("button", { name: "Detect interfaces of enabled models" }).click();
  const detectAllBody = (await detectAllRequest).postDataJSON();
  assertDetectBody(detectAllBody, { channel_id: "openai-primary", overwrite: false });
  expect("model" in detectAllBody).toBe(false);
  const notifications = page.getByRole("region", { name: /Notifications/ });
  await expect(notifications).toContainText("applied");
  await expect(page.locator("[data-sonner-toast]")).toHaveCount(0, { timeout: 10_000 });

  // Explicit per-model detect: overwrite=true, and the result is inline.
  await gpt5Card.getByRole("button", { name: "Detect interfaces for gpt-5" }).click();
  await expect.poll(() => detectRequests.some((body) => body.model === "gpt-5" && body.overwrite === true)).toBe(true);
  assertDetectBody(detectRequests.findLast((body) => body.model === "gpt-5" && body.overwrite === true), {
    channel_id: "openai-primary",
    model: "gpt-5",
    overwrite: true,
  });
  const detectStatus = gpt5Card.getByRole("status");
  await expect(detectStatus).toBeVisible();
  await expect(detectStatus).toContainText("Interface detection");
  await expect(detectStatus).toContainText("1 applied");
  await expect(detectStatus).toContainText("Overwrites existing interface flags");
  await expect(detectStatus).toContainText("Chat Completions · 120 ms");
  await expect(detectStatus).toContainText("Responses · 14 ms · 404 endpoint not supported");
});

test("model detail shows interface chips, re-detects, and hides the tri-state overrides", async ({ page }) => {
  await page.goto("/models/gpt-5");
  await expect(page.getByRole("heading", { name: "gpt-5" })).toBeVisible();

  const primaryCard = page.locator(".model-config-card").filter({ hasText: "openai-primary" }).first();
  await expect(primaryCard.locator(".channel-interface-chips").first()).toContainText("Native Responses");

  // The tri-state overrides are a disclosure, collapsed by default.
  await expect(primaryCard.getByLabel("Responses")).toHaveCount(0);
  await expect(primaryCard.getByRole("button", { name: "Advanced overrides (rarely changed)" })).toBeVisible();
  await primaryCard.getByRole("button", { name: "Advanced overrides (rarely changed)" }).click();
  await expect(primaryCard.getByText("These tri-state values override what auto-detection wrote. Keep them at inherit unless a model needs a manual correction.")).toBeVisible();
  await expect(primaryCard.getByLabel("Responses")).toBeVisible();

  // Re-detect writes this model's interfaces on this one channel.
  await primaryCard.getByRole("button", { name: "Re-detect interfaces openai-primary" }).click();
  await expect.poll(() => detectRequests.length).toBeGreaterThan(0);
  assertDetectBody(detectRequests.at(-1), { channel_id: "openai-primary", model: "gpt-5", overwrite: true });
  await expect(primaryCard.getByRole("status")).toContainText("Interface detection");
});

test("Chinese console calls the surface 渠道", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "zh-CN");
  });
  await page.goto("/channels");
  await expect(page.getByRole("heading", { name: "渠道", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "新增渠道" })).toBeVisible();

  await page.goto("/channels/openai-primary");
  await expect(page.getByRole("heading", { name: "OpenAI Primary" })).toBeVisible();
  await expect(page.getByRole("list", { name: "渠道接口" })).toBeVisible();
  await expect(page.locator(".provider-model-card").filter({ hasText: "gpt-5" }).first().getByRole("button", { name: "识别 gpt-5 的接口" })).toBeVisible();
  await expect(page.getByText("启用该模型时会自动识别 Chat Completions 与 Responses 接口。").first()).toBeVisible();
});

function assertDetectBody(body, expected) {
  expect(body).toEqual(expected);
}
