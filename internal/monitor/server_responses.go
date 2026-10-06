package monitor

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	responsesaudit "github.com/kingfs/Trajecta/internal/responses/audit"
	"github.com/kingfs/Trajecta/internal/responses/functionexec"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

type responsesAuditTraceResponse struct {
	Query             responsesAuditTraceQuery      `json:"query"`
	RequestAudit      *responsesRequestAuditView    `json:"request_audit,omitempty"`
	FinalResponse     *responsesFinalResponseView   `json:"final_response,omitempty"`
	Events            []responsesExecutionEventView `json:"events"`
	EntryExchange     *responsesExchangeView        `json:"entry_exchange,omitempty"`
	ModelExchanges    []responsesExchangeView       `json:"model_exchanges"`
	UpstreamExchanges []responsesUpstreamExchange   `json:"upstream_exchanges"`
	RawCassettes      []responsesRawCassetteView    `json:"raw_cassettes"`
}

type responsesToolCallAuditListResponse struct {
	Query           responsesToolCallAuditQuery  `json:"query"`
	Items           []responsesToolCallAuditView `json:"items"`
	Total           int                          `json:"total"`
	IncludePayloads bool                         `json:"include_payloads"`
}

type responsesAuditTraceQuery struct {
	ResponseID     string `json:"response_id,omitempty"`
	RequestAuditID string `json:"request_audit_id,omitempty"`
}

type responsesToolCallAuditQuery struct {
	ResponseID     string `json:"response_id,omitempty"`
	RequestAuditID string `json:"request_audit_id,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	CallID         string `json:"call_id,omitempty"`
	ToolName       string `json:"tool_name,omitempty"`
	Status         string `json:"status,omitempty"`
	Limit          int    `json:"limit"`
}

type responsesRequestAuditView struct {
	ID              string         `json:"id"`
	ResponseID      string         `json:"response_id,omitempty"`
	ConversationID  string         `json:"conversation_id,omitempty"`
	Method          string         `json:"method"`
	Path            string         `json:"path"`
	ClientRequestID string         `json:"client_request_id,omitempty"`
	HeaderJSON      map[string]any `json:"header_json,omitempty"`
	BodyPreview     string         `json:"body_preview,omitempty"`
	BodySHA256      string         `json:"body_sha256,omitempty"`
	Status          string         `json:"status,omitempty"`
	ErrorText       string         `json:"error_text,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
}

type responsesExecutionEventView struct {
	ID             string         `json:"id"`
	ResponseID     string         `json:"response_id,omitempty"`
	RequestAuditID string         `json:"request_audit_id,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	EventType      string         `json:"event_type"`
	Phase          string         `json:"phase,omitempty"`
	Status         string         `json:"status,omitempty"`
	Message        string         `json:"message,omitempty"`
	DetailsJSON    map[string]any `json:"details_json,omitempty"`
	OccurredAt     time.Time      `json:"occurred_at"`
}

type responsesUpstreamExchange struct {
	ID               string    `json:"id"`
	ResponseID       string    `json:"response_id,omitempty"`
	RequestAuditID   string    `json:"request_audit_id,omitempty"`
	TraceID          string    `json:"trace_id,omitempty"`
	CassettePath     string    `json:"cassette_path,omitempty"`
	ExchangeKind     string    `json:"exchange_kind,omitempty"`
	ExchangeRole     string    `json:"exchange_role,omitempty"`
	ParentExchangeID string    `json:"parent_exchange_id,omitempty"`
	SequenceIndex    int       `json:"sequence_index,omitempty"`
	UpstreamID       string    `json:"upstream_id,omitempty"`
	RouteTarget      string    `json:"route_target,omitempty"`
	Model            string    `json:"model,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	Endpoint         string    `json:"endpoint,omitempty"`
	StatusCode       int       `json:"status_code,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
	ErrorText        string    `json:"error_text,omitempty"`
}

type responsesExchangeView struct {
	ID               string    `json:"id,omitempty"`
	ResponseID       string    `json:"response_id,omitempty"`
	RequestAuditID   string    `json:"request_audit_id,omitempty"`
	TraceID          string    `json:"trace_id,omitempty"`
	CassettePath     string    `json:"cassette_path,omitempty"`
	ExchangeKind     string    `json:"exchange_kind,omitempty"`
	ExchangeRole     string    `json:"exchange_role,omitempty"`
	ParentExchangeID string    `json:"parent_exchange_id,omitempty"`
	SequenceIndex    int       `json:"sequence_index,omitempty"`
	UpstreamID       string    `json:"upstream_id,omitempty"`
	RouteTarget      string    `json:"route_target,omitempty"`
	Model            string    `json:"model,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	Endpoint         string    `json:"endpoint,omitempty"`
	StatusCode       int       `json:"status_code,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
	ErrorText        string    `json:"error_text,omitempty"`
}

type responsesFinalResponseView struct {
	ResponseID      string    `json:"response_id,omitempty"`
	RequestAuditID  string    `json:"request_audit_id,omitempty"`
	ConversationID  string    `json:"conversation_id,omitempty"`
	ClientRequestID string    `json:"client_request_id,omitempty"`
	Status          string    `json:"status,omitempty"`
	ErrorText       string    `json:"error_text,omitempty"`
	Model           string    `json:"model,omitempty"`
	Endpoint        string    `json:"endpoint,omitempty"`
	StatusCode      int       `json:"status_code,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
}

