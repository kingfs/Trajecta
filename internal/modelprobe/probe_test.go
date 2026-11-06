package modelprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/upstream"
)

func declaredProtocols(declared ...Protocol) map[Protocol]bool {
	out := map[Protocol]bool{
		ProtocolChatCompletions: false,
		ProtocolResponses:       false,
		ProtocolMessages:        false,
		ProtocolGenerateContent: false,
	}
	for _, protocol := range declared {
		out[protocol] = true
	}
	return out
}

func openAITarget(baseURL string) Target {
	return Target{
		ChannelID:       "nvidia-nim",
		Model:           "deepseek-v4.1-flash",
		WireModel:       "deepseek-ai/deepseek-v4.1-flash",
		ProtocolFamily:  upstream.ProtocolFamilyOpenAICompatible,
		ChannelEnabled:  true,
		ModelEnabled:    true,
		ModelState:      "enabled",
		Routable:        true,
		Declared:        declaredProtocols(ProtocolChatCompletions, ProtocolResponses),
		NativeResponses: true,
		Resolved: upstream.ResolvedUpstream{
			BaseURL:        strings.TrimRight(baseURL, "/") + "/v1",
			APIKey:         "sk-test-secret",
			ProviderPreset: "openai",
			APIType:        upstream.APITypeChatCompletions,
			ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
			RoutingProfile: upstream.RoutingProfileOpenAIDefault,
		},
	}
}

func familyTarget(baseURL, family, apiType, routingProfile string, protocols ...Protocol) Target {
	target := openAITarget(baseURL)
	target.ProtocolFamily = family
	target.Declared = declaredProtocols(protocols...)
	target.Resolved.BaseURL = baseURL
	target.Resolved.ProtocolFamily = family
	target.Resolved.APIType = apiType
	target.Resolved.RoutingProfile = routingProfile
	target.Resolved.ProviderPreset = ""
	return target
}

