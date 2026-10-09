// The semantic analysis results: the Observation IR rows a trace is parsed into, the
// metadata blob that travels with them, the findings the detectors raise, and the queries
// the Monitor and the MCP surface read them back through.

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/kingfs/Trajecta/pkg/observe"
	"strings"
	"time"
)

type ObservationSummary struct {
	TraceID          string
	Parser           string
	ParserVersion    string
	Status           string
	Provider         string
	Operation        string
	Model            string
	ExchangeKind     string
	ExchangeRole     string
	ParentExchangeID string
	SequenceIndex    int
	RequestAuditID   string
	ResponseID       string
	SummaryJSON      string
	WarningsJSON     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ObservationMetadata struct {
	Status        string
	Parser        string
	ParserVersion string
	UpdatedAt     time.Time
}

type FindingFilter struct {
	Category string
	Severity string
}

func (s *Store) populateObservationMetadata(entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	traceIDs := make([]string, 0, len(entries))
	byTraceID := make(map[string]int, len(entries))
	for idx := range entries {
		entries[idx].Observation.Status = "unparsed"
		traceIDs = append(traceIDs, entries[idx].ID)
		byTraceID[entries[idx].ID] = idx
	}
	meta, err := s.LoadObservationMetadata(traceIDs)
	if err != nil {
		return err
	}
	for traceID, observation := range meta {
		idx, ok := byTraceID[traceID]
		if !ok {
			continue
		}
		entries[idx].Observation = observation
	}
	return nil
}

func (s *Store) LoadObservationMetadata(traceIDs []string) (map[string]ObservationMetadata, error) {
	out := make(map[string]ObservationMetadata, len(traceIDs))
	if len(traceIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(traceIDs))
	for _, traceID := range traceIDs {
		traceID = strings.TrimSpace(traceID)
		if traceID == "" {
			continue
		}
		out[traceID] = ObservationMetadata{Status: "unparsed"}
		args = append(args, traceID)
	}
	if len(args) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`
		SELECT trace_id, parser, parser_version, status, updated_at
		FROM trace_observations
		WHERE trace_id IN (`+placeholders(len(args))+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			traceID   string
			meta      ObservationMetadata
			updatedAt any
		)
		if err := rows.Scan(&traceID, &meta.Parser, &meta.ParserVersion, &meta.Status, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if meta.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out[traceID] = meta
	}
	// A mid-iteration error (a dropped connection, a decode failure) has to surface: without this the
	// caller marks every trace missing from the short result as `unparsed`, which reads like a fact.
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	missingArgs := make([]string, 0, len(out))
	for traceID, meta := range out {
		if strings.TrimSpace(meta.Parser) == "" {
			missingArgs = append(missingArgs, traceID)
		}
	}
	if len(missingArgs) == 0 {
		return out, nil
	}
	// One row per trace id, straight off `parsejob_trace_id_unique` (Postgres) or
	// `idx_parse_jobs_trace_id` (SQLite). This used to be a `INNER JOIN (SELECT
	// trace_id, MAX(id) ... GROUP BY trace_id)` so that it would pick the newest
	// job of a trace that had several.
	//
	// That aggregate was the single worst statement in the deployment: the plan
	// materialised a grouped id list and hash-joined it, so every call seq-scanned
	// the whole of `parse_jobs` (239,578 rows, 6.59e9 rows read, 14,745 seq scans
	// on a 10-day window, twice per 60 s poll), and the list was never chunked, so
	// a session with more traces than the driver's bind-parameter limit failed
	// outright. Migration 20261006000000_unique_parse_jobs_trace_id collapsed the
	// duplicates and made trace_id unique, so "the newest job of a trace" is just
	// "the job of a trace" and the subquery has no work left to do.
	//
	// The uniqueness is load-bearing. `requirePostgresApplicationMigrations`
	// refuses to serve Postgres traffic without that index, and the SQLite schema
	// creates the same unique index at startup, so a database that has duplicates
	// cannot reach this code.
	for _, chunk := range chunkStrings(dedupeNonEmptyStrings(missingArgs), storeSQLParamChunk) {
		chunkRows, err := s.db.Query(`
			SELECT trace_id, status, updated_at
			FROM parse_jobs
			WHERE trace_id IN (`+placeholders(len(chunk))+`)
		`, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for chunkRows.Next() {
			var (
				traceID   string
				meta      ObservationMetadata
				updatedAt any
			)
			if err := chunkRows.Scan(&traceID, &meta.Status, &updatedAt); err != nil {
				chunkRows.Close()
				return nil, err
			}
			if meta.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
				chunkRows.Close()
				return nil, err
			}
			if strings.TrimSpace(meta.Status) == "" {
				meta.Status = "unparsed"
			}
			out[traceID] = meta
		}
		if err := chunkRows.Err(); err != nil {
			chunkRows.Close()
			return nil, err
		}
		if err := chunkRows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) SaveObservation(obs observe.TraceObservation) error {
	s.notifyChange(ChangeTraffic)
	if obs.TraceID == "" {
		return errors.New("save observation: trace id is required")
	}
	now := time.Now().UTC()
	warningsJSON, err := json.Marshal(obs.Warnings)
	if err != nil {
		return err
	}
	summaryJSON, err := json.Marshal(ObservationSummaryJSON(obs))
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := s.execTx(tx, `
		INSERT INTO parser_versions (parser, version, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(parser, version) DO NOTHING
	`, obs.Parser, obs.ParserVersion, now); err != nil {
		return err
	}
	if _, err := s.execTx(tx, `
		INSERT INTO trace_observations (
			trace_id, parser, parser_version, status, provider, operation, model,
			exchange_kind, exchange_role, parent_exchange_id, sequence_index, request_audit_id, response_id,
			summary_json, warnings_json, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(trace_id) DO UPDATE SET
			parser=excluded.parser,
			parser_version=excluded.parser_version,
			status=excluded.status,
			provider=excluded.provider,
			operation=excluded.operation,
			model=excluded.model,
			exchange_kind=excluded.exchange_kind,
			exchange_role=excluded.exchange_role,
			parent_exchange_id=excluded.parent_exchange_id,
			sequence_index=excluded.sequence_index,
			request_audit_id=excluded.request_audit_id,
			response_id=excluded.response_id,
			summary_json=excluded.summary_json,
			warnings_json=excluded.warnings_json,
			updated_at=excluded.updated_at
	`, sqlSafeText(obs.TraceID), sqlSafeText(obs.Parser), sqlSafeText(obs.ParserVersion), string(obs.Status), sqlSafeText(obs.Provider), sqlSafeText(obs.Operation), sqlSafeText(obs.Model),
		sqlSafeText(obs.ExchangeKind), sqlSafeText(obs.ExchangeRole), sqlSafeText(obs.ParentExchangeID), obs.SequenceIndex, sqlSafeText(obs.RequestAuditID), sqlSafeText(obs.ResponseID),
		sqlSafeBytes(summaryJSON), sqlSafeBytes(warningsJSON), now, now); err != nil {
		return err
	}
	// The semantic nodes are deliberately not written. They were ~240 rows and
	// ~1.4 MB per trace - 98% of the database's bytes, 115 GB of 117 GB - to
	// answer one detail view, and every re-analysis rewrote them. The cassette is
	// the source of truth for detail, so the node tree is parsed from it when a
	// trace is opened (see monitor.handleTraceObservation). What stays here is the
	// compact summary the lists, the Overview and the analysis jobs read.
	//
	// One parse job per trace: the observation result updates the job that
	// EnqueueParseJob queued (or creates it when a trace was parsed without
	// one). Inserting a second row here made every parsed trace appear twice in
	// parse_jobs and doubled the queue table with rows no worker ever claimed.
	//
	// This is one upsert rather than the UPDATE plus `INSERT ... WHERE NOT
	// EXISTS` it replaced. That pair was a read-then-write inside a transaction:
	// under READ COMMITTED two writers could both find no row and both insert,
	// which the unique index turns from a silent duplicate into a hard unique
	// violation. It also costs one statement instead of two on the path every
	// parsed trace takes. `attempts` keeps the same shape as before - 1 for a row
	// this statement creates, and 0 promoted to 1 for a row it finds.
	if _, err := s.execTx(tx, `
		INSERT INTO parse_jobs (trace_id, status, attempts, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?)
		ON CONFLICT (trace_id) DO UPDATE
		SET status = excluded.status,
			attempts = CASE WHEN parse_jobs.attempts = 0 THEN 1 ELSE parse_jobs.attempts END,
			last_error = '',
			updated_at = excluded.updated_at
	`, obs.TraceID, string(obs.Status), now, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) GetObservationSummary(traceID string) (ObservationSummary, error) {
	var summary ObservationSummary
	var createdAt, updatedAt any
	err := s.db.QueryRow(`
		SELECT trace_id, parser, parser_version, status, provider, operation, model,
			exchange_kind, exchange_role, parent_exchange_id, sequence_index, request_audit_id, response_id,
			summary_json, warnings_json, created_at, updated_at
		FROM trace_observations
		WHERE trace_id = ?
	`, traceID).Scan(
		&summary.TraceID,
		&summary.Parser,
		&summary.ParserVersion,
		&summary.Status,
		&summary.Provider,
		&summary.Operation,
		&summary.Model,
		&summary.ExchangeKind,
		&summary.ExchangeRole,
		&summary.ParentExchangeID,
		&summary.SequenceIndex,
		&summary.RequestAuditID,
		&summary.ResponseID,
		&summary.SummaryJSON,
		&summary.WarningsJSON,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return ObservationSummary{}, err
	}
	if summary.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return ObservationSummary{}, err
	}
	if summary.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return ObservationSummary{}, err
	}
	return summary, nil
}

