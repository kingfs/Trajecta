# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) 1.1.0, and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html). The release dates below are the dates of the tagged commits; the earlier release history of this project is also visible in the repository's git tags.

## [Unreleased]

### Changed

- Observation and findings writes are grouped into parameter-bounded multi-row statements. `SaveObservation` issued one `INSERT` per semantic node and `SaveFindings` one per finding, so reanalysing a session of a few hundred nodes issued a few hundred statements inside one transaction; both now send one `INSERT` per 900-parameter chunk (64 nodes, 69 findings). A multi-row statement cannot name the same `(trace_id, node_id)` or `(trace_id, finding_id)` twice on Postgres, so each batch folds its duplicate conflict keys first; findings keep the last occurrence, nodes keep the first, which is the order `observationFlatNodes` already folded with. `BenchmarkSaveFindings` (120 findings) and `BenchmarkSaveObservationNodes` (200 nodes) keep the write measured, and on SQLite the statement count drops from 120 to 3 and from 200 to 5. `TestSaveFindingsBatchesRepeatedKeys` covers the chunk boundary, the last-duplicate fold and the empty findings list that still clears the trace, and `TestSaveObservationBatchesSemanticNodes` covers a node list longer than one chunk.

### Fixed

- Task claiming is one atomic statement, so two processes cannot run the same job. The observe and reanalysis workers listed queued rows and then marked each one running with a second statement, which left a window for a second `trajecta serve` or a CLI run to claim the same parse or analysis job. `Store.ClaimParseJobs` and `Store.ClaimAnalysisJobsForWorker` now move a batch of queued rows to running in one `UPDATE ... RETURNING`: Postgres selects the rows with `FOR UPDATE SKIP LOCKED`, SQLite has no row locks and relies on its write lock plus an in-process claim mutex and an outer `status = 'queued'` predicate that makes a racing claimer update nothing, and the claim also increments `attempts` and stamps `started_at`, so a claimed job is not marked a second time. The synchronous reanalysis methods (`ReanalyzeTrace`, `ReanalyzeSession`, `ReanalyzeBatch`, and the rest), which run a job inline without the worker, mark their own job running as before. `TestClaimParseJobsIsExclusive` claims a 40-job queue from four goroutines and asserts every job is handed out once with one attempt, `TestClaimAnalysisJobsForWorkerIsExclusive` covers the analysis queue, and `TestPostgresClaimSkipsLockedRows` holds a row lock from a second connection and asserts the claim skips that row instead of blocking, then claims it once the lock is released.

## [2.1.1] - 2026-09-30

### Changed

