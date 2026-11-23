import { expect, test } from "@playwright/test";

/*
 * The 轨迹 and 统计 tabs.
 *
 * Both read one derived window from `/api/traces/<id>/session-steps`, so the
 * spec mocks that endpoint with two pages in each direction and asserts the
 * behaviour a reader depends on: the window grows without losing the rows
 * already on screen, the turns stay contiguous, and a turn that was open stays
 * open when older history is prepended and the ordinals shift.
 */

const streamStep = (globalIndex, overrides = {}) => ({
  index: 1,
  global_index: globalIndex,
  trace_id: `trace-${globalIndex}`,
  model: "gpt-test",
  provider: "openai_compatible",
  status: 200,
  is_stream: true,
  started_at: "2026-05-01T09:00:00Z",
  ended_at: "2026-05-01T09:00:01Z",
  usage: { input: 10, cache_read: 90, cache_write: 0, context: 100, output: 20, cache_hit: 0.9 },
  timing: { ttft_ms: 200, model_ms: 1000, decode_ms: 800, tps: 25, tool_wait_ms: null, wall_ms: 1000 },
  did: { tools: [{ name: "bash", calls: 1 }], calls: 1, answer_chars: 4 },
  content: {
    tool_calls: [{ id: `call-${globalIndex}`, name: "bash", args: '{"command":"ls"}' }],
    tool_results: [],
    answer: "done",
  },
  ...overrides,
});

const turn = (index, steps, trigger = "hello") => ({
  index,
  started_at: "2026-05-01T09:00:00Z",
  ended_at: "2026-05-01T09:00:01Z",
  wall_ms: 1000,
  trigger: { kind: "user", text: trigger, chars: trigger.length },
  steps,
});

const totals = {
  turns: null,
  steps: 5,
  input: 50,
  cache_read: 450,
  cache_write: 0,
  context: 500,
  output: 100,
  cache_hit: 0.9,
  ttft_ms: 1000,
  model_ms: 5000,
  wall_ms: 9000,
};

const firstPage = {
  session_id: "sess-1",
  builder_version: 3,
  focus_trace_id: "trace-4",
  window: { returned: 2, first_step: 3, last_step: 4, has_before: true, has_after: true, content_pending: 0, budget_ms: 4000 },
  totals,
  turns: [turn(1, [streamStep(3)]), turn(2, [streamStep(4)])],
};

const earlierPage = {
  ...firstPage,
  window: { returned: 2, first_step: 1, last_step: 2, has_before: false, has_after: true, content_pending: 0, budget_ms: 4000 },
  turns: [turn(1, [streamStep(1), streamStep(2)])],
};

const laterPage = {
  ...firstPage,
  window: { returned: 1, first_step: 5, last_step: 5, has_before: true, has_after: false, content_pending: 0, budget_ms: 4000 },
  turns: [turn(1, [streamStep(5)])],
};

const traceDetail = {
  id: "trace-1",
  header: {
    version: "LLM_PROXY_V3",
    meta: { model: "gpt-test", endpoint: "/v1/chat/completions", duration_ms: 1000, status_code: 200 },
    layout: { is_stream: true },
  },
  session: { session_id: "sess-1", session_source: "header.session_id" },
  audit: { total: 0 },
  tools: [],
  tool_calls: [],
  messages: [],
  project: { root: "/data/src/github.com/kingfs/Trajecta", root_key: "/data/src/github.com/kingfs/Trajecta", source: "dsh_runtime_context" },
};

const observation = {
  id: "trace-1",
  summary: {
    trace_id: "trace-1",
    parser: "openai",
    parser_version: "0.1.0",
    status: "parsed",
    provider: "openai_compatible",
    operation: "chat.completions",
    model: "gpt-test",
    warnings: [],
  },
  nodes: [],
  request_tree: [],
  response_tree: [],
  usage: { input_tokens: 1200, output_tokens: 20, total_tokens: 1220, cache_read_tokens: 900 },
  timings: { duration_ms: 1000, ttft_ms: 200 },
};

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("trajecta.monitor.language", "en");
    localStorage.setItem("trajecta.monitor.token", "test-monitor-jwt");
  });
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/auth/status") return route.fulfill({ json: { auth_required: false } });
    if (url.pathname === "/api/traces/trace-1") return route.fulfill({ json: traceDetail });
    if (url.pathname === "/api/traces/trace-1/observation") return route.fulfill({ json: observation });
    if (url.pathname === "/api/traces/trace-1/session-steps") {
      if (url.searchParams.get("before")) return route.fulfill({ json: earlierPage });
      if (url.searchParams.get("after")) return route.fulfill({ json: laterPage });
      return route.fulfill({ json: firstPage });
    }
    return route.fulfill({ json: {} });
  });
});

