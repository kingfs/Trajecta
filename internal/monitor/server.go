package monitor

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/providerprobe"
	"github.com/kingfs/Trajecta/internal/reanalysis"
	responsesaudit "github.com/kingfs/Trajecta/internal/responses/audit"
	"github.com/kingfs/Trajecta/internal/responses/functionexec"
	"github.com/kingfs/Trajecta/internal/routeplan"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	llmspecs "github.com/kingfs/go-llm-specs"
)

//go:embed ui/dist/*
var uiFS embed.FS

type listResponse struct {
	Items       []traceListItem `json:"items"`
	Stats       LogStats        `json:"stats"`
	Page        int             `json:"page"`
	PageSize    int             `json:"page_size"`
	Total       int             `json:"total"`
	TotalPages  int             `json:"total_pages"`
	RefreshedAt time.Time       `json:"refreshed_at"`
}

type overviewResponse struct {
	Window      string                  `json:"window"`
	RefreshedAt time.Time               `json:"refreshed_at"`
	Summary     overviewSummaryView     `json:"summary"`
	Timeline    []overviewTimelineItem  `json:"timeline"`
	Breakdown   overviewBreakdownView   `json:"breakdown"`
	Attention   overviewAttentionView   `json:"attention"`
	Analysis    overviewAnalysisSummary `json:"analysis"`
	Observation overviewObservationView `json:"observation"`
}

type overviewSummaryView struct {
	RequestCount   int     `json:"request_count"`
	SuccessRequest int     `json:"success_request"`
	FailedRequest  int     `json:"failed_request"`
	SuccessRate    float64 `json:"success_rate"`
	TotalTokens    int     `json:"total_tokens"`
	AvgTTFTMs      int     `json:"avg_ttft_ms"`
	AvgDurationMs  int64   `json:"avg_duration_ms"`
	P95TTFTMs      int     `json:"p95_ttft_ms"`
	P95DurationMs  int64   `json:"p95_duration_ms"`
	StreamCount    int     `json:"stream_count"`
	SessionCount   int     `json:"session_count"`
}

type overviewTimelineItem struct {
	Time          time.Time `json:"time"`
	RequestCount  int       `json:"request_count"`
	FailedRequest int       `json:"failed_request"`
	TotalTokens   int       `json:"total_tokens"`
	AvgTTFTMs     int       `json:"avg_ttft_ms"`
	AvgDurationMs int64     `json:"avg_duration_ms"`
}

type overviewBreakdownView struct {
	Models                []sessionCountItem `json:"models"`
	Providers             []sessionCountItem `json:"providers"`
	Endpoints             []sessionCountItem `json:"endpoints"`
	Upstreams             []sessionCountItem `json:"upstreams"`
	RoutingFailureReasons []sessionCountItem `json:"routing_failure_reasons"`
	FindingCategories     []sessionCountItem `json:"finding_categories"`
}

type overviewAttentionView struct {
	RecentFailures   []traceListItem      `json:"recent_failures"`
	HighRiskFindings []findingView        `json:"high_risk_findings"`
	RoutingFailures  []routingFailureItem `json:"routing_failures"`
	SlowTraces       []traceListItem      `json:"slow_traces"`
}

type overviewAnalysisSummary struct {
	Total  int               `json:"total"`
	Failed int               `json:"failed"`
	Recent []analysisRunView `json:"recent"`
}

type overviewObservationView struct {
	TotalObservations int                `json:"total_observations"`
	Parsed            int                `json:"parsed"`
	Failed            int                `json:"failed"`
	Queued            int                `json:"queued"`
	Running           int                `json:"running"`
	Unparsed          int                `json:"unparsed"`
	RecentFailures    []overviewParseJob `json:"recent_failures"`
}

type overviewParseJob struct {
	ID        int64     `json:"id"`
	TraceID   string    `json:"trace_id"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type systemEventListResponse struct {
	Items       []systemEventView `json:"items"`
	Page        int               `json:"page"`
	PageSize    int               `json:"page_size"`
	Total       int               `json:"total"`
	TotalPages  int               `json:"total_pages"`
	Window      string            `json:"window"`
	RefreshedAt time.Time         `json:"refreshed_at"`
}

type systemEventSummaryResponse struct {
	Total      int                `json:"total"`
	Unread     int                `json:"unread"`
	Critical   int                `json:"critical"`
	Error      int                `json:"error"`
	Warning    int                `json:"warning"`
	LastSeenAt *time.Time         `json:"last_seen_at,omitempty"`
	BySource   []sessionCountItem `json:"by_source"`
	ByCategory []sessionCountItem `json:"by_category"`
	Window     string             `json:"window"`
}

type routingSummaryResponse struct {
	Window                string                    `json:"window"`
	Model                 string                    `json:"model,omitempty"`
	RefreshedAt           time.Time                 `json:"refreshed_at"`
	TotalTraces           int                       `json:"total_traces"`
	ScannedTraces         int                       `json:"scanned_traces"`
	EventfulTraces        int                       `json:"eventful_traces"`
	LegacyOrMissingEvents int                       `json:"legacy_or_missing_events"`
	ParseErrors           int                       `json:"parse_errors"`
	FailureReasons        []sessionCountItem        `json:"failure_reasons"`
	SelectedUpstreams     []sessionCountItem        `json:"selected_upstreams"`
	SelectedRouteTargets  []sessionCountItem        `json:"selected_route_targets"`
	SelectedChannels      []sessionCountItem        `json:"selected_channels"`
	SelectedCredentials   []sessionCountItem        `json:"selected_credentials"`
	StickyStatuses        []sessionCountItem        `json:"sticky_statuses"`
	StickyBreaks          routingStickyBreakSummary `json:"sticky_breaks"`
}

type routingStickyBreakSummary struct {
	Total                int                `json:"total"`
	PreviousUpstreams    []sessionCountItem `json:"previous_upstreams"`
	NextUpstreams        []sessionCountItem `json:"next_upstreams"`
	PreviousRouteTargets []sessionCountItem `json:"previous_route_targets"`
	NextRouteTargets     []sessionCountItem `json:"next_route_targets"`
	PreviousChannels     []sessionCountItem `json:"previous_channels"`
	NextChannels         []sessionCountItem `json:"next_channels"`
	PreviousCredentials  []sessionCountItem `json:"previous_credentials"`
	NextCredentials      []sessionCountItem `json:"next_credentials"`
}

type systemEventStreamMessage struct {
	Type       string                     `json:"type"`
	EventID    string                     `json:"event_id,omitempty"`
	Status     string                     `json:"status,omitempty"`
	Severity   string                     `json:"severity,omitempty"`
	Source     string                     `json:"source,omitempty"`
	Category   string                     `json:"category,omitempty"`
	Summary    systemEventSummaryResponse `json:"summary"`
	Unread     int                        `json:"unread"`
	LastSeenAt *time.Time                 `json:"last_seen_at,omitempty"`
}

type systemEventView struct {
	ID              string          `json:"id"`
	Fingerprint     string          `json:"fingerprint"`
	Source          string          `json:"source"`
	Category        string          `json:"category"`
	Severity        string          `json:"severity"`
	Status          string          `json:"status"`
	Title           string          `json:"title"`
	Message         string          `json:"message"`
	Details         json.RawMessage `json:"details_json"`
	TraceID         string          `json:"trace_id,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	JobID           string          `json:"job_id,omitempty"`
	UpstreamID      string          `json:"upstream_id,omitempty"`
	Model           string          `json:"model,omitempty"`
	OccurrenceCount int             `json:"occurrence_count"`
	FirstSeenAt     time.Time       `json:"first_seen_at"`
	LastSeenAt      time.Time       `json:"last_seen_at"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	ReadAt          *time.Time      `json:"read_at,omitempty"`
	ResolvedAt      *time.Time      `json:"resolved_at,omitempty"`
}

type traceListItem struct {
	ID                string              `json:"id"`
	SessionID         string              `json:"session_id,omitempty"`
	SessionSource     string              `json:"session_source,omitempty"`
	RecordedAt        time.Time           `json:"recorded_at"`
	Model             string              `json:"model"`
	Provider          string              `json:"provider"`
	SelectedUpstream  string              `json:"selected_upstream_id,omitempty"`
	SelectedPreset    string              `json:"selected_upstream_provider_preset,omitempty"`
	Operation         string              `json:"operation"`
	Endpoint          string              `json:"endpoint"`
	Method            string              `json:"method"`
	URL               string              `json:"url"`
	StatusCode        int                 `json:"status_code"`
	DurationMs        int64               `json:"duration_ms"`
	TTFTMs            int64               `json:"ttft_ms"`
	TotalTokens       int                 `json:"total_tokens"`
	PromptTokens      int                 `json:"prompt_tokens"`
	CompletionTokens  int                 `json:"completion_tokens"`
	CachedTokens      int                 `json:"cached_tokens"`
	IsStream          bool                `json:"is_stream"`
	Error             string              `json:"error,omitempty"`
	RequestAuditID    string              `json:"request_audit_id,omitempty"`
	ResponseID        string              `json:"response_id,omitempty"`
	ExchangeID        string              `json:"exchange_id,omitempty"`
	ExchangeKind      string              `json:"exchange_kind,omitempty"`
	ExchangeRole      string              `json:"exchange_role,omitempty"`
	ParentExchangeID  string              `json:"parent_exchange_id,omitempty"`
	SequenceIndex     int                 `json:"sequence_index,omitempty"`
	UpstreamCallCount int                 `json:"upstream_call_count,omitempty"`
	UpstreamCalls     []traceListItem     `json:"upstream_calls,omitempty"`
	Observation       observationListView `json:"observation"`
}

type observationListView struct {
	Status        string    `json:"status"`
	Parser        string    `json:"parser,omitempty"`
	ParserVersion string    `json:"parser_version,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type sessionListResponse struct {
	Items       []sessionListItem `json:"items"`
	Page        int               `json:"page"`
	PageSize    int               `json:"page_size"`
	Total       int               `json:"total"`
	TotalPages  int               `json:"total_pages"`
	RefreshedAt time.Time         `json:"refreshed_at"`
}

type sessionListItem struct {
	SessionID      string    `json:"session_id"`
	SessionSource  string    `json:"session_source"`
	RequestCount   int       `json:"request_count"`
	FirstSeen      time.Time `json:"first_seen"`
	LastSeen       time.Time `json:"last_seen"`
	LastModel      string    `json:"last_model"`
	Providers      []string  `json:"providers"`
	SuccessRequest int       `json:"success_request"`
	FailedRequest  int       `json:"failed_request"`
	SuccessRate    float64   `json:"success_rate"`
	TotalTokens    int       `json:"total_tokens"`
	AvgTTFT        int       `json:"avg_ttft"`
	TotalDuration  int64     `json:"total_duration_ms"`
	StreamCount    int       `json:"stream_count"`
}

type sessionDetailResponse struct {
	Summary     sessionListItem       `json:"summary"`
	Breakdown   sessionBreakdownView  `json:"breakdown"`
	Timeline    []sessionTimelineItem `json:"timeline"`
	Performance performanceView       `json:"performance"`
	Analysis    []analysisRunView     `json:"analysis"`
	Traces      []traceListItem       `json:"traces"`
}

type analysisListResponse struct {
	SessionID string            `json:"session_id,omitempty"`
	TraceID   string            `json:"trace_id,omitempty"`
	Items     []analysisRunView `json:"items"`
	Total     int               `json:"total"`
}

