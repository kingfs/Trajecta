DROP INDEX IF EXISTS "upstreamexchange_request_id";
ALTER TABLE "upstream_exchanges" DROP COLUMN IF EXISTS "request_id";