test("statistics tab pages through the session in both directions", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=stats");
  await expect(page.getByText("Steps 3–4 · 5 in the session")).toBeVisible();
  await expect(page.getByRole("button", { name: /Turn 1/ })).toBeVisible();
  await expect(page.getByRole("button", { name: /Turn 2/ })).toBeVisible();

  // Older history: the list grows upwards and the ordinals stay contiguous,
  // while the step range identifies the turn the reader was looking at.
  await page.getByRole("button", { name: "Load earlier steps" }).click();
  await expect(page.getByRole("button", { name: /Turn 3/ })).toBeVisible();
  await expect(page.getByText("#1–#2")).toBeVisible();
  await expect(page.getByText("#3–#3")).toBeVisible();

  // The newer side is the other direction of the same cursor.
  await page.getByRole("button", { name: "Load later steps" }).click();
  await expect(page.getByRole("button", { name: /Turn 4/ })).toBeVisible();
  await expect(page.getByText("Steps 1–5 · 5 in the session")).toBeVisible();

  // With both ends closed the window is the whole session, so the totals row
  // may claim a turn count.
  await page.getByText("Show totals").click();
  await expect(page.getByText("Window total")).toBeVisible();
});

test("expanding a turn survives older history being prepended", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=stats");
  // The newest turn is open by default; open the other one too.
  await page.locator('[data-turn-key="trace-3"] .steps-scope-toggle').click();
  await expect(page.locator('[data-trace-id="trace-3"]')).toBeVisible();
  await expect(page.locator('[data-trace-id="trace-4"]')).toBeVisible();

  await page.getByRole("button", { name: "Load earlier steps" }).click();
  await expect(page.getByText("#1–#2")).toBeVisible();
  // The ordinals shifted (trace-3 is "Turn 2" now), the identity did not: both
  // steps that were open are still open, and the newly loaded turn is closed.
  await expect(page.locator('[data-trace-id="trace-3"]')).toBeVisible();
  await expect(page.locator('[data-trace-id="trace-4"]')).toBeVisible();
  await expect(page.locator('[data-trace-id="trace-1"]')).toHaveCount(0);
  await expect(page.locator('[data-trace-id="trace-2"]')).toHaveCount(0);
});

test("the timeline draws three lanes and changes width with its mode", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=trajectory");
  await expect(page.locator(".timeline-lane")).toHaveCount(3);
  await expect(page.locator(".timeline-lane .timeline-span-model")).toHaveCount(2);
  await expect(page.locator(".timeline-lane .timeline-span-input")).toHaveCount(2);

  const firstModel = page.locator(".timeline-lane .timeline-span-model").first();
  const sequenceWidth = (await firstModel.boundingBox()).width;
  await page.getByRole("button", { name: "Duration" }).click();
  await expect(page.getByRole("button", { name: "Duration" })).toHaveAttribute("aria-pressed", "true");
  const durationWidth = (await firstModel.boundingBox()).width;
  expect(durationWidth, "real durations widen the model span").toBeGreaterThan(sequenceWidth);
});

test("dragging the timeline filters the ledger", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=trajectory");
  const track = await page.locator(".timeline-track").boundingBox();
  const y = track.y + track.height / 2;
  await page.mouse.move(track.x + track.width * 0.1, y);
  await page.mouse.down();
  await page.mouse.move(track.x + track.width * 0.45, y, { steps: 6 });
  await page.mouse.up();

  await expect(page.getByText(/Time selection/)).toBeVisible();
  await expect(page.locator(".ledger-list [data-trace-id=\"trace-3\"]")).toHaveCount(1);
  await expect(page.locator(".ledger-list [data-trace-id=\"trace-4\"]")).toHaveCount(0);

  // Double-clicking the track clears the range and the ledger comes back.
  await page.locator(".timeline-track").dblclick({ position: { x: 20, y: 10 } });
  await expect(page.getByText(/Time selection/)).toHaveCount(0);
  await expect(page.locator(".ledger-list [data-trace-id=\"trace-4\"]")).toHaveCount(1);
});

test("the timeline tooltip names the span and its clock range", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=trajectory");
  await page.locator(".timeline-lane .timeline-span-model").first().hover();
  const tooltip = page.getByRole("tooltip");
  await expect(tooltip).toBeVisible({ timeout: 3000 });
  await expect(tooltip).toContainText("Model");
  await expect(tooltip).toContainText("→");
});

