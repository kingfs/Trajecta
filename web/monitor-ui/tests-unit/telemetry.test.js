import assert from "node:assert/strict";
import { after, describe, it } from "node:test";

import { buildInstallPing, installPingDue, lastInstallPing, reportInstall, ABOUT_QUERY_KEY, INSTALL_PING_KEY } from "../src/lib/telemetry.js";
import { queryClient } from "../src/lib/queryClient.ts";

/*
 * The install ping is the one thing this console does that leaves the machine,
 * and the properties that keep it honest are all in pure functions here. The
 * one that matters most is the first: a deployment that did not configure an
 * endpoint must produce no request at all, whatever else the payload says.
 */

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: (key) => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: (key) => values.delete(key),
    values,
  };
}

const ABOUT = {
  version: "v3.3.1-2-g4e766d6",
  commit: "4e766d6",
  goos: "linux",
  goarch: "amd64",
};

describe("install ping eligibility", () => {
  it("sends nothing when telemetry is off", () => {
    const settings = { enabled: false, provider: "custom", endpoint: "https://collector.test/ping" };
    assert.equal(buildInstallPing(settings, ABOUT, "abc"), null);
  });

  it("sends nothing when enabled but the endpoint is empty", () => {
    const settings = { enabled: true, provider: "custom", endpoint: "   " };
    assert.equal(buildInstallPing(settings, ABOUT, "abc"), null);
  });

  it("sends nothing without an install id", () => {
    const settings = { enabled: true, provider: "custom", endpoint: "https://collector.test/ping" };
    assert.equal(buildInstallPing(settings, ABOUT, ""), null);
  });
});

describe("install ping payload", () => {
  it("carries the version, commit and platform and nothing else", () => {
    const settings = { enabled: true, provider: "custom", endpoint: "https://collector.test/ping" };
    const ping = buildInstallPing(settings, ABOUT, "install-1");
    assert.equal(ping.kind, "beacon");
    assert.equal(ping.url, "https://collector.test/ping");

    const body = JSON.parse(ping.body);
    assert.equal(body.event, "install_ping");
    assert.equal(body.install_id, "install-1");
    assert.equal(body.version, "v3.3.1-2-g4e766d6");
    assert.equal(body.commit, "4e766d6");
    assert.equal(body.os, "linux");
    assert.equal(body.arch, "amd64");
    assert.deepEqual(
      Object.keys(body).sort(),
      ["arch", "commit", "event", "install_id", "os", "ts", "version"],
      "the payload must not grow a field without a deliberate decision",
    );
  });

  it("builds a Measurement Protocol body for the ga4 provider", () => {
    const settings = {
      enabled: true,
      provider: "ga4",
      endpoint: "https://www.google-analytics.com/mp/collect?measurement_id=G-1&api_secret=s",
    };
    const ping = buildInstallPing(settings, ABOUT, "install-1");
    const body = JSON.parse(ping.body);
    // GA4 counts users by client_id, so the deployment's id is what makes one
    // Monitor one user however many people open it.
    assert.equal(body.client_id, "install-1");
    assert.equal(body.events[0].name, "install_ping");
    assert.equal(body.events[0].params.version, "v3.3.1-2-g4e766d6");
  });

  it("puts the version in the path for the pixel provider", () => {
    const settings = {
      enabled: true,
      provider: "pixel",
      endpoint: "https://counter.goatcounter.com/count",
      path_prefix: "/trajecta/install/",
    };
    const ping = buildInstallPing(settings, ABOUT, "install-1");
    assert.equal(ping.kind, "pixel");
    const url = new URL(ping.url);
    // One trailing slash, not two: the prefix is a path, not a directory listing.
    assert.equal(url.searchParams.get("p"), "/trajecta/install/v3.3.1-2-g4e766d6");
  });
});

describe("install ping interval", () => {
  const now = Date.parse("2026-10-16T12:00:00Z");
  const day = 24 * 60 * 60 * 1000;

  it("reports on a browser that never has", () => {
    assert.equal(installPingDue(null, now, day, "v3.3.1"), true);
  });

  it("does not report twice in one day for the same version", () => {
    const previous = { at: now - 60_000, version: "v3.3.1" };
    assert.equal(installPingDue(previous, now, day, "v3.3.1"), false);
  });

  it("reports again once the interval has passed", () => {
    const previous = { at: now - day - 1, version: "v3.3.1" };
    assert.equal(installPingDue(previous, now, day, "v3.3.1"), true);
  });

  it("reports immediately after an upgrade", () => {
    const previous = { at: now - 60_000, version: "v3.3.1" };
    assert.equal(installPingDue(previous, now, day, "v3.4.0"), true);
  });
});

// The shared client schedules a gc timer for every cached query, and its default
// gcTime is five minutes - long enough to keep `node --test` from exiting.
after(() => queryClient.clear());

/*
 * The end-to-end shape of one report, with the two side effects - the request and
 * the storage - handed in. Seeding the shared query cache is what the App's own
 * call does too: the About page and the ping read the same `/api/about` entry, so
 * this doubles as the assertion that they share one request.
 */
describe("reportInstall", () => {
  const settings = { enabled: true, provider: "custom", endpoint: "https://collector.test/ping", interval_seconds: 86400 };
  const aboutWithID = () => ({ ...ABOUT, install_id: "install-1", telemetry: settings });

  it("sends one report and records it against the browser", async () => {
    queryClient.setQueryData(ABOUT_QUERY_KEY, aboutWithID());
    const storage = memoryStorage();
    const sent = [];
    const ok = await reportInstall({ storage, now: 1_000, send: (ping) => (sent.push(ping), true) });

    assert.equal(ok, true);
    assert.equal(sent.length, 1);
    assert.equal(JSON.parse(sent[0].body).install_id, "install-1");
    assert.deepEqual(lastInstallPing(storage), { at: 1_000, version: ABOUT.version });
  });

  it("sends nothing without a server-side install id, and does not spend the interval", async () => {
    queryClient.setQueryData(ABOUT_QUERY_KEY, { ...ABOUT, install_id: "", telemetry: settings });
    const storage = memoryStorage();
    let sends = 0;
    const ok = await reportInstall({ storage, now: 1_000, send: () => (sends += 1, true) });

    assert.equal(ok, false);
    assert.equal(sends, 0);
    // No record, so a transient database failure costs one page load rather than
    // a whole interval of reports.
    assert.equal(lastInstallPing(storage), null);
  });

  it("does not report again inside the interval", async () => {
    queryClient.setQueryData(ABOUT_QUERY_KEY, aboutWithID());
    const storage = memoryStorage();
    let sends = 0;
    const send = () => (sends += 1, true);
    await reportInstall({ storage, now: 1_000, send });
    const second = await reportInstall({ storage, now: 1_000 + 60_000, send });

    assert.equal(second, false);
    assert.equal(sends, 1);
  });

  it("reports again on the next load once the interval has passed", async () => {
    queryClient.setQueryData(ABOUT_QUERY_KEY, aboutWithID());
    const storage = memoryStorage();
    let sends = 0;
    const send = () => (sends += 1, true);
    await reportInstall({ storage, now: 1_000, send });
    await reportInstall({ storage, now: 1_000 + 24 * 60 * 60 * 1000, send });
    assert.equal(sends, 2);
  });
});

describe("install ping storage key", () => {
  it("is namespaced, so two consoles on one origin do not share a throttle", () => {
    assert.match(INSTALL_PING_KEY, /^trajecta\.monitor\./);
  });
});
