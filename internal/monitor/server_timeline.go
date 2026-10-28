// The system-event feed and the timeline views built from a trace: the event list,
// summary, stream and detail endpoints with their stored views, and the request,
// response and session timelines the monitor renders.

package monitor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	"net/http"
	"sort"
	"strings"
	"time"
)

type overviewTimelineItem struct {
	Time          time.Time `json:"time"`
	RequestCount  int       `json:"request_count"`
	FailedRequest int       `json:"failed_request"`
	TotalTokens   int       `json:"total_tokens"`
	AvgTTFTMs     int       `json:"avg_ttft_ms"`
	AvgDurationMs int64     `json:"avg_duration_ms"`
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
	MatchedTraceID  string          `json:"matched_trace_id,omitempty"`
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
		if !explicitTraceIDValid(r) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace_id must be a full trace id"})
			return
		}
		windowLabel, since := parseSystemEventWindow(r.URL.Query().Get("window"))
		filter := systemEventFilterFromRequest(r, since)
		if traceID := requestTraceID(r); traceID != "" {
			response, err := listTraceScopedSystemEvents(st, traceID, filter)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
				return
			}
			response.Window = windowLabel
			response.RefreshedAt = time.Now().UTC()
			writeJSON(w, http.StatusOK, response)
			return
		}
		matches, err := st.CredentialMaskTraceMatches(filter.Query)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		filter.TraceQueryFingerprints = systemEventTraceFingerprints(matches)
		page, err := st.ListSystemEvents(filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		views, err := systemEventViewsWithTrace(st, page.Items, matches)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, systemEventListResponse{
			Items:       views,
			Page:        page.Page,
			PageSize:    page.PageSize,
			Total:       page.Total,
			TotalPages:  page.TotalPages,
			Window:      windowLabel,
			RefreshedAt: time.Now().UTC(),
		})
	}
}

// listTraceScopedSystemEvents answers a trace-scoped event read. It overlays the
// rule-level notification with the occurrence's state and metadata, so
// read/resolve/ignore on one trace is independent of every other trace. The
// intermediate read deliberately drops the status filter: the status belongs to
// the occurrence, not to the rule row the SQL filter would see.
func listTraceScopedSystemEvents(st *store.Store, traceID string, filter store.SystemEventFilter) (systemEventListResponse, error) {
	page, pageSize := filter.Page, filter.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	statusFilter := systemEventStatusFilter(filter.Status)
	since := filter.Since
	matches, err := st.CredentialMaskTraceMatches(traceID)
	if err != nil {
		return systemEventListResponse{}, err
	}
	filter.Query = traceID
	filter.TraceQueryFingerprints = systemEventTraceFingerprints(matches)
	filter.Status = ""
	filter.Since = time.Time{}
	filter.After = ""
	filter.Page = 1
	filter.PageSize = 200

	var events []store.SystemEvent
	for round := 1; round <= 4; round++ {
		filter.Page = round
		pageResult, err := st.ListSystemEvents(filter)
		if err != nil {
			return systemEventListResponse{}, err
		}
		events = append(events, pageResult.Items...)
		if len(events) >= pageResult.Total || len(pageResult.Items) < filter.PageSize {
			break
		}
	}

	traceIDs := make([]string, 0, len(matches))
	for _, matchedTrace := range matches {
		traceIDs = append(traceIDs, matchedTrace)
	}
	occurrences, err := credentialMaskOccurrencesByRule(st, traceIDs)
	if err != nil {
		return systemEventListResponse{}, err
	}

	views := make([]systemEventView, 0, len(events))
	for _, event := range events {
		var view systemEventView
		if event.Source == "secretmask" && event.Category == "credential_masked" {
			matchedTrace := matches[event.Fingerprint]
			rule := store.CredentialMaskRuleFromEvent(event)
			if matchedTrace == "" || rule == "" {
				continue
			}
			occurrence, ok := occurrences[matchedTrace][rule]
			if !ok {
				continue
			}
			view = systemEventViewWithOccurrence(event, occurrence)
		} else {
			if strings.TrimSpace(event.TraceID) != traceID {
				continue
			}
			view = systemEventViewFromStore(event)
		}
		if statusFilter != "" && view.Status != statusFilter {
			continue
		}
		if !since.IsZero() && view.LastSeenAt.Before(since) {
			continue
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool {
		if !views[i].LastSeenAt.Equal(views[j].LastSeenAt) {
			return views[i].LastSeenAt.After(views[j].LastSeenAt)
		}
		return views[i].ID < views[j].ID
	})

	total := len(views)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	totalPages := 0
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	return systemEventListResponse{
		Items:      views[start:end],
		Page:       page,
		PageSize:   pageSize,
		Total:      total,
		TotalPages: totalPages,
	}, nil
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
		if !explicitTraceIDValid(r) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace_id must be a full trace id"})
			return
		}
		_, since := parseSystemEventWindow(r.URL.Query().Get("window"))
		filter := systemEventFilterFromRequest(r, since)
		// "Mark all read" is a transition out of unread. An absent status, or the
		// list endpoints' `all`, must not turn a resolved row back into a read one:
		// the action would silently discard the resolution the reader recorded.
		switch strings.ToLower(strings.TrimSpace(filter.Status)) {
		case "", "all":
			filter.Status = store.SystemEventStatusUnread
		}
		if traceID := requestTraceID(r); traceID != "" {
			count, err := st.MarkTraceSystemEventsRead(traceID, filter)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mark read error: " + err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"updated": count})
			return
		}
		matches, err := st.CredentialMaskTraceMatches(filter.Query)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		filter.TraceQueryFingerprints = systemEventTraceFingerprints(matches)
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
		if len(parts) == 0 || parts[0] == "" {
			http.NotFound(w, r)
			return
		}
		if !explicitTraceIDValid(r) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "trace_id must be a full trace id"})
			return
		}
		eventID := parts[0]
		traceID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("trace_id")))

		event, err := st.GetSystemEvent(eventID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "event not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}

		if r.Method == http.MethodGet && len(parts) == 1 {
			view, explicitMissing, err := systemEventViewForTrace(st, event, traceID)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
				return
			}
			if explicitMissing {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential mask occurrence not found"})
				return
			}
			writeJSON(w, http.StatusOK, view)
			return
		}

		if r.Method != http.MethodPost || len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		action := parts[1]
		if !isSystemEventAction(action) {
			http.NotFound(w, r)
			return
		}

		if isCredentialMaskedEventRecord(event) {
			rule := store.CredentialMaskRuleFromEvent(event)
			scopeTrace := traceID
			if scopeTrace == "" {
				scopeTrace = event.TraceID
			}
			if rule == "" || scopeTrace == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential mask event requires trace_id and rule"})
				return
			}
			if _, err := st.SetCredentialMaskEventStatus(scopeTrace, rule, systemEventActionStatus(action)); err != nil {
				if errors.Is(err, store.ErrCredentialMaskOccurrenceNotFound) {
					writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential mask occurrence not found"})
					return
				}
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update error: " + err.Error()})
				return
			}
			occurrence, found, err := st.CredentialMaskEventOccurrence(scopeTrace, rule)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
				return
			}
			if !found {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential mask occurrence not found"})
				return
			}
			writeJSON(w, http.StatusOK, systemEventViewWithOccurrence(event, occurrence))
			return
		}

		switch action {
		case "read":
			err = st.MarkSystemEventRead(eventID)
		case "resolve":
			err = st.ResolveSystemEvent(eventID)
		case "ignore":
			err = st.IgnoreSystemEvent(eventID)
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "update error: " + err.Error()})
			return
		}
		event, err = st.GetSystemEvent(eventID)
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

