// Session summaries and the session pages built from them: the summary rows the index
// maintains per session, the rebuild that derives them from the logs, and the paged
// session queries the monitor reads.

package store

import (
	"fmt"
	"math"
	"os"
	"strings"
	"time"
)

type SessionSummary struct {
	SessionID      string
	SessionSource  string
	RequestCount   int
	FirstSeen      time.Time
	LastSeen       time.Time
	LastModel      string
	Providers      []string
	SuccessRequest int
	FailedRequest  int
	SuccessRate    float64
	TotalTokens    int
	AvgTTFT        int
	TotalDuration  int64
	StreamCount    int
}

type SessionPageResult struct {
	Items      []SessionSummary
	Total      int
	Page       int
	PageSize   int
	TotalPages int
}

type SessionSummaryRebuildStats struct {
	SessionID      string `json:"session_id,omitempty"`
	CandidateCount int    `json:"candidate_count"`
	ExistingCount  int    `json:"existing_count"`
	WouldDeleteAll bool   `json:"would_delete_all"`
	WouldDeleteOne bool   `json:"would_delete_one"`
}

func (s *Store) sessionProvidersAggregateSQL() string {
	if s != nil && s.driver == "postgres" {
		return "COALESCE(string_agg(DISTINCT CASE WHEN s.provider <> '' THEN s.provider END, ','), '')"
	}
	return "COALESCE(GROUP_CONCAT(DISTINCT CASE WHEN s.provider <> '' THEN s.provider END), '')"
}

func (s *Store) sessionIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	var sessionID string
	if err := s.db.QueryRow(`SELECT session_id FROM logs WHERE path = ?`, path).Scan(&sessionID); err != nil {
		return ""
	}
	return sessionID
}

// refreshSessionSummariesBestEffort returns the sessions whose summary could not be
// rebuilt, so the caller can keep them queued instead of losing the update.
func (s *Store) refreshSessionSummariesBestEffort(sessionIDs ...string) []string {
	seen := map[string]struct{}{}
	var failed []string
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		if err := s.RebuildSessionSummary(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "trajecta: refresh session summary %q failed: %v\n", sessionID, err)
			failed = append(failed, sessionID)
		}
	}
	return failed
}

func (s *Store) RebuildSessionSummary(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.execTx(tx, `DELETE FROM session_summaries WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := s.execTx(tx, s.insertSessionSummaryFromLogsSQL(`s.session_id = ? AND `+clientVisibleLogClause("s")), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionSummaryRebuildStats reports the summary table as it is, without
// applying anything the write path deferred: it is the dry-run input of
// `db summary rebuild sessions`, and the operator needs to see existing drift
// rather than have it repaired before it is counted.
func (s *Store) SessionSummaryRebuildStats(sessionID string) (SessionSummaryRebuildStats, error) {
	sessionID = strings.TrimSpace(sessionID)
	stats := SessionSummaryRebuildStats{SessionID: sessionID}
	if sessionID == "" {
		stats.WouldDeleteAll = true
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_summaries`).Scan(&stats.ExistingCount); err != nil {
			return stats, err
		}
		if err := s.db.QueryRow(`SELECT COUNT(DISTINCT session_id) FROM logs s WHERE s.session_id <> '' AND ` + clientVisibleLogClause("s")).Scan(&stats.CandidateCount); err != nil {
			return stats, err
		}
		return stats, nil
	}
	stats.WouldDeleteOne = true
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_summaries WHERE session_id = ?`, sessionID).Scan(&stats.ExistingCount); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT session_id) FROM logs s WHERE s.session_id = ? AND `+clientVisibleLogClause("s"), sessionID).Scan(&stats.CandidateCount); err != nil {
		return stats, err
	}
	return stats, nil
}

