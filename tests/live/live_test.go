// Package live holds black-box functional tests that run against a started
// Trajecta instance and a real upstream LLM gateway.
//
// The suite is opt-in: every test skips unless TRAJECTA_LIVE_UPSTREAM=1, so
// `go test ./...` stays offline and deterministic. Start the stack with
// tests/live/docker-compose.live.yml (see tests/live/README.md) and then run:
//
//	TRAJECTA_LIVE_UPSTREAM=1 go test ./tests/live -v
//
// Recognised variables: TRAJECTA_LIVE_UPSTREAM, TRAJECTA_LIVE_SERVER_URL,
// TRAJECTA_LIVE_MONITOR_URL, TRAJECTA_LIVE_PROXY_TOKEN (required for /v1/*),
// TRAJECTA_LIVE_MONITOR_TOKEN or TRAJECTA_LIVE_MONITOR_USER/PASSWORD,
// TRAJECTA_LIVE_TRACE_DIR and TRAJECTA_LIVE_MODEL.
package live

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/replay"
)

const (
	defaultServerURL  = "http://127.0.0.1:18080"
	defaultMonitorURL = "http://127.0.0.1:18081"
	defaultModel      = "deepseek-flash"
)

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func serverURL() string  { return envOr("TRAJECTA_LIVE_SERVER_URL", defaultServerURL) }
func monitorURL() string { return envOr("TRAJECTA_LIVE_MONITOR_URL", defaultMonitorURL) }
func modelName() string  { return envOr("TRAJECTA_LIVE_MODEL", defaultModel) }

// requireLive skips the test unless the operator explicitly opted in.
func requireLive(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_UPSTREAM")) != "1" {
		t.Skip("set TRAJECTA_LIVE_UPSTREAM=1 to run live upstream tests")
	}
}

type httpResult struct {
	Status  int
	Header  http.Header
	Body    []byte
	Latency time.Duration
}

func (r httpResult) String() string {
	body := string(r.Body)
	if len(body) > 2000 {
		body = body[:2000] + "...(truncated)"
	}
	return fmt.Sprintf("status=%d latency=%s body=%s", r.Status, r.Latency, body)
}

func request(t *testing.T, method, url string, body any, headers map[string]string) httpResult {
	t.Helper()

	var reader io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(v)
	case []byte:
		reader = bytes.NewReader(v)
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 180 * time.Second}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return httpResult{Status: resp.StatusCode, Header: resp.Header, Body: payload, Latency: time.Since(start)}
}

func decodeJSON(t *testing.T, raw []byte, out any) {
	t.Helper()
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("decode json: %v; raw=%s", err, truncate(string(raw), 800))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// proxyToken returns the Trajecta API token the client must present on /v1/*.
// The proxy rejects unauthenticated traffic as soon as an auth store exists.
func proxyToken(t *testing.T) string {
	t.Helper()
	token := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_PROXY_TOKEN"))
	if token == "" {
		t.Skip("set TRAJECTA_LIVE_PROXY_TOKEN to the token issued by `server auth create-token`")
	}
	return token
}

func proxyRequest(t *testing.T, method, path string, body any, extra map[string]string) httpResult {
	t.Helper()
	headers := map[string]string{"Authorization": "Bearer " + proxyToken(t)}
	for k, v := range extra {
		headers[k] = v
	}
	return request(t, method, serverURL()+path, body, headers)
}

// ---------------------------------------------------------------------------
// C1/C2/C3: chat completions pass-through, streaming and tool calls
// ---------------------------------------------------------------------------

func TestLiveChatCompletions(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly the word pong and nothing else."}},
		"max_tokens": 128,
	}
	res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("chat completions: want 200, got %s", res)
	}

	var parsed struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	decodeJSON(t, res.Body, &parsed)

	if len(parsed.Choices) == 0 {
		t.Fatalf("chat completions: no choices; %s", res)
	}
	if parsed.Object != "chat.completion" {
		t.Errorf("chat completions: object=%q, want chat.completion", parsed.Object)
	}
	if parsed.Model != modelName() {
		t.Errorf("chat completions: model=%q, want %q", parsed.Model, modelName())
	}
	if strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		t.Errorf("chat completions: empty assistant content (finish_reason=%q); %s", parsed.Choices[0].FinishReason, res)
	}
	if parsed.Usage == nil {
		t.Errorf("chat completions: missing usage block; %s", res)
	}
}

