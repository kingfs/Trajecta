package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
)

const modelDetectTestProbeAt = "2026-11-01T12:00:00Z"

func newModelDetectTestStore(t *testing.T, baseURL, apiType, protocolFamily, routingProfile, modelResource, capabilitiesJSON string, models []store.ChannelModelRecord) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	probeAt, err := time.Parse(time.RFC3339, modelDetectTestProbeAt)
	if err != nil {
		t.Fatalf("time.Parse() error = %v", err)
	}
	if capabilitiesJSON == "" {
		capabilitiesJSON = "{}"
	}
	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:               "nim",
		Name:             "NVIDIA NIM",
		BaseURL:          baseURL,
		APIType:          apiType,
		ProtocolFamily:   protocolFamily,
		RoutingProfile:   routingProfile,
		ModelResource:    modelResource,
		CapabilitiesJSON: capabilitiesJSON,
		APIKeyCiphertext: []byte("dummy-key"),
		HeadersJSON:      "{}",
		Enabled:          true,
		LastProbeAt:      probeAt,
		LastProbeStatus:  "success",
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	if err := st.ReplaceChannelModels("nim", models); err != nil {
		t.Fatalf("ReplaceChannelModels() error = %v", err)
	}
	return st
}

func modelDetectChatAndResponsesUpstream(t *testing.T, responsesFails bool, responsesCalls *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
		case "/v1/responses":
			if responsesCalls != nil {
				atomic.AddInt32(responsesCalls, 1)
			}
			if responsesFails {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("404 page not found\n"))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","output":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func postProviderModelDetect(t *testing.T, handler http.HandlerFunc, payload string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/provider-model-detect", strings.NewReader(payload)))
	return rr
}

func channelModelRecordFor(t *testing.T, st *store.Store, model string) store.ChannelModelRecord {
	t.Helper()
	models, err := st.ListChannelModels("nim", false)
	if err != nil {
		t.Fatalf("ListChannelModels() error = %v", err)
	}
	for _, record := range models {
		if record.Model == model {
			return record
		}
	}
	t.Fatalf("model %q not found in %+v", model, models)
	return store.ChannelModelRecord{}
}

