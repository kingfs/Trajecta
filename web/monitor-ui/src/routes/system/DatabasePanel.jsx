import React from "react";
import { Card } from "../../components/ui/card";
import { InlineTag } from "../../components/common/Badges";
import { StatCard } from "../../components/common/Display";
import { EmptyState } from "../../components/common/EmptyState";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { formatDateTime } from "../../lib/monitor";
import { SystemFact, formatBytes, formatCount, formatMs, formatNumber, formatPercent, formatSettingBytes } from "./format";

/**
 * PostgreSQL statistics tab: the server and database counters, the key
 * settings this workload is sensitive to, connection/activity facts, the
 * longest running statement, the checkpointer, and the relation and index
 * tables. Everything here comes from /api/system/db; a non-Postgres store
 * answers with a structured unsupported payload, so the tab degrades to a note
 * instead of failing.
 *
 * The shell owns the page header, so this component renders only its own
 * panel body and keeps its own 60s refresh.
 */
export function DatabasePanel() {
  const { t } = useI18n();

  const database = useJSON(apiPaths.systemDatabase, []);
  const db = database.data;

  return (
    <>
      <Card as="section">
        <div className="panel-head">
          <div>
            <h2>{t("system.databasePanel")}</h2>
          </div>
        </div>

        {database.error ? <EmptyState tone="danger" title={t("system.databaseUnavailable")} detail={database.error} /> : null}
        {!database.error && !db ? <p className="system-note">{t("system.loading")}</p> : null}
        {!database.error && db && (db.unsupported || !db.supported) ? (
          <EmptyState title={t("system.unsupportedTitle")} detail={db.reason || `driver: ${db.driver || "unknown"}`} />
        ) : null}
        {!database.error && db && db.supported ? <DatabaseStatistics db={db} t={t} /> : null}
      </Card>
    </>
  );
}

