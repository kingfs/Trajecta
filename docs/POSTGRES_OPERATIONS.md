# PostgreSQL 运维手册

本文档面向 Trajecta application database 的 PostgreSQL 生产/准生产运维：只读基线采集、pg_stat_statements 基线、并发索引变更、查询调优、派生 summary 维护、回填与灰度读、分区归档和锁排查。

storage model、driver 选择和部署拓扑见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)；稳定 CLI 入口见 [开发指南](./DEVELOPMENT.md)；Monitor 读路径和页面语义见 [Monitor 指南](./MONITOR_GUIDE.md)；audit/observation 表语义见 [观测与审计](./OBSERVATION_AND_AUDIT.md)。

硬性边界：

- raw `.http` cassette 始终是 replay 与详情视图的事实源；数据库只是列表、过滤和聚合的派生索引。
- 本文优化目标是 PostgreSQL application DB；SQLite 仅为 local/dev/test fallback，不是优化对象。
- 不支持用 `db migrate down` 作为生产回滚路径。生产回滚依赖备份恢复、流量回退或审阅过的手工计划。

## 适用范围与前置权限

适用对象：

- PostgreSQL production application DB，检查点 SQL 迁移位于 `ent/postgres-migrations/`。
- Monitor / MCP / CLI 依赖的结构化查询：`logs` trace index、session summary、Responses audit、analysis jobs、system events、routing/channel/model 数据。
- 只读诊断、索引与查询优化、派生 summary、回填、灰度读、分区/归档。

建议把只读诊断连接与受控 DDL 连接分离，并显式设置 DSN：

```bash
export TRAJECTA_DATABASE_DSN='postgres://...'
psql "$TRAJECTA_DATABASE_DSN" -v ON_ERROR_STOP=1
```

改动前先确认迁移状态（Postgres 下两个命令都读取共享的 application `schema_migrations` namespace）：

```bash
server -c config/config.yaml db migrate status --check-db
server -c config/config.yaml auth migrate status --check-db
```

两者应报告 schema 健康且 non-dirty。若处于 dirty 状态，先处理迁移一致性，不进入优化流程。

## 基线采集（表大小、dead tuples、索引使用、invalid index、数据分布）

仓库自带基线采集脚本，可把全部输出写入一个文件：

```bash
TRAJECTA_DATABASE_DSN='postgres://user:pass@host/db?sslmode=require' \
  BASELINE_WINDOW='7 days' \
  scripts/postgres-baseline.sh /tmp/trajecta-postgres-baseline.txt
```

脚本读取 `TRAJECTA_DATABASE_DSN`，回退 `DATABASE_URL` 或 libpq `PG*` 变量；`BASELINE_WINDOW` 默认 `7 days`。除下面明确标注的 reset 外，所有语句都只读。

本节按脚本的覆盖范围贴出基线语句：表大小与 vacuum 各覆盖 12 张表，索引使用覆盖到 `trace_findings` 为止的 9 张表。为便于手工执行，脚本里由 psql 变量注入的占位符在这里展开为默认值：`BASELINE_WINDOW` 写作 `interval '7 days'`，keyset EXPLAIN 的游标写作 `TIMESTAMPTZ 'REPLACE_WITH_CURSOR_AT'` 与 `'REPLACE_WITH_LAST_ID'`（脚本对应默认值为 `1970-01-01T00:00:00Z` 与空字符串）。脚本不包含的其它表见本节末尾的“扩展手查（脚本不覆盖的表）”，那部分需要手工执行。

环境与扩展状态：

```sql
SELECT version();

SELECT current_database() AS database, current_schema() AS schema, current_user AS user;

SELECT extname, extversion
FROM pg_extension
WHERE extname IN ('pg_stat_statements', 'pgstattuple')
ORDER BY extname;

SELECT name, setting, unit, source
FROM pg_settings
WHERE name IN (
  'shared_preload_libraries',
  'track_io_timing',
  'pg_stat_statements.track',
  'pg_stat_statements.max',
  'autovacuum',
  'autovacuum_vacuum_scale_factor',
  'autovacuum_analyze_scale_factor'
)
ORDER BY name;
```

表大小（脚本覆盖 `logs`、`session_summaries`、`overview_metric_buckets`、`overview_metric_bucket_members`、`trace_observations`、`parse_jobs`、`system_events`、`analysis_runs`、`trace_findings`、`request_audits`、`execution_events`、`upstream_exchanges` 共 12 张表）：

