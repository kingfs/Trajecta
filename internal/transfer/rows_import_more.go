package transfer

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// The handlers in this file cover the remaining tables. Every one is written
// to be idempotent: a second import of the same bundle changes nothing and
// reports the rows as skipped, and a later import of a newer bundle only
// touches what actually changed.

// ---------------------------------------------------------------------------
// model_aliases
// ---------------------------------------------------------------------------

func (s *importState) handleModelAliases(body io.Reader, size int64) error {
	rep := s.report("model_aliases")
	return s.processJSONL("model_aliases", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[modelAliasRow](line)
		if err != nil {
			return err
		}
		src.ChannelID = s.resolver.remapID(src.ChannelID)
		var existingEnabled, existingTarget, existingDesc, existingSource any
		var existingUpdated any
		err = s.view.TransferQueryRow(s.ctx,
			`SELECT enabled, target_model, description, source, updated_at FROM model_aliases WHERE id = ?`, src.ID).
			Scan(&existingEnabled, &existingTarget, &existingDesc, &existingSource, &existingUpdated)
		if err == nil {
			// The alias id already exists. Keep the target row's routing intent;
			// enable a disabled alias only when the source disagrees, never the
			// other way around (disabling is deliberate).
			if !anyBool(existingEnabled) && src.Enabled {
				rep.Updated++
				return s.exec(`UPDATE model_aliases SET enabled = ?, updated_at = ? WHERE id = ?`, true, src.UpdatedAt, src.ID)
			}
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO model_aliases (id, alias, target_model, channel_id, enabled, description, source, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.Alias, src.TargetModel, src.ChannelID, src.Enabled, src.Description, orElse(src.Source, "manual"), src.CreatedAt, src.UpdatedAt)
	})
}

// ---------------------------------------------------------------------------
// model_catalog — merge by model, keep the newer timestamps.
// ---------------------------------------------------------------------------

func (s *importState) handleModelCatalog(body io.Reader, size int64) error {
	rep := s.report("model_catalog")
	return s.processJSONL("model_catalog", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[modelCatalogRow](line)
		if err != nil {
			return err
		}
		var dstFirst, dstLast, dstUsed, dstDisplay, dstFamily, dstVendor, dstDesc, dstTags any
		err = s.view.TransferQueryRow(s.ctx,
			`SELECT first_seen_at, last_seen_at, last_used_at, display_name, family, vendor, description, tags_json FROM model_catalog WHERE model = ?`, src.Model).
			Scan(&dstFirst, &dstLast, &dstUsed, &dstDisplay, &dstFamily, &dstVendor, &dstDesc, &dstTags)
		if err != nil {
			rep.Inserted++
			return s.exec(`
				INSERT INTO model_catalog (model, display_name, family, vendor, description, tags_json, first_seen_at, last_seen_at, last_used_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				src.Model, src.DisplayName, src.Family, src.Vendor, src.Description,
				orElseJSON(src.TagsJSON, "[]"), src.FirstSeenAt, src.LastSeenAt, nilIfEmptyPtr(src.LastUsedAt))
		}
		first := minTimeString(anyTimeString(dstFirst), src.FirstSeenAt)
		last := maxTimeString(anyTimeString(dstLast), src.LastSeenAt)
		dstUsedStr := ""
		if p := ptrTimeString(dstUsed); p != nil {
			dstUsedStr = *p
		}
		srcUsed := ""
		if src.LastUsedAt != nil {
			srcUsed = *src.LastUsedAt
		}
		used := maxTimeString(dstUsedStr, srcUsed)
		if first == anyTimeString(dstFirst) && last == anyTimeString(dstLast) && used == dstUsedStr {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Updated++
		return s.exec(`
			UPDATE model_catalog SET first_seen_at = ?, last_seen_at = ?, last_used_at = ? WHERE model = ?`,
			first, last, nilIfEmpty(used), src.Model)
	})
}

// ---------------------------------------------------------------------------
// app_settings — whitelist only, never overwrite an existing target key.
// ---------------------------------------------------------------------------

func (s *importState) handleAppSettings(body io.Reader, size int64) error {
	rep := s.report("app_settings")
	return s.processJSONL("app_settings", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[appSettingRow](line)
		if err != nil {
			return err
		}
		if !appSettingsImportWhitelist[src.Key] {
			rep.SkippedConflict++
			return nil
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM app_settings WHERE setting_key = ?`, src.Key)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`INSERT INTO app_settings (setting_key, value_json, updated_at) VALUES (?, ?, ?)`,
			src.Key, src.ValueJSON, src.UpdatedAt)
	})
}

