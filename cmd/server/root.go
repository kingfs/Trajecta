package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// cliName is the server artifact name: it drives the usage text, the version
// payload and the CLI schema name. The product name (for example the Codex
// model_provider) is separate and stays "trajecta".
const cliName = "server"

func run(args []string) int {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	runtime := newCLIRuntime()
	cmd := newRootCommandWithRuntime(runtime)
	cmd.SetArgs(normalizeLegacyFlagArgs(args))
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	recorder := &cliErrorRecorder{}
	slog.SetDefault(slog.New(newCLILogHandler(os.Stderr, recorder)))

	if err := cmd.Execute(); err != nil {
		exit := cliExitErrorFromErr(err)
		// Commands that only log their failure and return a bare code (the
		// trace store, config loading) would otherwise collapse into
		// COMMAND_FAILED/"command failed"; surface the logged cause instead.
		if recorder.has() && fallback(exit.message, "command failed") == "command failed" {
			exit.message = recorder.message()
		}
		// cobra fails before the persistent --format flag is bound when the
		// command itself is unknown, so read it from argv as a fallback.
		format := runtime.outputFormat()
		if format != "json" {
			if fromArgs := outputFormatFromArgs(args); fromArgs != "" {
				format = fromArgs
			}
		}
		writeCLIError(cmd.ErrOrStderr(), format, exit)
		return exit.code
	}
	return 0
}

// outputFormatFromArgs finds an explicit --format value in the raw argv, for the
// failure paths where cobra never bound the flag.
func outputFormatFromArgs(args []string) string {
	for i, arg := range args {
		if arg == "--format" && i+1 < len(args) {
			return args[i+1]
		}
		if value, ok := strings.CutPrefix(arg, "--format="); ok {
			return value
		}
	}
	return ""
}

// cliExitErrorFromErr maps a command error to the documented exit contract:
// usage/validation errors exit 3, internal failures exit 8.
func cliExitErrorFromErr(err error) cliExitError {
	var exit cliExitError
	if errors.As(err, &exit) {
		return exit
	}
	message := err.Error()
	if isUsageError(err) {
		return cliUsageError(message, "")
	}
	return cliExitError{
		code:     exitCodeInternal,
		category: errorCategoryInternal,
		errCode:  "INTERNAL_ERROR",
		message:  message,
	}
}

// isUsageError reports whether a bare cobra error is really a usage error.
func isUsageError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	for _, prefix := range []string{
		"unknown command ",
		"unknown flag: ",
		"unknown shorthand flag: ",
		"required flag(s) ",
		"invalid argument ",
		"accepts at most ",
		"accepts between ",
		"accepts ",
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

// cliErrorRecorder keeps the most recent ERROR log line so a command that only
// logs its failure can still report the real cause in the JSON envelope.
type cliErrorRecorder struct {
	mu       sync.Mutex
	lastText string
}

func (r *cliErrorRecorder) record(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastText = message
}

func (r *cliErrorRecorder) has() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(r.lastText) != ""
}

func (r *cliErrorRecorder) message() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastText
}

// cliLogHandler writes the usual text log and mirrors ERROR records into the
// recorder.
type cliLogHandler struct {
	primary  slog.Handler
	recorder *cliErrorRecorder
}

func newCLILogHandler(w io.Writer, recorder *cliErrorRecorder) slog.Handler {
	return &cliLogHandler{
		primary:  slog.NewTextHandler(w, nil),
		recorder: recorder,
	}
}

func (h *cliLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.primary.Enabled(ctx, level)
}

func (h *cliLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level >= slog.LevelError {
		// The `error` attribute carries the real cause; the remaining attributes
		// are context that the human-readable log line already shows.
		var cause string
		var context []string
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "error" {
				if cause == "" {
					cause = attr.Value.String()
				}
				return true
			}
			context = append(context, fmt.Sprintf("%s=%v", attr.Key, attr.Value.Any()))
			return true
		})
		message := record.Message
		if cause != "" {
			message = message + ": " + cause
		} else if len(context) > 0 {
			message = message + ": " + strings.Join(context, " ")
		}
		h.recorder.record(message)
	}
	return h.primary.Handle(ctx, record)
}

func (h *cliLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &cliLogHandler{primary: h.primary.WithAttrs(attrs), recorder: h.recorder}
}

func (h *cliLogHandler) WithGroup(name string) slog.Handler {
	return &cliLogHandler{primary: h.primary.WithGroup(name), recorder: h.recorder}
}

