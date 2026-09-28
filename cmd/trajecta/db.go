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
	return cmd
}

func (r *cliRuntime) execDB(cmd *cobra.Command, flags dbFlags) error {
	env, cfg, err := r.prepare()
	if err != nil {
		return err
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
				if table.ToleratedKeys > 0 {
					fmt.Fprintf(w, "      ~ tolerated %d missing snapshot keys (%s)\n", table.ToleratedKeys, legacymigrate.SnapshotDriftReason(table.Table))
				}
			}
		}
	}
	fmt.Fprintf(w, "totals            %d copied, %d already present, %d failed rows, %d missing keys, %d tolerated snapshot keys, %d tables\n",
		report.Copied, report.Duplicate, report.Failed, report.Missing, report.Tolerated, report.TablesDone)
	if report.DryRun && !applied {
		fmt.Fprintln(w, "note              dry run; pass --apply to write the rows into Postgres")
	}
	if report.Tolerated > 0 && report.Missing == 0 && report.Failed == 0 {
		fmt.Fprintln(w, "note              every missing key belongs to a runtime snapshot table (upstream_targets, upstream_models)")
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "warning           %s\n", warning)
	}
}