type responsesRawCassetteView struct {
	ExchangeID       string                         `json:"exchange_id,omitempty"`
	TraceID          string                         `json:"trace_id,omitempty"`
	CassettePath     string                         `json:"cassette_path,omitempty"`
	ExchangeKind     string                         `json:"exchange_kind,omitempty"`
	ExchangeRole     string                         `json:"exchange_role,omitempty"`
	ParentExchangeID string                         `json:"parent_exchange_id,omitempty"`
	SequenceIndex    int                            `json:"sequence_index,omitempty"`
	ReadError        string                         `json:"read_error,omitempty"`
	Header           recordHeaderView               `json:"header,omitempty"`
	Events           []recordfile.RecordEvent       `json:"events,omitempty"`
	Request          recordfile.HTTPRequestSummary  `json:"request,omitempty"`
	Response         recordfile.HTTPResponseSummary `json:"response,omitempty"`
}

type responsesToolCallAuditView struct {
	ID              string                        `json:"id"`
	ResponseID      string                        `json:"response_id,omitempty"`
	RequestAuditID  string                        `json:"request_audit_id,omitempty"`
	ConversationID  string                        `json:"conversation_id,omitempty"`
	CallID          string                        `json:"call_id"`
	ToolType        string                        `json:"tool_type,omitempty"`
	ToolName        string                        `json:"tool_name,omitempty"`
	Executor        string                        `json:"executor,omitempty"`
	Status          string                        `json:"status,omitempty"`
	Phase           string                        `json:"phase,omitempty"`
	InputSummary    responsesaudit.PayloadSummary `json:"input_summary"`
	OutputSummary   responsesaudit.PayloadSummary `json:"output_summary"`
	MetadataSummary responsesaudit.PayloadSummary `json:"metadata_summary"`
	InputJSON       map[string]any                `json:"input_json,omitempty"`
	OutputJSON      map[string]any                `json:"output_json,omitempty"`
	MetadataJSON    map[string]any                `json:"metadata_json,omitempty"`
	ErrorText       string                        `json:"error_text,omitempty"`
	StartedAt       time.Time                     `json:"started_at,omitempty"`
	CompletedAt     time.Time                     `json:"completed_at,omitempty"`
	CreatedAt       time.Time                     `json:"created_at"`
}

type responsesFunctionExecutorsSummary struct {
	Enabled        bool                                   `json:"enabled"`
	Timeout        string                                 `json:"timeout"`
	MaxResultBytes int                                    `json:"max_result_bytes"`
	Redaction      responsesFunctionExecutorRedactionView `json:"redaction"`
	SupportedTypes []string                               `json:"supported_types"`
	Executors      []responsesFunctionExecutorBindingView `json:"executors"`
	Warnings       []string                               `json:"warnings"`
}

type responsesFunctionExecutorRedactionView struct {
	Arguments bool `json:"arguments"`
	Output    bool `json:"output"`
}