```sql
SELECT
  n.nspname AS schema_name,
  c.relname AS table_name,
  c.reltuples::bigint AS estimated_rows,
  pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
  pg_size_pretty(pg_relation_size(c.oid)) AS table_size,
  pg_size_pretty(pg_indexes_size(c.oid)) AS indexes_size
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname = current_schema()
  AND c.relname IN (
    'logs',
    'session_summaries',
    'overview_metric_buckets',
    'overview_metric_bucket_members',
    'trace_observations',
    'parse_jobs',
    'system_events',
    'analysis_runs',
    'trace_findings',
    'request_audits',
    'execution_events',
    'upstream_exchanges'
  )
ORDER BY pg_total_relation_size(c.oid) DESC;
```

vacuum 与 dead tuples（与表大小相同的 12 张表）：

```sql
SELECT
  relname,
  n_live_tup,
  n_dead_tup,
  ROUND(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) AS dead_pct,
  last_vacuum,
  last_autovacuum,
  last_analyze,
  last_autoanalyze,
  vacuum_count,
  autovacuum_count,
  analyze_count,
  autoanalyze_count
FROM pg_stat_user_tables
WHERE relname IN (
  'logs',
  'session_summaries',
  'overview_metric_buckets',
  'overview_metric_bucket_members',
  'trace_observations',
  'parse_jobs',
  'system_events',
  'analysis_runs',
  'trace_findings',
  'request_audits',
  'execution_events',
  'upstream_exchanges'
)
ORDER BY n_dead_tup DESC;
```

索引使用（脚本覆盖 `logs`、`session_summaries`、`overview_metric_buckets`、`overview_metric_bucket_members`、`trace_observations`、`parse_jobs`、`system_events`、`analysis_runs`、`trace_findings` 共 9 张表；`idx_scan` 长期为 0 且体积大的索引是候选清理对象）：

```sql
SELECT
  s.relname AS table_name,
  s.indexrelname AS index_name,
  s.idx_scan,
  s.idx_tup_read,
  s.idx_tup_fetch,
  pg_size_pretty(pg_relation_size(i.indexrelid)) AS index_size,
  pg_get_indexdef(i.indexrelid) AS index_def
FROM pg_stat_user_indexes s
JOIN pg_index i ON i.indexrelid = s.indexrelid
WHERE s.relname IN (
  'logs',
  'session_summaries',
  'overview_metric_buckets',
  'overview_metric_bucket_members',
  'trace_observations',
  'parse_jobs',
  'system_events',
  'analysis_runs',
  'trace_findings'
)
ORDER BY pg_relation_size(i.indexrelid) DESC, s.idx_scan ASC;
```

invalid index：

```sql
SELECT
  n.nspname AS schema_name,
  c.relname AS index_name,
  t.relname AS table_name,
  i.indisvalid,
  i.indisready,
  pg_size_pretty(pg_relation_size(c.oid)) AS index_size,
  pg_get_indexdef(c.oid) AS index_def
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
JOIN pg_class t ON t.oid = i.indrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema()
  AND (NOT i.indisvalid OR NOT i.indisready)
ORDER BY pg_relation_size(c.oid) DESC;
```

`logs` 的数据分布（client-visible 过滤条件与运行时代码一致，为 `COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')`）：

```sql
SELECT
  COUNT(*) AS total_logs,
  COUNT(*) FILTER (WHERE COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')) AS client_visible_logs,
  COUNT(DISTINCT session_id) FILTER (WHERE session_id <> '') AS sessions,
  MIN(recorded_at) AS first_recorded_at,
  MAX(recorded_at) AS last_recorded_at
FROM logs;
```

按小时分布：

```sql
SELECT
  date_trunc('hour', recorded_at) AS hour,
  COUNT(*) AS request_count,
  COUNT(*) FILTER (WHERE status_code BETWEEN 200 AND 299 AND error_text = '') AS success_count,
  COUNT(*) FILTER (WHERE status_code < 200 OR status_code >= 300 OR error_text <> '') AS failure_count,
  SUM(total_tokens) AS total_tokens,
  ROUND(AVG(NULLIF(ttft_ms, 0))::numeric, 2) AS avg_ttft_ms,
  ROUND(AVG(NULLIF(duration_ms, 0))::numeric, 2) AS avg_duration_ms
FROM logs
WHERE recorded_at >= now() - interval '7 days'
  AND COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')
GROUP BY 1
ORDER BY 1 DESC
LIMIT 168;
```

summary 覆盖率：

```sql
SELECT
  (SELECT COUNT(DISTINCT session_id) FROM logs WHERE session_id <> '' AND COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')) AS log_sessions,
  (SELECT COUNT(*) FROM session_summaries) AS summary_sessions,
  (SELECT MAX(updated_at) FROM session_summaries) AS summary_max_updated_at,
  (SELECT MIN(updated_at) FROM session_summaries) AS summary_min_updated_at;
```

