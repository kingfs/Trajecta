package proxy

// Wiring-level end-to-end matrix for opt-in protocol conversion
// (router.selection.protocol_conversion, default legacy engine).
//
// The engine-level semantic parity between the four protocols is already
// pinned by the 672-cell corpus in pkg/xir/convert/parity_test.go. This file
// pins the *forwarding wiring* around that engine: for every cross-protocol
// pair (client entrypoint chat/messages/gemini x upstream target
// chat/responses/messages/gemini, minus the same-protocol passthrough pairs)
// it asserts that
//
//   a) the upstream receives exactly one request, on the target protocol's
//      path, with a target-protocol body shape and the wire model name;
//   b) the client receives a source-protocol response with the text intact;
//   c) the cassette records the client-visible bytes on both sides
//      (record/wire separation) plus the protocol_conversion event;
//   d) the response usage is readable in the source protocol's vocabulary.
//
// The three same-protocol pairs run as controls: with a native candidate the
// bytes must pass through untouched even though conversion is enabled.
//
// Everything is offline and deterministic: httptest upstreams that serve only
// their own protocol surface (any other path is a 404), minimal text samples.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/pkg/protocolconvert"
)

const (
	convMatrixModel     = "conv-matrix-model"
	convMatrixWireModel = "conv-matrix-wire"
	convMatrixPing      = "ping-conv-matrix"
	convMatrixPong      = "pong-conv-matrix"
	convMatrixSecret    = "conv-matrix-secret"
)

// convMatrixUpstream serves exactly one protocol surface and records what it
// received. A request on any other path is answered with 404 so a mis-wired
// conversion fails loudly instead of being absorbed by a catch-all handler.
type convMatrixUpstream struct {
	proto  protocolconvert.Protocol
	server *httptest.Server
	calls  atomic.Int32

	mu     sync.Mutex
	path   string
	body   []byte
	header http.Header
	// delay, when set, sleeps before answering so the router's measured
	// latency cost for this target rises (native-preference test).
	delay time.Duration
}

