package router

import (
	"net/http"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

func priorityModeConfig(mode string) *config.Config {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "primary",
				Enabled:        boolPtr(true),
				Priority:       100,
				Weight:         1,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
			{
				ID:             "backup",
				Enabled:        boolPtr(true),
				Priority:       10,
				Weight:         20,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"gpt-5"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.Epsilon = float64Ptr(0)
	cfg.Router.Selection.PriorityMode = mode
	return cfg
}

func priorityModeRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", strings.NewReader(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func priorityModeRouter(t *testing.T, cfg *config.Config) *Router {
	t.Helper()
	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return rtr
}

func TestPriorityModeCompatKeepsCostFirst(t *testing.T) {
	rtr := priorityModeRouter(t, priorityModeConfig(""))
	selection, err := rtr.Select(priorityModeRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if selection.Target.ID != "backup" {
		t.Fatalf("compat selected %q, want backup (lower cost wins)", selection.Target.ID)
	}
	if selection.Decision.PriorityMode != "compat" {
		t.Fatalf("PriorityMode = %q, want compat", selection.Decision.PriorityMode)
	}
}

func TestPriorityModeStrictSelectsHighestTier(t *testing.T) {
	rtr := priorityModeRouter(t, priorityModeConfig("strict"))
	selection, err := rtr.Select(priorityModeRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("strict selected %q, want primary", selection.Target.ID)
	}
	if selection.Decision.PriorityMode != "strict" {
		t.Fatalf("PriorityMode = %q, want strict", selection.Decision.PriorityMode)
	}
	if selection.Decision.SelectedTier != 100 {
		t.Fatalf("SelectedTier = %d, want 100", selection.Decision.SelectedTier)
	}
}

func TestPriorityModeStrictFailsOverToNextTier(t *testing.T) {
	rtr := priorityModeRouter(t, priorityModeConfig("strict"))
	var primaryID string
	for _, target := range rtr.Targets() {
		if target.ID == "primary" {
			primaryID = target.ID
		}
	}
	if primaryID == "" {
		t.Fatal("primary target not found")
	}
	req := priorityModeRequest(t)
	selection, err := rtr.SelectWithExclusion(req, []byte(`{"model":"gpt-5","input":"hello"}`), []string{primaryID})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Target.ID != "backup" {
		t.Fatalf("strict failover selected %q, want backup", selection.Target.ID)
	}
}

func TestNativeBeatsHigherPriorityConversion(t *testing.T) {
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{
			{
				ID:             "converter",
				Enabled:        boolPtr(true),
				Priority:       100,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"m"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.anthropic.com",
					ProviderPreset: "anthropic",
				},
			},
			{
				ID:             "native",
				Enabled:        boolPtr(true),
				Priority:       10,
				ModelDiscovery: ModelDiscoveryStaticOnly,
				StaticModels:   []string{"m"},
				Upstream: config.UpstreamConfig{
					BaseURL:        "https://api.openai.com/v1",
					ProviderPreset: "openai",
				},
			},
		},
	}
	cfg.Router.Selection.Epsilon = float64Ptr(0)
	cfg.Router.Selection.PriorityMode = "strict"
	cfg.Router.Selection.ProtocolConversion = true
	rtr := priorityModeRouter(t, cfg)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	selection, err := rtr.Select(req)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Target.ID != "native" {
		t.Fatalf("selected %q, want native (native must beat a higher-priority conversion)", selection.Target.ID)
	}
}

func TestSetPriorityModeAtRuntime(t *testing.T) {
	rtr := priorityModeRouter(t, priorityModeConfig(""))
	if got := rtr.PriorityMode(); got != "compat" {
		t.Fatalf("initial PriorityMode = %q, want compat", got)
	}
	if err := rtr.SetPriorityMode("strict"); err != nil {
		t.Fatal(err)
	}
	if got := rtr.PriorityMode(); got != "strict" {
		t.Fatalf("PriorityMode = %q, want strict", got)
	}
	selection, err := rtr.Select(priorityModeRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if selection.Target.ID != "primary" {
		t.Fatalf("selected %q, want primary after SetPriorityMode(strict)", selection.Target.ID)
	}
	if err := rtr.SetPriorityMode("tiered"); err == nil {
		t.Fatal("SetPriorityMode accepted an unknown mode")
	}
	if err := rtr.SetPriorityMode("compat"); err != nil {
		t.Fatal(err)
	}
	if got := rtr.PriorityMode(); got != "compat" {
		t.Fatalf("PriorityMode = %q, want compat", got)
	}
}

func TestNewHonorsExplicitZeroEpsilon(t *testing.T) {
	rtr := priorityModeRouter(t, priorityModeConfig(""))
	if rtr.costs.Epsilon != 0 {
		t.Fatalf("costs.Epsilon = %v, want explicit zero", rtr.costs.Epsilon)
	}
}

func TestExplainExposesPriorityModeAndSelectedTier(t *testing.T) {
	rtr := priorityModeRouter(t, priorityModeConfig("strict"))
	result := rtr.Explain(ExplainRequest{
		RawPath: "/v1/chat/completions",
		Body:    []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
	})
	if result.Error != "" {
		t.Fatalf("Explain error = %q", result.Error)
	}
	if result.Result.Plan.PriorityMode != "strict" {
		t.Fatalf("PriorityMode = %q, want strict", result.Result.Plan.PriorityMode)
	}
	if result.Result.Plan.SelectedTier != 100 {
		t.Fatalf("SelectedTier = %d, want 100", result.Result.Plan.SelectedTier)
	}
}
