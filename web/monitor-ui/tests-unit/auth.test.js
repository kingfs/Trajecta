import assert from "node:assert/strict";
import { test } from "node:test";
import { apiPaths, clearLegacyMonitorToken, downloadBlob, loginMonitor, logoutMonitor } from "../src/lib/api.js";

function jsonResponse(status, payload) {
  return { ok: status >= 200 && status < 300, status, json: async () => payload };
}

test("login verifies the cookie with auth/me rather than using the response token", async (t) => {
  const requests = [];
  const user = { username: "reader", role: "reader", scope: "all" };
  t.mock.method(globalThis, "fetch", async (path, options) => {
    requests.push({ path, options });
    return jsonResponse(200, path === apiPaths.authLogin ? { token: "do-not-use" } : user);
  });
  assert.deepEqual(await loginMonitor({ username: "reader", password: "password" }), user);
  assert.deepEqual(requests.map(({ path }) => path), [apiPaths.authLogin, apiPaths.authMe]);
  for (const { options } of requests) {
    assert.equal(options.credentials, "same-origin");
    assert.equal(options.headers.Authorization, undefined);
  }
});

test("a rejected session cookie does not produce a successful login or fallback admin", async (t) => {
  t.mock.method(globalThis, "fetch", async (path) => jsonResponse(
    path === apiPaths.authLogin ? 200 : 401,
    path === apiPaths.authLogin ? { token: "unused" } : { error: "unauthorized" },
  ));
  await assert.rejects(loginMonitor({ username: "reader", password: "password" }), { status: 401, message: "unauthorized" });
});

test("failed password verification never attempts auth/me", async (t) => {
  const paths = [];
  t.mock.method(globalThis, "fetch", async (path) => {
    paths.push(path);
    return jsonResponse(401, { error: "invalid username or password" });
  });
  await assert.rejects(loginMonitor({ username: "reader", password: "wrong" }), { status: 401 });
  assert.deepEqual(paths, [apiPaths.authLogin]);
});

test("logout waits for the same-origin POST response", async (t) => {
  let respond;
  let finished = false;
  t.mock.method(globalThis, "fetch", (path, options) => {
    assert.equal(path, apiPaths.authLogout);
    assert.equal(options.method, "POST");
    assert.equal(options.credentials, "same-origin");
    return new Promise((resolve) => { respond = resolve; });
  });
  const pending = logoutMonitor().then(() => { finished = true; });
  await Promise.resolve();
  assert.equal(finished, false);
  respond(jsonResponse(200, { ok: true }));
  await pending;
  assert.equal(finished, true);
});

test("logout surfaces an HTTP error rather than claiming success", async (t) => {
  t.mock.method(globalThis, "fetch", async () => jsonResponse(503, { error: "service unavailable" }));
  await assert.rejects(logoutMonitor(), { status: 503, message: "service unavailable" });
});

test("logout surfaces a network failure rather than claiming success", async (t) => {
  t.mock.method(globalThis, "fetch", async () => { throw new TypeError("offline"); });
  await assert.rejects(logoutMonitor(), { message: "offline" });
});

test("legacy cleanup only removes the old credential and tolerates inaccessible storage", () => {
  const previous = Object.getOwnPropertyDescriptor(globalThis, "window");
  const removed = [];
  try {
    Object.defineProperty(globalThis, "window", { configurable: true, value: { localStorage: {
      getItem() { assert.fail("legacy credentials must never be read"); },
      removeItem(key) { removed.push(key); },
    } } });
    clearLegacyMonitorToken();
    assert.deepEqual(removed, ["trajecta.monitor.token"]);
    Object.defineProperty(globalThis, "window", { configurable: true, value: { get localStorage() { throw new Error("storage denied"); } } });
    assert.doesNotThrow(clearLegacyMonitorToken);
  } finally {
    if (previous) Object.defineProperty(globalThis, "window", previous);
    else delete globalThis.window;
  }
});

test("downloads use the cookie rather than a browser-readable bearer token", async (t) => {
  const blob = new Blob(["trajectory"]);
  t.mock.method(globalThis, "fetch", async (path, options) => {
    assert.equal(options.credentials, "same-origin");
    assert.equal(options.headers?.Authorization, undefined);
    return { ok: true, blob: async () => blob };
  });
  assert.equal(await downloadBlob("/api/sessions/example/trajectory"), blob);
});
