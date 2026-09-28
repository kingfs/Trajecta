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

func newLayoutCommand(runtime *cliRuntime) *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "layout",
		Short: "Inspect and reorganize the cassette directory layout",
		Long: `layout works on the directory shape of the cassette vault.

The recorder writes "<upstream site host>/<model>/<YYYY>/<MM>/<DD>/<name>.http".
A model name may itself contain "/" (for example "feature/gpt-5.6-sol"), and
recordings that never resolved an upstream have no site segment at all, which is
why a vault can drift between shapes over time.

Only the directory layout changes here; cassette contents and the replay format
are untouched. The move plan is derived from the model name recorded in each
cassette prelude, because that is the only reliable separator between the site
segment and a model path.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&root, "root", "", "cassette root directory (default: the configured trace output directory)")
	cmd.AddCommand(newLayoutPlanCommand(runtime, &root))
	cmd.AddCommand(newLayoutApplyCommand(runtime, &root))
	return cmd
}

func newLayoutPlanCommand(runtime *cliRuntime, root *string) *cobra.Command {
	var (
		unknownSite        string
		outPath            string
		sample             int
		tolerateUnreadable bool
	)
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Report cassettes that are not in <site>/<model>/YYYY/MM/DD (read-only)",
		Long: `plan reads the prelude of every cassette and reports which ones are already in
the current layout and which ones would move, together with the exact target
paths. It never creates, moves, renames or deletes anything.

Cassettes whose directory path holds a site segment but no model segment, or
whose path and recorded model disagree, are reported as ambiguous instead of
being moved: those need a manual decision. Recordings that never resolved an
upstream are planned into "<unknown-site>/<model>/YYYY/MM/DD" because their
prelude URL is relative and the real upstream host is gone.

Reorganizing a vault that a server has already indexed also requires updating
the trace index afterwards (logs.path is the primary key, and
upstream_exchanges.cassette_path may hold the same path). That step is not part
of this read-only command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := runtime.prepare()
			if err != nil {
				return err
			}
			dir, warnings, err := runtime.resolveCassetteRoot(cfg, *root)
			if err != nil {
				return err
			}
			report, err := legacymigrate.PlanCassetteLayout(cmd.Context(), legacymigrate.LayoutOptions{
				Root:        dir,
				UnknownSite: unknownSite,
				Workers:     runtime.workers(),
				Progress:    runtime.progressFunc(),
			})
			if err != nil {
				return err
			}
			if strings.TrimSpace(outPath) != "" {
				if err := writeLayoutPlan(cmd, outPath, report); err != nil {
					return err
				}
				warnings = append(warnings, fmt.Sprintf("full plan written to %s", outPath))
			}
			ok := !report.Failed() || tolerateUnreadable
			if err := runtime.writeResult(cmd.OutOrStdout(), "layout plan", ok, report, func(w io.Writer) error {
				printLayoutPlan(w, report, sample)
				return nil
			}, warnings); err != nil {
				return err
			}
			if report.Failed() && !tolerateUnreadable {
				return fail("%d cassettes could not be classified", report.Unreadable)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&unknownSite, "unknown-site", legacymigrate.DefaultUnknownSite, "directory segment for recordings that never resolved an upstream")
	cmd.Flags().StringVar(&outPath, "out", "", "write the full move plan to a .json or .csv file")
	cmd.Flags().IntVar(&sample, "sample", 10, "how many planned moves to print")
	cmd.Flags().BoolVar(&tolerateUnreadable, "tolerate-unreadable", false, "report cassettes that cannot be classified without failing")
	return cmd
}

// writeLayoutPlan persists the full move list so a later apply run can use it
// verbatim. JSON keeps the whole report; CSV keeps only the moves.
func writeLayoutPlan(cmd *cobra.Command, path string, report *legacymigrate.LayoutReport) error {
	file, err := os.Create(path)
	if err != nil {
		return usageErr("--out", "%s is not writable: %v", path, err)
	}
	defer file.Close()

	switch strings.ToLower(filepath.Ext(path)) {
	case ".csv":
		writer := csv.NewWriter(file)
		if err := writer.Write([]string{"from", "to", "model", "site"}); err != nil {
			return err
		}
		for _, move := range report.Moves {
			if err := writer.Write([]string{move.From, move.To, move.Model, move.Site}); err != nil {
				return err
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return err
		}
	default:
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			return err
		}
	}
	return nil
}

func printLayoutPlan(w io.Writer, report *legacymigrate.LayoutReport, sample int) {
	fmt.Fprintf(w, "cassette root     %s\n", report.Root)
	fmt.Fprintf(w, "cassettes         %d (%s)\n", report.Scanned, humanBytes(report.TotalBytes))
	fmt.Fprintf(w, "  canonical       %d\n", report.Canonical)
	fmt.Fprintf(w, "  site missing    %d  -> %s/<model>/YYYY/MM/DD\n", report.SiteMissing, report.UnknownSite)
	fmt.Fprintf(w, "  model truncated %d  -> %s/<model>/YYYY/MM/DD\n", report.ModelPrefix, report.UnknownSite)
	fmt.Fprintf(w, "  ambiguous       %d  (needs a manual decision)\n", report.Ambiguous)
	if report.Unreadable > 0 {
		fmt.Fprintf(w, "  unreadable      %d\n", report.Unreadable)
	}
	fmt.Fprintf(w, "prelude magic\n")
	fmt.Fprintf(w, "  current         %d\n", report.CurrentMagic)
	fmt.Fprintf(w, "  legacy          %d\n", report.LegacyMagic)
	if report.OtherMagic > 0 {
		fmt.Fprintf(w, "  other formats   %d\n", report.OtherMagic)
	}
	fmt.Fprintf(w, "planned moves     %d files, %s\n", report.MovesPlanned, humanBytes(report.MovesBytes))
	fmt.Fprintf(w, "took              %dms\n", report.DurationMS)

	if sample > 0 && len(report.Moves) > 0 {
		fmt.Fprintf(w, "sample moves\n")
		for i, move := range report.Moves {
			if i >= sample {
				break
			}
			fmt.Fprintf(w, "  %s\n    -> %s\n", move.From, move.To)
		}
		if int64(sample) < report.MovesPlanned {
			fmt.Fprintf(w, "  (%d more; pass --out plan.json to keep every move)\n", report.MovesPlanned-int64(sample))
		}
	}
	if len(report.AmbiguousPaths) > 0 {
		fmt.Fprintf(w, "ambiguous\n")
		for _, move := range report.AmbiguousPaths {
			fmt.Fprintf(w, "  %s (model=%s site=%s)\n", move.From, move.Model, move.Site)
		}
	}
	printFailures(w, report.Failures)
	fmt.Fprintln(w, "note              read-only; nothing was created, moved or deleted")
}
