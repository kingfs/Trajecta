package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/modelprobe"
	"github.com/kingfs/Trajecta/internal/store"
)

// modelProbeTestUpstream returns an httptest upstream that serves Chat
// Completions and answers /v1/responses with the plain-text 404 NVIDIA NIM
// returns for a route it does not serve.
func modelProbeTestUpstream(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()
	var responsesCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			if got := r.Header.Get("Authorization"); got != "Bearer dummy-key" {
				t.Errorf("Authorization = %q, want the decrypted channel key", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode chat body: %v", err)
			}
			if body["model"] != "deepseek-ai/deepseek-v4.1-flash" {
				t.Errorf("chat body model = %v, want the wire model", body["model"])
			}
			if body["max_tokens"] != float64(16) || body["stream"] != false {
				t.Errorf("chat body = %v", body)
			}
			messages, _ := body["messages"].([]any)
			if len(messages) != 1 {
				t.Errorf("chat body messages = %v", body["messages"])
			} else if message, _ := messages[0].(map[string]any); message["content"] != "ping" {
				t.Errorf("chat body message = %v", message)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"}}]}`))
		case "/v1/responses":
			atomic.AddInt32(&responsesCalls, 1)
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("404 page not found\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &responsesCalls
}

func newModelProbeTestStore(t *testing.T, baseURL string) (*store.Store, time.Time) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	probeAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "nvidia-nim",
		Name:             "NVIDIA NIM",
		BaseURL:          baseURL,
		ProviderPreset:   "nvidia_nim",
		APIType:          "chat_completions",
		ProtocolFamily:   "openai_compatible",
		APIKeyCiphertext: []byte("dummy-key"),
		APIKeyHint:       "dum...-key",
		HeadersJSON:      "{}",
		Enabled:          true,
		LastProbeAt:      probeAt,
		LastProbeStatus:  "success",
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("nvidia-nim", []store.ChannelModelRecord{
		{
			Model:             "deepseek-v4.1-flash",
			UpstreamModel:     "deepseek-ai/deepseek-v4.1-flash",
			Source:            "manual",
			Enabled:           true,
			SupportsResponses: intPtr(1),
			LastProbeAt:       probeAt,
		},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}
	return st, probeAt
}

func postProviderModelTest(t *testing.T, handler http.HandlerFunc, payload string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/provider-model-test", strings.NewReader(payload)))
	return rr
}

func TestProviderModelTestAPIProbesModelPerProtocol(t *testing.T) {
	upstreamServer, responsesCalls := modelProbeTestUpstream(t)
	st, channelProbeAt := newModelProbeTestStore(t, upstreamServer.URL+"/v1")
	svc := channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second})
	handler := providerModelTestAPIHandler(st, svc)

	rr := postProviderModelTest(t, handler, `{"channel_id":"nvidia-nim","model":"deepseek-v4.1-flash","protocol":"auto"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt32(responsesCalls); got != 1 {
		t.Fatalf("/v1/responses calls = %d, want 1", got)
	}
	if strings.Contains(rr.Body.String(), "dummy-key") {
		t.Fatalf("API key leaked into the response: %s", rr.Body.String())
	}

	var report modelprobe.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal(report) error = %v; body = %s", err, rr.Body.String())
	}
	if report.ChannelID != "nvidia-nim" || report.Model != "deepseek-v4.1-flash" || report.WireModel != "deepseek-ai/deepseek-v4.1-flash" {
		t.Fatalf("identity = %+v", report)
	}
	if report.ProtocolFamily != "openai_compatible" {
		t.Fatalf("protocol_family = %q", report.ProtocolFamily)
	}
	if !report.ChannelEnabled || !report.ModelEnabled || report.ModelState != string(channel.ModelStateUnverified) || !report.Routable {
		t.Fatalf("state fields = %+v", report)
	}
	if !report.Declared[modelprobe.ProtocolChatCompletions] || !report.Declared[modelprobe.ProtocolResponses] {
		t.Fatalf("declared = %+v", report.Declared)
	}
	if !report.NativeResponses {
		t.Fatalf("native_responses = false, want the model override to be native")
	}
	if report.Status != "partial" {
		t.Fatalf("status = %q, want partial; results = %+v", report.Status, report.Results)
	}
	if report.StartedAt.IsZero() || report.CompletedAt.IsZero() || report.CompletedAt.Before(report.StartedAt) {
		t.Fatalf("timing = %+v", report)
	}

	byProtocol := map[modelprobe.Protocol]modelprobe.Result{}
	for _, result := range report.Results {
		byProtocol[result.Protocol] = result
	}
	if byProtocol[modelprobe.ProtocolChatCompletions].Status != modelprobe.StatusOK {
		t.Fatalf("chat result = %+v", byProtocol[modelprobe.ProtocolChatCompletions])
	}
	responses := byProtocol[modelprobe.ProtocolResponses]
	if responses.Status != modelprobe.StatusEndpointNotSupported || responses.HTTPStatus != http.StatusNotFound {
		t.Fatalf("responses result = %+v, want plain-text 404 classified as endpoint_not_supported", responses)
	}
	if !strings.Contains(responses.URL, "/v1/responses") || responses.Method != http.MethodPost {
		t.Fatalf("responses url/method = %q/%q", responses.URL, responses.Method)
	}
	if len(report.Suggestions) != 1 {
		t.Fatalf("suggestions = %+v", report.Suggestions)
	}
	suggestion := report.Suggestions[0]
	if suggestion.Kind != "disable_capability" || suggestion.Capability != "responses" || suggestion.Scope != "model" {
		t.Fatalf("suggestion = %+v", suggestion)
	}

	// The probe is read-only: no probe run row, and no channel/model timestamp moved.
	runs, err := st.ListChannelProbeRuns("nvidia-nim", 10)
	if err != nil {
		t.Fatalf("ListChannelProbeRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("probe wrote channel_probe_runs rows: %+v", runs)
	}
	channelRecord, err := st.GetChannelConfig("nvidia-nim")
	if err != nil {
		t.Fatalf("GetChannelConfig() error = %v", err)
	}
	if !channelRecord.LastProbeAt.Equal(channelProbeAt) {
		t.Fatalf("channel last_probe_at = %s, want %s", channelRecord.LastProbeAt, channelProbeAt)
	}
	models, err := st.ListChannelModels("nvidia-nim", false)
	if err != nil {
		t.Fatalf("ListChannelModels() error = %v", err)
	}
	if len(models) != 1 || !models[0].LastProbeAt.Equal(channelProbeAt) {
		t.Fatalf("channel_models last_probe_at changed: %+v", models)
	}
}

func TestProviderModelTestAPIReportsDeclaredResponsesWithoutNativePassThrough(t *testing.T) {
	// The NVIDIA NIM incident: the channel advertises both capabilities, its api_type is
	// chat_completions and the model has no override. The native path must report
	// native_responses=false while still probing the declared Responses route, because
	// that route is what the operator needs to see is broken.
	upstreamServer, responsesCalls := modelProbeTestUpstream(t)
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "nvidia-nim",
		Name:             "NVIDIA NIM",
		BaseURL:          upstreamServer.URL + "/v1",
		ProviderPreset:   "nvidia_nim",
		APIType:          "chat_completions",
		ProtocolFamily:   "openai_compatible",
		CapabilitiesJSON: `{"responses":true,"chat_completions":true}`,
		APIKeyCiphertext: []byte("dummy-key"),
		HeadersJSON:      "{}",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	// No SupportsResponses override: the declaration comes from the channel.
	if err := st.ReplaceChannelModels("nvidia-nim", []store.ChannelModelRecord{
		{Model: "deepseek-v4.1-flash", UpstreamModel: "deepseek-ai/deepseek-v4.1-flash", Source: "manual", Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}
	handler := providerModelTestAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))

	rr := postProviderModelTest(t, handler, `{"channel_id":"nvidia-nim","model":"deepseek-v4.1-flash","protocol":"auto"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt32(responsesCalls); got != 1 {
		t.Fatalf("/v1/responses calls = %d, want 1 even though the native path is not eligible", got)
	}
	var report modelprobe.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal(report) error = %v", err)
	}
	if report.NativeResponses {
		t.Fatalf("native_responses = true, want false")
	}
	if !report.Declared[modelprobe.ProtocolResponses] {
		t.Fatalf("declared = %+v, want Responses declared from the channel capability", report.Declared)
	}
	byProtocol := map[modelprobe.Protocol]modelprobe.Result{}
	for _, result := range report.Results {
		byProtocol[result.Protocol] = result
	}
	if byProtocol[modelprobe.ProtocolChatCompletions].Status != modelprobe.StatusOK {
		t.Fatalf("chat result = %+v", byProtocol[modelprobe.ProtocolChatCompletions])
	}
	if byProtocol[modelprobe.ProtocolResponses].Status != modelprobe.StatusEndpointNotSupported {
		t.Fatalf("responses result = %+v", byProtocol[modelprobe.ProtocolResponses])
	}
	if len(report.Warnings) == 0 || !strings.Contains(strings.Join(report.Warnings, " "), "not used natively") {
		t.Fatalf("warnings = %+v, want the native-runtime warning", report.Warnings)
	}
	if len(report.Suggestions) != 1 || report.Suggestions[0].Kind != "disable_capability" || report.Suggestions[0].Capability != "responses" {
		t.Fatalf("suggestions = %+v", report.Suggestions)
	}
}

func TestProviderModelTestAPIExplicitProtocolOnlyProbesThatSurface(t *testing.T) {
	upstreamServer, responsesCalls := modelProbeTestUpstream(t)
	// The chat handler asserts the Authorization header, so an explicit Responses
	// probe must not accidentally hit the chat path.
	st, _ := newModelProbeTestStore(t, upstreamServer.URL+"/v1")
	handler := providerModelTestAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))

	rr := postProviderModelTest(t, handler, `{"channel_id":"nvidia-nim","model":"deepseek-v4.1-flash","protocol":"responses"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var report modelprobe.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal(report) error = %v", err)
	}
	if len(report.Results) != 1 || report.Results[0].Protocol != modelprobe.ProtocolResponses {
		t.Fatalf("results = %+v, want only responses", report.Results)
	}
	if report.Results[0].Status != modelprobe.StatusEndpointNotSupported {
		t.Fatalf("responses status = %q", report.Results[0].Status)
	}
	if got := atomic.LoadInt32(responsesCalls); got != 1 {
		t.Fatalf("/v1/responses calls = %d, want 1", got)
	}
	if report.Status != "failed" {
		t.Fatalf("status = %q, want failed", report.Status)
	}
}

func TestProviderModelTestAPIRejectsBadRequests(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstreamServer.Close()
	st, _ := newModelProbeTestStore(t, upstreamServer.URL+"/v1")
	handler := providerModelTestAPIHandler(st, channel.NewService(st))

	for _, tc := range []struct {
		name       string
		method     string
		payload    string
		statusCode int
	}{
		{"get", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"invalid json", http.MethodPost, `{"channel_id":`, http.StatusBadRequest},
		{"missing channel", http.MethodPost, `{"model":"deepseek-v4.1-flash"}`, http.StatusBadRequest},
		{"missing model", http.MethodPost, `{"channel_id":"nvidia-nim"}`, http.StatusBadRequest},
		{"invalid protocol", http.MethodPost, `{"channel_id":"nvidia-nim","model":"deepseek-v4.1-flash","protocol":"grpc"}`, http.StatusBadRequest},
		{"unknown channel", http.MethodPost, `{"channel_id":"nope","model":"deepseek-v4.1-flash"}`, http.StatusNotFound},
		{"unknown model", http.MethodPost, `{"channel_id":"nvidia-nim","model":"nope"}`, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, "/api/provider-model-test", strings.NewReader(tc.payload))
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.statusCode {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, tc.statusCode, rr.Body.String())
			}
			if tc.statusCode != http.StatusMethodNotAllowed && !strings.Contains(rr.Body.String(), `"error"`) {
				t.Fatalf("error body = %s", rr.Body.String())
			}
			if tc.statusCode == http.StatusMethodNotAllowed && rr.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow = %q", rr.Header().Get("Allow"))
			}
		})
	}
}

func TestProviderModelTestAPIMapsUnresolvableConfigToBadRequest(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "broken",
		Name:           "Broken",
		BaseURL:        "https://example.invalid/v1",
		APIType:        "chat_completions",
		ProtocolFamily: "not-a-protocol-family",
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("broken", []store.ChannelModelRecord{{Model: "m", Source: "manual", Enabled: true}}); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}
	handler := providerModelTestAPIHandler(st, channel.NewService(st))

	rr := postProviderModelTest(t, handler, `{"channel_id":"broken","model":"m"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestProviderModelTestAPILogicalFailureIsTwoHundred(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer upstreamServer.Close()
	st, _ := newModelProbeTestStore(t, upstreamServer.URL+"/v1")
	handler := providerModelTestAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))

	rr := postProviderModelTest(t, handler, `{"channel_id":"nvidia-nim","model":"deepseek-v4.1-flash","protocol":"chat_completions"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for a logical probe failure; body = %s", rr.Code, rr.Body.String())
	}
	var report modelprobe.Report
	if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil {
		t.Fatalf("json.Unmarshal(report) error = %v", err)
	}
	if report.Status != "failed" || len(report.Results) != 1 || report.Results[0].Status != modelprobe.StatusAuthFailed {
		t.Fatalf("report = %+v", report)
	}
}

func TestProviderModelTestAPIRequiresChannelService(t *testing.T) {
	rr := postProviderModelTest(t, providerModelTestAPIHandler(nil, nil), `{"channel_id":"a","model":"m"}`)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
