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

本节按脚本的覆盖范围贴出基线语句：表大小与 vacuum 各覆盖 10 张当前 schema 的表，索引使用覆盖到 `trace_findings` 为止的 7 张表。已移除的派生表 `overview_metric_buckets` / `overview_metric_bucket_members` 不在这些清单里；第 9 节用 `to_regclass(...) IS NOT NULL` 判断它们是否仍存在，只有存在时才读取，不存在时脚本打印 `skipped` 并以 0 退出，而不是报错或把空结果当成 0 行。为便于手工执行，脚本里由 psql 变量注入的占位符在这里展开为默认值：`BASELINE_WINDOW` 写作 `interval '7 days'`，keyset EXPLAIN 的游标写作 `TIMESTAMPTZ 'REPLACE_WITH_CURSOR_AT'` 与 `'REPLACE_WITH_LAST_ID'`（脚本对应默认值为 `1970-01-01T00:00:00Z` 与空字符串）。脚本不包含的其它表见本节末尾的“扩展手查（脚本不覆盖的表）”，那部分需要手工执行。

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

表大小（脚本覆盖 `logs`、`session_summaries`、`trace_observations`、`parse_jobs`、`system_events`、`analysis_runs`、`trace_findings`、`request_audits`、`execution_events`、`upstream_exchanges` 共 10 张当前 schema 的表）：

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

vacuum 与 dead tuples（与表大小相同的 10 张表）：

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

索引使用（脚本覆盖 `logs`、`session_summaries`、`trace_observations`、`parse_jobs`、`system_events`、`analysis_runs`、`trace_findings` 共 7 张表；`idx_scan` 长期为 0 且体积大的索引是候选清理对象）：

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

旧的 Overview 小时桶（`overview_metric_buckets` / `overview_metric_bucket_members`）代码已不再写入，Overview 改为直接聚合 `logs`（Postgres 历史迁移仍会创建这两张空表，除非运维手工 `DROP`）。脚本只在它们仍存在时才读取；下面就是脚本用的 `\gset` + `\if` 写法，表不存在时打印一行 `skipped`：

```sql
SELECT to_regclass('overview_metric_buckets') IS NOT NULL AS has_overview_buckets \gset
\if :has_overview_buckets
SELECT
  COUNT(*) AS bucket_count,
  MIN(bucket_start) AS first_bucket,
  MAX(bucket_start) AS last_bucket,
  SUM(request_count) AS bucket_requests,
  SUM(success_request) AS bucket_success,
  SUM(failed_request) AS bucket_failed,
  SUM(total_tokens) AS bucket_tokens,
  pg_size_pretty(pg_total_relation_size('overview_metric_buckets')) AS total_size
FROM overview_metric_buckets;
\else
\echo 'skipped: overview_metric_buckets absent (removed derived table)'
\endif

SELECT to_regclass('overview_metric_bucket_members') IS NOT NULL AS has_overview_members \gset
\if :has_overview_members
SELECT
  COUNT(*) AS member_count,
  MIN(updated_at) AS first_member_update,
  MAX(updated_at) AS last_member_update,
  pg_size_pretty(pg_total_relation_size('overview_metric_bucket_members')) AS total_size
FROM overview_metric_bucket_members;
\else
\echo 'skipped: overview_metric_bucket_members absent (removed derived table)'
\endif
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

`scripts/postgres-baseline.sh` 固定覆盖上面的表集合，不包含 routing/model 配置表、`tool_call_audits`、`analysis_jobs`，也不把 `request_audits`、`execution_events`、`upstream_exchanges` 纳入索引使用查询。需要这些数据时手工执行以下语句。注意下面这份表集合是示例而非全集：`semantic_nodes`（旧部署里最大的表；运行时代码已不再写入它，但 Postgres 的历史迁移仍会建表，所以只有运维手工 `DROP` 之后它才消失——把这个名字留在 `IN (...)` 里不会报错，`relname IN` 中的不存在表名只是匹配不到行，也可以直接删掉）、`responses`、`response_items`、`upstream_targets`、`upstream_models`、`channel_probe_runs`、`datasets`、`dataset_examples`、`scores`、`experiment_runs`、`parser_versions`、`users`、`api_tokens` 既不在脚本里，也不在下面这份列表里，需要时把它们加进 `IN (...)`。

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
    'semantic_nodes',
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

## Monitor 系统页（内置基线视图）

Monitor 的 `/system` 页面（管理员可见）提供了本节基线的在线版本，`GET /api/system/db` 一次请求即读取：`pg_database_size` 与 `pg_stat_database` 的命中率/temp/deadlock/事务计数、`pg_stat_activity` 的连接分布与最久运行语句、`public` 下最大的关系、`pg_stat_user_indexes` 的未使用索引（`idx_scan = 0`，按大小）与读放大最严重的索引、`pg_stat_user_tables` 的热表、检查点计数与 `pg_settings` 的关键参数。

采集纪律（改动这一页时必须保持）：

- 只读：全部语句在一条 `BEGIN ... READ ONLY` 事务内执行，最后回滚；页面与测试都不得执行 `EXPLAIN`、`EXPLAIN ANALYZE`、`VACUUM`、`ANALYZE` 或任何写语句。
- 有界：事务内 `SET LOCAL statement_timeout = 2000`，外层 `context.WithTimeout(5s)`，每个列表 `LIMIT 20`，`pg_stat_activity.query` 先 `left(query, 4000)` 再由 Go 侧脱敏并截断到 500 字符。
- 分节失败不整体失败：单节错误进入响应的 `warnings`，其余分节照常返回。
- 非 Postgres 驱动返回 HTTP 200 且 `supported=false`、`unsupported=true`，不返回 5xx。

检查点计数在 Postgres 17 上来自 `pg_stat_checkpointer`（`num_timed`/`num_requested`）；`pg_stat_bgwriter.checkpoints_timed`/`checkpoints_req` 在这些版本已不存在，响应里的 `source` 字段标明实际来源。

应用侧的慢语句采集默认关闭，由 `debug.slow_query_threshold`（`TRAJECTA_DEBUG_SLOW_QUERY_THRESHOLD`，默认 `0`）控制：为 0 时不包装、不记录，热路径没有额外开销；设为正数（例如 `"200ms"`）后记录超过阈值的语句，保存在容量 50 的进程内环形缓冲区中，语句先脱敏再截断到 500 字符。它记录的是应用发出的语句文本，不依赖 `pg_stat_statements` 扩展。

profiling 由 `debug.pprof_enabled`（`TRAJECTA_DEBUG_PPROF_ENABLED`，默认 false）控制；为 true 时 `net/http/pprof` 挂载在 Monitor 的 `/debug/pprof/`，为 false 时该前缀返回 404。

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

`shared_buffers` 属实例侧参数，应用不会设置它（仓库里没有 `shared_buffers` / `effective_cache_size` 配置项）。参考实例的 117 GB 数据库仍沿用默认 `shared_buffers = 128 MB`，缓存命中率约 **87.83%**，`pg_stat_statements` 里高成本的列表/overview 查询因此反复读盘（`shared_blks_read` 高）。这是运维侧最值得优先调整的一项：专用主机上的常见起点是把 `shared_buffers` 提到内存的约 25%，并同时把 `effective_cache_size` 设为内存的约 50–75%，比逐条查询调 `work_mem` 更根本；改后需重启（托管实例走参数组），并重新采样基线确认命中率与 top SQL 的变化。

Trajecta 热表优先级：

- `logs`：trace list、session list、overview、model/provider/time filters。
- `request_audits`、`execution_events`、`upstream_exchanges`、`tool_call_audits`：Responses audit 与 exchange correlation。
- `analysis_jobs`、`analysis_runs`、`trace_observations`、`trace_findings`、`system_events`：重分析、findings、Monitor summary。
- `channel_configs`、`channel_models`、`model_catalog`、`model_aliases`：routing/model 管理读路径。

## 大表聚合的内存与并行度

`semantic_nodes` 说明：本节与后面的「派生表修复里最容易踩的…」「第二容易踩的…」两节，都用旧库的 `semantic_nodes`（旧部署里最大的表）做实测。运行时代码已不再写入该表，但 Postgres 的历史迁移仍会建表且没有 drop 迁移，所以除非运维手工 `DROP`，它仍在；`trajecta upgrade db --reconcile-derived-trace-ids` 通过 `targetTableColumns` 从当前 Postgres 的 `information_schema` 动态发现带 `trace_id` 的派生表，所以只要表还在，下面的结论就继续适用；运维按 [存储与部署](./STORAGE_AND_DEPLOYMENT.md) 手工 `DROP TABLE semantic_nodes` 之后，这些章节随之作废。

`/dev/shm` 决定并行查询能申请多少共享内存。容器默认只有 64 MB，`work_mem` 给得高、并行 worker 又多时，hash 聚合会直接失败：

```text
pq: could not resize shared memory segment "/PostgreSQL.4213527324" to 67244032 bytes: No space left on device (53100)
```

派生表修复（`trajecta upgrade db --reconcile-derived-trace-ids`）在一次真实运行中就是这样失败的，而且当时 `work_mem` 还是默认的 4 MB——申请的段只有 8 MB 也被拒，所以 64 MB 的 `/dev/shm` 对任何并行度都偏小。同一份数据（`semantic_nodes` 7400 万行、总大小 120 GB、堆 55 GB，`trace_id` 上有 `(trace_id, depth, node_index)` 索引）的实测：

| 观察 | 结果 |
| --- | --- |
| 默认 `work_mem=4MB`、`max_parallel_workers_per_gather=2` | `DISTINCT trace_id` 反连接长时间不结束，`pg_stat_database.temp_bytes` 持续增长（排序溢出到磁盘） |
| 并行段申请 | 超过 64 MB 的 `/dev/shm` 时直接报错结束，与 `work_mem` 高低无关 |

**这些参数不能通过 DSN 的 `options` 传递。** 本项目的 Postgres 驱动是 `github.com/lib/pq`，它不转发 DSN 里的 `options`；同一串 DSN 在 `libpq`（`psql`）下生效，经应用连接时被静默忽略——所以「换个 DSN 就调好了」是错觉，用它解释运行快慢也会得出错误结论：

```text
# psql（libpq）读到 32MB / 0
psql "postgres://…/trajecta?sslmode=disable&options=-c%20work_mem%3D32MB%20-c%20max_parallel_workers_per_gather%3D0" \
  -c "select current_setting('work_mem'), current_setting('max_parallel_workers_per_gather')"
