package upstream

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func mustResolve(t *testing.T, cfg Config) Resolved {
	t.Helper()
	resolved, err := Resolve(cfg)
	if err != nil {
		t.Fatalf("Resolve(%+v): %v", cfg, err)
	}
	return resolved
}

func TestResolvePresetDefaults(t *testing.T) {
	tests := []struct {
		preset    string
		protocols []xir.Protocol
	}{
		{"openai", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses}},
		{"anthropic", []xir.Protocol{xir.ProtocolMessages}},
		{"deepseek", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}},
		{"openrouter", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}},
		{"google", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolGemini}},
		{"vertex", []xir.Protocol{xir.ProtocolGemini}},
		{"ollama", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses}},
		{"vllm", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses}},
		{"alibaba", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}},
		{"alibaba_intl", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}},
		{"moonshot", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}},
		{"zhipu", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}},
		{"mistral", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses}},
		{"together", []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}},
	}
	for _, test := range tests {
		resolved := mustResolve(t, Config{PresetID: test.preset})
		if resolved.Preset.ID != test.preset {
			t.Errorf("preset %q: Resolved.Preset.ID = %q", test.preset, resolved.Preset.ID)
		}
		if !slices.Equal(resolved.Protocols, test.protocols) {
			t.Errorf("preset %q protocols = %v, want %v", test.preset, resolved.Protocols, test.protocols)
		}
		if len(resolved.Endpoints) == 0 && test.preset != "azure" {
			t.Errorf("preset %q has no resolved endpoints", test.preset)
		}
	}
}

func TestResolvePresetAuthAndModels(t *testing.T) {
	openai := mustResolve(t, Config{PresetID: "openai"})
	if openai.Auth.Mode != AuthModeAPIKey || openai.Auth.Header != "Authorization" || openai.Auth.Scheme != "Bearer" {
		t.Errorf("openai auth = %+v", openai.Auth)
	}
	if openai.Models.ListPath != "/models" || openai.Models.Protocol != xir.ProtocolChatCompletions {
		t.Errorf("openai models = %+v", openai.Models)
	}
	if openai.Preset.Kind != KindVendor {
		t.Errorf("openai kind = %q", openai.Preset.Kind)
	}

	anthropic := mustResolve(t, Config{PresetID: "anthropic"})
	if anthropic.Auth.Header != "x-api-key" || anthropic.Auth.Scheme != "" {
		t.Errorf("anthropic auth = %+v", anthropic.Auth)
	}
	if len(anthropic.Auth.Headers) != 1 || anthropic.Auth.Headers[0].Name != "anthropic-version" {
		t.Errorf("anthropic auth templates = %+v", anthropic.Auth.Headers)
	}

	vertex := mustResolve(t, Config{PresetID: "vertex"})
	if vertex.ModelResource != "publishers/google/models" {
		t.Errorf("vertex ModelResource = %q", vertex.ModelResource)
	}

	ollama := mustResolve(t, Config{PresetID: "ollama"})
	if ollama.Auth.Mode != AuthModeNone {
		t.Errorf("ollama auth mode = %q", ollama.Auth.Mode)
	}
}

func TestResolveOverridesAndPlan(t *testing.T) {
	credential := &Credential{Mode: AuthModeAPIKey, APIKey: "secret", Scopes: []string{"a"}}
	resolved := mustResolve(t, Config{
		PresetID: "zai",
		Plan:     "coding",
		Endpoints: map[xir.Protocol]string{
			xir.ProtocolChatCompletions: "https://gateway.example/v1/",
		},
		Auth:           AuthSpec{Header: "x-api-key", Scheme: ""},
		Headers:        map[string]string{"X-Test": "1"},
		APIVersion:     "2024-10-21",
		Deployment:     "deployment",
		Project:        "project",
		Location:       "us-central1",
		ModelResource:  "publishers/google/models",
		Credential:     credential,
		ModelProtocols: map[string][]xir.Protocol{"model-a": {xir.ProtocolChatCompletions}},
	})

	if resolved.Plan == nil || resolved.Plan.ID != "coding" {
		t.Fatalf("selected plan = %+v", resolved.Plan)
	}
	if got := resolved.Endpoints[xir.ProtocolChatCompletions]; got != "https://gateway.example/v1" {
		t.Errorf("chat endpoint = %q", got)
	}
	if got := resolved.Endpoints[xir.ProtocolResponses]; got != "https://api.z.ai/api/v1" {
		t.Errorf("responses endpoint = %q", got)
	}
	if got := resolved.Endpoints[xir.ProtocolMessages]; got != "https://api.z.ai/api/anthropic" {
		t.Errorf("messages endpoint = %q", got)
	}
	if resolved.Auth.Header != "x-api-key" || resolved.Auth.Scheme != "" {
		t.Errorf("auth override = %+v", resolved.Auth)
	}
	if resolved.Auth.Mode != AuthModeAPIKey {
		t.Errorf("auth mode should inherit the preset, got %q", resolved.Auth.Mode)
	}
	if resolved.Headers["X-Test"] != "1" {
		t.Errorf("headers = %+v", resolved.Headers)
	}
	if resolved.APIVersion != "2024-10-21" || resolved.Deployment != "deployment" || resolved.Project != "project" || resolved.Location != "us-central1" {
		t.Errorf("provider fields not carried: %+v", resolved)
	}
	if resolved.ModelResource != "publishers/google/models" {
		t.Errorf("ModelResource = %q", resolved.ModelResource)
	}
	if resolved.Credential.APIKey != "secret" {
		t.Errorf("credential = %+v", resolved.Credential)
	}
	credential.Scopes[0] = "mutated"
	if resolved.Credential.Scopes[0] != "a" {
		t.Error("Resolved credential aliases the Config credential")
	}
	if got := resolved.Capabilities("model-a"); !got.ChatCompletions || got.Responses || got.Messages || got.Gemini {
		t.Errorf("model override capabilities = %+v", got)
	}
}