type responsesFunctionExecutorBindingView struct {
	Name              string                               `json:"name"`
	Type              string                               `json:"type"`
	Enabled           bool                                 `json:"enabled"`
	Available         bool                                 `json:"available"`
	Process           responsesFunctionExecutorProcessView `json:"process"`
	OutputConfigured  bool                                 `json:"output_configured"`
	CommandConfigured bool                                 `json:"command_configured"`
	Warnings          []string                             `json:"warnings"`
}

type responsesFunctionExecutorProcessView struct {
	WorkingDir             string   `json:"working_dir,omitempty"`
	RequireAbsoluteCommand bool     `json:"require_absolute_command,omitempty"`
	AllowedCommandDirs     []string `json:"allowed_command_dirs,omitempty"`
	RejectRoot             bool     `json:"reject_root,omitempty"`
}

type responsesFunctionExecutorUpdateRequest struct {
	ValidateOnly   *bool                                    `json:"validate_only"`
	Enabled        *bool                                    `json:"enabled"`
	Timeout        string                                   `json:"timeout"`
	MaxResultBytes *int                                     `json:"max_result_bytes"`
	Redaction      *responsesFunctionExecutorRedactionPatch `json:"redaction"`
	Executors      []responsesFunctionExecutorBindingPatch  `json:"executors"`
	ExecutorsSet   bool                                     `json:"-"`
}

type responsesFunctionExecutorRedactionPatch struct {
	Arguments *bool `json:"arguments"`
	Output    *bool `json:"output"`
}

type responsesFunctionExecutorBindingPatch struct {
	Name    string                                 `json:"name"`
	Type    string                                 `json:"type"`
	Enabled *bool                                  `json:"enabled"`
	Process *responsesFunctionExecutorProcessPatch `json:"process"`
}

type responsesFunctionExecutorProcessPatch struct {
	WorkingDir             string   `json:"working_dir"`
	RequireAbsoluteCommand *bool    `json:"require_absolute_command"`
	AllowedCommandDirs     []string `json:"allowed_command_dirs"`
	RejectRoot             *bool    `json:"reject_root"`
}

type responsesFunctionExecutorUpdateResponse struct {
	Applied      bool                              `json:"applied"`
	ValidateOnly bool                              `json:"validate_only"`
	Summary      responsesFunctionExecutorsSummary `json:"summary"`
}

type ResponsesFunctionExecutorState struct {
	mu      sync.RWMutex
	cfg     config.ResponsesFunctionExecutorConfig
	store   *store.Store
	manager *functionexec.Manager
}

type ResponsesFunctionExecutorStateOption func(*ResponsesFunctionExecutorState)

func responsesFunctionExecutorsAPIHandler(state *ResponsesFunctionExecutorState) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, state.summary())
		case http.MethodPost:
			var req responsesFunctionExecutorUpdateRequest
			dec := json.NewDecoder(r.Body)
			dec.DisallowUnknownFields()
			if err := dec.Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid function executor configuration payload"})
				return
			}
			validateOnly := true
			if req.ValidateOnly != nil {
				validateOnly = *req.ValidateOnly
			}
			var (
				summary responsesFunctionExecutorsSummary
				err     error
			)
			if validateOnly {
				summary, err = state.validate(req)
			} else {
				summary, err = state.apply(r.Context(), req)
			}
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, responsesFunctionExecutorUpdateResponse{
				Applied:      !validateOnly,
				ValidateOnly: validateOnly,
				Summary:      summary,
			})
		default:
			http.NotFound(w, r)
		}
	}
}

