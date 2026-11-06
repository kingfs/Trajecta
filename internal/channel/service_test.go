package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/modelprobe"
	"github.com/kingfs/Trajecta/internal/providerprobe"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
)

func TestBootstrapFromConfigImportsYAMLUpstreamsOnce(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	enabled := true
	responsesEnabled := false
	chatCompletionsEnabled := true
	toolCallingEnabled := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "openai-primary",
				Enabled:        &enabled,
				Priority:       100,
				Weight:         1,
				CapacityHint:   2,
				ModelDiscovery: "static_only",
				StaticModels:   []string{"gpt-5", "GPT-5", "gpt-4.1"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ApiKey:         "test-inline-key",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
					Mode:           "responses_server",
					Capabilities: config.UpstreamCapabilitiesConfig{
						Responses:       &responsesEnabled,
						ChatCompletions: &chatCompletionsEnabled,
						ToolCalling:     &toolCallingEnabled,
					},
					Headers: map[string]string{
						"X-Test": "true",
					},
				},
			},
		},
	}

	svc := NewService(st)
	imported, err := svc.BootstrapFromConfig(cfg)
	if err != nil {
		t.Fatalf("BootstrapFromConfig() error = %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}
	imported, err = svc.BootstrapFromConfig(cfg)
	if err != nil {
		t.Fatalf("second BootstrapFromConfig() error = %v", err)
	}
	if imported != 0 {
		t.Fatalf("second imported = %d, want 0", imported)
	}
	record, err := st.GetChannelConfig("openai-primary")
	if err != nil {
		t.Fatalf("GetChannelConfig() error = %v", err)
	}
	if record.Source != "bootstrap" {
		t.Fatalf("record.Source = %q, want bootstrap", record.Source)
	}
	if record.APIType != "chat_completions" || record.Mode != "responses_server" {
		t.Fatalf("record api surface = %q/%q", record.APIType, record.Mode)
	}
	if !strings.Contains(record.CapabilitiesJSON, `"chat_completions":true`) || !strings.Contains(record.CapabilitiesJSON, `"responses":false`) {
		t.Fatalf("record.CapabilitiesJSON = %s", record.CapabilitiesJSON)
	}

	targets, err := svc.RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	target := targets[0]
	if target.ID != "openai-primary" {
		t.Fatalf("target.ID = %q", target.ID)
	}
	if target.Upstream.ApiKey != "test-inline-key" {
		t.Fatalf("target.Upstream.ApiKey = %q", target.Upstream.ApiKey)
	}
	if got := target.Upstream.Headers["X-Test"]; got != "true" {
		t.Fatalf("target header X-Test = %q", got)
	}
	if target.Upstream.APIType != "chat_completions" || target.Upstream.Mode != "responses_server" {
		t.Fatalf("target api surface = %q/%q", target.Upstream.APIType, target.Upstream.Mode)
	}
	if target.Upstream.Capabilities.ChatCompletions == nil || !*target.Upstream.Capabilities.ChatCompletions {
		t.Fatalf("target.Upstream.Capabilities.ChatCompletions = %#v", target.Upstream.Capabilities.ChatCompletions)
	}
	if target.Upstream.Capabilities.Responses == nil || *target.Upstream.Capabilities.Responses {
		t.Fatalf("target.Upstream.Capabilities.Responses = %#v", target.Upstream.Capabilities.Responses)
	}
	if len(target.StaticModels) != 2 || target.StaticModels[0] != "gpt-4.1" || target.StaticModels[1] != "gpt-5" {
		t.Fatalf("target.StaticModels = %#v", target.StaticModels)
	}
}

func TestBootstrapFromConfigSkipsExplicitCredentialsWithoutStorageProjection(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID: "openai-primary",
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ApiKey:         "test-inline-key",
					ProviderPreset: "openai",
				},
				Credentials: []config.CredentialConfig{
					{
						ID:     "primary",
						ApiKey: "test-explicit-key",
					},
				},
			},
		},
	}

	imported, err := NewService(st).BootstrapFromConfig(cfg)
	if err != nil {
		t.Fatalf("BootstrapFromConfig() error = %v", err)
	}
	if imported != 0 {
		t.Fatalf("imported = %d, want 0", imported)
	}
	channels, err := st.ListChannelConfigs()
	if err != nil {
		t.Fatalf("ListChannelConfigs() error = %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("len(channels) = %d, want 0", len(channels))
	}
}