type analysisRunView struct {
	ID              int64     `json:"id"`
	TraceID         string    `json:"trace_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	Kind            string    `json:"kind"`
	Analyzer        string    `json:"analyzer"`
	AnalyzerVersion string    `json:"analyzer_version"`
	Model           string    `json:"model,omitempty"`
	InputRef        string    `json:"input_ref"`
	Output          any       `json:"output"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

type analysisJobListResponse struct {
	Items []analysisJobView `json:"items"`
	Total int               `json:"total"`
}

type analysisJobResponse struct {
	Job analysisJobView `json:"job"`
}

type reanalysisResponse struct {
	Job    analysisJobView `json:"job"`
	Result any             `json:"result,omitempty"`
}

type reanalysisRequest struct {
	Mode            string `json:"mode"`
	Scan            bool   `json:"scan"`
	RewriteCassette bool   `json:"rewrite_cassette"`
	Reparse         bool   `json:"reparse"`
}

type batchReanalysisRequest struct {
	Mode         string `json:"mode"`
	Query        string `json:"q"`
	Provider     string `json:"provider"`
	Model        string `json:"model"`
	Endpoint     string `json:"endpoint"`
	Upstream     string `json:"upstream"`
	Status       string `json:"status"`
	Observation  string `json:"observation"`
	MissingUsage bool   `json:"missing_usage"`
	Limit        int    `json:"limit"`
	RepairUsage  bool   `json:"repair_usage"`
	Reparse      bool   `json:"reparse"`
	Scan         bool   `json:"scan"`
}

type analysisJobView struct {
	ID         int64     `json:"id"`
	JobType    string    `json:"job_type"`
	TargetType string    `json:"target_type"`
	TargetID   string    `json:"target_id"`
	Status     string    `json:"status"`
	Steps      []string  `json:"steps"`
	Request    any       `json:"request"`
	Result     any       `json:"result"`
	LastError  string    `json:"last_error,omitempty"`
	Attempts   int       `json:"attempts"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
}

type sessionBreakdownView struct {
	Models       []sessionCountItem `json:"models"`
	Endpoints    []sessionCountItem `json:"endpoints"`
	FailedTraces int                `json:"failed_traces"`
}

type sessionCountItem struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type sessionTimelineItem struct {
	TraceID     string    `json:"trace_id"`
	Time        time.Time `json:"time"`
	Model       string    `json:"model"`
	Provider    string    `json:"provider"`
	Endpoint    string    `json:"endpoint"`
	StatusCode  int       `json:"status_code"`
	DurationMs  int64     `json:"duration_ms"`
	TTFTMs      int64     `json:"ttft_ms"`
	TotalTokens int       `json:"total_tokens"`
	IsStream    bool      `json:"is_stream"`
	Error       string    `json:"error,omitempty"`
}

type detailResponse struct {
	ID                     string                   `json:"id"`
	ResponseID             string                   `json:"response_id,omitempty"`
	RequestAuditID         string                   `json:"request_audit_id,omitempty"`
	ResponsesAudit         *traceResponsesAuditRef  `json:"responses_audit,omitempty"`
	Session                *traceSessionView        `json:"session,omitempty"`
	Header                 recordHeaderView         `json:"header"`
	Events                 []recordEventView        `json:"events"`
	Messages               []ChatMessage            `json:"messages"`
	Tools                  []RequestTool            `json:"tools"`
	AIContent              string                   `json:"ai_content"`
	AIReasoning            string                   `json:"ai_reasoning"`
	AIBlocks               []ContentBlock           `json:"ai_blocks"`
	ToolCalls              []ToolCall               `json:"tool_calls"`
	UpstreamCalls          []traceListItem          `json:"upstream_calls,omitempty"`
	SelectedUpstreamHealth *traceUpstreamHealthView `json:"selected_upstream_health,omitempty"`
	Performance            performanceView          `json:"performance"`
}

type traceResponsesAuditRef struct {
	ResponseID     string `json:"response_id,omitempty"`
	RequestAuditID string `json:"request_audit_id,omitempty"`
}

type performanceResponse struct {
	ID          string          `json:"id"`
	Scope       string          `json:"scope"`
	Performance performanceView `json:"performance"`
}

type performanceView struct {
	RequestCount       int             `json:"request_count"`
	SuccessRequest     int             `json:"success_request"`
	FailedRequest      int             `json:"failed_request"`
	SuccessRate        float64         `json:"success_rate"`
	DurationMs         int64           `json:"duration_ms"`
	TTFTMs             int64           `json:"ttft_ms"`
	TokensPerSec       float64         `json:"tokens_per_sec"`
	TotalTokens        int             `json:"total_tokens"`
	PromptTokens       int             `json:"prompt_tokens"`
	CompletionTokens   int             `json:"completion_tokens"`
	CachedTokens       int             `json:"cached_tokens"`
	CacheRatio         float64         `json:"cache_ratio"`
	StatusCode         int             `json:"status_code,omitempty"`
	ProviderError      string          `json:"provider_error,omitempty"`
	IsStream           bool            `json:"is_stream,omitempty"`
	SelectedUpstreamID string          `json:"selected_upstream_id,omitempty"`
	RoutingPolicy      string          `json:"routing_policy,omitempty"`
	RoutingFallback    bool            `json:"routing_fallback,omitempty"`
	Upstreams          []upstreamPerf  `json:"upstreams,omitempty"`
	ByModel            []perfCountItem `json:"by_model,omitempty"`
	ByEndpoint         []perfCountItem `json:"by_endpoint,omitempty"`
}

type perfCountItem struct {
	Label        string  `json:"label"`
	Count        int     `json:"count"`
	TotalTokens  int     `json:"total_tokens"`
	AvgDuration  int64   `json:"avg_duration_ms"`
	AvgTTFT      int64   `json:"avg_ttft_ms"`
	SuccessRate  float64 `json:"success_rate"`
	TokensPerSec float64 `json:"tokens_per_sec"`
}

type traceUpstreamHealthView struct {
	ID                string              `json:"id"`
	HealthState       string              `json:"health_state"`
	TTFTFastMs        float64             `json:"ttft_fast_ms"`
	TTFTSlowMs        float64             `json:"ttft_slow_ms"`
	LatencyFastMs     float64             `json:"latency_fast_ms"`
	ErrorRate         float64             `json:"error_rate"`
	TimeoutRate       float64             `json:"timeout_rate"`
	Inflight          int64               `json:"inflight"`
	LastRefreshAt     time.Time           `json:"last_refresh_at"`
	LastRefreshStatus string              `json:"last_refresh_status"`
	HealthThresholds  healthThresholdView `json:"health_thresholds"`
}

type rawDetailResponse struct {
	ID               string            `json:"id"`
	RequestProtocol  string            `json:"request_protocol"`
	ResponseProtocol string            `json:"response_protocol"`
	Header           recordHeaderView  `json:"header"`
	Events           []recordEventView `json:"events"`
}

type observationDetailResponse struct {
	ID      string                 `json:"id"`
	Summary observationSummaryView `json:"summary"`
	Nodes   []observationNodeView  `json:"nodes"`
	Tree    []observationNodeView  `json:"tree"`
}

type findingListResponse struct {
	ID       string        `json:"id"`
	Items    []findingView `json:"items"`
	Total    int           `json:"total"`
	Severity string        `json:"severity,omitempty"`
	Category string        `json:"category,omitempty"`
}

type findingView struct {
	ID              string    `json:"id"`
	TraceID         string    `json:"trace_id"`
	Category        string    `json:"category"`
	Severity        string    `json:"severity"`
	Confidence      float64   `json:"confidence"`
	Title           string    `json:"title"`
	Description     string    `json:"description,omitempty"`
	EvidencePath    string    `json:"evidence_path"`
	EvidenceExcerpt string    `json:"evidence_excerpt,omitempty"`
	NodeID          string    `json:"node_id,omitempty"`
	Detector        string    `json:"detector"`
	DetectorVersion string    `json:"detector_version"`
	CreatedAt       time.Time `json:"created_at"`
}

type observationSummaryView struct {
	TraceID       string    `json:"trace_id"`
	Parser        string    `json:"parser"`
	ParserVersion string    `json:"parser_version"`
	Status        string    `json:"status"`
	Provider      string    `json:"provider"`
	Operation     string    `json:"operation"`
	Model         string    `json:"model"`
	Summary       any       `json:"summary"`
	Warnings      any       `json:"warnings"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type observationNodeView struct {
	ID             string                `json:"id"`
	ParentID       string                `json:"parent_id,omitempty"`
	ProviderType   string                `json:"provider_type"`
	NormalizedType string                `json:"normalized_type"`
	Role           string                `json:"role,omitempty"`
	Path           string                `json:"path"`
	Index          int                   `json:"index"`
	Depth          int                   `json:"depth,omitempty"`
	TextPreview    string                `json:"text_preview,omitempty"`
	Raw            json.RawMessage       `json:"raw,omitempty"`
	Children       []observationNodeView `json:"children,omitempty"`
}

type traceSessionView struct {
	SessionID     string `json:"session_id"`
	SessionSource string `json:"session_source"`
}

type recordHeaderView struct {
	Version string      `json:"version"`
	Meta    interface{} `json:"meta"`
	Layout  interface{} `json:"layout"`
	Usage   interface{} `json:"usage"`
}

type recordEventView map[string]interface{}

type timelineItemView struct {
	Kind     string             `json:"kind"`
	Label    string             `json:"label,omitempty"`
	Summary  string             `json:"summary,omitempty"`
	Body     string             `json:"body,omitempty"`
	Role     string             `json:"role,omitempty"`
	Name     string             `json:"name,omitempty"`
	ID       string             `json:"id,omitempty"`
	Status   string             `json:"status,omitempty"`
	Children []timelineItemView `json:"children,omitempty"`
}

type LogStats struct {
	TotalRequest   int     `json:"total_request"`
	AvgTTFT        int     `json:"avg_ttft"`
	TotalTokens    int     `json:"total_tokens"`
	SuccessRequest int     `json:"success_request"`
	FailedRequest  int     `json:"failed_request"`
	SuccessRate    float64 `json:"success_rate"`
}

type RouteOptions struct {
	Router                           *router.Router
	ChannelService                   *channel.Service
	AuthVerifier                     auth.TokenVerifier
	MonitorAuthVerifier              auth.TokenVerifier
	MonitorJWT                       *auth.JWTManager
	AuthStore                        *auth.Store
	SessionTTL                       time.Duration
	ResponsesFunctionExecutors       config.ResponsesFunctionExecutorConfig
	ResponsesFunctionExecutorState   *ResponsesFunctionExecutorState
	ResponsesFunctionExecutorStore   *store.Store
	ResponsesFunctionExecutorManager *functionexec.Manager
}

func WithResponsesFunctionExecutorPersistence(st *store.Store) ResponsesFunctionExecutorStateOption {
	return func(s *ResponsesFunctionExecutorState) {
		s.store = st
	}
}

func WithResponsesFunctionExecutorManager(manager *functionexec.Manager) ResponsesFunctionExecutorStateOption {
	return func(s *ResponsesFunctionExecutorState) {
		s.manager = manager
	}
}

func NewResponsesFunctionExecutorState(cfg config.ResponsesFunctionExecutorConfig, opts ...ResponsesFunctionExecutorStateOption) *ResponsesFunctionExecutorState {
	state := &ResponsesFunctionExecutorState{cfg: cloneResponsesFunctionExecutorConfig(cfg)}
	for _, opt := range opts {
		if opt != nil {
			opt(state)
		}
	}
	return state
}

func (s *ResponsesFunctionExecutorState) summary() responsesFunctionExecutorsSummary {
	if s == nil {
		return responsesFunctionExecutorsSummaryFromConfig(config.ResponsesFunctionExecutorConfig{})
	}
	s.mu.RLock()
	cfg := cloneResponsesFunctionExecutorConfig(s.cfg)
	s.mu.RUnlock()
	return responsesFunctionExecutorsSummaryFromConfig(cfg)
}

func (s *ResponsesFunctionExecutorState) validate(req responsesFunctionExecutorUpdateRequest) (responsesFunctionExecutorsSummary, error) {
	if s == nil {
		s = NewResponsesFunctionExecutorState(config.ResponsesFunctionExecutorConfig{})
	}
	s.mu.RLock()
	cfg := cloneResponsesFunctionExecutorConfig(s.cfg)
	s.mu.RUnlock()
	updated, err := applyResponsesFunctionExecutorUpdate(cfg, req)
	if err != nil {
		return responsesFunctionExecutorsSummary{}, err
	}
	return responsesFunctionExecutorsSummaryFromConfig(updated), nil
}

func (s *ResponsesFunctionExecutorState) apply(ctx context.Context, req responsesFunctionExecutorUpdateRequest) (responsesFunctionExecutorsSummary, error) {
	if s == nil {
		s = NewResponsesFunctionExecutorState(config.ResponsesFunctionExecutorConfig{})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated, err := applyResponsesFunctionExecutorUpdate(cloneResponsesFunctionExecutorConfig(s.cfg), req)
	if err != nil {
		return responsesFunctionExecutorsSummary{}, err
	}
	if _, err := functionexec.Registrations(updated); err != nil {
		return responsesFunctionExecutorsSummary{}, err
	}
	if s.store != nil {
		if err := s.store.SaveResponsesFunctionExecutorConfigSnapshot(ctx, updated); err != nil {
			return responsesFunctionExecutorsSummary{}, err
		}
	}
	if s.manager != nil {
		if err := s.manager.Apply(updated); err != nil {
			return responsesFunctionExecutorsSummary{}, err
		}
	}
	s.cfg = cloneResponsesFunctionExecutorConfig(updated)
	return responsesFunctionExecutorsSummaryFromConfig(updated), nil
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token  string `json:"token"`
	Prefix string `json:"prefix"`
}

type meResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
	Scope    string `json:"scope"`
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type createTokenRequest struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
	TTL   string `json:"ttl"`
}

type createTokenResponse struct {
	Token  string `json:"token"`
	Prefix string `json:"prefix"`
}

type tokenListResponse struct {
	Items []tokenItem `json:"items"`
	Total int         `json:"total"`
}

type tokenItem struct {
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scope      string     `json:"scope"`
	Enabled    bool       `json:"enabled"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

type routingFailureSummaryView struct {
	Total    int                        `json:"total"`
	Reasons  []sessionCountItem         `json:"reasons"`
	Recent   []routingFailureItem       `json:"recent"`
	Timeline []routingFailureBucketItem `json:"timeline"`
}

type routingFailureItem struct {
	TraceID    string    `json:"trace_id"`
	Model      string    `json:"model"`
	Endpoint   string    `json:"endpoint"`
	RecordedAt time.Time `json:"recorded_at"`
	Reason     string    `json:"reason"`
	ErrorText  string    `json:"error_text,omitempty"`
	StatusCode int       `json:"status_code"`
}

type routingFailureBucketItem struct {
	Time  time.Time `json:"time"`
	Count int       `json:"count"`
}

type healthThresholdView struct {
	TTFTDegradedRatio   float64 `json:"ttft_degraded_ratio"`
	ErrorRateDegraded   float64 `json:"error_rate_degraded"`
	TimeoutRateDegraded float64 `json:"timeout_rate_degraded"`
	ErrorRateOpen       float64 `json:"error_rate_open"`
	TimeoutRateOpen     float64 `json:"timeout_rate_open"`
	FailureThreshold    int64   `json:"failure_threshold"`
	OpenWindow          string  `json:"open_window"`
}

func (u *channelHeaderUpdate) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		u.Value = value
		return nil
	}
	var object struct {
		Value  string `json:"value"`
		Keep   bool   `json:"keep"`
		Delete bool   `json:"delete"`
	}
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	u.Value = object.Value
	u.Keep = object.Keep
	u.Delete = object.Delete
	return nil
}

type routingSettingsView struct {
	ResponsesStrategy  string    `json:"responses_strategy"`
	SelectionPolicy    string    `json:"selection_policy"`
	MissingModelPolicy string    `json:"missing_model_policy"`
	RoutePlanLogLevel  string    `json:"route_plan_log_level,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

type routingInspectRequest struct {
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
	Stream   bool   `json:"stream"`
	Tools    bool   `json:"tools"`
}

type routingInspectResponse struct {
	Request     routingInspectRequest `json:"request"`
	Result      *routeplan.Result     `json:"result,omitempty"`
	Error       string                `json:"error,omitempty"`
	RefreshedAt time.Time             `json:"refreshed_at"`
}

type usageSummaryView struct {
	RequestCount     int       `json:"request_count"`
	SuccessRequest   int       `json:"success_request"`
	FailedRequest    int       `json:"failed_request"`
	SuccessRate      float64   `json:"success_rate"`
	MissingUsage     int       `json:"missing_usage_request"`
	TotalTokens      int       `json:"total_tokens"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CachedTokens     int       `json:"cached_tokens"`
	AvgTTFT          int       `json:"avg_ttft"`
	AvgDurationMs    int64     `json:"avg_duration_ms"`
	LastSeen         time.Time `json:"last_seen,omitempty"`
}

type usageTrendView struct {
	Time          time.Time `json:"time"`
	RequestCount  int       `json:"request_count"`
	FailedRequest int       `json:"failed_request"`
	MissingUsage  int       `json:"missing_usage_request"`
	TotalTokens   int       `json:"total_tokens"`
	ModelCount    int       `json:"model_count"`
}

func RegisterRoutes(mux *http.ServeMux, st *store.Store, opts ...RouteOptions) {
	var opt RouteOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	monitorVerifier := opt.MonitorAuthVerifier
	if monitorVerifier == nil {
		monitorVerifier = opt.AuthVerifier
	}
	functionExecutorState := opt.ResponsesFunctionExecutorState
	if functionExecutorState == nil {
		stateOptions := []ResponsesFunctionExecutorStateOption{}
		functionExecutorStore := opt.ResponsesFunctionExecutorStore
		if functionExecutorStore == nil {
			functionExecutorStore = st
		}
		if functionExecutorStore != nil {
			stateOptions = append(stateOptions, WithResponsesFunctionExecutorPersistence(functionExecutorStore))
		}
		if opt.ResponsesFunctionExecutorManager != nil {
			stateOptions = append(stateOptions, WithResponsesFunctionExecutorManager(opt.ResponsesFunctionExecutorManager))
		}
		functionExecutorState = NewResponsesFunctionExecutorState(opt.ResponsesFunctionExecutors, stateOptions...)
	}
	mux.HandleFunc("/api/auth/status", authStatusAPIHandler(monitorVerifier))
	mux.HandleFunc("/api/auth/login", authLoginAPIHandler(opt.AuthStore, opt.MonitorJWT, opt.SessionTTL))
	mux.HandleFunc("/api/auth/check", monitorAuthRequired(authCheckAPIHandler(), monitorVerifier))
	mux.HandleFunc("/api/auth/me", monitorAuthRequired(authMeAPIHandler(), monitorVerifier))
	mux.HandleFunc("/api/auth/password", monitorAuthRequired(authChangePasswordAPIHandler(opt.AuthStore), monitorVerifier))
	mux.HandleFunc("/api/auth/tokens", monitorAuthRequired(authTokensAPIHandler(opt.AuthStore), monitorVerifier))
	mux.HandleFunc("/api/auth/tokens/", monitorAuthRequired(authTokenDetailAPIHandler(opt.AuthStore), monitorVerifier))
	mux.HandleFunc("/api/overview", monitorAuthRequired(overviewAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/events/summary", monitorAuthRequired(systemEventSummaryAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/events/read-all", monitorAuthRequired(systemEventReadAllAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/events/stream", monitorAuthRequired(systemEventStreamAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/events", monitorAuthRequired(systemEventListAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/events/", monitorAuthRequired(systemEventDetailAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/responses/function-executors", monitorAuthRequired(responsesFunctionExecutorsAPIHandler(functionExecutorState), monitorVerifier))
	mux.HandleFunc("/api/responses/audit/trace", monitorAuthRequired(responsesAuditTraceAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/responses/audit/tool-calls", monitorAuthRequired(responsesToolCallAuditsAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/routing/exchanges", monitorAuthRequired(routingExchangeListAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/routing/inspect", monitorAuthRequired(routingInspectAPIHandler(st, opt.Router), monitorVerifier))
	mux.HandleFunc("/api/routing/summary", monitorAuthRequired(routingSummaryAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/settings/routing", monitorAuthRequired(routingSettingsAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/settings/channels", monitorAuthRequired(channelBootstrapSettingsAPIHandler(st, opt.ChannelService), monitorVerifier))
	mux.HandleFunc("/api/model-aliases/validate", monitorAuthRequired(modelAliasValidateAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/model-aliases", monitorAuthRequired(configurationAPIHandler(st, opt.Router, opt.ChannelService, func(st *store.Store, _ *router.Router, _ *channel.Service) http.HandlerFunc {
		return modelAliasListCreateAPIHandler(st)
	}), monitorVerifier))
	mux.HandleFunc("/api/model-aliases/", monitorAuthRequired(configurationAPIHandler(st, opt.Router, opt.ChannelService, func(st *store.Store, _ *router.Router, _ *channel.Service) http.HandlerFunc {
		return modelAliasDetailAPIHandler(st)
	}), monitorVerifier))
	mux.HandleFunc("/api/traces", monitorAuthRequired(listAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/traces/", monitorAuthRequired(traceAPIHandler(st, opt.Router), monitorVerifier))
	mux.HandleFunc("/api/sessions", monitorAuthRequired(sessionListAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/sessions/", monitorAuthRequired(sessionDetailAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/findings", monitorAuthRequired(findingListAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/analysis/batch/reanalyze", monitorAuthRequired(analysisBatchReanalyzeAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/analysis/jobs", monitorAuthRequired(analysisJobListAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/analysis/jobs/", monitorAuthRequired(analysisJobDetailAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/analysis", monitorAuthRequired(analysisListAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/models", monitorAuthRequired(modelListAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/models/", monitorAuthRequired(modelDetailAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/secrets/local-key", monitorAuthRequired(localSecretKeyAPIHandler(st), monitorVerifier))
	mux.HandleFunc("/api/provider-setup/", monitorAuthRequired(providerSetupAPIHandler(st, opt.Router, opt.ChannelService), monitorVerifier))
	mux.HandleFunc("/api/provider-probe/report/apply", monitorAuthRequired(providerProbeReportApplyAPIHandler(st, opt.Router, opt.ChannelService), monitorVerifier))
	mux.HandleFunc("/api/provider-probe/report", monitorAuthRequired(providerProbeReportAPIHandler(st, opt.ChannelService), monitorVerifier))
	mux.HandleFunc("/api/provider-probe", monitorAuthRequired(providerProbeAPIHandler(), monitorVerifier))
	mux.HandleFunc("/api/channels", monitorAuthRequired(channelListCreateAPIHandler(st, opt.Router, opt.ChannelService), monitorVerifier))
	mux.HandleFunc("/api/channels/", monitorAuthRequired(channelDetailAPIHandler(st, opt.Router, opt.ChannelService), monitorVerifier))
	mux.HandleFunc("/api/provider-presets", monitorAuthRequired(providerPresetAPIHandler(), monitorVerifier))
	mux.HandleFunc("/api/upstreams", monitorAuthRequired(upstreamListAPIHandler(st, opt.Router), monitorVerifier))
	mux.HandleFunc("/api/upstreams/", monitorAuthRequired(upstreamDetailAPIHandler(st, opt.Router), monitorVerifier))
	mux.Handle("/", appHandler())
}

func authStatusAPIHandler(verifier auth.TokenVerifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"auth_required": verifier != nil})
	}
}

func authLoginAPIHandler(authStore *auth.Store, jwtManager *auth.JWTManager, ttl time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
			return
		}
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid login payload"})
			return
		}
		var token auth.TokenResult
		var err error
		if jwtManager != nil {
			var principal auth.Principal
			principal, err = authStore.AuthenticatePassword(r.Context(), req.Username, req.Password)
			if err == nil {
				token, err = jwtManager.IssueToken(principal)
			}
		} else {
			token, err = authStore.Login(r.Context(), req.Username, req.Password, ttl)
		}
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
			return
		}
		writeJSON(w, http.StatusOK, loginResponse{Token: token.Token, Prefix: token.Prefix})
	}
}

func (r *responsesFunctionExecutorUpdateRequest) UnmarshalJSON(data []byte) error {
	type alias responsesFunctionExecutorUpdateRequest
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if err := validateJSONKeys(raw, map[string]struct{}{
		"validate_only":    {},
		"enabled":          {},
		"timeout":          {},
		"max_result_bytes": {},
		"redaction":        {},
		"executors":        {},
	}); err != nil {
		return err
	}
	if redactionRaw, ok := raw["redaction"]; ok {
		var redaction map[string]json.RawMessage
		if err := json.Unmarshal(redactionRaw, &redaction); err != nil {
			return err
		}
		if err := validateJSONKeys(redaction, map[string]struct{}{"arguments": {}, "output": {}}); err != nil {
			return err
		}
	}
	if executorsRaw, ok := raw["executors"]; ok {
		var executors []map[string]json.RawMessage
		if err := json.Unmarshal(executorsRaw, &executors); err != nil {
			return err
		}
		for _, executor := range executors {
			if err := validateJSONKeys(executor, map[string]struct{}{
				"name":    {},
				"type":    {},
				"enabled": {},
				"process": {},
			}); err != nil {
				return err
			}
			if processRaw, ok := executor["process"]; ok {
				var process map[string]json.RawMessage
				if err := json.Unmarshal(processRaw, &process); err != nil {
					return err
				}
				if err := validateJSONKeys(process, map[string]struct{}{
					"working_dir":              {},
					"require_absolute_command": {},
					"allowed_command_dirs":     {},
					"reject_root":              {},
				}); err != nil {
					return err
				}
			}
		}
	}
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = responsesFunctionExecutorUpdateRequest(decoded)
	_, r.ExecutorsSet = raw["executors"]
	return nil
}

func validateJSONKeys(raw map[string]json.RawMessage, allowed map[string]struct{}) error {
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	return nil
}

func applyResponsesFunctionExecutorUpdate(cfg config.ResponsesFunctionExecutorConfig, req responsesFunctionExecutorUpdateRequest) (config.ResponsesFunctionExecutorConfig, error) {
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if strings.TrimSpace(req.Timeout) != "" {
		timeout, err := time.ParseDuration(strings.TrimSpace(req.Timeout))
		if err != nil {
			return config.ResponsesFunctionExecutorConfig{}, errors.New("timeout must be a Go duration such as 5s")
		}
		cfg.Timeout = timeout
	}
	if req.MaxResultBytes != nil {
		cfg.MaxResultBytes = *req.MaxResultBytes
	}
	if req.Redaction != nil {
		if req.Redaction.Arguments != nil {
			cfg.Redaction.Arguments = *req.Redaction.Arguments
		}
		if req.Redaction.Output != nil {
			cfg.Redaction.Output = *req.Redaction.Output
		}
	}
	if req.ExecutorsSet {
		cfg.Executors = mergeResponsesFunctionExecutorBindings(cfg.Executors, req.Executors)
	}
	return cfg, nil
}

func mergeResponsesFunctionExecutorBindings(current []config.ResponsesFunctionExecutorBinding, patches []responsesFunctionExecutorBindingPatch) []config.ResponsesFunctionExecutorBinding {
	byName := make(map[string]config.ResponsesFunctionExecutorBinding, len(current))
	for _, binding := range current {
		byName[strings.TrimSpace(binding.Name)] = binding
	}
	out := make([]config.ResponsesFunctionExecutorBinding, 0, len(patches))
	for _, patch := range patches {
		name := strings.TrimSpace(patch.Name)
		binding := byName[name]
		binding.Name = name
		if strings.TrimSpace(patch.Type) != "" {
			binding.Type = patch.Type
		}
		if patch.Enabled != nil {
			binding.Enabled = cloneBoolPtr(patch.Enabled)
		}
		if patch.Process != nil {
			binding.Process.WorkingDir = patch.Process.WorkingDir
			if patch.Process.RequireAbsoluteCommand != nil {
				binding.Process.RequireAbsoluteCommand = *patch.Process.RequireAbsoluteCommand
			}
			if patch.Process.AllowedCommandDirs != nil {
				binding.Process.AllowedCommandDirs = append([]string{}, patch.Process.AllowedCommandDirs...)
			}
			if patch.Process.RejectRoot != nil {
				binding.Process.RejectRoot = *patch.Process.RejectRoot
			}
		}
		out = append(out, binding)
	}
	return out
}

func cloneResponsesFunctionExecutorConfig(cfg config.ResponsesFunctionExecutorConfig) config.ResponsesFunctionExecutorConfig {
	cfg.Warnings = append([]string{}, cfg.Warnings...)
	cfg.Executors = append([]config.ResponsesFunctionExecutorBinding(nil), cfg.Executors...)
	for i := range cfg.Executors {
		cfg.Executors[i].Enabled = cloneBoolPtr(cfg.Executors[i].Enabled)
		cfg.Executors[i].Args = append([]string{}, cfg.Executors[i].Args...)
		cfg.Executors[i].EnvAllowlist = append([]string{}, cfg.Executors[i].EnvAllowlist...)
		cfg.Executors[i].Process.AllowedCommandDirs = append([]string{}, cfg.Executors[i].Process.AllowedCommandDirs...)
		cfg.Executors[i].Warnings = append([]string{}, cfg.Executors[i].Warnings...)
		if cfg.Executors[i].Env != nil {
			env := make(map[string]string, len(cfg.Executors[i].Env))
			for key, value := range cfg.Executors[i].Env {
				env[key] = value
			}
			cfg.Executors[i].Env = env
		}
	}
	return cfg
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func authCheckAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func systemEventListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		windowLabel, since := parseSystemEventWindow(r.URL.Query().Get("window"))
		filter := systemEventFilterFromRequest(r, since)
		page, err := st.ListSystemEvents(filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, systemEventListResponse{
			Items:       systemEventViews(page.Items),
			Page:        page.Page,
			PageSize:    page.PageSize,
			Total:       page.Total,
			TotalPages:  page.TotalPages,
			Window:      windowLabel,
			RefreshedAt: time.Now().UTC(),
		})
	}
}

func systemEventSummaryAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		windowLabel, since := parseSystemEventWindow(r.URL.Query().Get("window"))
		summary, err := st.SystemEventSummary(since)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "summary error: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, systemEventSummaryView(summary, windowLabel))
	}
}

func systemEventReadAllAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_, since := parseSystemEventWindow(r.URL.Query().Get("window"))
		filter := systemEventFilterFromRequest(r, since)
		if strings.TrimSpace(filter.Status) == "" {
			filter.Status = store.SystemEventStatusUnread
		}
		count, err := st.MarkAllSystemEventsRead(filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark read error: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": count})
	}
}

func systemEventStreamAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch, unsubscribe := st.SubscribeSystemEvents(16)
		defer unsubscribe()

		writeSystemEventStreamMessage(w, "system_event.summary", store.SystemEventNotification{}, st)
		flusher.Flush()

		heartbeat := time.NewTicker(25 * time.Second)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case notification, ok := <-ch:
				if !ok {
					return
				}
				writeSystemEventStreamMessage(w, "system_event.updated", notification, st)
				flusher.Flush()
			case <-heartbeat.C:
				_, _ = w.Write([]byte(": ping\n\n"))
				flusher.Flush()
			}
		}
	}
}

func systemEventDetailAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		rest := strings.Trim(strings.TrimPrefix(pathClean(r.URL.Path), "/api/events/"), "/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		eventID := parts[0]
		action := parts[1]
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var err error
		switch action {
		case "read":
			err = st.MarkSystemEventRead(eventID)
		case "resolve":
			err = st.ResolveSystemEvent(eventID)
		case "ignore":
			err = st.IgnoreSystemEvent(eventID)
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update error: " + err.Error()})
			return
		}
		event, err := st.GetSystemEvent(eventID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, systemEventViewFromStore(event))
	}
}

func routingSummaryAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}

		windowLabel, since := parseUpstreamWindow(r.URL.Query().Get("window"))
		modelFilter := strings.TrimSpace(r.URL.Query().Get("model"))
		summary, err := buildRoutingSummary(st, since, modelFilter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "routing summary error: " + err.Error()})
			return
		}
		summary.Window = windowLabel
		summary.Model = modelFilter
		summary.RefreshedAt = time.Now().UTC()
		writeJSON(w, http.StatusOK, summary)
	}
}

func authMeAPIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		writeJSON(w, http.StatusOK, meResponse{
			Username: principal.Username,
			Role:     principal.Role,
			Scope:    principal.Scope,
		})
	}
}

func authChangePasswordAPIHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok || strings.TrimSpace(principal.Username) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		var req changePasswordRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid password payload"})
			return
		}
		if err := authStore.VerifyPassword(r.Context(), principal.Username, req.CurrentPassword); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "current password is incorrect"})
			return
		}
		if err := authStore.ResetPassword(r.Context(), principal.Username, req.NewPassword); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func authTokensAPIHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListAuthTokens(w, r, authStore)
		case http.MethodPost:
			handleCreateAuthToken(w, r, authStore)
		default:
			http.NotFound(w, r)
		}
	}
}

func handleListAuthTokens(w http.ResponseWriter, r *http.Request, authStore *auth.Store) {
	if authStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || strings.TrimSpace(principal.Username) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	tokens, err := authStore.ListTokens(r.Context(), principal.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	items := make([]tokenItem, 0, len(tokens))
	for _, token := range tokens {
		items = append(items, tokenItemFromRecord(token))
	}
	writeJSON(w, http.StatusOK, tokenListResponse{
		Items: items,
		Total: len(items),
	})
}

func handleCreateAuthToken(w http.ResponseWriter, r *http.Request, authStore *auth.Store) {
	if authStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
		return
	}
	principal, ok := auth.PrincipalFromContext(r.Context())
	if !ok || strings.TrimSpace(principal.Username) == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req createTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid token payload"})
		return
	}
	var ttl time.Duration
	if strings.TrimSpace(req.TTL) != "" {
		parsed, err := time.ParseDuration(req.TTL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid ttl"})
			return
		}
		ttl = parsed
	}
	token, err := authStore.CreateToken(r.Context(), principal.Username, req.Name, req.Scope, ttl)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, createTokenResponse{Token: token.Token, Prefix: token.Prefix})
}

func authTokenDetailAPIHandler(authStore *auth.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		if authStore == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth store not configured"})
			return
		}
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok || strings.TrimSpace(principal.Username) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		tokenIDText := strings.TrimPrefix(pathClean(r.URL.Path), "/api/auth/tokens/")
		tokenID, err := strconv.Atoi(tokenIDText)
		if err != nil || tokenID <= 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
			return
		}
		deleteToken := parseBool(r.URL.Query().Get("delete"))
		if deleteToken {
			err = authStore.DeleteToken(r.Context(), principal.Username, tokenID)
		} else {
			err = authStore.RevokeToken(r.Context(), principal.Username, tokenID)
		}
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "token not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func inputModalitiesFromCapabilities(features llmspecs.Capability) []string {
	out := make([]string, 0, 4)
	if features.Has(llmspecs.ModalityTextIn) {
		out = append(out, "text")
	}
	if features.Has(llmspecs.ModalityImageIn) {
		out = append(out, "image")
	}
	if features.Has(llmspecs.ModalityAudioIn) {
		out = append(out, "audio")
	}
	if features.Has(llmspecs.ModalityVideoIn) {
		out = append(out, "video")
	}
	if features.Has(llmspecs.ModalityFileIn) {
		out = append(out, "file")
	}
	return out
}

func outputModalitiesFromCapabilities(features llmspecs.Capability) []string {
	out := make([]string, 0, 3)
	if features.Has(llmspecs.ModalityTextOut) {
		out = append(out, "text")
	}
	if features.Has(llmspecs.ModalityImageOut) {
		out = append(out, "image")
	}
	if features.Has(llmspecs.ModalityAudioOut) {
		out = append(out, "audio")
	}
	if features.Has(llmspecs.ModalityVideoOut) {
		out = append(out, "video")
	}
	if features.Has(llmspecs.ModalityFileOut) {
		out = append(out, "file")
	}
	return out
}

func buildProviderSetupResponse(ctx context.Context, req providerSetupRequest, st *store.Store) (providerSetupResponse, error) {
	report, probeErr := providerprobe.Probe(ctx, providerprobe.ProbeTarget{
		ProviderID:              strings.TrimSpace(valueOrExisting(req.ID, req.Name)),
		BaseURL:                 strings.TrimSpace(req.BaseURL),
		APIKey:                  strings.TrimSpace(req.APIKey),
		Headers:                 setupPlainHeaders(req.Headers),
		SpecifiedAPIType:        strings.TrimSpace(req.APIType),
		SpecifiedProtocolFamily: strings.TrimSpace(req.ProtocolFamily),
	}, nil)
	normalizedReq := mergeProviderSetupSuggestions(req.channelUpsertRequest, report)
	record := channelRecordFromRequest(normalizedReq, store.ChannelConfigRecord{})
	normalized := channelItemFromSetupRecord(st, record, normalizedReq)
	resp := providerSetupResponse{
		Status:           report.Status,
		NormalizedConfig: normalized,
		Secret: providerSetupSecret{
			APIKeySet:          strings.TrimSpace(req.APIKey) != "",
			APIKeyHint:         secretHint(req.APIKey),
			SecretStorageMode:  secretStorageMode(st),
			RedactionGuarantee: "api_key is never echoed; secret headers are redacted",
		},
		Probe:    &report,
		Warnings: append([]string(nil), report.Warnings...),
	}
	if resp.Status == "" {
		resp.Status = "unknown"
	}
	return resp, probeErr
}

func mergeProviderSetupSuggestions(req channelUpsertRequest, report providerprobe.Report) channelUpsertRequest {
	out := req
	if out.Enabled == nil {
		enabled := true
		out.Enabled = &enabled
	}
	if strings.TrimSpace(out.APIType) == "" && strings.TrimSpace(report.SuggestedAPIType) != "" {
		out.APIType = report.SuggestedAPIType
	}
	if strings.TrimSpace(out.ProtocolFamily) == "" && strings.TrimSpace(report.SuggestedProtocolFamily) != "" {
		out.ProtocolFamily = report.SuggestedProtocolFamily
	}
	if out.Capabilities == nil {
		out.Capabilities = &config.UpstreamCapabilitiesConfig{}
	}
	for _, capability := range report.Capabilities {
		upstream.SetCapabilityIfUnset(out.Capabilities, capability, true)
	}
	return out
}

func secretStorageMode(st *store.Store) string {
	if st == nil {
		return ""
	}
	return st.SecretStorageMode()
}

func setupPlainHeaders(headers map[string]channelHeaderUpdate) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, update := range headers {
		if update.Delete || update.Keep {
			continue
		}
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		out[name] = update.Value
	}
	return out
}

func appliedProviderProbeSuggestions(items []channel.ProviderProbeApplyItem) bool {
	for _, item := range items {
		if item.Applied {
			return true
		}
	}
	return false
}

func localSecretKeyAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("export") == "1" {
				key, status, err := st.ExportLocalSecretKey()
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				name := "trace_index.secret." + valueOrExisting(status.Fingerprint, "backup")
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(key)
				return
			}
			status := st.SecretStatus()
			writeJSON(w, http.StatusOK, status)
		case http.MethodPost:
			if r.URL.Query().Get("rotate") != "1" {
				http.NotFound(w, r)
				return
			}
			result, err := st.RotateLocalSecretKey()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		default:
			http.NotFound(w, r)
		}
	}
}

func monitorAuthRequired(next http.HandlerFunc, verifier auth.TokenVerifier) http.HandlerFunc {
	if verifier == nil {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		authReq := r
		if token := strings.TrimSpace(r.URL.Query().Get("access_token")); token != "" && r.Header.Get("Authorization") == "" && allowMonitorQueryAccessToken(r) {
			authReq = r.Clone(r.Context())
			authReq.Header = r.Header.Clone()
			authReq.Header.Set("Authorization", "Bearer "+token)
		}
		principal, ok := auth.VerifyRequest(authReq, verifier)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="trajecta-monitor"`)
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	}
}

func allowMonitorQueryAccessToken(r *http.Request) bool {
	return r.Method == http.MethodGet && pathClean(r.URL.Path) == "/api/events/stream"
}

func appHandler() http.Handler {
	distFS, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "embedded ui not available", http.StatusInternalServerError)
		})
	}

	fileServer := http.FileServer(http.FS(distFS))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}

		clean := strings.TrimPrefix(pathClean(r.URL.EscapedPath()), "/")
		if clean == "" {
			serveEmbeddedIndex(distFS, w, r)
			return
		}
		if _, err := fs.Stat(distFS, clean); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		serveEmbeddedIndex(distFS, w, r)
	})
}

