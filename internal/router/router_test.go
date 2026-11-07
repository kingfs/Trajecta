package router

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/upstream"
)

func boolPtr(v bool) *bool { return &v }

func float64Ptr(v float64) *float64 { return &v }

func TestRouterSelectUsesModelCatalog(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-4.1", "claude-3-7-sonnet"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-4.1","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "fallback" {
		t.Fatalf("selected target = %q, want fallback", selection.Target.ID)
	}
}

func TestRouterAllowsEmptyStartupConfig(t *testing.T) {
	rtr, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("New(empty) error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize(empty) error = %v", err)
	}
	if got := rtr.Targets(); len(got) != 0 {
		t.Fatalf("len(Targets()) = %d, want 0", len(got))
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	_, err = rtr.Select(req)
	if err == nil {
		t.Fatal("Select(empty) error = nil, want no supporting target")
	}
	if SelectionFailureReason(err) != SelectionFailureNoSupportingTarget {
		t.Fatalf("SelectionFailureReason() = %q, want %q", SelectionFailureReason(err), SelectionFailureNoSupportingTarget)
	}
}

// TestRouterSelectionFailureNamesTheRequestedModelForUncoveredEndpoint pins
// that a routing failure for an endpoint no adapter covers still names the model
// the client asked for.
//
// /v1/embeddings is classified and recorded but never routed, so it always fails
// here. The model used to be lost because it is read through an adapter and no
// adapter covers the endpoint, which turned the failure into
// `model "" for endpoint "/v1/embeddings"` for a request that plainly said
// which model it wanted.
func TestRouterSelectionFailureNamesTheRequestedModelForUncoveredEndpoint(t *testing.T) {
	rtr, err := New(&config.Config{})
	if err != nil {
		t.Fatalf("New(empty) error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize(empty) error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/embeddings", strings.NewReader(`{"model":"bge-m3","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	_, err = rtr.Select(req)
	if err == nil {
		t.Fatal("Select(embeddings) error = nil, want no supporting target")
	}
	if SelectionFailureReason(err) != SelectionFailureNoSupportingTarget {
		t.Fatalf("SelectionFailureReason() = %q, want %q", SelectionFailureReason(err), SelectionFailureNoSupportingTarget)
	}
	if !strings.Contains(err.Error(), `model "bge-m3"`) {
		t.Fatalf("Select(embeddings) error = %q, want it to name the requested model", err)
	}
}

func TestRouterReloadReplacesCatalogAndPreservesOldOnFailure(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	target := rtr.Targets()[0]
	target.onFinish(RequestFeatures{}, Outcome{Success: true, StatusCode: 200, DurationMs: 1234, TTFTMs: 321}, rtr.costs, 3, time.Minute)
	beforeLatency := target.snapshot().LatencyFastMs

	err = rtr.Reload([]config.UpstreamTargetConfig{
		{
			ID:             "primary",
			Enabled:        boolPtr(true),
			Priority:       100,
			ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-4.1"},
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://api.openai.com/v1",
				ProviderPreset: "openai",
			},
		},
	})
	if err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := rtr.Targets()[0].snapshot().LatencyFastMs; got != beforeLatency {
		t.Fatalf("LatencyFastMs after reload = %v, want inherited %v", got, beforeLatency)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-4.1","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select(gpt-4.1) error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("selected target = %q, want primary", selection.Target.ID)
	}

	err = rtr.Reload([]config.UpstreamTargetConfig{
		{
			ID:             "broken",
			Enabled:        boolPtr(true),
			ModelDiscovery: ModelDiscoveryStaticOnly,
			Upstream: config.UpstreamConfig{
				BaseURL: "://bad-url",
			},
		},
	})
	if err == nil {
		t.Fatalf("Reload(invalid) error = nil, want error")
	}
	selection, err = rtr.Select(req)
	if err != nil {
		t.Fatalf("Select after failed reload error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("selected target after failed reload = %q, want primary", selection.Target.ID)
	}
}

func TestRouterSingleTargetDoesNotWidenModelAuthorization(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:                   "default",
				Enabled:              boolPtr(true),
				Priority:             100,
				ModelDiscovery:       ModelDiscoveryStaticOnly,
				ConfiguredModelsOnly: true,
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(`{"model":"unknown-future-model"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if _, err := rtr.Select(req); err == nil {
		t.Fatal("Select() succeeded, want a strict authorization failure for an undeclared model")
	} else if got := SelectionFailureReason(err); got != SelectionFailureModelUnknown {
		t.Fatalf("SelectionFailureReason() = %q, want %q", got, SelectionFailureModelUnknown)
	}
}

func TestRouterChatCompletionsRequiresChatCapableAPISurface(t *testing.T) {
	chatDisabled := false
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "native-responses",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
					APIType:        "responses_native",
					Capabilities: config.UpstreamCapabilitiesConfig{
						ChatCompletions: &chatDisabled,
					},
				},
			},
			{
				ID:             "chat",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://compat.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "chat" {
		t.Fatalf("selected target = %q, want chat", selection.Target.ID)
	}
	if selection.Decision == nil || len(selection.Decision.Candidates) != 2 {
		t.Fatalf("selection decision missing candidates: %+v", selection.Decision)
	}
	for _, candidate := range selection.Decision.Candidates {
		if candidate.ID == "chat" && candidate.APIType != "chat_completions" {
			t.Fatalf("chat candidate APIType = %q, want chat_completions", candidate.APIType)
		}
		if candidate.ID == "native-responses" && (candidate.SupportsPath || candidate.FilterReason != "unsupported_path") {
			t.Fatalf("native responses candidate = %+v, want unsupported_path", candidate)
		}
	}
	snapshots := rtr.Snapshots()
	if len(snapshots) != 2 {
		t.Fatalf("len(snapshots) = %d, want 2", len(snapshots))
	}
	for _, snapshot := range snapshots {
		if snapshot.ID == "native-responses" && snapshot.APIType != "responses_native" {
			t.Fatalf("native response snapshot APIType = %q, want responses_native", snapshot.APIType)
		}
	}
}

func TestRouterResponsesEndpointAllowsNativeResponsesTarget(t *testing.T) {
	chatDisabled := false
	responsesEnabled := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "native-responses",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
					APIType:        "responses_native",
					Mode:           "proxy",
					Capabilities: config.UpstreamCapabilitiesConfig{
						Responses:       &responsesEnabled,
						ChatCompletions: &chatDisabled,
					},
				},
			},
			{
				ID:             "chat",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://compat.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "native-responses" {
		t.Fatalf("selected target = %q, want native-responses", selection.Target.ID)
	}
	if selection.Target.Upstream.APIType != "responses_native" {
		t.Fatalf("selected APIType = %q, want responses_native", selection.Target.Upstream.APIType)
	}
	if selection.Decision == nil || len(selection.Decision.Candidates) != 2 {
		t.Fatalf("selection decision missing candidates: %+v", selection.Decision)
	}
	for _, candidate := range selection.Decision.Candidates {
		if candidate.ID == "native-responses" && (!candidate.SupportsPath || candidate.FilterReason != "") {
			t.Fatalf("native responses candidate = %+v, want selectable for /v1/responses", candidate)
		}
		if candidate.ID == "chat" && !candidate.SupportsPath {
			t.Fatalf("chat completions candidate = %+v, want compatible fallback for /v1/responses", candidate)
		}
	}
}

func TestSupportsLocalResponsesServerBackendExcludesNativeResponsesOnlyTarget(t *testing.T) {
	responsesEnabled := true
	chatDisabled := false
	native, err := upstream.Resolve(config.UpstreamConfig{
		BaseURL:        "https://api.openai.com/v1",
		ProviderPreset: "openai",
		APIType:        "responses_native",
		Capabilities: config.UpstreamCapabilitiesConfig{
			Responses:       &responsesEnabled,
			ChatCompletions: &chatDisabled,
		},
	})
	if err != nil {
		t.Fatalf("Resolve(native) error = %v", err)
	}
	if !native.SupportsEndpointForModel("/v1/responses", "") {
		t.Fatal("native responses target should support /v1/responses pass-through")
	}
	if SupportsLocalResponsesServerBackend(native) {
		t.Fatal("native responses-only target must not satisfy local Responses server backend")
	}

	chat, err := upstream.Resolve(config.UpstreamConfig{
		BaseURL:        "https://compat.example.com/v1",
		ProviderPreset: "openai",
		APIType:        "chat_completions",
	})
	if err != nil {
		t.Fatalf("Resolve(chat) error = %v", err)
	}
	if !SupportsLocalResponsesServerBackend(chat) {
		t.Fatal("chat completions target should satisfy local Responses server backend")
	}
}

