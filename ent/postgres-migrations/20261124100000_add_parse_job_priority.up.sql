-- Parse jobs gain a lane. Fresh traffic and historical re-derivation used to
-- share one FIFO queue, so a project backfill of a hundred thousand traces put
-- every new trace behind it. The two lanes are claimed separately, fresh first.
ALTER TABLE "parse_jobs" ADD COLUMN IF NOT EXISTS "priority" character varying NOT NULL DEFAULT 'fresh';

-- The claim statement filters on (status, priority) and orders by updated_at.
CREATE INDEX IF NOT EXISTS "parsejob_priority_status_updated_at"
	ON "parse_jobs" ("priority", "status", "updated_at");
