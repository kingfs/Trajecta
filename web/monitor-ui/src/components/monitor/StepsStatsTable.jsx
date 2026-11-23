import React, { useMemo, useState } from "react";
import { InlineTag } from "../common/Badges";
import { SegmentedControl, SegmentedControlItem } from "../ui/segmented-control";
import { useI18n } from "../../lib/i18n";
import { formatDateTime } from "../../lib/monitor";
import {
  barWidthPercent,
  formatMillis,
  formatRatioPercent,
  formatThroughput,
  formatTokens,
  stepScale,
  timeBarSegments,
  toolSummary,
  turnAggregate,
  turnScale,
  windowAggregate,
} from "../../lib/sessionSteps";

/*
 * The 统计 tab: one row per turn, expandable into its steps.
 *
 * The layout follows what a reader asks of a long agent session - "which turn
 * burned the context, where did the time go" - so a turn row carries the
 * aggregate and a step row carries the same columns for one exchange. The
 * numbers are the server's; the only decisions here are the context mode (a
 * turn can show the latest prompt size or the cumulative one) and the bar
 * scale, which compares steps within a turn and turns within the window.
 */
export function StepsStatsTable({ view, onOpenTrace, activeTraceID }) {
  const { t } = useI18n();
  const turns = useMemo(() => view?.turns || [], [view]);
  const [expanded, setExpanded] = useState(null);
  const [contextMode, setContextMode] = useState("latest");
  const [showTotals, setShowTotals] = useState(false);

  // The newest turn is open by default: it is the one a reader just produced.
  const autoOpen = useMemo(() => new Set(turns.length ? [turns[turns.length - 1].index] : []), [turns]);
  const openTurns = expanded ?? autoOpen;
  const turnsScale = useMemo(() => turnScale(turns), [turns]);
  const totals = useMemo(() => windowAggregate(turns), [turns]);

  if (!turns.length) return null;

  const toggleTurn = (index) => {
    const next = new Set(openTurns);
    if (next.has(index)) next.delete(index);
    else next.add(index);
    setExpanded(next);
  };

  const contextValue = (aggregate) => (contextMode === "latest" ? aggregate.latestContext : aggregate.context);

  return (
    <>
      <div className="steps-toolbar">
        <span className="steps-toolbar-label">{t("steps.contextMode")}</span>
        <SegmentedControl value={contextMode} onValueChange={setContextMode} aria-label={t("steps.contextMode")}>
          <SegmentedControlItem value="latest" active={contextMode === "latest"}>
            {t("steps.contextLatest")}
          </SegmentedControlItem>
          <SegmentedControlItem value="cumulative" active={contextMode === "cumulative"}>
            {t("steps.contextCumulative")}
          </SegmentedControlItem>
        </SegmentedControl>
        <label className="steps-checkbox">
          <input type="checkbox" checked={showTotals} onChange={(event) => setShowTotals(event.target.checked)} />
          <span>{t("steps.showTotals")}</span>
        </label>
      </div>
      <div className="steps-table-scroll">
        <table className="steps-table">
          <thead>
            <tr>
              <th scope="col" title={t("steps.scopeHint")}>
                {t("steps.columnScope")}
              </th>
              <th scope="col" title={t("steps.triggerHint")}>
                {t("steps.columnTrigger")}
              </th>
              <th scope="col" title={t("steps.didHint")}>
                {t("steps.columnDid")}
              </th>
              <th scope="col" title={t("steps.contextHint")} className="steps-num">
                {t("steps.columnContext")}
              </th>
              <th scope="col" title={t("steps.outputHint")} className="steps-num">
                {t("steps.columnOutput")}
              </th>
              <th scope="col" title={t("steps.cacheHint")} className="steps-num">
                {t("steps.columnCache")}
              </th>
              <th scope="col" title={t("steps.ttftHint")} className="steps-num">
                {t("steps.columnTTFT")}
              </th>
              <th scope="col" title={t("steps.modelHint")} className="steps-num">
                {t("steps.columnModel")}
              </th>
              <th scope="col" title={t("steps.tpsHint")} className="steps-num">
                {t("steps.columnTPS")}
              </th>
              <th scope="col" title={t("steps.toolHint")} className="steps-num">
                {t("steps.columnTool")}
              </th>
              <th scope="col" title={t("steps.timeHint")} className="steps-num">
                {t("steps.columnTime")}
              </th>
            </tr>
          </thead>
          <tbody>
            {turns.map((turn) => {
              const aggregate = turnAggregate(turn);
              const open = openTurns.has(turn.index);
              const scale = stepScale(turn);
              const segments = timeBarSegments({ wallMs: turn.wall_ms, modelMs: aggregate.modelMs, ttftMs: aggregate.ttftMs, toolWaitMs: aggregate.toolWaitMs });
              return (
                <React.Fragment key={`turn-${turn.index}`}>
                  <tr className="steps-row-turn">
                    <td className="steps-scope">
                      {/* A real button, not a click handler on the row: the
                          table needs a keyboard-reachable control with
                          aria-expanded, and a <tr role="button"> would break
                          the table semantics a screen reader relies on. */}
                      <button
                        type="button"
                        className="steps-scope-toggle"
                        aria-expanded={open}
                        onClick={() => toggleTurn(turn.index)}
                      >
                        <span className={open ? "steps-caret steps-caret-open" : "steps-caret"} aria-hidden="true">
                          ▸
                        </span>
                        <strong>{t("steps.turnLabel", { turn: turn.index })}</strong>
                      </button>
                      <span className="steps-scope-meta">
                        {t("steps.stepsCount", { count: aggregate.steps })}
                        {aggregate.failures ? <InlineTag tone="danger">{t("steps.failedCount", { count: aggregate.failures })}</InlineTag> : null}
                      </span>
                    </td>
                    <td className="steps-trigger">
                      <TriggerCell trigger={turn.trigger} />
                    </td>
                    <td className="steps-did">
                      <DidCell aggregate={aggregate} tools={turn.steps?.flatMap((step) => step.did?.tools || []) || []} />
                    </td>
                    <td className="steps-num">{formatTokens(contextValue(aggregate))}</td>
                    <td className="steps-num">{formatTokens(aggregate.output)}</td>
                    <td className="steps-num">{formatRatioPercent(aggregate.cacheHit)}</td>
                    <td className="steps-num">{formatMillis(aggregate.ttftMs)}</td>
                    <td className="steps-num">{formatMillis(aggregate.modelMs)}</td>
                    <td className="steps-num">{formatThroughput(aggregate.tps)}</td>
                    <td className="steps-num">{formatMillis(aggregate.toolWaitMs)}</td>
                    <td className="steps-num">
                      <TimeCell segments={segments} totalMs={turn.wall_ms} scale={turnsScale} wide />
                    </td>
                  </tr>
                  {open
                    ? turn.steps?.map((step, stepIndex) => {
                        const previous = stepIndex > 0 ? turn.steps[stepIndex - 1] : null;
                        const stepSegments = timeBarSegments({
                          wallMs: step.timing?.wall_ms ?? step.timing?.model_ms,
                          modelMs: step.timing?.model_ms,
                          ttftMs: step.timing?.ttft_ms,
                          toolWaitMs: step.timing?.tool_wait_ms,
                        });
                        return (
                          <tr
                            key={step.trace_id}
                            className={
                              step.trace_id === activeTraceID ? "steps-row-step steps-row-step-active" : "steps-row-step"
                            }
                          >
                            <td className="steps-scope">
                              <span className="steps-scope-meta steps-global">#{step.global_index}</span>
                              <button
                                type="button"
                                className="steps-scope-link"
                                title={t("steps.openTrace")}
                                onClick={() => onOpenTrace?.(step.trace_id)}
                              >
                                {t("steps.stepLabel", { step: step.index })}
                              </button>
                              {step.status >= 400 ? <InlineTag tone="danger">{step.status}</InlineTag> : null}
                              {step.estimated?.includes("duplicate_request") ? <InlineTag tone="gold">{t("steps.retryTag")}</InlineTag> : null}
                            </td>
                            <td className="steps-trigger">
                              <span className="steps-step-time">{formatDateTime(step.started_at)}</span>
                              <StepGap step={step} previous={previous} />
                            </td>
                            <td className="steps-did">
                              <div className="steps-did-text">{toolSummary(step) || t("steps.noTools")}</div>
                              {step.did?.answer_chars ? (
                                <span className="steps-did-meta">{t("steps.answerChars", { count: step.did.answer_chars })}</span>
                              ) : null}
                            </td>
                            <td className="steps-num">{formatTokens(step.usage?.context)}</td>
                            <td className="steps-num">{formatTokens(step.usage?.output)}</td>
                            <td className="steps-num">{formatRatioPercent(step.usage?.cache_hit)}</td>
                            <td className="steps-num">{formatMillis(step.timing?.ttft_ms)}</td>
                            <td className="steps-num">{formatMillis(step.timing?.model_ms)}</td>
                            <td className="steps-num">{formatThroughput(step.timing?.tps)}</td>
                            <td className="steps-num">{formatMillis(step.timing?.tool_wait_ms)}</td>
                            <td className="steps-num">
                              <TimeCell segments={stepSegments} totalMs={step.timing?.wall_ms ?? step.timing?.model_ms} scale={scale} />
                            </td>
                          </tr>
                        );
                      })
                    : null}
                </React.Fragment>
              );
            })}
            {showTotals ? (
              <tr className="steps-row-total">
                <td className="steps-scope">
                  <strong>{t("steps.totalsWindow")}</strong>
                </td>
                <td className="steps-trigger">
                  {t("steps.totalsWindowHint", { turns: turns.length, steps: totals.steps })}
                </td>
                <td className="steps-did">{formatTokens(totals.calls)}</td>
                <td className="steps-num">{formatTokens(totals.context)}</td>
                <td className="steps-num">{formatTokens(totals.output)}</td>
                <td className="steps-num">{formatRatioPercent(totals.cacheHit)}</td>
                <td className="steps-num">{formatMillis(totals.ttftMs)}</td>
                <td className="steps-num">{formatMillis(totals.modelMs)}</td>
                <td className="steps-num">{formatThroughput(totals.tps)}</td>
                <td className="steps-num">{formatMillis(totals.toolWaitMs)}</td>
                <td className="steps-num">{formatMillis(totals.wallMs)}</td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
    </>
  );
}

function TriggerCell({ trigger }) {
  const { t } = useI18n();
  if (!trigger || !trigger.kind) return <span className="steps-muted">—</span>;
  if (trigger.kind === "user" && trigger.text) {
    return <span className="steps-trigger-text">{trigger.text}</span>;
  }
  return <InlineTag tone="accent">{t(`steps.trigger.${trigger.kind}`)}</InlineTag>;
}

function DidCell({ aggregate, tools }) {
  const { t } = useI18n();
  const summary = summarizeTools(tools);
  return (
    <>
      <div className="steps-did-text">{summary || t("steps.noTools")}</div>
      {aggregate.calls ? <span className="steps-did-meta">{t("steps.callsCount", { count: aggregate.calls })}</span> : null}
    </>
  );
}

function StepGap({ step, previous }) {
  const { t } = useI18n();
  if (!previous) return null;
  const previousEnd = new Date(previous.ended_at).getTime();
  const started = new Date(step.started_at).getTime();
  if (!Number.isFinite(previousEnd) || !Number.isFinite(started)) return null;
  const gap = Math.max(0, started - previousEnd);
  if (gap < 1000) return null;
  return <span className="steps-gap">{t("steps.gapFromPrevious", { value: formatMillis(gap) })}</span>;
}

function TimeCell({ segments, totalMs, scale, wide = false }) {
  const { t } = useI18n();
  const width = barWidthPercent(totalMs, scale);
  if (!Number.isFinite(Number(totalMs)) || Number(totalMs) <= 0) {
    return <span className="steps-muted">—</span>;
  }
  return (
    <div className={wide ? "steps-time steps-time-wide" : "steps-time"}>
      <div className="steps-time-track" title={t("steps.timeHint")}>
        <div className="steps-time-fill" style={{ width: `${Math.max(width, 4)}%` }}>
          {(segments.segments || []).map((segment) => (
            <span key={segment.key} className={`steps-time-seg steps-time-${segment.key}`} style={{ flexGrow: segment.ms }} />
          ))}
        </div>
      </div>
      <span className="steps-time-value">{formatMillis(totalMs)}</span>
    </div>
  );
}

function summarizeTools(tools) {
  const counts = new Map();
  for (const tool of tools) {
    if (!tool?.name) continue;
    counts.set(tool.name, (counts.get(tool.name) || 0) + Number(tool.calls || 1));
  }
  return [...counts.entries()].map(([name, calls]) => (calls > 1 ? `${name} ×${calls}` : name)).join(" · ");
}
