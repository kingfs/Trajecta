import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { FIND_MATCH_LIMIT, findHighlight, findMatches, highlightNeedles } from "../src/lib/highlight.js";

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

  it("tries the HTML-escaped and percent-encoded spellings of a URL", () => {
    const value = "https://api.github.com/orgs/chaitin/repos?per_page=100&type=all";
    const needles = highlightNeedles(value);
    assert.ok(needles.includes(value));
    assert.ok(needles.includes(value.replaceAll("&", "&amp;")));
    assert.ok(needles.includes(encodeURIComponent(value)));
    assert.ok(needles.includes(value.replaceAll("&", "%26")));
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

  it("finds a URL whose ampersand is HTML-escaped or percent-encoded", () => {
    const value = "https://api.github.com/orgs/chaitin/repos?per_page=100&type=all";
    const escaped = value.replaceAll("&", "&amp;");
    assert.equal(findHighlight(`fetch ${escaped} now`, value)?.needle, escaped);
    const encoded = encodeURIComponent(value);
    assert.equal(findHighlight(`fetch ${encoded} now`, value)?.needle, encoded);
  });

  it("refuses a value repeated so often it is a pattern", () => {
    const text = Array.from({ length: 20 }, () => "/tmp/x").join(" ");
    assert.equal(findHighlight(text, "/tmp/x"), null);
  });
});

describe("findMatches", () => {
  it("matches case-insensitively and keeps the original casing in the mark", () => {
    const found = findMatches("Line one, line two", "LINE");
    assert.deepEqual(found.segments, [
      { text: "Line", hit: true },
      { text: " one, " },
      { text: "line", hit: true },
      { text: " two" },
    ]);
    assert.equal(found.count, 2);
  });

  it("treats regular-expression punctuation as text", () => {
    const found = findMatches("open a.b then [x] then a+b", "a.b");
    assert.equal(found.count, 1);
    assert.equal(found.segments.filter((segment) => segment.hit)[0].text, "a.b");
  });

  it("returns null when there is nothing to mark", () => {
    assert.equal(findMatches("nothing here", "missing"), null);
    assert.equal(findMatches("", "x"), null);
    assert.equal(findMatches("text", "  "), null);
  });

  it("caps a pathological block rather than freezing the page", () => {
    const found = findMatches("a".repeat(250), "a");
    assert.equal(found.count, FIND_MATCH_LIMIT);
    assert.equal(found.truncated, true);
  });
});