# 应用连接（lib/pq）仍是 4MB / 2，复核正在运行的会话即可看到：
select current_setting('work_mem'), current_setting('max_parallel_workers_per_gather')
from pg_stat_activity where pid = <pid>;
```

会话级调参要么写进 `postgresql.conf`／容器启动参数，要么按库或角色设置——服务端在建立连接时施加，与驱动无关，实测可即时生效也可即时撤销：

```sql
ALTER DATABASE trajecta SET work_mem = '32MB';
-- 撤销：ALTER DATABASE trajecta RESET work_mem;
```

判断是否仍在溢写要看增量而不是累计值：`pg_stat_database.temp_bytes` 是自统计重置以来的累计量，前后两次采样相减才说明当前语句有没有落盘。

这个上限不只影响大表。在默认 64 MB 下，只要多个会话同时使用并行 hash 节点，`logs` 这种 26 万行量级的普通 `count(*)` 也会报同一个 `No space left on device`——修复期间实测被拒两次，其中一次只申请 8 MB。所以提高 `/dev/shm` 是比任何会话级 `work_mem` 调整都更根本的修法；会话级设置只能保证「本次会话不占用共享内存」，挡不住同一实例上并发的其它会话。仓库的 `docker-compose.yml` 已为 `postgres` 服务设置 `shm_size: "1gb"`；改动后必须重建 postgres 容器才生效（`docker compose up -d --force-recreate postgres`），且自建 compose 的部署要同步这一项。

枚举 `DISTINCT trace_id` 没有更省 I/O 的替代写法。常见想法是用递归 CTE 做松散索引扫描（每次取 `WHERE trace_id > 上一个值 ORDER BY trace_id LIMIT 1`），它确实有效——索引下降会直接跳过同一个 trace 的全部重复项，头 1 万次迭代只要 162 ms（约 16 µs/次）——但到 20 万次迭代就超过 200 秒：每次跳跃要跨过约 50 个叶页，于是退化成一次**随机**叶页读，在这台盘上比顺序读取整个 7.4 GB 索引更贵。所以孤儿枚举保持顺序全扫。

**但顺序全扫不是那 167 分钟的原因，原因是连接方式。** 规划器把这个反连接做成了 `Nested Loop Anti Join`：外层每产出一个 `DISTINCT trace_id`，就拿它去 `logs_trace_id_key` 探一次。外层要把 7.4 GB 索引整个扫过一遍，这一路会持续把 21 MB 的 `logs` 索引挤出 buffer cache，于是每次探测都退化成一次**随机**磁盘读；而探测次数正比于不同 trace id 的个数（约 28.9 万个，量法见下）。这条路没有跑完：`Nested Loop` 版本在 193 分钟时被中止且仍未结束，而统计修正后同一个反连接（`Merge Anti Join`，两侧各扫一次）在 **77 分钟**内跑完。所以代价并不在「必须顺序扫完 7.4 GB」，而在「每个不同 id 都要付一次随机读」。

它选错计划，是因为规划器相信 `semantic_nodes.trace_id` 只有 29,253 个不同值，而真实值约 28.9 万个。29,253 是**采样伪影**，重跑 `ANALYZE` 修不好：统计采样只有约 3 万行，高基数列的不同值数天然被截断在采样行数附近，所以它必然落在 3 万上下。正解是在列上放覆盖统计：

```sql
-- 负值表示「占行数的比例」，会随表增长自动伸缩
ALTER TABLE semantic_nodes ALTER COLUMN trace_id SET (n_distinct = -0.004);
```

真实比例要用一条**严格约束**量出来，不能靠外推：`logs.trace_id` 是唯一键，全表 248,164 行就有 248,164 个不同 trace id，所以 `semantic_nodes` 中「存在于 `logs`」的不同 id 不可能超过 248,164 个。用 `TABLESAMPLE SYSTEM (0.001)` 抽一小批不同 id 再去 `logs` 里判定，实测 77 个里 66 个存在（86%），于是 `D ≈ 248,164 / 0.86 ≈ 28.9 万`，占 7,400 万行的 **0.39%**。

这里也纠正本文早前一次错误的外推：`TABLESAMPLE SYSTEM (0.1)` 采到的 73,488 行里有 8,874 个不同 id，按行数放大会得到「约 890 万个」——但页级采样在「同一个 trace 的行跨多页」时会系统性高估不同值占比，那个数量级与上面的严格约束直接矛盾，应以约束为准。

改完后规划器在**默认设置下**自己改选 `Merge Anti Join`，两侧各扫一次、没有逐 id 探测。值得记下的是原来的选择有多脆：估计值 29,253 下嵌套循环代价 6,072,730、合并 6,075,165，**只差 0.04%**，于是硬币翻到了灾难性的那一面；修正后合并领先约 7%（串行）到 17%（并行）。这也说明「代价几乎相等」的计划选择在寻道受限的盘上值得人工复核，而不是交给代价模型。统计对了，任何会话都拿到正确计划——所以也不该靠调整会话参数绕开。

顺带两条可复用的判据：

- 这台盘上顺序与随机差两个数量级：冷读 782 MiB 用了 55.3 秒（**14.8 MB/s**），而 `TABLESAMPLE SYSTEM (0.1)` 只读 57 MB 却要 6 分 14 秒。看到「读得不多却很久」，先怀疑访问模式而不是数据量。
- `pg_statio_user_indexes` **看不到长语句的进度**：PostgreSQL 15 起统计由后端在事务结束时上报，一条跑了三小时的语句在结束前计数不会出现。把「计数不动」读成「没在扫这条索引」是错的——当时那批增量其实来自线上服务的正常 trace 查询。

孤儿枚举为什么不走「先取候选」的捷径：可以只把归档 SQLite 里出现过的 trace id 加上 `logs` 里的供体 id 作为候选集，再用 `logs_trace_id_key` 逐个判定，I/O 远小于一次全扫。但那只能覆盖「旧库来源」的孤儿；服务自身重写索引时留下的 id 可能既不在当前 `logs`、也不在归档库里，漏掉一个就会留下永久悬空的派生行。这里的顺序全扫是**用一次 I/O 换完备性**：只要 `DISTINCT trace_id` 的枚举是完整的，后面那步按 trace id 逐条判定就走索引、代价极小。

## 派生表修复里最容易踩的一条：`IS NOT DISTINCT FROM` 不走索引

`IS NOT DISTINCT FROM` **不能作为 btree 索引条件**。用它比较派生表的身份列时，规划器只会保留 `trace_id` 进 `Index Cond`，其余列退化成 join filter，于是每个候选行都要把对端 trace 的**全部**节点取回来再逐行丢弃，代价是 O(候选行 × 对端 trace 行数)。

实测：`semantic_nodes` 有 7,400 万行、约 28.9 万个不同 trace id（见上一节）。一批 200 个 id 的重复计数在改前要跑 **46 分钟以上**，而同一表达式还用在 `DELETE` 上，只会更慢。身份列都是 `NOT NULL`，改用 `=` 语义完全等价，计数变成两列 index-only 探测、删除变成 `Index Scan`，同一对 id 从 462 ms 降到 0.255 ms：

```
-- 改前
Join Filter: (NOT ((c.node_id)::text IS DISTINCT FROM (d.node_id)::text))
  Rows Removed by Join Filter: 171
  Index Cond: (trace_id = m.current)                       -- node_id 没进索引条件