func TestParseProtocol(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Protocol
	}{
		{"", ProtocolAuto},
		{"auto", ProtocolAuto},
		{"AUTO", ProtocolAuto},
		{" chat_completions ", ProtocolChatCompletions},
		{"responses", ProtocolResponses},
		{"messages", ProtocolMessages},
		{"generate_content", ProtocolGenerateContent},
		{"generate-content", ProtocolGenerateContent},
	} {
		got, err := ParseProtocol(tc.in)
		if err != nil {
			t.Fatalf("ParseProtocol(%q) error = %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseProtocol(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	for _, invalid := range []string{"grpc", "chat", "openai"} {
		if _, err := ParseProtocol(invalid); err == nil {
			t.Fatalf("ParseProtocol(%q) error = nil, want an error", invalid)
		}
	}
}

func TestRunChatCompletionsSendsMinimalBodyAndAuth(t *testing.T) {
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"}}]}`))
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	target.Declared = declaredProtocols(ProtocolChatCompletions)
	report := Run(context.Background(), target, ProtocolAuto, nil)

	if report.Status != ReportStatusOK {
		t.Fatalf("status = %q, want ok; results = %+v", report.Status, report.Results)
	}
	if len(report.Results) != 1 || report.Results[0].Status != StatusOK {
		t.Fatalf("results = %+v", report.Results)
	}
	result := report.Results[0]
	if result.HTTPStatus != http.StatusOK || result.LatencyMS < 0 || result.Error != "" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.HasSuffix(result.URL, "/v1/chat/completions") || result.Method != http.MethodPost {
		t.Fatalf("result url/method = %q/%q", result.URL, result.Method)
	}
	if gotAuth != "Bearer sk-test-secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "deepseek-ai/deepseek-v4.1-flash" {
		t.Fatalf("body.model = %v", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(16) || gotBody["stream"] != false {
		t.Fatalf("body = %v", gotBody)
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 1 {
		t.Fatalf("body.messages = %v", gotBody["messages"])
	}
	message := messages[0].(map[string]any)
	if message["role"] != "user" || message["content"] != "ping" {
		t.Fatalf("body.messages[0] = %v", message)
	}
	if len(report.Suggestions) != 0 {
		t.Fatalf("suggestions = %+v, want none", report.Suggestions)
	}
}

func TestRunClassifiesNVIDIAResponsesPlain404AsEndpointNotSupported(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"chat.completion","choices":[]}`))
		case "/v1/responses":
			// Exactly what NVIDIA NIM answers for a route it does not serve.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	report := Run(context.Background(), openAITarget(server.URL), ProtocolAuto, nil)

	if report.Status != ReportStatusPartial {
		t.Fatalf("status = %q, want partial; results = %+v", report.Status, report.Results)
	}
	byProtocol := map[Protocol]Result{}
	for _, result := range report.Results {
		byProtocol[result.Protocol] = result
	}
	if byProtocol[ProtocolChatCompletions].Status != StatusOK {
		t.Fatalf("chat result = %+v", byProtocol[ProtocolChatCompletions])
	}
	responses := byProtocol[ProtocolResponses]
	if responses.Status != StatusEndpointNotSupported {
		t.Fatalf("responses status = %q, want endpoint_not_supported; result = %+v", responses.Status, responses)
	}
	if responses.HTTPStatus != http.StatusNotFound {
		t.Fatalf("responses http status = %d", responses.HTTPStatus)
	}
	if !responses.Declared {
		t.Fatalf("responses declared = false")
	}
	if len(report.Suggestions) != 1 {
		t.Fatalf("suggestions = %+v, want one disable_capability", report.Suggestions)
	}
	suggestion := report.Suggestions[0]
	if suggestion.Kind != "disable_capability" || suggestion.Capability != "responses" || suggestion.Scope != "model" {
		t.Fatalf("suggestion = %+v", suggestion)
	}
}

func TestRunAutoProbesDeclaredNonNativeResponses(t *testing.T) {
	// The incident shape: the channel declares Responses support, but the gateway will
	// not pass `/v1/responses` through natively. The probe must still test the route so
	// the operator learns that the declaration is not backed by the upstream.
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[]}`))
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	target.NativeResponses = false
	report := Run(context.Background(), target, ProtocolAuto, nil)

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want chat and responses", got)
	}
	if report.Status != ReportStatusPartial {
		t.Fatalf("status = %q, want partial; results = %+v", report.Status, report.Results)
	}
	var responses *Result
	for i := range report.Results {
		if report.Results[i].Protocol == ProtocolResponses {
			responses = &report.Results[i]
		}
	}
	if responses == nil || responses.Status != StatusEndpointNotSupported || !responses.Declared {
		t.Fatalf("responses result = %+v", responses)
	}
	const nativeHint = "declared Responses is not used natively; the gateway will answer /v1/responses through the local runtime unless the model is marked supports_responses=true"
	if !containsString(report.Warnings, nativeHint) {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
	if !strings.Contains(responses.Hint, nativeHint) {
		t.Fatalf("responses hint = %q, want the native-runtime hint", responses.Hint)
	}
	if !hasSuggestion(report.Suggestions, "disable_capability", "model") {
		t.Fatalf("suggestions = %+v, want disable_capability", report.Suggestions)
	}
}

func TestRunAutoWarnsWhenResponsesIsTranslatedToChat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	target.Declared = declaredProtocols(ProtocolChatCompletions)
	target.NativeResponses = true
	report := Run(context.Background(), target, ProtocolAuto, nil)

	if !containsString(report.Warnings, "/v1/responses is not declared; the local Responses runtime will translate /v1/responses requests into /v1/chat/completions") {
		t.Fatalf("warnings = %#v", report.Warnings)
	}
}

