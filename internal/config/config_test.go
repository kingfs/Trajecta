package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestRedactDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "postgres url password",
			dsn:  "postgres://user:secret@example.com:5432/traces?sslmode=require",
			want: "postgres://user:<redacted>@example.com:5432/traces?sslmode=require",
		},
		{
			name: "postgres url without password",
			dsn:  "postgres://user@example.com:5432/traces",
			want: "postgres://user@example.com:5432/traces",
		},
		{
			name: "keyword password",
			dsn:  "host=localhost user=trace password=secret dbname=traces",
			want: "host=localhost user=trace password=<redacted> dbname=traces",
		},
		{
			name: "keyword password case insensitive",
			dsn:  "host=localhost user=trace Password=secret dbname=traces",
			want: "host=localhost user=trace Password=<redacted> dbname=traces",
		},
		{
			name: "sqlite path",
			dsn:  "./logs/trajecta.sqlite3",
			want: "./logs/trajecta.sqlite3",
		},
		{
			// lib/pq accepts keyword parameters as URL query values, so a URL
			// DSN can carry the password only in the query string. The password
			// in the userinfo was already redacted while this copy stayed
			// readable, and the DSN is printed by `db migrate` and logged at
			// startup.
			name: "postgres url password in query",
			dsn:  "postgres://user@example.com:5432/traces?password=secret",
			want: "postgres://user@example.com:5432/traces?password=<redacted>",
		},
		{
			name: "postgres url password in userinfo and query",
			dsn:  "postgres://user:secret@example.com:5432/traces?password=secret&sslmode=require",
			want: "postgres://user:<redacted>@example.com:5432/traces?password=<redacted>&sslmode=require",
		},
		{
			name: "postgres url other sensitive query keys",
			dsn:  "postgres://user@example.com/traces?api_key=k&passwd=p",
			want: "postgres://user@example.com/traces?api_key=<redacted>&passwd=<redacted>",
		},
		{
			name: "postgres url keeps non sensitive query order and values",
			dsn:  "postgres://user@example.com/traces?sslmode=require&application_name=trajecta",
			want: "postgres://user@example.com/traces?sslmode=require&application_name=trajecta",
		},
		{
			name: "keyword sslpassword",
			dsn:  "host=localhost user=trace sslpassword=secret dbname=traces",
			want: "host=localhost user=trace sslpassword=<redacted> dbname=traces",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RedactDSN(tt.dsn); got != tt.want {
				t.Fatalf("RedactDSN() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLitePathFromDSN(t *testing.T) {
	tests := []struct {
		name string
		dsn  string
		want string
	}{
		{name: "plain path", dsn: "/tmp/traces.sqlite3", want: "/tmp/traces.sqlite3"},
		{name: "sqlite url absolute", dsn: "sqlite:///tmp/traces.sqlite3", want: "/tmp/traces.sqlite3"},
		{name: "file uri with query", dsn: "file:/tmp/traces.sqlite3?mode=rwc&_pragma=busy_timeout(5000)", want: "/tmp/traces.sqlite3"},
		{name: "memory uri", dsn: "file::memory:?cache=shared", want: ":memory:"},
		{name: "empty", dsn: " ", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SQLitePathFromDSN(tt.dsn); got != tt.want {
				t.Fatalf("SQLitePathFromDSN() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLegacyOutputDirEnvOverridesTraceAndDebugOutput(t *testing.T) {
	t.Setenv("TRAJECTA_OUTPUT_DIR", "/app/data/traces")
	t.Setenv("TRAJECTA_TRACE_OUTPUT_DIR", "")

	cfg := Config{}
	cfg.Trace.OutputDir = "./logs"
	cfg.Debug.OutputDir = "./debug"
	applyEnvOverrides(&cfg)

	if cfg.TraceOutputDir() != "/app/data/traces" {
		t.Fatalf("TraceOutputDir() = %q, want /app/data/traces", cfg.TraceOutputDir())
	}
	if cfg.Debug.OutputDir != "/app/data/traces" {
		t.Fatalf("Debug.OutputDir = %q, want /app/data/traces", cfg.Debug.OutputDir)
	}
}

func TestTraceOutputDirEnvOverridesLegacyOutputDirEnv(t *testing.T) {
	t.Setenv("TRAJECTA_OUTPUT_DIR", "/app/data/legacy")
	t.Setenv("TRAJECTA_TRACE_OUTPUT_DIR", "/app/data/traces")

	cfg := Config{}
	cfg.Trace.OutputDir = "./logs"
	cfg.Debug.OutputDir = "./debug"
	applyEnvOverrides(&cfg)

	if cfg.TraceOutputDir() != "/app/data/traces" {
		t.Fatalf("TraceOutputDir() = %q, want /app/data/traces", cfg.TraceOutputDir())
	}
	if cfg.Debug.OutputDir != "/app/data/legacy" {
		t.Fatalf("Debug.OutputDir = %q, want /app/data/legacy", cfg.Debug.OutputDir)
	}
}

func TestDatabaseUseSessionSummaryReadDefaultsOffAndLoadsConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
database:
  use_session_summary_read: true
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	defaultCfg := Config{}
	if defaultCfg.DatabaseUseSessionSummaryRead() {
		t.Fatal("DatabaseUseSessionSummaryRead() default = true, want false")
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.DatabaseUseSessionSummaryRead() {
		t.Fatal("DatabaseUseSessionSummaryRead() = false, want true")
	}
}

func TestDatabaseUseSessionSummaryReadEnvOverride(t *testing.T) {
	t.Setenv("TRAJECTA_DATABASE_USE_SESSION_SUMMARY_READ", "true")

	cfg := Config{}
	applyEnvOverrides(&cfg)
	if !cfg.DatabaseUseSessionSummaryRead() {
		t.Fatal("DatabaseUseSessionSummaryRead() = false, want env override true")
	}
}

func TestLoadExpandsEnvReferences(t *testing.T) {
	t.Setenv("TRAJECTA_TEST_DATABASE_DSN", "postgres://user:pass@example.com:5432/traces")
	path := writeTempConfig(t, `
database:
  dsn: "$env:TRAJECTA_TEST_DATABASE_DSN"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.DSN != "postgres://user:pass@example.com:5432/traces" {
		t.Fatalf("database.dsn = %q, want the expanded value", cfg.Database.DSN)
	}
}

func TestLoadLimitConfigDisabledByDefault(t *testing.T) {
	cfg := Config{}
	if cfg.Limits.Enabled {
		t.Fatalf("Limits.Enabled = true, want false")
	}
	if cfg.Limits.LocalConcurrencyEnabled() {
		t.Fatalf("LocalConcurrencyEnabled() = true, want false")
	}
}

func TestLoadLimitConfigFromYAML(t *testing.T) {
	path := writeTempConfig(t, `
limits:
  enabled: true
  scope: "header"
  max_concurrent: 1
  max_queued: 2
  channel_key_header: "X-Trajecta-Channel"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Limits.LocalConcurrencyEnabled() {
		t.Fatalf("LocalConcurrencyEnabled() = false, want true")
	}
	if cfg.Limits.MaxConcurrent != 1 || cfg.Limits.MaxQueued != 2 || cfg.Limits.ChannelKeyHeader != "X-Trajecta-Channel" || cfg.Limits.ScopeOrDefault() != "header" {
		t.Fatalf("Limits = %+v", cfg.Limits)
	}
}

func TestResponsesServerConfigDefaults(t *testing.T) {
	cfg := Config{}
	if cfg.ResponsesDefaultModel() != "" {
		t.Fatalf("ResponsesDefaultModel() = %q, want empty", cfg.ResponsesDefaultModel())
	}
	if cfg.ResponsesForceStore() {
		t.Fatalf("ResponsesForceStore() = true, want false")
	}
	if got := cfg.ResponsesMaxRequestBodyBytes(); got != 64<<20 {
		t.Fatalf("ResponsesMaxRequestBodyBytes() = %d, want %d", got, 64<<20)
	}
	if got := cfg.ResponsesServerPath(); got != "/v1/responses" {
		t.Fatalf("ResponsesServerPath() = %q, want /v1/responses", got)
	}
	if cfg.ResponsesAutoCompactEnabled() {
		t.Fatalf("ResponsesAutoCompactEnabled() = true, want false")
	}
	if got := cfg.ResponsesCompactHistoryItemThreshold(); got != 0 {
		t.Fatalf("ResponsesCompactHistoryItemThreshold() = %d, want 0", got)
	}
	if got := cfg.ResponsesModelProfiles(); len(got) != 0 {
		t.Fatalf("ResponsesModelProfiles() len = %d, want 0", len(got))
	}
	executors := cfg.ResponsesFunctionExecutorsConfig()
	if executors.Enabled {
		t.Fatalf("ResponsesFunctionExecutorsConfig().Enabled = true, want false")
	}
	if executors.Timeout != 5*time.Second {
		t.Fatalf("ResponsesFunctionExecutorsConfig().Timeout = %v, want 5s", executors.Timeout)
	}
	if executors.MaxResultBytes != 64<<10 {
		t.Fatalf("ResponsesFunctionExecutorsConfig().MaxResultBytes = %d, want %d", executors.MaxResultBytes, 64<<10)
	}
	codexCompat := cfg.ResponsesCodexCompatConfig()
	if codexCompat.Enabled {
		t.Fatalf("ResponsesCodexCompatConfig().Enabled = true, want false")
	}
	if len(codexCompat.AutoInjectHostedTools) != 0 {
		t.Fatalf("ResponsesCodexCompatConfig().AutoInjectHostedTools = %v, want empty", codexCompat.AutoInjectHostedTools)
	}
	if codexCompat.InjectWhenToolsAbsent == nil || !*codexCompat.InjectWhenToolsAbsent {
		t.Fatalf("ResponsesCodexCompatConfig().InjectWhenToolsAbsent = %v, want true", codexCompat.InjectWhenToolsAbsent)
	}
	if codexCompat.PreserveClientTools == nil || !*codexCompat.PreserveClientTools {
		t.Fatalf("ResponsesCodexCompatConfig().PreserveClientTools = %v, want true", codexCompat.PreserveClientTools)
	}
	if codexCompat.DefaultToolChoice != "auto" {
		t.Fatalf("ResponsesCodexCompatConfig().DefaultToolChoice = %#v, want auto", codexCompat.DefaultToolChoice)
	}
}

func TestLoadParsesResponsesServerConfigFromYAML(t *testing.T) {
	workingDir := t.TempDir()
	commandPath := filepath.Join(workingDir, "lookup")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write command fixture: %v", err)
	}
	path := writeTempConfig(t, fmt.Sprintf(`
responses_server:
  default_model: "qwen3"
  force_store: true
  max_request_body_bytes: 1048576
  path: "/v1/responses"
  auto_compact: true
  compact_history_item_threshold: 12
  codex_compat:
    enabled: true
    auto_inject_hosted_tools: [" web_search ", "mcp"]
    inject_when_tools_absent: false
    preserve_client_tools: false
    default_tool_choice: required
  function_executors:
    enabled: true
    timeout: 2s
    max_result_bytes: 128
    redaction:
      arguments: true
      output: true
    executors:
      - name: "lookup"
        type: "static_response"
        output:
          ok: true
      - name: "disabled_lookup"
        type: "static_response"
        enabled: false
        output: "off"
      - name: "run_lookup"
        type: "external_command"
        command: " %s "
        args: ["ok"]
        timeout: 1s
        env:
          STATIC_VALUE: "static"
        env_allowlist: [" PATH "]
        process:
          working_dir: %q
          require_absolute_command: true
          allowed_command_dirs: [" %s "]
          reject_root: true
  model_profiles:
    - name: "qwen3"
      context_window_tokens: 32768
      max_output_tokens: 4096
      tool_output_token_limit: 6000
      model_reasoning_effort: " high "
      compact_history_item_threshold: 8
      upstream_model: "qwen/qwen3"
      tokenize_counter:
        enabled: true
        upstream_id: " primary "
        timeout: 750ms
    - pattern: "gpt-4o*"
      compact_history_item_threshold: 6
`, commandPath, workingDir, workingDir))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ResponsesDefaultModel(); got != "qwen3" {
		t.Fatalf("ResponsesDefaultModel() = %q, want qwen3", got)
	}
	if !cfg.ResponsesForceStore() {
		t.Fatalf("ResponsesForceStore() = false, want true")
	}
	if got := cfg.ResponsesMaxRequestBodyBytes(); got != 1048576 {
		t.Fatalf("ResponsesMaxRequestBodyBytes() = %d, want 1048576", got)
	}
	if got := cfg.ResponsesServerPath(); got != "/v1/responses" {
		t.Fatalf("ResponsesServerPath() = %q, want /v1/responses", got)
	}
	if !cfg.ResponsesAutoCompactEnabled() {
		t.Fatalf("ResponsesAutoCompactEnabled() = false, want true")
	}
	if got := cfg.ResponsesCompactHistoryItemThreshold(); got != 12 {
		t.Fatalf("ResponsesCompactHistoryItemThreshold() = %d, want 12", got)
	}
	codexCompat := cfg.ResponsesCodexCompatConfig()
	if !codexCompat.Enabled || len(codexCompat.AutoInjectHostedTools) != 2 || codexCompat.AutoInjectHostedTools[0] != "web_search" || codexCompat.AutoInjectHostedTools[1] != "mcp" || codexCompat.InjectWhenToolsAbsent == nil || *codexCompat.InjectWhenToolsAbsent || codexCompat.PreserveClientTools == nil || *codexCompat.PreserveClientTools || codexCompat.DefaultToolChoice != "required" {
		t.Fatalf("ResponsesCodexCompatConfig() = %+v, want YAML values", codexCompat)
	}
	profiles := cfg.ResponsesModelProfiles()
	if len(profiles) != 2 {
		t.Fatalf("ResponsesModelProfiles() len = %d, want 2", len(profiles))
	}
	if got := profiles[0]; got.Name != "qwen3" || got.ContextWindowTokens != 32768 || got.MaxOutputTokens != 4096 || got.ToolOutputTokenLimit != 6000 || got.ModelReasoningEffort != "high" || got.CompactHistoryItemThreshold != 8 || got.UpstreamModel != "qwen/qwen3" {
		t.Fatalf("first model profile = %+v", got)
	}
	if got := profiles[0].TokenizeCounter; got.Enabled == nil || !*got.Enabled || got.UpstreamID != "primary" || got.Timeout != 750*time.Millisecond {
		t.Fatalf("first model profile tokenize_counter = %+v, want enabled primary 750ms", got)
	}
	if got := profiles[1]; got.Pattern != "gpt-4o*" || got.CompactHistoryItemThreshold != 6 {
		t.Fatalf("second model profile = %+v", got)
	}
	executors := cfg.ResponsesFunctionExecutorsConfig()
	if !executors.Enabled || executors.Timeout != 2*time.Second || executors.MaxResultBytes != 128 || !executors.Redaction.Arguments || !executors.Redaction.Output {
		t.Fatalf("function executors policy = %+v, want enabled 2s 128 redacted", executors)
	}
	if len(executors.Executors) != 3 {
		t.Fatalf("function executors len = %d, want 3", len(executors.Executors))
	}
	if got := executors.Executors[0]; got.Name != "lookup" || got.Type != "static_response" {
		t.Fatalf("first function executor = %+v", got)
	}
	if got := executors.Executors[1]; got.Name != "disabled_lookup" || got.Enabled == nil || *got.Enabled {
		t.Fatalf("second function executor = %+v", got)
	}
	if got := executors.Executors[2]; got.Name != "run_lookup" || got.Type != "external_command" || got.Command != commandPath || got.Timeout != time.Second || len(got.Args) != 1 || got.Args[0] != "ok" || got.Env["STATIC_VALUE"] != "static" || len(got.EnvAllowlist) != 1 || got.EnvAllowlist[0] != "PATH" || got.Process.WorkingDir != workingDir || !got.Process.RequireAbsoluteCommand || len(got.Process.AllowedCommandDirs) != 1 || got.Process.AllowedCommandDirs[0] != workingDir || !got.Process.RejectRoot {
		t.Fatalf("third function executor = %+v", got)
	}
}

func TestResponsesFunctionExecutorsConfigValidation(t *testing.T) {
	disabled := false
	cfg := Config{}
	cfg.ResponsesServer.FunctionExecutors.Enabled = true
	cfg.ResponsesServer.FunctionExecutors.Executors = []ResponsesFunctionExecutorBinding{
		{Name: " lookup ", Type: " STATIC_RESPONSE ", Output: "ok"},
		{Name: "lookup", Type: "static_response", Output: "duplicate"},
		{Name: " ", Type: "static_response", Output: "missing name"},
		{Name: "future", Type: "external_command", Command: "echo ok"},
		{Name: "mystery", Type: "unknown_type"},
		{Name: "off", Type: "static_response", Enabled: &disabled},
	}

	executors := cfg.ResponsesFunctionExecutorsConfig()
	if len(executors.Executors) != 6 {
		t.Fatalf("len(executors) = %d, want 6", len(executors.Executors))
	}
	if got := executors.Executors[0]; got.Name != "lookup" || got.Type != "static_response" || !got.Available || len(got.Warnings) != 0 {
		t.Fatalf("first executor = %+v, want normalized available static_response", got)
	}
	if got := executors.Executors[1]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "duplicate executor name") {
		t.Fatalf("duplicate executor = %+v, want unavailable duplicate warning", got)
	}
	if got := executors.Executors[2]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "name is required") {
		t.Fatalf("empty-name executor = %+v, want unavailable name warning", got)
	}
	if got := executors.Executors[3]; !got.Available || got.Command != "echo ok" || len(got.Warnings) != 0 {
		t.Fatalf("external command executor = %+v, want available external command", got)
	}
	if got := executors.Executors[4]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "unsupported executor type") {
		t.Fatalf("unknown executor = %+v, want unavailable unsupported warning", got)
	}
	if got := executors.Executors[5]; got.Available || len(got.Warnings) != 0 {
		t.Fatalf("disabled executor = %+v, want unavailable without validation warning", got)
	}
	if got := strings.Join(executors.Warnings, " "); strings.Contains(got, "no available executors") {
		t.Fatalf("warnings = %q, did not expect no-available warning when one static executor is available", got)
	}
}

func TestMatchResponsesModelProfilePrefersExactBeforePattern(t *testing.T) {
	cfg := Config{}
	cfg.ResponsesServer.ModelProfiles = []ResponsesModelProfileConfig{
		{
			Pattern:             "gpt-*",
			ContextWindowTokens: 100,
		},
		{
			Name:                "gpt-5",
			ContextWindowTokens: 200,
		},
	}

	match := cfg.MatchResponsesModelProfile("gpt-5")
	if !match.Matched || match.Kind != "exact" || match.Index != 1 || match.Source != "responses_server.model_profiles[1].name" {
		t.Fatalf("exact match = %+v", match)
	}
	if match.Profile.ContextWindowTokens != 200 {
		t.Fatalf("exact profile context window = %d, want 200", match.Profile.ContextWindowTokens)
	}
}

func TestMatchResponsesModelProfileFallsBackToPattern(t *testing.T) {
	cfg := Config{}
	cfg.ResponsesServer.ModelProfiles = []ResponsesModelProfileConfig{
		{
			Name: "qwen3",
		},
		{
			Pattern:                     "gpt-4o*",
			CompactHistoryItemThreshold: 8,
		},
	}

	match := cfg.MatchResponsesModelProfile("gpt-4o-mini")
	if !match.Matched || match.Kind != "pattern" || match.Index != 1 || match.Source != "responses_server.model_profiles[1].pattern" {
		t.Fatalf("pattern match = %+v", match)
	}
	if match.Profile.CompactHistoryItemThreshold != 8 {
		t.Fatalf("pattern profile threshold = %d, want 8", match.Profile.CompactHistoryItemThreshold)
	}
}

func TestMatchResponsesModelProfileNoProfile(t *testing.T) {
	cfg := Config{}
	cfg.ResponsesServer.ModelProfiles = []ResponsesModelProfileConfig{
		{Name: "qwen3"},
		{Pattern: "gpt-4o*"},
	}

	match := cfg.MatchResponsesModelProfile("claude")
	if match.Matched || match.Index != -1 || match.Source != "none" {
		t.Fatalf("no match = %+v", match)
	}
}

func TestResponsesFunctionExecutorsConfigWarnsWhenEnabledWithoutAvailableExecutor(t *testing.T) {
	disabled := false
	cfg := Config{}
	cfg.ResponsesServer.FunctionExecutors.Enabled = true
	cfg.ResponsesServer.FunctionExecutors.Executors = []ResponsesFunctionExecutorBinding{
		{Name: "future", Type: "external_command"},
		{Name: "off", Type: "static_response", Enabled: &disabled},
	}

	executors := cfg.ResponsesFunctionExecutorsConfig()
	if got := strings.Join(executors.Warnings, " "); !strings.Contains(got, "no available executors") {
		t.Fatalf("warnings = %q, want no available executors warning", got)
	}
}

func TestResponsesFunctionExecutorsConfigValidatesExternalCommandProcessIsolation(t *testing.T) {
	cfg := Config{}
	cfg.ResponsesServer.FunctionExecutors.Enabled = true
	cfg.ResponsesServer.FunctionExecutors.Executors = []ResponsesFunctionExecutorBinding{
		{
			Name:    "relative_working_dir",
			Type:    "external_command",
			Command: "/bin/echo",
			Process: ResponsesFunctionExecutorProcessConfig{
				WorkingDir: "relative-dir",
			},
		},
		{
			Name:    "relative_command",
			Type:    "external_command",
			Command: "echo",
			Process: ResponsesFunctionExecutorProcessConfig{
				RequireAbsoluteCommand: true,
			},
		},
	}

	executors := cfg.ResponsesFunctionExecutorsConfig()
	if len(executors.Executors) != 2 {
		t.Fatalf("len(executors) = %d, want 2", len(executors.Executors))
	}
	if got := executors.Executors[0]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "working_dir must be absolute") {
		t.Fatalf("relative working dir executor = %+v, want unavailable working_dir warning", got)
	}
	if got := executors.Executors[1]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "command must be absolute") {
		t.Fatalf("relative command executor = %+v, want unavailable absolute command warning", got)
	}
	if got := strings.Join(executors.Warnings, " "); !strings.Contains(got, "no available executors") {
		t.Fatalf("warnings = %q, want no available executors warning", got)
	}
}

func TestResponsesFunctionExecutorsConfigValidatesAllowedCommandDirs(t *testing.T) {
	allowedDir := t.TempDir()
	commandPath := filepath.Join(allowedDir, "lookup")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write command fixture: %v", err)
	}
	filePath := filepath.Join(t.TempDir(), "not-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file fixture: %v", err)
	}
	outsideCommand := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outsideCommand, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write outside command fixture: %v", err)
	}

	cfg := Config{}
	cfg.ResponsesServer.FunctionExecutors.Enabled = true
	cfg.ResponsesServer.FunctionExecutors.Executors = []ResponsesFunctionExecutorBinding{
		{
			Name:    "allowed",
			Type:    "external_command",
			Command: commandPath,
			Process: ResponsesFunctionExecutorProcessConfig{
				AllowedCommandDirs: []string{" " + allowedDir + " ", ""},
				RejectRoot:         true,
			},
		},
		{
			Name:    "relative_allowed_dir",
			Type:    "external_command",
			Command: commandPath,
			Process: ResponsesFunctionExecutorProcessConfig{
				AllowedCommandDirs: []string{"relative-dir"},
			},
		},
		{
			Name:    "missing_allowed_dir",
			Type:    "external_command",
			Command: commandPath,
			Process: ResponsesFunctionExecutorProcessConfig{
				AllowedCommandDirs: []string{filepath.Join(t.TempDir(), "missing")},
			},
		},
		{
			Name:    "file_allowed_dir",
			Type:    "external_command",
			Command: commandPath,
			Process: ResponsesFunctionExecutorProcessConfig{
				AllowedCommandDirs: []string{filePath},
			},
		},
		{
			Name:    "outside_allowed_dir",
			Type:    "external_command",
			Command: outsideCommand,
			Process: ResponsesFunctionExecutorProcessConfig{
				AllowedCommandDirs: []string{allowedDir},
			},
		},
		{
			Name:    "root_rejected",
			Type:    "external_command",
			Command: commandPath,
			Process: ResponsesFunctionExecutorProcessConfig{
				AllowedCommandDirs: []string{string(filepath.Separator)},
				RejectRoot:         true,
			},
		},
	}

	executors := cfg.ResponsesFunctionExecutorsConfig()
	if len(executors.Executors) != 6 {
		t.Fatalf("len(executors) = %d, want 6", len(executors.Executors))
	}
	if got := executors.Executors[0]; !got.Available || len(got.Warnings) != 0 || len(got.Process.AllowedCommandDirs) != 1 || got.Process.AllowedCommandDirs[0] != allowedDir || !got.Process.RejectRoot {
		t.Fatalf("allowed executor = %+v, want available with trimmed allowed dirs", got)
	}
	if got := executors.Executors[1]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "allowed_command_dirs entries must be absolute") {
		t.Fatalf("relative allowed dir executor = %+v, want absolute warning", got)
	}
	if got := executors.Executors[2]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "allowed_command_dirs entry is not accessible") {
		t.Fatalf("missing allowed dir executor = %+v, want accessible warning", got)
	}
	if got := executors.Executors[3]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "allowed_command_dirs entries must be directories") {
		t.Fatalf("file allowed dir executor = %+v, want directory warning", got)
	}
	if got := executors.Executors[4]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "command must resolve inside process.allowed_command_dirs") {
		t.Fatalf("outside command executor = %+v, want allowed dir warning", got)
	}
	if got := executors.Executors[5]; got.Available || !strings.Contains(strings.Join(got.Warnings, " "), "must not include filesystem root") {
		t.Fatalf("root rejected executor = %+v, want reject_root warning", got)
	}
}