-- 改后
  Index Cond: ((trace_id = m.current) AND (node_id = (d.node_id)::text))
```

判断方法就是看 `EXPLAIN`：`Index Cond` 里必须出现你要比较的每一列；只有 `trace_id` 出现、另一列出现在 `Filter`/`Join Filter` 里，就是这个问题。代码侧由 `internal/legacymigrate` 从 `pg_attribute.attnotnull` 取可空性，只有真正可空的身份列才退回 NULL 安全写法。

## 派生表修复里第二容易踩的一条：schema 缺了以 `trace_id` 打头的索引

Postgres 的 `parse_jobs` 长期只有 `pkey(id)` 和 `(status, updated_at)` 两个索引，没有任何以 `trace_id` 打头的索引；而同一张表在 SQLite 启动 schema 里一直有 `idx_parse_jobs_status_trace`，也就是这个形状**从来没有跟着进 Postgres 迁移**。于是所有按 trace id 取解析任务的查询都只能全表扫：Monitor 的 trace 详情、派生表修复的 `trace_id = ANY(...)` 与去重、以及 `--prune-superseded-index-rows` 的 `NOT EXISTS (… d.trace_id = l.trace_id)` 守卫。

代价在一次真实修复里是可测的：prune 守卫对每个待删索引行探一次 `parse_jobs`，`pg_stat_user_tables` 因此记下 `seq_scan 14,650`、`seq_tup_read 6,580,802,230`——约 67 亿行，等于同一张 449k 行的表被完整扫了 14,994 次，这是那一步 11 分钟几乎全部的来源。同一条按 trace id 的探测，前后对比：

| 计划 | 执行时间 | 读页 |
| --- | --- | --- |
| 修复前（无可用索引，`Parallel Seq Scan`） | ≈58 ms | 7,404 页（约 60 MB） |
| 修复后（`Index Only Scan using parsejob_trace_id_status`） | 0.125 ms | 4 页 |

修法是把索引补进 ent schema（`ent/schema/parse_job.go` 的 `index.Fields("trace_id", "status")`）并配一条版本化迁移（`20260929090000_add_parse_jobs_trace_id_index`）。迁移里用 `CREATE INDEX IF NOT EXISTS`，18 MB、2.3 秒，因此可以先把索引手工建在实例上，之后迁移只是空操作。`trace_id` 打头也顺带覆盖 Monitor 的 `trace_id = ? AND status = ?`（两列都进 `Index Cond`），只按 `status` 的查询仍由原有的 `(status, updated_at)` 服务。

这类缺口的发现方法不是看慢查询，而是把两套 schema 的索引形状对起来：从 `internal/store` 的 `sqlite_schema.go`（70 处）、`schema_bootstrap.go`（21 处）与 `overview.go`（2 处）里抽出所有反引号语句中的 `CREATE INDEX … ON <表>(<列>)`，与迁移后数据库的 `pg_indexes` 逐形状（表 + 归一化后的列序列，忽略 `ASC`/`DESC`、partial 与唯一性）比对。

按这个方法复核过当前状态（SQLite 启动 schema 82 个形状，迁移后的 Postgres 87 个，`parsejob_trace_id_status` 等上一批补的形状两侧都在），剩下的差异都不影响能力，只是形状不同：

| 只有 SQLite | 只有 Postgres | 判定 |
| --- | --- | --- |
| `logs(session_id, recorded_at, trace_id)` | `logs(session_id, recorded_at)` | PG 的索引少了尾列 `trace_id`，同序时只多一次并列排序 |
| `parse_jobs(status, trace_id)` | `parsejob_trace_id_status(trace_id, status)` | 两列都是等值条件，两种顺序都能进 `Index Cond` |
| `session_summaries(last_seen)` | `session_summaries(last_seen DESC, session_id DESC)` | PG 的形状是 SQLite 的超集 |
| `system_events(last_seen_at, id)`、`(status, last_seen_at, id)`、`(source, category, last_seen_at, id)` | `systemevent_status_last_seen_at(status, last_seen_at)`、`systemevent_source_category_last_seen_at(source, category, last_seen_at)` | 头几列相同，PG 上没有尾列 `id`，稳定分页的并列需要一次排序 |
| `system_events(trace_id, last_seen_at) WHERE trace_id <> ''` | `systemevent_trace_id_last_seen_at(trace_id, last_seen_at)` | SQLite 用 partial，PG 用全量 |
| — | `api_tokens(enabled)`、`api_tokens(prefix)`、`api_tokens(token_hash)`、`users(username)`、`datasets(updated_at)`、`eval_runs(created_at)`、`parser_versions(parser, version)`、`trace_observations(request_audit_id, updated_at)`、`trace_observations(response_id, updated_at)` | 都在小表或只在 Postgres 才可能大的表上（`trace_observations`），SQLite 侧全表扫的代价可忽略 |

另外复核了一次前缀冗余：把 Postgres 里所有非唯一、非 partial 索引按列序列两两比较，没有「左边的列是右边严格前缀」的重复索引。

**待确认的删除候选：`tracelog_parent_exchange_id`。** `logs.parent_exchange_id` 在 `internal/store` 里只出现在 SELECT 投影和写入里，全仓库没有任何 `WHERE parent_exchange_id = ?`（raw SQL 与 ent 谓词都查过），MCP 与 Monitor 侧只在结构体/JSON 里引用该字段；索引由 `c8601e6` 引入。`logs` 是写入最热的表，这个索引只在每次插入时增加维护成本，但「代码里没用到」不等于「实例上没用到」——生产上先按下面的查询确认 `idx_scan = 0`，再决定是否 `DROP INDEX CONCURRENTLY`：

```sql
SELECT indexrelname, idx_scan, pg_size_pretty(pg_relation_size(indexrelid)) AS size
FROM pg_stat_user_indexes
WHERE relname = 'logs'
ORDER BY idx_scan, indexrelname;
```

`idx_scan` 统计在 `pg_stat_reset()` 或实例重启后清零，判断前要先确认统计窗口覆盖了完整的流量周期（至少一个工作日加一次周报/月度报表）。

## 第二批缺口：`logs(selected_upstream_id)`、审计表与分析表的 `created_at`

按上面同样的方法再把两套 schema 对一遍，又找到五处形状缺口，都进同一条版本化迁移 `20260930130000_add_analytics_indexes`（`ent/schema` 同步声明，SQLite 启动 schema 在 `ensureHotpathIndexes` 里用同样的索引名与列补齐；其中 `tracefinding_severity_created_at` 这个形状后来被 `20261009090000_add_hot_path_indexes` 替换，见本节末尾）：

| 索引 | 表 | 服务的查询 |
| --- | --- | --- |
| `tracelog_selected_upstream_id_recorded_at` | `logs` | `ListUpstreamAnalytics` 与每个 upstream 的 model coverage / recent errors / recent failures（`WHERE selected_upstream_id = ?` 或 `<> ''`），以及 `DISTINCT selected_upstream_id` 列表 |
| `requestaudit_created_at_id` | `request_audits` | 默认 Responses audit 列表（无过滤，`ORDER BY created_at DESC, id DESC`） |
| `toolcallaudit_created_at_id` | `tool_call_audits` | 同上 |
| `analysisrun_created_at_id` | `analysis_runs` | `ListAnalysisRuns("", "", "", limit)`（overview 的最近分析），原有两个索引都以 `trace_id` 或 `session_id` 打头 |
| `tracefinding_severity_created_at_id` | `trace_findings` | `overviewHighRiskFindings` 的 `severity IN ('critical', 'high')`（查询改成两次单 severity 查找后按 `created_at DESC, id DESC` 排序），原有索引都以 `trace_id` 打头。它取代了 `20260930130000` 建的 `tracefinding_severity_created_at (severity, created_at)`：旧形状无法服务 `CASE` 排序，`20261009090000_add_hot_path_indexes` 已把它 `DROP` |

`logs` 这一处最贵：`selected_upstream_id` 被 24 处 `= ?` / `<> ''` 谓词加 3 处 `GROUP BY` 命中，分布在 `internal/store/store.go`（4 处谓词）、`analytics.go`（11 处谓词 + 2 处 `GROUP BY`）与 `channel_store.go`（9 处谓词 + 1 处 `GROUP BY`）里，而它不在任何一个索引里，于是 upstream analytics 页面按 upstream 数量发起的 1+4N 条查询，每条都是 `logs` 的全表扫。用 20 万行的同形合成表测同一条聚合（`selected_upstream_id = 'ch3' AND recorded_at >= now() - interval '7 days'`）：

| 计划 | 执行时间 |
| --- | --- |
| 无索引（`Parallel Seq Scan`，20 万行） | 41.9 ms |
| `Bitmap Index Scan`（2.5 万行） | 10.9 ms |

真实 `logs` 是 388 MB / 248,164 行的宽表，同一个形状在 5 个 upstream 的 analytics 页上会重复约 20 次，省下的是页面级的重复全表扫而不是单条查询的常数。审计表的两个索引则要跟着请求写入放大，取舍依据是「无过滤、按 `created_at` 倒序分页」的默认列表在 Postgres 上只能全表扫加排序；另外三张表都不在请求热路径上。

这五个索引和 `parsejob_trace_id_status` 一样由 `db migrate up`（或启动时的迁移）以普通 `CREATE INDEX` 创建：`logs` 上的那个要在 388 MB 的表上写入阻塞数秒。可以先在实例上手工执行对应的 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`，之后的迁移就是空操作。

