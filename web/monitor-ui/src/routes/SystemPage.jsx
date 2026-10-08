import React, { useEffect, useState } from "react";
import { InlineTag } from "../components/common/Badges";
import { StatCard } from "../components/common/Display";
import { EmptyState } from "../components/common/EmptyState";
import { useJSON } from "../hooks/useJSON";
import { apiPaths } from "../lib/api";
import { useI18n } from "../lib/i18n";
import { formatDateTime } from "../lib/monitor";

const REFRESH_MS = 60_000;

// The page is admin-only, and it is also the one page whose text is about the
// process rather than about traces, so it keeps its own small dictionary
// instead of growing the shared one by forty keys.
const copy = {
  "zh-CN": {
    eyebrow: "诊断",
    title: "系统",
    refresh: "每 60 秒刷新",
    sampled: (value) => `采样于 ${value}`,
    runtimeUnavailable: "运行时快照不可用",
    databaseUnavailable: "数据库快照不可用",
    slowUnavailable: "慢查询快照不可用",
    goroutines: "协程",
    uptime: "运行时长",
    heapInUse: "堆在用",
    gcCycles: "GC 次数",
    goProcess: "Go 进程",
    runtime: "运行时",
    goVersion: "Go 版本",
    platform: "平台",
    numCPU: "CPU 核数",
    gomaxprocs: "GOMAXPROCS",
    heapAlloc: "累计堆分配",
    heapObjects: "存活堆对象",
    totalSys: "进程总内存",
    stackBytes: "栈内存",
    gcPauses: "GC 暂停",
    gcPauseTotal: "暂停累计",
    gcLast: "最近一次 GC",
    gcRecent: "最近暂停 min / p50 / p75 / max",
    noGCYet: "尚未发生 GC",
    pool: "连接池",
    poolDriver: "驱动",
    poolMaxOpen: "最大连接",
    poolOpen: "已打开",
    poolInUse: "使用中",
    poolIdle: "空闲",
    poolWaits: "等待次数",
    poolWaitTime: "等待累计",
    poolMaxIdleClosed: "因空闲被关闭",
    poolMaxLifetimeClosed: "因超时被关闭",
    database: "数据库",
    databasePanel: "PostgreSQL 统计",
    databaseSize: "数据库大小",
    cacheHit: "缓存命中率",
    tempBytes: "临时文件写入",
    tempFiles: "临时文件",
    deadlocks: "死锁",
    transactions: "事务提交 / 回滚",
    server: "服务器",
    serverVersion: "PostgreSQL 版本",
    settings: "关键参数",
    settingUnitBytes: "字节",
    activity: "连接与活动",
    sessions: "本库连接",
    totalSessions: "实例总连接",
    byState: "按状态",
    byWaitEvent: "按等待事件",
    longestQuery: "运行最久的语句",
    longestQueryNone: "当前没有运行中的语句",
    longestQueryMeta: (pid, state, wait, duration) => `PID ${pid} · ${state}${wait ? ` · 等待 ${wait}` : ""} · ${duration}`,
    checkpointer: "检查点",
    checkpointerTimed: "定时检查点",
    checkpointerRequested: "请求检查点",
    checkpointerWrite: "写入耗时",
    checkpointerSync: "同步耗时",
    checkpointerBuffers: "写出缓冲区",
    relations: "最大的表（public，按总大小）",
    relationName: "关系",
    relationTotal: "总大小",
    relationHeap: "堆大小",
    relationRows: "估算行数",
    unusedIndexes: "未被使用的索引（idx_scan = 0，按大小）",
    unusedIndexesHint: "每个未使用索引都在写入时付出维护成本；核对后再决定是否删除。",
    worstIndexes: "读放大最严重的索引（idx_tup_read / idx_scan）",
    hottestTables: "写入最频繁的表",
    indexName: "索引",
    indexTable: "表",
    indexScans: "扫描次数",
    indexTuplesRead: "读取元组",
    indexTuplesFetched: "取回元组",
    indexSize: "大小",
    indexFetchRatio: "取回比",
    tableName: "表",
    tableLive: "存活行",
    tableInserts: "插入",
    tableUpdates: "更新",
    tableDeletes: "删除",
    tableSeqScan: "顺序扫描",
    tableSeqTupRead: "顺序扫描行",
    tableSize: "大小",
    none: "无",
    warnings: "采集警告",
    slowQueries: "慢查询",
    slowCollector: "语句采集器",
    slowDisabledTitle: "慢查询采集器已关闭",
    slowDisabledDetail: "设置 debug.slow_query_threshold（例如 \"200ms\"）并重启服务后，慢于该阈值的语句会被记录。阈值为 0 时采集器完全不记录，热路径没有额外开销。",
    slowEnabled: (threshold, capacity) => `已开启：记录慢于 ${threshold} 的语句，最多保留最近 ${capacity} 条`,
    slowAt: "时间",
    slowOperation: "操作",
    slowDuration: "耗时",
    slowStatement: "语句（已脱敏并截断）",
    slowEmpty: "采集器已开启，但还没有记录到慢语句。",
    unsupportedTitle: "当前数据库不使用 PostgreSQL",
    loading: "加载中…",
    off: "已关闭",
    yes: "是",
    no: "否",
  },
  en: {
    eyebrow: "Diagnostics",
    title: "System",
    refresh: "Refreshes every 60s",
    sampled: (value) => `sampled ${value}`,
    runtimeUnavailable: "Runtime snapshot unavailable",
    databaseUnavailable: "Database snapshot unavailable",
    slowUnavailable: "Slow-query snapshot unavailable",
    goroutines: "Goroutines",
    uptime: "Uptime",
    heapInUse: "Heap in use",
    gcCycles: "GC cycles",
    goProcess: "Go process",
    runtime: "Runtime",
    goVersion: "Go version",
    platform: "Platform",
    numCPU: "CPU cores",
    gomaxprocs: "GOMAXPROCS",
    heapAlloc: "Cumulative heap allocation",
    heapObjects: "Live heap objects",
    totalSys: "Total process memory",
    stackBytes: "Stack memory",
    gcPauses: "GC pauses",
    gcPauseTotal: "Pause total",
    gcLast: "Last GC",
    gcRecent: "Recent pause min / p50 / p75 / max",
    noGCYet: "no collection yet",
    pool: "Connection pool",
    poolDriver: "Driver",
    poolMaxOpen: "Max open",
    poolOpen: "Open",
    poolInUse: "In use",
    poolIdle: "Idle",
    poolWaits: "Wait count",
    poolWaitTime: "Wait duration",
    poolMaxIdleClosed: "Closed (idle)",
    poolMaxLifetimeClosed: "Closed (lifetime)",
    database: "Database",
    databasePanel: "PostgreSQL statistics",
    databaseSize: "Database size",
    cacheHit: "Cache hit ratio",
    tempBytes: "Temp file bytes",
    tempFiles: "Temp files",
    deadlocks: "Deadlocks",
    transactions: "Transactions committed / rolled back",
    server: "Server",
    serverVersion: "PostgreSQL version",
    settings: "Key settings",
    settingUnitBytes: "bytes",
    activity: "Connections and activity",
    sessions: "Sessions on this database",
    totalSessions: "Sessions on the instance",
    byState: "By state",
    byWaitEvent: "By wait event",
    longestQuery: "Longest running statement",
    longestQueryNone: "no statement is currently running",
    longestQueryMeta: (pid, state, wait, duration) => `PID ${pid} · ${state}${wait ? ` · waiting on ${wait}` : ""} · ${duration}`,
    checkpointer: "Checkpointer",
    checkpointerTimed: "Timed checkpoints",
    checkpointerRequested: "Requested checkpoints",
    checkpointerWrite: "Write time",
    checkpointerSync: "Sync time",
    checkpointerBuffers: "Buffers written",
    relations: "Largest relations (public, by total size)",
    relationName: "Relation",
    relationTotal: "Total",
    relationHeap: "Heap",
    relationRows: "Estimated rows",
    unusedIndexes: "Unused indexes (idx_scan = 0, by size)",
    unusedIndexesHint: "Every unused index is still maintained on write; confirm before dropping one.",
    worstIndexes: "Worst read amplification (idx_tup_read / idx_scan)",
    hottestTables: "Busiest tables",
    indexName: "Index",
    indexTable: "Table",
    indexScans: "Scans",
    indexTuplesRead: "Tuples read",
    indexTuplesFetched: "Tuples fetched",
    indexSize: "Size",
    indexFetchRatio: "Fetch ratio",
    tableName: "Table",
    tableLive: "Live rows",
    tableInserts: "Inserts",
    tableUpdates: "Updates",
    tableDeletes: "Deletes",
    tableSeqScan: "Seq scans",
    tableSeqTupRead: "Seq rows read",
    tableSize: "Size",
    none: "none",
    warnings: "Collector warnings",
    slowQueries: "Slow queries",
    slowCollector: "Statement collector",
    slowDisabledTitle: "Slow-query collector disabled",
    slowDisabledDetail: "Set debug.slow_query_threshold (for example \"200ms\") and restart to record statements slower than that. At a threshold of 0 the collector records nothing and adds no work to the hot path.",
    slowEnabled: (threshold, capacity) => `Armed: records statements slower than ${threshold}, keeping the newest ${capacity}`,
    slowAt: "Time",
    slowOperation: "Operation",
    slowDuration: "Duration",
    slowStatement: "Statement (redacted and truncated)",
    slowEmpty: "The collector is armed but has not recorded a slow statement yet.",
    unsupportedTitle: "This deployment does not use PostgreSQL",
    loading: "Loading…",
    off: "off",
    yes: "yes",
    no: "no",
  },
};