func TestResponsesServerEnvOverrides(t *testing.T) {
	// TRAJECTA_RESPONSES_ENABLED no longer exists: the local Responses
	// execution mode is always available, so the legacy variable is ignored.
	t.Setenv("TRAJECTA_RESPONSES_ENABLED", "false")
	t.Setenv("TRAJECTA_RESPONSES_DEFAULT_MODEL", "env-model")
	t.Setenv("TRAJECTA_RESPONSES_FORCE_STORE", "true")
	t.Setenv("TRAJECTA_RESPONSES_MAX_REQUEST_BODY_BYTES", "2097152")
	t.Setenv("TRAJECTA_RESPONSES_PATH", "/custom/responses")
	t.Setenv("TRAJECTA_RESPONSES_AUTO_COMPACT", "true")
	t.Setenv("TRAJECTA_RESPONSES_COMPACT_HISTORY_ITEM_THRESHOLD", "7")
	t.Setenv("TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_ENABLED", "true")
	t.Setenv("TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_TIMEOUT", "3s")
	t.Setenv("TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_MAX_RESULT_BYTES", "256")
	t.Setenv("TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_REDACT_ARGUMENTS", "true")
	t.Setenv("TRAJECTA_RESPONSES_FUNCTION_EXECUTORS_REDACT_OUTPUT", "true")
	t.Setenv("TRAJECTA_RESPONSES_CODEX_COMPAT_ENABLED", "true")
	t.Setenv("TRAJECTA_RESPONSES_CODEX_COMPAT_AUTO_INJECT_HOSTED_TOOLS", " web_search, mcp ,,")
	t.Setenv("TRAJECTA_RESPONSES_CODEX_COMPAT_INJECT_WHEN_TOOLS_ABSENT", "false")
	t.Setenv("TRAJECTA_RESPONSES_CODEX_COMPAT_PRESERVE_CLIENT_TOOLS", "false")
	t.Setenv("TRAJECTA_RESPONSES_CODEX_COMPAT_DEFAULT_TOOL_CHOICE", "required")

	cfg := Config{}
	cfg.ResponsesServer.DefaultModel = "yaml-model"
	cfg.ResponsesServer.MaxRequestBodyBytes = 1024
	applyEnvOverrides(&cfg)

	if got := cfg.ResponsesDefaultModel(); got != "env-model" {
		t.Fatalf("ResponsesDefaultModel() = %q, want env-model", got)
	}
	if !cfg.ResponsesForceStore() {
		t.Fatalf("ResponsesForceStore() = false, want true")
	}
	if got := cfg.ResponsesMaxRequestBodyBytes(); got != 2097152 {
		t.Fatalf("ResponsesMaxRequestBodyBytes() = %d, want 2097152", got)
	}
	if got := cfg.ResponsesServerPath(); got != "/custom/responses" {
		t.Fatalf("ResponsesServerPath() = %q, want /custom/responses", got)
	}
	if !cfg.ResponsesAutoCompactEnabled() {
		t.Fatalf("ResponsesAutoCompactEnabled() = false, want true")
	}
	if got := cfg.ResponsesCompactHistoryItemThreshold(); got != 7 {
		t.Fatalf("ResponsesCompactHistoryItemThreshold() = %d, want 7", got)
	}
	executors := cfg.ResponsesFunctionExecutorsConfig()
	if !executors.Enabled || executors.Timeout != 3*time.Second || executors.MaxResultBytes != 256 || !executors.Redaction.Arguments || !executors.Redaction.Output {
		t.Fatalf("ResponsesFunctionExecutorsConfig() = %+v, want env overrides", executors)
	}
	codexCompat := cfg.ResponsesCodexCompatConfig()
	if !codexCompat.Enabled || len(codexCompat.AutoInjectHostedTools) != 2 || codexCompat.AutoInjectHostedTools[0] != "web_search" || codexCompat.AutoInjectHostedTools[1] != "mcp" || codexCompat.InjectWhenToolsAbsent == nil || *codexCompat.InjectWhenToolsAbsent || codexCompat.PreserveClientTools == nil || *codexCompat.PreserveClientTools || codexCompat.DefaultToolChoice != "required" {
		t.Fatalf("ResponsesCodexCompatConfig() = %+v, want env overrides", codexCompat)
	}
}

