package upstream

import (
	"slices"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestCapabilities(t *testing.T) {
	tests := []struct {
		preset string
		want   xir.Capabilities
	}{
		{
			preset: "openai",
			want:   xir.Capabilities{ChatCompletions: true, Responses: true},
		},
		{
			preset: "anthropic",
			want:   xir.Capabilities{Messages: true},
		},
		{
			preset: "deepseek",
			want:   xir.Capabilities{ChatCompletions: true, Responses: true, Messages: true},
		},
		{
			preset: "google",
			want:   xir.Capabilities{ChatCompletions: true, Gemini: true},
		},
		{
			preset: "vertex",
			want:   xir.Capabilities{Gemini: true},
		},
		{
			preset: "ollama",
			want:   xir.Capabilities{ChatCompletions: true},
		},
	}
	for _, test := range tests {
		resolved := mustResolve(t, Config{PresetID: test.preset})
		if got := resolved.Capabilities("any-model"); got != test.want {
			t.Errorf("preset %q Capabilities = %+v, want %+v", test.preset, got, test.want)
		}
	}
}

func TestCapabilitiesModelOverride(t *testing.T) {
	resolved := mustResolve(t, Config{
		PresetID: "deepseek",
		ModelProtocols: map[string][]xir.Protocol{
			"DeepSeek-Chat": {xir.ProtocolChatCompletions},
			"all-off":       {},
		},
	})
	if got := resolved.Capabilities("deepseek-chat"); !got.ChatCompletions || got.Responses || got.Messages || got.Gemini {
		t.Errorf("deepseek-chat capabilities = %+v", got)
	}
	if got := resolved.Capabilities("DEEPSEEK-CHAT"); !got.ChatCompletions || got.Responses {
		t.Errorf("model override should be case-insensitive, got %+v", got)
	}
	if got := resolved.Capabilities("unlisted-model"); !got.ChatCompletions || !got.Responses || !got.Messages {
		t.Errorf("unlisted-model capabilities = %+v", got)
	}
	if got := resolved.Capabilities("all-off"); got.ChatCompletions || got.Responses || got.Messages || got.Gemini {
		t.Errorf("empty override should disable every protocol, got %+v", got)
	}
	if got := resolved.Capabilities(""); !got.ChatCompletions || !got.Responses || !got.Messages {
		t.Errorf("empty model should use upstream protocols, got %+v", got)
	}
}

func TestProtocolsForModelFiltersMissingEndpoints(t *testing.T) {
	resolved := mustResolve(t, Config{PresetID: "openai"})
	resolved.Protocols = []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolMessages}
	got := resolved.ProtocolsForModel("model")
	want := []xir.Protocol{xir.ProtocolChatCompletions}
	if !slices.Equal(got, want) {
		t.Errorf("ProtocolsForModel = %v, want %v", got, want)
	}
}

func TestCapabilitiesHas(t *testing.T) {
	resolved := mustResolve(t, Config{PresetID: "deepseek"})
	capabilities := resolved.Capabilities("model")
	for _, protocol := range []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages} {
		if !capabilities.Has(protocol) {
			t.Errorf("Capabilities.Has(%q) = false", protocol)
		}
	}
	if capabilities.Has(xir.ProtocolGemini) {
		t.Error("Capabilities.Has(gemini) = true")
	}
	if got := capabilities.Protocols(); !slices.Equal(got, []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages}) {
		t.Errorf("Capabilities.Protocols = %v", got)
	}
}
