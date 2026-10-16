// Command trajecta is the Trajecta command-line tool. It is the operations CLI:
// it runs the deployment work that never needs the proxy process.
//
// That is the one-off rename migration (.env prefix mapping, legacy SQLite
// merge into Postgres, cassette magic rewrite and structural validation,
// SQLite archiving), the cassette vault reorganisation, and every
// server-management command group the server binary used to carry: analyze,
// audit, auth, config, db, doctor, migrate, models, provider, schema and tools.
//
// Commands that write are dry runs unless --apply is passed.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/kingfs/Trajecta/internal/cli"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

// options holds every flag value; it is shared by all subcommands.
type options struct {
	envFile     string
	noEnv       bool
	configPath  string
	format      string
	jobs        int
	apply       bool
	sqliteOpen  string
	traceDirs   []string
	sqlitePaths []string
	batchSize   int
}

// cliRuntime carries the parsed flags plus helpers shared by the commands.
type cliRuntime struct {
	opts *options

	// ops is the shared runtime the server-management command groups resolve
	// --config and --format from. It is bound to the same two flags.
	ops *cli.Runtime
}

// activeOpsRuntime is the shared runtime of the command tree being executed, so
// the process entry point can hand it to cli.Execute.
var activeOpsRuntime *cli.Runtime

