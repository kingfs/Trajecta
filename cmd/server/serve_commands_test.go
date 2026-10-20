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
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"
)

func TestLogResolvedUpstreamConfigIncludesRoutingDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	prev := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() {
		slog.SetDefault(prev)
	})

	logResolvedUpstreamConfig(
		upstream.ResolvedUpstream{
			BaseURL:        "https://generativelanguage.googleapis.com",
			ProviderPreset: "google_genai",
			ProtocolFamily: upstream.ProtocolFamilyGoogleGenAI,
			RoutingProfile: upstream.RoutingProfileGoogleAIStudio,
		},
		upstream.StartupDiagnostics{
			ConnectivityEndpoint: "/v1beta/models",
			ConnectivityURL:      "https://generativelanguage.googleapis.com/v1beta/models",
			ModelRoutingHint:     "model is selected in the request path",
		},
	)

	output := buf.String()
	for _, want := range []string{
		"Resolved upstream config",
		"provider_preset=google_genai",
		"protocol_family=google_genai",
		"routing_profile=google_ai_studio",
		"connectivity_endpoint=/v1beta/models",
		"connectivity_url=https://generativelanguage.googleapis.com/v1beta/models",
		"model_routing_hint=\"model is selected in the request path\"",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("log output = %q, want contain %q", output, want)
		}
	}
}

func TestRouterConfigFromChannelsUsesDatabaseTargets(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
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
	cfg.Upstream.BaseURL = "https://yaml.example.com/v1"
	cfg.Upstream.ProviderPreset = "openai"

	routerCfg, source, err := routerConfigFromChannels(cfg, channel.NewService(st))
	if err != nil {
		t.Fatalf("routerConfigFromChannels() error = %v", err)
	}
	if source != "database" {
		t.Fatalf("source = %q, want database", source)
	}
	if len(routerCfg.Upstreams) != 1 || routerCfg.Upstreams[0].ID != "db-channel" {
		t.Fatalf("routerCfg.Upstreams = %#v", routerCfg.Upstreams)
	}
	if routerCfg.Upstream.BaseURL != "" {
		t.Fatalf("routerCfg.Upstream.BaseURL = %q, want empty", routerCfg.Upstream.BaseURL)
	}
}

func TestRouterConfigFromChannelsFallsBackToYAML(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = "https://yaml.example.com/v1"
	cfg.Upstream.ProviderPreset = "openai"

	routerCfg, source, err := routerConfigFromChannels(cfg, channel.NewService(st))
	if err != nil {
		t.Fatalf("routerConfigFromChannels() error = %v", err)
	}
	if source != "yaml" {
		t.Fatalf("source = %q, want yaml", source)
	}
	if routerCfg != cfg {
		t.Fatalf("routerConfigFromChannels should return original cfg on YAML fallback")
	}
}

func TestRouterConfigFromChannelsKeepsYAMLWhenExplicitCredentialsExist(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
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

	cfg := &config.Config{}
	cfg.Upstreams = []config.UpstreamTargetConfig{
		{
			ID: "yaml-channel",
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://yaml.example.com/v1",
				ApiKey:         "$env:OPENAI_TEST_KEY",
				ProviderPreset: "openai",
			},
			Credentials: []config.CredentialConfig{
				{
					ID:     "primary",
					ApiKey: "$env:OPENAI_TEST_KEY",
				},
			},
		},
	}

	routerCfg, source, err := routerConfigFromChannels(cfg, channel.NewService(st))
	if err != nil {
		t.Fatalf("routerConfigFromChannels() error = %v", err)
	}
	if source != "yaml" {
		t.Fatalf("source = %q, want yaml", source)
	}
	if routerCfg != cfg {
		t.Fatalf("routerConfigFromChannels should return original cfg when YAML credentials are explicit")
	}
	if got := routerCfg.Upstreams[0].EffectiveCredentials()[0].ID; got != "primary" {
		t.Fatalf("credential id = %q, want primary", got)
	}
}

