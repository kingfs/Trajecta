package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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
	name           string
}

func newExportCommand(rt *Runtime) *cobra.Command {
	var opts exportOptions
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export recorded data into a portable bundle for another trajecta deployment",
		Long: `Export a time window of recordings (or everything) into one bundle file.

--out is an output DIRECTORY. The bundle file is named after the moment it was
written, e.g. trajecta-export-20261111T143255Z-all.tar.gz, and written inside
that directory; when --out is omitted the default is <trace.output_dir>/backups.
Pass --name to choose the file name yourself, or give --out a .tar/.tar.gz name
to write exactly that file.

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
	cmd.Flags().StringVar(&opts.out, "out", "", "Directory to write the bundle into (default {{output_dir}}/backups)")
	cmd.Flags().StringVar(&opts.name, "name", "", "Bundle file name to use inside --out instead of the generated one")
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

	bundlePath, err := resolveExportBundlePath(exportTargetOptions{
		Out:         opts.out,
		Name:        opts.name,
		Compression: opts.compression,
		OutputDir:   cfg.TraceOutputDir(),
		All:         opts.all,
		Since:       since,
		Until:       until,
		Now:         time.Now().UTC(),
	})
	if err != nil {
		slog.Error("Resolve export target failed", "error", err)
		return 1
	}

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
		Out:            bundlePath,
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

// exportTargetOptions describes how the concrete bundle path is derived from
// --out / --name.
type exportTargetOptions struct {
	Out         string
	Name        string
	Compression string
	OutputDir   string
	All         bool
	Since       *time.Time
	Until       *time.Time
	Now         time.Time
}

// resolveExportBundlePath turns the operator-facing output location into one
// concrete file path and creates the directory that will hold it.
//
// --out is a directory. The file name carries the write moment (UTC, so it
// sorts chronologically) plus the exported window, which makes a backup
// directory self-describing. An existing file is never overwritten by a
// generated name: a numeric suffix is appended instead. Passing an archive
// suffix in --out, or an explicit --name, still selects an exact file, which
// is what keeps scripted callers working.
func resolveExportBundlePath(opts exportTargetOptions) (string, error) {
	ext := ".tar.gz"
	if strings.EqualFold(strings.TrimSpace(opts.Compression), "none") {
		ext = ".tar"
	}
	out := strings.TrimSpace(opts.Out)
	name := strings.TrimSpace(opts.Name)
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("--name %q must not contain a path separator", name)
	}
	if name != "" {
		if !hasArchiveSuffix(name) {
			name += ext
		}
		out = orElsePath(out, filepath.Join(opts.OutputDir, "backups"))
		if err := os.MkdirAll(out, 0o755); err != nil {
			return "", fmt.Errorf("create export directory %s: %w", out, err)
		}
		return uniqueBundlePath(filepath.Join(out, name)), nil
	}
	if out == "" {
		out = filepath.Join(opts.OutputDir, "backups")
	}
	if fi, err := os.Stat(out); err == nil && !fi.IsDir() {
		return "", fmt.Errorf("--out %q is a file; give a directory, or a .tar/.tar.gz name to write exactly that file", out)
	}
	if hasArchiveSuffix(out) {
		// Explicit file path: parent must exist so the operator notices a typo.
		parent := filepath.Dir(out)
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", fmt.Errorf("create export directory %s: %w", parent, err)
		}
		return out, nil
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", fmt.Errorf("create export directory %s: %w", out, err)
	}
	return uniqueBundlePath(filepath.Join(out, generatedBundleName(opts, ext))), nil
}

// generatedBundleName is the default name: write moment first so a directory
// listing sorts oldest to newest, then the exported window.
func generatedBundleName(opts exportTargetOptions, ext string) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	stamp := now.UTC().Format("20060102T150405Z")
	window := "all"
	if !opts.All {
		parts := []string{}
		if opts.Since != nil {
			parts = append(parts, opts.Since.UTC().Format("20060102"))
		} else {
			parts = append(parts, "start")
		}
		if opts.Until != nil {
			parts = append(parts, opts.Until.UTC().Format("20060102"))
		} else {
			parts = append(parts, "now")
		}
		window = strings.Join(parts, "-")
	}
	return fmt.Sprintf("trajecta-export-%s-%s%s", stamp, window, ext)
}

// uniqueBundlePath appends -2, -3, ... before the extension until the path is
// free, so a generated name never replaces an existing backup.
func uniqueBundlePath(path string) string {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}
	ext := archiveSuffix(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return path
}

func hasArchiveSuffix(name string) bool { return archiveSuffix(name) != "" }

// archiveSuffix returns the trailing archive extension, or "" when the name
// does not end in one.
func archiveSuffix(name string) string {
	lower := strings.ToLower(name)
	for _, suffix := range []string{".tar.gz", ".tgz", ".tar"} {
		if strings.HasSuffix(lower, suffix) {
			return name[len(name)-len(suffix):]
		}
	}
	return ""
}

func orElsePath(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