func decodeChannelProbeRequest(r *http.Request) (channelProbeRequest, error) {
	var req channelProbeRequest
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return req, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return channelProbeRequest{}, errors.New("invalid probe payload")
	}
	return req, nil
}

const routingSettingsKey = "routing.settings"

func routingSettingsAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			settings, err := loadRoutingSettings(r.Context(), st)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, settings)
		case http.MethodPatch:
			settings, err := loadRoutingSettings(r.Context(), st)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			var req routingSettingsView
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid routing settings payload"})
				return
			}
			settings = mergeRoutingSettings(settings, req)
			if err := validateRoutingSettings(settings); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			settings.UpdatedAt = time.Now().UTC()
			if err := st.SaveAppSettingJSON(r.Context(), routingSettingsKey, settings); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, settings)
		default:
			http.NotFound(w, r)
		}
	}
}

func routingInspectAPIHandler(st *store.Store, rtr *router.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		var req routingInspectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid routing inspect payload"})
			return
		}
		settings, err := loadRoutingSettings(r.Context(), st)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		planReq, upstreams, err := routingInspectPlanInput(st, req, settings)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		applyLiveHealthToInspectCandidates(upstreams, rtr, planReq.RequestedModel)
		result, planErr := routeplan.Plan(planReq, upstreams)
		resp := routingInspectResponse{Request: req, RefreshedAt: time.Now().UTC()}
		if planErr != nil {
			resp.Error = planErr.Error()
			resp.Result = &result
			writeJSON(w, http.StatusOK, resp)
			return
		}
		resp.Result = &result
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleChannelConfig(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string) {
	switch r.Method {
	case http.MethodGet:
		record, err := st.GetChannelConfig(channelID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		models, err := st.ListChannelModels(channelID, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		item := channelItemFromRecord(st, record, len(models), enabledChannelModelCount(models, channelID))
		windowLabel, since := parseAnalyticsWindow(r.URL.Query().Get("window"))
		bucketSize, bucketCount := analyticsBucketSpec(windowLabel)
		enrichChannelItemAnalytics(st, &item, channelID, since, bucketSize, bucketCount, true)
		writeJSON(w, http.StatusOK, item)
	case http.MethodPatch:
		existing, err := st.GetChannelConfig(channelID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		var req channelUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid channel payload"})
			return
		}
		req.ID = channelID
		record, err := st.UpsertChannelConfig(channelRecordFromRequest(req, existing))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		models, _ := st.ListChannelModels(channelID, false)
		writeJSON(w, http.StatusOK, channelItemFromRecord(st, record, len(models), enabledChannelModelCount(models, channelID)))
	case http.MethodDelete:
		if err := st.DeleteChannelConfig(channelID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func handleChannelModels(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string) {
	switch r.Method {
	case http.MethodGet:
		models, err := st.ListChannelModels(channelID, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		items := make([]channelModelItem, 0, len(models))
		for _, model := range models {
			items = append(items, channelModelItemFromRecord(model))
		}
		writeJSON(w, http.StatusOK, channelModelsResponse{Items: items, RefreshedAt: time.Now().UTC()})
	case http.MethodPost:
		var req channelModelCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		record, err := st.UpsertChannelModel(channelID, store.ChannelModelRecord{
			Model:       req.Model,
			DisplayName: req.DisplayName,
			Source:      "manual",
			Enabled:     enabled,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, channelModelItemFromRecord(record))
	default:
		http.NotFound(w, r)
	}
}

func handleChannelModelsBatch(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string) {
	if r.Method != http.MethodPatch {
		http.NotFound(w, r)
		return
	}
	var req channelModelBatchPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
		return
	}
	models := normalizeModelList(req.Models)
	if len(models) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "models are required"})
		return
	}
	for _, model := range models {
		if err := st.SetChannelModelEnabled(channelID, model, req.Enabled); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, channelModelBatchPatchResponse{Updated: len(models), Models: models, Enabled: req.Enabled})
}

func handleChannelModel(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string, model string) {
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		if err := st.DeleteChannelModel(channelID, model); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
		return
	}
	var req channelModelPatchRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
		return
	}
	// The capability columns are tri-state (unset = inherit). An explicit JSON
	// null means "clear back to inherit", which a *bool alone cannot express,
	// so presence of the key is checked separately.
	var rawFields map[string]json.RawMessage
	_ = json.Unmarshal(bodyBytes, &rawFields)
	explicitNull := func(key string) bool {
		value, ok := rawFields[key]
		return ok && strings.TrimSpace(string(value)) == "null"
	}
	record, err := st.UpdateChannelModelProfile(channelID, model, store.ChannelModelProfilePatch{
		DisplayName:                  req.DisplayName,
		Enabled:                      req.Enabled,
		SupportsResponses:            req.SupportsResponses,
		ClearSupportsResponses:       explicitNull("supports_responses"),
		SupportsChatCompletions:      req.SupportsChatCompletions,
		ClearSupportsChatCompletions: explicitNull("supports_chat_completions"),
		SupportsEmbeddings:           req.SupportsEmbeddings,
		ClearSupportsEmbeddings:      explicitNull("supports_embeddings"),
		ContextWindow:                req.ContextWindow,
		MaxOutputTokens:              req.MaxOutputTokens,
		CompactHistoryItemThreshold:  req.CompactHistoryItemThreshold,
		UpstreamModel:                req.UpstreamModel,
		ProfileSource:                req.ProfileSource,
		ProfileAdoptionStatus:        req.ProfileAdoptionStatus,
	})
	if errors.Is(err, sql.ErrNoRows) {
		record, err = st.UpsertChannelModel(channelID, channelModelRecordFromPatch(model, req))
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, channelModelItemFromRecord(record))
}

func capabilityIntPtr(value *bool) *int {
	if value == nil {
		return nil
	}
	out := 0
	if *value {
		out = 1
	}
	return &out
}

func positiveIntPtr(value *int) *int {
	if value == nil || *value <= 0 {
		return nil
	}
	return value
}

func normalizeModelList(models []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	sort.Strings(out)
	return out
}

func defaultRoutingSettings() routingSettingsView {
	return routingSettingsView{
		// Native Responses upstreams are preferred; the local Responses server
		// is only a fallback. This makes the decision follow the requested
		// model: a model served by a Responses-capable channel is proxied
		// as-is, while one served only by Chat Completions goes through the
		// local server. See docs/RESPONSES_RUNTIME.md.
		ResponsesStrategy:  "auto",
		SelectionPolicy:    router.PolicyP2C,
		MissingModelPolicy: router.FallbackReject,
		RoutePlanLogLevel:  "normal",
	}
}

func loadRoutingSettings(ctx context.Context, st *store.Store) (routingSettingsView, error) {
	settings := defaultRoutingSettings()
	if st == nil {
		return settings, nil
	}
	var persisted routingSettingsView
	found, err := st.LoadAppSettingJSON(ctx, routingSettingsKey, &persisted)
	if err != nil {
		return routingSettingsView{}, err
	}
	if found {
		settings = mergeRoutingSettings(settings, persisted)
	}
	if err := validateRoutingSettings(settings); err != nil {
		return defaultRoutingSettings(), nil
	}
	return settings, nil
}

func mergeRoutingSettings(base routingSettingsView, patch routingSettingsView) routingSettingsView {
	if strings.TrimSpace(patch.ResponsesStrategy) != "" {
		base.ResponsesStrategy = strings.TrimSpace(patch.ResponsesStrategy)
	}
	if strings.TrimSpace(patch.SelectionPolicy) != "" {
		base.SelectionPolicy = strings.TrimSpace(patch.SelectionPolicy)
	}
	if strings.TrimSpace(patch.MissingModelPolicy) != "" {
		base.MissingModelPolicy = strings.TrimSpace(patch.MissingModelPolicy)
	}
	if strings.TrimSpace(patch.RoutePlanLogLevel) != "" {
		base.RoutePlanLogLevel = strings.TrimSpace(patch.RoutePlanLogLevel)
	}
	if !patch.UpdatedAt.IsZero() {
		base.UpdatedAt = patch.UpdatedAt
	}
	return base
}

func validateRoutingSettings(settings routingSettingsView) error {
	switch routeplan.ResponsesStrategy(settings.ResponsesStrategy) {
	case routeplan.ResponsesStrategyAuto, routeplan.ResponsesStrategyPreferNative, routeplan.ResponsesStrategyPreferLocalServer, routeplan.ResponsesStrategyNativeOnly, routeplan.ResponsesStrategyLocalServerOnly:
	default:
		return fmt.Errorf("unsupported responses_strategy %q", settings.ResponsesStrategy)
	}
	switch settings.SelectionPolicy {
	case router.PolicyP2C, router.PolicyFirstAvailable:
	default:
		return fmt.Errorf("unsupported selection_policy %q", settings.SelectionPolicy)
	}
	switch settings.MissingModelPolicy {
	case router.FallbackReject, "fallback":
	default:
		return fmt.Errorf("unsupported missing_model_policy %q", settings.MissingModelPolicy)
	}
	switch settings.RoutePlanLogLevel {
	case "", "normal", "verbose":
	default:
		return fmt.Errorf("unsupported route_plan_log_level %q", settings.RoutePlanLogLevel)
	}
	return nil
}

func normalizeModelAliasPreview(record store.ModelAliasRecord) store.ModelAliasRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.Alias = strings.ToLower(strings.TrimSpace(record.Alias))
	record.TargetModel = strings.ToLower(strings.TrimSpace(record.TargetModel))
	record.ChannelID = strings.TrimSpace(record.ChannelID)
	record.Description = strings.TrimSpace(record.Description)
	record.Source = strings.TrimSpace(record.Source)
	return record
}

