package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

// runReport aggregates the whole migration pipeline.
type runReport struct {
	Env            *legacymigrate.EnvLoadResult      `json:"env,omitempty"`
	Database       *legacymigrate.CopyReport         `json:"database,omitempty"`
	Rewrite        *legacymigrate.MagicRewriteReport `json:"cassettes_rewrite,omitempty"`
	Check          *legacymigrate.CheckReport        `json:"cassettes_check,omitempty"`
	Archived       []string                          `json:"archived,omitempty"`
	ArchiveRefused string                            `json:"archive_refused,omitempty"`
	Warnings       []string                          `json:"warnings,omitempty"`
}

type runFlags struct {
	skipCassettes bool
	skipArchive   bool
	forceArchive  bool
	fillMissing   bool
	tolerate      bool
	root          string
}

// runOK reports whether the pipeline found anything that needs attention: a
// merge that could not place every row, or a cassette check with errors. A
// refused archive (a dry run, or --skip-archive) is not a failure.
func runOK(report *runReport) bool {
	if report.Database != nil && !report.Database.OK() {
		return false
	}
	if report.Check != nil && report.Check.Failed() {
		return false
	}
	return true
}

func newRunCommand(runtime *cliRuntime) *cobra.Command {
	flags := runFlags{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the whole migration: database, cassettes, then archive",
		Long: `run performs every migration step in order:

  1. merge the legacy SQLite databases into Postgres
  2. rewrite the pre-rename cassette prelude magic
  3. validate the cassette structure
  4. archive the SQLite databases once every row is verified present

The SQLite files are archived only when steps 1 and 3 reported no problems.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.execRun(cmd, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.skipCassettes, "skip-cassettes", false, "skip the cassette rewrite and validation steps")
	cmd.Flags().BoolVar(&flags.skipArchive, "skip-archive", false, "never archive the SQLite databases, even when everything is verified")
	cmd.Flags().BoolVar(&flags.forceArchive, "force-archive", false, "archive without verifying that every row reached Postgres")
	cmd.Flags().BoolVar(&flags.fillMissing, "fill-missing-required", false,
		"insert placeholder values for required Postgres columns the legacy table lacks")
	cmd.Flags().BoolVar(&flags.tolerate, "tolerate-partial", false, "downgrade cassette layout mismatches to warnings")
	cmd.Flags().StringVar(&flags.root, "root", "", "cassette root directory (default: the configured trace output directory)")
	return cmd
}

func (r *cliRuntime) execRun(cmd *cobra.Command, flags runFlags) error {
	env, cfg, err := r.prepare()
	if err != nil {
		return err
	}
	report := &runReport{Env: env}
	if env != nil {
		report.Warnings = append(report.Warnings, env.Warnings...)
	}
	// The pipeline narrates its phases on stdout in text mode; with --format
	// json stdout must stay machine readable, so the narration is discarded and
	// only the JSON envelope below is emitted.
	stdout := cmd.OutOrStdout()
	out := stdout
	if r.jsonOutput() {
		out = io.Discard
	}

	fmt.Fprintf(out, "== 1/4 database ==\n")
	database, warnings, err := r.runDatabaseMerge(cmd, cfg, dbFlags{fillMissing: flags.fillMissing})
	if err != nil {
		return err
	}
	report.Database = database
	report.Warnings = append(report.Warnings, warnings...)
	if database != nil {
		printCopyReport(out, database, r.opts.apply)
	}

	var checkPassed = true
	if !flags.skipCassettes {
		fmt.Fprintf(out, "== 2/4 cassettes rewrite ==\n")
		rootDir, rootWarnings, rootErr := r.resolveCassetteRoot(cfg, flags.root)
		report.Warnings = append(report.Warnings, rootWarnings...)
		if rootErr != nil {
			report.Warnings = append(report.Warnings, rootErr.Error())
			fmt.Fprintf(out, "skipped: %v\n", rootErr)
		} else {
			rewrite, rewriteErr := legacymigrate.RewriteCassetteMagic(cmd.Context(), legacymigrate.MagicRewriteOptions{
				Root:        rootDir,
				Workers:     r.workers(),
				DryRun:      !r.opts.apply,
				VerifyAfter: true,
				Progress:    r.progressFunc(),
			})
			if rewriteErr != nil {
				return rewriteErr
			}
			report.Rewrite = rewrite
			printRewriteReport(out, rewrite)

			fmt.Fprintf(out, "== 3/4 cassettes check ==\n")
			check, checkErr := legacymigrate.CheckCassettes(cmd.Context(), legacymigrate.CheckOptions{
				Root:            rootDir,
				Workers:         r.workers(),
				Progress:        r.progressFunc(),
				ToleratePartial: flags.tolerate,
			})
			if checkErr != nil {
				return checkErr
			}
			report.Check = check
			printCheckReport(out, check)
			checkPassed = !check.Failed()
		}
	}

	fmt.Fprintf(out, "== 4/4 archive ==\n")
	paths, _, err := r.resolveSQLitePaths(cfg)
	if err != nil {
		return err
	}
	switch {
	case flags.skipArchive:
		report.ArchiveRefused = "disabled with --skip-archive"
	case len(paths) == 0:
		report.ArchiveRefused = "no legacy SQLite database found"
	case !r.opts.apply:
		report.ArchiveRefused = "dry run"
	case database != nil && !database.OK():
		report.ArchiveRefused = "the database merge reported rows that need attention"
	case !checkPassed:
		report.ArchiveRefused = "the cassette check reported errors"
	}
	if report.ArchiveRefused != "" {
		fmt.Fprintf(out, "not archived: %s\n", report.ArchiveRefused)
	} else {
		for _, path := range paths {
			moved, archiveErr := legacymigrate.ArchiveSQLiteDatabase(path, ".migrated")
			if archiveErr != nil {
				return archiveErr
			}
			report.Archived = append(report.Archived, moved...)
			for _, entry := range moved {
				fmt.Fprintf(out, "archived          %s\n", entry)
			}
		}
	}

	err = r.writeResult(stdout, "run", runOK(report), report, nil, report.Warnings)
	if err != nil {
		return err
	}
	for _, warning := range report.Warnings {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: "+warning)
	}
	if database != nil && !database.OK() {
		return fail("the database merge reported %d failed rows, %d missing keys and %d tables that need attention",
			database.Failed, database.Missing, database.TablesSkip)
	}
	if !checkPassed {
		return fail("the cassette check reported structural errors")
	}
	return nil
}

// buildInfo is the version payload shared by the text and JSON renderings.
type buildInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Branch  string `json:"branch"`
}

func newVersionCommand(runtime *cliRuntime) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the trajecta-migrate build information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := buildInfo{Name: "trajecta-migrate", Version: Version, Commit: Commit, Date: Date, Branch: Branch}
			return runtime.writeResult(cmd.OutOrStdout(), "version", true, info, func(w io.Writer) error {
				fmt.Fprintf(w, "trajecta-migrate %s (commit %s, built %s, branch %s)\n", info.Version, info.Commit, info.Date, info.Branch)
				return nil
			}, nil)
		},
	}
}