func TestProbeDiscoversModelsAndUpdatesCatalogs(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-probe" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"},{"id":"gpt-4.1"}]}`))
	}))
	defer upstreamServer.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "probe-channel",
		Name:             "Probe Channel",
		BaseURL:          upstreamServer.URL + "/v1",
		ProviderPreset:   "openai",
		APIKeyCiphertext: []byte("sk-probe"),
		HeadersJSON:      "{}",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}

	enable := true
	result, err := NewService(st).ProbeWithOptions("probe-channel", ProbeOptions{EnableDiscovered: &enable})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if result.Status != "success" {
		t.Fatalf("Status = %q", result.Status)
	}
	if len(result.Models) != 2 || result.Models[0] != "gpt-4.1" || result.Models[1] != "gpt-5" {
		t.Fatalf("Models = %#v", result.Models)
	}

	models, err := st.ListChannelModels("probe-channel", true)
	if err != nil {
		t.Fatalf("ListChannelModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(channel models) = %d, want 2", len(models))
	}
	upstreamModels, err := st.ListUpstreamModels()
	if err != nil {
		t.Fatalf("ListUpstreamModels() error = %v", err)
	}
	if len(upstreamModels) != 2 {
		t.Fatalf("len(upstream models) = %d, want 2", len(upstreamModels))
	}
	runs, err := st.ListChannelProbeRuns("probe-channel", 10)
	if err != nil {
		t.Fatalf("ListChannelProbeRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "success" || runs[0].DiscoveredCount != 2 {
		t.Fatalf("probe runs = %#v", runs)
	}
}

func TestProbeWithOptionsDetectsProviderSurface(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
		case "/v1/chat/completions":
			http.Error(w, "missing model", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "probe-channel",
		Name:             "Probe Channel",
		BaseURL:          upstreamServer.URL + "/v1",
		ProviderPreset:   "openai",
		APIKeyCiphertext: []byte("sk-probe"),
		HeadersJSON:      "{}",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}

	result, err := NewService(st).ProbeWithOptions("probe-channel", ProbeOptions{DetectProvider: true})
	if err != nil {
		t.Fatalf("ProbeWithOptions() error = %v", err)
	}
	if result.ProviderReport.Status != "detected" {
		t.Fatalf("ProviderReport.Status = %q, want detected", result.ProviderReport.Status)
	}
	if result.ProviderReport.SuggestedAPIType != "chat_completions" || result.ProviderReport.SuggestedProtocolFamily != "openai_compatible" {
		t.Fatalf("provider suggestion = %q/%q", result.ProviderReport.SuggestedAPIType, result.ProviderReport.SuggestedProtocolFamily)
	}
	if !slices.Contains(result.ProviderReport.Capabilities, "chat_completions") || !slices.Contains(result.ProviderReport.Capabilities, "models") {
		t.Fatalf("ProviderReport.Capabilities = %#v", result.ProviderReport.Capabilities)
	}
	runs, err := st.ListChannelProbeRuns("probe-channel", 10)
	if err != nil {
		t.Fatalf("ListChannelProbeRuns() error = %v", err)
	}
	if len(runs) != 1 || !strings.Contains(runs[0].RequestMetaJSON, `"provider_probe"`) {
		t.Fatalf("probe run meta = %#v", runs)
	}
}

func TestProviderProbeReportDetectsChannelsReadOnly(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
		case "/v1/chat/completions":
			http.Error(w, "missing model", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "probe-channel",
		Name:             "Probe Channel",
		BaseURL:          upstreamServer.URL + "/v1",
		APIType:          "responses",
		ProtocolFamily:   "openai_compatible",
		APIKeyCiphertext: []byte("sk-probe"),
		HeadersJSON:      "{}",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}

	report, err := NewService(st).ProviderProbeReport(context.Background(), ProviderProbeReportOptions{})
	if err != nil {
		t.Fatalf("ProviderProbeReport() error = %v", err)
	}
	if len(report.Reports) != 1 {
		t.Fatalf("len(report.Reports) = %d, want 1", len(report.Reports))
	}
	got := report.Reports[0]
	if got.TargetSource != "channel" || got.ProviderID != "probe-channel" || got.Status != "detected" {
		t.Fatalf("report identity/status = %+v", got)
	}
	if got.SuggestedAPIType != "chat_completions" || got.SuggestedProtocolFamily != "openai_compatible" {
		t.Fatalf("suggestion = %q/%q", got.SuggestedAPIType, got.SuggestedProtocolFamily)
	}
	if !slices.Contains(got.Warnings, `specified api_type "responses" differs from probed suggestion "chat_completions"`) {
		t.Fatalf("Warnings = %#v, want api_type mismatch", got.Warnings)
	}
	runs, err := st.ListChannelProbeRuns("probe-channel", 10)
	if err != nil {
		t.Fatalf("ListChannelProbeRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("ProviderProbeReport wrote probe runs: %#v", runs)
	}
	models, err := st.ListChannelModels("probe-channel", false)
	if err != nil {
		t.Fatalf("ListChannelModels() error = %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("ProviderProbeReport wrote channel models: %#v", models)
	}
}

func TestApplyProviderProbeReportFillsOnlyMissingNonSensitiveSuggestions(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
		case "/v1/chat/completions":
			http.Error(w, "missing model", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	disabled := false
	capabilitiesJSON, err := json.Marshal(config.UpstreamCapabilitiesConfig{ChatCompletions: &disabled})
	if err != nil {
		t.Fatalf("json.Marshal(capabilities) error = %v", err)
	}
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "probe-channel",
		Name:             "Probe Channel",
		BaseURL:          upstreamServer.URL + "/v1",
		APIKeyCiphertext: []byte("sk-probe"),
		APIKeyHint:       "sk...robe",
		HeadersJSON:      `{"Authorization":"Bearer secret","X-Test":"visible"}`,
		CapabilitiesJSON: string(capabilitiesJSON),
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}

	result, err := NewService(st).ApplyProviderProbeReport(context.Background(), ProviderProbeReportOptions{})
	if err != nil {
		t.Fatalf("ApplyProviderProbeReport() error = %v", err)
	}
	if len(result.Applied) != 1 || !result.Applied[0].Applied {
		t.Fatalf("Applied = %#v", result.Applied)
	}
	if !slices.Contains(result.Applied[0].AppliedFields, "api_type") ||
		!slices.Contains(result.Applied[0].AppliedFields, "protocol_family") ||
		!slices.Contains(result.Applied[0].AppliedFields, "capabilities.models") {
		t.Fatalf("AppliedFields = %#v", result.Applied[0].AppliedFields)
	}
	if slices.Contains(result.Applied[0].AppliedFields, "capabilities.chat_completions") {
		t.Fatalf("explicit false capability was applied: %#v", result.Applied[0].AppliedFields)
	}
	record, err := st.GetChannelConfig("probe-channel")
	if err != nil {
		t.Fatalf("GetChannelConfig() error = %v", err)
	}
	if record.APIType != "chat_completions" || record.ProtocolFamily != "openai_compatible" {
		t.Fatalf("record suggestions = %q/%q", record.APIType, record.ProtocolFamily)
	}
	if string(record.APIKeyCiphertext) != "sk-probe" || !strings.Contains(record.HeadersJSON, "Bearer secret") {
		t.Fatalf("secret fields were not preserved: api_key=%q headers=%s", string(record.APIKeyCiphertext), record.HeadersJSON)
	}
	var capabilities config.UpstreamCapabilitiesConfig
	if err := json.Unmarshal([]byte(record.CapabilitiesJSON), &capabilities); err != nil {
		t.Fatalf("json.Unmarshal(capabilities) error = %v", err)
	}
	if capabilities.ChatCompletions == nil || *capabilities.ChatCompletions {
		t.Fatalf("explicit chat_completions capability was overwritten: %#v", capabilities.ChatCompletions)
	}
	if capabilities.Models == nil || !*capabilities.Models {
		t.Fatalf("models capability was not applied: %#v", capabilities.Models)
	}
	runs, err := st.ListChannelProbeRuns("probe-channel", 10)
	if err != nil {
		t.Fatalf("ListChannelProbeRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("ApplyProviderProbeReport wrote probe runs: %#v", runs)
	}
}

func TestApplyProviderProbeReportSkipsNonDetectedAndSupportsChannelSelection(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"}]}`))
		case "/v1/chat/completions":
			http.Error(w, "missing model", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstreamServer.Close()
	unknownServer := httptest.NewServer(http.NotFoundHandler())
	defer unknownServer.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	for _, record := range []store.ChannelConfigRecord{
		{ID: "selected-channel", Name: "Selected Channel", BaseURL: upstreamServer.URL + "/v1", HeadersJSON: "{}", Enabled: true},
		{ID: "other-channel", Name: "Other Channel", BaseURL: upstreamServer.URL + "/v1", HeadersJSON: "{}", Enabled: true},
		{ID: "unknown-channel", Name: "Unknown Channel", BaseURL: unknownServer.URL + "/v1", HeadersJSON: "{}", Enabled: true},
	} {
		if _, err := st.UpsertChannelConfig(record); err != nil {
			t.Fatalf("UpsertChannelConfig(%s) error = %v", record.ID, err)
		}
	}

	selected, err := NewService(st).ApplyProviderProbeReport(context.Background(), ProviderProbeReportOptions{ChannelID: "selected-channel"})
	if err != nil {
		t.Fatalf("selected ApplyProviderProbeReport() error = %v", err)
	}
	if len(selected.Applied) != 1 || selected.Applied[0].ChannelID != "selected-channel" || !selected.Applied[0].Applied {
		t.Fatalf("selected Applied = %#v", selected.Applied)
	}
	other, err := st.GetChannelConfig("other-channel")
	if err != nil {
		t.Fatalf("GetChannelConfig(other-channel) error = %v", err)
	}
	if other.APIType != "" || other.ProtocolFamily != "" {
		t.Fatalf("unselected channel was updated: %q/%q", other.APIType, other.ProtocolFamily)
	}

	unknown, err := NewService(st).ApplyProviderProbeReport(context.Background(), ProviderProbeReportOptions{ChannelID: "unknown-channel"})
	if err != nil {
		t.Fatalf("unknown ApplyProviderProbeReport() error = %v", err)
	}
	if len(unknown.Applied) != 1 || unknown.Applied[0].Applied || unknown.Applied[0].SkippedReason == "" {
		t.Fatalf("unknown Applied = %#v", unknown.Applied)
	}
	record, err := st.GetChannelConfig("unknown-channel")
	if err != nil {
		t.Fatalf("GetChannelConfig(unknown-channel) error = %v", err)
	}
	if record.APIType != "" || record.ProtocolFamily != "" || record.CapabilitiesJSON != "{}" {
		t.Fatalf("unknown channel was updated: %+v", record)
	}
}