func TestLiveChatCompletionsStream(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model":      modelName(),
		"stream":     true,
		"messages":   []map[string]string{{"role": "user", "content": "Count from 1 to 5, comma separated."}},
		"max_tokens": 128,
	}
	res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("streaming chat completions: want 200, got %s", res)
	}
	if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Errorf("streaming chat completions: content-type=%q, want text/event-stream", ct)
	}

	var (
		dataLines int
		sawDone   bool
		content   strings.Builder
	)
	scanner := bufio.NewScanner(bytes.NewReader(res.Body))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			sawDone = true
			continue
		}
		dataLines++
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Errorf("streaming chat completions: invalid chunk %q: %v", truncate(payload, 200), err)
			continue
		}
		for _, c := range chunk.Choices {
			content.WriteString(c.Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read stream: %v", err)
	}
	if dataLines == 0 {
		t.Errorf("streaming chat completions: no data chunks; %s", res)
	}
	if !sawDone {
		t.Errorf("streaming chat completions: missing [DONE] terminator; %s", res)
	}
	if strings.TrimSpace(content.String()) == "" {
		t.Errorf("streaming chat completions: no delta content accumulated; %s", res)
	}
}

func TestLiveChatCompletionsToolCall(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model": modelName(),
		"messages": []map[string]string{
			{"role": "user", "content": "What is the weather in Shanghai? Use the tool."},
		},
		"max_tokens": 256,
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "get_weather",
				"description": "Get the current weather for a city",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{"type": "string"},
					},
					"required": []string{"city"},
				},
			},
		}},
		"tool_choice": "auto",
	}
	res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("tool call: want 200, got %s", res)
	}

	var parsed struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	decodeJSON(t, res.Body, &parsed)
	if len(parsed.Choices) == 0 {
		t.Fatalf("tool call: no choices; %s", res)
	}
	calls := parsed.Choices[0].Message.ToolCalls
	if len(calls) == 0 {
		t.Skipf("upstream model chose not to call the tool (finish_reason=%q); pass-through itself succeeded: %s",
			parsed.Choices[0].FinishReason, truncate(string(res.Body), 400))
	}
	if calls[0].Function.Name != "get_weather" {
		t.Errorf("tool call: name=%q, want get_weather", calls[0].Function.Name)
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(calls[0].Function.Arguments), &args); err != nil {
		t.Errorf("tool call: arguments are not valid JSON: %q", calls[0].Function.Arguments)
	}
}

// ---------------------------------------------------------------------------
// C4/C5/C6/C7: protocol surfaces
// ---------------------------------------------------------------------------

func TestLiveResponsesEndpoint(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model": modelName(),
		"input": "Reply with exactly the word pong and nothing else.",
	}
	res := proxyRequest(t, http.MethodPost, "/v1/responses", body, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("responses: want 200, got %s", res)
	}

	var parsed struct {
		ID         string `json:"id"`
		Object     string `json:"object"`
		Status     string `json:"status"`
		OutputText string `json:"output_text"`
		Output     []struct {
			Type string `json:"type"`
		} `json:"output"`
		Usage map[string]any `json:"usage"`
	}
	decodeJSON(t, res.Body, &parsed)
	if parsed.Object != "response" {
		t.Errorf("responses: object=%q, want response", parsed.Object)
	}
	if parsed.Status != "completed" {
		t.Errorf("responses: status=%q, want completed", parsed.Status)
	}
	if strings.TrimSpace(parsed.OutputText) == "" && len(parsed.Output) == 0 {
		t.Errorf("responses: empty output; %s", res)
	}
}

func TestLiveAnthropicMessages(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model":      modelName(),
		"max_tokens": 128,
		"messages":   []map[string]any{{"role": "user", "content": "Reply with exactly the word pong and nothing else."}},
	}
	res := proxyRequest(t, http.MethodPost, "/v1/messages", body, map[string]string{
		"anthropic-version": "2023-06-01",
	})
	if res.Status != http.StatusOK {
		t.Fatalf("messages: want 200, got %s", res)
	}

	var parsed struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage map[string]any `json:"usage"`
	}
	decodeJSON(t, res.Body, &parsed)
	if parsed.Type != "message" {
		t.Errorf("messages: type=%q, want message", parsed.Type)
	}
	if parsed.Role != "assistant" {
		t.Errorf("messages: role=%q, want assistant", parsed.Role)
	}
	if len(parsed.Content) == 0 {
		t.Errorf("messages: empty content; %s", res)
	}
	if parsed.Usage == nil {
		t.Errorf("messages: missing usage; %s", res)
	}
}

func TestLiveModelsList(t *testing.T) {
	requireLive(t)

	res := proxyRequest(t, http.MethodGet, "/v1/models", nil, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("models: want 200, got %s", res)
	}

	var parsed struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decodeJSON(t, res.Body, &parsed)
	if len(parsed.Data) == 0 {
		t.Fatalf("models: empty data; %s", res)
	}
	found := false
	for _, m := range parsed.Data {
		if m.ID == modelName() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("models: %q not present in %d entries", modelName(), len(parsed.Data))
	}
}

