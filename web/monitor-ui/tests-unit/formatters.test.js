import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  formatDateTime,
  formatRawDuration,
  formatRawNumber,
  formatTimelineBucketLabel,
  formatTime,
} from "../src/lib/monitor.js";

/*
 * The date and raw-number formatters used to hardcode a locale: "zh-CN" in the
 * three date ones and the browser default in the number ones. The English
 * console therefore printed Chinese-ordered dates - `2026/06/22 16:00:00` under
 * an English "Recorded at" label - which no label around it could fix.
 *
 * They resolve the language at format time now, from the same places the
 * provider writes it, so a test only has to put a value there. `window` is a
 * browser global node does not have, and `currentLanguage()` already falls back
 * to the default language when it is missing.
 */
function useLanguage(language) {
  globalThis.window = {
    localStorage: {
      getItem: (key) => (key === "trajecta.monitor.language" ? language : null),
      setItem: () => {},
    },
  };
}

// 08:00Z. The assertions below are about field order rather than about the day,
// so they hold in any timezone the suite happens to run in.
const INSTANT = "2026-06-22T08:00:00Z";

describe("date formatters follow the active language", () => {
  it("prints year first under zh-CN", () => {
    useLanguage("zh-CN");
    assert.match(formatDateTime(INSTANT), /^\d{4}\/\d{2}\/\d{2}/);
    assert.match(formatTime(INSTANT), /^\d{2}:\d{2}:\d{2}$/);
    assert.match(formatTimelineBucketLabel(INSTANT), /^\d{2}\/\d{2}\s/);
  });

  it("prints day first, with a meridiem, under en", () => {
    useLanguage("en");
    assert.match(formatDateTime(INSTANT), /^\d{2}\/\d{2}\/\d{4},/);
    assert.match(formatTime(INSTANT), /^\d{2}:\d{2}:\d{2}\s?(AM|PM)$/);
    assert.match(formatTimelineBucketLabel(INSTANT), /^\d{2}\/\d{2},/);
  });

  it("resolves the language when it formats, not when it is imported", () => {
    // The failure this pins is a captured locale: a formatter that read the
    // language once would keep printing it after the reader switched.
    useLanguage("zh-CN");
    const chinese = formatDateTime(INSTANT);
    useLanguage("en");
    const english = formatDateTime(INSTANT);
    assert.notEqual(chinese, english);
    assert.match(chinese, /^2026/);
    assert.doesNotMatch(english, /^2026/);
  });

  it("falls back to the default language with no stored preference", () => {
    useLanguage(null);
    assert.match(formatDateTime(INSTANT), /^\d{4}\//);
  });
});

describe("raw tooltip values", () => {
  it("groups digits the same way in both languages", () => {
    for (const language of ["zh-CN", "en"]) {
      useLanguage(language);
      assert.equal(formatRawNumber(125000), "125,000");
      assert.equal(formatRawDuration(125000), "125,000 ms");
      assert.equal(formatRawNumber(0), "0");
    }
  });

  it("keeps the empty marker for a missing instant", () => {
    assert.equal(formatDateTime(null), "-");
    assert.equal(formatTime(""), "-");
    assert.equal(formatTimelineBucketLabel(undefined), "-");
    assert.equal(formatRawDuration(0), "0 ms");
  });

  it("does not render a non-numeric value as NaN", () => {
    assert.equal(formatRawNumber("nope"), "0");
    assert.equal(formatRawDuration("nope"), "0 ms");
  });
});