func TestProbeFailureRecordsRunAndKeepsExistingModels(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no models", http.StatusInternalServerError)
	}))
	defer upstreamServer.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "probe-channel",
		Name:           "Probe Channel",
		BaseURL:        upstreamServer.URL + "/v1",
		ProviderPreset: "openai",
		HeadersJSON:    "{}",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("probe-channel", []store.ChannelModelRecord{
		{Model: "existing-model", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}

	result, err := NewService(st).Probe("probe-channel")
	if err == nil {
		t.Fatalf("Probe() error = nil, want failure")
	}
	if result.Status != "failed" {
		t.Fatalf("Status = %q, want failed", result.Status)
	}

	models, err := st.ListChannelModels("probe-channel", true)
	if err != nil {
		t.Fatalf("ListChannelModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Model != "existing-model" {
		t.Fatalf("models after failed probe = %#v", models)
	}
	runs, err := st.ListChannelProbeRuns("probe-channel", 10)
	if err != nil {
		t.Fatalf("ListChannelProbeRuns() error = %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "failed" || runs[0].ErrorText == "" {
		t.Fatalf("probe runs = %#v", runs)
	}
	if !strings.Contains(runs[0].RequestMetaJSON, `"failure_reason":"upstream_error"`) {
		t.Fatalf("RequestMetaJSON = %s, want upstream_error classification", runs[0].RequestMetaJSON)
	}
}

func TestProbeWithOptionsDiscoversDisabledModelsWithoutReplacingManual(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("request path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-new"},{"id":"gpt-manual"}]}`))
	}))
	defer upstreamServer.Close()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "probe-channel",
		Name:           "Probe Channel",
		BaseURL:        upstreamServer.URL + "/v1",
		ProviderPreset: "openai",
		HeadersJSON:    "{}",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if _, err := st.UpsertChannelModel("probe-channel", store.ChannelModelRecord{
		Model:       "gpt-manual",
		DisplayName: "Manual Model",
		Source:      "manual",
		Enabled:     true,
	}); err != nil {
		t.Fatalf("UpsertChannelModel(manual) error = %v", err)
	}

	enable := false
	result, err := NewService(st).ProbeWithOptions("probe-channel", ProbeOptions{EnableDiscovered: &enable})
	if err != nil {
		t.Fatalf("ProbeWithOptions() error = %v", err)
	}
	if result.EnabledCount != 1 {
		t.Fatalf("EnabledCount = %d, want 1", result.EnabledCount)
	}
	models, err := st.ListChannelModels("probe-channel", false)
	if err != nil {
		t.Fatalf("ListChannelModels() error = %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2: %#v", len(models), models)
	}
	byModel := map[string]store.ChannelModelRecord{}
	for _, model := range models {
		byModel[model.Model] = model
	}
	if byModel["gpt-manual"].Source != "manual" || !byModel["gpt-manual"].Enabled || byModel["gpt-manual"].DisplayName != "Manual Model" {
		t.Fatalf("manual model was not preserved: %+v", byModel["gpt-manual"])
	}
	if byModel["gpt-new"].Source != "discovered" || byModel["gpt-new"].Enabled {
		t.Fatalf("new discovered model = %+v, want disabled discovered", byModel["gpt-new"])
	}
}

func TestRuntimeTargetsSkipsDisabledChannelsAndModels(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "enabled",
		Name:           "Enabled",
		BaseURL:        "https://example.com/v1",
		ProviderPreset: "openai",
		HeadersJSON:    "{}",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig(enabled) error = %v", err)
	}
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "disabled",
		Name:           "Disabled",
		BaseURL:        "https://disabled.example.com/v1",
		ProviderPreset: "openai",
		HeadersJSON:    "{}",
		Enabled:        false,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig(disabled) error = %v", err)
	}
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "enabled-secondary",
		Name:           "Enabled Secondary",
		BaseURL:        "https://secondary.example.com/v1",
		ProviderPreset: "anthropic",
		HeadersJSON:    "{}",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig(enabled-secondary) error = %v", err)
	}
	if err := st.ReplaceChannelModels("enabled", []store.ChannelModelRecord{
		{Model: "gpt-5", Source: "manual", Enabled: true},
		{Model: "gpt-4.1", Source: "manual", Enabled: false},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}
	if err := st.ReplaceChannelModels("enabled-secondary", []store.ChannelModelRecord{
		{Model: "claude-3", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels(enabled-secondary) error = %v", err)
	}

	targets, err := NewService(st).RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}
	// Each channel keeps its own models: the service reads every channel's models
	// in one query and groups them, so a channel must never inherit its
	// neighbour's list.
	byID := map[string][]string{}
	for _, target := range targets {
		byID[target.ID] = target.StaticModels
		if !target.ConfiguredModelsOnly {
			t.Fatalf("ConfiguredModelsOnly = false for %q, want true for a channel runtime target", target.ID)
		}
	}
	if len(byID["enabled"]) != 1 || byID["enabled"][0] != "gpt-5" {
		t.Fatalf("StaticModels(enabled) = %#v", byID["enabled"])
	}
	if len(byID["enabled-secondary"]) != 1 || byID["enabled-secondary"][0] != "claude-3" {
		t.Fatalf("StaticModels(enabled-secondary) = %#v", byID["enabled-secondary"])
	}
	if _, ok := byID["disabled"]; ok {
		t.Fatalf("disabled channel reached the runtime targets: %#v", byID)
	}
}

func TestRuntimeTargetsProjectsEnabledModelAliases(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	for _, channelID := range []string{"primary", "secondary"} {
		if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
			ID:             channelID,
			Name:           channelID,
			BaseURL:        "https://" + channelID + ".example.com/v1",
			ProviderPreset: "openai",
			HeadersJSON:    "{}",
			Enabled:        true,
		}); err != nil {
			t.Fatalf("UpsertChannelConfig(%s) error = %v", channelID, err)
		}
		if err := st.ReplaceChannelModels(channelID, []store.ChannelModelRecord{
			{Model: "gpt-5.5", Source: "manual", Enabled: true},
		}); err != nil {
			t.Fatalf("ReplaceChannelModels(%s) error = %v", channelID, err)
		}
	}

	if _, err := st.UpsertModelAlias(store.ModelAliasRecord{Alias: "abc", TargetModel: "gpt-5.5", Enabled: true}); err != nil {
		t.Fatalf("UpsertModelAlias(global) error = %v", err)
	}
	if _, err := st.UpsertModelAlias(store.ModelAliasRecord{Alias: "disabled", TargetModel: "gpt-5.5", Enabled: false}); err != nil {
		t.Fatalf("UpsertModelAlias(disabled) error = %v", err)
	}

	targets, err := NewService(st).RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}
	for _, target := range targets {
		if !slices.Equal(target.StaticModels, []string{"abc", "gpt-5.5"}) {
			t.Fatalf("target %s StaticModels = %#v", target.ID, target.StaticModels)
		}
	}
}

func TestRuntimeTargetsProjectsChannelScopedModelAliases(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	for _, channelID := range []string{"primary", "secondary"} {
		if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
			ID:             channelID,
			Name:           channelID,
			BaseURL:        "https://" + channelID + ".example.com/v1",
			ProviderPreset: "openai",
			HeadersJSON:    "{}",
			Enabled:        true,
		}); err != nil {
			t.Fatalf("UpsertChannelConfig(%s) error = %v", channelID, err)
		}
		if err := st.ReplaceChannelModels(channelID, []store.ChannelModelRecord{
			{Model: "gpt-5.5", Source: "manual", Enabled: true},
		}); err != nil {
			t.Fatalf("ReplaceChannelModels(%s) error = %v", channelID, err)
		}
	}

	if _, err := st.UpsertModelAlias(store.ModelAliasRecord{Alias: "coder", TargetModel: "gpt-5.5", ChannelID: "primary", Enabled: true}); err != nil {
		t.Fatalf("UpsertModelAlias(scoped) error = %v", err)
	}

	targets, err := NewService(st).RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	modelsByTarget := map[string][]string{}
	for _, target := range targets {
		modelsByTarget[target.ID] = target.StaticModels
	}
	if !slices.Equal(modelsByTarget["primary"], []string{"coder", "gpt-5.5"}) {
		t.Fatalf("primary StaticModels = %#v", modelsByTarget["primary"])
	}
	if !slices.Equal(modelsByTarget["secondary"], []string{"gpt-5.5"}) {
		t.Fatalf("secondary StaticModels = %#v", modelsByTarget["secondary"])
	}
}

func TestRuntimeTargetsProjectsPerModelCapabilities(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "mixed",
		Name:           "Mixed",
		BaseURL:        "https://mixed.example.com/v1",
		ProviderPreset: "openai",
		APIType:        "responses_native",
		HeadersJSON:    "{}",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	yes := 1
	no := 0
	if err := st.ReplaceChannelModels("mixed", []store.ChannelModelRecord{
		{Model: "gpt-native", Source: "manual", Enabled: true, SupportsResponses: &yes, SupportsChatCompletions: &no},
		{Model: "gpt-chat", Source: "manual", Enabled: true, SupportsResponses: &no, SupportsChatCompletions: &yes},
		{Model: "gpt-plain", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}
	if _, err := st.UpsertModelAlias(store.ModelAliasRecord{Alias: "native-alias", TargetModel: "gpt-native", Enabled: true}); err != nil {
		t.Fatalf("UpsertModelAlias() error = %v", err)
	}

	targets, err := NewService(st).RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	caps := targets[0].Upstream.ModelCapabilities

	native, ok := caps["gpt-native"]
	if !ok || native.Responses == nil || !*native.Responses || native.ChatCompletions == nil || *native.ChatCompletions {
		t.Fatalf("gpt-native capabilities = %+v, want responses=true chat=false", native)
	}
	chat, ok := caps["gpt-chat"]
	if !ok || chat.Responses == nil || *chat.Responses || chat.ChatCompletions == nil || !*chat.ChatCompletions {
		t.Fatalf("gpt-chat capabilities = %+v, want responses=false chat=true", chat)
	}
	alias, ok := caps["native-alias"]
	if !ok || alias.Responses == nil || !*alias.Responses {
		t.Fatalf("native-alias capabilities = %+v, want the gpt-native override", alias)
	}
	if _, ok := caps["gpt-plain"]; ok {
		t.Fatalf("gpt-plain must not carry an override, got %+v", caps["gpt-plain"])
	}
}

func TestBootstrapFromConfigImportsPerModelCapabilities(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	enabled := true
	responsesDisabled := false
	chatEnabled := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "mixed",
				Enabled:        &enabled,
				ModelDiscovery: "static_only",
				StaticModels:   []string{"gpt-native", "gpt-chat"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "responses_native",
					Mode:           "proxy",
					ModelCapabilities: map[string]config.UpstreamCapabilitiesConfig{
						"gpt-chat": {Responses: &responsesDisabled, ChatCompletions: &chatEnabled},
					},
				},
			},
		},
	}

	svc := NewService(st)
	if _, err := svc.BootstrapFromConfig(cfg); err != nil {
		t.Fatalf("BootstrapFromConfig() error = %v", err)
	}

	targets, err := svc.RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	caps := targets[0].Upstream.ModelCapabilities
	chat, ok := caps["gpt-chat"]
	if !ok || chat.Responses == nil || *chat.Responses || chat.ChatCompletions == nil || !*chat.ChatCompletions {
		t.Fatalf("gpt-chat capabilities = %+v, want responses=false chat=true", chat)
	}
	if _, ok := caps["gpt-native"]; ok {
		t.Fatalf("gpt-native must not carry an override, got %+v", caps["gpt-native"])
	}
}

func TestProbeDefaultsToDisabledAndPreservesManualChoices(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"new"},{"id":"enabled"},{"id":"disabled"}]}`))
	}))
	defer upstreamServer.Close()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{ID: "p", Name: "P", Enabled: true, BaseURL: upstreamServer.URL + "/v1", ProviderPreset: "openai"}); err != nil {
		t.Fatal(err)
	}
	contextWindow := 128000
	for _, model := range []store.ChannelModelRecord{{Model: "enabled", Enabled: true, Source: "manual", ContextWindow: &contextWindow, ProfileAdoptionStatus: "adopted"}, {Model: "disabled", Enabled: false, Source: "discovered"}} {
		if _, err := st.UpsertChannelModel("p", model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewService(st).Probe("p"); err != nil {
		t.Fatal(err)
	}
	models, err := st.ListChannelModels("p", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("models=%v", models)
	}
	for _, model := range models {
		if model.Model == "enabled" && (model.ContextWindow == nil || *model.ContextWindow != contextWindow || model.ProfileAdoptionStatus != "adopted") {
			t.Fatalf("discovery erased model profile: %+v", model)
		}
		if model.Enabled != (model.Model == "enabled") {
			t.Fatalf("discovery changed authorization: %+v", model)
		}
	}
}

func TestServiceBuildersLeaveTheirReceiverUntouched(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	base := NewService(st)
	scoped := base.WithStore(st)
	readOnly := base.WithReadOnly(true)
	withClient := base.WithHTTPClient(&http.Client{})

	if base.ReadOnly() {
		t.Fatal("WithReadOnly mutated the receiver")
	}
	if !readOnly.ReadOnly() {
		t.Fatal("WithReadOnly did not apply to the derived service")
	}
	if base.httpClient != nil {
		t.Fatal("WithHTTPClient mutated the receiver")
	}
	if withClient.httpClient == nil {
		t.Fatal("WithHTTPClient did not apply to the derived service")
	}
	if scoped == base || readOnly == base || withClient == base {
		t.Fatal("With* helpers returned the receiver instead of a derived service")
	}
	// A derived service must keep the specialization of the service it came from.
	if !readOnly.WithStore(st).ReadOnly() {
		t.Fatal("WithStore dropped the read-only flag")
	}
}

func TestResetConfigurationInitializedReopensYAMLImport(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	svc := NewService(st)

	if initialized, err := svc.HasConfiguration(); err != nil || initialized {
		t.Fatalf("HasConfiguration() on a fresh store = %v, %v; want false", initialized, err)
	}
	if err := svc.MarkConfigurationInitialized(); err != nil {
		t.Fatalf("MarkConfigurationInitialized() error = %v", err)
	}
	if initialized, err := svc.HasConfiguration(); err != nil || !initialized {
		t.Fatalf("HasConfiguration() after marking = %v, %v; want true", initialized, err)
	}
	if err := svc.ResetConfigurationInitialized(); err != nil {
		t.Fatalf("ResetConfigurationInitialized() error = %v", err)
	}
	if initialized, err := svc.HasConfiguration(); err != nil || initialized {
		t.Fatalf("HasConfiguration() after reset = %v, %v; want false", initialized, err)
	}
	// Resetting never deletes channels: stored configuration still wins over YAML.
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{ID: "primary", Name: "Primary", Enabled: true, BaseURL: "https://example.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	if initialized, err := svc.HasConfiguration(); err != nil || !initialized {
		t.Fatalf("HasConfiguration() with a stored channel = %v, %v; want true", initialized, err)
	}
	if err := svc.ResetConfigurationInitialized(); err != nil {
		t.Fatalf("ResetConfigurationInitialized() error = %v", err)
	}
	if initialized, err := svc.HasConfiguration(); err != nil || !initialized {
		t.Fatalf("reset dropped stored channels: initialized=%v err=%v", initialized, err)
	}
}

func TestBootstrapFromConfigStopsWritingOnceInitialized(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	svc := NewService(st)

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{ID: "primary", Name: "Primary", Enabled: true, BaseURL: "https://example.invalid/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetConfigurationInitialized(); err != nil {
		t.Fatal(err)
	}
	imported, err := svc.BootstrapFromConfig(&config.Config{})
	if err != nil || imported != 0 {
		t.Fatalf("BootstrapFromConfig() = %d, %v; want 0, nil", imported, err)
	}
	var marker bool
	if found, err := st.LoadAppSettingJSON(context.Background(), "channels.initialized", &marker); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatal("an already-initialized startup rewrote the bootstrap marker")
	}
	if initialized, err := svc.HasConfiguration(); err != nil || !initialized {
		t.Fatalf("stored channels no longer count as initialized: initialized=%v err=%v", initialized, err)
	}
}

