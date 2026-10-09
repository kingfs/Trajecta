import assert from "node:assert/strict";
import { test } from "node:test";

import { apiPaths } from "../src/lib/api.js";
import {
  REALTIME_TOPIC_PREFIXES,
  realtimeSocketURL,
  realtimeTopicForURL,
  registerRealtimeQuery,
  registeredRealtimeQueries,
  registeredRealtimeTopics,
} from "../src/lib/realtime.js";

/*
 * The console's freshness is one WebSocket plus a rule: a read belongs to the
 * topic whose prefix its URL starts with. Both halves of that rule are silent
 * when they are wrong - a read that maps to no topic simply stops updating, and
 * a read that maps to the wrong topic refetches in a loop - so the mapping and
 * the registry are pinned here rather than left to the browser suite.
 */

test("a request URL maps to the topic that covers it", () => {
  assert.equal(realtimeTopicForURL("/api/traces?window=7d&page=2"), "traffic");
  assert.equal(realtimeTopicForURL("/api/traces/abc/observation"), "traffic");
  assert.equal(realtimeTopicForURL("/api/sessions/abc"), "traffic");
  assert.equal(realtimeTopicForURL("/api/overview?window=all"), "traffic");
  assert.equal(realtimeTopicForURL("/api/findings?window=all"), "traffic");
  assert.equal(realtimeTopicForURL("/api/analysis/jobs"), "traffic");
  assert.equal(realtimeTopicForURL("/api/routing/exchanges?limit=50"), "traffic");
  assert.equal(realtimeTopicForURL("/api/upstreams/gpt-5"), "traffic");
  assert.equal(realtimeTopicForURL("/api/models/gpt-5?window=today"), "traffic");
  assert.equal(realtimeTopicForURL("/api/channels/openai-primary"), "traffic");
  assert.equal(realtimeTopicForURL("/api/events/summary?window=all"), "events");
  assert.equal(realtimeTopicForURL("/api/events/abc"), "events");
  assert.equal(realtimeTopicForURL("/api/system/host"), "system");
  assert.equal(realtimeTopicForURL("/api/system/db"), "system");
});

test("a read nothing pushes for belongs to no topic", () => {
  // These are session-scoped or write-shaped: they are refetched by the action
  // that changed them, and a push for them would be a request per recorded
  // request with nothing on screen to update.
  for (const url of [
    "/api/auth/me",
    "/api/provider-presets",
    "/api/settings/channels",
    "/api/responses/function-executors",
    "/api/secrets/local-key",
    "",
    null,
    undefined,
    "https://example.test/api/traces",
  ]) {
    assert.equal(realtimeTopicForURL(url), "", `${url} should map to no topic`);
  }
});

test("the topic registry tracks what is on screen", () => {
  const before = registeredRealtimeTopics();
  const offPage = registerRealtimeQuery("", ["GET", "/api/auth/me"]);
  assert.equal(realtimeTopicForURL("/api/auth/me"), "");
  offPage();

  const stopTraces = registerRealtimeQuery("traffic", ["GET", "/api/traces", "7d"]);
  const stopEvents = registerRealtimeQuery("events", ["GET", "/api/events/summary"]);
  assert.deepEqual(registeredRealtimeTopics(), ["events", "traffic"]);
  assert.deepEqual(registeredRealtimeQueries("traffic"), [["GET", "/api/traces", "7d"]]);

  // The same topic from two components keeps both keys, and leaving one page
  // does not unsubscribe the other.
  const stopMoreTraces = registerRealtimeQuery("traffic", ["GET", "/api/sessions"]);
  assert.equal(registeredRealtimeQueries("traffic").length, 2);
  stopTraces();
  assert.deepEqual(registeredRealtimeQueries("traffic"), [["GET", "/api/sessions"]]);
  assert.deepEqual(registeredRealtimeTopics(), ["events", "traffic"]);

  stopMoreTraces();
  stopEvents();
  assert.deepEqual(registeredRealtimeTopics(), before);
  assert.deepEqual(registeredRealtimeQueries("traffic"), []);
});

test("the socket URL carries the token the browser cannot put in a header", () => {
  const location = { href: "https://monitor.example.test/overview?window=7d", protocol: "https:" };
  const url = new URL(realtimeSocketURL(location, "token with spaces"));
  assert.equal(url.protocol, "wss:");
  assert.equal(url.pathname, apiPaths.eventsSocket);
  assert.equal(url.searchParams.get("access_token"), "token with spaces");

  // A console without an auth store has no token to send, and must not send an
  // empty one.
  const anonymous = new URL(realtimeSocketURL({ href: "http://localhost:8080/", protocol: "http:" }, ""));
  assert.equal(anonymous.protocol, "ws:");
  assert.equal(anonymous.search, "");
});

test("every topic the server publishes has at least one read mapped to it", () => {
  // The reverse direction of the mapping: a topic nothing subscribes to is
  // either a wiring mistake or dead code on the server.
  for (const topic of ["traffic", "events", "system"]) {
    assert.ok(REALTIME_TOPIC_PREFIXES[topic]?.length > 0, `${topic} has no prefixes`);
  }
});
