// Package cli holds the Trajecta command-line contract and the
// server-management command groups.
//
// The contract is the exit-code table, the JSON envelope, the viper-backed
// Runtime that resolves --config and --format, and the ERROR log recorder that
// lets a command report the cause it only logged. Both binaries share it.
//
// The command groups are the operations that never need the proxy process:
// analyze, audit, auth, config, db, doctor, migrate, models, provider, schema
// and tools. The trajecta binary registers all of them through Register; the
// server binary imports this package for the contract only and registers none.
package cli

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

// Version is the CLI version reported by the version and schema envelopes.
// Each binary assigns it from its own build metadata before it builds the
// command tree.
var Version = "dev"

// Options describes the binary embedding the command groups.
type Options struct {
	// Name labels the command tree; it drives the schema payload.
	Name string
	// Version overrides the package Version for the schema payload.
	Version string
}

// Runtime carries the resolved --config and --format values plus the helpers the
// command groups share.
type Runtime struct {
	settings *viper.Viper
	name     string
	version  string

	// resultWritten records that a command already emitted its own envelope, so
	// the top-level failure path does not print a second one.
	resultWritten bool
}

// NewRuntime returns a Runtime backed by its own viper instance, with the
// TRAJECTA_ environment prefix already wired up.
func NewRuntime() *Runtime {
	settings := viper.New()
	settings.SetEnvPrefix("TRAJECTA")
	settings.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	settings.AutomaticEnv()
	return &Runtime{settings: settings}
}

// RuntimeFromViper returns a Runtime that reads --config and --format from an
// existing viper instance, so an embedding binary keeps its own flag
// definitions.
func RuntimeFromViper(settings *viper.Viper) *Runtime {
	if settings == nil {
		return NewRuntime()
	}
	return &Runtime{settings: settings}
}

// BindPFlag connects a persistent flag to the runtime setting of the same name.
func (rt *Runtime) BindPFlag(key string, flag *pflag.Flag) {
	if rt == nil || rt.settings == nil || flag == nil {
		return
	}
	if err := rt.settings.BindPFlag(key, flag); err != nil {
		panic(err)
	}
}

// Name is the command-tree name used by the schema payload.
func (rt *Runtime) Name() string {
	if rt == nil || rt.name == "" {
		return "trajecta"
	}
	return rt.name
}

// Version is the version used by the schema payload.
func (rt *Runtime) Version() string {
	if rt == nil || rt.version == "" {
		return Version
	}
	return rt.version
}

// ConfigPath is the resolved --config value.
func (rt *Runtime) ConfigPath() string {
	if rt == nil || rt.settings == nil {
		return "config.yaml"
	}
	if path := rt.settings.GetString("config"); path != "" {
		return path
	}
	return "config.yaml"
}

// OutputFormat is the resolved --format value.
func (rt *Runtime) OutputFormat() string {
	if rt == nil || rt.settings == nil {
		return "text"
	}
	if format := rt.settings.GetString("format"); format != "" {
		return format
	}
	return "text"
}

// MarkResultWritten records that a command emitted its own result envelope,
// which may already carry ok=false and its own warnings.
func (rt *Runtime) MarkResultWritten() {
	if rt != nil {
		rt.resultWritten = true
	}
}

// ResultWritten reports whether a command already emitted its own envelope.
func (rt *Runtime) ResultWritten() bool {
	return rt != nil && rt.resultWritten
}

// ValidateOutputFormat rejects any --format other than text or json.
func (rt *Runtime) ValidateOutputFormat() error {
	switch rt.OutputFormat() {
	case "text", "json":
		return nil
	default:
		return ExitError{
			code:     ExitCodeUsage,
			category: ErrorCategoryUsage,
			errCode:  "INVALID_OUTPUT_FORMAT",
			message:  "--format must be one of: text, json",
			field:    "format",
		}
	}
}

