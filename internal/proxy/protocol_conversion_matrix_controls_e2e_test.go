package proxy

// Routing-contract controls around the conversion matrix: native preference
// under cost pressure, the streaming rejection contract, the /v1/responses
// entrypoint boundary, and a chat->messages tool round trip (the reverse
// direction of the messages->chat loop already pinned in
// protocol_conversion_test.go).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

func convMatrixFloatPtr(v float64) *float64 { return &v }

// TestHandlerConversionNativeWinsEvenWhenCheaper pins the hard
// passthrough-first rule at the wiring level under cost pressure: the native
// chat target is warmed until its measured latency cost is clearly above the
// idle conversion candidate's cost floor, epsilon is zero and priority mode
// is the default cost-first compat - yet the native target must still win
// every request and the conversion candidate must never be contacted.
func TestHandlerConversionNativeWinsEvenWhenCheaper(t *testing.T) {
	native := newConvMatrixUpstream(t, protocolconvert.ProtocolChatCompletions)
	converter := newConvMatrixUpstream(t, protocolconvert.ProtocolMessages)

	proxyURL, _ := newConvMatrixHandlerCfg(t, func(cfg *config.Config) {
		cfg.Router.Selection.Epsilon = convMatrixFloatPtr(0)
		cfg.Router.Selection.Sticky = boolPtr(false)
		// PriorityMode stays the default "compat": cost first, so without
		// the hard native preference the idle converter would win below.
	}, native.convMatrixTarget("native-chat"), converter.convMatrixTarget("converter-messages"))

	// Warm the native target with slow successes so its latency EWMA (and
	// therefore expectedCost) rises above the idle converter's MinCostFloor.
	native.setDelay(200 * time.Millisecond)
	clientBody := convMatrixClientBody(protocolconvert.ProtocolChatCompletions, false)
	for i := 0; i < 3; i++ {
		status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolChatCompletions,
			convMatrixClientPath(protocolconvert.ProtocolChatCompletions, convMatrixModel), clientBody)
		if status != http.StatusOK {
			t.Fatalf("warmup %d: status = %d, want 200; body=%s", i, status, body)
		}
	}
	if got := converter.calls.Load(); got != 0 {
		t.Fatalf("converter was contacted %d times during native warmup", got)
	}

	// The measured request: the converter is now strictly cheaper, but the
	// native candidate is healthy, so it must still be selected and the
	// bytes must pass through untouched.
	status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolChatCompletions,
		convMatrixClientPath(protocolconvert.ProtocolChatCompletions, convMatrixModel), clientBody)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if got := native.calls.Load(); got != 4 {
		t.Fatalf("native calls = %d, want 4 (3 warmup + 1 measured)", got)
	}
	if got := converter.calls.Load(); got != 0 {
		t.Fatalf("converter calls = %d, want 0: native must win even when conversion is cheaper", got)
	}
	_, wireBody, _ := native.snapshot()
	if string(wireBody) != clientBody {
		t.Fatalf("native selection rewrote the body (conversion happened):\n got %s\nwant %s", wireBody, clientBody)
	}
	if !strings.Contains(string(body), `"object":"chat.completion"`) {
		t.Fatalf("client response is not the native chat shape: %s", body)
	}
}

