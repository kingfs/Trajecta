package store

import (
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/observe"
)

type OverviewOptions struct {
	Since time.Time
	// Location anchors the calendar buckets. Nil keeps the UTC grid.
	Location    *time.Location
	BucketSize  time.Duration
	BucketCount int
	Limit       int
}

type OverviewSummary struct {
	RequestCount   int
	SuccessRequest int
	FailedRequest  int
	SuccessRate    float64
	TotalTokens    int
	AvgTTFTMs      int
	AvgDurationMs  int64
	P95TTFTMs      int
	P95DurationMs  int64
	StreamCount    int
	SessionCount   int
}

type OverviewTimelineItem struct {
	Time          time.Time
	RequestCount  int
	FailedRequest int
	TotalTokens   int
	AvgTTFTMs     int
	AvgDurationMs int64
}

type OverviewBreakdown struct {
	Models                []CountItem
	Providers             []CountItem
	Endpoints             []CountItem
	Upstreams             []CountItem
	RoutingFailureReasons []CountItem
	FindingCategories     []CountItem
}

type OverviewAttention struct {
	RecentFailures   []LogEntry
	HighRiskFindings []observe.Finding
	RoutingFailures  []RoutingFailureRecord
	SlowTraces       []LogEntry
}

type OverviewAnalysisSummary struct {
	Total  int
	Failed int
	Recent []AnalysisRunRecord
}

type OverviewObservationSummary struct {
	TotalObservations int
	Parsed            int
	Failed            int
	Queued            int
	Running           int
	Unparsed          int
	RecentFailures    []ParseJobRecord
}

type OverviewDashboard struct {
	Summary     OverviewSummary
	Timeline    []OverviewTimelineItem
	Breakdown   OverviewBreakdown
	Attention   OverviewAttention
	Analysis    OverviewAnalysisSummary
	Observation OverviewObservationSummary
}

const (
	SystemEventStatusUnread   = "unread"
	SystemEventStatusRead     = "read"
	SystemEventStatusResolved = "resolved"
	SystemEventStatusIgnored  = "ignored"
)

// storeSQLParamChunk bounds how many bind parameters one statement carries.
// Postgres allows 65535 and current SQLite builds allow 32766, but the historic
// SQLite limit of 999 is the only value that is safe for every driver and
// version the project supports.
const storeSQLParamChunk = 900

func (s *Store) Overview(opts OverviewOptions) (OverviewDashboard, error) {
	if opts.Limit <= 0 {
		opts.Limit = 5
	}
	if opts.BucketSize <= 0 {
		opts.BucketSize = time.Hour
	}
	if opts.BucketCount <= 0 {
		opts.BucketCount = 12
	}

	whereSQL, whereArgs := overviewLogWhere(opts.Since)
	summary, err := s.overviewSummary(whereSQL, whereArgs)
	if err != nil {
		return OverviewDashboard{}, err
	}
	timeline, err := s.overviewTimeline(whereSQL, whereArgs, opts)
	if err != nil {
		return OverviewDashboard{}, err
	}
	breakdown, err := s.overviewBreakdown(whereSQL, whereArgs, opts.Limit)
	if err != nil {
		return OverviewDashboard{}, err
	}
	attention, err := s.overviewAttention(whereSQL, whereArgs, opts.Limit)
	if err != nil {
		return OverviewDashboard{}, err
	}
	analysis, err := s.overviewAnalysis(opts.Limit)
	if err != nil {
		return OverviewDashboard{}, err
	}
	observation, err := s.overviewObservation(opts.Limit)
	if err != nil {
		return OverviewDashboard{}, err
	}
	return OverviewDashboard{
		Summary:     summary,
		Timeline:    timeline,
		Breakdown:   breakdown,
		Attention:   attention,
		Analysis:    analysis,
		Observation: observation,
	}, nil
}

