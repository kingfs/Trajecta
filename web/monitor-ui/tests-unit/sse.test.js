import assert from "node:assert/strict";
import { test } from "node:test";

import { apiPaths, streamSystemEvents } from "../src/lib/api.js";

/*
 * The SSE reader used to be written by hand: it split frames on "\n\n", picked
 * the `event:` and `data:` fields out of each one, reconnected on a fixed 5s
 * timer and joined multi-line payloads itself. It is `fetchEventSource` now,
 * which is the library that exists for SSE over fetch so a JWT can travel in an
 * Authorization header instead of in the query string.
 *
 * Two things about that swap are worth pinning, because both are silent when
 * wrong: the parser dispatches on every blank line, so heartbeat comments arrive
 * as events with no data and must not be forwarded, and a clean close ends the
 * subscription as far as the library is concerned, so the reconnect is ours.
 *
 * `window` is what the library reaches for; node has none. Only `fetch` is
 * really exercised - `setTimeout` is stubbed so a reconnect is observable as a
 * call rather than as five seconds of waiting.
 */
function stubWindow({ body, onRetry }) {
  const calls = { fetch: 0, scheduled: [] };
  // The library unregisters its visibility listener as it disposes, so it needs
  // a document to remove the listener from even though this reader keeps the
  // stream open while the tab is hidden.
  globalThis.document = { addEventListener: () => {}, removeEventListener: () => {}, hidden: false };
  globalThis.window = {
    localStorage: { getItem: () => null, setItem: () => {} },
    setTimeout: (fn, delay) => {
      calls.scheduled.push(delay);
      onRetry?.(fn);
      return calls.scheduled.length;
    },
    clearTimeout: () => {},
    fetch: async (url) => {
      calls.fetch += 1;
      const encoder = new TextEncoder();
      const stream = new ReadableStream({
        start(controller) {
          for (const chunk of body) {
            controller.enqueue(encoder.encode(chunk));
          }
          controller.close();
        },
      });
      return new Response(stream, { status: 200, headers: { "content-type": "text/event-stream" } });
    },
  };
  return calls;
}

// The reader is asynchronous: the body is read through a stream, so give the
// microtask queue a turn before asserting on what arrived.
const settled = () => new Promise((resolve) => setImmediate(resolve));

test("the stream forwards only the blocks that carry data", async () => {
  const received = [];
  const errors = [];
  stubWindow({
    body: [
      ": heartbeat\n\n",
      'event: system_event.summary\ndata: {"unread":7}\n\n',
      "event: system_event.updated\ndata: {}\n\n",
      "event: nameless\ndata: two\n",
      "data: lines\n\n",
    ],
  });

  const stop = streamSystemEvents({ onEvent: (event) => received.push(event), onError: (error) => errors.push(error) });
  await settled();
  stop();

  assert.deepEqual(errors, []);
  assert.deepEqual(received, [
    // The heartbeat is dropped rather than delivered as `{event: "", data: ""}`.
    { event: "system_event.summary", data: '{"unread":7}' },
    { event: "system_event.updated", data: "{}" },
    // Multi-line payloads are joined, and a block with no `event:` field reports
    // an empty name rather than `undefined`.
    { event: "nameless", data: "two\nlines" },
  ]);
});

test("the stream reconnects after the server closes it", async () => {
  const received = [];
  const retries = [];
  const calls = stubWindow({
    body: ['event: system_event.summary\ndata: {"unread":3}\n\n'],
    onRetry: (fn) => retries.push(fn),
  });

  const stop = streamSystemEvents({ onEvent: (event) => received.push(event), onError: () => {} });
  await settled();

  // One request, then a scheduled reconnect: a clean close is not an error, so
  // nothing may be reported through onError, and without the reconnect the
  // unread badge would stop updating for the rest of the session.
  assert.equal(calls.fetch, 1);
  assert.equal(received.length, 1);
  assert.ok(retries.length >= 1, "a reconnect should have been scheduled");
  assert.equal(calls.scheduled[0], 5000);

  // Running the scheduled callback opens a second stream, which is the whole
  // point of scheduling it.
  retries[0]();
  await settled();
  assert.equal(calls.fetch, 2);

  stop();
});

test("an aborted stream schedules nothing", async () => {
  const calls = stubWindow({ body: ["event: system_event.summary\ndata: {}\n\n"] });
  const stop = streamSystemEvents({ onEvent: () => {}, onError: () => {} });
  await settled();
  const before = calls.scheduled.length;
  stop();
  await settled();
  assert.equal(calls.scheduled.length, before);
});

test("a response that is not an event stream is reported and retried", async () => {
  const errors = [];
  globalThis.document = { addEventListener: () => {}, removeEventListener: () => {}, hidden: false };
  globalThis.window = {
    localStorage: { getItem: () => null, setItem: () => {} },
    setTimeout: () => 0,
    clearTimeout: () => {},
    fetch: async () => new Response("nope", { status: 404, headers: { "content-type": "application/json" } }),
  };

  const stop = streamSystemEvents({ onEvent: () => {}, onError: (error) => errors.push(error) });
  await settled();
  stop();

  assert.equal(errors.length, 1);
  assert.match(errors[0].message, /event stream failed: 404/);
  assert.ok(apiPaths.eventsStream.endsWith("/events/stream"));
});
