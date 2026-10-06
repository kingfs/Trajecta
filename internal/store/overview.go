package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/observe"
)

type overviewMetricContribution struct {
	Path              string
	BucketStart       time.Time
	BucketSizeSeconds int
	RequestCount      int64
	SuccessRequest    int64
	FailedRequest     int64
	TotalTokens       int64
	TTFTSum           int64
	TTFTCount         int64
	DurationSum       int64
	DurationCount     int64
	StreamCount       int64
}

type OverviewOptions struct {
	Since       time.Time
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

func (s *Store) ensureOverviewMetricBucketsSchema() error {
	timeType := "datetime"
	if s.driver == "postgres" {
		timeType = "timestamptz"
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS overview_metric_buckets (
		bucket_start %s NOT NULL,
		bucket_size_seconds INTEGER NOT NULL,
		request_count BIGINT NOT NULL DEFAULT 0,
		success_request BIGINT NOT NULL DEFAULT 0,
		failed_request BIGINT NOT NULL DEFAULT 0,
		total_tokens BIGINT NOT NULL DEFAULT 0,
		ttft_sum BIGINT NOT NULL DEFAULT 0,
		ttft_count BIGINT NOT NULL DEFAULT 0,
		duration_sum BIGINT NOT NULL DEFAULT 0,
		duration_count BIGINT NOT NULL DEFAULT 0,
		stream_count BIGINT NOT NULL DEFAULT 0,
		updated_at %s NOT NULL,
		PRIMARY KEY (bucket_start, bucket_size_seconds)
	)`, timeType, timeType)); err != nil {
		return err
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS overview_metric_bucket_members (
		path TEXT PRIMARY KEY,
		bucket_start %s NOT NULL,
		bucket_size_seconds INTEGER NOT NULL,
		request_count BIGINT NOT NULL DEFAULT 0,
		success_request BIGINT NOT NULL DEFAULT 0,
		failed_request BIGINT NOT NULL DEFAULT 0,
		total_tokens BIGINT NOT NULL DEFAULT 0,
		ttft_sum BIGINT NOT NULL DEFAULT 0,
		ttft_count BIGINT NOT NULL DEFAULT 0,
		duration_sum BIGINT NOT NULL DEFAULT 0,
		duration_count BIGINT NOT NULL DEFAULT 0,
		stream_count BIGINT NOT NULL DEFAULT 0,
		updated_at %s NOT NULL
	)`, timeType, timeType)); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_overview_metric_buckets_start ON overview_metric_buckets(bucket_start)`,
		`CREATE INDEX IF NOT EXISTS idx_overview_metric_bucket_members_bucket ON overview_metric_bucket_members(bucket_start, bucket_size_seconds)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) refreshOverviewMetricBucketsBestEffort(paths []string) {
	if len(paths) == 0 {
		return
	}
	if err := s.refreshOverviewMetricBuckets(paths); err != nil {
		fmt.Fprintf(os.Stderr, "trajecta: refresh overview metric buckets failed: %v\n", err)
	}
}

// refreshOverviewMetricBuckets rebuilds the hourly bucket contribution of a set
// of paths in one transaction. Path by path costs a contribution read, a member
// read, a member delete and two upserts each in its own transaction; the batched
// form reads both sides once and writes one aggregated delta per distinct bucket,
// with the member rows deleted and re-inserted in parameter-bounded chunks.

// addOverviewMetricContributionDelta accumulates one bucket delta. A net-zero
// delta writes nothing: the previous implementation added the removed member and
// the new contribution as two separate upserts, which left a zero-valued row
// behind when a path ended up in the same bucket with the same numbers.

func overviewMetricBucketKey(contribution overviewMetricContribution) string {
	return contribution.BucketStart.UTC().Format(timeLayout) + "|" + strconv.Itoa(contribution.BucketSizeSeconds)
}

