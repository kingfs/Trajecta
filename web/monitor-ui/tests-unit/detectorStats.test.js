/*
 * The pure half of the detection stats section and the rule sandbox: counter
 * ordering, the 30-day rule tally sort and the sandbox finding projection.
 * The browser spec can only show the states its mock returns; these assertions
 * pin the rules that matter for every payload: canonical counter order with
 * unknown keys kept, a stable hits-descending table, span-ordered findings and
 * the stand-in column's decision table (suppression wins over maskability).
 */
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  DETECTOR_HOT_COUNTER_ORDER,
  hotCounterEntries,
  detectorRuleRows,
  sandboxFindingRows,
  sandboxStandInState,
} from "../src/lib/detectorStats.js";

describe("hotCounterEntries", () => {
  it("keeps the canonical StatsSnapshot order regardless of key insertion order", () => {
    const hot = {
      mask_events: 3,
      hot_calls: 100,
      scanner_errors: 1,
      ir_projected: 40,
    };
    assert.deepEqual(hotCounterEntries(hot), [
      { key: "hot_calls", value: 100 },
      { key: "ir_projected", value: 40 },
      { key: "mask_events", value: 3 },
      { key: "scanner_errors", value: 1 },
    ]);
  });

  it("covers every documented counter exactly once", () => {
    const hot = Object.fromEntries(DETECTOR_HOT_COUNTER_ORDER.map((key, index) => [key, index]));
    const entries = hotCounterEntries(hot);
    assert.deepEqual(entries.map((entry) => entry.key), DETECTOR_HOT_COUNTER_ORDER);
    assert.equal(new Set(entries.map((entry) => entry.key)).size, DETECTOR_HOT_COUNTER_ORDER.length);
  });

  it("keeps unknown counters from a newer server after the known ones, sorted", () => {
    const entries = hotCounterEntries({ z_future: 5, hot_calls: 2, a_future: 1 });
    assert.deepEqual(entries, [
      { key: "hot_calls", value: 2 },
      { key: "a_future", value: 1 },
      { key: "z_future", value: 5 },
    ]);
  });

  it("normalizes missing, negative and fractional counts to safe integers", () => {
    const entries = hotCounterEntries({ hot_calls: -4, mask_events: 2.6, scanner_errors: "7", ir_projected: null });
    assert.deepEqual(entries, [
      { key: "hot_calls", value: 0 },
      { key: "ir_projected", value: 0 },
      { key: "mask_events", value: 3 },
      { key: "scanner_errors", value: 7 },
    ]);
  });

  it("returns an empty list for a missing snapshot instead of fabricated zeros", () => {
    assert.deepEqual(hotCounterEntries(null), []);
    assert.deepEqual(hotCounterEntries(undefined), []);
    assert.deepEqual(hotCounterEntries("nope"), []);
    assert.deepEqual(hotCounterEntries({}), []);
  });
});

describe("detectorRuleRows", () => {
  it("sorts by replacement count descending, ties by rule name", () => {
    const rows = detectorRuleRows([
      { rule: "mobile.cn", kind: "mobile", hits: 4, last_seen: "2026-11-01T00:00:00Z" },
      { rule: "openai.project_key", kind: "api_key", hits: 9, last_seen: "2026-11-10T00:00:00Z" },
      { rule: "aws.access_key", kind: "api_key", hits: 4, last_seen: "2026-11-02T00:00:00Z" },
    ]);
    assert.deepEqual(rows.map((row) => row.rule), ["openai.project_key", "aws.access_key", "mobile.cn"]);
    assert.equal(rows[0].hits, 9);
    assert.equal(rows[0].lastSeen, "2026-11-10T00:00:00Z");
  });

  it("normalizes optional fields and drops entries without a rule name", () => {
    const rows = detectorRuleRows([
      { rule: "dsn.password", hits: "3" },
      { hits: 100 },
      null,
      { rule: "" },
    ]);
    assert.deepEqual(rows, [{ rule: "dsn.password", kind: "", hits: 3, lastSeen: "" }]);
  });

  it("returns an empty table for a missing or empty tally", () => {
    assert.deepEqual(detectorRuleRows(null), []);
    assert.deepEqual(detectorRuleRows([]), []);
    assert.deepEqual(detectorRuleRows(undefined), []);
  });
});

describe("sandboxFindingRows", () => {
  const payload = {
    body_length: 120,
    findings: [
      { rule: "national_id.cn", start: 60, end: 78, score: 80, confidence: "high", maskable: true, suppressed: true },
      { rule: "openai.project_key", vendor: "OpenAI", kind: "api_key", start: 10, end: 61, score: 90, confidence: "high", reasons: "prefix", maskable: true, suppressed: false, stand_in: "sk-proj-XXXX" },
      { rule: "access.local_path", start: 10, end: 30, score: 10, confidence: "low", maskable: false },
    ],
  };

  it("orders findings by their span in the pasted body", () => {
    const rows = sandboxFindingRows(payload);
    assert.deepEqual(rows.map((row) => [row.rule, row.start, row.end]), [
      ["access.local_path", 10, 30],
      ["openai.project_key", 10, 61],
      ["national_id.cn", 60, 78],
    ]);
  });

  it("keeps the server's suppression decision and stand-in verbatim", () => {
    const rows = sandboxFindingRows(payload);
    const key = rows.find((row) => row.rule === "openai.project_key");
    assert.equal(key.vendor, "OpenAI");
    assert.equal(key.kind, "api_key");
    assert.equal(key.standIn, "sk-proj-XXXX");
    assert.equal(key.suppressed, false);
    assert.equal(key.reasons, "prefix");
    const id = rows.find((row) => row.rule === "national_id.cn");
    assert.equal(id.suppressed, true);
    assert.equal(id.standIn, "");
  });

  it("normalizes a sparse finding and tolerates a missing payload", () => {
    const rows = sandboxFindingRows({ findings: [{ rule: "dsn.password" }, null, { start: 3 }] });
    assert.deepEqual(rows, [{
      rule: "dsn.password",
      vendor: "",
      kind: "",
      start: 0,
      end: 0,
      score: 0,
      confidence: "",
      reasons: "",
      maskable: false,
      suppressed: false,
      standIn: "",
    }]);
    assert.deepEqual(sandboxFindingRows(null), []);
    assert.deepEqual(sandboxFindingRows({}), []);
  });
});

describe("sandboxStandInState", () => {
  it("lets suppression win over a rendered stand-in", () => {
    assert.equal(sandboxStandInState({ suppressed: true, maskable: true, standIn: "sk-X" }), "suppressed");
  });

  it("marks a report-only finding as never replaced", () => {
    assert.equal(sandboxStandInState({ suppressed: false, maskable: false, standIn: "" }), "report_only");
  });

  it("shows the stand-in only for a maskable, unsuppressed finding that has one", () => {
    assert.equal(sandboxStandInState({ maskable: true, standIn: "sk-X" }), "stand_in");
    assert.equal(sandboxStandInState({ maskable: true, standIn: "" }), "none");
    assert.equal(sandboxStandInState(null), "none");
  });
});
