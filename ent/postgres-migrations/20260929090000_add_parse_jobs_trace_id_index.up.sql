-- create index "parsejob_trace_id_status" to table: "parse_jobs"
CREATE INDEX IF NOT EXISTS "parsejob_trace_id_status" ON "parse_jobs" ("trace_id", "status");