func TestRouterSelectFiltersTargetsWithoutToolCallingCapability(t *testing.T) {
	toolCallingDisabled := false
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "no-tools",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://compat-no-tools.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
					Capabilities: config.UpstreamCapabilitiesConfig{
						ToolCalling: &toolCallingDisabled,
					},
				},
			},
			{
				ID:             "tools",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://compat-tools.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "tools" {
		t.Fatalf("selected target = %q, want tools", selection.Target.ID)
	}
	var sawNoTools bool
	for _, candidate := range selection.Decision.Candidates {
		if candidate.ID != "no-tools" {
			continue
		}
		sawNoTools = true
		if candidate.SupportsTools || candidate.Selectable || candidate.FilterReason != "unsupported_tools" {
			t.Fatalf("no-tools candidate = %+v, want unsupported_tools", candidate)
		}
	}
	if !sawNoTools {
		t.Fatalf("decision did not include no-tools candidate: %+v", selection.Decision)
	}
}

func TestRouterSelectReturnsStructuredNoSupportingTargetError(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5", "claude-3-7-sonnet"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-4.1"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	// /v1/embeddings is classified and recorded but never routed: no adapter
	// covers it. The model is declared, so this exercises the "supported model,
	// unsupported endpoint" path rather than model authorization.
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/embeddings", strings.NewReader(`{"model":"claude-3-7-sonnet","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	_, err = rtr.Select(req)
	if err == nil {
		t.Fatalf("Select() error = nil, want structured error")
	}
	if SelectionFailureReason(err) != SelectionFailureNoSupportingTarget {
		t.Fatalf("SelectionFailureReason() = %q, want %q", SelectionFailureReason(err), SelectionFailureNoSupportingTarget)
	}
	decision := SelectionDecision(err)
	if decision == nil {
		t.Fatalf("SelectionDecision() = nil, want decision trace")
	}
	if decision.ModelName != "claude-3-7-sonnet" {
		t.Fatalf("decision.ModelName = %q, want claude-3-7-sonnet", decision.ModelName)
	}
	if decision.FailureReason != SelectionFailureNoSupportingTarget {
		t.Fatalf("decision.FailureReason = %q, want %q", decision.FailureReason, SelectionFailureNoSupportingTarget)
	}
	if len(decision.Candidates) != 2 {
		t.Fatalf("len(decision.Candidates) = %d, want 2", len(decision.Candidates))
	}
	for _, candidate := range decision.Candidates {
		if candidate.Selectable {
			t.Fatalf("candidate %q selectable = true, want false", candidate.ID)
		}
		if candidate.FilterReason != "unsupported_path" {
			t.Fatalf("candidate %q FilterReason = %q, want unsupported_path", candidate.ID, candidate.FilterReason)
		}
	}
}

func TestRouterSelectionCarriesDecisionTrace(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Decision == nil {
		t.Fatalf("selection.Decision = nil, want decision trace")
	}
	if selection.Decision.SelectedID != "primary" {
		t.Fatalf("decision.SelectedID = %q, want primary", selection.Decision.SelectedID)
	}
	if selection.Decision.AvailableCount != 2 {
		t.Fatalf("decision.AvailableCount = %d, want 2", selection.Decision.AvailableCount)
	}
	if len(selection.Decision.Candidates) != 2 {
		t.Fatalf("len(decision.Candidates) = %d, want 2", len(selection.Decision.Candidates))
	}
}

func TestRouterStickySessionReusesBoundTarget(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable
	// This test pins the affinity mechanics, not the rebalancing policy: disable
	// the cost check so the deliberately slower bound target stays sticky.
	cfg.Router.Selection.StickyMaxCostRatio = float64Ptr(0)

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req1 := stickyRequest(t, "same-session")
	selection, err := rtr.Select(req1)
	if err != nil {
		t.Fatalf("Select(first) error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("first target = %q, want primary", selection.Target.ID)
	}
	if selection.Decision.StickyStatus != "bind" {
		t.Fatalf("first sticky status = %q, want bind", selection.Decision.StickyStatus)
	}

	targets := rtr.Targets()
	targets[0].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 5000, TTFTMs: 1800}, rtr.costs, 3, time.Minute)
	targets[1].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 400, TTFTMs: 80}, rtr.costs, 3, time.Minute)
	req2 := stickyRequest(t, "same-session")
	selection, err = rtr.Select(req2)
	if err != nil {
		t.Fatalf("Select(second) error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("second target = %q, want sticky primary", selection.Target.ID)
	}
	if selection.Decision.StickyStatus != "hit" {
		t.Fatalf("second sticky status = %q, want hit", selection.Decision.StickyStatus)
	}
}

func TestRouterStickyBindingSurvivesRetryExclusion(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	req := stickyRequest(t, "session-break")
	selection, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("Select(first) error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("first target = %q, want primary", selection.Target.ID)
	}

	selection, err = rtr.SelectWithExclusion(stickyRequest(t, "session-break"), body, []string{"primary"})
	if err != nil {
		t.Fatalf("SelectWithExclusion() error = %v", err)
	}
	if selection.Target.ID != "fallback" {
		t.Fatalf("excluded sticky target selection = %q, want fallback", selection.Target.ID)
	}
	// The exclusion only describes this attempt, so the decision holds the
	// binding instead of rewriting it to the fallback.
	if selection.Decision.StickyStatus != "hold" {
		t.Fatalf("sticky status = %q, want hold", selection.Decision.StickyStatus)
	}
	if !hasStickyDecision(selection.Decision, "hold", "primary") {
		t.Fatalf("sticky events = %+v, want hold for primary", selection.Decision.StickyEvents)
	}
	if selection.Decision.StickyBreakID != "primary" {
		t.Fatalf("sticky break id = %q, want primary", selection.Decision.StickyBreakID)
	}

	// The next request must try the preferred target again rather than staying
	// pinned to the fallback for the rest of the TTL.
	selection, err = rtr.Select(stickyRequest(t, "session-break"))
	if err != nil {
		t.Fatalf("Select(after retry) error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("after retry target = %q, want primary", selection.Target.ID)
	}
	if selection.Decision.StickyStatus != "hit" {
		t.Fatalf("after retry sticky status = %q, want hit", selection.Decision.StickyStatus)
	}
}

// TestRouterStickyBindingBreaksWhenTargetUnavailable keeps the other half of
// the contract: a binding whose target is unavailable for a reason other than
// this request's own retry exclusion is still broken and rebound, so a target
// that is genuinely down does not keep receiving traffic.
func TestRouterStickyBindingBreaksWhenTargetUnavailable(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream:       config.UpstreamConfig{BaseURL: "https://api.openai.com/v1", ProviderPreset: "openai"},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream:       config.UpstreamConfig{BaseURL: "https://openrouter.ai/api/v1", ProviderPreset: "openrouter"},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	first, err := rtr.SelectWithBody(stickyRequest(t, "session-open"), body)
	if err != nil {
		t.Fatalf("Select(first) error = %v", err)
	}
	if first.Target.ID != "primary" || first.Decision.StickyStatus != "bind" {
		t.Fatalf("first selection target/status = %q/%q, want primary bind", first.Target.ID, first.Decision.StickyStatus)
	}

	primary := rtr.targets[0]
	for i := 0; i < 3; i++ {
		primary.onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: false, StatusCode: 503}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	}
	// A 5xx on a known model is model-scoped: the target-level breaker stays
	// healthy and the per-model one is what removes the candidate.
	if state := primary.snapshot().ModelHealth["gpt-5"].HealthState; state != HealthOpen {
		t.Fatalf("primary gpt-5 model health = %q, want open", state)
	}

	second, err := rtr.SelectWithBody(stickyRequest(t, "session-open"), body)
	if err != nil {
		t.Fatalf("Select(open) error = %v", err)
	}
	if second.Target.ID != "fallback" {
		t.Fatalf("open-target selection = %q, want fallback", second.Target.ID)
	}
	if !hasStickyDecision(second.Decision, "break", "primary") {
		t.Fatalf("sticky events = %+v, want break for primary", second.Decision.StickyEvents)
	}
	if second.Decision.StickyStatus != "bind" {
		t.Fatalf("sticky status = %q, want bind to the fallback", second.Decision.StickyStatus)
	}

	third, err := rtr.SelectWithBody(stickyRequest(t, "session-open"), body)
	if err != nil {
		t.Fatalf("Select(after rebind) error = %v", err)
	}
	if third.Target.ID != "fallback" || third.Decision.StickyStatus != "hit" {
		t.Fatalf("after rebind target/status = %q/%q, want fallback hit", third.Target.ID, third.Decision.StickyStatus)
	}
}

func TestRouterSelectWithoutStickyKeyUnchanged(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream:       config.UpstreamConfig{BaseURL: "https://api.openai.com/v1"},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream:       config.UpstreamConfig{BaseURL: "https://openrouter.ai/api/v1"},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("selected target = %q, want primary", selection.Target.ID)
	}
	if selection.Decision.StickyStatus != "" {
		t.Fatalf("StickyStatus = %q, want empty", selection.Decision.StickyStatus)
	}
}

