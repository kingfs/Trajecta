package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// AnalysisQueueSettings is the runtime policy of the two parse lanes.
//
// The queue has two lanes with deliberately different priorities:
//
//   - fresh: the traces the proxy just recorded. They are what a reader opens
//     in the Monitor, so they are claimed first and at the historical rate's
//     expense.
//   - backfill: derived data that is re-read from old cassettes (project
//     attribution today, any re-derivation later). It is large and not urgent,
//     so it runs continuously at a lower, operator-controlled rate and never
//     blocks a fresh trace.
//
// Both lanes are settings in `app_settings` rather than YAML, because the right
// rate depends on the deployment's disk and database headroom at the moment,
// not on how it was installed. The defaults are the values that were hardcoded
// before this existed, so an untouched deployment behaves exactly as before.
type AnalysisQueueSettings struct {
	// ParseBatchSize and ParseIntervalMs pace the fresh lane.
	ParseBatchSize  int `json:"parse_batch_size"`
	ParseIntervalMs int `json:"parse_interval_ms"`
	// ParseMaxAttempts is how many claims a trace gets before it is recorded as
	// failed. A parse error is usually permanent (a corrupt cassette), but a
	// database hiccup is not, and a single failure must not bury a trace.
	ParseMaxAttempts int `json:"parse_max_attempts"`

	// BackfillEnabled pauses the historical lane without touching the fresh one.
	BackfillEnabled bool `json:"backfill_enabled"`
	// BackfillBatchSize and BackfillIntervalMs pace the historical lane.
	BackfillBatchSize  int `json:"backfill_batch_size"`
	BackfillIntervalMs int `json:"backfill_interval_ms"`
	// BackfillChunkSize is how many traces one planner pass queues, and
	// BackfillLowWatermark is the queue depth below which the planner tops it up.
	// Together they make historical attribution a continuous background job
	// rather than a one-shot request.
	BackfillChunkSize    int `json:"backfill_chunk_size"`
	BackfillLowWatermark int `json:"backfill_low_watermark"`

	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// AnalysisQueueSettingsKey is the application-database key holding the policy.
const AnalysisQueueSettingsKey = "analysis.queue"

// Parse job lanes.
const (
	ParsePriorityFresh    = "fresh"
	ParsePriorityBackfill = "backfill"
)

// Bounds. The intervals have a floor because a zero interval would spin the
// worker, and the batch sizes have a ceiling because one claim holds a database
// transaction's worth of rows and the whole batch is processed serially.
const (
	minParseIntervalMs      = 200
	maxParseIntervalMs      = 3_600_000
	minParseBatchSize       = 1
	maxParseBatchSize       = 500
	minParseMaxAttempts     = 1
	maxParseMaxAttempts     = 20
	minBackfillBatchSize    = 1
	maxBackfillBatchSize    = 200
	minBackfillChunkSize    = 1
	maxBackfillChunkSize    = 5000
	minBackfillLowWatermark = 0
	maxBackfillLowWatermark = 10000
)

// DefaultAnalysisQueueSettings returns the pre-existing hardcoded behaviour plus
// a historical lane that is on but slow.
func DefaultAnalysisQueueSettings() AnalysisQueueSettings {
	return AnalysisQueueSettings{
		ParseBatchSize:       10,
		ParseIntervalMs:      5000,
		ParseMaxAttempts:     3,
		BackfillEnabled:      true,
		BackfillBatchSize:    2,
		BackfillIntervalMs:   5000,
		BackfillChunkSize:    1000,
		BackfillLowWatermark: 200,
	}
}

// NormalizeAnalysisQueueSettings fills zero fields from the defaults and clamps
// the rest into the allowed range. A stored row written by an older build (or
// hand-edited) must never be able to stall or spin a worker.
func NormalizeAnalysisQueueSettings(settings AnalysisQueueSettings) AnalysisQueueSettings {
	defaults := DefaultAnalysisQueueSettings()
	if settings.ParseBatchSize == 0 {
		settings.ParseBatchSize = defaults.ParseBatchSize
	}
	if settings.ParseIntervalMs == 0 {
		settings.ParseIntervalMs = defaults.ParseIntervalMs
	}
	if settings.ParseMaxAttempts == 0 {
		settings.ParseMaxAttempts = defaults.ParseMaxAttempts
	}
	if settings.BackfillBatchSize == 0 {
		settings.BackfillBatchSize = defaults.BackfillBatchSize
	}
	if settings.BackfillIntervalMs == 0 {
		settings.BackfillIntervalMs = defaults.BackfillIntervalMs
	}
	if settings.BackfillChunkSize == 0 {
		settings.BackfillChunkSize = defaults.BackfillChunkSize
	}
	if settings.BackfillLowWatermark == 0 {
		settings.BackfillLowWatermark = defaults.BackfillLowWatermark
	}
	settings.ParseBatchSize = clampInt(settings.ParseBatchSize, minParseBatchSize, maxParseBatchSize)
	settings.ParseIntervalMs = clampInt(settings.ParseIntervalMs, minParseIntervalMs, maxParseIntervalMs)
	settings.ParseMaxAttempts = clampInt(settings.ParseMaxAttempts, minParseMaxAttempts, maxParseMaxAttempts)
	settings.BackfillBatchSize = clampInt(settings.BackfillBatchSize, minBackfillBatchSize, maxBackfillBatchSize)
	settings.BackfillIntervalMs = clampInt(settings.BackfillIntervalMs, minParseIntervalMs, maxParseIntervalMs)
	settings.BackfillChunkSize = clampInt(settings.BackfillChunkSize, minBackfillChunkSize, maxBackfillChunkSize)
	settings.BackfillLowWatermark = clampInt(settings.BackfillLowWatermark, minBackfillLowWatermark, maxBackfillLowWatermark)
	return settings
}

// ValidateAnalysisQueueSettings rejects a payload a caller should fix instead of
// silently clamping, so an operator who typed 0 for an interval is told why.
func ValidateAnalysisQueueSettings(settings AnalysisQueueSettings) string {
	if settings.ParseBatchSize < minParseBatchSize || settings.ParseBatchSize > maxParseBatchSize {
		return "parse_batch_size must be between 1 and 500"
	}
	if settings.ParseIntervalMs < minParseIntervalMs || settings.ParseIntervalMs > maxParseIntervalMs {
		return "parse_interval_ms must be between 200 and 3600000"
	}
	if settings.ParseMaxAttempts < minParseMaxAttempts || settings.ParseMaxAttempts > maxParseMaxAttempts {
		return "parse_max_attempts must be between 1 and 20"
	}
	if settings.BackfillBatchSize < minBackfillBatchSize || settings.BackfillBatchSize > maxBackfillBatchSize {
		return "backfill_batch_size must be between 1 and 200"
	}
	if settings.BackfillIntervalMs < minParseIntervalMs || settings.BackfillIntervalMs > maxParseIntervalMs {
		return "backfill_interval_ms must be between 200 and 3600000"
	}
	if settings.BackfillChunkSize < minBackfillChunkSize || settings.BackfillChunkSize > maxBackfillChunkSize {
		return "backfill_chunk_size must be between 1 and 5000"
	}
	if settings.BackfillLowWatermark < minBackfillLowWatermark || settings.BackfillLowWatermark > maxBackfillLowWatermark {
		return "backfill_low_watermark must be between 0 and 10000"
	}
	return ""
}

// LoadAnalysisQueueSettings reads the stored policy, falling back to the
// defaults when the row is missing or unreadable. A broken row must not stop the
// queue from draining.
func (s *Store) LoadAnalysisQueueSettings(ctx context.Context) AnalysisQueueSettings {
	var settings AnalysisQueueSettings
	found, err := s.LoadAppSettingJSON(ctx, AnalysisQueueSettingsKey, &settings)
	if err != nil || !found {
		return DefaultAnalysisQueueSettings()
	}
	return NormalizeAnalysisQueueSettings(settings)
}

// SaveAnalysisQueueSettings stores a validated policy.
func (s *Store) SaveAnalysisQueueSettings(ctx context.Context, settings AnalysisQueueSettings) error {
	settings = NormalizeAnalysisQueueSettings(settings)
	settings.UpdatedAt = time.Now().UTC()
	return s.SaveAppSettingJSON(ctx, AnalysisQueueSettingsKey, settings)
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// ParseJobLane normalises a lane name, mapping anything unknown to fresh so a
// bad value can never hide work from the historical lane.
func ParseJobLane(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), ParsePriorityBackfill) {
		return ParsePriorityBackfill
	}
	return ParsePriorityFresh
}

