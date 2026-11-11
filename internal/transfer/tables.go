package transfer

// Row codecs for every table the bundle carries. Times cross the wire as
// RFC3339Nano strings (the same layout the store uses on disk); booleans and
// integers travel as JSON booleans/numbers; NULLs as JSON null. Each table is
// scanned into a typed struct with an explicit Scan function and written as
// line-delimited JSON (\n terminated). Secrets are decrypted before they
// reach this file: nothing here knows how to call the secret box.

import (
	"database/sql"
	"encoding/json"
	"time"
)

const exportTimeLayout = time.RFC3339Nano

func anyBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t == "true" || t == "1" || t == "t"
	case []byte:
		s := string(t)
		return s == "true" || s == "1" || s == "t"
	default:
		return false
	}
}

func anyInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case string:
		var out int64
		_ = json.Unmarshal([]byte(t), &out)
		return out
	case []byte:
		var out int64
		_ = json.Unmarshal(t, &out)
		return out
	default:
		return 0
	}
}

func anyFloat64(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case int:
		return float64(t)
	case string:
		var out float64
		_ = json.Unmarshal([]byte(t), &out)
		return out
	case []byte:
		var out float64
		_ = json.Unmarshal(t, &out)
		return out
	default:
		return 0
	}
}

func anyString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(exportTimeLayout)
	default:
		b, err := json.Marshal(t)
		if err == nil && string(b) != "null" {
			var s string
			if err := json.Unmarshal(b, &s); err == nil {
				return s
			}
		}
		return ""
	}
}

// anyTimeString normalises a driver time value to RFC3339Nano UTC text.
func anyTimeString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case time.Time:
		return t.UTC().Format(exportTimeLayout)
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return anyString(v)
	}
}

func ptrTimeString(v any) *string {
	switch t := v.(type) {
	case nil:
		return nil
	case sql.NullTime:
		if !t.Valid {
			return nil
		}
		s := t.Time.UTC().Format(exportTimeLayout)
		return &s
	case sql.NullString:
		if !t.Valid || t.String == "" {
			return nil
		}
		return &t.String
	case string:
		if t == "" {
			return nil
		}
		return &t
	case []byte:
		if len(t) == 0 {
			return nil
		}
		s := string(t)
		return &s
	case time.Time:
		if t.IsZero() {
			return nil
		}
		s := t.UTC().Format(exportTimeLayout)
		return &s
	default:
		return nil
	}
}

func ptrInt64(v any) *int64 {
	switch t := v.(type) {
	case nil:
		return nil
	case sql.NullInt64:
		if !t.Valid {
			return nil
		}
		return &t.Int64
	case int64:
		return &t
	default:
		n := anyInt64(v)
		return &n
	}
}

func ptrString(v any) *string { return ptrTimeString(v) }

// ---------------------------------------------------------------------------
// channel_configs
// ---------------------------------------------------------------------------

