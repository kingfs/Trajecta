package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

func newLayoutApplyCommand(runtime *cliRuntime, root *string) *cobra.Command {
	var (
		planPath    string
		dbPrefix    string
		outPath     string
		sample      int
		apply       bool
		verifyModel bool
		onlyModels  []string
		limit       int
		noDB        bool
	)
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Move planned cassettes and repoint the trace index (dry run by default)",
		Long: `apply executes the move plan produced by "layout plan".

Each planned move renames one cassette and, in the same step, repoints every
index row that stores its path: logs.path (the primary key of the trace index),
upstream_exchanges.cassette_path and overview_metric_bucket_members.path.
logs.trace_id is never touched, so parse jobs, observations, findings, analysis
jobs and session summaries stay attached to their trace.

The plan is read from the file written by "layout plan --out" (.json or .csv).
A move is applied only when the source exists and the target does not; a failed
index update moves the file back, so the filesystem and the database never
disagree. Re-running the same plan is safe: files that are already at their
target are reported as already-applied, and a half-finished move (file moved,
index not yet updated) is repaired.

Nothing is moved without --apply. The database prefix defaults to --root; set
--db-prefix when the server stores a different absolute path (for example a
server running in a container stores /app/data/traces/...).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := runtime.prepare()
			if err != nil {
				return err
			}
			if strings.TrimSpace(planPath) == "" {
				return usageErr("--plan", "a plan file is required; create one with `trajecta layout plan --out plan.json`")
			}
			dir, warnings, err := runtime.resolveCassetteRoot(cfg, *root)
			if err != nil {
				return err
			}
			moves, err := readLayoutPlan(planPath)
			if err != nil {
				return err
			}

			dsn := ""
			if !noDB {
				if !strings.EqualFold(strings.TrimSpace(cfg.Database.Driver), "postgres") {
					return usageErr("--no-db", "the trace index must be Postgres, but the configuration resolves to %q; set TRAJECTA_DATABASE_DRIVER=postgres and TRAJECTA_DATABASE_DSN, or pass --no-db to move files without updating the index", cfg.Database.Driver)
				}
				dsn = cfg.Database.DSN
				if strings.TrimSpace(dsn) == "" {
					return usageErr("--no-db", "the Postgres DSN is empty; set TRAJECTA_DATABASE_DSN, or pass --no-db to move files without updating the index")
				}
			} else {
				warnings = append(warnings, "running with --no-db: cassette files move but the trace index is not updated")
			}

			report, err := legacymigrate.ApplyCassetteLayout(cmd.Context(), legacymigrate.ApplyOptions{
				Root:            dir,
				DBPrefix:        dbPrefix,
				PostgresDSN:     dsn,
				RequireDatabase: !noDB && apply,
				Moves:           moves,
				Workers:         runtime.workers(),
				Progress:        runtime.progressFunc(),
				DryRun:          !apply,
				VerifyModel:     verifyModel,
				OnlyModels:      onlyModels,
				Limit:           limit,
				Sample:          sample,
			})
			if err != nil {
				return err
			}
			if strings.TrimSpace(outPath) != "" {
				if err := writeLayoutApplyReport(outPath, report); err != nil {
					return err
				}
				warnings = append(warnings, fmt.Sprintf("full report written to %s", outPath))
			}
			ok := !report.Failed()
			if err := runtime.writeResult(cmd.OutOrStdout(), "layout apply", ok, report, func(w io.Writer) error {
				printLayoutApply(w, report, sample)
				return nil
			}, warnings); err != nil {
				return err
			}
			if report.Failed() {
				return fail("%d planned moves failed; nothing else was changed", report.FailedMoves)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&planPath, "plan", "", "move plan written by `layout plan --out` (.json or .csv)")
	cmd.Flags().BoolVar(&apply, "apply", false, "execute the plan (default: report what would happen)")
	cmd.Flags().StringVar(&dbPrefix, "db-prefix", "", "path prefix the database stores for --root (default: --root itself)")
	cmd.Flags().BoolVar(&noDB, "no-db", false, "move files without updating the trace index")
	cmd.Flags().BoolVar(&verifyModel, "verify-model", true, "re-read each prelude and refuse moves whose recorded model changed")
	cmd.Flags().StringArrayVar(&onlyModels, "only-model", nil, "only apply moves for these recorded models (repeatable)")
	cmd.Flags().IntVar(&limit, "limit", 0, "apply at most this many moves (0 = all)")
	cmd.Flags().StringVar(&outPath, "out", "", "write the full apply report to a .json or .csv file")
	cmd.Flags().IntVar(&sample, "sample", 10, "how many applied moves to print")
	return cmd
}

// readLayoutPlan accepts the JSON report written by `layout plan --out`, a bare
// JSON array of moves, or the CSV form.
func readLayoutPlan(path string) ([]legacymigrate.LayoutMove, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, usageErr("--plan", "%s is not readable: %v", path, err)
	}
	defer file.Close()

	if strings.EqualFold(filepath.Ext(path), ".csv") {
		reader := csv.NewReader(file)
		header, err := reader.Read()
		if err != nil {
			return nil, usageErr("--plan", "%s is not a move plan: %v", path, err)
		}
		index := map[string]int{}
		for i, name := range header {
			index[strings.ToLower(strings.TrimSpace(name))] = i
		}
		fromIdx, okFrom := index["from"]
		toIdx, okTo := index["to"]
		if !okFrom || !okTo {
			return nil, usageErr("--plan", "%s needs a from,to header", path)
		}
		var moves []legacymigrate.LayoutMove
		for {
			record, err := reader.Read()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, usageErr("--plan", "%s: %v", path, err)
			}
			move := legacymigrate.LayoutMove{From: record[fromIdx], To: record[toIdx]}
			if i, ok := index["model"]; ok && i < len(record) {
				move.Model = record[i]
			}
			if i, ok := index["site"]; ok && i < len(record) {
				move.Site = record[i]
			}
			moves = append(moves, move)
		}
		if len(moves) == 0 {
			return nil, usageErr("--plan", "%s holds no moves", path)
		}
		return moves, nil
	}

	content, err := io.ReadAll(file)
	if err != nil {
		return nil, usageErr("--plan", "%s is not readable: %v", path, err)
	}
	var report legacymigrate.LayoutReport
	if err := json.Unmarshal(content, &report); err == nil && len(report.Moves) > 0 {
		if report.MovesTruncated {
			return nil, usageErr("--plan", "%s was written with --sample only and does not hold every move; regenerate it without --sample", path)
		}
		return report.Moves, nil
	}
	var moves []legacymigrate.LayoutMove
	if err := json.Unmarshal(content, &moves); err != nil {
		return nil, usageErr("--plan", "%s is not a layout plan: %v", path, err)
	}
	if len(moves) == 0 {
		return nil, usageErr("--plan", "%s holds no moves", path)
	}
	return moves, nil
}

func writeLayoutApplyReport(path string, report *legacymigrate.LayoutApplyReport) error {
	file, err := os.Create(path)
	if err != nil {
		return usageErr("--out", "%s is not writable: %v", path, err)
	}
	defer file.Close()

	if strings.EqualFold(filepath.Ext(path), ".csv") {
		writer := csv.NewWriter(file)
		if err := writer.Write([]string{"status", "from", "to", "model", "logs_rows", "exchange_rows", "overview_rows"}); err != nil {
			return err
		}
		for _, sample := range report.Samples {
			if err := writer.Write([]string{
				sample.Status, sample.From, sample.To, sample.Model,
				fmt.Sprint(sample.Refs.Logs), fmt.Sprint(sample.Refs.Exchanges), fmt.Sprint(sample.Refs.OverviewMembers),
			}); err != nil {
				return err
			}
		}
		for _, failure := range report.Failures {
			if err := writer.Write([]string{"failed:" + failure.Stage, failure.From, failure.To, "", "", "", ""}); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(file, "# %s: %s\n", failure.Stage, failure.Message); err != nil {
				return err
			}
		}
		writer.Flush()
		return writer.Error()
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

func printLayoutApply(w io.Writer, report *legacymigrate.LayoutApplyReport, sample int) {
	mode := "apply"
	if report.DryRun {
		mode = "dry run"
	}
	fmt.Fprintf(w, "mode              %s\n", mode)
	fmt.Fprintf(w, "cassette root     %s\n", report.Root)
	fmt.Fprintf(w, "database prefix   %s\n", report.DBPrefix)
	fmt.Fprintf(w, "trace index       %s\n", map[bool]string{true: "postgres (repointed per move)", false: "not used"}[report.Database])
	fmt.Fprintf(w, "plan              %d moves, %d selected", report.Planned, report.Selected)
	if report.Remaining > 0 {
		fmt.Fprintf(w, ", %d left for a later run (--limit)", report.Remaining)
	}
	fmt.Fprintln(w)
	verb := "moved"
	if report.DryRun {
		verb = "would move"
	}
	fmt.Fprintf(w, "  %-15s %d files, %s\n", verb, report.Moved, humanBytes(report.MovedBytes))
	fmt.Fprintf(w, "  %-15s %d\n", "index repaired", report.Resumed)
	fmt.Fprintf(w, "  %-15s %d\n", "already applied", report.AlreadyApplied)
	fmt.Fprintf(w, "  %-15s %d\n", "missing source", report.MissingSource)
	fmt.Fprintf(w, "  %-15s %d\n", "target occupied", report.TargetExists)
	if report.FailedMoves > 0 {
		fmt.Fprintf(w, "  %-15s %d\n", "failed", report.FailedMoves)
	}
	if report.Database {
		label := "index rows"
		if report.DryRun {
			label = "index rows (would be repointed)"
		}
		fmt.Fprintf(w, "%s\n", label)
		fmt.Fprintf(w, "  %-15s %d\n", "rows", report.DatabaseRows)
		fmt.Fprintf(w, "  %-15s %d\n", "no logs row", report.CassettesWithoutIndex)
	}
	fmt.Fprintf(w, "took              %dms\n", report.DurationMS)

	if sample > 0 && len(report.Samples) > 0 {
		fmt.Fprintf(w, "sample\n")
		for i, applied := range report.Samples {
			if i >= sample {
				break
			}
			fmt.Fprintf(w, "  [%s] %s\n    -> %s", applied.Status, applied.From, applied.To)
			if applied.Refs.Rows() > 0 {
				fmt.Fprintf(w, " (index rows: logs=%d exchanges=%d overview=%d)", applied.Refs.Logs, applied.Refs.Exchanges, applied.Refs.OverviewMembers)
			}
			fmt.Fprintln(w)
		}
	}
	if len(report.Failures) > 0 {
		fmt.Fprintf(w, "failures\n")
		for _, failure := range report.Failures {
			fmt.Fprintf(w, "  %s (%s): %s\n", failure.From, failure.Stage, failure.Message)
		}
	}
	if report.Note != "" {
		fmt.Fprintf(w, "note              %s\n", report.Note)
	}
}