// TestLiveCountTokensSynthetic covers the documented fallback: the upstream
// answers 404 for /v1/messages/count_tokens, so the proxy synthesises a count
// response and still records the exchange.
func TestLiveCountTokensSynthetic(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model":    modelName(),
		"messages": []map[string]any{{"role": "user", "content": "hello"}},
	}
	res := proxyRequest(t, http.MethodPost, "/v1/messages/count_tokens", body, map[string]string{
		"anthropic-version": "2023-06-01",
	})
	if res.Status != http.StatusOK {
		t.Fatalf("count_tokens: want 200 (synthetic fallback), got %s", res)
	}
	var parsed struct {
		InputTokens int `json:"input_tokens"`
	}
	decodeJSON(t, res.Body, &parsed)
	if parsed.InputTokens <= 0 {
		t.Errorf("count_tokens: input_tokens=%d, want > 0; %s", parsed.InputTokens, res)
	}
}

// TestLiveEmbeddingsIsNotRoutable pins the documented limitation: embeddings
// requests are classified and recorded but never routed to an upstream.
func TestLiveEmbeddingsIsNotRoutable(t *testing.T) {
	requireLive(t)

	body := map[string]any{"model": "bge-m3", "input": "hello"}
	res := proxyRequest(t, http.MethodPost, "/v1/embeddings", body, nil)
	if res.Status >= 200 && res.Status < 300 {
		t.Errorf("embeddings: expected a routing failure, got %s", res)
	}
}

// TestLiveUnknownModelRejected covers the fallback policy
// router.fallback.on_missing_model=reject.
func TestLiveUnknownModelRejected(t *testing.T) {
	requireLive(t)

	body := map[string]any{
		"model":      "definitely-not-a-real-model-" + fmt.Sprint(time.Now().UnixNano()),
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
		"max_tokens": 8,
	}
	res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if res.Status >= 200 && res.Status < 300 {
		t.Errorf("unknown model: expected rejection, got %s", res)
	}
	if res.Status == http.StatusOK {
		t.Errorf("unknown model: got 200")
	}
}

// TestLiveInvalidJSON checks that a malformed body fails fast and locally.
func TestLiveInvalidJSON(t *testing.T) {
	requireLive(t)

	res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", "{not-json", nil)
	if res.Status < 400 {
		t.Errorf("invalid json: expected a 4xx/5xx, got %s", res)
	}
}

// ---------------------------------------------------------------------------
// C8/C9/C10: recording, indexing and replay
// ---------------------------------------------------------------------------

func TestLiveCassetteRecorded(t *testing.T) {
	requireLive(t)

	dir := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_TRACE_DIR"))
	if dir == "" {
		t.Skip("set TRAJECTA_LIVE_TRACE_DIR to the host directory bind-mounted at /app/data/traces")
	}

	marker := fmt.Sprintf("cassette-probe-%d", time.Now().UnixNano())
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Echo this marker: " + marker}},
		"max_tokens": 64,
	}
	res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("cassette probe request: want 200, got %s", res)
	}

	deadline := time.Now().Add(15 * time.Second)
	var cassette string
	for time.Now().Before(deadline) && cassette == "" {
		found, err := findCassette(dir, marker)
		if err != nil {
			t.Fatalf("scan cassettes: %v", err)
		}
		cassette = found
		if cassette == "" {
			time.Sleep(250 * time.Millisecond)
		}
	}
	if cassette == "" {
		t.Fatalf("no cassette containing marker %q under %s", marker, dir)
	}

	raw, err := os.ReadFile(cassette)
	if err != nil {
		t.Fatalf("read cassette: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("# trajecta/v3")) {
		t.Errorf("cassette %s: prelude magic is %q, want # trajecta/v3", cassette, firstLine(raw))
	}

	rel, _ := filepath.Rel(dir, cassette)
	summary, err := replay.ReplayFile(cassette, replay.SummaryOptions{BodyLimit: 4096})
	if err != nil {
		t.Fatalf("replay %s: %v", rel, err)
	}
	if summary.StatusCode != http.StatusOK {
		t.Errorf("replay %s: status=%d, want 200", rel, summary.StatusCode)
	}
	if !strings.Contains(summary.Body, "chat.completion") {
		t.Errorf("replay %s: body does not look like a chat completion: %s", rel, truncate(summary.Body, 400))
	}
}

func findCassette(root, marker string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".http") || found != "" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if bytes.Contains(raw, []byte(marker)) {
			found = path
		}
		return nil
	})
	return found, err
}

func firstLine(raw []byte) string {
	if idx := bytes.IndexByte(raw, '\n'); idx >= 0 {
		return string(raw[:idx])
	}
	return string(raw)
}