```sql
SELECT
  COUNT(*) AS bucket_count,
  MIN(bucket_start) AS first_bucket,
  MAX(bucket_start) AS last_bucket,
  SUM(request_count) AS bucket_requests,
  SUM(success_request) AS bucket_success,
  SUM(failed_request) AS bucket_failed,
  SUM(total_tokens) AS bucket_tokens
FROM overview_metric_buckets;
```

```sql
SELECT
  COUNT(*) AS member_count,
  MIN(updated_at) AS first_member_update,
  MAX(updated_at) AS last_member_update
FROM overview_metric_bucket_members;
```

backlog 与 system events：

```sql
SELECT
  'trace_observations' AS source,
  status,
  COUNT(*) AS count
FROM trace_observations
GROUP BY status
UNION ALL
SELECT
  'parse_jobs' AS source,
  status,
  COUNT(*) AS count
FROM parse_jobs
GROUP BY status
ORDER BY source, count DESC;
```

```sql
SELECT
  status,
  severity,
  source,
  category,
  COUNT(*) AS count,
  MAX(last_seen_at) AS newest
FROM system_events
GROUP BY status, severity, source, category
ORDER BY count DESC, newest DESC
LIMIT 50;
```

### 扩展手查（脚本不覆盖的表）

`scripts/postgres-baseline.sh` 固定覆盖上面的表集合，不包含 routing/model 配置表、`tool_call_audits`、`analysis_jobs`，也不把 `request_audits`、`execution_events`、`upstream_exchanges` 纳入索引使用查询。需要这些数据时手工执行以下语句：

表大小（脚本外）：

```sql
SELECT
  n.nspname AS schema_name,
  c.relname AS table_name,
  c.reltuples::bigint AS estimated_rows,
  pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
  pg_size_pretty(pg_relation_size(c.oid)) AS table_size,
  pg_size_pretty(pg_indexes_size(c.oid)) AS indexes_size
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind = 'r'
  AND n.nspname = current_schema()
  AND c.relname IN (
    'tool_call_audits',
    'analysis_jobs',
    'app_settings',
    'channel_configs',
    'channel_models',
    'model_catalog',
    'model_aliases'
  )
ORDER BY pg_total_relation_size(c.oid) DESC;
```

vacuum 与 dead tuples（脚本外，表集合同上）：

```sql
SELECT
  relname,
  n_live_tup,
  n_dead_tup,
  ROUND(100.0 * n_dead_tup / NULLIF(n_live_tup + n_dead_tup, 0), 2) AS dead_pct,
  last_vacuum,
  last_autovacuum,
  last_analyze,
  last_autoanalyze,
  vacuum_count,
  autovacuum_count,
  analyze_count,
  autoanalyze_count
FROM pg_stat_user_tables
WHERE relname IN (
  'tool_call_audits',
  'analysis_jobs',
  'app_settings',
  'channel_configs',
  'channel_models',
  'model_catalog',
  'model_aliases'
)
ORDER BY n_dead_tup DESC;
```

索引使用（脚本外）：

```sql
SELECT
  s.relname AS table_name,
  s.indexrelname AS index_name,
  s.idx_scan,
  s.idx_tup_read,
  s.idx_tup_fetch,
  pg_size_pretty(pg_relation_size(i.indexrelid)) AS index_size,
  pg_get_indexdef(i.indexrelid) AS index_def
FROM pg_stat_user_indexes s
JOIN pg_index i ON i.indexrelid = s.indexrelid
WHERE s.relname IN (
  'request_audits',
  'execution_events',
  'upstream_exchanges',
  'tool_call_audits',
  'analysis_jobs',
  'app_settings',
  'channel_configs',
  'channel_models',
  'model_catalog',
  'model_aliases'
)
ORDER BY pg_relation_size(i.indexrelid) DESC, s.idx_scan ASC;
```

## pg_stat_statements 基线与热点查询

启用要求：实例需加载扩展；托管数据库若要求在参数组配置 `shared_preload_libraries = 'pg_stat_statements'`，需按平台流程滚动重启，不要在没有变更窗口时临时重启生产库。

```sql
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
```

建议参数：

```text
pg_stat_statements.track = all
pg_stat_statements.max = 10000
track_io_timing = on
```

基线至少覆盖一个完整业务峰谷周期：低流量环境建议 24 小时，高流量环境至少 2 小时峰值窗口。采样开始记录时间戳并重置（reset 有副作用，仅在明确开始窗口时执行）：

```sql
SELECT now() AS baseline_started_at;
SELECT pg_stat_statements_reset();
```

