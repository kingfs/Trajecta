package monitor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/providerprobe"
	"github.com/kingfs/Trajecta/internal/routeplan"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
	llmspecs "github.com/kingfs/go-llm-specs"
)

type providerPresetResponse struct {
	Items    []string                   `json:"items"`
	Presets  []providerPresetItem       `json:"presets"`
	Defaults providerPresetFormDefaults `json:"defaults"`
}

type providerPresetItem struct {
	ID              string   `json:"id"`
	ProtocolFamily  string   `json:"protocol_family"`
	RoutingProfile  string   `json:"routing_profile"`
	SupportLevel    string   `json:"support_level"`
	AllowedProfiles []string `json:"allowed_profiles"`
}

type providerPresetFormDefaults struct {
	ProtocolFamilies []string `json:"protocol_families"`
	RoutingProfiles  []string `json:"routing_profiles"`
	ModelDiscovery   []string `json:"model_discovery"`
}

type upstreamPerf struct {
	ID             string  `json:"id"`
	BaseURL        string  `json:"base_url,omitempty"`
	ProviderPreset string  `json:"provider_preset,omitempty"`
	RequestCount   int     `json:"request_count"`
	SuccessRequest int     `json:"success_request"`
	FailedRequest  int     `json:"failed_request"`
	SuccessRate    float64 `json:"success_rate"`
	TotalTokens    int     `json:"total_tokens"`
	AvgTTFT        int     `json:"avg_ttft"`
	HealthState    string  `json:"health_state,omitempty"`
	ErrorRate      float64 `json:"error_rate,omitempty"`
	TimeoutRate    float64 `json:"timeout_rate,omitempty"`
}

type upstreamListResponse struct {
	Items           []upstreamItem            `json:"items"`
	RoutingFailures routingFailureSummaryView `json:"routing_failures"`
	RefreshedAt     time.Time                 `json:"refreshed_at"`
	Window          string                    `json:"window"`
	Model           string                    `json:"model"`
}

type upstreamItem struct {
	ID                string                `json:"id"`
	Enabled           bool                  `json:"enabled"`
	Priority          int                   `json:"priority"`
	Weight            float64               `json:"weight"`
	CapacityHint      float64               `json:"capacity_hint"`
	ModelDiscovery    string                `json:"model_discovery"`
	BaseURL           string                `json:"base_url"`
	ProviderPreset    string                `json:"provider_preset"`
	APIType           string                `json:"api_type"`
	Mode              string                `json:"mode,omitempty"`
	ProtocolFamily    string                `json:"protocol_family"`
	RoutingProfile    string                `json:"routing_profile"`
	HealthState       string                `json:"health_state"`
	Inflight          int64                 `json:"inflight"`
	TTFTFastMs        float64               `json:"ttft_fast_ms"`
	TTFTSlowMs        float64               `json:"ttft_slow_ms"`
	LatencyFastMs     float64               `json:"latency_fast_ms"`
	ErrorRate         float64               `json:"error_rate"`
	TimeoutRate       float64               `json:"timeout_rate"`
	LastRefreshAt     time.Time             `json:"last_refresh_at"`
	LastRefreshStatus string                `json:"last_refresh_status"`
	LastRefreshError  string                `json:"last_refresh_error,omitempty"`
	OpenUntil         time.Time             `json:"open_until,omitempty"`
	Models            []string              `json:"models"`
	RequestCount      int                   `json:"request_count"`
	SuccessRequest    int                   `json:"success_request"`
	FailedRequest     int                   `json:"failed_request"`
	SuccessRate       float64               `json:"success_rate"`
	TotalTokens       int                   `json:"total_tokens"`
	AvgTTFT           int                   `json:"avg_ttft"`
	LastSeen          time.Time             `json:"last_seen"`
	RecentModels      []string              `json:"recent_models"`
	LastModel         string                `json:"last_model"`
	RecentErrors      []string              `json:"recent_errors"`
	RecentFailures    []upstreamFailureItem `json:"recent_failures"`
	Performance       performanceView       `json:"performance"`
}

type upstreamFailureItem struct {
	TraceID    string    `json:"trace_id"`
	Model      string    `json:"model"`
	Endpoint   string    `json:"endpoint"`
	Reason     string    `json:"reason"`
	StatusCode int       `json:"status_code"`
	RecordedAt time.Time `json:"recorded_at"`
	ErrorText  string    `json:"error_text,omitempty"`
}

type upstreamDetailResponse struct {
	Target           upstreamItem               `json:"target"`
	Breakdown        upstreamBreakdownView      `json:"breakdown"`
	Timeline         []upstreamFailureItem      `json:"timeline"`
	FailureTimeline  []routingFailureBucketItem `json:"failure_timeline"`
	HealthThresholds healthThresholdView        `json:"health_thresholds"`
	Traces           []traceListItem            `json:"traces"`
	RefreshedAt      time.Time                  `json:"refreshed_at"`
	Window           string                     `json:"window"`
	Model            string                     `json:"model"`
}

type upstreamBreakdownView struct {
	Models         []sessionCountItem `json:"models"`
	Endpoints      []sessionCountItem `json:"endpoints"`
	FailureReasons []sessionCountItem `json:"failure_reasons"`
	FailedTraces   int                `json:"failed_traces"`
}

type channelListResponse struct {
	Items       []channelItem `json:"items"`
	RefreshedAt time.Time     `json:"refreshed_at"`
}

type channelItem struct {
	ID                 string                `json:"id"`
	Name               string                `json:"name"`
	Description        string                `json:"description,omitempty"`
	Source             string                `json:"source"`
	BaseURL            string                `json:"base_url"`
	ProviderPreset     string                `json:"provider_preset"`
	APIType            string                `json:"api_type"`
	Mode               string                `json:"mode"`
	Capabilities       upstreamCapabilities  `json:"capabilities,omitempty"`
	ProtocolFamily     string                `json:"protocol_family"`
	RoutingProfile     string                `json:"routing_profile"`
	APIVersion         string                `json:"api_version,omitempty"`
	Deployment         string                `json:"deployment,omitempty"`
	Project            string                `json:"project,omitempty"`
	Location           string                `json:"location,omitempty"`
	ModelResource      string                `json:"model_resource,omitempty"`
	APIKeyHint         string                `json:"api_key_hint,omitempty"`
	SecretStorageMode  string                `json:"secret_storage_mode"`
	Headers            map[string]string     `json:"headers,omitempty"`
	Enabled            bool                  `json:"enabled"`
	Priority           int                   `json:"priority"`
	Weight             float64               `json:"weight"`
	CapacityHint       float64               `json:"capacity_hint"`
	ModelDiscovery     string                `json:"model_discovery"`
	AllowUnknownModels bool                  `json:"allow_unknown_models"`
	ModelCount         int                   `json:"model_count"`
	EnabledModelCount  int                   `json:"enabled_model_count"`
	CreatedAt          time.Time             `json:"created_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
	LastProbeAt        time.Time             `json:"last_probe_at,omitempty"`
	LastProbeStatus    string                `json:"last_probe_status,omitempty"`
	LastProbeError     string                `json:"last_probe_error,omitempty"`
	Summary            usageSummaryView      `json:"summary,omitempty"`
	Trends             []usageTrendView      `json:"trends,omitempty"`
	ModelsUsage        []modelChannelItem    `json:"models_usage,omitempty"`
	RecentFailures     []upstreamFailureItem `json:"recent_failures,omitempty"`
	RecentProbeRuns    []channelProbeRunItem `json:"recent_probe_runs,omitempty"`
}

type channelProbeRunItem struct {
	ID              string    `json:"id"`
	Status          string    `json:"status"`
	FailureReason   string    `json:"failure_reason,omitempty"`
	RetryHint       string    `json:"retry_hint,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	DurationMs      int64     `json:"duration_ms"`
	DiscoveredCount int       `json:"discovered_count"`
	EnabledCount    int       `json:"enabled_count"`
	Endpoint        string    `json:"endpoint,omitempty"`
	StatusCode      int       `json:"status_code,omitempty"`
	ErrorText       string    `json:"error_text,omitempty"`
}

