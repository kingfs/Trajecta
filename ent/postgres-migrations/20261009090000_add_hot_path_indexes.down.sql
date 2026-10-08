DROP INDEX IF EXISTS "tracefinding_severity_created_at_id";
CREATE INDEX IF NOT EXISTS "tracefinding_severity_created_at" ON "trace_findings" ("severity", "created_at");
DROP INDEX IF EXISTS "analysisjob_created_at_id";
DROP INDEX IF EXISTS "tracefinding_created_at_id";

CREATE INDEX IF NOT EXISTS "tracelog_request_audit_id_recorded_at" ON "logs" ("request_audit_id", "recorded_at");
CREATE INDEX IF NOT EXISTS "tracelog_exchange_kind_recorded_at" ON "logs" ("exchange_kind", "recorded_at");