// ---------------------------------------------------------------------------
// C9/C11: management plane visibility
// ---------------------------------------------------------------------------

func monitorToken(t *testing.T) string {
	t.Helper()

	if token := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_MONITOR_TOKEN")); token != "" {
		return token
	}
	user := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_MONITOR_USER"))
	pass := strings.TrimSpace(os.Getenv("TRAJECTA_LIVE_MONITOR_PASSWORD"))
	if user == "" || pass == "" {
		t.Skip("set TRAJECTA_LIVE_MONITOR_TOKEN or TRAJECTA_LIVE_MONITOR_USER/PASSWORD")
	}

	res := request(t, http.MethodPost, monitorURL()+"/api/auth/login", map[string]string{
		"username": user,
		"password": pass,
	}, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("login: want 200, got %s", res)
	}
	var parsed struct {
		Token string `json:"token"`
	}
	decodeJSON(t, res.Body, &parsed)
	if parsed.Token == "" {
		t.Fatalf("login: empty token; %s", res)
	}
	return parsed.Token
}

func monitorGET(t *testing.T, path string) httpResult {
	t.Helper()
	return request(t, http.MethodGet, monitorURL()+path, nil, map[string]string{
		"Authorization": "Bearer " + monitorToken(t),
	})
}

func TestLiveMonitorUnauthorized(t *testing.T) {
	requireLive(t)

	res := request(t, http.MethodGet, monitorURL()+"/api/overview", nil, nil)
	if res.Status != http.StatusUnauthorized {
		t.Errorf("unauthenticated /api/overview: want 401, got %s", res)
	}
}

func TestLiveMonitorReadSurface(t *testing.T) {
	requireLive(t)

	// Make sure there is at least one trace to look at.
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "monitor-surface-probe"}},
		"max_tokens": 32,
	}
	if res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil); res.Status != http.StatusOK {
		t.Fatalf("seed request: want 200, got %s", res)
	}

	for _, path := range []string{
		"/api/auth/status",
		"/api/overview",
		"/api/traces",
		"/api/sessions",
		"/api/models",
		"/api/channels",
		"/api/upstreams",
		"/api/events",
		"/api/findings",
		"/api/routing/summary",
		"/api/settings/channels",
		"/api/model-aliases",
		"/api/provider-presets",
		"/api/analysis",
		"/api/analysis/jobs",
	} {
		path := path
		t.Run(path, func(t *testing.T) {
			res := monitorGET(t, path)
			if res.Status != http.StatusOK {
				t.Fatalf("%s: want 200, got %s", path, res)
			}
			if !json.Valid(res.Body) {
				t.Fatalf("%s: response is not valid JSON: %s", path, truncate(string(res.Body), 200))
			}
		})
	}
}

func TestLiveMonitorTraceCorrelatesWithCassette(t *testing.T) {
	requireLive(t)

	// The list view carries metadata only, so correlate through "newest trace
	// after this request" and then look for the marker in the detail payload.
	before := newestTraceID(t)

	marker := fmt.Sprintf("correlation-probe-%d", time.Now().UnixNano())
	body := map[string]any{
		"model":      modelName(),
		"messages":   []map[string]string{{"role": "user", "content": "Log: " + marker}},
		"max_tokens": 32,
	}
	if res := proxyRequest(t, http.MethodPost, "/v1/chat/completions", body, nil); res.Status != http.StatusOK {
		t.Fatalf("correlation probe: want 200, got %s", res)
	}

	deadline := time.Now().Add(30 * time.Second)
	id := ""
	for time.Now().Before(deadline) {
		if candidate := newestTraceID(t); candidate != "" && candidate != before {
			id = candidate
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if id == "" {
		t.Fatalf("no new trace appeared in /api/traces within 30s (previous newest=%s)", before)
	}

	detail := monitorGET(t, "/api/traces/"+id)
	if detail.Status != http.StatusOK {
		t.Fatalf("trace detail %s: want 200, got %s", id, detail)
	}
	if !bytes.Contains(detail.Body, []byte(marker)) {
		t.Errorf("trace detail %s does not contain the request marker %q", id, marker)
	}
	if !bytes.Contains(detail.Body, []byte(modelName())) {
		t.Errorf("trace detail %s does not mention model %q", id, modelName())
	}
}

func newestTraceID(t *testing.T) string {
	t.Helper()
	res := monitorGET(t, "/api/traces?limit=1")
	if res.Status != http.StatusOK {
		t.Fatalf("/api/traces?limit=1: want 200, got %s", res)
	}
	var parsed struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	decodeJSON(t, res.Body, &parsed)
	if len(parsed.Items) == 0 {
		return ""
	}
	return parsed.Items[0].ID
}
