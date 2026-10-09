// System events: the notification rows the index raises for parse, analysis, routing and
// transport failures, the queries and keyset cursor the monitor pages them with, and the
// subscriber fan-out that tells a connected UI a feed changed. They are a self-contained
// surface - nothing here reads a cassette or a log row except through the log columns the
// event carries.

package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	"strings"
	"time"
)

type SystemEvent struct {
	ID              string
	Fingerprint     string
	Source          string
	Category        string
	Severity        string
	Status          string
	Title           string
	Message         string
	DetailsJSON     json.RawMessage
	TraceID         string
	SessionID       string
	JobID           string
	UpstreamID      string
	Model           string
	OccurrenceCount int
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ReadAt          time.Time
	ResolvedAt      time.Time
}

type SystemEventFilter struct {
	Status   string
	Severity string
	Source   string
	Category string
	Query    string
	Since    time.Time
	After    string
	Page     int
	PageSize int
}

type SystemEventPageResult struct {
	Items      []SystemEvent
	Total      int
	Page       int
	PageSize   int
	TotalPages int
	NextCursor string
	HasMore    bool
}

const systemEventCursorVersion = 1

type systemEventCursor struct {
	Version    int    `json:"v"`
	LastSeenAt string `json:"last_seen_at"`
	ID         string `json:"id"`
}

type systemEventCursorPosition struct {
	LastSeenAt time.Time
	ID         string
}

type SystemEventSummary struct {
	Total      int
	Unread     int
	Critical   int
	Error      int
	Warning    int
	LastSeenAt time.Time
	BySource   []CountItem
	ByCategory []CountItem
}

type SystemEventNotification struct {
	Sequence uint64
	EventID  string
	Status   string
	Severity string
	Source   string
	Category string
	At       time.Time
}