func (s *Store) RebuildSessionSummaries() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.execTx(tx, `DELETE FROM session_summaries`); err != nil {
		return err
	}
	if _, err := s.execTx(tx, s.insertSessionSummaryFromLogsSQL(clientVisibleLogClause("s"))); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) insertSessionSummaryFromLogsSQL(whereSQL string) string {
	whereSQL = andSQL(`s.session_id <> ''`, whereSQL)
	return `
		INSERT INTO session_summaries (
			session_id, session_source, request_count, first_seen, last_seen, last_model, providers,
			success_request, failed_request, success_rate, total_tokens, avg_ttft, total_duration, stream_count, updated_at
		)
		SELECT
			s.session_id,
			MIN(s.session_source) AS session_source,
			COUNT(*) AS request_count,
			MIN(s.recorded_at) AS first_seen,
			MAX(s.recorded_at) AS last_seen,
			COALESCE((
				SELECT model FROM logs l2
				WHERE l2.session_id = s.session_id
					AND ` + clientVisibleLogClause("l2") + `
				ORDER BY l2.recorded_at DESC, l2.trace_id DESC
				LIMIT 1
			), '') AS last_model,
			` + s.sessionProvidersAggregateSQL() + ` AS providers,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN s.status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.ttft_ms END), 0) AS avg_ttft,
			COALESCE(SUM(s.duration_ms), 0) AS total_duration,
			COALESCE(SUM(` + s.boolCountCaseSQL("s.is_stream") + `), 0) AS stream_count,
			CURRENT_TIMESTAMP AS updated_at
		FROM logs s
		WHERE ` + whereSQL + `
		GROUP BY s.session_id
	`
}

// sessionSummaryFilterSupported reports whether the summary row can answer the filter with exactly
// the semantics the log-derived path gives it. The row stores the session's last model, its provider
// list and its request counters, so a filter on the model or a free-text query - both of which the
// log path answers for *any* request of the session - cannot be expressed from it: a session that
// used the filtered model first and another model last would be missing from the summary answer.
// Those filters stay on the log path so the canary read switch never changes what is listed.
func sessionSummaryFilterSupported(filter ListFilter) bool {
	return strings.TrimSpace(filter.Model) == "" &&
		strings.TrimSpace(filter.Query) == "" &&
		strings.TrimSpace(filter.Endpoint) == "" &&
		strings.TrimSpace(filter.SelectedUpstream) == "" &&
		strings.TrimSpace(filter.ObservationStatus) == "" &&
		!filter.MissingUsage &&
		filter.MinDurationMs == 0 &&
		filter.MaxDurationMs == 0 &&
		filter.MinTTFTMs == 0 &&
		filter.MaxTTFTMs == 0 &&
		filter.MinTokens == 0 &&
		filter.MaxTokens == 0
}

