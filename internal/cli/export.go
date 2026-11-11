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

type exportOptions struct {
	configPath     string
	format         string
	stdout         io.Writer
	out            string
	since          string
	until          string
	all            bool
	password       string
	allowPlaintext bool
	compression    string
	force          bool
	sourceName     string
	logFile        string
}

func newExportCommand(rt *Runtime) *cobra.Command {
	var opts exportOptions
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export recorded data into a portable bundle for another trajecta deployment",
		Long: `Export a time window of recordings (or everything) into one bundle file.

The bundle carries the trace index, the recorded cassettes, the channel and
model configuration, the LLM analysis results, the audit trail and the
evaluation data. Derived tables that a cassette reparse can rebuild are not
exported.

The bundle is encrypted with a password derived through scrypt. When --password
is omitted, a random password is generated and printed once at the end of the
command; keep it, because it is not stored anywhere.

Secrets (channel API keys, masked credential values) are decrypted with this
deployment's local key on the way out and re-encrypted with the target's key on
import, so the target never has to share this deployment's key file.

Stop the server before exporting so the index and the cassettes cannot move
under the export.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.configPath = rt.ConfigPath()
			opts.format = rt.OutputFormat()
			opts.stdout = cmd.OutOrStdout()
			return RunCode(func() int { return runExportWithOptions(opts, rt.version) })
		},
	}
	cmd.Flags().StringVar(&opts.out, "out", "", "Bundle file to write (required)")
	cmd.Flags().StringVar(&opts.since, "since", "", "Include recordings at or after this time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().StringVar(&opts.until, "until", "", "Include recordings at or before this time (RFC3339 or YYYY-MM-DD)")
	cmd.Flags().BoolVar(&opts.all, "all", false, "Export every recording instead of a time window")
	cmd.Flags().StringVar(&opts.password, "password", "", "Bundle password (default: generate a random one; env TRAJECTA_EXPORT_PASSWORD)")
	cmd.Flags().BoolVar(&opts.allowPlaintext, "allow-plaintext", false, "Write the bundle unencrypted (it then contains decrypted credentials)")
	cmd.Flags().StringVar(&opts.compression, "compression", "gzip", "Bundle compression: gzip or none")
	cmd.Flags().BoolVar(&opts.force, "force", false, "Overwrite an existing bundle file")
	cmd.Flags().StringVar(&opts.sourceName, "source-name", "", "Label recorded in the manifest, e.g. home or office")
	cmd.Flags().StringVar(&opts.logFile, "log-file", "", "Write the full per-item log to this file as well")
	return cmd
}

func runExportWithOptions(opts exportOptions, version string) int {
	cfg, err := appconfig.Load(opts.configPath)
	if err != nil {
		slog.Error("Failed to load config", "path", opts.configPath, "error", err)
		return 1
	}
	loc := cfg.MonitorLocation()
	since, err := parseWindowTime(opts.since, loc)
	if err != nil {
		slog.Error("Invalid --since", "value", opts.since, "error", err)
		return 1
	}
	until, err := parseWindowTime(opts.until, loc)
	if err != nil {
		slog.Error("Invalid --until", "value", opts.until, "error", err)
		return 1
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
		password = strings.TrimSpace(os.Getenv("TRAJECTA_EXPORT_PASSWORD"))
	}
	if opts.allowPlaintext && password != "" {
		slog.Error("--allow-plaintext cannot be combined with --password", "error", fmt.Errorf("choose one"))
		return 1
	}

	result, err := transfer.Export(context.Background(), st, cfg.TraceOutputDir(), transfer.ExportOptions{
		Since:          since,
		Until:          until,
		All:            opts.all,
		Out:            opts.out,
		Force:          opts.force,
		Password:       password,
		AllowPlaintext: opts.allowPlaintext,
		Compression:    opts.compression,
		SourceName:     opts.sourceName,
		Version:        version,
		Log:            log,
	})
	if err != nil {
		slog.Error("Export failed", "error", err)
		return 1
	}

	warnings := []string{}
	if result.Cassettes.MissingFiles > 0 {
		warnings = append(warnings, fmt.Sprintf("%d indexed cassettes are missing from disk and were not exported", result.Cassettes.MissingFiles))
	}
	if result.Password != "" {
		warnings = append(warnings, "the generated password is shown once and is not stored in the bundle")
	}
	if err := WriteResultWithWarnings(opts.stdout, opts.format, "export", result, warnings, func(w io.Writer) error {
		fmt.Fprintf(w, "bundle: %s\n", result.Bundle)
		fmt.Fprintf(w, "size: %d bytes\n", result.Bytes)
		fmt.Fprintf(w, "duration: %s\n", result.Duration.Round(time.Millisecond))
		fmt.Fprintf(w, "cassettes: %d copied, %d bytes, %d orphan, %d missing\n",
			result.Cassettes.Count, result.Cassettes.Bytes, result.Cassettes.OrphanCount, result.Cassettes.MissingFiles)
		for _, table := range result.Tables {
			if table.Rows == 0 {
				continue
			}
			fmt.Fprintf(w, "table %-32s %d rows\n", table.Name, table.Rows)
		}
		if result.Password != "" {
			fmt.Fprintf(w, "\n")
			fmt.Fprintf(w, "=========================================================\n")
			fmt.Fprintf(w, " bundle password (shown once, keep it safe):\n")
			fmt.Fprintf(w, "   %s\n", result.Password)
			fmt.Fprintf(w, "=========================================================\n")
			fmt.Fprintf(w, "The importing side needs this password. It is not stored\n")
			fmt.Fprintf(w, "in the bundle and cannot be recovered.\n")
		}
		return nil
	}); err != nil {
		slog.Error("Write export result failed", "error", err)
		return 1
	}
	return 0
}

// parseWindowTime accepts RFC3339 or a date. A bare date means local midnight
// in the deployment's monitor timezone, matching how the Monitor renders time.
func parseWindowTime(raw string, loc *time.Location) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if loc == nil {
		loc = time.Local
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return &parsed, nil
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return &parsed, nil
		}
	}
	if parsed, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
		return &parsed, nil
	}
	return nil, fmt.Errorf("want RFC3339 (2026-09-01T00:00:00+08:00) or YYYY-MM-DD")
}