- `Store.Stats()` computes every request-list statistic in a single aggregate pass. The Monitor request list calls it on every page load, filter change and page turn, and it previously issued four separate aggregate queries over the same `logs` rows (total count, success count, mean TTFT, token sum), each of them filtering with the non-sargable `COALESCE(exchange_kind, '')` predicate, so the page paid for four scans of the same data. The single statement keeps the existing semantics exactly: the 2xx boundary is inclusive on 200 and exclusive on 300, failed rows contribute neither TTFT nor tokens, a successful row with a zero TTFT still counts in the AVG denominator, and upstream exchanges stay out of the totals. `TestStatsHandlesAverageTTFTAsFloat` pins the aggregate values and `TestStatsHandlesEmptyAndNonClientVisibleRows` covers the empty store, the 200/299/300 boundary, the zero-TTFT denominator and the client-visibility filter.
- A vault walk refreshes the derived tables once per session and once per hour bucket instead of once per changed file. `Sync` and `Rebuild` called the per-file path for every cassette, and both derived tables are rebuilt from whole sessions and hour buckets: a session with N recordings was summarised N times, each rebuild re-aggregating all of the session's rows, and every path paid for its own transaction with a contribution read, a member read, a member delete and two upserts. The walk now collects the paths and sessions it touched and applies them once at the end: `refreshOverviewMetricBuckets` merges the per-path deltas into one upsert per distinct bucket, and deletes and re-inserts the member rows in chunks bounded by 900 bind values (the historic SQLite variable limit, which every driver accepts). `RefreshOverviewMetricBucketForPath` remains the immediate per-path form that the equivalence test compares against. On a 200-cassette single-session vault the new `BenchmarkSyncSingleSessionVault` full walk drops from 1366 ms / 63,984 allocs to 883 ms / 42,277 allocs (SQLite, arm64); the saving is larger on Postgres, where every removed statement was a round trip. `TestBatchedOverviewMetricRefreshMatchesPerPath` asserts the batched refresh produces the same bucket and member state as the per-path refresh, including the case where a path's index row disappeared, and `TestSyncRefreshesDerivedTablesOncePerSession` covers the walk, the aggregation over several recordings of one session, and the unchanged second walk.
- The recording write path defers both derived read models instead of maintaining them inline, and every reader applies the deferred queue before it answers. `UpsertLogWithGrouping` runs once per completed request: it rebuilt the affected `session_summaries` row and rewrote that path's `overview_metric_bucket_members` and bucket rows inside the write, so a session with N recordings re-aggregated all N rows on every request, and `UpdateLogUsage` paid two extra point queries only to resolve its trace. A write now records the touched path, trace and session in an in-process queue (`markDerivedRefreshForPath`, `markDerivedRefreshForTrace`) and moves on; the queue is applied when it reaches 256 entries, when `Store.FlushDerivedRefresh` is called, at the end of a vault walk, and before every read of the derived tables. `ListSessionPage` and `GetSession` flush first, so the Monitor still observes every write that completed before its request, and the flush coalesces everything accumulated in between into one rebuild per session and one bucket update per hour bucket; `UpdateLogUsage` no longer resolves its trace at all, because the flush does that in one batched query. `serve` flushes on shutdown (after the background sync stops), so a clean stop does not leave the derived rows behind the index rows. On a fixed 500-recording ingest, an interleaved A/B measurement on one machine (minimum of three runs per side, repeated twice) goes from 4.66 s to 2.28 s and from 2.56 s to 1.32 s, about twice as fast, with identical final derived state. The queue is deliberately not persisted: a hard kill drops at most 256 pending refreshes, the affected summaries stay stale until the next write to that session or `db summary rebuild sessions`, and the Monitor's poll is normally the flush point, so the exposure is limited to derived tables that currently have no reader at all. `TestDerivedRefreshIsDeferredAndAppliedOnRead` pins the deferral, the read barrier and the queue bound, and the CLI tests that build drift out of band now flush before they poison the table.
- The monitor request statistics follow the list filter. `Stats` took no filter, so the list handler answered a filtered page with global numbers; it now builds the same where clause the list uses and returns the totals for the selected rows, which `TestStatsFollowsListFilterAndInvalidatesCache` checks against `ListPage` totals for the empty, model, provider, status and combined filters. The aggregate is also cached for 15 seconds per filter and cleared by every log write, so a page turn reuses the numbers while an ingest is reflected immediately.
- `overviewObservation` reads its six counts in one statement instead of four, each field still computed by the scalar subquery that served it before. The measured breakdown of that statement on a 250,000-row `logs` / 200,000-row `trace_observations` table is recorded in the operations guide, including the finding that its unparsed anti-join already runs as a hash anti join and that `NOT IN`, `EXCEPT` and `INTERSECT` rewrites are 1.6x to 9.7x slower, so that count stays as it is rather than gaining a derived counter or a cache at the current 60-second poll cadence.
- The overview timeline reads each `recorded_at` as the driver's value instead of text. The Postgres driver returns a `time.Time` for `timestamptz`, so scanning the column into a string made it format every row to RFC3339 text only for `timeParse` to parse it back; `timeParseValue` now reads the timestamp directly. Over a 2,000-row / 24-bucket window the timeline loop drops from 24.8 ms and 48,367 allocations to 22.3 ms and 46,367 allocations, one allocation per row, and `BenchmarkOverviewTimeline` keeps the loop measured. A row that somehow carries an empty timestamp still fails the call instead of vanishing from the chart.
- The PostgreSQL operations guide now records the current index drift between the SQLite startup schema and the migrated Postgres schema (82 against 83 shapes) instead of the outdated claim that only three tables differ. Every remaining difference is a shape difference without a capability gap, and the same comparison reports no prefix-redundant index. It also records `tracelog_parent_exchange_id` as a removal candidate with the code-side evidence (no raw SQL and no ent predicate filters `logs.parent_exchange_id`) and the `pg_stat_user_indexes.idx_scan = 0` check that has to confirm it on the instance before anything is dropped.
- The batch analyze command resolves its `--request-id` flags in one grouped read. `resolveAnalyzeBatchTraceIDs` called `GetByRequestID` per flag, and the new `GetByRequestIDs` returns the newest row per request id in chunks, ordered so the first row of a request id is the one the per-id read returned; an id without rows stays absent from the map and still fails with `resolve request id %q`, which the command re-reads to keep the original error. `TestGetByRequestIDsReturnsNewestRowPerRequestID` covers the retried request id that carries two rows, a missing id and the empty input.
- The PostgreSQL operations guide records what the five opt-in `logs` partial indexes of `db migrate optimize-indexes` are worth, measured on a 250,000-row / 202 MB table of the same shape: the failure, routing-failure and slow lists drop from 61-81 ms to about 0.5 ms, the recent and session lists barely move, and building the five with a plain `CREATE INDEX` blocks writes for 1.98 s and adds about 28 MB, which is why they stay opt-in. The same measurement rejects adding `logs(ttft_ms)` and `logs(duration_ms)`: the percentile shape reads 7,007 rows in 20 ms over its 24-hour window and the plain column indexes changed neither the plan nor that timing.
- Model discovery writes in grouped statements. `mergeDiscoveredChannelModels` called `UpsertChannelModel` once per discovered model, and that method upserts the channel model row, upserts the catalog entry and reads the row back, so a discovery of 60 models ran about 180 statements, each in its own transaction. The new `UpsertChannelModels` writes the whole discovery in one transaction with grouped upserts (100 models per statement) and one read back per chunk, and the discovery path calls it once. The single-model method and its semantics are unchanged: a model repeated inside one batch collapses to its last occurrence, because one `INSERT` cannot update the same conflict target twice on Postgres, while the returned slice still holds one record per input record; and records without a probe time go into a separate statement, because the single-row upsert left `last_probe_at` out of the statement and therefore kept the stored value on conflict. `BenchmarkUpsertChannelModels`, a 60-model re-discovery on SQLite: 93.29 ms down to 7.02 ms (13.3x, 42480 down to 11628 allocations). `TestUpsertChannelModelsGroupsWritesAndKeepsProbeTimes` covers the stored probe time that must survive, the duplicate collapse, the catalog entries, the input order and the empty and invalid inputs, on both drivers.
- Two more per-item reads are grouped. `GetDatasetExamples` read the trace row of every example, so a dataset detail view issued one query per example; `traceLogsByTraceID` now loads the trace ids of the whole page in chunks and the example whose trace row is gone is still skipped, which is what the single read did with `sql.ErrNoRows`. The batch reanalysis job read the findings of every trace of a session with its own `ListFindings` call; `ListFindingsByTraceIDs` groups them per trace in chunks and the job keeps every trace of the session in its map, so a trace without findings still reports none rather than a missing key. On SQLite with 120 traces, `BenchmarkGetDatasetExamples`-style measurement is not needed: the example page goes from one query per example to one per 900 ids.
- Adding traces to a dataset no longer checks each trace on its own. `AppendDatasetExamples` read the trace row and counted the existing dataset example once per candidate trace, so a batch of N traces issued 2N queries inside its transaction; both checks now run per chunk of ids (`knownTraceIDs`, `datasetExampleTraceIDs`) and the loop reads two maps, with a trace that no longer exists still reporting the error the single-trace read raised. `TestAppendDatasetExamplesRejectsBatchWithUnknownTrace` appends a batch whose unknown trace sits behind one the dataset already holds, and checks that the rejected batch writes nothing.
- The Responses item history is written and read in batches. `EntStore.Put` issued one `INSERT` per input and output item inside its transaction, and `Get`, `InputItems`, `ContinuationItems` and the compact-boundary check read one item row per id; `ContinuationItems` additionally read every response of the chain twice and ran the boundary check item by item. Items now go in with multi-row inserts (200 builders per statement) and come back with one `id IN (...)` query per history, where the caller's own id list keeps the stored order. `BenchmarkEntStoreItemHistoryIO` measures a 120-item history on SQLite: read 11.45 ms down to 2.30 ms (5.0x, 20160 down to 7938 allocations) and write 13.04 ms down to 7.36 ms (1.8x) per history. `TestEntStoreChunksLongItemHistories` covers the chunk boundary and the restored order, and `TestEntStoreDanglingItemReferenceStillFails` keeps the error that a history referencing a deleted item produced, instead of silently dropping the item.
- Listing datasets no longer counts the examples of every dataset with its own query. `ListDatasets` ran a `COUNT(*)` per dataset row, so the dataset picker grew a query per dataset; the counts now come from one `GROUP BY dataset_id` pass over `dataset_examples` and are looked up per row. `TestDatasetRoundTripAndDedupAppend` now covers three datasets with two, one and zero examples, which a single-dataset fixture could not catch: a grouping mistake would hand one dataset its neighbour's count.
- The upstream analytics page no longer ranks four lists per upstream. `ListUpstreamAnalytics` called `upstreamModelCoverage` (two queries), `upstreamRecentErrors` and `upstreamRecentFailures` once per upstream after its grouped pass, so a page listing N upstreams issued 1+4N queries; the model ranking, the latest model, the recent errors and the recent failures are now three grouped queries for the whole page (`upstreamModelCoverageAll`, `upstreamRecentErrorsAll`, `upstreamRecentFailuresAll`), each ranking the per-upstream rows with `ROW_NUMBER() OVER (PARTITION BY selected_upstream_id ...)` so the top-N cut and its order stay in the database collation the per-upstream query used. The single-upstream helpers remain for `GetChannelRecentFailures`. On SQLite with 8 upstreams of 30 requests, `BenchmarkUpstreamAnalytics` reports 7.27 ms per-upstream against 4.96 ms batched (1.5x, 24 queries down to 3); the SQLite win is limited because the ranking work is the same either way, while the round trips drop from 4N to 3. `TestUpstreamAnalyticsBatchMatchesPerUpstreamQueries` compares every batched list with the per-upstream result for three upstreams, one of which has no failures at all, and for a filtered and an unfiltered window.
- Routing reloads no longer read the channel model table once per channel. `RuntimeTargets` called `ListChannelModels(channel.ID)` inside its loop over the enabled channels, and the Monitor runs it inside the configuration transaction that persists every channel, model and alias write, so each reload held the write transaction open for one query per enabled channel. Every channel's models now come back in one query ordered by channel id, the same rows in the same per-channel order, and are grouped in Go. `TestRuntimeTargetsSkipsDisabledChannelsAndModels` now configures two enabled channels with different models and asserts that each runtime target keeps its own list.
- The channel list no longer asks for one usage summary and one trend series per channel. `GET /api/channels` called `GetChannelUsageSummary` and `GetChannelUsageTrends` inside its loop over the configured channels, so a page load issued 3C queries and every one of the trend series re-read the same logs window for a single channel; `GetChannelUsageSummaries` and `GetChannelUsageTrendsBatch` now produce both in one grouped pass each, and the trend pass anchors each channel at its own latest record exactly like the per-channel form, so a series stays identical. A channel with no recorded request at all is absent from the grouped trend map, because the scan cannot know the empty window such a channel has, and the list keeps its own query for that case. On SQLite with 12 channels of 20 requests, `BenchmarkChannelUsageAnalytics` reports 6.35 ms per-channel against 3.53 ms batched (1.8x, 36 queries down to 3); `TestChannelUsageBatchMatchesPerChannelQueries` compares every batched series and summary with the per-channel result, and `TestChannelManagementAPI` covers the channel without rows.
- The model catalog no longer runs one usage aggregate pair per model. `ListModelCatalogAnalytics` asked `usageSummary` for the selected window and for today once per catalog entry, so a catalog with M models issued 2M queries that each aggregated `logs` for a single model; both windows are now one `GROUP BY model` pass (`usageSummariesByModel`) and the per-model records are map lookups. The projection and the row-to-record conversion are shared with the single-key form, so the grouped result is the same aggregate, and a catalog model with no rows in a window gets the zero summary a no-match query returned. On SQLite with 40 models and 8 logs each, `BenchmarkModelCatalogUsageSummaries` reports 6.60 ms/1,680 allocs for the per-key form against 1.32 ms/596 allocs grouped (5.0x); `TestModelCatalogAnalyticsSummarizesEveryModel` pins the two windows, the empty catalog entry and the success/missing-usage counts apart.
- The Overview page no longer scans the same window three times for its summary. `overviewSummary` ran one aggregate pass and then `overviewPercentile` ran its own `COUNT(*)` per percentile only to learn the offset to pick, so a page load filtered the logs window twice more for the two p95 values; the counts are now two more conditional sums in the pass that was already running (`ttft_samples`, `duration_samples`), and the percentile helper takes the sample count instead of measuring it. The two `parse_jobs` queue counters in the observation panel were also two round trips over the same small table and are now one aggregate. `TestOverviewPercentilesIgnoreZeroSamples` pins the sample semantics the summary now owns: rows with a zero metric stay out of the denominator, and a window whose samples are all zero reports a p95 of zero rather than the offset a wrong count would select.

