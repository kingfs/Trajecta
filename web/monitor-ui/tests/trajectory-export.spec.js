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
  });
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/auth/status") return route.fulfill({ json: { auth_required: false } });
    if (path === "/api/sessions/export-session") return route.fulfill({ json: session });
    return route.fulfill({ json: {} });
  });
});

test("session ATIF export requires a click and downloads JSONL without exposing credentials", async ({ page }) => {
  const requests = [];
  await page.route("**/api/sessions/export-session/trajectory", async (route) => {
    requests.push({ url: route.request().url(), authorization: route.request().headers().authorization });
    await route.fulfill({ contentType: "application/x-ndjson", body: JSON.stringify(record) + "\n" });
  });
  // The ATIF preview re-parses every cassette of the session, so it is read
  // only when the collapsed block on the 轨迹 tab is opened. What is asserted
  // first is that opening the page reads nothing.
  await page.goto("/sessions/export-session");
  await page.getByRole("button", { name: /^Trajectory/ }).click();
  expect(requests, "the page must not re-parse the session on mount").toHaveLength(0);
  await page.getByText(/ATIF session trajectory/).click();
  await expect.poll(() => requests.length).toBe(1);
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
  expect(requests).toHaveLength(2);
  expect(requests.every(({ url, authorization }) => !url.includes("access_token=") && authorization === undefined)).toBe(true);
});

test("session ATIF export shows a failure and allows retry", async ({ page }) => {
  await page.route("**/api/sessions/export-session/trajectory", (route) => route.fulfill({ status: 404, json: { error: "Session has no recorded requests" } }));
  await page.goto("/sessions/export-session");
  // The ATIF preview lives in the 轨迹 tab now (collapsed until opened), so the
  // error is asserted after opening the tab and the block.
  await page.getByRole("button", { name: /^Trajectory/ }).first().click();
  await page.getByText(/ATIF session trajectory/).click();
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