总耗时最高的 SQL：

```sql
SELECT
  queryid,
  calls,
  ROUND(total_exec_time::numeric, 2) AS total_exec_ms,
  ROUND(mean_exec_time::numeric, 2) AS mean_exec_ms,
  ROUND(max_exec_time::numeric, 2) AS max_exec_ms,
  rows,
  shared_blks_hit,
  shared_blks_read,
  shared_blks_dirtied,
  temp_blks_read,
  temp_blks_written,
  LEFT(query, 500) AS query_sample
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY total_exec_time DESC
LIMIT 30;
```

平均耗时最高的 SQL（过滤低频噪声）：

```sql
SELECT
  queryid,
  calls,
  ROUND(mean_exec_time::numeric, 2) AS mean_exec_ms,
  ROUND(max_exec_time::numeric, 2) AS max_exec_ms,
  ROUND(total_exec_time::numeric, 2) AS total_exec_ms,
  rows,
  LEFT(query, 500) AS query_sample
FROM pg_stat_statements
WHERE calls >= 10
  AND dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY mean_exec_time DESC
LIMIT 30;
```

读指标：

- `total_exec_time` 高：整体成本高，常见于列表、overview、sessions 聚合。
- `mean_exec_time` / `max_exec_time` 高：用户可感知慢查询或偶发执行计划问题。
- `shared_blks_read` 高：缓存命中不足或索引不匹配。
- `temp_blks_written` 高：排序/聚合/hash 溢出，检查 `work_mem`、索引顺序或 summary 设计。
- `rows/calls` 远高于页面大小：过滤条件或分页策略有问题。

Trajecta 热表优先级：

- `logs`：trace list、session list、overview、model/provider/time filters。
- `request_audits`、`execution_events`、`upstream_exchanges`、`tool_call_audits`：Responses audit 与 exchange correlation。
- `analysis_jobs`、`analysis_runs`、`trace_observations`、`trace_findings`、`system_events`：重分析、findings、Monitor summary。
- `channel_configs`、`channel_models`、`model_catalog`、`model_aliases`：routing/model 管理读路径。

## 大表聚合的内存与并行度

`/dev/shm` 决定并行查询能申请多少共享内存。容器默认只有 64 MB，`work_mem` 给得高、并行 worker 又多时，hash 聚合会直接失败：

```text
pq: could not resize shared memory segment "/PostgreSQL.4213527324" to 67244032 bytes: No space left on device (53100)
```

派生表修复（`trajecta upgrade db --reconcile-derived-trace-ids`）在一次真实运行中就是这样失败的：`work_mem=256MB` 配 6 个 worker 需要 67 MB 的段，超过 64 MB。同一份数据（`semantic_nodes` 7400 万行、总大小 120 GB、堆 55 GB，`trace_id` 上有 `(trace_id, depth, node_index)` 索引）的实测：

| 会话设置 | 结果 |
| --- | --- |
| 默认 `work_mem=4MB`、`max_parallel_workers_per_gather=2` | `DISTINCT trace_id` 反连接超过 45 分钟未结束，`pg_stat_database.temp_bytes` 持续增长（排序溢出到磁盘） |
| `work_mem=32MB`、`max_parallel_workers_per_gather=1` | 5 分钟窗口内新增临时文件 0 MB，扫描约 25 分钟完成 |
| `work_mem=256MB`、`max_parallel_workers_per_gather=6` | 不再溢写，但并行段超过 64 MB 的 `/dev/shm` 而报错 |

可用两种方式解决：只对重活调「中等 `work_mem` + 少量并行」，或给 `postgres` 服务显式设置 `shm_size`（官方镜像建议不小于 1 GB）后再提高并行度。前者不需要改全局配置，例如在 DSN 上追加：

```text
options=-c%20work_mem%3D32MB%20-c%20max_parallel_workers_per_gather%3D1
```

判断是否仍在溢写要看增量而不是累计值：`pg_stat_database.temp_bytes` 是自统计重置以来的累计量，前后两次采样相减才说明当前语句有没有落盘。

这个上限不只影响大表。在默认 64 MB 下，只要多个会话同时使用并行 hash 节点，`logs` 这种 26 万行量级的普通 `count(*)` 也会报同一个 `No space left on device`——修复期间实测被拒两次，其中一次只申请 8 MB。所以给 `postgres` 服务设置 `shm_size: 1g` 并重建容器，是这台机器上比任何会话级 `work_mem` 调整都更根本的修法；会话级设置只能保证「本次会话不占用共享内存」，挡不住同一实例上并发的其它会话。