type channelConfigRow struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	Source         string `json:"source"`
	BaseURL        string `json:"base_url"`
	ProviderPreset string `json:"provider_preset"`
	Plan           string `json:"plan"`
	ProtocolFamily string `json:"protocol_family"`
	RoutingProfile string `json:"routing_profile"`
	APIVersion     string `json:"api_version"`
	Deployment     string `json:"deployment"`
	Project        string `json:"project"`
	Location       string `json:"location"`
	ModelResource  string `json:"model_resource"`
	APIKey         string `json:"api_key"` // plaintext, decrypted at export
	// apiKeyCipher carries the source ciphertext from scan to the decrypt
	// step; it is never serialised into the bundle.
	apiKeyCipher       []byte
	APIKeyHint         string  `json:"api_key_hint"`
	HeadersJSON        string  `json:"headers_json"` // decrypted secret members
	Enabled            bool    `json:"enabled"`
	Priority           int64   `json:"priority"`
	Weight             float64 `json:"weight"`
	CapacityHint       float64 `json:"capacity_hint"`
	ModelDiscovery     string  `json:"model_discovery"`
	AllowUnknownModels bool    `json:"allow_unknown_models"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	LastProbeAt        *string `json:"last_probe_at"`
	LastProbeStatus    string  `json:"last_probe_status"`
	LastProbeError     string  `json:"last_probe_error"`
	APIType            string  `json:"api_type"`
	Mode               string  `json:"mode"`
	CapabilitiesJSON   string  `json:"capabilities_json"`
	ProxyJSON          string  `json:"proxy_json"`
}

const channelConfigScan = `id, name, description, source, base_url, provider_preset, plan, protocol_family, routing_profile, api_version, deployment, project, location, model_resource, api_key_ciphertext, api_key_hint, headers_json, enabled, priority, weight, capacity_hint, model_discovery, allow_unknown_models, created_at, updated_at, last_probe_at, last_probe_status, last_probe_error, api_type, mode, capabilities_json, proxy_json`

func scanChannelConfig(rows *sql.Rows) (channelConfigRow, error) {
	var r channelConfigRow
	var enabled any
	var lastProbeAt any
	var apiKeyCipher []byte
	var allowUnknown any
	var createdAt any
	var updatedAt any
	err := rows.Scan(
		&r.ID, &r.Name, &r.Description, &r.Source, &r.BaseURL, &r.ProviderPreset, &r.Plan, &r.ProtocolFamily,
		&r.RoutingProfile, &r.APIVersion, &r.Deployment, &r.Project, &r.Location, &r.ModelResource,
		&apiKeyCipher, &r.APIKeyHint, &r.HeadersJSON,
		&enabled, &r.Priority, &r.Weight, &r.CapacityHint, &r.ModelDiscovery, &allowUnknown,
		&createdAt, &updatedAt, &lastProbeAt,
		&r.LastProbeStatus, &r.LastProbeError, &r.APIType, &r.Mode, &r.CapabilitiesJSON, &r.ProxyJSON,
	)
	if err != nil {
		return r, err
	}
	r.Enabled = anyBool(enabled)
	r.AllowUnknownModels = anyBool(allowUnknown)
	r.CreatedAt = anyTimeString(createdAt)
	r.UpdatedAt = anyTimeString(updatedAt)
	r.LastProbeAt = ptrTimeString(lastProbeAt)
	r.apiKeyCipher = apiKeyCipher
	return r, nil
}

// ---------------------------------------------------------------------------
// channel_models
// ---------------------------------------------------------------------------

type channelModelRow struct {
	ID                          int64   `json:"id"` // dropped at import, identity reassigned
	ChannelID                   string  `json:"channel_id"`
	Model                       string  `json:"model"`
	DisplayName                 string  `json:"display_name"`
	Source                      string  `json:"source"`
	Enabled                     bool    `json:"enabled"`
	SupportsResponses           *int64  `json:"supports_responses"`
	SupportsChatCompletions     *int64  `json:"supports_chat_completions"`
	SupportsEmbeddings          *int64  `json:"supports_embeddings"`
	ContextWindow               *int64  `json:"context_window"`
	MaxOutputTokens             *int64  `json:"max_output_tokens"`
	CompactHistoryItemThreshold *int64  `json:"compact_history_item_threshold"`
	UpstreamModel               string  `json:"upstream_model"`
	ProfileSource               string  `json:"profile_source"`
	ProfileAdoptionStatus       string  `json:"profile_adoption_status"`
	InputModalitiesJSON         string  `json:"input_modalities_json"`
	OutputModalitiesJSON        string  `json:"output_modalities_json"`
	RawModelJSON                string  `json:"raw_model_json"`
	ProtocolsJSON               string  `json:"protocols_json"`
	FirstSeenAt                 string  `json:"first_seen_at"`
	LastSeenAt                  string  `json:"last_seen_at"`
	LastProbeAt                 *string `json:"last_probe_at"`
}

func scanChannelModel(rows *sql.Rows) (channelModelRow, error) {
	var r channelModelRow
	var id, enabled any
	var supportsResponses, supportsChat, supportsEmbeddings any
	var contextWindow, maxOutputTokens, compactThreshold any
	var firstSeenAt, lastSeenAt, lastProbeAt any
	err := rows.Scan(
		&id, &r.ChannelID, &r.Model, &r.DisplayName, &r.Source, &enabled,
		&supportsResponses, &supportsChat, &supportsEmbeddings,
		&contextWindow, &maxOutputTokens, &compactThreshold,
		&r.UpstreamModel, &r.ProfileSource, &r.ProfileAdoptionStatus,
		&r.InputModalitiesJSON, &r.OutputModalitiesJSON, &r.RawModelJSON, &r.ProtocolsJSON,
		&firstSeenAt, &lastSeenAt, &lastProbeAt,
	)
	if err != nil {
		return r, err
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
	return r, nil
}

// ---------------------------------------------------------------------------
// model_aliases
// ---------------------------------------------------------------------------

type modelAliasRow struct {
	ID          string `json:"id"`
	Alias       string `json:"alias"`
	TargetModel string `json:"target_model"`
	ChannelID   string `json:"channel_id"`
	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
	Source      string `json:"source"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

const modelAliasScan = `id, alias, target_model, channel_id, enabled, description, source, created_at, updated_at`