// Execute runs one command tree under the shared top-level contract: the ERROR
// log recorder that surfaces a logged cause in the JSON envelope, the legacy
// single-dash flag normalization, and the exit-code classification. It is the
// single place the binaries hand control to cobra.
func Execute(args []string, root *cobra.Command, rt *Runtime) int {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	root.SetArgs(NormalizeLegacyFlagArgs(args))
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)
	recorder := &ErrorRecorder{}
	slog.SetDefault(slog.New(NewLogHandler(os.Stderr, recorder)))

	if err := root.Execute(); err != nil {
		exit := ExitErrorFromErr(err)
		// Commands that only log their failure and return a bare code (the
		// trace store, config loading) would otherwise collapse into
		// COMMAND_FAILED/"command failed"; surface the logged cause instead.
		if recorder.Has() && fallback(exit.message, "command failed") == "command failed" {
			exit.message = recorder.Message()
		}
		// cobra fails before the persistent --format flag is bound when the
		// command itself is unknown, so read it from argv as a fallback.
		format := rt.OutputFormat()
		if format != "json" {
			if fromArgs := outputFormatFromArgs(args); fromArgs != "" {
				format = fromArgs
			}
		}
		// A command that wrote an ok=false envelope already reported the failure;
		// only the text form still needs the cause on stderr.
		if rt.ResultWritten() {
			if format != "json" {
				fmt.Fprintln(root.ErrOrStderr(), "error: "+exit.Error())
			}
			return exit.code
		}
		WriteError(root.ErrOrStderr(), format, exit)
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

// ExitErrorFromErr maps a command error to the documented exit contract:
// usage/validation errors exit 3, internal failures exit 8.
func ExitErrorFromErr(err error) ExitError {
	var exit ExitError
	if errors.As(err, &exit) {
		return exit
	}
	message := err.Error()
	if IsUsageError(err) {
		return UsageError(message, "")
	}
	return ExitError{
		code:     ExitCodeInternal,
		category: ErrorCategoryInternal,
		errCode:  "INTERNAL_ERROR",
		message:  message,
	}
}

// IsUsageError reports whether a bare cobra error is really a usage error.
func IsUsageError(err error) bool {
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

// ErrorRecorder keeps the most recent ERROR log line so a command that only
// logs its failure can still report the real cause in the JSON envelope.
type ErrorRecorder struct {
	mu       sync.Mutex
	lastText string
}

func (r *ErrorRecorder) record(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastText = message
}

// Has reports whether an ERROR line was recorded.
func (r *ErrorRecorder) Has() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(r.lastText) != ""
}

// Message is the most recent recorded ERROR line.
func (r *ErrorRecorder) Message() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastText
}

// LogHandler writes the usual text log and mirrors ERROR records into the
// recorder.
type LogHandler struct {
	primary  slog.Handler
	recorder *ErrorRecorder
}

// NewLogHandler returns the CLI's slog handler.
func NewLogHandler(w io.Writer, recorder *ErrorRecorder) slog.Handler {
	return &LogHandler{
		primary:  slog.NewTextHandler(w, nil),
		recorder: recorder,
	}
}

func (h *LogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.primary.Enabled(ctx, level)
}