func TestProviderModelDetectAPIAppliesDecisiveInterfaces(t *testing.T) {
	var responsesCalls int32
	upstreamServer := modelDetectChatAndResponsesUpstream(t, true, &responsesCalls)
	probeAt, _ := time.Parse(time.RFC3339, modelDetectTestProbeAt)
	st := newModelDetectTestStore(t, upstreamServer.URL+"/v1", "chat_completions", "openai_compatible", "", "", "{}", []store.ChannelModelRecord{
		{Model: "deepseek-v4.1-flash", UpstreamModel: "deepseek-ai/deepseek-v4.1-flash", Source: "manual", Enabled: true, LastProbeAt: probeAt},
	})
	svc := channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second})
	handler := providerModelDetectAPIHandler(st, svc, nil)

	rr := postProviderModelDetect(t, handler, `{"channel_id":"nim"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := atomic.LoadInt32(&responsesCalls); got != 1 {
		t.Fatalf("/v1/responses calls = %d, want 1", got)
	}
	if strings.Contains(rr.Body.String(), "dummy-key") {
		t.Fatalf("API key leaked into the response: %s", rr.Body.String())
	}
	var resp providerModelDetectResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v; body = %s", err, rr.Body.String())
	}
	if resp.ChannelID != "nim" || resp.Overwrite || resp.DurationMS < 0 {
		t.Fatalf("response metadata = %+v", resp)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %+v", resp.Items)
	}
	item := resp.Items[0]
	if item.Model != "deepseek-v4.1-flash" || item.WireModel != "deepseek-ai/deepseek-v4.1-flash" || item.ProtocolFamily != "openai_compatible" {
		t.Fatalf("item identity = %+v", item)
	}
	protocols := map[string]providerModelDetectProtocol{}
	for _, protocol := range item.Protocols {
		protocols[protocol.Protocol] = protocol
	}
	if protocols["chat_completions"].Status != "ok" {
		t.Fatalf("chat protocol = %+v", protocols["chat_completions"])
	}
	if protocols["responses"].Status != "endpoint_not_supported" || protocols["responses"].HTTPStatus != http.StatusNotFound {
		t.Fatalf("responses protocol = %+v", protocols["responses"])
	}
	if item.Applied.SupportsChatCompletions == nil || !*item.Applied.SupportsChatCompletions {
		t.Fatalf("applied = %+v, want supports_chat_completions true", item.Applied)
	}
	if item.Applied.SupportsResponses == nil || *item.Applied.SupportsResponses {
		t.Fatalf("applied = %+v, want supports_responses false", item.Applied)
	}
	if item.SkippedReason != "" {
		t.Fatalf("skipped_reason = %q", item.SkippedReason)
	}

	record := channelModelRecordFor(t, st, "deepseek-v4.1-flash")
	if record.SupportsChatCompletions == nil || *record.SupportsChatCompletions == 0 {
		t.Fatalf("supports_chat_completions not persisted: %+v", record.SupportsChatCompletions)
	}
	if record.SupportsResponses == nil || *record.SupportsResponses != 0 {
		t.Fatalf("supports_responses not persisted as false: %+v", record.SupportsResponses)
	}

	// Read-only probe: no probe run, and no channel/model timestamp moved.
	runs, err := st.ListChannelProbeRuns("nim", 10)
	if err != nil {
		t.Fatalf("ListChannelProbeRuns() error = %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("detection wrote channel_probe_runs rows: %+v", runs)
	}
	channelRecord, err := st.GetChannelConfig("nim")
	if err != nil {
		t.Fatalf("GetChannelConfig() error = %v", err)
	}
	if !channelRecord.LastProbeAt.Equal(probeAt) {
		t.Fatalf("channel last_probe_at = %s, want %s", channelRecord.LastProbeAt, probeAt)
	}
	record = channelModelRecordFor(t, st, "deepseek-v4.1-flash")
	if !record.LastProbeAt.Equal(probeAt) {
		t.Fatalf("model last_probe_at = %s, want %s", record.LastProbeAt, probeAt)
	}
}

func TestProviderModelDetectAPINativeModelMarksBothInterfaces(t *testing.T) {
	upstreamServer := modelDetectChatAndResponsesUpstream(t, false, nil)
	st := newModelDetectTestStore(t, upstreamServer.URL+"/v1", "chat_completions", "openai_compatible", "", "", "{}", []store.ChannelModelRecord{
		{Model: "native-model", Source: "manual", Enabled: true},
	})
	handler := providerModelDetectAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), nil)

	rr := postProviderModelDetect(t, handler, `{"channel_id":"nim","model":"native-model"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp providerModelDetectResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	applied := resp.Items[0].Applied
	if applied.SupportsChatCompletions == nil || !*applied.SupportsChatCompletions {
		t.Fatalf("applied = %+v, want chat true", applied)
	}
	if applied.SupportsResponses == nil || !*applied.SupportsResponses {
		t.Fatalf("applied = %+v, want responses true", applied)
	}
	record := channelModelRecordFor(t, st, "native-model")
	if record.SupportsChatCompletions == nil || *record.SupportsChatCompletions == 0 {
		t.Fatalf("supports_chat_completions = %+v", record.SupportsChatCompletions)
	}
	if record.SupportsResponses == nil || *record.SupportsResponses == 0 {
		t.Fatalf("supports_responses = %+v", record.SupportsResponses)
	}
}

func TestProviderModelDetectAPIOverwriteSemantics(t *testing.T) {
	upstreamServer := modelDetectChatAndResponsesUpstream(t, true, nil)
	st := newModelDetectTestStore(t, upstreamServer.URL+"/v1", "chat_completions", "openai_compatible", "", "", "{}", []store.ChannelModelRecord{
		{
			Model:                   "configured-model",
			Source:                  "manual",
			Enabled:                 true,
			SupportsChatCompletions: intPtr(0),
			SupportsResponses:       intPtr(1),
		},
	})
	handler := providerModelDetectAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), nil)

	rr := postProviderModelDetect(t, handler, `{"channel_id":"nim"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var keep providerModelDetectResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &keep); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	item := keep.Items[0]
	if item.Applied.SupportsChatCompletions != nil || item.Applied.SupportsResponses != nil {
		t.Fatalf("overwrite=false wrote over explicit values: %+v", item.Applied)
	}
	if !strings.Contains(item.SkippedReason, "supports_chat_completions is already configured") ||
		!strings.Contains(item.SkippedReason, "supports_responses is already configured") {
		t.Fatalf("skipped_reason = %q", item.SkippedReason)
	}
	record := channelModelRecordFor(t, st, "configured-model")
	if record.SupportsChatCompletions == nil || *record.SupportsChatCompletions != 0 || record.SupportsResponses == nil || *record.SupportsResponses == 0 {
		t.Fatalf("overwrite=false changed the columns: chat=%v responses=%v", record.SupportsChatCompletions, record.SupportsResponses)
	}

	rr = postProviderModelDetect(t, handler, `{"channel_id":"nim","overwrite":true}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("overwrite status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var overwritten providerModelDetectResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &overwritten); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	if !overwritten.Overwrite {
		t.Fatalf("overwrite echo = false")
	}
	item = overwritten.Items[0]
	if item.Applied.SupportsChatCompletions == nil || !*item.Applied.SupportsChatCompletions {
		t.Fatalf("overwrite applied = %+v, want chat true", item.Applied)
	}
	if item.Applied.SupportsResponses == nil || *item.Applied.SupportsResponses {
		t.Fatalf("overwrite applied = %+v, want responses false", item.Applied)
	}
	record = channelModelRecordFor(t, st, "configured-model")
	if record.SupportsChatCompletions == nil || *record.SupportsChatCompletions == 0 {
		t.Fatalf("overwrite did not set chat true: %+v", record.SupportsChatCompletions)
	}
	if record.SupportsResponses == nil || *record.SupportsResponses != 0 {
		t.Fatalf("overwrite did not set responses false: %+v", record.SupportsResponses)
	}
}