func (s *Store) insertOverviewMetricMembersTx(tx *sql.Tx, contributions map[string]overviewMetricContribution) error {
	if len(contributions) == 0 {
		return nil
	}
	now := time.Now().UTC().Format(timeLayout)
	const memberColumns = 13
	rowsPerStatement := storeSQLParamChunk / memberColumns
	valueRow := "(" + placeholders(memberColumns) + ")"
	for _, chunk := range chunkStrings(sortedKeys(contributions), rowsPerStatement) {
		values := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*memberColumns)
		for _, path := range chunk {
			contribution := contributions[path]
			values = append(values, valueRow)
			args = append(args,
				contribution.Path,
				contribution.BucketStart.UTC().Format(timeLayout),
				contribution.BucketSizeSeconds,
				contribution.RequestCount,
				contribution.SuccessRequest,
				contribution.FailedRequest,
				contribution.TotalTokens,
				contribution.TTFTSum,
				contribution.TTFTCount,
				contribution.DurationSum,
				contribution.DurationCount,
				contribution.StreamCount,
				now,
			)
		}
		if _, err := s.execTx(tx, `
			INSERT INTO overview_metric_bucket_members (
				path, bucket_start, bucket_size_seconds, request_count, success_request, failed_request,
				total_tokens, ttft_sum, ttft_count, duration_sum, duration_count, stream_count, updated_at
			) VALUES `+strings.Join(values, ", "), args...); err != nil {
			return err
		}
	}
	return nil
}

// storeSQLParamChunk bounds how many bind parameters one statement carries.
// Postgres allows 65535 and current SQLite builds allow 32766, but the historic
// SQLite limit of 999 is the only value that is safe for every driver and
// version the project supports.
const storeSQLParamChunk = 900

func (s *Store) RefreshOverviewMetricBucketForPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	contribution, ok, err := s.overviewMetricContributionForPath(path)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.applyOverviewMetricContributionTx(tx, path, contribution, ok); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RebuildOverviewMetricBuckets() error {
	rows, err := s.db.Query(`
		SELECT path, recorded_at, status_code, error_text, total_tokens, ttft_ms, duration_ms, is_stream
		FROM logs
		WHERE ` + clientVisibleLogClause("") + `
		ORDER BY recorded_at ASC, path ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var contributions []overviewMetricContribution
	for rows.Next() {
		contribution, err := s.scanOverviewMetricContribution(rows)
		if err != nil {
			return err
		}
		contributions = append(contributions, contribution)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.execTx(tx, `DELETE FROM overview_metric_bucket_members`); err != nil {
		return err
	}
	if _, err := s.execTx(tx, `DELETE FROM overview_metric_buckets`); err != nil {
		return err
	}
	for _, contribution := range contributions {
		if err := s.insertOverviewMetricContributionTx(tx, contribution); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) overviewMetricContributionForPath(path string) (overviewMetricContribution, bool, error) {
	row := s.db.QueryRow(`
		SELECT path, recorded_at, status_code, error_text, total_tokens, ttft_ms, duration_ms, is_stream
		FROM logs
		WHERE path = ? AND `+clientVisibleLogClause("")+`
	`, path)
	contribution, err := s.scanOverviewMetricContribution(row)
	if errors.Is(err, sql.ErrNoRows) {
		return overviewMetricContribution{}, false, nil
	}
	if err != nil {
		return overviewMetricContribution{}, false, err
	}
	return contribution, true, nil
}

type overviewMetricContributionScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanOverviewMetricContribution(scanner overviewMetricContributionScanner) (overviewMetricContribution, error) {
	var (
		contribution overviewMetricContribution
		recordedAt   any
		statusCode   int
		errorText    string
		totalTokens  int64
		ttftMs       int64
		durationMs   int64
		isStream     any
	)
	if err := scanner.Scan(
		&contribution.Path,
		&recordedAt,
		&statusCode,
		&errorText,
		&totalTokens,
		&ttftMs,
		&durationMs,
		&isStream,
	); err != nil {
		return overviewMetricContribution{}, err
	}
	recorded, err := timeParseValue(recordedAt)
	if err != nil {
		return overviewMetricContribution{}, err
	}
	contribution.BucketStart = recorded.UTC().Truncate(overviewMetricBucketSize)
	contribution.BucketSizeSeconds = int(overviewMetricBucketSize / time.Second)
	contribution.RequestCount = 1
	if statusCode >= 200 && statusCode < 300 && strings.TrimSpace(errorText) == "" {
		contribution.SuccessRequest = 1
	} else {
		contribution.FailedRequest = 1
	}
	contribution.TotalTokens = totalTokens
	if ttftMs > 0 {
		contribution.TTFTSum = ttftMs
		contribution.TTFTCount = 1
	}
	if durationMs > 0 {
		contribution.DurationSum = durationMs
		contribution.DurationCount = 1
	}
	if boolValue(isStream) {
		contribution.StreamCount = 1
	}
	return contribution, nil
}

func (s *Store) applyOverviewMetricContributionTx(tx *sql.Tx, path string, contribution overviewMetricContribution, hasContribution bool) error {
	previous, ok, err := s.overviewMetricMemberTx(tx, path)
	if err != nil {
		return err
	}
	if ok {
		previous.RequestCount = -previous.RequestCount
		previous.SuccessRequest = -previous.SuccessRequest
		previous.FailedRequest = -previous.FailedRequest
		previous.TotalTokens = -previous.TotalTokens
		previous.TTFTSum = -previous.TTFTSum
		previous.TTFTCount = -previous.TTFTCount
		previous.DurationSum = -previous.DurationSum
		previous.DurationCount = -previous.DurationCount
		previous.StreamCount = -previous.StreamCount
		if err := s.addOverviewMetricBucketTx(tx, previous); err != nil {
			return err
		}
		if _, err := s.execTx(tx, `DELETE FROM overview_metric_bucket_members WHERE path = ?`, path); err != nil {
			return err
		}
	}
	if !hasContribution {
		return nil
	}
	return s.insertOverviewMetricContributionTx(tx, contribution)
}

func (s *Store) overviewMetricMemberTx(tx *sql.Tx, path string) (overviewMetricContribution, bool, error) {
	var contribution overviewMetricContribution
	var bucketStart any
	err := tx.QueryRow(s.db.rebind(`
		SELECT path, bucket_start, bucket_size_seconds, request_count, success_request, failed_request,
			total_tokens, ttft_sum, ttft_count, duration_sum, duration_count, stream_count
		FROM overview_metric_bucket_members
		WHERE path = ?
	`), path).Scan(
		&contribution.Path,
		&bucketStart,
		&contribution.BucketSizeSeconds,
		&contribution.RequestCount,
		&contribution.SuccessRequest,
		&contribution.FailedRequest,
		&contribution.TotalTokens,
		&contribution.TTFTSum,
		&contribution.TTFTCount,
		&contribution.DurationSum,
		&contribution.DurationCount,
		&contribution.StreamCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return overviewMetricContribution{}, false, nil
	}
	if err != nil {
		return overviewMetricContribution{}, false, err
	}
	parsed, err := timeParseValue(bucketStart)
	if err != nil {
		return overviewMetricContribution{}, false, err
	}
	contribution.BucketStart = parsed.UTC()
	return contribution, true, nil
}

func (s *Store) insertOverviewMetricContributionTx(tx *sql.Tx, contribution overviewMetricContribution) error {
	now := time.Now().UTC().Format(timeLayout)
	if _, err := s.execTx(tx, `
		INSERT INTO overview_metric_bucket_members (
			path, bucket_start, bucket_size_seconds, request_count, success_request, failed_request,
			total_tokens, ttft_sum, ttft_count, duration_sum, duration_count, stream_count, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, contribution.Path,
		contribution.BucketStart.UTC().Format(timeLayout),
		contribution.BucketSizeSeconds,
		contribution.RequestCount,
		contribution.SuccessRequest,
		contribution.FailedRequest,
		contribution.TotalTokens,
		contribution.TTFTSum,
		contribution.TTFTCount,
		contribution.DurationSum,
		contribution.DurationCount,
		contribution.StreamCount,
		now,
	); err != nil {
		return err
	}
	return s.addOverviewMetricBucketTx(tx, contribution)
}

func (s *Store) addOverviewMetricBucketTx(tx *sql.Tx, contribution overviewMetricContribution) error {
	now := time.Now().UTC().Format(timeLayout)
	_, err := s.execTx(tx, `
		INSERT INTO overview_metric_buckets (
			bucket_start, bucket_size_seconds, request_count, success_request, failed_request,
			total_tokens, ttft_sum, ttft_count, duration_sum, duration_count, stream_count, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bucket_start, bucket_size_seconds) DO UPDATE SET
			request_count = overview_metric_buckets.request_count + excluded.request_count,
			success_request = overview_metric_buckets.success_request + excluded.success_request,
			failed_request = overview_metric_buckets.failed_request + excluded.failed_request,
			total_tokens = overview_metric_buckets.total_tokens + excluded.total_tokens,
			ttft_sum = overview_metric_buckets.ttft_sum + excluded.ttft_sum,
			ttft_count = overview_metric_buckets.ttft_count + excluded.ttft_count,
			duration_sum = overview_metric_buckets.duration_sum + excluded.duration_sum,
			duration_count = overview_metric_buckets.duration_count + excluded.duration_count,
			stream_count = overview_metric_buckets.stream_count + excluded.stream_count,
			updated_at = excluded.updated_at
	`, contribution.BucketStart.UTC().Format(timeLayout),
		contribution.BucketSizeSeconds,
		contribution.RequestCount,
		contribution.SuccessRequest,
		contribution.FailedRequest,
		contribution.TotalTokens,
		contribution.TTFTSum,
		contribution.TTFTCount,
		contribution.DurationSum,
		contribution.DurationCount,
		contribution.StreamCount,
		now,
	)
	return err
}

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
	var ttftSamples, durationSamples int
	query := `
		SELECT
			COUNT(*) AS request_count,
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 AND error_text = '' THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms END), 0) AS avg_ttft,
			COALESCE(AVG(CASE WHEN duration_ms > 0 THEN duration_ms END), 0) AS avg_duration,
			COALESCE(SUM(` + s.boolCountCaseSQL("is_stream") + `), 0) AS stream_count,
			COUNT(DISTINCT CASE WHEN session_id != '' THEN session_id END) AS session_count,
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
		&summary.SessionCount,
		&ttftSamples,
		&durationSamples,
	); err != nil {
		return OverviewSummary{}, err
	}
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
	bucketStart := referenceTime.UTC().Truncate(opts.BucketSize).Add(-time.Duration(opts.BucketCount-1) * opts.BucketSize)
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
		bucketTime := recorded.UTC().Truncate(opts.BucketSize)
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

func (s *Store) overviewBreakdown(whereSQL string, whereArgs []any, limit int) (OverviewBreakdown, error) {
	var out OverviewBreakdown
	var err error
	if out.Models, err = s.overviewCountBy("model", whereSQL, whereArgs, limit); err != nil {
		return OverviewBreakdown{}, err
	}
	if out.Providers, err = s.overviewCountBy("provider", whereSQL, whereArgs, limit); err != nil {
		return OverviewBreakdown{}, err
	}
	if out.Endpoints, err = s.overviewCountBy("endpoint", whereSQL, whereArgs, limit); err != nil {
		return OverviewBreakdown{}, err
	}
	if out.Upstreams, err = s.overviewCountBy("selected_upstream_id", whereSQL, whereArgs, limit); err != nil {
		return OverviewBreakdown{}, err
	}
	if out.RoutingFailureReasons, err = s.overviewCountBy("routing_failure_reason", whereSQL, whereArgs, limit); err != nil {
		return OverviewBreakdown{}, err
	}
	if out.FindingCategories, err = s.overviewFindingCategories(limit); err != nil {
		return OverviewBreakdown{}, err
	}
	return out, nil
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

func (s *Store) overviewHighRiskFindings(limit int) ([]observe.Finding, error) {
	rows, err := s.db.Query(`
		SELECT finding_id, trace_id, category, severity, confidence, title, description,
			evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
		FROM trace_findings
		WHERE severity IN ('critical', 'high')
		ORDER BY
			CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 ELSE 2 END,
			created_at DESC,
			id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
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
			(SELECT COALESCE(SUM(CASE WHEN status = 'queued' THEN 1 ELSE 0 END), 0) FROM parse_jobs),
			(SELECT COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0) FROM parse_jobs)
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
