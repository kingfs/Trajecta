// Read-side analytics over the indexed exchanges: the record types the monitor's
// analytics endpoints return, the queries that aggregate them, and the helpers those
// queries share. They are read-only and depend on nothing else in the package's write
// and migration paths, which is why they live apart from store.go.

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type UpstreamAnalyticsRecord struct {
	UpstreamID     string
	RequestCount   int
	SuccessRequest int
	FailedRequest  int
	SuccessRate    float64
	TotalTokens    int
	AvgTTFT        int
	LastSeen       time.Time
	Models         []string
	LastModel      string
	RecentErrors   []string
	RecentFailures []UpstreamFailureRecord
}

type RoutingFailureAnalytics struct {
	Total    int
	Reasons  []CountItem
	Recent   []RoutingFailureRecord
	Timeline []TimeCountItem
}

type ModelCatalogAnalyticsRecord struct {
	Model               string
	DisplayName         string
	ProviderCount       int
	ChannelCount        int
	EnabledChannelCount int
	Summary             UsageSummaryRecord
	Today               UsageSummaryRecord
	Channels            []string
}

type ModelDetailAnalyticsRecord struct {
	Model    ModelCatalogAnalyticsRecord
	Trends   []UsageTrendRecord
	Channels []ChannelModelAnalyticsRecord
}

type ChannelModelAnalyticsRecord struct {
	ChannelID string
	Model     string
	Enabled   bool
	Source    string
	Summary   UsageSummaryRecord
}