### Fixed

- The Postgres schema was still missing five index shapes that stable queries need. They are now declared in `ent/schema`, added by `20260930130000_add_analytics_indexes` and mirrored by the SQLite startup schema: `logs(selected_upstream_id, recorded_at)`, `request_audits(created_at, id)`, `tool_call_audits(created_at, id)`, `analysis_runs(created_at, id)` and `trace_findings(severity, created_at)`. `selected_upstream_id` was in no index at all while ten queries filter or group by it, which turned each of the 1+4N queries behind the upstream analytics page into a sequential scan of `logs`; on a 200,000-row table of the same shape one upstream's aggregate drops from 41.9 ms (`Parallel Seq Scan`, 200,000 rows) to 10.9 ms (`Bitmap Index Scan`, 25,000 rows), and the 388 MB production table repeats that shape about twenty times per page. The other four cover the unfiltered `created_at`-ordered audit lists and the severity-filtered high-risk findings list, which until now had only trace-id-leading or filtered indexes. All five arrive through `db migrate up`, so `logs` receives an ordinary blocking `CREATE INDEX` at startup; pre-creating it with `CREATE INDEX CONCURRENTLY IF NOT EXISTS` makes the migration a no-op, the pattern the parse-jobs index already documents.

## [2.1.0] - 2026-09-30

### Added

