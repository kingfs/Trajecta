import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  // The Monitor UI defaults to Chinese when no language is persisted, while
  // this smoke suite asserts English labels. Pin the language for every test.
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "en");
  });
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    const method = route.request().method();

    if (path === "/api/auth/status") {
      return route.fulfill({ json: { auth_required: false } });
    }
    if (path === "/api/events/summary") {
      return route.fulfill({ json: eventSummaryPayload() });
    }
    if (path === "/api/events/read-all" && method === "POST") {
      return route.fulfill({ json: { updated: 3 } });
    }
    if (path === "/api/events") {
      expect(url.searchParams.get("window")).toBe("all");
      expect(url.searchParams.get("status")).toBe("unread");
      return route.fulfill({ json: eventListPayload() });
    }
    if (path === "/api/routing/summary") {
      return route.fulfill({ json: {} });
    }
    if (path === "/api/models") {
      return route.fulfill({ json: modelListPayload() });
    }
    if (path === "/api/models/gpt-5") {
      return route.fulfill({ json: modelDetailPayload() });
    }
    if (path === "/api/channels") {
      return route.fulfill({ json: channelListPayload() });
    }
    if (path === "/api/provider-probe" && method === "POST") {
      const body = route.request().postDataJSON();
      expect(body.base_url).toBe("https://api.openai.example/v1");
      expect(body.api_type).toBe("chat_completions");
      return route.fulfill({ json: providerProbePreviewPayload() });
    }
    if (path === "/api/provider-probe/report" && method === "POST") {
      const body = route.request().postDataJSON();
      expect(body).toEqual({ channel_id: "openai-primary" });
      return route.fulfill({ json: providerProbeBatchReportPayload() });
    }
    if (path === "/api/provider-probe/report/apply" && method === "POST") {
      const body = route.request().postDataJSON();
      expect(body).toEqual({ channel_id: "openai-primary" });
      expect(JSON.stringify(body)).not.toContain("sk-test-secret");
      return route.fulfill({
        json: {
          report: providerProbeBatchReportPayload(),
          applied: [{ channel_id: "openai-primary", status: "detected", applied: true, applied_fields: ["capabilities.models"] }],
        },
      });
    }
    if (path === "/api/provider-setup/validate" && method === "POST") {
      const body = route.request().postDataJSON();
      expect(body.base_url).toBe("https://api.openai.example/v1");
      expect(body.api_key).toBe("sk-test-secret");
      return route.fulfill({ json: providerSetupValidatePayload(body) });
    }
    if (path === "/api/provider-setup/apply" && method === "POST") {
      const body = route.request().postDataJSON();
      expect(body.base_url).toBe("https://api.openai.example/v1");
      expect(body.api_key).toBe("sk-test-secret");
      expect(body.api_type).toBe("chat_completions");
      expect(body.protocol_family).toBe("openai_compatible");
      return route.fulfill({ json: { applied: true, channel: channelDetailPayload() } });
    }
    if (path === "/api/provider-presets") {
      return route.fulfill({ json: providerPresetPayload() });
    }
    if (path === "/api/channels/openai-primary" && method === "GET") {
      return route.fulfill({ json: channelDetailPayload() });
    }
    if (path === "/api/channels/openai-primary" && method === "PATCH") {
      return route.fulfill({ json: channelDetailPayload() });
    }
    if (path === "/api/channels/openai-primary/probe") {
      const body = route.request().postDataJSON();
      expect(body.enable_discovered).toBe(false);
      expect(body.detect_provider).toBe(true);
      return route.fulfill({ status: 502, json: probeFailurePayload() });
    }
    if (path === "/api/channels/openai-primary/models") {
      return route.fulfill({ json: { model: "gpt-manual", enabled: true, source: "manual" } });
    }
    if (path === "/api/channels/openai-primary/models/gpt-5") {
      return route.fulfill({ json: { ok: true } });
    }
    if (path === "/api/channels/openai-primary/models/batch" && method === "PATCH") {
      const body = route.request().postDataJSON();
      expect(body).toEqual({ models: ["gpt-new"], enabled: true });
      return route.fulfill({ json: { updated: 1, models: ["gpt-new"], enabled: true } });
    }
    if (path === "/api/routing/exchanges") {
      return route.fulfill({ json: traceListPayload() });
    }
    if (path === "/api/routing/summary") {
      return route.fulfill({ json: routingSummaryPayload() });
    }
    if (path === "/api/traces") {
      if (url.searchParams.get("status") === "error") {
        expect(url.searchParams.get("model")).toBe("gpt-5");
        expect(url.searchParams.get("upstream")).toBe("openai-primary");
        expect(url.searchParams.get("min_ttft_ms")).toBe("100");
        expect(url.searchParams.get("max_tokens")).toBe("500");
      }
      return route.fulfill({ json: traceListPayload() });
    }
    if (path === "/api/traces/trace-routed") {
      return route.fulfill({ json: tracePayload() });
    }
    if (path === "/api/traces/trace-routed/raw") {
      return route.fulfill({ json: { data: { request_protocol: "{}", response_protocol: "{}" } } });
    }
    if (path === "/api/traces/trace-routed/reanalyze" && method === "POST") {
      return route.fulfill({ json: { job: analysisJobPayload({ id: 301, job_type: "trace_reanalyze", target_type: "trace", target_id: "trace-routed", status: "completed" }) } });
    }
    if (path === "/api/traces/trace-routed/observation" || path === "/api/traces/trace-routed/findings" || path === "/api/traces/trace-routed/performance") {
      return route.fulfill({ json: {} });
    }
    if (path === "/api/traces") {
      return route.fulfill({ json: traceListPayload() });
    }
    if (path === "/api/sessions") {
      return route.fulfill({ json: sessionListPayload() });
    }
    if (path === "/api/system/host") {
      return route.fulfill({ json: systemHostPayload() });
    }
    if (path === "/api/system/runtime") {
      return route.fulfill({ json: systemRuntimePayload() });
    }
    if (path === "/api/findings") {
      return route.fulfill({ json: { total: 0, items: [] } });
    }
    if (path === "/api/responses/audit/trace") {
      expect(["resp_123", "resp 123/encoded"]).toContain(url.searchParams.get("response_id"));
      return route.fulfill({ json: responsesAuditTracePayload() });
    }
    if (path === "/api/responses/function-executors") {
      return route.fulfill({ json: responsesFunctionExecutorsPayload() });
    }
    if (path === "/api/analysis") {
      return route.fulfill({ json: analysisPayload() });
    }
    if (path === "/api/analysis/jobs") {
      return route.fulfill({ json: { total: 1, items: [analysisJobPayload()] } });
    }
    if (path === "/api/analysis/batch/reanalyze" && method === "POST") {
      const body = route.request().postDataJSON();
      expect(body.missing_usage).toBe(true);
      expect(body.repair_usage).toBe(true);
      return route.fulfill({ json: { job: analysisJobPayload({ id: 202, job_type: "batch_reanalyze", target_type: "batch", target_id: "trace_filter", status: "queued" }) } });
    }
    return route.fulfill({ status: 404, json: { error: `unhandled ${method} ${path}` } });
  });
});

test("models marketplace and detail render", async ({ page }) => {
  await page.goto("/models");
  await expect(page.getByRole("heading", { name: "Models", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: /gpt-5/i })).toBeVisible();
  await expect(page.getByText("1 missing usage").first()).toBeVisible();

  await page.getByRole("link", { name: /gpt-5/i }).first().click();
  await expect(page.getByRole("heading", { name: "gpt-5" })).toBeVisible();
  await expect(page.getByText("Provider coverage")).toBeVisible();
  await expect(page.getByText("openai-primary").first()).toBeVisible();
});

