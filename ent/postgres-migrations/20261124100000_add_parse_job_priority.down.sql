DROP INDEX IF EXISTS "parsejob_priority_status_updated_at";
ALTER TABLE "parse_jobs" DROP COLUMN IF EXISTS "priority";
