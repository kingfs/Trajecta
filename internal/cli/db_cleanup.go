package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/appdb"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/spf13/cobra"
)

// dbCleanupRequestsOptions holds the resolved flags of `db cleanup requests`.
type dbCleanupRequestsOptions struct {
	configPath      string
	format          string
	stdout          io.Writer
	statuses        []string
	failed          bool
	traceIDs        []string
	sessionID       string
	model           string
	endpoint        string
	upstreamID      string
	since           string
	until           string
	limit           int
	keepCassettes   bool
	purgeReferences bool
	apply           bool
}

// newDBCleanupCommand is the maintenance group that removes indexed records and
// the derived rows that hang off them.
func newDBCleanupCommand(runtime *Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "cleanup",
		Short:         "Remove indexed requests and their derived data",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return RequireSubcommand(cmd)
		},
	}
	cmd.AddCommand(newDBCleanupRequestsCommand(runtime))
	return cmd
}

func newDBCleanupRequestsCommand(runtime *Runtime) *cobra.Command {
	opts := dbCleanupRequestsOptions{}
	cmd := &cobra.Command{
		Use:   "requests",
		Short: "Delete recorded requests by HTTP status (for example 404) and rebuild their statistics",
		Long: `Delete recorded requests that match an HTTP status filter, together with
everything derived from them.

The selection is a dry run unless --apply is passed. An applied run deletes the
matching logs rows and the child model exchanges they own, the
upstream_exchanges rows, the per-trace derived rows (observations, findings,
resources, credential-mask audits, analysis runs, parse jobs), the trace-targeted
analysis jobs, and the matching cassette files; it then rebuilds session_summaries
from the remaining logs. --keep-cassettes limits an applied run to the database.
--purge-references also removes curated rows that point at the traces (dataset
examples, eval scores, system events).

Stop serve before applying: the proxy is the only writer, and a run against a
running SQLite database may fail on its lock.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.configPath = runtime.ConfigPath()
			opts.format = runtime.OutputFormat()
			opts.stdout = cmd.OutOrStdout()
			filter, err := dbCleanupRequestsFilter(opts)
			if err != nil {
				return err
			}
			code, wroteResult := runDBCleanupRequestsWithOptions(opts, filter)
			if code == 0 {
				return nil
			}
			// The command writes its own ok=false envelope when a cassette
			// deletion fails; marking it keeps the top-level error path from
			// printing a second envelope for the same result.
			if wroteResult {
				runtime.MarkResultWritten()
			}
			return ExitErrorFromCode(code)
		},
	}
	cmd.Flags().StringArrayVar(&opts.statuses, "status", nil, "HTTP status code to delete (repeatable; a comma-separated list is accepted)")
	cmd.Flags().BoolVar(&opts.failed, "failed", false, "Delete every non-2xx request, including one that recorded an error under a 2xx status")
	cmd.Flags().StringArrayVar(&opts.traceIDs, "trace-id", nil, "Delete this trace id (repeatable)")
	cmd.Flags().StringVar(&opts.sessionID, "session-id", "", "Only requests of this session")
	cmd.Flags().StringVar(&opts.model, "model", "", "Only requests of this model")
	cmd.Flags().StringVar(&opts.endpoint, "endpoint", "", "Only requests of this endpoint")
	cmd.Flags().StringVar(&opts.upstreamID, "upstream", "", "Only requests routed to this upstream id")
	cmd.Flags().StringVar(&opts.since, "since", "", "Only requests recorded at or after this RFC3339 timestamp")
	cmd.Flags().StringVar(&opts.until, "until", "", "Only requests recorded at or before this RFC3339 timestamp")
	cmd.Flags().IntVar(&opts.limit, "limit", 0, "Delete at most this many matched requests (0 means all)")
	cmd.Flags().BoolVar(&opts.keepCassettes, "keep-cassettes", false, "Keep the .http cassette files and only clean the database")
	cmd.Flags().BoolVar(&opts.purgeReferences, "purge-references", false, "Also delete dataset examples, eval scores and system events that point at the traces")
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "Actually delete; without it the command is a dry run")
	return cmd
}

// dbCleanupRequestsFilter validates the flags and turns them into a store
// filter. A malformed invocation is an exit-3 usage error.
func dbCleanupRequestsFilter(opts dbCleanupRequestsOptions) (store.RequestCleanupFilter, error) {
	filter := store.RequestCleanupFilter{
		Failed:     opts.failed,
		SessionID:  strings.TrimSpace(opts.sessionID),
		Model:      strings.TrimSpace(opts.model),
		Endpoint:   strings.TrimSpace(opts.endpoint),
		UpstreamID: strings.TrimSpace(opts.upstreamID),
		Limit:      opts.limit,
	}
	var err error
	if filter.StatusCodes, err = parseCleanupStatusCodes(opts.statuses); err != nil {
		return filter, err
	}
	for _, raw := range opts.traceIDs {
		for _, piece := range strings.Split(raw, ",") {
			if id := strings.TrimSpace(piece); id != "" {
				filter.TraceIDs = append(filter.TraceIDs, id)
			}
		}
	}
	if !opts.failed && len(filter.StatusCodes) == 0 && len(filter.TraceIDs) == 0 {
		return filter, UsageError("db cleanup requests requires --status, --failed or --trace-id", "status")
	}
	if filter.Limit < 0 {
		return filter, UsageError("--limit must not be negative", "limit")
	}
	if filter.Since, err = parseCleanupTime(opts.since, "since"); err != nil {
		return filter, err
	}
	if filter.Until, err = parseCleanupTime(opts.until, "until"); err != nil {
		return filter, err
	}
	if !filter.Since.IsZero() && !filter.Until.IsZero() && filter.Until.Before(filter.Since) {
		return filter, UsageError("--until must not be earlier than --since", "until")
	}
	return filter, nil
}

func parseCleanupStatusCodes(values []string) ([]int, error) {
	seen := map[int]struct{}{}
	var codes []int
	for _, raw := range values {
		for _, piece := range strings.Split(raw, ",") {
			piece = strings.TrimSpace(piece)
			if piece == "" {
				continue
			}
			code, err := strconv.Atoi(piece)
			if err != nil {
				return nil, UsageError(fmt.Sprintf("--status %q is not an HTTP status code", piece), "status")
			}
			if code < 100 || code > 599 {
				return nil, UsageError(fmt.Sprintf("--status %d is outside 100-599", code), "status")
			}
			if _, ok := seen[code]; ok {
				continue
			}
			seen[code] = struct{}{}
			codes = append(codes, code)
		}
	}
	return codes, nil
}

func parseCleanupTime(value string, field string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, UsageError(fmt.Sprintf("--%s %q is not an RFC3339 timestamp", field, value), field)
	}
	return parsed.UTC(), nil
}

// runDBCleanupRequestsWithOptions runs the command and reports its exit code plus
// whether it already wrote a result envelope. The caller needs the second value
// to tell "the command reported a partial failure" apart from "the command never
// got to write", because only the first must suppress the top-level envelope.
func runDBCleanupRequestsWithOptions(opts dbCleanupRequestsOptions, filter store.RequestCleanupFilter) (int, bool) {
	cfg, err := config.Load(opts.configPath)
	if err != nil {
		slog.Error("Failed to load config", "path", opts.configPath, "error", err)
		return 1, false
	}
	st, err := appdb.Open(cfg)
	if err != nil {
		slog.Error("Open trace store failed", "error", err)
		return 1, false
	}
	defer func() { _ = st.Close() }()

	result := store.RequestCleanupApplyResult{}
	if opts.apply {
		result, err = st.ApplyRequestCleanup(filter, store.RequestCleanupApplyOptions{
			PurgeReferences: opts.purgeReferences,
		})
	} else {
		var plan store.RequestCleanupPlan
		plan, err = st.PlanRequestCleanup(filter)
		result = store.RequestCleanupApplyResult{Plan: plan}
	}
	if err != nil {
		slog.Error("Request cleanup failed", "error", err)
		return 1, false
	}

	// A dry run must not touch the filesystem, so a cassette is only removed when
	// the run is applied and --keep-cassettes was not passed.
	keepCassettes := opts.keepCassettes || !opts.apply
	cassettes := cleanupCassetteFiles(cfg.TraceOutputDir(), result.Plan.CassettePaths, keepCassettes)
	report := buildDBCleanupRequestsReport(opts, filter, result, cassettes)

	warnings := cassettes.warnings(keepCassettes)
	if len(result.Plan.ReferenceCounts) > 0 && !opts.purgeReferences {
		warnings = append(warnings, "curated rows still point at the selected traces (dataset_examples/scores/system_events); rerun with --purge-references to remove them")
	}
	ok := len(cassettes.failures) == 0
	if err := WriteResultWithState(stdoutOrDefault(opts.stdout), opts.format, "db.cleanup.requests", ok, report, warnings, func(w io.Writer) error {
		writeDBCleanupRequestsText(w, report)
		return nil
	}); err != nil {
		slog.Error("Write db cleanup requests result failed", "error", err)
		return 1, false
	}
	if !ok {
		return 1, true
	}
	return 0, true
}

// buildDBCleanupRequestsReport assembles the JSON/text payload shared by the dry
// run and the applied run.
func buildDBCleanupRequestsReport(opts dbCleanupRequestsOptions, filter store.RequestCleanupFilter, result store.RequestCleanupApplyResult, cassettes cleanupCassetteResult) map[string]any {
	plan := result.Plan
	report := map[string]any{
		"dry_run":           !opts.apply,
		"applied":           opts.apply,
		"keep_cassettes":    opts.keepCassettes,
		"purge_references":  opts.purgeReferences,
		"matched_requests":  plan.MatchedRequests,
		"child_exchanges":   plan.ChildExchanges,
		"total_log_rows":    plan.TotalLogRows,
		"affected_sessions": plan.AffectedSessions,
		"cassette_count":    plan.CassetteCount,
		"cassette_bytes":    plan.CassetteBytes,
		"missing_cassettes": plan.MissingCassettes,
		"status_counts":     cleanupStatusCountMap(plan.StatusCounts),
		"sample_requests":   cleanupSampleRequests(plan.SampleRequests),
		"filter":            cleanupFilterReport(filter),
	}
	if len(plan.ReferenceCounts) > 0 {
		report["reference_counts"] = plan.ReferenceCounts
	}
	if !opts.apply {
		report["would_delete_log_rows"] = plan.TotalLogRows
		report["would_delete_cassettes"] = plan.CassetteCount
		report["would_rebuild_sessions"] = plan.AffectedSessions
		return report
	}
	report["deleted_logs"] = result.DeletedLogs
	report["deleted_upstream_exchanges"] = result.DeletedUpstreamExchanges
	report["deleted_derived"] = result.DeletedDerived
	report["deleted_analysis_jobs"] = result.DeletedAnalysisJobs
	report["rebuilt_sessions"] = result.RebuiltSessions
	report["removed_session_summaries"] = result.RemovedSessionSummaries
	report["pruned_resources"] = result.PrunedResources
	if opts.purgeReferences {
		report["deleted_references"] = result.DeletedReferences
	}
	report["deleted_cassettes"] = cassettes.deleted
	report["freed_cassette_bytes"] = cassettes.freedBytes
	report["kept_cassettes"] = cassettes.kept
	report["failed_cassettes"] = cassettes.failures
	report["skipped_cassettes"] = cassettes.skipped
	return report
}

func cleanupStatusCountMap(counts []store.RequestCleanupStatusCount) map[string]int {
	out := make(map[string]int, len(counts))
	for _, item := range counts {
		out[strconv.Itoa(item.StatusCode)] = item.Count
	}
	return out
}

func cleanupSampleRequests(samples []store.RequestCleanupRequest) []map[string]any {
	out := make([]map[string]any, 0, len(samples))
	for _, item := range samples {
		out = append(out, map[string]any{
			"trace_id":    item.TraceID,
			"path":        item.Path,
			"status_code": item.StatusCode,
			"model":       item.Model,
			"session_id":  item.SessionID,
			"endpoint":    item.Endpoint,
			"recorded_at": item.RecordedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

func cleanupFilterReport(filter store.RequestCleanupFilter) map[string]any {
	report := map[string]any{
		"failed": filter.Failed,
		"limit":  filter.Limit,
	}
	if len(filter.StatusCodes) > 0 {
		report["status_codes"] = filter.StatusCodes
	}
	if len(filter.TraceIDs) > 0 {
		report["trace_ids"] = filter.TraceIDs
	}
	if filter.SessionID != "" {
		report["session_id"] = filter.SessionID
	}
	if filter.Model != "" {
		report["model"] = filter.Model
	}
	if filter.Endpoint != "" {
		report["endpoint"] = filter.Endpoint
	}
	if filter.UpstreamID != "" {
		report["upstream_id"] = filter.UpstreamID
	}
	if !filter.Since.IsZero() {
		report["since"] = filter.Since.UTC().Format(time.RFC3339)
	}
	if !filter.Until.IsZero() {
		report["until"] = filter.Until.UTC().Format(time.RFC3339)
	}
	return report
}

func writeDBCleanupRequestsText(w io.Writer, report map[string]any) {
	mode := "applied"
	if dry, _ := report["dry_run"].(bool); dry {
		mode = "dry-run"
	}
	fmt.Fprintf(w, "db cleanup requests (%s)\n", mode)
	fmt.Fprintf(w, "matched_requests: %v\n", report["matched_requests"])
	fmt.Fprintf(w, "child_exchanges: %v\n", report["child_exchanges"])
	fmt.Fprintf(w, "total_log_rows: %v\n", report["total_log_rows"])
	fmt.Fprintf(w, "affected_sessions: %v\n", report["affected_sessions"])
	if counts, ok := report["status_counts"].(map[string]int); ok && len(counts) > 0 {
		fmt.Fprintf(w, "status_counts: %s\n", formatCleanupStatusCounts(counts))
	}
	fmt.Fprintf(w, "cassette_count: %v\n", report["cassette_count"])
	fmt.Fprintf(w, "cassette_bytes: %v (%s)\n", report["cassette_bytes"], cleanupHumanBytes(asInt64(report["cassette_bytes"])))
	if missing, ok := report["missing_cassettes"]; ok {
		fmt.Fprintf(w, "missing_cassettes: %v\n", missing)
	}
	if refs, ok := report["reference_counts"].(map[string]int); ok && len(refs) > 0 {
		fmt.Fprintf(w, "reference_counts: %s\n", formatCleanupStatusCounts(refs))
	}
	if samples, ok := report["sample_requests"].([]map[string]any); ok && len(samples) > 0 {
		fmt.Fprintf(w, "sample_requests:\n")
		for _, sample := range samples {
			fmt.Fprintf(w, "  %v  HTTP %v  %v  %v\n", sample["trace_id"], sample["status_code"], sample["model"], sample["recorded_at"])
		}
	}
	if dry, _ := report["dry_run"].(bool); dry {
		fmt.Fprintf(w, "would_delete_log_rows: %v\n", report["would_delete_log_rows"])
		fmt.Fprintf(w, "would_delete_cassettes: %v\n", report["would_delete_cassettes"])
		fmt.Fprintf(w, "would_rebuild_sessions: %v\n", report["would_rebuild_sessions"])
		fmt.Fprintln(w, "no changes applied; rerun with --apply to delete")
		return
	}
	fmt.Fprintf(w, "deleted_logs: %v\n", report["deleted_logs"])
	fmt.Fprintf(w, "deleted_upstream_exchanges: %v\n", report["deleted_upstream_exchanges"])
	if derived, ok := report["deleted_derived"].(map[string]int); ok {
		for _, table := range sortedCleanupTableNames(derived) {
			if derived[table] == 0 {
				continue
			}
			fmt.Fprintf(w, "deleted_%s: %d\n", table, derived[table])
		}
	}
	fmt.Fprintf(w, "deleted_analysis_jobs: %v\n", report["deleted_analysis_jobs"])
	if refs, ok := report["deleted_references"].(map[string]int); ok {
		for _, table := range sortedCleanupTableNames(refs) {
			fmt.Fprintf(w, "deleted_reference_%s: %d\n", table, refs[table])
		}
	}
	fmt.Fprintf(w, "rebuilt_sessions: %v\n", report["rebuilt_sessions"])
	fmt.Fprintf(w, "removed_session_summaries: %v\n", report["removed_session_summaries"])
	fmt.Fprintf(w, "pruned_resources: %v\n", report["pruned_resources"])
	if kept, _ := report["keep_cassettes"].(bool); kept {
		fmt.Fprintf(w, "kept_cassettes: %v\n", report["kept_cassettes"])
	} else {
		fmt.Fprintf(w, "deleted_cassettes: %v\n", report["deleted_cassettes"])
		fmt.Fprintf(w, "freed_cassette_bytes: %v (%s)\n", report["freed_cassette_bytes"], cleanupHumanBytes(asInt64(report["freed_cassette_bytes"])))
	}
	if failures, ok := report["failed_cassettes"].([]string); ok && len(failures) > 0 {
		fmt.Fprintf(w, "failed_cassettes: %d\n", len(failures))
		for _, failure := range failures {
			fmt.Fprintf(w, "  %s\n", failure)
		}
	}
	if skipped, ok := report["skipped_cassettes"].([]string); ok && len(skipped) > 0 {
		fmt.Fprintf(w, "skipped_cassettes: %d (outside the trace output dir or not a .http file)\n", len(skipped))
	}
}

func formatCleanupStatusCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, " ")
}

func sortedCleanupTableNames(values map[string]int) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

func asInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	default:
		return 0
	}
}

func cleanupHumanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.2f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.2f EiB", value/unit)
}

// cleanupCassetteResult is the filesystem half of an applied run.
type cleanupCassetteResult struct {
	deleted    int
	kept       int
	freedBytes int64
	failures   []string
	skipped    []string
}

func (r cleanupCassetteResult) warnings(keep bool) []string {
	var warnings []string
	if keep {
		return warnings
	}
	if len(r.skipped) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d cassette file(s) were skipped because they are outside the trace output dir or not .http files", len(r.skipped)))
	}
	if len(r.failures) > 0 {
		warnings = append(warnings, fmt.Sprintf("%d cassette file(s) could not be deleted; the index rows are already gone and a later sync may re-index them", len(r.failures)))
	}
	return warnings
}

// cleanupCassetteFiles removes the plan's cassette files. It refuses any path
// that is not a .http file under outputDir, so a corrupted or hand-edited
// logs.path cannot make the command delete something else. A missing file is
// not an error; index rows can outlive a cassette moved away by hand.
func cleanupCassetteFiles(outputDir string, paths []string, keep bool) cleanupCassetteResult {
	result := cleanupCassetteResult{}
	if keep {
		result.kept = len(paths)
		return result
	}
	if len(paths) == 0 {
		return result
	}
	root := filepath.Clean(outputDir)
	rootAbs, rootErr := filepath.Abs(root)
	for _, path := range paths {
		if !strings.EqualFold(filepath.Ext(path), ".http") {
			result.skipped = append(result.skipped, path)
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			result.failures = append(result.failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if rootErr != nil || !cleanupPathWithin(rootAbs, abs) {
			result.skipped = append(result.skipped, path)
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			result.failures = append(result.failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		size := info.Size()
		if err := os.Remove(abs); err != nil {
			result.failures = append(result.failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		result.deleted++
		result.freedBytes += size
	}
	return result
}

// cleanupPathWithin reports whether abs sits at or below root.
func cleanupPathWithin(root string, abs string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