func TestBootstrapFromConfigReimportsYAMLAfterReset(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	svc := NewService(st)

	enabled := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:      "openai-primary",
			Enabled: &enabled,
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://example.invalid/v1",
				ProviderPreset: "openai",
			},
		}},
	}
	if imported, err := svc.BootstrapFromConfig(cfg); err != nil || imported != 1 {
		t.Fatalf("first BootstrapFromConfig() = %d, %v; want 1, nil", imported, err)
	}
	channels, err := st.ListChannelConfigs()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range channels {
		if err := st.DeleteChannelConfig(record.ID); err != nil {
			t.Fatal(err)
		}
	}
	if initialized, err := svc.HasConfiguration(); err != nil || !initialized {
		t.Fatalf("marker was not persisted by the first import: initialized=%v err=%v", initialized, err)
	}
	if imported, err := svc.BootstrapFromConfig(cfg); err != nil || imported != 0 {
		t.Fatalf("BootstrapFromConfig() before reset = %d, %v; want 0, nil", imported, err)
	}
	if err := svc.ResetConfigurationInitialized(); err != nil {
		t.Fatal(err)
	}
	if imported, err := svc.BootstrapFromConfig(cfg); err != nil || imported != 1 {
		t.Fatalf("BootstrapFromConfig() after reset = %d, %v; want 1, nil", imported, err)
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func intPointer(value int) *int {
	return &value
}

func TestApplyProviderProbeSuggestionsGatesResponsesCapability(t *testing.T) {
	for _, tc := range []struct {
		name         string
		apiType      string
		capabilities config.UpstreamCapabilitiesConfig
		wantApplied  bool
	}{
		{
			name:        "chat typed channel",
			apiType:     upstream.APITypeChatCompletions,
			wantApplied: false,
		},
		{
			name:        "responses typed channel",
			apiType:     upstream.APITypeResponses,
			wantApplied: true,
		},
		{
			name:        "responses native typed channel",
			apiType:     upstream.APITypeResponsesNative,
			wantApplied: true,
		},
		{
			name:         "chat explicitly disabled",
			apiType:      upstream.APITypeChatCompletions,
			capabilities: config.UpstreamCapabilitiesConfig{ChatCompletions: boolPointer(false)},
			wantApplied:  true,
		},
		{
			name:         "chat explicitly enabled",
			apiType:      upstream.APITypeChatCompletions,
			capabilities: config.UpstreamCapabilitiesConfig{ChatCompletions: boolPointer(true)},
			wantApplied:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capabilitiesJSON, err := json.Marshal(tc.capabilities)
			if err != nil {
				t.Fatalf("json.Marshal(capabilities) error = %v", err)
			}
			channel := store.ChannelConfigRecord{
				ID:               "probe-channel",
				Name:             "Probe Channel",
				BaseURL:          "https://example.invalid/v1",
				APIType:          tc.apiType,
				CapabilitiesJSON: string(capabilitiesJSON),
			}
			updated, fields, err := applyProviderProbeSuggestions(channel, providerprobe.Report{
				CapabilitySuggestion: providerprobe.CapabilitySuggestion{
					Capabilities: []string{"responses", "chat_completions", "models"},
				},
			})
			if err != nil {
				t.Fatalf("applyProviderProbeSuggestions() error = %v", err)
			}
			var capabilities config.UpstreamCapabilitiesConfig
			if err := json.Unmarshal([]byte(updated.CapabilitiesJSON), &capabilities); err != nil {
				t.Fatalf("json.Unmarshal(capabilities) error = %v", err)
			}
			applied := capabilities.Responses != nil && *capabilities.Responses
			if applied != tc.wantApplied {
				t.Fatalf("responses applied = %v, want %v (fields = %#v)", applied, tc.wantApplied, fields)
			}
			if slices.Contains(fields, "capabilities.responses") != tc.wantApplied {
				t.Fatalf("fields = %#v, responses field mismatch", fields)
			}
			// The gate only affects the Responses capability; the rest of the report is applied.
			// An explicit value is never overwritten (SetCapabilityIfUnset semantics).
			switch {
			case tc.capabilities.ChatCompletions == nil:
				if capabilities.ChatCompletions == nil || !*capabilities.ChatCompletions {
					t.Fatalf("chat_completions capability was not applied: %#v", capabilities.ChatCompletions)
				}
			case capabilities.ChatCompletions == nil || *capabilities.ChatCompletions != *tc.capabilities.ChatCompletions:
				t.Fatalf("explicit chat_completions capability was overwritten: %#v", capabilities.ChatCompletions)
			}
			if capabilities.Models == nil || !*capabilities.Models {
				t.Fatalf("models capability was not applied: %#v", capabilities.Models)
			}
		})
	}
}