func TestProviderModelDetectAPIAuthFailureLeavesInterfacesUnset(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer upstreamServer.Close()
	st := newModelDetectTestStore(t, upstreamServer.URL+"/v1", "chat_completions", "openai_compatible", "", "", "{}", []store.ChannelModelRecord{
		{Model: "auth-model", Source: "manual", Enabled: true},
	})
	handler := providerModelDetectAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), nil)

	rr := postProviderModelDetect(t, handler, `{"channel_id":"nim"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp providerModelDetectResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	item := resp.Items[0]
	if item.Applied.SupportsChatCompletions != nil || item.Applied.SupportsResponses != nil {
		t.Fatalf("auth failure applied values: %+v", item.Applied)
	}
	if !strings.Contains(item.SkippedReason, "inconclusive") {
		t.Fatalf("skipped_reason = %q", item.SkippedReason)
	}
	for _, protocol := range item.Protocols {
		if protocol.Status != "auth_failed" {
			t.Fatalf("protocol = %+v, want auth_failed", protocol)
		}
	}
	record := channelModelRecordFor(t, st, "auth-model")
	if record.SupportsChatCompletions != nil || record.SupportsResponses != nil {
		t.Fatalf("auth failure wrote columns: chat=%v responses=%v", record.SupportsChatCompletions, record.SupportsResponses)
	}
}

func TestProviderModelDetectAPITimeoutLeavesInterfacesUnset(t *testing.T) {
	previous := providerModelDetectProtocolTimeout
	providerModelDetectProtocolTimeout = 50 * time.Millisecond
	t.Cleanup(func() { providerModelDetectProtocolTimeout = previous })

	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstreamServer.Close()
	st := newModelDetectTestStore(t, upstreamServer.URL+"/v1", "chat_completions", "openai_compatible", "", "", "{}", []store.ChannelModelRecord{
		{Model: "slow-model", Source: "manual", Enabled: true},
	})
	handler := providerModelDetectAPIHandler(st, channel.NewService(st), nil)

	rr := postProviderModelDetect(t, handler, `{"channel_id":"nim"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp providerModelDetectResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	item := resp.Items[0]
	if item.Applied.SupportsChatCompletions != nil || item.Applied.SupportsResponses != nil {
		t.Fatalf("timeout applied values: %+v", item.Applied)
	}
	for _, protocol := range item.Protocols {
		if protocol.Status != "timeout" {
			t.Fatalf("protocol = %+v, want timeout", protocol)
		}
	}
	record := channelModelRecordFor(t, st, "slow-model")
	if record.SupportsChatCompletions != nil || record.SupportsResponses != nil {
		t.Fatalf("timeout wrote columns: chat=%v responses=%v", record.SupportsChatCompletions, record.SupportsResponses)
	}
}

func TestProviderModelDetectAPINonOpenAIFamilyIsReportOnly(t *testing.T) {
	var gotPath string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg-1","content":[{"type":"text","text":"pong"}]}`))
	}))
	defer upstreamServer.Close()
	st := newModelDetectTestStore(t, upstreamServer.URL, "messages", "anthropic_messages", "", "", "{}", []store.ChannelModelRecord{
		{Model: "claude-test", Source: "manual", Enabled: true},
	})
	handler := providerModelDetectAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), nil)

	rr := postProviderModelDetect(t, handler, `{"channel_id":"nim"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if gotPath != "/v1/messages" {
		t.Fatalf("path = %q, want /v1/messages", gotPath)
	}
	var resp providerModelDetectResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	item := resp.Items[0]
	if item.ProtocolFamily != "anthropic_messages" || len(item.Protocols) != 1 || item.Protocols[0].Protocol != "messages" {
		t.Fatalf("item = %+v", item)
	}
	if item.Protocols[0].Status != "ok" {
		t.Fatalf("protocol = %+v", item.Protocols[0])
	}
	if item.Applied.SupportsChatCompletions != nil || item.Applied.SupportsResponses != nil {
		t.Fatalf("report-only family applied columns: %+v", item.Applied)
	}
	if !strings.Contains(item.SkippedReason, "report-only") {
		t.Fatalf("skipped_reason = %q", item.SkippedReason)
	}
}

func TestProviderModelDetectAPIReloadsRouter(t *testing.T) {
	upstreamServer := modelDetectChatAndResponsesUpstream(t, false, nil)
	st := newModelDetectTestStore(t, upstreamServer.URL+"/v1", "chat_completions", "openai_compatible", "", "", "{}", []store.ChannelModelRecord{
		{Model: "gpt-5", Source: "manual", Enabled: true},
	})
	svc := channel.NewService(st)
	targets, err := svc.RuntimeTargets()
	if err != nil {
		t.Fatalf("RuntimeTargets() error = %v", err)
	}
	rtr, err := router.New(&config.Config{Upstreams: targets})
	if err != nil {
		t.Fatalf("router.New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	t.Cleanup(rtr.Close)
	handler := providerModelDetectAPIHandler(st, svc.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), rtr)

	rr := postProviderModelDetect(t, handler, `{"channel_id":"nim"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var found bool
	for _, target := range rtr.Targets() {
		if target.ChannelID != "nim" {
			continue
		}
		found = true
		enabled, configured := target.Upstream.ModelCapability("gpt-5", upstream.CapabilityResponses)
		if !configured || !enabled {
			t.Fatalf("reloaded target responses capability = enabled %v configured %v, want true/true", enabled, configured)
		}
	}
	if !found {
		t.Fatalf("router has no target for the channel: %+v", rtr.Targets())
	}
}

func TestProviderModelDetectAPIRejectsBadRequests(t *testing.T) {
	upstreamServer := modelDetectChatAndResponsesUpstream(t, false, nil)
	st := newModelDetectTestStore(t, upstreamServer.URL+"/v1", "chat_completions", "openai_compatible", "", "", "{}", []store.ChannelModelRecord{
		{Model: "m", Source: "manual", Enabled: true},
	})
	handler := providerModelDetectAPIHandler(st, channel.NewService(st).WithHTTPClient(&http.Client{Timeout: 5 * time.Second}), nil)

	for _, tc := range []struct {
		name       string
		method     string
		payload    string
		statusCode int
	}{
		{"get", http.MethodGet, "", http.StatusMethodNotAllowed},
		{"invalid json", http.MethodPost, `{"channel_id":`, http.StatusBadRequest},
		{"missing channel", http.MethodPost, `{"model":"m"}`, http.StatusBadRequest},
		{"unknown channel", http.MethodPost, `{"channel_id":"nope"}`, http.StatusNotFound},
		{"unknown model", http.MethodPost, `{"channel_id":"nim","model":"nope"}`, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(method, "/api/provider-model-detect", strings.NewReader(tc.payload)))
			if rr.Code != tc.statusCode {
				t.Fatalf("status = %d, want %d; body = %s", rr.Code, tc.statusCode, rr.Body.String())
			}
			if tc.statusCode == http.StatusMethodNotAllowed && rr.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow = %q", rr.Header().Get("Allow"))
			}
		})
	}
}