type channelModelsResponse struct {
	Items       []channelModelItem `json:"items"`
	RefreshedAt time.Time          `json:"refreshed_at"`
}

type channelModelItem struct {
	Model                       string    `json:"model"`
	DisplayName                 string    `json:"display_name,omitempty"`
	Source                      string    `json:"source"`
	Enabled                     bool      `json:"enabled"`
	SupportsResponses           *bool     `json:"supports_responses,omitempty"`
	SupportsChatCompletions     *bool     `json:"supports_chat_completions,omitempty"`
	SupportsEmbeddings          *bool     `json:"supports_embeddings,omitempty"`
	ContextWindow               *int      `json:"context_window,omitempty"`
	MaxOutputTokens             *int      `json:"max_output_tokens,omitempty"`
	CompactHistoryItemThreshold *int      `json:"compact_history_item_threshold,omitempty"`
	UpstreamModel               string    `json:"upstream_model,omitempty"`
	ProfileSource               string    `json:"profile_source,omitempty"`
	ProfileAdoptionStatus       string    `json:"profile_adoption_status,omitempty"`
	InputModalities             []string  `json:"input_modalities,omitempty"`
	OutputModalities            []string  `json:"output_modalities,omitempty"`
	FirstSeenAt                 time.Time `json:"first_seen_at"`
	LastSeenAt                  time.Time `json:"last_seen_at"`
	LastProbeAt                 time.Time `json:"last_probe_at,omitempty"`
}

type modelSpecLookupResponse struct {
	Query       string                `json:"query"`
	Matched     bool                  `json:"matched"`
	Suggestion  *modelSpecSuggestion  `json:"suggestion,omitempty"`
	Candidates  []modelSpecSuggestion `json:"candidates,omitempty"`
	RefreshedAt time.Time             `json:"refreshed_at"`
}