后续形状变更由两条迁移完成，它们同样由 `db migrate up` 以普通 `CREATE INDEX` / `DROP INDEX` 执行，想避免写阻塞就先把 `CREATE INDEX` 手工换成 `CONCURRENTLY`：

- `20261009090000_add_hot_path_indexes`：新增 `tracefinding_created_at_id`（`trace_findings(created_at DESC, id DESC)`，findings 列表默认排序）与 `analysisjob_created_at_id`（`analysis_jobs(created_at DESC, id DESC)`，analysis job 列表默认排序）；新增 `tracefinding_severity_created_at_id`（`(severity, created_at DESC, id DESC)`）并 `DROP` 掉 `tracefinding_severity_created_at`；`DROP` 掉从未被扫描的 `tracelog_request_audit_id_recorded_at` 与 `tracelog_exchange_kind_recorded_at`（各约 15 MB，`logs` 是最热写入路径，无人读取的索引就是纯写入放大）。down 迁移把四者反转。
- `20261008120000_add_log_routing_detail`：给 `logs` 新增 `route_target_id`、`channel_id`、`credential_id`、`sticky_status`、`sticky_previous_upstream_id` 五列（`NOT NULL DEFAULT ''`），让路由 summary 从逐 cassette 读 prelude 变成对 `logs` 的一次 `GROUP BY`。它**不**建新索引：summary 唯一的谓词是时间窗口，已有的 `tracelog_recorded_at`（`logs(recorded_at)`）已经用 bitmap index scan 覆盖它，而 `sticky_status` 在代码里从来不是谓词，所以 `(recorded_at, sticky_status)` 只会是更宽、永远不会被选中的死索引——在这张全进程最热的写入表上，无人读的索引就是纯粹的写放大。