func isSystemEventAction(action string) bool {
	switch action {
	case "read", "resolve", "ignore":
		return true
	default:
		return false
	}
}

func systemEventActionStatus(action string) string {
	switch action {
	case "resolve":
		return store.SystemEventStatusResolved
	case "ignore":
		return store.SystemEventStatusIgnored
	default:
		return store.SystemEventStatusRead
	}
}

func isCredentialMaskedEventRecord(event store.SystemEvent) bool {
	return event.Source == "secretmask" && event.Category == "credential_masked"
}

// systemEventViewForTrace returns the event projected onto a trace's credential
// occurrence. A missing explicit trace is reported separately so GET can answer
// 404 instead of silently falling back to the newest occurrence.
func systemEventViewForTrace(st *store.Store, event store.SystemEvent, traceID string) (systemEventView, bool, error) {
	if !isCredentialMaskedEventRecord(event) {
		return systemEventViewFromStore(event), false, nil
	}
	rule := store.CredentialMaskRuleFromEvent(event)
	scopeTrace := strings.TrimSpace(traceID)
	if scopeTrace == "" {
		scopeTrace = event.TraceID
	}
	if rule == "" || scopeTrace == "" {
		return systemEventViewFromStore(event), false, nil
	}
	occurrence, found, err := st.CredentialMaskEventOccurrence(scopeTrace, rule)
	if err != nil {
		return systemEventView{}, false, err
	}
	if !found {
		return systemEventViewFromStore(event), strings.TrimSpace(traceID) != "", nil
	}
	return systemEventViewWithOccurrence(event, occurrence), false, nil
}

