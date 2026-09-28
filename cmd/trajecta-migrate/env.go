package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

// envReport is the machine-readable result of `trajecta-migrate env`.
type envReport struct {
	EnvFile         *legacymigrate.EnvLoadResult    `json:"env_file,omitempty"`
	ConfigPath      string                          `json:"config_path"`
	DatabaseDriver  string                          `json:"database_driver"`
	DatabaseDSN     string                          `json:"database_dsn"`
	TraceOutputDir  string                          `json:"trace_output_dir"`
	SearchDirs      []string                        `json:"search_dirs"`
	LegacyDatabases []legacymigrate.SQLiteCandidate `json:"legacy_databases,omitempty"`
	Compose         *legacymigrate.ComposeScan      `json:"compose,omitempty"`
	Warnings        []string                        `json:"warnings,omitempty"`
}

func newEnvCommand(runtime *cliRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Load the deployment .env file and report the effective configuration",
		Long: `env applies the deployment .env file the way the server would, maps the
pre-rename LLM_TRACELAB_* variables onto TRAJECTA_*, then reports the effective
database and trace configuration together with the legacy databases it finds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.execEnv(cmd)
		},
	}
	return cmd
}

func (r *cliRuntime) execEnv(cmd *cobra.Command) error {
	env, cfg, err := r.prepare()
	if err != nil {
		return err
	}
	var warnings []string
	if env != nil {
		warnings = append(warnings, env.Warnings...)
	}

	dirs, dirWarnings := r.resolveTraceDirs(cfg)
	warnings = append(warnings, dirWarnings...)
	legacy := legacymigrate.DiscoverLegacySQLiteFiles(dirs)

	composePath := firstExistingFile("docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml")
	var compose *legacymigrate.ComposeScan
	if composePath != "" {
		compose = legacymigrate.ScanComposeEnvPrefix(composePath)
		if compose.UsesLegacyPrefix() {
			warnings = append(warnings, compose.Summary())
		}
	}

	report := &envReport{
		EnvFile:         maskedEnvResult(env),
		ConfigPath:      r.opts.configPath,
		DatabaseDriver:  cfg.Database.Driver,
		DatabaseDSN:     legacymigrate.MaskSecret("DSN", cfg.Database.DSN),
		TraceOutputDir:  cfg.TraceOutputDir(),
		SearchDirs:      dirs,
		LegacyDatabases: legacy,
		Compose:         compose,
		Warnings:        warnings,
	}
	if len(legacy) == 0 {
		report.Warnings = append(report.Warnings, "no legacy SQLite application database was found in the searched directories")
	}

	writeErr := r.writeResult(cmd.OutOrStdout(), "env", true, report, func(w io.Writer) error {
		r.printEnvText(w, report)
		return nil
	}, report.Warnings)
	if writeErr != nil {
		return writeErr
	}
	if r.jsonOutput() {
		for _, warning := range report.Warnings {
			fmt.Fprintln(os.Stderr, "warning: "+warning)
		}
	}
	return nil
}

func (r *cliRuntime) printEnvText(w io.Writer, report *envReport) {
	if report.EnvFile != nil {
		fmt.Fprintf(w, "env file            %s\n", report.EnvFile.Path)
		fmt.Fprintf(w, "variables applied   %d applied, %d kept from the environment\n", report.EnvFile.Applied, report.EnvFile.Kept)
		for _, entry := range report.EnvFile.Entries {
			label := entry.SourceKey
			if entry.Renamed {
				label = entry.SourceKey + " -> " + entry.Key
			}
			status := "applied"
			if !entry.Applied {
				status = entry.Note
				if status == "" {
					status = "skipped"
				}
			}
			fmt.Fprintf(w, "  %-52s %s\n", label, status)
			if entry.Applied {
				// Values are already masked in the report.
				fmt.Fprintf(w, "  %-52s = %s\n", "", entry.Value)
			}
		}
	} else {
		fmt.Fprintf(w, "env file            (none)\n")
	}
	fmt.Fprintf(w, "config              %s\n", report.ConfigPath)
	fmt.Fprintf(w, "database driver     %s\n", orNone(report.DatabaseDriver))
	fmt.Fprintf(w, "database dsn        %s\n", orNone(report.DatabaseDSN))
	fmt.Fprintf(w, "trace output dir    %s\n", orNone(report.TraceOutputDir))
	fmt.Fprintf(w, "search dirs         %s\n", strings.Join(report.SearchDirs, ", "))
	fmt.Fprintf(w, "legacy databases    %d\n", len(report.LegacyDatabases))
	for _, candidate := range report.LegacyDatabases {
		fmt.Fprintf(w, "  %-60s %10s  %s\n", candidate.Path, humanBytes(candidate.Size), candidate.ModTime.Format("2006-01-02 15:04"))
	}
	if report.Compose != nil {
		fmt.Fprintf(w, "deployment manifest %s\n", report.Compose.Summary())
		if len(report.Compose.LegacyKeys) > 0 {
			fmt.Fprintf(w, "  legacy variables: %s\n", strings.Join(report.Compose.LegacyKeys, ", "))
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintf(w, "warnings            %d\n", len(report.Warnings))
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "  - %s\n", warning)
		}
	}
}

func orNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(not set)"
	}
	return value
}

// maskedEnvResult copies a load result with credential-like values masked, so
// that the report can be pasted into an issue or a log without leaking keys.
// The process environment keeps the real values.
func maskedEnvResult(result *legacymigrate.EnvLoadResult) *legacymigrate.EnvLoadResult {
	if result == nil {
		return nil
	}
	masked := *result
	masked.Entries = make([]legacymigrate.EnvEntry, len(result.Entries))
	copy(masked.Entries, result.Entries)
	for i := range masked.Entries {
		masked.Entries[i].Value = legacymigrate.MaskSecret(masked.Entries[i].Key, masked.Entries[i].Value)
	}
	return &masked
}

func firstExistingFile(names ...string) string {
	for _, name := range names {
		if info, err := os.Stat(name); err == nil && !info.IsDir() {
			return name
		}
	}
	return ""
}
