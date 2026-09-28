package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

// dbFlags collects the `db` command flags.
type dbFlags struct {
	tables        []string
	skipTables    []string
	fillMissing   bool
	noVerify      bool
	noAnalyze     bool
	verifyOnly    bool
	tolerateDrift bool
	reconcile     bool
	pruneStale    bool
	dataRoot      string
	recordedRoot  string
	maxSamples    int
}

func newDBCommand(runtime *cliRuntime) *cobra.Command {
	flags := dbFlags{}
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Merge legacy SQLite application databases into Postgres",
		Long: `db copies every row of the legacy SQLite application databases into the
configured Postgres database.

The merge is additive and idempotent: rows whose primary key (or any other
unique key) already exists are skipped, so it can be re-run safely. Required
Postgres columns that the legacy schema does not have are reported instead of
being invented; pass --fill-missing-required to insert placeholders instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.execDB(cmd, flags)
		},
	}
	cmd.Flags().StringArrayVar(&flags.tables, "table", nil, "only merge these tables (repeatable)")
	cmd.Flags().StringArrayVar(&flags.skipTables, "skip-table", nil, "never merge these tables (repeatable)")
	cmd.Flags().BoolVar(&flags.fillMissing, "fill-missing-required", false,
		"insert placeholder values for required Postgres columns the legacy table lacks")
	cmd.Flags().BoolVar(&flags.noVerify, "no-verify-keys", false, "skip the post-copy primary key verification")
	cmd.Flags().BoolVar(&flags.noAnalyze, "no-analyze", false, "do not run ANALYZE after a table is merged")
	cmd.Flags().BoolVar(&flags.verifyOnly, "verify-only", false, "check that the legacy rows exist in Postgres without writing anything")
	cmd.Flags().BoolVar(&flags.tolerateDrift, "tolerate-snapshot-drift", false,
		"excuse missing keys in runtime snapshot tables (upstream_targets, upstream_models) that the running server rewrites")
	cmd.Flags().BoolVar(&flags.reconcile, "reconcile-derived-trace-ids", false,
		"repair derived tables whose trace_id still carries the legacy value instead of merging rows")
	cmd.Flags().BoolVar(&flags.pruneStale, "prune-superseded-index-rows", false,
		"with --apply, also delete the trace index rows whose cassette moved away once nothing derives from them")
	cmd.Flags().StringVar(&flags.dataRoot, "data-root", "",
		"local root the recorded cassette paths live under (default: the configured trace directory)")
	cmd.Flags().StringVar(&flags.recordedRoot, "recorded-prefix", "",
		"prefix the database recorded, mapped onto --data-root (defaults to --data-root)")
	cmd.Flags().IntVar(&flags.maxSamples, "max-samples", 10,
		"how many example paths the superseded-row report lists (0 restores the default)")
	return cmd
}

func (r *cliRuntime) execDB(cmd *cobra.Command, flags dbFlags) error {
	env, cfg, err := r.prepare()
	if err != nil {
		return err
	}
	if flags.reconcile {
		return r.execReconcile(cmd, cfg, flags)
	}
	report, warnings, err := r.runDatabaseMerge(cmd, cfg, flags)
	if env != nil {
		warnings = append(warnings, env.Warnings...)
	}
	if err != nil {
		return err
	}
	if report == nil {
		// No legacy database was found. Report the empty result so that a JSON
		// consumer still receives an envelope.
		return r.writeResult(cmd.OutOrStdout(), "db", true, nil, func(w io.Writer) error {
			fmt.Fprintln(w, "nothing to merge: no legacy SQLite application database was found")
			return nil
		}, warnings)
	}
	if writeErr := r.writeResult(cmd.OutOrStdout(), "db", report != nil && report.OK(), report, func(w io.Writer) error {
		printCopyReport(w, report, r.opts.apply && !flags.verifyOnly)
		return nil
	}, warnings); writeErr != nil {
		return writeErr
	}
	for _, warning := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warning)
	}
	if !report.OK() {
		return fail("the merge reported %d failed rows, %d missing keys and %d tables that need attention",
			report.Failed, report.Missing, report.TablesSkip)
	}
	return nil
}

// execReconcile repairs derived tables whose trace ids still carry the value the
// legacy index recorded. It never deletes the last copy of a row: rows whose
// identity already exists under the current id are removed as superseded
// duplicates, and every other orphan row is moved onto the current id.
func (r *cliRuntime) execReconcile(cmd *cobra.Command, cfg *config.Config, flags dbFlags) error {
	if !strings.EqualFold(strings.TrimSpace(cfg.Database.Driver), "postgres") {
		return usageErr("database.driver",
			"the target must be Postgres, but the configuration resolves to %q; set TRAJECTA_DATABASE_DRIVER=postgres and TRAJECTA_DATABASE_DSN",
			cfg.Database.Driver)
	}
	paths, warnings, err := r.resolveSQLitePaths(cfg)
	if err != nil {
		return err
	}
	if len(paths) == 0 && strings.TrimSpace(flags.dataRoot) == "" {
		return fail("no legacy SQLite database was found; pass --sqlite <path> (the archived %s files are accepted)", "*.sqlite3.migrated")
	}
	dataRoot := strings.TrimSpace(flags.dataRoot)
	if dataRoot == "" {
		dirs, dirWarnings := r.resolveTraceDirs(cfg)
		warnings = append(warnings, dirWarnings...)
		if len(dirs) > 0 {
			dataRoot = dirs[0]
		}
	}
	if len(paths) == 0 {
		warnings = append(warnings, "no legacy SQLite database was given, so the orphan trace ids cannot be mapped onto the current ones; only the superseded index rows are handled")
	}
	report, err := legacymigrate.ReconcileDerivedTraceIDs(cmd.Context(), legacymigrate.ReconcileOptions{
		SQLitePaths:              paths,
		PostgresDSN:              cfg.Database.DSN,
		Apply:                    r.opts.apply,
		OpenMode:                 r.opts.sqliteOpen,
		BatchSize:                r.opts.batchSize,
		Tables:                   flags.tables,
		DataRoot:                 dataRoot,
		RecordedPrefix:           strings.TrimSpace(flags.recordedRoot),
		PruneSupersededIndexRows: flags.pruneStale,
		MaxSamples:               flags.maxSamples,
	})
	if err != nil {
		return err
	}
	if writeErr := r.writeResult(cmd.OutOrStdout(), "db-reconcile", report.OK(), report, func(w io.Writer) error {
		printReconcileReport(w, report)
		return nil
	}, warnings); writeErr != nil {
		return writeErr
	}
	for _, warning := range warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warning)
	}
	if !report.OK() {
		return fail("reconciliation left %d orphan rows and %d tables that need attention",
			report.Unresolved(), reconcileErrors(report))
	}
	return nil
}

// reconcileErrors counts the tables whose reconciliation failed.
func reconcileErrors(report *legacymigrate.ReconcileReport) int {
	failed := 0
	for _, table := range report.Tables {
		if table.Err != "" {
			failed++
		}
	}
	if report.Superseded != nil && report.Superseded.Err != "" {
		failed++
	}
	return failed
}

// printReconcileReport renders a reconciliation report as text.
func printReconcileReport(w io.Writer, report *legacymigrate.ReconcileReport) {
	fmt.Fprintf(w, "%-32s %9s %9s %10s %10s %11s\n", "table", "orphans", "mapped", "duplicates", "remapped", "unresolved")
	for _, table := range report.Tables {
		fmt.Fprintf(w, "%-32s %9d %9d %10d %10d %11d", table.Table, table.OrphanIDs, table.MappedIDs, table.DuplicateRows, table.RemappedRows, table.UnresolvedRows)
		if len(table.IdentityKeys) > 0 {
			fmt.Fprintf(w, "  identity(%s)", strings.Join(table.IdentityKeys, ", "))
		}
		if table.Err != "" {
			fmt.Fprintf(w, "  error: %s", table.Err)
		}
		fmt.Fprintln(w)
		for _, id := range table.SampleUnmapped {
			fmt.Fprintf(w, "      ? no legacy trace for %s\n", id)
		}
	}
	fmt.Fprintf(w, "totals            %d legacy ids, %d mapped to a current trace, %d duplicate rows, %d rows remapped, %d unresolved\n",
		report.LegacyIDs, report.MappedIDs, report.Deleted(), report.Remapped(), report.Unresolved())
	if stats := report.Superseded; stats != nil {
		fmt.Fprintf(w, "superseded rows   %d index rows checked, %d missing at the recorded path, %d superseded, %d ambiguous, %d with no cassette anywhere\n",
			stats.Checked, stats.MissingFiles, stats.Superseded, stats.Ambiguous, stats.OrphanFiles)
		fmt.Fprintf(w, "  derived rows of the superseded ids: %d duplicate, %d remapped, %d unresolved\n",
			stats.DerivedDuplicates, stats.DerivedRemapped, stats.DerivedUnresolved)
		if report.DryRun {
			fmt.Fprintf(w, "  index rows the prune targets once the derived rows above are reconciled: %d (needs --apply --prune-superseded-index-rows)\n", stats.PrunePending)
		} else {
			fmt.Fprintf(w, "  index rows pruned: %d\n", stats.Pruned)
		}
		printSample := func(reason string, samples []string, total int64) {
			for _, path := range samples {
				fmt.Fprintf(w, "      ? %s: %s\n", reason, path)
			}
			if remaining := total - int64(len(samples)); remaining > 0 {
				fmt.Fprintf(w, "      ... and %d more (raise --max-samples to list them)\n", remaining)
			}
		}
		printSample("cassette missing everywhere", stats.SampleOrphanPaths, stats.OrphanFiles)
		printSample("several indexed candidates", stats.SampleAmbiguous, stats.Ambiguous)
		if stats.Err != "" {
			fmt.Fprintf(w, "      error: %s\n", stats.Err)
		}
	}
	if report.DryRun {
		fmt.Fprintln(w, "note              dry run; pass --apply to delete the duplicates and rewrite the remaining rows")
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "warning           %s\n", warning)
	}
}

// runDatabaseMerge resolves the legacy databases and merges them.
func (r *cliRuntime) runDatabaseMerge(cmd *cobra.Command, cfg *config.Config, flags dbFlags) (*legacymigrate.CopyReport, []string, error) {
	if !strings.EqualFold(strings.TrimSpace(cfg.Database.Driver), "postgres") {
		return nil, nil, usageErr("database.driver",
			"the target must be Postgres, but the configuration resolves to %q; set TRAJECTA_DATABASE_DRIVER=postgres and TRAJECTA_DATABASE_DSN",
			cfg.Database.Driver)
	}
	paths, warnings, err := r.resolveSQLitePaths(cfg)
	if err != nil {
		return nil, nil, err
	}
	if len(paths) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "no legacy SQLite application database was found; nothing to merge")
		return nil, warnings, nil
	}

	report, err := legacymigrate.MergeSQLiteIntoPostgres(cmd.Context(), legacymigrate.CopyOptions{
		SQLitePaths:           paths,
		PostgresDSN:           cfg.Database.DSN,
		BatchSize:             r.opts.batchSize,
		Workers:               r.databaseWorkers(),
		Tables:                flags.tables,
		SkipTables:            flags.skipTables,
		FillMissing:           flags.fillMissing,
		Analyze:               !flags.noAnalyze,
		VerifyKeys:            !flags.noVerify,
		VerifyOnly:            flags.verifyOnly,
		TolerateSnapshotDrift: flags.tolerateDrift,
		DryRun:                !r.opts.apply || flags.verifyOnly,
		OpenMode:              r.opts.sqliteOpen,
		Progress:              r.progressFunc(),
	})
	if err != nil {
		return nil, warnings, err
	}
	return report, warnings, nil
}

func (r *cliRuntime) databaseWorkers() int {
	if r.opts.jobs > 0 {
		return r.opts.jobs
	}
	return legacymigrate.DefaultDatabaseWorkers()
}

// printCopyReport renders a merge report as text.
func printCopyReport(w io.Writer, report *legacymigrate.CopyReport, applied bool) {
	for _, database := range report.Databases {
		fmt.Fprintf(w, "legacy database   %s\n", database.Path)
		if database.OpenMode != "" {
			fmt.Fprintf(w, "  open mode %s, size %s, took %dms\n", database.OpenMode, humanBytes(database.SizeBytes), database.DurationMS)
		}
		if database.Err != "" {
			fmt.Fprintf(w, "  error: %s\n", database.Err)
		}
		if len(database.Tables) > 0 {
			fmt.Fprintf(w, "  %-30s %9s %9s %10s %7s  %s\n", "table", "source", "copied", "duplicate", "failed", "status")
			for _, table := range database.Tables {
				fmt.Fprintf(w, "  %-30s %9d %9d %10d %7d  %s", table.Table, table.SourceRows, table.Copied, table.Duplicate, table.Failed, table.Status)
				if table.Reason != "" {
					fmt.Fprintf(w, " (%s)", table.Reason)
				}
				fmt.Fprintln(w)
				if len(table.MissingRequired) > 0 {
					fmt.Fprintf(w, "      ! required Postgres columns missing from the legacy table: %s\n", strings.Join(table.MissingRequired, ", "))
				}
				if table.Sequence != "" {
					fmt.Fprintf(w, "      + %s\n", table.Sequence)
				}
				for _, failure := range table.SampleFailures {
					fmt.Fprintf(w, "      ! %s\n", failure)
				}
				for _, missing := range table.SampleMissingKeys {
					fmt.Fprintf(w, "      ? not found in Postgres: %s\n", missing)
				}
				if table.AltKeyMatched > 0 {
					keys := ""
					if len(table.UniqueKeys) > 1 {
						keys = " under " + strings.Join(table.UniqueKeys[1:], ", ")
					}
					fmt.Fprintf(w, "      ~ %d rows matched a secondary unique key%s\n", table.AltKeyMatched, keys)
					for _, match := range table.SampleAltKeyMatches {
						fmt.Fprintf(w, "        %s\n", match)
					}
				}
				if table.ToleratedKeys > 0 {
					fmt.Fprintf(w, "      ~ tolerated %d missing snapshot keys (%s)\n", table.ToleratedKeys, legacymigrate.SnapshotDriftReason(table.Table))
				}
			}
		}
	}
	fmt.Fprintf(w, "totals            %d copied, %d already present, %d failed rows, %d missing keys, %d matched by a secondary unique key, %d tolerated snapshot keys, %d tables\n",
		report.Copied, report.Duplicate, report.Failed, report.Missing, report.AltKeyMatched, report.Tolerated, report.TablesDone)
	if report.DryRun && !applied {
		fmt.Fprintln(w, "note              dry run; pass --apply to write the rows into Postgres")
	}
	if report.AltKeyMatched > 0 && report.Missing == 0 && report.Failed == 0 {
		fmt.Fprintln(w, "note              every row the primary key did not match exists under another unique key")
	}
	if report.Tolerated > 0 && report.Missing == 0 && report.Failed == 0 {
		fmt.Fprintln(w, "note              every missing key belongs to a runtime snapshot table (upstream_targets, upstream_models)")
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "warning           %s\n", warning)
	}
}