test("provider management renders and supports core actions", async ({ page }) => {
  await page.goto("/providers");
  await expect(page.getByRole("heading", { name: "Providers", exact: true })).toBeVisible();
  await expect(page.getByText("web-managed").first()).toBeVisible();
  await expect(page.getByText("encrypted-local").first()).toBeVisible();
  await page.getByRole("button", { name: "New provider" }).click();
  await expect(page.getByRole("heading", { name: "Create provider" })).toBeVisible();
  await expect(page.getByLabel("Provider preset")).toHaveValue("openai");
  // The form starts from an explicit API type + protocol family surface, which
  // is allowed to be created without a validation round-trip.
  await expect(page.getByRole("button", { name: "Create provider" })).toBeEnabled();
  await page.getByLabel("Base URL").fill("https://api.openai.example/v1");
  await page.getByLabel("API key").fill("sk-test-secret");
  await page.getByRole("button", { name: "Detect provider" }).click();
  await expect(page.getByRole("heading", { name: "Probe suggestions" })).toBeVisible();
  await page.getByRole("button", { name: "Apply suggestions" }).click();
  await expect(page.getByLabel("API type")).toHaveValue("chat_completions");
  await expect(page.getByLabel("API mode")).toHaveValue("proxy");
  await expect(page.getByLabel("protocol")).toHaveValue("openai_compatible");
  await expect(page.getByLabel("Routing profile")).toHaveValue("openai_default");
  await expect(page.getByRole("button", { name: "Create provider" })).toBeEnabled();
  await page.getByRole("button", { name: "Validate setup" }).click();
  await expect(page.getByRole("heading", { name: "Ready to create" })).toBeVisible();
  await expect(page.getByText("stored as sk-...cret")).toBeVisible();
  await expect(page.getByRole("button", { name: "Create provider" })).toBeEnabled();
  // Editing a validation-relevant field drops the previous validation result.
  await page.getByLabel("Base URL").fill("https://api.openai.example/v1/");
  await expect(page.getByText("Validate setup before creating the provider.")).toBeVisible();
  await page.getByLabel("Base URL").fill("https://api.openai.example/v1");
  await page.getByRole("button", { name: "Validate setup" }).click();
  await expect(page.getByRole("button", { name: "Create provider" })).toBeEnabled();
  await page.getByRole("button", { name: "Create provider" }).click();
  await expect(page.getByRole("heading", { name: "Create provider" })).toBeHidden();

  await expect(page).toHaveURL(/providers\/openai-primary/);
  await expect(page.getByRole("heading", { name: "OpenAI Primary" })).toBeVisible();
  await expect(page.getByText("config source").first()).toBeVisible();
  await expect(page.getByText("web-managed").first()).toBeVisible();
  await expect(page.getByText("chat_completions").first()).toBeVisible();
  await expect(page.getByText("responses_server").first()).toBeVisible();
  await expect(page.getByText("1 missing usage").first()).toBeVisible();
  await expect(page.getByText("encrypted-local").first()).toBeVisible();
  await expect(page.getByText("discovered, disabled")).toBeVisible();

  await page.getByRole("button", { name: "Edit provider" }).click();
  await expect(page.getByRole("heading", { name: "Edit provider" })).toBeVisible();
  await expect(page.getByLabel("Provider preset")).toHaveValue("openai");
  await expect(page.getByLabel("Provider enabled")).toBeVisible();
  await expect(page.locator(".provider-edit-modal").getByText(/^Enabled$/)).toHaveCount(0);
  await page.getByRole("button", { name: "Advanced options" }).click();
  await expect(page.getByLabel("API type")).toHaveValue("chat_completions");
  await expect(page.getByLabel("API mode")).toHaveValue("responses_server");
  await expect(page.getByLabel("protocol")).toHaveValue("openai_compatible");
  await expect(page.getByLabel("Routing profile")).toHaveValue("openai_default");
  await expect(page.locator("textarea")).toContainText("Authorization: ***");
  await page.getByRole("button", { name: "Cancel" }).click();

  await page.getByPlaceholder("Add model manually").fill("gpt-manual");
  await page.getByRole("button", { name: "Add model" }).click();
  await expect(page.getByRole("button", { name: "Adding" })).toBeHidden();

  await page.getByRole("button", { name: "Probe provider" }).click();
  await expect(page.getByText("auth_error").first()).toBeVisible();
  await expect(page.getByText(/Verify the API key/i).first()).toBeVisible();
  await expect(page.getByRole("heading", { name: "Probe suggestions" })).toBeVisible();
  await expect(page.getByText("chat_completions").first()).toBeVisible();

  await page.getByRole("button", { name: "Enable discovered (1)" }).click();
  await expect(page.getByRole("button", { name: "Enabling" })).toBeHidden();
});

test("routing page renders selected route records", async ({ page }) => {
  await page.goto("/routing");
  await expect(page.getByRole("heading", { name: "Routing" })).toBeVisible();
  await expect(page.getByText("Recent selected routes")).toBeVisible();
  await expect(page.getByText("openai-primary").first()).toBeVisible();
  await expect(page.getByText("gpt-5").first()).toBeVisible();
  await expect(page.getByText("1 missing usage").first()).toBeVisible();
  await page.getByPlaceholder("Model").fill("gpt-5");
  await page.getByPlaceholder("Channel / upstream").fill("openai-primary");
  await page.getByLabel("Routing status").selectOption("error");
  await page.getByPlaceholder("Min TTFT").fill("100");
  await page.getByPlaceholder("Max tokens").fill("500");
  await page.getByRole("button", { name: "Apply" }).click();
  await expect(page).toHaveURL(/status=error/);
  await expect(page).toHaveURL(/min_ttft_ms=100/);
  await page.getByRole("button", { name: "Reset" }).click();
  await expect(page).not.toHaveURL(/status=error/);
});

test("events page opens the all-window unread inbox", async ({ page }) => {
  await page.goto("/events");
  await expect(page.getByRole("heading", { name: "Events" })).toBeVisible();
  await expect(page.getByRole("button", { name: "all", exact: true })).toHaveClass(/active/);
  await expect(page.getByText("18").first()).toBeVisible();
  await expect(page.getByText("analysis job failed").first()).toBeVisible();
});