## 并发索引变更

加索引前必须同时满足：pg_stat_statements 有明确慢 SQL 或高成本 SQL；`EXPLAIN (ANALYZE, BUFFERS)` 证明现有索引未覆盖过滤、排序或 join；候选索引匹配稳定产品查询而非一次性排障；已评估写入放大、索引体积和 vacuum 成本。

内置的安全入口是 `db migrate optimize-indexes`（`--dry-run` 只预览不执行），命令归属与通用行为见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)。该命令要求非空 Postgres DSN，逐条执行非事务的 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`；SQLite 上该命令直接报告 `index_optimization_status: not_applicable` 并以 0 退出（`internal/appdbmigrate` 的 `OptimizeIndexes` 对 SQLite 才会返回 `ErrSQLiteUsesStoreInit`，CLI 在调用前已按迁移模式短路）。当前它只覆盖 `logs` 热点查询，创建以下 5 个索引：

```text
tracelog_recent_client_visible_idx              最新 logs 与分页 trace list（recorded_at DESC, trace_id DESC）
tracelog_session_recent_client_visible_idx      session 详情页与每 session 最新 trace
tracelog_failure_recent_client_visible_idx      最近失败 trace list 与 overview attention failure
tracelog_routing_failure_recent_client_visible_idx  routing failure 分析与列表
tracelog_duration_slow_client_visible_idx       duration DESC 慢请求列表
```

未建这 5 个索引时的实测基线（`logs` 248,164 行 / 388 MB，`shared_buffers` 已预热，`max_parallel_workers_per_gather=0`）：

| 产品查询 | 计划 | 执行时间 |
| --- | --- | --- |
| 最近失败列表（`status_code >= 400` 按 `recorded_at DESC` 取 50） | `Index Scan Backward using tracelog_recorded_at`，`Rows Removed by Filter: 8790`，读 836 页 | 934 ms |
| 慢请求列表（`duration_ms DESC` 取 50） | `Seq Scan on logs`（246,336 行）+ top-N heapsort，读 24,682 页 | 250 ms |
| 最新 trace 列表（`recorded_at DESC` 取 50） | `Index Scan Backward using tracelog_recorded_at` | 0.16 ms |

第三行是反例：同一个 `COALESCE(exchange_kind, '') IN (...)` 过滤，只要排序键是 `recorded_at` 而结果集又是最新的 50 条，现有的 `tracelog_recorded_at` 就已足够——所以这 5 个索引是按查询形态逐个判定后加的，不是「给 `logs` 多加几个索引总没坏处」。

这 5 个索引的收益与代价用一张同形合成表量过（250,000 行 / 202 MB，`ANALYZE logs` 后取三轮 `EXPLAIN (ANALYZE, BUFFERS)` 的最小值）：

| 产品查询 | 只有默认迁移索引 | 加上 5 个 partial index |
| --- | --- | --- |
| 最新 trace 列表（`recorded_at DESC, trace_id DESC` 取 50） | 0.64 ms | 0.45 ms |
| session 详情（`session_id = ?` 取 50） | 2.45 ms | 0.44 ms |
| 最近失败列表（`recorded_at >= ? AND 失败谓词` 取 50） | 71.5 ms | 0.50 ms |
| routing failure 列表（`routing_failure_reason <> ''` 取 50） | 61.2 ms（`Seq Scan`） | 0.48 ms |
| 慢请求列表（`duration_ms DESC` 取 50） | 81.0 ms | 0.49 ms |

前两行说明「已有索引够用」的那一档基本没有变化，后三行才是这 5 个索引存在的理由：默认索引下它们要么靠 `tracelog_recorded_at` 扫掉整个窗口再过滤，要么直接全表扫，加上之后都走 `Index Only Scan`，只读 50 行对应的索引页。

代价同样量了：在 250,000 行 / 202 MB 的表上逐条执行普通 `CREATE INDEX`（版本化迁移与 `db migrate up` 用的形态）合计 **1.98 s 的写入阻塞**，5 个索引合计约 28 MB。因此这 5 个索引留在 opt-in 的 `db migrate optimize-indexes`（`CREATE INDEX CONCURRENTLY`，不阻塞写入）里，而不是进默认迁移路径；生产 `logs` 388 MB，阻塞时间按同比例放大。

overview 的两个百分位查询（`overviewPercentile`：`WHERE <窗口谓词> AND col > 0 ORDER BY col ASC LIMIT 1 OFFSET n`）也顺带量过，结论是**不要**为它们加 `logs(ttft_ms)` / `logs(duration_ms)`：24 小时窗口下这个形状只取 7,007 行、耗时 20.4 ms，单独建 `(ttft_ms)`（239 ms 建索引）与 `(duration_ms)`（250 ms）之后 `EXPLAIN` 的计划与耗时都不变（仍是 partial index 位图扫描 + top-N heapsort，20.2 ms），原因是排序键与窗口谓词没有可用的组合索引时规划器仍偏好小索引加排序。窗口放宽到 30 天时该形状会退化为 83 ms（加 partial index 后 133 ms，因为位图扫描要取回 212,486 行再排序），这属于窗口大小问题而不是索引缺口。

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

## Overview 观测汇总的实测

Monitor 的 Overview 页面每 60 秒轮询一次（`overview.refresh` = `refresh / 60s`），其中观测汇总的六个数在 2026-09 之前是 4 条独立语句，现在合并成 1 条（`internal/store/overview.go` 的 `overviewObservation`）。同形合成表（250,000 行 `logs` / 200,000 行 `trace_observations` / 20,000 行 `parse_jobs`，`ANALYZE` 后三轮取最小）实测：

| 查询 | 计划 | 执行时间 |
| --- | --- | --- |
| `trace_observations` 的 total / parsed | `Parallel Seq Scan` | 33.5 ms |
| failed 去重（观测与 `parse_jobs` 的 UNION） | `Bitmap Index Scan` + 排序去重 | 30.7 ms |
| **unparsed 反连接**（`NOT EXISTS`） | `Parallel Hash Anti Join` | **105.2 ms** |
| parse_jobs 的 queued / running | `Seq Scan` | 6.2 ms |

反连接这条是汇总里唯一真正贵的：`logs` 的 13546 页要全部读一遍再和 `trace_observations` 做哈希反连接。**它现在已经是规划器能给出的最优形态**，另外三种等价写法都更慢，所以不要为了「看起来更 SQL」去改写它：

| 等价写法 | 执行时间 | 结果是否与 `NOT EXISTS` 相等 |
| --- | --- | --- |
| `NOT EXISTS`（现有） | 105.2 ms | — |
| `NOT IN (SELECT …)` | 164.0 ms | 相等 |
| `SELECT trace_id FROM logs EXCEPT SELECT …` | 998.9 ms | 相等 |
| `COUNT(logs) - COUNT(logs INTERSECT trace_observations)` | 1021.0 ms | 相等 |

结论：把 4 条语句合并成 1 条只是省 3 次往返，DB 侧工作量不变（同一条语句里六个标量子查询各自扫描一次），真正的开销上限由反连接决定。要去掉这 105 ms 只有两条路，都不是索引能解决的：

1. **派生计数**：在 `logs` 写入、`SaveObservation`、任务状态变更时维护 unparsed 计数，并配一条对账语句／命令。读变成 O(1)，代价是计数可能漂移，需要覆盖所有写入路径。
2. **短 TTL 缓存 + 写路径失效**：读到的是同一份精确结果，只是最多滞后一个 TTL。

按当前 60 秒轮询频率，105 ms／分钟（生产 `logs` 388 MB，按行数比例约 150 ms）不值得引入上面任何一种复杂度；如果将来轮询频率提高或 `logs` 增长到百万级，再按上面的顺序做。

## 运维脚本（`scripts/postgres/`）

`scripts/postgres/` 把上面这些操作封装成可重复执行的入口。每个脚本都只是薄薄一层 `docker compose` 加项目自带命令：它不自己拼 DSN、不复制凭据，而是复用 compose 已经注入服务的 `TRAJECTA_DATABASE_DRIVER` / `TRAJECTA_DATABASE_DSN` / `TRAJECTA_CONFIG`，并用 `docker compose exec` 在数据库容器内跑 `psql`（走容器内本地认证）。所以同一套脚本既能驱动本仓库的 `docker-compose.yml`，也能驱动一份自建部署目录（本次就是用 `TRAJECTA_OPS_DEPLOY_DIR=/data/gateway` 逐条验证的）。

| 脚本 | 作用 | 何时跑 |
| --- | --- | --- |
| `common.sh` | 共享库：配置解析、`psql`/CLI/API 包装、通过/失败计数器。只被 source，单独执行会报错退出 | — |
| `evidence.sh` | **只读**证据报告：驱动与配置、迁移版本、SQLite 归档、索引健康、路径前缀与文件存在性、派生表孤儿、Monitor API、cassette magic 抽样。永不判定失败，适合贴进迁移记录或事故说明 | 迁移后、例行巡检 |
| `acceptance.sh` | 不变量验收：派生表无孤儿、`logs.path` 不越出已知根、无活动 SQLite、迁移不 dirty、无 invalid/not-ready 索引、`parse_jobs` 探测走索引、`parse_jobs` 去重语句仍计划为反连接（写成 `NOT IN (子查询)` 时 PostgreSQL 会保留 `SubPlan`，分组 id 超出 hash 预算后逐行重扫）、仅当 `semantic_nodes` 仍存在时其反连接仍为 `Merge Anti Join`（表已 `DROP` 的库打印一行 skipped，不计失败）、reconcile 干跑无 superseded 与待清理、Monitor API 可用。**任一项失败退出码为 1** | 迁移或修复后的门禁 |
| `reconcile-apply.sh` | 跑 `upgrade db --reconcile-derived-trace-ids`（默认干跑，`--apply` 才写；可加 `--prune-superseded-index-rows`），完整日志落到 `backups/` | 派生 trace id 需要修复时 |
| `vacuum-after-repair.sh` | 修复后的统计刷新与死元组回收：中小编制表 `VACUUM (ANALYZE)`，大表默认只 `ANALYZE` 并报出 dead tuples | 修复结束后紧接着 |
| `optimize-indexes.sh` | 建热查询部分索引（`server db migrate optimize-indexes`，`CONCURRENTLY` 且幂等）并用 `EXPLAIN (ANALYZE)` 复测失败列表与慢请求列表 | 首次建索引、索引变更后 |
| `verify-after-rewrite.sh` | cassette 普查 → 结构校验（`--fail-on-legacy`，旧 magic 记为错误）→ 起服务 → 无活动 SQLite → API 探针 | magic 重写之后 |
| `wait-for-rewrite.sh` | 等一个长时间重写容器退出，并校验其末行计数自洽：`scanned == rewritten + current + other` | 重写跑在一次性容器里时 |
| `rebuild-image.sh` | 从工作树重建 `server`/`trajecta` 并烤进镜像；替换前把旧镜像打成 `<image>-prev-<utc>` 以便回滚 | 本地迭代后端改动 |
| `restart-and-verify.sh` | 用镜像重建容器、确认容器**确实**运行在该镜像上、等 Monitor 就绪、显式应用迁移、再交给 `acceptance.sh` | 部署新镜像之后 |

每个脚本的 `--help` 就是打印它自己的头部注释（例如 `scripts/postgres/acceptance.sh --help`），参数含义以那里为准。

### 配置：先自动探测，再环境变量

默认值面向本仓库的 `docker-compose.yml`（服务名 `trajecta`、数据目录 `data/traces`），自建部署只需覆盖少数几项：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `TRAJECTA_OPS_DEPLOY_DIR` | `$PWD` | 放 `docker-compose.yml` 的目录 |
| `TRAJECTA_OPS_COMPOSE_FILE` | `$DEPLOY_DIR/docker-compose.yml` | compose 文件 |
| `TRAJECTA_OPS_SERVICE` | 自动探测 `trajecta` 或 `llm-tracelab` | 应用服务名 |
| `TRAJECTA_OPS_POSTGRES_SERVICE` | 自动探测 `postgres` | 数据库服务名 |
| `TRAJECTA_OPS_PG_USER` / `TRAJECTA_OPS_PG_DB` | `.env` 的 `POSTGRES_USER`/`POSTGRES_DB`，缺失时回读 postgres 容器自身环境，再退回 `trajecta` | 数据库角色与库名 |
| `TRAJECTA_OPS_DATA_ROOT` / `TRAJECTA_OPS_DATA_ROOT_HOST` | `/app/data/traces` / `$DEPLOY_DIR/data/traces` | 容器内与宿主机的 cassette 根，用于互相映射路径 |
| `TRAJECTA_OPS_LEGACY_ROOT` | `/opt/llm_proxy/logs` | 改名前记录在库里的旧前缀，验收要求它只剩 0 行 |
| `TRAJECTA_OPS_MONITOR_PORT` | `.env` 的 `TRAJECTA_HOST_MONITOR_PORT`，否则 `8081` | Monitor 端口；`TRAJECTA_OPS_BASE_URL` 可整体覆盖 |
| `AUTH_USER` / `AUTH_PASSWORD` | 部署 `.env` 同名键 | API 探针凭据；缺失时只跳过探针 |
| `DOCKER_CONFIG` | 系统默认 | 默认 docker 配置目录不可写时（沙箱、CI）需要显式设置 |

各脚本还有自己的开关，同样可用环境变量给定：`reconcile-apply.sh` 的 `TRAJECTA_OPS_LOG_DIR`、`evidence.sh` 的 `TRAJECTA_OPS_MAGIC_SAMPLE` 与 `TRAJECTA_OPS_LEGACY_TRACE_LIST`（`trace_id|旧路径` 列表，用来抽样复验曾经 404 的 legacy trace）、`vacuum-after-repair.sh` 的 `TRAJECTA_OPS_SMALL_TABLES`/`TRAJECTA_OPS_BIG_TABLES`/`TRAJECTA_OPS_BIG_TABLES_VACUUM`、`rebuild-image.sh` 的 `TRAJECTA_OPS_IMAGE`/`TRAJECTA_OPS_BASE_IMAGE`。

### 典型序列

```bash
export TRAJECTA_OPS_DEPLOY_DIR=/data/gateway    # 自建部署目录；本仓库里留空即 $PWD

