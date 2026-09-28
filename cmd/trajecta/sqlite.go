package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

// archiveReport is the machine-readable result of `sqlite archive`.
type archiveReport struct {
	DryRun       bool                      `json:"dry_run"`
	Verification *legacymigrate.CopyReport `json:"verification,omitempty"`
	Archived     []string                  `json:"archived,omitempty"`
	Planned      []string                  `json:"planned,omitempty"`
}

func newSQLiteCommand(runtime *cliRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sqlite",
		Short: "Inspect and archive the legacy SQLite databases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newSQLiteListCommand(runtime))
	cmd.AddCommand(newSQLiteArchiveCommand(runtime))
	return cmd
}

func newSQLiteListCommand(runtime *cliRuntime) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the legacy SQLite databases that would be migrated",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := runtime.prepare()
			if err != nil {
				return err
			}
			paths, warnings, err := runtime.resolveSQLitePaths(cfg)
			if err != nil {
				return err
			}
			candidates := make([]legacymigrate.SQLiteCandidate, 0, len(paths))
			for _, path := range paths {
				info, statErr := os.Stat(path)
				if statErr != nil {
					continue
				}
				candidates = append(candidates, legacymigrate.SQLiteCandidate{
					Path: path, Size: info.Size(), ModTime: info.ModTime(),
				})
			}
			return runtime.writeResult(cmd.OutOrStdout(), "sqlite list", true, map[string]any{
				"databases": candidates,
			}, func(w io.Writer) error {
				if len(candidates) == 0 {
					fmt.Fprintln(w, "no legacy SQLite application database was found")
				}
				for _, candidate := range candidates {
					fmt.Fprintf(w, "  %-60s %10s  %s\n", candidate.Path, humanBytes(candidate.Size), candidate.ModTime.Format("2006-01-02 15:04"))
				}
				return nil
			}, warnings)
		},
	}
}

func newSQLiteArchiveCommand(runtime *cliRuntime) *cobra.Command {
	var (
		suffix        string
		force         bool
		tolerateDrift bool
	)
	cmd := &cobra.Command{
		Use:   "archive",
		Short: "Rename the legacy SQLite databases once their rows exist in Postgres",
		Long: `archive renames every legacy SQLite database to <name>.migrated.

Before touching anything it verifies that every primary key stored in the
SQLite files already exists in Postgres, so a database whose rows were not
merged is never archived. Files are renamed, never deleted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.execArchive(cmd, suffix, force, tolerateDrift)
		},
	}
	cmd.Flags().StringVar(&suffix, "suffix", ".migrated", "suffix appended to the archived database file name")
	cmd.Flags().BoolVar(&force, "force", false, "archive without verifying that every row reached Postgres")
	cmd.Flags().BoolVar(&tolerateDrift, "tolerate-snapshot-drift", false,
		"excuse missing keys in the runtime snapshot tables (upstream_targets, upstream_models) that the running server rewrites")
	return cmd
}

func (r *cliRuntime) execArchive(cmd *cobra.Command, suffix string, force, tolerateDrift bool) error {
	env, cfg, err := r.prepare()
	if err != nil {
		return err
	}
	paths, warnings, err := r.resolveSQLitePaths(cfg)
	if env != nil {
		warnings = append(warnings, env.Warnings...)
	}
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "no legacy SQLite application database was found; nothing to archive")
		return r.writeResult(cmd.OutOrStdout(), "sqlite archive", true, &archiveReport{DryRun: !r.opts.apply}, func(w io.Writer) error {
			fmt.Fprintln(w, "nothing to archive: no legacy SQLite application database was found")
			return nil
		}, warnings)
	}

	report := &archiveReport{DryRun: !r.opts.apply}
	var verification *legacymigrate.CopyReport
	if !force {
		if strings.TrimSpace(cfg.Database.DSN) == "" {
			return usageErr("database.dsn", "no Postgres DSN is configured, so the archive gate cannot verify the rows")
		}
		verification, err = legacymigrate.MergeSQLiteIntoPostgres(cmd.Context(), legacymigrate.CopyOptions{
			SQLitePaths:           paths,
			PostgresDSN:           cfg.Database.DSN,
			VerifyOnly:            true,
			BatchSize:             r.opts.batchSize,
			Workers:               r.databaseWorkers(),
			OpenMode:              r.opts.sqliteOpen,
			TolerateSnapshotDrift: tolerateDrift,
			Progress:              r.progressFunc(),
		})
		if err != nil {
			return err
		}
		report.Verification = verification
		if !verification.OK() {
			if writeErr := r.writeResult(cmd.OutOrStdout(), "sqlite archive", false, report, func(w io.Writer) error {
				printCopyReport(w, verification, false)
				fmt.Fprintln(w, "refused           the verification found rows that are not in Postgres")
				if !tolerateDrift {
					fmt.Fprintln(w, "hint              rows the running server replaced in a runtime snapshot table can be excused with --tolerate-snapshot-drift")
				}
				return nil
			}, warnings); writeErr != nil {
				return writeErr
			}
			hint := "(pass --force to archive anyway)"
			if !tolerateDrift {
				hint = "(pass --tolerate-snapshot-drift for runtime snapshot drift, or --force to archive anyway)"
			}
			return fail("refusing to archive: %d missing keys and %d tables need attention %s",
				verification.Missing, verification.TablesSkip, hint)
		}
	}

	for _, path := range paths {
		if !r.opts.apply {
			report.Planned = append(report.Planned, path+" -> "+path+suffix)
			continue
		}
		moved, archiveErr := legacymigrate.ArchiveSQLiteDatabase(path, suffix)
		if archiveErr != nil {
			return archiveErr
		}
		report.Archived = append(report.Archived, moved...)
	}

	return r.writeResult(cmd.OutOrStdout(), "sqlite archive", true, report, func(w io.Writer) error {
		if report.Verification != nil {
			printCopyReport(w, report.Verification, false)
		}
		for _, planned := range report.Planned {
			fmt.Fprintf(w, "would archive     %s\n", planned)
		}
		for _, archived := range report.Archived {
			fmt.Fprintf(w, "archived          %s\n", archived)
		}
		if !r.opts.apply {
			fmt.Fprintln(w, "note              dry run; pass --apply to rename the files")
		}
		return nil
	}, warnings)
}
