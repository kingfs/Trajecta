-- Project attribution: the project directory a client harness declares about
-- itself in the request prompt.
--
-- Both tables are derived from the raw cassettes - the declaration is read out
-- of the request body during parsing - so they are rebuilt rather than
-- transported by `trajecta export`/`import`. `trace_projects` is the per-trace
-- fact; `session_projects` is the rollup the session/project pages read, keyed
-- by the session's first declaration and carrying the latest one plus a switch
-- count for a session that changed directory.
CREATE TABLE IF NOT EXISTS "trace_projects" (
  "trace_id" character varying NOT NULL,
  "session_id" character varying NOT NULL DEFAULT '',
  "root" text NOT NULL DEFAULT '',
  "root_key" text NOT NULL DEFAULT '',
  "source" character varying NOT NULL DEFAULT '',
  "conflicting" boolean NOT NULL DEFAULT false,
  "created_at" timestamptz NOT NULL,
  PRIMARY KEY ("trace_id")
);

CREATE INDEX IF NOT EXISTS "traceproject_session_id" ON "trace_projects" ("session_id");
CREATE INDEX IF NOT EXISTS "traceproject_root_key" ON "trace_projects" ("root_key");

CREATE TABLE IF NOT EXISTS "session_projects" (
  "session_id" character varying NOT NULL,
  "root" text NOT NULL DEFAULT '',
  "root_key" text NOT NULL DEFAULT '',
  "source" character varying NOT NULL DEFAULT '',
  "latest_root" text NOT NULL DEFAULT '',
  "latest_root_key" text NOT NULL DEFAULT '',
  "switch_count" bigint NOT NULL DEFAULT 0,
  "first_trace_id" character varying NOT NULL DEFAULT '',
  "first_seen" timestamptz NOT NULL,
  "last_declared_at" timestamptz NOT NULL,
  "updated_at" timestamptz NOT NULL,
  PRIMARY KEY ("session_id")
);

CREATE INDEX IF NOT EXISTS "sessionproject_root_key" ON "session_projects" ("root_key", "last_declared_at" DESC);