## 派生表修复里最容易踩的一条：`IS NOT DISTINCT FROM` 不走索引

`IS NOT DISTINCT FROM` **不能作为 btree 索引条件**。用它比较派生表的身份列时，规划器只会保留 `trace_id` 进 `Index Cond`，其余列退化成 join filter，于是每个候选行都要把对端 trace 的**全部**节点取回来再逐行丢弃，代价是 O(候选行 × 对端 trace 行数)。

实测：`semantic_nodes` 有 7,400 万行、29,253 个不同 trace id，平均每个 trace id 2,530 行。一批 200 个 id 的重复计数因此要跑 **46 分钟以上**，而同一表达式还用在 `DELETE` 上，只会更慢。身份列都是 `NOT NULL`，改用 `=` 语义完全等价，计数变成两列 index-only 探测、删除变成 `Index Scan`，同一对 id 从 462 ms 降到 0.255 ms：

```
-- 改前
Join Filter: (NOT ((c.node_id)::text IS DISTINCT FROM (d.node_id)::text))
  Rows Removed by Join Filter: 171
  Index Cond: (trace_id = m.current)                       -- node_id 没进索引条件
-- 改后
  Index Cond: ((trace_id = m.current) AND (node_id = (d.node_id)::text))
```

判断方法就是看 `EXPLAIN`：`Index Cond` 里必须出现你要比较的每一列；只有 `trace_id` 出现、另一列出现在 `Filter`/`Join Filter` 里，就是这个问题。代码侧由 `internal/legacymigrate` 从 `pg_attribute.attnotnull` 取可空性，只有真正可空的身份列才退回 NULL 安全写法。

## 并发索引变更

加索引前必须同时满足：pg_stat_statements 有明确慢 SQL 或高成本 SQL；`EXPLAIN (ANALYZE, BUFFERS)` 证明现有索引未覆盖过滤、排序或 join；候选索引匹配稳定产品查询而非一次性排障；已评估写入放大、索引体积和 vacuum 成本。

内置的安全入口是 `db migrate optimize-indexes`（`--dry-run` 只预览不执行），命令归属与通用行为见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)。该命令要求非空 Postgres DSN，逐条执行非事务的 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`；SQLite 不适用，此时返回 `ErrSQLiteUsesStoreInit`（`internal/appdbmigrate/migrate.go` 的 `OptimizeIndexes`）。当前它只覆盖 `logs` 热点查询，创建以下 5 个索引：

```text
tracelog_recent_client_visible_idx              最新 logs 与分页 trace list（recorded_at DESC, trace_id DESC）
tracelog_session_recent_client_visible_idx      session 详情页与每 session 最新 trace
tracelog_failure_recent_client_visible_idx      最近失败 trace list 与 overview attention failure
tracelog_routing_failure_recent_client_visible_idx  routing failure 分析与列表
tracelog_duration_slow_client_visible_idx       duration DESC 慢请求列表
```

相应的谓词形态：

```sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS tracelog_recent_client_visible_idx
ON logs (recorded_at DESC, trace_id DESC)
WHERE COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy');

CREATE INDEX CONCURRENTLY IF NOT EXISTS tracelog_session_recent_client_visible_idx
ON logs (session_id, recorded_at DESC, trace_id DESC)
WHERE session_id <> '' AND COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy');
```

设计规则：

- 时间列表通常需要 `(filter_columns..., recorded_at DESC)` 或 `(filter_columns..., created_at DESC)`。
- 只查询非空 session/response/request id 时优先用 partial index；不为低选择性布尔字段单独建索引。
- JSON/JSONB 只有在产品查询稳定后才加表达式或 GIN 索引。
- 新索引名与现有风格一致：`tracelog_*`、`requestaudit_*`、`executionevent_*`、`upstreamexchange_*`、`toolcallaudit_*`。

注意：

- `CREATE INDEX CONCURRENTLY` 不能在事务中执行。
- 失败会留下 invalid index；确认没有查询依赖后再清理，例如 `DROP INDEX CONCURRENTLY IF EXISTS ...`。
- 创建后执行 `ANALYZE logs;`。

上线后确认索引被使用：

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT trace_id, recorded_at, model, provider, status_code
FROM logs
WHERE provider = 'openai' AND recorded_at >= now() - interval '24 hours'
ORDER BY recorded_at DESC
LIMIT 50;
```

导出现有索引清单：

```sql
SELECT
  schemaname,
  tablename,
  indexname
FROM pg_indexes
WHERE schemaname = 'public'
ORDER BY tablename, indexname;
```

## 查询调优与 EXPLAIN 模板

分析步骤：