func scanModelAlias(rows *sql.Rows) (modelAliasRow, error) {
	var r modelAliasRow
	var enabled any
	var createdAt, updatedAt any
	err := rows.Scan(&r.ID, &r.Alias, &r.TargetModel, &r.ChannelID, &enabled, &r.Description, &r.Source, &createdAt, &updatedAt)
	if err != nil {
		return r, err
	}
	r.Enabled = anyBool(enabled)
	r.CreatedAt = anyTimeString(createdAt)
	r.UpdatedAt = anyTimeString(updatedAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// model_catalog
// ---------------------------------------------------------------------------

type modelCatalogRow struct {
	Model       string  `json:"model"`
	DisplayName string  `json:"display_name"`
	Family      string  `json:"family"`
	Vendor      string  `json:"vendor"`
	Description string  `json:"description"`
	TagsJSON    string  `json:"tags_json"`
	FirstSeenAt string  `json:"first_seen_at"`
	LastSeenAt  string  `json:"last_seen_at"`
	LastUsedAt  *string `json:"last_used_at"`
}

const modelCatalogScan = `model, display_name, family, vendor, description, tags_json, first_seen_at, last_seen_at, last_used_at`

func scanModelCatalog(rows *sql.Rows) (modelCatalogRow, error) {
	var r modelCatalogRow
	var firstSeenAt, lastSeenAt, lastUsedAt any
	err := rows.Scan(&r.Model, &r.DisplayName, &r.Family, &r.Vendor, &r.Description, &r.TagsJSON, &firstSeenAt, &lastSeenAt, &lastUsedAt)
	if err != nil {
		return r, err
	}
	r.FirstSeenAt = anyTimeString(firstSeenAt)
	r.LastSeenAt = anyTimeString(lastSeenAt)
	r.LastUsedAt = ptrTimeString(lastUsedAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// app_settings (whitelist enforced at import; export keeps every row and lets
// the import filter)
// ---------------------------------------------------------------------------

type appSettingRow struct {
	Key       string `json:"setting_key"`
	ValueJSON string `json:"value_json"`
	UpdatedAt string `json:"updated_at"`
}

const appSettingScan = `setting_key, value_json, updated_at`

func scanAppSetting(rows *sql.Rows) (appSettingRow, error) {
	var r appSettingRow
	var updatedAt any
	err := rows.Scan(&r.Key, &r.ValueJSON, &updatedAt)
	if err != nil {
		return r, err
	}
	r.UpdatedAt = anyTimeString(updatedAt)
	return r, nil
}

// appSettingsExportWhitelist is the only set of keys the import accepts.
// network.proxy and telemetry/repair markers are deliberately excluded: the
// first is environment-specific egress state, the others would corrupt the
// target deployment's repair bookkeeping.
var appSettingsImportWhitelist = map[string]bool{
	"routing.settings": true,
	"rates.alerts":     true,
	"responses.function_executors.config_snapshot": true,
	// Channel plugin enablement is configuration, not environment-specific
	// egress state; import inserts it only when the target has no row yet.
	"plugins.channels": true,
}

// ---------------------------------------------------------------------------
// channel_probe_runs
// ---------------------------------------------------------------------------

type channelProbeRunRow struct {
	ID                 string  `json:"id"`
	ChannelID          string  `json:"channel_id"`
	Status             string  `json:"status"`
	StartedAt          string  `json:"started_at"`
	CompletedAt        *string `json:"completed_at"`
	DurationMs         int64   `json:"duration_ms"`
	DiscoveredCount    int64   `json:"discovered_count"`
	EnabledCount       int64   `json:"enabled_count"`
	Endpoint           string  `json:"endpoint"`
	StatusCode         int64   `json:"status_code"`
	ErrorText          string  `json:"error_text"`
	RequestMetaJSON    string  `json:"request_meta_json"`
	ResponseSampleJSON string  `json:"response_sample_json"`
}

const channelProbeRunScan = `id, channel_id, status, started_at, completed_at, duration_ms, discovered_count, enabled_count, endpoint, status_code, error_text, request_meta_json, response_sample_json`

func scanChannelProbeRun(rows *sql.Rows) (channelProbeRunRow, error) {
	var r channelProbeRunRow
	var startedAt, completedAt any
	err := rows.Scan(&r.ID, &r.ChannelID, &r.Status, &startedAt, &completedAt, &r.DurationMs, &r.DiscoveredCount, &r.EnabledCount, &r.Endpoint, &r.StatusCode, &r.ErrorText, &r.RequestMetaJSON, &r.ResponseSampleJSON)
	if err != nil {
		return r, err
	}
	r.StartedAt = anyTimeString(startedAt)
	r.CompletedAt = ptrTimeString(completedAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// datasets / eval_runs / experiment_runs / dataset_examples / scores
// ---------------------------------------------------------------------------

type datasetRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

const datasetScan = `id, name, description, created_at, updated_at`

func scanDataset(rows *sql.Rows) (datasetRow, error) {
	var r datasetRow
	var createdAt, updatedAt any
	err := rows.Scan(&r.ID, &r.Name, &r.Description, &createdAt, &updatedAt)
	if err != nil {
		return r, err
	}
	r.CreatedAt = anyTimeString(createdAt)
	r.UpdatedAt = anyTimeString(updatedAt)
	return r, nil
}

type evalRunRow struct {
	ID           string `json:"id"`
	DatasetID    string `json:"dataset_id"`
	SourceType   string `json:"source_type"`
	SourceID     string `json:"source_id"`
	EvaluatorSet string `json:"evaluator_set"`
	CreatedAt    string `json:"created_at"`
	CompletedAt  string `json:"completed_at"`
	TraceCount   int64  `json:"trace_count"`
	ScoreCount   int64  `json:"score_count"`
	PassCount    int64  `json:"pass_count"`
	FailCount    int64  `json:"fail_count"`
}

const evalRunScan = `id, dataset_id, source_type, source_id, evaluator_set, created_at, completed_at, trace_count, score_count, pass_count, fail_count`

func scanEvalRun(rows *sql.Rows) (evalRunRow, error) {
	var r evalRunRow
	var createdAt, completedAt any
	err := rows.Scan(&r.ID, &r.DatasetID, &r.SourceType, &r.SourceID, &r.EvaluatorSet, &createdAt, &completedAt, &r.TraceCount, &r.ScoreCount, &r.PassCount, &r.FailCount)
	if err != nil {
		return r, err
	}
	r.CreatedAt = anyTimeString(createdAt)
	r.CompletedAt = anyTimeString(completedAt)
	return r, nil
}

type experimentRunRow struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	Description         string  `json:"description"`
	BaselineEvalRunID   string  `json:"baseline_eval_run_id"`
	CandidateEvalRunID  string  `json:"candidate_eval_run_id"`
	CreatedAt           string  `json:"created_at"`
	BaselineScoreCount  int64   `json:"baseline_score_count"`
	CandidateScoreCount int64   `json:"candidate_score_count"`
	BaselinePassRate    float64 `json:"baseline_pass_rate"`
	CandidatePassRate   float64 `json:"candidate_pass_rate"`
	PassRateDelta       float64 `json:"pass_rate_delta"`
	MatchedScoreCount   int64   `json:"matched_score_count"`
	ImprovementCount    int64   `json:"improvement_count"`
	RegressionCount     int64   `json:"regression_count"`
}

const experimentRunScan = `id, name, description, baseline_eval_run_id, candidate_eval_run_id, created_at, baseline_score_count, candidate_score_count, baseline_pass_rate, candidate_pass_rate, pass_rate_delta, matched_score_count, improvement_count, regression_count`

func scanExperimentRun(rows *sql.Rows) (experimentRunRow, error) {
	var r experimentRunRow
	var createdAt any
	err := rows.Scan(&r.ID, &r.Name, &r.Description, &r.BaselineEvalRunID, &r.CandidateEvalRunID, &createdAt, &r.BaselineScoreCount, &r.CandidateScoreCount, &r.BaselinePassRate, &r.CandidatePassRate, &r.PassRateDelta, &r.MatchedScoreCount, &r.ImprovementCount, &r.RegressionCount)
	if err != nil {
		return r, err
	}
	r.CreatedAt = anyTimeString(createdAt)
	return r, nil
}

type datasetExampleRow struct {
	ID         int64  `json:"id"` // dropped at import, identity reassigned
	DatasetID  string `json:"dataset_id"`
	TraceID    string `json:"trace_id"`
	Position   int64  `json:"position"`
	AddedAt    string `json:"added_at"`
	SourceType string `json:"source_type"`
	SourceID   string `json:"source_id"`
	Note       string `json:"note"`
}

const datasetExampleScan = `id, dataset_id, trace_id, position, added_at, source_type, source_id, note`

func scanDatasetExample(rows *sql.Rows) (datasetExampleRow, error) {
	var r datasetExampleRow
	var id, position any
	var addedAt any
	err := rows.Scan(&id, &r.DatasetID, &r.TraceID, &position, &addedAt, &r.SourceType, &r.SourceID, &r.Note)
	if err != nil {
		return r, err
	}
	r.ID = anyInt64(id)
	r.Position = anyInt64(position)
	r.AddedAt = anyTimeString(addedAt)
	return r, nil
}

type scoreRow struct {
	ID           string  `json:"id"`
	TraceID      string  `json:"trace_id"`
	SessionID    string  `json:"session_id"`
	DatasetID    string  `json:"dataset_id"`
	EvalRunID    string  `json:"eval_run_id"`
	EvaluatorKey string  `json:"evaluator_key"`
	Value        float64 `json:"value"`
	Status       string  `json:"status"`
	Label        string  `json:"label"`
	Explanation  string  `json:"explanation"`
	CreatedAt    string  `json:"created_at"`
}

const scoreScan = `id, trace_id, session_id, dataset_id, eval_run_id, evaluator_key, value, status, label, explanation, created_at`

func scanScore(rows *sql.Rows) (scoreRow, error) {
	var r scoreRow
	var createdAt any
	err := rows.Scan(&r.ID, &r.TraceID, &r.SessionID, &r.DatasetID, &r.EvalRunID, &r.EvaluatorKey, &r.Value, &r.Status, &r.Label, &r.Explanation, &createdAt)
	if err != nil {
		return r, err
	}
	r.CreatedAt = anyTimeString(createdAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// responses / response_items
// ---------------------------------------------------------------------------

// jsonb columns cross the wire as raw JSON strings; an empty string is not a
// legal jsonb value, so NULL becomes "" on export and is treated as SQL NULL
// on import (the payload column is NOT NULL and is never empty in practice).

type responseRow struct {
	ID                 string  `json:"id"`
	ConversationID     string  `json:"conversation_id"`
	PreviousResponseID string  `json:"previous_response_id"`
	Status             string  `json:"status"`
	Model              string  `json:"model"`
	HistoryItemIDs     *string `json:"history_item_ids"`
	OutputItemIDs      *string `json:"output_item_ids"`
	EffectiveTools     *string `json:"effective_tools"`
	Metadata           *string `json:"metadata"`
	Usage              *string `json:"usage"`
	Error              *string `json:"error"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
}

const responseScan = `id, conversation_id, previous_response_id, status, model, history_item_ids, output_item_ids, effective_tools, metadata, usage, error, created_at, updated_at`

func scanResponse(rows *sql.Rows) (responseRow, error) {
	var r responseRow
	var createdAt, updatedAt any
	var historyItemIDs, outputItemIDs, effectiveTools, metadata, usage, responseError any
	err := rows.Scan(&r.ID, &r.ConversationID, &r.PreviousResponseID, &r.Status, &r.Model, &historyItemIDs, &outputItemIDs, &effectiveTools, &metadata, &usage, &responseError, &createdAt, &updatedAt)
	if err != nil {
		return r, err
	}
	r.CreatedAt = anyTimeString(createdAt)
	r.UpdatedAt = anyTimeString(updatedAt)
	r.HistoryItemIDs = ptrString(historyItemIDs)
	r.OutputItemIDs = ptrString(outputItemIDs)
	r.EffectiveTools = ptrString(effectiveTools)
	r.Metadata = ptrString(metadata)
	r.Usage = ptrString(usage)
	r.Error = ptrString(responseError)
	return r, nil
}

type responseItemRow struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ResponseID     string `json:"response_id"`
	ConversationID string `json:"conversation_id"`
	Payload        string `json:"payload"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

const responseItemScan = `id, kind, response_id, conversation_id, payload, created_at, updated_at`

func scanResponseItem(rows *sql.Rows) (responseItemRow, error) {
	var r responseItemRow
	var payload any
	var createdAt, updatedAt any
	err := rows.Scan(&r.ID, &r.Kind, &r.ResponseID, &r.ConversationID, &payload, &createdAt, &updatedAt)
	if err != nil {
		return r, err
	}
	r.Payload = anyString(payload)
	r.CreatedAt = anyTimeString(createdAt)
	r.UpdatedAt = anyTimeString(updatedAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// request_audits / execution_events / tool_call_audits
// ---------------------------------------------------------------------------

type requestAuditRow struct {
	ID              string  `json:"id"`
	ResponseID      *string `json:"response_id"`
	ConversationID  *string `json:"conversation_id"`
	Method          string  `json:"method"`
	Path            string  `json:"path"`
	ClientRequestID *string `json:"client_request_id"`
	HeaderJSON      *string `json:"header_json"`
	BodyPreview     *string `json:"body_preview"`
	BodySHA256      *string `json:"body_sha256"`
	RedactionJSON   *string `json:"redaction_json"`
	Status          string  `json:"status"`
	ErrorText       *string `json:"error_text"`
	CreatedAt       string  `json:"created_at"`
}

const requestAuditScan = `id, response_id, conversation_id, method, path, client_request_id, header_json, body_preview, body_sha256, redaction_json, status, error_text, created_at`

func scanRequestAudit(rows *sql.Rows) (requestAuditRow, error) {
	var r requestAuditRow
	var createdAt any
	var responseID, conversationID, clientRequestID, headerJSON, bodyPreview, bodySHA256, redactionJSON, errorText any
	err := rows.Scan(&r.ID, &responseID, &conversationID, &r.Method, &r.Path, &clientRequestID, &headerJSON, &bodyPreview, &bodySHA256, &redactionJSON, &r.Status, &errorText, &createdAt)
	if err != nil {
		return r, err
	}
	r.ResponseID = ptrString(responseID)
	r.ConversationID = ptrString(conversationID)
	r.ClientRequestID = ptrString(clientRequestID)
	r.BodyPreview = ptrString(bodyPreview)
	r.BodySHA256 = ptrString(bodySHA256)
	r.ErrorText = ptrString(errorText)
	r.HeaderJSON = ptrString(headerJSON)
	r.RedactionJSON = ptrString(redactionJSON)
	r.CreatedAt = anyTimeString(createdAt)
	return r, nil
}

type executionEventRow struct {
	ID             string  `json:"id"`
	ResponseID     *string `json:"response_id"`
	RequestAuditID *string `json:"request_audit_id"`
	ConversationID *string `json:"conversation_id"`
	EventType      string  `json:"event_type"`
	Phase          string  `json:"phase"`
	Status         string  `json:"status"`
	Message        *string `json:"message"`
	DetailsJSON    *string `json:"details_json"`
	OccurredAt     string  `json:"occurred_at"`
}

const executionEventScan = `id, response_id, request_audit_id, conversation_id, event_type, phase, status, message, details_json, occurred_at`

func scanExecutionEvent(rows *sql.Rows) (executionEventRow, error) {
	var r executionEventRow
	var occurredAt, detailsJSON any
	var responseID, requestAuditID, conversationID, message any
	err := rows.Scan(&r.ID, &responseID, &requestAuditID, &conversationID, &r.EventType, &r.Phase, &r.Status, &message, &detailsJSON, &occurredAt)
	if err != nil {
		return r, err
	}
	r.ResponseID = ptrString(responseID)
	r.RequestAuditID = ptrString(requestAuditID)
	r.ConversationID = ptrString(conversationID)
	r.Message = ptrString(message)
	r.DetailsJSON = ptrString(detailsJSON)
	r.OccurredAt = anyTimeString(occurredAt)
	return r, nil
}

type toolCallAuditRow struct {
	ID             string  `json:"id"`
	ResponseID     *string `json:"response_id"`
	RequestAuditID *string `json:"request_audit_id"`
	ConversationID *string `json:"conversation_id"`
	CallID         string  `json:"call_id"`
	ToolType       string  `json:"tool_type"`
	ToolName       *string `json:"tool_name"`
	Executor       *string `json:"executor"`
	Status         string  `json:"status"`
	Phase          string  `json:"phase"`
	InputJSON      *string `json:"input_json"`
	OutputJSON     *string `json:"output_json"`
	ErrorText      *string `json:"error_text"`
	MetadataJSON   *string `json:"metadata_json"`
	StartedAt      *string `json:"started_at"`
	CompletedAt    *string `json:"completed_at"`
	CreatedAt      string  `json:"created_at"`
}

const toolCallAuditScan = `id, response_id, request_audit_id, conversation_id, call_id, tool_type, tool_name, executor, status, phase, input_json, output_json, error_text, metadata_json, started_at, completed_at, created_at`

func scanToolCallAudit(rows *sql.Rows) (toolCallAuditRow, error) {
	var r toolCallAuditRow
	var createdAt, startedAt, completedAt any
	var inputJSON, outputJSON, metadataJSON any
	var responseID, requestAuditID, conversationID, toolName, executor, errorText any
	err := rows.Scan(&r.ID, &responseID, &requestAuditID, &conversationID, &r.CallID, &r.ToolType, &toolName, &executor, &r.Status, &r.Phase, &inputJSON, &outputJSON, &errorText, &metadataJSON, &startedAt, &completedAt, &createdAt)
	if err != nil {
		return r, err
	}
	r.ResponseID = ptrString(responseID)
	r.RequestAuditID = ptrString(requestAuditID)
	r.ConversationID = ptrString(conversationID)
	r.ToolName = ptrString(toolName)
	r.Executor = ptrString(executor)
	r.ErrorText = ptrString(errorText)
	r.InputJSON = ptrString(inputJSON)
	r.OutputJSON = ptrString(outputJSON)
	r.MetadataJSON = ptrString(metadataJSON)
	r.StartedAt = ptrTimeString(startedAt)
	r.CompletedAt = ptrTimeString(completedAt)
	r.CreatedAt = anyTimeString(createdAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// system_events
// ---------------------------------------------------------------------------

type systemEventRow struct {
	ID              string  `json:"id"`
	Fingerprint     string  `json:"fingerprint"`
	Source          string  `json:"source"`
	Category        string  `json:"category"`
	Severity        string  `json:"severity"`
	Status          string  `json:"status"`
	Title           string  `json:"title"`
	Message         string  `json:"message"`
	DetailsJSON     string  `json:"details_json"`
	TraceID         string  `json:"trace_id"`
	SessionID       string  `json:"session_id"`
	JobID           string  `json:"job_id"`
	UpstreamID      string  `json:"upstream_id"`
	Model           string  `json:"model"`
	OccurrenceCount int64   `json:"occurrence_count"`
	FirstSeenAt     string  `json:"first_seen_at"`
	LastSeenAt      string  `json:"last_seen_at"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
	ReadAt          *string `json:"read_at"`
	ResolvedAt      *string `json:"resolved_at"`
}

const systemEventScan = `id, fingerprint, source, category, severity, status, title, message, details_json, trace_id, session_id, job_id, upstream_id, model, occurrence_count, first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at`

func scanSystemEvent(rows *sql.Rows) (systemEventRow, error) {
	var r systemEventRow
	var occurrenceCount any
	var firstSeenAt, lastSeenAt, createdAt, updatedAt, readAt, resolvedAt any
	err := rows.Scan(&r.ID, &r.Fingerprint, &r.Source, &r.Category, &r.Severity, &r.Status, &r.Title, &r.Message, &r.DetailsJSON, &r.TraceID, &r.SessionID, &r.JobID, &r.UpstreamID, &r.Model, &occurrenceCount, &firstSeenAt, &lastSeenAt, &createdAt, &updatedAt, &readAt, &resolvedAt)
	if err != nil {
		return r, err
	}
	r.OccurrenceCount = anyInt64(occurrenceCount)
	r.FirstSeenAt = anyTimeString(firstSeenAt)
	r.LastSeenAt = anyTimeString(lastSeenAt)
	r.CreatedAt = anyTimeString(createdAt)
	r.UpdatedAt = anyTimeString(updatedAt)
	r.ReadAt = ptrTimeString(readAt)
	r.ResolvedAt = ptrTimeString(resolvedAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// analysis_runs (identity id dropped at import)
// ---------------------------------------------------------------------------

type analysisRunRow struct {
	ID              int64  `json:"id"`
	TraceID         string `json:"trace_id"`
	SessionID       string `json:"session_id"`
	Kind            string `json:"kind"`
	Analyzer        string `json:"analyzer"`
	AnalyzerVersion string `json:"analyzer_version"`
	Model           string `json:"model"`
	InputRef        string `json:"input_ref"`
	OutputJSON      string `json:"output_json"`
	Status          string `json:"status"`
	CreatedAt       string `json:"created_at"`
}

const analysisRunScan = `id, trace_id, session_id, kind, analyzer, analyzer_version, model, input_ref, output_json, status, created_at`

func scanAnalysisRun(rows *sql.Rows) (analysisRunRow, error) {
	var r analysisRunRow
	var id any
	var createdAt any
	err := rows.Scan(&id, &r.TraceID, &r.SessionID, &r.Kind, &r.Analyzer, &r.AnalyzerVersion, &r.Model, &r.InputRef, &r.OutputJSON, &r.Status, &createdAt)
	if err != nil {
		return r, err
	}
	r.ID = anyInt64(id)
	r.CreatedAt = anyTimeString(createdAt)
	return r, nil
}

// ---------------------------------------------------------------------------
// logs (57 columns)
// ---------------------------------------------------------------------------

type logRow struct {
	Path                           string  `json:"path"`
	TraceID                        string  `json:"trace_id"`
	ModTimeNs                      int64   `json:"mod_time_ns"`
	FileSize                       int64   `json:"file_size"`
	Version                        string  `json:"version"`
	RequestID                      string  `json:"request_id"`
	RecordedAt                     string  `json:"recorded_at"`
	Model                          string  `json:"model"`
	Provider                       string  `json:"provider"`
	Operation                      string  `json:"operation"`
	Endpoint                       string  `json:"endpoint"`
	URL                            string  `json:"url"`
	Method                         string  `json:"method"`
	StatusCode                     int64   `json:"status_code"`
	DurationMs                     int64   `json:"duration_ms"`
	TTFTMs                         int64   `json:"ttft_ms"`
	ClientIP                       string  `json:"client_ip"`
	ContentLength                  int64   `json:"content_length"`
	ErrorText                      string  `json:"error_text"`
	PromptTokens                   int64   `json:"prompt_tokens"`
	CompletionTokens               int64   `json:"completion_tokens"`
	TotalTokens                    int64   `json:"total_tokens"`
	CachedTokens                   int64   `json:"cached_tokens"`
	ReqHeaderLen                   int64   `json:"req_header_len"`
	ReqBodyLen                     int64   `json:"req_body_len"`
	ResHeaderLen                   int64   `json:"res_header_len"`
	ResBodyLen                     int64   `json:"res_body_len"`
	IsStream                       bool    `json:"is_stream"`
	SessionID                      string  `json:"session_id"`
	SessionSource                  string  `json:"session_source"`
	WindowID                       string  `json:"window_id"`
	ClientRequestID                string  `json:"client_request_id"`
	RequestAuditID                 string  `json:"request_audit_id"`
	ResponseID                     string  `json:"response_id"`
	ExchangeID                     string  `json:"exchange_id"`
	ExchangeKind                   string  `json:"exchange_kind"`
	ExchangeRole                   string  `json:"exchange_role"`
	ParentExchangeID               string  `json:"parent_exchange_id"`
	SequenceIndex                  int64   `json:"sequence_index"`
	SelectedUpstreamID             string  `json:"selected_upstream_id"`
	SelectedUpstreamBaseURL        string  `json:"selected_upstream_base_url"`
	SelectedUpstreamProviderPreset string  `json:"selected_upstream_provider_preset"`
	RoutingPolicy                  string  `json:"routing_policy"`
	RoutingScore                   float64 `json:"routing_score"`
	RoutingCandidateCount          int64   `json:"routing_candidate_count"`
	RoutingFailureReason           string  `json:"routing_failure_reason"`
	RouteTargetID                  string  `json:"route_target_id"`
	ChannelID                      string  `json:"channel_id"`
	CredentialID                   string  `json:"credential_id"`
	StickyStatus                   string  `json:"sticky_status"`
	StickyPreviousUpstreamID       string  `json:"sticky_previous_upstream_id"`
	ClientHarness                  string  `json:"client_harness"`
	ClientHarnessName              string  `json:"client_harness_name"`
	ClientHarnessVersion           string  `json:"client_harness_version"`
	ClientHarnessKind              string  `json:"client_harness_kind"`
	ClientHarnessConfidence        string  `json:"client_harness_confidence"`
	ClientHarnessTagsJSON          string  `json:"client_harness_tags_json"`
}

const logScanColumns = `path, trace_id, mod_time_ns, file_size, version, request_id, recorded_at, model, provider, operation, endpoint, url, method, status_code, duration_ms, ttft_ms, client_ip, content_length, error_text, prompt_tokens, completion_tokens, total_tokens, cached_tokens, req_header_len, req_body_len, res_header_len, res_body_len, is_stream, session_id, session_source, window_id, client_request_id, request_audit_id, response_id, exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index, selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset, routing_policy, routing_score, routing_candidate_count, routing_failure_reason, route_target_id, channel_id, credential_id, sticky_status, sticky_previous_upstream_id, client_harness, client_harness_name, client_harness_version, client_harness_kind, client_harness_confidence, client_harness_tags_json`

// logInsertColumns is every column except trace_id, for the ON CONFLICT
// update path (trace_id never changes on an existing path) and for inserts
// when the target has no row for the path.
const logInsertColumns = logScanColumns

func scanLog(rows *sql.Rows) (logRow, error) {
	var r logRow
	var modTimeNs, fileSize, statusCode, durationMs, ttftMs, contentLength any
	var promptTokens, completionTokens, totalTokens, cachedTokens any
	var reqHeaderLen, reqBodyLen, resHeaderLen, resBodyLen any
	var isStream, sequenceIndex, routingCandidateCount any
	var routingScore any
	var recordedAt any
	err := rows.Scan(
		&r.Path, &r.TraceID, &modTimeNs, &fileSize, &r.Version, &r.RequestID, &recordedAt,
		&r.Model, &r.Provider, &r.Operation, &r.Endpoint, &r.URL, &r.Method,
		&statusCode, &durationMs, &ttftMs, &r.ClientIP, &contentLength, &r.ErrorText,
		&promptTokens, &completionTokens, &totalTokens, &cachedTokens,
		&reqHeaderLen, &reqBodyLen, &resHeaderLen, &resBodyLen, &isStream,
		&r.SessionID, &r.SessionSource, &r.WindowID, &r.ClientRequestID,
		&r.RequestAuditID, &r.ResponseID,
		&r.ExchangeID, &r.ExchangeKind, &r.ExchangeRole, &r.ParentExchangeID, &sequenceIndex,
		&r.SelectedUpstreamID, &r.SelectedUpstreamBaseURL, &r.SelectedUpstreamProviderPreset,
		&r.RoutingPolicy, &routingScore, &routingCandidateCount, &r.RoutingFailureReason,
		&r.RouteTargetID, &r.ChannelID, &r.CredentialID, &r.StickyStatus, &r.StickyPreviousUpstreamID,
		&r.ClientHarness, &r.ClientHarnessName, &r.ClientHarnessVersion, &r.ClientHarnessKind,
		&r.ClientHarnessConfidence, &r.ClientHarnessTagsJSON,
	)
	if err != nil {
		return r, err
	}
	r.ModTimeNs = anyInt64(modTimeNs)
	r.FileSize = anyInt64(fileSize)
	r.RecordedAt = anyTimeString(recordedAt)
	r.StatusCode = anyInt64(statusCode)
	r.DurationMs = anyInt64(durationMs)
	r.TTFTMs = anyInt64(ttftMs)
	r.ContentLength = anyInt64(contentLength)
	r.PromptTokens = anyInt64(promptTokens)
	r.CompletionTokens = anyInt64(completionTokens)
	r.TotalTokens = anyInt64(totalTokens)
	r.CachedTokens = anyInt64(cachedTokens)
	r.ReqHeaderLen = anyInt64(reqHeaderLen)
	r.ReqBodyLen = anyInt64(reqBodyLen)
	r.ResHeaderLen = anyInt64(resHeaderLen)
	r.ResBodyLen = anyInt64(resBodyLen)
	r.IsStream = anyBool(isStream)
	r.SequenceIndex = anyInt64(sequenceIndex)
	r.RoutingScore = anyFloat64(routingScore)
	r.RoutingCandidateCount = anyInt64(routingCandidateCount)
	return r, nil
}

// ---------------------------------------------------------------------------
// upstream_exchanges
// ---------------------------------------------------------------------------

type upstreamExchangeRow struct {
	ID               string  `json:"id"`
	ResponseID       *string `json:"response_id"`
	RequestAuditID   *string `json:"request_audit_id"`
	TraceID          *string `json:"trace_id"`
	CassettePath     *string `json:"cassette_path"`
	UpstreamID       *string `json:"upstream_id"`
	RouteTarget      *string `json:"route_target"`
	Model            *string `json:"model"`
	Endpoint         *string `json:"endpoint"`
	StatusCode       *int64  `json:"status_code"`
	StartedAt        *string `json:"started_at"`
	CompletedAt      *string `json:"completed_at"`
	ErrorText        *string `json:"error_text"`
	ExchangeID       *string `json:"exchange_id"`
	ExchangeKind     *string `json:"exchange_kind"`
	ExchangeRole     *string `json:"exchange_role"`
	ParentExchangeID *string `json:"parent_exchange_id"`
	SequenceIndex    *int64  `json:"sequence_index"`
	RequestID        *string `json:"request_id"`
}

const upstreamExchangeScan = `id, response_id, request_audit_id, trace_id, cassette_path, upstream_id, route_target, model, endpoint, status_code, started_at, completed_at, error_text, exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index, request_id`

func scanUpstreamExchange(rows *sql.Rows) (upstreamExchangeRow, error) {
	var r upstreamExchangeRow
	var responseID, requestAuditID, traceID, cassettePath, upstreamID, routeTarget, model, endpoint any
	var statusCode, startedAt, completedAt, errorText any
	var exchangeID, exchangeKind, exchangeRole, parentExchangeID, sequenceIndex, requestID any
	err := rows.Scan(&r.ID, &responseID, &requestAuditID, &traceID, &cassettePath, &upstreamID, &routeTarget, &model, &endpoint, &statusCode, &startedAt, &completedAt, &errorText, &exchangeID, &exchangeKind, &exchangeRole, &parentExchangeID, &sequenceIndex, &requestID)
	if err != nil {
		return r, err
	}
	r.ResponseID = ptrString(responseID)
	r.RequestAuditID = ptrString(requestAuditID)
	r.TraceID = ptrString(traceID)
	r.CassettePath = ptrString(cassettePath)
	r.UpstreamID = ptrString(upstreamID)
	r.RouteTarget = ptrString(routeTarget)
	r.Model = ptrString(model)
	r.Endpoint = ptrString(endpoint)
	r.StatusCode = ptrInt64(statusCode)
	r.StartedAt = ptrTimeString(startedAt)
	r.CompletedAt = ptrTimeString(completedAt)
	r.ErrorText = ptrString(errorText)
	r.ExchangeID = ptrString(exchangeID)
	r.ExchangeKind = ptrString(exchangeKind)
	r.ExchangeRole = ptrString(exchangeRole)
	r.ParentExchangeID = ptrString(parentExchangeID)
	r.SequenceIndex = ptrInt64(sequenceIndex)
	r.RequestID = ptrString(requestID)
	return r, nil
}

// ---------------------------------------------------------------------------
// credential_mask_audits / credential_mask_event_states
//
// At export both value columns are decrypted with the source box and travel
// as plaintext inside the (password-protected) bundle; import re-encrypts.
// ---------------------------------------------------------------------------

type credentialMaskAuditRow struct {
	ID            string `json:"id"`
	TraceID       string `json:"trace_id"`
	EventIndex    int64  `json:"event_index"`
	Rule          string `json:"rule"`
	Vendor        string `json:"vendor"`
	Kind          string `json:"kind"`
	Confidence    string `json:"confidence"`
	Score         int64  `json:"score"`
	Reasons       string `json:"reasons"`
	StartOffset   int64  `json:"start_offset"`
	EndOffset     int64  `json:"end_offset"`
	ValueLength   int64  `json:"value_length"`
	OriginalValue string `json:"original_value"`
	StandInValue  string `json:"stand_in_value"`
	StorageMode   string `json:"storage_mode"`
	DetectedAt    string `json:"detected_at"`
	CreatedAt     string `json:"created_at"`
}

const credentialMaskAuditScan = `id, trace_id, event_index, rule, vendor, kind, confidence, score, reasons, start_offset, end_offset, value_length, original_value, stand_in_value, storage_mode, detected_at, created_at`

func scanCredentialMaskAudit(rows *sql.Rows) (credentialMaskAuditRow, error) {
	var r credentialMaskAuditRow
	var eventIndex, score, startOffset, endOffset, valueLength any
	var detectedAt, createdAt any
	err := rows.Scan(&r.ID, &r.TraceID, &eventIndex, &r.Rule, &r.Vendor, &r.Kind, &r.Confidence, &score, &r.Reasons, &startOffset, &endOffset, &valueLength, &r.OriginalValue, &r.StandInValue, &r.StorageMode, &detectedAt, &createdAt)
	if err != nil {
		return r, err
	}
	r.EventIndex = anyInt64(eventIndex)
	r.Score = anyInt64(score)
	r.StartOffset = anyInt64(startOffset)
	r.EndOffset = anyInt64(endOffset)
	r.ValueLength = anyInt64(valueLength)
	r.DetectedAt = anyTimeString(detectedAt)
	r.CreatedAt = anyTimeString(createdAt)
	return r, nil
}

type credentialMaskEventStateRow struct {
	TraceID    string  `json:"trace_id"`
	Rule       string  `json:"rule"`
	Status     string  `json:"status"`
	ReadAt     *string `json:"read_at"`
	ResolvedAt *string `json:"resolved_at"`
	UpdatedAt  string  `json:"updated_at"`
}

const credentialMaskEventStateScan = `trace_id, rule, status, read_at, resolved_at, updated_at`

func scanCredentialMaskEventState(rows *sql.Rows) (credentialMaskEventStateRow, error) {
	var r credentialMaskEventStateRow
	var readAt, resolvedAt, updatedAt any
	err := rows.Scan(&r.TraceID, &r.Rule, &r.Status, &readAt, &resolvedAt, &updatedAt)
	if err != nil {
		return r, err
	}
	r.ReadAt = ptrTimeString(readAt)
	r.ResolvedAt = ptrTimeString(resolvedAt)
	r.UpdatedAt = anyTimeString(updatedAt)
	return r, nil
}

// jsonLine marshals one row and appends the newline delimiter.
func jsonLine(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