func TestModelProbeTargetResolvesModelAndDeclaredProtocols(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "nvidia-nim",
		Name:             "NVIDIA NIM",
		BaseURL:          "https://integrate.api.nvidia.com/v1",
		ProviderPreset:   "nvidia_nim",
		APIType:          upstream.APITypeChatCompletions,
		ProtocolFamily:   upstream.ProtocolFamilyOpenAICompatible,
		APIKeyCiphertext: []byte("sk-live-secret"),
		HeadersJSON:      "{}",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("nvidia-nim", []store.ChannelModelRecord{
		{
			Model:             "deepseek-v4.1-flash",
			UpstreamModel:     "deepseek-ai/deepseek-v4.1-flash",
			Source:            "manual",
			Enabled:           true,
			SupportsResponses: intPointer(1),
		},
		{
			Model:   "off-model",
			Source:  "manual",
			Enabled: false,
		},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}

	target, err := NewService(st).ModelProbeTarget("nvidia-nim", "  DEEPSEEK-V4.1-FLASH ")
	if err != nil {
		t.Fatalf("ModelProbeTarget() error = %v", err)
	}
	if target.ChannelID != "nvidia-nim" || target.Model != "deepseek-v4.1-flash" {
		t.Fatalf("identity = %q/%q", target.ChannelID, target.Model)
	}
	if target.WireModel != "deepseek-ai/deepseek-v4.1-flash" {
		t.Fatalf("wire model = %q", target.WireModel)
	}
	if target.ProtocolFamily != upstream.ProtocolFamilyOpenAICompatible {
		t.Fatalf("protocol family = %q", target.ProtocolFamily)
	}
	if target.Resolved.APIKey != "sk-live-secret" {
		t.Fatalf("resolved API key = %q, want the decrypted key", target.Resolved.APIKey)
	}
	if !target.Resolved.SupportsChatCompletionsAPIForModel(target.Model) {
		t.Fatalf("chat completions not supported by the resolved upstream")
	}
	if !target.Declared[modelprobe.ProtocolChatCompletions] || !target.Declared[modelprobe.ProtocolResponses] {
		t.Fatalf("declared = %#v", target.Declared)
	}
	if !target.NativeResponses {
		t.Fatalf("native responses = false, want true for a model that declares Responses")
	}
	if !target.ChannelEnabled || !target.ModelEnabled {
		t.Fatalf("enabled flags = %v/%v", target.ChannelEnabled, target.ModelEnabled)
	}
	if target.ModelState != string(ModelStateUnverified) || !target.Routable {
		t.Fatalf("state = %q routable = %v", target.ModelState, target.Routable)
	}

	if _, err := NewService(st).ModelProbeTarget("nvidia-nim", "not-a-model"); !errors.Is(err, ErrChannelModelNotFound) {
		t.Fatalf("ModelProbeTarget(unknown model) error = %v, want ErrChannelModelNotFound", err)
	}
	if _, err := NewService(st).ModelProbeTarget("missing-channel", "deepseek-v4.1-flash"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ModelProbeTarget(unknown channel) error = %v, want sql.ErrNoRows", err)
	}
	if _, err := NewService(st).ModelProbeTarget("nvidia-nim", ""); err == nil {
		t.Fatalf("ModelProbeTarget(empty model) error = nil")
	}
}

func TestModelProbeTargetDeclaresChannelResponsesWithoutNativeSupport(t *testing.T) {
	// The NVIDIA NIM incident shape: the channel advertises both Responses and Chat
	// Completions but its api_type is chat_completions and the model has no override.
	// Responses is still *declared* (that is what the operator needs tested) even though
	// the gateway will not use the native path.
	capabilitiesJSON, err := json.Marshal(config.UpstreamCapabilitiesConfig{
		Responses:       boolPointer(true),
		ChatCompletions: boolPointer(true),
	})
	if err != nil {
		t.Fatalf("json.Marshal(capabilities) error = %v", err)
	}
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "nvidia-nim",
		Name:             "NVIDIA NIM",
		BaseURL:          "https://integrate.api.nvidia.com/v1",
		ProviderPreset:   "nvidia_nim",
		APIType:          upstream.APITypeChatCompletions,
		ProtocolFamily:   upstream.ProtocolFamilyOpenAICompatible,
		CapabilitiesJSON: string(capabilitiesJSON),
		APIKeyCiphertext: []byte("sk-live-secret"),
		HeadersJSON:      "{}",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("nvidia-nim", []store.ChannelModelRecord{
		// No SupportsResponses override: the declaration comes from the channel.
		{Model: "deepseek-v4.1-flash", UpstreamModel: "deepseek-ai/deepseek-v4.1-flash", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}

	target, err := NewService(st).ModelProbeTarget("nvidia-nim", "deepseek-v4.1-flash")
	if err != nil {
		t.Fatalf("ModelProbeTarget() error = %v", err)
	}
	if !target.Declared[modelprobe.ProtocolResponses] {
		t.Fatalf("declared = %#v, want the channel-level Responses capability declared", target.Declared)
	}
	if !target.Declared[modelprobe.ProtocolChatCompletions] {
		t.Fatalf("declared = %#v, want chat completions declared", target.Declared)
	}
	if target.NativeResponses {
		t.Fatalf("native_responses = true, want false for a chat-typed channel without a model override")
	}
}

func TestModelProbeTargetMapsUnresolvableChannelConfig(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "broken",
		Name:           "Broken",
		BaseURL:        "https://example.invalid/v1",
		APIType:        upstream.APITypeChatCompletions,
		ProtocolFamily: "not-a-protocol-family",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("broken", []store.ChannelModelRecord{{Model: "m", Source: "manual", Enabled: true}}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}

	if _, err := NewService(st).ModelProbeTarget("broken", "m"); !errors.Is(err, ErrChannelConfigInvalid) {
		t.Fatalf("ModelProbeTarget() error = %v, want ErrChannelConfigInvalid", err)
	}
}

func newInterfaceTestStore(t *testing.T, baseURL, apiType, protocolFamily, routingProfile, modelResource string, capabilities config.UpstreamCapabilitiesConfig, models []store.ChannelModelRecord) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	capabilitiesJSON, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatalf("json.Marshal(capabilities) error = %v", err)
	}
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "probe",
		Name:             "Probe Channel",
		BaseURL:          baseURL,
		APIType:          apiType,
		ProtocolFamily:   protocolFamily,
		RoutingProfile:   routingProfile,
		ModelResource:    modelResource,
		CapabilitiesJSON: string(capabilitiesJSON),
		APIKeyCiphertext: []byte("sk-detect"),
		HeadersJSON:      "{}",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("probe", models); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}
	return st
}

