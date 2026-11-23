import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useI18n } from "../../lib/i18n";
import { formatMillis } from "../../lib/sessionSteps";
import {
  TIMELINE_LANES,
  buildTimelineSpans,
  formatClock,
  layoutTimeline,
  panViewport,
  projectSelection,
  selectionFromDrag,
  timelineMode,
  traceIDsInSelection,
  zoomViewport,
} from "../../lib/timeline";

/*
 * The 轨迹 timeline: three lanes over the same window the ledger renders.
 *
 * The proxy only sees exchanges, so a step is drawn as what it produced: a
 * marker where the request started (输入), the upstream exchange with the
 * first-byte boundary inside it (模型), and the gap until the next request
 * (工具, which is where the client ran the tools).
 *
 * Interaction follows the DSH trajectory: the 时长 switch picks between equal
 * widths and real durations, 实际时间 keeps or removes the idle gaps, the wheel
 * zooms around the cursor, dragging on empty space selects a range (which
 * filters the ledger), a right-drag pans, and Escape or a double click clears.
 * Every span is a preview; the ledger is the accessible list.
 */

const LANE_LABELS = {
  input: "steps.laneInput",
  model: "steps.laneModel",
  tools: "steps.laneTools",
};

const DURATION_KEY = "trajecta.timeline.duration";
const ACTUAL_TIME_KEY = "trajecta.timeline.actualTime";
const HOVER_DELAY_MS = 300;

function readFlag(key) {
  try {
    return window.localStorage.getItem(key) === "1";
  } catch {
    return false;
  }
}

function writeFlag(key, value) {
  try {
    window.localStorage.setItem(key, value ? "1" : "0");
  } catch {
    // A private window with storage disabled is not a reason to break the view.
  }
}