test("trajectory tab renders the ledger and filters it", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=trajectory");
  await expect(page.getByRole("button", { name: /Turn 1/ })).toBeVisible();
  await expect(page.locator(".ledger-row-tool").first()).toContainText("bash");

  await page.getByRole("searchbox").fill("nothing-matches-this");
  await expect(page.locator(".ledger-row")).toHaveCount(0);
  await page.getByRole("searchbox").fill("bash");
  await expect(page.locator(".ledger-row-tool").first()).toBeVisible();
  await expect(page.getByRole("button", { name: /Turn 1/ })).toBeVisible();
});

test("a ledger search marks the matching spans on the timeline", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=trajectory");
  await expect(page.locator(".timeline-span-match")).toHaveCount(0);

  await page.getByRole("searchbox").fill("bash");
  // Both steps called bash in the fixture, so every span is a match; searching
  // something that only one step contains marks just that step.
  await expect(page.locator(".timeline-span-match")).not.toHaveCount(0);
  await expect(page.locator(".timeline-span-dimmed")).toHaveCount(0);

  await page.getByRole("searchbox").fill("nothing-matches-this");
  await expect(page.locator(".timeline-span-match")).toHaveCount(0);
  await expect(page.locator(".timeline-span-dimmed")).not.toHaveCount(0);
});

test("protocol tab shows the exchange's usage and timing", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=protocol");
  await expect(page.locator("[data-exchange-facts]")).toBeVisible();
  // OpenAI's input counter already includes the cache read, so the context is
  // 1.2K rather than 2.1K and the hit rate is 900/1200.
  await expect(page.locator("[data-exchange-facts]")).toContainText("75.0%");
  await expect(page.locator("[data-exchange-facts]")).toContainText("1.2K");
  await expect(page.locator("[data-exchange-facts]")).toContainText("0.2s");
});

test("the project a harness declared is shown on the trace and session pages", async ({ page }) => {
  await page.goto("/traces/trace-1?tab=stats");
  const traceProject = page.getByRole("link", { name: "Trajecta" });
  await expect(traceProject).toBeVisible();
  await expect(traceProject).toHaveAttribute("href", /tab=sessions&project=%2Fdata%2Fsrc%2Fgithub.com%2Fkingfs%2FTrajecta/);

  await page.route("**/api/sessions/sess-1/steps*", (route) => route.fulfill({ json: firstPage }));
  await page.route("**/api/sessions/sess-1/trajectory", (route) => route.fulfill({ json: { steps: [], extra: { warnings: [] } } }));
  await page.route("**/api/sessions/sess-1", (route) =>
    route.fulfill({
      json: {
        summary: { session_id: "sess-1", request_count: 5, providers: [], success_rate: 100 },
        project: { root: "/data/src/github.com/kingfs/Trajecta", root_key: "/data/src/github.com/kingfs/Trajecta", source: "dsh_runtime_context" },
        breakdown: { models: [], endpoints: [], failed_traces: 0 },
        timeline: [],
        traces: [],
        analysis: [],
      },
    }),
  );
  await page.goto("/sessions/sess-1");
  await expect(page.getByRole("link", { name: "Trajecta" })).toBeVisible();
});

test("session page reuses the step model in its own tabs", async ({ page }) => {
  await page.route("**/api/sessions/sess-1/steps*", (route) => route.fulfill({ json: firstPage }));
  await page.route("**/api/sessions/sess-1/trajectory", (route) => route.fulfill({ json: { steps: [], extra: { warnings: [] } } }));
  await page.route("**/api/sessions/sess-1", (route) =>
    route.fulfill({
      json: {
        summary: { session_id: "sess-1", request_count: 5, providers: [], success_rate: 100 },
        project: { root: "/data/src/github.com/kingfs/Trajecta", root_key: "/data/src/github.com/kingfs/Trajecta", source: "dsh_runtime_context" },
        breakdown: { models: [], endpoints: [], failed_traces: 0 },
        timeline: [],
        traces: [],
        analysis: [],
      },
    }),
  );

  await page.goto("/sessions/sess-1");
  await page.getByRole("button", { name: "Statistics" }).click();
  await expect(page.getByText("Steps 3–4 · 5 in the session")).toBeVisible();
  await expect(page.getByRole("button", { name: /Turn 1/ })).toBeVisible();
});
