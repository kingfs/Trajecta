-- The routing summary aggregated over every trace in its window by opening the
-- cassette of each one and parsing the prelude events, one random read per
-- trace. On the deployment's rotational disk that measured 57 ms per trace, so
-- the default "today" window took 49 s.
--
-- These five columns mirror the `routing.selected` / `routing.sticky.*` prelude
-- events the summary aggregates, so it becomes a single GROUP BY over logs. The
-- events remain the durable record; the columns are an index, and a cassette
-- re-index derives them with the same helper the write path uses.
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "route_target_id" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "channel_id" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "credential_id" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "sticky_status" character varying NOT NULL DEFAULT '';
ALTER TABLE "logs" ADD COLUMN IF NOT EXISTS "sticky_previous_upstream_id" character varying NOT NULL DEFAULT '';

-- Every routing summary groups by the window first, sometimes narrowed to one
-- model, so both shapes are covered.
CREATE INDEX IF NOT EXISTS "tracelog_recorded_at_sticky" ON "logs" ("recorded_at", "sticky_status");