func (s *Store) listSessionPageFromSummaries(page int, pageSize int, filter ListFilter) (SessionPageResult, bool, error) {
	// EXISTS rather than COUNT(*): this only asks whether the table has any row at
	// all, and on a large install the count has to visit every row of the summary
	// table just to answer it. The count below is the one the page needs for its
	// total, and it is filtered.
	var hasSummaries bool
	if err := s.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM session_summaries)`).Scan(&hasSummaries); err != nil {
		return SessionPageResult{}, false, err
	}
	if !hasSummaries {
		return SessionPageResult{}, false, nil
	}

	whereSQL, whereArgs := buildSessionSummaryFilterClause(filter)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_summaries s WHERE `+whereSQL, whereArgs...).Scan(&total); err != nil {
		return SessionPageResult{}, false, err
	}
	offset := (page - 1) * pageSize
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, pageSize, offset)
	rows, err := s.db.Query(`
		SELECT
			s.session_id,
			s.session_source,
			s.request_count,
			s.first_seen,
			s.last_seen,
			s.last_model,
			s.providers,
			s.success_request,
			s.failed_request,
			s.success_rate,
			s.total_tokens,
			s.avg_ttft,
			s.total_duration,
			s.stream_count
		FROM session_summaries s
		WHERE `+whereSQL+`
		ORDER BY s.last_seen DESC, s.session_id DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return SessionPageResult{}, false, err
	}
	defer rows.Close()

	result := SessionPageResult{
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}
	for rows.Next() {
		summary, err := scanSessionSummary(rows)
		if err != nil {
			return SessionPageResult{}, false, err
		}
		result.Items = append(result.Items, summary)
	}
	if err := rows.Err(); err != nil {
		return SessionPageResult{}, false, err
	}
	if total > 0 {
		result.TotalPages = int(math.Ceil(float64(total) / float64(pageSize)))
	}
	return result, true, nil
}

func (s *Store) getSessionFromSummary(sessionID string) (SessionSummary, error) {
	row := s.db.QueryRow(`
		SELECT
			session_id,
			session_source,
			request_count,
			first_seen,
			last_seen,
			last_model,
			providers,
			success_request,
			failed_request,
			success_rate,
			total_tokens,
			avg_ttft,
			total_duration,
			stream_count
		FROM session_summaries
		WHERE session_id = ?
	`, sessionID)
	return scanSessionSummary(row)
}

func buildSessionSummaryFilterClause(filter ListFilter) (string, []any) {
	var clauses []string
	var args []any
	if query := strings.TrimSpace(filter.Query); query != "" {
		like := "%" + escapeLike(query) + "%"
		clauses = append(clauses, `(LOWER(s.session_id) LIKE LOWER(?) ESCAPE '\' OR LOWER(s.last_model) LIKE LOWER(?) ESCAPE '\' OR LOWER(s.providers) LIKE LOWER(?) ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if provider := strings.TrimSpace(filter.Provider); provider != "" {
		like := "%" + escapeLike(provider) + "%"
		clauses = append(clauses, `LOWER(s.providers) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, like)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		clauses = append(clauses, `LOWER(s.last_model) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+escapeLike(model)+"%")
	}
	switch strings.ToLower(strings.TrimSpace(filter.Status)) {
	case "success":
		clauses = append(clauses, `s.failed_request = 0`)
	case "failed", "error":
		clauses = append(clauses, `s.failed_request > 0`)
	}
	if len(clauses) == 0 {
		return "1=1", nil
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) ListSessionPage(page int, pageSize int, filter ListFilter) (SessionPageResult, error) {
	// The summary read path is a derived read model, so it observes every
	// deferred write before it answers; the log-derived fallback below does not
	// need it but is cheap to keep behind the same barrier.
	s.flushDerivedRefresh()
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if s.useSessionSummaryRead && sessionSummaryFilterSupported(filter) {
		result, ok, err := s.listSessionPageFromSummaries(page, pageSize, filter)
		if err == nil && ok {
			return result, nil
		}
	}

	// The list shows session-level counters, and both read paths derive them from the 2xx status
	// codes of the session's requests: success_request counts 2xx, failed_request counts everything
	// else. The status filter is session-level for the same reason, so it must not go through the
	// shared log clause, which would select a session that merely contains one matching request.
	sessionFilter := filter
	sessionFilter.Status = ""
	whereSQL, whereArgs := buildLogFilterClause(sessionFilter, "s")
	sessionWhere := andSQL(`s.session_id <> ''`, clientVisibleLogClause("s"))
	switch strings.ToLower(strings.TrimSpace(filter.Status)) {
	case "success":
		sessionWhere = andSQL(sessionWhere, `NOT EXISTS (SELECT 1 FROM logs f WHERE f.session_id = s.session_id AND `+clientVisibleLogClause("f")+` AND f.status_code NOT BETWEEN 200 AND 299)`)
	case "failed", "error":
		sessionWhere = andSQL(sessionWhere, `EXISTS (SELECT 1 FROM logs f WHERE f.session_id = s.session_id AND `+clientVisibleLogClause("f")+` AND f.status_code NOT BETWEEN 200 AND 299)`)
	}
	sessionWhere = andSQL(sessionWhere, whereSQL)
	sessionIDs, total, err := s.listSessionPageIDs(sessionWhere, whereArgs, page, pageSize)
	if err != nil {
		return SessionPageResult{}, err
	}

	result := SessionPageResult{
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}
	if total == 0 {
		return result, nil
	}
	result.TotalPages = totalPages(total, pageSize)
	if len(sessionIDs) == 0 {
		return result, nil
	}

	queryArgs := append([]any{}, whereArgs...)
	for _, sessionID := range sessionIDs {
		queryArgs = append(queryArgs, sessionID)
	}
	listSQL := `
		SELECT
			s.session_id,
			MIN(s.session_source) AS session_source,
			COUNT(*) AS request_count,
			MIN(s.recorded_at) AS first_seen,
			MAX(s.recorded_at) AS last_seen,
			COALESCE((
				SELECT model FROM logs l2
				WHERE l2.session_id = s.session_id
					AND ` + clientVisibleLogClause("l2") + `
				ORDER BY l2.recorded_at DESC, l2.trace_id DESC
				LIMIT 1
			), '') AS last_model,
			` + s.sessionProvidersAggregateSQL() + ` AS providers,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN s.status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.ttft_ms END), 0) AS avg_ttft,
			COALESCE(SUM(s.duration_ms), 0) AS total_duration,
			COALESCE(SUM(` + s.boolCountCaseSQL("s.is_stream") + `), 0) AS stream_count
		FROM logs s
		WHERE ` + sessionWhere + ` AND s.session_id IN (` + placeholders(len(sessionIDs)) + `)
		GROUP BY s.session_id
	`
	rows, err := s.db.Query(listSQL, queryArgs...)
	if err != nil {
		return SessionPageResult{}, err
	}
	defer rows.Close()

	bySessionID := make(map[string]SessionSummary, len(sessionIDs))
	for rows.Next() {
		summary, err := scanSessionSummary(rows)
		if err != nil {
			return SessionPageResult{}, err
		}
		bySessionID[summary.SessionID] = summary
	}
	if err := rows.Err(); err != nil {
		return SessionPageResult{}, err
	}
	for _, sessionID := range sessionIDs {
		if summary, ok := bySessionID[sessionID]; ok {
			result.Items = append(result.Items, summary)
		}
	}
	return result, nil
}

func (s *Store) listSessionPageIDs(sessionWhere string, whereArgs []any, page int, pageSize int) ([]string, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT s.session_id) FROM logs s WHERE `+sessionWhere, whereArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	offset := (page - 1) * pageSize
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, pageSize, offset)
	rows, err := s.db.Query(`
		SELECT s.session_id
		FROM logs s
		WHERE `+sessionWhere+`
		GROUP BY s.session_id
		ORDER BY MAX(s.recorded_at) DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, 0, err
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return sessionIDs, total, nil
}

