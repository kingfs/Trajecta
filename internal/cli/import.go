package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/transfer"
)

type importOptions struct {
	configPath        string
	format            string
	stdout            io.Writer
	password          string
	apply             bool
	channelMap        []string
	channelStrategy   string
	keepExistingFiles bool
	logFile           string
}

func newImportCommand(rt *Runtime) *cobra.Command {
	var opts importOptions
	cmd := &cobra.Command{
		Use:   "import <bundle>",
		Short: "Merge an exported bundle into this deployment",
		Long: `Merge one export bundle into this deployment's database and cassette vault.

The import is incremental and can be run repeatedly. A row or file that is
already present and unchanged is skipped; one that changed is updated; nothing
is ever deleted. Importing the same bundle twice therefore writes nothing the
second time, and importing a newer bundle only applies the delta.

Channels are matched by base_url plus API key, not by name: the same endpoint
with the same credential is the same channel and is reused (its models are
unioned), while the same endpoint with a different credential becomes a new
channel. Use --channel-map to force a source channel onto a target one.

Cassettes are written under this deployment's trace output directory with their
recorded modification times preserved, so the next index sync does not re-index
the whole imported set. Session summaries are rebuilt after the merge, which is
what makes the imported sessions queryable in the Monitor's history views.

Without --apply the command only reports what it would do. Stop the server
before importing.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.configPath = rt.ConfigPath()
			opts.format = rt.OutputFormat()
			opts.stdout = cmd.OutOrStdout()
			return RunCode(func() int { return runImportWithOptions(opts, args[0]) })
		},
	}
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "Write the merge (default is a dry run)")
	cmd.Flags().StringVar(&opts.password, "password", "", "Bundle password (env TRAJECTA_IMPORT_PASSWORD)")
	cmd.Flags().StringArrayVar(&opts.channelMap, "channel-map", nil, "Force source channel id onto a target id: <source-id>=<target-id> (repeatable)")
	cmd.Flags().StringVar(&opts.channelStrategy, "channel-strategy", "reuse", "Channel merge strategy: reuse (match by base_url+api key) or create")
	cmd.Flags().BoolVar(&opts.keepExistingFiles, "keep-existing-files", false, "Keep a cassette that differs from the bundle instead of overwriting it")
	cmd.Flags().StringVar(&opts.logFile, "log-file", "", "Write the full per-item log to this file as well")
	return cmd
}

func runImportWithOptions(opts importOptions, bundle string) int {
	cfg, err := appconfig.Load(opts.configPath)
	if err != nil {
		slog.Error("Failed to load config", "path", opts.configPath, "error", err)
		return 1
	}
	channelMap := map[string]string{}
	for _, spec := range opts.channelMap {
		source, target, ok := strings.Cut(spec, "=")
		source = strings.TrimSpace(source)
		target = strings.TrimSpace(target)
		if !ok || source == "" || target == "" {
			slog.Error("Invalid --channel-map", "value", spec, "want", "<source-id>=<target-id>")
			return 1
		}
		channelMap[source] = target
	}

	st, closeStore, code := openTraceStoreForCommand(opts.configPath)
	if code != 0 {
		return code
	}
	defer closeStore()

	log, err := transfer.NewLogger(opts.stdout, opts.logFile)
	if err != nil {
		slog.Error("Open transfer log failed", "error", err)
		return 1
	}
	defer log.Close()

	password := strings.TrimSpace(opts.password)
	if password == "" {
		password = strings.TrimSpace(os.Getenv("TRAJECTA_IMPORT_PASSWORD"))
	}

	result, err := transfer.Import(context.Background(), st, cfg.TraceOutputDir(), transfer.ImportOptions{
		Bundle:            bundle,
		Password:          password,
		Apply:             opts.apply,
		ChannelMap:        channelMap,
		ChannelStrategy:   opts.channelStrategy,
		KeepExistingFiles: opts.keepExistingFiles,
		Log:               log,
	})
	if err != nil {
		slog.Error("Import failed", "error", err)
		return 1
	}

	if err := WriteResultWithWarnings(opts.stdout, opts.format, "import", result, result.Warnings, func(w io.Writer) error {
		mode := "applied"
		if result.DryRun {
			mode = "dry run (no changes written; re-run with --apply)"
		}
		fmt.Fprintf(w, "mode: %s\n", mode)
		fmt.Fprintf(w, "bundle: %s\n", result.Bundle)
		fmt.Fprintf(w, "duration: %s\n", result.Duration.Round(time.Millisecond))
		fmt.Fprintf(w, "\nchannels\n")
		if len(result.ChannelReuses) == 0 && len(result.ChannelsCreated) == 0 {
			fmt.Fprintf(w, "  none\n")
		}
		for _, reuse := range result.ChannelReuses {
			fmt.Fprintf(w, "  skip %-28q -> reuse %-28q (%s)\n", reuse.SourceName, reuse.TargetName, reuse.Matched)
		}
		for _, created := range result.ChannelsCreated {
			fmt.Fprintf(w, "  create %s\n", created)
		}
		fmt.Fprintf(w, "\ntables\n")
		for _, table := range result.Tables {
			if table.Processed == 0 {
				continue
			}
			fmt.Fprintf(w, "  %-32s processed %-8d inserted %-8d updated %-8d unchanged %-8d conflict %-8d trace-skip %d\n",
				table.Name, table.Processed, table.Inserted, table.Updated, table.SkippedUnchanged, table.SkippedConflict, table.SkippedTrace)
		}
		fmt.Fprintf(w, "\ncassettes\n")
		fmt.Fprintf(w, "  copied %d, updated %d, identical %d, conflicts %d, missing %d\n",
			result.Cassettes.Copied, result.Cassettes.Updated, result.Cassettes.SkippedIdentical,
			result.Cassettes.Conflicts, result.Cassettes.Missing)
		for _, sample := range result.Cassettes.ConflictSamples {
			fmt.Fprintf(w, "  conflict: %s\n", sample)
		}
		fmt.Fprintf(w, "\nsessions\n")
		if result.SessionRebuilt {
			fmt.Fprintf(w, "  summaries rebuilt in %s\n", result.SessionRebuildDur.Round(time.Millisecond))
		} else if result.DryRun {
			fmt.Fprintf(w, "  summaries will be rebuilt after the merge\n")
		} else {
			fmt.Fprintf(w, "  NOT rebuilt; run `trajecta db summary rebuild sessions`\n")
		}
		return nil
	}); err != nil {
		slog.Error("Write import result failed", "error", err)
		return 1
	}
	return 0
}