func (s *Store) overviewSummary(whereSQL string, whereArgs []any) (OverviewSummary, error) {
	var summary OverviewSummary
	var avgTTFT, avgDuration float64
	// ttft_samples and duration_samples are the denominators the p95 lookups
	// below need. They filter exactly the way those lookups do (`col > 0`), so
	// the single pass over the window also replaces their two COUNT queries.
	//
	// session_count is deliberately not one of these columns. COUNT(DISTINCT)
	// inside a wide aggregate forces a hash of every session id in the window on
	// top of the scan: on the reference deployment (251k client-visible rows) the
	// pass below took 818 ms with it and 311 ms without, while the same distinct
	// count alone costs 85 ms because `tracelog_session_recent_client_visible_idx`
	// carries session_id for exactly these rows and answers it index-only. Asking
	// for it separately is therefore ~420 ms cheaper than folding it in.
	var ttftSamples, durationSamples int
	query := `
		SELECT
			COUNT(*) AS request_count,
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 AND error_text = '' THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms END), 0) AS avg_ttft,
			COALESCE(AVG(CASE WHEN duration_ms > 0 THEN duration_ms END), 0) AS avg_duration,
			COALESCE(SUM(` + s.boolCountCaseSQL("is_stream") + `), 0) AS stream_count,
			COALESCE(SUM(CASE WHEN ttft_ms > 0 THEN 1 ELSE 0 END), 0) AS ttft_samples,
			COALESCE(SUM(CASE WHEN duration_ms > 0 THEN 1 ELSE 0 END), 0) AS duration_samples
		FROM logs
		WHERE ` + whereSQL
	if err := s.db.QueryRow(query, whereArgs...).Scan(
		&summary.RequestCount,
		&summary.SuccessRequest,
		&summary.TotalTokens,
		&avgTTFT,
		&avgDuration,
		&summary.StreamCount,
		&ttftSamples,
		&durationSamples,
	); err != nil {
		return OverviewSummary{}, err
	}
	sessionCount, err := s.overviewSessionCount(whereSQL, whereArgs)
	if err != nil {
		return OverviewSummary{}, err
	}
	summary.SessionCount = sessionCount
	summary.FailedRequest = summary.RequestCount - summary.SuccessRequest
	if summary.RequestCount > 0 {
		summary.SuccessRate = 100.0 * float64(summary.SuccessRequest) / float64(summary.RequestCount)
	}
	summary.AvgTTFTMs = int(math.Round(avgTTFT))
	summary.AvgDurationMs = int64(math.Round(avgDuration))
	if summary.RequestCount > 0 {
		p95TTFT, err := s.overviewPercentile("ttft_ms", whereSQL, whereArgs, 0.95, ttftSamples)
		if err != nil {
			return OverviewSummary{}, err
		}
		p95Duration, err := s.overviewPercentile("duration_ms", whereSQL, whereArgs, 0.95, durationSamples)
		if err != nil {
			return OverviewSummary{}, err
		}
		summary.P95TTFTMs = int(p95TTFT)
		summary.P95DurationMs = p95Duration
	}
	return summary, nil
}

// overviewPercentile returns the value at the given percentile among the rows the
// window matched with a positive value for the column. count is that number of
// samples, which the caller already computed in its own aggregate pass; the
// lookup itself is the only query left.