func TestDetectModelInterfacesOpenAI(t *testing.T) {
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			authHeader = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[]}`))
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	st := newInterfaceTestStore(t, server.URL+"/v1", upstream.APITypeChatCompletions, upstream.ProtocolFamilyOpenAICompatible, "", "", config.UpstreamCapabilitiesConfig{}, []store.ChannelModelRecord{
		{Model: "deepseek-v4.1-flash", UpstreamModel: "deepseek-ai/deepseek-v4.1-flash", Source: "manual", Enabled: true},
	})
	detection, err := NewService(st).DetectModelInterfaces(context.Background(), "probe", "deepseek-v4.1-flash", 2*time.Second)
	if err != nil {
		t.Fatalf("DetectModelInterfaces() error = %v", err)
	}
	if authHeader != "Bearer sk-detect" {
		t.Fatalf("Authorization = %q, want the decrypted channel key", authHeader)
	}
	if detection.WireModel != "deepseek-ai/deepseek-v4.1-flash" || detection.ProtocolFamily != upstream.ProtocolFamilyOpenAICompatible {
		t.Fatalf("detection identity = %+v", detection)
	}
	if len(detection.Protocols) != 2 {
		t.Fatalf("protocols = %+v", detection.Protocols)
	}
	byProtocol := map[string]ModelInterfaceProtocolReport{}
	for _, protocol := range detection.Protocols {
		byProtocol[protocol.Protocol] = protocol
	}
	if byProtocol["chat_completions"].Status != modelprobe.StatusOK {
		t.Fatalf("chat report = %+v", byProtocol["chat_completions"])
	}
	if byProtocol["chat_completions"].LatencyMS < 0 || byProtocol["chat_completions"].HTTPStatus != http.StatusOK {
		t.Fatalf("chat report = %+v", byProtocol["chat_completions"])
	}
	if byProtocol["responses"].Status != modelprobe.StatusEndpointNotSupported {
		t.Fatalf("responses report = %+v", byProtocol["responses"])
	}
	if detection.SupportsChatCompletions == nil || !*detection.SupportsChatCompletions {
		t.Fatalf("SupportsChatCompletions = %v, want true", detection.SupportsChatCompletions)
	}
	if detection.SupportsResponses == nil || *detection.SupportsResponses {
		t.Fatalf("SupportsResponses = %v, want false", detection.SupportsResponses)
	}
}

func TestDetectModelInterfacesOtherFamilies(t *testing.T) {
	for _, tc := range []struct {
		name           string
		family         string
		apiType        string
		routingProfile string
		modelResource  string
		wireModel      string
		protocol       string
		wantPathPrefix string
		responseBody   string
	}{
		{
			name:           "anthropic messages",
			family:         upstream.ProtocolFamilyAnthropicMessages,
			apiType:        upstream.APITypeMessages,
			wireModel:      "claude-test",
			protocol:       "messages",
			wantPathPrefix: "/v1/messages",
			responseBody:   `{"id":"msg-1","content":[{"type":"text","text":"pong"}]}`,
		},
		{
			name:           "google generate content",
			family:         upstream.ProtocolFamilyGoogleGenAI,
			apiType:        upstream.APITypeGemini,
			wireModel:      "gemini-2.5-flash",
			protocol:       "generate_content",
			wantPathPrefix: "/v1beta/models/gemini-2.5-flash:generateContent",
			responseBody:   `{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}`,
		},
		{
			name:           "vertex generate content",
			family:         upstream.ProtocolFamilyVertexNative,
			apiType:        upstream.APITypeGemini,
			routingProfile: upstream.RoutingProfileVertexExpress,
			modelResource:  "publishers/google/models",
			wireModel:      "gemini-2.5-pro",
			protocol:       "generate_content",
			wantPathPrefix: "/v1/publishers/models/gemini-2.5-pro:generateContent",
			responseBody:   `{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.responseBody))
			}))
			defer server.Close()
			st := newInterfaceTestStore(t, server.URL, tc.apiType, tc.family, tc.routingProfile, tc.modelResource, config.UpstreamCapabilitiesConfig{}, []store.ChannelModelRecord{
				{Model: "client-model", UpstreamModel: tc.wireModel, Source: "manual", Enabled: true},
			})
			detection, err := NewService(st).DetectModelInterfaces(context.Background(), "probe", "client-model", 2*time.Second)
			if err != nil {
				t.Fatalf("DetectModelInterfaces() error = %v", err)
			}
			if gotPath != tc.wantPathPrefix {
				t.Fatalf("path = %q, want %q", gotPath, tc.wantPathPrefix)
			}
			if len(detection.Protocols) != 1 || detection.Protocols[0].Protocol != tc.protocol {
				t.Fatalf("protocols = %+v, want only %s", detection.Protocols, tc.protocol)
			}
			if detection.Protocols[0].Status != modelprobe.StatusOK {
				t.Fatalf("protocol report = %+v", detection.Protocols[0])
			}
			if detection.SupportsChatCompletions != nil || detection.SupportsResponses != nil {
				t.Fatalf("non-openai detection decided capability columns: %+v", detection)
			}
		})
	}
}