export function SystemPage() {
  const { language } = useI18n();
  const text = copy[language] || copy["zh-CN"];
  const [refreshTick, setRefreshTick] = useState(0);

  useEffect(() => {
    const timer = window.setInterval(() => setRefreshTick((tick) => tick + 1), REFRESH_MS);
    return () => window.clearInterval(timer);
  }, []);

  const runtime = useJSON(apiPaths.systemRuntime, [refreshTick]);
  const database = useJSON(apiPaths.systemDatabase, [refreshTick]);
  const slowQueries = useJSON(apiPaths.systemSlowQueries, [refreshTick]);

  const facts = runtime.data;
  const db = database.data;
  const slow = slowQueries.data;
  const pool = facts?.db_pool;

  return (
    <main className="shell shell-list">
      <header className="topbar">
        <div>
          <p className="eyebrow">{text.eyebrow}</p>
          <h1>{text.title}</h1>
        </div>
        <div className="topbar-meta">
          <span className="badge">{text.refresh}</span>
          {facts?.generated_at ? <span className="badge">{text.sampled(formatDateTime(facts.generated_at))}</span> : null}
        </div>
      </header>

      {runtime.error ? <EmptyState tone="danger" title={text.runtimeUnavailable} detail={runtime.error} /> : null}

      <section className="hero-grid hero-grid-compact">
        <StatCard label={text.goroutines} value={facts ? formatCount(facts.goroutines) : "—"} />
        <StatCard label={text.uptime} value={facts ? formatUptime(facts.uptime_seconds) : "—"} detail={facts ? `${formatCount(Math.round(facts.uptime_seconds))}s` : ""} />
        <StatCard label={text.heapInUse} value={facts ? formatBytes(facts.heap?.in_use_bytes) : "—"} detail={facts ? formatBytes(facts.heap?.total_sys_bytes) : ""} />
        <StatCard label={text.gcCycles} value={facts ? formatCount(facts.gc?.cycles) : "—"} accent={facts && Number(facts.gc?.pause_total_ms || 0) > 1000 ? "accent-gold" : ""} />
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{text.goProcess}</p>
            <h2>{text.runtime}</h2>
          </div>
        </div>
        {!facts ? (
          <p className="system-note">{text.loading}</p>
        ) : (
          <>
            <div className="system-kv">
              <SystemFact label={text.goVersion} value={facts.go_version} />
              <SystemFact label={text.platform} value={`${facts.goos}/${facts.goarch}`} />
              <SystemFact label={text.numCPU} value={facts.num_cpu} />
              <SystemFact label={text.gomaxprocs} value={facts.gomaxprocs} />
              <SystemFact label={text.heapAlloc} value={formatBytes(facts.heap?.alloc_bytes)} />
              <SystemFact label={text.heapObjects} value={formatCount(facts.heap?.objects)} />
              <SystemFact label={text.totalSys} value={formatBytes(facts.heap?.total_sys_bytes)} />
              <SystemFact label={text.stackBytes} value={formatBytes(facts.heap?.stack_bytes)} />
            </div>

            <div className="system-subhead">
              <h3>{text.gcPauses}</h3>
              <span className="system-note">{text.gcRecent}</span>
            </div>
            <div className="system-kv">
              <SystemFact label={text.gcCycles} value={formatCount(facts.gc?.cycles)} />
              <SystemFact label={text.gcPauses} value={formatCount(facts.gc?.recent_pause_count)} />
              <SystemFact label={text.gcPauseTotal} value={formatMs(facts.gc?.pause_total_ms)} />
              <SystemFact label={text.gcLast} value={facts.gc?.last_gc ? formatDateTime(facts.gc.last_gc) : text.noGCYet} />
              <SystemFact
                label={text.gcRecent}
                value={
                  facts.gc?.recent_pause_count
                    ? [facts.gc?.recent_pause_min_ms, facts.gc?.recent_pause_p50_ms, facts.gc?.recent_pause_p75_ms, facts.gc?.recent_pause_max_ms].map(formatMs).join(" / ")
                    : text.none
                }
              />
            </div>

            <div className="system-subhead">
              <h3>{text.pool}</h3>
              <span className="system-note">{pool?.driver || "—"}</span>
            </div>
            <div className="system-kv">
              <SystemFact label={text.poolDriver} value={pool?.driver || "—"} />
              <SystemFact label={text.poolMaxOpen} value={formatCount(pool?.max_open)} />
              <SystemFact label={text.poolOpen} value={formatCount(pool?.open)} />
              <SystemFact label={text.poolInUse} value={formatCount(pool?.in_use)} accent={Number(pool?.in_use || 0) >= Number(pool?.max_open || 0) && Number(pool?.max_open || 0) > 0 ? "system-fact-warn" : ""} />
              <SystemFact label={text.poolIdle} value={formatCount(pool?.idle)} />
              <SystemFact label={text.poolWaits} value={formatCount(pool?.wait_count)} accent={Number(pool?.wait_count || 0) > 0 ? "system-fact-warn" : ""} />
              <SystemFact label={text.poolWaitTime} value={formatMs(pool?.wait_duration_ms)} />
              <SystemFact label={text.poolMaxIdleClosed} value={formatCount(pool?.max_idle_closed)} />
              <SystemFact label={text.poolMaxLifetimeClosed} value={formatCount(pool?.max_lifetime_closed)} />
            </div>
          </>
        )}
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{text.database}</p>
            <h2>{text.databasePanel}</h2>
          </div>
        </div>
        {database.error ? <EmptyState tone="danger" title={text.databaseUnavailable} detail={database.error} /> : null}
        {!database.error && !db ? <p className="system-note">{text.loading}</p> : null}
        {!database.error && db && (db.unsupported || !db.supported) ? (
          <EmptyState title={text.unsupportedTitle} detail={db.reason || `driver: ${db.driver || "unknown"}`} />
        ) : null}
        {!database.error && db && db.supported ? <DatabasePanel db={db} text={text} /> : null}
      </section>

      <section className="panel">
        <div className="panel-head">
          <div>
            <p className="eyebrow">{text.slowCollector}</p>
            <h2>{text.slowQueries}</h2>
          </div>
          {slow ? <span className="badge">{slow.enabled ? formatMs(slow.threshold_ms) : text.off}</span> : null}
        </div>
        {slowQueries.error ? <EmptyState tone="danger" title={text.slowUnavailable} detail={slowQueries.error} /> : null}
        {!slowQueries.error && !slow ? <p className="system-note">{text.loading}</p> : null}
        {!slowQueries.error && slow && !slow.enabled ? <EmptyState title={text.slowDisabledTitle} detail={text.slowDisabledDetail} /> : null}
        {!slowQueries.error && slow && slow.enabled ? (
          <>
            <p className="system-note">{text.slowEnabled(formatMs(slow.threshold_ms), formatCount(slow.capacity))}</p>
            {(slow.items || []).length === 0 ? (
              <EmptyState title={text.slowEmpty} />
            ) : (
              <div className="trace-table system-table system-table--slow">
                <div className="trace-table-head system-table-head">
                  <span>{text.slowAt}</span>
                  <span>{text.slowOperation}</span>
                  <span>{text.slowDuration}</span>
                  <span>{text.slowStatement}</span>
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
    </main>
  );
}

function DatabasePanel({ db, text }) {
  const cacheHit = Number(db.database?.cache_hit_ratio || 0);
  const cacheAccent = cacheHit > 0 && cacheHit < 0.95 ? (cacheHit < 0.9 ? "accent-red" : "accent-gold") : cacheHit > 0 ? "accent-green" : "";
  const unused = db.indexes?.unused || [];
  const worst = db.indexes?.worst_tup_read_per_scan || [];
  const relations = db.relations || [];
  const tables = db.tables || [];
  const settings = db.settings || [];
  const activity = db.activity;
  const unitOf = (setting) => (setting.unit === "B" || setting.unit === "8kB" ? formatSettingBytes(setting) : "");

  return (
    <>
      <div className="hero-grid hero-grid-compact">
        <StatCard label={text.databaseSize} value={formatBytes(db.database?.size_bytes)} />
        <StatCard label={text.cacheHit} value={cacheHit ? formatPercent(cacheHit) : "—"} accent={cacheAccent} detail={`${formatCount(db.database?.blks_hit)} / ${formatCount((db.database?.blks_hit || 0) + (db.database?.blks_read || 0))}`} />
        <StatCard label={text.tempBytes} value={formatBytes(db.database?.temp_bytes)} accent={Number(db.database?.temp_bytes || 0) > 0 ? "accent-gold" : ""} detail={`${formatCount(db.database?.temp_files)} ${text.tempFiles}`} />
        <StatCard label={text.deadlocks} value={formatCount(db.database?.deadlocks)} accent={Number(db.database?.deadlocks || 0) > 0 ? "accent-red" : ""} />
      </div>

      {(db.warnings || []).length > 0 ? (
        <div className="system-warnings">
          <strong>{text.warnings}</strong>
          <ul>
            {db.warnings.map((warning) => (
              <li key={warning} className="mono">{warning}</li>
            ))}
          </ul>
        </div>
      ) : null}

      <div className="system-subhead">
        <h3>{text.server}</h3>
      </div>
      <div className="system-kv">
        <SystemFact label={text.serverVersion} value={db.server?.version || "—"} />
        <SystemFact label={text.transactions} value={`${formatCount(db.database?.xact_commit)} / ${formatCount(db.database?.xact_rollback)}`} />
      </div>

      <div className="system-subhead">
        <h3>{text.settings}</h3>
      </div>
      <div className="system-kv">
        {settings.map((setting) => (
          <SystemFact key={setting.name} label={setting.name} value={unitOf(setting) || setting.value || "—"} />
        ))}
      </div>

      <div className="system-subhead">
        <h3>{text.activity}</h3>
      </div>
      <div className="system-kv">
        <SystemFact label={text.sessions} value={formatCount(activity?.sessions)} />
        <SystemFact label={text.totalSessions} value={formatCount(activity?.total_sessions)} />
        <div className="system-fact">
          <span>{text.byState}</span>
          <div className="system-tags">
            {(activity?.by_state || []).length === 0 ? <span className="system-note">{text.none}</span> : null}
            {(activity?.by_state || []).map((entry) => (
              <InlineTag key={entry.label} tone={entry.label === "active" ? "accent-green" : "default"}>{`${entry.label} × ${formatCount(entry.count)}`}</InlineTag>
            ))}
          </div>
        </div>
        <div className="system-fact">
          <span>{text.byWaitEvent}</span>
          <div className="system-tags">
            {(activity?.by_wait_event_type || []).length === 0 ? <span className="system-note">{text.none}</span> : null}
            {(activity?.by_wait_event_type || []).map((entry) => (
              <InlineTag key={entry.label} tone={entry.label === "none" ? "default" : "accent-gold"}>{`${entry.label} × ${formatCount(entry.count)}`}</InlineTag>
            ))}
          </div>
        </div>
      </div>

      <div className="system-subhead">
        <h3>{text.longestQuery}</h3>
      </div>
      {activity?.longest_query ? (
        <div className="system-longest">
          <span className="system-note">
            {text.longestQueryMeta(activity.longest_query.pid, activity.longest_query.state || "unknown", activity.longest_query.wait_event || activity.longest_query.wait_event_type || "", formatMs(activity.longest_query.duration_ms))}
          </span>
          <pre className="code-block system-statement">{activity.longest_query.statement}</pre>
        </div>
      ) : (
        <p className="system-note">{text.longestQueryNone}</p>
      )}

      {db.checkpointer ? (
        <>
          <div className="system-subhead">
            <h3>{text.checkpointer}</h3>
            <span className="system-note">{db.checkpointer.source}</span>
          </div>
          <div className="system-kv">
            <SystemFact label={text.checkpointerTimed} value={formatCount(db.checkpointer.num_timed)} />
            <SystemFact label={text.checkpointerRequested} value={formatCount(db.checkpointer.num_requested)} />
            <SystemFact label={text.checkpointerWrite} value={formatMs(db.checkpointer.write_time_ms)} />
            <SystemFact label={text.checkpointerSync} value={formatMs(db.checkpointer.sync_time_ms)} />
            <SystemFact label={text.checkpointerBuffers} value={formatCount(db.checkpointer.buffers_written)} />
          </div>
        </>
      ) : null}

      <div className="system-subhead">
        <h3>{text.relations}</h3>
        <span className="system-note">{formatCount(relations.length)}</span>
      </div>
      {relations.length === 0 ? (
        <p className="system-note">{text.none}</p>
      ) : (
        <div className="trace-table system-table system-table--relations">
          <div className="trace-table-head system-table-head">
            <span>{text.relationName}</span>
            <span>{text.relationTotal}</span>
            <span>{text.relationHeap}</span>
            <span>{text.relationRows}</span>
          </div>
          {relations.map((relation) => (
            <div className="trace-row system-row" key={relation.name}>
              <span className="mono">{relation.name}</span>
              <span className="mono" title={formatCount(relation.total_bytes)}>{formatBytes(relation.total_bytes)}</span>
              <span className="mono" title={formatCount(relation.heap_bytes)}>{formatBytes(relation.heap_bytes)}</span>
              <span className="mono">{formatCount(relation.est_rows)}</span>
            </div>
          ))}
        </div>
      )}

      <div className="system-subhead">
        <h3>{text.unusedIndexes}</h3>
        <span className="system-note">{formatCount(unused.length)}</span>
      </div>
      <p className="system-note">{text.unusedIndexesHint}</p>
      {unused.length === 0 ? (
        <p className="system-note">{text.none}</p>
      ) : (
        <div className="trace-table system-table system-table--indexes">
          <div className="trace-table-head system-table-head">
            <span>{text.indexName}</span>
            <span>{text.indexTable}</span>
            <span>{text.indexScans}</span>
            <span>{text.indexTuplesRead}</span>
            <span>{text.indexSize}</span>
          </div>
          {unused.map((index) => (
            <div className="trace-row system-row system-row-warn" key={`${index.table}.${index.name}`}>
              <span className="mono">{index.name}</span>
              <span className="mono">{index.table}</span>
              <span className="mono">{formatCount(index.idx_scan)}</span>
              <span className="mono">{formatCount(index.idx_tup_read)}</span>
              <span className="mono" title={formatCount(index.size_bytes)}>{formatBytes(index.size_bytes)}</span>
            </div>
          ))}
        </div>
      )}

      <div className="system-subhead">
        <h3>{text.worstIndexes}</h3>
        <span className="system-note">{formatCount(worst.length)}</span>
      </div>
      {worst.length === 0 ? (
        <p className="system-note">{text.none}</p>
      ) : (
        <div className="trace-table system-table system-table--indexes">
          <div className="trace-table-head system-table-head">
            <span>{text.indexName}</span>
            <span>{text.indexTable}</span>
            <span>{text.indexScans}</span>
            <span>{text.indexTuplesRead}</span>
            <span>{text.indexSize}</span>
          </div>
          {worst.map((index) => (
            <div className="trace-row system-row" key={`${index.table}.${index.name}`}>
              <span className="mono">{index.name}</span>
              <span className="mono">{index.table}</span>
              <span className="mono">{formatCount(index.idx_scan)}</span>
              <span className="mono" title={text.indexFetchRatio + ": " + formatPercent(index.fetch_ratio)}>
                {formatCount(index.idx_tup_read)} <span className="system-note">{formatNumber(index.tup_read_per_scan, 1)}/scan</span>
              </span>
              <span className="mono" title={formatCount(index.size_bytes)}>{formatBytes(index.size_bytes)}</span>
            </div>
          ))}
        </div>
      )}

      <div className="system-subhead">
        <h3>{text.hottestTables}</h3>
        <span className="system-note">{formatCount(tables.length)}</span>
      </div>
      {tables.length === 0 ? (
        <p className="system-note">{text.none}</p>
      ) : (
        <div className="trace-table system-table system-table--tables">
          <div className="trace-table-head system-table-head">
            <span>{text.tableName}</span>
            <span>{text.tableLive}</span>
            <span>{text.tableInserts}</span>
            <span>{text.tableUpdates}</span>
            <span>{text.tableDeletes}</span>
            <span>{text.tableSeqScan}</span>
            <span>{text.tableSeqTupRead}</span>
            <span>{text.tableSize}</span>
          </div>
          {tables.map((table) => (
            <div className="trace-row system-row" key={table.name}>
              <span className="mono">{table.name}</span>
              <span className="mono">{formatCount(table.n_live_tup)}</span>
              <span className="mono">{formatCount(table.n_tup_ins)}</span>
              <span className="mono">{formatCount(table.n_tup_upd)}</span>
              <span className="mono">{formatCount(table.n_tup_del)}</span>
              <span className="mono">{formatCount(table.seq_scan)}</span>
              <span className="mono">{formatCount(table.seq_tup_read)}</span>
              <span className="mono" title={formatCount(table.size_bytes)}>{formatBytes(table.size_bytes)}</span>
            </div>
          ))}
        </div>
      )}
    </>
  );
}

function SystemFact({ label, value, accent = "" }) {
  return (
    <div className={`system-fact ${accent}`.trim()}>
      <span>{label}</span>
      <strong className="mono">{value === undefined || value === null || value === "" ? "—" : value}</strong>
    </div>
  );
}

// Postgres reports byte settings with unit "8kB" (shared_buffers) or "B"
// (work_mem); the raw value is only meaningful converted.
function formatSettingBytes(setting) {
  const raw = Number(setting.raw ?? setting.value);
  if (!Number.isFinite(raw)) {
    return "";
  }
  const multiplier = setting.unit === "8kB" ? 8192 : 1;
  return formatBytes(raw * multiplier);
}

function formatBytes(value) {
  const bytes = Number(value);
  if (!Number.isFinite(bytes)) {
    return "—";
  }
  const units = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
  let scaled = Math.abs(bytes);
  let unit = 0;
  while (scaled >= 1024 && unit < units.length - 1) {
    scaled /= 1024;
    unit += 1;
  }
  const rendered = unit === 0 ? scaled.toFixed(0) : scaled.toFixed(scaled >= 100 ? 0 : scaled >= 10 ? 1 : 2);
  return `${bytes < 0 ? "-" : ""}${rendered} ${units[unit]}`;
}

function formatCount(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return number.toLocaleString("en-US");
}

function formatNumber(value, digits = 2) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return number.toFixed(digits);
}

function formatPercent(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) {
    return "—";
  }
  return `${(number * 100).toFixed(2)} %`;
}

function formatMs(value) {
  const ms = Number(value);
  if (!Number.isFinite(ms)) {
    return "—";
  }
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(2)} s`;
  }
  if (ms >= 1) {
    return `${ms.toFixed(ms >= 100 ? 0 : 1)} ms`;
  }
  return `${(ms * 1000).toFixed(0)} µs`;
}

function formatUptime(seconds) {
  const total = Number(seconds);
  if (!Number.isFinite(total) || total < 0) {
    return "—";
  }
  const days = Math.floor(total / 86400);
  const hours = Math.floor((total % 86400) / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  if (days > 0) {
    return `${days}d ${hours}h`;
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  return `${minutes}m ${Math.floor(total % 60)}s`;
}
