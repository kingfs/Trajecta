package transfer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/kingfs/Trajecta/internal/store"
)

// importState carries everything one bundle import needs to thread through the
// per-table handlers: the channel id map, the dataset/eval/response id maps,
// the rows-skipped-by-conflict bookkeeping, the target view (tx-scoped in
// --apply mode) and the report accumulators.
type importState struct {
	ctx       context.Context
	view      *store.Store
	log       *Logger
	outputDir string
	apply     bool
	result    *ImportResult

	resolver *channelResolver

	datasetIDMap      map[string]string
	evalRunIDMap      map[string]string
	responseIDMap     map[string]string
	requestAuditIDMap map[string]string

	// skipTraces holds trace_ids whose logs row was skipped because the same
	// path exists in the target with a different trace_id. Derived/audit rows
	// that reference those traces are skipped with them.
	skipTraces map[string]bool
	// skipPaths holds paths whose logs row conflicted; upstream_exchanges rows
	// for those cassettes are skipped too.
	skipPaths map[string]bool

	byName map[string]*TableImportReport

	// manifest drives path rewriting and the --all cassette rule.
	manifest *Manifest
	// keepExistingFiles keeps a differing target cassette untouched.
	keepExistingFiles bool
	// desiredFiles maps bundle-relative cassette path -> recorded mtime and
	// whether the file must not be written (its index row was skipped).
	desiredFiles map[string]desiredFile
	// files accumulates the cassette outcomes.
	files FileImportReport
}

func newImportState(ctx context.Context, view *store.Store, log *Logger, outputDir string, apply bool, result *ImportResult, channelMap map[string]string) *importState {
	return &importState{
		ctx:               ctx,
		view:              view,
		log:               log,
		outputDir:         outputDir,
		apply:             apply,
		result:            result,
		resolver:          newChannelResolver(log, channelMap),
		datasetIDMap:      map[string]string{},
		evalRunIDMap:      map[string]string{},
		responseIDMap:     map[string]string{},
		requestAuditIDMap: map[string]string{},
		skipTraces:        map[string]bool{},
		skipPaths:         map[string]bool{},
		byName:            map[string]*TableImportReport{},
	}
}

func (s *importState) report(name string) *TableImportReport {
	r, ok := s.byName[name]
	if !ok {
		r = &TableImportReport{Name: name}
		s.byName[name] = r
	}
	return r
}

func (s *importState) flushTableReports() {
	s.result.Tables = make([]TableImportReport, 0, len(s.byName))
	for _, name := range tableExportOrder {
		if r, ok := s.byName[name]; ok {
			s.result.Tables = append(s.result.Tables, *r)
		}
	}
}

// processJSONL decodes one JSONL tar entry line by line, invoking fn per
// record. A decoder failure aborts the import; partial writes roll back with
// the surrounding transaction.
func (s *importState) processJSONL(name string, size int64, body io.Reader, fn func(line []byte) error) error {
	limited := io.LimitReader(body, size)
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 0, 64<<10), 256<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if err := fn(line); err != nil {
			return fmt.Errorf("import %s: %w", name, err)
		}
	}
	return scanner.Err()
}

func decodeRow[T any](line []byte) (T, error) {
	var row T
	err := json.Unmarshal(line, &row)
	return row, err
}

func (s *importState) exec(query string, args ...any) error {
	if !s.apply {
		return nil
	}
	_, err := s.view.TransferExec(s.ctx, query, args...)
	return err
}

