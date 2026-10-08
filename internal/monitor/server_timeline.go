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