func parseSystemEventWindow(value string) (string, time.Time) {
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "today", "", "24h":
		return "today", startOfDisplayDay(now)
	case "7d":
		return "7d", now.Add(-7 * 24 * time.Hour)
	case "30d":
		return "30d", now.Add(-30 * 24 * time.Hour)
	case "all":
		return "all", time.Time{}
	default:
		return "today", startOfDisplayDay(now)
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

// systemEventTraceFingerprints returns the stable order the list query binds.
func systemEventTraceFingerprints(matches map[string]string) []string {
	if len(matches) == 0 {
		return nil
	}
	fingerprints := make([]string, 0, len(matches))
	for fingerprint := range matches {
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Strings(fingerprints)
	return fingerprints
}

// systemEventViewsWithTrace attaches the trace a trace-id search matched. The
// event row keeps the rule's newest trace_id; matched_trace_id is the read-only
// answer to "which trace did this search name", so the detail panel can show
// that trace's credential pair instead of the newest occurrence's.
func systemEventViewsWithTrace(st *store.Store, events []store.SystemEvent, matches map[string]string) ([]systemEventView, error) {
	if len(matches) == 0 {
		return systemEventViews(events), nil
	}
	traceIDs := make([]string, 0, len(matches))
	for _, traceID := range matches {
		traceIDs = append(traceIDs, traceID)
	}
	occurrences, err := credentialMaskOccurrencesByRule(st, traceIDs)
	if err != nil {
		return nil, err
	}
	views := make([]systemEventView, 0, len(events))
	for _, event := range events {
		traceID := matches[event.Fingerprint]
		if traceID == "" {
			views = append(views, systemEventViewFromStore(event))
			continue
		}
		if isCredentialMaskedEventRecord(event) {
			rule := store.CredentialMaskRuleFromEvent(event)
			if occurrence, ok := occurrences[traceID][rule]; ok {
				views = append(views, systemEventViewWithOccurrence(event, occurrence))
				continue
			}
		}
		view := systemEventViewFromStore(event)
		view.MatchedTraceID = traceID
		views = append(views, view)
	}
	return views, nil
}

// requestTraceID is the one trace scope the event API accepts: an explicit
// trace_id query parameter, or a full UUID typed into the search box. Keeping it
// a query parameter follows /api/secret-masks?trace_id=; the event id stays the
// path resource.
func requestTraceID(r *http.Request) string {
	if traceID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("trace_id"))); traceID != "" {
		return traceID
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if looksLikeFullTraceID(query) {
		return query
	}
	return ""
}

// explicitTraceIDValid rejects a partial trace_id on the API surfaces that act
// on an occurrence: a prefix cannot name one occurrence, and silently resolving
// it to the newest match would make an action target the wrong trace. Prefix
// discovery remains available through the list's q search.
func explicitTraceIDValid(r *http.Request) bool {
	raw := strings.TrimSpace(r.URL.Query().Get("trace_id"))
	return raw == "" || looksLikeFullTraceID(raw)
}

func looksLikeFullTraceID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// credentialMaskOccurrencesByRule loads the derived occurrence rows for each
// trace once. A trace-scoped page touches only the rules that matched that trace.
func credentialMaskOccurrencesByRule(st *store.Store, traceIDs []string) (map[string]map[string]store.CredentialMaskEventOccurrence, error) {
	out := map[string]map[string]store.CredentialMaskEventOccurrence{}
	for _, traceID := range dedupeStrings(traceIDs) {
		occurrences, err := st.CredentialMaskEventOccurrences(traceID)
		if err != nil {
			return nil, err
		}
		byRule := make(map[string]store.CredentialMaskEventOccurrence, len(occurrences))
		for _, occurrence := range occurrences {
			byRule[occurrence.Rule] = occurrence
		}
		out[traceID] = byRule
	}
	return out, nil
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// systemEventViewWithOccurrence replaces the rule-level cache fields with the
// selected trace's occurrence. The event id stays the rule event's id because
// the notification is still one row per rule; trace_id in the action query is
// what scopes the write.
func systemEventViewWithOccurrence(event store.SystemEvent, occurrence store.CredentialMaskEventOccurrence) systemEventView {
	view := systemEventViewFromStore(event)
	view.TraceID = occurrence.TraceID
	view.MatchedTraceID = occurrence.TraceID
	view.Status = occurrence.Status
	view.ReadAt = optionalTime(occurrence.ReadAt)
	view.ResolvedAt = optionalTime(occurrence.ResolvedAt)
	view.OccurrenceCount = 1
	view.FirstSeenAt = occurrence.FirstDetectedAt
	view.LastSeenAt = occurrence.LastDetectedAt
	view.Details = credentialMaskOccurrenceDetails(occurrence)
	if event.TraceID != occurrence.TraceID {
		// These columns on the rule row belong to its newest trace. A historical
		// occurrence must not inherit the session/upstream/model of a different
		// recording; the trace link is the authoritative destination.
		view.SessionID = ""
		view.UpstreamID = ""
		view.Model = ""
	}
	return view
}

func credentialMaskOccurrenceDetails(occurrence store.CredentialMaskEventOccurrence) json.RawMessage {
	payload := map[string]any{
		"audit":      "credential_mask_audits",
		"rule":       occurrence.Rule,
		"vendor":     occurrence.Vendor,
		"kind":       occurrence.Kind,
		"confidence": occurrence.Confidence,
		"score":      occurrence.Score,
		"length":     occurrence.ValueLength,
		"offset":     occurrence.Offset,
		"values":     occurrence.Count,
		"trace_id":   occurrence.TraceID,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func systemEventStatusFilter(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "all" {
		return ""
	}
	return value
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