func writeAliasError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model alias not found"})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

func writeAliasValidationError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrModelAliasConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func routingInspectPlanInput(st *store.Store, req routingInspectRequest, settings routingSettingsView) (routeplan.Request, []routeplan.UpstreamCandidate, error) {
	entrypoint, err := routingInspectEntrypoint(req.Endpoint)
	if err != nil {
		return routeplan.Request{}, nil, err
	}
	model := strings.TrimSpace(req.Model)
	resolved, err := resolvedModelCandidatesForInspect(st, model)
	if err != nil {
		return routeplan.Request{}, nil, err
	}
	upstreams, err := upstreamCandidatesForInspect(st)
	if err != nil {
		return routeplan.Request{}, nil, err
	}
	return routeplan.Request{
		Entrypoint:              entrypoint,
		RequestedModel:          model,
		ResolvedModelCandidates: resolved,
		ResponsesStrategy:       routeplan.ResponsesStrategy(settings.ResponsesStrategy),
		HasTools:                req.Tools,
		Stream:                  req.Stream,
	}, upstreams, nil
}

// applyLiveHealthToInspectCandidates overlays the router's live health onto the
// statically loaded candidates so the inspector previews the same decision the
// proxy would actually take (circuit breakers, degraded state, …).
func applyLiveHealthToInspectCandidates(upstreams []routeplan.UpstreamCandidate, rtr *router.Router, model string) {
	if rtr == nil || len(upstreams) == 0 {
		return
	}
	snapshots := rtr.Snapshots()
	if len(snapshots) == 0 {
		return
	}
	byID := make(map[string]router.Snapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.ID != "" {
			byID[snapshot.ID] = snapshot
		}
		if snapshot.RouteTargetID != "" {
			byID[snapshot.RouteTargetID] = snapshot
		}
	}
	now := time.Now()
	for i := range upstreams {
		snapshot, ok := byID[upstreams[i].ID]
		if !ok {
			continue
		}
		applyInspectSnapshot(upstreams, i, snapshot, model, now)
	}
}

// applyInspectSnapshot projects one router snapshot onto one candidate.
func applyInspectSnapshot(upstreams []routeplan.UpstreamCandidate, i int, snapshot router.Snapshot, model string, now time.Time) {
	{
		health := &routeplan.CandidateHealth{
			HealthState: snapshot.HealthState,
			Selectable:  inspectHealthSelectable(snapshot, now),
		}
		if !snapshot.OpenUntil.IsZero() {
			openUntil := snapshot.OpenUntil
			health.OpenUntil = &openUntil
		}
		// The per-model breaker opens on that model's own failure EWMA even
		// while the channel-level state stays healthy, so it has to be overlaid
		// separately or the inspector previews a selection the proxy refuses.
		if modelState, ok := snapshot.ModelHealth[strings.ToLower(strings.TrimSpace(model))]; ok {
			health.ModelHealthState = modelState.HealthState
			if !modelState.OpenUntil.IsZero() {
				modelOpenUntil := modelState.OpenUntil
				health.ModelOpenUntil = &modelOpenUntil
			}
			if modelState.HealthState == router.HealthOpen &&
				!modelState.OpenUntil.IsZero() && now.Before(modelState.OpenUntil) {
				health.Selectable = false
				health.Reason = routeplan.ReasonModelCircuitOpen
			}
		}
		if !health.Selectable && health.Reason == "" {
			if health.OpenUntil != nil && health.OpenUntil.After(now) {
				health.Reason = routeplan.ReasonTargetCircuitOpen
			} else {
				health.Reason = routeplan.ReasonTargetUnhealthy
			}
		}
		upstreams[i].Health = health
	}
}

// probationInflightLimitMirror mirrors router.probationInflightLimit: a
// probation target serves one in-flight request at a time.
const probationInflightLimitMirror int64 = 1

// inspectHealthSelectable mirrors the router's canSelect health rules.
func inspectHealthSelectable(snapshot router.Snapshot, now time.Time) bool {
	if !snapshot.Enabled {
		return false
	}
	switch snapshot.HealthState {
	case router.HealthOpen:
		if snapshot.OpenUntil.IsZero() || now.Before(snapshot.OpenUntil) {
			return false
		}
	case router.HealthProbation:
		if snapshot.Inflight >= probationInflightLimitMirror {
			return false
		}
	}
	return true
}

func routingInspectEntrypoint(value string) (routeplan.ClientEntrypoint, error) {
	switch strings.TrimSpace(value) {
	case "", "responses", "/v1/responses":
		return routeplan.EntrypointResponses, nil
	case "chat_completions", "/v1/chat/completions":
		return routeplan.EntrypointChatCompletions, nil
	case "anthropic_messages", "/v1/messages":
		return routeplan.EntrypointAnthropicMessage, nil
	default:
		return "", fmt.Errorf("unsupported endpoint %q", value)
	}
}

func resolvedModelCandidatesForInspect(st *store.Store, model string) ([]routeplan.ResolvedModelCandidate, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil
	}
	out := []routeplan.ResolvedModelCandidate{{Model: model, Source: "request"}}
	aliases, err := st.ListModelAliases(model, true)
	if err != nil {
		return nil, err
	}
	for _, alias := range aliases {
		out = append(out, routeplan.ResolvedModelCandidate{
			Model:     alias.TargetModel,
			Alias:     alias.Alias,
			ChannelID: alias.ChannelID,
			Source:    alias.Source,
		})
	}
	return out, nil
}

type inspectCapabilities struct {
	chatCompletions   bool
	responses         bool
	anthropicMessages bool
	toolCalling       bool
}

func inspectModelCapabilities(model store.ChannelModelRecord) (routeplan.ModelCapabilities, bool) {
	caps := routeplan.ModelCapabilities{
		SupportsChatCompletions: inspectCapabilityBool(model.SupportsChatCompletions),
		SupportsResponses:       inspectCapabilityBool(model.SupportsResponses),
	}
	if caps.SupportsChatCompletions == nil && caps.SupportsResponses == nil {
		return routeplan.ModelCapabilities{}, false
	}
	return caps, true
}

func inspectCapabilityBool(value *int) *bool {
	if value == nil {
		return nil
	}
	enabled := *value != 0
	return &enabled
}

func parseBoolQuery(value string, fallback bool) bool {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func reloadRouterFromChannels(rtr *router.Router, channelService *channel.Service) error {
	if rtr == nil {
		return nil
	}
	if channelService == nil {
		return errors.New("channel service not configured")
	}
	targets, err := channelService.RuntimeTargets()
	if err != nil {
		return err
	}
	return rtr.Reload(targets)
}

func effectiveChannelService(st *store.Store, channelService *channel.Service) *channel.Service {
	if channelService != nil {
		return channelService
	}
	if st == nil {
		return nil
	}
	return channel.NewService(st)
}

func enrichModelChannelItemFromRecord(item *modelChannelItem, record store.ChannelModelRecord) {
	if item == nil {
		return
	}
	item.DisplayName = record.DisplayName
	item.Enabled = record.Enabled
	item.Source = record.Source
	item.SupportsResponses = capabilityBool(record.SupportsResponses)
	item.SupportsChatCompletions = capabilityBool(record.SupportsChatCompletions)
	item.SupportsEmbeddings = capabilityBool(record.SupportsEmbeddings)
	item.ContextWindow = record.ContextWindow
	item.MaxOutputTokens = record.MaxOutputTokens
	item.CompactHistoryItemThreshold = record.CompactHistoryItemThreshold
	item.UpstreamModel = record.UpstreamModel
	item.ProfileSource = record.ProfileSource
	item.ProfileAdoptionStatus = record.ProfileAdoptionStatus
	item.InputModalities = decodeStringList(record.InputModalitiesJSON)
	item.OutputModalities = decodeStringList(record.OutputModalitiesJSON)
}

func capabilityBool(value *int) *bool {
	if value == nil {
		return nil
	}
	out := *value != 0
	return &out
}

func decodeStringList(raw string) []string {
	var out []string
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "[]"
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

func enrichChannelItemAnalytics(st *store.Store, item *channelItem, channelID string, since time.Time, bucketSize time.Duration, bucketCount int, includeDetail bool) {
	if st == nil || item == nil {
		return
	}
	if summary, err := st.GetChannelUsageSummary(channelID, since); err == nil {
		item.Summary = usageSummaryViewFromRecord(summary)
	}
	if trends, err := st.GetChannelUsageTrends(channelID, since, bucketSize, bucketCount); err == nil {
		item.Trends = usageTrendViews(trends)
	}
	if !includeDetail {
		return
	}
	if models, err := st.GetChannelModelUsage(channelID, since); err == nil {
		item.ModelsUsage = make([]modelChannelItem, 0, len(models))
		for _, model := range models {
			item.ModelsUsage = append(item.ModelsUsage, modelChannelItem{
				ChannelID: model.ChannelID,
				Model:     model.Model,
				Enabled:   model.Enabled,
				Source:    model.Source,
				Summary:   usageSummaryViewFromRecord(model.Summary),
			})
		}
	}
	if failures, err := st.GetChannelRecentFailures(channelID, since, 10); err == nil {
		item.RecentFailures = toUpstreamFailureItems(failures)
	}
	if probeRuns, err := st.ListChannelProbeRuns(channelID, 8); err == nil {
		item.RecentProbeRuns = channelProbeRunItems(probeRuns)
	}
}

func probeRunMeta(raw string) (string, string) {
	var meta struct {
		FailureReason string `json:"failure_reason"`
		RetryHint     string `json:"retry_hint"`
	}
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return "", ""
	}
	return meta.FailureReason, meta.RetryHint
}

func usageSummaryViewFromRecord(record store.UsageSummaryRecord) usageSummaryView {
	return usageSummaryView{
		RequestCount:     record.RequestCount,
		SuccessRequest:   record.SuccessRequest,
		FailedRequest:    record.FailedRequest,
		SuccessRate:      record.SuccessRate,
		MissingUsage:     record.MissingUsage,
		TotalTokens:      record.TotalTokens,
		PromptTokens:     record.PromptTokens,
		CompletionTokens: record.CompletionTokens,
		CachedTokens:     record.CachedTokens,
		AvgTTFT:          record.AvgTTFT,
		AvgDurationMs:    record.AvgDurationMs,
		LastSeen:         record.LastSeen,
	}
}

func usageTrendViews(records []store.UsageTrendRecord) []usageTrendView {
	out := make([]usageTrendView, 0, len(records))
	for _, record := range records {
		out = append(out, usageTrendView{
			Time:          record.Time,
			RequestCount:  record.RequestCount,
			FailedRequest: record.FailedRequest,
			MissingUsage:  record.MissingUsage,
			TotalTokens:   record.TotalTokens,
			ModelCount:    record.ModelCount,
		})
	}
	return out
}

func enabledChannelModelCount(models []store.ChannelModelRecord, channelID string) int {
	count := 0
	for _, model := range models {
		if model.ChannelID == channelID && model.Enabled {
			count++
		}
	}
	return count
}

func redactHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for key, value := range headers {
		if isSecretHeader(key) && strings.TrimSpace(value) != "" {
			out[key] = "***"
			continue
		}
		out[key] = value
	}
	return out
}

func applyHeaderUpdates(existingJSON string, updates map[string]channelHeaderUpdate) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(existingJSON) != "" {
		_ = json.Unmarshal([]byte(existingJSON), &out)
	}
	next := map[string]string{}
	for key, update := range updates {
		name := strings.TrimSpace(key)
		if name == "" || update.Delete {
			continue
		}
		if update.Keep {
			if value, ok := out[name]; ok {
				next[name] = value
			}
			continue
		}
		next[name] = update.Value
	}
	return next
}

