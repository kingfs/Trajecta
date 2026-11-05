package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// A provider 404 is the most common "my model does not work" report, and the
// provider's own message is the only thing that separates a retired model id
// from a wrong base_url. The final error must carry the channel name, the path
// and that message instead of an opaque id.
func TestUpstream404ErrorCarriesProviderNamePathAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"model gpt-5 is not available on this key","type":"invalid_request_error"}}`)
	}))
	t.Cleanup(upstream.Close)

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:                   "provider-uuid",
			Name:                 "Mistral Aggregator",
			Enabled:              boolPtr(true),
			Priority:             100,
			ModelDiscovery:       router.ModelDiscoveryStaticOnly,
			StaticModels:         []string{"gpt-5"},
			ConfiguredModelsOnly: true,
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ApiKey:         "k",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}},
	}
	cfg.Debug.OutputDir = t.TempDir()
	cfg.Debug.MaskKey = true
	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxySrv := newStubProxyServer(t, handler)

	resp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatalf("POST error = %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", resp.StatusCode, text)
	}
	for _, want := range []string{
		"Mistral Aggregator",
		"provider-uuid",
		"/v1/chat/completions",
		"model gpt-5 is not available on this key",
		"does not serve this model id",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("error body = %s, want it to contain %q", text, want)
		}
	}
}

// A 404 from a provider router is not the same failure as a 404 from a provider
// handler that rejects the model id. Go's mux answers a route miss with
// text/plain "404 page not found"; the old classifier matched "not found" and
// told the operator to re-sync a model list even though the model exists and the
// provider simply does not serve that endpoint for it. Keep the two hints apart.
func TestUpstreamFailureHintClassifies404Source(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		want        string
		notContains []string
	}{
		{
			name:        "go_mux_route_miss_plain_text",
			status:      http.StatusNotFound,
			contentType: "text/plain; charset=utf-8",
			body:        "404 page not found\n",
			want:        upstreamHint404PathNotServed,
			notContains: []string{"does not serve this model id"},
		},
		{
			name:        "route_miss_whitespace_and_case",
			status:      http.StatusNotFound,
			contentType: "text/plain",
			body:        "  \n 404 PAGE NOT FOUND  \n",
			want:        upstreamHint404PathNotServed,
			notContains: []string{"does not serve this model id"},
		},
		{
			name:        "html_error_page",
			status:      http.StatusNotFound,
			contentType: "text/html; charset=utf-8",
			body:        "<!DOCTYPE html><html><body><h1>404 page not found</h1></body></html>",
			want:        upstreamHint404PathNotServed,
			notContains: []string{"does not serve this model id"},
		},
		{
			name:        "json_path_miss_without_model",
			status:      http.StatusNotFound,
			contentType: "application/json",
			body:        `{"detail":"Not Found"}`,
			want:        upstreamHint404PathNotServed,
			notContains: []string{"does not serve this model id"},
		},
		{
			name:        "json_model_not_found_envelope",
			status:      http.StatusNotFound,
			contentType: "application/json",
			body:        `{"error":{"message":"model not found","code":"model_not_found"}}`,
			want:        upstreamHint404UnknownModel,
		},
		{
			name:        "json_model_not_available_envelope",
			status:      http.StatusNotFound,
			contentType: "application/json",
			body:        `{"error":{"message":"model gpt-5 is not available on this key","type":"invalid_request_error"}}`,
			want:        upstreamHint404UnknownModel,
		},
		{
			name:   "empty_body",
			status: http.StatusNotFound,
			body:   "",
			want:   upstreamHint404NoBody,
		},
		{
			name:   "blank_body",
			status: http.StatusNotFound,
			body:   "   \n\t ",
			want:   upstreamHint404NoBody,
		},
		{
			name:        "non_404_status",
			status:      http.StatusInternalServerError,
			contentType: "application/json",
			body:        `{"error":{"message":"model not found"}}`,
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			header := http.Header{}
			if tt.contentType != "" {
				header.Set("Content-Type", tt.contentType)
			}
			resp := &http.Response{StatusCode: tt.status, Header: header}
			got := upstreamFailureHint(resp, tt.body)
			if got != tt.want {
				t.Fatalf("upstreamFailureHint() = %q, want %q", got, tt.want)
			}
			for _, bad := range tt.notContains {
				if strings.Contains(got, bad) {
					t.Fatalf("upstreamFailureHint() = %q, must not contain %q", got, bad)
				}
			}
		})
	}
}