scripts/postgres/evidence.sh                    # 先看现状（只读）
scripts/postgres/reconcile-apply.sh             # 干跑，读它写进 backups/ 的日志
scripts/postgres/reconcile-apply.sh --apply --prune-superseded-index-rows
scripts/postgres/vacuum-after-repair.sh         # 修复留下的死元组与陈旧统计
scripts/postgres/acceptance.sh                  # 门禁：失败即非 0 退出
```

`acceptance.sh`、`evidence.sh` 和 `optimize-indexes.sh` 的探针查询都以 `PGOPTIONS='-c max_parallel_workers_per_gather=0'` 注入会话，且 `scripts/postgres/common.sh` 把该变量应用到所有脚本的 `psql` 调用（libpq 会转发连接 `options`，而应用侧的 `lib/pq` 不会——见上文 `psql` 与应用连接对比）。它们要的是**正确结果**而不是并行加速，这样即便容器的 `/dev/shm` 很小也不会让整条验收语句失败。

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

重建入口是 `db summary rebuild sessions`（`--session-id` 局部回填、`--dry-run` 只读统计不写库、不带 `--session-id` 时全量删除并重建），命令语义见 [存储与部署](./STORAGE_AND_DEPLOYMENT.md)。这是当前唯一的派生汇总重建命令：`overview_metric_buckets` / `overview_metric_bucket_members` 已随 Overview 改为直接聚合 `logs` 一并停止维护，`Store.RebuildOverviewMetricBuckets` 与 `db summary rebuild overview` 都不存在了；这两张表不会被代码读取或写入（新建 Postgres 库经 `db migrate up` 仍会由历史迁移创建为空表，SQLite 启动 schema 不再创建），是否 `DROP` 由运维决定。

这条队列只服务于 `session_summaries`：写入路径只把受影响的 trace 与 session 记进一个进程内队列（`Store.markSessionSummariesRefresh` / `markDerivedRefreshForTrace`），队列达到上限（256 条）或调用 `Store.FlushDerivedRefresh()` 时才真正落库。**谁消费这个队列取决于是否调用了 `Store.StartDerivedRefresh()`**：`serve` 会调用，此后后台 goroutine 是唯一消费者，写入直接唤醒它（通常毫秒级），2 秒的 ticker（`store.DerivedRefreshInterval`）只是漏唤醒与失败重试的兜底；此时读 `session_summaries` 的两个入口（`ListSessionPage`、`GetSession`）**不再**自己冲刷，因此一次页面渲染不会为整会话重建买单，代价是读取端最多看到亚秒级的滞后。没有启动 flusher 的调用方（测试与无服务器的 CLI 命令）保持同步语义：这两个入口在查询之前先冲刷队列，读与它之前的写立即一致。`db summary rebuild sessions --dry-run` 用的 `SessionSummaryRebuildStats` 在两种模式下都故意不冲刷，它要报告表里现存的漂移而不是先把漂移修好。冲刷按 session 去重重建一次 `session_summaries`。`Sync`/`Rebuild` 在遍历结束后冲刷一次，所以 N 个同 session 的 cassette 只汇总一次而不是 N 次；遍历中途失败时索引行已经提交，派生刷新照常执行，失败只打印到 stderr。

队列只存在于进程内，不落盘：进程被强杀时最多丢掉 256 条待刷新记录。丢掉的 session 汇总会一直滞后，直到该 session 下次写入或执行 `db summary rebuild sessions` 才恢复。启动 flusher 之后重试不再依赖有人来读：失败与漏唤醒都由 ticker 兜底，所以一段无人访问的 session 也不会永久停在滞后状态。`Store.Close()` 先停 flusher 再做最后一次冲刷（避免冲刷与关闭后的连接池竞争、把 work 重新塞回没有消费者的队列），正常停机不会把待刷新记录留给下一次启动；`serve` 在后台同步停止之后、关闭数据库之前也会显式冲刷一次。

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

当前事实：`ent/postgres-migrations/*.up.sql` 里没有任何 `PARTITION` 语句，`logs`、`trace_observations`、`request_audits`、`execution_events`、`upstream_exchanges`、`tool_call_audits`、`system_events` 等都是普通表；`internal/appdbmigrate` 实现了 up/down 与并发索引优化，但 CLI 的 `db migrate down` 明确拒绝执行（包内 `MigrateDown` 目前没有 CLI 调用者），也不包含分区或归档逻辑。

代码里也没有按时间删除、搬迁或导出历史行的 retention/archive job。`internal/store` 中可验证的删除路径全部是派生数据重建，不删除 raw `.http` cassette：

- `RebuildSessionSummaries` / `RebuildSessionSummary`：删除并按 `logs` 重建 `session_summaries`。
- `SaveObservation`：按 `trace_id` upsert `trace_observations` 的紧凑摘要（并维护 `parser_versions` 与 `parse_jobs`），不再触碰已移除的 `semantic_nodes`；`SaveFindings`：按 `trace_id` 删除并重写 `trace_findings`。
- `Store.Reset` + `Store.Rebuild`（顶层 `migrate --rebuild-index`）：清空并重新扫描重建 `logs` 索引。

因此表分区与归档 job 在本仓库代码中未实现。若确需分区，属于 DBA 侧手工操作（自行建分区表、迁移数据并调整查询），应用侧没有配套的分区维护或归档代码；任何此类操作都必须自行保证 `.http` cassette 仍是 replay 的事实源。

## 应用侧读写连接池与语句超时

应用只通过配置管理自己的连接池，不会替实例调 `shared_buffers`、`work_mem` 等参数。Postgres 下可以给 Monitor/MCP 的读路径单独开一个只读池，避免慢看板占满写入路径的连接（配置键在 `database` 下，实现在 `internal/config/config.go` 与 `internal/store/store.go`）：

| 配置键 | 环境变量 | 默认 | 作用 |
| --- | --- | --- | --- |
| `database.read_max_open_conns` | `TRAJECTA_DATABASE_READ_MAX_OPEN_CONNS` | `0` | 只读池连接数上限。`0` 表示不单独开池，所有语句共用写入池的 `database.max_open_conns` |
| `database.read_max_idle_conns` | `TRAJECTA_DATABASE_READ_MAX_IDLE_CONNS` | `0`（等同 `read_max_open_conns`） | 只读池的空闲连接数；单独设置它不会开池 |
| `database.read_statement_timeout` | `TRAJECTA_DATABASE_READ_STATEMENT_TIMEOUT` | `0s` | 只读池上每条语句的 `statement_timeout`；`0` 表示不设上界。单独设置它也会开池 |

要点：

- 只对 Postgres 生效：`openReadPool` 对 SQLite 直接返回「用写入池」，因为 SQLite 在文件级序列化写入，第二个池只会增加争用。
- 池在 `read_max_open_conns > 0` 或 `read_statement_timeout > 0` 时打开，并在启动时 `Ping`；打不开就启动失败，而不是等第一个 Monitor 请求。
- 只有不在事务内、且首个关键字是 `SELECT` / `VALUES` 的语句才走只读池（`isReadOnlyStatement` 的判定刻意窄：`WITH`、`INSERT`/`UPDATE`/`DELETE`、DDL 与无法识别的语句一律走写入池，避免把写语句静默切到只读池）；事务内语句永远跟随该事务使用的池。写路径（proxy finalizer、parse worker、reanalysis 的写入）保持 `database.max_open_conns`。
- `read_statement_timeout` 通过 lib/pq 的连接选项 `statement_timeout` 施加，作用在该连接上的每条语句；超时的读以错误返回，而不是继续占着连接和磁盘队列。
- 只读池没有单独的 `application_name`，在 `pg_stat_activity` 里与写入池的连接无法直接区分；确认池是否生效要对照配置上限与实际连接数。

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
