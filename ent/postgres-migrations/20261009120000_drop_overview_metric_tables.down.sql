-- Recreate the two review-aggregate tables, empty and identical to the shape
-- 20260703110000_add_overview_metric_buckets created.
--
-- The rows are not restored: the drop is not reversible in data, only in schema,
-- and the content is recomputable from `logs` in any case. A binary rolled back to
-- a release that still writes these tables can refill them.
CREATE TABLE IF NOT EXISTS "overview_metric_buckets" (
	"bucket_start" timestamptz NOT NULL,
	"bucket_size_seconds" bigint NOT NULL,
	"request_count" bigint NOT NULL DEFAULT 0,
	"success_request" bigint NOT NULL DEFAULT 0,
	"failed_request" bigint NOT NULL DEFAULT 0,
	"total_tokens" bigint NOT NULL DEFAULT 0,
	"ttft_sum" bigint NOT NULL DEFAULT 0,
	"ttft_count" bigint NOT NULL DEFAULT 0,
	"duration_sum" bigint NOT NULL DEFAULT 0,
	"duration_count" bigint NOT NULL DEFAULT 0,
	"stream_count" bigint NOT NULL DEFAULT 0,
	"updated_at" timestamptz NOT NULL,
	PRIMARY KEY ("bucket_start", "bucket_size_seconds")
);

CREATE INDEX IF NOT EXISTS "idx_overview_metric_buckets_start" ON "overview_metric_buckets" ("bucket_start");

CREATE TABLE IF NOT EXISTS "overview_metric_bucket_members" (
	"path" character varying NOT NULL,
	"bucket_start" timestamptz NOT NULL,
	"bucket_size_seconds" bigint NOT NULL,
	"request_count" bigint NOT NULL DEFAULT 0,
	"success_request" bigint NOT NULL DEFAULT 0,
	"failed_request" bigint NOT NULL DEFAULT 0,
	"total_tokens" bigint NOT NULL DEFAULT 0,
	"ttft_sum" bigint NOT NULL DEFAULT 0,
	"ttft_count" bigint NOT NULL DEFAULT 0,
	"duration_sum" bigint NOT NULL DEFAULT 0,
	"duration_count" bigint NOT NULL DEFAULT 0,
	"stream_count" bigint NOT NULL DEFAULT 0,
	"updated_at" timestamptz NOT NULL,
	PRIMARY KEY ("path")
);

CREATE INDEX IF NOT EXISTS "idx_overview_metric_bucket_members_bucket" ON "overview_metric_bucket_members" ("bucket_start", "bucket_size_seconds");
