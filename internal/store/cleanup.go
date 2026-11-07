// Request cleanup: the derived-data side of `trajecta db cleanup requests`.
//
// The command removes recorded requests that match an HTTP-status filter and
// everything that hangs off those traces - child model exchanges, the per-trace
// derived tables, the upstream-exchange audit rows and the session summaries.
// The selection, the delete set and the summary rebuild live here rather than in
// the CLI so the whole operation is testable against a real store and the CLI
// stays a flag-and-format layer.
//
// Raw `.http` cassettes are the source of truth, so the store never deletes
// files: it reports the cassette paths it removed from the index and leaves the
// filesystem step to the caller, which can then apply its own containment checks.

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// requestCleanupDerivedTables are the per-trace tables that are rebuilt from a
// cassette. They reference a trace only through trace_id, so deleting the ids
// removes every row that would otherwise become an orphan.
var requestCleanupDerivedTables = []string{
	"trace_observations",
	"trace_findings",
	"trace_resources",
	"credential_mask_audits",
	"credential_mask_event_states",
	"analysis_runs",
	"parse_jobs",
}

// requestCleanupReferenceTables point at a trace from a table an operator filled
// on purpose - curated datasets, eval scores, the event feed. They are only
// deleted when the caller explicitly opts in, because dropping them changes
// durable operator data rather than rebuildable index state.
var requestCleanupReferenceTables = []string{
	"dataset_examples",
	"scores",
	"system_events",
}

// requestCleanupSampleLimit caps how many matched requests a plan carries for
// display. The counts and the delete set are never capped.
const requestCleanupSampleLimit = 50

// RequestCleanupFilter selects the recorded requests a cleanup run targets. At
// least one of StatusCodes, Failed or TraceIDs must be set; the remaining fields
// only narrow the selection.
type RequestCleanupFilter struct {
	// StatusCodes are exact HTTP status codes to match (for example 404).
	StatusCodes []int
	// Failed matches every non-2xx status plus any request that recorded an error.
	Failed bool
	// TraceIDs selects traces explicitly. When it is set it also matches
	// child model exchanges, which status filters deliberately skip because they
	// are not client-visible.
	TraceIDs   []string
	SessionID  string
	Model      string
	Endpoint   string
	UpstreamID string
	Since      time.Time
	Until      time.Time
	// Limit caps how many client-visible requests a plan selects. Zero means all.
	Limit int
}

// RequestCleanupRequest describes one log row in a cleanup plan.
type RequestCleanupRequest struct {
	TraceID    string    `json:"trace_id"`
	Path       string    `json:"path"`
	StatusCode int       `json:"status_code"`
	Model      string    `json:"model,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	Endpoint   string    `json:"endpoint,omitempty"`
	ErrorText  string    `json:"error_text,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
	// Child is true for a model-kind exchange pulled in because it belongs to a
	// matched client-visible request.
	Child bool `json:"child_exchange"`
}

// RequestCleanupStatusCount is the distribution of matched statuses.
type RequestCleanupStatusCount struct {
	StatusCode int `json:"status_code"`
	Count      int `json:"count"`
}

// RequestCleanupPlan is the read-only result of a dry run: what would be
// deleted, and how much cassette data is involved.
type RequestCleanupPlan struct {
	SampleRequests   []RequestCleanupRequest     `json:"sample_requests"`
	StatusCounts     []RequestCleanupStatusCount `json:"status_counts"`
	MatchedRequests  int                         `json:"matched_requests"`
	ChildExchanges   int                         `json:"child_exchanges"`
	TotalLogRows     int                         `json:"total_log_rows"`
	AffectedSessions int                         `json:"affected_sessions"`
	CassetteCount    int                         `json:"cassette_count"`
	CassetteBytes    int64                       `json:"cassette_bytes"`
	MissingCassettes int                         `json:"missing_cassettes"`
	// ReferenceCounts counts the curated rows (dataset examples, eval scores,
	// system events) that still point at the selected traces. They are reported
	// whether or not the run purges them, so a caller that keeps them knows how
	// much now-unreachable data it is choosing to leave behind.
	ReferenceCounts map[string]int `json:"reference_counts,omitempty"`

	// TraceIDs, CassettePaths and SessionIDs are the machine-readable delete
	// set. They are not serialized: the CLI reports their counts, and a plan can
	// be huge.
	TraceIDs      []string `json:"-"`
	CassettePaths []string `json:"-"`
	SessionIDs    []string `json:"-"`
}