// ---------------------------------------------------------------------------
// channel_probe_runs
// ---------------------------------------------------------------------------

func (s *importState) handleChannelProbeRuns(body io.Reader, size int64) error {
	rep := s.report("channel_probe_runs")
	return s.processJSONL("channel_probe_runs", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[channelProbeRunRow](line)
		if err != nil {
			return err
		}
		src.ChannelID = s.resolver.remapID(src.ChannelID)
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM channel_probe_runs WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO channel_probe_runs (id, channel_id, status, started_at, completed_at, duration_ms, discovered_count, enabled_count, endpoint, status_code, error_text, request_meta_json, response_sample_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.ChannelID, src.Status, src.StartedAt, nilIfEmptyPtr(src.CompletedAt), src.DurationMs,
			src.DiscoveredCount, src.EnabledCount, src.Endpoint, src.StatusCode, src.ErrorText,
			orElseJSON(src.RequestMetaJSON, "{}"), orElseJSON(src.ResponseSampleJSON, "{}"))
	})
}

// ---------------------------------------------------------------------------
// datasets / eval_runs / experiment_runs / dataset_examples / scores
// ---------------------------------------------------------------------------

func (s *importState) handleDatasets(body io.Reader, size int64) error {
	rep := s.report("datasets")
	return s.processJSONL("datasets", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[datasetRow](line)
		if err != nil {
			return err
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM datasets WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			s.datasetIDMap[src.ID] = src.ID
			rep.SkippedUnchanged++
			return nil
		}
		s.datasetIDMap[src.ID] = src.ID
		rep.Inserted++
		return s.exec(`INSERT INTO datasets (id, name, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			src.ID, src.Name, src.Description, src.CreatedAt, src.UpdatedAt)
	})
}

func (s *importState) handleEvalRuns(body io.Reader, size int64) error {
	rep := s.report("eval_runs")
	return s.processJSONL("eval_runs", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[evalRunRow](line)
		if err != nil {
			return err
		}
		if mapped, ok := s.datasetIDMap[src.DatasetID]; ok {
			src.DatasetID = mapped
		}
		s.evalRunIDMap[src.ID] = src.ID
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM eval_runs WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO eval_runs (id, dataset_id, source_type, source_id, evaluator_set, created_at, completed_at, trace_count, score_count, pass_count, fail_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.DatasetID, src.SourceType, src.SourceID, src.EvaluatorSet,
			src.CreatedAt, src.CompletedAt, src.TraceCount, src.ScoreCount, src.PassCount, src.FailCount)
	})
}

func (s *importState) handleExperimentRuns(body io.Reader, size int64) error {
	rep := s.report("experiment_runs")
	return s.processJSONL("experiment_runs", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[experimentRunRow](line)
		if err != nil {
			return err
		}
		if mapped, ok := s.evalRunIDMap[src.BaselineEvalRunID]; ok {
			src.BaselineEvalRunID = mapped
		}
		if mapped, ok := s.evalRunIDMap[src.CandidateEvalRunID]; ok {
			src.CandidateEvalRunID = mapped
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM experiment_runs WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO experiment_runs (id, name, description, baseline_eval_run_id, candidate_eval_run_id, created_at,
				baseline_score_count, candidate_score_count, baseline_pass_rate, candidate_pass_rate, pass_rate_delta,
				matched_score_count, improvement_count, regression_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.Name, src.Description, src.BaselineEvalRunID, src.CandidateEvalRunID, src.CreatedAt,
			src.BaselineScoreCount, src.CandidateScoreCount, src.BaselinePassRate, src.CandidatePassRate, src.PassRateDelta,
			src.MatchedScoreCount, src.ImprovementCount, src.RegressionCount)
	})
}

func (s *importState) handleDatasetExamples(body io.Reader, size int64) error {
	rep := s.report("dataset_examples")
	return s.processJSONL("dataset_examples", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[datasetExampleRow](line)
		if err != nil {
			return err
		}
		if s.skipTraces[src.TraceID] {
			rep.SkippedTrace++
			return nil
		}
		if mapped, ok := s.datasetIDMap[src.DatasetID]; ok {
			src.DatasetID = mapped
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM dataset_examples WHERE dataset_id = ? AND trace_id = ?`, src.DatasetID, src.TraceID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO dataset_examples (dataset_id, trace_id, position, added_at, source_type, source_id, note)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			src.DatasetID, src.TraceID, src.Position, src.AddedAt, src.SourceType, src.SourceID, src.Note)
	})
}