func TestRouterImplicitDefaultRouteTargetPreservesTargetID(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ApiKey:         "sk-primary-secret",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	targets := rtr.Targets()
	if len(targets) != 1 {
		t.Fatalf("len(targets) = %d, want 1", len(targets))
	}
	target := targets[0]
	if target.ID != "primary" || target.RouteTargetID != "primary:default" || target.ChannelID != "primary" || target.CredentialID != "default" {
		t.Fatalf("target identity = %+v, want old id with implicit default credential", target.snapshot())
	}

	selection, err := rtr.Select(stickyRequest(t, "implicit-default"))
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("selected target = %q, want primary", selection.Target.ID)
	}
	if selection.Decision.SelectedID != "primary" || selection.Decision.SelectedRouteTargetID != "primary:default" || selection.Decision.SelectedChannelID != "primary" || selection.Decision.SelectedCredentialID != "default" {
		t.Fatalf("decision selection = %+v, want implicit default identity", selection.Decision)
	}
	if len(selection.Decision.Candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(selection.Decision.Candidates))
	}
	candidate := selection.Decision.Candidates[0]
	if candidate.ID != "primary" || candidate.RouteTargetID != "primary:default" || candidate.ChannelID != "primary" || candidate.CredentialID != "default" {
		t.Fatalf("candidate = %+v, want implicit default identity", candidate)
	}
}

func TestRouterExpandsExplicitCredentialsIntoRouteTargets(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "openai-primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ApiKey:         "sk-inline-ignored",
					ProviderPreset: "openai",
				},
				Credentials: []config.CredentialConfig{
					{ID: "personal", Name: "personal key", ApiKey: "sk-personal-secret"},
					{ID: "backup", Name: "backup key", ApiKey: "sk-backup-secret"},
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	targets := rtr.Targets()
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}
	gotIDs := []string{targets[0].ID, targets[1].ID}
	wantIDs := []string{"openai-primary:backup", "openai-primary:personal"}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("target IDs = %#v, want %#v", gotIDs, wantIDs)
	}
	for _, target := range targets {
		if target.ChannelID != "openai-primary" {
			t.Fatalf("target %q channel = %q, want openai-primary", target.ID, target.ChannelID)
		}
		if target.RouteTargetID != target.ID {
			t.Fatalf("target %q RouteTargetID = %q, want same as ID", target.ID, target.RouteTargetID)
		}
	}

	selection, err := rtr.Select(stickyRequest(t, "explicit-credentials"))
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "openai-primary:backup" {
		t.Fatalf("selected target = %q, want deterministic first route target", selection.Target.ID)
	}
	if selection.Decision.SelectedRouteTargetID != "openai-primary:backup" || selection.Decision.SelectedChannelID != "openai-primary" || selection.Decision.SelectedCredentialID != "backup" {
		t.Fatalf("decision selection = %+v, want backup route target fields", selection.Decision)
	}
	if len(selection.Decision.Candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2", len(selection.Decision.Candidates))
	}
	for _, candidate := range selection.Decision.Candidates {
		if candidate.ChannelID != "openai-primary" || candidate.RouteTargetID == "" || candidate.CredentialID == "" {
			t.Fatalf("candidate missing credential route fields: %+v", candidate)
		}
	}
}

func TestRouterStickyBindsConcreteCredentialRouteTarget(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "openai-primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
				Credentials: []config.CredentialConfig{
					{ID: "a", ApiKey: "sk-a-secret"},
					{ID: "b", ApiKey: "sk-b-secret"},
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	first, err := rtr.SelectWithBody(stickyRequest(t, "credential-sticky"), body)
	if err != nil {
		t.Fatalf("Select(first) error = %v", err)
	}
	if first.Target.ID != "openai-primary:a" || first.Decision.StickyStatus != "bind" {
		t.Fatalf("first selection target/status = %q/%q, want openai-primary:a bind", first.Target.ID, first.Decision.StickyStatus)
	}
	if first.Decision.StickyTargetID != "openai-primary:a" || first.Decision.SelectedRouteTargetID != "openai-primary:a" {
		t.Fatalf("first decision = %+v, want concrete route target binding", first.Decision)
	}
	rtr.Release(first)

	second, err := rtr.SelectWithBody(stickyRequest(t, "credential-sticky"), body)
	if err != nil {
		t.Fatalf("Select(second) error = %v", err)
	}
	if second.Target.ID != "openai-primary:a" || second.Decision.StickyStatus != "hit" {
		t.Fatalf("second selection target/status = %q/%q, want openai-primary:a hit", second.Target.ID, second.Decision.StickyStatus)
	}
	if !hasStickyRouteDecision(second.Decision, "hit", "openai-primary:a", "") {
		t.Fatalf("sticky events = %+v, want hit for concrete route target", second.Decision.StickyEvents)
	}
	rtr.Release(second)

	held, err := rtr.SelectWithExclusion(stickyRequest(t, "credential-sticky"), body, []string{"openai-primary:a"})
	if err != nil {
		t.Fatalf("SelectWithExclusion() error = %v", err)
	}
	if held.Target.ID != "openai-primary:b" {
		t.Fatalf("excluded selection = %q, want openai-primary:b", held.Target.ID)
	}
	if held.Decision.StickyStatus != "hold" {
		t.Fatalf("sticky status = %q, want hold", held.Decision.StickyStatus)
	}
	if !hasStickyRouteDecision(held.Decision, "hold", "", "openai-primary:a") {
		t.Fatalf("sticky events = %+v, want hold for openai-primary:a", held.Decision.StickyEvents)
	}
	rtr.Release(held)

	// The binding must still name the concrete route target the failed attempt
	// excluded, so the next request returns to that credential rather than
	// silently moving the conversation to the backup one.
	third, err := rtr.SelectWithBody(stickyRequest(t, "credential-sticky"), body)
	if err != nil {
		t.Fatalf("Select(third) error = %v", err)
	}
	if third.Target.ID != "openai-primary:a" || third.Decision.StickyStatus != "hit" {
		t.Fatalf("third selection target/status = %q/%q, want openai-primary:a hit", third.Target.ID, third.Decision.StickyStatus)
	}
}

func stickyRequest(t *testing.T, sessionID string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session_id", sessionID)
	return req
}

func hasStickyDecision(decision *DecisionTrace, status string, breakID string) bool {
	if decision == nil {
		return false
	}
	for _, event := range decision.StickyEvents {
		if event.Status == status && event.BreakID == breakID {
			return true
		}
	}
	return false
}

func hasStickyRouteDecision(decision *DecisionTrace, status string, routeTargetID string, breakID string) bool {
	if decision == nil {
		return false
	}
	for _, event := range decision.StickyEvents {
		if event.Status == status && event.RouteTargetID == routeTargetID && event.BreakID == breakID {
			return true
		}
	}
	return false
}

func TestRouterSelectAllowsModelListRequestsWithoutCatalogMatch(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "openai-primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "anthropic-secondary",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"claude-sonnet-4-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.anthropic.com",
					ProviderPreset: "anthropic",
				},
			},
		},
	}

	// This test checks model-list eligibility, not randomized P2C exploration.
	cfg.Router.Selection.Policy = PolicyFirstAvailable
	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://proxy.local/v1/models", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "openai-primary" {
		t.Fatalf("selected target = %q, want openai-primary", selection.Target.ID)
	}
}

func TestRouterSelectAllowsAnthropicModelListEndpoint(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "anthropic-primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"claude-sonnet-4-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.anthropic.com",
					ProviderPreset: "anthropic",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://proxy.local/v1/models", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "anthropic-primary" {
		t.Fatalf("selected target = %q, want anthropic-primary", selection.Target.ID)
	}
}

func TestRouterDoesNotRouteAnthropicMessagesToOpenAICompatibleTarget(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "openai-primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"glm-5.1", "deepseek-chat"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "anthropic-primary",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"glm-5.1"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.anthropic.com",
					ProviderPreset: "anthropic",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/messages?beta=true", strings.NewReader(`{"model":"glm-5.1","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "anthropic-primary" {
		t.Fatalf("selected target = %q, want anthropic-primary", selection.Target.ID)
	}
}

func TestRouterExtractsModelAndRoutesAnthropicCountTokens(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "openai-primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"glm-5.1"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "anthropic-primary",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"glm-5.1"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.anthropic.com",
					ProviderPreset: "anthropic",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/messages/count_tokens?beta=true", strings.NewReader(`{"model":"glm-5.1","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "anthropic-primary" {
		t.Fatalf("selected target = %q, want anthropic-primary", selection.Target.ID)
	}
	if selection.Decision == nil || selection.Decision.ModelName != "glm-5.1" {
		t.Fatalf("decision model = %#v, want glm-5.1", selection.Decision)
	}
}

func TestRouterExtractsModelAndRoutesOpenAITokenize(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "vllm-primary",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"qwen3.6-35b-a3b"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "http://vllm.local:8000/v1",
					ProviderPreset: "vllm",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/tokenize", strings.NewReader(`{"model":"qwen3.6-35b-a3b","prompt":"hello","add_special_tokens":false}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "vllm-primary" {
		t.Fatalf("selected target = %q, want vllm-primary", selection.Target.ID)
	}
	if selection.Decision == nil || selection.Decision.ModelName != "qwen3.6-35b-a3b" {
		t.Fatalf("decision model = %#v, want qwen3.6-35b-a3b", selection.Decision)
	}
}

