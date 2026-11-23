import React from "react";
import { Card } from "../ui/card";
import { EmptyState } from "../common/EmptyState";
import { InlineTag } from "../common/Badges";
import { Button } from "../ui/button";
import { StepsStatsTable } from "./StepsStatsTable";
import { TrajectoryLedger } from "./TrajectoryLedger";
import { useSessionSteps } from "../../hooks/useSessionSteps";
import { useI18n } from "../../lib/i18n";
import { hasEstimates } from "../../lib/sessionSteps";

/*
 * The shell both step views share: fetch one window, explain how it was
 * derived, and hand the turns to the table or the ledger.
 *
 * The panel is deliberately loud about two things a reader would otherwise
 * misread: the turn grouping is inferred from the exchange sequence (the proxy
 * cannot see agent events), and a step whose cassette was not read in time has
 * numbers but no content. Both arrive as tags on the header rather than as
 * silent gaps.
 */
export function SessionStepsPanel({ mode, traceID, sessionID, onOpenTrace, activeTraceID }) {
  const { t } = useI18n();
  const {
    view,
    loading,
    error,
    canLoadOlder,
    canLoadNewer,
    loadingOlder,
    loadingNewer,
    loadOlder,
    loadNewer,
  } = useSessionSteps({ traceID, sessionID });

  if (loading && !view) {
    return <EmptyState title={t("steps.loading")} detail={t("steps.loadingDetail")} />;
  }
  // A failed page fetch must not throw away the pages already on screen; only
  // a failure with nothing loaded is a full error state.
  if (error && !view) {
    return <EmptyState title={t("steps.loadError")} detail={error} tone="danger" />;
  }
  if (!view || !(view.turns || []).length) {
    return <EmptyState title={t("steps.empty")} detail={sessionID ? t("steps.emptyDetail") : t("steps.emptyNoSession")} />;
  }
  const window = view.window || {};
  const totals = view.totals || {};
  const pending = Number(window.content_pending || 0);
  const estimated = hasEstimates(view);

  return (
    <Card as="section" className="steps-panel">
      <div className="panel-head">
        <div>
          <h2>{mode === "trajectory" ? t("traceDetail.tabTrajectory") : t("traceDetail.tabStats")}</h2>
          <p className="panel-subtitle">
            {t("steps.windowSummary", {
              first: window.first_step ?? 0,
              last: window.last_step ?? 0,
              steps: totals.steps ?? 0,
            })}
          </p>
        </div>
        <div className="trace-tag-group">
          {estimated ? <InlineTag tone="gold">{t("steps.inferred")}</InlineTag> : null}
          {pending ? (
            <InlineTag tone="gold">{t("steps.contentPending", { count: pending })}</InlineTag>
          ) : null}
          {window.pages > 1 ? <InlineTag tone="accent">{t("steps.pagesLoaded", { count: window.pages })}</InlineTag> : null}
        </div>
      </div>
      {error ? <p className="steps-page-error">{error}</p> : null}
      {window.has_before || window.has_after ? (
        <div className="steps-pager">
          {window.has_before ? (
            <Button variant="ghost" size="sm" onClick={loadOlder} disabled={!canLoadOlder}>
              {loadingOlder ? t("steps.loadingOlder") : t("steps.loadOlder")}
            </Button>
          ) : null}
          {window.has_after ? (
            <Button variant="ghost" size="sm" onClick={loadNewer} disabled={!canLoadNewer}>
              {loadingNewer ? t("steps.loadingNewer") : t("steps.loadNewer")}
            </Button>
          ) : null}
        </div>
      ) : null}
      {mode === "trajectory" ? (
        <TrajectoryLedger view={view} onOpenTrace={onOpenTrace} activeTraceID={activeTraceID} />
      ) : (
        <StepsStatsTable view={view} onOpenTrace={onOpenTrace} activeTraceID={activeTraceID} />
      )}
      <p className="steps-footnote">{t("steps.footnote")}</p>
    </Card>
  );
}