// TestHandlerConversionStreamingRejected walks every cross-protocol pair with
// a streaming request and no native candidate: the router must refuse the
// selection (502, zero upstream calls) instead of buffering a stream through
// the non-streaming converter. Gemini signals streaming in the request line
// (:streamGenerateContent), chat/messages in the body.
func TestHandlerConversionStreamingRejected(t *testing.T) {
	entries := []protocolconvert.Protocol{
		protocolconvert.ProtocolChatCompletions,
		protocolconvert.ProtocolMessages,
		protocolconvert.ProtocolGemini,
	}
	targets := []protocolconvert.Protocol{
		protocolconvert.ProtocolChatCompletions,
		protocolconvert.ProtocolResponses,
		protocolconvert.ProtocolMessages,
		protocolconvert.ProtocolGemini,
	}
	for _, client := range entries {
		for _, target := range targets {
			if client == target {
				continue
			}
			t.Run(fmt.Sprintf("%s_to_%s", client, target), func(t *testing.T) {
				up := newConvMatrixUpstream(t, target)
				proxyURL, _ := newConvMatrixHandler(t, up.convMatrixTarget("only-"+string(target)))

				path := convMatrixClientPath(client, convMatrixModel)
				if client == protocolconvert.ProtocolGemini {
					path = "/v1beta/models/" + convMatrixModel + ":streamGenerateContent"
				}
				status, body, _ := doConvMatrixRequest(t, proxyURL, client, path, convMatrixClientBody(client, true))
				if status != http.StatusBadGateway {
					t.Fatalf("status = %d, want 502 (streaming conversion is unsupported); body=%s", status, body)
				}
				if got := up.calls.Load(); got != 0 {
					t.Fatalf("streaming request reached the upstream %d times; it must be rejected before any call", got)
				}
			})
		}
	}
}

// TestHandlerConversionGeminiAltSSERejected pins the second Gemini streaming
// spelling: ?alt=sse on generateContent looks non-streaming to the router
// (the stream flag lives in the query, not the body), so the conversion gate
// itself must reject it with the 400 protocol_conversion_unsupported contract
// before any upstream call.
func TestHandlerConversionGeminiAltSSERejected(t *testing.T) {
	up := newConvMatrixUpstream(t, protocolconvert.ProtocolChatCompletions)
	proxyURL, _ := newConvMatrixHandler(t, up.convMatrixTarget("chat-only"))

	path := convMatrixClientPath(protocolconvert.ProtocolGemini, convMatrixModel) + "?alt=sse"
	status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolGemini, path,
		convMatrixClientBody(protocolconvert.ProtocolGemini, false))
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", status, body)
	}
	// The Gemini entrypoint renders proxy errors in the Gemini envelope
	// (numeric code + INVALID_ARGUMENT), so the symbolic
	// protocol_conversion_unsupported code is not in the body; the message
	// must still name the alt=sse rejection.
	if !strings.Contains(string(body), "alt=sse") || !strings.Contains(string(body), "cannot be converted") {
		t.Fatalf("body = %s, want the alt=sse conversion rejection message", body)
	}
	if got := up.calls.Load(); got != 0 {
		t.Fatalf("alt=sse request reached the upstream %d times", got)
	}
}

// TestHandlerConversionStreamingNativeControl is the streaming counterpart of
// the native-preference rule: with both a native and a convertible candidate,
// a streaming request must pass through the native target as SSE and never
// touch the conversion candidate.
func TestHandlerConversionStreamingNativeControl(t *testing.T) {
	native := newConvMatrixUpstream(t, protocolconvert.ProtocolChatCompletions)
	converter := newConvMatrixUpstream(t, protocolconvert.ProtocolMessages)
	proxyURL, _ := newConvMatrixHandler(t, native.convMatrixTarget("native-chat"), converter.convMatrixTarget("converter-messages"))

	status, body, header := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolChatCompletions,
		convMatrixClientPath(protocolconvert.ProtocolChatCompletions, convMatrixModel),
		convMatrixClientBody(protocolconvert.ProtocolChatCompletions, true))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(string(body), "chat.completion.chunk") || !strings.Contains(string(body), convMatrixPong) {
		t.Fatalf("stream body is not the native chat SSE: %s", body)
	}
	if got := native.calls.Load(); got != 1 {
		t.Fatalf("native calls = %d, want 1", got)
	}
	if got := converter.calls.Load(); got != 0 {
		t.Fatalf("converter calls = %d, want 0: streaming must never convert", got)
	}
}