func (s *Store) GetSession(sessionID string) (SessionSummary, error) {
	s.flushDerivedRefresh()
	if s.useSessionSummaryRead {
		summary, err := s.getSessionFromSummary(sessionID)
		if err == nil {
			return summary, nil
		}
	}
	row := s.db.QueryRow(`
		SELECT
			s.session_id,
			MIN(s.session_source) AS session_source,
			COUNT(*) AS request_count,
			MIN(s.recorded_at) AS first_seen,
			MAX(s.recorded_at) AS last_seen,
			COALESCE((
				SELECT model FROM logs l2
				WHERE l2.session_id = s.session_id
					AND `+clientVisibleLogClause("l2")+`
				ORDER BY l2.recorded_at DESC, l2.trace_id DESC
				LIMIT 1
			), '') AS last_model,
			`+s.sessionProvidersAggregateSQL()+` AS providers,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN s.status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.ttft_ms END), 0) AS avg_ttft,
			COALESCE(SUM(s.duration_ms), 0) AS total_duration,
			COALESCE(SUM(`+s.boolCountCaseSQL("s.is_stream")+`), 0) AS stream_count
		FROM logs s
		WHERE s.session_id = ? AND `+clientVisibleLogClause("s")+`
		GROUP BY s.session_id
	`, sessionID)
	return scanSessionSummary(row)
}

func (s *Store) ListTracesBySession(sessionID string) ([]LogEntry, error) {
	rows, err := s.db.Query(`
		SELECT
			trace_id, path, version, request_id, recorded_at, model, provider, operation, endpoint, url, method, status_code,
			duration_ms, ttft_ms, client_ip, content_length, error_text,
			prompt_tokens, completion_tokens, total_tokens, cached_tokens,
			req_header_len, req_body_len, res_header_len, res_body_len, is_stream,
			session_id, session_source, window_id, client_request_id,
			request_audit_id, response_id,
			exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index,
			selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
			routing_policy, routing_score, routing_candidate_count, routing_failure_reason
		FROM logs
		WHERE session_id = ? AND `+clientVisibleLogClause("")+`
		ORDER BY recorded_at DESC, trace_id DESC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := s.populateObservationMetadata(entries); err != nil {
		return nil, err
	}
	return entries, rows.Err()
}

func scanSessionSummary(scanner interface {
	Scan(dest ...any) error
}) (SessionSummary, error) {
	var (
		summary      SessionSummary
		firstSeen    any
		lastSeen     any
		providersCSV string
		avgTTFT      float64
	)
	err := scanner.Scan(
		&summary.SessionID,
		&summary.SessionSource,
		&summary.RequestCount,
		&firstSeen,
		&lastSeen,
		&summary.LastModel,
		&providersCSV,
		&summary.SuccessRequest,
		&summary.FailedRequest,
		&summary.SuccessRate,
		&summary.TotalTokens,
		&avgTTFT,
		&summary.TotalDuration,
		&summary.StreamCount,
	)
	if err != nil {
		return SessionSummary{}, err
	}
	summary.FirstSeen, err = timeParseValue(firstSeen)
	if err != nil {
		return SessionSummary{}, err
	}
	summary.LastSeen, err = timeParseValue(lastSeen)
	if err != nil {
		return SessionSummary{}, err
	}
	summary.AvgTTFT = int(math.Round(avgTTFT))
	summary.Providers = splitProviders(providersCSV)
	return summary, nil
}

func normalizeWindowSessionID(windowID string) string {
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		return ""
	}
	sessionID, _, found := strings.Cut(windowID, ":")
	if !found {
		return windowID
	}
	return strings.TrimSpace(sessionID)
}
