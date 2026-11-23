/*
 * The geometry behind the 轨迹 timeline.
 *
 * Three lanes - input, model, tools - and four duration modes, the same model
 * the DSH trajectory uses:
 *
 *   | 时长 | 实际时间 | mode     | x axis                  | span width |
 *   |------|----------|----------|-------------------------|------------|
 *   | off  | off      | sequence | operation index         | equal      |
 *   | off  | on       | time     | real timestamps         | a dot      |
 *   | on   | off      | duration | real timestamps, no idle| real       |
 *   | on   | on       | actual   | real timestamps         | real       |
 *
 * The proxy only knows per-exchange facts, so one step contributes three
 * spans: a zero-width marker when the request started (输入), the upstream
 * exchange (模型, with the first-byte boundary inside it), and the gap until
 * the next request (工具, which is where the client ran the tools).
 *
 * Everything here is pure: the component owns pointer state, this module owns
 * the numbers, and `tests-unit/timeline.test.js` checks the numbers.
 */

/** Lane order is also the intra-step paint order. */
export const TIMELINE_LANES = ["input", "model", "tools"];

/** The smallest span the layout will produce, as a fraction of the axis. */
const MIN_SPAN = 0.002;

/** The narrowest viewport zooming may reach, as a fraction of the domain. */
export const MIN_VIEWPORT = 0.02;

/** Mode from the two switches (see the table above). */
export function timelineMode({ actualDuration = false, actualTime = false } = {}) {
  if (actualDuration && actualTime) return "actual";
  if (actualDuration) return "duration";
  if (actualTime) return "time";
  return "sequence";
}

/** Whether a mode positions spans on a real clock. */
export function modeUsesTime(mode) {
  return mode !== "sequence";
}

/** Whether a mode compresses the idle gaps between exchanges. */
export function modeRemovesIdle(mode) {
  return mode === "sequence" || mode === "duration";
}

/**
 * Flatten the window into timeline spans.
 *
 * A step with no measurable gap contributes no tool span, and a step whose
 * start is unparseable contributes nothing at all: a fabricated timestamp
 * would put a span somewhere it never happened.
 */
export function buildTimelineSpans(turns) {
  const spans = [];
  const turnsOut = [];
  for (const turn of turns || []) {
    const steps = Array.isArray(turn?.steps) ? turn.steps : [];
    let turnStart = null;
    for (const step of steps) {
      const start = Date.parse(step?.started_at);
      if (!Number.isFinite(start)) continue;
      const duration = Number(step?.timing?.model_ms ?? 0);
      const end = Math.max(start, Number.isFinite(Date.parse(step?.ended_at)) ? Date.parse(step.ended_at) : start + duration);
      const ttft = Number(step?.timing?.ttft_ms);
      const wait = Number(step?.timing?.tool_wait_ms);
      const base = {
        turnKey: turn.key || turn.index,
        traceID: step.trace_id,
        model: step.model,
        failed: Number(step.status || 0) >= 400,
        estimated: Array.isArray(step.estimated) ? step.estimated : [],
        step,
      };
      spans.push({
        ...base,
        key: `${step.trace_id}:input`,
        lane: "input",
        start,
        end: start,
      });
      spans.push({
        ...base,
        key: `${step.trace_id}:model`,
        lane: "model",
        start,
        end,
        // The first-byte boundary is a fraction of the model span, drawn inside
        // it; it is not a position on the axis.
        ttft: Number.isFinite(ttft) && ttft > 0 && duration > 0 ? Math.min(1, ttft / duration) : null,
        ttftMs: Number.isFinite(ttft) && ttft > 0 ? ttft : null,
      });
      if (Number.isFinite(wait) && wait > 0) {
        spans.push({
          ...base,
          key: `${step.trace_id}:tools`,
          lane: "tools",
          start: end,
          end: end + wait,
        });
      }
      if (turnStart === null) turnStart = start;
    }
    if (turnStart !== null) {
      turnsOut.push({ key: turn.key || turn.index, index: turn.index, label: turn.trigger?.text || "", start: turnStart });
    }
  }
  return { spans, turns: turnsOut };
}

/**
 * Place the spans for one mode and viewport.
 *
 * `left`/`width` are the on-screen fractions (0..1, already zoomed and panned);
 * `domainLeft`/`domainRight` are the fractions of the unzoomed axis, which is
 * the space a selection lives in so zooming does not move it.
 */
