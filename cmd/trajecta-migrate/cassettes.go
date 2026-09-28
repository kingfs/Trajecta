package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

func newCassettesCommand(runtime *cliRuntime) *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "cassettes",
		Short: "Normalize and validate recorded .http cassettes",
		Long: `cassettes operates on the raw .http recordings.

The pre-rename prelude magic "# llm-tracelab/v3" is still accepted by every
reader, so rewriting it is optional; it only makes the vault consistent with
what current writers emit. Rewriting never touches the recorded payload.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().StringVar(&root, "root", "", "cassette root directory (default: the configured trace output directory)")
	cmd.AddCommand(newCassetteCensusCommand(runtime, &root))
	cmd.AddCommand(newCassetteRewriteCommand(runtime, &root))
	cmd.AddCommand(newCassetteCheckCommand(runtime, &root))
	return cmd
}

// resolveCassetteRoot picks the cassette directory to operate on.
func (r *cliRuntime) resolveCassetteRoot(cfg *config.Config, override string) (string, []string, error) {
	override = strings.TrimSpace(override)
	if override != "" {
		info, err := os.Stat(override)
		if err != nil {
			return "", nil, usageErr("--root", "%s is not readable: %v", override, err)
		}
		if !info.IsDir() {
			return "", nil, usageErr("--root", "%s is not a directory", override)
		}
		return override, nil, nil
	}
	dirs, warnings := r.resolveTraceDirs(cfg)
	for _, dir := range dirs {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, warnings, nil
		}
	}
	return "", warnings, usageErr("--root", "no cassette directory found; pass --root or --trace-dir")
}

func newCassetteCensusCommand(runtime *cliRuntime, root *string) *cobra.Command {
	return &cobra.Command{
		Use:     "census",
		Aliases: []string{"version"},
		Short:   "Count cassette prelude formats (fast, read-only)",
		Long: `census reads the first bytes of every cassette and reports how many use the
current magic, the pre-rename magic, or the legacy LLM_PROXY_V2 layout. The
recorded payload is never read, so this is the cheapest way to size the work.`,
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
			report, err := legacymigrate.CensusCassetteMagic(cmd.Context(), legacymigrate.MagicCensusOptions{
				Root:     dir,
				Workers:  runtime.workers(),
				Progress: runtime.progressFunc(),
			})
			if err != nil {
				return err
			}
			if err := runtime.writeResult(cmd.OutOrStdout(), "cassettes census", report.Errors == 0, report, func(w io.Writer) error {
				printCensusReport(w, report)
				return nil
			}, warnings); err != nil {
				return err
			}
			if report.Errors > 0 {
				return fail("%d cassettes could not be read", report.Errors)
			}
			return nil
		},
	}
}

func newCassetteRewriteCommand(runtime *cliRuntime, root *string) *cobra.Command {
	var verify bool
	cmd := &cobra.Command{
		Use:   "rewrite",
		Short: "Replace the pre-rename prelude magic with the current one",
		Long: `rewrite replaces only the first line of a cassette when it is exactly
"# llm-tracelab/v3". The recorded HTTP payload is copied byte for byte and the
file is replaced atomically through a temporary file in the same directory, so
an interrupted run can never truncate a cassette.`,
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
			if runtime.opts.apply {
				if removed, cleanupErr := legacymigrate.CleanupStaleTempFiles(cmd.Context(), dir); cleanupErr == nil && removed > 0 {
					fmt.Fprintf(cmd.ErrOrStderr(), "removed %d temporary file(s) left by an interrupted run\n", removed)
				}
			}
			report, err := legacymigrate.RewriteCassetteMagic(cmd.Context(), legacymigrate.MagicRewriteOptions{
				Root:        dir,
				Workers:     runtime.workers(),
				DryRun:      !runtime.opts.apply,
				VerifyAfter: verify,
				Progress:    runtime.progressFunc(),
			})
			if err != nil {
				return err
			}
			if err := runtime.writeResult(cmd.OutOrStdout(), "cassettes rewrite", rewriteOK(report), report, func(w io.Writer) error {
				printRewriteReport(w, report)
				return nil
			}, warnings); err != nil {
				return err
			}
			if report.Errors > 0 {
				return fail("%d cassettes could not be rewritten", report.Errors)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", true, "re-read the first line of every rewritten cassette")
	return cmd
}

// rewriteOK reports whether every cassette that needed a rewrite was rewritten.
func rewriteOK(report *legacymigrate.MagicRewriteReport) bool {
	return report == nil || report.Errors == 0
}

func newCassetteCheckCommand(runtime *cliRuntime, root *string) *cobra.Command {
	var (
		failOnLegacy    bool
		failOnV2        bool
		strict          bool
		toleratePartial bool
		maxIssues       int
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate cassette structure without reading the payload",
		Long: `check validates the prelude of every cassette: the magic line, the meta and