func TestRunServeLogsActionableInvalidUpstreamConfig(t *testing.T) {
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
  port: ""
upstream:
  base_url: "https://api.anthropic.com"
  provider_preset: "anthropic"
  protocol_family: "google_genai"
debug:
  output_dir: "` + dir + `"
  mask_key: false
`))
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code := runServe([]string{"-c", configPath})
	if code != 1 {
		t.Fatalf("runServe() = %d, want 1", code)
	}

	output := buf.String()
	for _, want := range []string{
		"Invalid upstream config",
		`upstream.provider_preset=`,
		`anthropic_messages`,
		`google_genai`,
	} {
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

func TestStartupProviderProbeDefaultDisabledDoesNotProbe(t *testing.T) {
	var calls int
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.NotFound(w, r)
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstreams = []config.UpstreamTargetConfig{
		{
			ID: "local",
			Upstream: config.UpstreamConfig{
				BaseURL: upstreamServer.URL + "/api/v1",
			},
		},
	}

	if err := applyStartupProviderProbeSuggestions(context.Background(), cfg, upstreamServer.Client()); err != nil {
		t.Fatalf("applyStartupProviderProbeSuggestions() error = %v", err)
	}
	if calls != 0 {
		t.Fatalf("startup probe made %d HTTP calls with default config, want 0", calls)
	}
	if cfg.Upstreams[0].Upstream.APIType != "" || cfg.Upstreams[0].Upstream.ProtocolFamily != "" {
		t.Fatalf("upstream config was modified while disabled: %+v", cfg.Upstreams[0].Upstream)
	}
}

func TestStartupProviderProbeFillsMissingAPISurface(t *testing.T) {
	upstreamServer := newOpenAIProbeTestServer(t)
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.ProviderProbe.StartupFill = true
	cfg.Upstreams = []config.UpstreamTargetConfig{
		{
			ID: "local",
			Upstream: config.UpstreamConfig{
				BaseURL: upstreamServer.URL + "/api/v1",
			},
		},
	}

	if err := applyStartupProviderProbeSuggestions(context.Background(), cfg, upstreamServer.Client()); err != nil {
		t.Fatalf("applyStartupProviderProbeSuggestions() error = %v", err)
	}
	got := cfg.Upstreams[0].Upstream
	if got.ProtocolFamily != upstream.ProtocolFamilyOpenAICompatible {
		t.Fatalf("ProtocolFamily = %q, want %q", got.ProtocolFamily, upstream.ProtocolFamilyOpenAICompatible)
	}
	if got.APIType != upstream.APITypeResponses {
		t.Fatalf("APIType = %q, want %q", got.APIType, upstream.APITypeResponses)
	}
	if got.Capabilities.Responses == nil || !*got.Capabilities.Responses {
		t.Fatalf("capabilities.responses = %v, want true", got.Capabilities.Responses)
	}
	if got.Capabilities.ChatCompletions == nil || !*got.Capabilities.ChatCompletions {
		t.Fatalf("capabilities.chat_completions = %v, want true", got.Capabilities.ChatCompletions)
	}
	if got.Capabilities.Models == nil || !*got.Capabilities.Models {
		t.Fatalf("capabilities.models = %v, want true", got.Capabilities.Models)
	}
}

func TestStartupProviderProbeFailureDoesNotBlockStartup(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.ProviderProbe.StartupFill = true
	cfg.Upstreams = []config.UpstreamTargetConfig{
		{
			ID: "local",
			Upstream: config.UpstreamConfig{
				BaseURL: upstreamServer.URL + "/api/v1",
			},
		},
	}

	if err := applyStartupProviderProbeSuggestions(context.Background(), cfg, upstreamServer.Client()); err != nil {
		t.Fatalf("applyStartupProviderProbeSuggestions() error = %v, want nil best-effort startup", err)
	}
	got := cfg.Upstreams[0].Upstream
	if got.APIType != "" || got.ProtocolFamily != "" || got.Capabilities.Responses != nil || got.Capabilities.ChatCompletions != nil {
		t.Fatalf("failed startup probe modified config: %+v", got)
	}
}

func TestStartupProviderProbeDoesNotOverrideExplicitAPISurface(t *testing.T) {
	upstreamServer := newOpenAIProbeTestServer(t)
	defer upstreamServer.Close()

	responsesDisabled := false
	cfg := &config.Config{}
	cfg.ProviderProbe.StartupFill = true
	cfg.Upstreams = []config.UpstreamTargetConfig{
		{
			ID: "local",
			Upstream: config.UpstreamConfig{
				BaseURL:        upstreamServer.URL + "/api/v1",
				APIType:        upstream.APITypeChatCompletions,
				ProtocolFamily: upstream.ProtocolFamilyAnthropicMessages,
				Capabilities: config.UpstreamCapabilitiesConfig{
					Responses: &responsesDisabled,
				},
			},
		},
	}

	if err := applyStartupProviderProbeSuggestions(context.Background(), cfg, upstreamServer.Client()); err != nil {
		t.Fatalf("applyStartupProviderProbeSuggestions() error = %v", err)
	}
	got := cfg.Upstreams[0].Upstream
	if got.ProtocolFamily != upstream.ProtocolFamilyAnthropicMessages {
		t.Fatalf("ProtocolFamily = %q, want explicit %q", got.ProtocolFamily, upstream.ProtocolFamilyAnthropicMessages)
	}
	if got.APIType != upstream.APITypeChatCompletions {
		t.Fatalf("APIType = %q, want explicit %q", got.APIType, upstream.APITypeChatCompletions)
	}
	if got.Capabilities.Responses == nil || *got.Capabilities.Responses {
		t.Fatalf("capabilities.responses = %v, want explicit false", got.Capabilities.Responses)
	}
	if got.Capabilities.Models == nil || !*got.Capabilities.Models {
		t.Fatalf("capabilities.models = %v, want missing field filled true", got.Capabilities.Models)
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
upstream:
  base_url: "https://api.openai.com/v1"
debug:
  output_dir: "` + dir + `"
  mask_key: false
`))
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code := runServe([]string{"-c", configPath})
	if code != 1 {
		t.Fatalf("runServe() = %d, want 1", code)
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
upstream:
  base_url: "https://api.openai.com/v1"
debug:
  output_dir: "` + dir + `"
  mask_key: false
`))
	if err := os.WriteFile(configPath, configBody, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code := runServe([]string{"-c", configPath})
	if code != 1 {
		t.Fatalf("runServe() = %d, want 1", code)
	}
	if output := buf.String(); !strings.Contains(output, "database.driver") ||
		!strings.Contains(output, "mysql") ||
		!strings.Contains(output, "is not supported yet; use sqlite or postgres") {
		t.Fatalf("log output = %q, want unsupported database driver", output)
	}
}

func TestNewManagementMuxServesStreamableMCP(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	cfg.MCP.Enabled = true
	cfg.MCP.Path = "/mcp"

	httpServer := httptest.NewServer(newManagementMux(st, nil, cfg))
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
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
}

func TestNewManagementMuxRejectsUnauthorizedMCP(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Monitor.Port = "8081"
	cfg.MCP.Enabled = true
	cfg.MCP.Path = "/mcp"
	authStore := newTestAuthStore(t)
	defer authStore.Close()

	httpServer := httptest.NewServer(newManagementMux(st, nil, cfg, authStore))
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

func TestNewManagementMuxServesAuthorizedStreamableMCP(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
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

	httpServer := httptest.NewServer(newManagementMux(st, nil, cfg, authStore))
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
	if err := auth.MigrateUp(dbPath, 0); err != nil {
		t.Fatalf("auth.MigrateUp() error = %v", err)
	}
	authStore, err := auth.Open(dbPath)
	if err != nil {
		t.Fatalf("auth.Open() error = %v", err)
	}
	if _, err := authStore.CreateUser(context.Background(), "admin", "change-me-123"); err != nil {
		_ = authStore.Close()
		t.Fatalf("CreateUser() error = %v", err)
	}
	return authStore
}

type authTransport struct {
	Token string
}

func TestRouterConfigurationKeepsDisabledAndDeletedChannelsAcrossBootstrap(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "deleted"}[deleted], func(t *testing.T) {
			st, err := store.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			cfg := &config.Config{Upstream: config.UpstreamConfig{BaseURL: "https://yaml.example.invalid/v1", ProviderPreset: "openai"}}
			svc := channel.NewService(st)
			if _, err := svc.BootstrapFromConfig(cfg); err != nil {
				t.Fatal(err)
			}
			channels, err := st.ListChannelConfigs()
			if err != nil {
				t.Fatal(err)
			}
			if len(channels) != 1 {
				t.Fatalf("channels=%v", channels)
			}
			if deleted {
				err = st.DeleteChannelConfig(channels[0].ID)
			} else {
				record := channels[0]
				record.Enabled = false
				_, err = st.UpsertChannelConfig(record)
			}
			if err != nil {
				t.Fatal(err)
			}
			if imported, err := svc.BootstrapFromConfig(cfg); err != nil || imported != 0 {
				t.Fatalf("bootstrap=%d %v", imported, err)
			}
			runtimeCfg, source, err := routerConfigFromChannels(cfg, svc)
			if err != nil {
				t.Fatal(err)
			}
			if source != "database" || len(runtimeCfg.EffectiveUpstreams()) != 0 {
				t.Fatalf("source=%s targets=%v", source, runtimeCfg.EffectiveUpstreams())
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

	cmd := newRootCommand()
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

	cmd := newRootCommand()
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

func newOpenAIProbeTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/models":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-test"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/chat/completions":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"model is required"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/responses":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"input is required"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}