type modelSpecSuggestion struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Provider         string   `json:"provider"`
	Family           string   `json:"family"`
	Series           string   `json:"series"`
	Summary          string   `json:"summary"`
	Description      string   `json:"description,omitempty"`
	DescriptionCN    string   `json:"description_cn,omitempty"`
	Tags             []string `json:"tags"`
	Aliases          []string `json:"aliases"`
	ContextWindow    int      `json:"context_window"`
	MaxOutputTokens  int      `json:"max_output_tokens"`
	Capabilities     []string `json:"capabilities"`
	SupportsChat     bool     `json:"supports_chat_completions"`
	SupportsTools    bool     `json:"supports_tool_calling"`
	SupportsJSON     bool     `json:"supports_json_mode"`
	SupportsEmbeds   bool     `json:"supports_embeddings"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
}

type channelProbeResponse struct {
	ChannelID       string                `json:"channel_id"`
	Status          string                `json:"status"`
	FailureReason   string                `json:"failure_reason,omitempty"`
	RetryHint       string                `json:"retry_hint,omitempty"`
	Models          []string              `json:"models"`
	DiscoveredCount int                   `json:"discovered_count"`
	EnabledCount    int                   `json:"enabled_count"`
	Endpoint        string                `json:"endpoint,omitempty"`
	ErrorText       string                `json:"error_text,omitempty"`
	ProviderProbe   *providerprobe.Report `json:"provider_probe,omitempty"`
	StartedAt       time.Time             `json:"started_at"`
	CompletedAt     time.Time             `json:"completed_at"`
	DurationMs      int64                 `json:"duration_ms"`
}

type channelProbeRequest struct {
	EnableDiscovered *bool `json:"enable_discovered"`
	DetectProvider   *bool `json:"detect_provider"`
}

type providerProbeRequest struct {
	ProviderID     string            `json:"provider_id"`
	BaseURL        string            `json:"base_url"`
	APIKey         string            `json:"api_key"`
	Headers        map[string]string `json:"headers"`
	APIType        string            `json:"api_type"`
	ProtocolFamily string            `json:"protocol_family"`
}

type providerSetupRequest struct {
	channelUpsertRequest
}

type providerSetupResponse struct {
	Status           string                `json:"status"`
	Applied          bool                  `json:"applied"`
	NormalizedConfig channelItem           `json:"normalized_config"`
	Secret           providerSetupSecret   `json:"secret"`
	Probe            *providerprobe.Report `json:"probe,omitempty"`
	Channel          *channelItem          `json:"channel,omitempty"`
	Warnings         []string              `json:"warnings,omitempty"`
}

type providerSetupSecret struct {
	APIKeySet          bool   `json:"api_key_set"`
	APIKeyHint         string `json:"api_key_hint,omitempty"`
	SecretStorageMode  string `json:"secret_storage_mode,omitempty"`
	RedactionGuarantee string `json:"redaction_guarantee"`
}

type providerProbeReportRequest struct {
	ChannelID string `json:"channel_id"`
}

type channelUpsertRequest struct {
	ID                 string                             `json:"id"`
	Name               string                             `json:"name"`
	Description        string                             `json:"description"`
	BaseURL            string                             `json:"base_url"`
	ProviderPreset     string                             `json:"provider_preset"`
	APIType            string                             `json:"api_type"`
	Mode               string                             `json:"mode"`
	Capabilities       *config.UpstreamCapabilitiesConfig `json:"capabilities"`
	ProtocolFamily     string                             `json:"protocol_family"`
	RoutingProfile     string                             `json:"routing_profile"`
	APIVersion         string                             `json:"api_version"`
	Deployment         string                             `json:"deployment"`
	Project            string                             `json:"project"`
	Location           string                             `json:"location"`
	ModelResource      string                             `json:"model_resource"`
	APIKey             string                             `json:"api_key"`
	Headers            map[string]channelHeaderUpdate     `json:"headers"`
	Enabled            *bool                              `json:"enabled"`
	Priority           *int                               `json:"priority"`
	Weight             *float64                           `json:"weight"`
	CapacityHint       *float64                           `json:"capacity_hint"`
	ModelDiscovery     string                             `json:"model_discovery"`
	AllowUnknownModels *bool                              `json:"allow_unknown_models"`
}

type channelHeaderUpdate struct {
	Value  string
	Keep   bool
	Delete bool
}

type upstreamCapabilities = config.UpstreamCapabilitiesConfig

type channelModelPatchRequest struct {
	DisplayName                 *string `json:"display_name"`
	Enabled                     *bool   `json:"enabled"`
	SupportsResponses           *bool   `json:"supports_responses"`
	SupportsChatCompletions     *bool   `json:"supports_chat_completions"`
	SupportsEmbeddings          *bool   `json:"supports_embeddings"`
	ContextWindow               *int    `json:"context_window"`
	MaxOutputTokens             *int    `json:"max_output_tokens"`
	CompactHistoryItemThreshold *int    `json:"compact_history_item_threshold"`
	UpstreamModel               *string `json:"upstream_model"`
	ProfileSource               *string `json:"profile_source"`
	ProfileAdoptionStatus       *string `json:"profile_adoption_status"`
}

type channelModelBatchPatchRequest struct {
	Models  []string `json:"models"`
	Enabled bool     `json:"enabled"`
}

type channelModelBatchPatchResponse struct {
	Updated int      `json:"updated"`
	Models  []string `json:"models"`
	Enabled bool     `json:"enabled"`
}

type channelModelCreateRequest struct {
	Model       string `json:"model"`
	DisplayName string `json:"display_name"`
	Enabled     *bool  `json:"enabled"`
}

type modelAliasListResponse struct {
	Items       []modelAliasItem `json:"items"`
	RefreshedAt time.Time        `json:"refreshed_at"`
}

type modelAliasItem struct {
	ID          string    `json:"id"`
	Alias       string    `json:"alias"`
	TargetModel string    `json:"target_model"`
	ChannelID   string    `json:"channel_id,omitempty"`
	Enabled     bool      `json:"enabled"`
	Description string    `json:"description,omitempty"`
	Source      string    `json:"source,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type modelAliasUpsertRequest struct {
	ID          string `json:"id"`
	Alias       string `json:"alias"`
	TargetModel string `json:"target_model"`
	ChannelID   string `json:"channel_id"`
	Enabled     *bool  `json:"enabled"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

type modelAliasValidationResponse struct {
	Valid       bool                       `json:"valid"`
	Alias       modelAliasItem             `json:"alias"`
	Errors      []string                   `json:"errors,omitempty"`
	Warnings    []modelAliasValidationNote `json:"warnings,omitempty"`
	RefreshedAt time.Time                  `json:"refreshed_at"`
}

type modelAliasValidationNote struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type modelListResponse struct {
	Items       []modelItem `json:"items"`
	RefreshedAt time.Time   `json:"refreshed_at"`
	Window      string      `json:"window"`
}

type modelItem struct {
	Model               string           `json:"model"`
	DisplayName         string           `json:"display_name,omitempty"`
	ProviderCount       int              `json:"provider_count"`
	ChannelCount        int              `json:"channel_count"`
	EnabledChannelCount int              `json:"enabled_channel_count"`
	Channels            []string         `json:"channels"`
	Summary             usageSummaryView `json:"summary"`
	Today               usageSummaryView `json:"today"`
}

type modelDetailResponse struct {
	Model       modelItem          `json:"model"`
	Trends      []usageTrendView   `json:"trends"`
	Channels    []modelChannelItem `json:"channels"`
	RefreshedAt time.Time          `json:"refreshed_at"`
	Window      string             `json:"window"`
}

type modelChannelItem struct {
	ChannelID                   string           `json:"channel_id"`
	Model                       string           `json:"model"`
	DisplayName                 string           `json:"display_name,omitempty"`
	Enabled                     bool             `json:"enabled"`
	Source                      string           `json:"source"`
	SupportsResponses           *bool            `json:"supports_responses,omitempty"`
	SupportsChatCompletions     *bool            `json:"supports_chat_completions,omitempty"`
	SupportsEmbeddings          *bool            `json:"supports_embeddings,omitempty"`
	ContextWindow               *int             `json:"context_window,omitempty"`
	MaxOutputTokens             *int             `json:"max_output_tokens,omitempty"`
	CompactHistoryItemThreshold *int             `json:"compact_history_item_threshold,omitempty"`
	UpstreamModel               string           `json:"upstream_model,omitempty"`
	ProfileSource               string           `json:"profile_source,omitempty"`
	ProfileAdoptionStatus       string           `json:"profile_adoption_status,omitempty"`
	InputModalities             []string         `json:"input_modalities,omitempty"`
	OutputModalities            []string         `json:"output_modalities,omitempty"`
	Summary                     usageSummaryView `json:"summary"`
}

func modelListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		windowLabel, since := parseAnalyticsWindow(r.URL.Query().Get("window"))
		todaySince := startOfUTCDay(time.Now().UTC())
		items, err := st.ListModelCatalogAnalytics(since, todaySince)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		resp := modelListResponse{RefreshedAt: time.Now().UTC(), Window: windowLabel}
		for _, item := range items {
			if item.Summary.RequestCount == 0 {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(item.Model), q) && !strings.Contains(strings.ToLower(item.DisplayName), q) {
				continue
			}
			resp.Items = append(resp.Items, modelItemFromRecord(item))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func modelDetailAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		relativePath := strings.Trim(strings.TrimPrefix(pathClean(r.URL.EscapedPath()), "/api/models/"), "/")
		if relativePath == "" {
			http.NotFound(w, r)
			return
		}
		if strings.HasSuffix(relativePath, "/spec-lookup") {
			model, err := url.PathUnescape(strings.TrimSuffix(relativePath, "/spec-lookup"))
			if err != nil || strings.TrimSpace(model) == "" {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, http.StatusOK, modelSpecLookup(model))
			return
		}
		model, err := url.PathUnescape(relativePath)
		if err != nil || strings.TrimSpace(model) == "" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		windowLabel, since := parseAnalyticsWindow(r.URL.Query().Get("window"))
		bucketSize, bucketCount := analyticsBucketSpec(windowLabel)
		detail, err := st.GetModelDetailAnalytics(model, since, startOfUTCDay(time.Now().UTC()), bucketSize, bucketCount)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		resp := modelDetailResponse{
			Model:       modelItemFromRecord(detail.Model),
			Trends:      usageTrendViews(detail.Trends),
			RefreshedAt: time.Now().UTC(),
			Window:      windowLabel,
		}
		modelRecords := map[string]store.ChannelModelRecord{}
		if records, err := st.ListChannelModels("", false); err == nil {
			for _, record := range records {
				if strings.EqualFold(record.Model, model) {
					modelRecords[record.ChannelID] = record
				}
			}
		}
		for _, channelRecord := range detail.Channels {
			item := modelChannelItem{
				ChannelID: channelRecord.ChannelID,
				Model:     channelRecord.Model,
				Enabled:   channelRecord.Enabled,
				Source:    channelRecord.Source,
				Summary:   usageSummaryViewFromRecord(channelRecord.Summary),
			}
			if record, ok := modelRecords[channelRecord.ChannelID]; ok {
				enrichModelChannelItemFromRecord(&item, record)
			}
			resp.Channels = append(resp.Channels, item)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func modelSpecLookup(model string) modelSpecLookupResponse {
	model = strings.TrimSpace(model)
	resp := modelSpecLookupResponse{
		Query:       model,
		RefreshedAt: time.Now().UTC(),
	}
	if spec, ok := llmspecs.Get(model); ok {
		suggestion := modelSpecSuggestionFromSpec(spec)
		resp.Matched = true
		resp.Suggestion = &suggestion
		return resp
	}
	candidates := llmspecs.Search(model, 5)
	resp.Candidates = make([]modelSpecSuggestion, 0, len(candidates))
	for _, candidate := range candidates {
		resp.Candidates = append(resp.Candidates, modelSpecSuggestionFromSpec(candidate))
	}
	if len(resp.Candidates) > 0 {
		resp.Suggestion = &resp.Candidates[0]
	}
	return resp
}

func modelSpecSuggestionFromSpec(model llmspecs.Model) modelSpecSuggestion {
	features := model.Features()
	card := model.Card()
	return modelSpecSuggestion{
		ID:               model.ID(),
		Name:             model.Name(),
		Provider:         model.Provider(),
		Family:           model.Family(),
		Series:           model.Series(),
		Summary:          model.Summary(),
		Description:      model.Description(),
		DescriptionCN:    model.DescriptionCN(),
		Tags:             append([]string(nil), model.Tags()...),
		Aliases:          append([]string(nil), model.Aliases()...),
		ContextWindow:    model.ContextLength(),
		MaxOutputTokens:  model.MaxOutput(),
		Capabilities:     features.ToStrings(),
		SupportsChat:     model.HasCapability(llmspecs.CapChat),
		SupportsTools:    model.HasCapability(llmspecs.CapFunctionCall),
		SupportsJSON:     model.HasCapability(llmspecs.CapJsonMode),
		SupportsEmbeds:   model.HasCapability(llmspecs.CapEmbedding),
		InputModalities:  inputModalitiesFromCapabilities(card.Features),
		OutputModalities: outputModalitiesFromCapabilities(card.Features),
	}
}

func providerPresetAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		matrix := upstream.PresetSupportMatrix()
		ids := make([]string, 0, len(matrix))
		for id := range matrix {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		items := make([]providerPresetItem, 0, len(ids))
		for _, id := range ids {
			spec := matrix[id]
			allowed := append([]string(nil), spec.AllowedProfiles...)
			sort.Strings(allowed)
			items = append(items, providerPresetItem{
				ID:              id,
				ProtocolFamily:  spec.ProtocolFamily,
				RoutingProfile:  spec.RoutingProfile,
				SupportLevel:    spec.SupportLevel,
				AllowedProfiles: allowed,
			})
		}
		writeJSON(w, http.StatusOK, providerPresetResponse{
			Items:   ids,
			Presets: items,
			Defaults: providerPresetFormDefaults{
				ProtocolFamilies: []string{
					upstream.ProtocolFamilyOpenAICompatible,
					upstream.ProtocolFamilyAnthropicMessages,
					upstream.ProtocolFamilyGoogleGenAI,
					upstream.ProtocolFamilyVertexNative,
				},
				RoutingProfiles: []string{
					upstream.RoutingProfileOpenAIDefault,
					upstream.RoutingProfileAzureOpenAIV1,
					upstream.RoutingProfileAzureOpenAIDeploy,
					upstream.RoutingProfileVLLMOpenAI,
					upstream.RoutingProfileAnthropicDefault,
					upstream.RoutingProfileGoogleAIStudio,
					upstream.RoutingProfileVertexExpress,
					upstream.RoutingProfileVertexProject,
				},
				ModelDiscovery: []string{"list_models", "disabled"},
			},
		})
	}
}

func providerProbeAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req providerProbeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provider probe payload"})
			return
		}
		report, err := providerprobe.Probe(r.Context(), providerprobe.ProbeTarget{
			ProviderID:              strings.TrimSpace(req.ProviderID),
			BaseURL:                 strings.TrimSpace(req.BaseURL),
			APIKey:                  strings.TrimSpace(req.APIKey),
			Headers:                 req.Headers,
			SpecifiedAPIType:        strings.TrimSpace(req.APIType),
			SpecifiedProtocolFamily: strings.TrimSpace(req.ProtocolFamily),
		}, nil)
		if err != nil && report.Status == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		status := http.StatusOK
		if err != nil {
			status = http.StatusBadGateway
		}
		writeJSON(w, status, report)
	}
}

func providerSetupAPIHandlerUncommitted(st *store.Store, rtr *router.Router, channelService *channel.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		action := strings.Trim(strings.TrimPrefix(pathClean(r.URL.Path), "/api/provider-setup/"), "/")
		if action != "validate" && action != "apply" {
			http.NotFound(w, r)
			return
		}
		var req providerSetupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provider setup payload"})
			return
		}
		resp, probeErr := buildProviderSetupResponse(r.Context(), req, st)
		if action == "validate" {
			status := http.StatusOK
			if probeErr != nil {
				status = http.StatusBadGateway
			}
			writeJSON(w, status, resp)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if !providerSetupApplyAllowed(resp, req) {
			writeJSON(w, http.StatusBadGateway, resp)
			return
		}
		record, err := st.UpsertChannelConfig(channelRecordFromRequest(providerSetupUpsertRequest(resp.NormalizedConfig, req.channelUpsertRequest), store.ChannelConfigRecord{}))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		svc := effectiveChannelService(st, channelService)
		if err := reloadRouterFromChannels(rtr, svc); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		item := channelItemFromRecord(st, record, 0, 0)
		resp.Applied = true
		resp.Channel = &item
		writeJSON(w, http.StatusOK, resp)
	}
}

func providerSetupApplyAllowed(resp providerSetupResponse, req providerSetupRequest) bool {
	if resp.Status == providerprobe.StatusDetected {
		return true
	}
	return strings.TrimSpace(req.APIType) != "" && strings.TrimSpace(req.ProtocolFamily) != ""
}

func channelItemFromSetupRecord(st *store.Store, record store.ChannelConfigRecord, req channelUpsertRequest) channelItem {
	headers := map[string]string{}
	if strings.TrimSpace(record.HeadersJSON) != "" {
		_ = json.Unmarshal([]byte(record.HeadersJSON), &headers)
	}
	capabilities := upstreamCapabilities{}
	if strings.TrimSpace(record.CapabilitiesJSON) != "" {
		_ = json.Unmarshal([]byte(record.CapabilitiesJSON), &capabilities)
	}
	return channelItem{
		ID:                 record.ID,
		Name:               record.Name,
		Description:        record.Description,
		Source:             record.Source,
		BaseURL:            record.BaseURL,
		ProviderPreset:     record.ProviderPreset,
		APIType:            record.APIType,
		Mode:               record.Mode,
		Capabilities:       capabilities,
		ProtocolFamily:     record.ProtocolFamily,
		RoutingProfile:     record.RoutingProfile,
		APIVersion:         record.APIVersion,
		Deployment:         record.Deployment,
		Project:            record.Project,
		Location:           record.Location,
		ModelResource:      record.ModelResource,
		APIKeyHint:         secretHint(req.APIKey),
		SecretStorageMode:  secretStorageMode(st),
		Headers:            redactHeaders(headers),
		Enabled:            record.Enabled,
		Priority:           record.Priority,
		Weight:             record.Weight,
		CapacityHint:       record.CapacityHint,
		ModelDiscovery:     record.ModelDiscovery,
		AllowUnknownModels: record.AllowUnknownModels,
	}
}

func providerSetupUpsertRequest(normalized channelItem, original channelUpsertRequest) channelUpsertRequest {
	return channelUpsertRequest{
		ID:                 normalized.ID,
		Name:               normalized.Name,
		Description:        normalized.Description,
		BaseURL:            normalized.BaseURL,
		ProviderPreset:     normalized.ProviderPreset,
		APIType:            normalized.APIType,
		Mode:               normalized.Mode,
		Capabilities:       &normalized.Capabilities,
		ProtocolFamily:     normalized.ProtocolFamily,
		RoutingProfile:     normalized.RoutingProfile,
		APIVersion:         normalized.APIVersion,
		Deployment:         normalized.Deployment,
		Project:            normalized.Project,
		Location:           normalized.Location,
		ModelResource:      normalized.ModelResource,
		APIKey:             original.APIKey,
		Headers:            original.Headers,
		Enabled:            &normalized.Enabled,
		Priority:           &normalized.Priority,
		Weight:             &normalized.Weight,
		CapacityHint:       &normalized.CapacityHint,
		ModelDiscovery:     normalized.ModelDiscovery,
		AllowUnknownModels: &normalized.AllowUnknownModels,
	}
}

func providerProbeReportAPIHandler(st *store.Store, channelService *channel.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		var req providerProbeReportRequest
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provider probe report payload"})
				return
			}
		}
		svc := effectiveChannelService(st, channelService)
		report, err := svc.ProviderProbeReport(r.Context(), channel.ProviderProbeReportOptions{
			ChannelID: strings.TrimSpace(req.ChannelID),
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, report)
	}
}

func providerProbeReportApplyAPIHandlerUncommitted(st *store.Store, rtr *router.Router, channelService *channel.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		var req providerProbeReportRequest
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provider probe report apply payload"})
				return
			}
		}
		svc := effectiveChannelService(st, channelService)
		result, err := svc.ApplyProviderProbeReport(r.Context(), channel.ProviderProbeReportOptions{
			ChannelID: strings.TrimSpace(req.ChannelID),
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if appliedProviderProbeSuggestions(result.Applied) {
			if err := reloadRouterFromChannels(rtr, svc); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func channelListCreateAPIHandlerUncommitted(st *store.Store, rtr *router.Router, channelService *channel.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			windowLabel, since := parseAnalyticsWindow(r.URL.Query().Get("window"))
			bucketSize, bucketCount := analyticsBucketSpec(windowLabel)
			channels, err := st.ListChannelConfigs()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			models, err := st.ListChannelModels("", false)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			counts := channelModelCounts(models)
			// The list only needs the summary and the trend series of each
			// channel, which one grouped pass each can produce for the whole
			// list. A channel with no rows at all is absent from the trend map,
			// so it keeps its own query, and a failed grouped pass falls back to
			// the per-channel path the same way.
			summaries, summariesErr := st.GetChannelUsageSummaries(since)
			trends, trendsErr := st.GetChannelUsageTrendsBatch(since, bucketSize, bucketCount)
			channelSummary := func(channelID string) store.UsageSummaryRecord {
				if summariesErr == nil {
					return summaries[channelID]
				}
				summary, err := st.GetChannelUsageSummary(channelID, since)
				if err != nil {
					return store.UsageSummaryRecord{}
				}
				return summary
			}
			channelTrends := func(channelID string) []store.UsageTrendRecord {
				if trendsErr == nil {
					if series, ok := trends[channelID]; ok {
						return series
					}
				}
				series, err := st.GetChannelUsageTrends(channelID, since, bucketSize, bucketCount)
				if err != nil {
					return nil
				}
				return series
			}
			items := make([]channelItem, 0, len(channels))
			for _, record := range channels {
				item := channelItemFromRecord(st, record, counts[record.ID], enabledChannelModelCount(models, record.ID))
				item.Summary = usageSummaryViewFromRecord(channelSummary(record.ID))
				item.Trends = usageTrendViews(channelTrends(record.ID))
				items = append(items, item)
			}
			writeJSON(w, http.StatusOK, channelListResponse{Items: items, RefreshedAt: time.Now().UTC()})
		case http.MethodPost:
			var req channelUpsertRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid channel payload"})
				return
			}
			record, err := st.UpsertChannelConfig(channelRecordFromRequest(req, store.ChannelConfigRecord{}))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, channelItemFromRecord(st, record, 0, 0))
		default:
			http.NotFound(w, r)
		}
	}
}

func channelDetailAPIHandlerUncommitted(st *store.Store, rtr *router.Router, channelService *channel.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		rest := strings.TrimPrefix(pathClean(r.URL.Path), "/api/channels/")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
			http.NotFound(w, r)
			return
		}
		channelID := parts[0]
		if len(parts) == 1 {
			handleChannelConfig(w, r, st, rtr, channelService, channelID)
			return
		}
		switch parts[1] {
		case "probe":
			if r.Method != http.MethodPost {
				http.NotFound(w, r)
				return
			}
			req, err := decodeChannelProbeRequest(r)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			svc := effectiveChannelService(st, channelService)
			detectProvider := true
			if req.DetectProvider != nil {
				detectProvider = *req.DetectProvider
			}
			result, err := svc.ProbeWithOptions(channelID, channel.ProbeOptions{EnableDiscovered: req.EnableDiscovered, DetectProvider: detectProvider})
			if err != nil && result.Status == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			status := http.StatusOK
			if err != nil {
				status = http.StatusBadGateway
			}
			if err == nil {
				if reloadErr := reloadRouterFromChannels(rtr, svc); reloadErr != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + reloadErr.Error()})
					return
				}
			}
			writeJSON(w, status, channelProbeResponseFromResult(result))
		case "models":
			if len(parts) == 2 {
				handleChannelModels(w, r, st, rtr, channelService, channelID)
				return
			}
			modelPath, ok := channelModelPathSegment(r, channelID)
			if !ok {
				http.NotFound(w, r)
				return
			}
			if modelPath == "batch" {
				handleChannelModelsBatch(w, r, st, rtr, channelService, channelID)
				return
			}
			handleChannelModel(w, r, st, rtr, channelService, channelID, modelPath)
		default:
			http.NotFound(w, r)
		}
	}
}

func channelModelPathSegment(r *http.Request, channelID string) (string, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	prefix := "/api/channels/" + url.PathEscape(channelID) + "/models/"
	escapedPath := r.URL.EscapedPath()
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", false
	}
	escapedModel := strings.Trim(escapedPath[len(prefix):], "/")
	if escapedModel == "" || strings.Contains(escapedModel, "/") {
		return "", false
	}
	model, err := url.PathUnescape(escapedModel)
	if err != nil || strings.TrimSpace(model) == "" {
		return "", false
	}
	return model, true
}

// channelBootstrapSettingsAPIHandler reports and clears the YAML bootstrap
// marker for channel configuration. GET answers whether the database owns the
// routing configuration; DELETE clears only the explicit marker, so a database
// that still stores channels keeps winning over YAML.
func channelBootstrapSettingsAPIHandler(st *store.Store, channelService *channel.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		svc := effectiveChannelService(st, channelService)
		if svc == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			initialized, err := svc.HasConfiguration()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"initialized": initialized})
		case http.MethodDelete:
			// Clearing the marker is a settings write, but it still goes through
			// the configuration transaction so it cannot interleave with a
			// management write that re-marks it.
			err := st.ConfigurationTransaction(r.Context(), func(tx *store.Store, commit func() error) error {
				if err := svc.WithStore(tx).ResetConfigurationInitialized(); err != nil {
					return err
				}
				return commit()
			})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			initialized, err := svc.HasConfiguration()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"initialized": initialized})
		default:
			http.NotFound(w, r)
		}
	}
}

func modelAliasListCreateAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			enabledOnly := parseBoolQuery(r.URL.Query().Get("enabled_only"), false)
			aliases, err := st.ListModelAliases(r.URL.Query().Get("alias"), enabledOnly)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			items := make([]modelAliasItem, 0, len(aliases))
			for _, alias := range aliases {
				items = append(items, modelAliasItemFromRecord(alias))
			}
			writeJSON(w, http.StatusOK, modelAliasListResponse{Items: items, RefreshedAt: time.Now().UTC()})
		case http.MethodPost:
			var req modelAliasUpsertRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model alias payload"})
				return
			}
			record, err := st.UpsertModelAlias(modelAliasRecordFromRequest(req, store.ModelAliasRecord{}))
			if err != nil {
				writeAliasValidationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, modelAliasItemFromRecord(record))
		default:
			http.NotFound(w, r)
		}
	}
}

func modelAliasValidateAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req modelAliasUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model alias payload"})
			return
		}
		record := normalizeModelAliasPreview(modelAliasRecordFromRequest(req, store.ModelAliasRecord{}))
		resp := modelAliasValidationResponse{
			Valid:       true,
			Alias:       modelAliasItemFromRecord(record),
			RefreshedAt: time.Now().UTC(),
		}
		if err := st.ValidateModelAlias(record); err != nil {
			resp.Valid = false
			resp.Errors = []string{err.Error()}
		}
		warnings, err := modelAliasValidationWarnings(st, record)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		resp.Warnings = warnings
		writeJSON(w, http.StatusOK, resp)
	}
}

func modelAliasDetailAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		id, err := url.PathUnescape(strings.Trim(strings.TrimPrefix(pathClean(r.URL.Path), "/api/model-aliases/"), "/"))
		if err != nil || strings.TrimSpace(id) == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			record, err := st.GetModelAlias(id)
			if err != nil {
				writeAliasError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, modelAliasItemFromRecord(record))
		case http.MethodPatch:
			existing, err := st.GetModelAlias(id)
			if err != nil {
				writeAliasError(w, err)
				return
			}
			var req modelAliasUpsertRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model alias payload"})
				return
			}
			req.ID = id
			record, err := st.UpsertModelAlias(modelAliasRecordFromRequest(req, existing))
			if err != nil {
				writeAliasValidationError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, modelAliasItemFromRecord(record))
		case http.MethodDelete:
			if err := st.DeleteModelAlias(id); err != nil {
				writeAliasError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}
}

func channelModelRecordFromPatch(model string, req channelModelPatchRequest) store.ChannelModelRecord {
	displayName := strings.TrimSpace(model)
	if req.DisplayName != nil && strings.TrimSpace(*req.DisplayName) != "" {
		displayName = strings.TrimSpace(*req.DisplayName)
	}
	record := store.ChannelModelRecord{
		Model:                       model,
		DisplayName:                 displayName,
		Source:                      "trace",
		SupportsResponses:           capabilityIntPtr(req.SupportsResponses),
		SupportsChatCompletions:     capabilityIntPtr(req.SupportsChatCompletions),
		SupportsEmbeddings:          capabilityIntPtr(req.SupportsEmbeddings),
		ContextWindow:               positiveIntPtr(req.ContextWindow),
		MaxOutputTokens:             positiveIntPtr(req.MaxOutputTokens),
		CompactHistoryItemThreshold: positiveIntPtr(req.CompactHistoryItemThreshold),
		InputModalitiesJSON:         "[]",
		OutputModalitiesJSON:        "[]",
		RawModelJSON:                "{}",
	}
	if req.Enabled != nil {
		record.Enabled = *req.Enabled
	}
	if req.UpstreamModel != nil {
		record.UpstreamModel = strings.TrimSpace(*req.UpstreamModel)
	}
	if req.ProfileSource != nil {
		record.ProfileSource = strings.TrimSpace(*req.ProfileSource)
	}
	if req.ProfileAdoptionStatus != nil {
		record.ProfileAdoptionStatus = strings.TrimSpace(*req.ProfileAdoptionStatus)
	}
	return record
}

func modelAliasRecordFromRequest(req modelAliasUpsertRequest, existing store.ModelAliasRecord) store.ModelAliasRecord {
	record := existing
	isCreate := record.ID == ""
	if strings.TrimSpace(req.ID) != "" {
		record.ID = strings.TrimSpace(req.ID)
	}
	if strings.TrimSpace(req.Alias) != "" {
		record.Alias = strings.TrimSpace(req.Alias)
	}
	if strings.TrimSpace(req.TargetModel) != "" {
		record.TargetModel = strings.TrimSpace(req.TargetModel)
	}
	record.ChannelID = strings.TrimSpace(req.ChannelID)
	if req.Enabled != nil {
		record.Enabled = *req.Enabled
	} else if isCreate {
		record.Enabled = true
	}
	if strings.TrimSpace(req.Description) != "" {
		record.Description = strings.TrimSpace(req.Description)
	}
	if strings.TrimSpace(req.Source) != "" {
		record.Source = strings.TrimSpace(req.Source)
	}
	return record
}

func modelAliasValidationWarnings(st *store.Store, record store.ModelAliasRecord) ([]modelAliasValidationNote, error) {
	if strings.TrimSpace(record.TargetModel) == "" {
		return nil, nil
	}
	channels, err := st.ListChannelConfigs()
	if err != nil {
		return nil, err
	}
	models, err := st.ListChannelModels("", false)
	if err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimSpace(record.TargetModel))
	enabledChannels := map[string]store.ChannelConfigRecord{}
	allChannels := map[string]store.ChannelConfigRecord{}
	for _, channel := range channels {
		allChannels[channel.ID] = channel
		if channel.Enabled {
			enabledChannels[channel.ID] = channel
		}
	}
	targetEnabledOnAnyChannel := false
	targetEnabledByChannel := map[string]bool{}
	for _, model := range models {
		if strings.ToLower(strings.TrimSpace(model.Model)) != target || !model.Enabled {
			continue
		}
		targetEnabledByChannel[model.ChannelID] = true
		if _, ok := enabledChannels[model.ChannelID]; ok {
			targetEnabledOnAnyChannel = true
		}
	}
	warnings := []modelAliasValidationNote{}
	if !targetEnabledOnAnyChannel {
		warnings = append(warnings, modelAliasValidationNote{
			Code:    "target_model_not_enabled",
			Message: "target model is not enabled on any enabled channel",
		})
	}
	if record.ChannelID != "" {
		channel, ok := allChannels[record.ChannelID]
		if !ok || !channel.Enabled {
			warnings = append(warnings, modelAliasValidationNote{
				Code:    "scoped_channel_disabled",
				Message: "scoped channel is disabled or missing",
			})
		}
		if !targetEnabledByChannel[record.ChannelID] {
			warnings = append(warnings, modelAliasValidationNote{
				Code:    "scoped_channel_target_model_not_enabled",
				Message: "scoped channel does not have the target model enabled",
			})
		}
	}
	return warnings, nil
}

func modelAliasItemFromRecord(record store.ModelAliasRecord) modelAliasItem {
	return modelAliasItem{
		ID:          record.ID,
		Alias:       record.Alias,
		TargetModel: record.TargetModel,
		ChannelID:   record.ChannelID,
		Enabled:     record.Enabled,
		Description: record.Description,
		Source:      record.Source,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}
}

func upstreamCandidatesForInspect(st *store.Store) ([]routeplan.UpstreamCandidate, error) {
	channels, err := st.ListChannelConfigs()
	if err != nil {
		return nil, err
	}
	models, err := st.ListChannelModels("", true)
	if err != nil {
		return nil, err
	}
	modelsByChannel := map[string][]string{}
	modelCapsByChannel := map[string]map[string]routeplan.ModelCapabilities{}
	for _, model := range models {
		modelsByChannel[model.ChannelID] = append(modelsByChannel[model.ChannelID], model.Model)
		caps, ok := inspectModelCapabilities(model)
		if !ok {
			continue
		}
		if modelCapsByChannel[model.ChannelID] == nil {
			modelCapsByChannel[model.ChannelID] = map[string]routeplan.ModelCapabilities{}
		}
		modelCapsByChannel[model.ChannelID][strings.ToLower(strings.TrimSpace(model.Model))] = caps
	}
	out := make([]routeplan.UpstreamCandidate, 0, len(channels))
	for _, channel := range channels {
		caps := upstreamCapabilitiesFromChannel(channel)
		out = append(out, routeplan.UpstreamCandidate{
			ID:                        channel.ID,
			RouteTargetID:             channel.ID,
			ChannelID:                 channel.ID,
			Enabled:                   channel.Enabled,
			Priority:                  channel.Priority,
			Weight:                    channel.Weight,
			Models:                    modelsByChannel[channel.ID],
			SupportsChatCompletions:   caps.chatCompletions,
			SupportsResponses:         caps.responses,
			SupportsAnthropicMessages: caps.anthropicMessages,
			SupportsToolCalling:       caps.toolCalling,
			ModelCapabilities:         modelCapsByChannel[channel.ID],
		})
	}
	return out, nil
}

func upstreamCapabilitiesFromChannel(channel store.ChannelConfigRecord) inspectCapabilities {
	var configured config.UpstreamCapabilitiesConfig
	capabilitiesJSON := strings.TrimSpace(channel.CapabilitiesJSON)
	if capabilitiesJSON == "" {
		capabilitiesJSON = "{}"
	}
	_ = json.Unmarshal([]byte(capabilitiesJSON), &configured)
	caps := inspectCapabilities{toolCalling: true}
	if configured.ChatCompletions != nil {
		caps.chatCompletions = *configured.ChatCompletions
	}
	if configured.Responses != nil {
		caps.responses = *configured.Responses
	}
	if configured.ToolCalling != nil {
		caps.toolCalling = *configured.ToolCalling
	}
	switch strings.TrimSpace(channel.APIType) {
	case upstream.APITypeChatCompletions, "":
		if configured.ChatCompletions == nil {
			caps.chatCompletions = true
		}
	case upstream.APITypeResponses, upstream.APITypeResponsesNative:
		if configured.Responses == nil {
			caps.responses = true
		}
	case upstream.APITypeMessages:
		caps.anthropicMessages = true
	}
	return caps
}

// inspectModelCapabilities converts a channel model profile's capability
// columns into the per-model overrides the route planner understands. Models
// without any declared capability are reported as absent so the planner keeps
// using the channel-level flags.

func channelRecordFromRequest(req channelUpsertRequest, existing store.ChannelConfigRecord) store.ChannelConfigRecord {
	record := existing
	if strings.TrimSpace(req.ID) != "" {
		record.ID = strings.TrimSpace(req.ID)
	}
	if strings.TrimSpace(req.Name) != "" {
		record.Name = strings.TrimSpace(req.Name)
	}
	if strings.TrimSpace(req.Description) != "" {
		record.Description = strings.TrimSpace(req.Description)
	}
	if strings.TrimSpace(record.Source) == "" {
		record.Source = "manual"
	}
	if strings.TrimSpace(req.BaseURL) != "" {
		record.BaseURL = strings.TrimSpace(req.BaseURL)
	}
	if strings.TrimSpace(req.ProviderPreset) != "" {
		record.ProviderPreset = strings.TrimSpace(req.ProviderPreset)
	}
	record.APIType = valueOrExisting(req.APIType, record.APIType)
	record.Mode = valueOrExisting(req.Mode, record.Mode)
	if req.Capabilities != nil {
		if data, err := json.Marshal(req.Capabilities); err == nil {
			record.CapabilitiesJSON = string(data)
		}
	}
	if strings.TrimSpace(req.ProtocolFamily) != "" {
		record.ProtocolFamily = strings.TrimSpace(req.ProtocolFamily)
	}
	if strings.TrimSpace(req.RoutingProfile) != "" {
		record.RoutingProfile = strings.TrimSpace(req.RoutingProfile)
	}
	record.APIVersion = valueOrExisting(req.APIVersion, record.APIVersion)
	record.Deployment = valueOrExisting(req.Deployment, record.Deployment)
	record.Project = valueOrExisting(req.Project, record.Project)
	record.Location = valueOrExisting(req.Location, record.Location)
	record.ModelResource = valueOrExisting(req.ModelResource, record.ModelResource)
	if strings.TrimSpace(req.APIKey) != "" {
		record.APIKeyCiphertext = []byte(strings.TrimSpace(req.APIKey))
		record.APIKeyHint = secretHint(req.APIKey)
	}
	if req.Headers != nil {
		headers := applyHeaderUpdates(existing.HeadersJSON, req.Headers)
		data, _ := json.Marshal(headers)
		record.HeadersJSON = string(data)
	}
	if req.Enabled != nil {
		record.Enabled = *req.Enabled
	} else if existing.ID == "" {
		record.Enabled = true
	}
	if req.Priority != nil {
		record.Priority = *req.Priority
	}
	if req.Weight != nil {
		record.Weight = *req.Weight
	}
	if req.CapacityHint != nil {
		record.CapacityHint = *req.CapacityHint
	}
	if strings.TrimSpace(req.ModelDiscovery) != "" {
		record.ModelDiscovery = strings.TrimSpace(req.ModelDiscovery)
	}
	if req.AllowUnknownModels != nil {
		record.AllowUnknownModels = *req.AllowUnknownModels
	}
	return record
}

func channelItemFromRecord(st *store.Store, record store.ChannelConfigRecord, modelCount int, enabledModelCount int) channelItem {
	headers := map[string]string{}
	if strings.TrimSpace(record.HeadersJSON) != "" {
		_ = json.Unmarshal([]byte(record.HeadersJSON), &headers)
	}
	capabilities := upstreamCapabilities{}
	if strings.TrimSpace(record.CapabilitiesJSON) != "" {
		_ = json.Unmarshal([]byte(record.CapabilitiesJSON), &capabilities)
	}
	return channelItem{
		ID:                 record.ID,
		Name:               record.Name,
		Description:        record.Description,
		Source:             record.Source,
		BaseURL:            record.BaseURL,
		ProviderPreset:     record.ProviderPreset,
		APIType:            record.APIType,
		Mode:               record.Mode,
		Capabilities:       capabilities,
		ProtocolFamily:     record.ProtocolFamily,
		RoutingProfile:     record.RoutingProfile,
		APIVersion:         record.APIVersion,
		Deployment:         record.Deployment,
		Project:            record.Project,
		Location:           record.Location,
		ModelResource:      record.ModelResource,
		APIKeyHint:         record.APIKeyHint,
		SecretStorageMode:  st.SecretStorageMode(),
		Headers:            redactHeaders(headers),
		Enabled:            record.Enabled,
		Priority:           record.Priority,
		Weight:             record.Weight,
		CapacityHint:       record.CapacityHint,
		ModelDiscovery:     record.ModelDiscovery,
		AllowUnknownModels: record.AllowUnknownModels,
		ModelCount:         modelCount,
		EnabledModelCount:  enabledModelCount,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
		LastProbeAt:        record.LastProbeAt,
		LastProbeStatus:    record.LastProbeStatus,
		LastProbeError:     record.LastProbeError,
	}
}

func channelModelItemFromRecord(record store.ChannelModelRecord) channelModelItem {
	return channelModelItem{
		Model:                       record.Model,
		DisplayName:                 record.DisplayName,
		Source:                      record.Source,
		Enabled:                     record.Enabled,
		SupportsResponses:           capabilityBool(record.SupportsResponses),
		SupportsChatCompletions:     capabilityBool(record.SupportsChatCompletions),
		SupportsEmbeddings:          capabilityBool(record.SupportsEmbeddings),
		ContextWindow:               record.ContextWindow,
		MaxOutputTokens:             record.MaxOutputTokens,
		CompactHistoryItemThreshold: record.CompactHistoryItemThreshold,
		UpstreamModel:               record.UpstreamModel,
		ProfileSource:               record.ProfileSource,
		ProfileAdoptionStatus:       record.ProfileAdoptionStatus,
		InputModalities:             decodeStringList(record.InputModalitiesJSON),
		OutputModalities:            decodeStringList(record.OutputModalitiesJSON),
		FirstSeenAt:                 record.FirstSeenAt,
		LastSeenAt:                  record.LastSeenAt,
		LastProbeAt:                 record.LastProbeAt,
	}
}

func channelProbeResponseFromResult(result channel.ProbeResult) channelProbeResponse {
	resp := channelProbeResponse{
		ChannelID:       result.ChannelID,
		Status:          result.Status,
		FailureReason:   result.FailureReason,
		RetryHint:       result.RetryHint,
		Models:          result.Models,
		DiscoveredCount: result.DiscoveredCount,
		EnabledCount:    result.EnabledCount,
		Endpoint:        result.Endpoint,
		ErrorText:       result.ErrorText,
		StartedAt:       result.StartedAt,
		CompletedAt:     result.CompletedAt,
		DurationMs:      result.DurationMs,
	}
	if result.ProviderReport.Status != "" {
		resp.ProviderProbe = &result.ProviderReport
	}
	return resp
}

func channelProbeRunItems(records []store.ChannelProbeRunRecord) []channelProbeRunItem {
	items := make([]channelProbeRunItem, 0, len(records))
	for _, record := range records {
		reason, hint := probeRunMeta(record.RequestMetaJSON)
		items = append(items, channelProbeRunItem{
			ID:              record.ID,
			Status:          record.Status,
			FailureReason:   reason,
			RetryHint:       hint,
			StartedAt:       record.StartedAt,
			CompletedAt:     record.CompletedAt,
			DurationMs:      record.DurationMs,
			DiscoveredCount: record.DiscoveredCount,
			EnabledCount:    record.EnabledCount,
			Endpoint:        record.Endpoint,
			StatusCode:      record.StatusCode,
			ErrorText:       record.ErrorText,
		})
	}
	return items
}

func modelItemFromRecord(record store.ModelCatalogAnalyticsRecord) modelItem {
	return modelItem{
		Model:               record.Model,
		DisplayName:         record.DisplayName,
		ProviderCount:       record.ProviderCount,
		ChannelCount:        record.ChannelCount,
		EnabledChannelCount: record.EnabledChannelCount,
		Channels:            record.Channels,
		Summary:             usageSummaryViewFromRecord(record.Summary),
		Today:               usageSummaryViewFromRecord(record.Today),
	}
}

func channelModelCounts(models []store.ChannelModelRecord) map[string]int {
	counts := map[string]int{}
	for _, model := range models {
		counts[model.ChannelID]++
	}
	return counts
}

func upstreamListAPIHandler(st *store.Store, rtr *router.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		windowLabel, since := parseUpstreamWindow(r.URL.Query().Get("window"))
		modelFilter := strings.TrimSpace(r.URL.Query().Get("model"))
		items, err := buildUpstreamItems(st, rtr, since, modelFilter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		routingFailures := routingFailureSummaryView{}
		if st != nil {
			bucketSize, bucketCount := routingFailureBucketSpec(windowLabel)
			analytics, err := st.GetRoutingFailureAnalytics(since, modelFilter, 5, 5, bucketSize, bucketCount)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query routing failures: " + err.Error()})
				return
			}
			routingFailures = routingFailureSummaryView{
				Total:    analytics.Total,
				Reasons:  toSessionCountItems(analytics.Reasons),
				Recent:   toRoutingFailureItems(analytics.Recent),
				Timeline: toRoutingFailureBucketItems(analytics.Timeline),
			}
		}

		writeJSON(w, http.StatusOK, upstreamListResponse{
			Items:           items,
			RoutingFailures: routingFailures,
			RefreshedAt:     time.Now().UTC(),
			Window:          windowLabel,
			Model:           modelFilter,
		})
	}
}

func upstreamDetailAPIHandler(st *store.Store, rtr *router.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}

		upstreamID := strings.TrimPrefix(pathClean(r.URL.Path), "/api/upstreams/")
		upstreamID = strings.Trim(upstreamID, "/")
		if upstreamID == "" || strings.Contains(upstreamID, "/") {
			http.NotFound(w, r)
			return
		}

		windowLabel, since := parseUpstreamWindow(r.URL.Query().Get("window"))
		modelFilter := strings.TrimSpace(r.URL.Query().Get("model"))
		bucketSize, bucketCount := routingFailureBucketSpec(windowLabel)
		detail, err := st.GetUpstreamDetail(upstreamID, since, modelFilter, 50, bucketSize, bucketCount)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "upstream not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query upstream detail: " + err.Error()})
			return
		}

		items, err := buildUpstreamItems(st, rtr, since, modelFilter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		var target upstreamItem
		for _, item := range items {
			if item.ID == upstreamID {
				target = item
				break
			}
		}
		if target.ID == "" {
			baseURL := ""
			providerPreset := ""
			if len(detail.Traces) > 0 {
				baseURL = detail.Traces[0].Header.Meta.SelectedUpstreamBaseURL
				providerPreset = detail.Traces[0].Header.Meta.SelectedUpstreamProviderPreset
			}
			target = upstreamItem{
				ID:             detail.Analytics.UpstreamID,
				BaseURL:        baseURL,
				ProviderPreset: providerPreset,
				RequestCount:   detail.Analytics.RequestCount,
				SuccessRequest: detail.Analytics.SuccessRequest,
				FailedRequest:  detail.Analytics.FailedRequest,
				SuccessRate:    detail.Analytics.SuccessRate,
				TotalTokens:    detail.Analytics.TotalTokens,
				AvgTTFT:        detail.Analytics.AvgTTFT,
				LastSeen:       detail.Analytics.LastSeen,
				RecentModels:   detail.Analytics.Models,
				LastModel:      detail.Analytics.LastModel,
				RecentErrors:   detail.Analytics.RecentErrors,
				RecentFailures: toUpstreamFailureItems(detail.Analytics.RecentFailures),
			}
		}

		resp := upstreamDetailResponse{
			Target: target,
			Breakdown: upstreamBreakdownView{
				Models:         toSessionCountItems(detail.Models),
				Endpoints:      toSessionCountItems(detail.Endpoints),
				FailureReasons: toSessionCountItems(detail.FailureReasons),
				FailedTraces:   detail.Analytics.FailedRequest,
			},
			Timeline:        toUpstreamFailureItems(detail.Analytics.RecentFailures),
			FailureTimeline: toRoutingFailureBucketItems(detail.Timeline),
			HealthThresholds: toHealthThresholdView(func() router.HealthThresholds {
				if rtr != nil {
					return rtr.HealthThresholds()
				}
				return router.DefaultHealthThresholds()
			}()),
			RefreshedAt: time.Now().UTC(),
			Window:      windowLabel,
			Model:       modelFilter,
		}
		for _, entry := range detail.Traces {
			resp.Traces = append(resp.Traces, traceListItemFromEntry(entry))
		}
		resp.Target.Performance = buildUpstreamPerformance(resp.Target)
		writeJSON(w, http.StatusOK, resp)
	}
}
