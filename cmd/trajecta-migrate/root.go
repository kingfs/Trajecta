// Command trajecta-migrate performs the one-off migration work that a
// Trajecta deployment needs after the project was renamed from llm-tracelab:
// it loads a pre-rename .env file, merges legacy SQLite application databases
// into Postgres, and normalizes or validates existing .http cassettes.
//
// Every command is a dry run unless --apply is passed.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/legacymigrate"
)

// Exit codes follow the trajecta CLI convention.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 3
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

	// resultWritten records that a command already emitted its result, so the
	// top level error path does not print a second envelope.
	resultWritten bool

	// command is the command path being executed, used to label error output.
	command string
}

// activeRuntime is the runtime of the command tree being executed. It lets the
// top level error path render failures in the selected output format.
var activeRuntime *cliRuntime

// usageError marks an invalid invocation.
type usageError struct {
	field   string
	message string
}

func (e *usageError) Error() string {
	if e.field == "" {
		return e.message
	}
	return fmt.Sprintf("%s: %s", e.field, e.message)
}

func usageErr(field, format string, args ...any) error {
	return &usageError{field: field, message: fmt.Sprintf(format, args...)}
}

// exitCodeFor maps an error to a process exit code.
func exitCodeFor(err error) int {
	if err == nil {
		return exitOK
	}
	var usage *usageError
	if ok := asUsageError(err, &usage); ok {
		return exitUsage
	}
	return exitFailure
}

func asUsageError(err error, target **usageError) bool {
	for err != nil {
		if typed, ok := err.(*usageError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// failureError marks a command that ran correctly but reported a failed
// outcome, such as a table that could not be merged.
type failureError struct{ message string }

func (e *failureError) Error() string { return e.message }

// fail builds a failureError.
func fail(format string, args ...any) error {
	return &failureError{message: fmt.Sprintf(format, args...)}
}

func newRootCommand() *cobra.Command {
	opts := &options{}
	runtime := &cliRuntime{opts: opts}
	activeRuntime = runtime

	root := &cobra.Command{
		Use:   "trajecta-migrate",
		Short: "Migrate a pre-rename llm-tracelab deployment to Trajecta",
		Long: `trajecta-migrate moves a pre-rename deployment onto the current layout.

It reads the deployment .env file, merges legacy SQLite application databases
into Postgres, rewrites the cassette prelude magic and validates cassette
structure. Nothing is written unless --apply is passed.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			runtime.command = cmd.CommandPath()
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
	flags.BoolVar(&opts.apply, "apply", false, "write changes; without this flag every command only reports what it would do")
	flags.StringVar(&opts.sqliteOpen, "sqlite-open", legacymigrate.OpenModeAuto,
		"how to open a legacy SQLite database: auto, ro, immutable or rw")
	flags.StringArrayVar(&opts.traceDirs, "trace-dir", nil, "directory holding legacy SQLite databases and cassettes (repeatable)")
	flags.StringArrayVar(&opts.sqlitePaths, "sqlite", nil, "explicit legacy SQLite database path (repeatable)")
	flags.IntVar(&opts.batchSize, "batch-size", 500, "rows per Postgres insert batch")

	root.AddCommand(newEnvCommand(runtime))
	root.AddCommand(newDBCommand(runtime))
	root.AddCommand(newCassettesCommand(runtime))
	root.AddCommand(newSQLiteCommand(runtime))
	root.AddCommand(newRunCommand(runtime))
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

func (r *cliRuntime) jsonOutput() bool {
	return strings.EqualFold(r.opts.format, "json")
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

type resultEnvelope struct {
	OK       bool     `json:"ok"`
	Command  string   `json:"command"`
	Result   any      `json:"result,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// writeResult renders a command result as JSON or as human-readable text. ok
// reflects the command's own verdict (a check that found errors reports
// ok=false) and is what callers turn into the exit code.
func (r *cliRuntime) writeResult(w io.Writer, command string, ok bool, result any, text func(io.Writer) error, warnings []string) error {
	r.resultWritten = true
	if r.jsonOutput() {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(resultEnvelope{OK: ok, Command: command, Result: result, Warnings: warnings})
	}
	if text == nil {
		return nil
	}
	return text(w)
}

// writeError renders a failed command. When a command already wrote a result
// (its envelope carries ok=false) nothing else is emitted, so a JSON consumer
// always sees exactly one object on stdout.
func (r *cliRuntime) writeError(w io.Writer, command string, err error) {
	if r == nil || r.resultWritten || !r.jsonOutput() {
		fmt.Fprintln(os.Stderr, "error: "+err.Error())
		return
	}
	if command == "" {
		command = "trajecta-migrate"
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(struct {
		OK      bool   `json:"ok"`
		Command string `json:"command"`
		Error   string `json:"error"`
	}{OK: false, Command: command, Error: err.Error()})
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