1. 从 pg_stat_statements 取 queryid、归一化 SQL、calls、mean/max latency。
2. 用真实参数重放 `EXPLAIN (ANALYZE, BUFFERS, VERBOSE)`。
3. 判断慢点是 filter、sort、join、aggregate、offset pagination、JSON 解析还是数据倾斜。
4. 先优化查询形态，再决定是否加索引或派生 summary。

Trajecta 常见优化方向：

- Trace list：避免深 `OFFSET`，优先 `(recorded_at, trace_id/path)` seek pagination。
- Session list：避免每次从 `logs` 全量 group by；session 数量大时改用 `session_summaries`。
- Overview：多指标聚合若反复扫描 `logs`、`trace_findings`、`system_events`，改为按时间窗口派生 bucket。
- Audit trace：按 `response_id`、`request_audit_id`、`conversation_id`、`trace_id` 走窄索引，避免在 audit 表上做模糊搜索。
- Analysis jobs：worker 取任务稳定使用 `(status, updated_at)` 或等价索引，并限制批量大小。
- JSONB：除非界面稳定需要，不要把 raw payload JSON 查询放到热路径。

Trace list 最新页：

```sql
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
SELECT
  trace_id, path, recorded_at, model, provider, operation, endpoint, status_code,
  duration_ms, ttft_ms, total_tokens, session_id
FROM logs
WHERE COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')
ORDER BY recorded_at DESC, trace_id DESC
LIMIT 50;
```

Session 列表第一页的 session id 阶段：

```sql
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
SELECT s.session_id
FROM logs s
WHERE s.session_id <> ''
  AND COALESCE(s.exchange_kind, '') IN ('', 'entry', 'proxy')
GROUP BY s.session_id
ORDER BY MAX(s.recorded_at) DESC
LIMIT 50 OFFSET 0;
```

Session 当前页聚合阶段（与脚本一致：用 `page_sessions` CTE 复用上一条的 session id 阶段；手工排障时可把 CTE 换成 `VALUES` 列表）：

```sql
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
WITH page_sessions AS (
  SELECT s.session_id
  FROM logs s
  WHERE s.session_id <> ''
    AND COALESCE(s.exchange_kind, '') IN ('', 'entry', 'proxy')
  GROUP BY s.session_id
  ORDER BY MAX(s.recorded_at) DESC
  LIMIT 50 OFFSET 0
)
SELECT
  s.session_id,
  COUNT(*) AS request_count,
  MIN(s.recorded_at) AS first_seen,
  MAX(s.recorded_at) AS last_seen,
  SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) AS success_request,
  SUM(CASE WHEN s.status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END) AS failed_request,
  SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.total_tokens ELSE 0 END) AS total_tokens,
  AVG(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.ttft_ms END) AS avg_ttft
FROM logs s
JOIN page_sessions p ON p.session_id = s.session_id
WHERE s.session_id <> ''
  AND COALESCE(s.exchange_kind, '') IN ('', 'entry', 'proxy')
GROUP BY s.session_id;
```

Overview summary：

```sql
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
SELECT
  COUNT(*) AS request_count,
  SUM(CASE WHEN status_code >= 200 AND status_code < 300 AND error_text = '' THEN 1 ELSE 0 END) AS success_request,
  SUM(total_tokens) AS total_tokens,
  AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms END) AS avg_ttft,
  AVG(CASE WHEN duration_ms > 0 THEN duration_ms END) AS avg_duration,
  COUNT(DISTINCT CASE WHEN session_id <> '' THEN session_id END) AS session_count
FROM logs
WHERE recorded_at >= now() - interval '7 days'
  AND COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy');
```

未解析 observation 计数：

```sql
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
SELECT COUNT(*)
FROM logs l
WHERE NOT EXISTS (
  SELECT 1
  FROM trace_observations o
  WHERE o.trace_id = l.trace_id
);
```

System events 最新列表与 keyset 翻页：

```sql
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
SELECT
  id, fingerprint, source, category, severity, status, title, last_seen_at
FROM system_events
WHERE status = 'unread'
ORDER BY last_seen_at DESC, id DESC
LIMIT 50 OFFSET 0;
```

```sql
EXPLAIN (ANALYZE, BUFFERS, VERBOSE)
SELECT
  id, fingerprint, source, category, severity, status, title, last_seen_at
FROM system_events
WHERE status = 'unread'
  AND (last_seen_at < TIMESTAMPTZ 'REPLACE_WITH_CURSOR_AT'
    OR (last_seen_at = TIMESTAMPTZ 'REPLACE_WITH_CURSOR_AT' AND id < 'REPLACE_WITH_LAST_ID'))
ORDER BY last_seen_at DESC, id DESC
LIMIT 51;
```

