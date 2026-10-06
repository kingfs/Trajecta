-- collapse the historical duplicate rows written by both the recorder enqueue
-- path and the observation result path, keeping the newest row per trace
DELETE FROM "parse_jobs"
WHERE "id" NOT IN (SELECT MAX("id") FROM "parse_jobs" GROUP BY "trace_id");
-- one parse job per trace
CREATE UNIQUE INDEX IF NOT EXISTS "parsejob_trace_id_unique" ON "parse_jobs" ("trace_id");