// TestHandlerResponsesEntrypointNoForwardConversion extends the boundary
// pinned by TestResponsesEntrypointNeverConverts to the Gemini surface and to
// the local-runtime fallback: /v1/responses never enters the forwarding
// conversion path, with or without a usable backend.
func TestHandlerResponsesEntrypointNoForwardConversion(t *testing.T) {
	responsesBody := `{"model":"` + convMatrixModel + `","input":"` + convMatrixPing + `"}`

	t.Run("gemini_only_rejected_without_contact", func(t *testing.T) {
		up := newConvMatrixUpstream(t, protocolconvert.ProtocolGemini)
		proxyURL, _ := newConvMatrixHandler(t, up.convMatrixTarget("gemini-only"))

		status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolChatCompletions, "/v1/responses", responsesBody)
		if status != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502; body=%s", status, body)
		}
		if got := up.calls.Load(); got != 0 {
			t.Fatalf("the Responses entrypoint was converted and sent to a Gemini-only target %d times", got)
		}
	})

	t.Run("chat_only_uses_local_runtime_not_forward_conversion", func(t *testing.T) {
		up := newConvMatrixUpstream(t, protocolconvert.ProtocolChatCompletions)
		proxyURL, outputDir := newConvMatrixHandler(t, up.convMatrixTarget("chat-only"))

		status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolChatCompletions, "/v1/responses", responsesBody)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200 (local Responses runtime); body=%s", status, body)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("client response is not JSON: %v (%s)", err, body)
		}
		if payload["object"] != "response" {
			t.Fatalf("client object = %v, want the Responses envelope", payload["object"])
		}
		if !strings.Contains(string(body), convMatrixPong) {
			t.Fatalf("local runtime response lost the text: %s", body)
		}
		if got := up.calls.Load(); got == 0 {
			t.Fatal("the local runtime never called its chat backend")
		}
		path, _, _ := up.snapshot()
		if path != "/v1/chat/completions" {
			t.Fatalf("upstream path = %q, want the runtime's own /v1/chat/completions orchestration", path)
		}
		// The cassette must attribute the exchange to the local runtime and
		// carry no forwarding-path conversion event.
		recordPath := waitForRecordedHTTPByEndpoint(t, outputDir, "/v1/responses", 2*time.Second)
		parsed, err := waitForRecordedPrelude(recordPath, 2*time.Second)
		if err != nil {
			t.Fatalf("waitForRecordedPrelude(%q) error = %v", recordPath, err)
		}
		plan := routePlanAttrsFromPrelude(t, parsed)
		if plan["execution_mode"] != "responses_server" {
			t.Fatalf("execution_mode = %v, want responses_server", plan["execution_mode"])
		}
		if hasRecordEvent(parsed.Events, "protocol_conversion") {
			t.Fatalf("the local Responses runtime path recorded a forwarding protocol_conversion event: %+v", parsed.Events)
		}
	})
}

