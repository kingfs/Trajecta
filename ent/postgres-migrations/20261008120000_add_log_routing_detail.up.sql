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

-- No index is added for these columns. The summary's only predicate is the
-- window, and "tracelog_recorded_at" on ("recorded_at") already serves it with a
-- bitmap index scan; the summary's plan was checked against the live database and
-- picks that index. A second index on ("recorded_at", "sticky_status") is strictly
-- wider, so the planner can never prefer it, and "sticky_status" is not a
-- predicate anywhere in the codebase - the one sticky-oriented reader, the MCP
-- query_sticky_routing tool, parses the cassette prelude and filters in Go. On
-- "logs", the hottest write path in the process, an index nobody reads is pure
-- write amplification: this same release drops three indexes for having
-- idx_scan = 0, two of them on this table.
