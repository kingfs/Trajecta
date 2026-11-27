package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/auth"
	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/cli"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

func TestLogResolvedTargetsIncludesRoutingDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	rtr, err := router.New(&config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "google-genai",
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gemini-2.5-pro"},
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://generativelanguage.googleapis.com",
				ProviderPreset: "google_genai",
			},
		}},
	})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	logResolvedTargets(rtr)

	output := buf.String()
	for _, want := range []string{
		"Resolved upstream target",
		"provider_preset=google_genai",
		"protocol_family=google_genai",
		"routing_profile=google_ai_studio",
		"connectivity_endpoint=/v1beta/models",
		"connectivity_url=https://generativelanguage.googleapis.com/v1beta/models",
		"model_routing_hint=\"model is selected in the request path",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("log output = %q, want contain %q", output, want)
		}
	}
}

func TestRouterConfigFromChannelsUsesDatabaseTargets(t *testing.T) {
	st, err := newTestStore(t.TempDir())
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "db-channel",
		Name:           "DB Channel",
		BaseURL:        "https://db.example.com/v1",
		ProviderPreset: "openai",
		HeadersJSON:    "{}",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("db-channel", []store.ChannelModelRecord{
		{Model: "gpt-5", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}

	cfg := &config.Config{}
	routerCfg, err := routerConfigFromChannels(cfg, channel.NewService(st))
	if err != nil {
		t.Fatalf("routerConfigFromChannels() error = %v", err)
	}
	if len(routerCfg.Upstreams) != 1 || routerCfg.Upstreams[0].ID != "db-channel" {
		t.Fatalf("routerCfg.Upstreams = %#v", routerCfg.Upstreams)
	}
	if routerCfg.Upstream.BaseURL != "" {
		t.Fatalf("routerCfg.Upstream.BaseURL = %q, want empty", routerCfg.Upstream.BaseURL)
	}
}

func TestRunServeRejectsYAMLChannelConfig(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configBody := []byte(strings.TrimSpace(`
database:
  driver: "sqlite"
server:
  port: "8080"
monitor:
  port: "8081"
upstreams:
  - id: primary
    upstream:
      base_url: "https://api.openai.com/v1"
debug:
  output_dir: "` + dir + `"
  mask_key: false
`))
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code := runServeWithConfig(configPath)
	if code != 1 {
		t.Fatalf("runServeWithConfig() = %d, want 1", code)
	}
	output := buf.String()
	for _, want := range []string{"YAML channel configuration is no longer supported", "application database"} {
		if !strings.Contains(output, want) {
			t.Fatalf("log output = %q, want contain %q", output, want)
		}
	}
}

func TestValidateServeRouterConfigRequiresLocalResponsesServerBackend(t *testing.T) {
	cfg := &config.Config{
		ResponsesServer: config.ResponsesServerConfig{},
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "anthropic-messages",
				ModelDiscovery: router.ModelDiscoveryStaticOnly,
				StaticModels:   []string{"claude-sonnet-4-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.anthropic.com/v1",
					ProviderPreset: "anthropic",
				},
			},
		},
	}

	err := validateServeRouterConfig(cfg, cfg)
	if err == nil {
		t.Fatal("validateServeRouterConfig() error = nil, want local Responses server backend validation error")
	}
	if !strings.Contains(err.Error(), router.LocalResponsesServerBackendRequiredError) {
		t.Fatalf("validateServeRouterConfig() error = %q, want contain %q", err.Error(), router.LocalResponsesServerBackendRequiredError)
	}
}

func TestRunServeRejectsMCPWithoutMonitorPort(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configBody := []byte(strings.TrimSpace(`
server:
  port: "8080"
monitor:
  port: ""
mcp:
  enabled: true
debug:
  output_dir: "` + dir + `"
  mask_key: false
`))
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code := runServeWithConfig(configPath)
	if code != 1 {
		t.Fatalf("runServeWithConfig() = %d, want 1", code)
	}
	if output := buf.String(); !strings.Contains(output, "monitor.port is required when mcp.enabled=true") {
		t.Fatalf("log output = %q, want monitor.port requirement", output)
	}
}

func TestRunServeRejectsUnknownDatabaseDriver(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configBody := []byte(strings.TrimSpace(`
server:
  port: "8080"
monitor:
  port: "8081"
mcp:
  enabled: true
database:
  driver: "mysql"
  dsn: "mysql://trajecta:secret@mysql:3306/trajecta"
debug:
  output_dir: "` + dir + `"
  mask_key: false
`))
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code := runServeWithConfig(configPath)
	if code != 1 {
		t.Fatalf("runServeWithConfig() = %d, want 1", code)
	}
	if output := buf.String(); !strings.Contains(output, "database.driver") ||
		!strings.Contains(output, "mysql") ||
		!strings.Contains(output, "is not supported yet; use sqlite or postgres") {
		t.Fatalf("log output = %q, want unsupported database driver", output)
	}
}