test("trace routing links to channel and upstream views", async ({ page }) => {
  await page.goto("/traces/trace-routed");
  await expect(page.getByRole("heading", { name: "Selected route target" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Open Channel" })).toHaveAttribute("href", "/providers/openai-primary");
  await expect(page.getByRole("link", { name: "Open Upstream" })).toHaveAttribute("href", "/upstreams/openai-primary");
  await expect(page.getByRole("link", { name: "Responses audit" })).toHaveAttribute("href", "/audit?response_id=resp+123%2Fencoded");
  await expect(page.getByRole("button", { name: "Show all" })).toBeVisible();
  await page.getByRole("button", { name: "Show all" }).click();
  await expect(page.getByRole("button", { name: "Show less" })).toBeVisible();
  await page.getByRole("button", { name: "Reanalyze" }).click();
  await expect(page.getByText(/job #301 completed/)).toBeVisible();
});

// The disclosure was a `useState` and a plain button: no `aria-expanded`, no
// `aria-controls`, and a hardcoded English "hide"/"show" in a UI that ships two
// languages. Radix Collapsible owns the first two, and the third is a key.
test("the disclosure reports its state and follows the language", async ({ page, isMobile }) => {
  await page.goto("/traces/trace-routed");
  const trigger = page.getByRole("button", { name: /^Reasoning/ }).first();
  await expect(trigger).toBeVisible();
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  await expect(trigger).toContainText("Show");
  // Closed, it names nothing: the section it would point at is not mounted, and
  // an `aria-controls` pointing at a missing id is worse than none. Radix sets
  // it only while the content is in the document.
  await expect(trigger).not.toHaveAttribute("aria-controls", /./);

  await trigger.click();
  await expect(trigger).toHaveAttribute("aria-expanded", "true");
  await expect(trigger).toContainText("Hide");
  const controls = await trigger.getAttribute("aria-controls");
  expect(controls).toBeTruthy();
  // The id it names resolves, which is the half the hand-written version could
  // not have got right without generating ids.
  await expect(page.locator(`[id="${controls}"]`)).toHaveCount(1);

  await trigger.click();
  await expect(trigger).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator(`[id="${controls}"]`)).toHaveCount(0);

  // Desktop only: the mobile project's emulated viewport scales pointer
  // coordinates against a 412px document inside an 826px layout viewport, so the
  // account button click lands on its container instead of the button.
  if (!isMobile) {
    await page.getByRole("button", { name: "Account" }).first().click();
    await page.getByRole("dialog", { name: "Account" }).getByRole("radio", { name: "中文" }).click();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("button", { name: /^推理/ }).first()).toContainText("展开");
  }
});

// The audit page became 质量 with a tab strip, and the two halves that used to
// share one scroll no longer do: lineage is a tab of 质量, while the server-side
// tool bindings moved to 系统 → 服务端工具.
test("quality page renders responses audit trace lineage", async ({ page }) => {
  await page.goto("/audit?response_id=resp_123");
  await expect(page.getByRole("heading", { name: "Quality", exact: true })).toBeVisible();
  // A deep link carrying response_id opens the lineage tab, not the findings tab.
  await expect(page.getByRole("tab", { name: "Request lineage", selected: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Responses request lineage" })).toBeVisible();
  await expect(page.getByText("audit_resp_123")).toBeVisible();
  await expect(page.getByText("response.request").first()).toBeVisible();
  await expect(page.getByRole("link", { name: "trace-routed" })).toHaveAttribute("href", "/traces/trace-routed");
});

// 请求 and 会话 used to be two sidebar entries and, for 请求, two URLs rendering
// the same component. They are two tabs of /traces now, with the old addresses
// redirected so existing links keep working.
test("traffic page merges requests and sessions behind a tab strip", async ({ page }) => {
  await page.goto("/traces");
  await expect(page.getByRole("heading", { name: "Traffic", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Requests", selected: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Recent requests" })).toBeVisible();

  await page.getByRole("tab", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/\/traces\?tab=sessions$/);
  await expect(page.getByRole("tab", { name: "Sessions", selected: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Recent sessions" })).toBeVisible();

  // The tab is URL state, so a reload lands on the same panel.
  await page.reload();
  await expect(page.getByRole("tab", { name: "Sessions", selected: true })).toBeVisible();

  await page.goto("/requests");
  await expect(page).toHaveURL(/\/traces$/);
  await page.goto("/sessions");
  await expect(page).toHaveURL(/\/traces\?tab=sessions$/);
  await expect(page.getByRole("tab", { name: "Sessions", selected: true })).toBeVisible();
});

// The system page reads the host, the process and Postgres through three
// independent endpoints, one per tab.
test("system runtime tab renders host, disk and network metrics", async ({ page }) => {
  await page.goto("/system");
  await expect(page.getByRole("heading", { name: "System", exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Host resources" })).toBeVisible();
  await expect(page.getByText("CPU usage")).toBeVisible();
  await expect(page.getByText("12.5 %")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Go process" })).toBeVisible();

  // Two overlay mounts on the same device collapse into one row that reports
  // how many mount points it stands for.
  const diskSection = page.locator(".system-table-row--disk");
  await expect(diskSection).toHaveCount(2);
  await expect(diskSection.first()).toContainText("/data");
  await expect(diskSection.first()).toContainText("+1");

  // Idle container bridges are dropped; only interfaces that moved bytes show.
  const networkRows = page.locator(".system-table-row--net");
  await expect(networkRows).toHaveCount(2);
  // Busiest interface first; the bridge that never carried a byte is gone.
  await expect(networkRows.first()).toContainText("eth0");
  await expect(networkRows.last()).toContainText("lo");
});

test("server-side tool bindings live on the system page", async ({ page }) => {
  await page.goto("/system?tab=tools");
  await expect(page.getByRole("heading", { name: "System", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Server-side tools", selected: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Server-side tools" })).toBeVisible();
  await expect(page.getByText("lookup_order")).toBeVisible();
  await expect(page.getByText("output configured")).toBeVisible();
  await expect(page.getByText("do-not-leak")).toHaveCount(0);
});

test("access page renders protocol entrypoint examples", async ({ page }) => {
  await page.goto("/connect");
  await expect(page.getByRole("heading", { name: "Access" })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Client setup", selected: true })).toBeVisible();
  await expect(page.getByText("OpenAI-compatible Chat Completions")).toBeVisible();
  await expect(page.getByText("OpenAI Responses / Codex")).toBeVisible();
  await expect(page.getByText("Anthropic Messages / Claude Code")).toBeVisible();
  await expect(page.getByText("/anthropic/messages").first()).toBeVisible();
});

// /analysis is a legacy address: it redirects to the analysis tab of 质量,
// carrying its query string.
test("legacy /analysis redirects to the analysis tab of the quality page", async ({ page }) => {
  await page.goto("/analysis");
  await expect(page).toHaveURL(/\/audit\?tab=analysis$/);
  await expect(page.getByRole("heading", { name: "Quality", exact: true })).toBeVisible();
  await expect(page.getByRole("tab", { name: "Analysis jobs", selected: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Job queue" })).toBeVisible();
  await expect(page.getByText("trace_reanalyze")).toBeVisible();
  await expect(page.getByText("session_summary", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Repair token stats" }).click();
  await expect(page.getByText(/Batch reanalysis job #202 queued/)).toBeVisible();
});

function modelListPayload() {
  return {
    refreshed_at: new Date().toISOString(),
    window: "24h",
    items: [{
      model: "gpt-5",
      display_name: "gpt-5",
      provider_count: 1,
      channel_count: 1,
      enabled_channel_count: 1,
      channels: ["openai-primary"],
      summary: usageSummary({ request_count: 12, missing_usage_request: 1, total_tokens: 23000 }),
      today: usageSummary({ request_count: 4, missing_usage_request: 1, total_tokens: 7000 }),
    }],
  };
}

function modelDetailPayload() {
  return {
    model: modelListPayload().items[0],
    trends: trendItems(),
    channels: [{
      channel_id: "openai-primary",
      model: "gpt-5",
      enabled: true,
      source: "manual",
      summary: usageSummary({ request_count: 12, missing_usage_request: 1, total_tokens: 23000 }),
    }],
    refreshed_at: new Date().toISOString(),
    window: "24h",
  };
}

function channelListPayload() {
  return {
    refreshed_at: new Date().toISOString(),
    items: [channelDetailPayload()],
  };
}

function providerPresetPayload() {
  return {
    items: ["anthropic", "azure_openai", "google_genai", "openai", "openrouter", "vertex", "vllm"],
    presets: [
      { id: "anthropic", protocol_family: "anthropic_messages", routing_profile: "anthropic_default", support_level: "verified", allowed_profiles: ["anthropic_default"] },
      { id: "azure_openai", protocol_family: "openai_compatible", routing_profile: "", support_level: "verified", allowed_profiles: ["azure_openai_v1", "azure_openai_deployment"] },
      { id: "google_genai", protocol_family: "google_genai", routing_profile: "google_ai_studio", support_level: "verified", allowed_profiles: ["google_ai_studio"] },
      { id: "openai", protocol_family: "openai_compatible", routing_profile: "openai_default", support_level: "verified", allowed_profiles: ["openai_default"] },
      { id: "openrouter", protocol_family: "openai_compatible", routing_profile: "openai_default", support_level: "verified", allowed_profiles: ["openai_default"] },
      { id: "vertex", protocol_family: "vertex_native", routing_profile: "", support_level: "verified", allowed_profiles: ["vertex_express", "vertex_project_location"] },
      { id: "vllm", protocol_family: "openai_compatible", routing_profile: "vllm_openai", support_level: "verified", allowed_profiles: ["vllm_openai"] },
    ],
    defaults: {
      protocol_families: ["openai_compatible", "anthropic_messages", "google_genai", "vertex_native"],
      routing_profiles: ["openai_default", "azure_openai_v1", "azure_openai_deployment", "vllm_openai", "anthropic_default", "google_ai_studio", "vertex_express", "vertex_project_location"],
      model_discovery: ["list_models", "disabled"],
    },
  };
}

function channelDetailPayload() {
  return {
    id: "openai-primary",
    name: "OpenAI Primary",
    source: "manual",
    base_url: "https://api.openai.example/v1",
    provider_preset: "openai",
    api_type: "chat_completions",
    mode: "responses_server",
    capabilities: { responses: false, chat_completions: true, tool_calling: true },
    protocol_family: "openai_compatible",
    routing_profile: "openai_default",
    api_version: "",
    deployment: "",
    project: "",
    location: "",
    model_resource: "",
    api_key_hint: "sk-...test",
    secret_storage_mode: "encrypted-local",
    headers: { Authorization: "***", "X-Test": "visible" },
    enabled: true,
    priority: 100,
    weight: 1,
    capacity_hint: 1,
    model_discovery: "list_models",
    allow_unknown_models: false,
    model_count: 2,
    enabled_model_count: 2,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    last_probe_status: "success",
    summary: usageSummary({ request_count: 12, failed_request: 1, missing_usage_request: 1, total_tokens: 23000 }),
    trends: trendItems(),
    models_usage: [
      { channel_id: "openai-primary", model: "gpt-5", enabled: true, source: "manual", summary: usageSummary({ request_count: 10, missing_usage_request: 1, total_tokens: 20000 }) },
      { channel_id: "openai-primary", model: "gpt-4.1", enabled: true, source: "manual", summary: usageSummary({ request_count: 2, total_tokens: 3000 }) },
      { channel_id: "openai-primary", model: "gpt-new", enabled: false, source: "discovered", summary: usageSummary() },
    ],
    recent_failures: [{
      trace_id: "trace-failed",
      model: "gpt-5",
      status_code: 429,
      reason: "http_429",
      recorded_at: new Date().toISOString(),
      error_text: "rate limited",
    }],
    recent_probe_runs: [{
      id: "probe-success",
      status: "success",
      started_at: new Date().toISOString(),
      completed_at: new Date().toISOString(),
      duration_ms: 10,
      discovered_count: 2,
      enabled_count: 2,
      endpoint: "/v1/models",
    }, {
      id: "probe-failed",
      status: "failed",
      failure_reason: "auth_error",
      retry_hint: "Verify the API key and custom authorization headers for this channel.",
      started_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      completed_at: new Date(Date.now() - 60 * 60 * 1000).toISOString(),
      duration_ms: 12,
      discovered_count: 0,
      enabled_count: 0,
      endpoint: "/v1/models",
      error_text: "upstream status: 401 Unauthorized",
    }],
  };
}

function responsesAuditTracePayload() {
  return {
    query: { response_id: "resp_123", request_audit_id: "audit_resp_123" },
    request_audit: {
      id: "audit_resp_123",
      response_id: "resp_123",
      conversation_id: "thread_123",
      method: "POST",
      path: "/v1/responses",
      client_request_id: "client-123",
      header_json: { "content-type": "application/json", "x-client-request-id": "client-123" },
      body_preview: "{\"model\":\"gpt-5\",\"input\":\"hello\"}",
      body_sha256: "sha256-demo",
      status: "completed",
      created_at: "2026-06-22T08:00:00Z",
    },
    events: [
      {
        id: "event-accepted",
        response_id: "resp_123",
        request_audit_id: "audit_resp_123",
        conversation_id: "thread_123",
        event_type: "response.request",
        phase: "request",
        status: "accepted",
        details_json: { method: "POST", path: "/v1/responses" },
        occurred_at: "2026-06-22T08:00:00Z",
      },
      {
        id: "event-completed",
        response_id: "resp_123",
        request_audit_id: "audit_resp_123",
        conversation_id: "thread_123",
        event_type: "response.request",
        phase: "request",
        status: "completed",
        occurred_at: "2026-06-22T08:00:01Z",
      },
    ],
    upstream_exchanges: [
      {
        id: "exchange-1",
        response_id: "resp_123",
        request_audit_id: "audit_resp_123",
        trace_id: "trace-routed",
        upstream_id: "openai-primary",
        model: "gpt-5",
        endpoint: "/v1/responses",
        status_code: 200,
        started_at: "2026-06-22T08:00:00Z",
        completed_at: "2026-06-22T08:00:01Z",
      },
    ],
  };
}

function eventSummaryPayload() {
  return {
    total: 163,
    unread: 18,
    critical: 0,
    error: 16,
    warning: 2,
    last_seen_at: new Date().toISOString(),
    by_source: [{ label: "analyzer", count: 136 }],
    by_category: [{ label: "analysis_job_failure", count: 136 }],
    window: "all",
  };
}

function eventListPayload() {
  return {
    page: 1,
    page_size: 50,
    total: 18,
    total_pages: 1,
    window: "all",
    refreshed_at: new Date().toISOString(),
    items: [{
      id: "event-analysis-1",
      fingerprint: "analyzer:analysis_job_failure:demo",
      source: "analyzer",
      category: "analysis_job_failure",
      severity: "error",
      status: "unread",
      title: "analysis job failed",
      message: "semantic scan failed",
      occurrence_count: 3,
      first_seen_at: new Date(Date.now() - 60_000).toISOString(),
      last_seen_at: new Date().toISOString(),
      created_at: new Date(Date.now() - 60_000).toISOString(),
      updated_at: new Date().toISOString(),
      details_json: { job_id: 101 },
    }],
  };
}

function tracePayload() {
  return {
    header: {
      meta: {
        request_id: "trace-routed",
        time: new Date().toISOString(),
        model: "gpt-5",
        provider: "openai",
        endpoint: "/v1/responses",
        status_code: 200,
        duration_ms: 1200,
        ttft_ms: 120,
        selected_upstream_id: "openai-primary",
        selected_upstream_base_url: "https://api.openai.example/v1",
        selected_upstream_provider_preset: "openai",
        routing_policy: "p2c",
        routing_score: 0.82,
        routing_candidate_count: 2,
        response_id: "resp 123/encoded",
        request_audit_id: "audit_resp_123",
      },
      usage: { prompt_tokens: 100, completion_tokens: 20, total_tokens: 120 },
      layout: { is_stream: false },
    },
    // Renders the one disclosure on this page that is not gated on a tool
    // message, which is what the disclosure test drives.
    ai_reasoning: "The route was chosen because the primary upstream was healthy.",
    messages: [
      { role: "system", content: longSystemPrompt(), message_type: "message" },
      { role: "user", content: "hello", message_type: "message" },
    ],
    events: [],
    tools: [],
  };
}

function longSystemPrompt() {
  return [
    "You are operating inside a local trace review workflow.",
    "Preserve exact user intent.",
    "Prefer source-near evidence.",
    "Do not call external services from replay tests.",
    "Keep cassette files human inspectable.",
    "Explain storage changes before changing schemas.",
    "Handle OpenAI-compatible payloads without translation.",
    "Treat replay compatibility as a hard requirement.",
    "Keep derived indexes rebuildable.",
    "Avoid mutating unrelated trace files.",
    "This line should be hidden until the reviewer expands the prompt.",
    "This final line verifies the expanded view keeps the full system prompt available.",
  ].join("\n");
}

function routingSummaryPayload() {
  return {
    eventful_traces: 1,
    legacy_or_missing_events: 0,
    parse_errors: 0,
    selected_route_targets: [{ label: "openai-primary", count: 1 }],
    selected_channels: [{ label: "openai-primary", count: 1 }],
    selected_credentials: [],
    selected_upstreams: [{ label: "openai-primary", count: 1 }],
    failure_reasons: [],
    sticky_statuses: [],
    sticky_breaks: { total: 0 },
  };
}

function traceListPayload() {
  return {
    page: 1,
    page_size: 100,
    total: 1,
    total_pages: 1,
    refreshed_at: new Date().toISOString(),
    stats: { total_request: 1, avg_ttft: 120, total_tokens: 120, success_request: 1, failed_request: 0, success_rate: 100 },
    items: [{
      id: "trace-routed",
      recorded_at: new Date().toISOString(),
      model: "gpt-5",
      provider: "openai",
      selected_upstream_id: "openai-primary",
      operation: "responses.create",
      endpoint: "/v1/responses",
      method: "POST",
      status_code: 200,
      duration_ms: 1200,
      ttft_ms: 120,
      total_tokens: 120,
      prompt_tokens: 100,
      completion_tokens: 20,
      cached_tokens: 0,
      is_stream: false,
    }, {
      id: "trace-missing-usage",
      recorded_at: new Date().toISOString(),
      model: "gpt-5",
      provider: "openai",
      selected_upstream_id: "openai-primary",
      operation: "responses.create",
      endpoint: "/v1/responses",
      method: "POST",
      status_code: 200,
      duration_ms: 900,
      ttft_ms: 90,
      total_tokens: 0,
      prompt_tokens: 0,
      completion_tokens: 0,
      cached_tokens: 0,
      is_stream: false,
    },
    // The metric chips format for width: this row is slow enough to need minutes
    // and its prefill is fast enough to need the "k" rate suffix.
    {
      id: "trace-long",
      recorded_at: new Date().toISOString(),
      model: "gpt-5",
      provider: "openai",
      selected_upstream_id: "openai-primary",
      operation: "responses.create",
      endpoint: "/v1/responses",
      method: "POST",
      status_code: 200,
      duration_ms: 125000,
      ttft_ms: 1000,
      total_tokens: 208110,
      prompt_tokens: 112055,
      completion_tokens: 96055,
      cached_tokens: 96055,
      is_stream: true,
    }],
  };
}

function sessionListPayload() {
  return {
    page: 1,
    page_size: 50,
    total: 1,
    total_pages: 1,
    refreshed_at: new Date().toISOString(),
    items: [{
      session_id: "session-a",
      session_source: "responses",
      last_model: "gpt-5",
      providers: ["openai"],
      first_seen: new Date().toISOString(),
      last_seen: new Date().toISOString(),
      request_count: 3,
      stream_count: 1,
      failed_request: 0,
      success_rate: 100,
      avg_ttft: 120,
      total_tokens: 340,
      total_duration_ms: 2400,
    }],
  };
}

function systemHostPayload() {
  return {
    generated_at: new Date().toISOString(),
    unsupported: false,
    reason: "",
    host: { hostname: "fixture-host", uptime_seconds: 90_000, kernel: "6.8.0-test" },
    cpu: {
      cores: 8, usage_percent: 12.5, user_percent: 9, system_percent: 3, iowait_percent: 0.5,
      idle_percent: 87.5, load1: 0.4, load5: 0.3, load15: 0.2,
      per_core_percent: [10, 11, 12, 13, 14, 15, 16, 17],
    },
    memory: {
      total_bytes: 16 * 1024 ** 3, used_bytes: 4 * 1024 ** 3, available_bytes: 12 * 1024 ** 3,
      used_percent: 25, cached_bytes: 2 * 1024 ** 3, swap_total_bytes: 1024 ** 3,
      swap_used_bytes: 0, swap_used_percent: 0,
    },
    // Two mount points on the same device, plus one that stands alone.
    disk: [
      { mount: "/data/docker/volumes/a/_data", filesystem: "ext4", device: "/dev/mapper/vg-data", total_bytes: 100 * 1024 ** 3, used_bytes: 60 * 1024 ** 3, available_bytes: 40 * 1024 ** 3, used_percent: 60 },
      { mount: "/data", filesystem: "ext4", device: "/dev/mapper/vg-data", total_bytes: 100 * 1024 ** 3, used_bytes: 60 * 1024 ** 3, available_bytes: 40 * 1024 ** 3, used_percent: 60 },
      { mount: "/", filesystem: "ext4", device: "/dev/mapper/vg-root", total_bytes: 50 * 1024 ** 3, used_bytes: 10 * 1024 ** 3, available_bytes: 40 * 1024 ** 3, used_percent: 20 },
    ],
    network: {
      interfaces: [
        { name: "lo", loopback: true, rx_bytes: 2048, tx_bytes: 2048, rx_bytes_per_sec: 10, tx_bytes_per_sec: 10 },
        { name: "eth0", loopback: false, rx_bytes: 4096, tx_bytes: 8192, rx_bytes_per_sec: 20, tx_bytes_per_sec: 40 },
        { name: "br-idle", loopback: false, rx_bytes: 0, tx_bytes: 0, rx_bytes_per_sec: 0, tx_bytes_per_sec: 0 },
      ],
      rx_bytes_per_sec: 30, tx_bytes_per_sec: 50,
    },
    process: { pid: 4321, rss_bytes: 64 * 1024 ** 2, vs_z_bytes: 1024 ** 3, cpu_percent: 1.5, threads: 12, open_fds: 21, started_at: new Date().toISOString() },
    warnings: [],
  };
}

function systemRuntimePayload() {
  return {
    generated_at: new Date().toISOString(),
    go_version: "go1.26.5",
    goos: "linux",
    goarch: "amd64",
    num_cpu: 8,
    gomaxprocs: 8,
    goroutines: 42,
    uptime_seconds: 3600,
    heap: { alloc_bytes: 8 * 1024 ** 2, in_use_bytes: 6 * 1024 ** 2, total_sys_bytes: 32 * 1024 ** 2, objects: 1234, stack_bytes: 512 * 1024 },
    gc: { cycles: 12, pause_total_ms: 4.5, recent_pause_count: 4, last_gc: new Date().toISOString(), recent_pause_min_ms: 0.1, recent_pause_p50_ms: 0.2, recent_pause_p75_ms: 0.3, recent_pause_max_ms: 0.4 },
    db_pool: { driver: "postgres", max_open: 10, open: 2, in_use: 1, idle: 1, wait_count: 0, wait_duration_ms: 0, max_idle_closed: 0, max_lifetime_closed: 0 },
  };
}

function responsesFunctionExecutorsPayload() {
  return {
    enabled: true,
    timeout: "2s",
    max_result_bytes: 256,
    redaction: {
      arguments: true,
      output: true,
    },
    supported_types: ["static_response"],
    executors: [
      {
        name: "lookup_order",
        type: "static_response",
        enabled: true,
        output_configured: true,
      },
    ],
    warnings: [],
  };
}

function analysisPayload() {
  return {
    total: 1,
    items: [{
      id: 1,
      session_id: "sess-demo",
      kind: "session_summary",
      analyzer: "session_summary",
      analyzer_version: "0.1.0",
      input_ref: "session:sess-demo",
      output: { session_id: "sess-demo" },
      status: "completed",
      created_at: new Date().toISOString(),
    }],
  };
}

function analysisJobPayload(overrides = {}) {
  return {
    id: 101,
    job_type: "trace_reanalyze",
    target_type: "trace",
    target_id: "trace-routed",
    status: "completed",
    steps: ["reparse_observation", "scan_findings"],
    request: {},
    result: { findings: { count: 0 } },
    attempts: 1,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  };
}

function probeFailurePayload() {
  return {
    channel_id: "openai-primary",
    status: "failed",
    failure_reason: "auth_error",
    retry_hint: "Verify the API key and custom authorization headers for this channel.",
    models: [],
    discovered_count: 0,
    enabled_count: 0,
    endpoint: "/v1/models",
    error_text: "upstream status: 401 Unauthorized",
    provider_probe: {
      provider_id: "openai-primary",
      base_url: "https://api.openai.example/v1",
      specified_api_type: "chat_completions",
      specified_protocol_family: "openai_compatible",
      checked_endpoints: [],
      status: "detected",
      suggested_api_type: "chat_completions",
      suggested_protocol_family: "openai_compatible",
      capabilities: ["chat_completions", "models"],
      confidence: 0.7,
    },
    started_at: new Date().toISOString(),
    completed_at: new Date().toISOString(),
    duration_ms: 12,
  };
}

function providerProbePreviewPayload() {
  return {
    provider_id: "OpenAI Primary",
    base_url: "https://api.openai.example/v1",
    specified_api_type: "chat_completions",
    specified_protocol_family: "openai_compatible",
    checked_endpoints: [],
    status: "detected",
    suggested_api_type: "chat_completions",
    suggested_protocol_family: "openai_compatible",
    capabilities: ["chat_completions", "models"],
    confidence: 0.7,
  };
}

function providerProbeBatchReportPayload() {
  return {
    reports: [
      {
        target_source: "channel",
        provider_id: "openai-primary",
        base_url: "https://api.openai.example/v1",
        specified_api_type: "chat_completions",
        specified_protocol_family: "openai_compatible",
        checked_endpoints: [],
        status: "detected",
        suggested_api_type: "responses",
        suggested_protocol_family: "openai_compatible",
        capabilities: ["chat_completions", "models"],
        confidence: 0.7,
      },
    ],
  };
}

function providerSetupValidatePayload(body) {
  return {
    normalized_config: {
      name: body.name || "OpenAI Primary",
      base_url: body.base_url,
      provider_preset: body.provider_preset || "openai",
      api_type: "chat_completions",
      mode: body.mode || "proxy",
      capabilities: { chat_completions: true, models: true },
      protocol_family: "openai_compatible",
      routing_profile: "openai_default",
      enabled: true,
      priority: body.priority ?? 100,
      weight: body.weight ?? 1,
      capacity_hint: body.capacity_hint ?? 1,
      model_discovery: body.model_discovery || "list_models",
      allow_unknown_models: Boolean(body.allow_unknown_models),
    },
    probe: providerProbePreviewPayload(),
    secret: {
      api_key_hint: "sk-...cret",
      secret_storage_mode: "encrypted-local",
      redaction_guarantee: "api_key is never echoed; secret headers are redacted",
    },
  };
}

function trendItems() {
  const now = Date.now();
  return Array.from({ length: 6 }, (_, index) => ({
    time: new Date(now - (5 - index) * 60 * 60 * 1000).toISOString(),
    request_count: index + 1,
    failed_request: index === 4 ? 1 : 0,
    total_tokens: (index + 1) * 1000,
    model_count: 1,
  }));
}

function usageSummary(overrides = {}) {
  return {
    request_count: 0,
    success_request: 0,
    failed_request: 0,
    success_rate: 100,
    missing_usage_request: 0,
    total_tokens: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    cached_tokens: 0,
    avg_ttft: 0,
    avg_duration_ms: 0,
    last_seen: new Date().toISOString(),
    ...overrides,
  };
}


test("disabled provider preserves model choices without claiming availability", async ({ page }) => {
  await page.route("**/api/channels/openai-primary?*", (route) => {
    const payload = channelDetailPayload();
    payload.enabled = false;
    payload.models_usage.push({ model: "historical-model", source: "trace", enabled: false, summary: {} });
    return route.fulfill({ json: payload });
  });
  await page.goto("/providers/openai-primary");
  await expect(page.getByText("Provider disabled: all model routes are blocked. Model selections are preserved.")).toBeVisible();
  await expect(page.getByLabel("gpt-5 enabled", { exact: true })).toBeChecked();
  await expect(page.getByText("blocked: provider disabled").first()).toBeVisible();
  await expect(page.getByLabel("historical-model enabled", { exact: true })).toHaveCount(0);
  await expect(page.getByText("History only — add model to configure routing")).toBeVisible();
  await expect(page.locator(".provider-model-card").getByText("new", { exact: true })).toHaveCount(0);
});

test("provider toggle failure is visible and preserves the displayed choice", async ({ page }) => {
  await page.route("**/api/channels/openai-primary", (route) => {
    if (route.request().method() === "PATCH") return route.fulfill({ status: 409, json: { error: "Upstreams are managed by YAML credentials" } });
    return route.fallback();
  });
  await page.goto("/providers");
  await page.getByLabel("OpenAI Primary enabled", { exact: true }).click();
  await expect(page.getByRole("alert")).toHaveText("Upstreams are managed by YAML credentials");
  await expect(page.getByLabel("OpenAI Primary enabled", { exact: true })).toBeChecked();
});

// Collapsing the rail hides every label by design, but a previous version also
// hid the toggle itself and left no way to expand the sidebar again.
test("collapsed sidebar keeps a visible expand control", async ({ page, isMobile }) => {
  // Below 1000px the shell is always a rail, so the collapsed flag changes
  // nothing and the toggle is deliberately hidden there.
  test.skip(isMobile, "the rail is width-forced below 1000px");
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.sidebar.collapsed", "true");
  });
  await page.goto("/overview");

  const shell = page.locator(".app-shell");
  await expect(shell).toHaveClass(/app-shell-collapsed/);

  const expand = page.getByRole("button", { name: "Expand sidebar" });
  await expect(expand).toBeVisible();
  await expand.click();

  await expect(shell).not.toHaveClass(/app-shell-collapsed/);
  const collapse = page.getByRole("button", { name: "Collapse sidebar" });
  await expect(collapse).toBeVisible();

  // The round trip is the regression: collapsing must stay reversible.
  await collapse.click();
  await expect(shell).toHaveClass(/app-shell-collapsed/);
  await expect(page.getByRole("button", { name: "Expand sidebar" })).toBeVisible();
});

// The account surface has been three shapes: a hand-positioned popover clamped
// to the rail, a centred modal, and now a panel anchored to the account button.
// The two properties below are the ones both earlier shapes got wrong - it opens
// upwards from the button, and it is wider than the rail it hangs off.
test("the account panel opens above the rail button and overflows the rail", async ({ page }) => {
  await page.goto("/overview");
  const trigger = page.getByRole("button", { name: "Account" }).first();
  const rail = page.locator(".app-sidebar");

  await trigger.click();
  const panel = page.getByRole("dialog", { name: "Account" });
  await expect(panel).toBeVisible();

  // A panel rather than a modal: no backdrop, and the page behind stays live.
  await expect(page.locator(".nav-modal-backdrop")).toHaveCount(0);
  expect(await page.evaluate(() => getComputedStyle(document.body).overflow)).not.toBe("hidden");

  const triggerBox = await trigger.boundingBox();
  const panelBox = await panel.boundingBox();
  const railBox = await rail.boundingBox();

  // Above the button it was opened from, with the gap `sideOffset` asks for.
  expect(panelBox.y + panelBox.height).toBeLessThanOrEqual(triggerBox.y + 1);
  expect(triggerBox.y - (panelBox.y + panelBox.height)).toBeLessThanOrEqual(16);
  // Flush with the button's left edge, or nudged inwards by the collision
  // padding when the button sits closer to the viewport edge than that padding
  // allows - which is what the 8px rail gutter makes it do. Never pushed the
  // other way, and never centred.
  expect(panelBox.x).toBeGreaterThanOrEqual(triggerBox.x);
  expect(panelBox.x - triggerBox.x).toBeLessThanOrEqual(12);
  // Wider than the rail, hanging over the page. This is the assertion the
  // clamped popover failed and the reason the modal existed in between.
  expect(panelBox.x + panelBox.width).toBeGreaterThan(railBox.x + railBox.width);
  // And still on screen: the flip/shift middleware owns that, not the CSS.
  const viewport = page.viewportSize();
  expect(panelBox.x).toBeGreaterThanOrEqual(0);
  expect(panelBox.x + panelBox.width).toBeLessThanOrEqual(viewport.width + 1);
  expect(panelBox.y).toBeGreaterThanOrEqual(0);

  await page.keyboard.press("Escape");
  await expect(panel).toHaveCount(0);
  await expect(trigger).toBeFocused();
});

// The panel is not a menu. It holds two radio groups, so the arrows belong to
// the group being read rather than moving between the panel's controls, and each
// pick reports itself as one of several rather than as an independent toggle.
test("the theme and language picks are radio groups with a roving tab stop", async ({ page }) => {
  await page.goto("/overview");
  await page.getByRole("button", { name: "Account" }).first().click();

  const panel = page.getByRole("dialog", { name: "Account" });
  const themes = panel.getByRole("radiogroup", { name: "Theme" });
  const languages = panel.getByRole("radiogroup", { name: "Language" });
  await expect(themes.getByRole("radio")).toHaveCount(3);
  await expect(languages.getByRole("radio")).toHaveCount(2);

  // Exactly one checked per group, which is the property a row of aria-pressed
  // buttons does not express.
  await expect(themes.getByRole("radio", { checked: true })).toHaveCount(1);
  await expect(languages.getByRole("radio", { checked: true })).toHaveCount(1);
  // The group is a single tab stop, so tabbing does not walk all five options.
  expect(await themes.getByRole("radio", { checked: true }).getAttribute("tabindex")).toBe("0");
  const unchecked = themes.getByRole("radio", { checked: false }).first();
  expect(await unchecked.getAttribute("tabindex")).toBe("-1");

  // Arrow keys move the selection inside the group without leaving the panel,
  // and picking does not dismiss it: both picks can be set in one visit.
  const before = await themes.getByRole("radio", { checked: true }).textContent();
  await themes.getByRole("radio", { checked: true }).focus();
  // Held across a frame rather than sent as an instant press: Radix moves focus
  // from a deferred callback and only selects the item it lands on if the arrow
  // is still down when that runs. A real key press always spans that gap.
  await page.keyboard.down("ArrowDown");
  await page.waitForTimeout(100);
  await page.keyboard.up("ArrowDown");
  await expect
    .poll(async () => themes.getByRole("radio", { checked: true }).textContent())
    .not.toBe(before);
  await expect(panel).toBeVisible();

  await expect(page.locator("html")).toHaveAttribute("data-theme", /^(light|dark)$/);
});

// Trace rows carry nine metrics; the chips show the icon and the number only and
// keep the label plus the unformatted value in the hover tooltip.
test("trace metric chips hide the label in the tooltip and scale their values", async ({ page }) => {
  await page.goto("/traces");

  await expect(page.locator(".trace-row").first()).toBeVisible();
  // No chip renders its label any more; the label lives in the title attribute.
  await expect(page.locator(".latency-metric-label, .mini-token-label, .token-badge-label")).toHaveCount(0);
  await expect(page.getByTitle(/^total duration · 1,200 ms$/)).toHaveAttribute("title", /^total duration · [\d,]+ ms$/);

  // 125000 ms renders as minutes, 1000 ms stays in seconds.
  await expect(page.getByTitle(/^total duration · 125,000 ms$/)).toHaveText("2m 5s");
  await expect(page.getByTitle(/^time to first token · 1,000 ms$/)).toHaveText("1s");

  // 112055 tok/s compacts to "112k tok/s" while the raw number stays on hover.
  await expect(page.getByTitle(/^prefill speed · 112,055 tok\/s$/)).toHaveText("112k tok/s");

  // Token counts keep their own formatting, with the operand pair on hover.
  await expect(page.getByTitle(/^cache hit rate · 46\.16% \(96,055 \/ 208,110\)$/)).toHaveText("46.2%");
  await expect(page.getByTitle(/^input tokens · 112,055$/)).toHaveText("112.1K");
  await expect(page.getByTitle(/^cached tokens · 96,055$/)).toHaveText("96.1K");
});

// The trace detail toolbar reuses the same chips, plus the cache hit rate that
// used to exist only on the request rows.
test("trace detail toolbar shows token chips and the cache rate", async ({ page }) => {
  await page.goto("/traces/trace-routed");

  const toolbar = page.locator(".detail-toolbar-tokens");
  await expect(toolbar.locator(".token-badge")).toHaveCount(5);
  await expect(toolbar.locator(".token-badge-label")).toHaveCount(0);
  await expect(toolbar.getByTitle(/^input tokens · [\d,]+$/)).toBeVisible();
  await expect(toolbar.getByTitle(/^total tokens · [\d,]+$/)).toBeVisible();
  await expect(toolbar.getByTitle(/^cache hit rate · .*% \([\d,]+ \/ [\d,]+\)$/)).toBeVisible();
});

// Messages live in per-language chunks that are fetched on demand. Before the
// split every Chinese and English string was inlined in the entry chunk, so both
// languages were downloaded by every reader.
function recordLocaleChunks(page) {
  const locales = [];
  page.on("request", (request) => {
    const match = new URL(request.url()).pathname.match(/\/(zh-CN|en)-[A-Za-z0-9_-]+\.js$/);
    if (match) {
      locales.push(match[1]);
    }
  });
  return locales;
}

test("only the active language chunk is fetched", async ({ page }) => {
  const locales = recordLocaleChunks(page);
  await page.goto("/overview");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();
  expect(locales).toContain("en");
  expect(locales).not.toContain("zh-CN");
});

test("pinning the other language fetches only that chunk", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "zh-CN");
  });
  const locales = recordLocaleChunks(page);
  await page.goto("/overview");
  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible();
  expect(locales).toContain("zh-CN");
  expect(locales).not.toContain("en");
});

// Switching at runtime loads the other chunk and re-renders without a reload.
// Desktop only: the mobile project's emulated viewport scales pointer
// coordinates against a 412px document inside an 826px layout viewport, so
// Playwright's click lands on the dialog container instead of the button.
// elementFromPoint at the button's own centre does resolve to the button, so
// this is an emulation artifact rather than a hit-testing bug.
test("switching language at runtime loads the other chunk", async ({ page, isMobile }) => {
  test.skip(isMobile, "mobile emulation scales click coordinates off the button");
  const locales = recordLocaleChunks(page);
  await page.goto("/overview");
  await expect(page.getByRole("heading", { name: "Overview" })).toBeVisible();

  await page.getByRole("button", { name: "Account" }).first().click();
  await page.getByRole("dialog", { name: "Account" }).getByRole("radio", { name: "中文" }).click();
  await page.keyboard.press("Escape");

  await expect(page.getByRole("heading", { name: "概览" })).toBeVisible();
  expect(locales).toContain("zh-CN");
});

// The stream carries the JWT in an Authorization header rather than in the query
// string, which is why it is SSE over fetch and not an EventSource. The polled
// summary says 18 unread; the streamed one says 7, so the badge proves which of
// the two reached the UI. A heartbeat comment rides along in the same body to
// pin that it is dropped rather than parsed as an event.
test("the event stream drives the unread badge", async ({ page }) => {
  await page.route("**/api/events/stream", async (route) => {
    // The opening poll and the opening stream start together, and the poll
    // replaces the whole summary when it lands, so a streamed value that arrives
    // first is overwritten by it. Holding the stream back until the poll has
    // settled is what makes this an assertion about the stream rather than about
    // which of the two won the mount race.
    await new Promise((resolve) => setTimeout(resolve, 400));
    await route.fulfill({
      status: 200,
      headers: { "content-type": "text/event-stream", "cache-control": "no-cache" },
      body: ': heartbeat\n\nevent: system_event.summary\ndata: {"unread":7}\n\n',
    });
  });

  await page.goto("/overview");
  // The nav renders a badge per counter, and the events entry is rendered twice
  // (the rail and its narrow-viewport duplicate), so this names one of them.
  // `.nav-item-badge` alone matches three and fails in strict mode.
  await expect(page.locator('a[href="/events"] .nav-item-badge').first()).toHaveText("7");
});

// The date formatters hardcoded "zh-CN", so the English console printed
// Chinese-ordered dates under English labels. They resolve the language when they
// format, so a switch re-formats the rows already on screen rather than waiting
// for the next fetch.
test("the rendered timestamps are formatted in the active language", async ({ page, isMobile }) => {
  test.skip(isMobile, "mobile emulation scales click coordinates off the account button");
  await page.goto("/traces");
  const stamp = page.locator(".trace-subline").first();
  await expect(stamp).toBeVisible();
  // English: day first, and a meridiem on the time.
  await expect(stamp).toHaveText(/^\d{2}\/\d{2}\/\d{4}, \d{2}:\d{2}:\d{2}\s?(AM|PM)$/);

  await page.getByRole("button", { name: "Account" }).first().click();
  await page.getByRole("dialog", { name: "Account" }).getByRole("radio", { name: "中文" }).click();
  await page.keyboard.press("Escape");

  // Chinese: year first, 24-hour, and no meridiem.
  await expect(stamp).toHaveText(/^\d{4}\/\d{2}\/\d{2} \d{2}:\d{2}:\d{2}$/);
});

// i18next is configured with single-brace interpolation and with both the key
// and namespace separators disabled, because the keys are flat names that
// contain dots. Each of these is a case that configuration exists for, observed
// through the rendered UI rather than through the module.
test("flat dotted keys and single-brace interpolation render through the UI", async ({ page }) => {
  await page.goto("/traces");

  // A dotted key resolves literally, not as a path into a nested object.
  await expect(page.getByTitle(/^total duration · /).first()).toBeVisible();
  // Interpolation uses single braces. `/models` renders providers.missingUsage,
  // which passes a `count`; there is no plural key, so i18next must resolve the
  // base key rather than falling back to the key name.
  await page.goto("/models");
  await expect(page.getByText(/^1 missing usage$/).first()).toBeVisible();
  // The remaining edge cases - a message whose whole value is a brace pair, and
  // a key no language defines - have no screen that renders them on demand, so
  // they live in tests-unit/i18n.test.js instead.
});

// recharts and its dependency tree are about 360 kB, a third of the bundle, and
// only four pages draw a chart. They are a lazy chunk now, so the entry stays
// free of them and a reader who never opens a chart page never downloads them.
test("the chart library is a lazy chunk fetched only where a chart is drawn", async ({ page }) => {
  const charts = [];
  page.on("response", (response) => {
    if (/\/ChartsImpl-[A-Za-z0-9_-]+\.js$/.test(new URL(response.url()).pathname)) {
      charts.push(response.url());
    }
  });

  // The events page has no chart on it.
  await page.goto("/events");
  await expect(page.getByRole("heading", { name: "Events", exact: true })).toBeVisible();
  expect(charts).toEqual([]);

  // The providers page draws two of them, and the placeholder is replaced by the
  // real thing once the chunk resolves.
  await page.goto("/providers");
  await expect(page.getByRole("heading", { name: "Providers", exact: true })).toBeVisible();
  await expect(page.locator(".recharts-surface").first()).toBeVisible();
  expect(charts.length).toBe(1);
});

// Every write handler used to bump a per-page `refreshTick` counter that the
// reads were keyed on. That counter is gone: a write now invalidates the query
// cache. If invalidation did not reach the read, the page would silently keep
// showing pre-write data, so the refetch itself is what this asserts.
test("a write refetches the read it invalidates", async ({ page }) => {
  const listReads = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/api/events" && request.method() === "GET") {
      listReads.push(url.search);
    }
  });

  await page.goto("/events");
  await expect(page.getByText("analysis job failed").first()).toBeVisible();
  expect(listReads.length).toBe(1);

  await page.getByRole("button", { name: "Mark all read" }).click();
  await expect.poll(() => listReads.length).toBe(2);
});


// The tab strip used to declare role="tablist" and a roving tabindex without
// implementing any of the keyboard behaviour that goes with them, so with only
// the active tab focusable the other tabs on a page could only be reached with
// a mouse. Radix implements the pattern; these three assertions are the parts
// of it that would silently regress.
test("page tabs are reachable and operable from the keyboard", async ({ page }) => {
  await page.goto("/traces");
  const requests = page.getByRole("tab", { name: "Requests" });
  const sessions = page.getByRole("tab", { name: "Sessions" });

  // The strip is one stop in the tab order: Radix makes the list itself the tab
  // stop and moves focus to the active tab when it is entered, so the triggers
  // carry tabindex="-1" until then. Tab therefore reaches the strip, and lands
  // on the tab that is selected rather than on the first one.
  await expect(page.getByRole("tablist")).toHaveAttribute("tabindex", "0");
  await expect(sessions).toHaveAttribute("tabindex", "-1");
  await page.getByRole("tablist").focus();
  await expect(requests).toBeFocused();

  await page.keyboard.press("ArrowRight");

  await expect(sessions).toBeFocused();
  await expect(sessions).toHaveAttribute("aria-selected", "true");
  await expect(page).toHaveURL(/\/traces\?tab=sessions$/);

  // Home and End are part of the pattern too.
  await page.keyboard.press("Home");
  await expect(requests).toBeFocused();
  await expect(page).toHaveURL(/\/traces$/);
});

// A hand-rolled modal handled Escape and the backdrop, and left everything else
// to the reader: focus stayed on the page behind it, Tab could walk out, and
// the background kept scrolling. Radix does all of it, so all of it is asserted.
test("dialogs trap focus, close on Escape and restore focus to the trigger", async ({ page }) => {
  await page.goto("/providers");
  const account = page.getByRole("button", { name: "Account" }).first();
  await account.click();
  // The password form is a dialog, not a panel: it is a page-level interruption
  // with a submit button, so it keeps the focus trap and the scroll lock.
  await page.getByRole("dialog", { name: "Account" }).getByRole("button", { name: "Change password" }).click();

  const dialog = page.getByRole("dialog", { name: "Change password" });
  await expect(dialog).toBeVisible();
  // Focus moved into the dialog rather than staying on the page behind it.
  await expect(dialog.locator(":focus")).toHaveCount(1);
  // The page behind cannot scroll while a modal is open.
  expect(await page.evaluate(() => getComputedStyle(document.body).overflow)).toBe("hidden");

  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  // Focus goes back to the button that opened the form, so the reader is not
  // dropped at the top of the page.
  await expect(account).toBeFocused();
});

// The provider edit form on the channel detail page was the last dialog still
// written by hand - a `createPortal` into `.nav-modal-backdrop` with no focus
// trap and no focus restore - so it is the one that a future edit could quietly
// leave behind when the shared primitive changes.
test("the provider edit dialog is the shared dialog primitive", async ({ page }) => {
  await page.goto("/providers/openai-primary");
  const trigger = page.getByRole("button", { name: "Edit provider" }).first();
  await trigger.click();

  const dialog = page.getByRole("dialog", { name: "Edit provider" });
  await expect(dialog).toBeVisible();
  await expect(dialog.locator(":focus")).toHaveCount(1);
  // It keeps its own width rather than the default card's 560px. Below 720px the
  // viewport is the cap, which is what `min(720px, 100%)` means.
  const width = await dialog.evaluate((e) => ({
    actual: parseFloat(getComputedStyle(e).width),
    cap: Math.min(720, window.innerWidth),
  }));
  expect(Math.round(width.actual)).toBe(Math.round(width.cap));

  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();
});



// A background that is too dark to be a light-mode surface, reported with the
// selector that produced it. Chromium reports a colour declared in OKLCH back as
// `oklch()`, so the lightness is read from it directly; anything else is
// converted. Shared so a new surface cannot be added to the app without a way to
// sweep it by the same rule.
async function darkSurfaces(page, selectors) {
  return page.evaluate((selectors) => {
    const lightness = (colour) => {
      const oklch = colour.match(/^oklch\(([\d.]+)/);
      if (oklch) return Number(oklch[1]);
      const nums = colour.match(/[\d.]+/g);
      if (!nums || nums.length < 3) return null;
      const [r, g, b] = nums.map(Number).map((c) => c / 255);
      return 0.2126 * r + 0.7152 * g + 0.0722 * b;
    };
    const out = [];
    for (const selector of selectors) {
      for (const el of document.querySelectorAll(selector)) {
        const background = getComputedStyle(el).backgroundColor;
        if (background === "rgba(0, 0, 0, 0)" || background === "transparent") continue;
        const l = lightness(background);
        if (l !== null && l < 0.7) out.push(`${selector} -> ${background}`);
      }
    }
    return [...new Set(out)];
  }, selectors);
}

// The palette is generated OKLCH: custom properties in two theme blocks that
// `applyTheme()` and an inline script in index.html select between. None of that
// is visible to a build, so these assert the two ways it can silently break -
// a colour syntax the browser rejects, which leaves the property empty, and a
// theme that is not the one the reader asked for.
// The light-theme overrides used to be written twice: once for
// `data-theme="light"` and once inside `@media (prefers-color-scheme: light)`
// for `data-theme="system"`. Resolving the theme in JavaScript left the second
// copy unreachable, and folding the two together is the kind of edit that can
// hand one component another's declaration. This sweeps every route in light
// mode and asserts that nothing that should be a light surface came out dark:
// the panels, cards, tables, filters, buttons and tags are all near-white.
test("no light-mode surface resolves dark on any route", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  const ROUTES = ["/overview", "/traces", "/events", "/audit", "/models", "/providers", "/routing", "/connect", "/system"];
  // Elements with no intentional dark state. `.ghost-button.active`, `.badge-live`
  // and the accent fills are deliberately inverted or coloured and are excluded.
  const SURFACES = [
    "header",
    ".panel",
    ".stat-card",
    ".trace-table",
    ".filter-bar",
    ".ghost-button:not(.active)",
    ".icon-button",
    ".inline-tag",
    ".detail-meta-pill",
    ".provider-model-card",
    ".model-catalog-row",
    ".timeline-card",
    ".payload-card",
    ".breakdown-card",
  ];
  const offenders = [];
  for (const route of ROUTES) {
    await page.goto(route);
    await expect(page.locator("main, .auth-screen, .app-shell").first()).toBeVisible();
    const found = await darkSurfaces(page, SURFACES);
    if (found.length) offenders.push(`${route}: ${found.join(", ")}`);
  }
  expect(offenders).toEqual([]);
});

// The account panel is only in the document while it is open, so the route sweep
// above cannot see it. It is a new surface on new tokens, which is exactly the
// shape of thing that came out dark in light mode before.
test("the account panel is a light surface in light mode", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/overview");
  await page.getByRole("button", { name: "Account" }).first().click();
  const panel = page.getByRole("dialog", { name: "Account" });
  await expect(panel).toBeVisible();

  expect(await darkSurfaces(page, ['[role="dialog"][aria-label="Account"]', '[role="dialog"][aria-label="Account"] .account-avatar'])).toEqual([]);

  // And the panel's own text is the light theme's foreground, not the dark one.
  const colours = await panel.evaluate((el) => ({
    background: getComputedStyle(el).backgroundColor,
    text: getComputedStyle(el.querySelector("strong")).color,
  }));
  expect(colours.background).not.toBe(colours.text);
  expect(colours.text).toMatch(/oklch\(0\.[0-4]/);
});

test("each theme resolves to real colours", async ({ page }) => {
  const seen = {};
  for (const scheme of ["dark", "light"]) {
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto("/overview");
    await expect(page.getByRole("heading", { name: "Overview", exact: true }).first()).toBeVisible();
    const state = await page.evaluate(() => {
      const root = getComputedStyle(document.documentElement);
      return {
        attribute: document.documentElement.dataset.theme,
        colorScheme: root.colorScheme,
        canvas: root.getPropertyValue("--bg-canvas").trim(),
        text: root.getPropertyValue("--text-primary").trim(),
        // color-mix() output, so a rejected value would read as an empty string.
        border: root.getPropertyValue("--border").trim(),
        bodyBackground: getComputedStyle(document.body).backgroundColor,
      };
    });
    expect(state.attribute).toBe(scheme);
    expect(state.colorScheme).toBe(scheme);
    for (const token of ["canvas", "text", "border"]) {
      expect(state[token], `--${token} is empty in the ${scheme} theme`).not.toBe("");
    }
    expect(state.bodyBackground).not.toBe("rgba(0, 0, 0, 0)");
    seen[scheme] = state.bodyBackground;
  }
  // Dark really is the darker of the two, not just a differently named theme.
  const luminance = (rgb) => rgb.match(/[\d.]+/g).slice(0, 3).map(Number).map((c) => c / 255).reduce((sum, c) => sum + c, 0);
  expect(luminance(seen.dark)).toBeLessThan(luminance(seen.light));
});

// A stored preference beats the OS setting, in either direction.
test("the stored theme preference overrides the system one", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.addInitScript(() => window.localStorage.setItem("trajecta.monitor.theme", "dark"));
  await page.goto("/overview");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});

test("following the system theme picks up a change while the page is open", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto("/overview");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
  await page.emulateMedia({ colorScheme: "light" });
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
});

// The tab underline is one element shared by the strip: motion's `layoutId`
// moves it between triggers rather than each trigger owning a copy. These assert
// the arrangement the animation depends on, which a CSS-only regression would
// not catch - a per-trigger underline would look identical standing still.
test("the tab strip shares a single indicator, under the active tab", async ({ page }) => {
  await page.goto("/traces");
  const under = async () => page.evaluate(() =>
    [...document.querySelectorAll("[data-tab-indicator]")].map((e) => e.parentElement.textContent.trim()),
  );
  // The strip renders after the page's first data request, so poll rather than
  // reading the DOM the instant the document is ready.
  await expect.poll(under).toEqual(["Requests"]);
  const requests = await page.evaluate(() => document.querySelector("[data-tab-indicator]")?.getBoundingClientRect().width);
  const tabWidth = await page.getByRole("tab", { name: "Requests" }).evaluate((e) => e.getBoundingClientRect().width);
  expect(Math.round(requests)).toBe(Math.round(tabWidth));

  await page.getByRole("tab", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/tab=sessions$/);
  // Still exactly one, and it has followed the selection.
  await expect.poll(under).toEqual(["Sessions"]);
});

test("reduced motion turns the dialog animation off", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/providers");
  // The provider form rather than the account panel: the reduced-motion rule is
  // about the dialog keyframes, and the account surface is a popover with none.
  await page.getByRole("button", { name: "New provider" }).click();
  const content = page.locator(".nav-modal");
  await expect(content).toBeVisible();
  expect(await content.evaluate((e) => getComputedStyle(e).animationName)).toBe("none");
  // And the dialog still closes, which is the part an exit animation can break.
  await page.keyboard.press("Escape");
  await expect(content).toHaveCount(0);
});




