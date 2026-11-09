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
	ResponsesStrategy string    `json:"responses_strategy"`
	SelectionPolicy   string    `json:"selection_policy"`
	PriorityMode      string    `json:"priority_mode"`
	RoutePlanLogLevel string    `json:"route_plan_log_level,omitempty"`
	UpdatedAt         time.Time `json:"updated_at,omitempty"`
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

func routingSettingsAPIHandler(st *store.Store, rtr *router.Router) http.HandlerFunc {
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
			if rtr != nil {
				if err := rtr.SetPriorityMode(settings.PriorityMode); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
					return
				}
			}
			writeJSON(w, http.StatusOK, settings)
		default:
			http.NotFound(w, r)
		}
	}
}

// routingInspectAPIHandler answers "what would the router do for this request"
// by asking the live router itself. The handler used to project the stored
// channel rows into a second planner that re-derived capabilities, health and
// selection; that planner could disagree with the forwarding path. Router.Explain
// now runs the real selection core in dry-run mode, so the inspector reports the
// same decision (and the same rejection) the proxy would take.
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
		if rtr == nil {
			// Production always has a live router; a test or a mis-wired
			// management server without one cannot preview a decision.
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "routing inspector requires a live router"})
			return
		}
		rawPath, err := routingInspectRawPath(req.Endpoint)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		settings, err := loadRoutingSettings(r.Context(), st)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		body, err := routingInspectBody(req)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		explained := rtr.Explain(router.ExplainRequest{
			RawPath:        rawPath,
			Body:           body,
			Strategy:       routeplan.ResponsesStrategy(settings.ResponsesStrategy),
			LocalAvailable: true,
		})
		resp := routingInspectResponse{
			Request:     req,
			Result:      &explained.Result,
			Error:       explained.Error,
			RefreshedAt: time.Now().UTC(),
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// routingInspectRawPath maps the inspect request's endpoint selector onto the
// client path the router plans for.
func routingInspectRawPath(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", "responses", "/v1/responses":
		return "/v1/responses", nil
	case "chat_completions", "/v1/chat/completions":
		return "/v1/chat/completions", nil
	case "anthropic_messages", "/v1/messages":
		return "/v1/messages", nil
	default:
		return "", fmt.Errorf("unsupported endpoint %q", value)
	}
}

// routingInspectBody renders the prospective request body the router plans from.
// Only the fields the decision reads are set: the model, the stream flag, and,
// when requested, one stub tool so the tool-calling capability filter applies.
func routingInspectBody(req routingInspectRequest) ([]byte, error) {
	payload := map[string]any{
		"model":  strings.TrimSpace(req.Model),
		"stream": req.Stream,
	}
	if req.Tools {
		payload["tools"] = []map[string]any{{
			"type":     "function",
			"function": map[string]any{"name": "inspect_tool"},
		}}
	}
	return json.Marshal(payload)
}

func defaultRoutingSettings() routingSettingsView {
	return routingSettingsView{
		// Native Responses upstreams are preferred; the local Responses server
		// is only a fallback. This makes the decision follow the requested
		// model: a model served by a Responses-capable channel is proxied
		// as-is, while one served only by Chat Completions goes through the
		// local server. See docs/RESPONSES_RUNTIME.md.
		ResponsesStrategy: "auto",
		SelectionPolicy:   router.PolicyP2C,
		PriorityMode:      "compat",
		RoutePlanLogLevel: "normal",
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
	if strings.TrimSpace(patch.PriorityMode) != "" {
		base.PriorityMode = strings.TrimSpace(patch.PriorityMode)
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
	switch settings.PriorityMode {
	case "", "compat", "strict":
	default:
		return fmt.Errorf("unsupported priority_mode %q", settings.PriorityMode)
	}
	switch settings.RoutePlanLogLevel {
	case "", "normal", "verbose":
	default:
		return fmt.Errorf("unsupported route_plan_log_level %q", settings.RoutePlanLogLevel)
	}
	return nil
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
		if err := attachTraceAudit(st, resp.Items); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query trace audit: " + err.Error()})
			return
		}
		attachTracePreviews(resp.Items)
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
		addStringCount(selectedUpstreams, bucket.SelectedUpstreamID, count)
		addStringCount(selectedRouteTargets, bucket.RouteTargetID, count)
		addStringCount(selectedChannels, bucket.ChannelID, count)
		addStringCount(selectedCredentials, bucket.CredentialID, count)
		addStringCount(stickyStatuses, bucket.StickyStatus, count)
		addStringCount(failureReasons, bucket.RoutingFailureReason, count)
		if bucket.StickyStatus == "break" {
			stickyBreaks += count
			addStringCount(stickyPrevious, bucket.StickyPreviousUpstreamID, count)
			addStringCount(stickyNext, bucket.SelectedUpstreamID, count)
			addStringCount(stickyNextRouteTargets, bucket.RouteTargetID, count)
			addStringCount(stickyNextChannels, bucket.ChannelID, count)
			addStringCount(stickyNextCredentials, bucket.CredentialID, count)
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
