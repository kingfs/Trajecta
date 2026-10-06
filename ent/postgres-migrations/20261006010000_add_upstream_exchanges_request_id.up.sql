-- Add the accurately named request id column. `trace_id` predates it and holds
-- the same value (the recorder prelude meta.request_id, equal to
-- logs.request_id); it stays for backward compatibility.
ALTER TABLE "upstream_exchanges" ADD COLUMN IF NOT EXISTS "request_id" character varying NULL;
UPDATE "upstream_exchanges" SET "request_id" = "trace_id" WHERE "request_id" IS NULL AND "trace_id" IS NOT NULL;
CREATE INDEX IF NOT EXISTS "upstreamexchange_request_id" ON "upstream_exchanges" ("request_id");