func (s *Store) overviewTimeline(whereSQL string, whereArgs []any, opts OverviewOptions) ([]OverviewTimelineItem, error) {
	referenceTime := time.Now().UTC()
	var latestRecordedAt any
	if err := s.db.QueryRow(`SELECT MAX(recorded_at) FROM logs WHERE `+whereSQL, whereArgs...).Scan(&latestRecordedAt); err != nil {
		return nil, err
	}
	if latestTime, err := timeParseNullableValue(latestRecordedAt); err != nil {
		return nil, err
	} else if !latestTime.IsZero() {
		referenceTime = latestTime
	}
	bucketStart := bucketSlot(referenceTime, opts.BucketSize, opts.Location).Add(-time.Duration(opts.BucketCount-1) * opts.BucketSize)
	queryArgs := append([]any{bucketStart.Format(timeLayout)}, whereArgs...)
	rows, err := s.db.Query(`
		SELECT recorded_at, status_code, error_text, total_tokens, ttft_ms, duration_ms
		FROM logs
		WHERE recorded_at >= ? AND `+whereSQL+`
		ORDER BY recorded_at ASC
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type bucketAccumulator struct {
		requestCount  int
		failedCount   int
		totalTokens   int
		ttftSum       int64
		ttftCount     int
		durationSum   int64
		durationCount int
	}
	buckets := make(map[time.Time]*bucketAccumulator, opts.BucketCount)
	for index := 0; index < opts.BucketCount; index++ {
		bucketTime := bucketStart.Add(time.Duration(index) * opts.BucketSize)
		buckets[bucketTime] = &bucketAccumulator{}
	}
	for rows.Next() {
		var (
			// Scanned as any: the Postgres driver hands back a time.Time for
			// timestamptz, so timeParseValue reads it directly instead of the
			// driver formatting it to text and timeParse parsing it back.
			recordedAt  any
			statusCode  int
			errorText   string
			totalTokens int
			ttftMs      int64
			durationMs  int64
		)
		if err := rows.Scan(&recordedAt, &statusCode, &errorText, &totalTokens, &ttftMs, &durationMs); err != nil {
			return nil, err
		}
		recorded, err := timeParseValue(recordedAt)
		if err != nil {
			return nil, err
		}
		if recorded.IsZero() {
			return nil, errors.New("overview timeline: log row has an empty recorded_at")
		}
		bucketTime := bucketSlot(recorded, opts.BucketSize, opts.Location)
		bucket, ok := buckets[bucketTime]
		if !ok {
			continue
		}
		bucket.requestCount++
		if statusCode < 200 || statusCode >= 300 || strings.TrimSpace(errorText) != "" {
			bucket.failedCount++
		}
		bucket.totalTokens += totalTokens
		if ttftMs > 0 {
			bucket.ttftSum += ttftMs
			bucket.ttftCount++
		}
		if durationMs > 0 {
			bucket.durationSum += durationMs
			bucket.durationCount++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]OverviewTimelineItem, 0, opts.BucketCount)
	for index := 0; index < opts.BucketCount; index++ {
		bucketTime := bucketStart.Add(time.Duration(index) * opts.BucketSize)
		bucket := buckets[bucketTime]
		item := OverviewTimelineItem{
			Time:          bucketTime,
			RequestCount:  bucket.requestCount,
			FailedRequest: bucket.failedCount,
			TotalTokens:   bucket.totalTokens,
		}
		if bucket.ttftCount > 0 {
			item.AvgTTFTMs = int(math.Round(float64(bucket.ttftSum) / float64(bucket.ttftCount)))
		}
		if bucket.durationCount > 0 {
			item.AvgDurationMs = int64(math.Round(float64(bucket.durationSum) / float64(bucket.durationCount)))
		}
		out = append(out, item)
	}
	return out, nil
}

// overviewBreakdownColumns are the log columns the Overview renders as top-N
// distributions, in the order the dashboard groups them.
var overviewBreakdownColumns = []string{"model", "provider", "endpoint", "selected_upstream_id", "routing_failure_reason"}

func (s *Store) overviewBreakdown(whereSQL string, whereArgs []any, limit int) (OverviewBreakdown, error) {
	distributions, err := s.overviewDistributions(overviewBreakdownColumns, whereSQL, whereArgs, limit)
	if err != nil {
		return OverviewBreakdown{}, err
	}
	out := OverviewBreakdown{
		Models:                distributions["model"],
		Providers:             distributions["provider"],
		Endpoints:             distributions["endpoint"],
		Upstreams:             distributions["selected_upstream_id"],
		RoutingFailureReasons: distributions["routing_failure_reason"],
	}
	if out.FindingCategories, err = s.overviewFindingCategories(limit); err != nil {
		return OverviewBreakdown{}, err
	}
	return out, nil
}

// overviewDistributions returns the top-N value counts for each column.
//
// Postgres answers all of them from one scan of logs with GROUPING SETS: the five
// separate GROUP BY queries each read the whole window, so on the reference
// deployment (251k client-visible rows, a 223 MB heap) they cost 285-320 ms each
// for 1238 ms in total, against 599 ms for the single grouped pass. The other
// drivers keep the per-column loop, because GROUPING SETS is Postgres-only.
//
// Every column here is NOT NULL with a ” default, so a NULL in the result set
// can only mean "this column is not part of the grouping set the row belongs to",
// which is what the row scan below relies on to route each row to its dimension.
func (s *Store) overviewDistributions(columns []string, whereSQL string, whereArgs []any, limit int) (map[string][]CountItem, error) {
	if s.driver != "postgres" || len(columns) == 0 {
		out := make(map[string][]CountItem, len(columns))
		for _, column := range columns {
			items, err := s.overviewCountBy(column, whereSQL, whereArgs, limit)
			if err != nil {
				return nil, err
			}
			out[column] = items
		}
		return out, nil
	}

	selectColumns := strings.Join(columns, ", ")
	sets := make([]string, 0, len(columns))
	for _, column := range columns {
		sets = append(sets, "("+column+")")
	}
	rows, err := s.db.Query(`
		SELECT `+selectColumns+`, COUNT(*) AS count
		FROM logs
		WHERE `+whereSQL+`
		GROUP BY GROUPING SETS (`+strings.Join(sets, ", ")+`)
	`, whereArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := make(map[string]map[string]int, len(columns))
	for rows.Next() {
		values := make([]any, len(columns)+1)
		scanned := make([]sql.NullString, len(columns))
		for index := range scanned {
			values[index] = &scanned[index]
		}
		var count int
		values[len(columns)] = &count
		if err := rows.Scan(values...); err != nil {
			return nil, err
		}
		// Exactly one column is non-NULL per row: the one this grouping set
		// groups by.
		for index, value := range scanned {
			if !value.Valid {
				continue
			}
			label := strings.TrimSpace(value.String)
			if label == "" {
				// The per-column queries exclude '' values, so a set that only
				// saw blanks contributes nothing.
				break
			}
			if counts[columns[index]] == nil {
				counts[columns[index]] = make(map[string]int)
			}
			counts[columns[index]][label] += count
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(map[string][]CountItem, len(columns))
	for _, column := range columns {
		out[column] = rankCountItems(counts[column], limit)
	}
	return out, nil
}

// rankCountItems turns a value->count map into the (count DESC, label ASC) top-N
// slice that overviewCountBy returns, so both read paths answer identically.
func rankCountItems(counts map[string]int, limit int) []CountItem {
	items := make([]CountItem, 0, len(counts))
	for label, count := range counts {
		items = append(items, CountItem{Label: label, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Label < items[j].Label
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// overviewSessionCount counts the distinct non-empty session ids in the window as
// its own statement. See overviewSummary for why it is not part of that pass.
func (s *Store) overviewSessionCount(whereSQL string, whereArgs []any) (int, error) {
	var count int
	if err := s.db.QueryRow(`
		SELECT COUNT(DISTINCT session_id)
		FROM logs
		WHERE `+whereSQL+` AND session_id != ''
	`, whereArgs...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) overviewCountBy(column string, whereSQL string, whereArgs []any, limit int) ([]CountItem, error) {
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, limit)
	rows, err := s.db.Query(`
		SELECT `+column+`, COUNT(*) AS count
		FROM logs
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

func (s *Store) overviewFindingCategories(limit int) ([]CountItem, error) {
	rows, err := s.db.Query(`
		SELECT category, COUNT(*) AS count
		FROM trace_findings
		WHERE category != ''
		GROUP BY category
		ORDER BY count DESC, category ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCountItems(rows)
}

func (s *Store) overviewAttention(whereSQL string, whereArgs []any, limit int) (OverviewAttention, error) {
	var out OverviewAttention
	var err error
	if out.RecentFailures, err = s.overviewTraceList(whereSQL+` AND (status_code < 200 OR status_code >= 300 OR error_text != '')`, whereArgs, `recorded_at DESC, trace_id DESC`, limit); err != nil {
		return OverviewAttention{}, err
	}
	if out.HighRiskFindings, err = s.overviewHighRiskFindings(limit); err != nil {
		return OverviewAttention{}, err
	}
	if out.RoutingFailures, err = s.overviewRoutingFailures(whereSQL+` AND routing_failure_reason != ''`, whereArgs, limit); err != nil {
		return OverviewAttention{}, err
	}
	if out.SlowTraces, err = s.overviewTraceList(whereSQL, whereArgs, `duration_ms DESC, recorded_at DESC, trace_id DESC`, limit); err != nil {
		return OverviewAttention{}, err
	}
	return out, nil
}

func (s *Store) overviewTraceList(whereSQL string, whereArgs []any, orderBy string, limit int) ([]LogEntry, error) {
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, limit)
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
		WHERE `+whereSQL+`
		ORDER BY `+orderBy+`
		LIMIT ?
	`, queryArgs...)
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
	return entries, rows.Err()
}

// overviewHighRiskFindings returns the newest critical findings followed by the
// newest high ones, up to limit in total.
//
// It is two single-severity lookups rather than one `WHERE severity IN
// ('critical', 'high') ORDER BY CASE severity ...` query. The CASE expression
// could not drive an index, so the combined form read the whole of
// trace_findings (341,161 rows, 272 MB) and sorted it for every Overview render.
// Each half here is a plain `severity = ?` filter ordered by the same
// (created_at DESC, id DESC) pair that `tracefinding_severity_created_at_id`
// indexes, so each stops after the rows it actually returns.
func (s *Store) overviewHighRiskFindings(limit int) ([]observe.Finding, error) {
	if limit <= 0 {
		return nil, nil
	}
	const why = `
		SELECT finding_id, trace_id, category, severity, confidence, title, description,
			evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
		FROM trace_findings
		WHERE severity = ?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`
	out := make([]observe.Finding, 0, limit)
	for _, severity := range []string{"critical", "high"} {
		remaining := limit - len(out)
		if remaining <= 0 {
			break
		}
		rows, err := s.db.Query(why, severity, remaining)
		if err != nil {
			return nil, err
		}
		findings, err := scanFindings(rows)
		closeErr := rows.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		out = append(out, findings...)
	}
	return out, nil
}

func (s *Store) overviewRoutingFailures(whereSQL string, whereArgs []any, limit int) ([]RoutingFailureRecord, error) {
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, limit)
	rows, err := s.db.Query(`
		SELECT trace_id, model, endpoint, recorded_at, routing_failure_reason, error_text, status_code
		FROM logs
		WHERE `+whereSQL+`
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RoutingFailureRecord
	for rows.Next() {
		var record RoutingFailureRecord
		var recordedAt string
		if err := rows.Scan(&record.TraceID, &record.Model, &record.Endpoint, &recordedAt, &record.Reason, &record.ErrorText, &record.StatusCode); err != nil {
			return nil, err
		}
		var err error
		record.RecordedAt, err = timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) overviewAnalysis(limit int) (OverviewAnalysisSummary, error) {
	var summary OverviewAnalysisSummary
	if err := s.db.QueryRow(`
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status NOT IN ('completed', 'success') THEN 1 ELSE 0 END), 0) AS failed
		FROM analysis_runs
	`).Scan(&summary.Total, &summary.Failed); err != nil {
		return OverviewAnalysisSummary{}, err
	}
	runs, err := s.ListAnalysisRuns("", "", "", limit)
	if err != nil {
		return OverviewAnalysisSummary{}, err
	}
	summary.Recent = runs
	return summary, nil
}

func (s *Store) overviewObservation(limit int) (OverviewObservationSummary, error) {
	var summary OverviewObservationSummary
	// One statement for the six counts; each scalar subquery is the query that
	// served the field on its own. The unparsed count is the expensive one: it
	// anti-joins logs against trace_observations, and the planner already picks
	// the hash anti join for it (see docs/POSTGRES_OPERATIONS.md).
	if err := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM trace_observations),
			(SELECT COALESCE(SUM(CASE WHEN status = 'parsed' THEN 1 ELSE 0 END), 0) FROM trace_observations),
			(SELECT COUNT(DISTINCT trace_id) FROM (
				SELECT trace_id FROM trace_observations WHERE status IN ('failed', 'parse_failed', 'analysis_failed')
				UNION
				SELECT trace_id FROM parse_jobs WHERE status = 'failed'
			) AS failed_traces),
			(SELECT COUNT(*) FROM logs l WHERE NOT EXISTS (
				SELECT 1 FROM trace_observations o WHERE o.trace_id = l.trace_id
			)),
			(SELECT COUNT(*) FROM parse_jobs WHERE status = 'queued'),
			(SELECT COUNT(*) FROM parse_jobs WHERE status = 'running')
	`).Scan(
		&summary.TotalObservations,
		&summary.Parsed,
		&summary.Failed,
		&summary.Unparsed,
		&summary.Queued,
		&summary.Running,
	); err != nil {
		return OverviewObservationSummary{}, err
	}
	jobs, err := s.overviewRecentParseFailures(limit)
	if err != nil {
		return OverviewObservationSummary{}, err
	}
	summary.RecentFailures = jobs
	return summary, nil
}

func (s *Store) overviewRecentParseFailures(limit int) ([]ParseJobRecord, error) {
	rows, err := s.db.Query(`
		SELECT id, trace_id, status, attempts, last_error, created_at, updated_at
		FROM parse_jobs
		WHERE status = 'failed'
		ORDER BY updated_at DESC, id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanParseJobs(rows)
}

func overviewLogWhere(since time.Time) (string, []any) {
	visible := clientVisibleLogClause("")
	if since.IsZero() {
		return visible, nil
	}
	return andSQL("recorded_at >= ?", visible), []any{since.UTC().Format(timeLayout)}
}
