-- The client harness classification is derived from the request headers and body
-- at record time and written as one `harness.detected` prelude event. These six
-- columns mirror that event into the indexed row, so the Monitor list and its
-- harness filter do not have to open every cassette. The event remains the
-- durable record; a cassette re-index derives the columns with the same helper
-- the write path uses.
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "client_harness" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "client_harness_name" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "client_harness_version" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "client_harness_kind" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "client_harness_confidence" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "client_harness_tags_json" character varying NOT NULL DEFAULT '[]';

-- No index is added for these columns. The only predicate on them is the list's
-- exact-match harness filter, which the `recorded_at` window already narrows:
-- "tracelog_recorded_at" on ("recorded_at") serves the list, and the planner
-- would still have to read the matching rows to sort them. On "logs", the
-- hottest write path in the process, an index nobody reads is pure write
-- amplification, the same reason migration 20261008120000_add_log_routing_detail
-- added no index for its columns.
