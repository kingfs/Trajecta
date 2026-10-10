import React from "react";
import { Card } from "../../components/ui/card";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime } from "../../lib/monitor";
import { SystemFact, formatBytes, formatCount, formatMs, formatPercentPoints, formatUptime } from "./format";

/**
 * The server: what this process is doing, as opposed to what the machine is
 * doing. It is the second half of what used to be one 运行时 tab.
 *
 * The split follows the data. Host metrics come from /proc and fail on a
 * platform without it; the Go runtime and the connection pool come from the
 * process itself and never fail. Interleaving them meant a machine that cannot
 * report its load average also hid its goroutine count, and it meant one very
 * long tab that a reader had to scan to the middle of.
 *
 * Three subheadings lost their right-hand note as well - the Go version, the GC
 * pause summary and the pool driver were each printed twice, once as the note
 * and once as the first fact under it.
 */
export function ServerPanel() {
  const { t } = useI18n();
  const host = useJSON(apiPaths.systemHost, []);
  const runtime = useJSON(apiPaths.systemRuntime, []);
  const live = host.data;
  const facts = runtime.data;
  const pool = facts?.db_pool;

  return (
    <>
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("system.process")}</h2>
          </div>
        </div>

        {host.error ? <EmptyState tone="danger" title={t("system.runtimeUnavailable")} detail={host.error} /> : null}
        {!host.error && !live ? <p className="system-note">{t("system.loading")}</p> : null}
        {!host.error && live?.unsupported ? <EmptyState title={t("system.hostUnsupported")} detail={live.reason || ""} /> : null}

        {!host.error && live && !live.unsupported ? (
          <div className="system-kv">
            <SystemFact label={t("system.procPid")} value={formatCount(live.process?.pid)} />
            <SystemFact label={t("system.procRss")} value={formatBytes(live.process?.rss_bytes)} />
            <SystemFact label={t("system.procCpu")} value={formatPercentPoints(live.process?.cpu_percent)} />
            <SystemFact label={t("system.procStarted")} value={live.process?.started_at ? formatDateTime(live.process.started_at) : "—"} />
          </div>
        ) : null}
      </Card>

      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("system.goProcess")}</h2>
          </div>
        </div>

        {runtime.error ? <EmptyState tone="danger" title={t("system.runtimeUnavailable")} detail={runtime.error} /> : null}
        {!runtime.error && !facts ? <p className="system-note">{t("system.loading")}</p> : null}

        {!runtime.error && facts ? (
          <>
            <div className="hero-grid hero-grid-compact">
              <StatCard label={t("system.goroutines")} value={formatCount(facts.goroutines)} />
              <StatCard label={t("system.uptime")} value={formatUptime(facts.uptime_seconds)} detail={`${formatCount(Math.round(facts.uptime_seconds))}s`} />
              <StatCard label={t("system.heapInUse")} value={formatBytes(facts.heap?.in_use_bytes)} detail={formatBytes(facts.heap?.total_sys_bytes)} />
              <StatCard label={t("system.gcCycles")} value={formatCount(facts.gc?.cycles)} accent={Number(facts.gc?.pause_total_ms || 0) > 1000 ? "accent-gold" : ""} />
            </div>

            <div className="system-subhead">
              <h3>{t("system.runtime")}</h3>
            </div>
            <div className="system-kv">
              <SystemFact label={t("system.goVersion")} value={facts.go_version} />
              <SystemFact label={t("system.platform")} value={`${facts.goos}/${facts.goarch}`} />
              <SystemFact label={t("system.numCPU")} value={facts.num_cpu} />
              <SystemFact label={t("system.gomaxprocs")} value={facts.gomaxprocs} />
              <SystemFact label={t("system.heapAlloc")} value={formatBytes(facts.heap?.alloc_bytes)} />
              <SystemFact label={t("system.heapObjects")} value={formatCount(facts.heap?.objects)} />
              <SystemFact label={t("system.totalSys")} value={formatBytes(facts.heap?.total_sys_bytes)} />
              <SystemFact label={t("system.stackBytes")} value={formatBytes(facts.heap?.stack_bytes)} />
            </div>

            <div className="system-subhead">
              <h3>{t("system.gcPauses")}</h3>
            </div>
            <div className="system-kv">
              <SystemFact label={t("system.gcPauseTotal")} value={formatMs(facts.gc?.pause_total_ms)} />
              <SystemFact label={t("system.gcLast")} value={facts.gc?.last_gc ? formatDateTime(facts.gc.last_gc) : t("system.noGCYet")} />
              <SystemFact
                label={t("system.gcRecent")}
                value={
                  facts.gc?.recent_pause_count
                    ? [facts.gc?.recent_pause_min_ms, facts.gc?.recent_pause_p50_ms, facts.gc?.recent_pause_p75_ms, facts.gc?.recent_pause_max_ms].map(formatMs).join(" / ")
                    : t("system.none")
                }
              />
            </div>

            <div className="system-subhead">
              <h3>{t("system.pool")}</h3>
            </div>
            <div className="system-kv">
              <SystemFact label={t("system.poolDriver")} value={pool?.driver || "—"} />
              <SystemFact label={t("system.poolMaxOpen")} value={formatCount(pool?.max_open)} />
              <SystemFact label={t("system.poolOpen")} value={formatCount(pool?.open)} />
              <SystemFact
                label={t("system.poolInUse")}
                value={formatCount(pool?.in_use)}
                accent={Number(pool?.in_use || 0) >= Number(pool?.max_open || 0) && Number(pool?.max_open || 0) > 0 ? "system-fact-warn" : ""}
              />
              <SystemFact label={t("system.poolIdle")} value={formatCount(pool?.idle)} />
              <SystemFact label={t("system.poolWaits")} value={formatCount(pool?.wait_count)} accent={Number(pool?.wait_count || 0) > 0 ? "system-fact-warn" : ""} />
              <SystemFact label={t("system.poolWaitTime")} value={formatMs(pool?.wait_duration_ms)} />
              <SystemFact label={t("system.poolMaxIdleClosed")} value={formatCount(pool?.max_idle_closed)} />
              <SystemFact label={t("system.poolMaxLifetimeClosed")} value={formatCount(pool?.max_lifetime_closed)} />
            </div>
          </>
        ) : null}
      </Card>
    </>
  );
}
