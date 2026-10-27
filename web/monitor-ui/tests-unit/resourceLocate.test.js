import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  MAX_HIGHLIGHT_LENGTH,
  conversationLocateHref,
  traceResourceLocateURL,
} from "../src/lib/resourceLocate.js";

/*
 * The locate button turns a resource row into a conversation address. The two
 * builders are what decide whether that address names a node the 会话 tab can
 * open and whether the value is small enough to travel in the URL.
 */
describe("traceResourceLocateURL", () => {
  it("encodes the trace id, kind and value", () => {
    assert.equal(
      traceResourceLocateURL("trace/1", "local_path", "C:\\Users\\alice\\main.go"),
      "/api/traces/trace%2F1/resources/locate?kind=local_path&value=C%3A%5CUsers%5Calice%5Cmain.go",
    );
  });
});

describe("conversationLocateHref", () => {
  it("addresses the conversation node and marks the value", () => {
    assert.equal(
      conversationLocateHref("trace-1", "node_abc", "/home/alice/main.go"),
      "/traces/trace-1?tab=conversation&node=node_abc&hl=%2Fhome%2Falice%2Fmain.go",
    );
  });

  it("returns no href when the server could not place the value", () => {
    assert.equal(conversationLocateHref("trace-1", ""), "");
    assert.equal(conversationLocateHref("", "node_abc"), "");
  });

  it("leaves a pathological value out of the address bar", () => {
    const long = "x".repeat(MAX_HIGHLIGHT_LENGTH + 1);
    const href = conversationLocateHref("trace-1", "node_abc", long);
    const params = new URL(href, "http://localhost").searchParams;
    assert.equal(params.get("node"), "node_abc");
    assert.equal(params.get("hl"), null);
  });
});
