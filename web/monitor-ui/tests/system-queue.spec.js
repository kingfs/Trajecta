import { expect, test } from "@playwright/test";

/*
 * The analysis queue panel on the System page.
 *
 * Two lanes with one policy: fresh traffic and the historical sweep are paced
 * separately, the values persist in `app_settings` and reach the running worker
 * immediately. The panel is the only place an operator can slow a sweep down
 * without a redeploy, so the spec pins that a save sends the numbers (not the
 * strings a number input holds) and that the reset only clears the cursor.
 */

const queue = {
  parse_batch_size: 10,
  parse_interval_ms: 5000,
  parse_max_attempts: 3,
  backfill_enabled: true,
  backfill_batch_size: 2,
  backfill_interval_ms: 5000,
  backfill_chunk_size: 1000,
  backfill_low_watermark: 200,
  backfill_scope: "declarers",
  analysis_batch_size: 5,
  analysis_interval_ms: 5000,
  queue: { fresh_queued: 4, backfill_queued: 120, running: 1, failed: 7, attributed: 315 },
  backfill: { project_through: "2026-11-20T10:00:00Z", completed_at: null },
  runtime_attached: true,
};

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("trajecta.monitor.language", "en");
    localStorage.setItem("trajecta.monitor.token", "test-monitor-jwt");
  });
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.pathname === "/api/auth/status") return route.fulfill({ json: { auth_required: false } });
    if (url.pathname === "/api/auth/me") return route.fulfill({ json: { username: "local", role: "local" } });
    if (url.pathname === "/api/settings/analysis-queue" && request.method() === "PATCH") {
      const patch = JSON.parse(request.postData() || "{}");
      const updated = { ...queue, ...patch };
      return route.fulfill({ json: updated });
    }
    if (url.pathname === "/api/settings/analysis-queue") return route.fulfill({ json: queue });
    if (url.pathname === "/api/settings/analysis-queue/reset-backfill") {
      return route.fulfill({ json: { ...queue, backfill: {} } });
    }
    return route.fulfill({ json: {} });
  });
});

test("the queue panel shows both lanes and saves numbers", async ({ page }) => {
  const patches = [];
  page.on("request", (request) => {
    if (request.method() === "PATCH" && request.url().includes("/api/settings/analysis-queue")) {
      patches.push(JSON.parse(request.postData() || "{}"));
    }
  });

  await page.goto("/system?tab=queue");
  await expect(page.getByRole("heading", { name: "Parse and backfill queue" })).toBeVisible();
  await expect(page.getByText("Queued (history)")).toBeVisible();
  await expect(page.getByText("Attributed traces")).toBeVisible();
  await expect(page.getByText("Historical sweep reached")).toBeVisible();

  const batch = page.getByLabel("Batch size").first();
  await batch.fill("40");
  await page.getByRole("button", { name: "Save", exact: true }).click();

  expect(patches).toHaveLength(1);
  expect(patches[0]).toMatchObject({
    parse_batch_size: 40,
    parse_interval_ms: 5000,
    backfill_enabled: true,
    backfill_chunk_size: 1000,
  });
  for (const [key, value] of Object.entries(patches[0])) {
    const isNumber = (key.startsWith("backfill_") || key.startsWith("analysis_")) && key !== "backfill_enabled" && key !== "backfill_scope";
    if (isNumber) expect(typeof value, `${key} must be a number`).toBe("number");
  }
  expect(patches[0].backfill_scope).toBe("declarers");
});

test("re-running the sweep clears the cursor after a confirmation", async ({ page }) => {
  const resets = [];
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().includes("/reset-backfill")) resets.push(request.url());
  });
  page.on("dialog", (dialog) => dialog.accept());

  await page.goto("/system?tab=queue");
  await page.getByRole("button", { name: "Re-run the historical sweep" }).click();

  await expect.poll(() => resets.length).toBe(1);
  await expect(page.getByText("The sweep has not started yet")).toBeVisible();
});