- `cmd/trajecta`, the Trajecta command-line tool (build artifact `trajecta`). It hosts operations that do not need the server process; the first command group, `trajecta upgrade`, performs the rename migration without shell scripting. It loads a pre-rename `.env` and maps `LLM_TRACELAB_*` onto `TRAJECTA_*`, merges the legacy SQLite application databases into Postgres (idempotent: rows whose primary or unique key already exists are skipped, only the column intersection is written, the four historical timestamp encodings are normalized to UTC, identity sequences only ever move forward, and every source primary key is verified present afterwards), rewrites the `# llm-tracelab/v3` cassette prelude magic in parallel (only the first line changes, the recorded payload is copied byte for byte through a temporary file and an atomic rename, and an interrupted run cannot truncate a cassette), validates cassette structure without reading the payload, warns when `docker-compose.yml` still passes legacy variables, and renames the SQLite files to `*.migrated` once every key is verified. The subcommands `env`, `db`, `cassettes`, `sqlite` run one step at a time, every command is a dry run unless `--apply` is passed, and `internal/legacymigrate` owns the logic. `task migrate:legacy` runs the pipeline from source.
- `docs/LEGACY_MIGRATION.md`, the Chinese end-to-end guide for migrating a pre-rename deployment: environment prefix mapping (including the requirement to update `docker-compose.yml`), the SQLite-to-Postgres merge, the optional cassette magic rewrite, structural validation, archiving, the `trajecta upgrade` pipeline, the command and exit-code reference, and troubleshooting entries.
- `trajecta layout apply`, which executes a reviewed `layout plan` move list. Every move renames one cassette and repoints the index rows that store its path in the same transaction: `logs.path` (the trace index primary key), `upstream_exchanges.cassette_path` and `overview_metric_bucket_members.path`. `logs.trace_id` is never rewritten, so parse jobs, observations, findings, analysis jobs and session summaries stay attached to their trace. The whole plan is validated before the first rename (relative paths that cannot escape the root), a move is applied only when the source exists and the target does not, a failed index update moves the file back, and re-running the same plan is safe: files already at their target report `already-applied`, and a half-finished move (file renamed, index not yet committed) is repaired and reported as `resumed`. `--verify-model` (default on) re-reads each prelude and refuses a move whose recorded model changed, `--db-prefix` maps a database path prefix onto a different local root (for example a container that stores `/app/data/traces/...`), `--limit` and `--only-model` stage a run, and `--no-db` moves files without touching the index. Nothing moves without `--apply`.
- `trajecta layout plan`, a read-only report of the cassette directory layout: it reads each prelude (4 KiB, extended to 512 KiB and 8 MiB only when the prelude is longer, because a streaming recording appends one event line per chunk and real preludes reach several hundred kilobytes) and classifies every cassette as already canonical (`<site host>/<model>/YYYY/MM/DD`), missing its site segment, holding only a leading part of the model name, ambiguous (path and recorded model disagree, for example a historical `<site>/<date>` shape) or unreadable. Only the first three classes are planned, recordings without a resolved upstream go to `<unknown-site>/<model>/YYYY/MM/DD` (default segment `unknown-site`), and `--out plan.json|plan.csv` keeps the full move list. Nothing is created, moved or deleted, and no database is touched.
- `--reconcile-derived-trace-ids` for `trajecta upgrade db`, which repairs the derived tables (`trace_observations`, `parse_jobs`, `semantic_nodes`, …) whose `trace_id` still carries the value the legacy index recorded. When the server re-indexes the cassettes before the legacy database is merged, `logs.trace_id` becomes the cassette-derived id while the merge skips the legacy `logs` row (its inserts rely on `ON CONFLICT DO NOTHING`), so those rows no longer join anything. The flag maps the orphan ids through the archived database (`logs.trace_id` -> `logs.path`) and the current index (`logs.path` -> `logs.trace_id`), deletes only the rows whose identity already exists under the mapped id, and moves the remaining rows onto it; a row that references no reachable legacy trace stays exactly as it is and is counted as unresolved, so the last copy of a row is never discarded. It is a dry run unless `--apply` is passed. It only covers the tables whose `trace_id` holds a `logs.trace_id`; `upstream_exchanges.trace_id` is the recorder prelude `meta.request_id`, a different id space, and is never rewritten. With `--data-root` (default: the configured trace directory) it also treats a `logs` row whose cassette moved away while the same recording stayed indexed at a path that exists as a donor: the donor's derived rows move onto the surviving trace id, and with `--prune-superseded-index-rows` and `--apply` the stale index row is deleted once no derived row references it any more. An index row whose cassette exists nowhere is kept and reported, and candidates that are not unique are skipped. The legacy index is read in one sequential pass, which keeps a large archived database off the seek-bound path, and the orphan scan folds the distinct ids before anti-joining them, so the hundred-million-row `semantic_nodes` table is not probed once per row. `--max-samples` controls how many example paths the report lists, so an operator can enumerate every path the repair refused to touch.
- `--tolerate-snapshot-drift` for `trajecta upgrade sqlite archive` and `trajecta upgrade db --verify-only`. The archive gate verifies every legacy primary key before renaming a database file, but `upstream_targets` and `upstream_models` are runtime snapshots that a running server rewrites from the live provider configuration (see `docs/ROUTING_AND_CREDENTIALS.md`), so their legacy rows can be absent from Postgres by design. The flag excuses missing keys in exactly those two tables, keeps every authoritative table strictly verified, and reports the excused count as `tolerated` instead of silently accepting it; without the flag the gate still refuses and now points at the flag.

### Changed

- The server process is now built as `server` and the CLI as `trajecta`. `task build:go` produces both (there is no `trajecta-migrate` artifact any more) and `task clean` removes both. Operator-visible paths change with it: the container entrypoint is `/app/bin/server`, the CLI ships as `/app/bin/trajecta`, and `docker compose exec` / `--entrypoint` invocations must use the new names.
- The application database driver is never chosen implicitly as SQLite any more. An unset `database.driver` now means Postgres everywhere (`config.DatabaseDriver`, the store and auth driver normalizers, and the auth store that `serve` opens), so a configuration that omits the driver fails loudly with a missing-DSN error instead of silently creating a local `trajecta.sqlite3` file next to the cassettes. SQLite stays fully supported for the local dev/test harness and for reading legacy databases during migration, but it must be selected explicitly with `database.driver: "sqlite"`; test configurations that relied on the old implicit default now name the driver.

### Fixed

