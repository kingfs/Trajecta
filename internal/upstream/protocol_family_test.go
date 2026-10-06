package upstream

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
)

// TestSupportsProtocolFamilyPinsTheFamilyRules covers the rule both the forwarding hot path and
// the Monitor's routing inspector ask, because it is the reason they agree.
//
// The proxy does not translate between protocol families, so the family decides which endpoints
// a channel can serve at all: an Anthropic channel answers `/v1/messages` and not
// `/v1/chat/completions`, and a Google or Vertex channel answers neither. The two exceptions
// the endpoint check keeps are `/v1/models`, which is answered for every family, and the
// OpenAI-compatible family, which serves everything an OpenAI-compatible provider serves.
func TestSupportsProtocolFamilyPinsTheFamilyRules(t *testing.T) {
	const (
		openAI  = llm.ProviderOpenAICompatible
		chat    = "/v1/chat/completions"
		msgs    = "/v1/messages"
		models  = "/v1/models"
		gemini  = "/v1beta/models:generateContent"
		anthroP = llm.ProviderAnthropic
		googleP = llm.ProviderGoogleGenAI
		vertexP = llm.ProviderVertexNative
	)
	for _, tc := range []struct {
		family   string
		provider string
		endpoint string
		want     bool
	}{
		{ProtocolFamilyAnthropicMessages, anthroP, msgs, true},
		{ProtocolFamilyAnthropicMessages, openAI, chat, false},
		{ProtocolFamilyAnthropicMessages, openAI, models, true},
		{ProtocolFamilyGoogleGenAI, googleP, gemini, true},
		{ProtocolFamilyGoogleGenAI, openAI, chat, false},
		{ProtocolFamilyVertexNative, vertexP, gemini, true},
		{ProtocolFamilyVertexNative, openAI, chat, false},
		{ProtocolFamilyOpenAICompatible, openAI, chat, true},
		{ProtocolFamilyOpenAICompatible, anthroP, msgs, false},
		{"", openAI, chat, true},
		{"", anthroP, msgs, false},
		{"something_else", openAI, chat, false},
	} {
		if got := SupportsProtocolFamily(tc.family, tc.provider, tc.endpoint); got != tc.want {
			t.Errorf("SupportsProtocolFamily(%q, %q, %q) = %v, want %v", tc.family, tc.provider, tc.endpoint, got, tc.want)
		}
	}
}
