import React from "react";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime } from "../../lib/monitor";
import { formatCount, formatMs } from "./format";

const REFRESH_MS = 60_000;

/**
 * Slow-query tab: the statement collector's armed/not-armed state and the
 * statements it recorded. The ring is in memory and is empty until
 * debug.slow_query_threshold is set to a positive duration, so "not armed" is
 * a normal state and not an error.
 *
 * The shell owns the page header, so this component renders only its own panel
 * body and keeps its own 60s refresh.
 */
export function SlowQueryPanel() {
  const { t } = useI18n();

  const slowQueries = useJSON(apiPaths.systemSlowQueries, [], { refetchInterval: REFRESH_MS });
  const slow = slowQueries.data;

  return (
    <>
      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>{t("system.slowQueries")}</h2>
          </div>
          <div className="panel-head-actions">
            {slow ? <span className="badge">{slow.enabled ? formatMs(slow.threshold_ms) : t("system.off")}</span> : null}
          </div>
        </div>

        {slowQueries.error ? <EmptyState tone="danger" title={t("system.slowUnavailable")} detail={slowQueries.error} /> : null}
        {!slowQueries.error && !slow ? <p className="system-note">{t("system.loading")}</p> : null}
        {!slowQueries.error && slow && !slow.enabled ? <EmptyState title={t("system.slowDisabledTitle")} detail={t("system.slowDisabledDetail")} /> : null}
        {!slowQueries.error && slow && slow.enabled ? (
          <>
            <p className="system-note">{t("system.slowEnabled", { threshold: formatMs(slow.threshold_ms), capacity: formatCount(slow.capacity) })}</p>
            {(slow.items || []).length === 0 ? (
              <EmptyState title={t("system.slowEmpty")} />
            ) : (
              <div className="trace-table system-table system-table--slow">
                <div className="trace-table-head system-table-head">
                  <span>{t("system.slowAt")}</span>
                  <span>{t("system.slowOperation")}</span>
                  <span>{t("system.slowDuration")}</span>
                  <span>{t("system.slowStatement")}</span>
                </div>
                {slow.items.map((item, index) => (
                  <div className="trace-row system-row" key={`${item.at}-${index}`}>
                    <span className="mono">{formatDateTime(item.at)}</span>
                    <span className="mono">{item.operation}</span>
                    <span className="mono">{formatMs(item.duration_ms)}</span>
                    <span className="mono system-statement" title={item.statement}>{item.statement}</span>
                  </div>
                ))}
              </div>
            )}
          </>
        ) : null}
      </section>
    </>
  );
}