func (s *importState) handleScores(body io.Reader, size int64) error {
	rep := s.report("scores")
	return s.processJSONL("scores", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[scoreRow](line)
		if err != nil {
			return err
		}
		if s.skipTraces[src.TraceID] {
			rep.SkippedTrace++
			return nil
		}
		if mapped, ok := s.datasetIDMap[src.DatasetID]; ok {
			src.DatasetID = mapped
		}
		if mapped, ok := s.evalRunIDMap[src.EvalRunID]; ok {
			src.EvalRunID = mapped
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM scores WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO scores (id, trace_id, session_id, dataset_id, eval_run_id, evaluator_key, value, status, label, explanation, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.TraceID, src.SessionID, src.DatasetID, src.EvalRunID, src.EvaluatorKey,
			src.Value, src.Status, src.Label, src.Explanation, src.CreatedAt)
	})
}

// ---------------------------------------------------------------------------
// responses / response_items
// ---------------------------------------------------------------------------

func (s *importState) handleResponses(body io.Reader, size int64) error {
	rep := s.report("responses")
	return s.processJSONL("responses", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[responseRow](line)
		if err != nil {
			return err
		}
		s.responseIDMap[src.ID] = src.ID
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM responses WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO responses (id, conversation_id, previous_response_id, status, model, history_item_ids, output_item_ids, effective_tools, metadata, usage, error, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.ConversationID, src.PreviousResponseID, orElse(src.Status, "queued"), src.Model,
			jsonOrNil(src.HistoryItemIDs), jsonOrNil(src.OutputItemIDs), jsonOrNil(src.EffectiveTools),
			jsonOrNil(src.Metadata), jsonOrNil(src.Usage), jsonOrNil(src.Error), src.CreatedAt, src.UpdatedAt)
	})
}

func (s *importState) handleResponseItems(body io.Reader, size int64) error {
	rep := s.report("response_items")
	return s.processJSONL("response_items", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[responseItemRow](line)
		if err != nil {
			return err
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM response_items WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO response_items (id, kind, response_id, conversation_id, payload, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.Kind, src.ResponseID, src.ConversationID, orElseJSON(src.Payload, "{}"), src.CreatedAt, src.UpdatedAt)
	})
}

// ---------------------------------------------------------------------------
// request_audits / execution_events / tool_call_audits
// ---------------------------------------------------------------------------

func (s *importState) handleRequestAudits(body io.Reader, size int64) error {
	rep := s.report("request_audits")
	return s.processJSONL("request_audits", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[requestAuditRow](line)
		if err != nil {
			return err
		}
		s.requestAuditIDMap[src.ID] = src.ID
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM request_audits WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO request_audits (id, response_id, conversation_id, method, path, client_request_id, header_json, body_preview, body_sha256, redaction_json, status, error_text, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, nilIfEmptyPtr(src.ResponseID), nilIfEmptyPtr(src.ConversationID), src.Method, src.Path,
			nilIfEmptyPtr(src.ClientRequestID), jsonOrNil(src.HeaderJSON), nilIfEmptyPtr(src.BodyPreview),
			nilIfEmptyPtr(src.BodySHA256), jsonOrNil(src.RedactionJSON), orElse(src.Status, ""), nilIfEmptyPtr(src.ErrorText), src.CreatedAt)
	})
}