// RequestCleanupApplyOptions configures what else an applied run removes.
type RequestCleanupApplyOptions struct {
	// PurgeReferences also deletes the curated rows that point at the traces
	// (dataset examples, eval scores, system events).
	PurgeReferences bool
}

// RequestCleanupApplyResult reports one applied run.
type RequestCleanupApplyResult struct {
	Plan                     RequestCleanupPlan `json:"plan"`
	DeletedLogs              int                `json:"deleted_logs"`
	DeletedUpstreamExchanges int                `json:"deleted_upstream_exchanges"`
	DeletedDerived           map[string]int     `json:"deleted_derived"`
	DeletedAnalysisJobs      int                `json:"deleted_analysis_jobs"`
	DeletedReferences        map[string]int     `json:"deleted_references,omitempty"`
	RebuiltSessions          int                `json:"rebuilt_sessions"`
	RemovedSessionSummaries  int                `json:"removed_session_summaries"`
	PrunedResources          int                `json:"pruned_resources"`
}

// requestCleanupRow is one selected log row plus the keys that link its children
// to it.
type requestCleanupRow struct {
	RequestCleanupRequest
	RequestAuditID string
	ResponseID     string
	ExchangeID     string
}

// PlanRequestCleanup selects the requests a filter matches and expands the
// client-visible ones to the child model exchanges they own. It never changes
// the store.
func (s *Store) PlanRequestCleanup(filter RequestCleanupFilter) (RequestCleanupPlan, error) {
	if s == nil || s.db == nil {
		return RequestCleanupPlan{}, errors.New("store is not open")
	}
	scopeSQL, args, err := requestCleanupScopeSQL(filter)
	if err != nil {
		return RequestCleanupPlan{}, err
	}
	query := `
		SELECT trace_id, path, status_code, model, session_id, endpoint, error_text, recorded_at,
		       request_audit_id, response_id, exchange_id
		FROM logs
		WHERE ` + scopeSQL + `
		ORDER BY recorded_at DESC, trace_id DESC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return RequestCleanupPlan{}, fmt.Errorf("select cleanup candidates: %w", err)
	}
	defer rows.Close()

	matched := make([]requestCleanupRow, 0, 64)
	for rows.Next() {
		row, err := scanRequestCleanupRow(rows)
		if err != nil {
			return RequestCleanupPlan{}, err
		}
		matched = append(matched, row)
	}
	if err := rows.Err(); err != nil {
		return RequestCleanupPlan{}, err
	}

	children, err := s.requestCleanupChildren(matched)
	if err != nil {
		return RequestCleanupPlan{}, err
	}

	plan := RequestCleanupPlan{
		SampleRequests:   make([]RequestCleanupRequest, 0, requestCleanupSampleLimit),
		MatchedRequests:  len(matched),
		ChildExchanges:   len(children),
		AffectedSessions: 0,
	}
	statusCounts := map[int]int{}
	traceSeen := make(map[string]struct{}, len(matched)+len(children))
	sessionSeen := map[string]struct{}{}
	pathSeen := map[string]struct{}{}

	add := func(row requestCleanupRow, child bool) {
		if row.TraceID == "" {
			return
		}
		if _, ok := traceSeen[row.TraceID]; ok {
			return
		}
		traceSeen[row.TraceID] = struct{}{}
		plan.TraceIDs = append(plan.TraceIDs, row.TraceID)
		if row.Path != "" {
			if _, ok := pathSeen[row.Path]; !ok {
				pathSeen[row.Path] = struct{}{}
				plan.CassettePaths = append(plan.CassettePaths, row.Path)
			}
		}
		statusCounts[row.StatusCode]++
		if child {
			return
		}
		if row.SessionID != "" {
			if _, ok := sessionSeen[row.SessionID]; !ok {
				sessionSeen[row.SessionID] = struct{}{}
				plan.SessionIDs = append(plan.SessionIDs, row.SessionID)
			}
		}
	}

	for _, row := range matched {
		add(row, false)
	}
	for _, row := range children {
		add(row, true)
	}

	plan.TotalLogRows = len(plan.TraceIDs)
	plan.AffectedSessions = len(plan.SessionIDs)
	for _, row := range matched {
		if len(plan.SampleRequests) >= requestCleanupSampleLimit {
			break
		}
		plan.SampleRequests = append(plan.SampleRequests, row.RequestCleanupRequest)
	}
	for _, code := range sortedStatusCodes(statusCounts) {
		plan.StatusCounts = append(plan.StatusCounts, RequestCleanupStatusCount{StatusCode: code, Count: statusCounts[code]})
	}

	cassetteCount, cassetteBytes, missing := cleanupCassetteStats(plan.CassettePaths)
	plan.CassetteCount = cassetteCount
	plan.CassetteBytes = cassetteBytes
	plan.MissingCassettes = missing
	if plan.ReferenceCounts, err = s.requestCleanupReferenceCounts(plan.TraceIDs); err != nil {
		return RequestCleanupPlan{}, err
	}
	return plan, nil
}

// requestCleanupReferenceCounts answers how much curated data still points at
// the selected traces. The tables are the ones requestCleanupReferenceTables
// lists, so the report and the optional purge stay in step.
func (s *Store) requestCleanupReferenceCounts(traceIDs []string) (map[string]int, error) {
	traceIDs = dedupeNonEmptyStrings(traceIDs)
	if len(traceIDs) == 0 {
		return nil, nil
	}
	counts := map[string]int{}
	for _, table := range requestCleanupReferenceTables {
		total := 0
		for _, chunk := range chunkStrings(traceIDs, storeSQLParamChunk) {
			var count int
			if err := s.db.QueryRow(
				`SELECT COUNT(*) FROM `+table+` WHERE trace_id IN (`+placeholders(len(chunk))+`)`,
				stringArgs(chunk)...,
			).Scan(&count); err != nil {
				return nil, fmt.Errorf("count %s references: %w", table, err)
			}
			total += count
		}
		if total > 0 {
			counts[table] = total
		}
	}
	if len(counts) == 0 {
		return nil, nil
	}
	return counts, nil
}

// ApplyRequestCleanup plans the selection and deletes it in one transaction.
// Cassette files are reported through the returned plan but never touched here.
func (s *Store) ApplyRequestCleanup(filter RequestCleanupFilter, opts RequestCleanupApplyOptions) (RequestCleanupApplyResult, error) {
	if s == nil || s.db == nil {
		return RequestCleanupApplyResult{}, errors.New("store is not open")
	}
	plan, err := s.PlanRequestCleanup(filter)
	if err != nil {
		return RequestCleanupApplyResult{}, err
	}
	result := RequestCleanupApplyResult{
		Plan:           plan,
		DeletedDerived: map[string]int{},
	}
	if len(plan.TraceIDs) == 0 {
		return result, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return result, fmt.Errorf("begin cleanup transaction: %w", err)
	}
	defer tx.Rollback()

	if result.DeletedLogs, err = requestCleanupDeleteByColumn(tx, s, "logs", "trace_id", plan.TraceIDs); err != nil {
		return result, err
	}
	// upstream_exchanges carries both the trace id and the cassette path of the
	// exchange it audited; either identifies a row this run owns.
	deletedExchanges, err := requestCleanupDeleteByColumn(tx, s, "upstream_exchanges", "trace_id", plan.TraceIDs)
	if err != nil {
		return result, err
	}
	deletedByPath, err := requestCleanupDeleteByColumn(tx, s, "upstream_exchanges", "cassette_path", plan.CassettePaths)
	if err != nil {
		return result, err
	}
	result.DeletedUpstreamExchanges = deletedExchanges + deletedByPath

	for _, table := range requestCleanupDerivedTables {
		deleted, err := requestCleanupDeleteByColumn(tx, s, table, "trace_id", plan.TraceIDs)
		if err != nil {
			return result, err
		}
		result.DeletedDerived[table] = deleted
	}

	if result.DeletedAnalysisJobs, err = requestCleanupDeleteAnalysisJobs(tx, s, plan.TraceIDs); err != nil {
		return result, err
	}

	if opts.PurgeReferences {
		result.DeletedReferences = map[string]int{}
		for _, table := range requestCleanupReferenceTables {
			deleted, err := requestCleanupDeleteByColumn(tx, s, table, "trace_id", plan.TraceIDs)
			if err != nil {
				return result, err
			}
			result.DeletedReferences[table] = deleted
		}
	}

	rebuilt, removed, err := s.rebuildSessionSummariesTx(tx, plan.SessionIDs)
	if err != nil {
		return result, err
	}
	result.RebuiltSessions = rebuilt
	result.RemovedSessionSummaries = removed

	if result.PrunedResources, err = pruneOrphanResourcesTx(tx, s); err != nil {
		return result, err
	}

	if err := tx.Commit(); err != nil {
		return result, fmt.Errorf("commit cleanup transaction: %w", err)
	}
	return result, nil
}

// requestCleanupScopeSQL builds the WHERE clause that selects the log rows to
// match. An explicit trace-id list replaces the client-visible restriction so a
// named child exchange can be removed directly; status filters stay on
// client-visible traffic, because a child failing does not by itself mean the
// request the operator sees failed.
func requestCleanupScopeSQL(filter RequestCleanupFilter) (string, []any, error) {
	traceIDs := dedupeNonEmptyStrings(filter.TraceIDs)
	if len(traceIDs) == 0 && len(filter.StatusCodes) == 0 && !filter.Failed {
		return "", nil, errors.New("request cleanup needs --status, --failed or --trace-id")
	}

	clauses := make([]string, 0, 8)
	args := make([]any, 0, 8)
	if len(traceIDs) > 0 {
		clauses = append(clauses, `trace_id IN (`+placeholders(len(traceIDs))+`)`)
		args = append(args, stringArgs(traceIDs)...)
	} else {
		clauses = append(clauses, clientVisibleLogClause(""))
	}
	if len(filter.StatusCodes) > 0 {
		clauses = append(clauses, `status_code IN (`+placeholders(len(filter.StatusCodes))+`)`)
		for _, code := range filter.StatusCodes {
			args = append(args, code)
		}
	}
	if filter.Failed {
		clauses = append(clauses, `(status_code NOT BETWEEN 200 AND 299 OR error_text <> '')`)
	}
	if sessionID := strings.TrimSpace(filter.SessionID); sessionID != "" {
		clauses = append(clauses, `session_id = ?`)
		args = append(args, sessionID)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		clauses = append(clauses, `model = ?`)
		args = append(args, model)
	}
	if endpoint := strings.TrimSpace(filter.Endpoint); endpoint != "" {
		clauses = append(clauses, `endpoint = ?`)
		args = append(args, endpoint)
	}
	if upstreamID := strings.TrimSpace(filter.UpstreamID); upstreamID != "" {
		clauses = append(clauses, `selected_upstream_id = ?`)
		args = append(args, upstreamID)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, `recorded_at >= ?`)
		args = append(args, filter.Since.UTC())
	}
	if !filter.Until.IsZero() {
		clauses = append(clauses, `recorded_at <= ?`)
		args = append(args, filter.Until.UTC())
	}
	return strings.Join(clauses, " AND "), args, nil
}

// requestCleanupChildren loads the model-kind exchanges owned by the matched
// client-visible requests. A child links to its entry through the request audit
// id, the response id or the parent exchange id, exactly as the trace detail
// view resolves child exchanges.
func (s *Store) requestCleanupChildren(parents []requestCleanupRow) ([]requestCleanupRow, error) {
	if len(parents) == 0 {
		return nil, nil
	}
	const perParentParams = 3
	chunkSize := storeSQLParamChunk / perParentParams
	if chunkSize < 1 {
		chunkSize = 1
	}
	seen := map[string]struct{}{}
	var children []requestCleanupRow
	for start := 0; start < len(parents); start += chunkSize {
		end := start + chunkSize
		if end > len(parents) {
			end = len(parents)
		}
		var audits, responses, exchanges []string
		for _, parent := range parents[start:end] {
			if id := strings.TrimSpace(parent.RequestAuditID); id != "" {
				audits = append(audits, id)
			}
			if id := strings.TrimSpace(parent.ResponseID); id != "" {
				responses = append(responses, id)
			}
			if id := strings.TrimSpace(parent.ExchangeID); id != "" {
				exchanges = append(exchanges, id)
			}
		}
		var clauses []string
		var args []any
		if len(audits) > 0 {
			clauses = append(clauses, `request_audit_id IN (`+placeholders(len(audits))+`)`)
			args = append(args, stringArgs(audits)...)
		}
		if len(responses) > 0 {
			clauses = append(clauses, `response_id IN (`+placeholders(len(responses))+`)`)
			args = append(args, stringArgs(responses)...)
		}
		if len(exchanges) > 0 {
			clauses = append(clauses, `parent_exchange_id IN (`+placeholders(len(exchanges))+`)`)
			args = append(args, stringArgs(exchanges)...)
		}
		if len(clauses) == 0 {
			continue
		}
		rows, err := s.db.Query(`
			SELECT trace_id, path, status_code, model, session_id, endpoint, error_text, recorded_at,
			       request_audit_id, response_id, exchange_id
			FROM logs
			WHERE exchange_kind = 'model' AND (`+strings.Join(clauses, " OR ")+`)
			ORDER BY sequence_index ASC, recorded_at ASC, trace_id ASC
		`, args...)
		if err != nil {
			return nil, fmt.Errorf("select cleanup child exchanges: %w", err)
		}
		for rows.Next() {
			row, err := scanRequestCleanupRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if row.TraceID == "" {
				continue
			}
			if _, ok := seen[row.TraceID]; ok {
				continue
			}
			seen[row.TraceID] = struct{}{}
			row.Child = true
			children = append(children, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return children, nil
}

func scanRequestCleanupRow(rows *sql.Rows) (requestCleanupRow, error) {
	var (
		row        requestCleanupRow
		recordedAt string
	)
	if err := rows.Scan(
		&row.TraceID, &row.Path, &row.StatusCode, &row.Model, &row.SessionID, &row.Endpoint,
		&row.ErrorText, &recordedAt, &row.RequestAuditID, &row.ResponseID, &row.ExchangeID,
	); err != nil {
		return row, fmt.Errorf("scan cleanup candidate: %w", err)
	}
	parsed, err := timeParse(recordedAt)
	if err != nil {
		return row, fmt.Errorf("parse cleanup candidate recorded_at %q: %w", recordedAt, err)
	}
	row.RecordedAt = parsed
	return row, nil
}

// requestCleanupDeleteByColumn deletes the rows of one table whose column is in
// values, in bounded batches so a large match cannot exceed the bind-parameter
// budget.
func requestCleanupDeleteByColumn(tx *sql.Tx, s *Store, table string, column string, values []string) (int, error) {
	values = dedupeNonEmptyStrings(values)
	total := 0
	for _, chunk := range chunkStrings(values, storeSQLParamChunk) {
		res, err := s.execTx(tx, `DELETE FROM `+table+` WHERE `+column+` IN (`+placeholders(len(chunk))+`)`, stringArgs(chunk)...)
		if err != nil {
			return total, fmt.Errorf("delete %s rows: %w", table, err)
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("count deleted %s rows: %w", table, err)
		}
		total += int(deleted)
	}
	return total, nil
}

func requestCleanupDeleteAnalysisJobs(tx *sql.Tx, s *Store, traceIDs []string) (int, error) {
	total := 0
	for _, chunk := range chunkStrings(dedupeNonEmptyStrings(traceIDs), storeSQLParamChunk) {
		res, err := s.execTx(tx, `
			DELETE FROM analysis_jobs
			WHERE target_type = 'trace' AND target_id IN (`+placeholders(len(chunk))+`)
		`, stringArgs(chunk)...)
		if err != nil {
			return total, fmt.Errorf("delete analysis_jobs rows: %w", err)
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("count deleted analysis_jobs rows: %w", err)
		}
		total += int(deleted)
	}
	return total, nil
}

// rebuildSessionSummariesTx deletes and re-derives the summaries of the given
// sessions inside the caller's transaction. A session whose last client-visible
// request was just deleted is not re-inserted, so its summary row disappears.
// Sessions are chunked but never split across chunks: a session appears in
// exactly one chunk, so its aggregate is always computed from all its rows.
func (s *Store) rebuildSessionSummariesTx(tx *sql.Tx, sessionIDs []string) (int, int, error) {
	ids := dedupeNonEmptyStrings(sessionIDs)
	if len(ids) == 0 {
		return 0, 0, nil
	}
	rebuilt, removed := 0, 0
	for _, chunk := range chunkStrings(ids, storeSQLParamChunk) {
		args := stringArgs(chunk)
		where := `session_id IN (` + placeholders(len(chunk)) + `)`

		var before int
		if err := s.queryRowTx(tx, `SELECT COUNT(*) FROM session_summaries WHERE `+where, args...).Scan(&before); err != nil {
			return rebuilt, removed, fmt.Errorf("count session summaries: %w", err)
		}
		if _, err := s.execTx(tx, `DELETE FROM session_summaries WHERE `+where, args...); err != nil {
			return rebuilt, removed, fmt.Errorf("clear session summaries: %w", err)
		}
		if _, err := s.execTx(tx, s.insertSessionSummaryFromLogsSQL(`s.`+where), args...); err != nil {
			return rebuilt, removed, fmt.Errorf("rebuild session summaries: %w", err)
		}
		var after int
		if err := s.queryRowTx(tx, `SELECT COUNT(*) FROM session_summaries WHERE `+where, args...).Scan(&after); err != nil {
			return rebuilt, removed, fmt.Errorf("count rebuilt session summaries: %w", err)
		}
		rebuilt += after
		removed += before - after
	}
	return rebuilt, removed, nil
}

// pruneOrphanResourcesTx drops catalogue rows no trace links to any more. It is
// the same decision SaveTraceResources makes per trace, done once for the bulk
// delete that just removed a set of trace_resources rows.
func pruneOrphanResourcesTx(tx *sql.Tx, s *Store) (int, error) {
	res, err := s.execTx(tx, `
		DELETE FROM resources
		WHERE NOT EXISTS (SELECT 1 FROM trace_resources tr WHERE tr.resource_id = resources.id)
	`)
	if err != nil {
		return 0, fmt.Errorf("prune orphan resources: %w", err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count pruned resources: %w", err)
	}
	return int(deleted), nil
}

// cleanupCassetteStats reports how many of the plan's cassette files exist and
// how large they are. A missing file is counted, not treated as an error: the
// index can outlive a cassette moved away by hand.
func cleanupCassetteStats(paths []string) (count int, bytes int64, missing int) {
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			missing++
			continue
		}
		if info.IsDir() {
			missing++
			continue
		}
		count++
		bytes += info.Size()
	}
	return count, bytes, missing
}

func sortedStatusCodes(counts map[int]int) []int {
	codes := make([]int, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	// Insertion sort: the map is tiny (a handful of status codes).
	for i := 1; i < len(codes); i++ {
		for j := i; j > 0 && codes[j-1] > codes[j]; j-- {
			codes[j-1], codes[j] = codes[j], codes[j-1]
		}
	}
	return codes
}