func TestRouterAggregatedModelsDeduplicatesAcrossUpstreams(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"glm-5.1", "gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "secondary",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"glm-5.1", "claude-sonnet-4-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.anthropic.com",
					ProviderPreset: "anthropic",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	models := rtr.AggregatedModels()
	want := []string{"claude-sonnet-4-5", "glm-5.1", "gpt-5"}
	if len(models) != len(want) {
		t.Fatalf("len(models) = %d, want %d (%v)", len(models), len(want), models)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models[%d] = %q, want %q (all=%v)", i, models[i], want[i], models)
		}
	}
}

func TestRouterAggregatedModelsPreferConfiguredModelsOverDiscoveredCatalog(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-enabled"},{"id":"gpt-disabled"},{"id":"gpt-upstream-only"}]}`))
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				ModelDiscovery: ModelDiscoveryListModels,
				StaticModels:   []string{"gpt-enabled"},
				Upstream: config.UpstreamConfig{
					BaseURL:        upstreamServer.URL + "/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	models := rtr.AggregatedModels()
	if len(models) != 1 || models[0] != "gpt-enabled" {
		t.Fatalf("AggregatedModels() = %#v, want only configured enabled model", models)
	}
}

func TestRouterAggregatedModelsHidesDiscoveredCatalogForConfiguredModelsOnlyTarget(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-disabled"},{"id":"gpt-upstream-only"}]}`))
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:                   "primary",
				Enabled:              boolPtr(true),
				ModelDiscovery:       ModelDiscoveryListModels,
				ConfiguredModelsOnly: true,
				Upstream: config.UpstreamConfig{
					BaseURL:        upstreamServer.URL + "/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if models := rtr.AggregatedModels(); len(models) != 0 {
		t.Fatalf("AggregatedModels() = %#v, want empty configured model list", models)
	}
}

func TestRouterSnapshotsExposeHealthAndModels(t *testing.T) {
	cfg := &config.Config{
		Router: config.RouterConfig{},
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5", "gpt-4.1"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.FailureThreshold = 1
	cfg.Router.Selection.OpenWindow = 50 * time.Millisecond

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	rtr.Complete(selection, Outcome{Success: false})

	snapshots := rtr.Snapshots()
	if len(snapshots) != 1 {
		t.Fatalf("len(snapshots) = %d, want 1", len(snapshots))
	}
	if snapshots[0].HealthState != HealthOpen {
		t.Fatalf("HealthState = %q, want %q", snapshots[0].HealthState, HealthOpen)
	}
	if len(snapshots[0].Models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(snapshots[0].Models))
	}
	if snapshots[0].LastRefreshStatus == "" {
		t.Fatalf("LastRefreshStatus is empty")
	}
}

func TestRouterClientCancelDoesNotOpenTarget(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.FailureThreshold = 1
	cfg.Router.Selection.OpenWindow = time.Minute

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	rtr.Complete(selection, Outcome{Success: false, ClientCanceled: true, StatusCode: http.StatusBadGateway, DurationMs: 100, Stream: true})

	snapshots := rtr.Snapshots()
	if snapshots[0].HealthState == HealthOpen {
		t.Fatalf("HealthState = %q, want non-open after client cancellation", snapshots[0].HealthState)
	}

	req2, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello again"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req2.Header.Set("Content-Type", "application/json")
	if _, err := rtr.Select(req2); err != nil {
		t.Fatalf("Select() after client cancellation error = %v", err)
	}
}

func TestRouterClientRequestErrorDoesNotOpenTarget(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.FailureThreshold = 1
	cfg.Router.Selection.OpenWindow = time.Minute

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	rtr.Complete(selection, Outcome{Success: false, StatusCode: http.StatusBadRequest, DurationMs: 100, TTFTMs: 30})

	snapshots := rtr.Snapshots()
	if snapshots[0].HealthState == HealthOpen {
		t.Fatalf("HealthState = %q, want non-open after 400 response", snapshots[0].HealthState)
	}
}

func TestRouterCostAwareSelectionPrefersLowerObservedCost(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "fast",
				Enabled:        boolPtr(true),
				Priority:       50,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "slow",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}
	cfg.Router.Selection.Epsilon = 0

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	reqFeatures := RequestFeatures{ModelName: "gpt-5", RequestBytes: 256, EstPromptTokens: 64, MaxTokens: 256}
	var fast *Target
	var slow *Target
	for _, target := range rtr.targets {
		switch target.ID {
		case "fast":
			fast = target
		case "slow":
			slow = target
		}
	}
	if fast == nil || slow == nil {
		t.Fatalf("missing test targets fast=%v slow=%v", fast, slow)
	}
	fast.onFinish(reqFeatures, Outcome{Success: true, StatusCode: 200, DurationMs: 400, TTFTMs: 80}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	slow.onFinish(reqFeatures, Outcome{Success: true, StatusCode: 200, DurationMs: 5000, TTFTMs: 1800}, rtr.costs, rtr.failureThreshold, rtr.openWindow)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "fast" {
		t.Fatalf("selected target = %q, want fast", selection.Target.ID)
	}
}

func TestRouterCostAwareSelectionAvoidsDegradedTarget(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "stable",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "flaky",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}
	cfg.Router.Selection.Epsilon = 0

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	reqFeatures := RequestFeatures{ModelName: "gpt-5", RequestBytes: 256, EstPromptTokens: 64, MaxTokens: 256}
	var stable *Target
	var flaky *Target
	for _, target := range rtr.targets {
		switch target.ID {
		case "stable":
			stable = target
		case "flaky":
			flaky = target
		}
	}
	if stable == nil || flaky == nil {
		t.Fatalf("missing test targets stable=%v flaky=%v", stable, flaky)
	}
	stable.onFinish(reqFeatures, Outcome{Success: true, StatusCode: 200, DurationMs: 700, TTFTMs: 120}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	for i := 0; i < 3; i++ {
		flaky.onFinish(reqFeatures, Outcome{Success: false, StatusCode: 0, DurationMs: 1200, TTFTMs: 0}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selection.Target.ID != "stable" {
		t.Fatalf("selected target = %q, want stable", selection.Target.ID)
	}
	snapshots := rtr.Snapshots()
	var flakySnapshot Snapshot
	for _, snapshot := range snapshots {
		if snapshot.ID == "flaky" {
			flakySnapshot = snapshot
		}
	}
	if flakySnapshot.HealthState != HealthOpen && flakySnapshot.HealthState != HealthDegraded {
		t.Fatalf("flaky health = %q, want open/degraded", flakySnapshot.HealthState)
	}
}

func TestRouterSelectReturnsStructuredAllTargetsOpenError(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "fallback",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://openrouter.ai/api/v1",
					ProviderPreset: "openrouter",
				},
			},
		},
	}
	cfg.Router.Selection.FailureThreshold = 1
	cfg.Router.Selection.OpenWindow = time.Minute
	cfg.Router.Selection.Epsilon = 0

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	reqFeatures := RequestFeatures{ModelName: "gpt-5", RequestBytes: 256, EstPromptTokens: 64, MaxTokens: 256}
	for _, target := range rtr.targets {
		target.onFinish(reqFeatures, Outcome{Success: false, StatusCode: 503, DurationMs: 1000, TTFTMs: 0}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	_, err = rtr.Select(req)
	if err == nil {
		t.Fatalf("Select() error = nil, want structured error")
	}
	if SelectionFailureReason(err) != SelectionFailureAllTargetsOpen {
		t.Fatalf("SelectionFailureReason() = %q, want %q", SelectionFailureReason(err), SelectionFailureAllTargetsOpen)
	}
}

func TestRouterSelectWithExclusion(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.primary.example/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "secondary",
				Enabled:        boolPtr(true),
				Priority:       90,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.secondary.example/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "tertiary",
				Enabled:        boolPtr(true),
				Priority:       80,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.tertiary.example/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	// Use first_available so priority ordering is deterministic.
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	body := []byte(`{"model":"gpt-5.5","input":"hello"}`)

	// First selection: should be highest-priority target.
	req1, _ := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	req1.Header.Set("Content-Type", "application/json")
	sel1, err := rtr.SelectWithBody(req1, body)
	if err != nil {
		t.Fatalf("first SelectWithBody() error = %v", err)
	}
	if sel1.Target.ID != "primary" {
		t.Fatalf("first selection = %q, want primary", sel1.Target.ID)
	}
	rtr.Complete(sel1, Outcome{Success: false, StatusCode: 404}) // simulate model-not-found

	// Second selection: exclude primary, should get secondary.
	req2, _ := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	req2.Header.Set("Content-Type", "application/json")
	sel2, err := rtr.SelectWithExclusion(req2, body, []string{"primary"})
	if err != nil {
		t.Fatalf("SelectWithExclusion(exclude primary) error = %v", err)
	}
	if sel2.Target.ID != "secondary" {
		t.Fatalf("second selection after exclusion = %q, want secondary", sel2.Target.ID)
	}

	// Third selection: exclude primary + secondary, should get tertiary.
	req3, _ := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	req3.Header.Set("Content-Type", "application/json")
	sel3, err := rtr.SelectWithExclusion(req3, body, []string{"primary", "secondary"})
	if err != nil {
		t.Fatalf("SelectWithExclusion(exclude primary+secondary) error = %v", err)
	}
	if sel3.Target.ID != "tertiary" {
		t.Fatalf("third selection after exclusion = %q, want tertiary", sel3.Target.ID)
	}

	// Exclude all targets: should fail.
	req4, _ := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	req4.Header.Set("Content-Type", "application/json")
	_, err = rtr.SelectWithExclusion(req4, body, []string{"primary", "secondary", "tertiary"})
	if err == nil {
		t.Fatalf("SelectWithExclusion(all excluded) error = nil, want error")
	}

	// Empty exclusion list should behave like SelectWithBody.
	req5, _ := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", nil)
	req5.Header.Set("Content-Type", "application/json")
	sel5, err := rtr.SelectWithExclusion(req5, body, nil)
	if err != nil {
		t.Fatalf("SelectWithExclusion(nil) error = %v", err)
	}
	if sel5.Target.ID != "primary" {
		t.Fatalf("SelectWithExclusion(nil) = %q, want primary (same as SelectWithBody)", sel5.Target.ID)
	}
}

func TestRouterRefreshNowRebuildsCatalog(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	target := rtr.Targets()[0]
	target.StaticModels = []string{"gpt-5.5"}

	if _, err := rtr.RefreshNow(); err != nil {
		t.Fatalf("RefreshNow() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() after RefreshNow error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("selected target = %q, want primary", selection.Target.ID)
	}
}

func TestRouterRefreshNowRecoversOpenTargetToProbation(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.FailureThreshold = 1
	cfg.Router.Selection.OpenWindow = time.Hour

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	rtr.Complete(selection, Outcome{Success: false, StatusCode: 0})
	if got := rtr.Snapshots()[0].HealthState; got != HealthOpen {
		t.Fatalf("HealthState after failure = %q, want %q", got, HealthOpen)
	}

	if _, err := rtr.RefreshNow(); err != nil {
		t.Fatalf("RefreshNow() error = %v", err)
	}
	if got := rtr.Snapshots()[0].HealthState; got != HealthProbation {
		t.Fatalf("HealthState after successful refresh = %q, want %q", got, HealthProbation)
	}
	if _, err := rtr.Select(req); err != nil {
		t.Fatalf("Select() after successful refresh error = %v", err)
	}
}

func TestRouterProbationTargetAllowsOnlyOneProbe(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.FailureThreshold = 1
	cfg.Router.Selection.OpenWindow = time.Hour

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	first, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	rtr.Complete(first, Outcome{Success: false, StatusCode: 0})
	if _, err := rtr.RefreshNow(); err != nil {
		t.Fatalf("RefreshNow() error = %v", err)
	}

	probe, err := rtr.Select(req)
	if err != nil {
		t.Fatalf("Select() probation probe error = %v", err)
	}

	_, err = rtr.Select(req)
	if err == nil {
		t.Fatalf("Select() concurrent probation request error = nil, want all targets open")
	}
	if SelectionFailureReason(err) != SelectionFailureAllTargetsOpen {
		t.Fatalf("SelectionFailureReason() = %q, want %q", SelectionFailureReason(err), SelectionFailureAllTargetsOpen)
	}

	rtr.Complete(probe, Outcome{Success: true, StatusCode: http.StatusOK})
	if _, err := rtr.Select(req); err != nil {
		t.Fatalf("Select() after successful probation probe error = %v", err)
	}
}

func TestRouterModelScopedFailureDoesNotOpenWholeTarget(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5.5", "gpt-5.1"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.FailureThreshold = 1
	cfg.Router.Selection.OpenWindow = time.Hour

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	failingReq, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5.5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	failingReq.Header.Set("Content-Type", "application/json")
	selection, err := rtr.Select(failingReq)
	if err != nil {
		t.Fatalf("Select(gpt-5.5) error = %v", err)
	}
	rtr.Complete(selection, Outcome{Success: false, StatusCode: http.StatusServiceUnavailable})

	_, err = rtr.Select(failingReq)
	if err == nil {
		t.Fatalf("Select(gpt-5.5) after model failure error = nil, want unavailable")
	}
	if SelectionFailureReason(err) != SelectionFailureAllTargetsOpen {
		t.Fatalf("SelectionFailureReason(gpt-5.5) = %q, want %q", SelectionFailureReason(err), SelectionFailureAllTargetsOpen)
	}

	healthyReq, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5.1","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	healthyReq.Header.Set("Content-Type", "application/json")
	selection, err = rtr.Select(healthyReq)
	if err != nil {
		t.Fatalf("Select(gpt-5.1) after gpt-5.5 model failure error = %v", err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("selected target = %q, want primary", selection.Target.ID)
	}
}

func TestTargetRefreshFailureCanOpenHealthState(t *testing.T) {
	target := &Target{
		ID:          "primary",
		models:      map[string]struct{}{},
		healthState: HealthHealthy,
	}
	costs := defaultCostConfig()

	target.setRefreshResult(nil, "error", errors.New("temporary discovery failure"), 2, time.Minute, costs)
	if got := target.snapshot().HealthState; got != HealthDegraded {
		t.Fatalf("HealthState after first refresh failure = %q, want %q", got, HealthDegraded)
	}

	target.setRefreshResult(nil, "error", errors.New("temporary discovery failure"), 2, time.Minute, costs)
	snapshot := target.snapshot()
	if snapshot.HealthState != HealthOpen {
		t.Fatalf("HealthState after second refresh failure = %q, want %q", snapshot.HealthState, HealthOpen)
	}
	if snapshot.OpenUntil.IsZero() {
		t.Fatalf("OpenUntil is zero after refresh failures opened target")
	}
}

func TestRouterPerModelCapabilitiesDriveEndpointEligibility(t *testing.T) {
	chatDisabled := false
	responsesEnabled := true
	chatEnabled := true
	responsesDisabled := false
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "mixed-surface",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"native-model", "chat-model"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "responses_native",
					Capabilities: config.UpstreamCapabilitiesConfig{
						Responses:       &responsesEnabled,
						ChatCompletions: &chatDisabled,
					},
					ModelCapabilities: map[string]config.UpstreamCapabilitiesConfig{
						"native-model": {Responses: &responsesEnabled, ChatCompletions: &chatDisabled},
						"chat-model":   {Responses: &responsesDisabled, ChatCompletions: &chatEnabled},
					},
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	responsesReq := func(model string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"`+model+`","input":"hello"}`))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		return req
	}
	chatReq := func(model string) *http.Request {
		req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[]}`))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		return req
	}

	tests := []struct {
		name             string
		model            string
		wantNative       bool
		wantNativeTarget bool
	}{
		{name: "responses model", model: "native-model", wantNative: true, wantNativeTarget: true},
		{name: "chat-only model", model: "chat-model", wantNative: false, wantNativeTarget: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"` + tt.model + `","input":"hello"}`)
			if got := rtr.HasSelectableNativeResponsesCandidateWithBody(responsesReq(tt.model), body); got != tt.wantNative {
				t.Fatalf("HasSelectableNativeResponsesCandidateWithBody(%q) = %v, want %v", tt.model, got, tt.wantNative)
			}
			if got := rtr.HasNativeResponsesTargetWithBody(responsesReq(tt.model), body); got != tt.wantNativeTarget {
				t.Fatalf("HasNativeResponsesTargetWithBody(%q) = %v, want %v", tt.model, got, tt.wantNativeTarget)
			}
		})
	}

	// The chat-only model must still be reachable as the local Responses
	// backend, while the responses-only model must not answer chat requests.
	chatBody := []byte(`{"model":"chat-model","messages":[]}`)
	if !rtr.HasSelectableCandidateWithBody(chatReq("chat-model"), chatBody) {
		t.Fatalf("chat-model should be selectable for /v1/chat/completions")
	}
	nativeChatBody := []byte(`{"model":"native-model","messages":[]}`)
	if rtr.HasSelectableCandidateWithBody(chatReq("native-model"), nativeChatBody) {
		t.Fatalf("native-model should not be selectable for /v1/chat/completions")
	}
	nativeResponsesBody := []byte(`{"model":"native-model","input":"hello"}`)
	if !rtr.HasSelectableCandidateWithBody(responsesReq("native-model"), nativeResponsesBody) {
		t.Fatalf("native-model should be selectable for /v1/responses")
	}
	chatAsBackendBody := []byte(`{"model":"chat-model","input":"hello"}`)
	if !rtr.HasSelectableCandidateWithBody(responsesReq("chat-model"), chatAsBackendBody) {
		t.Fatalf("chat-model should be eligible as a local Responses backend")
	}
}

// TestRouterEarliestAvailabilityCoversTheModelBreaker pins the per-model
// breaker. A model's own failure EWMA can open while the channel-level state is
// still healthy, and the earlier implementation only inspected the target
// scope: the caller then reported "no target supports this request" instead of
// "retry in Ns", pointing operators at their configuration.
func TestRouterEarliestAvailabilityCoversTheModelBreaker(t *testing.T) {
	chatEnabled := true
	responsesEnabled := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "single-model",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"deepseek-flash"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.example.com/v1",
					ProviderPreset: "openai",
					APIType:        "chat_completions",
					Capabilities: config.UpstreamCapabilitiesConfig{
						ChatCompletions: &chatEnabled,
						Responses:       &responsesEnabled,
					},
				},
			},
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable
	cfg.Router.Selection.FailureThreshold = 3
	cfg.Router.Selection.OpenWindow = 30 * time.Second

	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	body := []byte(`{"model":"deepseek-flash","input":"hello"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	features := RequestFeatures{ModelName: "deepseek-flash"}

	if _, ok := rtr.EarliestAvailabilityFor(req, body); ok {
		t.Fatalf("EarliestAvailabilityFor() reported an outage on a healthy router")
	}

	target := rtr.targets[0]
	costs := defaultCostConfig()
	// One 502 leaves the model degraded but selectable; the second crosses the
	// 0.35 open error-rate threshold while the channel stays healthy.
	for i := 0; i < 2; i++ {
		target.onStart(features)
		target.onFinish(features, Outcome{StatusCode: http.StatusBadGateway, DurationMs: 50}, costs, 3, 30*time.Second)
	}

	if state := target.snapshot().HealthState; state == HealthOpen {
		t.Fatalf("target health = %q, want the channel to stay healthy so this test covers the model scope", state)
	}
	chatReq := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	if rtr.HasSelectableCandidateWithBody(chatReq, body) {
		t.Fatalf("HasSelectableCandidateWithBody() = true, want the open model breaker to filter the candidate")
	}
	if rtr.HasSelectableNativeResponsesCandidateWithBody(req, body) {
		t.Fatalf("HasSelectableNativeResponsesCandidateWithBody() = true, want the open model breaker to filter the candidate")
	}

	availableAt, ok := rtr.EarliestAvailabilityFor(req, body)
	if !ok {
		t.Fatalf("EarliestAvailabilityFor() = false, want the open model breaker to be reported")
	}
	delay := time.Until(availableAt)
	if delay <= 0 || delay > 30*time.Second {
		t.Fatalf("EarliestAvailabilityFor() delay = %v, want a positive delay inside the 30s window", delay)
	}
}

// TestCandidateDecisionDoesNotReportAnElapsedOpenWindowAsOpen pins the
// `routing.candidates` diagnostic invariant that health_state=open means the
// candidate is not selectable. `canSelect` lazily promotes an elapsed open
// window to probation, but `candidateDecision` read the raw field, so the first
// request after a window elapsed recorded `health_state=open` together with
// `selectable=true`, which reads as a contradiction to anyone triaging a
// cassette (and to the live failover test).
func TestCandidateDecisionDoesNotReportAnElapsedOpenWindowAsOpen(t *testing.T) {
	t.Parallel()

	now := time.Now()
	newTarget := func(openUntil time.Time) *Target {
		return &Target{
			ID:            "broken-openai",
			RouteTargetID: "broken-openai:default",
			ChannelID:     "broken-openai",
			models:        map[string]struct{}{"deepseek-flash": {}},
			healthState:   HealthOpen,
			openUntil:     openUntil,
			Upstream: upstream.ResolvedUpstream{
				BaseURL:        "http://127.0.0.1:1/v1",
				ProviderPreset: "openai",
				ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
				APIType:        "chat_completions",
				Mode:           "proxy",
			},
		}
	}
	target := newTarget(now.Add(-time.Second))

	decision := target.candidateDecision("/v1/chat/completions", "deepseek-flash", now, RequestFeatures{ModelName: "deepseek-flash"}, false)
	if decision.FilterReason != "" {
		t.Fatalf("fixture is not routable: filter_reason=%q supports_path=%v supports_model=%v supports_tools=%v",
			decision.FilterReason, decision.SupportsPath, decision.SupportsModel, decision.SupportsTools)
	}
	if decision.HealthState == HealthOpen {
		t.Fatalf("candidate reported health_state=%q with selectable=%v after the window elapsed at %s",
			decision.HealthState, decision.Selectable, target.openUntil)
	}
	if !decision.Selectable {
		t.Fatalf("selectable = false, want true after the window elapsed (health=%q reason=%q)",
			decision.HealthState, decision.FilterReason)
	}

	// The monitor reads the snapshot, so it must not report a channel as open
	// after the window that closed it has elapsed either.
	if got := target.snapshot().HealthState; got == HealthOpen {
		t.Fatalf("snapshot reported health_state=%q for an elapsed window", got)
	}

	// A window that is still live must stay open and unselectable.
	live := newTarget(now.Add(time.Minute))
	liveDecision := live.candidateDecision("/v1/chat/completions", "deepseek-flash", now, RequestFeatures{ModelName: "deepseek-flash"}, false)
	if liveDecision.HealthState != HealthOpen {
		t.Fatalf("live window health_state = %q, want %q", liveDecision.HealthState, HealthOpen)
	}
	if liveDecision.Selectable {
		t.Fatalf("live window selectable = true, want false")
	}
	if liveDecision.FilterReason != "target_open" {
		t.Fatalf("live window filter_reason = %q, want target_open", liveDecision.FilterReason)
	}
}

// stickyAffinityRouter builds two channels that serve the same two models, so a
// test can distinguish "which route target" from "which model".
func stickyAffinityConfig() *config.Config {
	target := func(id string, priority int, baseURL string) config.UpstreamTargetConfig {
		return config.UpstreamTargetConfig{
			ID:             id,
			Enabled:        boolPtr(true),
			Priority:       priority,
			ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5", "gpt-4o"},
			Upstream:       config.UpstreamConfig{BaseURL: baseURL, ProviderPreset: "openai"},
		}
	}
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			target("primary", 100, "https://api.openai.com/v1"),
			target("fallback", 90, "https://openrouter.ai/api/v1"),
		},
	}
	cfg.Router.Selection.Policy = PolicyFirstAvailable
	// These tests pin which target a session is bound to, so keep the
	// cost-based rebalancing check out of the way; rebalancing has its own test.
	cfg.Router.Selection.StickyMaxCostRatio = float64Ptr(0)
	return cfg
}