func responsesFunctionExecutorsSummaryFromConfig(cfg config.ResponsesFunctionExecutorConfig) responsesFunctionExecutorsSummary {
	cfg = (config.Config{
		ResponsesServer: config.ResponsesServerConfig{
			FunctionExecutors: cfg,
		},
	}).ResponsesFunctionExecutorsConfig()
	out := responsesFunctionExecutorsSummary{
		Enabled:        cfg.Enabled,
		Timeout:        cfg.Timeout.String(),
		MaxResultBytes: cfg.MaxResultBytes,
		Redaction: responsesFunctionExecutorRedactionView{
			Arguments: cfg.Redaction.Arguments,
			Output:    cfg.Redaction.Output,
		},
		SupportedTypes: config.SupportedResponsesFunctionExecutorTypes(),
		Executors:      make([]responsesFunctionExecutorBindingView, 0, len(cfg.Executors)),
		Warnings:       append([]string{}, cfg.Warnings...),
	}
	for _, binding := range cfg.Executors {
		enabled := true
		if binding.Enabled != nil {
			enabled = *binding.Enabled
		}
		out.Executors = append(out.Executors, responsesFunctionExecutorBindingView{
			Name:      binding.Name,
			Type:      binding.Type,
			Enabled:   enabled,
			Available: binding.Available,
			Process: responsesFunctionExecutorProcessView{
				WorkingDir:             binding.Process.WorkingDir,
				RequireAbsoluteCommand: binding.Process.RequireAbsoluteCommand,
				AllowedCommandDirs:     append([]string{}, binding.Process.AllowedCommandDirs...),
				RejectRoot:             binding.Process.RejectRoot,
			},
			OutputConfigured:  binding.Output != nil,
			CommandConfigured: binding.Command != "",
			Warnings:          append([]string{}, binding.Warnings...),
		})
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	return out
}

func responsesAuditTraceAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		responseID := strings.TrimSpace(r.URL.Query().Get("response_id"))
		requestAuditID := strings.TrimSpace(r.URL.Query().Get("request_audit_id"))
		if responseID == "" && requestAuditID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "response_id or request_audit_id is required"})
			return
		}
		if st == nil || st.EntClient() == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "responses audit store not configured"})
			return
		}
		trace, found, err := responsesaudit.NewQueryService(st.EntClient()).GetRequestAuditTrace(r.Context(), responsesaudit.GetRequestAuditTraceParams{
			ResponseID:     responseID,
			RequestAuditID: requestAuditID,
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "responses audit query error: " + err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "responses audit trace not found"})
			return
		}
		writeJSON(w, http.StatusOK, responsesAuditTraceFromAudit(trace, responseID, requestAuditID))
	}
}

func responsesToolCallAuditsAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if st == nil || st.EntClient() == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "responses audit store not configured"})
			return
		}
		query := r.URL.Query()
		limit := parseInt(query.Get("limit"), responsesaudit.DefaultAuditQueryLimit)
		params := responsesaudit.ListToolCallAuditsParams{
			ResponseID:     strings.TrimSpace(query.Get("response_id")),
			RequestAuditID: strings.TrimSpace(query.Get("request_audit_id")),
			ConversationID: strings.TrimSpace(query.Get("conversation_id")),
			CallID:         strings.TrimSpace(query.Get("call_id")),
			ToolName:       strings.TrimSpace(query.Get("tool_name")),
			Status:         strings.TrimSpace(query.Get("status")),
			Limit:          limit,
		}
		records, err := responsesaudit.NewQueryService(st.EntClient()).ListToolCallAudits(r.Context(), params)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "responses tool call audit query error: " + err.Error()})
			return
		}
		includePayloads := parseBool(query.Get("include_payloads"))
		items := make([]responsesToolCallAuditView, 0, len(records))
		for _, record := range records {
			items = append(items, responsesToolCallAuditFromAudit(record, includePayloads))
		}
		writeJSON(w, http.StatusOK, responsesToolCallAuditListResponse{
			Query: responsesToolCallAuditQuery{
				ResponseID:     params.ResponseID,
				RequestAuditID: params.RequestAuditID,
				ConversationID: params.ConversationID,
				CallID:         params.CallID,
				ToolName:       params.ToolName,
				Status:         params.Status,
				Limit:          responsesaudit.NormalizeAuditQueryLimit(limit),
			},
			Items:           items,
			Total:           len(items),
			IncludePayloads: includePayloads,
		})
	}
}