export function TrajectoryTimeline({ turns, activeTraceID, searchMatches, onSelectionChange, hasBefore, onLoadOlder, loadingOlder }) {
  const { t } = useI18n();
  const [actualDuration, setActualDuration] = useState(() => readFlag(DURATION_KEY));
  const [actualTime, setActualTime] = useState(() => readFlag(ACTUAL_TIME_KEY));
  const [viewport, setViewport] = useState({ start: 0, end: 1 });
  const [selection, setSelection] = useState(null);
  const [drag, setDrag] = useState(null);
  const [hover, setHover] = useState(null);
  const trackRef = useRef(null);
  const dragRef = useRef(null);
  const hoverTimer = useRef(null);

  const mode = timelineMode({ actualDuration, actualTime });
  const { spans, turns: turnMarkers } = useMemo(() => buildTimelineSpans(turns), [turns]);
  const layout = useMemo(() => layoutTimeline(spans, { mode, viewport }), [spans, mode, viewport]);

  useEffect(() => writeFlag(DURATION_KEY, actualDuration), [actualDuration]);
  useEffect(() => writeFlag(ACTUAL_TIME_KEY, actualTime), [actualTime]);

  // The selected steps are derived here because only the layout knows which
  // spans a range touches; the ledger only needs the ids.
  useEffect(() => {
    if (!onSelectionChange) return;
    onSelectionChange(selection ? traceIDsInSelection(layout, selection) : null);
  }, [layout, selection, onSelectionChange]);

  // Leaving the view must not leave the ledger filtered by an invisible range.
  useEffect(() => () => onSelectionChange?.(null), [onSelectionChange]);

  const clearHover = useCallback(() => {
    if (hoverTimer.current) {
      window.clearTimeout(hoverTimer.current);
      hoverTimer.current = null;
    }
    setHover(null);
  }, []);

  const reset = useCallback(() => {
    setViewport({ start: 0, end: 1 });
    setSelection(null);
    clearHover();
  }, [clearHover]);

  const fractionAt = useCallback((clientX) => {
    const rect = trackRef.current?.getBoundingClientRect();
    if (!rect || rect.width <= 0) return 0;
    return Math.min(1, Math.max(0, (clientX - rect.left) / rect.width));
  }, []);

  const domainAt = useCallback(
    (fraction) => viewport.start + fraction * (viewport.end - viewport.start),
    [viewport],
  );

  const handleWheel = useCallback(
    (event) => {
      if (!spans.length) return;
      event.preventDefault();
      const factor = Math.exp(-event.deltaY * 0.0015);
      setViewport((current) => zoomViewport(current, fractionAt(event.clientX), factor));
    },
    [fractionAt, spans.length],
  );

  // React attaches wheel listeners passively, so preventDefault there is
  // ignored and the page would scroll while zooming. The native listener is
  // registered explicitly as non-passive.
  useEffect(() => {
    const node = trackRef.current;
    if (!node) return undefined;
    node.addEventListener("wheel", handleWheel, { passive: false });
    return () => node.removeEventListener("wheel", handleWheel);
  }, [handleWheel]);

  const handlePointerDown = useCallback(
    (event) => {
      if (event.button === 2) {
        dragRef.current = { kind: "pan", x: event.clientX };
        event.currentTarget.setPointerCapture?.(event.pointerId);
        return;
      }
      if (event.button !== 0) return;
      const fraction = fractionAt(event.clientX);
      dragRef.current = { kind: "select", x: event.clientX, anchor: domainAt(fraction) };
      event.currentTarget.setPointerCapture?.(event.pointerId);
    },
    [domainAt, fractionAt],
  );

  const handlePointerMove = useCallback(
    (event) => {
      const active = dragRef.current;
      if (active?.kind === "pan") {
        const rect = trackRef.current?.getBoundingClientRect();
        if (rect?.width) {
          const delta = (event.clientX - active.x) / rect.width;
          active.x = event.clientX;
          setViewport((current) => panViewport(current, -delta));
        }
        return;
      }
      if (active?.kind === "select") {
        const moved = Math.abs(event.clientX - active.x);
        if (moved < 4) return;
        const current = selectionFromDrag(active.anchor, domainAt(fractionAt(event.clientX)));
        setDrag(current);
        return;
      }
      // Hovering: the tooltip is deliberately delayed so dragging across the
      // lanes does not flash a card at every span.
      const key = event.target?.dataset?.spanKey;
      if (!key) {
        clearHover();
        return;
      }
      const span = layout.spans.find((item) => item.key === key);
      if (!span) return;
      const rect = trackRef.current?.getBoundingClientRect();
      const rawX = event.clientX - (rect?.left || 0);
      const point = { x: Math.min(rawX, Math.max(0, (rect?.width || 0) - 260)), y: event.clientY - (rect?.top || 0), span };
      if (hover?.span?.key === key) {
        setHover(point);
        return;
      }
      clearHover();
      hoverTimer.current = window.setTimeout(() => setHover(point), HOVER_DELAY_MS);
    },
    [clearHover, domainAt, fractionAt, hover, layout.spans],
  );

  const handlePointerUp = useCallback(
    (event) => {
      const active = dragRef.current;
      dragRef.current = null;
      if (!active) return;
      event.currentTarget.releasePointerCapture?.(event.pointerId);
      if (active.kind === "pan") return;
      const moved = Math.abs(event.clientX - active.x) < 4;
      if (!moved) {
        const dropped = selectionFromDrag(active.anchor, domainAt(fractionAt(event.clientX)));
        setDrag(null);
        setSelection(dropped);
        return;
      }
      // A click without a drag selects the step under the cursor, which is the
      // gesture that makes the timeline useful without a precise drag.
      const traceID = event.target?.dataset?.traceId;
      setDrag(null);
      if (traceID) {
        const touched = layout.spans.filter((span) => span.traceID === traceID);
        const start = Math.min(...touched.map((span) => span.domainLeft));
        const end = Math.max(...touched.map((span) => span.domainRight));
        setSelection({ start, end });
        return;
      }
      setSelection(null);
    },
    [domainAt, fractionAt, layout.spans],
  );

  const handleKeyDown = useCallback(
    (event) => {
      if (event.key === "Escape") {
        setSelection(null);
        event.stopPropagation();
      }
    },
    [],
  );

  if (!spans.length) return null;

  const projected = projectSelection(selection || drag, viewport);
  const laneTurns = turnMarkers
    .map((marker) => {
      const anchorSpan = layout.spans.find((span) => span.turnKey === marker.key && span.lane === "input");
      return anchorSpan ? { ...marker, left: anchorSpan.left } : null;
    })
    .filter(Boolean);

  return (
    <div className="timeline">
      <div className="timeline-head">
        <p className="timeline-hint">{t("steps.timelineHint")}</p>
        <div className="timeline-actions">
          <button
            type="button"
            className={actualDuration ? "timeline-toggle timeline-toggle-on" : "timeline-toggle"}
            aria-pressed={actualDuration}
            onClick={() => setActualDuration((value) => !value)}
          >
            {t("steps.timelineDuration")}
          </button>
          <button
            type="button"
            className={actualTime ? "timeline-toggle timeline-toggle-on" : "timeline-toggle"}
            aria-pressed={actualTime}
            onClick={() => setActualTime((value) => !value)}
          >
            {t("steps.timelineActualTime")}
          </button>
          <button type="button" className="timeline-toggle" onClick={reset}>
            {t("steps.timelineReset")}
          </button>
        </div>
      </div>
      <div className="timeline-body">
        {hasBefore ? (
          <button type="button" className="timeline-more" onClick={onLoadOlder} disabled={loadingOlder} title={t("steps.loadOlder")}>
            …
          </button>
        ) : null}
        <div
          className="timeline-track"
          ref={trackRef}
          role="group"
          aria-label={t("steps.timelineLabel")}
          tabIndex={0}
          onPointerDown={handlePointerDown}
          onPointerMove={handlePointerMove}
          onPointerUp={handlePointerUp}
          onPointerLeave={clearHover}
          onDoubleClick={reset}
          onKeyDown={handleKeyDown}
          onContextMenu={(event) => event.preventDefault()}
        >
          {TIMELINE_LANES.map((lane) => (
            <div className="timeline-lane" key={lane} data-lane={lane}>
              <div className="timeline-lane-track">
                {laneTurns.map((marker) => (
                  <span
                    key={`${lane}-turn-${marker.key}`}
                    className="timeline-turn"
                    style={{ left: `${marker.left * 100}%` }}
                    aria-hidden="true"
                  />
                ))}
                {layout.spans
                  .filter((span) => span.lane === lane && span.visible && span.width > 0)
                  .map((span) => (
                    <span
                      key={span.key}
                      className={[
                        "timeline-span",
                        `timeline-span-${span.lane}`,
                        span.failed ? "timeline-span-failed" : "",
                        span.traceID === activeTraceID ? "timeline-span-active" : "",
                        searchMatches && !searchMatches.has(span.traceID) ? "timeline-span-dimmed" : "",
                        searchMatches && searchMatches.has(span.traceID) ? "timeline-span-match" : "",
                      ]
                        .filter(Boolean)
                        .join(" ")}
                      style={{ left: `${span.left * 100}%`, width: `${Math.max(span.width, 0.0015) * 100}%` }}
                      data-span-key={span.key}
                      data-trace-id={span.traceID}
                    >
                      {span.lane === "model" && span.ttft !== null ? (
                        <span className="timeline-ttft" style={{ left: `${span.ttft * 100}%` }} aria-hidden="true" />
                      ) : null}
                    </span>
                  ))}
              </div>
            </div>
          ))}
          {projected && projected.width > 0 ? (
            <span
              className="timeline-selection"
              style={{ left: `${projected.left * 100}%`, width: `${projected.width * 100}%` }}
              aria-hidden="true"
            />
          ) : null}
          {hover ? (
            <span
              className="timeline-tooltip"
              style={{ left: `${hover.x}px`, top: `${hover.y}px` }}
              role="tooltip"
            >
              <strong>{t(LANE_LABELS[hover.span.lane])}</strong>
              <span>
                {formatClock(hover.span.start)} → {formatClock(hover.span.end)}
              </span>
              <span>
                {t("steps.timelineTotal", { value: formatMillis(Math.max(0, hover.span.end - hover.span.start)) })}
                {hover.span.lane === "model" && hover.span.ttftMs
                  ? ` · ${t("steps.columnTTFT")} ${formatMillis(hover.span.ttftMs)}`
                  : ""}
              </span>
            </span>
          ) : null}
        </div>
      </div>
      <div className="timeline-legend">
        <span className="timeline-legend-item">
          <span className="timeline-swatch timeline-swatch-input" />
          {t("steps.laneInput")}
        </span>
        <span className="timeline-legend-item">
          <span className="timeline-swatch timeline-swatch-model" />
          {t("steps.laneModel")}
        </span>
        <span className="timeline-legend-item">
          <span className="timeline-swatch timeline-swatch-tools" />
          {t("steps.laneTools")}
        </span>
        <span className="timeline-mode">{t(`steps.timelineMode.${mode}`)}</span>
      </div>
    </div>
  );
}