func newConvMatrixUpstream(t *testing.T, proto protocolconvert.Protocol) *convMatrixUpstream {
	t.Helper()
	u := &convMatrixUpstream{proto: proto}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.calls.Add(1)
		u.mu.Lock()
		u.path = r.URL.Path
		u.body = raw
		u.header = r.Header.Clone()
		delay := u.delay
		u.mu.Unlock()

		if !convMatrixPathServes(proto, r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"conv-matrix upstream serves only its own protocol path"}`)
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		var payload map[string]any
		_ = json.Unmarshal(raw, &payload)
		if stream, _ := payload["stream"].(bool); stream && proto == protocolconvert.ProtocolChatCompletions {
			// The local Responses runtime orchestrates its chat backend as a
			// stream; the native streaming control also needs SSE.
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, convMatrixChatSSE())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, convMatrixUpstreamResponse(proto))
	}))
	t.Cleanup(u.server.Close)
	return u
}

func convMatrixPathServes(proto protocolconvert.Protocol, path string) bool {
	switch proto {
	case protocolconvert.ProtocolChatCompletions:
		return path == "/v1/chat/completions"
	case protocolconvert.ProtocolResponses:
		return path == "/v1/responses"
	case protocolconvert.ProtocolMessages:
		return path == "/v1/messages"
	case protocolconvert.ProtocolGemini:
		return strings.HasPrefix(path, "/v1beta/models/") && strings.HasSuffix(path, ":generateContent")
	}
	return false
}

func (u *convMatrixUpstream) snapshot() (string, []byte, http.Header) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.path, append([]byte(nil), u.body...), u.header.Clone()
}

func (u *convMatrixUpstream) setDelay(d time.Duration) {
	u.mu.Lock()
	u.delay = d
	u.mu.Unlock()
}

// convMatrixTarget builds the channel config that makes the stub upstream
// serve exactly one native protocol surface for the matrix model.
func (u *convMatrixUpstream) convMatrixTarget(id string) config.UpstreamTargetConfig {
	target := config.UpstreamTargetConfig{
		ID:             id,
		Enabled:        boolPtr(true),
		Priority:       100,
		ModelDiscovery: router.ModelDiscoveryStaticOnly,
		StaticModels:   []string{convMatrixModel},
	}
	switch u.proto {
	case protocolconvert.ProtocolChatCompletions:
		target.Upstream = config.UpstreamConfig{
			BaseURL:        u.server.URL + "/v1",
			ApiKey:         convMatrixSecret,
			ProviderPreset: "openai",
			APIType:        "chat_completions",
		}
	case protocolconvert.ProtocolResponses:
		target.Upstream = config.UpstreamConfig{
			BaseURL:        u.server.URL + "/v1",
			ApiKey:         convMatrixSecret,
			ProviderPreset: "openai",
			APIType:        "responses_native",
			Capabilities: config.UpstreamCapabilitiesConfig{
				Responses:       boolPtr(true),
				ChatCompletions: boolPtr(false),
			},
		}
	case protocolconvert.ProtocolMessages:
		target.Upstream = config.UpstreamConfig{
			BaseURL:        u.server.URL,
			ApiKey:         convMatrixSecret,
			ProviderPreset: "anthropic",
			APIType:        "messages",
		}
	case protocolconvert.ProtocolGemini:
		target.Upstream = config.UpstreamConfig{
			BaseURL:        u.server.URL + "/v1beta",
			ApiKey:         convMatrixSecret,
			ProviderPreset: "google_genai",
			ProtocolFamily: "google_genai",
			RoutingProfile: "google_ai_studio",
		}
	}
	return target
}

// convMatrixUpstreamResponse is the minimal legal non-streaming response for
// each target protocol. The responses shape follows the real envelope pinned
// by pkg/xir/convert/testdata/parity/responses.json (created_at / output /
// usage). Usage numbers are identical across protocols (3 in / 2 out / 5
// total) so the client-side usage assertion is direction-independent.
func convMatrixUpstreamResponse(proto protocolconvert.Protocol) string {
	switch proto {
	case protocolconvert.ProtocolChatCompletions:
		return `{"id":"chatcmpl-conv","object":"chat.completion","created":1,"model":"` + convMatrixModel + `","choices":[{"index":0,"message":{"role":"assistant","content":"` + convMatrixPong + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
	case protocolconvert.ProtocolResponses:
		return `{"id":"resp_conv","object":"response","created_at":1,"model":"` + convMatrixModel + `","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + convMatrixPong + `"}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`
	case protocolconvert.ProtocolMessages:
		return `{"id":"msg_conv","type":"message","role":"assistant","model":"` + convMatrixModel + `","content":[{"type":"text","text":"` + convMatrixPong + `"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":2}}`
	case protocolconvert.ProtocolGemini:
		return `{"candidates":[{"content":{"role":"model","parts":[{"text":"` + convMatrixPong + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`
	}
	return `{}`
}

func convMatrixChatSSE() string {
	return strings.Join([]string{
		`data: {"id":"chatcmpl-conv-sse","object":"chat.completion.chunk","model":"` + convMatrixModel + `","choices":[{"index":0,"delta":{"role":"assistant","content":"` + convMatrixPong + `"},"finish_reason":null}]}`,
		``,
		`data: {"id":"chatcmpl-conv-sse","object":"chat.completion.chunk","model":"` + convMatrixModel + `","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		``,
		`data: {"id":"chatcmpl-conv-sse","object":"chat.completion.chunk","model":"` + convMatrixModel + `","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
		``,
		`data: [DONE]`,
		``,
	}, "\n")
}

// convMatrixClientPath is the client entrypoint for a source protocol. The
// Gemini model travels in the path (internal/proxy/entrypoints.go keeps the
// canonical /v1beta/models/{model}:generateContent surface).
func convMatrixClientPath(proto protocolconvert.Protocol, model string) string {
	switch proto {
	case protocolconvert.ProtocolChatCompletions:
		return "/v1/chat/completions"
	case protocolconvert.ProtocolResponses:
		return "/v1/responses"
	case protocolconvert.ProtocolMessages:
		return "/v1/messages"
	case protocolconvert.ProtocolGemini:
		return "/v1beta/models/" + model + ":generateContent"
	}
	return ""
}

func convMatrixClientBody(proto protocolconvert.Protocol, stream bool) string {
	streamField := ""
	if stream {
		streamField = `,"stream":true`
	}
	switch proto {
	case protocolconvert.ProtocolChatCompletions:
		return `{"model":"` + convMatrixModel + `","messages":[{"role":"user","content":"` + convMatrixPing + `"}]` + streamField + `}`
	case protocolconvert.ProtocolMessages:
		return `{"model":"` + convMatrixModel + `","max_tokens":64,"messages":[{"role":"user","content":[{"type":"text","text":"` + convMatrixPing + `"}]}]` + streamField + `}`
	case protocolconvert.ProtocolGemini:
		return `{"contents":[{"role":"user","parts":[{"text":"` + convMatrixPing + `"}]}]}`
	}
	return `{}`
}

// doConvMatrixRequest sends a source-protocol request to the proxy. The
// messages client carries Anthropic-scoped headers so the conversion tests
// can assert they do not leak onto a non-Anthropic wire.
func doConvMatrixRequest(t *testing.T, proxyURL string, proto protocolconvert.Protocol, path, body string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, proxyURL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if proto == protocolconvert.ProtocolMessages {
		req.Header.Set("x-api-key", "client-key")
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "conv-matrix-beta")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, resp.Header.Clone()
}

// newConvMatrixHandler builds a conversion-enabled handler (default legacy
// engine) over the given targets and returns its URL and trace output dir.
func newConvMatrixHandler(t *testing.T, targets ...config.UpstreamTargetConfig) (string, string) {
	t.Helper()
	return newConvMatrixHandlerCfg(t, nil, targets...)
}

// newConvMatrixHandlerCfg additionally lets a test tune the router selection
// (epsilon, sticky, ...) before the handler is built.
func newConvMatrixHandlerCfg(t *testing.T, mutate func(*config.Config), targets ...config.UpstreamTargetConfig) (string, string) {
	t.Helper()
	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cfg := &config.Config{Upstreams: targets}
	cfg.Debug.OutputDir = outputDir
	cfg.Router.Selection.ProtocolConversion = true
	if mutate != nil {
		mutate(cfg)
	}

	handler, err := newAnonymousTestHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandlerWithAuth() error = %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL, outputDir
}

// assertConvMatrixUpstreamShape pins (a): one call, target path, target body
// shape, wire model, target-scoped credentials and no Anthropic header leak.
func assertConvMatrixUpstreamShape(t *testing.T, u *convMatrixUpstream, target protocolconvert.Protocol, wantModel string) {
	t.Helper()
	if got := u.calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	path, body, header := u.snapshot()
	switch target {
	case protocolconvert.ProtocolGemini:
		wantPath := "/v1beta/models/" + wantModel + ":generateContent"
		if path != wantPath {
			t.Fatalf("upstream path = %q, want %q (model travels in the Gemini path)", path, wantPath)
		}
	default:
		if want := convMatrixClientPath(target, wantModel); path != want {
			t.Fatalf("upstream path = %q, want %q", path, want)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("upstream body is not JSON: %v (%s)", err, body)
	}
	if !strings.Contains(string(body), convMatrixPing) {
		t.Errorf("converted upstream body lost the user text: %s", body)
	}
	switch target {
	case protocolconvert.ProtocolChatCompletions:
		if payload["model"] != wantModel {
			t.Errorf("chat wire model = %v, want %q", payload["model"], wantModel)
		}
		if _, ok := payload["messages"].([]any); !ok {
			t.Errorf("chat body has no messages array: %s", body)
		}
		if _, ok := payload["contents"]; ok {
			t.Errorf("chat body carries Gemini contents: %s", body)
		}
		if _, ok := payload["input"]; ok {
			t.Errorf("chat body carries Responses input: %s", body)
		}
	case protocolconvert.ProtocolResponses:
		if payload["model"] != wantModel {
			t.Errorf("responses wire model = %v, want %q", payload["model"], wantModel)
		}
		if _, ok := payload["input"]; !ok {
			t.Errorf("responses body has no input: %s", body)
		}
		if _, ok := payload["messages"]; ok {
			t.Errorf("responses body carries chat messages: %s", body)
		}
		if _, ok := payload["contents"]; ok {
			t.Errorf("responses body carries Gemini contents: %s", body)
		}
	case protocolconvert.ProtocolMessages:
		if payload["model"] != wantModel {
			t.Errorf("messages wire model = %v, want %q", payload["model"], wantModel)
		}
		if _, ok := payload["messages"].([]any); !ok {
			t.Errorf("messages body has no messages array: %s", body)
		}
		if maxTokens, ok := payload["max_tokens"].(float64); !ok || maxTokens <= 0 {
			t.Errorf("messages body must carry a positive max_tokens (required field): %s", body)
		}
		if _, ok := payload["contents"]; ok {
			t.Errorf("messages body carries Gemini contents: %s", body)
		}
		if _, ok := payload["input"]; ok {
			t.Errorf("messages body carries Responses input: %s", body)
		}
	case protocolconvert.ProtocolGemini:
		if _, ok := payload["contents"].([]any); !ok {
			t.Errorf("gemini body has no contents array: %s", body)
		}
		if _, ok := payload["model"]; ok {
			t.Errorf("gemini body must not carry a model member (model is in the path): %s", body)
		}
		if _, ok := payload["messages"]; ok {
			t.Errorf("gemini body carries chat messages: %s", body)
		}
	}
	// Target-scoped credentials, and no client Anthropic surface leak.
	switch target {
	case protocolconvert.ProtocolMessages:
		if got := header.Get("x-api-key"); got != convMatrixSecret {
			t.Errorf("upstream x-api-key = %q, want the channel credential %q", got, convMatrixSecret)
		}
		if header.Get("anthropic-version") == "" {
			t.Errorf("converted messages request lacks anthropic-version")
		}
	case protocolconvert.ProtocolGemini:
		if got := header.Get("x-goog-api-key"); got != convMatrixSecret {
			t.Errorf("upstream x-goog-api-key = %q, want the channel credential %q", got, convMatrixSecret)
		}
	default:
		if got := header.Get("Authorization"); got != "Bearer "+convMatrixSecret {
			t.Errorf("upstream Authorization = %q, want Bearer %s", got, convMatrixSecret)
		}
	}
	if target != protocolconvert.ProtocolMessages {
		if got := header.Get("anthropic-version"); got != "" {
			t.Errorf("anthropic-version leaked onto a %s wire: %q", target, got)
		}
		if got := header.Get("anthropic-beta"); got != "" {
			t.Errorf("anthropic-beta leaked onto a %s wire: %q", target, got)
		}
	}
}

// assertConvMatrixClientShape pins (b) text fidelity in the source protocol
// and (d) usage readable in the source protocol's vocabulary.
func assertConvMatrixClientShape(t *testing.T, body []byte, client protocolconvert.Protocol) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("client response is not JSON: %v (%s)", err, body)
	}
	if !strings.Contains(string(body), convMatrixPong) {
		t.Fatalf("client response lost the upstream text: %s", body)
	}
	switch client {
	case protocolconvert.ProtocolChatCompletions:
		if payload["object"] != "chat.completion" {
			t.Errorf("client object = %v, want chat.completion", payload["object"])
		}
		choices, _ := payload["choices"].([]any)
		if len(choices) != 1 {
			t.Fatalf("client choices = %v, want one choice", payload["choices"])
		}
		choice, _ := choices[0].(map[string]any)
		message, _ := choice["message"].(map[string]any)
		if message["content"] != convMatrixPong {
			t.Errorf("client content = %v, want %q", message["content"], convMatrixPong)
		}
		if choice["finish_reason"] != "stop" {
			t.Errorf("client finish_reason = %v, want stop", choice["finish_reason"])
		}
		usage, _ := payload["usage"].(map[string]any)
		if usage["prompt_tokens"] != float64(3) || usage["completion_tokens"] != float64(2) || usage["total_tokens"] != float64(5) {
			t.Errorf("client usage = %v, want 3/2/5", payload["usage"])
		}
	case protocolconvert.ProtocolMessages:
		if payload["type"] != "message" {
			t.Errorf("client type = %v, want message", payload["type"])
		}
		if payload["stop_reason"] != "end_turn" {
			t.Errorf("client stop_reason = %v, want end_turn", payload["stop_reason"])
		}
		content, _ := payload["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("client content blocks empty: %s", body)
		}
		block, _ := content[0].(map[string]any)
		if block["type"] != "text" || block["text"] != convMatrixPong {
			t.Errorf("client first block = %v, want text %q", block, convMatrixPong)
		}
		usage, _ := payload["usage"].(map[string]any)
		if usage["input_tokens"] != float64(3) || usage["output_tokens"] != float64(2) {
			t.Errorf("client usage = %v, want input 3 / output 2", payload["usage"])
		}
	case protocolconvert.ProtocolGemini:
		candidates, _ := payload["candidates"].([]any)
		if len(candidates) != 1 {
			t.Fatalf("client candidates = %v, want one candidate", payload["candidates"])
		}
		candidate, _ := candidates[0].(map[string]any)
		if candidate["finishReason"] != "STOP" {
			t.Errorf("client finishReason = %v, want STOP", candidate["finishReason"])
		}
		content, _ := candidate["content"].(map[string]any)
		parts, _ := content["parts"].([]any)
		if len(parts) == 0 {
			t.Fatalf("client parts empty: %s", body)
		}
		part, _ := parts[0].(map[string]any)
		if part["text"] != convMatrixPong {
			t.Errorf("client first part = %v, want text %q", part, convMatrixPong)
		}
		usage, _ := payload["usageMetadata"].(map[string]any)
		if usage["promptTokenCount"] != float64(3) || usage["candidatesTokenCount"] != float64(2) || usage["totalTokenCount"] != float64(5) {
			t.Errorf("client usageMetadata = %v, want 3/2/5", payload["usageMetadata"])
		}
	}
}

// convMatrixTargetOnlyMarkers are response-body markers that exist only in
// the target protocol. None of them may appear in the cassette of a converted
// exchange: the cassette records the client-visible synthesis, not the wire.
func convMatrixTargetOnlyMarkers(target protocolconvert.Protocol) []string {
	switch target {
	case protocolconvert.ProtocolChatCompletions:
		return []string{"chat.completion"}
	case protocolconvert.ProtocolResponses:
		return []string{`"object":"response"`}
	case protocolconvert.ProtocolMessages:
		return []string{`"type":"message"`}
	case protocolconvert.ProtocolGemini:
		return []string{"usageMetadata", "finishReason"}
	}
	return nil
}

// assertConvMatrixCassette pins (c): the recorded request and response are the
// client-visible bytes, and the protocol_conversion event names the pair and
// the wire model.
func assertConvMatrixCassette(t *testing.T, outputDir string, client, target protocolconvert.Protocol, wireModel string) {
	t.Helper()
	recordPath := findRecordedHTTP(t, outputDir)
	raw := mustReadFile(t, recordPath)
	if !strings.Contains(string(raw), convMatrixPong) {
		t.Errorf("cassette does not record the client-visible response text:\n%s", raw)
	}
	switch client {
	case protocolconvert.ProtocolChatCompletions:
		if !strings.Contains(string(raw), `"content":"`+convMatrixPing+`"`) {
			t.Errorf("cassette does not record the original chat request body:\n%s", raw)
		}
	case protocolconvert.ProtocolMessages:
		if !strings.Contains(string(raw), `"type":"text"`) {
			t.Errorf("cassette does not record the original messages request body:\n%s", raw)
		}
	case protocolconvert.ProtocolGemini:
		if !strings.Contains(string(raw), `"parts"`) {
			t.Errorf("cassette does not record the original gemini request body:\n%s", raw)
		}
	}
	for _, marker := range convMatrixTargetOnlyMarkers(target) {
		if strings.Contains(string(raw), marker) {
			t.Errorf("cassette leaks the upstream %s wire marker %q (record/wire separation violated):\n%s", target, marker, raw)
		}
	}
	parsed, err := waitForRecordedPrelude(recordPath, 2*time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude(%q) error = %v", recordPath, err)
	}
	var convEvent map[string]any
	for _, event := range parsed.Events {
		if event.Type == "protocol_conversion" {
			convEvent = event.Attributes
			break
		}
	}
	if convEvent == nil {
		t.Fatalf("cassette has no protocol_conversion event: %+v", parsed.Events)
	}
	if convEvent["client_protocol"] != string(client) || convEvent["upstream_protocol"] != string(target) {
		t.Errorf("protocol_conversion pair = %v/%v, want %s/%s", convEvent["client_protocol"], convEvent["upstream_protocol"], client, target)
	}
	if convEvent["wire_model"] != wireModel {
		t.Errorf("protocol_conversion wire_model = %v, want %q", convEvent["wire_model"], wireModel)
	}
}

// TestHandlerProtocolConversionCrossMatrix walks the nine cross-protocol
// pairs. Each subtest runs against a fresh single-protocol upstream with no
// native candidate, so the router must fall back to conversion.
func TestHandlerProtocolConversionCrossMatrix(t *testing.T) {
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
				continue // same-protocol passthrough has its own control below
			}
			t.Run(fmt.Sprintf("%s_to_%s", client, target), func(t *testing.T) {
				up := newConvMatrixUpstream(t, target)
				proxyURL, outputDir := newConvMatrixHandler(t, up.convMatrixTarget("only-"+string(target)))

				status, body, _ := doConvMatrixRequest(t, proxyURL, client,
					convMatrixClientPath(client, convMatrixModel), convMatrixClientBody(client, false))
				if status != http.StatusOK {
					t.Fatalf("status = %d, want 200; body=%s", status, body)
				}
				assertConvMatrixUpstreamShape(t, up, target, convMatrixModel)
				assertConvMatrixClientShape(t, body, client)
				assertConvMatrixCassette(t, outputDir, client, target, convMatrixModel)
			})
		}
	}
}

// TestHandlerProtocolConversionSameProtocolControls pins the passthrough
// control: with conversion enabled but a native candidate present, the
// request and response bytes must be forwarded untouched and no conversion
// event may be recorded.
func TestHandlerProtocolConversionSameProtocolControls(t *testing.T) {
	for _, proto := range []protocolconvert.Protocol{
		protocolconvert.ProtocolChatCompletions,
		protocolconvert.ProtocolMessages,
		protocolconvert.ProtocolGemini,
	} {
		t.Run(string(proto), func(t *testing.T) {
			up := newConvMatrixUpstream(t, proto)
			proxyURL, outputDir := newConvMatrixHandler(t, up.convMatrixTarget("native-"+string(proto)))

			clientBody := convMatrixClientBody(proto, false)
			status, body, _ := doConvMatrixRequest(t, proxyURL, proto,
				convMatrixClientPath(proto, convMatrixModel), clientBody)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", status, body)
			}
			if got := up.calls.Load(); got != 1 {
				t.Fatalf("upstream calls = %d, want 1", got)
			}
			path, wireBody, _ := up.snapshot()
			if want := convMatrixClientPath(proto, convMatrixModel); path != want {
				t.Fatalf("upstream path = %q, want the untouched client path %q", path, want)
			}
			if string(wireBody) != clientBody {
				t.Fatalf("native passthrough rewrote the request body:\n got %s\nwant %s", wireBody, clientBody)
			}
			if string(body) != convMatrixUpstreamResponse(proto) {
				t.Fatalf("native passthrough rewrote the response body:\n got %s\nwant %s", body, convMatrixUpstreamResponse(proto))
			}
			recordPath := findRecordedHTTP(t, outputDir)
			parsed, err := waitForRecordedPrelude(recordPath, 2*time.Second)
			if err != nil {
				t.Fatalf("waitForRecordedPrelude(%q) error = %v", recordPath, err)
			}
			if hasRecordEvent(parsed.Events, "protocol_conversion") {
				t.Fatalf("native passthrough recorded a protocol_conversion event: %+v", parsed.Events)
			}
		})
	}
}

// TestHandlerProtocolConversionWireModelName pins the wire-name leg of the
// matrix: a channel model wire name (channel_models.upstream_model) must be
// what the converted upstream request carries - in the body for
// body-model protocols, in the path for Gemini.
func TestHandlerProtocolConversionWireModelName(t *testing.T) {
	t.Run("messages_to_chat_body_wire_name", func(t *testing.T) {
		up := newConvMatrixUpstream(t, protocolconvert.ProtocolChatCompletions)
		target := up.convMatrixTarget("chat-wire")
		target.StaticModels = []string{convMatrixModel, convMatrixWireModel}
		target.ModelWireNames = map[string]string{convMatrixModel: convMatrixWireModel}
		proxyURL, outputDir := newConvMatrixHandler(t, target)

		status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolMessages,
			convMatrixClientPath(protocolconvert.ProtocolMessages, convMatrixModel),
			convMatrixClientBody(protocolconvert.ProtocolMessages, false))
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", status, body)
		}
		assertConvMatrixUpstreamShape(t, up, protocolconvert.ProtocolChatCompletions, convMatrixWireModel)
		assertConvMatrixClientShape(t, body, protocolconvert.ProtocolMessages)
		assertConvMatrixCassette(t, outputDir, protocolconvert.ProtocolMessages, protocolconvert.ProtocolChatCompletions, convMatrixWireModel)
	})
	t.Run("chat_to_gemini_path_wire_name", func(t *testing.T) {
		up := newConvMatrixUpstream(t, protocolconvert.ProtocolGemini)
		target := up.convMatrixTarget("gemini-wire")
		target.StaticModels = []string{convMatrixModel, convMatrixWireModel}
		target.ModelWireNames = map[string]string{convMatrixModel: convMatrixWireModel}
		proxyURL, outputDir := newConvMatrixHandler(t, target)

		status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolChatCompletions,
			convMatrixClientPath(protocolconvert.ProtocolChatCompletions, convMatrixModel),
			convMatrixClientBody(protocolconvert.ProtocolChatCompletions, false))
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", status, body)
		}
		assertConvMatrixUpstreamShape(t, up, protocolconvert.ProtocolGemini, convMatrixWireModel)
		assertConvMatrixClientShape(t, body, protocolconvert.ProtocolChatCompletions)
		assertConvMatrixCassette(t, outputDir, protocolconvert.ProtocolChatCompletions, protocolconvert.ProtocolGemini, convMatrixWireModel)
	})
	// Regression: a Gemini source carries the model in the path, not the body,
	// so the conversion's wire-name lookup must resolve the target's mapping
	// instead of falling back to the client name (ModelFromBody finds nothing).
	t.Run("gemini_to_chat_path_source_wire_name", func(t *testing.T) {
		up := newConvMatrixUpstream(t, protocolconvert.ProtocolChatCompletions)
		target := up.convMatrixTarget("chat-wire-gemini-src")
		target.StaticModels = []string{convMatrixModel, convMatrixWireModel}
		target.ModelWireNames = map[string]string{convMatrixModel: convMatrixWireModel}
		proxyURL, outputDir := newConvMatrixHandler(t, target)

		status, body, _ := doConvMatrixRequest(t, proxyURL, protocolconvert.ProtocolGemini,
			convMatrixClientPath(protocolconvert.ProtocolGemini, convMatrixModel),
			convMatrixClientBody(protocolconvert.ProtocolGemini, false))
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", status, body)
		}
		assertConvMatrixUpstreamShape(t, up, protocolconvert.ProtocolChatCompletions, convMatrixWireModel)
		assertConvMatrixClientShape(t, body, protocolconvert.ProtocolGemini)
		assertConvMatrixCassette(t, outputDir, protocolconvert.ProtocolGemini, protocolconvert.ProtocolChatCompletions, convMatrixWireModel)
	})
}
