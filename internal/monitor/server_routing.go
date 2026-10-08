// Routing surface of the monitor API: the routing summary and its aggregate, the
// routing settings the UI reads and writes, the route inspection endpoints, and the
// failure views built from them.

package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/routeplan"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

type routingSummaryResponse struct {
	Window                string                    `json:"window"`
	Model                 string                    `json:"model,omitempty"`
	RefreshedAt           time.Time                 `json:"refreshed_at"`
	TotalTraces           int                       `json:"total_traces"`
	ScannedTraces         int                       `json:"scanned_traces"`
	EventfulTraces        int                       `json:"eventful_traces"`
	LegacyOrMissingEvents int                       `json:"legacy_or_missing_events"`
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

// buildRoutingSummary aggregates the routing decisions recorded in the window.
//
// It reads the routing facts the index row already carries rather than opening
// every cassette in the window, which is what made this endpoint take 49 s on
// the default window: one random read per trace, each dominated by the seek.
// See store.RoutingSummary.
func buildRoutingSummary(st *store.Store, since time.Time, modelFilter string) (routingSummaryResponse, error) {
	aggregate, err := st.RoutingSummary(since, modelFilter)
	if err != nil {
		return routingSummaryResponse{}, err
	}

	failureReasons := map[string]int{}
	selectedUpstreams := map[string]int{}
	selectedRouteTargets := map[string]int{}
	selectedChannels := map[string]int{}
	selectedCredentials := map[string]int{}
	stickyStatuses := map[string]int{}
	stickyPrevious := map[string]int{}
	stickyNext := map[string]int{}
	stickyNextRouteTargets := map[string]int{}
	stickyNextChannels := map[string]int{}
	stickyNextCredentials := map[string]int{}
	// The previous-side route target, channel and credential are counted from
	// `previous_route_target_id` and friends, which no producer has ever written:
	// the sticky events carry the identity of the target being switched to, not
	// the one being left. These stay empty and are kept for response-shape
	// compatibility; see routingDecisionEvents in internal/proxy.
	stickyPreviousRouteTargets := map[string]int{}
	stickyPreviousChannels := map[string]int{}
	stickyPreviousCredentials := map[string]int{}

	stickyBreaks := 0
	for _, bucket := range aggregate.Buckets {
		count := int(bucket.TraceCount)
		incrementStringCount(selectedUpstreams, bucket.SelectedUpstreamID)
		incrementStringCount(selectedRouteTargets, bucket.RouteTargetID)
		incrementStringCount(selectedChannels, bucket.ChannelID)
		incrementStringCount(selectedCredentials, bucket.CredentialID)
		incrementStringCount(stickyStatuses, bucket.StickyStatus)
		incrementStringCount(failureReasons, bucket.RoutingFailureReason)
		if bucket.StickyStatus == "break" {
			stickyBreaks += count
			incrementStringCount(stickyPrevious, bucket.StickyPreviousUpstreamID)
			incrementStringCount(stickyNext, bucket.SelectedUpstreamID)
			incrementStringCount(stickyNextRouteTargets, bucket.RouteTargetID)
			incrementStringCount(stickyNextChannels, bucket.ChannelID)
			incrementStringCount(stickyNextCredentials, bucket.CredentialID)
		}
	}

	response := routingSummaryResponse{
		TotalTraces:           int(aggregate.TotalTraces),
		ScannedTraces:         int(aggregate.TotalTraces),
		EventfulTraces:        int(aggregate.EventfulTraces),
		LegacyOrMissingEvents: int(aggregate.LegacyOrMissingEvents()),
		FailureReasons:        countMapToItems(failureReasons),
		SelectedUpstreams:     countMapToItems(selectedUpstreams),
		SelectedRouteTargets:  countMapToItems(selectedRouteTargets),
		SelectedChannels:      countMapToItems(selectedChannels),
		SelectedCredentials:   countMapToItems(selectedCredentials),
		StickyStatuses:        countMapToItems(stickyStatuses),
	}
	response.StickyBreaks.Total = stickyBreaks
	response.StickyBreaks.PreviousUpstreams = countMapToItems(stickyPrevious)
	response.StickyBreaks.NextUpstreams = countMapToItems(stickyNext)
	response.StickyBreaks.PreviousRouteTargets = countMapToItems(stickyPreviousRouteTargets)
	response.StickyBreaks.NextRouteTargets = countMapToItems(stickyNextRouteTargets)
	response.StickyBreaks.PreviousChannels = countMapToItems(stickyPreviousChannels)
	response.StickyBreaks.NextChannels = countMapToItems(stickyNextChannels)
	response.StickyBreaks.PreviousCredentials = countMapToItems(stickyPreviousCredentials)
	response.StickyBreaks.NextCredentials = countMapToItems(stickyNextCredentials)
	return response, nil
}