func TestManagementMuxServesStreamableMCP(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	cfg.MCP.Enabled = true
	cfg.MCP.Path = "/mcp"
	authStore := newTestAuthStore(t)
	defer authStore.Close()
	token, err := authStore.CreateToken(context.Background(), "admin", "mcp-test", auth.ScopeMCP, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}

	httpServer := httptest.NewServer(newManagementMuxWithSecretMappings(st, nil, cfg, nil, nil, nil, nil, nil, authStore))
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   httpServer.URL + "/mcp",
		HTTPClient: &http.Client{Transport: authTransport{Token: token.Token}},
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("negotiated MCP protocol version = %q, want 2026-07-28", got)
	}

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("session.ListTools() error = %v", err)
	}
	if len(tools.Tools) != 22 {
		t.Fatalf("len(tools.Tools) = %d, want 22", len(tools.Tools))
	}
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_traces", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("session.CallTool() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("list_traces failed: %+v", result.Content)
	}
}

func TestManagementMuxRejectsUnauthorizedMCP(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	cfg.MCP.Enabled = true
	cfg.MCP.Path = "/mcp"
	authStore := newTestAuthStore(t)
	defer authStore.Close()

	httpServer := httptest.NewServer(newManagementMuxWithSecretMappings(st, nil, cfg, nil, nil, nil, nil, nil, authStore))
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	_, err = client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
	}, nil)
	if err == nil {
		t.Fatalf("client.Connect() error = nil, want unauthorized error")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		t.Fatalf("client.Connect() error = %v, want unauthorized", err)
	}
}

func TestManagementMuxServesAuthorizedStreamableMCP(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	cfg.MCP.Enabled = true
	cfg.MCP.Path = "/mcp"
	authStore := newTestAuthStore(t)
	defer authStore.Close()
	token, err := authStore.CreateToken(context.Background(), "admin", "mcp", auth.DefaultTokenScope, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken() error = %v", err)
	}

	httpServer := httptest.NewServer(newManagementMuxWithSecretMappings(st, nil, cfg, nil, nil, nil, nil, nil, authStore))
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   httpServer.URL + "/mcp",
		HTTPClient: &http.Client{Transport: authTransport{Token: token.Token}},
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("session.ListTools() error = %v", err)
	}
	if len(tools.Tools) != 22 {
		t.Fatalf("len(tools.Tools) = %d, want 22", len(tools.Tools))
	}
}

func newTestAuthStore(t *testing.T) *auth.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "auth.sqlite3")
	if err := auth.MigrateDatabaseUp("sqlite", dbPath, 0); err != nil {
		t.Fatalf("auth.MigrateDatabaseUp() error = %v", err)
	}
	authStore, err := auth.OpenDatabase("sqlite", dbPath, 4, 4)
	if err != nil {
		t.Fatalf("auth.OpenDatabase() error = %v", err)
	}
	if _, err := authStore.CreateUserWithRole(context.Background(), "admin", "change-me-123", auth.RoleAdmin); err != nil {
		_ = authStore.Close()
		t.Fatalf("CreateUserWithRole() error = %v", err)
	}
	return authStore
}

type authTransport struct {
	Token string
}

func TestRouterConfigurationReadsOnlyEnabledDatabaseChannels(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "deleted"}[deleted], func(t *testing.T) {
			st, err := newTestStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
				ID:             "db-channel",
				Name:           "DB Channel",
				BaseURL:        "https://db.example.com/v1",
				ProviderPreset: "openai",
				Enabled:        true,
			}); err != nil {
				t.Fatal(err)
			}
			if deleted {
				if err := st.DeleteChannelConfig("db-channel"); err != nil {
					t.Fatal(err)
				}
			} else {
				record, err := st.GetChannelConfig("db-channel")
				if err != nil {
					t.Fatal(err)
				}
				record.Enabled = false
				if _, err := st.UpsertChannelConfig(record); err != nil {
					t.Fatal(err)
				}
			}
			runtimeCfg, err := routerConfigFromChannels(&config.Config{}, channel.NewService(st))
			if err != nil {
				t.Fatal(err)
			}
			if len(runtimeCfg.EffectiveUpstreams()) != 0 {
				t.Fatalf("targets = %v, want no enabled channel", runtimeCfg.EffectiveUpstreams())
			}
		})
	}
}

func (t authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+t.Token)
	return http.DefaultTransport.RoundTrip(clone)
}

func TestRootCommandHelpWorksWithConfigShortcut(t *testing.T) {
	t.Parallel()

	cmd := newRootCommandWithRuntime(cli.NewRuntime())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"-c", "config.yaml", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"Postgres-first LLM gateway with local Responses runtime and record/replay",
		"Available Commands:",
		"-c, --config string",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("help output = %q, want contain %q", output, want)
		}
	}
}

func TestVersionCommandSupportsJSONEnvelope(t *testing.T) {
	t.Parallel()

	cmd := newRootCommandWithRuntime(cli.NewRuntime())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"version", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	var envelope struct {
		OK      bool   `json:"ok"`
		Command string `json:"command"`
		Result  struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("json.Unmarshal() error = %v, output=%q", err, out.String())
	}
	if !envelope.OK || envelope.Command != "version" || envelope.Result.Name != cliName || envelope.Result.Version == "" {
		t.Fatalf("version envelope = %+v", envelope)
	}
}
