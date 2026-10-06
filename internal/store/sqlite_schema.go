package store

// sqliteSchemaStatements is the SQLite schema, applied in order at startup by
// initSchema. It is data rather than logic, and it is 550 lines of it, so it has
// its own file: inside initSchema it buried the driver branch and the migration
// steps that are the part worth reading. It is applied for the SQLite driver only
// - Postgres takes its schema from ent/dao/migrate.Tables - and the order is
// load-bearing (PRAGMA first, then tables, then indexes).
var sqliteSchemaStatements = []string{
	`PRAGMA journal_mode=WAL;`,
	`CREATE TABLE IF NOT EXISTS app_schema_status (
			namespace TEXT PRIMARY KEY,
			version INTEGER NOT NULL,
			mode TEXT NOT NULL,
			source TEXT NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE TABLE IF NOT EXISTS app_settings (
			setting_key TEXT PRIMARY KEY,
			value_json TEXT NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE TABLE IF NOT EXISTS session_summaries (
			session_id TEXT PRIMARY KEY,
			session_source TEXT NOT NULL DEFAULT '',
			request_count INTEGER NOT NULL DEFAULT 0,
			first_seen datetime NOT NULL,
			last_seen datetime NOT NULL,
			last_model TEXT NOT NULL DEFAULT '',
			providers TEXT NOT NULL DEFAULT '',
			success_request INTEGER NOT NULL DEFAULT 0,
			failed_request INTEGER NOT NULL DEFAULT 0,
			success_rate REAL NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			avg_ttft REAL NOT NULL DEFAULT 0,
			total_duration INTEGER NOT NULL DEFAULT 0,
			stream_count INTEGER NOT NULL DEFAULT 0,
			updated_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_session_summaries_last_seen ON session_summaries(last_seen DESC);`,
	`CREATE INDEX IF NOT EXISTS idx_session_summaries_last_model ON session_summaries(last_model);`,
	`CREATE TABLE IF NOT EXISTS overview_metric_buckets (
			bucket_start datetime NOT NULL,
			bucket_size_seconds INTEGER NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0,
			success_request INTEGER NOT NULL DEFAULT 0,
			failed_request INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			ttft_sum INTEGER NOT NULL DEFAULT 0,
			ttft_count INTEGER NOT NULL DEFAULT 0,
			duration_sum INTEGER NOT NULL DEFAULT 0,
			duration_count INTEGER NOT NULL DEFAULT 0,
			stream_count INTEGER NOT NULL DEFAULT 0,
			updated_at datetime NOT NULL,
			PRIMARY KEY (bucket_start, bucket_size_seconds)
		);`,
	`CREATE INDEX IF NOT EXISTS idx_overview_metric_buckets_start ON overview_metric_buckets(bucket_start);`,
	`CREATE TABLE IF NOT EXISTS overview_metric_bucket_members (
			path TEXT PRIMARY KEY,
			bucket_start datetime NOT NULL,
			bucket_size_seconds INTEGER NOT NULL,
			request_count INTEGER NOT NULL DEFAULT 0,
			success_request INTEGER NOT NULL DEFAULT 0,
			failed_request INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			ttft_sum INTEGER NOT NULL DEFAULT 0,
			ttft_count INTEGER NOT NULL DEFAULT 0,
			duration_sum INTEGER NOT NULL DEFAULT 0,
			duration_count INTEGER NOT NULL DEFAULT 0,
			stream_count INTEGER NOT NULL DEFAULT 0,
			updated_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_overview_metric_bucket_members_bucket ON overview_metric_bucket_members(bucket_start, bucket_size_seconds);`,
	`CREATE TABLE IF NOT EXISTS logs (
			path TEXT PRIMARY KEY,
			trace_id TEXT NOT NULL DEFAULT '',
			mod_time_ns INTEGER NOT NULL,
			file_size INTEGER NOT NULL,
			version TEXT NOT NULL,
			request_id TEXT NOT NULL,
			recorded_at datetime NOT NULL,
			model TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			operation TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			url TEXT NOT NULL,
			method TEXT NOT NULL,
			status_code INTEGER NOT NULL,
			duration_ms INTEGER NOT NULL,
			ttft_ms INTEGER NOT NULL,
			client_ip TEXT NOT NULL,
			content_length INTEGER NOT NULL,
			error_text TEXT NOT NULL,
			prompt_tokens INTEGER NOT NULL,
			completion_tokens INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			cached_tokens INTEGER NOT NULL,
			req_header_len INTEGER NOT NULL,
			req_body_len INTEGER NOT NULL,
			res_header_len INTEGER NOT NULL,
			res_body_len INTEGER NOT NULL,
			is_stream bool NOT NULL DEFAULT false,
			session_id TEXT NOT NULL DEFAULT '',
			session_source TEXT NOT NULL DEFAULT '',
			window_id TEXT NOT NULL DEFAULT '',
			client_request_id TEXT NOT NULL DEFAULT '',
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
			routing_failure_reason TEXT NOT NULL DEFAULT ''
		);`,
	`CREATE TABLE IF NOT EXISTS upstream_targets (
			id TEXT PRIMARY KEY,
			base_url TEXT NOT NULL,
			provider_preset TEXT NOT NULL,
			protocol_family TEXT NOT NULL,
			routing_profile TEXT NOT NULL,
			enabled bool NOT NULL DEFAULT true,
			priority INTEGER NOT NULL,
			weight REAL NOT NULL,
			capacity_hint REAL NOT NULL,
			last_refresh_at datetime NULL,
			last_refresh_status TEXT NOT NULL DEFAULT '',
			last_refresh_error TEXT NOT NULL DEFAULT ''
		);`,
	`CREATE TABLE IF NOT EXISTS upstream_models (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			upstream_id TEXT NOT NULL,
			model TEXT NOT NULL,
			source TEXT NOT NULL,
			seen_at datetime NOT NULL
		);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS upstreammodel_upstream_id_model ON upstream_models(upstream_id, model);`,
	`CREATE INDEX IF NOT EXISTS idx_upstream_models_model ON upstream_models(model);`,
	`CREATE TABLE IF NOT EXISTS channel_configs (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'manual',
			base_url TEXT NOT NULL,
			provider_preset TEXT NOT NULL DEFAULT '',
			api_type TEXT NOT NULL DEFAULT '',
			mode TEXT NOT NULL DEFAULT '',
			capabilities_json TEXT NOT NULL DEFAULT '{}',
			protocol_family TEXT NOT NULL DEFAULT '',
			routing_profile TEXT NOT NULL DEFAULT '',
			api_version TEXT NOT NULL DEFAULT '',
			deployment TEXT NOT NULL DEFAULT '',
			project TEXT NOT NULL DEFAULT '',
			location TEXT NOT NULL DEFAULT '',
			model_resource TEXT NOT NULL DEFAULT '',
			api_key_ciphertext BLOB NULL,
			api_key_hint TEXT NOT NULL DEFAULT '',
			headers_json TEXT NOT NULL DEFAULT '{}',
			enabled bool NOT NULL DEFAULT true,
			priority INTEGER NOT NULL DEFAULT 0,
			weight REAL NOT NULL DEFAULT 1,
			capacity_hint REAL NOT NULL DEFAULT 1,
			model_discovery TEXT NOT NULL DEFAULT 'list_models',
			allow_unknown_models bool NOT NULL DEFAULT false,
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL,
			last_probe_at datetime NULL,
			last_probe_status TEXT NOT NULL DEFAULT '',
			last_probe_error TEXT NOT NULL DEFAULT ''
		);`,
	`CREATE INDEX IF NOT EXISTS channelconfig_enabled_priority ON channel_configs(enabled, priority);`,
	`CREATE INDEX IF NOT EXISTS channelconfig_provider_preset ON channel_configs(provider_preset);`,
	`CREATE TABLE IF NOT EXISTS channel_models (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			channel_id TEXT NOT NULL,
			model TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			enabled bool NOT NULL DEFAULT true,
			supports_responses INTEGER NULL,
			supports_chat_completions INTEGER NULL,
			supports_embeddings INTEGER NULL,
			context_window INTEGER NULL,
			max_output_tokens INTEGER NULL,
			compact_history_item_threshold INTEGER NULL,
			upstream_model TEXT NOT NULL DEFAULT '',
			profile_source TEXT NOT NULL DEFAULT '',
			profile_adoption_status TEXT NOT NULL DEFAULT '',
			input_modalities_json TEXT NOT NULL DEFAULT '[]',
			output_modalities_json TEXT NOT NULL DEFAULT '[]',
			raw_model_json TEXT NOT NULL DEFAULT '{}',
			first_seen_at datetime NOT NULL,
			last_seen_at datetime NOT NULL,
			last_probe_at datetime NULL
		);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS channelmodel_channel_id_model ON channel_models(channel_id, model);`,
	`CREATE INDEX IF NOT EXISTS idx_channel_models_model ON channel_models(model);`,
	`CREATE INDEX IF NOT EXISTS channelmodel_channel_id_enabled ON channel_models(channel_id, enabled);`,
	`CREATE TABLE IF NOT EXISTS model_aliases (
			id TEXT PRIMARY KEY,
			alias TEXT NOT NULL,
			target_model TEXT NOT NULL,
			channel_id TEXT NOT NULL DEFAULT '',
			enabled bool NOT NULL DEFAULT true,
			description TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'manual',
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_model_aliases_alias ON model_aliases(alias);`,
	`CREATE INDEX IF NOT EXISTS idx_model_aliases_target ON model_aliases(target_model);`,
	`CREATE INDEX IF NOT EXISTS idx_model_aliases_channel ON model_aliases(channel_id);`,
	`CREATE TABLE IF NOT EXISTS model_catalog (
			model TEXT PRIMARY KEY,
			display_name TEXT NOT NULL DEFAULT '',
			family TEXT NOT NULL DEFAULT '',
			vendor TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			tags_json TEXT NOT NULL DEFAULT '[]',
			first_seen_at datetime NOT NULL,
			last_seen_at datetime NOT NULL,
			last_used_at datetime NULL
		);`,
	`CREATE TABLE IF NOT EXISTS channel_probe_runs (
			id TEXT PRIMARY KEY,
			channel_id TEXT NOT NULL,
			status TEXT NOT NULL,
			started_at datetime NOT NULL,
			completed_at datetime NULL,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			discovered_count INTEGER NOT NULL DEFAULT 0,
			enabled_count INTEGER NOT NULL DEFAULT 0,
			endpoint TEXT NOT NULL DEFAULT '',
			status_code INTEGER NOT NULL DEFAULT 0,
			error_text TEXT NOT NULL DEFAULT '',
			request_meta_json TEXT NOT NULL DEFAULT '{}',
			response_sample_json TEXT NOT NULL DEFAULT '{}'
		);`,
	`CREATE INDEX IF NOT EXISTS channelproberun_channel_id_started_at ON channel_probe_runs(channel_id, started_at);`,
	`CREATE INDEX IF NOT EXISTS channelproberun_status_started_at ON channel_probe_runs(status, started_at);`,
	`CREATE TABLE IF NOT EXISTS datasets (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE TABLE IF NOT EXISTS dataset_examples (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			dataset_id TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			position INTEGER NOT NULL,
			added_at datetime NOT NULL,
			source_type TEXT NOT NULL DEFAULT '',
			source_id TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT ''
		);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS datasetexample_dataset_id_trace_id ON dataset_examples(dataset_id, trace_id);`,
	`CREATE INDEX IF NOT EXISTS idx_dataset_examples_dataset_position ON dataset_examples(dataset_id, position ASC);`,
	`CREATE TABLE IF NOT EXISTS eval_runs (
			id TEXT PRIMARY KEY,
			dataset_id TEXT NOT NULL DEFAULT '',
			source_type TEXT NOT NULL DEFAULT '',
			source_id TEXT NOT NULL DEFAULT '',
			evaluator_set TEXT NOT NULL,
			created_at datetime NOT NULL,
			completed_at datetime NOT NULL,
			trace_count INTEGER NOT NULL DEFAULT 0,
			score_count INTEGER NOT NULL DEFAULT 0,
			pass_count INTEGER NOT NULL DEFAULT 0,
			fail_count INTEGER NOT NULL DEFAULT 0
		);`,
	`CREATE TABLE IF NOT EXISTS scores (
			id TEXT PRIMARY KEY,
			trace_id TEXT NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			dataset_id TEXT NOT NULL DEFAULT '',
			eval_run_id TEXT NOT NULL DEFAULT '',
			evaluator_key TEXT NOT NULL,
			value REAL NOT NULL,
			status TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			explanation TEXT NOT NULL DEFAULT '',
			created_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_scores_trace_id ON scores(trace_id, created_at DESC);`,
	`CREATE INDEX IF NOT EXISTS idx_scores_session_id ON scores(session_id, created_at DESC) WHERE session_id <> '';`,
	`CREATE INDEX IF NOT EXISTS idx_scores_dataset_id ON scores(dataset_id, created_at DESC) WHERE dataset_id <> '';`,
	`CREATE INDEX IF NOT EXISTS idx_scores_eval_run_id ON scores(eval_run_id, created_at DESC) WHERE eval_run_id <> '';`,
	`CREATE TABLE IF NOT EXISTS experiment_runs (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			baseline_eval_run_id TEXT NOT NULL,
			candidate_eval_run_id TEXT NOT NULL,
			created_at datetime NOT NULL,
			baseline_score_count INTEGER NOT NULL DEFAULT 0,
			candidate_score_count INTEGER NOT NULL DEFAULT 0,
			baseline_pass_rate REAL NOT NULL DEFAULT 0,
			candidate_pass_rate REAL NOT NULL DEFAULT 0,
			pass_rate_delta REAL NOT NULL DEFAULT 0,
			matched_score_count INTEGER NOT NULL DEFAULT 0,
			improvement_count INTEGER NOT NULL DEFAULT 0,
			regression_count INTEGER NOT NULL DEFAULT 0
		);`,
	`CREATE INDEX IF NOT EXISTS idx_experiment_runs_created_at ON experiment_runs(created_at DESC, id DESC);`,
	`CREATE TABLE IF NOT EXISTS trace_observations (
			trace_id TEXT PRIMARY KEY,
			parser TEXT NOT NULL,
			parser_version TEXT NOT NULL,
			status TEXT NOT NULL,
			provider TEXT NOT NULL DEFAULT '',
			operation TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			exchange_kind TEXT NOT NULL DEFAULT '',
			exchange_role TEXT NOT NULL DEFAULT '',
			parent_exchange_id TEXT NOT NULL DEFAULT '',
			sequence_index INTEGER NOT NULL DEFAULT 0,
			request_audit_id TEXT NOT NULL DEFAULT '',
			response_id TEXT NOT NULL DEFAULT '',
			summary_json TEXT NOT NULL DEFAULT '{}',
			warnings_json TEXT NOT NULL DEFAULT '[]',
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_trace_observations_status ON trace_observations(status, updated_at DESC);`,
	`CREATE TABLE IF NOT EXISTS semantic_nodes (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			trace_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			parent_node_id TEXT NOT NULL DEFAULT '',
			provider_type TEXT NOT NULL DEFAULT '',
			normalized_type TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '',
			node_index INTEGER NOT NULL DEFAULT 0,
			depth INTEGER NOT NULL DEFAULT 0,
			text_preview TEXT NOT NULL DEFAULT '',
			json TEXT NOT NULL DEFAULT '',
			raw TEXT NOT NULL DEFAULT '',
			raw_ref TEXT NOT NULL DEFAULT '',
			created_at datetime NOT NULL
		);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS semantic_nodes_trace_node_key ON semantic_nodes(trace_id, node_id);`,
	`CREATE INDEX IF NOT EXISTS idx_semantic_nodes_trace_depth ON semantic_nodes(trace_id, depth, node_index);`,
	`CREATE TABLE IF NOT EXISTS trace_findings (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			trace_id TEXT NOT NULL,
			finding_id TEXT NOT NULL,
			category TEXT NOT NULL,
			severity TEXT NOT NULL,
			confidence REAL NOT NULL DEFAULT 0,
			title TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			evidence_path TEXT NOT NULL DEFAULT '',
			evidence_excerpt TEXT NOT NULL DEFAULT '',
			node_id TEXT NOT NULL DEFAULT '',
			detector TEXT NOT NULL,
			detector_version TEXT NOT NULL,
			created_at datetime NOT NULL
		);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS trace_findings_trace_finding_key ON trace_findings(trace_id, finding_id);`,
	`CREATE INDEX IF NOT EXISTS idx_trace_findings_trace_severity ON trace_findings(trace_id, severity, category);`,
	`CREATE TABLE IF NOT EXISTS analysis_runs (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			trace_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL,
			analyzer TEXT NOT NULL,
			analyzer_version TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			input_ref TEXT NOT NULL DEFAULT '',
			output_json TEXT NOT NULL DEFAULT '{}',
			status TEXT NOT NULL,
			created_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_analysis_runs_session_kind ON analysis_runs(session_id, kind, created_at DESC);`,
	`CREATE INDEX IF NOT EXISTS idx_analysis_runs_trace_kind ON analysis_runs(trace_id, kind, created_at DESC);`,
	`CREATE TABLE IF NOT EXISTS analysis_jobs (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			job_type TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_id TEXT NOT NULL,
			status TEXT NOT NULL,
			steps_json TEXT NOT NULL DEFAULT '[]',
			request_json TEXT NOT NULL DEFAULT '{}',
			result_json TEXT NOT NULL DEFAULT '{}',
			last_error TEXT NOT NULL DEFAULT '',
			attempts INTEGER NOT NULL DEFAULT 0,
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL,
			started_at datetime NULL,
			finished_at datetime NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_analysis_jobs_status_updated ON analysis_jobs(status, updated_at ASC);`,
	`CREATE INDEX IF NOT EXISTS idx_analysis_jobs_target ON analysis_jobs(target_type, target_id, created_at DESC);`,
	`CREATE TABLE IF NOT EXISTS parse_jobs (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			trace_id TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS idx_parse_jobs_status ON parse_jobs(status, updated_at ASC);`,
	`DELETE FROM parse_jobs WHERE id NOT IN (SELECT MAX(id) FROM parse_jobs GROUP BY trace_id);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_parse_jobs_trace_id ON parse_jobs(trace_id);`,
	`CREATE TABLE IF NOT EXISTS parser_versions (
			parser TEXT NOT NULL,
			version TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			created_at datetime NOT NULL,
			PRIMARY KEY(parser, version)
		);`,
	`CREATE TABLE IF NOT EXISTS system_events (
			id TEXT PRIMARY KEY,
			fingerprint TEXT NOT NULL,
			source TEXT NOT NULL,
			category TEXT NOT NULL,
			severity TEXT NOT NULL,
			status TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			details_json TEXT NOT NULL DEFAULT '{}',
			trace_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			job_id TEXT NOT NULL DEFAULT '',
			upstream_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			occurrence_count INTEGER NOT NULL DEFAULT 1,
			first_seen_at datetime NOT NULL,
			last_seen_at datetime NOT NULL,
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL,
			read_at datetime NULL,
			resolved_at datetime NULL
		);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS system_events_fingerprint_key ON system_events(fingerprint);`,
	`CREATE INDEX IF NOT EXISTS idx_system_events_status_last_seen ON system_events(status, last_seen_at DESC);`,
	`CREATE INDEX IF NOT EXISTS idx_system_events_source_category ON system_events(source, category, last_seen_at DESC);`,
	`CREATE INDEX IF NOT EXISTS idx_system_events_trace_id ON system_events(trace_id, last_seen_at DESC) WHERE trace_id <> '';`,
	`CREATE TABLE IF NOT EXISTS responses (
			id TEXT PRIMARY KEY,
			conversation_id TEXT NOT NULL DEFAULT '',
			previous_response_id TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'queued',
			model TEXT NOT NULL,
			history_item_ids json NULL,
			output_item_ids json NULL,
			effective_tools json NULL,
			metadata json NULL,
			usage json NULL,
			error json NULL,
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS response_conversation_id_created_at ON responses(conversation_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS response_previous_response_id ON responses(previous_response_id);`,
	`CREATE INDEX IF NOT EXISTS response_status_created_at ON responses(status, created_at);`,
	`CREATE TABLE IF NOT EXISTS response_items (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			response_id TEXT NOT NULL DEFAULT '',
			conversation_id TEXT NOT NULL DEFAULT '',
			payload json NOT NULL,
			created_at datetime NOT NULL,
			updated_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS responseitem_conversation_id_created_at ON response_items(conversation_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS responseitem_response_id_kind ON response_items(response_id, kind);`,
	`CREATE TABLE IF NOT EXISTS request_audits (
			id TEXT PRIMARY KEY,
			response_id TEXT NULL,
			conversation_id TEXT NULL,
			method TEXT NOT NULL,
			path TEXT NOT NULL,
			client_request_id TEXT NULL,
			header_json json NULL,
			body_preview TEXT NULL,
			body_sha256 TEXT NULL,
			redaction_json json NULL,
			status TEXT NOT NULL DEFAULT '',
			error_text TEXT NULL,
			created_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS requestaudit_response_id_created_at ON request_audits(response_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS requestaudit_conversation_id_created_at ON request_audits(conversation_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS requestaudit_client_request_id ON request_audits(client_request_id);`,
	`CREATE INDEX IF NOT EXISTS requestaudit_status_created_at ON request_audits(status, created_at);`,
	`CREATE TABLE IF NOT EXISTS execution_events (
			id TEXT PRIMARY KEY,
			response_id TEXT NULL,
			request_audit_id TEXT NULL,
			conversation_id TEXT NULL,
			event_type TEXT NOT NULL,
			phase TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT '',
			message TEXT NULL,
			details_json json NULL,
			occurred_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS executionevent_response_id_occurred_at ON execution_events(response_id, occurred_at);`,
	`CREATE INDEX IF NOT EXISTS executionevent_conversation_id_occurred_at ON execution_events(conversation_id, occurred_at);`,
	`CREATE INDEX IF NOT EXISTS executionevent_event_type_occurred_at ON execution_events(event_type, occurred_at);`,
	`CREATE INDEX IF NOT EXISTS executionevent_phase_status_occurred_at ON execution_events(phase, status, occurred_at);`,
	`CREATE TABLE IF NOT EXISTS upstream_exchanges (
			id TEXT PRIMARY KEY,
			response_id TEXT NULL,
			request_audit_id TEXT NULL,
			request_id TEXT NULL,
			trace_id TEXT NULL,
			exchange_id TEXT NULL,
			exchange_kind TEXT NULL,
			exchange_role TEXT NULL,
			parent_exchange_id TEXT NULL,
			sequence_index INTEGER NULL,
			cassette_path TEXT NULL,
			upstream_id TEXT NULL,
			route_target TEXT NULL,
			model TEXT NULL,
			endpoint TEXT NULL,
			status_code INTEGER NULL,
			started_at datetime NULL,
			completed_at datetime NULL,
			error_text TEXT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS upstreamexchange_response_id_started_at ON upstream_exchanges(response_id, started_at);`,
	`CREATE INDEX IF NOT EXISTS upstreamexchange_request_audit_id_started_at ON upstream_exchanges(request_audit_id, started_at);`,
	`CREATE INDEX IF NOT EXISTS upstreamexchange_trace_id ON upstream_exchanges(trace_id);`,
	`CREATE INDEX IF NOT EXISTS upstreamexchange_exchange_id ON upstream_exchanges(exchange_id);`,
	`CREATE INDEX IF NOT EXISTS upstreamexchange_parent_exchange_id ON upstream_exchanges(parent_exchange_id);`,
	`CREATE INDEX IF NOT EXISTS upstreamexchange_upstream_id_started_at ON upstream_exchanges(upstream_id, started_at);`,
	`CREATE INDEX IF NOT EXISTS upstreamexchange_status_code_started_at ON upstream_exchanges(status_code, started_at);`,
	`CREATE TABLE IF NOT EXISTS tool_call_audits (
			id TEXT PRIMARY KEY,
			response_id TEXT NULL,
			request_audit_id TEXT NULL,
			conversation_id TEXT NULL,
			call_id TEXT NOT NULL,
			tool_type TEXT NOT NULL,
			tool_name TEXT NULL,
			executor TEXT NULL,
			status TEXT NOT NULL DEFAULT '',
			phase TEXT NOT NULL DEFAULT 'tool_call',
			input_json json NULL,
			output_json json NULL,
			error_text TEXT NULL,
			metadata_json json NULL,
			started_at datetime NULL,
			completed_at datetime NULL,
			created_at datetime NOT NULL
		);`,
	`CREATE INDEX IF NOT EXISTS toolcallaudit_request_audit_id_created_at ON tool_call_audits(request_audit_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS toolcallaudit_response_id_created_at ON tool_call_audits(response_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS toolcallaudit_conversation_id_created_at ON tool_call_audits(conversation_id, created_at);`,
	`CREATE INDEX IF NOT EXISTS toolcallaudit_call_id ON tool_call_audits(call_id);`,
	`CREATE INDEX IF NOT EXISTS toolcallaudit_tool_name_status_created_at ON tool_call_audits(tool_name, status, created_at);`,
	`CREATE INDEX IF NOT EXISTS toolcallaudit_status_created_at ON tool_call_audits(status, created_at);`,
}