func TestRunExplicitUndeclaredProtocolSkipsWithoutCalling(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	target.Declared = declaredProtocols(ProtocolChatCompletions)
	report := Run(context.Background(), target, ProtocolResponses, nil)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 for an undeclared protocol", got)
	}
	if report.Status != ReportStatusUnsupported {
		t.Fatalf("status = %q, want unsupported", report.Status)
	}
	if len(report.Results) != 1 || report.Results[0].Status != StatusSkipped || report.Results[0].Declared {
		t.Fatalf("results = %+v", report.Results)
	}
}

func TestRunProtocolFamilyMismatchIsUnsupportedWithoutCalling(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	target := familyTarget(server.URL, upstream.ProtocolFamilyAnthropicMessages, upstream.APITypeMessages, upstream.RoutingProfileAnthropicDefault, ProtocolMessages)
	target.WireModel = "claude-test"
	report := Run(context.Background(), target, ProtocolChatCompletions, nil)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
	if report.Status != ReportStatusUnsupported {
		t.Fatalf("status = %q, want unsupported", report.Status)
	}
	if len(report.Results) != 1 || report.Results[0].Status != StatusUnsupportedProtocol {
		t.Fatalf("results = %+v", report.Results)
	}
}

func TestRunAnthropicMessagesUsesXAPIKey(t *testing.T) {
	var gotAPIKey string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAPIKey = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg-1","type":"message","content":[{"type":"text","text":"pong"}]}`))
	}))
	defer server.Close()

	target := familyTarget(server.URL, upstream.ProtocolFamilyAnthropicMessages, upstream.APITypeMessages, upstream.RoutingProfileAnthropicDefault, ProtocolMessages)
	target.WireModel = "claude-test"
	target.Resolved.APIVersion = upstream.DefaultAnthropicAPIVersion
	report := Run(context.Background(), target, ProtocolAuto, nil)

	if report.Status != ReportStatusOK {
		t.Fatalf("status = %q, results = %+v", report.Status, report.Results)
	}
	if gotPath != "/v1/messages" || gotAPIKey != "sk-test-secret" {
		t.Fatalf("path/api-key = %q/%q", gotPath, gotAPIKey)
	}
}

func TestRunGenerateContentPaths(t *testing.T) {
	for _, tc := range []struct {
		name           string
		family         string
		routingProfile string
		wireModel      string
		wantPath       string
	}{
		{
			name:           "google",
			family:         upstream.ProtocolFamilyGoogleGenAI,
			routingProfile: upstream.RoutingProfileGoogleAIStudio,
			wireModel:      "gemini-2.5-flash",
			wantPath:       "/v1beta/models/gemini-2.5-flash:generateContent",
		},
		{
			name:           "vertex",
			family:         upstream.ProtocolFamilyVertexNative,
			routingProfile: upstream.RoutingProfileVertexExpress,
			wireModel:      "gemini-2.5-pro",
			wantPath:       "/v1/publishers/models/gemini-2.5-pro:generateContent",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
					t.Errorf("decode body: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"pong"}]}}]}`))
			}))
			defer server.Close()

			target := familyTarget(server.URL, tc.family, upstream.APITypeGemini, tc.routingProfile, ProtocolGenerateContent)
			target.WireModel = tc.wireModel
			report := Run(context.Background(), target, ProtocolAuto, nil)

			if report.Status != ReportStatusOK {
				t.Fatalf("status = %q, results = %+v", report.Status, report.Results)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tc.wantPath)
			}
			contents, ok := gotBody["contents"].([]any)
			if !ok || len(contents) != 1 {
				t.Fatalf("body.contents = %v", gotBody["contents"])
			}
			parts := contents[0].(map[string]any)["parts"].([]any)
			if parts[0].(map[string]any)["text"] != "ping" {
				t.Fatalf("body.parts = %v", parts)
			}
			generationConfig, ok := gotBody["generationConfig"].(map[string]any)
			if !ok || generationConfig["maxOutputTokens"] != float64(16) {
				t.Fatalf("body.generationConfig = %v", gotBody["generationConfig"])
			}
		})
	}
}

