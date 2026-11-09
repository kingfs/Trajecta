package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

func newAuthRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
}

func TestApplyAuth(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		cred       Credential
		wantHeader string
		wantValue  string
	}{
		{
			name:       "openai api key",
			cfg:        Config{PresetID: "openai"},
			cred:       Credential{APIKey: "sk-openai"},
			wantHeader: "Authorization",
			wantValue:  "Bearer sk-openai",
		},
		{
			name:       "authorization header defaults to bearer",
			cfg:        Config{PresetID: "openai", Auth: AuthSpec{Header: "Authorization"}},
			cred:       Credential{APIKey: "sk-openai"},
			wantHeader: "Authorization",
			wantValue:  "Bearer sk-openai",
		},
		{
			name:       "deepseek oauth access token",
			cfg:        Config{PresetID: "deepseek"},
			cred:       Credential{Mode: AuthModeOAuth, AccessToken: "oauth-token"},
			wantHeader: "Authorization",
			wantValue:  "Bearer oauth-token",
		},
		{
			name:       "anthropic api key",
			cfg:        Config{PresetID: "anthropic"},
			cred:       Credential{APIKey: "sk-ant"},
			wantHeader: "x-api-key",
			wantValue:  "sk-ant",
		},
		{
			name:       "google api key",
			cfg:        Config{PresetID: "google"},
			cred:       Credential{APIKey: "google-key"},
			wantHeader: "x-goog-api-key",
			wantValue:  "google-key",
		},
		{
			name: "azure api key",
			cfg: Config{
				PresetID:  "azure",
				Endpoints: map[xir.Protocol]string{xir.ProtocolChatCompletions: "https://resource.openai.azure.com/openai/v1"},
			},
			cred:       Credential{APIKey: "azure-key"},
			wantHeader: "api-key",
			wantValue:  "azure-key",
		},
		{
			name:       "cli credential access token",
			cfg:        Config{PresetID: "openai"},
			cred:       Credential{Mode: AuthModeCLICredential, AccessToken: "cli-token"},
			wantHeader: "Authorization",
			wantValue:  "Bearer cli-token",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := Resolve(test.cfg)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			req := newAuthRequest()
			if err := resolved.ApplyAuth(test.cred, req); err != nil {
				t.Fatalf("ApplyAuth: %v", err)
			}
			if got := req.Header.Get(test.wantHeader); got != test.wantValue {
				t.Errorf("header %q = %q, want %q", test.wantHeader, got, test.wantValue)
			}
		})
	}
}

func TestApplyAuthClearsCompetingHeaders(t *testing.T) {
	resolved := mustResolve(t, Config{PresetID: "openai"})
	req := newAuthRequest()
	for _, name := range []string{"Authorization", "x-api-key", "x-goog-api-key", "api-key"} {
		req.Header.Set(name, "client-"+name)
	}
	if err := resolved.ApplyAuth(Credential{APIKey: "sk-server"}, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-server" {
		t.Errorf("Authorization = %q", got)
	}
	for _, name := range []string{"x-api-key", "x-goog-api-key", "api-key"} {
		if got := req.Header.Get(name); got != "" {
			t.Errorf("competing header %q was not removed: %q", name, got)
		}
	}
}

func TestApplyAuthAnthropicVersionTemplate(t *testing.T) {
	resolved := mustResolve(t, Config{PresetID: "anthropic"})

	req := newAuthRequest()
	if err := resolved.ApplyAuth(Credential{APIKey: "sk-ant"}, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q", got)
	}

	req = newAuthRequest()
	req.Header.Set("anthropic-version", "2024-01-01")
	if err := resolved.ApplyAuth(Credential{APIKey: "sk-ant"}, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("anthropic-version"); got != "2024-01-01" {
		t.Errorf("anthropic-version should be preserved, got %q", got)
	}
}

func TestApplyAuthTemplatesAndStaticHeaders(t *testing.T) {
	resolved := mustResolve(t, Config{
		PresetID:   "openai",
		APIVersion: "2024-10-21",
		Auth: AuthSpec{
			Mode:   AuthModeAPIKey,
			Header: "Authorization",
			Scheme: "Bearer",
			Headers: []HeaderTemplate{
				{Name: "X-Account", Value: "acct-{{account_id}}"},
				{Name: "X-Plan", Value: "{{plan}}"},
				{Name: "X-Version", Value: "{{api_version}}"},
			},
		},
	})
	resolved.Headers = map[string]string{"X-Static": "yes", "Authorization": "Static override"}
	req := newAuthRequest()
	cred := Credential{APIKey: "sk", AccountID: "acct-1", Plan: "pro"}
	if err := resolved.ApplyAuth(cred, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("X-Account"); got != "acct-acct-1" {
		t.Errorf("X-Account = %q", got)
	}
	if got := req.Header.Get("X-Plan"); got != "pro" {
		t.Errorf("X-Plan = %q", got)
	}
	if got := req.Header.Get("X-Version"); got != "2024-10-21" {
		t.Errorf("X-Version = %q", got)
	}
	if got := req.Header.Get("X-Static"); got != "yes" {
		t.Errorf("X-Static = %q", got)
	}
	if got := req.Header.Get("Authorization"); got != "Static override" {
		t.Errorf("static header should be applied last, Authorization = %q", got)
	}
}

func TestApplyAuthNone(t *testing.T) {
	resolved := mustResolve(t, Config{PresetID: "ollama"})
	resolved.Headers = map[string]string{"X-Local": "1"}
	req := newAuthRequest()
	req.Header.Set("Authorization", "Bearer client-key")
	if err := resolved.ApplyAuth(Credential{}, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer client-key" {
		t.Errorf("none auth should leave the request auth header alone, got %q", got)
	}
	if got := req.Header.Get("X-Local"); got != "1" {
		t.Errorf("X-Local = %q", got)
	}
}

func TestApplyAuthNoneCredentialOverridesPresetMode(t *testing.T) {
	resolved := mustResolve(t, Config{PresetID: "openai"})
	req := newAuthRequest()
	if err := resolved.ApplyAuth(Credential{Mode: AuthModeNone}, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestApplyAuthErrors(t *testing.T) {
	openai := mustResolve(t, Config{PresetID: "openai"})
	if err := openai.ApplyAuth(Credential{}, newAuthRequest()); !errors.Is(err, ErrMissingCredential) {
		t.Errorf("missing API key error = %v", err)
	}
	if err := openai.ApplyAuth(Credential{Mode: AuthModeOAuth}, newAuthRequest()); !errors.Is(err, ErrMissingCredential) {
		t.Errorf("missing OAuth token error = %v", err)
	}
	if err := openai.ApplyAuth(Credential{Mode: AuthModeCLICredential}, newAuthRequest()); !errors.Is(err, ErrMissingCredential) {
		t.Errorf("missing CLI credential error = %v", err)
	}
	if err := openai.ApplyAuth(Credential{Mode: "bogus", APIKey: "sk"}, newAuthRequest()); !errors.Is(err, ErrInvalidAuthMode) {
		t.Errorf("invalid auth mode error = %v", err)
	}
	if err := openai.ApplyAuth(Credential{APIKey: "sk"}, nil); err == nil || !strings.Contains(err.Error(), "nil request") {
		t.Errorf("nil request error = %v", err)
	}
}