func (h *LogHandler) Handle(ctx context.Context, record slog.Record) error {
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

func (h *LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &LogHandler{primary: h.primary.WithAttrs(attrs), recorder: h.recorder}
}

func (h *LogHandler) WithGroup(name string) slog.Handler {
	return &LogHandler{primary: h.primary.WithGroup(name), recorder: h.recorder}
}

// RunCode turns a command's integer exit code into the matching error, so a
// RunE body can hand a `func() int` helper straight back to cobra.
func RunCode(run func() int) error {
	if code := run(); code != 0 {
		return ExitErrorFromCode(code)
	}
	return nil
}

// UsageError builds the documented exit-3 error.
func UsageError(message string, field string) ExitError {
	return ExitError{
		code:     ExitCodeUsage,
		category: ErrorCategoryUsage,
		errCode:  "USAGE_ERROR",
		message:  message,
		field:    field,
	}
}

// APIError builds the documented exit-1 error: the command ran but the
// operation failed.
func APIError(message string) ExitError {
	return ExitError{
		code:     ExitCodeAPI,
		category: ErrorCategoryAPI,
		errCode:  "COMMAND_FAILED",
		message:  message,
	}
}

// RequireSubcommand renders help and exits 3 when a command group is invoked
// without a subcommand.
func RequireSubcommand(cmd *cobra.Command) error {
	if cmd.Flag("format") != nil && cmd.Flag("format").Value.String() == "json" {
		return UsageError(fmt.Sprintf("%s requires a subcommand", cmd.CommandPath()), "command")
	}
	if err := cmd.Help(); err != nil {
		return ExitError{
			code:     ExitCodeInternal,
			category: ErrorCategoryInternal,
			errCode:  "HELP_RENDER_FAILED",
			message:  err.Error(),
		}
	}
	return ExitErrorFromCode(ExitCodeUsage)
}

// NormalizeLegacyFlagArgs rewrites a legacy single-dash long flag (-config) into
// the double-dash form cobra expects.
func NormalizeLegacyFlagArgs(args []string) []string {
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

// ExitError is the CLI's own error: it carries the process exit code and the
// envelope fields.
type ExitError struct {
	code     int
	category string
	errCode  string
	message  string
	field    string
}

func (e ExitError) Error() string {
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("exit %d", e.code)
}

// Exit codes and error categories follow the documented contract.
const (
	ExitCodeAPI           = 1
	ExitCodeUsage         = 3
	ExitCodeInternal      = 8
	ErrorCategoryAPI      = "api"
	ErrorCategoryUsage    = "usage"
	ErrorCategoryInternal = "internal"
)

// ExitErrorFromCode maps a bare integer exit code onto the contract.
func ExitErrorFromCode(code int) ExitError {
	switch code {
	case 2, ExitCodeUsage:
		return ExitError{
			code:     ExitCodeUsage,
			category: ErrorCategoryUsage,
			errCode:  "USAGE_ERROR",
			message:  "command usage is invalid",
		}
	case ExitCodeInternal:
		return ExitError{
			code:     ExitCodeInternal,
			category: ErrorCategoryInternal,
			errCode:  "INTERNAL_ERROR",
			message:  "command failed with an internal error",
		}
	default:
		return ExitError{
			code:     code,
			category: ErrorCategoryAPI,
			errCode:  "COMMAND_FAILED",
			message:  "command failed",
		}
	}
}

// Envelope is the JSON result/error document both binaries emit.
type Envelope struct {
	OK       bool         `json:"ok"`
	Command  string       `json:"command,omitempty"`
	Result   any          `json:"result,omitempty"`
	Warnings []string     `json:"warnings"`
	Error    *ErrorDetail `json:"error,omitempty"`
}

// ErrorDetail is the machine-readable error inside an Envelope.
type ErrorDetail struct {
	Code        string `json:"code"`
	Category    string `json:"category"`
	Message     string `json:"message"`
	Field       string `json:"field,omitempty"`
	Retryable   bool   `json:"retryable"`
	SafeToRetry bool   `json:"safe_to_retry"`
}

// WriteResult writes the text form or a success envelope.
func WriteResult(w io.Writer, format string, command string, result any, text func(io.Writer) error) error {
	return WriteResultWithWarnings(w, format, command, result, ResultWarnings(result), text)
}

// WriteResultWithWarnings writes the text form or a JSON envelope. The
// envelope-level warnings mirror the ones the text form prints, so a caller that
// only reads envelope.warnings does not miss them.
func WriteResultWithWarnings(w io.Writer, format string, command string, result any, warnings []string, text func(io.Writer) error) error {
	return WriteResultWithState(w, format, command, true, result, warnings, text)
}

// WriteResultWithState writes the text form or a JSON envelope whose ok field
// reflects the command's own verdict (a check that found problems reports
// ok=false while still exiting non-zero through the returned error).
func WriteResultWithState(w io.Writer, format string, command string, ok bool, result any, warnings []string, text func(io.Writer) error) error {
	if format != "json" {
		if text == nil {
			return nil
		}
		return text(w)
	}
	if warnings == nil {
		warnings = []string{}
	}
	return WriteJSON(w, Envelope{
		OK:       ok,
		Command:  command,
		Result:   result,
		Warnings: warnings,
	})
}

// ResultWarnings collects the warnings a command attached to its own result, so
// the envelope carries them whether they are a []string field or a map entry.
func ResultWarnings(result any) []string {
	switch value := result.(type) {
	case map[string]any:
		return StringSlice(value["warnings"])
	case []map[string]any:
		var out []string
		for _, item := range value {
			out = append(out, StringSlice(item["warnings"])...)
		}
		return out
	}
	return warningsFromStruct(result)
}

// StringSlice coerces the usual JSON-ish shapes into a []string.
func StringSlice(value any) []string {
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
		out = append(out, StringSlice(field.Interface())...)
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
				out = append(out, StringSlice(warnings.Interface())...)
			}
		}
	}
	return out
}

// WriteError renders a failure in the selected format.
func WriteError(w io.Writer, format string, exit ExitError) {
	if format != "json" {
		if exit.message != "" {
			fmt.Fprintln(w, exit.message)
		}
		return
	}
	_ = WriteJSON(w, Envelope{
		OK:       false,
		Warnings: []string{},
		Error: &ErrorDetail{
			Code:        fallback(exit.errCode, "COMMAND_FAILED"),
			Category:    fallback(exit.category, ErrorCategoryAPI),
			Message:     fallback(exit.message, "command failed"),
			Field:       exit.field,
			Retryable:   exit.category != ErrorCategoryUsage,
			SafeToRetry: exit.category != ErrorCategoryUsage,
		},
	})
}

// WriteJSON writes an indented JSON document.
func WriteJSON(w io.Writer, value any) error {
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