func stickyAffinityRouter(t *testing.T) *Router {
	t.Helper()
	return routerFromConfig(t, stickyAffinityConfig())
}

func routerFromConfig(t *testing.T, cfg *config.Config) *Router {
	t.Helper()
	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return rtr
}

func stickyTargetsByID(rtr *Router) map[string]*Target {
	targets := rtr.Targets()
	byID := make(map[string]*Target, len(targets))
	for _, target := range targets {
		byID[target.ID] = target
	}
	return byID
}

// TestRouterStickySpillsWhenBoundTargetBusy pins the concurrency half: while
// the session's bound target is executing a request, a concurrent sibling is
// scheduled by cost instead of queueing behind it, and the binding survives so
// the next sequential request returns to it.
func TestRouterStickySpillsWhenBoundTargetBusy(t *testing.T) {
	rtr := stickyAffinityRouter(t)

	req, body := stickyModelRequest(t, "session-busy", "gpt-5")
	first, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(first) error = %v", err)
	}
	if first.Target.ID != "primary" || first.Decision.StickyStatus != "bind" {
		t.Fatalf("first selection = %q status %q, want primary bind", first.Target.ID, first.Decision.StickyStatus)
	}
	// Leave the first request in flight: the sibling below arrives while the
	// bound target is still executing it.

	req, body = stickyModelRequest(t, "session-busy", "gpt-5")
	spilled, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(spill) error = %v", err)
	}
	if spilled.Target.ID != "fallback" || spilled.Decision.StickyStatus != "spill" {
		t.Fatalf("spill selection = %q status %q, want fallback spill", spilled.Target.ID, spilled.Decision.StickyStatus)
	}
	if !hasStickyDecision(spilled.Decision, "spill", "primary") {
		t.Fatalf("sticky events = %+v, want spill from primary", spilled.Decision.StickyEvents)
	}

	// The binding is kept: once the in-flight request finishes, the next
	// sequential request returns to the preferred target.
	rtr.Release(first)
	rtr.Release(spilled)
	req, body = stickyModelRequest(t, "session-busy", "gpt-5")
	back, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(back) error = %v", err)
	}
	if back.Target.ID != "primary" || back.Decision.StickyStatus != "hit" {
		t.Fatalf("sequential selection = %q status %q, want primary hit", back.Target.ID, back.Decision.StickyStatus)
	}
}