func (s *importState) queryCount(query string, args ...any) (int64, error) {
	var n int64
	row := s.view.TransferQueryRow(s.ctx, query, args...)
	if err := row.Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// queryRowExists reports whether any row matches query (COUNT form).
func (s *importState) queryRowExists(query string, args ...any) (bool, error) {
	n, err := s.queryCount(query, args...)
	return n > 0, err
}

// maxTimeString returns the lexicographically larger RFC3339Nano timestamp;
// both inputs share one format, so lexical order is chronological. An empty
// string means "no timestamp" and never wins.
func maxTimeString(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a >= b {
		return a
	}
	return b
}

func minTimeString(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if a <= b {
		return a
	}
	return b
}

// orElse returns fallback when value is the zero string.
func orElse(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// orElseJSON returns fallback when value is "", "{}" or "[]" (the JSON defaults
// the store uses for empty payloads).
func orElseJSON(value, fallback string) string {
	if value == "" || value == "{}" || value == "[]" {
		return fallback
	}
	return value
}

// ---------------------------------------------------------------------------
// channel_configs — create/reuse decisions come from the resolver; matched
// channels keep the target row verbatim.
// ---------------------------------------------------------------------------

func (s *importState) handleChannelConfigs(body io.Reader, size int64, target []targetChannel) error {
	rep := s.report("channel_configs")
	targetByID, targetByKey := buildTargetMaps(target)
	// The resolver must see every source channel before any dependent table is
	// processed, which the tar order already guarantees (this is the first
	// entry).
	err := s.processJSONL("channel_configs", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[channelConfigRow](line)
		if err != nil {
			return err
		}
		dstID, existed, err := s.resolver.resolve(src, target, targetByID, targetByKey)
		if err != nil {
			return err
		}
		if existed {
			rep.SkippedUnchanged++
			return nil
		}
		// New channel: re-encrypt with the target's secret box.
		cipher, err := s.view.TransferEncryptSecretString(src.APIKey)
		if err != nil {
			return fmt.Errorf("re-encrypt api key for channel %q: %w", src.ID, err)
		}
		headers, err := s.view.TransferEncryptHeadersJSON(src.HeadersJSON)
		if err != nil {
			return fmt.Errorf("re-encrypt headers for channel %q: %w", src.ID, err)
		}
		rep.Inserted++
		s.result.ChannelsCreated = append(s.result.ChannelsCreated, dstID)
		return s.exec(`
			INSERT INTO channel_configs (
				id, name, description, source, base_url, provider_preset, protocol_family, routing_profile,
				api_version, deployment, project, location, model_resource, api_key_ciphertext, api_key_hint,
				headers_json, enabled, priority, weight, capacity_hint, model_discovery, allow_unknown_models,
				created_at, updated_at, last_probe_at, last_probe_status, last_probe_error,
				api_type, mode, capabilities_json, proxy_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			dstID, src.Name, src.Description, src.Source, src.BaseURL, src.ProviderPreset, src.ProtocolFamily,
			src.RoutingProfile, src.APIVersion, src.Deployment, src.Project, src.Location, src.ModelResource,
			nilIfEmpty(cipher), src.APIKeyHint, headers, src.Enabled, src.Priority, src.Weight, src.CapacityHint,
			src.ModelDiscovery, src.AllowUnknownModels, src.CreatedAt, src.UpdatedAt, nilIfEmptyPtr(src.LastProbeAt),
			src.LastProbeStatus, src.LastProbeError, src.APIType, src.Mode, src.CapabilitiesJSON, src.ProxyJSON,
		)
	})
	s.result.ChannelReuses = append(s.result.ChannelReuses, s.resolver.reused...)
	return err
}

func nilIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return []byte(v)
}

func nilIfEmptyPtr(v *string) any {
	if v == nil || *v == "" {
		return nil
	}
	return *v
}

// ---------------------------------------------------------------------------
// channel_models — union merge against (mapped channel_id, model). Existing
// target pairs keep operator intent for enabled/source/profile fields; NULL
// tri-state capability columns are filled from source.
// ---------------------------------------------------------------------------

func (s *importState) handleChannelModels(body io.Reader, size int64) error {
	rep := s.report("channel_models")
	return s.processJSONL("channel_models", size, body, func(line []byte) error {
		rep.Processed++
		src, err := decodeRow[channelModelRow](line)
		if err != nil {
			return err
		}
		src.ChannelID = s.resolver.remapID(src.ChannelID)
		existing, found, err := s.loadChannelModel(src.ChannelID, src.Model)
		if err != nil {
			return err
		}
		if !found {
			rep.Inserted++
			return s.exec(`
				INSERT INTO channel_models (
					channel_id, model, display_name, source, enabled,
					supports_responses, supports_chat_completions, supports_embeddings,
					context_window, max_output_tokens, compact_history_item_threshold,
					upstream_model, profile_source, profile_adoption_status,
					input_modalities_json, output_modalities_json, raw_model_json,
					protocols_json, first_seen_at, last_seen_at, last_probe_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				src.ChannelID, src.Model, src.DisplayName, src.Source, src.Enabled,
				nilIfInt64Ptr(src.SupportsResponses), nilIfInt64Ptr(src.SupportsChatCompletions), nilIfInt64Ptr(src.SupportsEmbeddings),
				nilIfInt64Ptr(src.ContextWindow), nilIfInt64Ptr(src.MaxOutputTokens), nilIfInt64Ptr(src.CompactHistoryItemThreshold),
				src.UpstreamModel, src.ProfileSource, src.ProfileAdoptionStatus,
				orElseJSON(src.InputModalitiesJSON, "[]"), orElseJSON(src.OutputModalitiesJSON, "[]"), orElseJSON(src.RawModelJSON, "{}"),
				orElseJSON(src.ProtocolsJSON, "{}"), src.FirstSeenAt, src.LastSeenAt, nilIfEmptyPtr(src.LastProbeAt),
			)
		}
		merged, changed := mergeChannelModel(existing, src)
		_ = merged
		if !changed {
			rep.SkippedUnchanged++
			return nil
		}
		rep.Updated++
		rep.MergedModels++
		return s.exec(`
			UPDATE channel_models SET
				display_name = ?, source = ?, enabled = ?,
				supports_responses = ?, supports_chat_completions = ?, supports_embeddings = ?,
				context_window = ?, max_output_tokens = ?, compact_history_item_threshold = ?,
				upstream_model = ?, profile_source = ?, profile_adoption_status = ?,
				input_modalities_json = ?, output_modalities_json = ?, raw_model_json = ?,
				protocols_json = ?, first_seen_at = ?, last_seen_at = ?, last_probe_at = ?
			WHERE channel_id = ? AND model = ?`,
			merged.DisplayName, merged.Source, merged.Enabled,
			nilIfInt64Ptr(merged.SupportsResponses), nilIfInt64Ptr(merged.SupportsChatCompletions), nilIfInt64Ptr(merged.SupportsEmbeddings),
			nilIfInt64Ptr(merged.ContextWindow), nilIfInt64Ptr(merged.MaxOutputTokens), nilIfInt64Ptr(merged.CompactHistoryItemThreshold),
			merged.UpstreamModel, merged.ProfileSource, merged.ProfileAdoptionStatus,
			merged.InputModalitiesJSON, merged.OutputModalitiesJSON, merged.RawModelJSON,
			merged.ProtocolsJSON, merged.FirstSeenAt, merged.LastSeenAt, nilIfEmptyPtr(merged.LastProbeAt),
			src.ChannelID, src.Model,
		)
	})
}

func (s *importState) loadChannelModel(channelID, model string) (channelModelRow, bool, error) {
	var r channelModelRow
	var id, enabled any
	var supportsResponses, supportsChat, supportsEmbeddings any
	var contextWindow, maxOutputTokens, compactThreshold any
	var firstSeenAt, lastSeenAt, lastProbeAt any
	row := s.view.TransferQueryRow(s.ctx, `
		SELECT id, channel_id, model, display_name, source, enabled,
			supports_responses, supports_chat_completions, supports_embeddings,
			context_window, max_output_tokens, compact_history_item_threshold,
			upstream_model, profile_source, profile_adoption_status,
			input_modalities_json, output_modalities_json, raw_model_json,
			protocols_json, first_seen_at, last_seen_at, last_probe_at
		FROM channel_models WHERE channel_id = ? AND model = ?`, channelID, model)
	err := row.Scan(&id, &r.ChannelID, &r.Model, &r.DisplayName, &r.Source, &enabled,
		&supportsResponses, &supportsChat, &supportsEmbeddings,
		&contextWindow, &maxOutputTokens, &compactThreshold,
		&r.UpstreamModel, &r.ProfileSource, &r.ProfileAdoptionStatus,
		&r.InputModalitiesJSON, &r.OutputModalitiesJSON, &r.RawModelJSON,
		&r.ProtocolsJSON, &firstSeenAt, &lastSeenAt, &lastProbeAt)
	if err != nil {
		return r, false, nil // sql.ErrNoRows and friends: treat as absent
	}
	r.ID = anyInt64(id)
	r.Enabled = anyBool(enabled)
	r.SupportsResponses = ptrInt64(supportsResponses)
	r.SupportsChatCompletions = ptrInt64(supportsChat)
	r.SupportsEmbeddings = ptrInt64(supportsEmbeddings)
	r.ContextWindow = ptrInt64(contextWindow)
	r.MaxOutputTokens = ptrInt64(maxOutputTokens)
	r.CompactHistoryItemThreshold = ptrInt64(compactThreshold)
	r.FirstSeenAt = anyTimeString(firstSeenAt)
	r.LastSeenAt = anyTimeString(lastSeenAt)
	r.LastProbeAt = ptrTimeString(lastProbeAt)
	return r, true, nil
}

func mergeChannelModel(dst, src channelModelRow) (channelModelRow, bool) {
	changed := false
	// The tri-state capability columns only ever fill from NULL: an explicit
	// target value is operator intent and must not be overwritten.
	mergePtr := func(dst **int64, src *int64) {
		if *dst == nil && src != nil {
			*dst = src
			changed = true
		}
	}
	mergePtr(&dst.SupportsResponses, src.SupportsResponses)
	mergePtr(&dst.SupportsChatCompletions, src.SupportsChatCompletions)
	mergePtr(&dst.SupportsEmbeddings, src.SupportsEmbeddings)
	mergePtr(&dst.ContextWindow, src.ContextWindow)
	mergePtr(&dst.MaxOutputTokens, src.MaxOutputTokens)
	mergePtr(&dst.CompactHistoryItemThreshold, src.CompactHistoryItemThreshold)
	if dst.UpstreamModel == "" && src.UpstreamModel != "" {
		dst.UpstreamModel = src.UpstreamModel
		changed = true
	}
	if dst.DisplayName == "" && src.DisplayName != "" {
		dst.DisplayName = src.DisplayName
		changed = true
	}
	if dst.ProfileSource == "" && src.ProfileSource != "" {
		dst.ProfileSource = src.ProfileSource
		changed = true
	}
	if dst.ProfileAdoptionStatus == "" && src.ProfileAdoptionStatus != "" {
		dst.ProfileAdoptionStatus = src.ProfileAdoptionStatus
		changed = true
	}
	mergeJSON := func(dst *string, src string) {
		if orElseJSON(*dst, "") == "" && orElseJSON(src, "") != "" {
			*dst = src
			changed = true
		}
	}
	mergeJSON(&dst.ProtocolsJSON, src.ProtocolsJSON)
	mergeJSON(&dst.InputModalitiesJSON, src.InputModalitiesJSON)
	mergeJSON(&dst.OutputModalitiesJSON, src.OutputModalitiesJSON)
	mergeJSON(&dst.RawModelJSON, src.RawModelJSON)
	if first := minTimeString(dst.FirstSeenAt, src.FirstSeenAt); first != dst.FirstSeenAt {
		dst.FirstSeenAt = first
		changed = true
	}
	if last := maxTimeString(dst.LastSeenAt, src.LastSeenAt); last != dst.LastSeenAt {
		dst.LastSeenAt = last
		changed = true
	}
	dstProbe, srcProbe := "", ""
	if dst.LastProbeAt != nil {
		dstProbe = *dst.LastProbeAt
	}
	if src.LastProbeAt != nil {
		srcProbe = *src.LastProbeAt
	}
	if mergedProbe := maxTimeString(dstProbe, srcProbe); mergedProbe != dstProbe {
		dst.LastProbeAt = &mergedProbe
		changed = true
	}
	return dst, changed
}

func nilIfInt64Ptr(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
