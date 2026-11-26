package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

const traceRetentionBatchSize = 500

// runTraceRetention removes indexed client-visible requests older than the
// configured age, their child exchanges and rebuildable derived rows, then
// removes only regular .http files lexically contained by outputDir. Curated
// references remain intact, matching the explicit cleanup command's default.
func runTraceRetention(ctx context.Context, traceStore *store.Store, outputDir string, retentionDays int) error {
	if traceStore == nil {
		return errors.New("trace retention store is nil")
	}
	if retentionDays <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays)
	var deletedTraces, deletedCassettes, skippedCassettes int
	var freedBytes int64
	var deleteFailures []string

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := traceStore.ApplyRequestCleanup(store.RequestCleanupFilter{
			All:   true,
			Until: cutoff,
			Limit: traceRetentionBatchSize,
		}, store.RequestCleanupApplyOptions{})
		if err != nil {
			return fmt.Errorf("apply trace retention batch: %w", err)
		}
		if result.Plan.MatchedRequests == 0 {
			break
		}
		deletedTraces += result.Plan.TotalLogRows
		files := removeRetentionCassettes(outputDir, result.Plan.CassettePaths)
		deletedCassettes += files.deleted
		freedBytes += files.freedBytes
		skippedCassettes += len(files.skipped)
		deleteFailures = append(deleteFailures, files.failures...)
		if result.Plan.MatchedRequests < traceRetentionBatchSize {
			break
		}
	}

	if skippedCassettes > 0 {
		slog.Warn("Trace retention skipped cassette paths that were outside the trace root, symlinks, or not regular .http files",
			"count", skippedCassettes, "trace_output_dir", outputDir)
	}
	if len(deleteFailures) > 0 {
		slog.Warn("Trace retention removed index rows but some cassette files could not be deleted; a later index sync will retry in-root files",
			"count", len(deleteFailures), "trace_output_dir", outputDir, "errors", deleteFailures)
		return fmt.Errorf("delete %d expired cassette file(s): %s", len(deleteFailures), strings.Join(deleteFailures, "; "))
	}
	if deletedTraces > 0 {
		slog.Info("Trace retention completed", "retention_days", retentionDays, "cutoff", cutoff.UTC().Format(time.RFC3339),
			"deleted_trace_rows", deletedTraces, "deleted_cassettes", deletedCassettes, "freed_bytes", freedBytes)
	}
	return nil
}

type retentionFileResult struct {
	deleted    int
	freedBytes int64
	skipped    []string
	failures   []string
}

func removeRetentionCassettes(outputDir string, paths []string) retentionFileResult {
	result := retentionFileResult{}
	root := strings.TrimSpace(outputDir)
	if root == "" {
		root = "."
	}
	rootAbs, rootErr := filepath.Abs(filepath.Clean(root))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !strings.EqualFold(filepath.Ext(path), ".http") {
			result.skipped = append(result.skipped, path)
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			result.failures = append(result.failures, fmt.Sprintf("resolve path: %v", err))
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		if rootErr != nil || !retentionPathWithin(rootAbs, abs) {
			result.skipped = append(result.skipped, path)
			continue
		}
		info, err := os.Lstat(abs)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			result.failures = append(result.failures, fmt.Sprintf("inspect cassette: %v", err))
			continue
		}
		if !info.Mode().IsRegular() {
			result.skipped = append(result.skipped, path)
			continue
		}
		if err := os.Remove(abs); err != nil {
			result.failures = append(result.failures, fmt.Sprintf("remove cassette: %v", err))
			continue
		}
		result.deleted++
		result.freedBytes += info.Size()
	}
	return result
}

func retentionPathWithin(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