// TestRouterStickyRebalancesToMuchCheaperTarget covers the heterogeneous
// hardware case: a session first bound to the slow target must move once the
// other target has been measured to be several times cheaper, instead of
// staying pinned for the whole TTL.
func TestRouterStickyRebalancesToMuchCheaperTarget(t *testing.T) {
	cfg := stickyAffinityConfig()
	cfg.Router.Selection.StickyMaxCostRatio = float64Ptr(2)
	rtr := routerFromConfig(t, cfg)

	req, body := stickyModelRequest(t, "session-rebalance", "gpt-5")
	first, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(first) error = %v", err)
	}
	if first.Target.ID != "primary" || first.Decision.StickyStatus != "bind" {
		t.Fatalf("first selection target/status = %q/%q, want primary bind", first.Target.ID, first.Decision.StickyStatus)
	}

	targets := stickyTargetsByID(rtr)
	targets["primary"].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 5000, TTFTMs: 1800}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	targets["fallback"].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 400, TTFTMs: 80}, rtr.costs, rtr.failureThreshold, rtr.openWindow)

	req, body = stickyModelRequest(t, "session-rebalance", "gpt-5")
	rebalanced, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(rebalance) error = %v", err)
	}
	if rebalanced.Target.ID != "fallback" {
		t.Fatalf("rebalanced target = %q, want fallback", rebalanced.Target.ID)
	}
	if rebalanced.Decision.StickyStatus != "rebalance" {
		t.Fatalf("sticky status = %q, want rebalance", rebalanced.Decision.StickyStatus)
	}
	if !hasStickyRouteDecision(rebalanced.Decision, "rebalance", "fallback:default", "primary") {
		t.Fatalf("sticky events = %+v, want rebalance from primary to fallback", rebalanced.Decision.StickyEvents)
	}

	req, body = stickyModelRequest(t, "session-rebalance", "gpt-5")
	after, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(after) error = %v", err)
	}
	if after.Target.ID != "fallback" || after.Decision.StickyStatus != "hit" {
		t.Fatalf("after rebalance target/status = %q/%q, want fallback hit", after.Target.ID, after.Decision.StickyStatus)
	}
}

