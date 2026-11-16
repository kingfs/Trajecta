import React from "react";
import { CollapsibleCard, StatCard } from "../common/Display";
import { EmptyState } from "../common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime, formatRawNumber } from "../../lib/monitor";
import { hotCounterEntries, detectorRuleRows } from "../../lib/detectorStats";

/**
 * The detection tab's observability section: hot-path counters, the 30-day
 * per-rule replacement tally and the suppression/mapping sizes, all from
 * GET /api/detector-plugins/stats.
 *
 * The query lives in the collapsible body, not in this wrapper: Radix mounts
 * the content only while it is open, so a reader who never expands the section
 * never pays for the read (the rule tally is a database aggregate).
 */
export function DetectorStatsSection() {
  const { t } = useI18n();
  return (
    <div className="detector-stats-section">
      <CollapsibleCard
        title={t("plugins.detectionStats")}
        subtitle={t("plugins.detectionStatsHint")}
        defaultOpen={false}
      >
        <DetectorStatsBody />
      </CollapsibleCard>
    </div>
  );
}

function DetectorStatsBody() {
  const { t } = useI18n();
  const stats = useJSON(apiPaths.detectorPluginStats);

  if (stats.error) {
    return <EmptyState title={t("plugins.detectionStatsError")} detail={stats.error} tone="danger" compact />;
  }
  if (stats.loading && !stats.data) {
    return <EmptyState title={t("plugins.detectionStatsLoading")} detail={t("plugins.detectionStatsLoadingDetail")} compact />;
  }
  if (!stats.data) {
    return null;
  }

  const counters = hotCounterEntries(stats.data.hot);
  const rules = detectorRuleRows(stats.data.rules);

  return (
    <>
      <div className="detector-stats-grid" aria-label={t("plugins.detectionStatsHot")}>
        {counters.map((entry) => (
          <StatCard
            key={entry.key}
            label={t(`plugins.detectionStats.hot.${entry.key}`, { defaultValue: entry.key })}
            value={formatRawNumber(entry.value)}
            accent={entry.key === "scanner_errors" && entry.value > 0 ? "accent-red" : ""}
            mono
          />
        ))}
        <StatCard label={t("plugins.detectionStatsSuppressions")} value={formatRawNumber(stats.data.suppressions)} mono />
        <StatCard label={t("plugins.detectionStatsMappings")} value={formatRawNumber(stats.data.mappings)} mono />
      </div>

      <h3 className="detector-stats-rules-title">{t("plugins.detectionStatsRules")}</h3>
      {rules.length ? (
        <div className="detector-stats-table-wrap">
          <table className="detector-stats-table">
            <thead>
              <tr>
                <th>{t("plugins.detectionStatsRule")}</th>
                <th>{t("plugins.detectionStatsKind")}</th>
                <th className="detector-stats-num">{t("plugins.detectionStatsHits")}</th>
                <th>{t("plugins.detectionStatsLastSeen")}</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((row) => (
                <tr key={row.rule}>
                  <td className="mono">{row.rule}</td>
                  <td className="mono">{row.kind || "—"}</td>
                  <td className="detector-stats-num mono">{formatRawNumber(row.hits)}</td>
                  <td>{formatDateTime(row.lastSeen)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState
          title={t("plugins.detectionStatsRulesEmpty")}
          detail={t("plugins.detectionStatsRulesEmptyDetail")}
          compact
        />
      )}
    </>
  );
}