function DatabaseStatistics({ db, t }) {
  const cacheHit = Number(db.database?.cache_hit_ratio || 0);
  const cacheAccent = cacheHit > 0 && cacheHit < 0.95 ? (cacheHit < 0.9 ? "accent-red" : "accent-gold") : cacheHit > 0 ? "accent-green" : "";
  const unused = db.indexes?.unused || [];
  const worst = db.indexes?.worst_tup_read_per_scan || [];
  const relations = db.relations || [];
  const tables = db.tables || [];
  const settings = db.settings || [];
  const activity = db.activity;
  const query = activity?.longest_query;
  const wait = query?.wait_event || query?.wait_event_type || "";
  const unitOf = (setting) => (setting.unit === "B" || setting.unit === "8kB" ? formatSettingBytes(setting) : "");

  return (
    <>
      <div className="hero-grid hero-grid-compact">
        <StatCard label={t("system.databaseSize")} value={formatBytes(db.database?.size_bytes)} />
        <StatCard label={t("system.cacheHit")} value={cacheHit ? formatPercent(cacheHit) : "—"} accent={cacheAccent} detail={`${formatCount(db.database?.blks_hit)} / ${formatCount((db.database?.blks_hit || 0) + (db.database?.blks_read || 0))}`} />
        <StatCard label={t("system.tempBytes")} value={formatBytes(db.database?.temp_bytes)} accent={Number(db.database?.temp_bytes || 0) > 0 ? "accent-gold" : ""} detail={`${formatCount(db.database?.temp_files)} ${t("system.tempFiles")}`} />
        <StatCard label={t("system.deadlocks")} value={formatCount(db.database?.deadlocks)} accent={Number(db.database?.deadlocks || 0) > 0 ? "accent-red" : ""} />
      </div>

      {(db.warnings || []).length > 0 ? (
        <div className="system-warnings">
          <strong>{t("system.warnings")}</strong>
          <ul>
            {db.warnings.map((warning) => (
              <li key={warning} className="mono">{warning}</li>
            ))}
          </ul>
        </div>
      ) : null}

      <div className="system-subhead">
        <h3>{t("system.server")}</h3>
      </div>
      <div className="system-kv">
        <SystemFact label={t("system.serverVersion")} value={db.server?.version || "—"} />
        <SystemFact label={t("system.transactions")} value={`${formatCount(db.database?.xact_commit)} / ${formatCount(db.database?.xact_rollback)}`} />
      </div>

      <div className="system-subhead">
        <h3>{t("system.settings")}</h3>
      </div>
      <div className="system-kv">
        {settings.map((setting) => (
          <SystemFact key={setting.name} label={setting.name} value={unitOf(setting) || setting.value || "—"} />
        ))}
      </div>

      <div className="system-subhead">
        <h3>{t("system.activity")}</h3>
      </div>
      <div className="system-kv">
        <SystemFact label={t("system.sessions")} value={formatCount(activity?.sessions)} />
        <SystemFact label={t("system.totalSessions")} value={formatCount(activity?.total_sessions)} />
        <div className="system-fact">
          <span>{t("system.byState")}</span>
          <div className="system-tags">
            {(activity?.by_state || []).length === 0 ? <span className="system-note">{t("system.none")}</span> : null}
            {(activity?.by_state || []).map((entry) => (
              <InlineTag key={entry.label} tone={entry.label === "active" ? "accent-green" : "default"}>{`${entry.label} × ${formatCount(entry.count)}`}</InlineTag>
            ))}
          </div>
        </div>
        <div className="system-fact">
          <span>{t("system.byWaitEvent")}</span>
          <div className="system-tags">
            {(activity?.by_wait_event_type || []).length === 0 ? <span className="system-note">{t("system.none")}</span> : null}
            {(activity?.by_wait_event_type || []).map((entry) => (
              <InlineTag key={entry.label} tone={entry.label === "none" ? "default" : "accent-gold"}>{`${entry.label} × ${formatCount(entry.count)}`}</InlineTag>
            ))}
          </div>
        </div>
      </div>

      <div className="system-subhead">
        <h3>{t("system.longestQuery")}</h3>
      </div>
      {query ? (
        <div className="system-longest">
          <span className="system-note">
            {t("system.longestQueryMeta", {
              pid: query.pid,
              state: query.state || "unknown",
              duration: formatMs(query.duration_ms),
              wait: wait ? t("system.waitSuffix", { wait }) : "",
            })}
          </span>
          <pre className="code-block system-statement">{query.statement}</pre>
        </div>
      ) : (
        <p className="system-note">{t("system.longestQueryNone")}</p>
      )}

      {db.checkpointer ? (
        <>
          <div className="system-subhead">
            <h3>{t("system.checkpointer")}</h3>
            <span className="system-note">{db.checkpointer.source}</span>
          </div>
          <div className="system-kv">
            <SystemFact label={t("system.checkpointerTimed")} value={formatCount(db.checkpointer.num_timed)} />
            <SystemFact label={t("system.checkpointerRequested")} value={formatCount(db.checkpointer.num_requested)} />
            <SystemFact label={t("system.checkpointerWrite")} value={formatMs(db.checkpointer.write_time_ms)} />
            <SystemFact label={t("system.checkpointerSync")} value={formatMs(db.checkpointer.sync_time_ms)} />
            <SystemFact label={t("system.checkpointerBuffers")} value={formatCount(db.checkpointer.buffers_written)} />
          </div>
        </>
      ) : null}

      <div className="system-subhead">
        <h3>{t("system.relations")}</h3>
        <span className="system-note">{formatCount(relations.length)}</span>
      </div>
      {relations.length === 0 ? (
        <p className="system-note">{t("system.none")}</p>
      ) : (
        <div className="trace-table system-table system-table--relations">
          <div className="trace-table-head system-table-head">
            <span>{t("system.relationName")}</span>
            <span>{t("system.relationTotal")}</span>
            <span>{t("system.relationHeap")}</span>
            <span>{t("system.relationRows")}</span>
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
        <h3>{t("system.unusedIndexes")}</h3>
        <span className="system-note">{formatCount(unused.length)}</span>
      </div>
      <p className="system-note">{t("system.unusedIndexesHint")}</p>
      {unused.length === 0 ? (
        <p className="system-note">{t("system.none")}</p>
      ) : (
        <div className="trace-table system-table system-table--indexes">
          <div className="trace-table-head system-table-head">
            <span>{t("system.indexName")}</span>
            <span>{t("system.indexTable")}</span>
            <span>{t("system.indexScans")}</span>
            <span>{t("system.indexTuplesRead")}</span>
            <span>{t("system.indexSize")}</span>
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
        <h3>{t("system.worstIndexes")}</h3>
        <span className="system-note">{formatCount(worst.length)}</span>
      </div>
      {worst.length === 0 ? (
        <p className="system-note">{t("system.none")}</p>
      ) : (
        <div className="trace-table system-table system-table--indexes">
          <div className="trace-table-head system-table-head">
            <span>{t("system.indexName")}</span>
            <span>{t("system.indexTable")}</span>
            <span>{t("system.indexScans")}</span>
            <span>{t("system.indexTuplesRead")}</span>
            <span>{t("system.indexSize")}</span>
          </div>
          {worst.map((index) => (
            <div className="trace-row system-row" key={`${index.table}.${index.name}`}>
              <span className="mono">{index.name}</span>
              <span className="mono">{index.table}</span>
              <span className="mono">{formatCount(index.idx_scan)}</span>
              <span className="mono" title={t("system.indexFetchRatio") + ": " + formatPercent(index.fetch_ratio)}>
                {formatCount(index.idx_tup_read)} <span className="system-note">{formatNumber(index.tup_read_per_scan, 1)}/scan</span>
              </span>
              <span className="mono" title={formatCount(index.size_bytes)}>{formatBytes(index.size_bytes)}</span>
            </div>
          ))}
        </div>
      )}

      <div className="system-subhead">
        <h3>{t("system.hottestTables")}</h3>
        <span className="system-note">{formatCount(tables.length)}</span>
      </div>
      {tables.length === 0 ? (
        <p className="system-note">{t("system.none")}</p>
      ) : (
        <div className="trace-table system-table system-table--tables">
          <div className="trace-table-head system-table-head">
            <span>{t("system.tableName")}</span>
            <span>{t("system.tableLive")}</span>
            <span>{t("system.tableInserts")}</span>
            <span>{t("system.tableUpdates")}</span>
            <span>{t("system.tableDeletes")}</span>
            <span>{t("system.tableSeqScan")}</span>
            <span>{t("system.tableSeqTupRead")}</span>
            <span>{t("system.tableSize")}</span>
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