func responsesAuditTraceFromAudit(trace responsesaudit.RequestAuditTrace, responseID string, requestAuditID string) responsesAuditTraceResponse {
	out := responsesAuditTraceResponse{
		Query: responsesAuditTraceQuery{
			ResponseID:     firstNonEmpty(responseID, trace.RequestAudit.ResponseID),
			RequestAuditID: firstNonEmpty(requestAuditID, trace.RequestAudit.ID),
		},
		RequestAudit:      responsesRequestAuditFromAudit(trace.RequestAudit),
		FinalResponse:     responsesFinalResponseFromAudit(trace.FinalResponse),
		Events:            make([]responsesExecutionEventView, 0, len(trace.ExecutionEvents)),
		ModelExchanges:    make([]responsesExchangeView, 0, len(trace.UpstreamExchanges)),
		UpstreamExchanges: make([]responsesUpstreamExchange, 0, len(trace.UpstreamExchanges)),
		RawCassettes:      make([]responsesRawCassetteView, 0, len(trace.RawCassettes)),
	}
	for _, event := range trace.ExecutionEvents {
		out.Events = append(out.Events, responsesExecutionEventFromAudit(event))
	}
	for _, cassette := range trace.RawCassettes {
		out.RawCassettes = append(out.RawCassettes, responsesRawCassetteFromAudit(cassette))
	}
	cassetteByExchangeID := responsesRawCassettesByExchangeID(out.RawCassettes)
	for _, exchange := range trace.UpstreamExchanges {
		dto := responsesUpstreamExchangeFromAudit(exchange)
		responsesApplyCassetteMetadata(&dto, cassetteByExchangeID[dto.ID])
		out.UpstreamExchanges = append(out.UpstreamExchanges, dto)
		out.ModelExchanges = append(out.ModelExchanges, responsesModelExchangeFromUpstream(dto))
	}
	out.EntryExchange = responsesEntryExchangeFromAudit(trace, out.RawCassettes)
	return out
}

func responsesRequestAuditFromAudit(audit responsesaudit.RequestAuditView) *responsesRequestAuditView {
	return &responsesRequestAuditView{
		ID:              audit.ID,
		ResponseID:      audit.ResponseID,
		ConversationID:  audit.ConversationID,
		Method:          audit.Method,
		Path:            audit.Path,
		ClientRequestID: audit.ClientRequestID,
		HeaderJSON:      audit.HeaderJSON,
		BodyPreview:     audit.BodyPreview,
		BodySHA256:      audit.BodySha256,
		Status:          audit.Status,
		ErrorText:       audit.ErrorText,
		CreatedAt:       audit.CreatedAt,
	}
}

func responsesExecutionEventFromAudit(event responsesaudit.ExecutionEventView) responsesExecutionEventView {
	return responsesExecutionEventView{
		ID:             event.ID,
		ResponseID:     event.ResponseID,
		RequestAuditID: event.RequestAuditID,
		ConversationID: event.ConversationID,
		EventType:      event.EventType,
		Phase:          event.Phase,
		Status:         event.Status,
		Message:        event.Message,
		DetailsJSON:    event.DetailsJSON,
		OccurredAt:     event.OccurredAt,
	}
}

func responsesUpstreamExchangeFromAudit(exchange responsesaudit.UpstreamExchangeView) responsesUpstreamExchange {
	return responsesUpstreamExchange{
		ID:             exchange.ID,
		ResponseID:     exchange.ResponseID,
		RequestAuditID: exchange.RequestAuditID,
		TraceID:        exchange.TraceID,
		CassettePath:   exchange.CassettePath,
		UpstreamID:     exchange.UpstreamID,
		RouteTarget:    exchange.RouteTarget,
		Model:          exchange.Model,
		Endpoint:       exchange.Endpoint,
		StatusCode:     exchange.StatusCode,
		StartedAt:      exchange.StartedAt,
		CompletedAt:    exchange.CompletedAt,
		ErrorText:      exchange.ErrorText,
	}
}

func responsesModelExchangeFromUpstream(exchange responsesUpstreamExchange) responsesExchangeView {
	return responsesExchangeView{
		ID:               exchange.ID,
		ResponseID:       exchange.ResponseID,
		RequestAuditID:   exchange.RequestAuditID,
		TraceID:          exchange.TraceID,
		CassettePath:     exchange.CassettePath,
		ExchangeKind:     firstNonEmptyLocal(exchange.ExchangeKind, "model"),
		ExchangeRole:     exchange.ExchangeRole,
		ParentExchangeID: exchange.ParentExchangeID,
		SequenceIndex:    exchange.SequenceIndex,
		UpstreamID:       exchange.UpstreamID,
		RouteTarget:      exchange.RouteTarget,
		Model:            exchange.Model,
		Provider:         exchange.Provider,
		Endpoint:         exchange.Endpoint,
		StatusCode:       exchange.StatusCode,
		StartedAt:        exchange.StartedAt,
		CompletedAt:      exchange.CompletedAt,
		ErrorText:        exchange.ErrorText,
	}
}

