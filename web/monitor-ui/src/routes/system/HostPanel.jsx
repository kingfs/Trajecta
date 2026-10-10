import React from "react";
import { Card } from "../../components/ui/card";
import { MultiLineChart, UsageDonut } from "../../components/common/Charts";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatTime } from "../../lib/monitor";
import { SystemFact, formatBytes, formatCount, formatPercentPoints, formatRate, formatUptime } from "./format";

/**
 * Host resources: what the machine is doing.
 *
 * The page used to answer each question twice. CPU and memory each had a stat
 * tile, a line in the trend, a second block of numbers below it, and - for
 * memory - a meter as well; the process footprint appeared both as a tile and
 * again under 进程. Reading it meant deciding which of four identical numbers was
 * the current one.
 *
 * Each resource now has exactly one home:
 *
 *   - the trend carries history for CPU and memory
 *   - a ring carries composition for memory and disk, and its centre is the
 *     number a reader wants from it
 *   - one fact grid carries the CPU breakdown and the host facts
 *   - the network section carries throughput, as a trend plus a per-interface row
 *
 * The per-core strip is gone. Eight thin meters restated the total that is
 * already the first line of the trend, and on a large machine they were a wall
 * of noise rather than a reading.
 */
export function HostPanel() {
  const { t } = useI18n();
  const host = useJSON(apiPaths.systemHost, []);
  const live = host.data;

  // A container host reports one overlay device per image layer and one bind
  // mount per volume, all backed by the same filesystem and the same capacity.
  // Twelve rows of identical numbers is not information, so disks are folded by
  // device and the representative row lists the other mount points.
  const disks = collapseDisks(live?.disk);
  // The ring answers "how much disk is left" for the machine as a whole: the
  // entries are already folded by device, so a container's layer and its volume
  // cannot be counted twice.
  const diskTotal = disks.reduce((sum, item) => sum + Number(item.total_bytes || 0), 0);
  const diskUsed = disks.reduce((sum, item) => sum + Number(item.used_bytes || 0), 0);
  const diskPercent = diskTotal > 0 ? (diskUsed / diskTotal) * 100 : 0;

  // The two percentages share one chart because they share one unit. Throughput
  // does not, so it gets its own below rather than a second y-axis.
  const history = live?.history || [];
  const trendItems = history.map((point) => ({
    time: point.at,
    series: {
      cpu: { value: Number(point.cpu_percent || 0) },
      memory: { value: Number(point.memory_percent || 0) },
    },
  }));
  const trendSeries = [
    { key: "cpu", name: t("system.cpuUsage") },
    { key: "memory", name: t("system.memory") },
  ];

  const networkItems = history.map((point) => ({
    time: point.at,
    series: {
      rx: { value: Number(point.rx_bytes_per_sec || 0) },
      tx: { value: Number(point.tx_bytes_per_sec || 0) },
    },
  }));
  const networkSeries = [
    { key: "rx", name: t("system.netRxRate") },
    { key: "tx", name: t("system.netTxRate") },
  ];
  // Same story for bridges and veth pairs that have never carried a byte.
  const interfaces = (live?.network?.interfaces || [])
    .filter((item) => Number(item.rx_bytes || 0) + Number(item.tx_bytes || 0) > 0)
    .sort((a, b) => Number(b.rx_bytes || 0) + Number(b.tx_bytes || 0) - (Number(a.rx_bytes || 0) + Number(a.tx_bytes || 0)));

  // The kernel's three numbers nest differently than they look. Used is
  // `MemTotal - MemAvailable`, so it is the memory that cannot be reclaimed for
  // an application - the page cache is *not* inside it, which is why the cache
  // routinely exceeds used on a machine that has been up a while (24 GiB against
  // 5 GiB on the host this was written on). The cache is part of the available
  // pool, so drawing used, cached and available as three slices would count the
  // cache twice and the ring would add up to more than the machine has. The three
  // slices are used, the reclaimable cache, and what is left over; they sum to
  // the total exactly.
  const memoryTotal = Number(live?.memory?.total_bytes || 0);
  const memoryUsed = Number(live?.memory?.used_bytes || 0);
  const memoryAvailable = Number(live?.memory?.available_bytes || 0);
  const memoryCached = Math.min(Number(live?.memory?.cached_bytes || 0), memoryAvailable);
  const memoryFree = Math.max(0, memoryAvailable - memoryCached);

  return (
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

          <div className="system-subhead">
            <h3>{t("system.trendTitle")}</h3>
            <span className="system-note">{t("system.trendNote")}</span>
          </div>
          {history.length > 1 ? (
            <MultiLineChart
              items={trendItems}
              series={trendSeries}
              metric="value"
              height={240}
              valueFormatter={formatPercentPoints}
              yTickFormatter={(value) => `${Math.round(Number(value) || 0)}%`}
              labelFormatter={formatTime}
            />
          ) : (
            <p className="system-note">{t("system.trendWaiting")}</p>
          )}

          {/* The CPU breakdown and the host facts are one grid: they are all
              single numbers about the same machine, and splitting them cost a
              subheading each for two rows of values. */}
          <div className="system-subhead">
            <h3>{t("system.hostFacts")}</h3>
            <span className="system-note">{t("system.loadAverage")}: {formatNumber3(live.cpu?.load1)} / {formatNumber3(live.cpu?.load5)} / {formatNumber3(live.cpu?.load15)}</span>
          </div>
          <div className="system-kv">
            <SystemFact label={t("system.cpuUser")} value={formatPercentPoints(live.cpu?.user_percent)} />
            <SystemFact label={t("system.cpuSystem")} value={formatPercentPoints(live.cpu?.system_percent)} />
            <SystemFact label={t("system.cpuIowait")} value={formatPercentPoints(live.cpu?.iowait_percent)} />
            <SystemFact label={t("system.cpuIdle")} value={formatPercentPoints(live.cpu?.idle_percent)} />
            <SystemFact label={t("system.cpuCoresShort")} value={formatCount(live.cpu?.cores)} />
            <SystemFact label={t("system.hostKernel")} value={live.host?.kernel} />
            <SystemFact label={t("system.hostHostname")} value={live.host?.hostname} />
            <SystemFact label={t("system.hostUptime")} value={formatUptime(live.host?.uptime_seconds)} />
          </div>

          {/* Memory and disk answer the same question - how full is this - so
              they are drawn with the same widget on the same row. Each ring's
              own facts below it name its slices: a tooltip is not a legend, and
              without one the three arcs of the memory ring are unlabelled. */}
          <div className="system-split">
            <section className="system-gauge system-gauge--memory" aria-label={t("system.memory")}>
              <div className="system-subhead">
                <h3>{t("system.memory")}</h3>
              </div>
              {memoryTotal > 0 ? (
                <UsageDonut
                  height={220}
                  formatValue={formatBytes}
                  slices={[
                    { label: t("system.memUsed"), value: memoryUsed, color: "var(--accent)" },
                    { label: t("system.memCached"), value: memoryCached, color: "var(--violet)" },
                    { label: t("system.memFree"), value: memoryFree, color: "var(--surface-3)" },
                  ]}
                  centerValue={formatPercentPoints(live.memory?.used_percent)}
                  centerDetail={`${formatBytes(memoryUsed)} / ${formatBytes(memoryTotal)}`}
                />
              ) : (
                <EmptyState title={t("system.memory")} compact />
              )}
              <div className="system-kv system-kv-tight">
                <SystemFact label={t("system.memUsed")} value={formatBytes(memoryUsed)} />
                <SystemFact label={t("system.memCached")} value={formatBytes(memoryCached)} />
                <SystemFact label={t("system.memFree")} value={formatBytes(memoryFree)} />
                {/* The kernel's MemAvailable is the two slice rows above added
                    together, and it is the number to quote when the question is
                    "how much can a new process get". */}
                <SystemFact label={t("system.memAvailable")} value={formatBytes(memoryAvailable)} />
                <SystemFact label={t("system.swap")} value={Number(live.memory?.swap_total_bytes || 0) > 0 ? `${formatBytes(live.memory?.swap_used_bytes)} / ${formatBytes(live.memory?.swap_total_bytes)}` : "—"} />
              </div>
            </section>

            <section className="system-gauge system-gauge--disk" aria-label={t("system.disk")}>
              {/* Both gauges keep the same subhead structure - a heading and
                  nothing else - because a note in one and not the other makes
                  the two rings sit at different heights on the same row. */}
              <div className="system-subhead">
                <h3>{t("system.disk")}</h3>
              </div>
              {diskTotal > 0 ? (
                <UsageDonut
                  height={220}
                  formatValue={formatBytes}
                  slices={[
                    { label: t("system.diskUsed"), value: diskUsed, color: "var(--accent)" },
                    { label: t("system.diskAvailable"), value: Math.max(0, diskTotal - diskUsed), color: "var(--surface-3)" },
                  ]}
                  centerValue={formatPercentPoints(diskPercent)}
                  centerDetail={`${formatBytes(diskUsed)} / ${formatBytes(diskTotal)}`}
                />
              ) : (
                <EmptyState title={t("system.disk")} compact />
              )}
              <div className="system-kv system-kv-tight">
                <SystemFact label={t("system.diskUsed")} value={formatBytes(diskUsed)} />
                <SystemFact label={t("system.diskAvailable")} value={formatBytes(Math.max(0, diskTotal - diskUsed))} />
                <SystemFact label={t("system.diskMount")} value={disks.length ? disks.map((item) => item.mount).join("、") : "—"} />
              </div>
            </section>
          </div>

          <div className="system-subhead">
            <h3>{t("system.network")}</h3>
            <span className="system-note">{`${t("system.netRx")} ${formatRate(live.network?.rx_bytes_per_sec)} · ${t("system.netTx")} ${formatRate(live.network?.tx_bytes_per_sec)}`}</span>
          </div>
          {networkItems.length > 1 ? (
            <MultiLineChart
              items={networkItems}
              series={networkSeries}
              metric="value"
              height={200}
              valueFormatter={formatRate}
              yTickFormatter={(value) => formatBytes(Number(value) || 0)}
              labelFormatter={formatTime}
            />
          ) : (
            <p className="system-note">{t("system.trendWaiting")}</p>
          )}
          {interfaces.length ? (
            <div className="trace-table system-table system-table--net">
              <div className="trace-table-head system-table-head system-table-head--net">
                <span>{t("system.netIface")}</span>
                <span>{t("system.netRxRate")}</span>
                <span>{t("system.netTxRate")}</span>
              </div>
              {interfaces.map((item) => (
                // The cumulative counters are the row's title rather than two more
                // columns: they are totals since boot, so they only grow and said
                // nothing the rates beside them did not already answer.
                <div className="trace-row system-row system-table-row--net" key={item.name} title={`${t("system.netRx")} ${formatBytes(item.rx_bytes)} · ${t("system.netTx")} ${formatBytes(item.tx_bytes)}`}>
                  <span className="mono">
                    {item.name}
                    {item.loopback ? <span className="system-tag">{t("system.netLoopback")}</span> : null}
                  </span>
                  <span className="mono">{formatRate(item.rx_bytes_per_sec)}</span>
                  <span className="mono">{formatRate(item.tx_bytes_per_sec)}</span>
                </div>
              ))}
            </div>
          ) : (
            <p className="system-note">{t("system.none")}</p>
          )}
        </>
      ) : null}
    </Card>
  );
}

// Three decimals: a load average of 0.40 and one of 0.04 are different machines,
// and at two decimals they read the same.
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