func (s *Store) UpsertSystemEvent(event SystemEvent) (SystemEvent, error) {
	event.Fingerprint = strings.TrimSpace(event.Fingerprint)
	if event.Fingerprint == "" {
		return SystemEvent{}, errors.New("upsert system event: fingerprint is required")
	}
	event.Source = strings.TrimSpace(event.Source)
	if event.Source == "" {
		return SystemEvent{}, errors.New("upsert system event: source is required")
	}
	event.Category = strings.TrimSpace(event.Category)
	if event.Category == "" {
		return SystemEvent{}, errors.New("upsert system event: category is required")
	}
	event.Severity = strings.TrimSpace(event.Severity)
	if event.Severity == "" {
		event.Severity = "error"
	}
	now := time.Now().UTC()
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.FirstSeenAt.IsZero() {
		event.FirstSeenAt = now
	}
	if event.LastSeenAt.IsZero() {
		event.LastSeenAt = now
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	if event.Status == "" {
		event.Status = SystemEventStatusUnread
	}
	if event.OccurrenceCount <= 0 {
		event.OccurrenceCount = 1
	}
	detailsJSON := strings.TrimSpace(string(event.DetailsJSON))
	if detailsJSON == "" {
		detailsJSON = "{}"
	}

	_, err := s.db.Exec(`
		INSERT INTO system_events (
			id, fingerprint, source, category, severity, status, title, message, details_json,
			trace_id, session_id, job_id, upstream_id, model, occurrence_count,
			first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)
		ON CONFLICT(fingerprint) DO UPDATE SET
			source=excluded.source,
			category=excluded.category,
			severity=excluded.severity,
			status=CASE
				WHEN system_events.status = 'ignored' THEN system_events.status
				ELSE 'unread'
			END,
			title=excluded.title,
			message=excluded.message,
			details_json=excluded.details_json,
			trace_id=excluded.trace_id,
			session_id=excluded.session_id,
			job_id=excluded.job_id,
			upstream_id=excluded.upstream_id,
			model=excluded.model,
			occurrence_count=system_events.occurrence_count + 1,
			last_seen_at=excluded.last_seen_at,
			updated_at=excluded.updated_at,
			read_at=CASE
				WHEN system_events.status = 'ignored' THEN system_events.read_at
				ELSE NULL
			END,
			resolved_at=CASE
				WHEN system_events.status = 'ignored' THEN system_events.resolved_at
				ELSE NULL
			END
	`, sanitizeDBText(event.ID), sanitizeDBText(event.Fingerprint), sanitizeDBText(event.Source),
		sanitizeDBText(event.Category), sanitizeDBText(event.Severity), sanitizeDBText(event.Status),
		sanitizeDBText(textPreview(event.Title, 300)), sanitizeDBText(textPreview(event.Message, 2000)),
		sanitizeDBText(detailsJSON),
		sanitizeDBText(event.TraceID), sanitizeDBText(event.SessionID), sanitizeDBText(event.JobID),
		sanitizeDBText(event.UpstreamID), sanitizeDBText(event.Model), event.OccurrenceCount,
		event.FirstSeenAt, event.LastSeenAt, event.CreatedAt, event.UpdatedAt)
	if err != nil {
		return SystemEvent{}, err
	}
	saved, err := s.GetSystemEventByFingerprint(event.Fingerprint)
	if err != nil {
		return SystemEvent{}, err
	}
	s.notifySystemEventChanged(saved)
	return saved, nil
}

func (s *Store) GetSystemEvent(id string) (SystemEvent, error) {
	return s.getSystemEvent(`id = ?`, strings.TrimSpace(id))
}

func (s *Store) GetSystemEventByFingerprint(fingerprint string) (SystemEvent, error) {
	return s.getSystemEvent(`fingerprint = ?`, strings.TrimSpace(fingerprint))
}

func (s *Store) ListSystemEvents(filter SystemEventFilter) (SystemEventPageResult, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	whereSQL, args := buildSystemEventFilterClause(filter)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM system_events WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return SystemEventPageResult{}, err
	}
	queryArgs := append([]any{}, args...)
	offsetSQL := ` OFFSET ?`
	if strings.TrimSpace(filter.After) != "" {
		cursor, err := decodeSystemEventCursor(filter.After)
		if err != nil {
			return SystemEventPageResult{}, err
		}
		whereSQL += ` AND (last_seen_at < ? OR (last_seen_at = ? AND id < ?))`
		cursorLastSeen := cursor.LastSeenAt.UTC()
		queryArgs = append(queryArgs, cursorLastSeen, cursorLastSeen, cursor.ID)
		offsetSQL = ``
	}
	queryArgs = append(queryArgs, pageSize+1)
	if offsetSQL != "" {
		queryArgs = append(queryArgs, (page-1)*pageSize)
	}
	rows, err := s.db.Query(`
		SELECT id, fingerprint, source, category, severity, status, title, message, details_json,
			trace_id, session_id, job_id, upstream_id, model, occurrence_count,
			first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at
		FROM system_events
		WHERE `+whereSQL+`
		ORDER BY last_seen_at DESC, id DESC
		LIMIT ?`+offsetSQL+`
	`, queryArgs...)
	if err != nil {
		return SystemEventPageResult{}, err
	}
	defer rows.Close()
	items, err := scanSystemEvents(rows)
	if err != nil {
		return SystemEventPageResult{}, err
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor, err = encodeSystemEventCursor(items[len(items)-1])
		if err != nil {
			return SystemEventPageResult{}, err
		}
	}
	return SystemEventPageResult{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages(total, pageSize),
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

func (s *Store) SystemEventSummary(since time.Time) (SystemEventSummary, error) {
	whereSQL := `1 = 1`
	var args []any
	if !since.IsZero() {
		whereSQL = `last_seen_at >= ?`
		args = append(args, since.UTC().Format(timeLayout))
	}
	var summary SystemEventSummary
	var lastSeen any
	if err := s.db.QueryRow(`
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status = 'unread' THEN 1 ELSE 0 END), 0) AS unread,
			COALESCE(SUM(CASE WHEN severity = 'critical' AND status = 'unread' THEN 1 ELSE 0 END), 0) AS critical,
			COALESCE(SUM(CASE WHEN severity = 'error' AND status = 'unread' THEN 1 ELSE 0 END), 0) AS error,
			COALESCE(SUM(CASE WHEN severity = 'warning' AND status = 'unread' THEN 1 ELSE 0 END), 0) AS warning,
			MAX(last_seen_at) AS last_seen_at
		FROM system_events
		WHERE `+whereSQL, args...).Scan(&summary.Total, &summary.Unread, &summary.Critical, &summary.Error, &summary.Warning, &lastSeen); err != nil {
		return SystemEventSummary{}, err
	}
	var err error
	summary.LastSeenAt, err = timeParseNullableValue(lastSeen)
	if err != nil {
		return SystemEventSummary{}, err
	}
	summary.BySource, err = s.systemEventCountBy("source", whereSQL, args, 10)
	if err != nil {
		return SystemEventSummary{}, err
	}
	summary.ByCategory, err = s.systemEventCountBy("category", whereSQL, args, 10)
	if err != nil {
		return SystemEventSummary{}, err
	}
	return summary, nil
}

func (s *Store) MarkSystemEventRead(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'read', read_at = ?, updated_at = ?
		WHERE id = ? AND status != 'ignored'
	`, now, now, strings.TrimSpace(id))
	if err == nil {
		s.notifySystemEventIDChanged(id)
	}
	return err
}

func (s *Store) MarkAllSystemEventsRead(filter SystemEventFilter) (int, error) {
	whereSQL, args := buildSystemEventFilterClause(filter)
	now := time.Now().UTC()
	args = append([]any{now, now}, args...)
	result, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'read', read_at = ?, updated_at = ?
		WHERE status != 'ignored' AND `+whereSQL, args...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err == nil && count > 0 {
		s.notifySystemEventChanged(SystemEvent{Status: SystemEventStatusRead})
	}
	return int(count), err
}

func (s *Store) ResolveSystemEvent(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'resolved', resolved_at = ?, updated_at = ?
		WHERE id = ?
	`, now, now, strings.TrimSpace(id))
	if err == nil {
		s.notifySystemEventIDChanged(id)
	}
	return err
}

func (s *Store) IgnoreSystemEvent(id string) error {
	_, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'ignored', updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), strings.TrimSpace(id))
	if err == nil {
		s.notifySystemEventIDChanged(id)
	}
	return err
}

func (s *Store) SubscribeSystemEvents(buffer int) (<-chan SystemEventNotification, func()) {
	if buffer <= 0 {
		buffer = 8
	}
	ch := make(chan SystemEventNotification, buffer)
	s.shared.eventMu.Lock()
	if s.shared.eventSubs == nil {
		s.shared.eventSubs = map[chan SystemEventNotification]struct{}{}
	}
	s.shared.eventSubs[ch] = struct{}{}
	s.shared.eventMu.Unlock()
	return ch, func() {
		s.shared.eventMu.Lock()
		if _, ok := s.shared.eventSubs[ch]; ok {
			delete(s.shared.eventSubs, ch)
			close(ch)
		}
		s.shared.eventMu.Unlock()
	}
}

func scanSystemEvents(rows *sql.Rows) ([]SystemEvent, error) {
	var out []SystemEvent
	for rows.Next() {
		event, err := scanSystemEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

type systemEventScanner interface {
	Scan(dest ...any) error
}

func scanSystemEvent(row systemEventScanner) (SystemEvent, error) {
	var event SystemEvent
	var detailsJSON string
	var firstSeenAt, lastSeenAt, createdAt, updatedAt, readAt, resolvedAt any
	if err := row.Scan(
		&event.ID,
		&event.Fingerprint,
		&event.Source,
		&event.Category,
		&event.Severity,
		&event.Status,
		&event.Title,
		&event.Message,
		&detailsJSON,
		&event.TraceID,
		&event.SessionID,
		&event.JobID,
		&event.UpstreamID,
		&event.Model,
		&event.OccurrenceCount,
		&firstSeenAt,
		&lastSeenAt,
		&createdAt,
		&updatedAt,
		&readAt,
		&resolvedAt,
	); err != nil {
		return SystemEvent{}, err
	}
	event.DetailsJSON = json.RawMessage(detailsJSON)
	var err error
	if event.FirstSeenAt, err = timeParseValue(firstSeenAt); err != nil {
		return SystemEvent{}, err
	}
	if event.LastSeenAt, err = timeParseValue(lastSeenAt); err != nil {
		return SystemEvent{}, err
	}
	if event.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return SystemEvent{}, err
	}
	if event.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return SystemEvent{}, err
	}
	if event.ReadAt, err = timeParseNullableValue(readAt); err != nil {
		return SystemEvent{}, err
	}
	if event.ResolvedAt, err = timeParseNullableValue(resolvedAt); err != nil {
		return SystemEvent{}, err
	}
	return event, nil
}

func buildSystemEventFilterClause(filter SystemEventFilter) (string, []any) {
	var clauses []string
	var args []any
	if status := normalizedFilterToken(filter.Status); status != "" {
		clauses = append(clauses, `status = ?`)
		args = append(args, status)
	}
	if severity := normalizedFilterToken(filter.Severity); severity != "" {
		clauses = append(clauses, `severity = ?`)
		args = append(args, severity)
	}
	if source := normalizedFilterToken(filter.Source); source != "" {
		clauses = append(clauses, `source = ?`)
		args = append(args, source)
	}
	if category := normalizedFilterToken(filter.Category); category != "" {
		clauses = append(clauses, `category = ?`)
		args = append(args, category)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, `last_seen_at >= ?`)
		args = append(args, filter.Since.UTC().Format(timeLayout))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		pattern := "%" + escapeLike(query) + "%"
		clauses = append(clauses, `(
			LOWER(fingerprint) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(title) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(message) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(trace_id) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(session_id) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(upstream_id) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(model) LIKE LOWER(?) ESCAPE '\'
		)`)
		for range 7 {
			args = append(args, pattern)
		}
	}
	if len(clauses) == 0 {
		return "1 = 1", nil
	}
	return strings.Join(clauses, " AND "), args
}

// normalizedFilterToken normalizes a small enum-like filter value (event status/severity/source/
// category, finding severity/category). Values are matched case-insensitively and `all` means "no
// filter", so the same token means the same thing in every list API. The findings API used the raw
// query value, which made `?severity=High` and `?severity=all` return an empty list where the events
// API returned the matching rows.
func normalizedFilterToken(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "all" {
		return ""
	}
	return value
}

func (s *Store) getSystemEvent(whereSQL string, arg any) (SystemEvent, error) {
	var event SystemEvent
	row := s.db.QueryRow(`
		SELECT id, fingerprint, source, category, severity, status, title, message, details_json,
			trace_id, session_id, job_id, upstream_id, model, occurrence_count,
			first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at
		FROM system_events
		WHERE `+whereSQL+`
	`, arg)
	event, err := scanSystemEvent(row)
	if err != nil {
		return SystemEvent{}, err
	}
	return event, nil
}

func (s *Store) systemEventCountBy(column string, whereSQL string, args []any, limit int) ([]CountItem, error) {
	switch column {
	case "source", "category", "severity", "status":
	default:
		return nil, fmt.Errorf("unsupported system event count column %q", column)
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit)
	rows, err := s.db.Query(`
		SELECT `+column+`, COUNT(*) AS count
		FROM system_events
		WHERE `+whereSQL+` AND `+column+` != ''
		GROUP BY `+column+`
		ORDER BY count DESC, `+column+` ASC
		LIMIT ?
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCountItems(rows)
}

func (s *Store) upsertSystemEventsForLog(traceID string, header recordfile.RecordHeader, grouping GroupingInfo) error {
	if reason := strings.TrimSpace(header.Meta.RoutingFailureReason); reason != "" {
		if _, err := s.UpsertSystemEvent(systemEventForRoutingFailure(traceID, header, grouping, reason)); err != nil {
			return err
		}
		return nil
	}
	if errorText := strings.TrimSpace(header.Meta.Error); errorText != "" {
		if _, err := s.UpsertSystemEvent(systemEventForTransportError(traceID, header, grouping, errorText)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) notifySystemEventIDChanged(id string) {
	event, err := s.GetSystemEvent(strings.TrimSpace(id))
	if err != nil {
		return
	}
	s.notifySystemEventChanged(event)
}

func (s *Store) notifySystemEventChanged(event SystemEvent) {
	s.shared.eventMu.Lock()
	s.shared.eventSeq++
	notification := SystemEventNotification{
		Sequence: s.shared.eventSeq,
		EventID:  event.ID,
		Status:   event.Status,
		Severity: event.Severity,
		Source:   event.Source,
		Category: event.Category,
		At:       time.Now().UTC(),
	}
	// The sends happen while eventMu is still held, and the unsubscribe closure
	// closes the channel under that same mutex, so a send can never observe a
	// closed channel. Publishing from a snapshot taken before the unlock races
	// with close, and `select` treats a send on a closed channel as a ready
	// case, so the non-blocking `default` branch does not protect it. That panic
	// lands on the parse/reanalysis worker goroutines that call
	// UpsertSystemEvent, and nothing in the repository recovers, so it would
	// terminate the whole server. Each send is non-blocking, so holding the
	// mutex across the loop stays O(subscribers) and cannot block on a reader.
	for ch := range s.shared.eventSubs {
		select {
		case ch <- notification:
		default:
		}
	}
	s.shared.eventMu.Unlock()
	// The event feed is what the sidebar badge and the events page read, so it
	// is also a change topic. Published after the unlock: taking changeMu while
	// holding eventMu would add a second lock order for no benefit.
	s.notifyChange(ChangeEvents)
}

func systemEventForParseFailure(job ParseJobRecord) SystemEvent {
	return SystemEvent{
		Fingerprint: "parser:parse_job:" + normalizeEventFingerprintPart(job.LastError),
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Observation parse job failed",
		Message:     job.LastError,
		TraceID:     job.TraceID,
		JobID:       fmt.Sprint(job.ID),
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"attempts": job.Attempts,
			"status":   job.Status,
		}),
	}
}

func systemEventForAnalysisFailure(run AnalysisRunRecord) SystemEvent {
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"analyzer",
			normalizeEventFingerprintPart(run.Kind),
			normalizeEventFingerprintPart(run.Analyzer),
			normalizeEventFingerprintPart(run.Status),
			firstNonEmpty(run.TraceID, run.SessionID, "workspace"),
		}, ":"),
		Source:    "analyzer",
		Category:  "analysis_failure",
		Severity:  "error",
		Title:     "Analysis run failed",
		Message:   firstNonEmpty(run.OutputJSON, run.Status),
		TraceID:   run.TraceID,
		SessionID: run.SessionID,
		JobID:     fmt.Sprint(run.ID),
		Model:     run.Model,
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"kind":             run.Kind,
			"analyzer":         run.Analyzer,
			"analyzer_version": run.AnalyzerVersion,
			"input_ref":        run.InputRef,
			"status":           run.Status,
		}),
	}
}

