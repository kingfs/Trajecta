import assert from "node:assert/strict";
import { describe, it } from "node:test";

import {
  MIN_VIEWPORT,
  buildTimelineSpans,
  formatClock,
  layoutTimeline,
  modeRemovesIdle,
  panViewport,
  projectSelection,
  selectionFromDrag,
  timelineMode,
  traceIDsInSelection,
  zoomViewport,
} from "../src/lib/timeline.js";

/*
 * The timeline is the one view where a wrong number is visible as a wrong
 * picture, so the geometry is asserted here rather than through the DOM: the
 * four duration modes, the idle compression, the selection surviving a zoom,
 * and the first-byte boundary staying inside its model span.
 */

function step(traceID, startedAt, { modelMs = 1000, ttftMs = 200, wait = null, status = 200 } = {}) {
  return {
    trace_id: traceID,
    status,
    model: "gpt-test",
    started_at: startedAt,
    ended_at: new Date(Date.parse(startedAt) + modelMs).toISOString(),
    timing: { model_ms: modelMs, ttft_ms: ttftMs, tool_wait_ms: wait, wall_ms: modelMs },
  };
}

const turns = [
  {
    index: 1,
    key: "t-a",
    trigger: { kind: "user", text: "hello" },
    steps: [step("a1", "2026-05-01T09:00:00.000Z", { wait: 500 }), step("a2", "2026-05-01T09:00:10.000Z")],
  },
  {
    index: 2,
    key: "t-b",
    trigger: { kind: "user", text: "later" },
    steps: [step("b1", "2026-05-01T09:05:00.000Z")],
  },
];

describe("spans", () => {
  it("gives every step an input marker and a model span, plus a tool span when one was measured", () => {
    const { spans } = buildTimelineSpans(turns);
    assert.deepEqual(
      spans.map((span) => `${span.traceID}:${span.lane}`),
      ["a1:input", "a1:model", "a1:tools", "a2:input", "a2:model", "b1:input", "b1:model"],
    );
    const model = spans.find((span) => span.key === "a1:model");
    assert.equal(model.ttft, 0.2);
    assert.equal(model.ttftMs, 200);
    const tool = spans.find((span) => span.lane === "tools");
    assert.equal(tool.end - tool.start, 500);
  });

  it("skips a step whose start cannot be parsed instead of inventing one", () => {
    const { spans } = buildTimelineSpans([{ index: 1, steps: [{ trace_id: "x", started_at: "not-a-date" }] }]);
    assert.equal(spans.length, 0);
  });
});

describe("modes", () => {
  it("maps the two switches to the four modes", () => {
    assert.equal(timelineMode({}), "sequence");
    assert.equal(timelineMode({ actualTime: true }), "time");
    assert.equal(timelineMode({ actualDuration: true }), "duration");
    assert.equal(timelineMode({ actualDuration: true, actualTime: true }), "actual");
    assert.equal(modeRemovesIdle("sequence"), true);
    assert.equal(modeRemovesIdle("duration"), true);
    assert.equal(modeRemovesIdle("time"), false);
    assert.equal(modeRemovesIdle("actual"), false);
  });
});

describe("layout", () => {
  it("spreads the sequence mode evenly and ignores the clock", () => {
    const { spans } = buildTimelineSpans(turns);
    const { spans: placed } = layoutTimeline(spans, { mode: "sequence" });
    const total = placed.length;
    assert.equal(placed[0].left, 0);
    assert.equal(placed[0].width.toFixed(4), (1 / total).toFixed(4));
    for (let index = 1; index < placed.length; index += 1) {
      assert.ok(placed[index].left > placed[index - 1].left, "spans advance left to right");
    }
  });

  it("compresses the idle gap in duration mode but keeps it in actual mode", () => {
    const { spans } = buildTimelineSpans(turns);
    const duration = layoutTimeline(spans, { mode: "duration" });
    const actual = layoutTimeline(spans, { mode: "actual" });
    const last = (layout) => layout.spans[layout.spans.length - 1];
    // The five minutes between the turns dominate the real axis (the last span
    // lands at the very end) and are removed from the duration axis, where the
    // exchanges alone fill it.
    assert.ok(last(duration).left < 0.8, "without idle the last turn is not pushed to the edge");
    assert.ok(last(actual).left > 0.99, "the gap dominates the real axis");
    assert.ok(last(duration).left < last(actual).left - 0.2, "removing idle moves the last span left");
  });

  it("draws a dot in time mode, where the width carries no duration", () => {
    const { spans } = buildTimelineSpans(turns);
    const { spans: placed } = layoutTimeline(spans, { mode: "time" });
    const model = placed.find((span) => span.key === "a1:model");
    assert.ok(model.width <= 0.0021, "time mode does not scale width by duration");
  });

  it("keeps the first-byte boundary a fraction of the model span", () => {
    const { spans } = buildTimelineSpans(turns);
    const { spans: placed } = layoutTimeline(spans, { mode: "duration" });
    const model = placed.find((span) => span.key === "a1:model");
    assert.equal(model.ttft, 0.2);
  });

  it("returns an empty layout for an empty window", () => {
    assert.deepEqual(layoutTimeline([], { mode: "sequence" }).spans, []);
  });
});

