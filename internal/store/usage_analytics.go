package store

// Usage and analytics aggregation helpers.
//
// These read the indexed `logs` rows and fold them into the per-window summaries and per-bucket
// timelines the Monitor serves. They were split out of store.go, which had grown past 4.9k lines; the
// code is unchanged apart from naming the UTC boundary explicitly (see the bucket derivation below),
// because every recorded_at comparison in this file is against UTC.

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func (s *Store) boolCountCaseSQL(column string) string {
	if s != nil && s.driver == "postgres" {
		return "CASE WHEN " + column + " THEN 1 ELSE 0 END"
	}
	return "CASE WHEN " + column + " = 1 THEN 1 ELSE 0 END"
}

func (s *Store) listLogModels(since time.Time) ([]string, error) {
	where := "model <> '' AND LOWER(model) <> 'list_models'"
	args := []any{}
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	rows, err := s.db.Query(`SELECT DISTINCT LOWER(model) FROM logs WHERE `+where+` ORDER BY LOWER(model)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		if model = strings.TrimSpace(model); model != "" {
			out = append(out, model)
		}
	}
	return out, rows.Err()
}

// usageSummaryAggregateColumns is the aggregate projection behind a usage
// summary. The single-key and the grouped form below share it so that one can
// stand in for the other without drifting.
const usageSummaryAggregateColumns = `
		COUNT(*) AS request_count,
		COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
		COALESCE(SUM(CASE WHEN status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
		CASE WHEN COUNT(*) = 0 THEN 0 ELSE 100.0 * SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*) END AS success_rate,
		COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 AND total_tokens = 0 AND prompt_tokens = 0 AND completion_tokens = 0 THEN 1 ELSE 0 END), 0) AS missing_usage_request,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
		COALESCE(AVG(ttft_ms), 0) AS avg_ttft,
		COALESCE(AVG(duration_ms), 0) AS avg_duration_ms,
		MAX(recorded_at) AS last_seen`

// usageSummaryRecordFromAggregate finishes one aggregate row. A group the query
// did not return and a single-key query that matched nothing both stay zero.
func usageSummaryRecordFromAggregate(record UsageSummaryRecord, successRate float64, avgTTFT float64, avgDuration float64, lastSeenValue any) (UsageSummaryRecord, error) {
	record.SuccessRate = successRate
	record.AvgTTFT = int(math.Round(avgTTFT))
	record.AvgDurationMs = int64(math.Round(avgDuration))
	if lastSeen, err := timeParseNullableValue(lastSeenValue); err != nil {
		return UsageSummaryRecord{}, err
	} else if !lastSeen.IsZero() {
		record.LastSeen = lastSeen
	}
	return record, nil
}

func (s *Store) usageSummary(baseWhere string, baseArgs []any, since time.Time) (UsageSummaryRecord, error) {
	where := strings.TrimSpace(baseWhere)
	if where == "" {
		where = "1=1"
	}
	args := append([]any(nil), baseArgs...)
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	var (
		record        UsageSummaryRecord
		successRate   float64
		avgTTFT       float64
		avgDuration   float64
		lastSeenValue any
	)
	if err := s.db.QueryRow(`
		SELECT`+usageSummaryAggregateColumns+`
		FROM logs
		WHERE `+where, args...).Scan(
		&record.RequestCount,
		&record.SuccessRequest,
		&record.FailedRequest,
		&successRate,
		&record.MissingUsage,
		&record.TotalTokens,
		&record.PromptTokens,
		&record.CompletionTokens,
		&record.CachedTokens,
		&avgTTFT,
		&avgDuration,
		&lastSeenValue,
	); err != nil {
		return UsageSummaryRecord{}, err
	}
	return usageSummaryRecordFromAggregate(record, successRate, avgTTFT, avgDuration, lastSeenValue)
}

// usageSummaryGroupKeys is the closed set of columns a grouped usage summary may
// group by. The key is interpolated into the query text, so it is validated
// against this map instead of being taken from a caller.
var usageSummaryGroupKeys = map[string]struct{}{
	"model":                {},
	"selected_upstream_id": {},
}

// usageSummariesByGroupKey computes the same aggregate as usageSummary for one
// equality clause on groupKey, for every group in one pass, keyed by the raw
// column value. The model catalog and the channel list otherwise run this
// aggregate once per entry (twice per model, once per channel); a group the map
// does not contain is the zero summary a no-match single-key query returned.
func (s *Store) usageSummariesByGroupKey(groupKey string, since time.Time) (map[string]UsageSummaryRecord, error) {
	if _, ok := usageSummaryGroupKeys[groupKey]; !ok {
		return nil, fmt.Errorf("unsupported usage summary group key %q", groupKey)
	}
	where := "1=1"
	var args []any
	if !since.IsZero() {
		where = "recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	rows, err := s.db.Query(`
		SELECT `+groupKey+`,`+usageSummaryAggregateColumns+`
		FROM logs
		WHERE `+where+`
		GROUP BY `+groupKey+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summaries := map[string]UsageSummaryRecord{}
	for rows.Next() {
		var (
			group         string
			record        UsageSummaryRecord
			successRate   float64
			avgTTFT       float64
			avgDuration   float64
			lastSeenValue any
		)
		if err := rows.Scan(
			&group,
			&record.RequestCount,
			&record.SuccessRequest,
			&record.FailedRequest,
			&successRate,
			&record.MissingUsage,
			&record.TotalTokens,
			&record.PromptTokens,
			&record.CompletionTokens,
			&record.CachedTokens,
			&avgTTFT,
			&avgDuration,
			&lastSeenValue,
		); err != nil {
			return nil, err
		}
		converted, err := usageSummaryRecordFromAggregate(record, successRate, avgTTFT, avgDuration, lastSeenValue)
		if err != nil {
			return nil, err
		}
		summaries[group] = converted
	}
	return summaries, rows.Err()
}

func (s *Store) usageSummariesByModel(since time.Time) (map[string]UsageSummaryRecord, error) {
	return s.usageSummariesByGroupKey("model", since)
}

func (s *Store) usageTrends(baseWhere string, baseArgs []any, since time.Time, bucketSize time.Duration, bucketCount int, loc *time.Location) ([]UsageTrendRecord, error) {
	if bucketSize <= 0 {
		bucketSize = 24 * time.Hour
	}
	if bucketCount <= 0 {
		bucketCount = 7
	}
	where := strings.TrimSpace(baseWhere)
	if where == "" {
		where = "1=1"
	}
	args := append([]any(nil), baseArgs...)
	referenceTime := time.Now().UTC()
	var latestRecordedAt any
	if err := s.db.QueryRow(`SELECT MAX(recorded_at) FROM logs WHERE `+where, args...).Scan(&latestRecordedAt); err != nil {
		return nil, err
	}
	if latestTime, err := timeParseNullableValue(latestRecordedAt); err != nil {
		return nil, err
	} else if !latestTime.IsZero() {
		referenceTime = latestTime.UTC()
	}
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	// The grid and the per-row lookups both go through bucketSlot, which returns UTC
	// instants: a time.Time map key carries its location, so aligning only one side
	// would leave every lookup missing and return an all-zero timeline.
	bucketStart := bucketSlot(referenceTime, bucketSize, loc).Add(-time.Duration(bucketCount-1) * bucketSize)
	queryArgs := append([]any(nil), args...)
	queryArgs = append(queryArgs, bucketStart.Format(timeLayout))
	rows, err := s.db.Query(`
		SELECT recorded_at, status_code, total_tokens, prompt_tokens, completion_tokens, model
		FROM logs
		WHERE `+where+` AND recorded_at >= ?
		ORDER BY recorded_at ASC
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type bucket struct {
		requests int
		failed   int
		missing  int
		tokens   int
		models   map[string]struct{}
	}
	buckets := make(map[time.Time]*bucket, bucketCount)
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		buckets[slot] = &bucket{models: map[string]struct{}{}}
	}
	for rows.Next() {
		var (
			recordedAt  string
			statusCode  int
			totalTokens int
			prompt      int
			completion  int
			model       string
		)
		if err := rows.Scan(&recordedAt, &statusCode, &totalTokens, &prompt, &completion, &model); err != nil {
			return nil, err
		}
		recordedTime, err := timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		slot := bucketSlot(recordedTime, bucketSize, loc)
		item := buckets[slot]
		if item == nil {
			continue
		}
		item.requests++
		if statusCode < 200 || statusCode >= 300 {
			item.failed++
		}
		if statusCode >= 200 && statusCode < 300 && totalTokens == 0 && prompt == 0 && completion == 0 {
			item.missing++
		}
		item.tokens += totalTokens
		if model = strings.TrimSpace(model); isUsageModelName(model) {
			item.models[strings.ToLower(model)] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]UsageTrendRecord, 0, bucketCount)
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		item := buckets[slot]
		out = append(out, UsageTrendRecord{
			Time:          slot,
			RequestCount:  item.requests,
			FailedRequest: item.failed,
			MissingUsage:  item.missing,
			TotalTokens:   item.tokens,
			ModelCount:    len(item.models),
		})
	}
	return out, nil
}

func sortedKeys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func isUsageModelName(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model != "" && model != "list_models"
}

func sortedCountItems(counts map[string]int) []CountItem {
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
	return items
}
