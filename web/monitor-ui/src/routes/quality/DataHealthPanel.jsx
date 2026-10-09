import React from "react";
import { Link, useSearchParams } from "react-router-dom";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths, apiURL } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime, normalizeAnalyticsWindow } from "../../lib/monitor";

const REFRESH_MS = 60_000;

/**
 * Derived-data health: how much of the indexed traffic has been parsed into
 * observations, whether the parse and analysis queues are keeping up, and how
 * many runtime events nobody has looked at. This panel used to sit on the
 * overview page next to the traffic tiles, where a pipeline backlog read like a
 * property of the last hour of traffic.
 */
export function DataHealthPanel() {
  const { t } = useI18n();
  const [searchParams] = useSearchParams();
  const windowValue = normalizeAnalyticsWindow(searchParams.get("window"));
  const { loading, data, error } = useJSON(apiURL(apiPaths.overview, { window: windowValue }), [windowValue], { refetchInterval: REFRESH_MS });
  const { data: eventSummary } = useJSON(apiURL(apiPaths.eventsSummary, { window: windowValue }), [windowValue], { refetchInterval: REFRESH_MS });

  const observation = data?.observation || {};
  const analysis = data?.analysis || {};
  const queued = (observation.queued ?? 0) + (observation.running ?? 0);

  return (
    <>
      {error ? <EmptyState title={t("overview.loadError")} detail={error} tone="danger" /> : null}
      {loading && !data ? <EmptyState title={t("overview.loading")} detail={t("overview.loadingDetail")} /> : null}

      <section className="panel">
        <div className="panel-head">
          <div>
            <h2>{t("health.parsed")}</h2>
          </div>
          <div className="panel-head-actions">
            <Link className="ghost-button" to="/events">
              {t("health.openEvents")}
            </Link>
          </div>
        </div>
        <p className="system-note">{t("health.subtitle")}</p>
        <div className="hero-grid hero-grid-compact overview-health-grid">
          <StatCard
            label={t("health.unreadEvents")}
            value={eventSummary?.unread ?? 0}
            detail={eventSummary?.last_seen_at ? t("health.latest", { time: formatDateTime(eventSummary.last_seen_at) }) : t("health.noRuntimeExceptions")}
            accent={(eventSummary?.unread ?? 0) ? "accent-red" : "accent-green"}
          />
          <StatCard label={t("health.parsed")} value={observation.parsed ?? 0} detail={t("health.parsedDetail", { count: observation.total_observations ?? 0 })} accent="accent-green" />
          <Link className="stat-card stat-card-link" to="/traces?observation=unparsed">
            <span>{t("health.unparsed")}</span>
            <strong>{observation.unparsed ?? 0}</strong>
            <small className="stat-detail">{t("health.unparsedDetail")}</small>
          </Link>
          <StatCard label={t("health.parseQueue")} value={queued} detail={t("health.queueDetail", { queued: observation.queued ?? 0, running: observation.running ?? 0 })} accent={queued ? "accent-gold" : ""} />
          <StatCard label={t("nav.analysis")} value={analysis.total ?? 0} detail={t("health.analysisFailed", { count: analysis.failed ?? 0 })} accent={(analysis.failed ?? 0) ? "accent-red" : "accent-gold"} />
        </div>
        <div className="panel-foot-actions">
          <Link className="ghost-button" to="/traces?observation=unparsed">
            {t("health.viewUnparsed")}
          </Link>
        </div>
      </section>
    </>
  );
}
