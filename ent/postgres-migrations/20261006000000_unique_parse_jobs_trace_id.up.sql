-- collapse the historical duplicate rows written by both the recorder enqueue
-- path and the observation result path, keeping the newest row per trace.
--
-- `NOT IN (SELECT MAX(id) ...)` is deliberately not used here. PostgreSQL cannot
-- turn `NOT IN` into a hash anti join - it has to preserve NULL semantics that an
-- anti join does not - so it re-evaluates the subquery once per outer row. On a
-- 419,160-row parse_jobs with 239,318 distinct trace ids that plan costed
-- 11312529913 and held one backend at a full core for the better part of an hour
-- while the startup migration waited on it: the 221,353-row subquery result did
-- not fit in work_mem, was materialised to a 12 MB temp file, and was rescanned
-- once for every row of the table.
--
-- `NOT EXISTS` over the MAX(id) per trace plans as a Hash Right Anti Join (cost
-- 79676 on that same table). `id` is the primary key, so the row to keep is
-- identified by its id alone and no trace_id join is needed. This is
-- semantically identical: MAX(id) over a non-empty group is never NULL, so the
-- NULL-semantics gap between `NOT IN` and `NOT EXISTS` cannot change the result.
DELETE FROM "parse_jobs" AS p
WHERE NOT EXISTS (
	SELECT 1
	FROM (
		SELECT MAX("id") AS "keep_id"
		FROM "parse_jobs"
		GROUP BY "trace_id"
	) AS "kept"
	WHERE "kept"."keep_id" = p."id"
);
-- one parse job per trace
CREATE UNIQUE INDEX IF NOT EXISTS "parsejob_trace_id_unique" ON "parse_jobs" ("trace_id");