func responsesEntryExchangeFromAudit(trace responsesaudit.RequestAuditTrace, cassettes []responsesRawCassetteView) *responsesExchangeView {
	for _, cassette := range cassettes {
		if cassette.ExchangeKind != "entry" {
			continue
		}
		meta := responsesRecordMeta(cassette.Header.Meta)
		return &responsesExchangeView{
			ID:               firstNonEmptyLocal(cassette.ExchangeID, meta.ExchangeID),
			ResponseID:       firstNonEmptyLocal(meta.ResponseID, trace.RequestAudit.ResponseID, trace.FinalResponse.ResponseID),
			RequestAuditID:   firstNonEmptyLocal(meta.RequestAuditID, trace.RequestAudit.ID),
			TraceID:          firstNonEmptyLocal(cassette.TraceID, meta.RequestID),
			CassettePath:     cassette.CassettePath,
			ExchangeKind:     firstNonEmptyLocal(cassette.ExchangeKind, meta.ExchangeKind, "entry"),
			ExchangeRole:     firstNonEmptyLocal(cassette.ExchangeRole, meta.ExchangeRole, "client_request"),
			ParentExchangeID: firstNonEmptyLocal(cassette.ParentExchangeID, meta.ParentExchangeID),
			SequenceIndex:    firstNonZero(cassette.SequenceIndex, meta.SequenceIndex),
			Model:            meta.Model,
			Provider:         meta.Provider,
			Endpoint:         firstNonEmptyLocal(meta.Endpoint, trace.RequestAudit.Path),
			StatusCode:       firstNonZero(meta.StatusCode, trace.FinalResponse.StatusCode),
			CompletedAt:      trace.FinalResponse.CompletedAt,
			ErrorText:        firstNonEmptyLocal(meta.Error, trace.FinalResponse.ErrorText),
		}
	}
	if trace.RequestAudit.ID == "" && trace.RequestAudit.ResponseID == "" && trace.FinalResponse.ResponseID == "" {
		return nil
	}
	return &responsesExchangeView{
		ID:             trace.RequestAudit.ID,
		ResponseID:     firstNonEmptyLocal(trace.RequestAudit.ResponseID, trace.FinalResponse.ResponseID),
		RequestAuditID: trace.RequestAudit.ID,
		ExchangeKind:   "entry",
		ExchangeRole:   "client_request",
		Model:          trace.FinalResponse.Model,
		Endpoint:       firstNonEmptyLocal(trace.FinalResponse.Endpoint, trace.RequestAudit.Path),
		StatusCode:     trace.FinalResponse.StatusCode,
		StartedAt:      trace.RequestAudit.CreatedAt,
		CompletedAt:    trace.FinalResponse.CompletedAt,
		ErrorText:      firstNonEmptyLocal(trace.RequestAudit.ErrorText, trace.FinalResponse.ErrorText),
	}
}

func responsesRawCassettesByExchangeID(cassettes []responsesRawCassetteView) map[string]responsesRawCassetteView {
	out := make(map[string]responsesRawCassetteView, len(cassettes))
	for _, cassette := range cassettes {
		if cassette.ExchangeID != "" {
			out[cassette.ExchangeID] = cassette
		}
	}
	return out
}

func responsesApplyCassetteMetadata(exchange *responsesUpstreamExchange, cassette responsesRawCassetteView) {
	if exchange == nil {
		return
	}
	meta := responsesRecordMeta(cassette.Header.Meta)
	exchange.ExchangeKind = firstNonEmptyLocal(exchange.ExchangeKind, cassette.ExchangeKind, meta.ExchangeKind, "model")
	exchange.ExchangeRole = firstNonEmptyLocal(exchange.ExchangeRole, cassette.ExchangeRole, meta.ExchangeRole)
	exchange.ParentExchangeID = firstNonEmptyLocal(exchange.ParentExchangeID, cassette.ParentExchangeID, meta.ParentExchangeID)
	exchange.SequenceIndex = firstNonZero(exchange.SequenceIndex, cassette.SequenceIndex, meta.SequenceIndex)
	exchange.Provider = firstNonEmptyLocal(exchange.Provider, meta.Provider)
	exchange.Model = firstNonEmptyLocal(exchange.Model, meta.Model)
	exchange.Endpoint = firstNonEmptyLocal(exchange.Endpoint, meta.Endpoint)
}

