package upstream

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

func TestPerModelProtocolCapabilityAllowsAnthropicPath(t *testing.T) {
	enabled := true
	resolved := ResolvedUpstream{
		ProtocolFamily: ProtocolFamilyOpenAICompatible,
		Capabilities: config.UpstreamCapabilitiesConfig{
			ChatCompletions: &enabled,
		},
		ModelCapabilities: map[string]config.UpstreamCapabilitiesConfig{
			"gpt-5": {Messages: &enabled},
		},
	}
	if !resolved.SupportsRawPath("/v1/messages", "gpt-5") {
		t.Fatal("per-model Messages capability did not allow /v1/messages")
	}
	if resolved.SupportsRawPath("/v1/messages", "other") {
		t.Fatal("model without Messages capability was allowed")
	}
	if !resolved.ProtocolCapabilitiesForModel("gpt-5").Messages {
		t.Fatal("ProtocolCapabilitiesForModel missing Messages")
	}
}