## Session Summary 维护

`session_summaries` 是已上线的派生 read model（不是待建表），由 `logs` 重建，可删除重建，也可按 session 局部回填。真实列（以 `ent/postgres-migrations/20260703090000_add_session_summaries.up.sql` 为准）：

```text
session_summaries
- session_id          primary key
- session_source
- request_count
- first_seen
- last_seen
- last_model
- providers
- success_request
- failed_request
- success_rate
- total_tokens
- avg_ttft
- total_duration
- stream_count
- updated_at
```

索引为 `session_summaries_last_seen (last_seen DESC, session_id DESC)` 与 `session_summaries_last_model (last_model)`。

重建入口是 `db summary rebuild sessions`（`--session-id` 局部回填、`--dry-run` 只读统计不写库、不带 `--session-id` 时全量删除并重建），命令语义见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)。`overview_metric_buckets` / `overview_metric_bucket_members` 由写入路径按 path 增量维护；`Store.RebuildOverviewMetricBuckets`（`internal/store/store.go`）可全量删除并按 `logs` 重建，但当前没有 CLI 调用者，所以没有等价的命令行重建入口。

语义要点（用于一致性对比）：

- `request_count` 为该 session 下 client-visible 的 `logs` 行数；`first_seen`/`last_seen` 取 `MIN/MAX(recorded_at)`。
- `success_request` 统计 `status_code BETWEEN 200 AND 299`；`failed_request` 统计其余。
- `total_tokens` 与 `avg_ttft` 只累计/平均成功请求；`total_duration` 汇总全部请求；`stream_count` 统计 `is_stream`。
- `success_rate` 为成功请求占比乘以 100；`updated_at` 为重建时刻。
- `logs` 仍是 trace index 事实源；summary 删除后可重建。

回填前先 dry-run 统计候选：

```sql
SELECT
  count(DISTINCT session_id) AS sessions,
  count(*) AS traces
FROM logs
WHERE session_id <> ''
  AND COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy');
```

抽样一致性（对比 `logs` 聚合与 summary 行）：

```sql
SELECT session_id
FROM logs
WHERE session_id <> ''
  AND COALESCE(exchange_kind, '') IN ('', 'entry', 'proxy')
GROUP BY session_id
ORDER BY max(recorded_at) DESC
LIMIT 20;
```

对每个抽样 session 比较 `request_count`、`first_seen`、`last_seen`、`total_tokens`、`last_model`、`failed_request`；一致后再让 session list 读 summary。实现侧开关为 `database.use_session_summary_read: true` 或环境变量 `TRAJECTA_DATABASE_USE_SESSION_SUMMARY_READ=true`，默认关闭。

## Backfill 与灰度读

现有回填入口只有 `analyze backfill-exchanges`（`--dry-run` 只报告 scanned、冲突和分类计数，不更新 DB），命令语义与完整命令清单见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)。它只补齐 `upstream_exchanges` 的 exchange metadata 索引：实际写入的列是 `response_id`、`request_audit_id`、`trace_id`、`exchange_id`、`exchange_kind`、`exchange_role`、`parent_exchange_id`、`sequence_index`；`cassette_path`、`model`、`endpoint` 只是定位并读取对应 raw `.http` cassette 的输入，不会被回写。回填绝不重写 cassette。

运行时建议限制锁等待与语句时间：

```sql
SET lock_timeout = '2s';
SET statement_timeout = '30s';
```

按稳定键或时间窗口小步推进：

```sql
SELECT path, trace_id
FROM logs
WHERE recorded_at >= $1 AND recorded_at < $2
ORDER BY recorded_at, path
LIMIT 1000;
```

回填回滚优先字段级或 summary 级：新增字段按记录的范围置回默认值；派生 summary 可 truncate 后从事实源重建；新增索引用 `DROP INDEX CONCURRENTLY`。禁止把 `db migrate down` 当作普通 backfill 回滚。

灰度读用于把新 summary、新索引依赖查询或新查询形态逐步接入用户流量：

1. Shadow read：主路径仍读旧查询，新路径后台执行并记录差异。
2. Operator canary：只给内部 operator 或单实例启用新读路径。
3. Low percentage：小比例流量读新路径，保留旧路径 fallback。
4. Full read：默认读新路径，保留快速回退开关至少一个发布周期。

对比指标：行数、排序（尤其 `recorded_at DESC`、`created_at DESC` 及 tie-breaker）、聚合值是否一致；p95/p99 是否改善；Postgres CPU、IO、temp files、lock waits 是否稳定；应用错误率与 Monitor API 5xx 是否无回归。