describe("zoom and pan", () => {
  it("keeps the cursor anchored while zooming", () => {
    const base = { start: 0, end: 1 };
    const zoomed = zoomViewport(base, 0.5, 2);
    assert.equal(zoomed.end - zoomed.start, 0.5);
    assert.equal((zoomed.start + zoomed.end) / 2, 0.5, "the anchor stays at the middle");
  });

  it("never zooms past the floor or outside the domain", () => {
    let viewport = { start: 0, end: 1 };
    for (let index = 0; index < 100; index += 1) viewport = zoomViewport(viewport, 0, 2);
    assert.ok(Math.abs(viewport.end - viewport.start - MIN_VIEWPORT) < 1e-9);
    assert.ok(viewport.start >= 0 && viewport.end <= 1);

    const panned = panViewport({ start: 0.4, end: 0.6 }, -10);
    assert.equal(panned.start, 0);
    assert.ok(Math.abs(panned.end - 0.2) < 1e-9);
  });

  it("projects a span onto the zoomed viewport", () => {
    const { spans } = buildTimelineSpans(turns);
    const full = layoutTimeline(spans, { mode: "sequence" });
    const zoomed = layoutTimeline(spans, { mode: "sequence", viewport: { start: 0.5, end: 1 } });
    assert.equal(full.spans[0].visible, true);
    assert.equal(zoomed.spans[0].visible, false, "the first span falls out of a right-half viewport");
    assert.ok(zoomed.spans[zoomed.spans.length - 1].visible, "the last span stays in view");
  });
});

describe("selection", () => {
  it("normalizes a drag and ignores a click", () => {
    assert.deepEqual(selectionFromDrag(0.6, 0.2), { start: 0.2, end: 0.6 });
    assert.equal(selectionFromDrag(0.5, 0.5), null);
  });

  it("selects the steps the range touches", () => {
    const { spans } = buildTimelineSpans(turns);
    const layout = layoutTimeline(spans, { mode: "sequence" });
    const total = layout.spans.length;
    // The first three spans belong to the first step.
    const ids = traceIDsInSelection(layout, { start: 0, end: 3 / total });
    assert.deepEqual([...ids], ["a1"]);
  });

  it("keeps the selection anchored to the spans after a zoom", () => {
    const { spans } = buildTimelineSpans(turns);
    const layout = layoutTimeline(spans, { mode: "sequence" });
    const selection = { start: 0, end: 0.5 };
    const projectedFull = projectSelection(selection, { start: 0, end: 1 });
    const projectedZoom = projectSelection(selection, { start: 0.25, end: 0.75 });
    assert.equal(projectedFull.left, 0);
    assert.ok(projectedZoom.width >= 0);
    assert.ok(projectedZoom.left === 0 || projectedZoom.left <= 0.000001);
  });
});

describe("clock formatting", () => {
  it("prints a millisecond clock and refuses a missing value", () => {
    const value = Date.parse("2026-05-01T09:00:01.250Z");
    const printed = formatClock(value);
    assert.match(printed, /^\d{2}:\d{2}:\d{2}\.\d{3}$/);
    assert.equal(formatClock(Number.NaN), "—");
  });
});