func isSecretHeader(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "authorization" || strings.Contains(key, "api-key") || strings.Contains(key, "apikey") || strings.Contains(key, "token")
}

func valueOrExisting(value string, existing string) string {
	if strings.TrimSpace(value) == "" {
		return existing
	}
	return strings.TrimSpace(value)
}

func secretHint(secret string) string {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return secret
	}
	return secret[:3] + "..." + secret[len(secret)-4:]
}

func buildUpstreamItems(st *store.Store, rtr *router.Router, since time.Time, modelFilter string) ([]upstreamItem, error) {
	var items []upstreamItem
	analyticsByID := map[string]store.UpstreamAnalyticsRecord{}
	if st != nil {
		analytics, err := st.ListUpstreamAnalytics(5, 3, since, modelFilter)
		if err != nil {
			return nil, fmt.Errorf("query upstream analytics: %w", err)
		}
		for _, item := range analytics {
			analyticsByID[item.UpstreamID] = item
		}
	}

	switch {
	case rtr != nil:
		for _, snapshot := range rtr.Snapshots() {
			items = append(items, newUpstreamItemFromSnapshot(snapshot, analyticsByID[snapshot.ID]))
		}
	case st != nil:
		targets, err := st.ListUpstreamTargets()
		if err != nil {
			return nil, fmt.Errorf("query upstream targets: %w", err)
		}
		models, err := st.ListUpstreamModels()
		if err != nil {
			return nil, fmt.Errorf("query upstream models: %w", err)
		}
		modelMap := make(map[string][]string)
		for _, model := range models {
			modelMap[model.UpstreamID] = append(modelMap[model.UpstreamID], model.Model)
		}
		for _, target := range targets {
			sort.Strings(modelMap[target.ID])
			items = append(items, newUpstreamItemFromStore(target, modelMap[target.ID], analyticsByID[target.ID]))
		}
	default:
		return nil, errors.New("router not configured")
	}
	return items, nil
}

func newUpstreamItemFromSnapshot(snapshot router.Snapshot, analytics store.UpstreamAnalyticsRecord) upstreamItem {
	return upstreamItem{
		ID:                snapshot.ID,
		Enabled:           snapshot.Enabled,
		Priority:          snapshot.Priority,
		Weight:            snapshot.Weight,
		CapacityHint:      snapshot.CapacityHint,
		ModelDiscovery:    snapshot.ModelDiscovery,
		BaseURL:           snapshot.BaseURL,
		ProviderPreset:    snapshot.ProviderPreset,
		APIType:           snapshot.APIType,
		Mode:              snapshot.Mode,
		ProtocolFamily:    snapshot.ProtocolFamily,
		RoutingProfile:    snapshot.RoutingProfile,
		HealthState:       snapshot.HealthState,
		Inflight:          snapshot.Inflight,
		TTFTFastMs:        snapshot.TTFTFastMs,
		TTFTSlowMs:        snapshot.TTFTSlowMs,
		LatencyFastMs:     snapshot.LatencyFastMs,
		ErrorRate:         snapshot.ErrorRate,
		TimeoutRate:       snapshot.TimeoutRate,
		LastRefreshAt:     snapshot.LastRefreshAt,
		LastRefreshStatus: snapshot.LastRefreshStatus,
		LastRefreshError:  snapshot.LastRefreshError,
		OpenUntil:         snapshot.OpenUntil,
		Models:            snapshot.Models,
		RequestCount:      analytics.RequestCount,
		SuccessRequest:    analytics.SuccessRequest,
		FailedRequest:     analytics.FailedRequest,
		SuccessRate:       analytics.SuccessRate,
		TotalTokens:       analytics.TotalTokens,
		AvgTTFT:           analytics.AvgTTFT,
		LastSeen:          analytics.LastSeen,
		RecentModels:      analytics.Models,
		LastModel:         analytics.LastModel,
		RecentErrors:      analytics.RecentErrors,
		RecentFailures:    toUpstreamFailureItems(analytics.RecentFailures),
	}
}

func toHealthThresholdView(thresholds router.HealthThresholds) healthThresholdView {
	return healthThresholdView{
		TTFTDegradedRatio:   thresholds.TTFTDegradedRatio,
		ErrorRateDegraded:   thresholds.ErrorRateDegraded,
		TimeoutRateDegraded: thresholds.TimeoutRateDegraded,
		ErrorRateOpen:       thresholds.ErrorRateOpen,
		TimeoutRateOpen:     thresholds.TimeoutRateOpen,
		FailureThreshold:    thresholds.FailureThreshold,
		OpenWindow:          thresholds.OpenWindow.String(),
	}
}

func selectedUpstreamHealthView(rtr *router.Router, upstreamID string) *traceUpstreamHealthView {
	if rtr == nil || strings.TrimSpace(upstreamID) == "" {
		return nil
	}
	thresholds := toHealthThresholdView(rtr.HealthThresholds())
	for _, snapshot := range rtr.Snapshots() {
		if snapshot.ID != upstreamID {
			continue
		}
		return &traceUpstreamHealthView{
			ID:                snapshot.ID,
			HealthState:       snapshot.HealthState,
			TTFTFastMs:        snapshot.TTFTFastMs,
			TTFTSlowMs:        snapshot.TTFTSlowMs,
			LatencyFastMs:     snapshot.LatencyFastMs,
			ErrorRate:         snapshot.ErrorRate,
			TimeoutRate:       snapshot.TimeoutRate,
			Inflight:          snapshot.Inflight,
			LastRefreshAt:     snapshot.LastRefreshAt,
			LastRefreshStatus: snapshot.LastRefreshStatus,
			HealthThresholds:  thresholds,
		}
	}
	return nil
}

func newUpstreamItemFromStore(target store.UpstreamTargetRecord, models []string, analytics store.UpstreamAnalyticsRecord) upstreamItem {
	return upstreamItem{
		ID:                target.ID,
		Enabled:           target.Enabled,
		Priority:          target.Priority,
		Weight:            target.Weight,
		CapacityHint:      target.CapacityHint,
		BaseURL:           target.BaseURL,
		ProviderPreset:    target.ProviderPreset,
		ProtocolFamily:    target.ProtocolFamily,
		RoutingProfile:    target.RoutingProfile,
		HealthState:       "unknown",
		LastRefreshAt:     target.LastRefreshAt,
		LastRefreshStatus: target.LastRefreshStatus,
		LastRefreshError:  target.LastRefreshError,
		Models:            models,
		RequestCount:      analytics.RequestCount,
		SuccessRequest:    analytics.SuccessRequest,
		FailedRequest:     analytics.FailedRequest,
		SuccessRate:       analytics.SuccessRate,
		TotalTokens:       analytics.TotalTokens,
		AvgTTFT:           analytics.AvgTTFT,
		LastSeen:          analytics.LastSeen,
		RecentModels:      analytics.Models,
		LastModel:         analytics.LastModel,
		RecentErrors:      analytics.RecentErrors,
		RecentFailures:    toUpstreamFailureItems(analytics.RecentFailures),
	}
}

func toUpstreamFailureItems(records []store.UpstreamFailureRecord) []upstreamFailureItem {
	out := make([]upstreamFailureItem, 0, len(records))
	for _, record := range records {
		out = append(out, upstreamFailureItem{
			TraceID:    record.TraceID,
			Model:      record.Model,
			Endpoint:   record.Endpoint,
			Reason:     record.Reason,
			StatusCode: record.StatusCode,
			RecordedAt: record.RecordedAt,
			ErrorText:  record.ErrorText,
		})
	}
	return out
}

func toRoutingFailureItems(records []store.RoutingFailureRecord) []routingFailureItem {
	out := make([]routingFailureItem, 0, len(records))
	for _, record := range records {
		out = append(out, routingFailureItem{
			TraceID:    record.TraceID,
			Model:      record.Model,
			Endpoint:   record.Endpoint,
			RecordedAt: record.RecordedAt,
			Reason:     record.Reason,
			ErrorText:  record.ErrorText,
			StatusCode: record.StatusCode,
		})
	}
	return out
}

func toRoutingFailureBucketItems(records []store.TimeCountItem) []routingFailureBucketItem {
	out := make([]routingFailureBucketItem, 0, len(records))
	for _, record := range records {
		out = append(out, routingFailureBucketItem{
			Time:  record.Time,
			Count: record.Count,
		})
	}
	return out
}

func routingFailureBucketSpec(window string) (time.Duration, int) {
	switch window {
	case "today":
		return time.Hour, 24
	case "7d":
		return 12 * time.Hour, 14
	case "30d":
		return 24 * time.Hour, 30
	case "all":
		return 24 * time.Hour, 14
	default:
		return time.Hour, 24
	}
}

func analyticsBucketSpec(window string) (time.Duration, int) {
	switch window {
	case "today":
		return time.Hour, 24
	case "30d":
		return 24 * time.Hour, 30
	case "7d":
		return 24 * time.Hour, 7
	case "all":
		return 24 * time.Hour, 30
	default:
		return time.Hour, 24
	}
}

func parseUpstreamWindow(value string) (string, time.Time) {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "today", "", "24h":
		return "today", startOfUTCDay(now)
	case "7d":
		return "7d", now.Add(-7 * 24 * time.Hour)
	case "30d":
		return "30d", now.Add(-30 * 24 * time.Hour)
	case "all":
		return "all", time.Time{}
	default:
		return "today", startOfUTCDay(now)
	}
}

func parseOverviewWindow(value string) (string, time.Time, time.Duration, int) {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "today", "", "24h":
		return "today", startOfUTCDay(now), time.Hour, 24
	case "7d":
		return "7d", now.Add(-7 * 24 * time.Hour), 12 * time.Hour, 14
	case "30d":
		return "30d", now.Add(-30 * 24 * time.Hour), 24 * time.Hour, 30
	case "all":
		return "all", time.Time{}, 24 * time.Hour, 14
	default:
		return "today", startOfUTCDay(now), time.Hour, 24
	}
}

func parseAnalyticsWindow(value string) (string, time.Time) {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "today", "", "24h":
		return "today", startOfUTCDay(now)
	case "7d":
		return "7d", now.Add(-7 * 24 * time.Hour)
	case "30d":
		return "30d", now.Add(-30 * 24 * time.Hour)
	case "all":
		return "all", time.Time{}
	default:
		return "today", startOfUTCDay(now)
	}
}

func parseSystemEventWindow(value string) (string, time.Time) {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "today", "", "24h":
		return "today", startOfUTCDay(now)
	case "7d":
		return "7d", now.Add(-7 * 24 * time.Hour)
	case "30d":
		return "30d", now.Add(-30 * 24 * time.Hour)
	case "all":
		return "all", time.Time{}
	default:
		return "today", startOfUTCDay(now)
	}
}

func systemEventFilterFromRequest(r *http.Request, since time.Time) store.SystemEventFilter {
	q := r.URL.Query()
	return store.SystemEventFilter{
		Status:   q.Get("status"),
		Severity: q.Get("severity"),
		Source:   q.Get("source"),
		Category: q.Get("category"),
		Query:    q.Get("q"),
		Since:    since,
		Page:     parseInt(q.Get("page"), 1),
		PageSize: parsePageSize(q.Get("page_size")),
	}
}

func startOfUTCDay(now time.Time) time.Time {
	year, month, day := now.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func serveEmbeddedIndex(distFS fs.FS, w http.ResponseWriter, r *http.Request) {
	content, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		http.Error(w, "embedded ui not available", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(content)
}

func listAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}

		page := parseInt(r.URL.Query().Get("page"), 1)
		pageSize := parsePageSize(r.URL.Query().Get("page_size"))
		filter := parseListFilter(r)
		result, err := st.ListPage(page, pageSize, filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		stats, err := st.Stats(filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "stats error: " + err.Error()})
			return
		}

		resp := listResponse{
			Page:       result.Page,
			PageSize:   result.PageSize,
			Total:      result.Total,
			TotalPages: result.TotalPages,
			Stats: LogStats{
				TotalRequest:   stats.TotalRequest,
				AvgTTFT:        stats.AvgTTFT,
				TotalTokens:    stats.TotalTokens,
				SuccessRequest: stats.SuccessRequest,
				FailedRequest:  stats.FailedRequest,
				SuccessRate:    stats.SuccessRate,
			},
			RefreshedAt: time.Now().UTC(),
		}
		childrenByTrace, err := st.ListChildExchangesForEntries(result.Items)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query child exchanges: " + err.Error()})
			return
		}
		for _, entry := range result.Items {
			item := traceListItemFromEntry(entry)
			item.UpstreamCallCount = len(childrenByTrace[entry.ID])
			resp.Items = append(resp.Items, item)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func routingExchangeListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}

		page := parseInt(r.URL.Query().Get("page"), 1)
		pageSize := parsePageSize(r.URL.Query().Get("page_size"))
		filter := parseListFilter(r)
		result, err := st.ListRoutingPage(page, pageSize, filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}

		resp := listResponse{
			Page:        result.Page,
			PageSize:    result.PageSize,
			Total:       result.Total,
			TotalPages:  result.TotalPages,
			RefreshedAt: time.Now().UTC(),
		}
		for _, entry := range result.Items {
			resp.Items = append(resp.Items, traceListItemFromEntry(entry))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func sessionListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}

		page := parseInt(r.URL.Query().Get("page"), 1)
		pageSize := parsePageSize(r.URL.Query().Get("page_size"))
		filter := parseListFilter(r)
		result, err := st.ListSessionPage(page, pageSize, filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}

		resp := sessionListResponse{
			Page:        result.Page,
			PageSize:    result.PageSize,
			Total:       result.Total,
			TotalPages:  result.TotalPages,
			RefreshedAt: time.Now().UTC(),
		}
		for _, item := range result.Items {
			resp.Items = append(resp.Items, sessionSummaryItem(item))
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func sessionDetailAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}

		path := strings.TrimPrefix(pathClean(r.URL.Path), "/api/sessions/")
		path = strings.Trim(path, "/")
		parts := strings.Split(path, "/")
		sessionID := parts[0]
		if sessionID == "" || len(parts) > 2 {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 2 {
			if parts[1] == "trajectory" {
				if r.Method != http.MethodGet {
					w.Header().Set("Allow", http.MethodGet)
					writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
					return
				}
				handleSessionTrajectory(w, r, st, sessionID)
				return
			}
			if parts[1] == "analysis" && r.Method == http.MethodGet {
				handleSessionAnalysis(w, r, st, sessionID)
				return
			}
			if parts[1] == "reanalyze" && r.Method == http.MethodPost {
				handleSessionReanalyze(w, r, st, sessionID)
				return
			}
			http.NotFound(w, r)
			return
		}

		summary, err := st.GetSession(sessionID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) || errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		traces, err := st.ListTracesBySession(sessionID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		children, err := st.ListChildExchangesForEntries(traces)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query child exchanges: " + err.Error()})
			return
		}

		resp := sessionDetailResponse{
			Summary: sessionSummaryItem(summary),
		}
		for _, entry := range traces {
			item := traceListItemFromEntry(entry)
			for _, child := range children[entry.ID] {
				item.UpstreamCalls = append(item.UpstreamCalls, traceListItemFromEntry(child))
			}
			item.UpstreamCallCount = len(item.UpstreamCalls)
			resp.Traces = append(resp.Traces, item)
		}
		resp.Breakdown = buildSessionBreakdown(resp.Traces)
		resp.Timeline = buildSessionTimeline(resp.Traces)
		resp.Performance = buildAggregatePerformance(resp.Traces)
		if runs, err := st.ListAnalysisRuns(sessionID, "", "", 10); err == nil {
			resp.Analysis = analysisRunViews(runs)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleSessionAnalysis(w http.ResponseWriter, r *http.Request, st *store.Store, sessionID string) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	limit := parseInt(r.URL.Query().Get("limit"), 20)
	runs, err := st.ListAnalysisRuns(sessionID, "", kind, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query analysis runs: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, analysisListResponse{
		SessionID: sessionID,
		Items:     analysisRunViews(runs),
		Total:     len(runs),
	})
}

func handleSessionReanalyze(w http.ResponseWriter, r *http.Request, st *store.Store, sessionID string) {
	req, ok := decodeReanalysisRequest(w, r)
	if !ok {
		return
	}
	opts := reanalysis.SessionOptions{
		Reparse: req.Reparse,
		Scan:    req.Scan,
	}
	mode := requestMode(req.Mode, "async")
	svc := reanalysis.New(st, reanalysis.Options{})
	if mode == "async" {
		job, err := svc.EnqueueSessionReanalyze(sessionID, opts)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, reanalysisResponse{Job: analysisJobViewFromStore(job)})
		return
	}
	if mode != "sync" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be sync or async"})
		return
	}
	result, err := svc.ReanalyzeSession(r.Context(), sessionID, opts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, reanalysisResponse{Job: analysisJobViewFromStore(result.Job), Result: result})
}

func findingListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		filter := store.FindingFilter{
			Category: strings.TrimSpace(r.URL.Query().Get("category")),
			Severity: strings.TrimSpace(r.URL.Query().Get("severity")),
		}
		limit := parseInt(r.URL.Query().Get("limit"), 50)
		findings, err := st.ListAllFindings(filter, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		items := make([]findingView, 0, len(findings))
		for _, finding := range findings {
			items = append(items, findingViewFromObservation(finding))
		}
		writeJSON(w, http.StatusOK, findingListResponse{
			Items:    items,
			Total:    len(items),
			Severity: filter.Severity,
			Category: filter.Category,
		})
	}
}

func analysisListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		kind := strings.TrimSpace(r.URL.Query().Get("kind"))
		limit := parseInt(r.URL.Query().Get("limit"), 50)
		runs, err := st.ListAnalysisRuns("", "", kind, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, analysisListResponse{
			Items: analysisRunViews(runs),
			Total: len(runs),
		})
	}
}

func analysisJobListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		targetType := strings.TrimSpace(r.URL.Query().Get("target_type"))
		targetID := strings.TrimSpace(r.URL.Query().Get("target_id"))
		limit := parseInt(r.URL.Query().Get("limit"), 50)
		jobs, err := st.ListAnalysisJobs(status, targetType, targetID, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		items := make([]analysisJobView, 0, len(jobs))
		for _, job := range jobs {
			items = append(items, analysisJobViewFromStore(job))
		}
		writeJSON(w, http.StatusOK, analysisJobListResponse{Items: items, Total: len(items)})
	}
}

func analysisBatchReanalyzeAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req batchReanalysisRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
			return
		}
		opts := reanalysis.BatchOptions{
			Filter: store.ListFilter{
				Query:             strings.TrimSpace(req.Query),
				Provider:          strings.TrimSpace(req.Provider),
				Model:             strings.TrimSpace(req.Model),
				Endpoint:          strings.TrimSpace(req.Endpoint),
				SelectedUpstream:  strings.TrimSpace(req.Upstream),
				Status:            strings.TrimSpace(req.Status),
				ObservationStatus: strings.TrimSpace(req.Observation),
				MissingUsage:      req.MissingUsage,
			},
			Limit:       req.Limit,
			RepairUsage: req.RepairUsage,
			Reparse:     req.Reparse,
			Scan:        req.Scan,
		}
		mode := requestMode(req.Mode, "async")
		svc := reanalysis.New(st, reanalysis.Options{})
		switch mode {
		case "async":
			job, err := svc.EnqueueBatchReanalyze(opts)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusAccepted, reanalysisResponse{Job: analysisJobViewFromStore(job)})
		case "sync":
			result, err := svc.ReanalyzeBatch(r.Context(), opts)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, reanalysisResponse{Job: analysisJobViewFromStore(result.Job), Result: result})
		default:
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be sync or async"})
		}
	}
}

func analysisJobDetailAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(pathClean(r.URL.Path), "/api/analysis/jobs/")
		path = strings.Trim(path, "/")
		if path == "" {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(path, "/")
		id, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || id <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid job id"})
			return
		}
		if len(parts) == 2 {
			if parts[1] == "cancel" && r.Method == http.MethodPost {
				if err := st.MarkAnalysisJobCanceled(id); err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				job, err := st.GetAnalysisJob(id)
				if err != nil {
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, analysisJobResponse{Job: analysisJobViewFromStore(job)})
				return
			}
			http.NotFound(w, r)
			return
		}
		if len(parts) != 1 || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		job, err := st.GetAnalysisJob(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, analysisJobResponse{Job: analysisJobViewFromStore(job)})
	}
}

func overviewAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}

		windowLabel, since, bucketSize, bucketCount := parseOverviewWindow(r.URL.Query().Get("window"))
		dashboard, err := st.Overview(store.OverviewOptions{
			Since:       since,
			BucketSize:  bucketSize,
			BucketCount: bucketCount,
			Limit:       5,
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "overview error: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, overviewResponseFromStore(windowLabel, dashboard))
	}
}

func traceAPIHandler(st *store.Store, rtr *router.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(pathClean(r.URL.Path), "/api/traces/")
		path = strings.Trim(path, "/")
		if path == "" {
			http.NotFound(w, r)
			return
		}

		parts := strings.Split(path, "/")
		traceID := parts[0]
		entry, absPath, err := loadTrace(st, traceID)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		switch {
		case len(parts) == 1 && r.Method == http.MethodGet:
			handleTraceDetail(w, r, st, absPath, entry, rtr)
		case len(parts) == 2 && parts[1] == "raw" && r.Method == http.MethodGet:
			handleTraceRaw(w, absPath, entry)
		case len(parts) == 2 && parts[1] == "observation" && r.Method == http.MethodGet:
			handleTraceObservation(w, st, entry)
		case len(parts) == 2 && parts[1] == "findings" && r.Method == http.MethodGet:
			handleTraceFindings(w, r, st, entry)
		case len(parts) == 2 && parts[1] == "performance" && r.Method == http.MethodGet:
			handleTracePerformance(w, entry)
		case len(parts) == 2 && parts[1] == "download" && r.Method == http.MethodGet:
			serveTraceDownload(w, r, absPath)
		case len(parts) == 2 && parts[1] == "reparse" && r.Method == http.MethodPost:
			handleTraceReparse(w, r, st, entry)
		case len(parts) == 2 && parts[1] == "scan" && r.Method == http.MethodPost:
			handleTraceScan(w, r, st, entry)
		case len(parts) == 2 && parts[1] == "repair-usage" && r.Method == http.MethodPost:
			handleTraceRepairUsage(w, r, st, entry)
		case len(parts) == 2 && parts[1] == "reanalyze" && r.Method == http.MethodPost:
			handleTraceReanalyze(w, r, st, entry)
		default:
			http.NotFound(w, r)
		}
	}
}

func handleTraceReparse(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	req, ok := decodeReanalysisRequest(w, r)
	if !ok {
		return
	}
	runTraceReanalysis(w, r, st, requestMode(req.Mode, "sync"), func(svc *reanalysis.Service) (store.AnalysisJobRecord, error) {
		return svc.EnqueueTraceReparse(entry.ID, reanalysis.TraceOptions{Scan: req.Scan})
	}, func(svc *reanalysis.Service) (reanalysis.Result, error) {
		return svc.ReparseTrace(r.Context(), entry.ID, reanalysis.TraceOptions{Scan: req.Scan})
	})
}

func handleTraceScan(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	req, ok := decodeReanalysisRequest(w, r)
	if !ok {
		return
	}
	runTraceReanalysis(w, r, st, requestMode(req.Mode, "sync"), func(svc *reanalysis.Service) (store.AnalysisJobRecord, error) {
		return svc.EnqueueTraceRescan(entry.ID)
	}, func(svc *reanalysis.Service) (reanalysis.Result, error) {
		return svc.RescanTrace(r.Context(), entry.ID)
	})
}

func handleTraceRepairUsage(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	req, ok := decodeReanalysisRequest(w, r)
	if !ok {
		return
	}
	if req.RewriteCassette && requestMode(req.Mode, "sync") == "async" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "rewrite_cassette is only supported in sync mode"})
		return
	}
	runTraceReanalysis(w, r, st, requestMode(req.Mode, "sync"), func(svc *reanalysis.Service) (store.AnalysisJobRecord, error) {
		return svc.EnqueueTraceRepairUsage(entry.ID)
	}, func(svc *reanalysis.Service) (reanalysis.Result, error) {
		return svc.RepairTraceUsage(r.Context(), entry.ID, reanalysis.RepairUsageOptions{RewriteCassette: req.RewriteCassette})
	})
}

func handleTraceReanalyze(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	req, ok := decodeReanalysisRequest(w, r)
	if !ok {
		return
	}
	runTraceReanalysis(w, r, st, requestMode(req.Mode, "sync"), func(svc *reanalysis.Service) (store.AnalysisJobRecord, error) {
		return svc.EnqueueTraceReanalyze(entry.ID)
	}, func(svc *reanalysis.Service) (reanalysis.Result, error) {
		return svc.ReanalyzeTrace(r.Context(), entry.ID)
	})
}

func runTraceReanalysis(
	w http.ResponseWriter,
	r *http.Request,
	st *store.Store,
	mode string,
	enqueue func(*reanalysis.Service) (store.AnalysisJobRecord, error),
	runSync func(*reanalysis.Service) (reanalysis.Result, error),
) {
	svc := reanalysis.New(st, reanalysis.Options{})
	switch mode {
	case "async":
		job, err := enqueue(svc)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, reanalysisResponse{Job: analysisJobViewFromStore(job)})
	case "sync":
		result, err := runSync(svc)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, reanalysisResponse{Job: analysisJobViewFromStore(result.Job), Result: result})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be sync or async"})
	}
}

func decodeReanalysisRequest(w http.ResponseWriter, r *http.Request) (reanalysisRequest, bool) {
	var req reanalysisRequest
	if r.Body == nil || r.ContentLength == 0 {
		return req, true
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return reanalysisRequest{}, false
	}
	return req, true
}

func requestMode(mode string, fallback string) string {
	mode = strings.TrimSpace(strings.ToLower(mode))
	if mode == "" {
		return fallback
	}
	return mode
}

func handleTraceObservation(w http.ResponseWriter, st *store.Store, entry store.LogEntry) {
	summary, err := st.GetObservationSummary(entry.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace observation not found; run analyze reparse first"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	nodes, err := st.ListSemanticNodes(entry.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	tree := observe.RebuildNodeTree(nodes)
	payload := observationDetailResponse{
		ID:      entry.ID,
		Summary: observationSummaryFromStore(summary),
		Nodes:   observationNodeViewsFromFlat(nodes),
		Tree:    observationNodeViewsFromTree(tree, 0),
	}
	writeJSON(w, http.StatusOK, payload)
}

func handleTraceFindings(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	filter := store.FindingFilter{
		Category: strings.TrimSpace(r.URL.Query().Get("category")),
		Severity: strings.TrimSpace(r.URL.Query().Get("severity")),
	}
	findings, err := st.ListFindings(entry.ID, filter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	items := make([]findingView, 0, len(findings))
	for _, finding := range findings {
		items = append(items, findingViewFromObservation(finding))
	}
	writeJSON(w, http.StatusOK, findingListResponse{
		ID:       entry.ID,
		Items:    items,
		Total:    len(items),
		Severity: filter.Severity,
		Category: filter.Category,
	})
}

func handleTracePerformance(w http.ResponseWriter, entry store.LogEntry) {
	writeJSON(w, http.StatusOK, performanceResponse{
		ID:          entry.ID,
		Scope:       "trace",
		Performance: buildTracePerformance(entry),
	})
}

func handleTraceDetail(w http.ResponseWriter, r *http.Request, st *store.Store, absPath string, entry store.LogEntry, rtr *router.Router) {
	content, err := os.ReadFile(absPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
		return
	}
	parsed, err := ParseLogFile(content)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "parse error: " + err.Error()})
		return
	}

	resp := detailResponse{
		ID:          entry.ID,
		Messages:    parsed.ChatMessages,
		Tools:       parsed.RequestTools,
		AIContent:   parsed.AIContent,
		AIReasoning: parsed.AIReasoning,
		AIBlocks:    parsed.AIBlocks,
		ToolCalls:   parsed.ResponseToolCalls,
		Performance: buildTracePerformance(entry),
		Header: recordHeaderView{
			Version: parsed.Header.Version,
			Meta:    parsed.Header.Meta,
			Layout:  parsed.Header.Layout,
			Usage:   parsed.Header.Usage,
		},
	}
	if entry.SessionID != "" {
		resp.Session = &traceSessionView{
			SessionID:     entry.SessionID,
			SessionSource: entry.SessionSource,
		}
	}
	if health := selectedUpstreamHealthView(rtr, entry.Header.Meta.SelectedUpstreamID); health != nil {
		resp.SelectedUpstreamHealth = health
	}
	if ref, ok, err := traceResponsesAuditReference(r.Context(), st, entry); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query responses audit reference: " + err.Error()})
		return
	} else if ok {
		resp.ResponseID = ref.ResponseID
		resp.RequestAuditID = ref.RequestAuditID
		resp.ResponsesAudit = &traceResponsesAuditRef{
			ResponseID:     ref.ResponseID,
			RequestAuditID: ref.RequestAuditID,
		}
	}
	childrenByTrace, err := st.ListChildExchangesForEntries([]store.LogEntry{entry})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query child exchanges: " + err.Error()})
		return
	}
	for _, child := range childrenByTrace[entry.ID] {
		resp.UpstreamCalls = append(resp.UpstreamCalls, traceListItemFromEntry(child))
	}
	resp.Events = buildTimelineEventViews(parsed)
	writeJSON(w, http.StatusOK, resp)
}

func traceResponsesAuditReference(ctx context.Context, st *store.Store, entry store.LogEntry) (responsesaudit.RequestAuditReference, bool, error) {
	if st == nil || st.EntClient() == nil {
		return responsesaudit.RequestAuditReference{}, false, nil
	}
	query := responsesaudit.NewQueryService(st.EntClient())
	seen := map[string]struct{}{}
	for _, candidate := range []string{entry.ID, entry.Header.Meta.RequestID} {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		ref, ok, err := query.FindRequestAuditReferenceByTraceID(ctx, candidate)
		if err != nil || ok {
			return ref, ok, err
		}
	}
	return responsesaudit.RequestAuditReference{}, false, nil
}

func handleTraceRaw(w http.ResponseWriter, absPath string, entry store.LogEntry) {
	content, err := os.ReadFile(absPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
		return
	}
	parsed, err := ParseLogFile(content)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "parse error: " + err.Error()})
		return
	}

	payload := rawDetailResponse{
		ID:               entry.ID,
		RequestProtocol:  parsed.ReqFull,
		ResponseProtocol: parsed.ResFull,
		Header: recordHeaderView{
			Version: parsed.Header.Version,
			Meta:    parsed.Header.Meta,
			Layout:  parsed.Header.Layout,
			Usage:   parsed.Header.Usage,
		},
		Events: toEventViewsFromRecord(parsed.Events),
	}
	writeJSON(w, http.StatusOK, payload)
}

func loadTrace(st *store.Store, traceID string) (store.LogEntry, string, error) {
	if st == nil {
		return store.LogEntry{}, "", errors.New("store not configured")
	}
	entry, err := st.GetByID(traceID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrNotExist) {
			return store.LogEntry{}, "", os.ErrNotExist
		}
		if strings.Contains(err.Error(), "no rows") {
			return store.LogEntry{}, "", os.ErrNotExist
		}
		return store.LogEntry{}, "", err
	}
	absPath, err := filepath.Abs(entry.LogPath)
	if err != nil {
		return store.LogEntry{}, "", err
	}
	return entry, absPath, nil
}

func toEventViewsFromRecord(events []recordfile.RecordEvent) []recordEventView {
	if len(events) == 0 {
		return []recordEventView{}
	}
	payload := make([]recordEventView, 0, len(events))
	for _, event := range events {
		row := recordEventView{
			"type": event.Type,
			"time": event.Time,
		}
		if event.Method != "" {
			row["method"] = event.Method
		}
		if event.URL != "" {
			row["url"] = event.URL
		}
		if event.StatusCode != 0 {
			row["status_code"] = event.StatusCode
		}
		if event.IsStream {
			row["is_stream"] = event.IsStream
		}
		if event.HeaderBytes != 0 {
			row["header_bytes"] = event.HeaderBytes
		}
		if event.BodyBytes != 0 {
			row["body_bytes"] = event.BodyBytes
		}
		if event.Message != "" {
			row["message"] = event.Message
		}
		if len(event.Attributes) > 0 {
			row["attributes"] = event.Attributes
		}
		payload = append(payload, row)
	}
	return payload
}

func buildRoutingSummary(st *store.Store, since time.Time, modelFilter string) (routingSummaryResponse, error) {
	filter := store.ListFilter{Model: modelFilter}
	pageSize := MaxPageSize
	summary := routingSummaryResponse{}
	failureReasons := map[string]int{}
	selectedUpstreams := map[string]int{}
	selectedRouteTargets := map[string]int{}
	selectedChannels := map[string]int{}
	selectedCredentials := map[string]int{}
	stickyStatuses := map[string]int{}
	stickyPrevious := map[string]int{}
	stickyNext := map[string]int{}
	stickyPreviousRouteTargets := map[string]int{}
	stickyNextRouteTargets := map[string]int{}
	stickyPreviousChannels := map[string]int{}
	stickyNextChannels := map[string]int{}
	stickyPreviousCredentials := map[string]int{}
	stickyNextCredentials := map[string]int{}

	// ListPage does not expose a recorded_at filter, so stop once the descending
	// index reaches traces older than the requested monitor window.
	for page := 1; ; page++ {
		result, err := st.ListRoutingPage(page, pageSize, filter)
		if err != nil {
			return routingSummaryResponse{}, err
		}
		if page == 1 {
			summary.TotalTraces = result.Total
		}
		stop := false
		for _, entry := range result.Items {
			if !since.IsZero() && entry.Header.Meta.Time.Before(since) {
				stop = true
				continue
			}
			summary.ScannedTraces++
			routingEvents, parseErr := readRoutingPreludeEvents(entry.LogPath)
			if parseErr != nil {
				summary.ParseErrors++
				continue
			}
			if len(routingEvents) == 0 {
				summary.LegacyOrMissingEvents++
				continue
			}
			summary.EventfulTraces++
			aggregateRoutingEvents(
				routingEvents,
				failureReasons,
				selectedUpstreams,
				selectedRouteTargets,
				selectedChannels,
				selectedCredentials,
				stickyStatuses,
				stickyPrevious,
				stickyNext,
				stickyPreviousRouteTargets,
				stickyNextRouteTargets,
				stickyPreviousChannels,
				stickyNextChannels,
				stickyPreviousCredentials,
				stickyNextCredentials,
				&summary,
			)
		}
		if stop || len(result.Items) == 0 || result.TotalPages == 0 || page >= result.TotalPages {
			break
		}
	}

	summary.FailureReasons = countMapToItems(failureReasons)
	summary.SelectedUpstreams = countMapToItems(selectedUpstreams)
	summary.SelectedRouteTargets = countMapToItems(selectedRouteTargets)
	summary.SelectedChannels = countMapToItems(selectedChannels)
	summary.SelectedCredentials = countMapToItems(selectedCredentials)
	summary.StickyStatuses = countMapToItems(stickyStatuses)
	summary.StickyBreaks.PreviousUpstreams = countMapToItems(stickyPrevious)
	summary.StickyBreaks.NextUpstreams = countMapToItems(stickyNext)
	summary.StickyBreaks.PreviousRouteTargets = countMapToItems(stickyPreviousRouteTargets)
	summary.StickyBreaks.NextRouteTargets = countMapToItems(stickyNextRouteTargets)
	summary.StickyBreaks.PreviousChannels = countMapToItems(stickyPreviousChannels)
	summary.StickyBreaks.NextChannels = countMapToItems(stickyNextChannels)
	summary.StickyBreaks.PreviousCredentials = countMapToItems(stickyPreviousCredentials)
	summary.StickyBreaks.NextCredentials = countMapToItems(stickyNextCredentials)
	return summary, nil
}

func readRoutingPreludeEvents(path string) ([]recordfile.RecordEvent, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		return nil, err
	}
	events := make([]recordfile.RecordEvent, 0)
	for _, event := range parsed.Events {
		if strings.HasPrefix(event.Type, "routing.") {
			events = append(events, event)
		}
	}
	return events, nil
}