func (s *Store) ListModelCatalogAnalytics(since time.Time, todaySince time.Time) ([]ModelCatalogAnalyticsRecord, error) {
	modelSet := map[string]*ModelCatalogAnalyticsRecord{}
	channelModels, err := s.ListChannelModels("", false)
	if err != nil {
		return nil, err
	}
	providersByModel := map[string]map[string]struct{}{}
	channelsByModel := map[string]map[string]struct{}{}
	enabledChannelsByModel := map[string]map[string]struct{}{}
	for _, channelModel := range channelModels {
		model := strings.ToLower(strings.TrimSpace(channelModel.Model))
		if !isUsageModelName(model) {
			continue
		}
		record := modelSet[model]
		if record == nil {
			record = &ModelCatalogAnalyticsRecord{Model: model, DisplayName: channelModel.DisplayName}
			modelSet[model] = record
		}
		if providersByModel[model] == nil {
			providersByModel[model] = map[string]struct{}{}
			channelsByModel[model] = map[string]struct{}{}
			enabledChannelsByModel[model] = map[string]struct{}{}
		}
		channelsByModel[model][channelModel.ChannelID] = struct{}{}
		if channelModel.Enabled {
			enabledChannelsByModel[model][channelModel.ChannelID] = struct{}{}
		}
	}

	channels, err := s.ListChannelConfigs()
	if err != nil {
		return nil, err
	}
	providerByChannel := map[string]string{}
	for _, channel := range channels {
		providerByChannel[channel.ID] = channel.ProviderPreset
	}
	for model, channelIDs := range channelsByModel {
		for channelID := range channelIDs {
			if provider := providerByChannel[channelID]; provider != "" {
				providersByModel[model][provider] = struct{}{}
			}
		}
	}

	logModels, err := s.listLogModels(since)
	if err != nil {
		return nil, err
	}
	logModelChannels, err := s.logModelChannels(since)
	if err != nil {
		return nil, err
	}
	for _, model := range logModels {
		if !isUsageModelName(model) {
			continue
		}
		if modelSet[model] == nil {
			modelSet[model] = &ModelCatalogAnalyticsRecord{Model: model}
		}
		if providersByModel[model] == nil {
			providersByModel[model] = map[string]struct{}{}
			channelsByModel[model] = map[string]struct{}{}
			enabledChannelsByModel[model] = map[string]struct{}{}
		}
		for channelID := range logModelChannels[model] {
			channelsByModel[model][channelID] = struct{}{}
			if provider := providerByChannel[channelID]; provider != "" {
				providersByModel[model][provider] = struct{}{}
			}
		}
	}
	summariesByModel, err := s.usageSummariesByModel(since)
	if err != nil {
		return nil, err
	}
	todayByModel, err := s.usageSummariesByModel(todaySince)
	if err != nil {
		return nil, err
	}
	for _, record := range modelSet {
		record.Summary = summariesByModel[record.Model]
		record.Today = todayByModel[record.Model]
		record.ProviderCount = len(providersByModel[record.Model])
		record.ChannelCount = len(channelsByModel[record.Model])
		record.EnabledChannelCount = len(enabledChannelsByModel[record.Model])
		record.Channels = sortedKeys(channelsByModel[record.Model])
	}

	out := make([]ModelCatalogAnalyticsRecord, 0, len(modelSet))
	for _, record := range modelSet {
		out = append(out, *record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Summary.RequestCount != out[j].Summary.RequestCount {
			return out[i].Summary.RequestCount > out[j].Summary.RequestCount
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

func (s *Store) GetModelDetailAnalytics(model string, since time.Time, todaySince time.Time, bucketSize time.Duration, bucketCount int, loc *time.Location) (ModelDetailAnalyticsRecord, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ModelDetailAnalyticsRecord{}, errors.New("model is required")
	}
	all, err := s.ListModelCatalogAnalytics(since, todaySince)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	var detail ModelDetailAnalyticsRecord
	for _, item := range all {
		if item.Model == model {
			detail.Model = item
			break
		}
	}
	if detail.Model.Model == "" {
		return ModelDetailAnalyticsRecord{}, sql.ErrNoRows
	}
	trends, err := s.usageTrends("model = ?", []any{model}, since, bucketSize, bucketCount, loc)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	detail.Trends = trends

	channelModels, err := s.ListChannelModels("", false)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	seenChannels := map[string]struct{}{}
	for _, channelModel := range channelModels {
		if strings.ToLower(channelModel.Model) != model || !isUsageModelName(model) {
			continue
		}
		seenChannels[channelModel.ChannelID] = struct{}{}
		summary, err := s.usageSummary("model = ? AND selected_upstream_id = ?", []any{model, channelModel.ChannelID}, since)
		if err != nil {
			return ModelDetailAnalyticsRecord{}, err
		}
		detail.Channels = append(detail.Channels, ChannelModelAnalyticsRecord{
			ChannelID: channelModel.ChannelID,
			Model:     model,
			Enabled:   channelModel.Enabled,
			Source:    channelModel.Source,
			Summary:   summary,
		})
	}
	logChannels, err := s.modelLogChannels(model, since)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	for _, channelID := range logChannels {
		if _, ok := seenChannels[channelID]; ok {
			continue
		}
		summary, err := s.usageSummary("model = ? AND selected_upstream_id = ?", []any{model, channelID}, since)
		if err != nil {
			return ModelDetailAnalyticsRecord{}, err
		}
		detail.Channels = append(detail.Channels, ChannelModelAnalyticsRecord{
			ChannelID: channelID,
			Model:     model,
			Source:    "trace",
			Summary:   summary,
		})
	}
	sort.Slice(detail.Channels, func(i, j int) bool {
		if detail.Channels[i].Summary.RequestCount != detail.Channels[j].Summary.RequestCount {
			return detail.Channels[i].Summary.RequestCount > detail.Channels[j].Summary.RequestCount
		}
		return detail.Channels[i].ChannelID < detail.Channels[j].ChannelID
	})
	return detail, nil
}

func (s *Store) ListUpstreamAnalytics(limitModels int, limitErrors int, since time.Time, modelFilter string) ([]UpstreamAnalyticsRecord, error) {
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	rows, err := s.db.Query(`
		SELECT
			selected_upstream_id,
			COUNT(*) AS request_count,
			COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN status_code BETWEEN 200 AND 299 THEN ttft_ms END), 0) AS avg_ttft,
			MAX(recorded_at) AS last_seen
		FROM logs
		WHERE selected_upstream_id <> ''`+whereSQL+`
		GROUP BY selected_upstream_id
		ORDER BY request_count DESC, selected_upstream_id ASC
	`, whereArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// The per-upstream lists come from three grouped queries for the whole page
	// instead of four per upstream, which is what the loop below used to run.
	coverages, err := s.upstreamModelCoverageAll(limitModels, since, modelFilter)
	if err != nil {
		return nil, err
	}
	errorsByUpstream, err := s.upstreamRecentErrorsAll(limitErrors, since, modelFilter)
	if err != nil {
		return nil, err
	}
	failuresByUpstream, err := s.upstreamRecentFailuresAll(limitErrors, since, modelFilter)
	if err != nil {
		return nil, err
	}

	var out []UpstreamAnalyticsRecord
	for rows.Next() {
		var (
			record   UpstreamAnalyticsRecord
			lastSeen string
			avgTTFT  float64
		)
		if err := rows.Scan(
			&record.UpstreamID,
			&record.RequestCount,
			&record.SuccessRequest,
			&record.FailedRequest,
			&record.SuccessRate,
			&record.TotalTokens,
			&avgTTFT,
			&lastSeen,
		); err != nil {
			return nil, err
		}
		record.AvgTTFT = int(math.Round(avgTTFT))
		record.LastSeen, err = timeParse(lastSeen)
		if err != nil {
			return nil, err
		}
		coverage := coverages[record.UpstreamID]
		record.Models = coverage.Models
		record.LastModel = coverage.LastModel
		record.RecentErrors = errorsByUpstream[record.UpstreamID]
		record.RecentFailures = failuresByUpstream[record.UpstreamID]
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) GetRoutingFailureAnalytics(since time.Time, modelFilter string, limitReasons int, limitRecent int, bucketSize time.Duration, bucketCount int, loc *time.Location) (RoutingFailureAnalytics, error) {
	if limitReasons <= 0 {
		limitReasons = 5
	}
	if limitRecent <= 0 {
		limitRecent = 5
	}
	if bucketSize <= 0 {
		bucketSize = time.Hour
	}
	if bucketCount <= 0 {
		bucketCount = 12
	}

	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	baseWhere := `routing_failure_reason <> ''`
	if strings.TrimSpace(whereSQL) != "" {
		baseWhere += whereSQL
	}

	var analytics RoutingFailureAnalytics
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs WHERE `+baseWhere, whereArgs...).Scan(&analytics.Total); err != nil {
		return RoutingFailureAnalytics{}, err
	}

	reasonArgs := append([]any{}, whereArgs...)
	reasonArgs = append(reasonArgs, limitReasons)
	reasonRows, err := s.db.Query(`
		SELECT routing_failure_reason, COUNT(*) AS count
		FROM logs
		WHERE `+baseWhere+`
		GROUP BY routing_failure_reason
		ORDER BY count DESC, routing_failure_reason ASC
		LIMIT ?
	`, reasonArgs...)
	if err != nil {
		return RoutingFailureAnalytics{}, err
	}
	defer reasonRows.Close()
	for reasonRows.Next() {
		var item CountItem
		if err := reasonRows.Scan(&item.Label, &item.Count); err != nil {
			return RoutingFailureAnalytics{}, err
		}
		analytics.Reasons = append(analytics.Reasons, item)
	}
	if err := reasonRows.Err(); err != nil {
		return RoutingFailureAnalytics{}, err
	}

	recentArgs := append([]any{}, whereArgs...)
	recentArgs = append(recentArgs, limitRecent)
	recentRows, err := s.db.Query(`
		SELECT trace_id, model, endpoint, recorded_at, routing_failure_reason, error_text, status_code
		FROM logs
		WHERE `+baseWhere+`
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, recentArgs...)
	if err != nil {
		return RoutingFailureAnalytics{}, err
	}
	defer recentRows.Close()
	for recentRows.Next() {
		var (
			item       RoutingFailureRecord
			recordedAt string
		)
		if err := recentRows.Scan(&item.TraceID, &item.Model, &item.Endpoint, &recordedAt, &item.Reason, &item.ErrorText, &item.StatusCode); err != nil {
			return RoutingFailureAnalytics{}, err
		}
		item.RecordedAt, err = timeParse(recordedAt)
		if err != nil {
			return RoutingFailureAnalytics{}, err
		}
		analytics.Recent = append(analytics.Recent, item)
	}
	if err := recentRows.Err(); err != nil {
		return RoutingFailureAnalytics{}, err
	}

	referenceTime := time.Now().UTC()
	var latestRecordedAt any
	if err := s.db.QueryRow(`SELECT MAX(recorded_at) FROM logs WHERE `+baseWhere, whereArgs...).Scan(&latestRecordedAt); err != nil {
		return RoutingFailureAnalytics{}, err
	}
	if latestTime, err := timeParseNullableValue(latestRecordedAt); err != nil {
		return RoutingFailureAnalytics{}, err
	} else if !latestTime.IsZero() {
		referenceTime = latestTime
	}
	bucketStart := bucketSlot(referenceTime, bucketSize, loc).Add(-time.Duration(bucketCount-1) * bucketSize)
	timelineArgs := append([]any{bucketStart.Format(timeLayout)}, whereArgs...)
	timelineRows, err := s.db.Query(`
		SELECT recorded_at
		FROM logs
		WHERE recorded_at >= ? AND `+baseWhere+`
		ORDER BY recorded_at ASC
	`, timelineArgs...)
	if err != nil {
		return RoutingFailureAnalytics{}, err
	}
	defer timelineRows.Close()

	buckets := make(map[time.Time]int, bucketCount)
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		buckets[slot] = 0
	}
	for timelineRows.Next() {
		var recordedAt string
		if err := timelineRows.Scan(&recordedAt); err != nil {
			return RoutingFailureAnalytics{}, err
		}
		recordedTime, err := timeParse(recordedAt)
		if err != nil {
			return RoutingFailureAnalytics{}, err
		}
		slot := bucketSlot(recordedTime, bucketSize, loc)
		if slot.Before(bucketStart) {
			continue
		}
		if _, ok := buckets[slot]; ok {
			buckets[slot]++
		}
	}
	if err := timelineRows.Err(); err != nil {
		return RoutingFailureAnalytics{}, err
	}
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		analytics.Timeline = append(analytics.Timeline, TimeCountItem{
			Time:  slot,
			Count: buckets[slot],
		})
	}

	return analytics, nil
}

func (s *Store) upstreamModelCoverage(upstreamID string, limit int, since time.Time, modelFilter string) ([]string, string, error) {
	if limit <= 0 {
		limit = 5
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any{upstreamID}, whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT LOWER(model) AS model, COUNT(*) AS count
		FROM logs
		WHERE selected_upstream_id = ? AND model <> ''`+whereSQL+`
		GROUP BY LOWER(model)
		ORDER BY count DESC, model ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var models []string
	for rows.Next() {
		var model string
		var count int
		if err := rows.Scan(&model, &count); err != nil {
			return nil, "", err
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var lastModel string
	lastModelArgs := append([]any{upstreamID}, whereArgs...)
	if err := s.db.QueryRow(`
		SELECT LOWER(model)
		FROM logs
		WHERE selected_upstream_id = ? AND model <> ''`+whereSQL+`
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT 1
	`, lastModelArgs...).Scan(&lastModel); err != nil && err != sql.ErrNoRows {
		return nil, "", err
	}

	return models, lastModel, nil
}

func (s *Store) upstreamRecentErrors(upstreamID string, limit int, since time.Time, modelFilter string) ([]string, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any{upstreamID}, whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT error_text, status_code, endpoint
		FROM logs
		WHERE selected_upstream_id = ?
		  `+whereSQL+`
		  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var (
			errorText  string
			statusCode int
			endpoint   string
		)
		if err := rows.Scan(&errorText, &statusCode, &endpoint); err != nil {
			return nil, err
		}
		switch {
		case strings.TrimSpace(errorText) != "":
			out = append(out, errorText)
		case strings.TrimSpace(endpoint) != "":
			out = append(out, fmt.Sprintf("%s HTTP %d", endpoint, statusCode))
		default:
			out = append(out, fmt.Sprintf("HTTP %d", statusCode))
		}
	}
	return out, rows.Err()
}

func (s *Store) upstreamRecentFailures(upstreamID string, limit int, since time.Time, modelFilter string) ([]UpstreamFailureRecord, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any{upstreamID}, whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT trace_id, model, endpoint, status_code, recorded_at, error_text
		FROM logs
		WHERE selected_upstream_id = ?
		  `+whereSQL+`
		  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UpstreamFailureRecord
	for rows.Next() {
		var (
			record     UpstreamFailureRecord
			recordedAt string
		)
		if err := rows.Scan(&record.TraceID, &record.Model, &record.Endpoint, &record.StatusCode, &recordedAt, &record.ErrorText); err != nil {
			return nil, err
		}
		record.RecordedAt, err = timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		record.Reason = classifyUpstreamFailure(record.StatusCode, record.ErrorText)
		out = append(out, record)
	}
	return out, rows.Err()
}

// upstreamModelCoverageRecord is the per-upstream result of the batched model
// coverage query.
type upstreamModelCoverageRecord struct {
	Models    []string
	LastModel string
}

// upstreamModelCoverageAll computes upstreamModelCoverage for every upstream in
// two queries. The per-upstream form ran both of them once per upstream, and the
// analytics page calls it for every upstream it lists. Both queries rank inside
// the database so the model order and the top-N cut keep the collation the
// per-upstream query used.
func (s *Store) upstreamModelCoverageAll(limit int, since time.Time, modelFilter string) (map[string]upstreamModelCoverageRecord, error) {
	if limit <= 0 {
		limit = 5
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	out := map[string]upstreamModelCoverageRecord{}

	rankedArgs := append([]any(nil), whereArgs...)
	rankedArgs = append(rankedArgs, limit)
	rows, err := s.db.Query(`
		SELECT selected_upstream_id, model
		FROM (
			SELECT
				selected_upstream_id,
				LOWER(model) AS model,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY COUNT(*) DESC, LOWER(model) ASC
				) AS model_rank
			FROM logs
			WHERE selected_upstream_id <> '' AND model <> ''`+whereSQL+`
			GROUP BY selected_upstream_id, LOWER(model)
		) ranked
		WHERE model_rank <= ?
		ORDER BY selected_upstream_id, model_rank
	`, rankedArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			upstreamID string
			model      string
		)
		if err := rows.Scan(&upstreamID, &model); err != nil {
			rows.Close()
			return nil, err
		}
		record := out[upstreamID]
		record.Models = append(record.Models, model)
		out[upstreamID] = record
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	lastRows, err := s.db.Query(`
		SELECT selected_upstream_id, model
		FROM (
			SELECT
				selected_upstream_id,
				LOWER(model) AS model,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY recorded_at DESC, trace_id DESC
				) AS row_number
			FROM logs
			WHERE selected_upstream_id <> '' AND model <> ''`+whereSQL+`
		) ranked
		WHERE row_number = 1
	`, whereArgs...)
	if err != nil {
		return nil, err
	}
	defer lastRows.Close()
	for lastRows.Next() {
		var (
			upstreamID string
			model      string
		)
		if err := lastRows.Scan(&upstreamID, &model); err != nil {
			return nil, err
		}
		record := out[upstreamID]
		record.LastModel = model
		out[upstreamID] = record
	}
	return out, lastRows.Err()
}

// upstreamRecentErrorsAll computes upstreamRecentErrors for every upstream in one
// query, ranking the last limit rows of each upstream inside the database.
func (s *Store) upstreamRecentErrorsAll(limit int, since time.Time, modelFilter string) (map[string][]string, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any(nil), whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT selected_upstream_id, error_text, status_code, endpoint
		FROM (
			SELECT
				selected_upstream_id,
				error_text,
				status_code,
				endpoint,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY recorded_at DESC, trace_id DESC
				) AS row_number
			FROM logs
			WHERE selected_upstream_id <> ''`+whereSQL+`
			  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		) ranked
		WHERE row_number <= ?
		ORDER BY selected_upstream_id, row_number
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var (
			upstreamID string
			errorText  string
			statusCode int
			endpoint   string
		)
		if err := rows.Scan(&upstreamID, &errorText, &statusCode, &endpoint); err != nil {
			return nil, err
		}
		switch {
		case strings.TrimSpace(errorText) != "":
			out[upstreamID] = append(out[upstreamID], errorText)
		case strings.TrimSpace(endpoint) != "":
			out[upstreamID] = append(out[upstreamID], fmt.Sprintf("%s HTTP %d", endpoint, statusCode))
		default:
			out[upstreamID] = append(out[upstreamID], fmt.Sprintf("HTTP %d", statusCode))
		}
	}
	return out, rows.Err()
}

// upstreamRecentFailuresAll computes upstreamRecentFailures for every upstream
// in one query, ranking the last limit rows of each upstream inside the database.
func (s *Store) upstreamRecentFailuresAll(limit int, since time.Time, modelFilter string) (map[string][]UpstreamFailureRecord, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any(nil), whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT selected_upstream_id, trace_id, model, endpoint, status_code, recorded_at, error_text
		FROM (
			SELECT
				selected_upstream_id,
				trace_id,
				model,
				endpoint,
				status_code,
				recorded_at,
				error_text,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY recorded_at DESC, trace_id DESC
				) AS row_number
			FROM logs
			WHERE selected_upstream_id <> ''`+whereSQL+`
			  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		) ranked
		WHERE row_number <= ?
		ORDER BY selected_upstream_id, row_number
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]UpstreamFailureRecord{}
	for rows.Next() {
		var (
			upstreamID string
			record     UpstreamFailureRecord
			recordedAt string
		)
		if err := rows.Scan(&upstreamID, &record.TraceID, &record.Model, &record.Endpoint, &record.StatusCode, &recordedAt, &record.ErrorText); err != nil {
			return nil, err
		}
		parsed, err := timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		record.RecordedAt = parsed
		record.Reason = classifyUpstreamFailure(record.StatusCode, record.ErrorText)
		out[upstreamID] = append(out[upstreamID], record)
	}
	return out, rows.Err()
}

func buildUpstreamAnalyticsWhere(since time.Time, modelFilter string) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if !since.IsZero() {
		clauses = append(clauses, `recorded_at >= ?`)
		args = append(args, since.UTC().Format(timeLayout))
	}
	if model := strings.TrimSpace(modelFilter); model != "" {
		clauses = append(clauses, `LOWER(model) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+escapeLike(model)+"%")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

// bucketSlot aligns an instant to a bucket boundary in loc.
//
// The Monitor's charts are drawn in the operator's timezone, so a 24-hour bucket
// has to open at local midnight rather than at 08:00 local, which is what a UTC
// grid produces for UTC+08:00. Passing a nil loc keeps the previous UTC grid.
//
// The local case counts forward from the instant's own local midnight instead of
// calling time.Truncate. Truncate measures from the zero Time in year 1, and the
// span to the present overflows the nanosecond Duration it computes, so
// `t.Truncate(24 * time.Hour)` does not land on a day boundary at all - it landed
// 16 hours and a few microseconds away when this was written. Counting from
// midnight stays inside a day's worth of nanoseconds and is exact.
//
// Both the grid slots and the per-row lookups must go through this function: a
// time.Time map key carries its location as well as its instant, so aligning one
// side and not the other makes every lookup miss and the chart come back empty.
// The returned value is always in UTC, so the two sides agree.
func bucketSlot(t time.Time, size time.Duration, loc *time.Location) time.Time {
	if size <= 0 {
		return t.UTC()
	}
	if loc == nil || loc == time.UTC {
		return t.UTC().Truncate(size)
	}
	local := t.In(loc)
	year, month, day := local.Date()
	midnight := time.Date(year, month, day, 0, 0, 0, 0, loc)
	// Local midnight is itself a boundary, so the grid is midnight + j*size for
	// every integer j, and the boundary at or before t is the largest of those
	// that is not after t. sinceMidnight is under 24h, so this arithmetic cannot
	// overflow. Truncating the division is flooring here because it is not
	// negative.
	sinceMidnight := local.Sub(midnight)
	if sinceMidnight < 0 {
		sinceMidnight = 0
	}
	return midnight.Add((sinceMidnight / size) * size).UTC()
}