func TestRunClassifiesStatusCodeFamilies(t *testing.T) {
	for _, tc := range []struct {
		name       string
		statusCode int
		body       string
		content    string
		want       string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"error":{"message":"missing key"}}`, "application/json", StatusAuthFailed},
		{"bad request auth keyword", http.StatusBadRequest, `{"error":{"message":"Incorrect API key provided"}}`, "application/json", StatusAuthFailed},
		{"bad request", http.StatusBadRequest, `{"error":{"message":"unknown field messages"}}`, "application/json", StatusInvalidRequest},
		{"unprocessable", http.StatusUnprocessableEntity, `{"error":{"message":"unprocessable"}}`, "application/json", StatusInvalidRequest},
		{"bad request model not supported", http.StatusBadRequest, `{"error":{"message":"The model gpt-x is not supported"}}`, "application/json", StatusModelNotFound},
		{"bad request unknown model", http.StatusBadRequest, `{"error":{"message":"unknown model: gpt-x"}}`, "application/json", StatusModelNotFound},
		{"unprocessable model not found", http.StatusUnprocessableEntity, `{"error":{"message":"model foo not found"}}`, "application/json", StatusModelNotFound},
		{"bad request model code", http.StatusBadRequest, `{"error":{"code":"model_not_found","message":"no such model"}}`, "application/json", StatusModelNotFound},
		{"model not found code", http.StatusNotFound, `{"error":{"code":"model_not_found","message":"no route"}}`, "application/json", StatusModelNotFound},
		{"model does not exist", http.StatusNotFound, `{"error":{"message":"The model does not exist"}}`, "application/json", StatusModelNotFound},
		{"plain page not found", http.StatusNotFound, "404 page not found\n", "text/plain; charset=utf-8", StatusEndpointNotSupported},
		{"html not found", http.StatusNotFound, "<html><head><title>404</title></head></html>", "text/html", StatusEndpointNotSupported},
		{"empty not found", http.StatusNotFound, "", "", StatusEndpointNotSupported},
		{"method not allowed", http.StatusMethodNotAllowed, "not allowed", "text/plain", StatusEndpointNotSupported},
		{"not implemented", http.StatusNotImplemented, "", "", StatusEndpointNotSupported},
		{"rate limited", http.StatusTooManyRequests, `{"error":{"message":"slow down"}}`, "application/json", StatusRateLimited},
		{"upstream 5xx", http.StatusBadGateway, "bad gateway", "text/plain", StatusUpstream5xx},
		{"redirect", http.StatusFound, "", "", StatusInvalidResponse},
		{"invalid response text", http.StatusOK, "pong", "text/plain", StatusInvalidResponse},
		{"invalid response shape", http.StatusOK, `{"id":"x"}`, "application/json", StatusInvalidResponse},
		{"invalid response error envelope", http.StatusOK, `{"error":{"message":"boom"}}`, "application/json", StatusInvalidResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.content != "" {
					w.Header().Set("Content-Type", tc.content)
				}
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			target := openAITarget(server.URL)
			target.Declared = declaredProtocols(ProtocolChatCompletions)
			report := Run(context.Background(), target, ProtocolChatCompletions, nil)

			if len(report.Results) != 1 {
				t.Fatalf("results = %+v", report.Results)
			}
			result := report.Results[0]
			if result.Status != tc.want {
				t.Fatalf("status = %q, want %q (http %d body %q)", result.Status, tc.want, tc.statusCode, tc.body)
			}
			if result.HTTPStatus != tc.statusCode {
				t.Fatalf("http status = %d, want %d", result.HTTPStatus, tc.statusCode)
			}
			if tc.want == StatusOK && result.Error != "" {
				t.Fatalf("ok result carried an error: %q", result.Error)
			}
			if tc.want != StatusOK && result.Hint == "" {
				t.Fatalf("non-ok result has no hint: %+v", result)
			}
		})
	}
}

func TestRunClassifiesTransportFailures(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[]}`))
		}))
		defer server.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		target := openAITarget(server.URL)
		target.Declared = declaredProtocols(ProtocolChatCompletions)
		report := Run(ctx, target, ProtocolChatCompletions, nil)

		if len(report.Results) != 1 || report.Results[0].Status != StatusTimeout {
			t.Fatalf("results = %+v, want timeout", report.Results)
		}
	})

	t.Run("network error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		closedURL := server.URL
		server.Close()

		target := openAITarget(closedURL)
		target.Declared = declaredProtocols(ProtocolChatCompletions)
		report := Run(context.Background(), target, ProtocolChatCompletions, nil)

		if len(report.Results) != 1 || report.Results[0].Status != StatusNetworkError {
			t.Fatalf("results = %+v, want network_error", report.Results)
		}
	})
}

func TestRunNeverLeaksAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided: sk-test-secret"}}`))
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	target.Declared = declaredProtocols(ProtocolChatCompletions)
	report := Run(context.Background(), target, ProtocolChatCompletions, nil)

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	if strings.Contains(string(encoded), "sk-test-secret") {
		t.Fatalf("API key leaked into the report: %s", encoded)
	}
	if len(report.Results) != 1 || report.Results[0].Status != StatusAuthFailed {
		t.Fatalf("results = %+v", report.Results)
	}
	if !strings.Contains(report.Results[0].Error, "REDACTED") {
		t.Fatalf("error was not redacted: %q", report.Results[0].Error)
	}
	if len(report.Results[0].Error) > 200 {
		t.Fatalf("error snippet is longer than 200 runes: %d", len([]rune(report.Results[0].Error)))
	}
}

func TestRunReportsAuthAndConfigSuggestions(t *testing.T) {
	t.Run("all auth failed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"unauthorized"}}`))
		}))
		defer server.Close()

		target := openAITarget(server.URL)
		target.Declared = declaredProtocols(ProtocolChatCompletions, ProtocolResponses)
		report := Run(context.Background(), target, ProtocolAuto, nil)

		if !hasSuggestion(report.Suggestions, "verify_credentials", "channel") {
			t.Fatalf("suggestions = %+v, want verify_credentials", report.Suggestions)
		}
		if report.Status != ReportStatusFailed {
			t.Fatalf("status = %q, want failed", report.Status)
		}
	})

	t.Run("all model not found", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`))
		}))
		defer server.Close()

		target := openAITarget(server.URL)
		target.Declared = declaredProtocols(ProtocolChatCompletions, ProtocolResponses)
		report := Run(context.Background(), target, ProtocolAuto, nil)

		if !hasSuggestion(report.Suggestions, "resync_models", "channel") {
			t.Fatalf("suggestions = %+v, want resync_models", report.Suggestions)
		}
	})

	t.Run("all endpoint not supported", func(t *testing.T) {
		server := httptest.NewServer(http.NotFoundHandler())
		defer server.Close()

		target := openAITarget(server.URL)
		target.Declared = declaredProtocols(ProtocolChatCompletions, ProtocolResponses)
		report := Run(context.Background(), target, ProtocolAuto, nil)

		if !hasSuggestion(report.Suggestions, "check_channel_config", "channel") {
			t.Fatalf("suggestions = %+v, want check_channel_config", report.Suggestions)
		}
	})
}

func TestRunWithoutDeclaredProtocolsIsUnsupported(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	target.Declared = declaredProtocols()
	report := Run(context.Background(), target, ProtocolAuto, nil)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
	if report.Status != ReportStatusUnsupported || len(report.Results) != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Declared == nil || len(report.Declared) != 4 {
		t.Fatalf("declared = %+v, want all four protocols present", report.Declared)
	}
}

func TestRunCopiesTargetFieldsIntoReport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	target.ChannelID = "chan"
	target.Model = "client-model"
	target.WireModel = "wire-model"
	target.ProtocolFamily = upstream.ProtocolFamilyOpenAICompatible
	target.ModelState = "unverified"
	target.Routable = true
	target.ChannelEnabled = false
	target.ModelEnabled = true
	target.NativeResponses = false
	target.Declared = declaredProtocols(ProtocolChatCompletions)
	report := Run(context.Background(), target, ProtocolAuto, nil)

	if report.ChannelID != "chan" || report.Model != "client-model" || report.WireModel != "wire-model" {
		t.Fatalf("identity = %+v", report)
	}
	if report.ModelState != "unverified" || !report.Routable || report.ChannelEnabled || !report.ModelEnabled {
		t.Fatalf("state fields = %+v", report)
	}
	if report.NativeResponses {
		t.Fatalf("native_responses = true")
	}
	if report.StartedAt.IsZero() || report.CompletedAt.IsZero() || report.DurationMS < 0 {
		t.Fatalf("timing = %+v", report)
	}
	if report.Results == nil || report.Suggestions == nil || report.Warnings == nil {
		t.Fatalf("nil slices in report: %+v", report)
	}
	// The report must not materialize the resolved upstream (which carries the
	// decrypted API key).
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal(report) error = %v", err)
	}
	if strings.Contains(string(encoded), "sk-test-secret") {
		t.Fatalf("API key leaked into the report JSON: %s", encoded)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func hasSuggestion(suggestions []Suggestion, kind, scope string) bool {
	for _, suggestion := range suggestions {
		if suggestion.Kind == kind && suggestion.Scope == scope {
			return true
		}
	}
	return false
}

func TestRunForcedIgnoresDeclaredProtocols(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[]}`))
		case "/v1/responses":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	// The declarations are wrong/empty on purpose: forced detection must not care.
	target.Declared = declaredProtocols()
	report := RunForced(context.Background(), target, []Protocol{ProtocolChatCompletions, ProtocolResponses}, nil)

	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want both protocols probed", got)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results = %+v", report.Results)
	}
	byProtocol := map[Protocol]Result{}
	for _, result := range report.Results {
		byProtocol[result.Protocol] = result
	}
	if byProtocol[ProtocolChatCompletions].Status != StatusOK {
		t.Fatalf("chat result = %+v", byProtocol[ProtocolChatCompletions])
	}
	if byProtocol[ProtocolResponses].Status != StatusEndpointNotSupported {
		t.Fatalf("responses result = %+v", byProtocol[ProtocolResponses])
	}
	if byProtocol[ProtocolChatCompletions].Declared {
		t.Fatalf("forced probe reported declared = true for an undeclared protocol")
	}
	if report.Status != ReportStatusPartial {
		t.Fatalf("status = %q, want partial (chat ok, responses not served)", report.Status)
	}
}

func TestRunForcedCollapsesDuplicatesAndKeepsOrder(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer server.Close()

	target := openAITarget(server.URL)
	report := RunForced(context.Background(), target, []Protocol{
		ProtocolChatCompletions, ProtocolAuto, ProtocolChatCompletions,
	}, nil)
	if len(report.Results) != 1 || len(paths) != 1 {
		t.Fatalf("results = %+v, paths = %v", report.Results, paths)
	}
}

func TestRunForcedFamilyMismatchIsUnsupportedWithoutCalling(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()

	target := familyTarget(server.URL, upstream.ProtocolFamilyAnthropicMessages, upstream.APITypeMessages, upstream.RoutingProfileAnthropicDefault, ProtocolMessages)
	report := RunForced(context.Background(), target, []Protocol{ProtocolChatCompletions, ProtocolResponses}, nil)

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0", got)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results = %+v", report.Results)
	}
	for _, result := range report.Results {
		if result.Status != StatusUnsupportedProtocol {
			t.Fatalf("%s result = %+v", result.Protocol, result)
		}
	}
	if report.Status != ReportStatusUnsupported {
		t.Fatalf("status = %q, want unsupported", report.Status)
	}
}