func aggregateRoutingEvents(
	events []recordfile.RecordEvent,
	failureReasons map[string]int,
	selectedUpstreams map[string]int,
	selectedRouteTargets map[string]int,
	selectedChannels map[string]int,
	selectedCredentials map[string]int,
	stickyStatuses map[string]int,
	stickyPrevious map[string]int,
	stickyNext map[string]int,
	stickyPreviousRouteTargets map[string]int,
	stickyNextRouteTargets map[string]int,
	stickyPreviousChannels map[string]int,
	stickyNextChannels map[string]int,
	stickyPreviousCredentials map[string]int,
	stickyNextCredentials map[string]int,
	summary *routingSummaryResponse,
) {
	for _, event := range events {
		switch event.Type {
		case "routing.filtered":
			if reason := stringAttr(event.Attributes, "routing_failure_reason"); reason != "" {
				failureReasons[reason]++
			}
		case "routing.retry_queue_saturated":
			failureReasons["retry_queue_saturated"]++
		case "routing.selected":
			if upstreamID := stringAttr(event.Attributes, "upstream_id"); upstreamID != "" {
				selectedUpstreams[upstreamID]++
			}
			aggregateRoutingIdentity(event.Attributes, selectedRouteTargets, selectedChannels, selectedCredentials)
		case "routing.selection":
			if upstreamID := stringAttr(event.Attributes, "upstream_id"); upstreamID != "" {
				selectedUpstreams[upstreamID]++
			}
		}
		if strings.HasPrefix(event.Type, "routing.sticky.") {
			status := strings.TrimPrefix(event.Type, "routing.sticky.")
			if attrStatus := stringAttr(event.Attributes, "sticky_status"); attrStatus != "" {
				status = attrStatus
			}
			if status != "" {
				stickyStatuses[status]++
			}
			if status == "break" {
				summary.StickyBreaks.Total++
				if previousID := stringAttr(event.Attributes, "previous_upstream_id"); previousID != "" {
					stickyPrevious[previousID]++
				}
				if upstreamID := stringAttr(event.Attributes, "upstream_id"); upstreamID != "" {
					stickyNext[upstreamID]++
				}
				incrementStringCount(stickyPreviousRouteTargets, stringAttr(event.Attributes, "previous_route_target_id"))
				incrementStringCount(stickyNextRouteTargets, stringAttr(event.Attributes, "route_target_id"))
				incrementStringCount(stickyPreviousChannels, stringAttr(event.Attributes, "previous_channel_id"))
				incrementStringCount(stickyNextChannels, stringAttr(event.Attributes, "channel_id"))
				incrementStringCount(stickyPreviousCredentials, stringAttr(event.Attributes, "previous_credential_id"))
				incrementStringCount(stickyNextCredentials, stringAttr(event.Attributes, "credential_id"))
			}
		}
	}
}

func aggregateRoutingIdentity(attrs map[string]interface{}, routeTargets map[string]int, channels map[string]int, credentials map[string]int) {
	incrementStringCount(routeTargets, stringAttr(attrs, "route_target_id"))
	incrementStringCount(channels, stringAttr(attrs, "channel_id"))
	incrementStringCount(credentials, stringAttr(attrs, "credential_id"))
}

func incrementStringCount(counts map[string]int, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	counts[value]++
}

