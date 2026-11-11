package transfer

import (
	"fmt"
	"time"
)

// TableImportReport counts what happened to one table during import.
type TableImportReport struct {
	Name             string `json:"name"`
	Processed        int64  `json:"processed"`
	Inserted         int64  `json:"inserted"`
	Updated          int64  `json:"updated"`
	MergedModels     int64  `json:"merged_models,omitempty"`
	SkippedUnchanged int64  `json:"skipped_unchanged"`
	SkippedConflict  int64  `json:"skipped_conflict"`
	SkippedTrace     int64  `json:"skipped_trace"` // rows skipped because their trace or path was skipped
	Deduped          int64  `json:"deduped,omitempty"`
}

// FileImportReport counts cassette outcomes.
type FileImportReport struct {
	Processed        int64    `json:"processed"`
	Copied           int64    `json:"copied"`
	Updated          int64    `json:"updated"` // replaced because size/mtime differed
	SkippedIdentical int64    `json:"skipped_identical"`
	Missing          int64    `json:"missing"`
	Conflicts        int64    `json:"conflicts"` // differing file, overwrite disabled
	ConflictSamples  []string `json:"conflict_samples,omitempty"`
}

// ImportResult is the machine-readable outcome of one import.
type ImportResult struct {
	Bundle            string              `json:"bundle"`
	DryRun            bool                `json:"dry_run"`
	Tables            []TableImportReport `json:"tables"`
	ChannelReuses     []ChannelReuse      `json:"channel_reuses"`
	ChannelsCreated   []string            `json:"channels_created"`
	Cassettes         FileImportReport    `json:"cassettes"`
	SessionRebuilt    bool                `json:"session_rebuilt"`
	SessionRebuildDur time.Duration       `json:"session_rebuild_ms"`
	Warnings          []string            `json:"warnings,omitempty"`
	Duration          time.Duration       `json:"duration_ms"`
}

func (r *ImportResult) warn(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}
