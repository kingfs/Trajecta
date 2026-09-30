-- create index "tracelog_selected_upstream_id_recorded_at" to table: "logs"
CREATE INDEX IF NOT EXISTS "tracelog_selected_upstream_id_recorded_at" ON "logs" ("selected_upstream_id", "recorded_at");
-- create index "requestaudit_created_at_id" to table: "request_audits"
CREATE INDEX IF NOT EXISTS "requestaudit_created_at_id" ON "request_audits" ("created_at", "id");
-- create index "toolcallaudit_created_at_id" to table: "tool_call_audits"
CREATE INDEX IF NOT EXISTS "toolcallaudit_created_at_id" ON "tool_call_audits" ("created_at", "id");
-- create index "analysisrun_created_at_id" to table: "analysis_runs"
CREATE INDEX IF NOT EXISTS "analysisrun_created_at_id" ON "analysis_runs" ("created_at", "id");
-- create index "tracefinding_severity_created_at" to table: "trace_findings"
CREATE INDEX IF NOT EXISTS "tracefinding_severity_created_at" ON "trace_findings" ("severity", "created_at");