func newRootCommand() *cobra.Command {
	opts := &options{}
	runtime := &cliRuntime{opts: opts}

	root := &cobra.Command{
		Use:   "trajecta",
		Short: "Trajecta command-line tool",
		Long: `trajecta is the Trajecta command-line tool.

It runs every operation that does not need the proxy process. That is the
management command groups the server binary used to carry - analyze, audit,
auth, config, db, doctor, migrate, models, provider, schema and tools - plus the
deployment work specific to this CLI: reading a .env file, migrating a
pre-rename llm-tracelab deployment onto the current layout, and reorganising the
recorded .http cassette vault.

Only the rename-migration and vault-reorganisation commands are dry runs unless
--apply is passed; the management commands act when they are invoked.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return runtime.validate()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}

	flags := root.PersistentFlags()
	flags.StringVar(&opts.envFile, "env-file", "", "path to the .env file to load (default: ./.env when it exists)")
	flags.BoolVar(&opts.noEnv, "no-env", false, "do not read any .env file")
	flags.StringVarP(&opts.configPath, "config", "c", "config.yaml", "path to the configuration file")
	flags.StringVar(&opts.format, "format", "text", "output format: text or json")
	flags.IntVar(&opts.jobs, "jobs", 0, "concurrent workers (default: number of CPUs)")
	flags.BoolVar(&opts.apply, "apply", false, "write changes for the rename-migration and vault-reorganisation commands; those commands only report what they would do without it")
	flags.StringVar(&opts.sqliteOpen, "sqlite-open", legacymigrate.OpenModeAuto,
		"how to open a legacy SQLite database: auto, ro, immutable or rw")
	flags.StringArrayVar(&opts.traceDirs, "trace-dir", nil, "directory holding legacy SQLite databases and cassettes (repeatable)")
	flags.StringArrayVar(&opts.sqlitePaths, "sqlite", nil, "explicit legacy SQLite database path (repeatable)")
	flags.IntVar(&opts.batchSize, "batch-size", 500, "rows per Postgres insert batch")

	// The server-management groups read --config and --format through the shared
	// runtime, which resolves the same two persistent flags.
	settings := viper.New()
	settings.SetEnvPrefix("TRAJECTA")
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	settings.AutomaticEnv()
	if err := settings.BindPFlag("config", flags.Lookup("config")); err != nil {
		panic(err)
	}
	if err := settings.BindPFlag("format", flags.Lookup("format")); err != nil {
		panic(err)
	}
	runtime.ops = cli.RuntimeFromViper(settings)
	activeOpsRuntime = runtime.ops
	cli.Register(root, runtime.ops, cli.Options{Name: "trajecta", Version: Version})

	root.AddCommand(newUpgradeCommand(runtime))
	root.AddCommand(newLayoutCommand(runtime))
	root.AddCommand(newVersionCommand(runtime))
	return root
}

// validate checks flag values that every command depends on.
func (r *cliRuntime) validate() error {
	switch strings.ToLower(r.opts.format) {
	case "text", "json":
	default:
		return usageErr("--format", "unsupported format %q (want text or json)", r.opts.format)
	}
	switch r.opts.sqliteOpen {
	case legacymigrate.OpenModeAuto, legacymigrate.OpenModeReadOnly,
		legacymigrate.OpenModeImmutable, legacymigrate.OpenModeReadWrite:
	default:
		return usageErr("--sqlite-open", "unsupported mode %q (want auto, ro, immutable or rw)", r.opts.sqliteOpen)
	}
	if r.opts.batchSize < 1 {
		return usageErr("--batch-size", "must be at least 1")
	}
	if r.opts.jobs < 0 {
		return usageErr("--jobs", "must not be negative")
	}
	return nil
}

// usageErr marks an invalid invocation with the shared exit-3 contract.
func usageErr(field, format string, args ...any) error {
	return cli.UsageError(fmt.Sprintf(format, args...), field)
}

// fail marks a command that ran correctly but reported a failed outcome, such
// as a table that could not be merged. It exits 1 with the shared error
// envelope.
func fail(format string, args ...any) error {
	return cli.APIError(fmt.Sprintf(format, args...))
}

func (r *cliRuntime) jsonOutput() bool {
	return strings.EqualFold(r.opts.format, "json")
}

// format is the normalized --format value handed to the shared writer.
func (r *cliRuntime) format() string {
	return strings.ToLower(strings.TrimSpace(r.opts.format))
}

func (r *cliRuntime) workers() int {
	if r.opts.jobs > 0 {
		return r.opts.jobs
	}
	return legacymigrate.DefaultWorkers()
}

// loadEnvFile applies the deployment .env file to the process environment.
// Variables already exported in the shell win.
func (r *cliRuntime) loadEnvFile() (*legacymigrate.EnvLoadResult, error) {
	if r.opts.noEnv {
		return nil, nil
	}
	path := strings.TrimSpace(r.opts.envFile)
	if path == "" {
		candidate := legacymigrate.EnvFileCandidate(".")
		if _, err := os.Stat(candidate); err != nil {
			return nil, nil
		}
		path = candidate
	}
	return legacymigrate.LoadEnvFile(path, false)
}

// loadConfig reads the configuration after the environment has been prepared.
func (r *cliRuntime) loadConfig() (*config.Config, error) {
	return config.Load(r.opts.configPath)
}

// prepare loads the .env file and the configuration in the correct order.
func (r *cliRuntime) prepare() (*legacymigrate.EnvLoadResult, *config.Config, error) {
	env, err := r.loadEnvFile()
	if err != nil {
		return nil, nil, fmt.Errorf("load .env: %w", err)
	}
	cfg, err := r.loadConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	return env, cfg, nil
}

// resolveTraceDirs returns the directories searched for legacy databases and
// cassettes, together with warnings about directories that do not exist.
func (r *cliRuntime) resolveTraceDirs(cfg *config.Config) ([]string, []string) {
	var warnings []string
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		for _, existing := range dirs {
			if existing == dir {
				return
			}
		}
		dirs = append(dirs, dir)
	}

	for _, dir := range r.opts.traceDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err != nil {
			warnings = append(warnings, fmt.Sprintf("--trace-dir %s does not exist", dir))
			continue
		} else if !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("--trace-dir %s is not a directory", dir))
			continue
		}
		add(dir)
	}
	if len(r.opts.traceDirs) > 0 {
		return dirs, warnings
	}

	if cfg != nil {
		if dir := cfg.TraceOutputDir(); dir != "" {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				add(dir)
			} else {
				warnings = append(warnings, fmt.Sprintf("configured trace output dir %s is not present in this environment", dir))
			}
		}
	}
	for _, fallback := range []string{filepath.Join("data", "traces"), "logs", "data"} {
		if info, err := os.Stat(fallback); err == nil && info.IsDir() {
			add(fallback)
		}
	}
	if len(dirs) == 0 {
		add(".")
	}
	return dirs, warnings
}

// resolveSQLitePaths returns the legacy databases to merge, newest first.
func (r *cliRuntime) resolveSQLitePaths(cfg *config.Config) ([]string, []string, error) {
	if len(r.opts.sqlitePaths) > 0 {
		var paths []string
		for _, path := range r.opts.sqlitePaths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				return nil, nil, usageErr("--sqlite", "%s is not readable: %v", path, err)
			}
			if info.IsDir() {
				return nil, nil, usageErr("--sqlite", "%s is a directory", path)
			}
			paths = append(paths, path)
		}
		return paths, nil, nil
	}
	dirs, warnings := r.resolveTraceDirs(cfg)
	candidates := legacymigrate.DiscoverLegacySQLiteFiles(dirs)
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		paths = append(paths, candidate.Path)
	}
	return paths, warnings, nil
}

// progressFunc renders progress to stderr so that stdout stays machine readable.
func (r *cliRuntime) progressFunc() legacymigrate.ProgressFunc {
	return func(progress legacymigrate.Progress) {
		parts := []string{progress.Phase}
		if progress.Table != "" {
			parts = append(parts, "table="+progress.Table)
		}
		if progress.Total > 0 {
			parts = append(parts, fmt.Sprintf("rows=%d/%d", progress.Processed, progress.Total))
		}
		for _, field := range []struct {
			name  string
			value int64
		}{
			{"scanned", progress.Scanned},
			{"checked", progress.Checked},
			{"rewritten", progress.Rewritten},
			{"current", progress.Current},
			{"other", progress.Other},
			{"copied", progress.Copied},
			{"duplicate", progress.Duplicate},
			{"failed", progress.Failed},
		} {
			if field.value != 0 {
				parts = append(parts, fmt.Sprintf("%s=%d", field.name, field.value))
			}
		}
		if progress.Message != "" {
			parts = append(parts, progress.Message)
		}
		fmt.Fprintln(os.Stderr, "  ... "+strings.Join(parts, " "))
	}
}

// writeResult renders a command result as JSON or as human-readable text. ok
// reflects the command's own verdict (a check that found errors reports
// ok=false) and is what callers turn into the exit code.
func (r *cliRuntime) writeResult(w io.Writer, command string, ok bool, result any, text func(io.Writer) error, warnings []string) error {
	r.ops.MarkResultWritten()
	return cli.WriteResultWithState(w, r.format(), command, ok, result, warnings, text)
}

// humanBytes renders a byte count for reports.
func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	for _, suffix := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.2f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%.2f EiB", value/unit)
}