func responsesFinalResponseFromAudit(response responsesaudit.FinalResponseView) *responsesFinalResponseView {
	if response.ResponseID == "" && response.RequestAuditID == "" && response.Status == "" {
		return nil
	}
	return &responsesFinalResponseView{
		ResponseID:      response.ResponseID,
		RequestAuditID:  response.RequestAuditID,
		ConversationID:  response.ConversationID,
		ClientRequestID: response.ClientRequestID,
		Status:          response.Status,
		ErrorText:       response.ErrorText,
		Model:           response.Model,
		Endpoint:        response.Endpoint,
		StatusCode:      response.StatusCode,
		CompletedAt:     response.CompletedAt,
	}
}

func responsesRawCassetteFromAudit(cassette responsesaudit.RawCassetteView) responsesRawCassetteView {
	meta := responsesRecordMeta(cassette.Header.Meta)
	return responsesRawCassetteView{
		ExchangeID:       firstNonEmptyLocal(cassette.ExchangeID, meta.ExchangeID),
		TraceID:          firstNonEmptyLocal(cassette.TraceID, meta.RequestID),
		CassettePath:     cassette.CassettePath,
		ExchangeKind:     meta.ExchangeKind,
		ExchangeRole:     meta.ExchangeRole,
		ParentExchangeID: meta.ParentExchangeID,
		SequenceIndex:    meta.SequenceIndex,
		ReadError:        cassette.ReadError,
		Header: recordHeaderView{
			Version: cassette.Header.Version,
			Meta:    cassette.Header.Meta,
			Layout:  cassette.Header.Layout,
			Usage:   cassette.Header.Usage,
		},
		Events:   append([]recordfile.RecordEvent(nil), cassette.Events...),
		Request:  cassette.Request,
		Response: cassette.Response,
	}
}

type responsesRecordMetaView struct {
	RequestID        string
	ResponseID       string
	RequestAuditID   string
	ExchangeID       string
	ExchangeKind     string
	ExchangeRole     string
	ParentExchangeID string
	SequenceIndex    int
	Model            string
	Provider         string
	Endpoint         string
	StatusCode       int
	Error            string
}

func responsesRecordMeta(value any) responsesRecordMetaView {
	meta, ok := value.(recordfile.MetaData)
	if !ok {
		return responsesRecordMetaView{}
	}
	return responsesRecordMetaView{
		RequestID:        meta.RequestID,
		ResponseID:       meta.ResponseID,
		RequestAuditID:   meta.RequestAuditID,
		ExchangeID:       meta.ExchangeID,
		ExchangeKind:     meta.ExchangeKind,
		ExchangeRole:     meta.ExchangeRole,
		ParentExchangeID: meta.ParentExchangeID,
		SequenceIndex:    meta.SequenceIndex,
		Model:            meta.Model,
		Provider:         meta.Provider,
		Endpoint:         meta.Endpoint,
		StatusCode:       meta.StatusCode,
		Error:            meta.Error,
	}
}

func responsesToolCallAuditFromAudit(record responsesaudit.ToolCallAuditView, includePayloads bool) responsesToolCallAuditView {
	out := responsesToolCallAuditView{
		ID:              record.ID,
		ResponseID:      record.ResponseID,
		RequestAuditID:  record.RequestAuditID,
		ConversationID:  record.ConversationID,
		CallID:          record.CallID,
		ToolType:        record.ToolType,
		ToolName:        record.ToolName,
		Executor:        record.Executor,
		Status:          record.Status,
		Phase:           record.Phase,
		InputSummary:    responsesaudit.SummarizeJSONPayload(record.InputJSON),
		OutputSummary:   responsesaudit.SummarizeJSONPayload(record.OutputJSON),
		MetadataSummary: responsesaudit.SummarizeJSONPayload(record.MetadataJSON),
		ErrorText:       record.ErrorText,
		StartedAt:       record.StartedAt,
		CompletedAt:     record.CompletedAt,
		CreatedAt:       record.CreatedAt,
	}
	if includePayloads {
		out.InputJSON = record.InputJSON
		out.OutputJSON = record.OutputJSON
		out.MetadataJSON = record.MetadataJSON
	}
	return out
}
