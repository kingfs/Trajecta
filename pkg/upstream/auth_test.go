package upstream

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAuthRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
}

// presetResolved assembles the Resolved view the production callers build from
// a catalog preset: internal/upstream constructs Resolved literals and delegates
// BuildURL/ApplyAuth to this package. LookupPreset returns a deep copy, so the
// returned value is safe to mutate per test.
func presetResolved(t *testing.T, presetID string) Resolved {
	t.Helper()
	preset, ok := LookupPreset(presetID)
	if !ok {
		t.Fatalf("LookupPreset(%q) failed", presetID)
	}
	return Resolved{Preset: preset, Endpoints: preset.Endpoints, Auth: preset.Auth, Headers: preset.Headers}
}

func TestApplyAuth(t *testing.T) {
	tests := []struct {
		name       string
		preset     string
		auth       *AuthSpec
		cred       Credential
		wantHeader string
		wantValue  string
	}{
		{
			name:       "openai api key",
			preset:     "openai",
			cred:       Credential{APIKey: "sk-openai"},
			wantHeader: "Authorization",
			wantValue:  "Bearer sk-openai",
		},
		{
			name:       "authorization header defaults to bearer",
			preset:     "openai",
			auth:       &AuthSpec{Mode: AuthModeAPIKey, Header: "Authorization"},
			cred:       Credential{APIKey: "sk-openai"},
			wantHeader: "Authorization",
			wantValue:  "Bearer sk-openai",
		},
		{
			name:       "deepseek oauth access token",
			preset:     "deepseek",
			cred:       Credential{Mode: AuthModeOAuth, AccessToken: "oauth-token"},
			wantHeader: "Authorization",
			wantValue:  "Bearer oauth-token",
		},
		{
			name:       "anthropic api key",
			preset:     "anthropic",
			cred:       Credential{APIKey: "sk-ant"},
			wantHeader: "x-api-key",
			wantValue:  "sk-ant",
		},
		{
			name:       "google api key",
			preset:     "google",
			cred:       Credential{APIKey: "google-key"},
			wantHeader: "x-goog-api-key",
			wantValue:  "google-key",
		},
		{
			name:       "azure api key",
			preset:     "azure",
			cred:       Credential{APIKey: "azure-key"},
			wantHeader: "api-key",
			wantValue:  "azure-key",
		},
		{
			name:       "cli credential access token",
			preset:     "openai",
			cred:       Credential{Mode: AuthModeCLICredential, AccessToken: "cli-token"},
			wantHeader: "Authorization",
			wantValue:  "Bearer cli-token",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved := presetResolved(t, test.preset)
			if test.auth != nil {
				resolved.Auth = *test.auth
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
	resolved := presetResolved(t, "openai")
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
	resolved := presetResolved(t, "anthropic")

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
	resolved := presetResolved(t, "openai")
	resolved.APIVersion = "2024-10-21"
	resolved.Auth = AuthSpec{
		Mode:   AuthModeAPIKey,
		Header: "Authorization",
		Scheme: "Bearer",
		Headers: []HeaderTemplate{
			{Name: "X-Account", Value: "acct-{{account_id}}"},
			{Name: "X-Plan", Value: "{{plan}}"},
			{Name: "X-Version", Value: "{{api_version}}"},
		},
	}
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

func TestApplyAuthNoneDropsClientCredentialHeaders(t *testing.T) {
	resolved := presetResolved(t, "ollama")
	resolved.Headers = map[string]string{"X-Local": "1"}
	req := newAuthRequest()
	req.Header.Set("Authorization", "Bearer client-key")
	req.Header.Set("X-Api-Key", "client-api-key")
	req.Header.Set("X-Goog-Api-Key", "client-google-key")
	req.Header.Set("Api-Key", "client-azure-key")
	if err := resolved.ApplyAuth(Credential{}, req); err != nil {
		t.Fatal(err)
	}
	// A keyless channel is exactly the case where the caller's Trajecta token
	// used to reach the upstream: AuthModeNone means "send no credential", not
	// "forward whatever the client sent".
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key"} {
		if got := req.Header.Get(name); got != "" {
			t.Errorf("%s = %q, want empty", name, got)
		}
	}
	if got := req.Header.Get("X-Local"); got != "1" {
		t.Errorf("X-Local = %q", got)
	}
}

// A configured static header is the operator explicitly asking for a value on
// the wire, so it is applied after the clear and must survive.
func TestApplyAuthNoneKeepsConfiguredStaticAuthHeader(t *testing.T) {
	resolved := presetResolved(t, "ollama")
	resolved.Headers = map[string]string{"Authorization": "Bearer static-key"}
	req := newAuthRequest()
	req.Header.Set("Authorization", "Bearer client-key")
	if err := resolved.ApplyAuth(Credential{}, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer static-key" {
		t.Errorf("Authorization = %q, want the configured static header", got)
	}
}

func TestApplyAuthNoneCredentialOverridesPresetMode(t *testing.T) {
	resolved := presetResolved(t, "openai")
	req := newAuthRequest()
	if err := resolved.ApplyAuth(Credential{Mode: AuthModeNone}, req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want none", got)
	}
}

func TestApplyAuthErrors(t *testing.T) {
	openai := presetResolved(t, "openai")
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