func TestWebSearchConfigDisabledByDefault(t *testing.T) {
	cfg := Config{}
	webSearch := cfg.WebSearchConfig()

	if cfg.WebSearchEnabled() {
		t.Fatalf("WebSearchEnabled() = true, want false")
	}
	if webSearch.Enabled {
		t.Fatalf("WebSearchConfig().Enabled = true, want false")
	}
	if webSearch.Provider != "disabled" {
		t.Fatalf("WebSearchConfig().Provider = %q, want disabled", webSearch.Provider)
	}
	if webSearch.MaxResults != 5 {
		t.Fatalf("WebSearchConfig().MaxResults = %d, want 5", webSearch.MaxResults)
	}
	if webSearch.BaseURL != "" {
		t.Fatalf("WebSearchConfig().BaseURL = %q, want empty", webSearch.BaseURL)
	}
	if webSearch.TimeoutMS != 5000 {
		t.Fatalf("WebSearchConfig().TimeoutMS = %d, want 5000", webSearch.TimeoutMS)
	}
	if webSearch.UserAgent != "trajecta web_search" {
		t.Fatalf("WebSearchConfig().UserAgent = %q, want default", webSearch.UserAgent)
	}
}

func TestWebSearchEnvOverrides(t *testing.T) {
	t.Setenv("TRAJECTA_TOOLS_WEB_SEARCH_ENABLED", "true")
	t.Setenv("TRAJECTA_TOOLS_WEB_SEARCH_PROVIDER", "searxng")
	t.Setenv("TRAJECTA_TOOLS_WEB_SEARCH_MAX_RESULTS", "3")
	t.Setenv("TRAJECTA_TOOLS_WEB_SEARCH_BASE_URL", "http://127.0.0.1:8888")
	t.Setenv("TRAJECTA_TOOLS_WEB_SEARCH_TIMEOUT_MS", "2500")
	t.Setenv("TRAJECTA_TOOLS_WEB_SEARCH_USER_AGENT", "trajecta-test")

	cfg := Config{}
	cfg.Tools.WebSearch.Provider = "mock"
	cfg.Tools.WebSearch.MaxResults = 2
	cfg.Tools.WebSearch.TimeoutMS = 1000
	applyEnvOverrides(&cfg)

	webSearch := cfg.WebSearchConfig()
	if !cfg.WebSearchEnabled() {
		t.Fatalf("WebSearchEnabled() = false, want true")
	}
	if webSearch.Provider != "searxng" {
		t.Fatalf("WebSearchConfig().Provider = %q, want searxng", webSearch.Provider)
	}
	if webSearch.MaxResults != 3 {
		t.Fatalf("WebSearchConfig().MaxResults = %d, want 3", webSearch.MaxResults)
	}
	if webSearch.BaseURL != "http://127.0.0.1:8888" {
		t.Fatalf("WebSearchConfig().BaseURL = %q", webSearch.BaseURL)
	}
	if webSearch.TimeoutMS != 2500 {
		t.Fatalf("WebSearchConfig().TimeoutMS = %d, want 2500", webSearch.TimeoutMS)
	}
	if webSearch.UserAgent != "trajecta-test" {
		t.Fatalf("WebSearchConfig().UserAgent = %q", webSearch.UserAgent)
	}
}