export function layoutTimeline(spans, { mode = "sequence", viewport = { start: 0, end: 1 } } = {}) {
  if (!spans.length) return { spans: [], domain: { start: 0, end: 0 } };
  if (!modeUsesTime(mode)) {
    const total = spans.length;
    return {
      spans: spans.map((span, index) => {
        const domainLeft = index / total;
        const domainRight = (index + 1) / total;
        return withViewport(span, domainLeft, domainRight, domainRight, viewport);
      }),
      domain: { start: 0, end: 0 },
    };
  }

  const ordered = [...spans].sort(compareSpans);
  const adjusted = modeRemovesIdle(mode) ? removeIdle(ordered) : ordered;
  const domainStart = Math.min(...adjusted.map((span) => span.start));
  const domainEnd = Math.max(...adjusted.map((span) => span.end), domainStart + 1);
  const span = domainEnd - domainStart || 1;
  const layout = adjusted.map((item) => {
    const domainLeft = clamp01((item.start - domainStart) / span);
    const domainRight = clamp01((item.end - domainStart) / span);
    const width = mode === "time" ? MIN_SPAN : Math.max(MIN_SPAN, domainRight - domainLeft);
    return withViewport(item, domainLeft, Math.min(1, domainLeft + width), domainRight, viewport);
  });
  return { spans: layout, domain: { start: domainStart, end: domainEnd } };
}

/**
 * Map one span onto the current viewport. `left`/`right` are the drawn edges
 * (the width of a `time`-mode dot is forced to MIN_SPAN), while
 * `domainLeft`/`domainRight` keep the span's true extent so a selection covers
 * exactly the steps it touches.
 */
function withViewport(span, drawnLeft, drawnRight, extentRight, viewport) {
  const width = Math.max(0, viewport.end - viewport.start) || 1;
  const project = (value) => (value - viewport.start) / width;
  const left = project(drawnLeft);
  const right = project(drawnRight);
  const visible = right >= 0 && left <= 1;
  return {
    ...span,
    domainLeft: drawnLeft,
    domainRight: extentRight,
    left: clamp01(left),
    right: clamp01(right),
    width: Math.max(0, Math.min(1, right) - Math.max(0, left)),
    visible,
  };
}

/** Shift spans left by the idle time before them, so gaps do not dominate. */
function removeIdle(spans) {
  let removed = 0;
  let coveredUntil = spans[0]?.start ?? 0;
  return spans.map((span) => {
    if (span.start > coveredUntil) removed += span.start - coveredUntil;
    const shifted = { ...span, start: span.start - removed, end: Math.max(span.start - removed, span.end - removed) };
    coveredUntil = Math.max(coveredUntil, span.end);
    return shifted;
  });
}

function compareSpans(left, right) {
  if (left.start !== right.start) return left.start - right.start;
  const laneDelta = TIMELINE_LANES.indexOf(left.lane) - TIMELINE_LANES.indexOf(right.lane);
  if (laneDelta !== 0) return laneDelta;
  return String(left.key).localeCompare(String(right.key));
}

/** Zoom around a cursor position given as a fraction of the visible area. */
export function zoomViewport(viewport, anchor, factor, minSpan = MIN_VIEWPORT) {
  const span = Math.max(0, viewport.end - viewport.start) || 1;
  const nextSpan = clamp(span / factor, Math.min(minSpan, 1), 1);
  const position = viewport.start + clamp01(anchor) * span;
  const start = clamp(position - clamp01(anchor) * nextSpan, 0, 1 - nextSpan);
  return { start, end: start + nextSpan };
}

/** Pan the viewport by a fraction of its own width. */
export function panViewport(viewport, delta) {
  const span = Math.max(0, viewport.end - viewport.start) || 1;
  const start = clamp(viewport.start + delta * span, 0, 1 - span);
  return { start, end: start + span };
}

/** Normalize a drag into a domain-space selection, larger coordinate second. */
export function selectionFromDrag(left, right) {
  const start = clamp01(Math.min(left, right));
  const end = clamp01(Math.max(left, right));
  if (end - start < 0.0005) return null;
  return { start, end };
}

/** The steps a selection covers, in domain space. */
export function traceIDsInSelection(layout, selection) {
  if (!selection) return null;
  const ids = new Set();
  for (const span of layout.spans || []) {
    if (span.domainRight <= selection.start || span.domainLeft >= selection.end) continue;
    if (span.traceID) ids.add(span.traceID);
  }
  return ids;
}

/** A span's domain range converted to screen fractions. */
export function projectSelection(selection, viewport) {
  if (!selection) return null;
  const width = Math.max(0, viewport.end - viewport.start) || 1;
  const left = clamp01((selection.start - viewport.start) / width);
  const right = clamp01((selection.end - viewport.start) / width);
  return { left, width: Math.max(0, right - left) };
}

/** `HH:MM:SS.mmm`, the format a timeline tooltip needs. */
export function formatClock(ms) {
  if (!Number.isFinite(ms)) return "—";
  const date = new Date(ms);
  const pad = (value, size = 2) => String(value).padStart(size, "0");
  return `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}.${pad(date.getMilliseconds(), 3)}`;
}

function clamp(value, min, max) {
  return Math.min(max, Math.max(min, value));
}

function clamp01(value) {
  return clamp(Number.isFinite(value) ? value : 0, 0, 1);
}
