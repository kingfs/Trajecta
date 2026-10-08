-- The findings list and the analysis job list both order by
-- "created_at DESC, id DESC" and usually carry no equality filter, and neither
-- table had an index on that pair. Every page therefore sorted the whole table:
-- 341,161 findings in 272 MB and 84,500 analysis jobs in 58 MB, on a rotational
-- disk with a 128 MB shared_buffers.
CREATE INDEX IF NOT EXISTS "tracefinding_created_at_id" ON "trace_findings" ("created_at" DESC, "id" DESC);
CREATE INDEX IF NOT EXISTS "analysisjob_created_at_id" ON "analysis_jobs" ("created_at" DESC, "id" DESC);

-- The Overview's high-risk panel asks for the newest critical findings and the
-- newest high findings across every trace. It filtered `severity IN
-- ('critical', 'high')` and ordered by `CASE severity WHEN 'critical' THEN 0 ...
-- END, created_at DESC`, and a CASE expression cannot drive an index, so it read
-- all 272 MB and sorted it. The query is now a union of two single-severity
-- lookups, and this index serves each of them directly.
--
-- It replaces "tracefinding_severity_created_at" on (severity, created_at)
-- ascending, which the CASE also made unusable: pg_stat_user_indexes reported
-- idx_scan = 0 for it over the whole statistics window.
CREATE INDEX IF NOT EXISTS "tracefinding_severity_created_at_id" ON "trace_findings" ("severity", "created_at" DESC, "id" DESC);
DROP INDEX IF EXISTS "tracefinding_severity_created_at";

-- Both of these were never scanned either. `logs` is the hottest write path in
-- the process, so an index nobody reads is pure write amplification: 15 MB each,
-- maintained by every upsert.
DROP INDEX IF EXISTS "tracelog_request_audit_id_recorded_at";
DROP INDEX IF EXISTS "tracelog_exchange_kind_recorded_at";