event JSON, and the declared layout lengths against the real file size. The
recorded HTTP payload is never read or interpreted, so partial recordings are
reported as layout mismatches instead of being parsed.`,
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
			report, err := legacymigrate.CheckCassettes(cmd.Context(), legacymigrate.CheckOptions{
				Root:            dir,
				Workers:         runtime.workers(),
				Progress:        runtime.progressFunc(),
				FailOnLegacy:    failOnLegacy,
				FailOnV2:        failOnV2,
				Strict:          strict,
				ToleratePartial: toleratePartial,
				MaxIssues:       maxIssues,
			})
			if err != nil {
				return err
			}
			if err := runtime.writeResult(cmd.OutOrStdout(), "cassettes check", !report.Failed(), report, func(w io.Writer) error {
				printCheckReport(w, report)
				return nil
			}, warnings); err != nil {
				return err
			}
			if report.Failed() {
				return fail("%d cassettes failed the structural check", report.Errors)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&failOnLegacy, "fail-on-legacy", false, "treat the pre-rename magic as an error instead of a warning")
	cmd.Flags().BoolVar(&failOnV2, "fail-on-v2", false, "treat legacy LLM_PROXY_V2 recordings as errors")
	cmd.Flags().BoolVar(&strict, "strict", false, "treat every warning as an error")
	cmd.Flags().BoolVar(&toleratePartial, "tolerate-partial", false, "downgrade layout mismatches to warnings (interrupted recordings)")
	cmd.Flags().IntVar(&maxIssues, "max-issues", 200, "maximum number of issues to report")
	return cmd
}

func printCensusReport(w io.Writer, report *legacymigrate.MagicCensusReport) {
	fmt.Fprintf(w, "cassette root     %s\n", report.Root)
	fmt.Fprintf(w, "cassettes         %d (%s)\n", report.Scanned, humanBytes(report.TotalBytes))
	fmt.Fprintf(w, "  current magic   %d\n", report.Current)
	fmt.Fprintf(w, "  legacy magic    %d\n", report.Legacy)
	fmt.Fprintf(w, "  legacy v2       %d\n", report.V2)
	fmt.Fprintf(w, "  unknown         %d\n", report.Unknown)
	if report.Errors > 0 {
		fmt.Fprintf(w, "  unreadable      %d\n", report.Errors)
	}
	fmt.Fprintf(w, "took              %dms\n", report.DurationMS)
	printFailures(w, report.Failures)
}

func printRewriteReport(w io.Writer, report *legacymigrate.MagicRewriteReport) {
	fmt.Fprintf(w, "cassette root     %s\n", report.Root)
	fmt.Fprintf(w, "scanned           %d\n", report.Scanned)
	fmt.Fprintf(w, "rewritten         %d\n", report.Rewritten)
	fmt.Fprintf(w, "already current   %d\n", report.Current)
	fmt.Fprintf(w, "other formats     %d\n", report.Other)
	if report.Errors > 0 {
		fmt.Fprintf(w, "errors            %d\n", report.Errors)
	}
	fmt.Fprintf(w, "bytes rewritten   %s\n", humanBytes(report.BytesCopied))
	fmt.Fprintf(w, "took              %dms\n", report.DurationMS)
	if report.DryRun {
		fmt.Fprintln(w, "note              dry run; pass --apply to rewrite the cassettes")
	}
	printFailures(w, report.Failures)
}

func printCheckReport(w io.Writer, report *legacymigrate.CheckReport) {
	fmt.Fprintf(w, "cassette root     %s\n", report.Root)
	fmt.Fprintf(w, "scanned           %d\n", report.Scanned)
	fmt.Fprintf(w, "clean             %d\n", report.OK)
	fmt.Fprintf(w, "  current magic   %d\n", report.Current)
	fmt.Fprintf(w, "  legacy magic    %d\n", report.Legacy)
	fmt.Fprintf(w, "  legacy v2       %d\n", report.V2)
	fmt.Fprintf(w, "  unknown         %d\n", report.Unknown)
	fmt.Fprintf(w, "errors            %d\n", report.Errors)
	fmt.Fprintf(w, "warnings          %d\n", report.Warnings)
	fmt.Fprintf(w, "took              %dms\n", report.DurationMS)
	for _, issue := range report.Issues {
		fmt.Fprintf(w, "  [%s] %s: %s\n", issue.Severity, issue.Code, issue.Path)
		fmt.Fprintf(w, "      %s\n", issue.Message)
	}
	if report.IssuesTruncated {
		fmt.Fprintln(w, "  (more issues were found than reported)")
	}
}

func printFailures(w io.Writer, failures []legacymigrate.FileFailure) {
	for _, failure := range failures {
		fmt.Fprintf(w, "  ! %s: %s\n", failure.Path, failure.Message)
	}
}
