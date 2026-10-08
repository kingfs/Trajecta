// Schema bootstrapping for the two supported drivers: creating the tables, columns and
// indexes that a database created before a given column or index existed still needs.
// These run once at startup and take the write lock, so they are deliberately kept
// apart from the query and write paths in store.go.

package store

import (
	"context"
	"fmt"
	"strings"
)

func (s *Store) initSchema() error {
	if s.driver == "postgres" {
		if err := s.client.Schema.Create(context.Background()); err != nil {
			return err
		}
		if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS "app_settings" (
			"setting_key" character varying NOT NULL,
			"value_json" character varying NOT NULL,
			"updated_at" timestamptz NOT NULL,
			PRIMARY KEY ("setting_key")
		);`); err != nil {
			return err
		}
		if err := s.ensureSessionSummariesSchema(); err != nil {
			return err
		}
		if err := s.ensureModelAliasesSchema(); err != nil {
			return err
		}
		if err := s.ensureLogExchangeColumns(); err != nil {
			return err
		}
		return nil
	}
	stmts := sqliteSchemaStatements

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return s.applySQLiteSchemaUpgrades()
}

func (s *Store) ensureSessionSummariesSchema() error {
	timeType := "datetime"
	if s.driver == "postgres" {
		timeType = "timestamptz"
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS session_summaries (
		session_id TEXT PRIMARY KEY,
		session_source TEXT NOT NULL DEFAULT '',
		request_count INTEGER NOT NULL DEFAULT 0,
		first_seen %s NOT NULL,
		last_seen %s NOT NULL,
		last_model TEXT NOT NULL DEFAULT '',
		providers TEXT NOT NULL DEFAULT '',
		success_request INTEGER NOT NULL DEFAULT 0,
		failed_request INTEGER NOT NULL DEFAULT 0,
		success_rate REAL NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		avg_ttft REAL NOT NULL DEFAULT 0,
		total_duration INTEGER NOT NULL DEFAULT 0,
		stream_count INTEGER NOT NULL DEFAULT 0,
		updated_at %s NOT NULL
	)`, timeType, timeType, timeType)); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_session_summaries_last_seen ON session_summaries(last_seen DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_session_summaries_last_model ON session_summaries(last_model)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureEntCompatibleTables() error {
	if err := s.ensureUpstreamTargetsEntTable(); err != nil {
		return err
	}
	if err := s.ensureAutoIDTable(
		"upstream_models",
		`CREATE TABLE upstream_models (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			upstream_id TEXT NOT NULL,
			model TEXT NOT NULL,
			source TEXT NOT NULL,
			seen_at datetime NOT NULL
		)`,
		`INSERT INTO upstream_models (upstream_id, model, source, seen_at)
		 SELECT upstream_id, model, source, seen_at FROM upstream_models_old`,
		[]string{
			`CREATE UNIQUE INDEX IF NOT EXISTS upstreammodel_upstream_id_model ON upstream_models(upstream_id, model)`,
			`CREATE INDEX IF NOT EXISTS idx_upstream_models_model ON upstream_models(model)`,
		},
	); err != nil {
		return err
	}
	return s.ensureAutoIDTable(
		"dataset_examples",
		`CREATE TABLE dataset_examples (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			dataset_id TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			position INTEGER NOT NULL,
			added_at TEXT NOT NULL,
			source_type TEXT NOT NULL DEFAULT '',
			source_id TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO dataset_examples (dataset_id, trace_id, position, added_at, source_type, source_id, note)
		 SELECT dataset_id, trace_id, position, added_at, source_type, source_id, note FROM dataset_examples_old`,
		[]string{
			`CREATE UNIQUE INDEX IF NOT EXISTS datasetexample_dataset_id_trace_id ON dataset_examples(dataset_id, trace_id)`,
			`CREATE INDEX IF NOT EXISTS idx_dataset_examples_dataset_position ON dataset_examples(dataset_id, position ASC)`,
		},
	)
}

func (s *Store) ensureUpstreamTargetsEntTable() error {
	enabledType, err := s.columnType("upstream_targets", "enabled")
	if err != nil {
		return err
	}
	lastRefreshAtType, err := s.columnType("upstream_targets", "last_refresh_at")
	if err != nil {
		return err
	}
	if strings.EqualFold(enabledType, "bool") && strings.EqualFold(lastRefreshAtType, "datetime") {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE upstream_targets RENAME TO upstream_targets_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE upstream_targets (
		id TEXT PRIMARY KEY,
		base_url TEXT NOT NULL DEFAULT '',
		provider_preset TEXT NOT NULL DEFAULT '',
		protocol_family TEXT NOT NULL DEFAULT '',
		routing_profile TEXT NOT NULL DEFAULT '',
		enabled bool NOT NULL DEFAULT true,
		priority INTEGER NOT NULL DEFAULT 0,
		weight REAL NOT NULL DEFAULT 0,
		capacity_hint REAL NOT NULL DEFAULT 0,
		last_refresh_at datetime NULL,
		last_refresh_status TEXT NOT NULL DEFAULT '',
		last_refresh_error TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO upstream_targets (
		id, base_url, provider_preset, protocol_family, routing_profile, enabled,
		priority, weight, capacity_hint, last_refresh_at, last_refresh_status, last_refresh_error
	)
	SELECT
		id, base_url, provider_preset, protocol_family, routing_profile,
		CASE WHEN enabled IN (1, '1', 'true', 'TRUE') THEN true ELSE false END,
		priority, weight, capacity_hint,
		CASE WHEN last_refresh_at IS NULL OR TRIM(CAST(last_refresh_at AS text)) = '' THEN NULL ELSE last_refresh_at END,
		last_refresh_status, last_refresh_error
	FROM upstream_targets_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE upstream_targets_old`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureLogsDatetimeTable() error {
	recordedAtType, err := s.columnType("logs", "recorded_at")
	if err != nil {
		return err
	}
	isStreamType, err := s.columnType("logs", "is_stream")
	if err != nil {
		return err
	}
	if strings.EqualFold(recordedAtType, "datetime") && strings.EqualFold(isStreamType, "bool") {
		return nil
	}

	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_logs_recorded_at`,
		`DROP INDEX IF EXISTS idx_logs_model_recorded_at`,
		`DROP INDEX IF EXISTS idx_logs_trace_id`,
		`DROP INDEX IF EXISTS idx_logs_session_id_recorded_at`,
		`DROP INDEX IF EXISTS logs_trace_id_key`,
		`DROP INDEX IF EXISTS tracelog_recorded_at`,
		`DROP INDEX IF EXISTS tracelog_model_recorded_at`,
		`DROP INDEX IF EXISTS tracelog_session_id_recorded_at`,
		`DROP INDEX IF EXISTS tracelog_request_id`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE logs RENAME TO logs_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE logs (
		path TEXT PRIMARY KEY,
		trace_id TEXT NOT NULL DEFAULT '',
		mod_time_ns INTEGER NOT NULL,
		file_size INTEGER NOT NULL,
		version TEXT NOT NULL,
		request_id TEXT NOT NULL DEFAULT '',
		recorded_at datetime NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT '',
		operation TEXT NOT NULL DEFAULT '',
		endpoint TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '',
		status_code INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		ttft_ms INTEGER NOT NULL DEFAULT 0,
		client_ip TEXT NOT NULL DEFAULT '',
		content_length INTEGER NOT NULL DEFAULT 0,
		error_text TEXT NOT NULL DEFAULT '',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		req_header_len INTEGER NOT NULL DEFAULT 0,
		req_body_len INTEGER NOT NULL DEFAULT 0,
		res_header_len INTEGER NOT NULL DEFAULT 0,
		res_body_len INTEGER NOT NULL DEFAULT 0,
		is_stream bool NOT NULL DEFAULT false,
		session_id TEXT NOT NULL DEFAULT '',
		session_source TEXT NOT NULL DEFAULT '',
		window_id TEXT NOT NULL DEFAULT '',
		client_request_id TEXT NOT NULL DEFAULT '',
		request_audit_id TEXT NOT NULL DEFAULT '',
		response_id TEXT NOT NULL DEFAULT '',
		exchange_id TEXT NOT NULL DEFAULT '',
		exchange_kind TEXT NOT NULL DEFAULT '',
		exchange_role TEXT NOT NULL DEFAULT '',
		parent_exchange_id TEXT NOT NULL DEFAULT '',
		sequence_index INTEGER NOT NULL DEFAULT 0,
		selected_upstream_id TEXT NOT NULL DEFAULT '',
		selected_upstream_base_url TEXT NOT NULL DEFAULT '',
		selected_upstream_provider_preset TEXT NOT NULL DEFAULT '',
		routing_policy TEXT NOT NULL DEFAULT '',
		routing_score REAL NOT NULL DEFAULT 0,
		routing_candidate_count INTEGER NOT NULL DEFAULT 0,
		routing_failure_reason TEXT NOT NULL DEFAULT '',
		route_target_id TEXT NOT NULL DEFAULT '',
		channel_id TEXT NOT NULL DEFAULT '',
		credential_id TEXT NOT NULL DEFAULT '',
		sticky_status TEXT NOT NULL DEFAULT '',
		sticky_previous_upstream_id TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO logs (
		path, trace_id, mod_time_ns, file_size, version, request_id, recorded_at, model, provider, operation, endpoint, url, method,
		status_code, duration_ms, ttft_ms, client_ip, content_length, error_text,
		prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		req_header_len, req_body_len, res_header_len, res_body_len, is_stream,
		session_id, session_source, window_id, client_request_id,
		request_audit_id, response_id,
		exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index,
		selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
		routing_policy, routing_score, routing_candidate_count, routing_failure_reason,
		route_target_id, channel_id, credential_id, sticky_status, sticky_previous_upstream_id
	)
	SELECT
		path, trace_id, mod_time_ns, file_size, version, request_id,
		CASE WHEN recorded_at IS NULL OR TRIM(CAST(recorded_at AS text)) = '' THEN '1970-01-01T00:00:00Z' ELSE recorded_at END,
		model, provider, operation, endpoint, url, method,
		status_code, duration_ms, ttft_ms, client_ip, content_length, error_text,
		prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		req_header_len, req_body_len, res_header_len, res_body_len,
		CASE WHEN is_stream IN (1, '1', 'true', 'TRUE') THEN true ELSE false END,
		session_id, session_source, window_id, client_request_id,
		'', '',
		'', '', '', '', 0,
		selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
		routing_policy, routing_score, routing_candidate_count, routing_failure_reason,
		route_target_id, channel_id, credential_id, sticky_status, sticky_previous_upstream_id
	FROM logs_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE logs_old`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureAutoIDTable(table string, createSQL string, copySQL string, indexes []string) error {
	hasID, err := s.hasColumn(table, "id")
	if err != nil {
		return err
	}
	if hasID {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(createSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(copySQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE ` + table + `_old`); err != nil {
		return err
	}
	for _, stmt := range indexes {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ensureColumn(table string, column string, definition string) error {
	exists, err := s.hasColumn(table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func (s *Store) ensureModelAliasesSchema() error {
	if s == nil || s.db == nil {
		return nil
	}
	createdAtType := "datetime"
	updatedAtType := "datetime"
	if s.driver == "postgres" {
		createdAtType = "timestamptz"
		updatedAtType = "timestamptz"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS model_aliases (
			id TEXT PRIMARY KEY,
			alias TEXT NOT NULL,
			target_model TEXT NOT NULL,
			channel_id TEXT NOT NULL DEFAULT '',
			enabled bool NOT NULL DEFAULT true,
			description TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'manual',
			created_at %s NOT NULL,
			updated_at %s NOT NULL
		);`, createdAtType, updatedAtType),
		`CREATE INDEX IF NOT EXISTS idx_model_aliases_alias ON model_aliases(alias);`,
		`CREATE INDEX IF NOT EXISTS idx_model_aliases_target ON model_aliases(target_model);`,
		`CREATE INDEX IF NOT EXISTS idx_model_aliases_channel ON model_aliases(channel_id);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureLogExchangeColumns() error {
	if err := s.ensureColumn("logs", "request_audit_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "response_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "exchange_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "exchange_kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "exchange_role", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "parent_exchange_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "sequence_index", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// `(request_audit_id, recorded_at)` and `(exchange_kind, recorded_at)` used to
	// be created here as well. The planner never chose either on Postgres, and
	// migration 20261009090000_add_hot_path_indexes drops them there; SQLite keeps
	// only the access paths both engines actually use, so a database that already
	// has the two old indexes keeps them rather than losing a query plan it may
	// have been relying on.
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS tracelog_parent_exchange_id ON logs(parent_exchange_id)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureHotpathIndexes() error {
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_logs_trace_id_hotpath ON logs(trace_id)`,
		`CREATE INDEX IF NOT EXISTS tracelog_session_recorded_trace ON logs(session_id, recorded_at DESC, trace_id DESC) WHERE session_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_parse_jobs_status_trace ON parse_jobs(status, trace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_last_seen_id ON system_events(last_seen_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_status_last_seen_id ON system_events(status, last_seen_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_source_category_last_seen_id ON system_events(source, category, last_seen_at DESC, id DESC)`,
		// The same indexes the versioned Postgres migrations add, so the two
		// schemas keep serving the same access paths.
		`CREATE INDEX IF NOT EXISTS tracelog_selected_upstream_id_recorded_at ON logs(selected_upstream_id, recorded_at)`,
		`CREATE INDEX IF NOT EXISTS requestaudit_created_at_id ON request_audits(created_at, id)`,
		`CREATE INDEX IF NOT EXISTS toolcallaudit_created_at_id ON tool_call_audits(created_at, id)`,
		`CREATE INDEX IF NOT EXISTS analysisrun_created_at_id ON analysis_runs(created_at, id)`,
		`CREATE INDEX IF NOT EXISTS tracefinding_created_at_id ON trace_findings(created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS tracefinding_severity_created_at_id ON trace_findings(severity, created_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS analysisjob_created_at_id ON analysis_jobs(created_at DESC, id DESC)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