func (s *importState) handleExecutionEvents(body io.Reader, size int64) error {
	rep := s.report("execution_events")
	return s.processJSONL("execution_events", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[executionEventRow](line)
		if err != nil {
			return err
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM execution_events WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO execution_events (id, response_id, request_audit_id, conversation_id, event_type, phase, status, message, details_json, occurred_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, nilIfEmptyPtr(src.ResponseID), nilIfEmptyPtr(src.RequestAuditID), nilIfEmptyPtr(src.ConversationID),
			src.EventType, orElse(src.Phase, ""), orElse(src.Status, ""), nilIfEmptyPtr(src.Message),
			jsonOrNil(src.DetailsJSON), src.OccurredAt)
	})
}

func (s *importState) handleToolCallAudits(body io.Reader, size int64) error {
	rep := s.report("tool_call_audits")
	return s.processJSONL("tool_call_audits", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[toolCallAuditRow](line)
		if err != nil {
			return err
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM tool_call_audits WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO tool_call_audits (id, response_id, request_audit_id, conversation_id, call_id, tool_type, tool_name, executor, status, phase, input_json, output_json, error_text, metadata_json, started_at, completed_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, nilIfEmptyPtr(src.ResponseID), nilIfEmptyPtr(src.RequestAuditID), nilIfEmptyPtr(src.ConversationID),
			src.CallID, src.ToolType, nilIfEmptyPtr(src.ToolName), nilIfEmptyPtr(src.Executor),
			orElse(src.Status, ""), orElse(src.Phase, "tool_call"), jsonOrNil(src.InputJSON), jsonOrNil(src.OutputJSON),
			nilIfEmptyPtr(src.ErrorText), jsonOrNil(src.MetadataJSON), nilIfEmptyPtr(src.StartedAt), nilIfEmptyPtr(src.CompletedAt), src.CreatedAt)
	})
}

// ---------------------------------------------------------------------------
// system_events — merge by fingerprint: sum occurrences, keep the target's
// operator state (read/resolved) and its severity/status.
// ---------------------------------------------------------------------------