func (s *Store) SaveFindings(traceID string, findings []observe.Finding) error {
	s.notifyChange(ChangeTraffic)
	if strings.TrimSpace(traceID) == "" {
		return errors.New("save findings: trace id is required")
	}
	now := time.Now().UTC()
	normalized := make([]observe.Finding, 0, len(findings))
	for _, finding := range findings {
		if finding.ID == "" {
			return errors.New("save findings: finding id is required")
		}
		if finding.CreatedAt.IsZero() {
			finding.CreatedAt = now
		}
		if finding.TraceID == "" {
			finding.TraceID = traceID
		}
		normalized = append(normalized, finding)
	}
	// A multi-row INSERT cannot name the same (trace_id, finding_id) twice:
	// Postgres fails the whole statement, and the row-by-row form it replaces
	// failed on the second row anyway. Fold duplicates first, last one wins, so a
	// reanalysis that emits the same finding twice still writes one row.
	unique := dedupeFindingsByKey(normalized)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := s.execTx(tx, `DELETE FROM trace_findings WHERE trace_id = ?`, traceID); err != nil {
		return err
	}
	// One statement per parameter-bounded chunk of findings instead of one per
	// finding; a session reanalysis used to issue hundreds of inserts.
	const findingColumns = 13
	rowsPerStatement := storeSQLParamChunk / findingColumns
	valueRow := "(" + placeholders(findingColumns) + ")"
	for start := 0; start < len(unique); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		values := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*findingColumns)
		for _, finding := range chunk {
			values = append(values, valueRow)
			args = append(args, finding.TraceID, finding.ID, finding.Category, string(finding.Severity), finding.Confidence,
				finding.Title, finding.Description, finding.EvidencePath, textPreview(finding.EvidenceExcerpt, 500),
				finding.NodeID, finding.Detector, finding.DetectorVersion, finding.CreatedAt)
		}
		if _, err := s.execTx(tx, `
			INSERT INTO trace_findings (
				trace_id, finding_id, category, severity, confidence, title, description,
				evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
			) VALUES `+strings.Join(values, ", "), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// dedupeFindingsByKey folds findings that share the (trace_id, finding_id)
// unique key, keeping the last occurrence in input order.
func dedupeFindingsByKey(findings []observe.Finding) []observe.Finding {
	if len(findings) < 2 {
		return findings
	}
	out := make([]observe.Finding, 0, len(findings))
	at := make(map[string]int, len(findings))
	for _, finding := range findings {
		key := finding.TraceID + "\x00" + finding.ID
		if index, ok := at[key]; ok {
			out[index] = finding
			continue
		}
		at[key] = len(out)
		out = append(out, finding)
	}
	return out
}

func (s *Store) ListFindings(traceID string, filter FindingFilter) ([]observe.Finding, error) {
	if strings.TrimSpace(traceID) == "" {
		return nil, errors.New("list findings: trace id is required")
	}
	query := `
		SELECT finding_id, trace_id, category, severity, confidence, title, description,
			evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
		FROM trace_findings
		WHERE trace_id = ?
	`
	args := []any{traceID}
	if category := normalizedFilterToken(filter.Category); category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}
	if severity := normalizedFilterToken(filter.Severity); severity != "" {
		query += ` AND severity = ?`
		args = append(args, severity)
	}
	query += ` ORDER BY id ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []observe.Finding
	for rows.Next() {
		finding, err := scanFindingRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, finding)
	}
	return out, rows.Err()
}

// ListFindingsByTraceIDs groups the findings of the given traces by trace id, in
// chunks the database accepts. The batch reanalysis used to call ListFindings
// once per trace of the session.
func (s *Store) ListFindingsByTraceIDs(traceIDs []string) (map[string][]observe.Finding, error) {
	out := make(map[string][]observe.Finding, len(traceIDs))
	for _, chunk := range chunkStrings(dedupeNonEmptyStrings(traceIDs), storeSQLParamChunk) {
		rows, err := s.db.Query(`
			SELECT finding_id, trace_id, category, severity, confidence, title, description,
				evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
			FROM trace_findings
			WHERE trace_id IN (`+placeholders(len(chunk))+`)
			ORDER BY id ASC
		`, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			finding, err := scanFindingRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[finding.TraceID] = append(out[finding.TraceID], finding)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanFindingRow reads one trace_findings row from the shared column list.
func scanFindingRow(rows *sql.Rows) (observe.Finding, error) {
	var finding observe.Finding
	var severity string
	var createdAt any
	if err := rows.Scan(&finding.ID, &finding.TraceID, &finding.Category, &severity, &finding.Confidence, &finding.Title,
		&finding.Description, &finding.EvidencePath, &finding.EvidenceExcerpt, &finding.NodeID,
		&finding.Detector, &finding.DetectorVersion, &createdAt); err != nil {
		return observe.Finding{}, err
	}
	finding.Severity = observe.Severity(severity)
	parsed, err := timeParseValue(createdAt)
	if err != nil {
		return observe.Finding{}, err
	}
	finding.CreatedAt = parsed
	return finding, nil
}

func (s *Store) ListAllFindings(filter FindingFilter, limit int) ([]observe.Finding, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT finding_id, trace_id, category, severity, confidence, title, description,
			evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
		FROM trace_findings
		WHERE 1 = 1
	`
	var args []any
	if category := normalizedFilterToken(filter.Category); category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}
	if severity := normalizedFilterToken(filter.Severity); severity != "" {
		query += ` AND severity = ?`
		args = append(args, severity)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

func scanFindings(rows *sql.Rows) ([]observe.Finding, error) {
	var out []observe.Finding
	for rows.Next() {
		var finding observe.Finding
		var severity string
		var createdAt any
		if err := rows.Scan(&finding.ID, &finding.TraceID, &finding.Category, &severity, &finding.Confidence, &finding.Title,
			&finding.Description, &finding.EvidencePath, &finding.EvidenceExcerpt, &finding.NodeID,
			&finding.Detector, &finding.DetectorVersion, &createdAt); err != nil {
			return nil, err
		}
		finding.Severity = observe.Severity(severity)
		var err error
		finding.CreatedAt, err = timeParseValue(createdAt)
		if err != nil {
			return nil, err
		}
		out = append(out, finding)
	}
	return out, rows.Err()
}

// ObservationSummaryJSON is the compact per-trace summary stored on
// trace_observations. The Monitor renders the same shape when it parses a trace
// on demand, so the two views agree.
func ObservationSummaryJSON(obs observe.TraceObservation) map[string]any {
	return map[string]any{
		"request_nodes":  len(obs.Request.Nodes),
		"response_nodes": len(obs.Response.Nodes),
		"stream_events":  len(obs.Stream.Events),
		"tool_calls":     len(obs.Tools.Calls),
		"tool_results":   len(obs.Tools.Results),
		"findings":       len(obs.Findings),
		"exchange_kind":  obs.ExchangeKind,
		"exchange_role":  obs.ExchangeRole,
	}
}