回退必须是配置或发布级开关，不依赖 DDL rollback。索引和 summary 表可先保留，确认不再使用后再清理。

## 分区与归档

当前事实：`ent/postgres-migrations/*.up.sql` 里没有任何 `PARTITION` 语句，`logs`、`trace_observations`、`request_audits`、`execution_events`、`upstream_exchanges`、`tool_call_audits`、`system_events` 等都是普通表；`internal/appdbmigrate` 只实现迁移（up/status，`db migrate down` 明确不支持）与并发索引优化，不包含分区或归档逻辑。

代码里也没有按时间删除、搬迁或导出历史行的 retention/archive job。`internal/store` 中可验证的删除路径全部是派生数据重建，不删除 raw `.http` cassette：

- `RebuildSessionSummaries` / `RebuildSessionSummary`：删除并按 `logs` 重建 `session_summaries`。
- `RebuildOverviewMetricBuckets`：删除并按 `logs` 重建 `overview_metric_buckets` / `overview_metric_bucket_members`（当前无 CLI 调用者）。
- `SaveObservation`：按 `trace_id` 删除并重写 `semantic_nodes`；`SaveFindings`：按 `trace_id` 删除并重写 `trace_findings`。
- `Store.Reset` + `Store.Rebuild`（顶层 `migrate --rebuild-index`）：清空并重新扫描重建 `logs` 索引。

因此表分区与归档 job 在本仓库代码中未实现。若确需分区，属于 DBA 侧手工操作（自行建分区表、迁移数据并调整查询），应用侧没有配套的分区维护或归档代码；任何此类操作都必须自行保证 `.http` cassette 仍是 replay 的事实源。

## 锁与长事务排查

```sql
SELECT
  a.pid,
  a.usename,
  a.application_name,
  a.state,
  now() - a.xact_start AS xact_age,
  now() - a.query_start AS query_age,
  a.wait_event_type,
  a.wait_event,
  LEFT(a.query, 500) AS query_sample
FROM pg_stat_activity a
WHERE a.datname = current_database()
  AND (
    a.wait_event IS NOT NULL
    OR (a.xact_start IS NOT NULL AND now() - a.xact_start > interval '5 minutes')
    OR (a.query_start IS NOT NULL AND now() - a.query_start > interval '30 seconds')
  )
ORDER BY COALESCE(a.xact_start, a.query_start) ASC NULLS LAST;
```

DDL 或回填出现 `lock_timeout`、statement timeout 时暂停推进，先确认阻塞者再决定重试或改期。

## 可选重置

下面语句有副作用，只在准备开始明确采样窗口时执行：

```sql
SELECT pg_stat_statements_reset();
```

## 运维检查清单

上线前：

- [ ] 已确认目标是 Postgres production DB，而不是 SQLite fallback。
- [ ] `db migrate status --check-db` 非 dirty，migration version 符合预期。
- [ ] 已完成 pg_stat_statements 基线采样并保存 top SQL。
- [ ] 已保存相关查询的 `EXPLAIN (ANALYZE, BUFFERS)`。
- [ ] 新索引、summary 或回填方案有审阅过的 SQL/步骤。
- [ ] 大表 DDL 使用 `CONCURRENTLY`，且不在事务中执行。
- [ ] 已设置 `lock_timeout`、`statement_timeout` 和批量大小。
- [ ] 已确认备份、PITR 或快照可用，并记录恢复点。
- [ ] 已准备回退开关或旧读路径 fallback。

上线中：

- [ ] 先执行 read-only 检查和 dry-run。
- [ ] DDL 逐条执行；每条完成后检查 invalid index、锁等待和错误日志。
- [ ] 回填按小批次推进，记录 scanned、updated、conflicts、skipped、duration。
- [ ] 灰度读从 shadow/operator canary 开始，不直接全量切换。
- [ ] 持续观察 Monitor API p95/p99、Postgres CPU/IO、temp files、deadlocks、lock waits。
- [ ] 发现 statement timeout、lock timeout 或结果差异时暂停推进。

回滚：

- [ ] 读路径问题：先关闭灰度开关，回到旧查询。
- [ ] 新 summary 问题：停止写入/读取 summary，保留表用于排查；必要时 truncate 后重建。
- [ ] 新索引问题：确认未被依赖后执行 `DROP INDEX CONCURRENTLY IF EXISTS ...`。
- [ ] 回填问题：按记录的范围和幂等键撤销派生字段或重建 summary。
- [ ] schema dirty 或数据破坏：停止写入，按备份/PITR/审阅过的手工计划恢复。
- [ ] 不使用 `db migrate down` 作为生产快速回滚。