func TestDetectModelInterfacesInconclusive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
	}))
	defer server.Close()
	st := newInterfaceTestStore(t, server.URL+"/v1", upstream.APITypeChatCompletions, upstream.ProtocolFamilyOpenAICompatible, "", "", config.UpstreamCapabilitiesConfig{}, []store.ChannelModelRecord{
		{Model: "m", Source: "manual", Enabled: true},
	})
	detection, err := NewService(st).DetectModelInterfaces(context.Background(), "probe", "m", 2*time.Second)
	if err != nil {
		t.Fatalf("DetectModelInterfaces() error = %v", err)
	}
	if detection.SupportsChatCompletions != nil || detection.SupportsResponses != nil {
		t.Fatalf("auth failure decided capability columns: %+v", detection)
	}
	for _, protocol := range detection.Protocols {
		if protocol.Status != modelprobe.StatusAuthFailed {
			t.Fatalf("protocol report = %+v, want auth_failed", protocol)
		}
	}
}

func TestDetectModelInterfacesPerProtocolTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()
	st := newInterfaceTestStore(t, server.URL+"/v1", upstream.APITypeChatCompletions, upstream.ProtocolFamilyOpenAICompatible, "", "", config.UpstreamCapabilitiesConfig{}, []store.ChannelModelRecord{
		{Model: "m", Source: "manual", Enabled: true},
	})
	detection, err := NewService(st).DetectModelInterfaces(context.Background(), "probe", "m", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("DetectModelInterfaces() error = %v", err)
	}
	if detection.SupportsChatCompletions != nil || detection.SupportsResponses != nil {
		t.Fatalf("timeout decided capability columns: %+v", detection)
	}
	for _, protocol := range detection.Protocols {
		if protocol.Status != modelprobe.StatusTimeout {
			t.Fatalf("protocol report = %+v, want timeout", protocol)
		}
	}
}