func systemEventForAnalysisJobFailure(job AnalysisJobRecord) SystemEvent {
	traceID := ""
	sessionID := ""
	switch job.TargetType {
	case "trace":
		traceID = job.TargetID
	case "session":
		sessionID = job.TargetID
	}
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"analysis_job",
			normalizeEventFingerprintPart(job.JobType),
			normalizeEventFingerprintPart(job.TargetType),
			normalizeEventFingerprintPart(job.TargetID),
			normalizeEventFingerprintPart(job.LastError),
		}, ":"),
		Source:    "analyzer",
		Category:  "analysis_job_failure",
		Severity:  "error",
		Title:     "Reanalysis job failed",
		Message:   job.LastError,
		TraceID:   traceID,
		SessionID: sessionID,
		JobID:     fmt.Sprint(job.ID),
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"job_type":    job.JobType,
			"target_type": job.TargetType,
			"target_id":   job.TargetID,
			"status":      job.Status,
			"attempts":    job.Attempts,
			"steps":       job.StepsJSON,
		}),
	}
}

func systemEventForRoutingFailure(traceID string, header recordfile.RecordHeader, grouping GroupingInfo, reason string) SystemEvent {
	model := firstNonEmpty(header.Meta.Model, "unknown-model")
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"router",
			normalizeEventFingerprintPart(model),
			normalizeEventFingerprintPart(reason),
		}, ":"),
		Source:     "router",
		Category:   "routing_failure",
		Severity:   "error",
		Title:      "Routing failed",
		Message:    firstNonEmpty(header.Meta.Error, reason),
		TraceID:    traceID,
		SessionID:  grouping.SessionID,
		UpstreamID: header.Meta.SelectedUpstreamID,
		Model:      header.Meta.Model,
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"endpoint":                 header.Meta.Endpoint,
			"routing_policy":           header.Meta.RoutingPolicy,
			"routing_failure_reason":   reason,
			"routing_candidate_count":  header.Meta.RoutingCandidateCount,
			"status_code":              header.Meta.StatusCode,
			"selected_upstream_id":     header.Meta.SelectedUpstreamID,
			"selected_upstream_preset": header.Meta.SelectedUpstreamProviderPreset,
		}),
	}
}

