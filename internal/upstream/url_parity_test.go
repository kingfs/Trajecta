package upstream

import (
	"testing"

	pkgupstream "github.com/kingfs/Trajecta/pkg/upstream"
	"github.com/kingfs/Trajecta/pkg/xir"
)

func TestBuildURLParityWithPkg(t *testing.T) {
	cases := []struct {
		name     string
		family   string
		profile  string
		base     string
		protocol string
		path     string
	}{
		{"openai v1", ProtocolFamilyOpenAICompatible, RoutingProfileOpenAIDefault, "https://api.openai.com/v1", "chat_completions", "/v1/chat/completions"},
		{"openai path", ProtocolFamilyOpenAICompatible, RoutingProfileOpenAIDefault, "https://compat.example.com/openai", "chat_completions", "/v1/chat/completions"},
		{"openai api v1", ProtocolFamilyOpenAICompatible, RoutingProfileOpenAIDefault, "https://compat.example.com/api/v1", "chat_completions", "/v1/chat/completions"},
		{"openai models", ProtocolFamilyOpenAICompatible, RoutingProfileOpenAIDefault, "https://api.openai.com/v1", "chat_completions", "/v1/models"},
		{"openai query", ProtocolFamilyOpenAICompatible, RoutingProfileOpenAIDefault, "https://api.openai.com/v1", "chat_completions", "/v1/chat/completions?x=1"},
		{"anthropic", ProtocolFamilyAnthropicMessages, RoutingProfileAnthropicDefault, "https://api.anthropic.com", "messages", "/v1/messages"},
		{"gemini", ProtocolFamilyGoogleGenAI, RoutingProfileGoogleAIStudio, "https://generativelanguage.googleapis.com", "gemini", "/v1beta/models/gemini:generateContent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			internal := ResolvedUpstream{BaseURL: tc.base, ProtocolFamily: tc.family, RoutingProfile: tc.profile}
			got, err := internal.BuildURLForProtocol(tc.protocol, tc.path)
			if err != nil {
				t.Fatal(err)
			}
			p := pkgupstream.Resolved{Endpoints: map[xir.Protocol]string{xir.Protocol(tc.protocol): tc.base}}
			want, err := p.BuildURL(xir.Protocol(tc.protocol), tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("internal=%q pkg=%q", got, want)
			}
		})
	}
}

func TestBuildURLForProtocolDelegationBoundaries(t *testing.T) {
	openai := ResolvedUpstream{BaseURL: "https://api.openai.com/v1", ProtocolFamily: ProtocolFamilyOpenAICompatible, RoutingProfile: RoutingProfileOpenAIDefault}
	got, err := openai.BuildURLForProtocol("chat_completions", "/v1/chat/completions")
	if err != nil || got != "https://api.openai.com/v1/chat/completions" {
		t.Fatalf("delegated URL = %q err=%v", got, err)
	}

	tokenize := ResolvedUpstream{BaseURL: "http://localhost:38080/v1", ProtocolFamily: ProtocolFamilyOpenAICompatible, RoutingProfile: RoutingProfileOpenAIDefault}
	got, err = tokenize.BuildURLForProtocol("chat_completions", "/v1/tokenize")
	if err != nil || got != "http://localhost:38080/tokenize" {
		t.Fatalf("tokenize fallback = %q err=%v", got, err)
	}

	azure := ResolvedUpstream{BaseURL: "https://x.openai.azure.com/openai", ProtocolFamily: ProtocolFamilyOpenAICompatible, RoutingProfile: RoutingProfileAzureOpenAIV1, APIVersion: "preview"}
	got, err = azure.BuildURLForProtocol("chat_completions", "/v1/chat/completions")
	if err != nil || got != "https://x.openai.azure.com/openai/chat/completions?api-version=preview" {
		t.Fatalf("azure fallback = %q err=%v", got, err)
	}
}