// TestRouterStickyKeepsBindingWithinCostRatio pins the hysteresis: equal-cost
// targets must keep the binding, and a merely noisy measurement must not move
// the session and lose its prompt cache.
func TestRouterStickyKeepsBindingWithinCostRatio(t *testing.T) {
	cfg := stickyAffinityConfig()
	cfg.Router.Selection.StickyMaxCostRatio = float64Ptr(2)
	rtr := routerFromConfig(t, cfg)

	req, body := stickyModelRequest(t, "session-within-ratio", "gpt-5")
	if _, err := rtr.SelectWithBody(req, body); err != nil {
		t.Fatalf("SelectWithBody(first) error = %v", err)
	}

	targets := stickyTargetsByID(rtr)
	targets["primary"].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 450, TTFTMs: 90}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	targets["fallback"].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 400, TTFTMs: 80}, rtr.costs, rtr.failureThreshold, rtr.openWindow)

	req, body = stickyModelRequest(t, "session-within-ratio", "gpt-5")
	selection, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(second) error = %v", err)
	}
	if selection.Target.ID != "primary" || selection.Decision.StickyStatus != "hit" {
		t.Fatalf("selection target/status = %q/%q, want primary hit", selection.Target.ID, selection.Decision.StickyStatus)
	}
}

// TestRouterStickyRebalanceCanBeDisabled documents the opt-out: a ratio of 0
// keeps the historical "binding always wins" behaviour.
func TestRouterStickyRebalanceCanBeDisabled(t *testing.T) {
	cfg := stickyAffinityConfig()
	cfg.Router.Selection.StickyMaxCostRatio = float64Ptr(0)
	rtr := routerFromConfig(t, cfg)

	req, body := stickyModelRequest(t, "session-no-rebalance", "gpt-5")
	if _, err := rtr.SelectWithBody(req, body); err != nil {
		t.Fatalf("SelectWithBody(first) error = %v", err)
	}

	targets := stickyTargetsByID(rtr)
	targets["primary"].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 5000, TTFTMs: 1800}, rtr.costs, rtr.failureThreshold, rtr.openWindow)
	targets["fallback"].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 400, TTFTMs: 80}, rtr.costs, rtr.failureThreshold, rtr.openWindow)

	req, body = stickyModelRequest(t, "session-no-rebalance", "gpt-5")
	selection, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(second) error = %v", err)
	}
	if selection.Target.ID != "primary" || selection.Decision.StickyStatus != "hit" {
		t.Fatalf("selection target/status = %q/%q, want primary hit", selection.Target.ID, selection.Decision.StickyStatus)
	}
}

// TestRouterStickySelectionConfig covers the three settings: the one-hour TTL
// and 2.0 ratio defaults, an explicit TTL/ratio, and the binding opt-out.
func TestRouterStickySelectionConfig(t *testing.T) {
	cfg := stickyAffinityConfig()
	cfg.Router.Selection.StickyMaxCostRatio = nil
	rtr := routerFromConfig(t, cfg)
	if rtr.sticky == nil {
		t.Fatal("sticky store = nil, want enabled by default")
	}
	if rtr.sticky.ttl != defaultStickyBindingTTL {
		t.Fatalf("sticky ttl = %v, want %v", rtr.sticky.ttl, defaultStickyBindingTTL)
	}
	if rtr.stickyMaxCostRatio != defaultStickyMaxCostRatio {
		t.Fatalf("sticky max cost ratio = %v, want %v", rtr.stickyMaxCostRatio, defaultStickyMaxCostRatio)
	}

	cfg = stickyAffinityConfig()
	ttl := 5 * time.Minute
	ratio := 1.5
	cfg.Router.Selection.StickyTTL = ttl
	cfg.Router.Selection.StickyMaxCostRatio = &ratio
	rtr = routerFromConfig(t, cfg)
	if rtr.sticky == nil || rtr.sticky.ttl != ttl {
		t.Fatalf("sticky store = %+v, want ttl %v", rtr.sticky, ttl)
	}
	if rtr.stickyMaxCostRatio != ratio {
		t.Fatalf("sticky max cost ratio = %v, want %v", rtr.stickyMaxCostRatio, ratio)
	}

	cfg = stickyAffinityConfig()
	disabled := false
	cfg.Router.Selection.Sticky = &disabled
	rtr = routerFromConfig(t, cfg)
	if rtr.sticky != nil {
		t.Fatal("sticky store != nil, want nil when router.selection.sticky is false")
	}
	req, body := stickyModelRequest(t, "session-disabled", "gpt-5")
	selection, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(disabled) error = %v", err)
	}
	if selection.Decision.StickyStatus != "" || len(selection.Decision.StickyEvents) != 0 {
		t.Fatalf("sticky decision = %+v, want none when affinity is disabled", selection.Decision)
	}
}

func stickyModelRequest(t *testing.T, session string, model string) (*http.Request, []byte) {
	t.Helper()
	body := []byte(`{"model":"` + model + `","input":"hello"}`)
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if session != "" {
		req.Header.Set("Session_id", session)
	}
	return req, body
}

