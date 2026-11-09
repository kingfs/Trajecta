package upstream

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

func TestBuildURLSelectsPerProtocolEndpoint(t *testing.T) {
	resolved, err := Resolve(config.UpstreamConfig{
		BaseURL:        "https://api.example.com/v1",
		ApiKey:         "k",
		ProviderPreset: "openai",
		APIType:        "chat_completions",
		Capabilities: config.UpstreamCapabilitiesConfig{
			Endpoints: map[string]string{
				"messages": "https://api.example.com/anthropic",
				"gemini":   "https://generativelanguage.example.com",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want string
	}{
		{"/v1/chat/completions", "https://api.example.com/v1/chat/completions"},
		{"/v1/messages", "https://api.example.com/anthropic/v1/messages"},
		{"/v1beta/models/gemini-2.0:generateContent", "https://generativelanguage.example.com/v1beta/models/gemini-2.0:generateContent"},
	}
	for _, tc := range cases {
		got, err := resolved.BuildURL(tc.path)
		if err != nil {
			t.Fatalf("BuildURL(%q) error = %v", tc.path, err)
		}
		if got != tc.want {
			t.Fatalf("BuildURL(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
	got, err := resolved.BuildURLForProtocol("messages", "/v1/messages")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://api.example.com/anthropic/v1/messages" {
		t.Fatalf("BuildURLForProtocol = %q", got)
	}
}