// applySQLiteSchemaUpgrades brings an existing SQLite database up to the
// schema the DDL list expects: the additive column and index steps that follow
// the CREATE TABLE statements, plus the schema-status row the monitor reads.
//
// Every step must stay idempotent, because this runs on every startup against
// a database that already has most of them, and the order is the order the
// columns were introduced.
func (s *Store) applySQLiteSchemaUpgrades() error {
	if err := s.ensureColumn("logs", "trace_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("execution_events", "request_audit_id", "TEXT NULL"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS executionevent_request_audit_id_occurred_at ON execution_events(request_audit_id, occurred_at)`); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "provider", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "operation", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "endpoint", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "session_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "session_source", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "window_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "client_request_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureLogExchangeColumns(); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "selected_upstream_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "selected_upstream_base_url", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "selected_upstream_provider_preset", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "routing_policy", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "routing_score", "REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "routing_candidate_count", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "routing_failure_reason", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("upstream_exchanges", "request_id", "TEXT NULL"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE upstream_exchanges SET request_id = trace_id WHERE request_id IS NULL AND trace_id IS NOT NULL`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS upstreamexchange_request_id ON upstream_exchanges(request_id);`); err != nil {
		return err
	}
	if err := s.ensureColumn("upstream_exchanges", "exchange_id", "TEXT NULL"); err != nil {
		return err
	}
	if err := s.ensureColumn("upstream_exchanges", "exchange_kind", "TEXT NULL"); err != nil {
		return err
	}
	if err := s.ensureColumn("upstream_exchanges", "exchange_role", "TEXT NULL"); err != nil {
		return err
	}
	if err := s.ensureColumn("upstream_exchanges", "parent_exchange_id", "TEXT NULL"); err != nil {
		return err
	}
	if err := s.ensureColumn("upstream_exchanges", "sequence_index", "INTEGER NULL"); err != nil {
		return err
	}
	if err := s.ensureColumn("trace_observations", "exchange_kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("trace_observations", "exchange_role", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("trace_observations", "parent_exchange_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("trace_observations", "sequence_index", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn("trace_observations", "request_audit_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("trace_observations", "response_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS upstreamexchange_exchange_id ON upstream_exchanges(exchange_id)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS upstreamexchange_parent_exchange_id ON upstream_exchanges(parent_exchange_id)`); err != nil {
		return err
	}
	if err := s.ensureColumn("analysis_jobs", "request_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_configs", "source", "TEXT NOT NULL DEFAULT 'manual'"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_configs", "api_type", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_configs", "mode", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_configs", "capabilities_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_models", "max_output_tokens", "INTEGER NULL"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_models", "compact_history_item_threshold", "INTEGER NULL"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_models", "upstream_model", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_models", "profile_source", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("channel_models", "profile_adoption_status", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureSessionSummariesSchema(); err != nil {
		return err
	}
	if err := s.ensureOverviewMetricBucketsSchema(); err != nil {
		return err
	}
	if err := s.backfillTraceIDs(); err != nil {
		return err
	}
	if err := s.ensureLogsDatetimeTable(); err != nil {
		return err
	}
	postColumnStmts := []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS logs_trace_id_key ON logs(trace_id);`,
		`CREATE INDEX IF NOT EXISTS tracelog_recorded_at ON logs(recorded_at);`,
		`CREATE INDEX IF NOT EXISTS tracelog_model_recorded_at ON logs(model, recorded_at);`,
		`CREATE INDEX IF NOT EXISTS tracelog_session_id_recorded_at ON logs(session_id, recorded_at);`,
		`CREATE INDEX IF NOT EXISTS tracelog_session_recorded_trace ON logs(session_id, recorded_at DESC, trace_id DESC) WHERE session_id <> '';`,
		`CREATE INDEX IF NOT EXISTS tracelog_request_id ON logs(request_id);`,
		`CREATE INDEX IF NOT EXISTS idx_parse_jobs_status_trace ON parse_jobs(status, trace_id);`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_last_seen_id ON system_events(last_seen_at DESC, id DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_status_last_seen_id ON system_events(status, last_seen_at DESC, id DESC);`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_source_category_last_seen_id ON system_events(source, category, last_seen_at DESC, id DESC);`,
	}
	for _, stmt := range postColumnStmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := s.ensureEntCompatibleTables(); err != nil {
		return err
	}
	if err := s.backfillSemantics(); err != nil {
		return err
	}
	if err := s.backfillGrouping(); err != nil {
		return err
	}
	if err := s.ensureHotpathIndexes(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO app_schema_status (namespace, version, mode, source, updated_at)
		VALUES ('application', 1, 'schema-init', 'internal/store raw DDL startup initialization', CURRENT_TIMESTAMP)
		ON CONFLICT(namespace) DO UPDATE SET
			version = excluded.version,
			mode = excluded.mode,
			source = excluded.source,
			updated_at = excluded.updated_at`); err != nil {
		return err
	}
	return nil
}
