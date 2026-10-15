-- One row per credential the proxy replaced with a format-preserving stand-in.
-- See the Postgres migration of the same timestamp for why the pair lives here
-- and not in the cassette prelude, and for what "storage_mode" means.
CREATE TABLE `credential_mask_audits` (
  `id` text PRIMARY KEY,
  `trace_id` text NOT NULL DEFAULT '',
  `event_index` integer NOT NULL DEFAULT 0,
  `rule` text NOT NULL DEFAULT '',
  `vendor` text NOT NULL DEFAULT '',
  `kind` text NOT NULL DEFAULT '',
  `confidence` text NOT NULL DEFAULT '',
  `score` integer NOT NULL DEFAULT 0,
  `reasons` text NOT NULL DEFAULT '',
  `start_offset` integer NOT NULL DEFAULT 0,
  `end_offset` integer NOT NULL DEFAULT 0,
  `value_length` integer NOT NULL DEFAULT 0,
  `original_value` text NOT NULL DEFAULT '',
  `stand_in_value` text NOT NULL DEFAULT '',
  `storage_mode` text NOT NULL DEFAULT '',
  `detected_at` datetime NOT NULL,
  `created_at` datetime NOT NULL
);
CREATE UNIQUE INDEX `credentialmaskaudit_trace_id_event_index` ON `credential_mask_audits` (`trace_id`, `event_index`);
CREATE INDEX `credentialmaskaudit_detected_at_id` ON `credential_mask_audits` (`detected_at`, `id`);
CREATE INDEX `credentialmaskaudit_rule_detected_at` ON `credential_mask_audits` (`rule`, `detected_at`);