func TestLoadParsesWebSearchConfigFromYAML(t *testing.T) {
	clearWebSearchEnv(t)
	path := writeTempConfig(t, `
tools:
  web_search:
    enabled: true
    provider: "mock"
    max_results: 2
    base_url: "http://127.0.0.1:8888"
    timeout_ms: 1500
    user_agent: "yaml-agent"
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	webSearch := cfg.WebSearchConfig()
	if !cfg.WebSearchEnabled() {
		t.Fatalf("WebSearchEnabled() = false, want true")
	}
	if webSearch.Provider != "mock" {
		t.Fatalf("WebSearchConfig().Provider = %q, want mock", webSearch.Provider)
	}
	if webSearch.MaxResults != 2 {
		t.Fatalf("WebSearchConfig().MaxResults = %d, want 2", webSearch.MaxResults)
	}
	if webSearch.BaseURL != "http://127.0.0.1:8888" {
		t.Fatalf("WebSearchConfig().BaseURL = %q", webSearch.BaseURL)
	}
	if webSearch.TimeoutMS != 1500 {
		t.Fatalf("WebSearchConfig().TimeoutMS = %d, want 1500", webSearch.TimeoutMS)
	}
	if webSearch.UserAgent != "yaml-agent" {
		t.Fatalf("WebSearchConfig().UserAgent = %q", webSearch.UserAgent)
	}
}

func TestMCPToolsConfigDisabledByDefault(t *testing.T) {
	clearMCPToolsEnv(t)
	cfg := Config{}
	mcpTools := cfg.MCPToolsConfig()

	if cfg.MCPToolsEnabled() {
		t.Fatalf("MCPToolsEnabled() = true, want false")
	}
	if mcpTools.Enabled {
		t.Fatalf("MCPToolsConfig().Enabled = true, want false")
	}
	if mcpTools.DefaultTimeoutMS != 60000 {
		t.Fatalf("MCPToolsConfig().DefaultTimeoutMS = %d, want 60000", mcpTools.DefaultTimeoutMS)
	}
	if mcpTools.MaxResultBytes != 65536 {
		t.Fatalf("MCPToolsConfig().MaxResultBytes = %d, want 65536", mcpTools.MaxResultBytes)
	}
	if len(mcpTools.Servers) != 0 {
		t.Fatalf("MCPToolsConfig().Servers = %d, want 0", len(mcpTools.Servers))
	}
}

func TestMCPToolsEnvOverrides(t *testing.T) {
	t.Setenv("TRAJECTA_TOOLS_MCP_ENABLED", "true")
	t.Setenv("TRAJECTA_TOOLS_MCP_DEFAULT_TIMEOUT_MS", "2500")
	t.Setenv("TRAJECTA_TOOLS_MCP_MAX_RESULT_BYTES", "4096")

	cfg := Config{}
	cfg.Tools.MCP.DefaultTimeoutMS = 1000
	cfg.Tools.MCP.MaxResultBytes = 2048
	applyEnvOverrides(&cfg)

	mcpTools := cfg.MCPToolsConfig()
	if !cfg.MCPToolsEnabled() {
		t.Fatalf("MCPToolsEnabled() = false, want true")
	}
	if mcpTools.DefaultTimeoutMS != 2500 {
		t.Fatalf("MCPToolsConfig().DefaultTimeoutMS = %d, want 2500", mcpTools.DefaultTimeoutMS)
	}
	if mcpTools.MaxResultBytes != 4096 {
		t.Fatalf("MCPToolsConfig().MaxResultBytes = %d, want 4096", mcpTools.MaxResultBytes)
	}
}

func TestLoadParsesMCPToolsConfigFromYAML(t *testing.T) {
	clearMCPToolsEnv(t)
	path := writeTempConfig(t, `
tools:
  mcp:
    enabled: true
    default_timeout_ms: 1500
    max_result_bytes: 8192
    servers:
      - id: linear
        label: Linear
        url: "https://mcp.example.com/sse?api_key=query-secret"
        bearer_token_env: LINEAR_MCP_TOKEN
        enabled_tools: [create_issue, list_issues]
        disabled_tools: [delete_issue]
      - id: disabled
        url: "https://disabled.example.com/mcp"
        enabled: false
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	mcpTools := cfg.MCPToolsConfig()
	if !cfg.MCPToolsEnabled() {
		t.Fatalf("MCPToolsEnabled() = false, want true")
	}
	if mcpTools.DefaultTimeoutMS != 1500 || mcpTools.MaxResultBytes != 8192 {
		t.Fatalf("MCPToolsConfig() = %+v, want YAML timeout/result limits", mcpTools)
	}
	if len(mcpTools.Servers) != 2 {
		t.Fatalf("len(MCPToolsConfig().Servers) = %d, want 2", len(mcpTools.Servers))
	}
	first := mcpTools.Servers[0]
	if first.ID != "linear" || first.Label != "Linear" || first.URL != "https://mcp.example.com/sse?api_key=query-secret" || first.BearerTokenEnv != "LINEAR_MCP_TOKEN" || !first.EnabledOrDefault() {
		t.Fatalf("first server = %+v, want parsed enabled server", first)
	}
	if strings.Join(first.EnabledTools, ",") != "create_issue,list_issues" || strings.Join(first.DisabledTools, ",") != "delete_issue" {
		t.Fatalf("first server tools = %+v/%+v", first.EnabledTools, first.DisabledTools)
	}
	if mcpTools.Servers[1].EnabledOrDefault() {
		t.Fatalf("second server EnabledOrDefault() = true, want false")
	}
}

func TestLimitScopeDefaults(t *testing.T) {
	if got := (LimitConfig{}).ScopeOrDefault(); got != "global" {
		t.Fatalf("ScopeOrDefault() = %q, want global", got)
	}
	if got := (LimitConfig{ChannelKeyHeader: "X-Trajecta-Channel"}).ScopeOrDefault(); got != "header" {
		t.Fatalf("ScopeOrDefault() = %q, want header", got)
	}
	if got := (LimitConfig{Scope: " Credential ", ChannelKeyHeader: "X-Trajecta-Channel"}).ScopeOrDefault(); got != "credential" {
		t.Fatalf("ScopeOrDefault() = %q, want credential", got)
	}
}

func TestLoadFailsWhenEnvReferenceMissing(t *testing.T) {
	path := writeTempConfig(t, `
database:
  dsn: "$env:DOES_NOT_EXIST_FOR_TEST"
`)

	if _, err := Load(path); err == nil {
		t.Fatalf("Load() error = nil, want missing env error")
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func clearWebSearchEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"TRAJECTA_TOOLS_WEB_SEARCH_ENABLED",
		"TRAJECTA_TOOLS_WEB_SEARCH_PROVIDER",
		"TRAJECTA_TOOLS_WEB_SEARCH_MAX_RESULTS",
		"TRAJECTA_TOOLS_WEB_SEARCH_BASE_URL",
		"TRAJECTA_TOOLS_WEB_SEARCH_TIMEOUT_MS",
		"TRAJECTA_TOOLS_WEB_SEARCH_USER_AGENT",
	} {
		t.Setenv(name, "")
	}
}

func clearMCPToolsEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"TRAJECTA_TOOLS_MCP_ENABLED",
		"TRAJECTA_TOOLS_MCP_DEFAULT_TIMEOUT_MS",
		"TRAJECTA_TOOLS_MCP_MAX_RESULT_BYTES",
	} {
		t.Setenv(name, "")
	}
}