func TestProviderModelDetectAPIMapsStoreConfigErrors(t *testing.T) {
	t.Run("unresolvable channel config is 500", func(t *testing.T) {
		st := newModelDetectTestStore(t, "https://example.invalid/v1", "chat_completions", "not-a-family", "", "", "{}", []store.ChannelModelRecord{
			{Model: "m", Source: "manual", Enabled: true},
		})
		handler := providerModelDetectAPIHandler(st, channel.NewService(st), nil)
		rr := postProviderModelDetect(t, handler, `{"channel_id":"nim"}`)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500; body = %s", rr.Code, rr.Body.String())
		}
	})
}

func TestChannelModelAPIInterfacesForIncidentShape(t *testing.T) {
	st := newModelDetectTestStore(t, "https://integrate.api.nvidia.com/v1", "chat_completions", "openai_compatible", "", "", `{"responses":true,"chat_completions":true}`, []store.ChannelModelRecord{
		{Model: "deepseek-v4.1-flash", Source: "manual", Enabled: true},
	})

	rr := httptest.NewRecorder()
	channelDetailAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/channels/nim/models", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("models status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var modelsResp channelModelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &modelsResp); err != nil {
		t.Fatalf("json.Unmarshal(models) error = %v", err)
	}
	if len(modelsResp.Items) != 1 {
		t.Fatalf("items = %+v", modelsResp.Items)
	}
	interfaces := modelsResp.Items[0].Interfaces
	want := channel.ModelInterfaces{ChatCompletions: true, NativeResponses: false, ResponsesViaChat: true}
	if interfaces != want {
		t.Fatalf("interfaces = %+v, want %+v", interfaces, want)
	}

	rr = httptest.NewRecorder()
	channelDetailAPIHandler(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/channels/nim", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var detail channelItem
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("json.Unmarshal(detail) error = %v", err)
	}
	if !slices.Contains(detail.Interfaces, "chat_completions") || !slices.Contains(detail.Interfaces, "responses_via_chat") {
		t.Fatalf("channel interfaces = %+v, want chat_completions and responses_via_chat", detail.Interfaces)
	}
	if slices.Contains(detail.Interfaces, "native_responses") {
		t.Fatalf("channel interfaces = %+v, must not claim native responses", detail.Interfaces)
	}
}

func TestEnrichModelChannelItemInterfacesModelDetail(t *testing.T) {
	channelRecord := store.ChannelConfigRecord{
		BaseURL:          "https://integrate.api.nvidia.com/v1",
		APIType:          "chat_completions",
		ProtocolFamily:   "openai_compatible",
		CapabilitiesJSON: `{"responses":true,"chat_completions":true}`,
	}
	var item modelChannelItem
	enrichModelChannelItemFromRecord(&item, channelRecord, store.ChannelModelRecord{Model: "deepseek-v4.1-flash", Enabled: true})
	want := channel.ModelInterfaces{ChatCompletions: true, ResponsesViaChat: true}
	if item.Interfaces != want {
		t.Fatalf("models_usage interfaces = %+v, want %+v", item.Interfaces, want)
	}

	native := 1
	item = modelChannelItem{}
	enrichModelChannelItemFromRecord(&item, channelRecord, store.ChannelModelRecord{Model: "native-model", Enabled: true, SupportsResponses: &native})
	if !item.Interfaces.NativeResponses || item.Interfaces.ResponsesViaChat {
		t.Fatalf("native override interfaces = %+v", item.Interfaces)
	}
}