func TestEffectiveModelInterfaces(t *testing.T) {
	trueInt := 1
	falseInt := 0
	for _, tc := range []struct {
		name    string
		channel store.ChannelConfigRecord
		model   store.ChannelModelRecord
		want    ModelInterfaces
	}{
		{
			name: "incident shape: channel capabilities only, chat capable",
			channel: store.ChannelConfigRecord{
				BaseURL:          "https://integrate.api.nvidia.com/v1",
				APIType:          upstream.APITypeChatCompletions,
				ProtocolFamily:   upstream.ProtocolFamilyOpenAICompatible,
				CapabilitiesJSON: `{"responses":true,"chat_completions":true}`,
			},
			model: store.ChannelModelRecord{Model: "deepseek-v4.1-flash", Enabled: true},
			want:  ModelInterfaces{ChatCompletions: true, NativeResponses: false, ResponsesViaChat: true},
		},
		{
			name: "model opts into native responses",
			channel: store.ChannelConfigRecord{
				BaseURL:          "https://integrate.api.nvidia.com/v1",
				APIType:          upstream.APITypeChatCompletions,
				ProtocolFamily:   upstream.ProtocolFamilyOpenAICompatible,
				CapabilitiesJSON: `{"responses":true,"chat_completions":true}`,
			},
			model: store.ChannelModelRecord{Model: "deepseek-v4.1-flash", Enabled: true, SupportsResponses: &trueInt},
			want:  ModelInterfaces{ChatCompletions: true, NativeResponses: true, ResponsesViaChat: false},
		},
		{
			name: "model opts out of chat",
			channel: store.ChannelConfigRecord{
				BaseURL:        "https://api.openai.com/v1",
				APIType:        upstream.APITypeChatCompletions,
				ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
			},
			model: store.ChannelModelRecord{Model: "gpt-x", Enabled: true, SupportsChatCompletions: &falseInt},
			want:  ModelInterfaces{},
		},
		{
			name: "responses typed channel is native",
			channel: store.ChannelConfigRecord{
				BaseURL:        "https://api.openai.com/v1",
				APIType:        upstream.APITypeResponses,
				ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
			},
			model: store.ChannelModelRecord{Model: "gpt-x", Enabled: true},
			want:  ModelInterfaces{NativeResponses: true},
		},
		{
			name: "anthropic messages",
			channel: store.ChannelConfigRecord{
				BaseURL:        "https://api.anthropic.com",
				APIType:        upstream.APITypeMessages,
				ProtocolFamily: upstream.ProtocolFamilyAnthropicMessages,
			},
			model: store.ChannelModelRecord{Model: "claude-x", Enabled: true},
			want:  ModelInterfaces{Messages: true},
		},
		{
			name: "google generate content",
			channel: store.ChannelConfigRecord{
				BaseURL:        "https://generativelanguage.googleapis.com",
				APIType:        upstream.APITypeGemini,
				ProtocolFamily: upstream.ProtocolFamilyGoogleGenAI,
			},
			model: store.ChannelModelRecord{Model: "gemini-x", Enabled: true},
			want:  ModelInterfaces{GenerateContent: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.model.Enabled = true
			got := EffectiveModelInterfaces(tc.channel, tc.model)
			if got != tc.want {
				t.Fatalf("EffectiveModelInterfaces() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEffectiveModelInterfacesUnresolvableChannelDegradesToFamily(t *testing.T) {
	got := EffectiveModelInterfaces(store.ChannelConfigRecord{
		BaseURL:        "https://example.invalid",
		ProtocolFamily: "not-a-family",
	}, store.ChannelModelRecord{Model: "m", Enabled: true})
	if got != (ModelInterfaces{}) {
		t.Fatalf("EffectiveModelInterfaces() = %+v, want a zero value", got)
	}
}