// TestRouterStickyBindingIsPerSessionAndModel pins the key composition: one
// session keeps one target per model. A request for a second model must not
// rewrite the first model's binding, because that would move the conversation
// away from the upstream holding its prompt cache.
func TestRouterStickyBindingIsPerSessionAndModel(t *testing.T) {
	rtr := stickyAffinityRouter(t)

	req, body := stickyModelRequest(t, "session-a", "gpt-5")
	first, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(gpt-5 first) error = %v", err)
	}
	if first.Target.ID != "primary" || first.Decision.StickyStatus != "bind" {
		t.Fatalf("first gpt-5 selection = %q status %q, want primary bind", first.Target.ID, first.Decision.StickyStatus)
	}
	if first.Decision.StickyKeyFingerprint == "" {
		t.Fatal("the decision trace is missing the sticky key fingerprint")
	}

	// Make the fallback much more attractive: only the sticky binding can keep
	// the second request on the primary target now.
	targets := rtr.Targets()
	targets[0].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 5000, TTFTMs: 1800}, rtr.costs, 3, time.Minute)
	targets[1].onFinish(RequestFeatures{ModelName: "gpt-5"}, Outcome{Success: true, StatusCode: 200, DurationMs: 400, TTFTMs: 80}, rtr.costs, 3, time.Minute)

	req, body = stickyModelRequest(t, "session-a", "gpt-5")
	second, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(gpt-5 second) error = %v", err)
	}
	if second.Target.ID != "primary" || second.Decision.StickyStatus != "hit" {
		t.Fatalf("second gpt-5 selection = %q status %q, want sticky primary hit", second.Target.ID, second.Decision.StickyStatus)
	}
	rtr.Release(second)

	// A different model gets its own binding. Establish it on the fallback by
	// excluding primary, then prove it survives independently.
	chatModelReq, chatBody := stickyModelRequest(t, "session-a", "gpt-4o")
	chatBound, err := rtr.SelectWithExclusion(chatModelReq, chatBody, []string{"primary"})
	if err != nil {
		t.Fatalf("SelectWithExclusion(gpt-4o) error = %v", err)
	}
	if chatBound.Target.ID != "fallback" || chatBound.Decision.StickyStatus != "bind" {
		t.Fatalf("gpt-4o binding = %q status %q, want fallback bind (a new model must not hit the gpt-5 binding)", chatBound.Target.ID, chatBound.Decision.StickyStatus)
	}
	rtr.Release(chatBound)
	if chatBound.Decision.StickyKeyFingerprint == first.Decision.StickyKeyFingerprint {
		t.Fatal("gpt-5 and gpt-4o share a sticky key fingerprint, so their bindings collide")
	}

	chatReq, chatBody := stickyModelRequest(t, "session-a", "gpt-4o")
	chatHit, err := rtr.SelectWithBody(chatReq, chatBody)
	if err != nil {
		t.Fatalf("SelectWithBody(gpt-4o second) error = %v", err)
	}
	if chatHit.Target.ID != "fallback" || chatHit.Decision.StickyStatus != "hit" {
		t.Fatalf("second gpt-4o selection = %q status %q, want sticky fallback hit", chatHit.Target.ID, chatHit.Decision.StickyStatus)
	}
	rtr.Release(chatHit)

	// The gpt-4o binding must not have moved the gpt-5 conversation.
	req, body = stickyModelRequest(t, "session-a", "gpt-5")
	again, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody(gpt-5 third) error = %v", err)
	}
	if again.Target.ID != "primary" || again.Decision.StickyStatus != "hit" {
		t.Fatalf("third gpt-5 selection = %q status %q, want the original primary binding to survive", again.Target.ID, again.Decision.StickyStatus)
	}
}

// TestRouterStickySessionScopesToOneSessionAndOneTarget pins the conversation
// half: the same model on two channels keeps one session on one route target,
// while another session is not bound by it and can still use the other target.
func TestRouterStickySessionScopesToOneSessionAndOneTarget(t *testing.T) {
	rtr := stickyAffinityRouter(t)

	sessionAReq, sessionABody := stickyModelRequest(t, "session-a", "gpt-5")
	first, err := rtr.SelectWithBody(sessionAReq, sessionABody)
	if err != nil {
		t.Fatalf("SelectWithBody(session-a first) error = %v", err)
	}
	if first.Target.ID != "primary" || first.Decision.StickyStatus != "bind" {
		t.Fatalf("session-a first = %q status %q, want primary bind", first.Target.ID, first.Decision.StickyStatus)
	}
	rtr.Release(first)

	// The same session keeps the same target across selections.
	for i := 0; i < 2; i++ {
		req, body := stickyModelRequest(t, "session-a", "gpt-5")
		selection, err := rtr.SelectWithBody(req, body)
		if err != nil {
			t.Fatalf("SelectWithBody(session-a repeat %d) error = %v", i, err)
		}
		if selection.Target.ID != "primary" || selection.Decision.StickyStatus != "hit" {
			t.Fatalf("session-a repeat %d = %q status %q, want sticky primary hit", i, selection.Target.ID, selection.Decision.StickyStatus)
		}
		rtr.Release(selection)
	}

	// A different session has no binding and is free to land elsewhere.
	sessionBReq, sessionBBody := stickyModelRequest(t, "session-b", "gpt-5")
	distributed, err := rtr.SelectWithExclusion(sessionBReq, sessionBBody, []string{"primary"})
	if err != nil {
		t.Fatalf("SelectWithExclusion(session-b) error = %v", err)
	}
	if distributed.Target.ID != "fallback" || distributed.Decision.StickyStatus != "bind" {
		t.Fatalf("session-b first = %q status %q, want its own fallback bind", distributed.Target.ID, distributed.Decision.StickyStatus)
	}
	rtr.Release(distributed)

	// Each session now hits its own binding, proving the two are independent.
	sessionBReq, sessionBBody = stickyModelRequest(t, "session-b", "gpt-5")
	hitB, err := rtr.SelectWithBody(sessionBReq, sessionBBody)
	if err != nil {
		t.Fatalf("SelectWithBody(session-b repeat) error = %v", err)
	}
	if hitB.Target.ID != "fallback" || hitB.Decision.StickyStatus != "hit" {
		t.Fatalf("session-b repeat = %q status %q, want sticky fallback hit", hitB.Target.ID, hitB.Decision.StickyStatus)
	}
	rtr.Release(hitB)

	sessionAReq, sessionABody = stickyModelRequest(t, "session-a", "gpt-5")
	hitA, err := rtr.SelectWithBody(sessionAReq, sessionABody)
	if err != nil {
		t.Fatalf("SelectWithBody(session-a after session-b) error = %v", err)
	}
	if hitA.Target.ID != "primary" || hitA.Decision.StickyStatus != "hit" {
		t.Fatalf("session-a after session-b = %q status %q, want its primary binding untouched", hitA.Target.ID, hitA.Decision.StickyStatus)
	}
}

// TestRouterStickyBodyFallbacksCreateAffinity pins that the body-only session
// sources actually feed the router's binding, not just the extractor.
func TestRouterStickyBodyFallbacksCreateAffinity(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "prompt_cache_key", body: `{"model":"gpt-5","prompt_cache_key":"cache-key-1"}`},
		{name: "metadata session id", body: `{"model":"gpt-5","metadata":{"session_id":"meta-session-1"}}`},
		{name: "conversation_id", body: `{"model":"gpt-5","conversation_id":"conversation-1"}`},
		{name: "session_id", body: `{"model":"gpt-5","session_id":"session-body-1"}`},
		{name: "previous_response_id", body: `{"model":"gpt-5","previous_response_id":"resp-1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rtr := stickyAffinityRouter(t)
			body := []byte(tt.body)
			req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("http.NewRequest() error = %v", err)
			}
			first, err := rtr.SelectWithBody(req, body)
			if err != nil {
				t.Fatalf("SelectWithBody(first) error = %v", err)
			}
			if first.Decision.StickyStatus != "bind" {
				t.Fatalf("first sticky status = %q, want bind from the body session source", first.Decision.StickyStatus)
			}
			rtr.Release(first)
			req, err = http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("http.NewRequest() error = %v", err)
			}
			second, err := rtr.SelectWithBody(req, body)
			if err != nil {
				t.Fatalf("SelectWithBody(second) error = %v", err)
			}
			if second.Target.ID != first.Target.ID || second.Decision.StickyStatus != "hit" {
				t.Fatalf("second = %q status %q, want a sticky hit on the first target %q", second.Target.ID, second.Decision.StickyStatus, first.Target.ID)
			}
		})
	}
}

// TestRouterStickyXSessionHeaderCreatesAffinity pins the header alias the OpenAI
// SDKs commonly send.
func TestRouterStickyXSessionHeaderCreatesAffinity(t *testing.T) {
	rtr := stickyAffinityRouter(t)
	request := func() *http.Request {
		body := []byte(`{"model":"gpt-5","input":"hello"}`)
		req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		req.Header.Set("X-Session-Id", "x-session-1")
		return req
	}
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	first, err := rtr.SelectWithBody(request(), body)
	if err != nil {
		t.Fatalf("SelectWithBody(first) error = %v", err)
	}
	rtr.Release(first)
	second, err := rtr.SelectWithBody(request(), body)
	if err != nil {
		t.Fatalf("SelectWithBody(second) error = %v", err)
	}
	if first.Decision.StickyStatus != "bind" || second.Decision.StickyStatus != "hit" || second.Target.ID != first.Target.ID {
		t.Fatalf("X-Session-Id affinity = first %q/%q second %q/%q, want bind then hit on the same target",
			first.Target.ID, first.Decision.StickyStatus, second.Target.ID, second.Decision.StickyStatus)
	}
}

// TestRouterSelectWithoutSessionStillHasNoBinding pins that a request without any
// session source is routed normally and records no sticky event.
func TestRouterSelectWithoutSessionStillHasNoBinding(t *testing.T) {
	rtr := stickyAffinityRouter(t)
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	request := func() *http.Request {
		req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		return req
	}
	for i := 0; i < 2; i++ {
		selection, err := rtr.SelectWithBody(request(), body)
		if err != nil {
			t.Fatalf("SelectWithBody(%d) error = %v", i, err)
		}
		if selection.Decision.StickyStatus != "" || len(selection.Decision.StickyEvents) != 0 {
			t.Fatalf("select %d sticky status = %q events %+v, want no binding without a session source", i, selection.Decision.StickyStatus, selection.Decision.StickyEvents)
		}
	}
	rtr.sticky.mu.Lock()
	size := len(rtr.sticky.bindings)
	rtr.sticky.mu.Unlock()
	if size != 0 {
		t.Fatalf("bindings = %d, want 0 without a session source", size)
	}
}
