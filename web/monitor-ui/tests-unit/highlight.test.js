import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { findHighlight, highlightNeedles } from "../src/lib/highlight.js";

/*
 * The resource locate action marks the bytes it jumped to. The index normalizes
 * Windows separators while the conversation may still show the JSON-escaped
 * spelling, so both are tried; a value that no longer occurs must leave the text
 * alone rather than mark a prefix of some unrelated word.
 */
describe("highlightNeedles", () => {
  it("tries the normalized and the escaped spelling of a Windows path", () => {
    assert.deepEqual(highlightNeedles("C:\\Users\\alice\\main.go"), [
      "C:\\Users\\alice\\main.go",
      "C:\\\\Users\\\\alice\\\\main.go",
    ]);
  });

  it("does not invent a second spelling for a value without backslashes", () => {
    assert.deepEqual(highlightNeedles("/home/alice/main.go"), ["/home/alice/main.go"]);
    assert.deepEqual(highlightNeedles(""), []);
  });
});

describe("findHighlight", () => {
  it("splits the text around the value", () => {
    assert.deepEqual(findHighlight("read /home/alice/main.go now", "/home/alice/main.go"), {
      needle: "/home/alice/main.go",
      parts: ["read ", " now"],
    });
  });

  it("falls back to the escaped spelling", () => {
    const found = findHighlight('path: "C:\\\\Users\\\\alice\\\\main.go"', "C:\\Users\\alice\\main.go");
    assert.equal(found?.needle, "C:\\\\Users\\\\alice\\\\main.go");
  });

  it("does not mark a prefix of a longer word", () => {
    assert.equal(findHighlight("/home/alice/main.go.bak", "/home/alice/main.go"), null);
    assert.equal(findHighlight("/home/alice/other.go", "/home/alice/main.go"), null);
  });

  it("refuses a value repeated so often it is a pattern", () => {
    const text = Array.from({ length: 20 }, () => "/tmp/x").join(" ");
    assert.equal(findHighlight(text, "/tmp/x"), null);
  });
});
