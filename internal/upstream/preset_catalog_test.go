package upstream

import (
	"testing"

	pkgupstream "github.com/kingfs/Trajecta/pkg/upstream"
	"github.com/kingfs/Trajecta/pkg/xir"
)

// The runtime preset registry and the pure catalog must not drift: every
// catalog preset needs a legacy spec so channel validation/defaults can see it.
func TestEveryCatalogPresetHasLegacySpec(t *testing.T) {
	for _, preset := range pkgupstream.Presets() {
		if _, ok := providerPresetRegistry[preset.ID]; !ok {
			t.Errorf("catalog preset %q has no legacy provider spec", preset.ID)
		}
	}
}

func TestCatalogSpecInfersRuntimeDefaults(t *testing.T) {
	messages := catalogSpec(pkgupstream.Preset{Endpoints: map[xir.Protocol]string{xir.ProtocolMessages: "https://a"}})
	if messages.ProtocolFamily != ProtocolFamilyAnthropicMessages {
		t.Fatalf("messages family = %q", messages.ProtocolFamily)
	}
	gemini := catalogSpec(pkgupstream.Preset{Endpoints: map[xir.Protocol]string{xir.ProtocolGemini: "https://g"}})
	if gemini.ProtocolFamily != ProtocolFamilyGoogleGenAI {
		t.Fatalf("gemini family = %q", gemini.ProtocolFamily)
	}
	chat := catalogSpec(pkgupstream.Preset{Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://c"}})
	if chat.ProtocolFamily != ProtocolFamilyOpenAICompatible {
		t.Fatalf("chat family = %q", chat.ProtocolFamily)
	}
}