func systemEventForTransportError(traceID string, header recordfile.RecordHeader, grouping GroupingInfo, errorText string) SystemEvent {
	class := classifySystemTransportError(errorText, header.Meta.StatusCode)
	severity := "error"
	if class == "client_disconnect" {
		severity = "warning"
	}
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"upstream",
			normalizeEventFingerprintPart(firstNonEmpty(header.Meta.SelectedUpstreamID, "unknown-upstream")),
			normalizeEventFingerprintPart(header.Meta.Endpoint),
			class,
		}, ":"),
		Source:     "upstream",
		Category:   "transport_error",
		Severity:   severity,
		Title:      "Upstream transport error",
		Message:    errorText,
		TraceID:    traceID,
		SessionID:  grouping.SessionID,
		UpstreamID: header.Meta.SelectedUpstreamID,
		Model:      header.Meta.Model,
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"endpoint":             header.Meta.Endpoint,
			"status_code":          header.Meta.StatusCode,
			"error_class":          class,
			"selected_upstream_id": header.Meta.SelectedUpstreamID,
			"is_stream":            header.Layout.IsStream,
		}),
	}
}

func normalizeEventFingerprintPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func mustMarshalSystemEventDetails(details map[string]any) json.RawMessage {
	data, err := json.Marshal(details)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(data)
}

func encodeSystemEventCursor(event SystemEvent) (string, error) {
	event.ID = strings.TrimSpace(event.ID)
	if event.ID == "" || event.LastSeenAt.IsZero() {
		return "", errors.New("encode system event cursor: missing sort key")
	}
	payload := systemEventCursor{
		Version:    systemEventCursorVersion,
		LastSeenAt: event.LastSeenAt.UTC().Format(timeLayout),
		ID:         event.ID,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeSystemEventCursor(value string) (systemEventCursorPosition, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: cursor is empty")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: invalid encoding")
	}
	var payload systemEventCursor
	if err := json.Unmarshal(data, &payload); err != nil {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: invalid payload")
	}
	if payload.Version != systemEventCursorVersion {
		return systemEventCursorPosition{}, fmt.Errorf("decode system event cursor: unsupported version %d", payload.Version)
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: id is required")
	}
	lastSeenAt, err := timeParse(payload.LastSeenAt)
	if err != nil || lastSeenAt.IsZero() {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: invalid last_seen_at")
	}
	return systemEventCursorPosition{LastSeenAt: lastSeenAt.UTC(), ID: payload.ID}, nil
}
