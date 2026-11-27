import { expect, test } from "@playwright/test";

test("embedded Monitor loads its theme, modules and websocket under production CSP", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("trajecta.monitor.language", "en");
    localStorage.setItem("trajecta.monitor.theme", "light");
    window.cspViolations = [];
    document.addEventListener("securitypolicyviolation", (event) => {
      window.cspViolations.push({ directive: event.effectiveDirective, uri: event.blockedURI });
    });
  });
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const frames = [];
  page.on("websocket", (socket) => socket.on("framereceived", (event) => frames.push(event.payload)));
  const response = await page.goto("/models");
  const headers = response.headers();
  expect(headers["content-security-policy"]).toContain("script-src 'self';");
  expect(headers["x-frame-options"]).toBe("DENY");
  expect(headers["x-content-type-options"]).toBe("nosniff");
  await expect(page.getByRole("heading", { name: "Models", exact: true })).toBeVisible();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  expect((await page.request.get("/theme-init.js")).ok()).toBeTruthy();
  await expect.poll(() => frames.length).toBeGreaterThan(0);
  expect(errors).toEqual([]);
  expect(await page.evaluate(() => window.cspViolations)).toEqual([]);

  // A genuine inserted inline script is blocked, unlike automation evaluate.
  await page.evaluate(() => {
    const script = document.createElement("script");
    script.textContent = "window.untrustedInlineRan = true";
    document.head.append(script);
  });
  await expect.poll(() => page.evaluate(() => window.cspViolations.length)).toBeGreaterThan(0);
  expect(await page.evaluate(() => window.untrustedInlineRan)).toBeUndefined();
  expect(await page.evaluate(() => window.cspViolations.some((v) => v.uri === "inline" && v.directive.startsWith("script-src")))).toBeTruthy();
});