func (s *importState) handleSystemEvents(body io.Reader, size int64) error {
	rep := s.report("system_events")
	return s.processJSONL("system_events", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[systemEventRow](line)
		if err != nil {
			return err
		}
		if src.TraceID != "" && s.skipTraces[src.TraceID] {
			rep.SkippedTrace++
			return nil
		}
		src.UpstreamID = s.resolver.remapID(src.UpstreamID)
		var dstID, dstOccurrence, dstFirst, dstLast, dstReadAt, dstResolvedAt any
		err = s.view.TransferQueryRow(s.ctx,
			`SELECT id, occurrence_count, first_seen_at, last_seen_at, read_at, resolved_at FROM system_events WHERE fingerprint = ?`, src.Fingerprint).
			Scan(&dstID, &dstOccurrence, &dstFirst, &dstLast, &dstReadAt, &dstResolvedAt)
		if err != nil {
			rep.Inserted++
			return s.exec(`
				INSERT INTO system_events (id, fingerprint, source, category, severity, status, title, message, details_json, trace_id, session_id, job_id, upstream_id, model, occurrence_count, first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				src.ID, src.Fingerprint, src.Source, src.Category, src.Severity, src.Status, src.Title, src.Message,
				orElseJSON(src.DetailsJSON, "{}"), src.TraceID, src.SessionID, src.JobID, src.UpstreamID, src.Model,
				src.OccurrenceCount, src.FirstSeenAt, src.LastSeenAt, src.CreatedAt, src.UpdatedAt,
				nilIfEmptyPtr(src.ReadAt), nilIfEmptyPtr(src.ResolvedAt))
		}
		dstFirstStr := anyTimeString(dstFirst)
		dstLastStr := anyTimeString(dstLast)
		first := minTimeString(dstFirstStr, src.FirstSeenAt)
		last := maxTimeString(dstLastStr, src.LastSeenAt)
		// The target row is the one an operator has been reading: its
		// read/resolved state and severity win, and only its timestamps and
		// occurrence counter are merged.
		//
		// Occurrences are added only when the source event is newer than what
		// the target already knows. Re-importing the same bundle therefore adds
		// nothing (the timestamps are equal), which is what keeps the command
		// idempotent; an occurrence that shares the newest timestamp of an
		// already-merged event is not re-counted.
		merged := anyInt64(dstOccurrence)
		changed := false
		if src.LastSeenAt != "" && src.LastSeenAt > dstLastStr {
			merged += src.OccurrenceCount
			changed = true
		}
		if first != dstFirstStr || last != dstLastStr {
			changed = true
		}
		if !changed {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Updated++
		return s.exec(`UPDATE system_events SET occurrence_count = ?, first_seen_at = ?, last_seen_at = ? WHERE id = ?`,
			merged, first, last, anyString(dstID))
	})
}

// ---------------------------------------------------------------------------
// analysis_runs (identity id reassigned by the database)
// ---------------------------------------------------------------------------

func (s *importState) handleAnalysisRuns(body io.Reader, size int64) error {
	rep := s.report("analysis_runs")
	return s.processJSONL("analysis_runs", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[analysisRunRow](line)
		if err != nil {
			return err
		}
		if s.skipTraces[src.TraceID] {
			rep.SkippedTrace++
			return nil
		}
		exists, err := s.queryRowExists(`
			SELECT COUNT(*) FROM analysis_runs
			WHERE trace_id = ? AND kind = ? AND analyzer = ? AND analyzer_version = ? AND created_at = ?`,
			src.TraceID, src.Kind, src.Analyzer, src.AnalyzerVersion, src.CreatedAt)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO analysis_runs (trace_id, session_id, kind, analyzer, analyzer_version, model, input_ref, output_json, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.TraceID, src.SessionID, src.Kind, src.Analyzer, src.AnalyzerVersion,
			src.Model, src.InputRef, orElseJSON(src.OutputJSON, "{}"), src.Status, src.CreatedAt)
	})
}

// ---------------------------------------------------------------------------
// logs — path is the primary key; a path that already exists in the target
// with a different trace id is a conflict and the whole trace is skipped.
// ---------------------------------------------------------------------------

func (s *importState) handleLogs(body io.Reader, size int64) error {
	rep := s.report("logs")
	return s.processJSONL("logs", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[logRow](line)
		if err != nil {
			return err
		}
		targetPath, targetEntry, err := s.targetCassettePath(src.Path)
		if err != nil {
			rep.SkippedConflict++
			s.result.warn("logs row %s: %v", src.Path, err)
			s.skipTraces[src.TraceID] = true
			return nil
		}
		src.Path = targetPath
		src.ChannelID = s.resolver.remapID(src.ChannelID)
		src.RouteTargetID = s.resolver.remapRouteTarget(src.RouteTargetID)
		src.StickyPreviousUpstreamID = s.resolver.remapRouteTarget(src.StickyPreviousUpstreamID)
		src.SelectedUpstreamID = s.resolver.remapID(src.SelectedUpstreamID)

		// Record the mtime the cassette file must carry, so the next index Sync
		// does not treat the imported file as new.
		s.desiredFiles[targetEntry] = desiredFile{modTimeNs: src.ModTimeNs}

		var existingTraceID, existingModTime, existingSize any
		err = s.view.TransferQueryRow(s.ctx,
			`SELECT trace_id, mod_time_ns, file_size FROM logs WHERE path = ?`, src.Path).
			Scan(&existingTraceID, &existingModTime, &existingSize)
		if err == nil {
			existing := anyString(existingTraceID)
			if existing != src.TraceID {
				// Same cassette path, different trace: two deployments recorded
				// a file at the same path. Keeping the source row would move the
				// target's derived analysis onto foreign ids, so skip it and
				// report loudly.
				rep.SkippedConflict++
				s.skipPaths[src.Path] = true
				s.skipTraces[src.TraceID] = true
				s.desiredFiles[targetEntry] = desiredFile{modTimeNs: src.ModTimeNs, skip: true}
				s.result.warn("cassette path %s already indexed under trace %s (source trace %s); skipped", src.Path, existing, src.TraceID)
				return nil
			}
			if anyInt64(existingModTime) == src.ModTimeNs && anyInt64(existingSize) == src.FileSize {
				rep.SkippedUnchanged++
				return nil
			}
			// The cassette changed since the first import (re-recorded, or the
			// bundle is newer): refresh the indexed row, keeping its trace id.
			rep.Updated++
			if !s.apply {
				return nil
			}
			return s.exec(`
				UPDATE logs SET
					mod_time_ns = ?, file_size = ?, version = ?, request_id = ?, recorded_at = ?,
					model = ?, provider = ?, operation = ?, endpoint = ?, url = ?, method = ?,
					status_code = ?, duration_ms = ?, ttft_ms = ?, client_ip = ?, content_length = ?, error_text = ?,
					prompt_tokens = ?, completion_tokens = ?, total_tokens = ?, cached_tokens = ?,
					req_header_len = ?, req_body_len = ?, res_header_len = ?, res_body_len = ?, is_stream = ?,
					session_id = ?, session_source = ?, window_id = ?, client_request_id = ?,
					request_audit_id = ?, response_id = ?,
					exchange_id = ?, exchange_kind = ?, exchange_role = ?, parent_exchange_id = ?, sequence_index = ?,
					selected_upstream_id = ?, selected_upstream_base_url = ?, selected_upstream_provider_preset = ?,
					routing_policy = ?, routing_score = ?, routing_candidate_count = ?, routing_failure_reason = ?,
					route_target_id = ?, channel_id = ?, credential_id = ?, sticky_status = ?, sticky_previous_upstream_id = ?,
					client_harness = ?, client_harness_name = ?, client_harness_version = ?, client_harness_kind = ?,
					client_harness_confidence = ?, client_harness_tags_json = ?
				WHERE path = ? AND trace_id = ?`,
				src.ModTimeNs, src.FileSize, src.Version, src.RequestID, src.RecordedAt,
				src.Model, src.Provider, src.Operation, src.Endpoint, src.URL, src.Method,
				src.StatusCode, src.DurationMs, src.TTFTMs, src.ClientIP, src.ContentLength, src.ErrorText,
				src.PromptTokens, src.CompletionTokens, src.TotalTokens, src.CachedTokens,
				src.ReqHeaderLen, src.ReqBodyLen, src.ResHeaderLen, src.ResBodyLen, src.IsStream,
				src.SessionID, src.SessionSource, src.WindowID, src.ClientRequestID,
				src.RequestAuditID, src.ResponseID,
				src.ExchangeID, src.ExchangeKind, src.ExchangeRole, src.ParentExchangeID, src.SequenceIndex,
				src.SelectedUpstreamID, src.SelectedUpstreamBaseURL, src.SelectedUpstreamProviderPreset,
				src.RoutingPolicy, src.RoutingScore, src.RoutingCandidateCount, src.RoutingFailureReason,
				src.RouteTargetID, src.ChannelID, src.CredentialID, src.StickyStatus, src.StickyPreviousUpstreamID,
				src.ClientHarness, src.ClientHarnessName, src.ClientHarnessVersion, src.ClientHarnessKind,
				src.ClientHarnessConfidence, orElseJSON(src.ClientHarnessTagsJSON, "[]"),
				src.Path, src.TraceID,
			)
		}
		rep.Inserted++
		if !s.apply {
			return nil
		}
		return s.exec(`
			INSERT INTO logs (`+logScanColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.Path, src.TraceID, src.ModTimeNs, src.FileSize, src.Version, src.RequestID, src.RecordedAt,
			src.Model, src.Provider, src.Operation, src.Endpoint, src.URL, src.Method,
			src.StatusCode, src.DurationMs, src.TTFTMs, src.ClientIP, src.ContentLength, src.ErrorText,
			src.PromptTokens, src.CompletionTokens, src.TotalTokens, src.CachedTokens,
			src.ReqHeaderLen, src.ReqBodyLen, src.ResHeaderLen, src.ResBodyLen, src.IsStream,
			src.SessionID, src.SessionSource, src.WindowID, src.ClientRequestID,
			src.RequestAuditID, src.ResponseID,
			src.ExchangeID, src.ExchangeKind, src.ExchangeRole, src.ParentExchangeID, src.SequenceIndex,
			src.SelectedUpstreamID, src.SelectedUpstreamBaseURL, src.SelectedUpstreamProviderPreset,
			src.RoutingPolicy, src.RoutingScore, src.RoutingCandidateCount, src.RoutingFailureReason,
			src.RouteTargetID, src.ChannelID, src.CredentialID, src.StickyStatus, src.StickyPreviousUpstreamID,
			src.ClientHarness, src.ClientHarnessName, src.ClientHarnessVersion, src.ClientHarnessKind,
			src.ClientHarnessConfidence, orElseJSON(src.ClientHarnessTagsJSON, "[]"),
		)
	})
}

// targetCassettePath rewrites one source cassette path onto the target output
// directory. Both deployments mount /app/data, so the relative layout under
// output_dir carries over unchanged and only the prefix differs.
func (s *importState) targetCassettePath(sourcePath string) (targetPath, entry string, err error) {
	srcRoot := s.manifest.SourceOutputDir
	normalisedSrc := filepath.ToSlash(strings.TrimSuffix(srcRoot, "/"))
	normalisedPath := filepath.ToSlash(strings.TrimPrefix(sourcePath, "./"))
	rel := ""
	if normalisedSrc != "" && strings.HasPrefix(normalisedPath, normalisedSrc+"/") {
		rel = strings.TrimPrefix(normalisedPath, normalisedSrc+"/")
	} else if i := strings.Index(normalisedPath, "/traces/"); i >= 0 {
		// Fallback for a bundle exported with a different output_dir string
		// than the one its rows were recorded under: keep the layout below the
		// traces root.
		rel = strings.TrimPrefix(normalisedPath[i+len("/traces/"):], "/")
	}
	if rel == "" {
		return "", "", fmt.Errorf("cannot derive a relative cassette path from %q (source output dir %q)", sourcePath, srcRoot)
	}
	target := filepath.Join(s.outputDir, filepath.FromSlash(rel))
	return target, rel, nil
}

// ---------------------------------------------------------------------------
// upstream_exchanges
// ---------------------------------------------------------------------------

func (s *importState) handleUpstreamExchanges(body io.Reader, size int64) error {
	rep := s.report("upstream_exchanges")
	return s.processJSONL("upstream_exchanges", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[upstreamExchangeRow](line)
		if err != nil {
			return err
		}
		if src.CassettePath != nil && *src.CassettePath != "" {
			targetPath, _, err := s.targetCassettePath(*src.CassettePath)
			if err != nil {
				rep.SkippedConflict++
				return nil
			}
			src.CassettePath = &targetPath
			if s.skipPaths[*src.CassettePath] {
				rep.SkippedTrace++
				return nil
			}
		}
		src.UpstreamID = remapPtrString(src.UpstreamID, s.resolver.remapID)
		src.RouteTarget = remapPtrString(src.RouteTarget, s.resolver.remapRouteTarget)
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM upstream_exchanges WHERE id = ?`, src.ID)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO upstream_exchanges (id, response_id, request_audit_id, trace_id, cassette_path, upstream_id, route_target, model, endpoint, status_code, started_at, completed_at, error_text, exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index, request_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, nilIfEmptyPtr(src.ResponseID), nilIfEmptyPtr(src.RequestAuditID), nilIfEmptyPtr(src.TraceID),
			nilIfEmptyPtr(src.CassettePath), nilIfEmptyPtr(src.UpstreamID), nilIfEmptyPtr(src.RouteTarget),
			nilIfEmptyPtr(src.Model), nilIfEmptyPtr(src.Endpoint), nilIfInt64Ptr(src.StatusCode),
			nilIfEmptyPtr(src.StartedAt), nilIfEmptyPtr(src.CompletedAt), nilIfEmptyPtr(src.ErrorText),
			nilIfEmptyPtr(src.ExchangeID), nilIfEmptyPtr(src.ExchangeKind), nilIfEmptyPtr(src.ExchangeRole),
			nilIfEmptyPtr(src.ParentExchangeID), nilIfInt64Ptr(src.SequenceIndex), nilIfEmptyPtr(src.RequestID))
	})
}

func remapPtrString(v *string, fn func(string) string) *string {
	if v == nil || *v == "" {
		return v
	}
	mapped := fn(*v)
	return &mapped
}

// ---------------------------------------------------------------------------
// credential_mask_audits — values are re-encrypted with the target's box.
// ---------------------------------------------------------------------------

func (s *importState) handleCredentialMaskAudits(body io.Reader, size int64) error {
	rep := s.report("credential_mask_audits")
	return s.processJSONL("credential_mask_audits", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[credentialMaskAuditRow](line)
		if err != nil {
			return err
		}
		if s.skipTraces[src.TraceID] {
			rep.SkippedTrace++
			return nil
		}
		original, err := s.view.TransferEncryptSecretString(src.OriginalValue)
		if err != nil {
			return fmt.Errorf("re-encrypt mask audit original value: %w", err)
		}
		standIn, err := s.view.TransferEncryptSecretString(src.StandInValue)
		if err != nil {
			return fmt.Errorf("re-encrypt mask audit stand-in value: %w", err)
		}
		mode := s.view.SecretStorageMode()
		exists, err := s.queryRowExists(`
			SELECT COUNT(*) FROM credential_mask_audits WHERE trace_id = ? AND event_index = ?`, src.TraceID, src.EventIndex)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO credential_mask_audits (id, trace_id, event_index, rule, vendor, kind, confidence, score, reasons, start_offset, end_offset, value_length, original_value, stand_in_value, storage_mode, detected_at, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			src.ID, src.TraceID, src.EventIndex, src.Rule, src.Vendor, src.Kind, src.Confidence, src.Score,
			src.Reasons, src.StartOffset, src.EndOffset, src.ValueLength, original, standIn, mode,
			src.DetectedAt, src.CreatedAt)
	})
}

// ---------------------------------------------------------------------------
// credential_mask_event_states
// ---------------------------------------------------------------------------

func (s *importState) handleCredentialMaskEventStates(body io.Reader, size int64) error {
	rep := s.report("credential_mask_event_states")
	return s.processJSONL("credential_mask_event_states", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[credentialMaskEventStateRow](line)
		if err != nil {
			return err
		}
		if s.skipTraces[src.TraceID] {
			rep.SkippedTrace++
			return nil
		}
		exists, err := s.queryRowExists(`SELECT COUNT(*) FROM credential_mask_event_states WHERE trace_id = ? AND rule = ?`, src.TraceID, src.Rule)
		if err != nil {
			return err
		}
		if exists {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Inserted++
		return s.exec(`
			INSERT INTO credential_mask_event_states (trace_id, rule, status, read_at, resolved_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			src.TraceID, src.Rule, orElse(src.Status, ""), nilIfEmptyPtr(src.ReadAt), nilIfEmptyPtr(src.ResolvedAt), src.UpdatedAt)
	})
}

func jsonOrNil(v *string) any {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	return *v
}