// BackfillCursor is how far the continuous historical sweep has walked back.
//
// The sweep is not a one-shot request: the planner keeps a queue of recent
// un-attributed traces topped up and walks backwards through recorded_at until
// there is nothing older left. The cursor is what makes that terminate - without
// it the selector ("traces with no project row") would keep picking the same
// traces forever, because a trace that simply declares no working directory
// never gets a project row.
//
// A completed sweep stays completed. Re-deriving history after the extractor
// changes is an explicit decision, so the panel offers a reset that clears the
// cursor and starts the walk again from the newest trace.
type BackfillCursor struct {
	// ProjectThrough is the newest recorded_at the project sweep has reached;
	// traces at or before it have been queued or are already attributed.
	ProjectThrough time.Time `json:"project_through,omitempty"`
	// CompletedAt is set when a pass found nothing left to queue.
	CompletedAt time.Time `json:"completed_at,omitempty"`
	UpdatedAt   time.Time `json:"updated_at,omitempty"`
}

// MarshalJSON omits the timestamps the sweep has not reached.
//
// `omitempty` does nothing for a time.Time (a struct is never "empty"), so a
// zero cursor used to serialize as `{"project_through":"0001-01-01T00:00:00Z"}`
// and the Monitor read a zero as a real timestamp - the panel said the sweep was
// complete before it had started. The wire form is what the page believes.
func (c BackfillCursor) MarshalJSON() ([]byte, error) {
	type wire struct {
		ProjectThrough *time.Time `json:"project_through,omitempty"`
		CompletedAt    *time.Time `json:"completed_at,omitempty"`
		UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	}
	payload := wire{}
	if !c.ProjectThrough.IsZero() {
		payload.ProjectThrough = &c.ProjectThrough
	}
	if !c.CompletedAt.IsZero() {
		payload.CompletedAt = &c.CompletedAt
	}
	if !c.UpdatedAt.IsZero() {
		payload.UpdatedAt = &c.UpdatedAt
	}
	return json.Marshal(payload)
}

// AnalysisBackfillStateKey stores the cursor.
const AnalysisBackfillStateKey = "analysis.backfill"

// LoadBackfillState reads the cursor, returning the zero value when the sweep
// has never run or was reset.
func (s *Store) LoadBackfillState(ctx context.Context) BackfillCursor {
	var state BackfillCursor
	found, err := s.LoadAppSettingJSON(ctx, AnalysisBackfillStateKey, &state)
	if err != nil || !found {
		return BackfillCursor{}
	}
	return state
}

// SaveBackfillState stores the cursor.
func (s *Store) SaveBackfillState(ctx context.Context, state BackfillCursor) error {
	state.UpdatedAt = time.Now().UTC()
	return s.SaveAppSettingJSON(ctx, AnalysisBackfillStateKey, state)
}

// ResetBackfillState clears the cursor so the next planner pass starts again
// from the newest trace. It does not delete any derived row: the sweep only
// queues work.
func (s *Store) ResetBackfillState(ctx context.Context) error {
	_, err := s.DeleteAppSetting(ctx, AnalysisBackfillStateKey)
	return err
}
