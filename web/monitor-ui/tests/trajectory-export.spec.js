import { expect, test } from "@playwright/test";

const session = {
  summary: { session_id: "export-session", session_source: "header.session_id", request_count: 1, providers: [] },
  traces: [], timeline: [], analysis: [],
};

const record = {
  schema_version: "ATIF-v1.8",
  session_id: "export-session",
  agent: { name: "unknown", version: "unknown" },
  steps: [{ step_id: 1, source: "user", message: "hello" }],
  extra: { warnings: [] },
};

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("trajecta.monitor.language", "en");
    localStorage.setItem("trajecta.monitor.token", "test-monitor-jwt");
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/auth/status") return route.fulfill({ json: { auth_required: false } });
    if (path === "/api/sessions/export-session") return route.fulfill({ json: session });
    return route.fulfill({ json: {} });
  });
});

test("session ATIF export requires a click and downloads authenticated JSONL", async ({ page }) => {
  const reads = [];
  await page.route("**/api/sessions/export-session/trajectory", async (route) => {
    reads.push(route.request().headers().authorization);
    await route.fulfill({ contentType: "application/x-ndjson", body: JSON.stringify(record) + "\n" });
  });
  // The detail page previews the trajectory, so this URL is already read once
  // when the panel mounts. The click is what turns the same URL into a download,
  // so what is asserted here is the request the click adds - and the fact that
  // nothing is downloaded before it.
  const previewRead = page.waitForResponse("**/api/sessions/export-session/trajectory");
  await page.goto("/sessions/export-session");
  await previewRead;
  expect(reads).toHaveLength(1);
  const button = page.getByRole("button", { name: "Export trajectory (ATIF)" });
  await expect(button).toBeEnabled();
  const downloadPromise = page.waitForEvent("download");
  await button.click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("session-export-session.atif.jsonl");
  const stream = await download.createReadStream();
  let body = "";
  for await (const chunk of stream) body += chunk.toString();
  expect(body).toBe(JSON.stringify(record) + "\n");
  await expect(page.getByRole("region", { name: /Notifications/ })).toContainText("Trajectory downloaded. One complete session per JSONL line.");
  expect(reads).toHaveLength(2);
  expect(reads.every((authorization) => authorization === "Bearer test-monitor-jwt")).toBe(true);
});

test("session ATIF export shows a failure and allows retry", async ({ page }) => {
  await page.route("**/api/sessions/export-session/trajectory", (route) => route.fulfill({ status: 404, json: { error: "Session has no recorded requests" } }));
  await page.goto("/sessions/export-session");
  const button = page.getByRole("button", { name: "Export trajectory (ATIF)" });
  await button.click();
  // The panel's read runs into the same 404 as the click, so the message shows
  // up twice - once in the trajectory panel and once in the toast that reports
  // the failed download. The assertion names the page rather than the whole
  // document so it does not collide with the notification.
  await expect(page.getByRole("main").getByText("Session has no recorded requests")).toBeVisible();
  await expect(page.getByRole("region", { name: /Notifications/ })).toContainText("Session has no recorded requests");
  await expect(button).toBeEnabled();
});
