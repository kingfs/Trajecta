// Pure projections for the detection tab's stats section and rule sandbox.
// The server owns every number (internal/monitor/server_detector_stats.go);
// these helpers only normalize and order what the two endpoints return, so the
// rendering rules are unit-testable without a browser.

// The canonical display order of the hot-path counters, mirroring the JSON
// field order of detector.StatsSnapshot: call volume first, then the projection
// outcome and its skip reasons, then the masking lane and its failure modes.
export const DETECTOR_HOT_COUNTER_ORDER = [
  "hot_calls",
  "ir_projected",
  "ir_skipped_no_marker",
  "ir_skipped_over_size",
  "ir_parse_failed",
  "ir_decode_failed",
  "ir_skipped_other",
  "byte_fallbacks",
  "field_runs",
  "sealed_spans",
  "mask_candidates",
  "mask_events",
  "scanner_errors",
  "size_cap_hits",
];

function counterValue(value) {
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0) return 0;
  return Math.round(number);
}

// hotCounterEntries projects the `hot` object onto [{key, value}] pairs in the
// canonical order. Unknown keys from a newer server keep a stable (sorted)
// place after the known ones instead of being dropped, and a missing snapshot
// yields an empty list rather than a row of fabricated zeros.
export function hotCounterEntries(hot) {
  if (!hot || typeof hot !== "object") return [];
  const known = DETECTOR_HOT_COUNTER_ORDER
    .filter((key) => key in hot)
    .map((key) => ({ key, value: counterValue(hot[key]) }));
  const extra = Object.keys(hot)
    .filter((key) => !DETECTOR_HOT_COUNTER_ORDER.includes(key))
    .sort()
    .map((key) => ({ key, value: counterValue(hot[key]) }));
  return [...known, ...extra];
}

// detectorRuleRows normalizes the 30-day per-rule tally and sorts it by
// replacement count descending, ties broken by rule name so the table is
// stable across refetches. Entries without a rule name are dropped: the row
// would have nothing to identify it.
export function detectorRuleRows(rules) {
  const list = Array.isArray(rules) ? rules : [];
  return list
    .filter((entry) => entry && entry.rule)
    .map((entry) => ({
      rule: String(entry.rule),
      kind: entry.kind ? String(entry.kind) : "",
      hits: counterValue(entry.hits),
      lastSeen: entry.last_seen || "",
    }))
    .sort((a, b) => (b.hits - a.hits) || a.rule.localeCompare(b.rule));
}

// sandboxFindingRows normalizes the sandbox response's findings and orders
// them by their position in the pasted body (start, then end), which is the
// order an operator reads the body in. The server already scans in span order;
// sorting here keeps the table stable even if that ever changes.
export function sandboxFindingRows(payload) {
  const list = Array.isArray(payload?.findings) ? payload.findings : [];
  return list
    .filter((entry) => entry && entry.rule)
    .map((entry) => ({
      rule: String(entry.rule),
      vendor: entry.vendor ? String(entry.vendor) : "",
      kind: entry.kind ? String(entry.kind) : "",
      start: counterValue(entry.start),
      end: counterValue(entry.end),
      score: counterValue(entry.score),
      confidence: entry.confidence ? String(entry.confidence) : "",
      reasons: entry.reasons ? String(entry.reasons) : "",
      maskable: Boolean(entry.maskable),
      suppressed: Boolean(entry.suppressed),
      standIn: entry.stand_in ? String(entry.stand_in) : "",
    }))
    .sort((a, b) => (a.start - b.start) || (a.end - b.end));
}

// sandboxStandInState decides what the stand-in column shows for one finding.
// Suppression wins over everything (the value is deliberately left alone); a
// report-only finding is never replaced; a maskable finding carries the
// rendered stand-in, or nothing when the vault produced no event.
export function sandboxStandInState(finding) {
  if (!finding) return "none";
  if (finding.suppressed) return "suppressed";
  if (!finding.maskable) return "report_only";
  if (finding.standIn) return "stand_in";
  return "none";
}
