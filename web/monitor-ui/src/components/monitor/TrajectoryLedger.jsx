import React, { useEffect, useMemo, useRef, useState } from "react";
import { InlineTag } from "../common/Badges";
import { Input } from "../ui/input";
import { useI18n } from "../../lib/i18n";
import { buildLedgerRows, filterLedgerRows, formatMillis, formatRatioPercent, formatTokens } from "../../lib/sessionSteps";

/*
 * The 轨迹 tab: a session read as an event ledger.
 *
 * One row per fact - a turn opens, a step is one model exchange, the assistant
 * answered, a tool was called, its result came back - so a reader can scan what
 * happened in order instead of opening exchanges one at a time. Clicking a
 * step's row opens that trace; the ledger is an index, not a second detail
 * view, and every payload here is a server-truncated preview.
 *
 * The turn grouping is inferred (the proxy sees HTTP, not agent events), which
 * is why the header carries an "推断" tag and the turn rules are explained in
 * the panel description rather than hidden.
 */
export function TrajectoryLedger({ view, onOpenTrace, activeTraceID }) {
  const { t } = useI18n();
  const [query, setQuery] = useState("");
  const [collapsed, setCollapsed] = useState(() => new Set());
  const activeRef = useRef(null);

  const rows = useMemo(() => buildLedgerRows(view?.turns || []), [view]);
  const visibleRows = useMemo(() => filterLedgerRows(rows, query), [rows, query]);
  const searchActive = query.trim().length > 0;
  const turnIndexes = useMemo(() => rows.filter((row) => row.kind === "turn").map((row) => row.index), [rows]);

  useEffect(() => {
    if (!activeTraceID) return;
    const node = activeRef.current;
    if (node && typeof node.scrollIntoView === "function") {
      node.scrollIntoView({ block: "nearest" });
    }
  }, [activeTraceID, view]);

  if (!rows.length) return null;

  const toggleAll = () => {
    if (collapsed.size === turnIndexes.length) setCollapsed(new Set());
    else setCollapsed(new Set(turnIndexes));
  };

  const toggleTurn = (index) => {
    const next = new Set(collapsed);
    if (next.has(index)) next.delete(index);
    else next.add(index);
    setCollapsed(next);
  };

  const rendered = [];
  let lastTurn = null;
  for (const row of visibleRows) {
    if (row.kind === "turn") {
      lastTurn = row.index;
      rendered.push(
        <TurnRow
          key={row.key}
          row={row}
          collapsed={collapsed.has(row.index)}
          onToggle={() => toggleTurn(row.index)}
        />,
      );
      continue;
    }
    // A collapsed turn keeps its header and hides its facts, which is what
    // makes a two-hundred-step session scannable.
    if (lastTurn !== null && collapsed.has(lastTurn)) continue;
    rendered.push(
      <Row
        key={row.key}
        row={row}
        active={row.step?.trace_id === activeTraceID}
        activeRef={row.step?.trace_id === activeTraceID ? activeRef : null}
        onOpen={onOpenTrace}
      />,
    );
  }

  return (
    <>
      <div className="ledger-toolbar">
        <Input
          type="search"
          value={query}
          placeholder={t("steps.ledgerSearchPlaceholder")}
          aria-label={t("steps.ledgerSearch")}
          onChange={(event) => setQuery(event.target.value)}
        />
        <button type="button" className="ledger-toggle" onClick={toggleAll}>
          {collapsed.size === turnIndexes.length ? t("steps.expandAll") : t("steps.collapseAll")}
        </button>
        {searchActive ? <span className="ledger-count">{t("steps.ledgerMatches", { count: visibleRows.length - countTurns(visibleRows) })}</span> : null}
      </div>
      <div className="ledger-list">{rendered}</div>
    </>
  );
}

function countTurns(rows) {
  return rows.filter((row) => row.kind === "turn").length;
}

function TurnRow({ row, collapsed, onToggle }) {
  const { t } = useI18n();
  const trigger = row.trigger || {};
  return (
    <button type="button" className="ledger-turn" onClick={onToggle} aria-expanded={!collapsed}>
      <span className={collapsed ? "ledger-caret" : "ledger-caret ledger-caret-open"} aria-hidden="true">
        ▸
      </span>
      <strong>{t("steps.turnLabel", { turn: row.index })}</strong>
      <span className="ledger-turn-trigger">
        {trigger.text || t(`steps.trigger.${trigger.kind || "unknown"}`)}
      </span>
      <span className="ledger-turn-meta">
        {t("steps.stepsCount", { count: row.aggregate?.steps ?? 0 })}
        {row.aggregate?.failures ? <InlineTag tone="danger">{t("steps.failedCount", { count: row.aggregate.failures })}</InlineTag> : null}
        <span>{formatMillis(row.wallMs)}</span>
      </span>
    </button>
  );
}

function Row({ row, active, activeRef, onOpen }) {
  const { t } = useI18n();
  const className = ["ledger-row", `ledger-row-${row.kind}`, active ? "ledger-row-active" : ""].filter(Boolean).join(" ");
  if (row.kind === "step") {
    const step = row.step || {};
    return (
      <button
        type="button"
        ref={activeRef}
        className={`${className} ledger-row-clickable`}
        onClick={() => onOpen?.(step.trace_id)}
        title={t("steps.openTrace")}
      >
        <span className="ledger-kind ledger-kind-step">{t("steps.kindStep")}</span>
        <span className="ledger-step-index">#{step.global_index}</span>
        <span className="ledger-step-model">{step.model || t("steps.unknownModel")}</span>
        <span className="ledger-step-tags">
          {step.is_stream ? <InlineTag tone="accent">stream</InlineTag> : null}
          {step.status >= 400 ? <InlineTag tone="danger">{step.status}</InlineTag> : null}
          {step.estimated?.includes("content_pending") ? <InlineTag tone="gold">{t("steps.partialTag")}</InlineTag> : null}
        </span>
        <span className="ledger-step-usage">
          <span>{t("steps.contextShort", { value: formatTokens(step.usage?.context) })}</span>
          <span>{t("steps.outputShort", { value: formatTokens(step.usage?.output) })}</span>
          <span>{formatRatioPercent(step.usage?.cache_hit)}</span>
          <span>{formatMillis(step.timing?.model_ms)}</span>
        </span>
      </button>
    );
  }
  if (row.kind === "answer") {
    return (
      <div className={className}>
        <span className="ledger-kind ledger-kind-answer">{t("steps.kindAnswer")}</span>
        <span className="ledger-text">{row.text}</span>
      </div>
    );
  }
  if (row.kind === "tool") {
    const call = row.call || {};
    const result = row.result;
    return (
      <div className={className}>
        <span className="ledger-kind ledger-kind-tool">{t("steps.kindTool")}</span>
        <div className="ledger-tool-body">
          <div className="ledger-tool-head">
            <strong>{call.name || t("steps.unknownTool")}</strong>
            {call.args ? <span className="ledger-tool-args">{call.args}</span> : null}
          </div>
          {result ? (
            <div className="ledger-tool-result">
              <span aria-hidden="true">→</span>
              <span>{result.text || t("steps.emptyResult")}</span>
              {result.is_error ? <InlineTag tone="danger">{t("steps.toolError")}</InlineTag> : null}
            </div>
          ) : (
            <div className="ledger-tool-result ledger-tool-pending">{t("steps.resultPending")}</div>
          )}
        </div>
      </div>
    );
  }
  return null;
}
