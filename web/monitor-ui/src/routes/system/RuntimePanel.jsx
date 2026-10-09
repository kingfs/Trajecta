import React from "react";
import { Card } from "../../components/ui/card";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime } from "../../lib/monitor";
import { Meter, SystemFact, formatBytes, formatCount, formatMs, formatPercentPoints, formatRate, formatUptime } from "./format";

/**
 * Runtime is split into two panels on purpose: what the machine is doing (host
 * CPU, memory, disks, network, process footprint) and what the Go process is
 * doing (heap, GC, goroutines, connection pool). The second one is served by
 * /api/system/runtime, the first by /api/system/host, so a slow or unsupported
 * host probe never blanks the process view.
 */
export function RuntimePanel() {
  const { t } = useI18n();

  const host = useJSON(apiPaths.systemHost, []);
  const runtime = useJSON(apiPaths.systemRuntime, []);
  const live = host.data;
  const facts = runtime.data;
  const pool = facts?.db_pool;
  // A container host reports one overlay device per image layer and one bind
  // mount per volume, all backed by the same filesystem and the same capacity.
  // Twelve rows of identical numbers is not information, so disks are folded by
  // device and the representative row lists the other mount points.
  const disks = collapseDisks(live?.disk);
  // Same story for bridges and veth pairs that have never carried a byte.
  const interfaces = (live?.network?.interfaces || [])
    .filter((item) => Number(item.rx_bytes || 0) + Number(item.tx_bytes || 0) > 0)
    .sort((a, b) => Number(b.rx_bytes || 0) + Number(b.tx_bytes || 0) - (Number(a.rx_bytes || 0) + Number(a.tx_bytes || 0)));

  return (
    <>
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("system.hostSection")}</h2>
          </div>
          <div className="panel-head-actions">
            {live?.host?.hostname ? <span className="badge">{live.host.hostname}</span> : null}
          </div>
        </div>

        {host.error ? <EmptyState tone="danger" title={t("system.runtimeUnavailable")} detail={host.error} /> : null}
        {!host.error && !live ? <p className="system-note">{t("system.loading")}</p> : null}
        {!host.error && live?.unsupported ? <EmptyState title={t("system.hostUnsupported")} detail={live.reason || ""} /> : null}

        {!host.error && live && !live.unsupported ? (
          <>
            {(live.warnings || []).length ? (
              <div className="system-warnings">
                <strong>{t("system.hostSection")}</strong>
                <ul>
                  {(live.warnings || []).map((warning, index) => (
                    <li key={index}>{warning}</li>
                  ))}
                </ul>
              </div>
            ) : null}

            <div className="hero-grid hero-grid-compact">
              <StatCard
                label={t("system.cpuUsage")}
                value={formatPercentPoints(live.cpu?.usage_percent)}
                detail={t("system.cpuCores", { count: formatCount(live.cpu?.cores) })}
                accent={Number(live.cpu?.usage_percent || 0) >= 90 ? "accent-red" : Number(live.cpu?.usage_percent || 0) >= 75 ? "accent-gold" : ""}
              />
              <StatCard
                label={t("system.memory")}
                value={formatPercentPoints(live.memory?.used_percent)}
                detail={`${formatBytes(live.memory?.used_bytes)} / ${formatBytes(live.memory?.total_bytes)}`}
                accent={Number(live.memory?.used_percent || 0) >= 90 ? "accent-red" : Number(live.memory?.used_percent || 0) >= 75 ? "accent-gold" : ""}
              />
              <StatCard label={t("system.procRss")} value={formatBytes(live.process?.rss_bytes)} detail={`${t("system.procThreads")} ${formatCount(live.process?.threads)}`} />
              <StatCard
                label={t("system.network")}
                value={formatRate(live.network?.rx_bytes_per_sec)}
                detail={`${t("system.netTx")} ${formatRate(live.network?.tx_bytes_per_sec)}`}
              />
            </div>

            <div className="system-subhead">
              <h3>{t("system.cpu")}</h3>
              <span className="system-note">{t("system.loadAverage")}: {formatNumber3(live.cpu?.load1)} / {formatNumber3(live.cpu?.load5)} / {formatNumber3(live.cpu?.load15)}</span>
            </div>
            <div className="system-kv">
              <SystemFact label={t("system.cpuUser")} value={formatPercentPoints(live.cpu?.user_percent)} />
              <SystemFact label={t("system.cpuSystem")} value={formatPercentPoints(live.cpu?.system_percent)} />
              <SystemFact label={t("system.cpuIowait")} value={formatPercentPoints(live.cpu?.iowait_percent)} />
              <SystemFact label={t("system.hostKernel")} value={live.host?.kernel} />
              <SystemFact label={t("system.hostUptime")} value={formatUptime(live.host?.uptime_seconds)} />
              <SystemFact label={t("system.procStarted")} value={live.process?.started_at ? formatDateTime(live.process.started_at) : "—"} />
            </div>
            {(live.cpu?.per_core_percent || []).length ? (
              <div className="core-grid" aria-label={t("system.cpuPerCore")}>
                {(live.cpu.per_core_percent || []).map((value, index) => (
                  <div className="core-cell" key={index} title={`cpu${index} ${formatPercentPoints(value)}`}>
                    <Meter percent={value} label={`cpu${index} ${formatPercentPoints(value)}`} />
                    <span className="mono">{index}</span>
                  </div>
                ))}
              </div>
            ) : null}

            <div className="system-subhead">
              <h3>{t("system.memory")}</h3>
              <span className="system-note">{formatBytes(live.memory?.total_bytes)}</span>
            </div>
            <Meter percent={live.memory?.used_percent} label={t("system.memory")} />
            <div className="system-kv">
              <SystemFact label={t("system.memUsed")} value={formatBytes(live.memory?.used_bytes)} />
              <SystemFact label={t("system.memAvailable")} value={formatBytes(live.memory?.available_bytes)} />
              <SystemFact label={t("system.memCached")} value={formatBytes(live.memory?.cached_bytes)} />
              <SystemFact
                label={t("system.swap")}
                value={Number(live.memory?.swap_total_bytes || 0) > 0 ? `${formatBytes(live.memory?.swap_used_bytes)} / ${formatBytes(live.memory?.swap_total_bytes)}` : "—"}
                accent={Number(live.memory?.swap_used_percent || 0) >= 75 ? "system-fact-warn" : ""}
              />
            </div>

            <div className="system-subhead">
              <h3>{t("system.disk")}</h3>
              <span className="system-note">{formatCount(disks.length)}</span>
            </div>
            {disks.length ? (
              <div className="trace-table system-table">
                <div className="trace-table-head system-table-head system-table-head--disk">
                  <span>{t("system.diskMount")}</span>
                  <span>{t("system.diskFilesystem")}</span>
                  <span>{t("system.diskSize")}</span>
                  <span>{t("system.diskUsed")}</span>
                  <span>{t("system.diskAvailable")}</span>
                  <span>{t("system.diskUsedPercent")}</span>
                </div>
                {disks.map((item) => (
                  <div className="trace-row system-row system-table-row--disk" key={item.mount}>
                    <span className="mono" title={item.device}>
                      {item.mount}
                      {item.extraMounts ? <span className="system-note"> +{item.extraMounts}</span> : null}
                    </span>
                    <span className="mono">{item.filesystem}</span>
                    <span className="mono">{formatBytes(item.total_bytes)}</span>
                    <span className="mono">{formatBytes(item.used_bytes)}</span>
                    <span className="mono">{formatBytes(item.available_bytes)}</span>
                    <span className="disk-meter-cell">
                      <Meter percent={item.used_percent} label={`${item.mount} ${formatPercentPoints(item.used_percent)}`} />
                      <span className="mono">{formatPercentPoints(item.used_percent)}</span>
                    </span>
                  </div>
                ))}
              </div>
            ) : (
              <EmptyState title={t("system.disk")} compact />
            )}

            <div className="system-subhead">
              <h3>{t("system.network")}</h3>
              <span className="system-note">{`${t("system.netRx")} ${formatRate(live.network?.rx_bytes_per_sec)} · ${t("system.netTx")} ${formatRate(live.network?.tx_bytes_per_sec)}`}</span>
            </div>
            {interfaces.length ? (
              <div className="trace-table system-table">
                <div className="trace-table-head system-table-head system-table-head--net">
                  <span>{t("system.netIface")}</span>
                  <span>{t("system.netRx")}</span>
                  <span>{t("system.netTx")}</span>
                  <span>{t("system.netRxRate")}</span>
                  <span>{t("system.netTxRate")}</span>
                </div>
                {interfaces.map((item) => (
                  <div className="trace-row system-row system-table-row--net" key={item.name}>
                    <span className="mono">
                      {item.name}
                      {item.loopback ? <span className="system-note"> · {t("system.netLoopback")}</span> : null}
                    </span>
                    <span className="mono">{formatBytes(item.rx_bytes)}</span>
                    <span className="mono">{formatBytes(item.tx_bytes)}</span>
                    <span className="mono">{formatRate(item.rx_bytes_per_sec)}</span>
                    <span className="mono">{formatRate(item.tx_bytes_per_sec)}</span>
                  </div>
                ))}
              </div>
            ) : null}

            <div className="system-subhead">
              <h3>{t("system.process")}</h3>
            </div>
            <div className="system-kv">
              <SystemFact label={t("system.procPid")} value={formatCount(live.process?.pid)} />
              <SystemFact label={t("system.procRss")} value={formatBytes(live.process?.rss_bytes)} />
              <SystemFact label={t("system.procCpu")} value={formatPercentPoints(live.process?.cpu_percent)} />
              <SystemFact label={t("system.procThreads")} value={formatCount(live.process?.threads)} />
              <SystemFact label={t("system.procFds")} value={formatCount(live.process?.open_fds)} />
            </div>
          </>
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
              <span className="system-note">{facts.go_version}</span>
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
              <span className="system-note">{t("system.gcRecent")}</span>
            </div>
            <div className="system-kv">
              <SystemFact label={t("system.gcCycles")} value={formatCount(facts.gc?.cycles)} />
              <SystemFact label={t("system.gcPauses")} value={formatCount(facts.gc?.recent_pause_count)} />
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
              <span className="system-note">{pool?.driver || "—"}</span>
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

function formatNumber3(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return number.toFixed(2);
}

// Fold disk entries that describe the same device down to one row and keep the
// shortest mount path as its label, because that is the one a human recognises.
function collapseDisks(list) {
  const byDevice = new Map();
  for (const item of list || []) {
    const key = item.device || `${item.filesystem}:${item.total_bytes}:${item.used_bytes}`;
    const existing = byDevice.get(key);
    if (!existing) {
      byDevice.set(key, { ...item, mounts: [item.mount] });
      continue;
    }
    existing.mounts.push(item.mount);
  }
  return [...byDevice.values()].map((entry) => {
    const mounts = entry.mounts.slice().sort((a, b) => a.length - b.length || a.localeCompare(b));
    return { ...entry, mount: mounts[0], extraMounts: mounts.length - 1 };
  });
}
