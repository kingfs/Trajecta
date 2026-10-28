-- Per-occurrence read/resolve/ignore state for credential_masked events.
--
-- The event row itself stays one per rule: it is the bounded notification and
-- the cache of the newest occurrence's state. A reader acting on one trace's
-- occurrence must not mute the same rule in another trace, so the status that is
-- not the default is kept here, keyed by (trace_id, rule). A missing row means
-- unread; only a reader action writes one. That keeps the table proportional to
-- actions rather than to every masked value.
CREATE TABLE IF NOT EXISTS "credential_mask_event_states" (
  "trace_id" character varying NOT NULL DEFAULT '',
  "rule" character varying NOT NULL DEFAULT '',
  "status" character varying NOT NULL,
  "read_at" timestamptz NULL,
  "resolved_at" timestamptz NULL,
  "updated_at" timestamptz NOT NULL,
  PRIMARY KEY ("trace_id", "rule")
);
