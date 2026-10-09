import { expect, test } from "@playwright/test";

test.beforeEach(async ({ page }) => {
  // The Monitor UI defaults to Chinese when no language is persisted, while
  // this suite asserts English labels. Pin the language for every test.
  await page.addInitScript(() => {
    window.localStorage.setItem("trajecta.monitor.language", "en");
  });
});

test("real monitor server renders seeded model and channel data", async ({ page }) => {
  await page.goto("/models");
  await expect(page.getByRole("heading", { name: "Models", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: /gpt-5/i })).toBeVisible();
  await expect(page.getByText("openai-primary").first()).toBeVisible();

  await page.getByRole("link", { name: /gpt-5/i }).first().click();
  await expect(page.getByRole("heading", { name: "gpt-5" })).toBeVisible();
  await expect(page.getByText("Provider coverage")).toBeVisible();
  await expect(page.getByRole("link", { name: /openai-primary manual enabled/i })).toBeVisible();

  await page.goto("/providers/openai-primary");
  await expect(page.getByRole("heading", { name: "OpenAI Primary" })).toBeVisible();
  await expect(page.getByText("encrypted-local").first()).toBeVisible();
  await expect(page.getByText("rate limited")).toBeVisible();

  await page.getByRole("button", { name: "Edit provider" }).click();
  await expect(page.getByRole("heading", { name: "Edit provider" })).toBeVisible();
  // The preset is a listbox now, so it shows the option's label rather than a
  // DOM value.
  await expect(page.getByLabel("Provider preset")).toHaveText("openai");
  await page.getByRole("button", { name: "Advanced options" }).click();
  await expect(page.locator("textarea")).toContainText("Authorization: ***");
  await expect(page.locator("textarea")).toContainText("X-Test: visible");
});

test("real monitor server supports probe and manual model mutation", async ({ page }) => {
  await page.goto("/providers/openai-primary");

  await page.getByPlaceholder("Add model manually").fill("gpt-manual-real");
  await page.getByRole("button", { name: "Add model" }).click();
  await expect(page.getByText("gpt-manual-real")).toBeVisible();

  await page.getByRole("button", { name: "Probe provider" }).click();
  await expect(page.getByText("success").first()).toBeVisible();
  await expect(page.getByText("discovered, disabled").first()).toBeVisible();
  await page.getByRole("button", { name: "Enable discovered (1)" }).click();
  await expect(page.getByText("gpt-new-real")).toBeVisible();
});

test("real monitor server shows probe failure guidance", async ({ page }) => {
  await page.goto("/providers/broken-auth");
  await expect(page.getByRole("heading", { name: "Broken Auth" })).toBeVisible();

  await page.getByRole("button", { name: "Probe provider" }).click();
  await expect(page.getByText("not_found").first()).toBeVisible();
  await expect(page.getByText(/Check the base URL/i).first()).toBeVisible();
});

test("real monitor server renders routing decision records", async ({ page }) => {
  await page.goto("/routing");
  await expect(page.getByRole("heading", { name: "Routing" })).toBeVisible();
  await expect(page.getByText("Recent selected routes")).toBeVisible();
  await expect(page.getByText("Routed requests")).toBeVisible();
  await expect(page.getByText("gpt-5").first()).toBeVisible();
  await expect(page.getByRole("link", { name: "View trace" }).first()).toBeVisible();
});

test("real monitor server serves trace routing links", async ({ page }) => {
  const fixture = await page.request.get("/__fixture/state").then((response) => response.json());
  // The routing decision is the request's own fact, so it lives on the request
  // tab now that the first tab is the conversation alone.
  await page.goto(`/traces/${fixture.routed_trace_id}?tab=request`);
  await expect(page.getByRole("heading", { name: "Selected route target" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Open Channel" })).toHaveAttribute("href", "/providers/openai-primary");
  await expect(page.getByRole("link", { name: "Open Upstream" })).toHaveAttribute("href", "/upstreams/openai-primary");

  // The conversation reads the trace whether or not its observation has been
  // parsed: the parsed steps when there are nodes, the recorded messages when
  // there are not.
  await page.getByRole("tab", { name: "Conversation" }).click();
  await expect(page.locator(".conversation-list, .message-list")).toBeVisible();
});

// The console has no refresh timers: a page becomes fresh because the server
// pushed a topic at it. This drives the real Go server rather than a mock - the
// socket the browser opens is the one the binary serves, and the write that
// triggers the push is made out of band through the HTTP API, so the frame
// cannot be an echo of something the page itself did.
test("real monitor server pushes a store change to the console socket", async ({ page }) => {
  // The listeners are attached the moment the socket is created. Waiting for
  // the creation event and attaching afterwards loses the subscription frame:
  // the page sends it from `onopen`, which can happen before the test's next
  // line runs.
  const sent = [];
  const received = [];
  page.on("websocket", (socket) => {
    if (!socket.url().includes("/api/events/ws")) {
      return;
    }
    socket.on("framesent", (frame) => sent.push(String(frame.payload)));
    socket.on("framereceived", (frame) => received.push(String(frame.payload)));
  });

  await page.goto("/traces");
  await expect(page.getByRole("heading", { name: "Traffic" })).toBeVisible();

  // The browser has to tell the server what it is showing, or there is nothing
  // to push to it.
  await expect
    .poll(() => sent.some((payload) => payload.includes("traffic")), { timeout: 8_000 })
    .toBe(true);

  const traceID = (await (await page.request.get("/api/traces?page_size=1")).json())?.items?.[0]?.id;
  expect(traceID, "the fixture server seeded no traces").toBeTruthy();

  const listRequests = [];
  page.on("request", (request) => {
    if (new URL(request.url()).pathname === "/api/traces") {
      listRequests.push(Date.now());
    }
  });
  const before = listRequests.length;

  // A reanalysis writes an analysis run, which is a traffic change. Nothing in
  // the page asked for it, so a refetch afterwards can only come from the push.
  const response = await page.request.post(`/api/traces/${encodeURIComponent(traceID)}/reanalyze`, { data: {} });
  expect(response.ok(), `reanalyze answered ${response.status()}`).toBeTruthy();

  await expect
    .poll(() => received.some((payload) => payload.includes("traffic")), { timeout: 10_000 })
    .toBe(true);
  await expect.poll(() => listRequests.length, { timeout: 10_000 }).toBeGreaterThan(before);
});