func newRootCommand() *cobra.Command {
	return newRootCommandWithRuntime(newCLIRuntime())
}

func newRootCommandWithRuntime(runtime *cliRuntime) *cobra.Command {
	cmd := &cobra.Command{
		Use:           cliName,
		Short:         "Postgres-first LLM gateway with local Responses runtime and record/replay",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return runtime.validateOutputFormat()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCode(func() int {
				return runServeWithConfig(runtime.configPath())
			})
		},
	}
	cmd.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return cliUsageError(err.Error(), "flag")
	})
	cmd.PersistentFlags().StringP("config", "c", "config.yaml", "Path to configuration file")
	cmd.PersistentFlags().String("format", "text", "Output format: text or json")
	mustBindPFlag(runtime.settings, "config", cmd.PersistentFlags().Lookup("config"))
	mustBindPFlag(runtime.settings, "format", cmd.PersistentFlags().Lookup("format"))
	cmd.AddCommand(
		newServeCommand(runtime),
		newMigrateCommand(runtime),
		newDBCommand(runtime),
		newConfigCommand(runtime),
		newDoctorCommand(runtime),
		newProviderCommand(runtime),
		newModelsCommand(runtime),
		newToolsCommand(runtime),
		newAuditCommand(runtime),
		newAuthCommand(runtime),
		newAnalyzeCommand(runtime),
		newVersionCommand(runtime),
		newSchemaCommand(runtime, cmd),
		newCompletionCommand(cmd),
	)
	return cmd
}

type cliRuntime struct {
	settings *viper.Viper
}

func newCLIRuntime() *cliRuntime {
	settings := viper.New()
	settings.SetEnvPrefix("TRAJECTA")
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	settings.AutomaticEnv()
	return &cliRuntime{settings: settings}
}

func (rt *cliRuntime) configPath() string {
	if rt == nil || rt.settings == nil {
		return "config.yaml"
	}
	if path := rt.settings.GetString("config"); path != "" {
		return path
	}
	return "config.yaml"
}

func (rt *cliRuntime) outputFormat() string {
	if rt == nil || rt.settings == nil {
		return "text"
	}
	if format := rt.settings.GetString("format"); format != "" {
		return format
	}
	return "text"
}

func (rt *cliRuntime) validateOutputFormat() error {
	switch rt.outputFormat() {
	case "text", "json":
		return nil
	default:
		return cliExitError{
			code:     exitCodeUsage,
			category: errorCategoryUsage,
			errCode:  "INVALID_OUTPUT_FORMAT",
			message:  "--format must be one of: text, json",
			field:    "format",
		}
	}
}

func runCode(run func() int) error {
	if code := run(); code != 0 {
		return cliExitErrorFromCode(code)
	}
	return nil
}

func cliUsageError(message string, field string) cliExitError {
	return cliExitError{
		code:     exitCodeUsage,
		category: errorCategoryUsage,
		errCode:  "USAGE_ERROR",
		message:  message,
		field:    field,
	}
}

func requireSubcommand(cmd *cobra.Command) error {
	if cmd.Flag("format") != nil && cmd.Flag("format").Value.String() == "json" {
		return cliUsageError(fmt.Sprintf("%s requires a subcommand", cmd.CommandPath()), "command")
	}
	if err := cmd.Help(); err != nil {
		return cliExitError{
			code:     exitCodeInternal,
			category: errorCategoryInternal,
			errCode:  "HELP_RENDER_FAILED",
			message:  err.Error(),
		}
	}
	return cliExitErrorFromCode(exitCodeUsage)
}

func normalizeLegacyFlagArgs(args []string) []string {
	normalized := make([]string, len(args))
	for i, arg := range args {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && len(arg) > 2 {
			normalized[i] = "-" + arg
			continue
		}
		normalized[i] = arg
	}
	return normalized
}

func mustBindPFlag(settings *viper.Viper, key string, flag *pflag.Flag) {
	if err := settings.BindPFlag(key, flag); err != nil {
		panic(err)
	}
}

type cliExitError struct {
	code     int
	category string
	errCode  string
	message  string
	field    string
}

func (e cliExitError) Error() string {
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("exit %d", e.code)
}

const (
	exitCodeAPI           = 1
	exitCodeUsage         = 3
	exitCodeInternal      = 8
	errorCategoryAPI      = "api"
	errorCategoryUsage    = "usage"
	errorCategoryInternal = "internal"
)

