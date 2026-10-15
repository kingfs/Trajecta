-- One row per credential the proxy replaced with a format-preserving stand-in,
-- carrying both values so a content audit can show what stood in for what.
--
-- Why the pair is not in the cassette prelude: the prelude is copied into every
-- trace index, served by the monitor and exported, so a value written there
-- becomes a second, less protected copy of the secret that no rotation can
-- reach. The prelude keeps naming the rule, the offsets, the score and the
-- confidence - `secret.masked` - and this table is where an operator reads the
-- value. The cassette remains the source of truth for the exchange; this is an
-- index over it, which is why a trace re-index upserts on
-- ("trace_id", "event_index") instead of appending.
--
-- "original_value" and "stand_in_value" hold ciphertext when the local secret
-- key under the output directory is configured, and plaintext when it is not.
-- "storage_mode" records which, per row, so a reader never has to guess; the
-- same mode is reported by the monitor's configuration endpoint. An operator who
-- keeps the key next to the database has not gained much, but the choice is
-- explicit rather than implied.
CREATE TABLE "credential_mask_audits" (
  "id" character varying NOT NULL,
  "trace_id" character varying NOT NULL DEFAULT '',
  "event_index" bigint NOT NULL DEFAULT 0,
  "rule" character varying NOT NULL DEFAULT '',
  "vendor" character varying NOT NULL DEFAULT '',
  "kind" character varying NOT NULL DEFAULT '',
  "confidence" character varying NOT NULL DEFAULT '',
  "score" bigint NOT NULL DEFAULT 0,
  "reasons" character varying NOT NULL DEFAULT '',
  "start_offset" bigint NOT NULL DEFAULT 0,
  "end_offset" bigint NOT NULL DEFAULT 0,
  "value_length" bigint NOT NULL DEFAULT 0,
  "original_value" character varying NOT NULL DEFAULT '',
  "stand_in_value" character varying NOT NULL DEFAULT '',
  "storage_mode" character varying NOT NULL DEFAULT '',
  "detected_at" timestamptz NOT NULL,
  "created_at" timestamptz NOT NULL,
  PRIMARY KEY ("id")
);

-- A stable ("trace_id", "event_index") is what makes the write an upsert: the
-- event_index is the position of the event in the prelude the cassette recorded,
-- so re-indexing the same file lands on the same rows. It also serves the
-- per-trace lookup, which is why there is no separate ("trace_id", ...) index.
CREATE UNIQUE INDEX "credentialmaskaudit_trace_id_event_index" ON "credential_mask_audits" ("trace_id", "event_index");

-- The audit list is ordered by recency and filtered by rule, which is what the
-- two readers - the monitor page and the MCP tool - both ask for.
CREATE INDEX "credentialmaskaudit_detected_at_id" ON "credential_mask_audits" ("detected_at", "id");
CREATE INDEX "credentialmaskaudit_rule_detected_at" ON "credential_mask_audits" ("rule", "detected_at");