func TestResolveCustomUpstream(t *testing.T) {
	resolved := mustResolve(t, Config{
		Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://gateway.example/v1"},
		Auth:      AuthSpec{Mode: AuthModeNone},
	})
	if resolved.Preset.ID != "" || resolved.Preset.Name != "Custom" {
		t.Errorf("custom preset = %+v", resolved.Preset)
	}
	if !slices.Equal(resolved.Protocols, []xir.Protocol{xir.ProtocolChatCompletions}) {
		t.Errorf("custom protocols = %v", resolved.Protocols)
	}
	if resolved.Models.ListPath != "" {
		t.Errorf("custom model source = %+v", resolved.Models)
	}
}

func TestResolveErrors(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		want     error
		contains string
	}{
		{
			name: "unknown preset",
			cfg:  Config{PresetID: "nope"},
			want: ErrUnknownPreset,
		},
		{
			name: "unknown plan",
			cfg:  Config{PresetID: "openai", Plan: "coding"},
			want: ErrUnknownPlan,
		},
		{
			name: "azure has no universal endpoint",
			cfg:  Config{PresetID: "azure"},
			want: ErrNoEndpoints,
		},
		{
			name: "custom upstream requires auth mode",
			cfg:  Config{Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://example.test/v1"}},
			want: ErrInvalidAuthMode,
		},
		{
			name: "invalid auth mode",
			cfg:  Config{PresetID: "openai", Auth: AuthSpec{Mode: "bogus"}},
			want: ErrInvalidAuthMode,
		},
		{
			name: "relative endpoint",
			cfg: Config{
				PresetID:  "openai",
				Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "api.openai.com/v1"},
			},
			want: ErrInvalidEndpoint,
		},
		{
			name: "non-http endpoint",
			cfg: Config{
				PresetID:  "openai",
				Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "ftp://api.openai.com/v1"},
			},
			want: ErrInvalidEndpoint,
		},
		{
			name: "userinfo in endpoint",
			cfg: Config{
				PresetID:  "openai",
				Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://user:pass@example.test/v1"},
			},
			want: ErrInvalidEndpoint,
		},
		{
			name: "protocol without endpoint",
			cfg:  Config{PresetID: "anthropic", Protocols: []xir.Protocol{xir.ProtocolChatCompletions}},
			want: ErrNoEndpoint,
		},
		{
			name: "unknown protocol",
			cfg:  Config{PresetID: "openai", Protocols: []xir.Protocol{xir.Protocol("bogus")}},
			want: ErrUnsupportedProtocol,
		},
		{
			name: "unknown endpoint protocol",
			cfg: Config{
				PresetID:  "openai",
				Endpoints: map[xir.Protocol]string{xir.Protocol("bogus"): "https://example.test/v1"},
			},
			want: ErrUnsupportedProtocol,
		},
		{
			name:     "vertex project without location",
			cfg:      Config{PresetID: "vertex", Project: "project"},
			contains: "project and location",
		},
		{
			name: "model override without endpoint",
			cfg: Config{
				PresetID:       "openai",
				ModelProtocols: map[string][]xir.Protocol{"model": {xir.ProtocolMessages}},
			},
			want: ErrNoEndpoint,
		},
		{
			name: "model override unknown protocol",
			cfg: Config{
				PresetID:       "openai",
				ModelProtocols: map[string][]xir.Protocol{"model": {xir.Protocol("bogus")}},
			},
			want: ErrUnsupportedProtocol,
		},
		{
			name: "model override empty key",
			cfg: Config{
				PresetID:       "openai",
				ModelProtocols: map[string][]xir.Protocol{" ": {xir.ProtocolChatCompletions}},
			},
			contains: "empty model name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Resolve(test.cfg)
			if err == nil {
				t.Fatal("Resolve returned no error")
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Resolve error = %v, want errors.Is(%v)", err, test.want)
			}
			if test.contains != "" && !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Resolve error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

func TestResolveAzureWithEndpoint(t *testing.T) {
	resolved := mustResolve(t, Config{
		PresetID:   "azure",
		Endpoints:  map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://resource.openai.azure.com/openai/v1"},
		APIVersion: "preview",
		Deployment: "my-deployment",
	})
	if resolved.Auth.Header != "api-key" {
		t.Errorf("azure auth header = %q", resolved.Auth.Header)
	}
	if got := resolved.Endpoints[xir.ProtocolChatCompletions]; got != "https://resource.openai.azure.com/openai/v1" {
		t.Errorf("azure endpoint = %q", got)
	}
	if resolved.APIVersion != "preview" || resolved.Deployment != "my-deployment" {
		t.Errorf("azure fields = %q %q", resolved.APIVersion, resolved.Deployment)
	}
	if !reflect.DeepEqual(resolved.Protocols, []xir.Protocol{xir.ProtocolChatCompletions}) {
		t.Errorf("azure protocols = %v", resolved.Protocols)
	}
}