func cliExitErrorFromCode(code int) cliExitError {
	switch code {
	case 2, exitCodeUsage:
		return cliExitError{
			code:     exitCodeUsage,
			category: errorCategoryUsage,
			errCode:  "USAGE_ERROR",
			message:  "command usage is invalid",
		}
	case exitCodeInternal:
		return cliExitError{
			code:     exitCodeInternal,
			category: errorCategoryInternal,
			errCode:  "INTERNAL_ERROR",
			message:  "command failed with an internal error",
		}
	default:
		return cliExitError{
			code:     code,
			category: errorCategoryAPI,
			errCode:  "COMMAND_FAILED",
			message:  "command failed",
		}
	}
}

type cliEnvelope struct {
	OK       bool      `json:"ok"`
	Command  string    `json:"command,omitempty"`
	Result   any       `json:"result,omitempty"`
	Warnings []string  `json:"warnings"`
	Error    *cliError `json:"error,omitempty"`
}

type cliError struct {
	Code        string `json:"code"`
	Category    string `json:"category"`
	Message     string `json:"message"`
	Field       string `json:"field,omitempty"`
	Retryable   bool   `json:"retryable"`
	SafeToRetry bool   `json:"safe_to_retry"`
}

func writeCLIResult(w io.Writer, format string, command string, result any, text func(io.Writer) error) error {
	return writeCLIResultWithWarnings(w, format, command, result, resultWarnings(result), text)
}

// writeCLIResultWithWarnings writes the text form or a JSON envelope. The
// envelope-level warnings mirror the ones the text form prints, so a caller that
// only reads envelope.warnings does not miss them.
func writeCLIResultWithWarnings(w io.Writer, format string, command string, result any, warnings []string, text func(io.Writer) error) error {
	if format != "json" {
		if text == nil {
			return nil
		}
		return text(w)
	}
	if warnings == nil {
		warnings = []string{}
	}
	return writeJSON(w, cliEnvelope{
		OK:       true,
		Command:  command,
		Result:   result,
		Warnings: warnings,
	})
}

// resultWarnings collects the warnings a command attached to its own result, so
// the envelope carries them whether they are a []string field or a map entry.
func resultWarnings(result any) []string {
	switch value := result.(type) {
	case map[string]any:
		return stringSlice(value["warnings"])
	case []map[string]any:
		var out []string
		for _, item := range value {
			out = append(out, stringSlice(item["warnings"])...)
		}
		return out
	}
	return warningsFromStruct(result)
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	}
	return nil
}

// warningsFromStruct reads a top-level `warnings` []string field, plus a
// `reports[].warnings` list, through reflection so the helper stays dependency
// free and works for the report structs the provider commands return.
func warningsFromStruct(result any) []string {
	value := reflect.ValueOf(result)
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	if field := value.FieldByName("Warnings"); field.IsValid() && field.Kind() == reflect.Slice {
		out = append(out, stringSlice(field.Interface())...)
	}
	if field := value.FieldByName("Reports"); field.IsValid() && field.Kind() == reflect.Slice {
		for i := 0; i < field.Len(); i++ {
			item := field.Index(i)
			for item.Kind() == reflect.Pointer {
				if item.IsNil() {
					break
				}
				item = item.Elem()
			}
			if item.Kind() != reflect.Struct {
				continue
			}
			if warnings := item.FieldByName("Warnings"); warnings.IsValid() && warnings.Kind() == reflect.Slice {
				out = append(out, stringSlice(warnings.Interface())...)
			}
		}
	}
	return out
}

func writeCLIError(w io.Writer, format string, exit cliExitError) {
	if format != "json" {
		if exit.message != "" {
			fmt.Fprintln(w, exit.message)
		}
		return
	}
	_ = writeJSON(w, cliEnvelope{
		OK:       false,
		Warnings: []string{},
		Error: &cliError{
			Code:        fallback(exit.errCode, "COMMAND_FAILED"),
			Category:    fallback(exit.category, errorCategoryAPI),
			Message:     fallback(exit.message, "command failed"),
			Field:       exit.field,
			Retryable:   exit.category != errorCategoryUsage,
			SafeToRetry: exit.category != errorCategoryUsage,
		},
	})
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func fallback(value string, fallbackValue string) string {
	if value != "" {
		return value
	}
	return fallbackValue
}
