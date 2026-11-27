import { expect, test } from "@playwright/test";

const sessionCookie = "trajecta_monitor_session=test-browser-session";
const user = { username: "operator", role: "viewer", scope: "all" };

// Exercise browser-managed HttpOnly cookies, not a localStorage token or a mock
// that authorizes every request. These routes model the Monitor auth contract.
async function mockAuth(page, { acceptLoginCookie = true, logoutStatus = 200, abortLogout = false } = {}) {
  const requests = [];
  await page.routeWebSocket(/\/api\/events\/ws/, () => {});
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const headers = await request.allHeaders();
    requests.push({ path, headers, method: request.method() });
    const authenticated = headers.cookie?.includes(sessionCookie);
    if (path === "/api/auth/status") return route.fulfill({ json: { auth_required: true } });
    if (path === "/api/auth/check" || path === "/api/auth/me") {
      return route.fulfill({
        status: authenticated ? 200 : 401,
        json: authenticated ? (path.endsWith("/me") ? user : { ok: true }) : { error: "unauthorized" },
      });
    }
    if (path === "/api/auth/login") {
      expect(request.method()).toBe("POST");
      expect(request.postDataJSON()).toEqual({ username: "operator", password: "test-password" });
      return route.fulfill({
        headers: acceptLoginCookie ? { "Set-Cookie": `${sessionCookie}; Path=/api; HttpOnly; SameSite=Lax` } : {},
        json: { ok: true },
      });
    }
    if (path === "/api/auth/logout") {
      expect(request.method()).toBe("POST");
      if (abortLogout) return route.abort("failed");
      return route.fulfill({
        status: logoutStatus,
        headers: logoutStatus === 200 ? { "Set-Cookie": "trajecta_monitor_session=; Path=/api; HttpOnly; SameSite=Lax; Max-Age=0" } : {},
        json: logoutStatus === 200 ? { ok: true } : { error: "Logout temporarily unavailable" },
      });
    }
    return route.fulfill({ status: authenticated ? 200 : 401, json: authenticated ? {} : { error: "unauthorized" } });
  });
  return requests;
}

async function submitLogin(page) {
  await page.goto("/overview");
  await expect(page.getByRole("heading", { name: "Sign in to monitor" })).toBeVisible();
  await page.getByLabel("Username", { exact: true }).fill("operator");
  await page.getByLabel("Password", { exact: true }).fill("test-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
}

async function openSignOut(page) {
  await expect(page.getByRole("main")).toBeVisible();
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
}

test.beforeEach(async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("trajecta.monitor.language", "en"));
});

test("login 200 without an accepted cookie cannot enter the application", async ({ page, context }) => {
  const requests = await mockAuth(page, { acceptLoginCookie: false });
  await submitLogin(page);
  await expect(page.getByText("Invalid username or password.", { exact: true })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Sign in to monitor" })).toBeVisible();
  await expect(page.getByRole("main")).toHaveCount(0);
  expect(requests.some(({ path }) => path === "/api/auth/me")).toBe(true);
  expect((await context.cookies()).some(({ name }) => name === "trajecta_monitor_session")).toBe(false);
  expect(await page.evaluate(() => localStorage.getItem("trajecta.monitor.token"))).toBeNull();
});

test("logout 500 keeps the authenticated screen and reports the failure", async ({ page, context }) => {
  await mockAuth(page, { logoutStatus: 500 });
  await submitLogin(page);
  await openSignOut(page);
  await expect(page.getByRole("region", { name: /Notifications/ })).toContainText("Logout temporarily unavailable");
  await expect(page.getByRole("main")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Sign in to monitor" })).toHaveCount(0);
  expect((await context.cookies()).some(({ name }) => name === "trajecta_monitor_session")).toBe(true);
});

test("logout network failure keeps the authenticated screen and reports the failure", async ({ page, context }) => {
  await mockAuth(page, { abortLogout: true });
  await submitLogin(page);
  await openSignOut(page);
  await expect(page.getByRole("region", { name: /Notifications/ })).toContainText(/fetch/i);
  await expect(page.getByRole("main")).toBeVisible();
  await expect(page.getByRole("heading", { name: "Sign in to monitor" })).toHaveCount(0);
  expect((await context.cookies()).some(({ name }) => name === "trajecta_monitor_session")).toBe(true);
});

test("successful cookie login and logout return to the sign-in screen", async ({ page, context }) => {
  const requests = await mockAuth(page);
  await submitLogin(page);
  await expect(page.getByRole("main")).toBeVisible();
  const cookie = (await context.cookies()).find(({ name }) => name === "trajecta_monitor_session");
  expect(cookie?.httpOnly).toBe(true);
  expect(await page.evaluate(() => document.cookie)).not.toContain("trajecta_monitor_session");
  await openSignOut(page);
  await expect(page.getByRole("heading", { name: "Sign in to monitor" })).toBeVisible();
  await expect(page.getByText("Signed out.", { exact: true })).toBeVisible();
  await expect(page.getByRole("main")).toHaveCount(0);
  expect((await context.cookies()).some(({ name }) => name === "trajecta_monitor_session")).toBe(false);
  expect(requests.every(({ headers }) => headers.authorization === undefined)).toBe(true);
  expect(await page.evaluate(() => localStorage.getItem("trajecta.monitor.token"))).toBeNull();
  await page.reload();
  await expect(page.getByRole("heading", { name: "Sign in to monitor" })).toBeVisible();
});

test("startup removes the legacy localStorage token without using it or removing preferences", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("trajecta.monitor.token", "legacy-browser-token"));
  const requests = await mockAuth(page);
  await page.goto("/overview");
  await expect(page.getByRole("heading", { name: "Sign in to monitor" })).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem("trajecta.monitor.token"))).toBeNull();
  expect(await page.evaluate(() => localStorage.getItem("trajecta.monitor.language"))).toBe("en");
  expect(requests.every(({ headers }) => headers.authorization === undefined)).toBe(true);
});
