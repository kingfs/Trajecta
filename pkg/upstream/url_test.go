package upstream

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestBuildURL(t *testing.T) {
	tests := []struct {
		name     string
		cfg      Config
		protocol xir.Protocol
		path     string
		want     string
	}{
		{
			name:     "openai canonical path",
			cfg:      Config{PresetID: "openai"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "openai path without version",
			cfg:      Config{PresetID: "openai"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/chat/completions",
			want:     "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "openai responses",
			cfg:      Config{PresetID: "openai"},
			protocol: xir.ProtocolResponses,
			path:     "/v1/responses",
			want:     "https://api.openai.com/v1/responses",
		},
		{
			name:     "openai model list",
			cfg:      Config{PresetID: "openai"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/models",
			want:     "https://api.openai.com/v1/models",
		},
		{
			name:     "anthropic messages root",
			cfg:      Config{PresetID: "anthropic"},
			protocol: xir.ProtocolMessages,
			path:     "/v1/messages",
			want:     "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "deepseek chat",
			cfg:      Config{PresetID: "deepseek"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://api.deepseek.com/v1/chat/completions",
		},
		{
			name:     "deepseek responses",
			cfg:      Config{PresetID: "deepseek"},
			protocol: xir.ProtocolResponses,
			path:     "/v1/responses",
			want:     "https://api.deepseek.com/v1/responses",
		},
		{
			name:     "deepseek messages",
			cfg:      Config{PresetID: "deepseek"},
			protocol: xir.ProtocolMessages,
			path:     "/v1/messages",
			want:     "https://api.deepseek.com/anthropic/v1/messages",
		},
		{
			name:     "openrouter chat",
			cfg:      Config{PresetID: "openrouter"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://openrouter.ai/api/v1/chat/completions",
		},
		{
			name:     "openrouter messages",
			cfg:      Config{PresetID: "openrouter"},
			protocol: xir.ProtocolMessages,
			path:     "/v1/messages",
			want:     "https://openrouter.ai/api/v1/messages",
		},
		{
			name:     "google gemini model path and query",
			cfg:      Config{PresetID: "google"},
			protocol: xir.ProtocolGemini,
			path:     "/v1beta/models/gemini-2.5-flash:generateContent?alt=sse",
			want:     "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent?alt=sse",
		},
		{
			name:     "google gemini model list",
			cfg:      Config{PresetID: "google"},
			protocol: xir.ProtocolGemini,
			path:     "/models",
			want:     "https://generativelanguage.googleapis.com/v1beta/models",
		},
		{
			name:     "google openai compatibility surface",
			cfg:      Config{PresetID: "google"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
		},
		{
			name:     "zhipu chat uses paas v4 prefix",
			cfg:      Config{PresetID: "zhipu"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://open.bigmodel.cn/api/paas/v4/chat/completions",
		},
		{
			name:     "zhipu messages",
			cfg:      Config{PresetID: "zhipu"},
			protocol: xir.ProtocolMessages,
			path:     "/v1/messages",
			want:     "https://open.bigmodel.cn/api/anthropic/v1/messages",
		},
		{
			name:     "zai coding plan chat",
			cfg:      Config{PresetID: "zai", Plan: "coding"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://api.z.ai/api/coding/paas/v4/chat/completions",
		},
		{
			name:     "fireworks messages",
			cfg:      Config{PresetID: "fireworks"},
			protocol: xir.ProtocolMessages,
			path:     "/v1/messages",
			want:     "https://api.fireworks.ai/inference/v1/messages",
		},
		{
			name:     "vertex express model path",
			cfg:      Config{PresetID: "vertex"},
			protocol: xir.ProtocolGemini,
			path:     "/v1beta/models/gemini-2.5-pro:generateContent",
			want:     "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-pro:generateContent",
		},
		{
			name:     "local http endpoint",
			cfg:      Config{PresetID: "vllm"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "http://localhost:8000/v1/chat/completions",
		},
		{
			name:     "versionless base folds the v1 segment",
			cfg:      Config{PresetID: "github"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://models.github.ai/inference/chat/completions",
		},
		{
			name:     "base query preserved",
			cfg:      Config{PresetID: "openai", Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://example.test/v1?deployment=abc"}},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/models?limit=5",
			want:     "https://example.test/v1/models?deployment=abc&limit=5",
		},
		{
			name:     "client query overrides base query",
			cfg:      Config{PresetID: "openai", Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://example.test/v1?limit=5"}},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/models?limit=10",
			want:     "https://example.test/v1/models?limit=10",
		},
		{
			name:     "azure api version",
			cfg:      Config{PresetID: "azure", Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://resource.openai.azure.com/openai/v1"}, APIVersion: "preview"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions",
			want:     "https://resource.openai.azure.com/openai/v1/chat/completions?api-version=preview",
		},
		{
			name:     "azure client api version wins",
			cfg:      Config{PresetID: "azure", Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://resource.openai.azure.com/openai/v1"}, APIVersion: "preview"},
			protocol: xir.ProtocolChatCompletions,
			path:     "/v1/chat/completions?api-version=2024-10-21",
			want:     "https://resource.openai.azure.com/openai/v1/chat/completions?api-version=2024-10-21",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved := mustResolve(t, test.cfg)
			got, err := resolved.BuildURL(test.protocol, test.path)
			if err != nil {
				t.Fatalf("BuildURL: %v", err)
			}
			if got != test.want {
				t.Fatalf("BuildURL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildURLRootPath(t *testing.T) {
	resolved := mustResolve(t, Config{PresetID: "openai"})
	got, err := resolved.BuildURL(xir.ProtocolChatCompletions, "/")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.openai.com/v1" {
		t.Fatalf("BuildURL = %q", got)
	}
}

func TestBuildURLErrors(t *testing.T) {
	anthropic := mustResolve(t, Config{PresetID: "anthropic"})
	if _, err := anthropic.BuildURL(xir.ProtocolChatCompletions, "/v1/chat/completions"); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("missing endpoint error = %v", err)
	}
	if _, err := anthropic.BuildURL(xir.Protocol("bogus"), "/v1/chat/completions"); !errors.Is(err, ErrUnsupportedProtocol) {
		t.Errorf("unknown protocol error = %v", err)
	}
	if _, err := anthropic.BuildURL(xir.ProtocolMessages, "https://evil.example/v1/messages"); err == nil || !strings.Contains(err.Error(), "relative") {
		t.Errorf("absolute client path error = %v", err)
	}
	invalid := Resolved{Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "relative/path"}}
	if _, err := invalid.BuildURL(xir.ProtocolChatCompletions, "/v1/chat/completions"); !errors.Is(err, ErrInvalidEndpoint) {
		t.Errorf("invalid base URL error = %v", err)
	}
}

func TestBuildURLAllPresetEndpoints(t *testing.T) {
	for _, preset := range Presets() {
		if preset.ID == "azure" {
			// Azure has no universal per-resource base URL.
			continue
		}
		resolved := mustResolve(t, Config{PresetID: preset.ID})
		for protocol := range resolved.Endpoints {
			clientPath := protocol.Endpoint()
			got, err := resolved.BuildURL(protocol, clientPath)
			if err != nil {
				t.Errorf("preset %q protocol %q: BuildURL: %v", preset.ID, protocol, err)
				continue
			}
			parsed, err := url.Parse(got)
			if err != nil {
				t.Errorf("preset %q protocol %q: %q: %v", preset.ID, protocol, got, err)
				continue
			}
			if !parsed.IsAbs() || parsed.Host == "" {
				t.Errorf("preset %q protocol %q: %q is not absolute", preset.ID, protocol, got)
			}
			if strings.Contains(parsed.Path, "/v1/v1/") || strings.Contains(parsed.Path, "/v1beta/v1beta/") {
				t.Errorf("preset %q protocol %q: duplicated version in %q", preset.ID, protocol, parsed.Path)
			}
		}
	}
}
