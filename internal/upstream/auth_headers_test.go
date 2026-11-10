package upstream

import (
	"net/http"
	"strings"
	"testing"
)

func TestApplyAuthHeadersProfiles(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		header  string
		value   string
	}{
		{"openai", RoutingProfileOpenAIDefault, "Authorization", "Bearer sk-test"},
		{"azure", RoutingProfileAzureOpenAIV1, "api-key", "sk-test"},
		{"anthropic", RoutingProfileAnthropicDefault, "x-api-key", "sk-test"},
		{"google", RoutingProfileGoogleAIStudio, "x-goog-api-key", "sk-test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header := http.Header{"Authorization": {"stale"}, "X-Api-Key": {"stale"}, "X-Goog-Api-Key": {"stale"}}
			u := ResolvedUpstream{RoutingProfile: tc.profile, APIKey: "sk-test", Headers: map[string]string{"X-Static": "v"}}
			u.ApplyAuthHeaders(header)
			if got := header.Get(tc.header); got != tc.value {
				t.Fatalf("%s = %q, want %q (%v)", tc.header, got, tc.value, header)
			}
			if header.Get("X-Static") != "v" {
				t.Fatalf("static header missing: %v", header)
			}
			for _, stale := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key"} {
				if !strings.EqualFold(stale, tc.header) && header.Get(stale) != "" {
					t.Fatalf("stale %s survived: %v", stale, header)
				}
			}
		})
	}
}

func TestApplyAuthHeadersAnthropicVersionAndEmptyKey(t *testing.T) {
	header := http.Header{}
	u := ResolvedUpstream{RoutingProfile: RoutingProfileAnthropicDefault, APIKey: "sk", APIVersion: "2023-06-01", Headers: map[string]string{"X-Static": "v"}}
	u.ApplyAuthHeaders(header)
	if header.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", header.Get("anthropic-version"))
	}
	empty := http.Header{}
	(&ResolvedUpstream{Headers: map[string]string{"X-Static": "v"}}).ApplyAuthHeaders(empty)
	if empty.Get("Authorization") != "" || empty.Get("X-Static") != "v" {
		t.Fatalf("empty-key headers = %v", empty)
	}
}