- `trajecta upgrade db` (and the `sqlite archive` gate) reported legacy rows as missing whenever the running server had already created the same record under a regenerated surrogate primary key. The inserts use `ON CONFLICT DO NOTHING`, so any unique key skips a row, but the verification only compared the Postgres primary key: rows that the server had inserted first (for example the bootstrap `users` row, whose id is 1 while the legacy row's id is 4294967297, or `upstream_models`, whose identity column is regenerated while `(upstream_id, model)` matches) were counted as missing and blocked the archive. Verification now mirrors the copy: it checks the primary key first and then every other full, non-partial unique index, reports secondary-key matches apart as `alt_key_matched` instead of hiding them, and still counts a row as missing when no unique key matches. This is also what reconciles `logs` after `layout apply` rewrote `logs.path`, because the legacy rows only match on the unique `trace_id`.
- `trajecta upgrade db` dropped every legacy row whose text contained a NUL byte. NUL is valid UTF-8, so the existing `utf8.ValidString` guard let it through, and Postgres rejected the batch with `22021 invalid byte sequence for encoding "UTF8": 0x00`. Because a rejected batch is retried row by row, the failures also collapsed the merge from ~960 to ~75 rows/s; on the real vault roughly 0.3% of `semantic_nodes` rows (`text_preview`) carry a NUL, so about 48k rows would have been left out of Postgres and the archive step would then have refused to run. Text conversions now drop NUL bytes exactly like the trace index does, so both write paths agree and the rows merge.
- The Postgres schema never carried a `trace_id`-leading index on `parse_jobs`, although the SQLite startup schema has had `idx_parse_jobs_status_trace` all along and every consumer reaches parse jobs by trace id (the monitor's trace detail, `--reconcile-derived-trace-ids` and the `--prune-superseded-index-rows` guard). Each such lookup scanned the table: a real repair recorded `seq_scan 14,650` and `seq_tup_read 6,580,802,230` on `parse_jobs`, because the prune probe ran once per superseded index row, and that was almost the whole of the step's 11 minutes. `20260929090000_add_parse_jobs_trace_id_index` adds `parsejob_trace_id_status (trace_id, status)` to the ent schema and to Postgres; one probe drops from a 60 MB parallel sequential scan (~58 ms) to a 4-page index-only scan (0.125 ms). The migration uses `CREATE INDEX IF NOT EXISTS` (18 MB, 2.3 s), so an operator can create it on a live instance and let the migration become a no-op.
- `--reconcile-derived-trace-ids` compared the identity columns of a derived row with `IS NOT DISTINCT FROM`, which no btree index can serve: the planner kept only `trace_id` as an index condition and filtered the rest, so every candidate row fetched every row of the mapped trace and threw them away again. On `semantic_nodes` (74M rows) one batch of 200 ids ran for more than 46 minutes, and the `DELETE` uses the same expression, so it was slower still. The identity columns are now read from the catalog together with `attnotnull`, so a NOT NULL column is compared with `=`: the count becomes a two-column index-only probe and the delete an `Index Scan` with `Index Cond: ((trace_id = m.current) AND (node_id = d.node_id))`. A nullable identity column keeps the NULL-safe comparison.
- `semantic_nodes.trace_id` carried a distinct-value estimate of 29,253 for a table of ~74M rows, and that one number decided how long the derived-id repair took. `ANALYZE` cannot correct it: the sample is about 30k rows, so a high-cardinality column cannot show more distinct values than the sample has rows and the estimate always lands near 30k. The true value follows from a strict bound — `logs.trace_id` is unique with 248,164 rows, so at most that many distinct ids in `semantic_nodes` can exist in `logs`, and a `TABLESAMPLE SYSTEM (0.001)` probe found 66 of 77 sampled distinct ids present (86%), giving ~289k distinct ids, about 0.39% of rows. At 29,253 the anti-join's two candidate plans were within 0.04% of each other (6,072,730 against 6,075,165) and the planner picked the `Nested Loop Anti Join`, which probes `logs_trace_id_key` once per distinct id while scanning a 7.4 GB index; the 21 MB `logs` index is evicted from the buffer cache throughout, so every probe became a random disk read, and that run was stopped after 193 minutes with the scan still unfinished. With the estimate corrected the `Merge Anti Join` wins by ~7% serial and ~17% parallel and completed the same scan in 77 minutes. `20260929100000_set_semantic_nodes_trace_id_n_distinct` pins the estimate to a fraction of rows (`n_distinct = -0.004`), so it scales with the table rather than going stale, and the monitor's own plans benefit from the same correction.
- The README badge now reads the repository's tags (`github/v/tag`). The project publishes release tags without GitHub Releases, so the previous `github/v/release` badge rendered "no releases or repo not found", and it now also links to the tag list.
## [2.0.1] - 2026-09-28

Documentation-only release: no runtime, configuration, record-format or API
behaviour changed. The docs tree was re-verified against the code and the
READMEs were rewritten as project landing pages.

### Added

- `CHANGELOG.md`, `CONTRIBUTING.md` and `SECURITY.md` at the repository root, covering the release history, the contribution workflow with the verification commands to run before a pull request, and vulnerability reporting plus the operator notes for cassettes, secrets and exposed ports.
- `task ui:screenshots`, with a Playwright capture config and capture script that regenerate the README screenshots for the Chinese and English Monitor UIs from the offline fixture server.

### Changed

- `README.md` and `README_EN.md` were rewritten as landing pages and kept in sync: positioning, capability table, three quick-start paths (Docker Compose, source, upgrading from llm-tracelab), a five-minute record-and-replay walkthrough, the supported-upstream matrix, an architecture overview with a directory map, the documentation index, contributing and the license.
- The README screenshots were replaced with captures of the current UI (`images/` for the Chinese UI, `images/en/` for English), and four stale screenshots were removed.
- Every document under `docs/` was checked against the code and the claims that had drifted were corrected, including the scope of `debug.mask_key` on-disk redaction, the absence of timing simulation in the replay transport, the two-cassette shape of local Responses recording, the environment and table coverage of the Postgres baseline script, exchange-column nullability, the embeddings endpoint being classified but not routable, and the provider-preset matrix.
- `ent/migrate/README.md` no longer describes SQLite as the default generation workflow; Postgres is the production and tracked default.
- `docs/protocol-reference/README.md` links the four dated upstream schema snapshots directly, and `docs/DEVELOPMENT.md` links the ent migration workflow, the Codex fixture profile and the pull request template.

## [2.0.0] - 2026-09-28

### Added

- `scripts/migrate-to-trajecta.sh`, a migration helper that runs as a dry run by default and rewrites `.env` keys, renames the local SQLite database together with its `-wal`/`-shm` siblings, reports the cassette magic version of each recording, and lists the items that need a manual decision: Postgres database and role names, Docker image and volumes, CI secrets, and browser localStorage.
- Session trajectory export as ATIF JSONL.

### Changed

- The docs tree was consolidated from 43 files into 13 current-fact Chinese documents plus `docs/protocol-reference/`: `IMPLEMENTATION_STATUS.md`, `PROTOCOLS_AND_PROVIDERS.md`, `ROUTING_AND_CREDENTIALS.md`, `RESPONSES_RUNTIME.md`, `OBSERVATION_AND_AUDIT.md`, `STORAGE_AND_DEPLOYMENT.md`, `DEVELOPMENT.md` and `POSTGRES_OPERATIONS.md` were added, `ARCHITECTURE.md`, `MONITOR_GUIDE.md`, `MCP_GUIDE.md` and `PROXY_USAGE_EXAMPLES.md` were rewritten against the current UI routes, MCP tools and proxy entrypoints, and every roadmap, phase, milestone and progress claim was dropped in favour of current code facts.
- The embedded Monitor UI bundle was rebuilt from the renamed sources.
- Documentation corrected against the code: `db migrate down` and `auth migrate down` exit with usage code 3, `AutoMigrate: false` applies to Postgres only, the exact `analyze backfill-exchanges` column set, the written `exchange_kind`/`exchange_role` value sets, and the stable channel id rule.
- YAML keys that no config field reads are now warned about at startup, and unknown `limits.scope` values plus a header scope without `limits.channel_key_header` are rejected at load time.

### Fixed

- Compact ATIF v1.8 session trajectories are reconstructed correctly.
- Upstream and model routing switches are authoritative.
- Configuration-consistency gaps: every `Store` view of one database shares one state block, nested `ConfigurationTransaction` calls are rejected instead of deadlocking on the configuration lock, an upstream write lock stops a background refresh from writing rows for a deleted target, the routing snapshot is published only after the database transaction commits, and `GET`/`DELETE /api/settings/channels` expose and clear the `channels.initialized` bootstrap marker.
- `ensureBootstrapUpstream` wrote the rejected `routing_profile: openai`, so `LLM_TRACELAB_BOOTSTRAP_UPSTREAM_BASE_URL` and its API key produced a config that failed to resolve; it now writes `openai_default`.
- Trace detail read `prompt_token_details` while the record tag is `prompt_tokens_details`, so cached-token details were always empty.
- A channel model capability PATCH now distinguishes an absent key from an explicit `null`, so "inherit" clears a pinned value again.

### Removed

- Stale code and dead configuration found during an audit: zero-reference exports and constants across `internal/*` and `pkg/*`, unwritten struct fields, the dead `POST /api/router/reload` route and handler, a zero-reference frontend component, dead `apiPaths`, unused i18n keys, and the removed `startup_policy` and `responses_server.enabled` keys from configs, examples and docs.
- 40 obsolete documentation files, including the v1 documentation tree, the design notes and roadmaps, duplicate baselines, `.gitattributes` and a one-off audit report; `.codex/config.toml`, which held an internal MCP address, was untracked and deleted.

### Breaking

- The project was renamed from llm-tracelab to Trajecta. Forwarding, recording, replay, the Responses runtime, Monitor and audit behaviour are unchanged; naming is the only breaking change.
- Environment variables now use the `TRAJECTA_*` prefix. The `LLM_TRACELAB_*` prefix is no longer read, so existing deployments must rename their variables.
- The Go module path changed from `github.com/kingfs/llm-tracelab` to `github.com/kingfs/Trajecta`; importers must update their import paths.
- Binary, Docker image and Compose identifiers are now `trajecta`, `kingfs/trajecta`, and the `trajecta` service and volume.
- Cassette writers now emit the prelude magic `# trajecta/v3`. Readers still accept the legacy `# llm-tracelab/v3` and the older `LLM_PROXY_V2` format, and `LLM_PROXY_V3` remains the deliberate, stable format version identifier. Cassettes are never rewritten by default.
- The local SQLite database default is now `trajecta.sqlite3`; a lone legacy `llm_tracelab.sqlite3` is reused in place instead of starting an empty database.
- Monitor localStorage keys moved to `trajecta.monitor.*`.

## [1.2.5] - 2026-09-14

### Added

- UI-managed gateway routing: model alias persistence, generic application-settings JSON helpers, Monitor routing configuration APIs, a gateway route-planning core, route-plan events recorded in cassettes and shown in trace detail, and a per-model routing simulator on the Monitor Routing page.
- Postgres runtime store support with a Postgres operations runbook, baseline SQL and migration checks, a baseline collection script, and a non-transactional index optimizer command.
- Session summaries with a store cache, a Postgres migration and operations controls, plus overview metric buckets and cursor pagination for system events.
- Support for MCP protocol version 2026-07-28.
- Model alias validation in Monitor and alias rewriting of upstream model names.

### Changed

- Native-vs-local Responses resolution is now per model: an explicit `channel_models.supports_responses`/`supports_chat_completions` value (or `upstream.model_capabilities` in YAML) overrides the channel-level `api_type` and capabilities, while an unset value keeps channel-level behaviour.
- The `responses_server.enabled` switch and the `LLM_TRACELAB_RESPONSES_ENABLED` variable were removed: the local Responses execution mode is always available, and the runtime is built lazily on first use so an optional provider configuration failure surfaces as a 502 on that request instead of blocking startup.
- The default Responses routing strategy is `auto` (native pass-through preferred, local translation as fallback) instead of always preferring the local Responses server; each strategy is described on the Routing page.
- Store query hot paths were optimized.

### Fixed

- Serve keeps booting when no local Responses chat-completions backend is configured; the preflight check is now a warning instead of a fatal error, so the management UI stays reachable.
- Monitor renders the per-model capability columns as tri-state inherit/supported/unsupported selects and sends `null` for "inherit", so saving an unrelated field no longer pins the columns to `true` and overrides channel-level capabilities.
- The Postgres model-aliases migration and the native Responses routing fallback were fixed, and a DeepSeek root base URL is accepted again.

## [1.2.4] - 2026-06-26

### Changed

- The Monitor distinguishes providers explicitly in its model and channel views.

### Fixed

- Route selection for "all" in the analyze refresh, and analyze parallelism.
- Model catalog routing visibility.

## [1.2.3] - 2026-06-25

### Fixed

- Migration errors.
- The embedded Postgres auth migration is skipped on `serve`, so serving no longer runs it accidentally.
- A legacy configuration that lacks an LLM API key is accepted again.

## [1.2.2] - 2026-06-25

### Added

- Model detail metadata endpoints.

### Changed

- Monitor authentication uses JWT.
- The experimental PP/TG performance-rate columns, the aggregate rate hardening and the tiny-window suppression were reverted before release, so the request list keeps its previous behaviour.

## [1.2.1] - 2026-06-25

### Added

- Batch analysis as a CLI command, with a simplified analysis refresh workflow.
- Client traces are separated from internal model exchanges; upstream model calls are shown in Monitor and Responses server entries link to the model exchange they produced.

### Fixed

- Trace detail refresh and duplicated stream output.
- Monitor detail layout and performance tab; detail pages are full width and the event inbox and trace readability were improved.
- Observation parsing handles SSE responses, empty responses, non-LLM HTTP error responses and interrupted streams.
- `all` is treated as a wildcard for system event filters.
- Postgres log exchange column migration, and Codex session headers are preserved by the Responses server.

## [1.2.0] - 2026-06-24

### Added

- Exchange metadata: a storage foundation, recording of entry and model exchanges, classification of Responses model exchange roles, an audit exchange query model, exposure through Monitor and MCP, an exchange metadata backfill command and exchange-aware observation parsing scoped by exchange kind.
- Hosted tools for Codex: a hosted tool contract package, a mock MCP hosted executor, MCP hosted tool configuration and diagnostics, streamable HTTP MCP executor support, web search routed through the hosted registry, and Codex hosted tool request normalization with compatibility injection.
- A Codex model profile editor in Monitor and enriched `models codex-profile` output.
- Monitor i18n preferences and an MCP descriptor streaming path.

### Fixed

- Responses server upstream time-to-first-token is recorded.
- Trace-derived model channel management.
- Monitor login and search Compose stability, monitor provider UI and observation UTF-8 handling.

### Removed

- Obsolete Responses runtime helpers.

## [1.1.0] - 2026-06-23

### Added

- A local Responses runtime: semantic runtime skeleton, HTTP API handler and chat-completions client, incremental and deferred SSE streaming with a rich stream lifecycle, threshold-based auto compaction with compact v2 provenance metadata, and model-profile token budgeting.
- Server-side function executors: a registry, streamed function call arguments, external command execution with process isolation and sandbox constraints, safe executor configuration snapshots with persistent hot reload, and a hosted web search tool loop.
- Responses audit: audit schema, query service, CLI, Monitor API and trace views, tool call audit persistence with lifecycle queries, rejected hosted tool choices, and correlation of Responses upstream exchanges.
- Provider setup tooling: probe CLI, reports and batch apply (CLI and Monitor UI), a probe suggestion library, provider profile adoption diagnostics and dry-run reports, provider detection before Monitor creation, an opt-in startup probe fill, provider tokenize counters with auto-selection, and the upstream API surface in channel configuration.
- Doctor diagnostics: a provider probe, model catalog drift, Codex local-config drift, Responses store health, `config inspect` with a source summary, and application/auth migration status reporting.
- Postgres store support: versioned SQL migrations for the app and auth namespaces with status, rollback and generation paths, runtime store tables, an application store open separated from migrations, and raw SQL compatibility auditing.
- Docker Compose packaging for a Postgres-first gateway.

### Changed

- Upstream capabilities are centralized and enforced for chat routing, and the tool-calling capability is respected when routing.
- The Responses runtime applies the profile's upstream model, and rejected hosted tool audits are persisted.

### Fixed

- Unsupported hosted response tools are rejected instead of being forwarded.
- The Responses gateway Compose smoke path was hardened, the default gateway config is inspectable, manual provider setup no longer requires validation; a missing or wrong Responses chat backend fails startup validation while `responses_server.enabled` is on.
- Docker build proxy environment handling was normalized.

## [1.0.8] - 2026-06-16

### Fixed

- Tokenization requests are routed by model.
- vLLM tokenization is routed to the provider's root endpoints.

## [1.0.7] - 2026-06-16

### Added

- vLLM tokenizer endpoints are proxied.
- Aggregated model metadata is enriched.

### Fixed

- Tokenizer upstream path rewriting.

## [1.0.6] - 2026-06-12

### Changed

- Monitor UI and backend enhancements.

## [1.0.5] - 2026-06-05

### Added

- Provider entrypoints and build metadata.
- A synthetic Anthropic count-tokens fallback.
- Recognition of Claude Code sessions.
- Restructured current documentation and a current protocol reference.

### Fixed

- Router protocol family path matching.

## [1.0.4] - 2026-06-01

### Added

- Credential-aware routing: upstream credential config projection, route targets expanded by credential, routing reads grouped by credential fields, credential routing event fields, and Monitor display of credential routing.
- Scoped local limit keys with rejection events, and in-memory sticky routing with an MCP drilldown.
- Routing decision timeline recording, exposure through MCP and trace detail, Monitor routing aggregation, and failure clustering by routing event.

### Fixed

- Routing candidate URLs are redacted.
- Fallback routing selection is deterministic.
- Merged credential helper dead code was removed.

## [1.0.3] - 2026-05-19

### Added

- Unparsed traces are surfaced in Monitor, and traces can be filtered by observation status in Monitor and MCP.
- Documentation of the unparsed trace reanalysis workflow.

## [1.0.2] - 2026-05-19

### Added

- Model-scoped upstream health, upstream retry jitter and observability, and health checks tied to router recovery.
- Retry queue saturation is exposed in failure classification.

### Fixed

- The upstream retry wait queue is bounded, and upstream retry, refresh and probe concurrency and resilience were improved.

## [1.0.1] - 2026-05-15

### Added

- Reanalysis: a job foundation, batch reanalysis jobs, Monitor controls and API, usage repair from recorded cassettes, and workflow documentation.
- A system events centre: store, emission from failures, Monitor API and UI with streaming updates, and MCP tools.

### Fixed

- Usage is parsed from long stream events.
- Model event listing.
- Semantic nodes are deduplicated before persistence.

## [1.0.0] - 2026-05-14

### Added

- An observation pipeline: Observation IR, OpenAI, Claude and Gemini parsers, OpenAI streaming observation parsing, persisted trace observations with an async parser worker, a protocol observation API, and audit findings exposed through the API.
- A Monitor redesign: new shell navigation, redrawn overview, trace, session, audit and analysis pages, an overview API with p95 latency and observation health, breakdown drilldowns, a performance view and session analysis runs, plus an account menu and theme switcher.
- Channel management: configuration storage, a channel model probe service, Monitor APIs with a web editing UI (header keep semantics, modal edit form, advanced provider options), manual model management, batch model toggles, local secret key storage with rotation and backup, routing decision filters, and channel and model catalog analytics APIs.
- An auth token inventory page with list and revoke APIs, and encryption of local channel secrets.
- Quality gates: embedded-UI smoke tests, a browser smoke suite, a full quality gate and legacy V2 replay coverage.

### Changed

- Serve uses the channel store as the router configuration source and reloads the router when channels change.

### Fixed

- Non-stream provider errors are parsed correctly.
- Serve background workers are awaited, model marketplace analytics were refined, and token form overlap was fixed.
- The full quality gate passes.

## [0.10.0] - 2026-05-12

### Added

- Throughput and cache-hit rate computation.
- AI-native CLI contracts.

### Changed

- Monitor latency and session failure display.

### Fixed

- Model routing selection errors.

## [0.9.1] - 2026-04-28

### Changed

- The server CLI was split into separate command files and aligned with Cobra/Viper, using Cobra flags.
- golangci-lint checks are enforced.

### Removed

- Unused MCP server code.

## [0.9.0] - 2026-04-27

### Added

- A user-backed auth control plane, with user tokens used for all service authentication.

### Changed

- The structured database schema was unified and the store read and write paths were migrated to ent.
- Docker runtime storage configuration, local configuration for the multi-upstream runtime, and service defaults were aligned; legacy trace indexes are adopted during database migrations, relative SQLite database paths are handled, and monitor list page display was accelerated.

### Fixed

- Auth token and SQLite DSN handling were hardened.
- Duplicate trace detail tabs were removed.

## [0.8.0] - 2026-04-27

### Added

- A read-only MCP server over streamable HTTP with auth tokens and a narrowed tool surface: replay tools, dataset curation, persisted experiment runs with baselines, scores and comparison, versioned evaluator profiles, deterministic budget evaluators, a tool-call conformance evaluator, summaries and explanations, dataset creation from regressions, and trace failure clustering.
- Model list aggregation and routing across upstreams, and token access control.
- Core performance benchmarks and a development command matrix.

### Changed

- Monitor was split into dedicated routes with shared components and primitives, unified empty states and a unified visual hierarchy.
- Recorder and replay performance: prelude parse allocations reduced, replay cassette response offsets cached, and the request body reused across the proxy pipeline.
- SQLite store configuration was hardened, Docker build environment handling was unified, and session grouping is persisted and backfilled for stale rows.

### Fixed

- Monitor trace list spacing and tool definitions, and acceptance review findings.

## [0.7.0] - 2026-04-17

### Added

- Multi-upstream routing with cost-aware upstream selection, upstream monitoring and refresh, filtered upstream analytics views and drilldowns.
- Routing diagnostics: routing failure analytics, timeline and trend detail, upstream failure reason classification, recording of routing selection failures, per-trace routing decision explanations, and upstream health signals, thresholds and detail drilldowns.

### Fixed

- `allow_static` fallback routing is covered by tests.

## [0.6.0] - 2026-04-17

### Added

- Provider support: Anthropic upstream protocol support, Google GenAI upstream and stream support, and a Vertex native upstream with a `generateContent` adapter and a controlled preset.
- An expanded upstream preset registry with routing profiles, preset validation and end-to-end coverage, plus cassette-backed model listing.
- Startup upstream routing diagnostics.
- Session-aware Monitor views: a session timeline overview, failure summaries and context, deep links between trace tabs, sessions and focused timeline/response panels, and Monitor list filters.

### Changed

- The cassette fixture catalog was refactored and expanded with history, block, refusal, error, safety, stream-error, partial-completion, multi-turn and tool-result matrices.

### Fixed

- Responses non-stream and streamed refusal semantics, and preservation of custom tool call arguments.
- Google finish reason semantics and the Vertex connectivity diagnostics path.
- Legacy store schema upgrades for session columns, and OpenAI-compatible `base_url` prefixes are required.

## [0.5.0] - 2026-04-01

### Changed

- The front end was refactored, with reworked timeline and summary display.

### Fixed

- A build error and front-end display issues.
- Unnecessary environment variables were removed.

## [0.4.0] - 2026-03-30

### Fixed

- Display issues, including OpenClaw messages that could not be rendered.

## [0.3.0] - 2026-03-27

### Added

- `/v1/responses` support.
- Claude API content display, including cached tokens, and proxy support.

### Changed

- Tooling and the default configuration and ignore rules were adjusted.

### Fixed

- Claude cached-token accounting errors.
- Ordering/timing issues, statistics errors and unfriendly configuration.

## [0.2.0] - 2026-01-30

### Added

- Message format conversion between three providers, including mutual conversion.
- Reduced memory copying.
- A run screenshot in the documentation.

### Fixed

- Test loading.

## [0.1.0] - 2026-01-01

### Added

- The initial project commit.
- A GitHub Actions workflow and a Docker image build script.

### Changed

- The project was named llm-tracelab.

[Unreleased]: https://github.com/kingfs/Trajecta/compare/v2.1.1...HEAD
[2.1.1]: https://github.com/kingfs/Trajecta/compare/v2.1.0...v2.1.1
[2.1.0]: https://github.com/kingfs/Trajecta/compare/v2.0.1...v2.1.0
[2.0.1]: https://github.com/kingfs/Trajecta/compare/v2.0.0...v2.0.1
[2.0.0]: https://github.com/kingfs/Trajecta/compare/v1.2.5...v2.0.0
[1.2.5]: https://github.com/kingfs/Trajecta/compare/v1.2.4...v1.2.5
[1.2.4]: https://github.com/kingfs/Trajecta/compare/v1.2.3...v1.2.4
[1.2.3]: https://github.com/kingfs/Trajecta/compare/v1.2.2...v1.2.3
[1.2.2]: https://github.com/kingfs/Trajecta/compare/v1.2.1...v1.2.2
[1.2.1]: https://github.com/kingfs/Trajecta/compare/v1.2.0...v1.2.1
[1.2.0]: https://github.com/kingfs/Trajecta/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/kingfs/Trajecta/compare/v1.0.8...v1.1.0
[1.0.8]: https://github.com/kingfs/Trajecta/compare/v1.0.7...v1.0.8
[1.0.7]: https://github.com/kingfs/Trajecta/compare/v1.0.6...v1.0.7
[1.0.6]: https://github.com/kingfs/Trajecta/compare/v1.0.5...v1.0.6
[1.0.5]: https://github.com/kingfs/Trajecta/compare/v1.0.4...v1.0.5
[1.0.4]: https://github.com/kingfs/Trajecta/compare/v1.0.3...v1.0.4
[1.0.3]: https://github.com/kingfs/Trajecta/compare/v1.0.2...v1.0.3
[1.0.2]: https://github.com/kingfs/Trajecta/compare/v1.0.1...v1.0.2
[1.0.1]: https://github.com/kingfs/Trajecta/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/kingfs/Trajecta/compare/v0.10.0...v1.0.0
[0.10.0]: https://github.com/kingfs/Trajecta/compare/v0.9.1...v0.10.0
[0.9.1]: https://github.com/kingfs/Trajecta/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/kingfs/Trajecta/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/kingfs/Trajecta/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/kingfs/Trajecta/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/kingfs/Trajecta/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/kingfs/Trajecta/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/kingfs/Trajecta/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/kingfs/Trajecta/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/kingfs/Trajecta/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/kingfs/Trajecta/releases/tag/v0.1.0