// TestHandlerChatToMessagesToolRoundTrip is the tool-calling bonus pair in the
// direction the existing tests do not cover: an OpenAI chat client declares
// tools and consumes tool_calls through a Messages-only upstream. Turn 1 must
// reach the upstream as Anthropic input_schema tools and come back as chat
// tool_calls; turn 2 must carry the tool result back as a tool_result block.
func TestHandlerChatToMessagesToolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var firstBody, secondBody string
	var firstPath, secondPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		if strings.Contains(string(raw), "tool_result") {
			secondBody = string(raw)
			secondPath = r.URL.Path
		} else {
			firstBody = string(raw)
			firstPath = r.URL.Path
		}
		mu.Unlock()
		if r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"messages-only upstream"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(raw), "tool_result") {
			_, _ = io.WriteString(w, `{"id":"msg_t2","type":"message","role":"assistant","model":"`+convMatrixModel+`","content":[{"type":"text","text":"It is sunny"}],"stop_reason":"end_turn","usage":{"input_tokens":12,"output_tokens":4}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg_t1","type":"message","role":"assistant","model":"`+convMatrixModel+`","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"SH"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer upstream.Close()

	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{Upstreams: []config.UpstreamTargetConfig{{
		ID:             "messages-only",
		Enabled:        boolPtr(true),
		Priority:       100,
		ModelDiscovery: router.ModelDiscoveryStaticOnly,
		StaticModels:   []string{convMatrixModel},
		Upstream: config.UpstreamConfig{
			BaseURL:        upstream.URL,
			ApiKey:         convMatrixSecret,
			ProviderPreset: "anthropic",
			APIType:        "messages",
		},
	}}}
	cfg.Debug.OutputDir = outputDir
	cfg.Router.Selection.ProtocolConversion = true

	handler, err := NewHandlerWithAuth(cfg, st, nil, nil)
	if err != nil {
		t.Fatalf("NewHandlerWithAuth() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	// Turn 1: chat client declares a function tool.
	firstClientBody := `{"model":"` + convMatrixModel + `","messages":[{"role":"user","content":"What is the weather?"}],"tools":[{"type":"function","function":{"name":"weather","description":"d","parameters":{"type":"object"}}}]}`
	status, body, _ := doConvMatrixRequest(t, proxyServer.URL, protocolconvert.ProtocolChatCompletions, "/v1/chat/completions", firstClientBody)
	if status != http.StatusOK {
		t.Fatalf("turn 1: status = %d, want 200; body=%s", status, body)
	}
	mu.Lock()
	upFirstBody, upFirstPath := firstBody, firstPath
	mu.Unlock()
	if upFirstPath != "/v1/messages" {
		t.Fatalf("turn 1 upstream path = %q, want /v1/messages", upFirstPath)
	}
	var upTools struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal([]byte(upFirstBody), &upTools); err != nil {
		t.Fatalf("turn 1 upstream body is not JSON: %v (%s)", err, upFirstBody)
	}
	if len(upTools.Tools) != 1 || upTools.Tools[0].Name != "weather" || upTools.Tools[0].InputSchema == nil {
		t.Fatalf("turn 1 upstream tools are not Anthropic input_schema declarations: %s", upFirstBody)
	}
	if strings.Contains(upFirstBody, `"function"`) {
		t.Fatalf("turn 1 upstream body kept the chat nested function wrapper: %s", upFirstBody)
	}

	var turn1 struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &turn1); err != nil {
		t.Fatalf("turn 1 client response is not JSON: %v (%s)", err, body)
	}
	if len(turn1.Choices) != 1 || turn1.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("turn 1 client response is not a chat tool_calls completion: %s", body)
	}
	calls := turn1.Choices[0].Message.ToolCalls
	if len(calls) != 1 || calls[0].ID == "" || calls[0].Function.Name != "weather" || !strings.Contains(calls[0].Function.Arguments, "SH") {
		t.Fatalf("turn 1 client tool_calls = %+v, want the converted weather call", calls)
	}
	callID := calls[0].ID

	// Turn 2: the chat client feeds the result back as a role "tool" message.
	secondClientBody := `{"model":"` + convMatrixModel + `","messages":[` +
		`{"role":"user","content":"What is the weather?"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"` + callID + `","type":"function","function":{"name":"weather","arguments":"{\"city\":\"SH\"}"}}]},` +
		`{"role":"tool","tool_call_id":"` + callID + `","content":"sunny"}]}`
	status, body, _ = doConvMatrixRequest(t, proxyServer.URL, protocolconvert.ProtocolChatCompletions, "/v1/chat/completions", secondClientBody)
	if status != http.StatusOK {
		t.Fatalf("turn 2: status = %d, want 200; body=%s", status, body)
	}
	mu.Lock()
	upSecondBody, upSecondPath := secondBody, secondPath
	mu.Unlock()
	if upSecondPath != "/v1/messages" {
		t.Fatalf("turn 2 upstream path = %q, want /v1/messages", upSecondPath)
	}
	if !strings.Contains(upSecondBody, `"type":"tool_result"`) ||
		!strings.Contains(upSecondBody, `"tool_use_id":"`+callID+`"`) ||
		!strings.Contains(upSecondBody, "sunny") {
		t.Fatalf("turn 2 did not reach the upstream as a Messages tool_result block: %s", upSecondBody)
	}
	if !strings.Contains(string(body), "It is sunny") || !strings.Contains(string(body), `"object":"chat.completion"`) {
		t.Fatalf("turn 2 client response is not the chat text completion: %s", body)
	}
}