func TestUnknownConfigKeysReportsRemovedOptions(t *testing.T) {
	t.Parallel()

	// These two keys were documented and shipped for a long time but no Config
	// field reads them; without this check a config file keeps loading and the
	// option stays silently inert.
	data := []byte(`
responses_server:
  enabled: true
router:
  model_discovery:
    startup_policy: "best_effort"
`)

	got := unknownConfigKeys(data)
	if len(got) != 2 {
		t.Fatalf("unknownConfigKeys() = %v, want 2 messages", got)
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"enabled", "startup_policy"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("unknownConfigKeys() = %v, want a message mentioning %q", got, want)
		}
	}
}

func TestUnknownConfigKeysAllowsKnownOptions(t *testing.T) {
	t.Parallel()

	data := []byte(`
server:
  port: "8080"
responses_server:
  default_model: "gpt-5"
router:
  model_discovery:
    enabled: true
    refresh_interval: "5m"
  selection:
    policy: "weighted"
limits:
  enabled: true
  scope: "header"
  channel_key_header: "X-Channel-Key"
`)

	if got := unknownConfigKeys(data); len(got) != 0 {
		t.Fatalf("unknownConfigKeys() = %v, want none", got)
	}
}

// TestShippedConfigsHaveNoUnknownKeys guards the tracked config files against
// re-introducing keys that no longer exist in the Config struct.
func TestShippedConfigsHaveNoUnknownKeys(t *testing.T) {
	t.Parallel()

	examples, err := filepath.Glob(filepath.Join("..", "..", "config", "examples", "*.yaml"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	paths := append([]string{filepath.Join("..", "..", "config", "config.yaml")}, examples...)
	if len(paths) < 2 {
		t.Fatalf("expected the shipped config plus examples, got %v", paths)
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		if got := unknownConfigKeys(data); len(got) != 0 {
			t.Errorf("%s declares config keys this build ignores: %v", path, got)
		}
	}
}

func TestResolveDefaultSQLitePathPrefersLegacyFileInPlace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacySQLiteFileName)
	if err := os.WriteFile(legacy, []byte("legacy"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Only the pre-rename file exists: it must be reused instead of silently
	// starting a new empty database under the new default name.
	if got := ResolveDefaultSQLitePath(dir); got != legacy {
		t.Fatalf("ResolveDefaultSQLitePath() = %q, want legacy %q", got, legacy)
	}

	// Once the new default exists it wins.
	current := DefaultSQLitePath(dir)
	if err := os.WriteFile(current, []byte("current"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if got := ResolveDefaultSQLitePath(dir); got != current {
		t.Fatalf("ResolveDefaultSQLitePath() = %q, want current %q", got, current)
	}
}

func TestResolveDefaultSQLitePathWithoutLegacyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if got, want := ResolveDefaultSQLitePath(dir), DefaultSQLitePath(dir); got != want {
		t.Fatalf("ResolveDefaultSQLitePath() = %q, want %q", got, want)
	}
	if got, want := filepath.Base(DefaultSQLitePath(dir)), "trajecta.sqlite3"; got != want {
		t.Fatalf("default sqlite file name = %q, want %q", got, want)
	}
}

func TestDatabaseDriverDefaultsToPostgres(t *testing.T) {
	t.Parallel()

	// The application database is the shared source of truth: a configuration
	// that does not name a driver must not silently fall back to a local SQLite
	// file. SQLite has to be selected explicitly.
	cfg := Config{}
	if got := cfg.DatabaseDriver(); got != "postgres" {
		t.Fatalf("DatabaseDriver() = %q, want postgres", got)
	}
	cfg.Database.Driver = " SQLite "
	if got := cfg.DatabaseDriver(); got != "sqlite" {
		t.Fatalf("DatabaseDriver() = %q, want sqlite when it is named explicitly", got)
	}
}

func TestServerTimeoutsLoadFromConfig(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, `
server:
  port: "8080"
  read_timeout: 45s
  write_timeout: 90s
  upstream_response_header_timeout: 25s
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ServerReadTimeout(); got != 45*time.Second {
		t.Fatalf("ServerReadTimeout() = %v, want 45s", got)
	}
	if got := cfg.ServerWriteTimeout(); got != 90*time.Second {
		t.Fatalf("ServerWriteTimeout() = %v, want 90s", got)
	}
	if got := cfg.UpstreamResponseHeaderTimeout(); got != 25*time.Second {
		t.Fatalf("UpstreamResponseHeaderTimeout() = %v, want 25s", got)
	}
}

func TestServerTimeoutsDefaultToNoWriteDeadline(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, `
server:
  port: "8080"
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ServerReadTimeout(); got != 5*time.Minute {
		t.Fatalf("ServerReadTimeout() = %v, want 5m", got)
	}
	// No write deadline by default. `http.Server.WriteTimeout` covers the whole
	// response write, not the gap between writes, so any fixed default truncates
	// the responses this proxy exists to serve: a long completion, a streamed
	// response, or the Monitor's SSE event stream.
	if got := cfg.ServerWriteTimeout(); got != 0 {
		t.Fatalf("ServerWriteTimeout() = %v, want 0 (no deadline)", got)
	}
	// No bound on waiting for upstream response headers by default either. The
	// proxy cannot tell a stuck upstream from a slow one, so a default would
	// truncate a legitimate non-streaming reasoning call that spends minutes
	// before its first byte; an operator bounds it with
	// `server.upstream_response_header_timeout` instead.
	if got := cfg.UpstreamResponseHeaderTimeout(); got != 0 {
		t.Fatalf("UpstreamResponseHeaderTimeout() = %v, want 0 (no bound)", got)
	}
}

func TestServerTimeoutsHonourEnvOverrides(t *testing.T) {
	t.Setenv("TRAJECTA_SERVER_READ_TIMEOUT", "11s")
	t.Setenv("TRAJECTA_SERVER_WRITE_TIMEOUT", "12s")
	t.Setenv("TRAJECTA_SERVER_UPSTREAM_RESPONSE_HEADER_TIMEOUT", "13s")

	cfg, err := Load(writeTempConfig(t, `
server:
  port: "8080"
  read_timeout: 45s
  write_timeout: 90s
  upstream_response_header_timeout: 25s
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ServerReadTimeout(); got != 11*time.Second {
		t.Fatalf("ServerReadTimeout() = %v, want the env override 11s", got)
	}
	if got := cfg.ServerWriteTimeout(); got != 12*time.Second {
		t.Fatalf("ServerWriteTimeout() = %v, want the env override 12s", got)
	}
	if got := cfg.UpstreamResponseHeaderTimeout(); got != 13*time.Second {
		t.Fatalf("UpstreamResponseHeaderTimeout() = %v, want the env override 13s", got)
	}
}

func TestEnvOverridesIgnoreUnparsableValues(t *testing.T) {
	// These fields are anonymous structs, so they are set through the value rather
	// than a composite literal.
	var cfg Config
	cfg.MCP.Enabled = true
	cfg.Server.ReadTimeout = 7 * time.Second
	cfg.Database.MaxOpenConns = 5
	cfg.ProviderProbe.Timeout = 3 * time.Second
	t.Setenv("TRAJECTA_MCP_ENABLED", "not-a-bool")
	t.Setenv("TRAJECTA_SERVER_READ_TIMEOUT", "not-a-duration")
	t.Setenv("TRAJECTA_DATABASE_MAX_OPEN_CONNS", "not-an-int")
	t.Setenv("TRAJECTA_PROVIDER_PROBE_TIMEOUT", "12")

	applyEnvOverrides(&cfg)

	if !cfg.MCP.Enabled {
		t.Fatal("MCP.Enabled = false, want the loaded true kept when the value does not parse")
	}
	if cfg.Server.ReadTimeout != 7*time.Second {
		t.Fatalf("Server.ReadTimeout = %v, want the loaded value kept", cfg.Server.ReadTimeout)
	}
	if cfg.Database.MaxOpenConns != 5 {
		t.Fatalf("Database.MaxOpenConns = %d, want the loaded value kept", cfg.Database.MaxOpenConns)
	}
	if cfg.ProviderProbe.Timeout != 3*time.Second {
		t.Fatalf("ProviderProbe.Timeout = %v, want the loaded value kept", cfg.ProviderProbe.Timeout)
	}
}

// TestApplyEnvOverridesKeepsItsSurfaceInOnePlace is a structural gate over the
// override table.
//
// applyEnvOverrides is 58 variables. They used to be 58 near-identical blocks, and
// the parsing rule for each type lived inside its own block, so a new variable
// could invent a slightly different rule. Every read now goes through a typed
// helper, and this asserts it stays that way, that no variable is applied twice,
// and that the surface has not silently shrunk.
func TestApplyEnvOverridesKeepsItsSurfaceInOnePlace(t *testing.T) {
	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	body, ok := functionBodyForTest(string(source), "applyEnvOverrides")
	if !ok {
		t.Fatal("cannot locate applyEnvOverrides in config.go")
	}
	for _, direct := range []string{"os.Getenv(", "os.LookupEnv("} {
		if strings.Contains(body, direct) {
			t.Fatalf("applyEnvOverrides calls %s directly; every variable belongs in a typed helper so the parsing rule is shared", direct)
		}
	}
	keys := regexp.MustCompile(`"(TRAJECTA_[A-Z0-9_]+)"`).FindAllStringSubmatch(body, -1)
	seen := map[string]int{}
	for _, match := range keys {
		seen[match[1]]++
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("%s appears %d times, want once", key, count)
		}
	}
	if len(seen) < 25 {
		t.Fatalf("applyEnvOverrides handles %d variables, want at least 25; a removal this size should be deliberate", len(seen))
	}
}

// functionBodyForTest returns the source of a top-level function, up to the next
// top-level declaration.
func functionBodyForTest(source, name string) (string, bool) {
	marker := "func " + name + "("
	start := strings.Index(source, marker)
	if start < 0 {
		return "", false
	}
	rest := source[start+len(marker):]
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		return rest[:next], true
	}
	return rest, true
}

// TestLoadRejectsRoutingValuesNoConsumerRecognises pins the routing values that used to be
// accepted and then quietly replaced.
//
// The router reads these fields through normalizing helpers that answer an
// unrecognised value with a default, because a normalization running on the request path has
// no way to report a configuration mistake. Their defaults differ from what the operator
// wrote: `policy: first-availble` selects p2c and `model_discovery: static` turns upstream
// discovery back on. Loading used to accept both without a word, so the only way to
// notice was to read the effective routing state. They now fail at load with the accepted
// values in the message, the way an unknown limits.scope already did. `on_missing_model` is
// no longer a field at all: the unknown-model fallback was removed.
func TestLoadRejectsRoutingValuesNoConsumerRecognises(t *testing.T) {
	// Channel model_discovery is no longer a YAML key, so only the router
	// selection policy remains here. The accepted spellings still load.
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "selection policy",
			body: "router:\n  selection:\n    policy: first-availble\n",
			want: `router.selection.policy "first-availble" is not supported; use one of p2c, first_available`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, tc.body))
			if err == nil {
				t.Fatal("Load() accepted a value no consumer recognises")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load() error = %v, want it to contain %q", err, tc.want)
			}
		})
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "accepted values", body: "router:\n  selection:\n    policy: first_available\n"},
		{name: "values omitted", body: "router:\n  selection: {}\n"},
		{name: "mixed case accepted", body: "router:\n  selection:\n    policy: First_Available\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeTempConfig(t, tc.body)); err != nil {
				t.Fatalf("Load() error = %v, want the configuration to load", err)
			}
		})
	}
}

func TestShippedConfigsUseKnownRoutingValues(t *testing.T) {
	t.Parallel()

	examples, err := filepath.Glob(filepath.Join("..", "..", "config", "examples", "*.yaml"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	paths := append([]string{filepath.Join("..", "..", "config", "config.yaml")}, examples...)
	// The live harness configs are tracked and have to load too: a value the consumers cannot
	// honour is a broken deployment, and the live suite is where it would show up late.
	live, err := filepath.Glob(filepath.Join("..", "..", "tests", "live", "config", "*.yaml"))
	if err != nil {
		t.Fatalf("Glob(live) error = %v", err)
	}
	paths = append(paths, live...)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("Unmarshal(%s) error = %v", path, err)
		}
		if err := validateRouterEnums(&cfg); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		if err := validateLimits(&cfg); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		if err := validateChaosRules(&cfg); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

// TestLoadRejectsChaosRulesFaultInjectionCannotHonour pins the load-time validation of the
// fault-injection rules.
//
// The proxy matches a chaos action with an exact comparison after internal/chaos normalizes
// it, so a value outside {delay, error} marked the rule as hit and then did nothing: the
// request went to the upstream as if chaos were disabled, which the probe confirmed
// (action "erorr" -> 200, upstream received the request). A rate above the documented 1.0
// turned "inject on 2% of requests" into "inject on all of them", and a status_code outside
// the range net/http accepts panicked inside WriteHeader - the probe got
// `invalid WriteHeader code 1000` and the client saw EOF instead of an error response.
// Load now rejects all three, while the accepted spellings keep working.
func TestLoadRejectsChaosRulesFaultInjectionCannotHonour(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "unknown action",
			body:    "chaos:\n  enabled: true\n  rules:\n    - model: \"*\"\n      action: \"erorr\"\n",
			wantErr: `chaos.rules[0] action "erorr" is not supported; use one of delay, error`,
		},
		{
			name:    "missing action",
			body:    "chaos:\n  enabled: true\n  rules:\n    - model: gpt-5\n      rate: 1.0\n",
			wantErr: "chaos.rules[0] (gpt-5) has no action; use one of delay, error",
		},
		{
			name:    "rate above the documented range",
			body:    "chaos:\n  enabled: true\n  rules:\n    - action: error\n      rate: 2\n",
			wantErr: "chaos.rules[0] rate 2 is outside the documented range 0.0 to 1.0",
		},
		{
			name:    "negative rate",
			body:    "chaos:\n  enabled: true\n  rules:\n    - action: error\n      rate: -0.5\n",
			wantErr: "chaos.rules[0] rate -0.5 is outside the documented range 0.0 to 1.0",
		},
		{
			name:    "status code above what net/http writes",
			body:    "chaos:\n  enabled: true\n  rules:\n    - action: error\n      rate: 1.0\n      status_code: 1000\n",
			wantErr: "chaos.rules[0] status_code 1000 cannot be written by net/http; use 0 for the default (500) or a value between 100 and 999",
		},
		{
			name:    "status code below what net/http writes",
			body:    "chaos:\n  enabled: true\n  rules:\n    - action: error\n      rate: 1.0\n      status_code: 99\n",
			wantErr: "chaos.rules[0] status_code 99 cannot be written by net/http",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTempConfig(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "mixed-case action and boundary values",
			body: "chaos:\n  enabled: true\n  rules:\n    - model: \"*\"\n      rate: 1.0\n      action: \"ERROR\"\n      status_code: 503\n",
		},
		{
			name: "delay rule without a status code",
			body: "chaos:\n  enabled: true\n  rules:\n    - action: delay\n      rate: 0.5\n      delay: 250ms\n",
		},
		{
			name: "error rule that defaults its status and message",
			body: "chaos:\n  enabled: true\n  rules:\n    - action: error\n      rate: 0\n",
		},
		{
			name: "rules that are present but not enabled",
			body: "chaos:\n  enabled: false\n  rules:\n    - action: error\n      rate: 1.0\n",
		},
		{
			name: "no chaos section at all",
			body: "trace:\n  output_dir: /tmp/trajecta-chaos\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(writeTempConfig(t, tc.body)); err != nil {
				t.Fatalf("Load() error = %v, want the rule accepted", err)
			}
		})
	}
}

// TestMaskKeyDefaultsToTheDocumentedValue pins the documented default of debug.mask_key.
//
// SECURITY.md, README.md and README_EN.md all say the recorder masks the credential-carrying
// request headers "with the default debug.mask_key: true", but the field's zero value is
// false, so a configuration that omitted the key recorded the client's Authorization,
// api-key, x-api-key and x-goog-api-key headers verbatim into a cassette on disk. The default
// is now what the documents promise, an explicit value still wins, and the environment
// override still beats the file.
func TestMaskKeyDefaultsToTheDocumentedValue(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		env  string
		want bool
	}{
		{name: "key omitted", body: "trace:\n  output_dir: /tmp/trajecta-mask\n", want: true},
		{name: "empty debug section", body: "debug: {}\n", want: true},
		{name: "explicitly true", body: "debug:\n  mask_key: true\n", want: true},
		{name: "explicitly false", body: "debug:\n  mask_key: false\n", want: false},
		{name: "env false overrides the default", body: "debug: {}\n", env: "false", want: false},
		{name: "env false overrides the file", body: "debug:\n  mask_key: true\n", env: "false", want: false},
		{name: "env true overrides the file", body: "debug:\n  mask_key: false\n", env: "true", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				t.Setenv("TRAJECTA_MASK_KEY", "")
			} else {
				t.Setenv("TRAJECTA_MASK_KEY", tc.env)
			}
			cfg, err := Load(writeTempConfig(t, tc.body))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Debug.MaskKey != tc.want {
				t.Fatalf("Debug.MaskKey = %v, want %v", cfg.Debug.MaskKey, tc.want)
			}
		})
	}
}

// TestSecretMaskingDefaultsOn pins the contract an operator relies on: masking
// is on unless the configuration says otherwise.
//
// The field's zero value is false, so a configuration that simply omitted the
// key would have run with the protection off while the documents said it was
// on. Load seeds the field before decoding the YAML, which keeps an explicit
// `enabled: false` and the environment override authoritative.
func TestSecretMaskingDefaultsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		env  string
		want bool
	}{
		{name: "key omitted", body: "trace:\n  output_dir: /tmp/trajecta-secretmask\n", want: true},
		{name: "empty section", body: "secret_masking: {}\n", want: true},
		{name: "explicitly true", body: "secret_masking:\n  enabled: true\n", want: true},
		{name: "explicitly false", body: "secret_masking:\n  enabled: false\n", want: false},
		{name: "env false overrides the default", body: "secret_masking: {}\n", env: "false", want: false},
		{name: "env false overrides the file", body: "secret_masking:\n  enabled: true\n", env: "false", want: false},
		{name: "env true overrides the file", body: "secret_masking:\n  enabled: false\n", env: "true", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRAJECTA_SECRET_MASKING_ENABLED", tc.env)
			cfg, err := Load(writeTempConfig(t, tc.body))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.SecretMasking.Enabled != tc.want {
				t.Fatalf("SecretMasking.Enabled = %v, want %v", cfg.SecretMasking.Enabled, tc.want)
			}
		})
	}
}

// TestUpdateCheckDefaultsOn pins the About page contract: the "check for a newer
// release" button exists unless the configuration turns it away.
//
// The field's zero value is false, so a configuration that simply omitted the
// key would have removed the button while the page still drew it. Load seeds
// the field before decoding, which keeps an explicit `enabled: false` and the
// environment override authoritative.
func TestUpdateCheckDefaultsOn(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		env  string
		want bool
	}{
		{name: "key omitted", body: "trace:\n  output_dir: /tmp/trajecta-update-check\n", want: true},
		{name: "empty section", body: "update_check: {}\n", want: true},
		{name: "explicitly true", body: "update_check:\n  enabled: true\n", want: true},
		{name: "explicitly false", body: "update_check:\n  enabled: false\n", want: false},
		{name: "env false overrides the default", body: "update_check: {}\n", env: "false", want: false},
		{name: "env true overrides the file", body: "update_check:\n  enabled: false\n", env: "true", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRAJECTA_UPDATE_CHECK_ENABLED", tc.env)
			cfg, err := Load(writeTempConfig(t, tc.body))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.UpdateCheckEnabled() != tc.want {
				t.Fatalf("UpdateCheckEnabled() = %v, want %v", cfg.UpdateCheckEnabled(), tc.want)
			}
		})
	}
}

// TestTelemetryDefaultsOff is the load-bearing one: a local-first proxy records
// whatever its users point at it, so the anonymous install ping may not start
// reporting because a configuration file simply did not mention it.
func TestTelemetryDefaultsOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "key omitted", body: "trace:\n  output_dir: /tmp/trajecta-telemetry\n"},
		{name: "empty section", body: "telemetry: {}\n"},
		{name: "enabled with no endpoint", body: "telemetry:\n  enabled: true\n"},
		{name: "enabled with a blank endpoint", body: "telemetry:\n  enabled: true\n  endpoint: \"   \"\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRAJECTA_TELEMETRY_ENABLED", "")
			t.Setenv("TRAJECTA_TELEMETRY_ENDPOINT", "")
			cfg, err := Load(writeTempConfig(t, tc.body))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			settings := cfg.TelemetrySettings()
			if settings.Enabled {
				t.Fatalf("TelemetrySettings().Enabled = true for %q; want off", tc.name)
			}
			if settings.Provider != TelemetryProviderCustom {
				t.Fatalf("TelemetrySettings().Provider = %q, want %q", settings.Provider, TelemetryProviderCustom)
			}
			if settings.Interval != DefaultTelemetryInterval {
				t.Fatalf("TelemetrySettings().Interval = %v, want %v", settings.Interval, DefaultTelemetryInterval)
			}
			if settings.PathPrefix != DefaultTelemetryPathPrefix {
				t.Fatalf("TelemetrySettings().PathPrefix = %q, want %q", settings.PathPrefix, DefaultTelemetryPathPrefix)
			}
		})
	}
}

func TestTelemetryEnabledWithAnEndpoint(t *testing.T) {
	t.Setenv("TRAJECTA_TELEMETRY_ENABLED", "")
	t.Setenv("TRAJECTA_TELEMETRY_ENDPOINT", "")

	cfg, err := Load(writeTempConfig(t, `
telemetry:
  enabled: true
  provider: "PIXEL"
  endpoint: "https://counter.example/count"
  path_prefix: "/my/installs"
  interval: 6h
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	settings := cfg.TelemetrySettings()
	if !settings.Enabled {
		t.Fatal("TelemetrySettings().Enabled = false, want true")
	}
	if settings.Provider != TelemetryProviderPixel {
		t.Fatalf("TelemetrySettings().Provider = %q, want %q", settings.Provider, TelemetryProviderPixel)
	}
	if settings.Interval != 6*time.Hour {
		t.Fatalf("TelemetrySettings().Interval = %v, want 6h", settings.Interval)
	}
	if settings.PathPrefix != "/my/installs" {
		t.Fatalf("TelemetrySettings().PathPrefix = %q, want /my/installs", settings.PathPrefix)
	}
}

func TestLoadRejectsAnUnknownTelemetryProvider(t *testing.T) {
	t.Setenv("TRAJECTA_TELEMETRY_ENABLED", "")
	t.Setenv("TRAJECTA_TELEMETRY_PROVIDER", "")

	_, err := Load(writeTempConfig(t, `
telemetry:
  enabled: true
  provider: "plausible"
  endpoint: "https://counter.example/count"
`))
	if err == nil {
		t.Fatal("Load() error = nil, want a rejection")
	}
	if !strings.Contains(err.Error(), "telemetry.provider") {
		t.Fatalf("Load() error = %v, want it to name telemetry.provider", err)
	}
}

func TestLoadRejectsYAMLChannelConfiguration(t *testing.T) {
	for _, body := range []string{
		"upstream:\n  base_url: https://api.openai.com/v1\n",
		"upstreams:\n  - id: primary\n    upstream:\n      base_url: https://api.openai.com/v1\n",
	} {
		_, err := Load(writeTempConfig(t, body))
		if err == nil {
			t.Fatalf("Load(%q) error = nil, want a YAML channel rejection", body)
		}
		if !strings.Contains(err.Error(), "no longer supported") || !strings.Contains(err.Error(), "application database") {
			t.Fatalf("Load() error = %v, want an actionable database-only message", err)
		}
	}
}