func stringAttr(attrs map[string]interface{}, key string) string {
	if len(attrs) == 0 {
		return ""
	}
	value, ok := attrs[key]
	if !ok {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func countMapToItems(counts map[string]int) []sessionCountItem {
	items := make([]sessionCountItem, 0, len(counts))
	for label, count := range counts {
		if strings.TrimSpace(label) == "" || count == 0 {
			continue
		}
		items = append(items, sessionCountItem{Label: label, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Label < items[j].Label
	})
	return items
}

func observationSummaryFromStore(summary store.ObservationSummary) observationSummaryView {
	var summaryPayload any = map[string]any{}
	if strings.TrimSpace(summary.SummaryJSON) != "" {
		_ = json.Unmarshal([]byte(summary.SummaryJSON), &summaryPayload)
	}
	var warningsPayload any = []any{}
	if strings.TrimSpace(summary.WarningsJSON) != "" {
		_ = json.Unmarshal([]byte(summary.WarningsJSON), &warningsPayload)
	}
	return observationSummaryView{
		TraceID:       summary.TraceID,
		Parser:        summary.Parser,
		ParserVersion: summary.ParserVersion,
		Status:        summary.Status,
		Provider:      summary.Provider,
		Operation:     summary.Operation,
		Model:         summary.Model,
		Summary:       summaryPayload,
		Warnings:      warningsPayload,
		CreatedAt:     summary.CreatedAt,
		UpdatedAt:     summary.UpdatedAt,
	}
}

func observationNodeViewsFromFlat(nodes []observe.FlatSemanticNode) []observationNodeView {
	out := make([]observationNodeView, 0, len(nodes))
	for _, row := range nodes {
		out = append(out, observationNodeViewFromNode(row.Node, row.ParentID, row.Depth))
	}
	return out
}

func observationNodeViewsFromTree(nodes []observe.SemanticNode, depth int) []observationNodeView {
	out := make([]observationNodeView, 0, len(nodes))
	for _, node := range nodes {
		view := observationNodeViewFromNode(node, node.ParentID, depth)
		view.Children = observationNodeViewsFromTree(node.Children, depth+1)
		out = append(out, view)
	}
	return out
}

func observationNodeViewFromNode(node observe.SemanticNode, parentID string, depth int) observationNodeView {
	return observationNodeView{
		ID:             node.ID,
		ParentID:       parentID,
		ProviderType:   node.ProviderType,
		NormalizedType: string(node.NormalizedType),
		Role:           node.Role,
		Path:           node.Path,
		Index:          node.Index,
		Depth:          depth,
		TextPreview:    node.Text,
		Raw:            node.Raw,
	}
}

func findingViewFromObservation(finding observe.Finding) findingView {
	return findingView{
		ID:              finding.ID,
		TraceID:         finding.TraceID,
		Category:        finding.Category,
		Severity:        string(finding.Severity),
		Confidence:      finding.Confidence,
		Title:           finding.Title,
		Description:     finding.Description,
		EvidencePath:    finding.EvidencePath,
		EvidenceExcerpt: finding.EvidenceExcerpt,
		NodeID:          finding.NodeID,
		Detector:        finding.Detector,
		DetectorVersion: finding.DetectorVersion,
		CreatedAt:       finding.CreatedAt,
	}
}

func analysisRunViews(runs []store.AnalysisRunRecord) []analysisRunView {
	out := make([]analysisRunView, 0, len(runs))
	for _, run := range runs {
		var output any = map[string]any{}
		if strings.TrimSpace(run.OutputJSON) != "" {
			_ = json.Unmarshal([]byte(run.OutputJSON), &output)
		}
		out = append(out, analysisRunView{
			ID:              run.ID,
			TraceID:         run.TraceID,
			SessionID:       run.SessionID,
			Kind:            run.Kind,
			Analyzer:        run.Analyzer,
			AnalyzerVersion: run.AnalyzerVersion,
			Model:           run.Model,
			InputRef:        run.InputRef,
			Output:          output,
			Status:          run.Status,
			CreatedAt:       run.CreatedAt,
		})
	}
	return out
}

func analysisJobViewFromStore(job store.AnalysisJobRecord) analysisJobView {
	var steps []string
	if strings.TrimSpace(job.StepsJSON) != "" {
		_ = json.Unmarshal([]byte(job.StepsJSON), &steps)
	}
	var request any = map[string]any{}
	if strings.TrimSpace(job.RequestJSON) != "" {
		_ = json.Unmarshal([]byte(job.RequestJSON), &request)
	}
	var result any = map[string]any{}
	if strings.TrimSpace(job.ResultJSON) != "" {
		_ = json.Unmarshal([]byte(job.ResultJSON), &result)
	}
	return analysisJobView{
		ID:         job.ID,
		JobType:    job.JobType,
		TargetType: job.TargetType,
		TargetID:   job.TargetID,
		Status:     job.Status,
		Steps:      steps,
		Request:    request,
		Result:     result,
		LastError:  job.LastError,
		Attempts:   job.Attempts,
		CreatedAt:  job.CreatedAt,
		UpdatedAt:  job.UpdatedAt,
		StartedAt:  job.StartedAt,
		FinishedAt: job.FinishedAt,
	}
}

func overviewResponseFromStore(window string, dashboard store.OverviewDashboard) overviewResponse {
	return overviewResponse{
		Window:      window,
		RefreshedAt: time.Now().UTC(),
		Summary: overviewSummaryView{
			RequestCount:   dashboard.Summary.RequestCount,
			SuccessRequest: dashboard.Summary.SuccessRequest,
			FailedRequest:  dashboard.Summary.FailedRequest,
			SuccessRate:    dashboard.Summary.SuccessRate,
			TotalTokens:    dashboard.Summary.TotalTokens,
			AvgTTFTMs:      dashboard.Summary.AvgTTFTMs,
			AvgDurationMs:  dashboard.Summary.AvgDurationMs,
			P95TTFTMs:      dashboard.Summary.P95TTFTMs,
			P95DurationMs:  dashboard.Summary.P95DurationMs,
			StreamCount:    dashboard.Summary.StreamCount,
			SessionCount:   dashboard.Summary.SessionCount,
		},
		Timeline: overviewTimelineViews(dashboard.Timeline),
		Breakdown: overviewBreakdownView{
			Models:                countItemViews(dashboard.Breakdown.Models),
			Providers:             countItemViews(dashboard.Breakdown.Providers),
			Endpoints:             countItemViews(dashboard.Breakdown.Endpoints),
			Upstreams:             countItemViews(dashboard.Breakdown.Upstreams),
			RoutingFailureReasons: countItemViews(dashboard.Breakdown.RoutingFailureReasons),
			FindingCategories:     countItemViews(dashboard.Breakdown.FindingCategories),
		},
		Attention: overviewAttentionView{
			RecentFailures:   traceListItemsFromEntries(dashboard.Attention.RecentFailures),
			HighRiskFindings: findingViewsFromObservations(dashboard.Attention.HighRiskFindings),
			RoutingFailures:  toRoutingFailureItems(dashboard.Attention.RoutingFailures),
			SlowTraces:       traceListItemsFromEntries(dashboard.Attention.SlowTraces),
		},
		Analysis: overviewAnalysisSummary{
			Total:  dashboard.Analysis.Total,
			Failed: dashboard.Analysis.Failed,
			Recent: analysisRunViews(dashboard.Analysis.Recent),
		},
		Observation: overviewObservationView{
			TotalObservations: dashboard.Observation.TotalObservations,
			Parsed:            dashboard.Observation.Parsed,
			Failed:            dashboard.Observation.Failed,
			Queued:            dashboard.Observation.Queued,
			Running:           dashboard.Observation.Running,
			Unparsed:          dashboard.Observation.Unparsed,
			RecentFailures:    parseJobViews(dashboard.Observation.RecentFailures),
		},
	}
}

func overviewTimelineViews(items []store.OverviewTimelineItem) []overviewTimelineItem {
	out := make([]overviewTimelineItem, 0, len(items))
	for _, item := range items {
		out = append(out, overviewTimelineItem{
			Time:          item.Time,
			RequestCount:  item.RequestCount,
			FailedRequest: item.FailedRequest,
			TotalTokens:   item.TotalTokens,
			AvgTTFTMs:     item.AvgTTFTMs,
			AvgDurationMs: item.AvgDurationMs,
		})
	}
	return out
}

func countItemViews(items []store.CountItem) []sessionCountItem {
	out := make([]sessionCountItem, 0, len(items))
	for _, item := range items {
		out = append(out, sessionCountItem{
			Label: item.Label,
			Count: item.Count,
		})
	}
	return out
}

func traceListItemsFromEntries(entries []store.LogEntry) []traceListItem {
	out := make([]traceListItem, 0, len(entries))
	for _, entry := range entries {
		out = append(out, traceListItemFromEntry(entry))
	}
	return out
}

func findingViewsFromObservations(findings []observe.Finding) []findingView {
	out := make([]findingView, 0, len(findings))
	for _, finding := range findings {
		out = append(out, findingViewFromObservation(finding))
	}
	return out
}

func parseJobViews(jobs []store.ParseJobRecord) []overviewParseJob {
	out := make([]overviewParseJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, overviewParseJob{
			ID:        job.ID,
			TraceID:   job.TraceID,
			Status:    job.Status,
			Attempts:  job.Attempts,
			LastError: job.LastError,
			CreatedAt: job.CreatedAt,
			UpdatedAt: job.UpdatedAt,
		})
	}
	return out
}

func tokenItemFromRecord(record auth.TokenRecord) tokenItem {
	return tokenItem{
		ID:         record.ID,
		Name:       record.Name,
		Prefix:     record.Prefix,
		Scope:      record.Scope,
		Enabled:    record.Enabled,
		Status:     tokenStatus(record),
		CreatedAt:  record.CreatedAt,
		ExpiresAt:  record.ExpiresAt,
		LastUsedAt: record.LastUsedAt,
	}
}

func tokenStatus(record auth.TokenRecord) string {
	if !record.Enabled {
		return "revoked"
	}
	if record.ExpiresAt != nil && time.Now().UTC().After(*record.ExpiresAt) {
		return "expired"
	}
	return "active"
}

func buildTimelineEventViews(parsed *ParsedData) []recordEventView {
	if parsed == nil {
		return []recordEventView{}
	}
	events := filterTimelineEvents(parsed.Events)
	if len(events) == 0 {
		return []recordEventView{}
	}
	views := toEventViewsFromRecord(events)
	for idx, event := range events {
		var items []timelineItemView
		switch event.Type {
		case "request":
			items = buildRequestTimelineItems(parsed)
		case "response":
			items = buildResponseTimelineItems(parsed)
		}
		if len(items) == 0 {
			continue
		}
		views[idx]["timeline_items"] = items
		views[idx]["message"] = firstNonEmpty(event.Message, renderTimelineTree(flattenTimelineItems(items)))
	}
	return views
}

func sessionSummaryItem(summary store.SessionSummary) sessionListItem {
	return sessionListItem{
		SessionID:      summary.SessionID,
		SessionSource:  summary.SessionSource,
		RequestCount:   summary.RequestCount,
		FirstSeen:      summary.FirstSeen,
		LastSeen:       summary.LastSeen,
		LastModel:      summary.LastModel,
		Providers:      summary.Providers,
		SuccessRequest: summary.SuccessRequest,
		FailedRequest:  summary.FailedRequest,
		SuccessRate:    summary.SuccessRate,
		TotalTokens:    summary.TotalTokens,
		AvgTTFT:        summary.AvgTTFT,
		TotalDuration:  summary.TotalDuration,
		StreamCount:    summary.StreamCount,
	}
}

func traceListItemFromEntry(entry store.LogEntry) traceListItem {
	return traceListItem{
		ID:               entry.ID,
		SessionID:        entry.SessionID,
		SessionSource:    entry.SessionSource,
		RecordedAt:       entry.Header.Meta.Time,
		Model:            entry.Header.Meta.Model,
		Provider:         entry.Header.Meta.Provider,
		SelectedUpstream: entry.Header.Meta.SelectedUpstreamID,
		SelectedPreset:   entry.Header.Meta.SelectedUpstreamProviderPreset,
		Operation:        entry.Header.Meta.Operation,
		Endpoint:         entry.Header.Meta.Endpoint,
		Method:           entry.Header.Meta.Method,
		URL:              entry.Header.Meta.URL,
		StatusCode:       entry.Header.Meta.StatusCode,
		DurationMs:       entry.Header.Meta.DurationMs,
		TTFTMs:           entry.Header.Meta.TTFTMs,
		TotalTokens:      entry.Header.Usage.TotalTokens,
		PromptTokens:     entry.Header.Usage.PromptTokens,
		CompletionTokens: entry.Header.Usage.CompletionTokens,
		CachedTokens:     cachedTokens(entry),
		IsStream:         entry.Header.Layout.IsStream,
		Error:            entry.Header.Meta.Error,
		RequestAuditID:   entry.Header.Meta.RequestAuditID,
		ResponseID:       entry.Header.Meta.ResponseID,
		ExchangeID:       entry.Header.Meta.ExchangeID,
		ExchangeKind:     entry.Header.Meta.ExchangeKind,
		ExchangeRole:     entry.Header.Meta.ExchangeRole,
		ParentExchangeID: entry.Header.Meta.ParentExchangeID,
		SequenceIndex:    entry.Header.Meta.SequenceIndex,
		Observation:      observationListViewFromStore(entry.Observation),
	}
}

func observationListViewFromStore(meta store.ObservationMetadata) observationListView {
	status := strings.TrimSpace(meta.Status)
	if status == "" {
		status = "unparsed"
	}
	return observationListView{
		Status:        status,
		Parser:        meta.Parser,
		ParserVersion: meta.ParserVersion,
		UpdatedAt:     meta.UpdatedAt,
	}
}

func systemEventViews(events []store.SystemEvent) []systemEventView {
	if len(events) == 0 {
		return []systemEventView{}
	}
	out := make([]systemEventView, 0, len(events))
	for _, event := range events {
		out = append(out, systemEventViewFromStore(event))
	}
	return out
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func systemEventViewFromStore(event store.SystemEvent) systemEventView {
	details := json.RawMessage(`{}`)
	if len(event.DetailsJSON) > 0 {
		details = event.DetailsJSON
	}
	return systemEventView{
		ID:              event.ID,
		Fingerprint:     event.Fingerprint,
		Source:          event.Source,
		Category:        event.Category,
		Severity:        event.Severity,
		Status:          event.Status,
		Title:           event.Title,
		Message:         event.Message,
		Details:         details,
		TraceID:         event.TraceID,
		SessionID:       event.SessionID,
		JobID:           event.JobID,
		UpstreamID:      event.UpstreamID,
		Model:           event.Model,
		OccurrenceCount: event.OccurrenceCount,
		FirstSeenAt:     event.FirstSeenAt,
		LastSeenAt:      event.LastSeenAt,
		CreatedAt:       event.CreatedAt,
		UpdatedAt:       event.UpdatedAt,
		ReadAt:          optionalTime(event.ReadAt),
		ResolvedAt:      optionalTime(event.ResolvedAt),
	}
}

func systemEventSummaryView(summary store.SystemEventSummary, window string) systemEventSummaryResponse {
	return systemEventSummaryResponse{
		Total:      summary.Total,
		Unread:     summary.Unread,
		Critical:   summary.Critical,
		Error:      summary.Error,
		Warning:    summary.Warning,
		LastSeenAt: optionalTime(summary.LastSeenAt),
		BySource:   countItemViews(summary.BySource),
		ByCategory: countItemViews(summary.ByCategory),
		Window:     window,
	}
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func buildTracePerformance(entry store.LogEntry) performanceView {
	item := traceListItemFromEntry(entry)
	success := 0
	failed := 0
	if item.StatusCode >= 200 && item.StatusCode < 300 && strings.TrimSpace(item.Error) == "" {
		success = 1
	} else {
		failed = 1
	}
	return performanceView{
		RequestCount:       1,
		SuccessRequest:     success,
		FailedRequest:      failed,
		SuccessRate:        float64(success),
		DurationMs:         item.DurationMs,
		TTFTMs:             item.TTFTMs,
		TokensPerSec:       tokensPerSec(item.TotalTokens, item.DurationMs),
		TotalTokens:        item.TotalTokens,
		PromptTokens:       item.PromptTokens,
		CompletionTokens:   item.CompletionTokens,
		CachedTokens:       item.CachedTokens,
		CacheRatio:         cacheRatio(item.CachedTokens, item.PromptTokens),
		StatusCode:         item.StatusCode,
		ProviderError:      item.Error,
		IsStream:           item.IsStream,
		SelectedUpstreamID: entry.Header.Meta.SelectedUpstreamID,
		RoutingPolicy:      entry.Header.Meta.RoutingPolicy,
		RoutingFallback:    entry.Header.Meta.RoutingCandidateCount > 1,
	}
}

func buildAggregatePerformance(traces []traceListItem) performanceView {
	perf := performanceView{RequestCount: len(traces)}
	models := map[string]*perfAccumulator{}
	endpoints := map[string]*perfAccumulator{}
	for _, trace := range traces {
		addTraceToPerformance(&perf, trace)
		addTraceToAccumulator(models, firstNonEmpty(trace.Model, "unknown-model"), trace)
		addTraceToAccumulator(endpoints, firstNonEmpty(trace.Endpoint, trace.Operation, trace.URL, "unknown-endpoint"), trace)
	}
	finalizePerformance(&perf)
	perf.ByModel = perfCountItems(models)
	perf.ByEndpoint = perfCountItems(endpoints)
	return perf
}

func buildUpstreamPerformance(item upstreamItem) performanceView {
	perf := performanceView{
		RequestCount:   item.RequestCount,
		SuccessRequest: item.SuccessRequest,
		FailedRequest:  item.FailedRequest,
		SuccessRate:    item.SuccessRate,
		TotalTokens:    item.TotalTokens,
		TTFTMs:         int64(item.AvgTTFT),
	}
	perf.Upstreams = []upstreamPerf{{
		ID:             item.ID,
		BaseURL:        item.BaseURL,
		ProviderPreset: item.ProviderPreset,
		RequestCount:   item.RequestCount,
		SuccessRequest: item.SuccessRequest,
		FailedRequest:  item.FailedRequest,
		SuccessRate:    item.SuccessRate,
		TotalTokens:    item.TotalTokens,
		AvgTTFT:        item.AvgTTFT,
		HealthState:    item.HealthState,
		ErrorRate:      item.ErrorRate,
		TimeoutRate:    item.TimeoutRate,
	}}
	return perf
}

type perfAccumulator struct {
	count        int
	success      int
	totalTokens  int
	totalTTFT    int64
	totalLatency int64
}

func addTraceToPerformance(perf *performanceView, trace traceListItem) {
	perf.DurationMs += trace.DurationMs
	perf.TTFTMs += trace.TTFTMs
	perf.TotalTokens += trace.TotalTokens
	perf.PromptTokens += trace.PromptTokens
	perf.CompletionTokens += trace.CompletionTokens
	perf.CachedTokens += trace.CachedTokens
	if trace.StatusCode >= 200 && trace.StatusCode < 300 && strings.TrimSpace(trace.Error) == "" {
		perf.SuccessRequest++
	} else {
		perf.FailedRequest++
	}
}

func finalizePerformance(perf *performanceView) {
	totalDuration := perf.DurationMs
	if perf.RequestCount > 0 {
		perf.SuccessRate = float64(perf.SuccessRequest) / float64(perf.RequestCount)
		perf.DurationMs = perf.DurationMs / int64(perf.RequestCount)
		perf.TTFTMs = perf.TTFTMs / int64(perf.RequestCount)
	}
	perf.TokensPerSec = tokensPerSec(perf.TotalTokens, totalDuration)
	perf.CacheRatio = cacheRatio(perf.CachedTokens, perf.PromptTokens)
}

func addTraceToAccumulator(items map[string]*perfAccumulator, key string, trace traceListItem) {
	acc := items[key]
	if acc == nil {
		acc = &perfAccumulator{}
		items[key] = acc
	}
	acc.count++
	acc.totalTokens += trace.TotalTokens
	acc.totalTTFT += trace.TTFTMs
	acc.totalLatency += trace.DurationMs
	if trace.StatusCode >= 200 && trace.StatusCode < 300 && strings.TrimSpace(trace.Error) == "" {
		acc.success++
	}
}

func perfCountItems(items map[string]*perfAccumulator) []perfCountItem {
	out := make([]perfCountItem, 0, len(items))
	for label, acc := range items {
		item := perfCountItem{Label: label, Count: acc.count, TotalTokens: acc.totalTokens}
		if acc.count > 0 {
			item.AvgDuration = acc.totalLatency / int64(acc.count)
			item.AvgTTFT = acc.totalTTFT / int64(acc.count)
			item.SuccessRate = float64(acc.success) / float64(acc.count)
			item.TokensPerSec = tokensPerSec(acc.totalTokens, acc.totalLatency)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Label < out[j].Label
	})
	return out
}

func tokensPerSec(tokens int, durationMs int64) float64 {
	if tokens <= 0 || durationMs <= 0 {
		return 0
	}
	return float64(tokens) * 1000 / float64(durationMs)
}

func cacheRatio(cached int, prompt int) float64 {
	if cached <= 0 || prompt <= 0 {
		return 0
	}
	return float64(cached) / float64(prompt)
}

func parseListFilter(r *http.Request) store.ListFilter {
	if r == nil {
		return store.ListFilter{}
	}
	query := r.URL.Query()
	return store.ListFilter{
		Query:             strings.TrimSpace(query.Get("q")),
		Provider:          strings.TrimSpace(query.Get("provider")),
		Model:             strings.TrimSpace(query.Get("model")),
		Endpoint:          strings.TrimSpace(query.Get("endpoint")),
		SelectedUpstream:  strings.TrimSpace(query.Get("upstream")),
		Status:            strings.TrimSpace(query.Get("status")),
		ObservationStatus: strings.TrimSpace(firstNonEmptyLocal(query.Get("observation"), query.Get("observation_status"))),
		MissingUsage:      parseBool(query.Get("missing_usage")),
		MinDurationMs:     int64(parseInt(query.Get("min_duration_ms"), 0)),
		MaxDurationMs:     int64(parseInt(query.Get("max_duration_ms"), 0)),
		MinTTFTMs:         int64(parseInt(query.Get("min_ttft_ms"), 0)),
		MaxTTFTMs:         int64(parseInt(query.Get("max_ttft_ms"), 0)),
		MinTokens:         parseInt(query.Get("min_tokens"), 0),
		MaxTokens:         parseInt(query.Get("max_tokens"), 0),
	}
}

func buildSessionBreakdown(traces []traceListItem) sessionBreakdownView {
	modelCounts := map[string]int{}
	endpointCounts := map[string]int{}
	failed := 0
	for _, trace := range traces {
		model := firstNonEmpty(trace.Model, "unknown-model")
		endpoint := firstNonEmpty(trace.Endpoint, trace.Operation, trace.URL, "unknown-endpoint")
		modelCounts[model]++
		endpointCounts[endpoint]++
		if trace.StatusCode < 200 || trace.StatusCode >= 300 {
			failed++
		}
	}
	return sessionBreakdownView{
		Models:       sortSessionCounts(modelCounts),
		Endpoints:    sortSessionCounts(endpointCounts),
		FailedTraces: failed,
	}
}

func sortSessionCounts(counts map[string]int) []sessionCountItem {
	items := make([]sessionCountItem, 0, len(counts))
	for label, count := range counts {
		items = append(items, sessionCountItem{Label: label, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Label < items[j].Label
	})
	return items
}

func toSessionCountItems(items []store.CountItem) []sessionCountItem {
	out := make([]sessionCountItem, 0, len(items))
	for _, item := range items {
		out = append(out, sessionCountItem{
			Label: item.Label,
			Count: item.Count,
		})
	}
	return out
}

func buildSessionTimeline(traces []traceListItem) []sessionTimelineItem {
	items := make([]sessionTimelineItem, 0, len(traces))
	for _, trace := range traces {
		items = append(items, sessionTimelineItem{
			TraceID:     trace.ID,
			Time:        trace.RecordedAt,
			Model:       trace.Model,
			Provider:    trace.Provider,
			Endpoint:    trace.Endpoint,
			StatusCode:  trace.StatusCode,
			DurationMs:  trace.DurationMs,
			TTFTMs:      trace.TTFTMs,
			TotalTokens: trace.TotalTokens,
			IsStream:    trace.IsStream,
			Error:       trace.Error,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].Time.Equal(items[j].Time) {
			return items[i].Time.Before(items[j].Time)
		}
		return items[i].TraceID < items[j].TraceID
	})
	return items
}

func filterTimelineEvents(events []recordfile.RecordEvent) []recordfile.RecordEvent {
	if len(events) == 0 {
		return nil
	}
	filtered := make([]recordfile.RecordEvent, 0, len(events))
	for _, event := range events {
		if strings.HasSuffix(event.Type, ".delta") {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func buildRequestTimelineItems(parsed *ParsedData) []timelineItemView {
	if parsed == nil || len(parsed.ChatMessages) == 0 {
		return nil
	}

	var items []timelineItemView
	for _, message := range parsed.ChatMessages {
		if item, ok := timelineItemForChatMessage(message); ok {
			items = append(items, item)
		}
	}
	return items
}

func buildResponseTimelineItems(parsed *ParsedData) []timelineItemView {
	if parsed == nil {
		return nil
	}

	var items []timelineItemView
	if parsed.AIReasoning != "" {
		body := timelineCompact(parsed.AIReasoning)
		items = append(items, timelineItemView{
			Kind:    "thinking",
			Label:   "Thinking",
			Summary: timelinePreview(body),
			Body:    body,
		})
	}
	for _, call := range parsed.ResponseToolCalls {
		items = append(items, timelineToolCallItem(call, "tool call"))
	}
	if parsed.AIContent != "" {
		body := timelineCompact(parsed.AIContent)
		items = append(items, timelineItemView{
			Kind:    "output",
			Label:   "Final output",
			Summary: timelinePreview(body),
			Body:    body,
		})
	}
	for _, block := range parsed.AIBlocks {
		summary := firstNonEmpty(block.Text, block.Meta, block.URL, block.FileID)
		if summary == "" {
			continue
		}
		items = append(items, timelineItemView{
			Kind:    firstNonEmpty(block.Kind, "block"),
			Label:   firstNonEmpty(block.Title, block.Kind, "output"),
			Summary: timelinePreview(summary),
			Body:    timelineCompact(summary),
		})
	}
	return items
}

func timelineItemForChatMessage(message ChatMessage) (timelineItemView, bool) {
	role := firstNonEmpty(message.Role, "message")
	switch message.MessageType {
	case "tool_result", "function_call_output":
		return timelineToolResultItem(message), true
	}

	item := timelineItemView{
		Kind:  "message",
		Label: timelineRoleLabel(role),
		Role:  role,
	}
	if message.Content != "" {
		body := timelineCompact(message.Content)
		item.Summary = timelinePreview(body)
		item.Body = body
	}
	for _, block := range message.Blocks {
		summary := firstNonEmpty(block.Text, block.Meta, block.URL, block.FileID)
		if summary == "" {
			continue
		}
		item.Children = append(item.Children, timelineItemView{
			Kind:    firstNonEmpty(block.Kind, "block"),
			Label:   firstNonEmpty(block.Title, block.Kind, "block"),
			Summary: timelinePreview(summary),
			Body:    timelineCompact(summary),
		})
	}
	for _, call := range message.ToolCalls {
		label := "tool call"
		if role == "assistant" && strings.Contains(message.MessageType, "function_call") {
			label = "function call"
		}
		item.Children = append(item.Children, timelineToolCallItem(call, label))
	}
	if item.Summary == "" && len(item.Children) == 0 {
		return timelineItemView{}, false
	}
	return item, true
}

func timelineToolCallItem(call ToolCall, label string) timelineItemView {
	body := timelineCompact(call.Function.Arguments)
	if body == "" {
		body = "{}"
	}
	return timelineItemView{
		Kind:    "tool_call",
		Label:   label,
		Name:    firstNonEmpty(call.Function.Name, call.ID, "tool"),
		ID:      call.ID,
		Summary: timelinePreview(body),
		Body:    body,
	}
}

func timelineToolResultItem(message ChatMessage) timelineItemView {
	body := timelineCompact(message.Content)
	if body == "" {
		body = "(empty)"
	}
	status := "ok"
	if message.IsError {
		status = "error"
	}
	return timelineItemView{
		Kind:    "tool_response",
		Label:   "tool response",
		Name:    firstNonEmpty(message.Name, message.ToolCallID, "tool"),
		ID:      message.ToolCallID,
		Status:  status,
		Summary: timelinePreview(body),
		Body:    body,
	}
}

func flattenTimelineItems(items []timelineItemView) []string {
	if len(items) == 0 {
		return nil
	}
	var lines []string
	for _, item := range items {
		lines = append(lines, flattenTimelineItem(item)...)
	}
	return lines
}

func flattenTimelineItem(item timelineItemView) []string {
	if item.Kind == "message" {
		line := firstNonEmpty(item.Role, item.Label, "message")
		if item.Summary != "" {
			line += ": " + item.Summary
		}
		lines := []string{line}
		for _, child := range item.Children {
			lines = append(lines, "  "+flattenTimelineItemLine(child))
		}
		return lines
	}
	return []string{flattenTimelineItemLine(item)}
}

func flattenTimelineItemLine(item timelineItemView) string {
	switch item.Kind {
	case "tool_call":
		line := fmt.Sprintf("%s %s", firstNonEmpty(item.Label, "tool call"), firstNonEmpty(item.Name, "tool"))
		if item.ID != "" {
			line += fmt.Sprintf(" [%s]", item.ID)
		}
		if item.Summary != "" {
			line += ": " + item.Summary
		}
		return line
	case "tool_response":
		line := fmt.Sprintf("%s %s", firstNonEmpty(item.Label, "tool response"), firstNonEmpty(item.Name, "tool"))
		if item.ID != "" {
			line += fmt.Sprintf(" [%s]", item.ID)
		}
		if item.Summary != "" {
			line += ": " + item.Summary
		}
		if item.Status == "error" {
			line += " [error]"
		}
		return line
	default:
		line := firstNonEmpty(item.Label, item.Kind, "item")
		if item.Summary != "" {
			line += ": " + item.Summary
		}
		return line
	}
}

func renderTimelineTree(items []string) string {
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items))
	for idx, item := range items {
		prefix := "├─ "
		if idx == len(items)-1 {
			prefix = "└─ "
		}
		lines = append(lines, prefix+item)
	}
	return strings.Join(lines, "\n")
}

func timelinePreview(value string) string {
	compact := timelineCompact(value)
	if compact == "" {
		return ""
	}
	const limit = 180
	runes := []rune(compact)
	if len(runes) <= limit {
		return compact
	}
	return string(runes[:limit-1]) + "…"
}

func timelineCompact(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func timelineRoleLabel(role string) string {
	switch role {
	case "system":
		return "System"
	case "user":
		return "User"
	case "assistant":
		return "Assistant"
	case "tool":
		return "Tool"
	default:
		if role == "" {
			return "Message"
		}
		return role
	}
}

func serveTraceDownload(w http.ResponseWriter, r *http.Request, absPath string) {
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(absPath)))
	http.ServeFile(w, r, absPath)
}

func cachedTokens(entry store.LogEntry) int {
	if entry.Header.Usage.PromptTokenDetails == nil {
		return 0
	}
	return entry.Header.Usage.PromptTokenDetails.CachedTokens
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeSystemEventStreamMessage(w http.ResponseWriter, eventType string, notification store.SystemEventNotification, st *store.Store) {
	summary, err := st.SystemEventSummary(time.Time{})
	if err != nil {
		payload, _ := json.Marshal(map[string]string{"error": err.Error()})
		_, _ = fmt.Fprintf(w, "event: system_event.error\ndata: %s\n\n", payload)
		return
	}
	summaryView := systemEventSummaryView(summary, "all")
	payload := systemEventStreamMessage{
		Type:       eventType,
		EventID:    notification.EventID,
		Status:     notification.Status,
		Severity:   notification.Severity,
		Source:     notification.Source,
		Category:   notification.Category,
		Summary:    summaryView,
		Unread:     summaryView.Unread,
		LastSeenAt: summaryView.LastSeenAt,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
}

// Page-size bounds for every list endpoint. They are exported because the MCP
// tool surface advertises the same contract over this API.
const (
	DefaultPageSize = 50
	MaxPageSize     = 200
)

// parsePageSize reads a `page_size` query parameter, clamped to
// [1, MaxPageSize].
//
// The value reaches a SQL LIMIT, so an unbounded one is a robustness problem
// rather than a formatting detail: `page_size=100000000` made the server read
// that many rows into memory, and a non-positive value relied on each store
// method happening to substitute its own default. The clamp keeps the HTTP API
// and the MCP tools on one contract.
func parsePageSize(v string) int {
	size := parseInt(v, DefaultPageSize)
	if size < 1 {
		return DefaultPageSize
	}
	if size > MaxPageSize {
		return MaxPageSize
	}
	return size
}

func parseInt(v string, fallback int) int {
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func firstNonEmptyLocal(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pathClean(v string) string {
	if v == "" {
		return "/"
	}
	clean := filepath.ToSlash(filepath.Clean(v))
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return clean
}
